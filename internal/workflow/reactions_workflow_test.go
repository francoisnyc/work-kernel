// Tests for proof-of-concept Step 2, part 2: link reactions in the Item
// workflow, with fake activities, in Temporal's test environment.
//
//   - A transition request held by a reaction runs RunReaction (not
//     RunCheck), one hop below the request, within the hook budget; the
//     activity resolves the hold itself. When the budget runs out (or the
//     reaction fails for good) the hold resolves as error through
//     ResolveReaction (budget_exhausted, reaction_failed). Closing the hold
//     calls the reaction off.
//   - Closing a reaction's hold any other way than allowed (denied, failed,
//     canceled by an escape) runs the undo for that hold, caused by the
//     closing event, one hop below it. Allowed runs none.
//   - Entering a state by an undo's trigger (approved -> withdrawn) runs
//     the undo for the hold that entered the state left, never an earlier
//     one; a loaded Snapshot carries that hold as EnteredHold.
//   - Undos LoadItem reports (PendingUndos) run at the start of a run.
//   - An undo is never dropped: a failure that may pass is retried without
//     limit; a refusal (non-retryable Rejected, the code as its detail) is
//     recorded once through ReactionFailed.
package workflow_test

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"

	wf "github.com/francoisnyc/work-kernel/internal/workflow"
)

const (
	hold1 = "0193a1b2-0000-7000-8000-0000000000b1"
	hold2 = "0193a1b2-0000-7000-8000-0000000000b2"
)

// leaveSnapshot is what LoadItem reports for a vacation request v2: the HR
// check on draft -> submitted, deduct holding submitted -> approved, refund
// undoing deduct and running on approved -> withdrawn.
func leaveSnapshot(state string, itemSeq, enteredSeq int64) wf.Snapshot {
	return wf.Snapshot{
		TypeID: "vacation_request", TypeVersion: 2, Timers: map[string]wf.TimerSpec{},
		Checks:        map[string]string{"draft->submitted": "hr_check"},
		HeldReactions: map[string]string{"submitted->approved": "deduct"},
		Undos:         map[string]string{"deduct": "refund"},
		UndoOnEntry:   map[string]string{"approved->withdrawn": "refund"},
		HookBudget:    budget,
		State:         state, LastAppliedSeq: itemSeq,
		EnteredSeq: enteredSeq, EnteredAt: t0,
		CorrelationID: "corr-1",
	}
}

type reactionCall struct {
	In wf.ReactionInput
	At time.Time
}

// reactionHarness is the hook harness plus fakes for the reaction activities.
type reactionHarness struct {
	*hookHarness
	mu2         sync.Mutex
	reactions   []reactionCall
	resolves    []wf.ResolveReactionInput
	undos       []reactionCall
	failed      []wf.ReactionFailedInput
	reactionErr error          // returned by every RunReaction attempt
	undoErrs    []error        // returned by successive Undo attempts, then nil
	undoFails   map[string]int // by hold: how many attempts fail (retryably) first
}

func newReactionHarness(t *testing.T, loaded wf.Snapshot) *reactionHarness {
	t.Helper()
	h := &reactionHarness{hookHarness: newHookHarness(t, loaded), undoFails: map[string]int{}}
	// The fakes below record under mu2; tests read the records after the run.
	h.env.RegisterActivityWithOptions(func(ctx context.Context, in wf.ReactionInput) error {
		h.mu2.Lock()
		defer h.mu2.Unlock()
		h.reactions = append(h.reactions, reactionCall{In: in, At: h.env.Now()})
		return h.reactionErr
	}, activity.RegisterOptions{Name: wf.ActivityRunReaction})
	h.env.RegisterActivityWithOptions(func(ctx context.Context, in wf.ResolveReactionInput) error {
		h.mu2.Lock()
		defer h.mu2.Unlock()
		h.resolves = append(h.resolves, in)
		return nil
	}, activity.RegisterOptions{Name: wf.ActivityResolveReaction})
	h.env.RegisterActivityWithOptions(func(ctx context.Context, in wf.ReactionInput) error {
		h.mu2.Lock()
		defer h.mu2.Unlock()
		h.undos = append(h.undos, reactionCall{In: in, At: h.env.Now()})
		if h.undoFails[in.HoldID] > 0 {
			h.undoFails[in.HoldID]--
			return failing()
		}
		if len(h.undoErrs) == 0 {
			return nil
		}
		err := h.undoErrs[0]
		h.undoErrs = h.undoErrs[1:]
		return err
	}, activity.RegisterOptions{Name: wf.ActivityUndo})
	h.env.RegisterActivityWithOptions(func(ctx context.Context, in wf.ReactionFailedInput) error {
		h.mu2.Lock()
		defer h.mu2.Unlock()
		h.failed = append(h.failed, in)
		return nil
	}, activity.RegisterOptions{Name: wf.ActivityReactionFailed})
	return h
}

// approvalRequest: bob asks for submitted -> approved, opening hold.
func approvalRequest(seq int64, hold string) wf.Event {
	return wf.Event{Seq: seq, Op: "transition_requested", From: "submitted", RequestedTo: "approved",
		HoldID: hold, CorrelationID: "corr-7"}
}

// reactionClose closes the approval hold: allowed (into approved) or
// denied or failed, as the reaction, one hop below the request.
func reactionClose(seq int64, op, to, hold string) wf.Event {
	return wf.Event{Seq: seq, Op: op, From: "submitted", To: to, HoldID: hold, CorrelationID: "corr-7", Hop: 1}
}

func reactionInput(reaction, hold string, causeSeq int64, corr string, hop int) wf.ReactionInput {
	return wf.ReactionInput{
		ItemID: itemID, TypeID: "vacation_request", TypeVersion: 2, ReactionID: reaction,
		HoldID: hold, CauseSeq: causeSeq, CorrelationID: corr, Hop: hop,
	}
}

func inputs(calls []reactionCall) []wf.ReactionInput {
	var out []wf.ReactionInput
	for _, c := range calls {
		out = append(out, c.In)
	}
	return out
}

// The approval request runs deduct once, one hop below the request; the
// check hooks are not involved; an allowed close runs no undo.
func TestReactions_ApprovalRunsTheHeldReaction(t *testing.T) {
	h := newReactionHarness(t, leaveSnapshot("submitted", 5, 5))
	h.deliver(0, approvalRequest(6, hold1))
	allowed := h.deliver(time.Minute, reactionClose(7, "transition", "approved", hold1))
	h.runToCompletion()

	if want := []wf.ReactionInput{reactionInput("deduct", hold1, 6, "corr-7", 1)}; !reflect.DeepEqual(inputs(h.reactions), want) {
		t.Errorf("RunReaction calls = %+v\nwant %+v", inputs(h.reactions), want)
	}
	if len(h.check) != 0 || len(h.hookHarness.resolves) != 0 {
		t.Errorf("RunCheck %v, ResolveHold %v: want none for a reaction's hold", h.check, h.hookHarness.resolves)
	}
	if len(h.undos) != 0 || len(h.resolves) != 0 {
		t.Errorf("Undo %v, ResolveReaction %v: want none after an allowed close", h.undos, h.resolves)
	}
	h.wantApplied("the approval", allowed, true)
}

// Every non-allow close of the reaction's hold runs its undo once, for that
// hold, caused by the closing event, one hop below it.
func TestReactions_NonAllowCloseRunsTheUndo(t *testing.T) {
	escape := wf.Event{Seq: 7, Op: "transition", From: "submitted", To: "withdrawn", HoldID: hold1, CorrelationID: "corr-9"}
	cases := []struct {
		name    string
		closing wf.Event
		want    wf.ReactionInput
	}{
		{"denied", reactionClose(7, "transition_denied", "", hold1), reactionInput("refund", hold1, 7, "corr-7", 2)},
		{"failed", reactionClose(7, "transition_failed", "", hold1), reactionInput("refund", hold1, 7, "corr-7", 2)},
		{"canceled by an escape", escape, reactionInput("refund", hold1, 7, "corr-9", 1)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newReactionHarness(t, leaveSnapshot("submitted", 5, 5))
			h.deliver(0, approvalRequest(6, hold1))
			closed := h.deliver(time.Minute, c.closing)
			h.runToCompletion()
			if want := []wf.ReactionInput{c.want}; !reflect.DeepEqual(inputs(h.undos), want) {
				t.Errorf("Undo calls = %+v\nwant %+v", inputs(h.undos), want)
			}
			if len(h.undos) == 1 && h.undos[0].At.Before(t0.Add(time.Minute)) {
				t.Errorf("Undo at %v, before the hold closed", h.undos[0].At)
			}
			h.wantApplied("the closing event", closed, true)
		})
	}
}

// Denied, approved again, then withdrawn, in one run (the denial's undo
// is still retrying, so the run is alive when the withdrawal comes): the
// denial's undo is for the first hold, the withdrawal's for the hold that
// entered approved.
func TestReactions_WithdrawalUndoesTheHoldThatEnteredApproved(t *testing.T) {
	h := newReactionHarness(t, leaveSnapshot("submitted", 5, 5))
	h.undoFails[hold1] = 1 << 20 // keeps failing well past the withdrawal
	h.deliver(0, approvalRequest(6, hold1))
	h.deliver(time.Minute, reactionClose(7, "transition_denied", "", hold1))
	h.deliver(2*time.Minute, approvalRequest(8, hold2))
	h.deliver(3*time.Minute, reactionClose(9, "transition", "approved", hold2))
	withdrawn := h.deliver(10*time.Minute, wf.Event{Seq: 10, Op: "transition", From: "approved", To: "withdrawn", CorrelationID: "corr-w"})
	h.env.RegisterDelayedCallback(func() { // then it succeeds
		h.mu2.Lock()
		defer h.mu2.Unlock()
		h.undoFails[hold1] = 0
	}, time.Hour)
	h.runToCompletion()

	seen := map[wf.ReactionInput]int{}
	for _, c := range h.undos {
		seen[c.In]++
	}
	first, second := reactionInput("refund", hold1, 7, "corr-7", 2), reactionInput("refund", hold2, 10, "corr-w", 1)
	if seen[first] == 0 || seen[second] != 1 || len(seen) != 2 {
		t.Errorf("Undo inputs = %v\nwant attempts of %+v and one of %+v, nothing else", seen, first, second)
	}
	h.wantApplied("the withdrawal", withdrawn, true)
}

// A run that starts in approved (the run that saw the approval completed)
// gets the entering hold from LoadItem.
func TestReactions_WithdrawalAfterALoad_UsesEnteredHold(t *testing.T) {
	snap := leaveSnapshot("approved", 9, 9)
	snap.EnteredHold = hold2
	h := newReactionHarness(t, snap)
	h.deliver(0, wf.Event{Seq: 10, Op: "transition", From: "approved", To: "withdrawn", CorrelationID: "corr-w"})
	h.runToCompletion()
	if want := []wf.ReactionInput{reactionInput("refund", hold2, 10, "corr-w", 1)}; !reflect.DeepEqual(inputs(h.undos), want) {
		t.Errorf("Undo calls = %+v\nwant %+v", inputs(h.undos), want)
	}
}

// Undos LoadItem reports run at the start; the run waits for them.
func TestReactions_PendingUndosFromTheLoad_Run(t *testing.T) {
	snap := leaveSnapshot("withdrawn", 8, 8)
	snap.PendingUndos = []wf.PendingUndo{{ReactionID: "refund", HoldID: hold1, CauseSeq: 8, CorrelationID: "corr-x", Hop: 0}}
	h := newReactionHarness(t, snap)
	h.undoErrs = []error{failing()} // the first attempt fails; the run must not end before the retry
	h.runToCompletion()
	want := reactionInput("refund", hold1, 8, "corr-x", 1)
	if len(h.undos) != 2 || h.undos[1].In != want {
		t.Errorf("Undo calls = %+v, want two attempts of %+v", inputs(h.undos), want)
	}
}

// An undo is never dropped: failures that may pass are retried without a
// budget; a refusal is recorded once, with its code.
func TestReactions_UndoRetriesThenRecordsARefusal(t *testing.T) {
	h := newReactionHarness(t, leaveSnapshot("submitted", 5, 5))
	for i := 0; i < 4; i++ {
		h.undoErrs = append(h.undoErrs, failing())
	}
	h.deliver(0, approvalRequest(6, hold1))
	h.deliver(time.Minute, reactionClose(7, "transition_failed", "", hold1))
	h.runToCompletion()
	if len(h.undos) != 5 || len(h.failed) != 0 {
		t.Errorf("Undo attempts %d, ReactionFailed %v; want 5 and none", len(h.undos), h.failed)
	}

	h = newReactionHarness(t, leaveSnapshot("submitted", 5, 5))
	h.undoErrs = []error{temporal.NewNonRetryableApplicationError("refused", wf.ErrTypeRejected, nil, "reaction_not_accepted")}
	h.deliver(0, approvalRequest(6, hold1))
	h.deliver(time.Minute, reactionClose(7, "transition_denied", "", hold1))
	h.runToCompletion()
	want := []wf.ReactionFailedInput{{Reaction: reactionInput("refund", hold1, 7, "corr-7", 2), Reason: "reaction_not_accepted"}}
	if len(h.undos) != 1 || !reflect.DeepEqual(h.failed, want) {
		t.Errorf("Undo attempts %d, ReactionFailed %+v; want 1 and %+v", len(h.undos), h.failed, want)
	}
}

// When the reaction cannot answer within its budget, its hold resolves as
// error through ResolveReaction; failing for good, at once.
func TestReactions_GiveUpResolvesAsError(t *testing.T) {
	h := newReactionHarness(t, leaveSnapshot("submitted", 5, 5))
	h.reactionErr = failing()
	h.deliver(0, approvalRequest(6, hold1))
	h.deliver(time.Hour, reactionClose(7, "transition_failed", "", hold1))
	h.runToCompletion()
	if len(h.reactions) < 2 {
		t.Errorf("RunReaction attempts = %d, want retries within the budget", len(h.reactions))
	}
	want := []wf.ResolveReactionInput{{Reaction: reactionInput("deduct", hold1, 6, "corr-7", 1), Reason: "budget_exhausted"}}
	if !reflect.DeepEqual(h.resolves, want) {
		t.Errorf("ResolveReaction calls = %+v\nwant %+v", h.resolves, want)
	}

	h = newReactionHarness(t, leaveSnapshot("submitted", 5, 5))
	h.reactionErr = temporal.NewNonRetryableApplicationError("bad", wf.ErrTypeInvalid, nil)
	h.deliver(0, approvalRequest(6, hold1))
	h.deliver(time.Hour, reactionClose(7, "transition_failed", "", hold1))
	h.runToCompletion()
	want[0].Reason = "reaction_failed"
	if len(h.reactions) != 1 || !reflect.DeepEqual(h.resolves, want) {
		t.Errorf("RunReaction %d, ResolveReaction %+v; want 1 and %+v", len(h.reactions), h.resolves, want)
	}
}

// The escape closes the hold: the reaction is called off, nothing resolves
// the hold afterwards, and the undo runs.
func TestReactions_EscapeCallsOffTheReaction(t *testing.T) {
	h := newReactionHarness(t, leaveSnapshot("submitted", 5, 5))
	h.reactionErr = failing()
	h.deliver(0, approvalRequest(6, hold1))
	h.deliver(3*time.Minute, wf.Event{Seq: 7, Op: "transition", From: "submitted", To: "withdrawn", HoldID: hold1})
	h.runToCompletion()
	for _, c := range h.reactions {
		if c.At.After(t0.Add(3 * time.Minute)) {
			t.Errorf("RunReaction attempt at %v, after the escape", c.At)
		}
	}
	if len(h.resolves) != 0 || len(h.undos) != 1 {
		t.Errorf("ResolveReaction %v, Undo %v; want none and one", h.resolves, inputs(h.undos))
	}
	if now := h.env.Now(); now.After(t0.Add(budget)) {
		t.Errorf("workflow ended at %v: it waited out the canceled reaction's budget", now)
	}
}
