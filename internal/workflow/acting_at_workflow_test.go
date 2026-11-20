// Tests for Jira part 2, step 3a: an acting timer at an instant, in the
// workflow (docs/design.md, "Decisions for Jira part 2", primitive 2; the
// Phase A answers of step 3, item 5).
//
// A state may have acting timers that act when the instant a datetime
// field holds passes, WITHOUT a move: the item service sets fields
// (ActAt), then the timer's action hook runs.
//
//   - The instants are read with LoadActingInstants: on entering such a
//     state (or starting in one), and again after an event that names one
//     of the fields (Event.FieldIDs). No other event reads anything.
//   - Unset arms nothing, but the run stays. Past acts at once. A changed
//     instant cancels the wait and arms the new one: the timer acts again
//     for the new instant, never twice for the same one in one entry.
//   - Leaving the state disarms. A new entry arms again.
//   - It does not wait for a hold, like every acting timer.
//   - NotYet (the item service's clock is behind): armed again for the
//     remainder. Stale: looked at again after the next event.
//   - The hook runs only when ActAt says so, keyed by the instant.
//   - A Type without such a timer never calls either activity: every
//     other test's harness lacks them, and would fail.
package workflow_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"

	wf "github.com/francoisnyc/work-kernel/internal/workflow"
)

// meterSnapshot: a meter whose `running` state marks it late at f_due_at
// and then tells someone (the hook tell_late). `paused` has no behavior of
// its own; keptIn gives it a far-off timer where a test needs the run kept.
func meterSnapshot(state string, itemSeq, enteredSeq int64) wf.Snapshot {
	return wf.Snapshot{
		TypeID: "meter", TypeVersion: 1, Timers: map[string]wf.TimerSpec{},
		ActingAt:   map[string][]wf.ActingAtSpec{"running": {{ID: "late", Field: "f_due_at", Hook: "tell_late"}}},
		HookBudget: budget,
		State:      state, LastAppliedSeq: itemSeq,
		EnteredSeq: enteredSeq, EnteredAt: t0,
		CorrelationID: "corr-1", Hop: 2,
	}
}

type actCall struct {
	In wf.ActAtInput
	At time.Time
}

// actHarness is the mail harness (RunAction) plus fakes for the two new
// activities. instants holds what "the fields hold" now, per timer.
type actHarness struct {
	*mailHarness
	instants map[string]*time.Time
	reads    []time.Time
	acts     []actCall
	// actErrs are returned by successive ActAt calls; nil when used up.
	actErrs []error
	// noHook makes ActAt answer that the hook is not to run.
	noHook bool
}

func newActHarness(t *testing.T, loaded wf.Snapshot, due *time.Time) *actHarness {
	t.Helper()
	h := &actHarness{mailHarness: newMailHarness(t, loaded), instants: map[string]*time.Time{"late": due}}
	h.env.RegisterActivityWithOptions(func(ctx context.Context, id string) ([]wf.ActingInstant, error) {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.reads = append(h.reads, h.env.Now())
		var out []wf.ActingInstant
		for _, timer := range []string{"late", "begun"} {
			at, known := h.instants[timer]
			switch {
			case !known:
			case at == nil:
				out = append(out, wf.ActingInstant{TimerID: timer})
			default:
				out = append(out, wf.ActingInstant{TimerID: timer, Set: true, At: *at})
			}
		}
		return out, nil
	}, activity.RegisterOptions{Name: wf.ActivityLoadActingInstants})
	h.env.RegisterActivityWithOptions(func(ctx context.Context, in wf.ActAtInput) (wf.ActAtResult, error) {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.acts = append(h.acts, actCall{In: in, At: h.env.Now()})
		if len(h.actErrs) > 0 {
			err := h.actErrs[0]
			h.actErrs = h.actErrs[1:]
			if err != nil {
				return wf.ActAtResult{}, err
			}
		}
		return wf.ActAtResult{RunHook: !h.noHook}, nil
	}, activity.RegisterOptions{Name: wf.ActivityActAt})
	return h
}

// setDue changes f_due_at `after` the start and delivers the event that
// names it, as the relay would.
func (h *actHarness) setDue(after time.Duration, seq int64, due *time.Time) *outcome {
	h.t.Helper()
	h.env.RegisterDelayedCallback(func() {
		h.mu.Lock()
		h.instants["late"] = due
		h.mu.Unlock()
	}, after-time.Millisecond)
	ev := updateEvent(seq)
	ev.FieldIDs = []string{"f_due_at"}
	return h.deliver(after, ev)
}

func stoppedEvent(seq int64) wf.Event { return transitionEvent(seq, "running", "stopped") }

func actInput(entered int64, instant time.Time) wf.ActAtInput {
	return wf.ActAtInput{
		ItemID: itemID, TypeID: "meter", TypeVersion: 1, State: "running", TimerID: "late",
		EnteredSeq: entered, Instant: instant, CorrelationID: "corr-1", Hop: 3,
	}
}

// tellInput is RunAction's input for the hook after the timer acted for
// one instant: keyed by the timer, the entry and the instant.
func tellInput(entered int64, instant time.Time) wf.HookInput {
	return wf.HookInput{
		ItemID: itemID, TypeID: "meter", TypeVersion: 1, HookID: "tell_late",
		CauseSeq: entered, CorrelationID: "corr-1", Hop: 3, Timer: "late",
		IdempotencyKey: fmt.Sprintf("timer:%s:late:%d:%s:hook", itemID, entered, instant.UTC().Format("2006-01-02T15:04:05.000000Z")),
	}
}

func (h *actHarness) wantActs(what string, want ...time.Time) {
	h.t.Helper()
	if len(h.acts) != len(want) {
		h.t.Fatalf("%s: ActAt calls = %+v, want %d", what, h.acts, len(want))
	}
	for i, at := range want {
		if !h.acts[i].At.Equal(at) {
			h.t.Errorf("%s: ActAt call %d at %v, want %v", what, i+1, h.acts[i].At, at)
		}
	}
}

// --- arming ---

// Started in the state: the instants are read once, the timer acts when
// its instant passes, with the instant it was armed for, and then its hook
// runs. The Item stays in the state and so does the run: its own write
// comes back as an update, and nothing acts twice.
func TestActingAt_ActsAtTheInstantThenRunsItsHook(t *testing.T) {
	due := t0.Add(3 * time.Hour)
	h := newActHarness(t, meterSnapshot("running", 5, 5), &due)
	own := h.deliver(3*time.Hour+time.Minute, updateEvent(6)) // the timer's own write (f_late_at)
	left := h.deliver(48*time.Hour, stoppedEvent(7))
	h.runToCompletion()

	if len(h.reads) != 1 || !h.reads[0].Equal(t0) {
		t.Fatalf("LoadActingInstants calls at %v, want one, at the start", h.reads)
	}
	h.wantActs("the due instant", due)
	if got := h.acts[0].In; got != actInput(5, due) {
		t.Errorf("ActAt = %+v\nwant %+v", got, actInput(5, due))
	}
	tells := h.runsOf("tell_late")
	if len(tells) != 1 || tells[0].In != tellInput(5, due) || !tells[0].At.Equal(due) {
		t.Errorf("RunAction calls = %+v, want one %+v at the instant", tells, tellInput(5, due))
	}
	if len(h.fires) != 0 {
		t.Errorf("FireTimer calls = %+v, want none: an acting timer makes no move", h.fires)
	}
	h.wantApplied("the timer's own write", own, true)
	h.wantApplied("leaving two days later", left, true)
}

// A past instant acts at once; an unset one arms nothing and the run stays
// to hear it being set.
func TestActingAt_PastActsAtOnce_UnsetArmsNothing(t *testing.T) {
	past := t0.Add(-48 * time.Hour)
	h := newActHarness(t, meterSnapshot("running", 5, 5), &past)
	h.deliver(time.Hour, stoppedEvent(6))
	h.runToCompletion()
	h.wantActs("a past instant", t0)
	if got := h.acts[0].In.Instant; !got.Equal(past) {
		t.Errorf("ActAt's instant = %v, want the field's %v, not the time it acts", got, past)
	}

	h = newActHarness(t, meterSnapshot("running", 5, 5), nil)
	left := h.deliver(72*time.Hour, stoppedEvent(6))
	h.runToCompletion()
	h.wantActs("an unset field")
	h.wantApplied("leaving three days later", left, true)
	if now := h.env.Now(); now.Before(t0.Add(72 * time.Hour)) {
		t.Errorf("the run ended at %v: with the field unset it must stay", now)
	}
}

// Entering the state by an event reads the instants then, and the cause is
// that event.
func TestActingAt_EnteringTheStateArms(t *testing.T) {
	due := t0.Add(10 * time.Hour)
	h := newActHarness(t, keptIn(meterSnapshot("paused", 5, 5), "paused"), &due)
	h.deliver(time.Hour, transitionEvent(6, "paused", "running"))
	h.deliver(11*time.Hour, stoppedEvent(7))
	h.runToCompletion()
	if len(h.reads) != 1 || !h.reads[0].Equal(t0.Add(time.Hour)) {
		t.Fatalf("LoadActingInstants calls at %v, want one, on entering running (+1h)", h.reads)
	}
	h.wantActs("after entering", due)
	if got := h.acts[0].In; got.EnteredSeq != 6 || got.Hop != 1 {
		t.Errorf("ActAt = %+v, want entered seq 6 and hop 1 (the entering event's hop + 1)", got)
	}
}

// keptIn gives states a far-off timer, so a run in one of them stays.
func keptIn(s wf.Snapshot, states ...string) wf.Snapshot {
	if s.Timers == nil {
		s.Timers = map[string]wf.TimerSpec{}
	}
	for _, st := range states {
		s.Timers[st] = wf.TimerSpec{After: 10000 * time.Hour, To: "stopped"}
	}
	return s
}

// Leaving the state disarms: nothing acts at the instant.
func TestActingAt_LeavingTheStateDisarms(t *testing.T) {
	due := t0.Add(3 * time.Hour)
	h := newActHarness(t, meterSnapshot("running", 5, 5), &due)
	h.deliver(time.Hour, stoppedEvent(6))
	h.runToCompletion()
	h.wantActs("after leaving")
	if now := h.env.Now(); now.After(t0.Add(2 * time.Hour)) {
		t.Errorf("the run ended at %v: it waited for a disarmed timer", now)
	}
}

// --- re-arming ---

// The instant is moved before it passes: nothing acts at the old one, the
// timer acts at the new one.
func TestActingAt_Moved_Rearms(t *testing.T) {
	due, later := t0.Add(3*time.Hour), t0.Add(8*time.Hour)
	h := newActHarness(t, meterSnapshot("running", 5, 5), &due)
	h.setDue(time.Hour, 6, &later)
	h.deliver(9*time.Hour, stoppedEvent(7))
	h.runToCompletion()
	if len(h.reads) != 2 || !h.reads[1].Equal(t0.Add(time.Hour)) {
		t.Fatalf("LoadActingInstants calls at %v, want two: at the start and at the update", h.reads)
	}
	h.wantActs("moved later", later)
	if got := h.acts[0].In; got != actInput(5, later) {
		t.Errorf("ActAt = %+v, want the new instant and the same entry", got)
	}
}

// After it acted, the instant changes: the timer acts again for the new
// instant, in the same entry. Cleared, it is disarmed.
func TestActingAt_ANewInstantActsAgain_AClearDisarms(t *testing.T) {
	due, next := t0.Add(3*time.Hour), t0.Add(6*time.Hour)
	h := newActHarness(t, meterSnapshot("running", 5, 5), &due)
	h.setDue(4*time.Hour, 6, &next)
	h.setDue(7*time.Hour, 7, nil)
	left := h.deliver(30*time.Hour, stoppedEvent(8))
	h.runToCompletion()
	h.wantActs("two instants, then cleared", due, next)
	if h.acts[1].In != actInput(5, next) {
		t.Errorf("the second ActAt = %+v, want %+v", h.acts[1].In, actInput(5, next))
	}
	if tells := h.runsOf("tell_late"); len(tells) != 2 || tells[1].In != tellInput(5, next) {
		t.Errorf("RunAction calls = %+v, want one per instant", tells)
	}
	h.wantApplied("leaving a day after the clear", left, true)
}

// The field is named again by an event, with the same instant (a write
// that set it to what it was): read again, but the same instant never acts
// twice in one entry.
func TestActingAt_TheSameInstantActsOncePerEntry(t *testing.T) {
	due := t0.Add(3 * time.Hour)
	h := newActHarness(t, meterSnapshot("running", 5, 5), &due)
	h.setDue(5*time.Hour, 6, &due)
	h.deliver(9*time.Hour, stoppedEvent(7))
	h.runToCompletion()
	if len(h.reads) != 2 {
		t.Errorf("LoadActingInstants calls at %v, want two", h.reads)
	}
	h.wantActs("the same instant named twice", due)
}

// An event that names no field of an acting timer reads nothing and keeps
// the wait.
func TestActingAt_OtherEventsDoNotReadTheFields(t *testing.T) {
	due := t0.Add(3 * time.Hour)
	h := newActHarness(t, meterSnapshot("running", 5, 5), &due)
	other := updateEvent(6)
	other.FieldIDs = []string{"f_other_at"}
	h.deliver(time.Hour, other)
	h.deliver(2*time.Hour, updateEvent(7))
	h.deliver(4*time.Hour, stoppedEvent(8))
	h.runToCompletion()
	if len(h.reads) != 1 {
		t.Errorf("LoadActingInstants calls at %v, want only the first", h.reads)
	}
	h.wantActs("other events in between", due)
}

// Left and entered again: armed again, for the new entry. Whether the
// timer then does anything is the item service's to say (its `when`).
func TestActingAt_ANewEntryArmsAgain(t *testing.T) {
	due := t0.Add(3 * time.Hour)
	h := newActHarness(t, keptIn(meterSnapshot("running", 5, 5), "paused"), &due)
	h.deliver(4*time.Hour, transitionEvent(6, "running", "paused"))
	h.deliver(5*time.Hour, transitionEvent(7, "paused", "running")) // the instant is in the past by now
	h.deliver(6*time.Hour, stoppedEvent(8))
	h.runToCompletion()
	h.wantActs("two entries", due, t0.Add(5*time.Hour))
	if got := h.acts[1].In; got.EnteredSeq != 7 || !got.Instant.Equal(due) || got.TimerID != "late" {
		t.Errorf("the second ActAt = %+v, want entered seq 7 and the same instant", got)
	}
}

// Two timers on one state wait apart.
func TestActingAt_TwoTimersOnOneState(t *testing.T) {
	due, begun := t0.Add(3*time.Hour), t0.Add(time.Hour)
	snap := meterSnapshot("running", 5, 5)
	snap.ActingAt["running"] = append(snap.ActingAt["running"], wf.ActingAtSpec{ID: "begun", Field: "f_started_at"})
	h := newActHarness(t, snap, &due)
	h.instants["begun"] = &begun
	h.deliver(4*time.Hour, stoppedEvent(6))
	h.runToCompletion()
	h.wantActs("two timers", begun, due)
	if h.acts[0].In.TimerID != "begun" || h.acts[1].In.TimerID != "late" {
		t.Errorf("ActAt calls = %+v, want begun then late", h.acts)
	}
	// begun declares no hook: only late's runs.
	if len(h.actions) != 1 || h.actions[0].In.HookID != "tell_late" {
		t.Errorf("RunAction calls = %+v, want tell_late alone", h.actions)
	}
}

// --- the item service's answers ---

// The timer did nothing (its condition did not hold): no hook.
func TestActingAt_NoHookWhenTheItemServiceSaysSo(t *testing.T) {
	due := t0.Add(3 * time.Hour)
	h := newActHarness(t, meterSnapshot("running", 5, 5), &due)
	h.noHook = true
	h.deliver(4*time.Hour, stoppedEvent(6))
	h.runToCompletion()
	h.wantActs("a condition that does not hold", due)
	if len(h.actions) != 0 {
		t.Errorf("RunAction calls = %+v, want none", h.actions)
	}
}

// NotYet: the item service's clock is behind. Armed again for the
// remainder, with the same request: nothing was written.
func TestActingAt_NotYet_ArmsAgainForTheRemainder(t *testing.T) {
	due := t0.Add(3 * time.Hour)
	h := newActHarness(t, meterSnapshot("running", 5, 5), &due)
	h.actErrs = []error{notYet(250 * time.Millisecond), notYet(40 * time.Millisecond)}
	h.deliver(4*time.Hour, stoppedEvent(6))
	h.runToCompletion()
	h.wantActs("not yet, not yet, done", due, due.Add(250*time.Millisecond), due.Add(290*time.Millisecond))
	for i, c := range h.acts {
		if c.In != actInput(5, due) {
			t.Errorf("ActAt call %d = %+v, want the same request each time", i+1, c.In)
		}
	}
	if len(h.reads) != 1 {
		t.Errorf("LoadActingInstants calls at %v, want one", h.reads)
	}
	if tells := h.runsOf("tell_late"); len(tells) != 1 || !tells[0].At.Equal(due.Add(290*time.Millisecond)) {
		t.Errorf("RunAction calls = %+v, want one, after the write", tells)
	}
}

// Stale: what the timer was armed for is gone, and an event is on its way.
// Nothing more until that event; then the instants are read again.
func TestActingAt_Stale_LooksAgainAfterTheNextEvent(t *testing.T) {
	due, moved := t0.Add(3*time.Hour), t0.Add(5*time.Hour)
	h := newActHarness(t, meterSnapshot("running", 5, 5), &due)
	h.actErrs = []error{stale()}
	h.setDue(3*time.Hour+time.Minute, 6, &moved) // the write that made it stale arrives
	h.deliver(6*time.Hour, stoppedEvent(7))
	h.runToCompletion()
	h.wantActs("stale, then the new instant", due, moved)
	if tells := h.runsOf("tell_late"); len(tells) != 1 || tells[0].In != tellInput(5, moved) {
		t.Errorf("RunAction calls = %+v, want one, for the instant that acted", tells)
	}
}

// Rejected (an effect that cannot be computed): spent for that instant,
// not retried; a new instant arms it again.
func TestActingAt_Rejected_IsSpentForThatInstant(t *testing.T) {
	due, next := t0.Add(3*time.Hour), t0.Add(7*time.Hour)
	h := newActHarness(t, meterSnapshot("running", 5, 5), &due)
	h.actErrs = []error{temporal.NewNonRetryableApplicationError("effect_failed", wf.ErrTypeRejected, nil)}
	h.setDue(5*time.Hour, 6, &next)
	h.deliver(8*time.Hour, stoppedEvent(7))
	h.runToCompletion()
	h.wantActs("rejected, then a new instant", due, next)
	if tells := h.runsOf("tell_late"); len(tells) != 1 || tells[0].In != tellInput(5, next) {
		t.Errorf("RunAction calls = %+v, want none for the rejected instant", tells)
	}
}

// --- holds, and runs that continue ---

// It does not wait for a hold: a mark at an instant during a hold is the
// point, as a reminder is.
func TestActingAt_DoesNotWaitForAHold(t *testing.T) {
	due := t0.Add(3 * time.Hour)
	snap := meterSnapshot("running", 5, 5)
	snap.Checks = map[string]string{"running->stopped": "check"}
	h := newActHarness(t, snap, &due)
	h.deliver(time.Hour, wf.Event{Seq: 6, Op: "transition_requested", From: "running", RequestedTo: "stopped", HoldID: holdID})
	h.deliver(4*time.Hour, wf.Event{Seq: 7, Op: "transition", From: "running", To: "stopped", HoldID: holdID})
	h.runToCompletion()
	h.wantActs("during the hold", due)
}

// A run that was handed a snapshot reads nothing again: it has the
// instants, and what already acted.
func TestActingAt_StartedWithCarriedState(t *testing.T) {
	due := t0.Add(3 * time.Hour)
	carried := meterSnapshot("running", 6, 5)
	carried.ActingAtLoaded = true
	carried.ActingInstants = map[string]time.Time{"late": due}
	h := newActHarness(t, wf.Snapshot{}, nil)
	h.deliver(4*time.Hour, stoppedEvent(7))
	if err := h.run(wf.Input{ItemID: itemID, Carried: &carried}); err != nil {
		t.Fatalf("workflow error: %v", err)
	}
	if h.loads != 0 || len(h.reads) != 0 {
		t.Errorf("LoadItem calls = %d, LoadActingInstants calls = %d; want none", h.loads, len(h.reads))
	}
	h.wantActs("carried, not yet acted", due)

	// Carried as already acted for that instant: nothing more.
	done := meterSnapshot("running", 6, 5)
	done.ActingAtLoaded = true
	done.ActingInstants = map[string]time.Time{"late": due}
	done.ActingAtDone = map[string]time.Time{"late": due}
	h = newActHarness(t, wf.Snapshot{}, nil)
	h.deliver(4*time.Hour, stoppedEvent(7))
	if err := h.run(wf.Input{ItemID: itemID, Carried: &done}); err != nil {
		t.Fatalf("workflow error: %v", err)
	}
	h.wantActs("carried, already acted")
}
