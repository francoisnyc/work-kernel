// Tests for Step 7: action hooks in the Item workflow, with fake
// activities, in Temporal's test environment (time skips forward).
//
// An action hook runs once per entry into its state, keyed by that entry
// (hook:<item>:<entered seq>:<hook>), and the activity passes the key to the
// connector. Unlike an enrich hook it is never dropped: once its state is
// entered it runs to completion, even if the Item has already left the
// state, and a new run picks up actions LoadItem reports as not done, for
// any entry. When it cannot complete, it records hook_failed with a reason:
// budget_exhausted, key_conflict (the connector made a second, different
// result for the same key) or action_failed (failed for good).
package workflow_test

import (
	"context"
	"reflect"
	"strconv"
	"testing"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"

	wf "github.com/francoisnyc/work-kernel/internal/workflow"
)

// actionHarness is hookHarness plus fakes for the two action activities.
type actionHarness struct {
	*hookHarness
	action    []hookCall
	failed    []wf.HookFailedInput
	failedAt  []hookCall
	actionErr map[string]error // by hook ID; returned on every attempt
}

func newActionHarness(t *testing.T, loaded wf.Snapshot) *actionHarness {
	t.Helper()
	h := &actionHarness{hookHarness: newHookHarness(t, loaded), actionErr: map[string]error{}}
	h.env.RegisterActivityWithOptions(func(ctx context.Context, in wf.HookInput) error {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.action = append(h.action, hookCall{In: in, At: h.env.Now()})
		return h.actionErr[in.HookID]
	}, activity.RegisterOptions{Name: wf.ActivityRunAction})
	h.env.RegisterActivityWithOptions(func(ctx context.Context, in wf.HookFailedInput) error {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.failed = append(h.failed, in)
		h.failedAt = append(h.failedAt, hookCall{In: in.Hook, At: h.env.Now()})
		return nil
	}, activity.RegisterOptions{Name: wf.ActivityHookFailed})
	return h
}

// invoicedSnapshot is an order whose `approved` state has one action hook,
// `invoice`, and whose `new` state has enrich hooks (orderSnapshot's).
func invoicedSnapshot(state string, itemSeq, enteredSeq int64) wf.Snapshot {
	s := orderSnapshot(state, itemSeq, enteredSeq)
	s.Actions = map[string][]string{"approved": {"invoice"}}
	return s
}

func actionInput(causeSeq int64, corr string, hop int) wf.HookInput {
	return wf.HookInput{
		ItemID: itemID, TypeID: "order", TypeVersion: 1, HookID: "invoice",
		CauseSeq: causeSeq, CorrelationID: corr, Hop: hop,
		// One key per entry: the connector uses it to recognise a retry.
		IdempotencyKey: "hook:" + itemID + ":" + strconv.FormatInt(causeSeq, 10) + ":invoice",
	}
}

func entering(seq int64, from, to, corr string, hop int) wf.Event {
	ev := transitionEvent(seq, from, to)
	ev.CorrelationID, ev.Hop = corr, hop
	return ev
}

// Entering approved runs its action once, one hop below the entering event.
func TestActions_EnteringTheState_RunsTheAction(t *testing.T) {
	h := newActionHarness(t, invoicedSnapshot("open", 5, 5))
	h.deliver(0, entering(6, "open", "approved", "corr-2", 1)) // arrives with the start
	h.runToCompletion()

	if len(h.action) != 1 || !reflect.DeepEqual(h.action[0].In, actionInput(6, "corr-2", 2)) {
		t.Errorf("RunAction calls = %+v\nwant one: %+v", h.action, actionInput(6, "corr-2", 2))
	}
	if len(h.failed) != 0 || len(h.enrich) != 0 {
		t.Errorf("HookFailed calls %+v, RunEnrich calls %+v; want none", h.failed, h.enrich)
	}
}

// The ticket is resolved before the action started (both events arrive
// together). An enrich hook would be dropped; the action still runs, caused
// by the entry into approved, and the run does not end before it has.
func TestActions_StateLeftBeforeTheActionStarts_StillRuns(t *testing.T) {
	h := newActionHarness(t, invoicedSnapshot("open", 5, 5))
	h.deliver(0, entering(6, "open", "approved", "corr-2", 1))
	h.deliver(0, entering(7, "approved", "done", "corr-3", 0))
	h.runToCompletion()

	if len(h.action) != 1 || !reflect.DeepEqual(h.action[0].In, actionInput(6, "corr-2", 2)) {
		t.Errorf("RunAction calls = %+v\nwant one, caused by the entry into approved: %+v", h.action, actionInput(6, "corr-2", 2))
	}
}

// Each entry into approved is its own business event: two entries, two
// actions, each keyed by its own entry.
func TestActions_ReenteringTheState_RunsAgain(t *testing.T) {
	h := newActionHarness(t, invoicedSnapshot("open", 5, 5))
	h.deliver(0, entering(6, "open", "approved", "corr-2", 1))
	h.deliver(0, entering(7, "approved", "open", "corr-3", 0))
	h.deliver(0, entering(8, "open", "approved", "corr-4", 1))
	h.runToCompletion()

	got := map[int64]wf.HookInput{}
	for _, c := range h.action {
		got[c.In.CauseSeq] = c.In
	}
	want := map[int64]wf.HookInput{6: actionInput(6, "corr-2", 2), 8: actionInput(8, "corr-4", 2)}
	if len(h.action) != 2 || !reflect.DeepEqual(got, want) {
		t.Errorf("RunAction calls = %+v\nwant one per entry: %+v", h.action, want)
	}
}

// A run started after a crash loads the Item where it is now (done), and
// LoadItem reports the action not yet completed for an earlier entry. The
// new run runs it, with that entry as its cause and key.
func TestActions_LoadedPending_RunsForAnEarlierEntry(t *testing.T) {
	s := invoicedSnapshot("done", 9, 9)
	s.PendingActions = []wf.PendingAction{{HookID: "invoice", CauseSeq: 6, CorrelationID: "corr-2", Hop: 1}}
	h := newActionHarness(t, s)
	h.runToCompletion()

	if len(h.action) != 1 || !reflect.DeepEqual(h.action[0].In, actionInput(6, "corr-2", 2)) {
		t.Errorf("RunAction calls = %+v\nwant one for the loaded entry: %+v", h.action, actionInput(6, "corr-2", 2))
	}
}

// When the budget runs out, the action records hook_failed with reason
// budget_exhausted, for the same hook and entry. There is no fallback.
func TestActions_BudgetRunsOut_RecordsHookFailed(t *testing.T) {
	h := newActionHarness(t, invoicedSnapshot("open", 5, 5))
	h.actionErr["invoice"] = failing()
	h.deliver(0, entering(6, "open", "approved", "corr-2", 1))
	h.runToCompletion()

	want := wf.HookFailedInput{Hook: actionInput(6, "corr-2", 2), Reason: "budget_exhausted"}
	if len(h.failed) != 1 || !reflect.DeepEqual(h.failed[0], want) {
		t.Fatalf("HookFailed calls = %+v\nwant one: %+v", h.failed, want)
	}
	assertGaveUpAtTheBudget(t, "hook_failed", h.failedAt[0].At)
	if len(h.fallback) != 0 {
		t.Errorf("EnrichFallback calls = %+v, want none: an action has no fallback", h.fallback)
	}
}

// A final failure records hook_failed at once, with the reason its error
// type gives: a key conflict (the connector or the kernel saw the same key
// with a different request), or any other permanent failure.
func TestActions_FinalErrors_RecordHookFailedAtOnce(t *testing.T) {
	for name, c := range map[string]struct {
		err    error
		reason string
	}{
		"key conflict": {temporal.NewNonRetryableApplicationError("second invoice for one key", wf.ErrTypeKeyConflict, nil),
			"key_conflict"},
		"failed for good": {temporal.NewNonRetryableApplicationError("unknown customer", wf.ErrTypeHookFailed, nil),
			"action_failed"},
	} {
		t.Run(name, func(t *testing.T) {
			h := newActionHarness(t, invoicedSnapshot("open", 5, 5))
			h.actionErr["invoice"] = c.err
			h.deliver(0, entering(6, "open", "approved", "corr-2", 1))
			h.runToCompletion()

			if len(h.action) != 1 {
				t.Errorf("RunAction calls = %d, want 1: a final error is not retried", len(h.action))
			}
			want := wf.HookFailedInput{Hook: actionInput(6, "corr-2", 2), Reason: c.reason}
			if len(h.failed) != 1 || !reflect.DeepEqual(h.failed[0], want) {
				t.Fatalf("HookFailed calls = %+v\nwant one: %+v", h.failed, want)
			}
			if at := h.failedAt[0].At; at.After(t0.Add(margin)) {
				t.Errorf("hook_failed at %v, want at once (by %v)", at, t0.Add(margin))
			}
		})
	}
}
