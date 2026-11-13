// Tests for the Item workflow, run in Temporal's test environment: no server,
// and time skips forward whenever the workflow is only waiting. The two
// activities are replaced by fakes, so no database is involved either.
//
// Each test starts from a "snapshot": what the first activity loaded from
// Postgres. Events are then delivered at chosen moments, as the relay would.
package workflow_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	sdk "go.temporal.io/sdk/workflow"

	wf "github.com/francoisnyc/work-kernel/internal/workflow"
)

const itemID = "0193a1b2-0000-7000-8000-000000000001"

// t0 is when every test's workflow starts.
var t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// docTimers is the timer table of the tests' Type: states draft, review,
// escalated and published, of which two have a timer, so a test can move from
// one timer to another. The workflow never sees the Type itself, only this.
func docTimers() map[string]wf.TimerSpec {
	return map[string]wf.TimerSpec{
		"review":    {After: 4 * time.Hour, To: "draft"},
		"escalated": {After: time.Hour, To: "draft"},
	}
}

// snapshot returns what LoadItem would report for a doc that entered `state`
// with event enteredSeq at enteredAt, and is now at itemSeq.
func snapshot(t *testing.T, state string, itemSeq, enteredSeq int64, enteredAt time.Time) wf.Snapshot {
	t.Helper()
	return wf.Snapshot{
		TypeID: "doc", TypeVersion: 1, Timers: docTimers(),
		State: state, LastAppliedSeq: itemSeq,
		EnteredSeq: enteredSeq, EnteredAt: enteredAt,
		CorrelationID: "corr-1", Hop: 2,
	}
}

// fired is one call of the FireTimer activity, and the workflow time it happened at.
type fired struct {
	In wf.FireTimerInput
	At time.Time
}

// harness is one test's workflow environment plus the fake activities.
type harness struct {
	t   *testing.T
	env *testsuite.TestWorkflowEnvironment

	// Activities run on other goroutines, so the recordings are locked.
	mu       sync.Mutex
	loads    int
	fires    []fired
	fireErrs []error // returned by successive FireTimer calls; nil when used up

	// loadGate, if set, holds LoadItem back until it is closed, so a test can
	// make sure an update is accepted while the load is still running.
	loadGate chan struct{}
	loadDone bool
	loadErr  error // if set, LoadItem fails with it
}

func newHarness(t *testing.T, loaded wf.Snapshot) *harness {
	t.Helper()
	var suite testsuite.WorkflowTestSuite
	h := &harness{t: t, env: suite.NewTestWorkflowEnvironment()}
	h.env.SetStartTime(t0)
	h.env.RegisterWorkflowWithOptions(wf.ItemWorkflow, sdk.RegisterOptions{Name: wf.Name})

	h.env.RegisterActivityWithOptions(func(ctx context.Context, id string) (wf.Snapshot, error) {
		if h.loadGate != nil {
			select {
			case <-h.loadGate:
			case <-time.After(5 * time.Second): // real time; the test then fails on its own checks
			}
		}
		h.mu.Lock()
		defer h.mu.Unlock()
		h.loads++
		h.loadDone = true
		if h.loadErr != nil {
			return wf.Snapshot{}, h.loadErr
		}
		if id != itemID {
			return wf.Snapshot{}, fmt.Errorf("LoadItem called for %q", id)
		}
		return loaded, nil
	}, activity.RegisterOptions{Name: wf.ActivityLoadItem})

	h.env.RegisterActivityWithOptions(func(ctx context.Context, in wf.FireTimerInput) error {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.fires = append(h.fires, fired{In: in, At: h.env.Now()})
		if len(h.fireErrs) == 0 {
			return nil
		}
		err := h.fireErrs[0]
		h.fireErrs = h.fireErrs[1:]
		return err
	}, activity.RegisterOptions{Name: wf.ActivityFireTimer})
	return h
}

// outcome is how one delivered event ended.
type outcome struct {
	acceptedBeforeLoad bool  // the validator let it in while LoadItem was still running
	rejected           error // refused by the validator: never ran, never entered history
	done               bool  // the handler ran to its end
	applied            bool
	err                error
}

// deliver sends one event to the workflow `after` the start, as the relay
// would, and returns where its outcome will be recorded.
func (h *harness) deliver(after time.Duration, ev wf.Event) *outcome {
	h.t.Helper()
	out := &outcome{}
	ev.At = t0.Add(after)
	h.env.RegisterDelayedCallback(func() {
		// A unique update ID each time: the server would drop a repeated ID
		// itself, and these tests are about the workflow's own guard.
		id := fmt.Sprintf("%s:%d@%s", itemID, ev.Seq, after)
		h.env.UpdateWorkflow(wf.UpdateApply, id, &testsuite.TestUpdateCallback{
			OnAccept: func() {
				h.mu.Lock()
				out.acceptedBeforeLoad = !h.loadDone
				h.mu.Unlock()
			},
			OnReject: func(err error) { out.rejected = err },
			OnComplete: func(res any, err error) {
				out.done, out.err = true, err
				if r, ok := res.(wf.ApplyResult); ok {
					out.applied = r.Applied
				}
			},
		}, ev)
	}, after)
	return out
}

func transitionEvent(seq int64, from, to string) wf.Event {
	return wf.Event{Seq: seq, Op: "transition", From: from, To: to}
}

func updateEvent(seq int64) wf.Event { return wf.Event{Seq: seq, Op: "update"} }

// run executes the workflow to its end and returns its error.
func (h *harness) run(in wf.Input) error {
	h.t.Helper()
	h.env.ExecuteWorkflow(wf.Name, in)
	if !h.env.IsWorkflowCompleted() {
		h.t.Fatal("the workflow did not finish")
	}
	return h.env.GetWorkflowError()
}

// runToCompletion expects the run to end normally.
func (h *harness) runToCompletion() {
	h.t.Helper()
	if err := h.run(wf.Input{ItemID: itemID}); err != nil {
		h.t.Fatalf("workflow error: %v", err)
	}
}

func (h *harness) wantApplied(name string, out *outcome, want bool) {
	h.t.Helper()
	if out.rejected != nil || !out.done || out.err != nil || out.applied != want {
		h.t.Errorf("%s: rejected %v, done %v, applied %v, error %v; want it accepted, applied %v, without error",
			name, out.rejected, out.done, out.applied, out.err, want)
	}
}

func stale() error {
	return temporal.NewNonRetryableApplicationError("item moved on", wf.ErrTypeStale, nil)
}

func notFound() error {
	return temporal.NewNonRetryableApplicationError("item not found", wf.ErrTypeNotFound, nil)
}

func rejected() error {
	return temporal.NewNonRetryableApplicationError("guard not satisfied", wf.ErrTypeRejected, nil)
}

// --- the timer ---

// The timer runs from the moment the Item entered the state, not from when
// the workflow started. When it fires, the activity is called once, with
// everything the kernel write needs.
func TestItemWorkflow_TimerFires_OnceWithTheRightRequest(t *testing.T) {
	// In review since one hour before the workflow started; the timer is 4h.
	h := newHarness(t, snapshot(t, "review", 5, 5, t0.Add(-time.Hour)))
	back := h.deliver(3*time.Hour+time.Minute, transitionEvent(6, "review", "draft")) // the timer's own write
	h.runToCompletion()

	want := wf.FireTimerInput{
		ItemID: itemID, TypeID: "doc", TypeVersion: 1,
		FromState: "review", ToState: "draft",
		ExpectedItemSeq: 5, EnteredSeq: 5,
		CorrelationID: "corr-1", Hop: 3, // one deeper than the event that armed it
		IdempotencyKey: "timer:" + itemID + ":5:5", // item : entered seq : expected seq
	}
	if len(h.fires) != 1 {
		t.Fatalf("FireTimer calls = %+v, want exactly one", h.fires)
	}
	// Times are compared with Equal: two time.Time values for the same
	// instant can differ in how they store the zone, which DeepEqual sees.
	if at := h.fires[0].At; !at.Equal(t0.Add(3 * time.Hour)) {
		t.Errorf("FireTimer called at %v, want %v", at, t0.Add(3*time.Hour))
	}
	if !reflect.DeepEqual(h.fires[0].In, want) {
		t.Errorf("FireTimer request = %+v\nwant %+v", h.fires[0].In, want)
	}
	h.wantApplied("the timer's transition", back, true)
	if h.loads != 1 {
		t.Errorf("LoadItem calls = %d, want 1", h.loads)
	}
}

// A timer that was already due when the workflow started fires at once.
// This is the "worker was down past the deadline" case.
func TestItemWorkflow_TimerAlreadyDue_FiresImmediately(t *testing.T) {
	h := newHarness(t, snapshot(t, "review", 5, 5, t0.Add(-5*time.Hour)))
	h.deliver(time.Minute, transitionEvent(6, "review", "draft"))
	h.runToCompletion()

	if len(h.fires) != 1 || !h.fires[0].At.Equal(t0) {
		t.Errorf("FireTimer calls = %+v, want one, at the start", h.fires)
	}
}

// Leaving the state cancels its timer. In a state with no behavior the
// workflow has nothing to wait for, and completes.
func TestItemWorkflow_LeavingTheState_CancelsTheTimerAndCompletes(t *testing.T) {
	h := newHarness(t, snapshot(t, "review", 5, 5, t0))
	out := h.deliver(time.Hour, transitionEvent(6, "review", "published"))
	h.runToCompletion()

	h.wantApplied("transition to published", out, true)
	if len(h.fires) != 0 {
		t.Errorf("FireTimer calls = %+v, want none", h.fires)
	}
	if now := h.env.Now(); now.After(t0.Add(2 * time.Hour)) {
		t.Errorf("workflow ended at %v: it waited for a canceled timer", now)
	}
}

// Moving to another state with a timer replaces the old timer with the new
// state's, counted from the event that entered it.
func TestItemWorkflow_MovingToAnotherTimedState_RearmsTheTimer(t *testing.T) {
	h := newHarness(t, snapshot(t, "review", 5, 5, t0))
	enter := transitionEvent(6, "review", "escalated") // its 1h timer starts at +1h
	enter.CorrelationID, enter.Hop = "corr-2", 4
	h.deliver(time.Hour, enter)
	h.deliver(2*time.Hour+time.Minute, transitionEvent(7, "escalated", "draft"))
	h.runToCompletion()

	if len(h.fires) != 1 {
		t.Fatalf("FireTimer calls = %+v, want exactly one", h.fires)
	}
	got := h.fires[0]
	if !got.At.Equal(t0.Add(2*time.Hour)) || got.In.FromState != "escalated" || got.In.ToState != "draft" ||
		got.In.EnteredSeq != 6 || got.In.ExpectedItemSeq != 6 {
		t.Errorf("FireTimer call = %+v, want escalated -> draft at +2h, entered and expected seq 6", got)
	}
	// Attribution follows the event that entered the state.
	if got.In.CorrelationID != "corr-2" || got.In.Hop != 5 {
		t.Errorf("correlation %q, hop %d; want corr-2 and 5 (the entering event's hop + 1)",
			got.In.CorrelationID, got.In.Hop)
	}
}

// A field update does not restart the timer, but the timer's write must name
// the Item's latest item_seq.
func TestItemWorkflow_FieldUpdate_KeepsTheTimerAndMovesTheExpectedSeq(t *testing.T) {
	h := newHarness(t, snapshot(t, "review", 5, 5, t0))
	h.deliver(time.Hour, updateEvent(6))
	h.deliver(4*time.Hour+time.Minute, transitionEvent(7, "review", "draft"))
	h.runToCompletion()

	if len(h.fires) != 1 {
		t.Fatalf("FireTimer calls = %+v, want exactly one", h.fires)
	}
	got := h.fires[0]
	if !got.At.Equal(t0.Add(4*time.Hour)) || got.In.ExpectedItemSeq != 6 || got.In.EnteredSeq != 5 {
		t.Errorf("FireTimer call = %+v, want +4h with expected seq 6 and entered seq 5", got)
	}
}

// The kernel may refuse the timer's move (a guard, say). That is logged, not
// retried, and does not fail the workflow, which goes on following the Item.
func TestItemWorkflow_KernelRejectsTheTimer_WorkflowCarriesOn(t *testing.T) {
	h := newHarness(t, snapshot(t, "review", 5, 5, t0))
	h.fireErrs = []error{rejected()}
	later := h.deliver(9*time.Hour, transitionEvent(6, "review", "published")) // a person moves it
	h.runToCompletion()

	if len(h.fires) != 1 {
		t.Errorf("FireTimer calls = %d, want 1: a rejection is not retried", len(h.fires))
	}
	h.wantApplied("later transition", later, true)
}

// Anything else that goes wrong in the activity (the kernel is down, a
// timeout) is retried with backoff, with no limit on attempts: a timer must
// not be lost to an outage. Every attempt is the same request, same key.
func TestItemWorkflow_TimerActivityFails_IsRetriedUntilItWorks(t *testing.T) {
	h := newHarness(t, snapshot(t, "review", 5, 5, t0))
	for range 5 {
		h.fireErrs = append(h.fireErrs, errors.New("kernel is down"))
	}
	h.deliver(4*time.Hour+10*time.Minute, transitionEvent(6, "review", "draft"))
	h.runToCompletion()

	if len(h.fires) != 6 {
		t.Fatalf("FireTimer calls = %d, want 6 (five failures, then success)", len(h.fires))
	}
	for i, f := range h.fires {
		if f.In != h.fires[0].In {
			t.Errorf("attempt %d sent %+v, want the same request as the first", i+1, f.In)
		}
	}
	if !h.fires[5].At.After(h.fires[0].At) {
		t.Errorf("attempts at %v and %v: no backoff between retries", h.fires[0].At, h.fires[5].At)
	}
}

// Stale means the workflow is behind: an event has not reached it yet. If
// that event turns out to be a field update, the Item is still in the state,
// so the timer fires again with the new item_seq. The second request differs
// from the first, so it needs its own idempotency key: the kernel refuses a
// key reused with a different request.
func TestItemWorkflow_StaleThenAFieldUpdate_FiresAgain(t *testing.T) {
	h := newHarness(t, snapshot(t, "review", 5, 5, t0))
	h.fireErrs = []error{stale()}
	h.deliver(4*time.Hour+10*time.Minute, updateEvent(6)) // the event the workflow was missing
	h.deliver(4*time.Hour+20*time.Minute, transitionEvent(7, "review", "draft"))
	h.runToCompletion()

	if len(h.fires) != 2 {
		t.Fatalf("FireTimer calls = %+v, want two", h.fires)
	}
	if h.fires[0].In.ExpectedItemSeq != 5 || h.fires[1].In.ExpectedItemSeq != 6 {
		t.Errorf("expected seqs = %d then %d, want 5 then 6",
			h.fires[0].In.ExpectedItemSeq, h.fires[1].In.ExpectedItemSeq)
	}
	k1, k2 := h.fires[0].In.IdempotencyKey, h.fires[1].In.IdempotencyKey
	if k1 != "timer:"+itemID+":5:5" || k2 != "timer:"+itemID+":5:6" {
		t.Errorf("idempotency keys = %q then %q; want ...:5:5 then ...:5:6", k1, k2)
	}
	if !h.fires[1].At.Equal(t0.Add(4*time.Hour + 10*time.Minute)) {
		t.Errorf("second FireTimer at %v, want as soon as the update arrived", h.fires[1].At)
	}
}

// If the missing event was the Item leaving the state, the timer is simply over.
func TestItemWorkflow_StaleThenAStateChange_DoesNotFireAgain(t *testing.T) {
	h := newHarness(t, snapshot(t, "review", 5, 5, t0))
	h.fireErrs = []error{stale()}
	h.deliver(4*time.Hour+10*time.Minute, transitionEvent(6, "review", "published"))
	h.runToCompletion()

	if len(h.fires) != 1 {
		t.Errorf("FireTimer calls = %d, want 1", len(h.fires))
	}
}

// --- the Item is gone ---

// The Item no longer exists when the run starts. There is nothing to follow,
// so the load is not retried and the workflow completes. The event that
// started the run is answered, not applied, so the relay can move on.
func TestItemWorkflow_ItemGoneAtLoad_Completes(t *testing.T) {
	h := newHarness(t, wf.Snapshot{})
	h.loadErr = notFound()
	trigger := h.deliver(0, transitionEvent(2, "draft", "review"))
	h.runToCompletion()

	h.wantApplied("the event that started the run", trigger, false)
	if h.loads != 1 {
		t.Errorf("LoadItem calls = %d, want 1: not found is not retried", h.loads)
	}
	if len(h.fires) != 0 {
		t.Errorf("FireTimer calls = %+v, want none", h.fires)
	}
	if now := h.env.Now(); !now.Equal(t0) {
		t.Errorf("workflow ended at %v, want at the start", now)
	}
}

// The Item disappears while its timer runs. The timer's write finds nothing;
// like a rejection, that is final: no retry, and the workflow does not fail.
func TestItemWorkflow_ItemGoneWhenTheTimerFires_IsNotRetried(t *testing.T) {
	h := newHarness(t, snapshot(t, "review", 5, 5, t0))
	h.fireErrs = []error{notFound()}
	later := h.deliver(9*time.Hour, transitionEvent(6, "review", "published"))
	h.runToCompletion()

	if len(h.fires) != 1 {
		t.Errorf("FireTimer calls = %d, want 1: not found is not retried", len(h.fires))
	}
	h.wantApplied("later transition", later, true)
}

// --- duplicates and gaps ---

// An event at or below lastAppliedSeq is accepted and ignored; it must not
// restart the timer.
func TestItemWorkflow_Duplicate_IsAcceptedAndIgnored(t *testing.T) {
	h := newHarness(t, snapshot(t, "review", 5, 5, t0))
	old := h.deliver(time.Minute, transitionEvent(5, "draft", "review")) // already loaded
	first := h.deliver(time.Hour, updateEvent(6))
	again := h.deliver(2*time.Hour, updateEvent(6))
	h.deliver(4*time.Hour+time.Minute, transitionEvent(7, "review", "draft"))
	h.runToCompletion()

	h.wantApplied("event at the watermark", old, false)
	h.wantApplied("first delivery of 6", first, true)
	h.wantApplied("second delivery of 6", again, false)
	if len(h.fires) != 1 || !h.fires[0].At.Equal(t0.Add(4*time.Hour)) || h.fires[0].In.ExpectedItemSeq != 6 {
		t.Errorf("FireTimer calls = %+v, want one at +4h with expected seq 6", h.fires)
	}
}

// An event that skips a number is refused by the update's validator, so the
// relay retries from the lowest undelivered one. A validator's refusal is
// never written to the workflow's history, so retries do not grow it.
// Nothing is applied out of order.
func TestItemWorkflow_Gap_IsRejectedUntilTheMissingEventArrives(t *testing.T) {
	h := newHarness(t, snapshot(t, "review", 5, 5, t0))
	early := h.deliver(time.Hour, transitionEvent(7, "review", "published")) // 6 is missing
	six := h.deliver(2*time.Hour, updateEvent(6))
	seven := h.deliver(3*time.Hour, transitionEvent(7, "review", "published"))
	h.runToCompletion()

	if early.rejected == nil || early.done {
		t.Errorf("event 7 before 6: rejected %v, handler ran %v; want the validator to refuse it",
			early.rejected, early.done)
	}
	h.wantApplied("event 6", six, true)
	h.wantApplied("event 7, redelivered", seven, true)
	if now := h.env.Now(); now.Before(t0.Add(3 * time.Hour)) {
		t.Errorf("workflow ended at %v: the out-of-order event was applied", now)
	}
}

// --- starting mid-history ---

// The relay may lag: the workflow is started for event 6 (entering review),
// but by the time it loads the Item, the Item is at 9 and back in review since
// event 8. The loaded state wins: the timer counts from event 8, and events
// 6 to 9, still on their way, are ignored.
func TestItemWorkflow_StartsWhileTheRelayLags_UsesTheLoadedState(t *testing.T) {
	h := newHarness(t, snapshot(t, "review", 9, 8, t0.Add(-time.Hour)))
	trigger := h.deliver(0, transitionEvent(6, "draft", "review")) // arrives with the start
	seven := h.deliver(time.Minute, transitionEvent(7, "review", "draft"))
	h.deliver(3*time.Hour+time.Minute, transitionEvent(10, "review", "draft"))
	h.runToCompletion()

	h.wantApplied("the event that started the run", trigger, false)
	h.wantApplied("event 7", seven, false)
	if len(h.fires) != 1 {
		t.Fatalf("FireTimer calls = %+v, want exactly one", h.fires)
	}
	got := h.fires[0]
	if !got.At.Equal(t0.Add(3*time.Hour)) || got.In.EnteredSeq != 8 || got.In.ExpectedItemSeq != 9 {
		t.Errorf("FireTimer call = %+v, want +3h, entered seq 8, expected seq 9", got)
	}
}

// Updates that arrive before the load has finished are let in by the
// validator (it has nothing to compare with yet), but the handler waits for
// the load. So each is judged against the loaded item_seq, never against an
// empty state: at or below it is ignored, the next one is applied, and one
// further ahead fails, to be retried by the relay.
func TestItemWorkflow_UpdatesBeforeTheLoadFinishes_WaitForIt(t *testing.T) {
	h := newHarness(t, snapshot(t, "review", 5, 5, t0))
	h.loadGate = make(chan struct{})
	old := h.deliver(0, updateEvent(5))
	next := h.deliver(0, updateEvent(6))
	ahead := h.deliver(0, updateEvent(9))
	// Let the load finish only once all three have been accepted.
	h.env.RegisterDelayedCallback(func() { close(h.loadGate) }, time.Millisecond)
	h.deliver(time.Hour, transitionEvent(7, "review", "published"))
	h.runToCompletion()

	for name, out := range map[string]*outcome{"event 5": old, "event 6": next, "event 9": ahead} {
		if !out.acceptedBeforeLoad {
			t.Errorf("%s was not accepted while the load was running; the test did not exercise the early path", name)
		}
	}
	h.wantApplied("event 5, at the loaded item_seq", old, false)
	h.wantApplied("event 6, the next one", next, true)
	if ahead.rejected != nil || !ahead.done || ahead.err == nil {
		t.Errorf("event 9: rejected %v, done %v, error %v; want it accepted, then failed as a gap",
			ahead.rejected, ahead.done, ahead.err)
	}
}

// Same lag, but the Item has already left the timer state: there is nothing
// to do, so the run completes without firing anything.
func TestItemWorkflow_StartsAfterTheItemMovedOn_CompletesAtOnce(t *testing.T) {
	h := newHarness(t, snapshot(t, "published", 9, 9, t0.Add(-time.Hour)))
	trigger := h.deliver(0, transitionEvent(6, "draft", "review"))
	h.runToCompletion()

	h.wantApplied("the event that started the run", trigger, false)
	if len(h.fires) != 0 {
		t.Errorf("FireTimer calls = %+v, want none", h.fires)
	}
	if now := h.env.Now(); !now.Equal(t0) {
		t.Errorf("workflow ended at %v, want at the start", now)
	}
}

// --- continue-as-new ---

// When the server suggests it, the run hands over to a new one and passes on
// what it knows: lastAppliedSeq, the state, when that state was entered, and
// the timer table, so the new run needs neither Postgres nor the Type.
func TestItemWorkflow_ContinueAsNew_CarriesItsState(t *testing.T) {
	loaded := snapshot(t, "review", 5, 5, t0)
	h := newHarness(t, loaded)
	h.env.RegisterDelayedCallback(func() { h.env.SetContinueAsNewSuggested(true) }, 30*time.Minute)
	out := h.deliver(time.Hour, updateEvent(6))

	err := h.run(wf.Input{ItemID: itemID})
	// ContinueAsNewError is how a Temporal workflow says "restart me with
	// this input"; it is a result, not a failure.
	var next *sdk.ContinueAsNewError
	if !errors.As(err, &next) {
		t.Fatalf("workflow ended with %v, want continue-as-new", err)
	}
	h.wantApplied("event 6", out, true)

	var in wf.Input
	if err := converter.GetDefaultDataConverter().FromPayloads(next.Input, &in); err != nil {
		t.Fatalf("decode the next run's input: %v", err)
	}
	want := loaded
	want.LastAppliedSeq = 6
	if in.ItemID != itemID || in.Carried == nil {
		t.Fatalf("next run's input = %+v, want the item ID and a carried snapshot", in)
	}
	got := *in.Carried
	if got.State != want.State || got.LastAppliedSeq != 6 || got.EnteredSeq != 5 ||
		!got.EnteredAt.Equal(want.EnteredAt) || got.CorrelationID != "corr-1" || got.Hop != 2 ||
		got.TypeID != "doc" || got.TypeVersion != 1 || !reflect.DeepEqual(got.Timers, docTimers()) {
		t.Errorf("carried snapshot = %+v\nwant %+v", got, want)
	}
	if len(h.fires) != 0 {
		t.Errorf("FireTimer calls = %+v, want none before the handover", h.fires)
	}
}

// A run that was handed a snapshot does not load from Postgres: it keeps the
// old run's lastAppliedSeq and the old deadline.
func TestItemWorkflow_StartedWithCarriedState_ContinuesTheTimer(t *testing.T) {
	carried := snapshot(t, "review", 6, 5, t0.Add(-3*time.Hour)) // one hour of the 4h is left
	h := newHarness(t, wf.Snapshot{})
	dup := h.deliver(time.Minute, updateEvent(6))
	h.deliver(time.Hour+time.Minute, transitionEvent(7, "review", "draft"))

	if err := h.run(wf.Input{ItemID: itemID, Carried: &carried}); err != nil {
		t.Fatalf("workflow error: %v", err)
	}
	if h.loads != 0 {
		t.Errorf("LoadItem calls = %d, want 0", h.loads)
	}
	h.wantApplied("event 6, already applied by the old run", dup, false)
	if len(h.fires) != 1 || !h.fires[0].At.Equal(t0.Add(time.Hour)) || h.fires[0].In.ExpectedItemSeq != 6 {
		t.Errorf("FireTimer calls = %+v, want one at +1h with expected seq 6", h.fires)
	}
}
