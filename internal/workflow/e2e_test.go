// End-to-end tests for Step 3: the real kernel on Postgres, the real relay,
// the real workflow on a Temporal dev server, and a worker running in this
// process. They include the step's "done when": stopping the worker
// mid-timer still fires the timer.
//
// The timer here is two seconds long, and it runs in real time.
package workflow_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/worker"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
	"github.com/francoisnyc/work-kernel/internal/relay"
	"github.com/francoisnyc/work-kernel/internal/typedef"
	wf "github.com/francoisnyc/work-kernel/internal/workflow"
)

// pool is set in TestMain (server_test.go).
var pool *pgxpool.Pool

// A job is queued, then running. A running job that is not finished within
// two seconds goes back to the queue. A reviewed job is published by a timer
// too, but only if it has an owner: that guard lets a test make the kernel
// refuse a timer's move.
const jobType = `
id: job
version: 1
initial_state: queued
fields:
  - {id: f_owner, type: string}
  - {id: f_note, type: string}
states:
  - {id: queued, category: todo}
  - {id: running, category: active, timer: {after: 2s, to: queued}}
  - {id: review, category: active, timer: {after: 2s, to: published}}
  - {id: published, category: done}
  - {id: finished, category: done}
transitions:
  - {from: queued, to: running}
  - {from: running, to: queued}
  - {from: running, to: finished}
  - {from: queued, to: review}
  - {from: review, to: published, guard: 'f_owner is set'}
`

func registerJobType(ctx context.Context) error {
	ty, err := typedef.Parse([]byte(jobType))
	if err != nil {
		return err
	}
	return itemservice.New(pool).RegisterType(ctx, ty)
}

var alice = item.Principal{Actor: "user-alice"}

// --- the stack under test ---

// stack is one test's kernel, relay and worker, on a task queue of its own.
type stack struct {
	t          *testing.T
	k          *itemservice.Service
	queue      string
	closeDelay time.Duration // see wf.WorkerOptions.CloseDelay; zero except in one test
	worker     worker.Worker
	stopRelay  func()
}

// newStack starts a worker and a relay. The tests share one outbox, so each
// starts by marking whatever earlier tests left behind as delivered.
func newStack(t *testing.T) *stack {
	t.Helper()
	return newStackWithCloseDelay(t, 0)
}

func newStackWithCloseDelay(t *testing.T, closeDelay time.Duration) *stack {
	t.Helper()
	if _, err := pool.Exec(serverCtx(t), `update outbox set delivered_at = now() where delivered_at is null`); err != nil {
		t.Fatalf("clear outbox: %v", err)
	}
	s := &stack{t: t, k: itemservice.New(pool), queue: "items-" + uuid.NewString(), closeDelay: closeDelay}
	s.startWorker()
	s.startRelay()
	t.Cleanup(func() {
		s.stopRelay()
		s.stopWorker()
	})
	return s
}

func (s *stack) startWorker() {
	s.t.Helper()
	s.worker = wf.NewWorker(tc, s.queue, s.k, wf.WorkerOptions{CloseDelay: s.closeDelay})
	if err := s.worker.Start(); err != nil {
		s.t.Fatalf("start worker: %v", err)
	}
}

// stopWorker stops the worker, as a deploy or a crash would. Workflows keep
// their state on the server; nothing runs until a worker is back.
func (s *stack) stopWorker() {
	if s.worker != nil {
		s.worker.Stop()
		s.worker = nil
	}
}

func (s *stack) startRelay() {
	r := relay.New(pool, relay.NewTemporalWorkflows(tc, s.queue),
		relay.Options{PollInterval: 50 * time.Millisecond, Backoff: 200 * time.Millisecond, MaxBackoff: time.Second})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = r.Run(ctx)
	}()
	s.stopRelay = func() {
		cancel()
		<-done // wait until the relay has really stopped
	}
}

// --- helpers ---

func (s *stack) createJob(fields map[string]any) uuid.UUID {
	s.t.Helper()
	res, err := s.k.Create(serverCtx(s.t), itemservice.CreateRequest{
		IdempotencyKey: "key-" + uuid.NewString(), Principal: alice, TypeID: "job", TypeVersion: 1,
		Fields: fields, Attribution: item.Attribution{CorrelationID: "corr-" + uuid.NewString()},
	})
	if err != nil {
		s.t.Fatalf("create job: %v", err)
	}
	return res.ItemID
}

func (s *stack) move(id uuid.UUID, to string) itemservice.WriteResult {
	s.t.Helper()
	res, err := s.k.Transition(serverCtx(s.t), itemservice.TransitionRequest{
		IdempotencyKey: "key-" + uuid.NewString(), Principal: alice, ItemID: id, ToState: to,
	})
	if err != nil {
		s.t.Fatalf("transition to %s: %v", to, err)
	}
	return res
}

func (s *stack) setNote(id uuid.UUID, note string) itemservice.WriteResult {
	s.t.Helper()
	res, err := s.k.Update(serverCtx(s.t), itemservice.UpdateRequest{
		IdempotencyKey: "key-" + uuid.NewString(), Principal: alice, ItemID: id,
		Fields: map[string]any{"f_note": note},
	})
	if err != nil {
		s.t.Fatalf("update: %v", err)
	}
	return res
}

func (s *stack) state(id uuid.UUID) (state string, itemSeq int64) {
	s.t.Helper()
	it, err := s.k.Get(serverCtx(s.t), alice, id)
	if err != nil {
		s.t.Fatalf("Get: %v", err)
	}
	return it.State, it.ItemSeq
}

// These tests never sleep for a fixed time and then hope: a slow CI runner
// would make that flaky. They wait with one of two helpers.

const pollEvery = 100 * time.Millisecond

// eventually polls cond until it holds, and fails the test if it does not
// within the deadline. It returns as soon as cond holds.
func eventually(t *testing.T, within time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("not within %v: %s", within, what)
		}
		time.Sleep(pollEvery)
	}
}

// staysFor polls cond for the whole duration and fails the test the moment
// it stops holding. It is for negative checks: "nothing moves while ...".
func staysFor(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for {
		if !cond() {
			t.Fatalf("did not stay true for %v: %s", d, what)
		}
		if time.Now().After(deadline) {
			return
		}
		time.Sleep(pollEvery)
	}
}

func (s *stack) isAt(id uuid.UUID, state string, itemSeq int64) func() bool {
	return func() bool {
		got, seq := s.state(id)
		return got == state && seq == itemSeq
	}
}

// activityFailures returns the error type of every failed FireTimer attempt
// in the history of the Item's latest run, and whether all were non-retryable.
func activityFailures(t *testing.T, id uuid.UUID) (types []string, allFinal bool) {
	t.Helper()
	allFinal = true
	iter := tc.GetWorkflowHistory(serverCtx(t), id.String(), "", false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
	for iter.HasNext() {
		ev, err := iter.Next()
		if err != nil {
			t.Fatalf("read history: %v", err)
		}
		if failed := ev.GetActivityTaskFailedEventAttributes(); failed != nil {
			info := failed.GetFailure().GetApplicationFailureInfo()
			types = append(types, info.GetType())
			allFinal = allFinal && info.GetNonRetryable()
		}
	}
	return types, allFinal
}

func (s *stack) waitState(id uuid.UUID, want string, within time.Duration) {
	s.t.Helper()
	eventually(s.t, within, "item in state "+want, func() bool {
		got, _ := s.state(id)
		return got == want
	})
}

func (s *stack) waitDelivered(id uuid.UUID) {
	s.t.Helper()
	eventually(s.t, 15*time.Second, "all of the item's events delivered", func() bool {
		var n int
		err := pool.QueryRow(serverCtx(s.t),
			`select count(*) from outbox where item_id = $1 and delivered_at is null`, id).Scan(&n)
		return err == nil && n == 0
	})
}

// runStatus returns the status and run ID of the Item's latest workflow run;
// found is false if the Item never had a workflow.
func runStatus(t *testing.T, id uuid.UUID) (status enumspb.WorkflowExecutionStatus, runID string, historyLen int64, found bool) {
	t.Helper()
	desc, err := tc.DescribeWorkflowExecution(serverCtx(t), id.String(), "")
	var notFound *serviceerror.NotFound
	if errors.As(err, &notFound) {
		return 0, "", 0, false
	}
	if err != nil {
		t.Fatalf("describe workflow: %v", err)
	}
	info := desc.GetWorkflowExecutionInfo()
	return info.GetStatus(), info.GetExecution().GetRunId(), info.GetHistoryLength(), true
}

func (s *stack) waitRunning(id uuid.UUID) (runID string) {
	s.t.Helper()
	eventually(s.t, 15*time.Second, "a running workflow for the item", func() bool {
		status, run, _, found := runStatus(s.t, id)
		runID = run
		return found && status == enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING
	})
	return runID
}

func (s *stack) waitCompleted(id uuid.UUID) {
	s.t.Helper()
	eventually(s.t, 15*time.Second, "the item's workflow completed", func() bool {
		status, _, _, found := runStatus(s.t, id)
		return found && status == enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED
	})
}

// timerEvent is what these tests check on the event a timer wrote.
type timerEvent struct {
	Actor, Op, Hook, Causation, Correlation string
	Hop                                     int
}

func readEvent(t *testing.T, id uuid.UUID, seq int64) timerEvent {
	t.Helper()
	var ev timerEvent
	err := pool.QueryRow(serverCtx(t),
		`select actor, op, coalesce(originating_hook, ''), coalesce(causation_id, ''),
		        coalesce(correlation_id, ''), hop
		 from item_events where item_id = $1 and item_seq = $2`, id, seq,
	).Scan(&ev.Actor, &ev.Op, &ev.Hook, &ev.Causation, &ev.Correlation, &ev.Hop)
	if err != nil {
		t.Fatalf("read event %d: %v", seq, err)
	}
	return ev
}

// assertTimerWrote checks that event seq is the timer's transition out of
// `state`, attributed to the event enteredSeq that armed it.
func assertTimerWrote(t *testing.T, id uuid.UUID, seq int64, state string, enteredSeq int64) {
	t.Helper()
	got := readEvent(t, id, seq)
	entered := readEvent(t, id, enteredSeq)
	want := timerEvent{
		Actor: "system:timer", Op: "transition",
		Hook:        "timer:job@v1:" + state,
		Causation:   fmt.Sprintf("%s:%d", id, enteredSeq),
		Correlation: entered.Correlation,
		Hop:         entered.Hop + 1,
	}
	if got != want {
		t.Errorf("event %d = %+v\nwant %+v", seq, got, want)
	}
}

// --- Step 3 "done when" ---

// The worker stops while the timer is running and comes back before it is
// due. The timer lives on the server, so it fires on time all the same.
func TestE2E_WorkerStoppedMidTimer_RestartedBeforeItIsDue(t *testing.T) {
	s := newStack(t)
	id := s.createJob(nil) // item_seq 1, queued: no workflow yet
	s.move(id, "running")  // item_seq 2: the workflow starts, 2s timer
	s.waitRunning(id)
	armed := time.Now()

	s.stopWorker()
	s.startWorker()

	s.waitState(id, "queued", 20*time.Second)
	if early := time.Since(armed) < time.Second; early {
		t.Errorf("the job left running after %v: the 2s timer fired early", time.Since(armed))
	}
	if _, seq := s.state(id); seq != 3 {
		t.Fatalf("item_seq = %d, want 3 (create, to running, the timer's move)", seq)
	}
	assertTimerWrote(t, id, 3, "running", 2)
	// The timer's own event reaches the workflow, which then has nothing
	// left to wait for.
	s.waitCompleted(id)
}

// The worker is down when the timer comes due. Nothing happens until a
// worker is back; then the timer fires at once.
func TestE2E_WorkerStoppedMidTimer_RestartedAfterItWasDue(t *testing.T) {
	s := newStack(t)
	id := s.createJob(nil)
	s.move(id, "running")
	s.waitRunning(id)

	s.stopWorker()
	// The 2s timer comes due during these 3s, and with no worker nothing moves.
	staysFor(t, 3*time.Second, "the job stays running while the worker is down", s.isAt(id, "running", 2))
	s.startWorker()

	s.waitState(id, "queued", 30*time.Second)
	if _, seq := s.state(id); seq != 3 {
		t.Fatalf("item_seq = %d, want 3", seq)
	}
	assertTimerWrote(t, id, 3, "running", 2)
	s.waitCompleted(id)
}

// --- lazy start and "nothing listening" ---

// An Item that never enters a state with behavior never gets a workflow, and
// its events are still marked delivered ("not found").
func TestE2E_ItemWithoutBehavior_GetsNoWorkflow(t *testing.T) {
	s := newStack(t)
	id := s.createJob(nil)
	s.setNote(id, "still queued")

	s.waitDelivered(id)
	if _, _, _, found := runStatus(t, id); found {
		t.Error("a workflow exists for an item that never entered a timer state")
	}
}

// After the workflow has completed, later events that enter no timer state
// find nothing listening ("already completed") and are marked delivered.
func TestE2E_EventAfterTheWorkflowCompleted_IsMarkedDelivered(t *testing.T) {
	s := newStack(t)
	id := s.createJob(nil)
	s.move(id, "running")
	firstRun := s.waitRunning(id)
	s.move(id, "finished") // no behavior: the workflow completes
	s.waitCompleted(id)

	s.setNote(id, "after the fact")

	s.waitDelivered(id)
	status, run, _, _ := runStatus(t, id)
	if status != enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED || run != firstRun {
		t.Errorf("workflow = %v, run %s; want the first run (%s), still completed", status, run, firstRun)
	}
}

// --- a new run after a finished one ---

// The workflow finished; later the Item enters a timer state again. A new
// run starts, loads the Item from Postgres, and its timer fires.
func TestE2E_ReenteringATimerState_StartsANewRun(t *testing.T) {
	s := newStack(t)
	id := s.createJob(nil) // 1
	s.move(id, "running")  // 2
	firstRun := s.waitRunning(id)
	s.move(id, "queued") // 3: a person takes it back; the workflow completes
	s.waitCompleted(id)

	s.move(id, "running") // 4
	secondRun := s.waitRunning(id)
	if secondRun == firstRun {
		t.Fatalf("run ID is unchanged (%s): no new run was started", firstRun)
	}

	s.waitState(id, "queued", 20*time.Second)
	if _, seq := s.state(id); seq != 5 {
		t.Fatalf("item_seq = %d, want 5", seq)
	}
	assertTimerWrote(t, id, 5, "running", 4)
}

// An event can reach the workflow just as it is finishing. To hit that
// window every time, this test's worker makes the workflow pause for a while
// between deciding to complete and returning (CloseDelay). The Item leaves
// the timer state, and re-enters it inside that pause. The event must not be
// lost: the closing run takes it and carries on, and the timer it arms fires.
func TestE2E_EventWhileTheRunIsClosing_IsNotLost(t *testing.T) {
	s := newStackWithCloseDelay(t, 5*time.Second)
	id := s.createJob(nil) // 1
	s.move(id, "running")  // 2
	run := s.waitRunning(id)
	s.waitDelivered(id)

	s.move(id, "queued")  // 3: nothing left to wait for; the run starts closing
	s.waitDelivered(id)   // the workflow has applied 3, so it is inside the pause now
	s.move(id, "running") // 4: arrives while the run is closing

	s.waitDelivered(id)
	status, sameRun, _, _ := runStatus(t, id)
	if status != enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING || sameRun != run {
		t.Fatalf("workflow = %v, run %s; want run %s still running: the event should have reached the closing run",
			status, sameRun, run)
	}
	s.waitState(id, "queued", 20*time.Second)
	if _, seq := s.state(id); seq != 5 {
		t.Fatalf("item_seq = %d, want 5", seq)
	}
	assertTimerWrote(t, id, 5, "running", 4)
}

// --- stale, with the real kernel ---

// The relay is down, so a field update does not reach the workflow. When the
// timer fires, the kernel answers "stale": the Item is at 3, the workflow
// thinks 2. Once the relay is back and the update arrives, the timer fires
// again, as a new request under a new idempotency key, and goes through.
func TestE2E_StaleTimer_FiresAgainOnceTheWorkflowHasCaughtUp(t *testing.T) {
	s := newStack(t)
	id := s.createJob(nil) // 1
	s.move(id, "running")  // 2
	s.waitRunning(id)
	s.waitDelivered(id)

	s.stopRelay()
	s.setNote(id, "the workflow does not know this yet") // 3, undelivered
	eventually(t, 20*time.Second, "the timer fired and was told it is stale", func() bool {
		types, _ := activityFailures(t, id)
		return len(types) > 0
	})
	if types, final := activityFailures(t, id); len(types) != 1 || types[0] != wf.ErrTypeStale || !final {
		t.Fatalf("failed FireTimer attempts = %v (non-retryable %v), want one non-retryable %q", types, final, wf.ErrTypeStale)
	}
	// Stale wrote nothing, and without the missing event the workflow waits.
	staysFor(t, 2*time.Second, "the job stays running at item_seq 3 while the relay is down", s.isAt(id, "running", 3))

	s.startRelay()
	s.waitState(id, "queued", 20*time.Second)
	if _, seq := s.state(id); seq != 4 {
		t.Fatalf("item_seq = %d, want 4", seq)
	}
	assertTimerWrote(t, id, 4, "running", 2)
	// Both attempts left a trace only where they should: the stale one wrote
	// nothing and kept no idempotency key.
	var keys int
	err := pool.QueryRow(serverCtx(t),
		`select count(*) from idempotency where actor = 'system:timer' and key like $1`,
		"timer:"+id.String()+":%").Scan(&keys)
	if err != nil {
		t.Fatalf("count keys: %v", err)
	}
	if keys != 1 {
		t.Errorf("timer idempotency keys stored = %d, want 1 (the attempt that went through)", keys)
	}
}

// --- the kernel refuses the timer's move ---

// The timer on review moves to published only if the job has an owner. This
// one has none, so the kernel rejects the move. That is final: the activity
// is not retried, the Item stays where it is, and the workflow keeps running.
func TestE2E_KernelRejectsTheTimersMove_NoRetry(t *testing.T) {
	s := newStack(t)
	id := s.createJob(nil) // 1, no owner
	s.move(id, "review")   // 2: 2s timer to published
	s.waitRunning(id)

	eventually(t, 20*time.Second, "the timer fired and the kernel refused its move", func() bool {
		types, _ := activityFailures(t, id)
		return len(types) > 0
	})
	// Give retries time to show up, if there were any; nothing may change.
	staysFor(t, 3*time.Second, "one final rejection, the job still in review at item_seq 2", func() bool {
		types, final := activityFailures(t, id)
		return len(types) == 1 && types[0] == wf.ErrTypeRejected && final && s.isAt(id, "review", 2)()
	})
	if status, _, _, _ := runStatus(t, id); status != enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING {
		t.Errorf("workflow status = %v, want running", status)
	}
}

// --- gaps do not grow history ---

// A gap is refused by the update's validator, and what a validator refuses
// is not written to history: the relay can retry as often as it needs to.
func TestE2E_RejectedGap_DoesNotGrowHistory(t *testing.T) {
	s := newStack(t)
	id := s.createJob(nil)
	s.move(id, "running") // item_seq 2
	s.waitRunning(id)
	s.waitDelivered(id)
	_, _, before, _ := runStatus(t, id)

	workflows := relay.NewTemporalWorkflows(tc, s.queue)
	for range 3 {
		err := workflows.Deliver(serverCtx(t), relay.Delivery{
			ItemID: id.String(), UpdateID: id.String() + ":9",
			Event: wf.Event{Seq: 9, Op: "update", At: time.Now()},
		})
		if err == nil || errors.Is(err, relay.ErrNothingListening) {
			t.Fatalf("delivering event 9 after 2: error = %v, want the gap to be refused", err)
		}
	}

	_, _, after, _ := runStatus(t, id)
	if after != before {
		t.Errorf("history length went from %d to %d: refused gaps were recorded", before, after)
	}
}
