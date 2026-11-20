// Tests for Jira part 2, step 2: move and create reactions in the workflow.
//
// The workflow knows a reaction by its ID and its trigger, nothing more:
// one activity (RunAfterReaction) judges it, writes the target and records
// the outcome on the Item. The workflow's part is the queue:
//
//   - Entering a state adds that state's reactions, in the Type's order,
//     each only if the entry comes from one of its `from` states.
//   - "An Item was created inside me" (the child update) adds the
//     reactions that name the child's Type.
//   - LoadItem reports the reactions not yet recorded, so a run that starts
//     cold (the triggering event is already in what it loaded) or after a
//     crash runs them. A trigger both loaded and delivered runs once.
//   - They run one at a time, in order. A failure that may pass is retried
//     within the hook budget; then the reaction is recorded as failed,
//     budget_exhausted. A run never completes with a reaction pending.
//   - A Type without them issues the commands it always did.
package workflow_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"

	wf "github.com/francoisnyc/work-kernel/internal/workflow"
)

const childID = "0193a1b2-0000-7000-8000-0000000000c1"

// deskSnapshot: a desk whose `working` state runs two reactions on entry
// (checklist only from open, then log), and which reopens when a remark is
// created inside it. `waiting` and `open` have no behavior.
func deskSnapshot(state string, itemSeq, enteredSeq int64) wf.Snapshot {
	return wf.Snapshot{
		TypeID: "desk", TypeVersion: 1, Timers: map[string]wf.TimerSpec{},
		EnterReactions: map[string][]wf.EnterReaction{
			"working": {{ID: "checklist", From: []string{"open"}}, {ID: "log"}},
		},
		InsideReactions: map[string][]string{"remark": {"reopen"}},
		HookBudget:      budget,
		State:           state, LastAppliedSeq: itemSeq,
		EnteredSeq: enteredSeq, EnteredAt: t0,
		CorrelationID: "corr-1", Hop: 2,
	}
}

// alive gives the snapshot's states a timer far away, so a test run stays
// to take events delivered later (a run in a state without behavior
// completes at once, as it must). leave ends such a test.
func alive(s wf.Snapshot) wf.Snapshot {
	s.Timers = map[string]wf.TimerSpec{}
	for _, st := range []string{"open", "working", "waiting"} {
		s.Timers[st] = wf.TimerSpec{After: 10000 * time.Hour, To: "done"}
	}
	return s
}

func (h *afterHarness) leave(after time.Duration, seq int64, from string) {
	h.t.Helper()
	h.deliver(after, transitionEvent(seq, from, "done"))
}

type afterCall struct {
	In wf.AfterReactionInput
	At time.Time
}

type afterHarness struct {
	*hookHarness
	runs   []afterCall
	failed []wf.AfterReactionFailedInput
	// errs are returned by successive RunAfterReaction calls of a reaction
	// (by ID); nil when used up. always, if set for it, is returned every time.
	errs   map[string][]error
	always map[string]error
}

func newAfterHarness(t *testing.T, loaded wf.Snapshot) *afterHarness {
	t.Helper()
	h := &afterHarness{hookHarness: newHookHarness(t, loaded), errs: map[string][]error{}, always: map[string]error{}}
	h.env.RegisterActivityWithOptions(func(ctx context.Context, in wf.AfterReactionInput) error {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.runs = append(h.runs, afterCall{In: in, At: h.env.Now()})
		if err := h.always[in.ReactionID]; err != nil {
			return err
		}
		if q := h.errs[in.ReactionID]; len(q) > 0 {
			h.errs[in.ReactionID] = q[1:]
			return q[0]
		}
		return nil
	}, activity.RegisterOptions{Name: wf.ActivityRunAfterReaction})
	h.env.RegisterActivityWithOptions(func(ctx context.Context, in wf.AfterReactionFailedInput) error {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.failed = append(h.failed, in)
		return nil
	}, activity.RegisterOptions{Name: wf.ActivityAfterReactionFailed})
	return h
}

// child delivers "a remark was created inside" `after` the start.
func (h *afterHarness) child(after time.Duration, typeID, id string) *outcome {
	h.t.Helper()
	out := &outcome{}
	ev := wf.ChildEvent{ChildID: id, TypeID: typeID, Seq: 1, At: t0.Add(after), CorrelationID: "corr-child", Hop: 0}
	h.env.RegisterDelayedCallback(func() {
		h.env.UpdateWorkflow(wf.UpdateChild, fmt.Sprintf("%s:1:inside@%s", id, after), &testsuite.TestUpdateCallback{
			OnAccept:   func() {},
			OnReject:   func(err error) { out.rejected = err },
			OnComplete: func(res any, err error) { out.done, out.err = true, err },
		}, ev)
	}, after)
	return out
}

func (h *afterHarness) ran() []string {
	var out []string
	for _, r := range h.runs {
		out = append(out, r.In.ReactionID)
	}
	return out
}

func entryInput(reaction string, causeSeq int64, corr string, hop int) wf.AfterReactionInput {
	return wf.AfterReactionInput{
		ItemID: itemID, TypeID: "desk", TypeVersion: 1, ReactionID: reaction,
		CauseItem: itemID, CauseSeq: causeSeq, CorrelationID: corr, Hop: hop,
	}
}

// --- entering a state ---

// Entering working from open runs both reactions, in the Type's order, one
// hop below the entering event, caused by it.
func TestAfterReactions_EnteringAStateRunsThemInOrder(t *testing.T) {
	h := newAfterHarness(t, alive(deskSnapshot("open", 5, 5)))
	h.deliver(time.Minute, entering(6, "open", "working", "corr-7", 3))
	h.leave(time.Hour, 7, "working")
	h.runToCompletion()

	want := []wf.AfterReactionInput{entryInput("checklist", 6, "corr-7", 4), entryInput("log", 6, "corr-7", 4)}
	if len(h.runs) != 2 || !reflect.DeepEqual(h.runs[0].In, want[0]) || !reflect.DeepEqual(h.runs[1].In, want[1]) {
		t.Errorf("RunAfterReaction calls = %+v\nwant %+v", h.runs, want)
	}
	if len(h.failed) != 0 {
		t.Errorf("AfterReactionFailed calls = %+v, want none", h.failed)
	}
}

// From waiting, only the reaction without `from` runs.
func TestAfterReactions_OnlyFromTheirFromStates(t *testing.T) {
	h := newAfterHarness(t, alive(deskSnapshot("waiting", 5, 5)))
	h.deliver(time.Minute, entering(6, "waiting", "working", "corr-7", 0))
	h.leave(time.Hour, 7, "working")
	h.runToCompletion()
	if got := h.ran(); !reflect.DeepEqual(got, []string{"log"}) {
		t.Errorf("ran %v, want [log]: checklist is from open only", got)
	}
}

// --- an Item created inside ---

// A remark created inside runs reopen, caused by the remark's create, one
// hop below it. A child of a Type the desk does not react to runs nothing.
func TestAfterReactions_AChildCreatedInsideRunsItsReactions(t *testing.T) {
	h := newAfterHarness(t, alive(deskSnapshot("waiting", 5, 5)))
	other := h.child(30*time.Second, "sticker", "0193a1b2-0000-7000-8000-0000000000c2")
	told := h.child(time.Minute, "remark", childID)
	h.leave(time.Hour, 6, "waiting")
	h.runToCompletion()

	if other.rejected != nil || !other.done || other.err != nil || told.rejected != nil || !told.done || told.err != nil {
		t.Errorf("the child updates: %+v and %+v; want both taken without error", other, told)
	}
	want := wf.AfterReactionInput{
		ItemID: itemID, TypeID: "desk", TypeVersion: 1, ReactionID: "reopen",
		CauseItem: childID, CauseSeq: 1, CorrelationID: "corr-child", Hop: 1,
	}
	if len(h.runs) != 1 || !reflect.DeepEqual(h.runs[0].In, want) || !h.runs[0].At.Equal(t0.Add(time.Minute)) {
		t.Errorf("RunAfterReaction calls = %+v, want one, for the remark, when it was told", h.runs)
	}
	// A child changes nothing of what the run knows of its own Item.
	if h.loads != 1 {
		t.Errorf("LoadItem calls = %d, want 1", h.loads)
	}
}

// --- cold starts ---

// The run starts BECAUSE of the trigger, so what it loaded already has it:
// LoadItem reports the reaction pending, and the delivered trigger is the
// same one. It runs once.
func TestAfterReactions_ColdStartOnTheChildsCreate_RunsOnce(t *testing.T) {
	snap := deskSnapshot("waiting", 5, 5)
	snap.PendingReactions = []wf.PendingReaction{{ReactionID: "reopen", CauseItem: childID, CauseSeq: 1, CorrelationID: "corr-child", Hop: 0}}
	h := newAfterHarness(t, snap)
	h.child(0, "remark", childID) // arrives with the start
	h.runToCompletion()
	if len(h.runs) != 1 || h.runs[0].In.CauseItem != childID || h.runs[0].In.Hop != 1 {
		t.Errorf("RunAfterReaction calls = %+v, want exactly one for the remark", h.runs)
	}
}

// The same for an entry: the run starts on the event that entered working,
// loads the Item at that event, and gets the event again as a duplicate.
// (working has no timer here: the run completes when both are done.)
func TestAfterReactions_ColdStartOnTheEnteringEvent_RunsOnce(t *testing.T) {
	snap := deskSnapshot("working", 6, 6)
	snap.CorrelationID, snap.Hop = "corr-7", 3
	snap.PendingReactions = []wf.PendingReaction{
		{ReactionID: "checklist", CauseItem: itemID, CauseSeq: 6, CorrelationID: "corr-7", Hop: 3},
		{ReactionID: "log", CauseItem: itemID, CauseSeq: 6, CorrelationID: "corr-7", Hop: 3},
	}
	h := newAfterHarness(t, snap)
	dup := h.deliver(0, entering(6, "open", "working", "corr-7", 3))
	h.runToCompletion()
	h.wantApplied("the entering event, already loaded", dup, false)
	want := []wf.AfterReactionInput{entryInput("checklist", 6, "corr-7", 4), entryInput("log", 6, "corr-7", 4)}
	if len(h.runs) != 2 || !reflect.DeepEqual(h.runs[0].In, want[0]) || !reflect.DeepEqual(h.runs[1].In, want[1]) {
		t.Errorf("RunAfterReaction calls = %+v\nwant %+v", h.runs, want)
	}
}

// After a crash: a reaction of an EARLIER entry is still pending, though
// the Item has moved on. It runs, for its own cause.
func TestAfterReactions_LoadedPendingForAnEarlierEntry(t *testing.T) {
	snap := deskSnapshot("waiting", 9, 8)
	snap.PendingReactions = []wf.PendingReaction{{ReactionID: "log", CauseItem: itemID, CauseSeq: 6, CorrelationID: "corr-old", Hop: 1}}
	h := newAfterHarness(t, snap)
	h.runToCompletion()
	if len(h.runs) != 1 || !reflect.DeepEqual(h.runs[0].In, entryInput("log", 6, "corr-old", 2)) || !h.runs[0].At.Equal(t0) {
		t.Errorf("RunAfterReaction calls = %+v, want log for event 6, at the start", h.runs)
	}
}

// A run handed a pending reaction by the run before (continue-as-new) runs
// it without loading anything.
func TestAfterReactions_CarriedAcrossContinueAsNew(t *testing.T) {
	carried := deskSnapshot("waiting", 9, 8)
	carried.PendingReactions = []wf.PendingReaction{{ReactionID: "reopen", CauseItem: childID, CauseSeq: 1, CorrelationID: "corr-child"}}
	h := newAfterHarness(t, wf.Snapshot{})
	if err := h.run(wf.Input{ItemID: itemID, Carried: &carried}); err != nil {
		t.Fatalf("workflow error: %v", err)
	}
	if h.loads != 0 || len(h.runs) != 1 || h.runs[0].In.ReactionID != "reopen" {
		t.Errorf("loads %d, runs %+v; want no load and reopen run once", h.loads, h.runs)
	}
}

// --- failures ---

// A failure that may pass (the database is away, the target is held) is
// retried; nothing is recorded as failed.
func TestAfterReactions_ATransientFailureIsRetried(t *testing.T) {
	h := newAfterHarness(t, alive(deskSnapshot("waiting", 5, 5)))
	h.errs["reopen"] = []error{errors.New("connection refused"), errors.New("the target is held")}
	h.child(time.Minute, "remark", childID)
	h.leave(time.Hour, 6, "waiting")
	h.runToCompletion()
	if len(h.runs) != 3 || len(h.failed) != 0 {
		t.Errorf("%d attempts, %d recorded failures; want 3 attempts and none recorded", len(h.runs), len(h.failed))
	}
}

// When the budget runs out the reaction is recorded as failed,
// budget_exhausted, once; the next reaction still runs.
func TestAfterReactions_BudgetRunsOut_RecordsFailed(t *testing.T) {
	h := newAfterHarness(t, alive(deskSnapshot("open", 5, 5)))
	h.always["checklist"] = failing()
	h.deliver(time.Minute, entering(6, "open", "working", "corr-7", 0))
	h.leave(2*time.Hour, 7, "working")
	h.runToCompletion()
	want := wf.AfterReactionFailedInput{Reaction: entryInput("checklist", 6, "corr-7", 1), Reason: "budget_exhausted"}
	if len(h.failed) != 1 || !reflect.DeepEqual(h.failed[0], want) {
		t.Fatalf("AfterReactionFailed calls = %+v, want %+v", h.failed, want)
	}
	last := h.runs[len(h.runs)-1]
	if last.In.ReactionID != "log" {
		t.Errorf("the last RunAfterReaction is %s, want log: the next reaction runs after the first gave up", last.In.ReactionID)
	}
	assertGaveUpAtTheBudget(t, "checklist", last.At.Add(-time.Minute))
}

// A final error (the request is malformed: a bug) is recorded at once.
func TestAfterReactions_AFinalErrorIsRecordedAtOnce(t *testing.T) {
	h := newAfterHarness(t, alive(deskSnapshot("waiting", 5, 5)))
	h.always["reopen"] = invalid()
	h.child(time.Minute, "remark", childID)
	h.leave(time.Hour, 6, "waiting")
	h.runToCompletion()
	if len(h.runs) != 1 || len(h.failed) != 1 || h.failed[0].Reason != "reaction_failed" {
		t.Errorf("runs %d, failed %+v; want one attempt, recorded as reaction_failed", len(h.runs), h.failed)
	}
}

// --- the run's life ---

// In a state without behavior, a pending reaction keeps the run until it
// is done; then the run completes.
func TestAfterReactions_ARunDoesNotCompleteWithOnePending(t *testing.T) {
	snap := deskSnapshot("waiting", 5, 5)
	snap.PendingReactions = []wf.PendingReaction{{ReactionID: "reopen", CauseItem: childID, CauseSeq: 1}}
	h := newAfterHarness(t, snap)
	h.errs["reopen"] = []error{errors.New("down"), errors.New("down"), errors.New("down")}
	h.runToCompletion()
	if len(h.runs) != 4 {
		t.Errorf("%d attempts, want 4: the run stayed until the reaction was done", len(h.runs))
	}
	if !h.env.Now().After(t0) {
		t.Errorf("the run ended at its start: it did not wait for the retries")
	}
}

// A desk without reactions (every Snapshot before this step) never calls
// the new activities, and is told of no child.
func TestAfterReactions_NoneDeclared_NoneRun(t *testing.T) {
	snap := deskSnapshot("working", 5, 5)
	snap.EnterReactions, snap.InsideReactions = nil, nil
	snap.Timers = map[string]wf.TimerSpec{"working": {After: time.Hour, To: "waiting"}}
	h := newAfterHarness(t, snap)
	h.deliver(time.Hour+time.Minute, transitionEvent(6, "working", "waiting")) // the timer's own write
	h.runToCompletion()
	if len(h.runs) != 0 || len(h.failed) != 0 {
		t.Errorf("RunAfterReaction was called for a Type that declares none: %+v", h.runs)
	}
	if len(h.fires) != 1 {
		t.Errorf("FireTimer calls = %d, want 1: the timer works as before", len(h.fires))
	}
}
