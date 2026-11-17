// Tests for proof-of-concept Step 2, part 3: acting timers, deferred state
// timers and the refund email in the Item workflow, with fake activities,
// in Temporal's test environment (time skips forward).
//
//   - An acting timer runs its action hook (RunAction) After the entering
//     event, then Every, Max runs per entry, one hop below the entering
//     event, keyed timer:<item>:<timer>:<entry>:<n>, its run named in the
//     input. Leaving the state stops it; a new entry counts again; a
//     loaded count goes on from there; overdue runs fire one at once, then
//     the schedule. It does not wait for a hold. A run that gives up records
//     hook_failed for that run, and the next run still comes.
//   - A state timer due while a hold is open waits for the hold to close,
//     then looks again: the Item left the state, nothing; still there, it
//     fires at once. A Held answer is not spent: the timer looks again
//     after the next event.
//   - reaction_applied runs the reaction's action hooks (the refund email),
//     and a run stays alive until a pending action has finished, even in a
//     final state with no timer.
//   - On a scaled clock (DevDay), a hook still gets its whole budget.
package workflow_test

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"go.temporal.io/sdk/activity"

	wf "github.com/francoisnyc/work-kernel/internal/workflow"
)

// mailHarness is the hook harness plus RunAction and HookFailed fakes,
// whose failures are counted per hook (and per run for a timer).
type mailHarness struct {
	*hookHarness
	mu3         sync.Mutex
	actions     []hookCall
	failed      []wf.HookFailedInput
	actionFails map[string]int // by hook ID: how many attempts fail first (retryably)
}

func newMailHarness(t *testing.T, loaded wf.Snapshot) *mailHarness {
	t.Helper()
	h := &mailHarness{hookHarness: newHookHarness(t, loaded), actionFails: map[string]int{}}
	h.env.RegisterActivityWithOptions(func(ctx context.Context, in wf.HookInput) error {
		h.mu3.Lock()
		defer h.mu3.Unlock()
		h.actions = append(h.actions, hookCall{In: in, At: h.env.Now()})
		if h.actionFails[in.HookID] > 0 {
			h.actionFails[in.HookID]--
			return failing()
		}
		return nil
	}, activity.RegisterOptions{Name: wf.ActivityRunAction})
	h.env.RegisterActivityWithOptions(func(ctx context.Context, in wf.HookFailedInput) error {
		h.mu3.Lock()
		defer h.mu3.Unlock()
		h.failed = append(h.failed, in)
		return nil
	}, activity.RegisterOptions{Name: wf.ActivityHookFailed})
	return h
}

// runsOf are the RunAction calls of one hook, in order.
func (h *mailHarness) runsOf(hook string) []hookCall {
	var out []hookCall
	for _, c := range h.actions {
		if c.In.HookID == hook {
			out = append(out, c)
		}
	}
	return out
}

// v3Snapshot is what LoadItem reports for a vacation request v3 in state,
// entered by event enteredSeq at enteredAt: the expiry timer and the
// reminder on submitted, the mail hooks, deduct and refund.
func v3Snapshot(state string, itemSeq, enteredSeq int64, enteredAt time.Time) wf.Snapshot {
	s := leaveSnapshot(state, itemSeq, enteredSeq)
	s.TypeVersion = 3
	s.EnteredAt = enteredAt
	s.Timers = map[string]wf.TimerSpec{"submitted": {After: 120 * time.Hour, To: "expired"}}
	s.Acting = map[string][]wf.ActingSpec{"submitted": {{ID: "remind", After: 48 * time.Hour, Every: 24 * time.Hour,
		Max: 2, Hook: "remind_approver"}}}
	s.Actions = map[string][]string{"approved": {"notify_approved"}, "rejected": {"notify_rejected"},
		"expired": {"notify_expired"}}
	s.ReactionHooks = map[string][]string{"refund": {"refund_email"}}
	return s
}

// remindInput is RunAction's input for one reminder run.
func remindInput(entered int64, run, hop int) wf.HookInput {
	return wf.HookInput{
		ItemID: itemID, TypeID: "vacation_request", TypeVersion: 3, HookID: "remind_approver",
		CauseSeq: entered, CorrelationID: "corr-1", Hop: hop, Timer: "remind", Run: run,
		IdempotencyKey: fmt.Sprintf("timer:%s:remind:%d:%d", itemID, entered, run),
	}
}

// --- acting timers ---

// No expiry here, to see the reminders alone: two runs, at 48h and 72h.
func TestActing_RemindsOnScheduleUpToMax(t *testing.T) {
	snap := v3Snapshot("submitted", 5, 5, t0)
	snap.Timers = map[string]wf.TimerSpec{}
	h := newMailHarness(t, snap)
	h.runToCompletion()

	runs := h.runsOf("remind_approver")
	if len(runs) != 2 {
		t.Fatalf("reminders = %+v, want 2 (max)", runs)
	}
	for i, wantAt := range []time.Time{t0.Add(48 * time.Hour), t0.Add(72 * time.Hour)} {
		if want := remindInput(5, i+1, 1); !reflect.DeepEqual(runs[i].In, want) {
			t.Errorf("reminder %d input = %+v\nwant %+v", i+1, runs[i].In, want)
		}
		if !runs[i].At.Equal(wantAt) {
			t.Errorf("reminder %d at %v, want %v", i+1, runs[i].At, wantAt)
		}
	}
}

// Leaving the state stops it: approved after the first reminder.
func TestActing_StopsWhenTheStateIsLeft(t *testing.T) {
	h := newMailHarness(t, v3Snapshot("submitted", 5, 5, t0))
	h.deliver(50*time.Hour, wf.Event{Seq: 6, Op: "transition", From: "submitted", To: "approved"})
	h.runToCompletion()
	if runs := h.runsOf("remind_approver"); len(runs) != 1 {
		t.Errorf("reminders = %d, want 1: the request left submitted before the second", len(runs))
	}
}

// A run that starts after a crash goes on from the loaded count.
func TestActing_GoesOnFromTheLoadedCount(t *testing.T) {
	snap := v3Snapshot("submitted", 6, 5, t0.Add(-60*time.Hour)) // the first ran 12 hours ago
	snap.Timers = map[string]wf.TimerSpec{}
	snap.ActingRuns = map[string]int{"remind": 1}
	h := newMailHarness(t, snap)
	h.runToCompletion()
	runs := h.runsOf("remind_approver")
	if len(runs) != 1 || runs[0].In.Run != 2 || !runs[0].At.Equal(t0.Add(12*time.Hour)) {
		t.Errorf("reminders = %+v, want only run 2, at %v", runs, t0.Add(12*time.Hour))
	}
}

// Overdue runs (the worker was down): one at once, then the schedule from
// it, never a burst.
func TestActing_OneOverdueRunThenTheSchedule(t *testing.T) {
	snap := v3Snapshot("submitted", 5, 5, t0.Add(-10*24*time.Hour))
	snap.Timers = map[string]wf.TimerSpec{}
	h := newMailHarness(t, snap)
	h.runToCompletion()
	runs := h.runsOf("remind_approver")
	if len(runs) != 2 || !runs[0].At.Equal(t0) || !runs[1].At.Equal(t0.Add(24*time.Hour)) {
		t.Errorf("reminders = %+v, want run 1 at once and run 2 a day later", runs)
	}
}

// Recall and resubmit is a new entry: its runs count from 1 again, caused
// by the new entry.
func TestActing_ANewEntryCountsAgain(t *testing.T) {
	snap := v3Snapshot("submitted", 5, 5, t0)
	// No expiry; a long draft timer keeps the run alive between the two.
	snap.Timers = map[string]wf.TimerSpec{"draft": {After: 1000 * time.Hour, To: "submitted"}}
	h := newMailHarness(t, snap)
	h.deliver(time.Hour, wf.Event{Seq: 6, Op: "transition", From: "submitted", To: "draft", CorrelationID: "corr-1"})
	h.deliver(2*time.Hour, wf.Event{Seq: 7, Op: "transition", From: "draft", To: "submitted", CorrelationID: "corr-1"})
	h.runToCompletion()
	runs := h.runsOf("remind_approver")
	if len(runs) != 2 || runs[0].In != remindInput(7, 1, 1) || runs[1].In != remindInput(7, 2, 1) ||
		!runs[0].At.Equal(t0.Add(50*time.Hour)) {
		t.Errorf("reminders = %+v, want runs 1 and 2 of entry 7, from 50h", runs)
	}
}

// Acting timers do not wait for a hold: the approval's hold is open from
// 40h to 60h, and the first reminder still goes at 48h.
func TestActing_DoesNotWaitForAHold(t *testing.T) {
	snap := v3Snapshot("submitted", 5, 5, t0)
	snap.Timers = map[string]wf.TimerSpec{}
	h := newMailHarness(t, snap)
	h.reactionFakes()
	h.deliver(40*time.Hour, approvalRequest(6, hold1))
	h.deliver(60*time.Hour, reactionClose(7, "transition_denied", "", hold1))
	h.runToCompletion()
	runs := h.runsOf("remind_approver")
	if len(runs) == 0 || !runs[0].At.Equal(t0.Add(48*time.Hour)) {
		t.Errorf("reminders = %+v, want the first at 48h, during the hold", runs)
	}
}

// A run that gives up records hook_failed for that run; the next run still
// comes.
func TestActing_ARunThatGivesUp(t *testing.T) {
	snap := v3Snapshot("submitted", 5, 5, t0)
	snap.Timers = map[string]wf.TimerSpec{}
	h := newMailHarness(t, snap)
	h.actionFails["remind_approver"] = 1 << 20
	h.env.RegisterDelayedCallback(func() {
		h.mu3.Lock()
		defer h.mu3.Unlock()
		h.actionFails["remind_approver"] = 0 // the stub is back before the second run
	}, 70*time.Hour)
	h.runToCompletion()
	if len(h.failed) != 1 || h.failed[0].Hook.Timer != "remind" || h.failed[0].Hook.Run != 1 {
		t.Errorf("HookFailed = %+v, want one, for run 1", h.failed)
	}
	runs := h.runsOf("remind_approver")
	if len(runs) == 0 || runs[len(runs)-1].In.Run != 2 {
		t.Errorf("reminders = %+v, want the last to be run 2, after run 1 gave up", runs)
	}
}

// --- deferred state timers ---

// The expiry (120h) comes due during the approval's hold (100h to 130h).
// The hold allows: approved; the expiry does nothing.
func TestTimer_DueDuringAHold_NothingOnceLeft(t *testing.T) {
	snap := v3Snapshot("submitted", 5, 5, t0)
	snap.Acting = nil
	h := newMailHarness(t, snap)
	h.reactionFakes()
	h.deliver(100*time.Hour, approvalRequest(6, hold1))
	h.deliver(130*time.Hour, reactionClose(7, "transition", "approved", hold1))
	h.runToCompletion()
	if len(h.fires) != 0 {
		t.Errorf("FireTimer calls = %+v, want none: the request was approved", h.fires)
	}
}

// The hold denies: still submitted, the expiry fires at once.
func TestTimer_DueDuringAHold_FiresOnceTheHoldCloses(t *testing.T) {
	snap := v3Snapshot("submitted", 5, 5, t0)
	snap.Acting = nil
	h := newMailHarness(t, snap)
	h.reactionFakes()
	h.deliver(100*time.Hour, approvalRequest(6, hold1))
	h.deliver(130*time.Hour, reactionClose(7, "transition_denied", "", hold1))
	// What the timer's move writes, as the relay delivers it.
	h.deliver(130*time.Hour+time.Minute, wf.Event{Seq: 8, Op: "transition", From: "submitted", To: "expired", Hop: 1})
	h.runToCompletion()
	if len(h.fires) != 1 {
		t.Fatalf("FireTimer calls = %+v, want one", h.fires)
	}
	f := h.fires[0]
	if !f.At.Equal(t0.Add(130*time.Hour)) || f.In.FromState != "submitted" || f.In.ToState != "expired" ||
		f.In.ExpectedItemSeq != 7 {
		t.Errorf("FireTimer = %+v at %v; want submitted -> expired at the denial (130h), expected seq 7", f.In, f.At)
	}
}

// The item service answers Held (a hold the workflow has not heard of
// yet): the timer is not spent; it looks again after the next event.
func TestTimer_HeldIsNotSpent(t *testing.T) {
	snap := v3Snapshot("submitted", 5, 5, t0)
	snap.Acting = nil
	h := newMailHarness(t, snap)
	h.reactionFakes()
	h.fireErrs = []error{held()}
	h.deliver(125*time.Hour, approvalRequest(6, hold1))
	h.deliver(126*time.Hour, reactionClose(7, "transition_denied", "", hold1))
	h.deliver(126*time.Hour+time.Minute, wf.Event{Seq: 8, Op: "transition", From: "submitted", To: "expired", Hop: 1})
	h.runToCompletion()
	if len(h.fires) != 2 {
		t.Fatalf("FireTimer calls = %d, want 2: Held, then once the hold closed", len(h.fires))
	}
	if !h.fires[0].At.Equal(t0.Add(120*time.Hour)) || !h.fires[1].At.Equal(t0.Add(126*time.Hour)) {
		t.Errorf("FireTimer at %v and %v, want 120h and 126h", h.fires[0].At, h.fires[1].At)
	}
}

// --- the refund email ---

// reaction_applied runs the refund email, in a final state with no timer,
// one hop below it, keyed by that event. The event starts the run (as the
// relay does), so it arrives with the start.
func TestReactionApplied_RunsTheRefundEmail(t *testing.T) {
	h := newMailHarness(t, v3Snapshot("withdrawn", 8, 8, t0))
	h.deliver(0, wf.Event{Seq: 9, Op: "reaction_applied", Reaction: "refund", CorrelationID: "corr-r", Hop: 1})
	h.runToCompletion()
	want := wf.HookInput{ItemID: itemID, TypeID: "vacation_request", TypeVersion: 3, HookID: "refund_email",
		CauseSeq: 9, CorrelationID: "corr-r", Hop: 2, IdempotencyKey: fmt.Sprintf("hook:%s:9:refund_email", itemID)}
	if runs := h.runsOf("refund_email"); len(runs) != 1 || runs[0].In != want {
		t.Errorf("refund emails = %+v, want one: %+v", runs, want)
	}
}

// A run that starts after a crash, with the refund email pending (LoadItem
// reports it), sends it, and does not end before it is sent, although the
// request is withdrawn and nothing else waits.
func TestPendingEmail_KeepsTheRunAlive(t *testing.T) {
	snap := v3Snapshot("withdrawn", 9, 8, t0)
	snap.PendingActions = []wf.PendingAction{{HookID: "refund_email", CauseSeq: 9, CorrelationID: "corr-r", Hop: 1}}
	h := newMailHarness(t, snap)
	h.actionFails["refund_email"] = 3
	h.runToCompletion()
	runs := h.runsOf("refund_email")
	if len(runs) != 4 {
		t.Fatalf("refund email attempts = %d, want 4 (three failures, then sent)", len(runs))
	}
	if now := h.env.Now(); now.Before(runs[3].At) {
		t.Errorf("the run ended at %v, before the email was sent at %v", now, runs[3].At)
	}
	if len(h.failed) != 0 {
		t.Errorf("HookFailed = %+v, want none: it was sent within the budget", h.failed)
	}
}

// --- the scaled clock ---

// A scaled worker's Snapshot scales the Type's durations only: a check still
// gets its whole budget (10 minutes here, a day being 2 s).
func TestDevClock_AHookKeepsItsWholeBudget(t *testing.T) {
	snap := orderSnapshot("open", 5, 5)
	snap.DevDay = 2 * time.Second
	h := newHookHarness(t, snap)
	h.checkErr["check"] = failing()
	h.deliver(0, requestEvent(6))
	h.deliver(time.Hour, closing(7, "transition_failed", ""))
	h.runToCompletion()
	if len(h.resolves) != 1 {
		t.Fatalf("ResolveHold calls = %+v, want one", h.resolves)
	}
	assertGaveUpAtTheBudget(t, "ResolveHold on a scaled clock", h.resolves[0].At)
}

// reactionFakes registers no-op reaction activities, for tests whose
// approvals are decided by the events they deliver.
func (h *mailHarness) reactionFakes() {
	noop := func(ctx context.Context, in wf.ReactionInput) error { return nil }
	h.env.RegisterActivityWithOptions(noop, activity.RegisterOptions{Name: wf.ActivityRunReaction})
	h.env.RegisterActivityWithOptions(noop, activity.RegisterOptions{Name: wf.ActivityUndo})
	h.env.RegisterActivityWithOptions(func(ctx context.Context, in wf.ResolveReactionInput) error { return nil },
		activity.RegisterOptions{Name: wf.ActivityResolveReaction})
	h.env.RegisterActivityWithOptions(func(ctx context.Context, in wf.ReactionFailedInput) error { return nil },
		activity.RegisterOptions{Name: wf.ActivityReactionFailed})
}
