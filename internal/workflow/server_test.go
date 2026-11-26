// Tests that pin down how the Temporal server we run against behaves, before
// the relay and the workflow rely on it. They use a small probe workflow, not
// the real one, and a real dev server in a container (the same pinned image as
// docker-compose.yml).
package workflow_test

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"github.com/francoisnyc/work-kernel/internal/testdb"
)

const temporalImage = "temporalio/temporal:1.10.0" // keep in step with docker-compose.yml

// tc is the client shared by every real-server test in this package.
var tc client.Client

// temporalHostPort is the test server's address, for a test that needs a
// connection of its own (one it can cut, as a crash would).
var temporalHostPort string

func TestMain(m *testing.M) {
	os.Exit(runTests(m))
}

func runTests(m *testing.M) int {
	ctx := context.Background()
	server, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        temporalImage,
			Cmd:          []string{"server", "start-dev", "--ip", "0.0.0.0", "--headless"},
			ExposedPorts: []string{"7233/tcp"},
			WaitingFor:   wait.ForListeningPort("7233/tcp").WithStartupTimeout(2 * time.Minute),
		},
		Started: true,
	})
	if err != nil {
		log.Printf("start temporal: %v", err)
		return 1
	}
	defer func() { _ = testcontainers.TerminateContainer(server) }()

	hostPort, err := server.PortEndpoint(ctx, "7233/tcp", "")
	if err != nil {
		log.Printf("temporal endpoint: %v", err)
		return 1
	}
	// The port opens a moment before the default namespace is ready, so retry.
	for attempt := 0; ; attempt++ {
		temporalHostPort = hostPort
		tc, err = client.Dial(client.Options{HostPort: hostPort})
		if err == nil {
			break
		}
		if attempt == 30 {
			log.Printf("connect to temporal: %v", err)
			return 1
		}
		time.Sleep(time.Second)
	}
	defer tc.Close()

	// The end-to-end tests (e2e_test.go) also need Postgres and the kernel.
	var stopDB func()
	pool, stopDB, err = testdb.Start(ctx)
	if err != nil {
		log.Print(err)
		return 1
	}
	defer stopDB()
	if err := registerJobType(ctx); err != nil {
		log.Printf("register job type: %v", err)
		return 1
	}
	if err := registerNewestTypes(ctx); err != nil {
		log.Printf("register the newest Types: %v", err)
		return 1
	}

	code := m.Run()
	// Track 1 item 5, step 3a: after every test has run, with a real
	// worker (timers, reactions, holds), no Item has a state_entered_at
	// that is not the time of the event that put it in its state.
	if drift, err := testdb.StateEnteredAtDrift(ctx, pool); err != nil {
		log.Printf("state_entered_at: reading every Item: %v", err)
		return 1
	} else if len(drift) > 0 {
		log.Printf("FAIL: state_entered_at is not the time of the Item's latest create or transition event, for (at most 20 shown):\n  %s",
			strings.Join(drift, "\n  "))
		return 1
	}
	return code
}

// --- the probe workflow ---

type probeUpdate struct {
	Seq    int64
	Finish bool // ask the run to complete once this update is handled
}

type probeResult struct {
	Seq     int64
	Applied int // how many updates this run has handled, this one included
}

// probeWorkflow handles "apply" updates until one asks it to finish.
func probeWorkflow(ctx workflow.Context) error {
	var (
		applied int
		finish  bool
	)
	err := workflow.SetUpdateHandler(ctx, "apply", func(ctx workflow.Context, u probeUpdate) (probeResult, error) {
		applied++
		if u.Finish {
			finish = true
		}
		return probeResult{Seq: u.Seq, Applied: applied}, nil
	})
	if err != nil {
		return err
	}
	// Await parks the workflow until the condition holds; it is the workflow
	// equivalent of waiting on a condition variable.
	return workflow.Await(ctx, func() bool { return finish && workflow.AllHandlersFinished(ctx) })
}

// startProbeWorker runs a worker for a fresh task queue and returns the queue.
func startProbeWorker(t *testing.T) string {
	t.Helper()
	queue := "probe-" + uuid.NewString()
	w := worker.New(tc, queue, worker.Options{})
	w.RegisterWorkflowWithOptions(probeWorkflow, workflow.RegisterOptions{Name: "probe"})
	if err := w.Start(); err != nil {
		t.Fatalf("start worker: %v", err)
	}
	t.Cleanup(w.Stop)
	return queue
}

func serverCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// updateWithStart sends one update the way the relay will: start the workflow
// if it is not running, use the running one if it is.
func updateWithStart(t *testing.T, queue, workflowID string, u probeUpdate) (res probeResult, runID string, err error) {
	t.Helper()
	ctx := serverCtx(t)
	start := tc.NewWithStartWorkflowOperation(client.StartWorkflowOptions{
		ID:                       workflowID,
		TaskQueue:                queue,
		WorkflowIDConflictPolicy: enumspb.WORKFLOW_ID_CONFLICT_POLICY_USE_EXISTING,
	}, "probe")
	handle, err := tc.UpdateWithStartWorkflow(ctx, client.UpdateWithStartWorkflowOptions{
		StartWorkflowOperation: start,
		UpdateOptions: client.UpdateWorkflowOptions{
			UpdateID:     fmt.Sprintf("%s:%d", workflowID, u.Seq),
			UpdateName:   "apply",
			Args:         []any{u},
			WaitForStage: client.WorkflowUpdateStageCompleted,
		},
	})
	if err != nil {
		return probeResult{}, "", err
	}
	if err := handle.Get(ctx, &res); err != nil {
		return probeResult{}, "", err
	}
	run, err := start.Get(ctx)
	if err != nil {
		return probeResult{}, "", err
	}
	return res, run.GetRunID(), nil
}

// plainUpdate sends an update without starting anything.
func plainUpdate(t *testing.T, workflowID string, u probeUpdate) (probeResult, error) {
	t.Helper()
	ctx := serverCtx(t)
	handle, err := tc.UpdateWorkflow(ctx, client.UpdateWorkflowOptions{
		WorkflowID:   workflowID,
		UpdateID:     fmt.Sprintf("%s:%d", workflowID, u.Seq),
		UpdateName:   "apply",
		Args:         []any{u},
		WaitForStage: client.WorkflowUpdateStageCompleted,
	})
	if err != nil {
		return probeResult{}, err
	}
	var res probeResult
	err = handle.Get(ctx, &res)
	return res, err
}

// waitCompleted blocks until the workflow's current run has closed.
func waitCompleted(t *testing.T, workflowID string) {
	t.Helper()
	if err := tc.GetWorkflow(serverCtx(t), workflowID, "").Get(serverCtx(t), nil); err != nil {
		t.Fatalf("wait for workflow %s to complete: %v", workflowID, err)
	}
}

// --- the tests ---

// Update-With-Start starts the workflow on first use and reaches the same run
// afterwards (conflict policy USE_EXISTING).
func TestServer_UpdateWithStart_StartsThenReusesTheRun(t *testing.T) {
	queue := startProbeWorker(t)
	id := "item-" + uuid.NewString()

	first, run1, err := updateWithStart(t, queue, id, probeUpdate{Seq: 1})
	if err != nil {
		t.Fatalf("first update-with-start: %v", err)
	}
	second, run2, err := updateWithStart(t, queue, id, probeUpdate{Seq: 2})
	if err != nil {
		t.Fatalf("second update-with-start: %v", err)
	}
	if first != (probeResult{Seq: 1, Applied: 1}) || second != (probeResult{Seq: 2, Applied: 2}) {
		t.Errorf("results = %+v, %+v; want seq 1 applied 1, then seq 2 applied 2", first, second)
	}
	if run1 != run2 {
		t.Errorf("run IDs differ (%s, %s): the second call started a new run", run1, run2)
	}
}

// The same update ID sent twice to one run is handled once: the server
// returns the first result. This is the first of two guards against
// duplicates; lastAppliedSeq is the second, for when the run has changed.
func TestServer_SameUpdateIDTwice_IsHandledOnce(t *testing.T) {
	queue := startProbeWorker(t)
	id := "item-" + uuid.NewString()

	first, _, err := updateWithStart(t, queue, id, probeUpdate{Seq: 1})
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	again, err := plainUpdate(t, id, probeUpdate{Seq: 1})
	if err != nil {
		t.Fatalf("same update ID again: %v", err)
	}
	if again != first {
		t.Errorf("second result = %+v, want the first (%+v)", again, first)
	}
	next, err := plainUpdate(t, id, probeUpdate{Seq: 2})
	if err != nil {
		t.Fatalf("next update: %v", err)
	}
	if next.Applied != 2 {
		t.Errorf("updates handled = %d, want 2: the duplicate must not reach the handler", next.Applied)
	}
}

// "Nothing listening", case 1: a plain Update to a workflow that never
// existed fails with NotFound.
func TestServer_PlainUpdate_ToAMissingWorkflow_IsNotFound(t *testing.T) {
	_, err := plainUpdate(t, "item-"+uuid.NewString(), probeUpdate{Seq: 1})
	var notFound *serviceerror.NotFound
	if !errors.As(err, &notFound) {
		t.Fatalf("error = %T %v, want *serviceerror.NotFound", err, err)
	}
}

// "Nothing listening", case 2: a plain Update to a completed workflow also
// fails with NotFound, so the relay can treat both cases alike.
func TestServer_PlainUpdate_ToACompletedWorkflow_IsNotFound(t *testing.T) {
	queue := startProbeWorker(t)
	id := "item-" + uuid.NewString()
	if _, _, err := updateWithStart(t, queue, id, probeUpdate{Seq: 1, Finish: true}); err != nil {
		t.Fatalf("update-with-start: %v", err)
	}
	waitCompleted(t, id)

	_, err := plainUpdate(t, id, probeUpdate{Seq: 2})
	var notFound *serviceerror.NotFound
	if !errors.As(err, &notFound) {
		t.Fatalf("error = %T %v, want *serviceerror.NotFound", err, err)
	}
}

// After a run has completed, Update-With-Start with the same workflow ID
// starts a new run and delivers the update to it.
func TestServer_UpdateWithStart_AfterCompletion_StartsANewRun(t *testing.T) {
	queue := startProbeWorker(t)
	id := "item-" + uuid.NewString()
	_, run1, err := updateWithStart(t, queue, id, probeUpdate{Seq: 1, Finish: true})
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	waitCompleted(t, id)

	res, run2, err := updateWithStart(t, queue, id, probeUpdate{Seq: 2})
	if err != nil {
		t.Fatalf("update-with-start after completion: %v", err)
	}
	if run2 == run1 {
		t.Errorf("run ID is unchanged (%s): no new run was started", run1)
	}
	if res != (probeResult{Seq: 2, Applied: 1}) {
		t.Errorf("result = %+v, want seq 2 as the new run's first update", res)
	}
}
