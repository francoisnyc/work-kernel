// End-to-end test for Jira part 2, step 1: a run with a pending timer at a
// field's instant continues as new, and the timer survives. On a real
// Temporal server: the first run arms the timer, re-arms it when the end
// moves, and is then told to continue as new; the second run carries the
// instant it was handed (it reads nothing again) and closes the lap at its
// instant, once.
//
// A dev server suggests continue-as-new after thousands of history events.
// This test starts a second one that suggests it after canHistoryCount, so
// a handful of updates is enough. With RECORD_HISTORIES=1 it also saves the
// two runs' histories for TestReplay_AtTimerHistories.
package workflow_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	enumspb "go.temporal.io/api/enums/v1"
	historypb "go.temporal.io/api/history/v1"
	"go.temporal.io/api/temporalproto"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/francoisnyc/work-kernel/internal/itemservice"
	wf "github.com/francoisnyc/work-kernel/internal/workflow"
)

// canHistoryCount is the history length after which the second server
// suggests continue-as-new.
const canHistoryCount = 40

// The histories of a lap whose timer was armed, re-armed, carried over a
// continue-as-new and fired: recorded by TestE2E_At_ContinueAsNew_KeepsTheTimer
// (RECORD_HISTORIES=1) from the code that first had at-timers. Do not
// re-record them after changing the workflow: then they would prove nothing.
var atHistories = []string{
	"lap_at_rearm_then_continue_as_new.json", // run 1: LoadInstant, a timer, canceled and re-armed, continue-as-new
	"lap_at_carried_timer_fires.json",        // run 2: the carried instant, the timer fires, FireTimer
}

// useContinueAsNewServer points the package's client at a Temporal dev
// server that suggests continue-as-new early, for this test only. The
// package's tests do not run in parallel, so nothing else sees the swap.
func useContinueAsNewServer(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	server, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: temporalImage,
			Cmd: []string{"server", "start-dev", "--ip", "0.0.0.0", "--headless",
				"--dynamic-config-value", fmt.Sprintf("limit.historyCount.suggestContinueAsNew=%d", canHistoryCount)},
			ExposedPorts: []string{"7233/tcp"},
			WaitingFor:   wait.ForListeningPort("7233/tcp").WithStartupTimeout(2 * time.Minute),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("start a second temporal: %v", err)
	}
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(server) })
	hostPort, err := server.PortEndpoint(ctx, "7233/tcp", "")
	if err != nil {
		t.Fatalf("temporal endpoint: %v", err)
	}
	var c client.Client
	for attempt := 0; ; attempt++ {
		if c, err = client.Dial(client.Options{HostPort: hostPort}); err == nil {
			break
		}
		if attempt == 30 {
			t.Fatalf("connect to the second temporal: %v", err)
		}
		time.Sleep(time.Second)
	}
	shared := tc
	tc = c
	// Cleanups run last in, first out: the stack's worker and relay (set up
	// after this) stop before the client goes back.
	t.Cleanup(func() {
		tc = shared
		c.Close()
	})
}

func TestE2E_At_ContinueAsNew_KeepsTheTimer(t *testing.T) {
	useContinueAsNewServer(t)
	s := newLapStack(t, 0, wf.WorkerOptions{})

	id := s.newLap(in(time.Hour), nil)
	s.move(id, "active") // event 2: the timer is armed, an hour away
	first := s.waitRunning(id)
	end := in(12 * time.Second)
	s.setEnd(id, end) // event 3: re-armed
	s.waitDelivered(id)

	// Events that do not touch the end, until the server has suggested
	// continue-as-new and the run has handed over, the timer pending.
	second := first
	for i := 0; second == first; i++ {
		if i == 60 {
			t.Fatalf("after %d updates the run is still %s: no continue-as-new", i, first)
		}
		if time.Until(*end) < 4*time.Second {
			t.Fatalf("no continue-as-new %v before the end (after %d updates): the test's own margin is gone", time.Until(*end), i)
		}
		if _, err := s.k.Update(serverCtx(t), itemservice.UpdateRequest{
			IdempotencyKey: "key-" + uuid.NewString(), Principal: alice, ItemID: id,
			Fields: map[string]any{"f_name": fmt.Sprintf("lap, renamed %d", i)},
		}); err != nil {
			t.Fatalf("rename: %v", err)
		}
		s.waitDelivered(id)
		_, second, _, _ = runStatus(t, id)
	}
	desc, err := tc.DescribeWorkflowExecution(serverCtx(t), id.String(), first)
	if err != nil {
		t.Fatalf("describe the first run: %v", err)
	}
	if got := desc.GetWorkflowExecutionInfo().GetStatus(); got != enumspb.WORKFLOW_EXECUTION_STATUS_CONTINUED_AS_NEW {
		t.Fatalf("the first run is %v, want continued as new", got)
	}
	if state, _ := s.state(id); state != "active" {
		t.Fatalf("the lap is %s after the handover, want still active: its end is seconds away", state)
	}

	// The second run closes the lap at its instant, once.
	staysFor(t, time.Until(*end)-300*time.Millisecond, "active until the end", func() bool {
		state, _ := s.state(id)
		return state == "active"
	})
	s.waitState(id, "closed", 15*time.Second)
	s.assertClosedByTheTimer(id, *end, 2)
	s.waitCompleted(id)
	if _, last, _, _ := runStatus(t, id); last != second {
		t.Errorf("a third run %s closed the lap; want the second, %s", last, second)
	}

	// The second run was handed the instant: it loaded nothing and read
	// nothing. The first read it twice: on entering the state, and when the
	// end moved; never for a rename.
	for run, want := range map[string]map[string]int{
		first:  {wf.ActivityLoadItem: 1, wf.ActivityLoadInstant: 2, wf.ActivityFireTimer: 0},
		second: {wf.ActivityLoadItem: 0, wf.ActivityLoadInstant: 0, wf.ActivityFireTimer: 1},
	} {
		got := activityCounts(t, id, run)
		for name, n := range want {
			if got[name] != n {
				t.Errorf("run %s: %d %s activities, want %d (all: %v)", run, got[name], name, n, got)
			}
		}
	}

	if os.Getenv("RECORD_HISTORIES") == "1" {
		saveRunHistory(t, id, first, atHistories[0])
		saveRunHistory(t, id, second, atHistories[1])
	}
}

// runHistory reads one run's history.
func runHistory(t *testing.T, id uuid.UUID, runID string) *historypb.History {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	it := tc.GetWorkflowHistory(ctx, id.String(), runID, false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
	var history historypb.History
	for it.HasNext() {
		ev, err := it.Next()
		if err != nil {
			t.Fatalf("read history: %v", err)
		}
		history.Events = append(history.Events, ev)
	}
	return &history
}

// activityCounts counts the activities one run scheduled, by name.
func activityCounts(t *testing.T, id uuid.UUID, runID string) map[string]int {
	t.Helper()
	out := map[string]int{}
	for _, ev := range runHistory(t, id, runID).Events {
		if a := ev.GetActivityTaskScheduledEventAttributes(); a != nil {
			out[a.GetActivityType().GetName()]++
		}
	}
	return out
}

func saveRunHistory(t *testing.T, id uuid.UUID, runID, name string) {
	t.Helper()
	history := runHistory(t, id, runID)
	data, err := temporalproto.CustomJSONMarshalOptions{Indent: "  "}.Marshal(history)
	if err != nil {
		t.Fatalf("encode history: %v", err)
	}
	if err := os.MkdirAll(historiesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(historiesDir, name), data, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("recorded %d events to %s", len(history.Events), name)
}

// The recorded at-timer histories replay on today's workflow code without a
// non-determinism error: the first run ends by continuing as new with its
// timer pending, the second fires the timer it was handed.
func TestReplay_AtTimerHistories(t *testing.T) {
	for _, name := range atHistories {
		t.Run(name, func(t *testing.T) {
			f, err := os.Open(filepath.Join(historiesDir, name))
			if err != nil {
				t.Fatalf("open recorded history: %v", err)
			}
			defer f.Close()
			history, err := client.HistoryFromJSON(f, client.HistoryJSONOptions{})
			if err != nil {
				t.Fatalf("read history: %v", err)
			}
			replayer := worker.NewWorkflowReplayer()
			replayer.RegisterWorkflowWithOptions(wf.ItemWorkflow, workflow.RegisterOptions{Name: wf.Name})
			if err := replayer.ReplayWorkflowHistory(nil, history); err != nil {
				t.Fatalf("replay %s: %v", name, err)
			}
		})
	}
}
