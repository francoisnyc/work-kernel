// Tests for Jira part 2, step 1: a timer at a field's instant, in the
// workflow (docs/design.md, "Decisions for Jira part 2", primitive 2).
//
// A state may name a datetime field: when the instant it holds passes while
// the Item is in the state, the workflow makes the state's move through
// FireTimer, as for a duration timer.
//
//   - The instant is the one field value the workflow sees. It reads it
//     with LoadInstant: on entering such a state (or starting in one), and
//     again after every event that names the field (Event.FieldIDs). No
//     other event reads anything.
//   - Unset arms nothing, but the run stays: it must hear the field being
//     set. Past fires at once. A change cancels the old timer and arms the
//     new instant in the same run; a clear disarms.
//   - A hold defers it like any state timer.
//   - The item service may answer NotYet (its clock is behind): the timer
//     arms again for the remainder it names.
//   - A Type without such a state never calls LoadInstant: every other
//     test's harness has no such activity, and would fail.
package workflow_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/temporal"
	sdk "go.temporal.io/sdk/workflow"

	wf "github.com/francoisnyc/work-kernel/internal/workflow"
)

// sprintSnapshot: an iteration whose `active` state closes at f_end_at.
// It can be asked to review (a held move, checked by `check`), so a test
// can open a hold on it.
func sprintSnapshot(state string, itemSeq, enteredSeq int64) wf.Snapshot {
	return wf.Snapshot{
		TypeID: "iteration", TypeVersion: 1, Timers: map[string]wf.TimerSpec{},
		At:         map[string]wf.AtSpec{"active": {Field: "f_end_at", To: "closed"}},
		Checks:     map[string]string{"active->review": "check"},
		HookBudget: budget,
		State:      state, LastAppliedSeq: itemSeq,
		EnteredSeq: enteredSeq, EnteredAt: t0,
		CorrelationID: "corr-1", Hop: 2,
	}
}

type instantCall struct {
	ItemID string
	At     time.Time
}

// atHarness is the hook harness plus a fake LoadInstant, which answers
// with whatever instant the test has set "in the field" by then.
type atHarness struct {
	*hookHarness
	instant *time.Time // the field's value now; nil is unset
	reads   []instantCall
}

func newAtHarness(t *testing.T, loaded wf.Snapshot, instant *time.Time) *atHarness {
	t.Helper()
	h := &atHarness{hookHarness: newHookHarness(t, loaded), instant: instant}
	h.env.RegisterActivityWithOptions(func(ctx context.Context, id string) (wf.Instant, error) {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.reads = append(h.reads, instantCall{ItemID: id, At: h.env.Now()})
		if h.instant == nil {
			return wf.Instant{}, nil
		}
		return wf.Instant{Set: true, At: *h.instant}, nil
	}, activity.RegisterOptions{Name: wf.ActivityLoadInstant})
	return h
}

func at(d time.Duration) *time.Time {
	v := t0.Add(d)
	return &v
}

// setField changes the field `after` the start and delivers the update
// event that names it, as the relay would: the value is in Postgres before
// the event reaches the workflow.
func (h *atHarness) setField(after time.Duration, seq int64, instant *time.Time) *outcome {
	h.t.Helper()
	h.env.RegisterDelayedCallback(func() {
		h.mu.Lock()
		h.instant = instant
		h.mu.Unlock()
	}, after-time.Millisecond)
	ev := updateEvent(seq)
	ev.FieldIDs = []string{"f_end_at"}
	return h.deliver(after, ev)
}

func closedEvent(seq int64) wf.Event { return transitionEvent(seq, "active", "closed") }

func notYet(remaining time.Duration) error {
	return temporal.NewNonRetryableApplicationError("not yet", wf.ErrTypeNotYet, nil, remaining)
}

// --- arming ---

// Started in the state: the instant is read once, and the move is made
// when it passes, with a duration timer's request and key.
func TestAt_FiresAtTheInstant(t *testing.T) {
	h := newAtHarness(t, sprintSnapshot("active", 5, 5), at(3*time.Hour))
	back := h.deliver(3*time.Hour+time.Minute, closedEvent(6)) // the timer's own write
	h.runToCompletion()

	if len(h.reads) != 1 || h.reads[0].ItemID != itemID || !h.reads[0].At.Equal(t0) {
		t.Fatalf("LoadInstant calls = %+v, want one, at the start, for the Item", h.reads)
	}
	if len(h.fires) != 1 {
		t.Fatalf("FireTimer calls = %+v, want exactly one", h.fires)
	}
	want := wf.FireTimerInput{
		ItemID: itemID, TypeID: "iteration", TypeVersion: 1,
		FromState: "active", ToState: "closed",
		ExpectedItemSeq: 5, EnteredSeq: 5,
		CorrelationID: "corr-1", Hop: 3,
		IdempotencyKey: "timer:" + itemID + ":5:5",
	}
	if got := h.fires[0]; got.In != want || !got.At.Equal(t0.Add(3*time.Hour)) {
		t.Errorf("FireTimer = %+v at %v\nwant %+v at +3h", got.In, got.At, want)
	}
	h.wantApplied("the timer's transition", back, true)
}

// Entering the state by an event reads the instant then. The entering
// event's time does not matter: an instant is not counted from it. (The
// run starts in `waiting`, a state with a far-off duration timer: a run in
// a state without behavior would complete at once, as it must.)
func TestAt_EnteringTheStateReadsTheInstant(t *testing.T) {
	snap := sprintSnapshot("waiting", 5, 5)
	snap.Timers = map[string]wf.TimerSpec{"waiting": {After: 1000 * time.Hour, To: "planned"}}
	h := newAtHarness(t, snap, at(10*time.Hour))
	h.deliver(time.Hour, transitionEvent(6, "waiting", "active"))
	h.deliver(10*time.Hour+time.Minute, closedEvent(7))
	h.runToCompletion()

	if len(h.reads) != 1 || !h.reads[0].At.Equal(t0.Add(time.Hour)) {
		t.Fatalf("LoadInstant calls = %+v, want one, on entering the state (+1h)", h.reads)
	}
	if len(h.fires) != 1 || !h.fires[0].At.Equal(t0.Add(10*time.Hour)) ||
		h.fires[0].In.EnteredSeq != 6 || h.fires[0].In.ExpectedItemSeq != 6 {
		t.Errorf("FireTimer calls = %+v, want one at +10h, entered and expected seq 6", h.fires)
	}
}

// An instant already past when the state is entered fires at once.
func TestAt_APastInstantFiresAtOnce(t *testing.T) {
	h := newAtHarness(t, sprintSnapshot("active", 5, 5), at(-48*time.Hour))
	h.deliver(time.Minute, closedEvent(6))
	h.runToCompletion()
	if len(h.fires) != 1 || !h.fires[0].At.Equal(t0) {
		t.Errorf("FireTimer calls = %+v, want one, at the start", h.fires)
	}
}

// Unset arms nothing, and the run does not complete: it is still there,
// days later, to take the event that leaves the state.
func TestAt_UnsetArmsNothingAndTheRunStays(t *testing.T) {
	h := newAtHarness(t, sprintSnapshot("active", 5, 5), nil)
	left := h.deliver(72*time.Hour, transitionEvent(6, "active", "planned"))
	h.runToCompletion()

	if len(h.fires) != 0 {
		t.Errorf("FireTimer calls = %+v, want none", h.fires)
	}
	h.wantApplied("leaving the state three days later", left, true)
	if now := h.env.Now(); now.Before(t0.Add(72 * time.Hour)) {
		t.Errorf("the run ended at %v: with the field unset it must stay, to hear it being set", now)
	}
}

// Leaving the state cancels the timer.
func TestAt_LeavingTheStateCancels(t *testing.T) {
	h := newAtHarness(t, sprintSnapshot("active", 5, 5), at(3*time.Hour))
	h.deliver(time.Hour, transitionEvent(6, "active", "planned"))
	h.runToCompletion()
	if len(h.fires) != 0 {
		t.Errorf("FireTimer calls = %+v, want none", h.fires)
	}
	if now := h.env.Now(); now.After(t0.Add(2 * time.Hour)) {
		t.Errorf("the run ended at %v: it waited for a canceled timer", now)
	}
}

// --- re-arming ---

// The end is moved later: the old timer is canceled (nothing happens at the
// old instant), the new one fires, in the same run.
func TestAt_MovedLater_Rearms(t *testing.T) {
	h := newAtHarness(t, sprintSnapshot("active", 5, 5), at(3*time.Hour))
	moved := h.setField(time.Hour, 6, at(8*time.Hour))
	h.deliver(8*time.Hour+time.Minute, closedEvent(7))
	h.runToCompletion()

	h.wantApplied("the update that moves the end", moved, true)
	if len(h.reads) != 2 || !h.reads[1].At.Equal(t0.Add(time.Hour)) {
		t.Fatalf("LoadInstant calls = %+v, want two: at the start and at the update", h.reads)
	}
	if len(h.fires) != 1 {
		t.Fatalf("FireTimer calls = %+v, want exactly one (none at the old instant)", h.fires)
	}
	if got := h.fires[0]; !got.At.Equal(t0.Add(8*time.Hour)) || got.In.ExpectedItemSeq != 6 || got.In.EnteredSeq != 5 ||
		got.In.IdempotencyKey != "timer:"+itemID+":5:6" {
		t.Errorf("FireTimer = %+v at %v; want +8h, entered seq 5, expected seq 6", got.In, got.At)
	}
	if h.loads != 1 {
		t.Errorf("LoadItem calls = %d, want 1: the same run", h.loads)
	}
}

// Moved earlier, and moved into the past: sooner, and at once.
func TestAt_MovedEarlier_Rearms(t *testing.T) {
	h := newAtHarness(t, sprintSnapshot("active", 5, 5), at(8*time.Hour))
	h.setField(time.Hour, 6, at(2*time.Hour))
	h.deliver(2*time.Hour+time.Minute, closedEvent(7))
	h.runToCompletion()
	if len(h.fires) != 1 || !h.fires[0].At.Equal(t0.Add(2*time.Hour)) {
		t.Errorf("FireTimer calls = %+v, want one at +2h", h.fires)
	}

	h = newAtHarness(t, sprintSnapshot("active", 5, 5), at(8*time.Hour))
	h.setField(time.Hour, 6, at(-time.Hour))
	h.deliver(time.Hour+time.Minute, closedEvent(7))
	h.runToCompletion()
	if len(h.fires) != 1 || !h.fires[0].At.Equal(t0.Add(time.Hour)) {
		t.Errorf("moved into the past: FireTimer calls = %+v, want one at the update (+1h)", h.fires)
	}
}

// Set after the state was entered without one, and cleared again: armed,
// then disarmed.
func TestAt_SetLaterAndCleared(t *testing.T) {
	h := newAtHarness(t, sprintSnapshot("active", 5, 5), nil)
	h.setField(time.Hour, 6, at(5*time.Hour))
	h.deliver(5*time.Hour+time.Minute, closedEvent(7))
	h.runToCompletion()
	if len(h.fires) != 1 || !h.fires[0].At.Equal(t0.Add(5*time.Hour)) {
		t.Errorf("set later: FireTimer calls = %+v, want one at +5h", h.fires)
	}

	h = newAtHarness(t, sprintSnapshot("active", 5, 5), at(3*time.Hour))
	h.setField(time.Hour, 6, nil)
	left := h.deliver(24*time.Hour, transitionEvent(7, "active", "planned"))
	h.runToCompletion()
	if len(h.fires) != 0 {
		t.Errorf("cleared: FireTimer calls = %+v, want none", h.fires)
	}
	h.wantApplied("leaving a day after the clear", left, true)
}

// An event that does not name the field reads nothing and keeps the timer;
// the timer's write names the Item's latest item_seq.
func TestAt_OtherEventsDoNotReadTheField(t *testing.T) {
	h := newAtHarness(t, sprintSnapshot("active", 5, 5), at(3*time.Hour))
	other := updateEvent(6)
	other.FieldIDs = []string{"f_started_at"} // another field some state names in `at`
	h.deliver(time.Hour, other)
	h.deliver(2*time.Hour, updateEvent(7)) // no field at all (a policy change)
	h.deliver(3*time.Hour+time.Minute, closedEvent(8))
	h.runToCompletion()

	if len(h.reads) != 1 {
		t.Errorf("LoadInstant calls = %+v, want only the first: no later event names f_end_at", h.reads)
	}
	if len(h.fires) != 1 || !h.fires[0].At.Equal(t0.Add(3*time.Hour)) || h.fires[0].In.ExpectedItemSeq != 7 {
		t.Errorf("FireTimer calls = %+v, want one at +3h with expected seq 7", h.fires)
	}
}

// --- the item service's answers ---

// NotYet: the item service's clock is behind. The timer arms again for the
// remainder it names, and fires again, under the same key.
func TestAt_NotYet_ArmsAgainForTheRemainder(t *testing.T) {
	h := newAtHarness(t, sprintSnapshot("active", 5, 5), at(3*time.Hour))
	h.fireErrs = []error{notYet(250 * time.Millisecond), notYet(40 * time.Millisecond)}
	h.deliver(3*time.Hour+time.Minute, closedEvent(6))
	h.runToCompletion()

	if len(h.fires) != 3 {
		t.Fatalf("FireTimer calls = %d, want 3: not yet, not yet, done", len(h.fires))
	}
	due := t0.Add(3 * time.Hour)
	for i, want := range []time.Time{due, due.Add(250 * time.Millisecond), due.Add(290 * time.Millisecond)} {
		if !h.fires[i].At.Equal(want) {
			t.Errorf("firing %d at %v, want %v", i+1, h.fires[i].At, want)
		}
		if h.fires[i].In.IdempotencyKey != h.fires[0].In.IdempotencyKey {
			t.Errorf("firing %d uses key %q, want the first firing's: nothing was written", i+1, h.fires[i].In.IdempotencyKey)
		}
	}
	if len(h.reads) != 1 {
		t.Errorf("LoadInstant calls = %+v, want one: not yet names the remainder itself", h.reads)
	}
}

// While it waits for the remainder the end is moved: the new instant wins.
func TestAt_NotYet_ThenMoved(t *testing.T) {
	h := newAtHarness(t, sprintSnapshot("active", 5, 5), at(3*time.Hour))
	h.fireErrs = []error{notYet(10 * time.Minute)}
	h.setField(3*time.Hour+time.Minute, 6, at(6*time.Hour))
	h.deliver(6*time.Hour+time.Minute, closedEvent(7))
	h.runToCompletion()
	if len(h.fires) != 2 || !h.fires[1].At.Equal(t0.Add(6*time.Hour)) {
		t.Errorf("FireTimer calls = %+v, want two: at +3h (not yet) and at the new instant, +6h", h.fires)
	}
}

// Rejected (a guard says no): the timer is spent, as for a duration timer,
// until the field changes, which arms it again.
func TestAt_Rejected_IsSpentUntilTheFieldChanges(t *testing.T) {
	h := newAtHarness(t, sprintSnapshot("active", 5, 5), at(3*time.Hour))
	h.fireErrs = []error{rejected()}
	h.setField(5*time.Hour, 6, at(7*time.Hour))
	h.deliver(7*time.Hour+time.Minute, closedEvent(7))
	h.runToCompletion()
	if len(h.fires) != 2 || !h.fires[0].At.Equal(t0.Add(3*time.Hour)) || !h.fires[1].At.Equal(t0.Add(7*time.Hour)) {
		t.Errorf("FireTimer calls = %+v, want two: +3h (rejected) and +7h (armed again)", h.fires)
	}
}

// Stale: an event is on its way. It moves the end: the instant is read
// again and the timer waits for the new one, instead of firing at once.
func TestAt_Stale_ThenTheEventThatMovedTheEnd(t *testing.T) {
	h := newAtHarness(t, sprintSnapshot("active", 5, 5), at(3*time.Hour))
	h.fireErrs = []error{stale()}
	h.setField(3*time.Hour+time.Second, 6, at(9*time.Hour))
	h.deliver(9*time.Hour+time.Minute, closedEvent(7))
	h.runToCompletion()
	if len(h.fires) != 2 || !h.fires[1].At.Equal(t0.Add(9*time.Hour)) || h.fires[1].In.ExpectedItemSeq != 6 {
		t.Errorf("FireTimer calls = %+v, want two: +3h (stale) and +9h with expected seq 6", h.fires)
	}
}

// --- holds ---

// Due while a hold is open: it waits for the hold to close, then looks
// again. Still in the state: it fires at once.
func TestAt_DueDuringAHold_FiresOnceTheHoldCloses(t *testing.T) {
	h := newAtHarness(t, sprintSnapshot("active", 5, 5), at(3*time.Hour))
	h.deliver(2*time.Hour+55*time.Minute, wf.Event{Seq: 6, Op: "transition_requested", From: "active",
		RequestedTo: "review", HoldID: holdID, CorrelationID: "corr-7"})
	h.deliver(3*time.Hour+5*time.Minute, wf.Event{Seq: 7, Op: "transition_denied", From: "active", HoldID: holdID})
	h.deliver(3*time.Hour+6*time.Minute, closedEvent(8))
	h.runToCompletion()
	if len(h.fires) != 1 || !h.fires[0].At.Equal(t0.Add(3*time.Hour+5*time.Minute)) || h.fires[0].In.ExpectedItemSeq != 7 {
		t.Errorf("FireTimer calls = %+v, want one, when the hold closed (+3h05), expected seq 7", h.fires)
	}
}

// The hold allows the move out of the state: the timer is gone.
func TestAt_DueDuringAHold_NothingOnceLeft(t *testing.T) {
	h := newAtHarness(t, sprintSnapshot("active", 5, 5), at(3*time.Hour))
	h.deliver(2*time.Hour+55*time.Minute, wf.Event{Seq: 6, Op: "transition_requested", From: "active",
		RequestedTo: "review", HoldID: holdID, CorrelationID: "corr-7"})
	h.deliver(3*time.Hour+5*time.Minute, wf.Event{Seq: 7, Op: "transition", From: "active", To: "review", HoldID: holdID})
	h.runToCompletion()
	if len(h.fires) != 0 {
		t.Errorf("FireTimer calls = %+v, want none: the Item left the state", h.fires)
	}
}

// --- continue-as-new ---

// A run that continues as new carries the instant it read; the next run
// reads nothing and keeps the deadline.
func TestAt_ContinueAsNew_CarriesTheInstant(t *testing.T) {
	h := newAtHarness(t, sprintSnapshot("active", 5, 5), at(3*time.Hour))
	h.env.RegisterDelayedCallback(func() { h.env.SetContinueAsNewSuggested(true) }, 30*time.Minute)
	h.deliver(time.Hour, updateEvent(6))
	err := h.run(wf.Input{ItemID: itemID})
	var next *sdk.ContinueAsNewError
	if !errors.As(err, &next) {
		t.Fatalf("workflow ended with %v, want continue-as-new", err)
	}
	var in wf.Input
	if err := converter.GetDefaultDataConverter().FromPayloads(next.Input, &in); err != nil {
		t.Fatalf("decode the next run's input: %v", err)
	}
	c := in.Carried
	if c == nil || !c.AtLoaded || c.AtInstant == nil || !c.AtInstant.Equal(t0.Add(3*time.Hour)) ||
		c.At["active"] != (wf.AtSpec{Field: "f_end_at", To: "closed"}) {
		t.Fatalf("carried snapshot = %+v, want the at table and the instant read", c)
	}

	h2 := newAtHarness(t, wf.Snapshot{}, at(99*time.Hour)) // would be wrong to read
	h2.deliver(3*time.Hour+time.Minute, closedEvent(7))
	if err := h2.run(wf.Input{ItemID: itemID, Carried: c}); err != nil {
		t.Fatalf("the next run: %v", err)
	}
	if h2.loads != 0 || len(h2.reads) != 0 {
		t.Errorf("the next run loaded %d times and read the instant %d times, want neither", h2.loads, len(h2.reads))
	}
	if len(h2.fires) != 1 || !h2.fires[0].At.Equal(t0.Add(3*time.Hour)) {
		t.Errorf("FireTimer calls = %+v, want one at +3h", h2.fires)
	}
}

// A carried snapshot of an unset field stays unset without a read.
func TestAt_ContinueAsNew_CarriesUnset(t *testing.T) {
	carried := sprintSnapshot("active", 6, 5)
	carried.AtLoaded = true
	h := newAtHarness(t, wf.Snapshot{}, at(time.Hour))
	h.deliver(24*time.Hour, transitionEvent(7, "active", "planned"))
	if err := h.run(wf.Input{ItemID: itemID, Carried: &carried}); err != nil {
		t.Fatalf("workflow error: %v", err)
	}
	if len(h.reads) != 0 || len(h.fires) != 0 {
		t.Errorf("read %d times, fired %d times; want neither: unset was carried", len(h.reads), len(h.fires))
	}
}
