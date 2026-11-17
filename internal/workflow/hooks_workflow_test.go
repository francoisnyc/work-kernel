// Tests for Step 6, part 2: hooks in the Item workflow, with fake
// activities, in Temporal's test environment (time skips forward).
//
// The workflow schedules hooks; it never sees field values. Each hook is an
// activity that reads its inputs from the item service, calls out, and
// writes back through Internal() itself. The workflow bounds each hook with a
// budget (ScheduleToClose). When an enrich hook's budget runs out, its
// fallback is written; when a check's runs out, the hold resolves as error.
package workflow_test

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"strconv"
	"testing"
	"time"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	wf "github.com/francoisnyc/work-kernel/internal/workflow"
)

const (
	holdID = "0193a1b2-0000-7000-8000-0000000000a1"
	budget = 10 * time.Minute
	// margin is how late after its budget a hook may still give up.
	margin = 5 * time.Second
)

// assertGaveUpAtTheBudget checks when a hook gave up, for a hook started at
// the start. Temporal schedules no retry that would start after the budget
// (ScheduleToClose), so a hook gives up as soon as its next attempt cannot
// fit: up to one retry interval early, never later than the budget.
func assertGaveUpAtTheBudget(t *testing.T, what string, at time.Time) {
	t.Helper()
	earliest, latest := t0.Add(budget-wf.MaxRetryInterval), t0.Add(budget+margin)
	if at.Before(earliest) || at.After(latest) {
		t.Errorf("%s at %v, want between %v and %v (the budget, less one retry interval, plus %v)",
			what, at, earliest, latest, margin)
	}
}

// orderSnapshot is what LoadItem reports for an order: entering `new` runs
// two enrich hooks (get_plan has a fallback), and open -> approved is
// checked by `check`. No timers unless a test adds one.
func orderSnapshot(state string, itemSeq, enteredSeq int64) wf.Snapshot {
	return wf.Snapshot{
		TypeID: "order", TypeVersion: 1, Timers: map[string]wf.TimerSpec{},
		Enrich: map[string][]wf.HookSpec{
			"new": {{ID: "get_plan", HasFallback: true}, {ID: "get_status"}},
		},
		Checks:     map[string]string{"open->approved": "check"},
		HookBudget: budget,
		State:      state, LastAppliedSeq: itemSeq,
		EnteredSeq: enteredSeq, EnteredAt: t0,
		CorrelationID: "corr-1", Hop: 2,
	}
}

type hookCall struct {
	In wf.HookInput
	At time.Time
}

type resolveCall struct {
	In wf.ResolveInput
	At time.Time
}

// hookHarness is harness plus fakes for the four hook activities.
type hookHarness struct {
	*harness
	enrich, fallback, check []hookCall
	resolves                []resolveCall
	enrichErr, checkErr     map[string]error // by hook ID; returned on every attempt
}

func newHookHarness(t *testing.T, loaded wf.Snapshot) *hookHarness {
	t.Helper()
	h := &hookHarness{harness: newHarness(t, loaded), enrichErr: map[string]error{}, checkErr: map[string]error{}}
	record := func(into *[]hookCall, errs map[string]error) func(context.Context, wf.HookInput) error {
		return func(ctx context.Context, in wf.HookInput) error {
			h.mu.Lock()
			defer h.mu.Unlock()
			*into = append(*into, hookCall{In: in, At: h.env.Now()})
			if errs != nil {
				return errs[in.HookID]
			}
			return nil
		}
	}
	h.env.RegisterActivityWithOptions(record(&h.enrich, h.enrichErr), activity.RegisterOptions{Name: wf.ActivityRunEnrich})
	h.env.RegisterActivityWithOptions(record(&h.fallback, nil), activity.RegisterOptions{Name: wf.ActivityEnrichFallback})
	h.env.RegisterActivityWithOptions(record(&h.check, h.checkErr), activity.RegisterOptions{Name: wf.ActivityRunCheck})
	h.env.RegisterActivityWithOptions(func(ctx context.Context, in wf.ResolveInput) error {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.resolves = append(h.resolves, resolveCall{In: in, At: h.env.Now()})
		return nil
	}, activity.RegisterOptions{Name: wf.ActivityResolveHold})
	return h
}

func requestEvent(seq int64) wf.Event {
	return wf.Event{Seq: seq, Op: "transition_requested", From: "open", RequestedTo: "approved",
		HoldID: holdID, CorrelationID: "corr-7"}
}

// closing returns an event that closes the hold: allow (a transition into
// approved), deny, failure, or the escape.
func closing(seq int64, op, to string) wf.Event {
	return wf.Event{Seq: seq, Op: op, From: "open", To: to, HoldID: holdID}
}

func checkInput(causeSeq int64, corr string, hop int) wf.HookInput {
	return wf.HookInput{
		ItemID: itemID, TypeID: "order", TypeVersion: 1, HookID: "check",
		CauseSeq: causeSeq, HoldID: holdID, CorrelationID: corr, Hop: hop,
		IdempotencyKey: "hold:" + holdID, // one resolution per hold, whoever writes it
	}
}

func failing() error { return errors.New("connector is down") }

// --- checks ---

// A transition request runs the check once, as a hook one hop below the
// request, keyed by the hold. The hook writes the resolution itself; the
// workflow then hears it as an event like any other.
func TestHooks_TransitionRequest_RunsTheCheck(t *testing.T) {
	h := newHookHarness(t, orderSnapshot("open", 5, 5))
	h.deliver(0, requestEvent(6)) // arrives with the start
	allowed := h.deliver(2*time.Minute, closing(7, "transition", "approved"))
	h.runToCompletion()

	if len(h.check) != 1 {
		t.Fatalf("RunCheck calls = %+v, want one", h.check)
	}
	if want := checkInput(6, "corr-7", 1); !reflect.DeepEqual(h.check[0].In, want) {
		t.Errorf("RunCheck input = %+v\nwant %+v", h.check[0].In, want)
	}
	if !h.check[0].At.Equal(t0) {
		t.Errorf("RunCheck at %v, want at the request (the start)", h.check[0].At)
	}
	if len(h.resolves) != 0 {
		t.Errorf("ResolveHold calls = %+v, want none: the check resolved the hold itself", h.resolves)
	}
	h.wantApplied("the allowed transition", allowed, true)
}

// An open hold keeps the workflow alive, even in a state with no other
// behavior, until an event closes it.
func TestHooks_AnOpenHoldKeepsTheWorkflow(t *testing.T) {
	h := newHookHarness(t, orderSnapshot("open", 5, 5))
	h.deliver(0, requestEvent(6)) // arrives with the start
	denied := h.deliver(5*time.Hour, closing(7, "transition_denied", ""))
	h.runToCompletion()

	h.wantApplied("the denial, five hours later", denied, true)
	if now := h.env.Now(); now.Before(t0.Add(5 * time.Hour)) {
		t.Errorf("workflow ended at %v, before the hold was closed", now)
	}
}

// When the check's budget runs out (the connector keeps failing), the hold
// resolves as error, with the same key the check would have used, so the
// hold is resolved at most once.
func TestHooks_CheckBudgetRunsOut_ResolvesAsError(t *testing.T) {
	h := newHookHarness(t, orderSnapshot("open", 5, 5))
	h.checkErr["check"] = failing()
	h.deliver(0, requestEvent(6)) // arrives with the start
	failed := h.deliver(time.Hour, closing(7, "transition_failed", ""))
	h.runToCompletion()

	if len(h.check) < 2 {
		t.Errorf("RunCheck calls = %d, want retries within the budget", len(h.check))
	}
	for _, c := range h.check {
		if c.At.After(t0.Add(budget)) {
			t.Errorf("RunCheck attempt at %v, after the budget ran out", c.At)
		}
	}
	if len(h.resolves) != 1 {
		t.Fatalf("ResolveHold calls = %+v, want one", h.resolves)
	}
	want := wf.ResolveInput{Hook: checkInput(6, "corr-7", 1), Verdict: "error", Reason: "budget_exhausted"}
	if got := h.resolves[0]; !reflect.DeepEqual(got.In, want) {
		t.Errorf("ResolveHold input = %+v\nwant %+v", got.In, want)
	}
	assertGaveUpAtTheBudget(t, "ResolveHold", h.resolves[0].At)
	h.wantApplied("transition_failed", failed, true)
}

// A check that fails for good (an unknown customer, say) does not wait out
// its budget: the hold resolves as error at once, with reason check_failed.
func TestHooks_CheckFailsForGood_ResolvesAsErrorAtOnce(t *testing.T) {
	h := newHookHarness(t, orderSnapshot("open", 5, 5))
	h.checkErr["check"] = temporal.NewNonRetryableApplicationError("unknown customer", wf.ErrTypeHookFailed, nil)
	h.deliver(0, requestEvent(6)) // arrives with the start
	h.deliver(time.Hour, closing(7, "transition_failed", ""))
	h.runToCompletion()

	if len(h.check) != 1 {
		t.Errorf("RunCheck calls = %d, want 1: a permanent failure is not retried", len(h.check))
	}
	want := wf.ResolveInput{Hook: checkInput(6, "corr-7", 1), Verdict: "error", Reason: "check_failed"}
	if len(h.resolves) != 1 || !reflect.DeepEqual(h.resolves[0].In, want) || !h.resolves[0].At.Equal(t0) {
		t.Errorf("ResolveHold calls = %+v, want one at once: %+v", h.resolves, want)
	}
}

// The escape closes the hold: the check is called off, and nothing resolves
// the hold afterwards.
func TestHooks_EscapeCallsOffTheCheck(t *testing.T) {
	h := newHookHarness(t, orderSnapshot("open", 5, 5))
	h.checkErr["check"] = failing()
	h.deliver(0, requestEvent(6)) // arrives with the start
	escaped := h.deliver(3*time.Minute, closing(7, "transition", "dropped"))
	h.runToCompletion()

	h.wantApplied("the escape", escaped, true)
	if len(h.resolves) != 0 {
		t.Errorf("ResolveHold calls = %+v, want none after the escape", h.resolves)
	}
	for _, c := range h.check {
		if c.At.After(t0.Add(3 * time.Minute)) {
			t.Errorf("RunCheck attempt at %v, after the escape", c.At)
		}
	}
	if now := h.env.Now(); now.After(t0.Add(budget)) {
		t.Errorf("workflow ended at %v: it waited out the canceled check's budget", now)
	}
}

// --- enrich hooks ---

func sortedByHook(calls []hookCall) []hookCall {
	out := append([]hookCall(nil), calls...)
	sort.Slice(out, func(i, j int) bool { return out[i].In.HookID < out[j].In.HookID })
	return out
}

func enrichInput(hookID string, causeSeq int64, corr string, hop int) wf.HookInput {
	return wf.HookInput{
		ItemID: itemID, TypeID: "order", TypeVersion: 1, HookID: hookID,
		CauseSeq: causeSeq, CorrelationID: corr, Hop: hop,
		// One write per hook per entry, whether the hook's own or its fallback.
		IdempotencyKey: "hook:" + itemID + ":" + strconv.FormatInt(causeSeq, 10) + ":" + hookID,
	}
}

// Entering a state runs each of its enrich hooks once, caused by the event
// that entered it. They never hold the Item.
func TestHooks_EnteringAState_RunsItsEnrichHooks(t *testing.T) {
	h := newHookHarness(t, orderSnapshot("open", 5, 5))
	enter := transitionEvent(6, "open", "new")
	enter.CorrelationID, enter.Hop = "corr-2", 0
	h.deliver(0, enter) // arrives with the start
	h.runToCompletion()

	got := sortedByHook(h.enrich)
	want := []wf.HookInput{enrichInput("get_plan", 6, "corr-2", 1), enrichInput("get_status", 6, "corr-2", 1)}
	if len(got) != 2 || !reflect.DeepEqual(got[0].In, want[0]) || !reflect.DeepEqual(got[1].In, want[1]) {
		t.Errorf("RunEnrich calls = %+v\nwant %+v", got, want)
	}
	if len(h.fallback) != 0 {
		t.Errorf("EnrichFallback calls = %+v, want none", h.fallback)
	}
}

// When an enrich hook's budget runs out, its fallback is written (same key:
// at most one write per hook and entry). A hook without a fallback leaves
// its fields unset: nothing is called.
func TestHooks_EnrichBudgetRunsOut_WritesTheFallback(t *testing.T) {
	h := newHookHarness(t, orderSnapshot("open", 5, 5))
	h.enrichErr["get_plan"] = failing()
	h.enrichErr["get_status"] = failing()
	h.deliver(0, transitionEvent(6, "open", "new")) // arrives with the start
	h.runToCompletion()

	if len(h.fallback) != 1 {
		t.Fatalf("EnrichFallback calls = %+v, want one, for get_plan", h.fallback)
	}
	if want := enrichInput("get_plan", 6, "", 1); !reflect.DeepEqual(h.fallback[0].In, want) {
		t.Errorf("EnrichFallback input = %+v\nwant %+v", h.fallback[0].In, want)
	}
	assertGaveUpAtTheBudget(t, "the fallback", h.fallback[0].At)
}

// --- a run that starts with work already pending ---

// A run started by the very event that needs a hook loads that event as
// already applied, so the event alone cannot trigger the hook. LoadItem
// reports the pending work instead: enrich hooks not yet run for the current
// entry, and an open hold.
func TestHooks_LoadedWithPendingWork(t *testing.T) {
	t.Run("an enrich hook not yet run", func(t *testing.T) {
		s := orderSnapshot("new", 5, 5)
		s.PendingEnrich = []string{"get_status"}
		h := newHookHarness(t, s)
		h.runToCompletion()

		if len(h.enrich) != 1 {
			t.Fatalf("RunEnrich calls = %+v, want one, for get_status", h.enrich)
		}
		if want := enrichInput("get_status", 5, "corr-1", 3); !reflect.DeepEqual(h.enrich[0].In, want) {
			t.Errorf("RunEnrich input = %+v\nwant %+v", h.enrich[0].In, want)
		}
	})
	t.Run("an open hold", func(t *testing.T) {
		s := orderSnapshot("open", 6, 5)
		s.Hold = &wf.OpenHold{ID: holdID, Hook: "check", From: "open", RequestedTo: "approved",
			RequestedSeq: 6, CorrelationID: "corr-7", Hop: 0}
		h := newHookHarness(t, s)
		denied := h.deliver(time.Hour, closing(7, "transition_denied", ""))
		h.runToCompletion()

		if len(h.check) != 1 || !reflect.DeepEqual(h.check[0].In, checkInput(6, "corr-7", 1)) {
			t.Errorf("RunCheck calls = %+v, want one for the loaded hold", h.check)
		}
		h.wantApplied("the denial", denied, true)
	})
}

// --- the timer meets a hold ---

func held() error {
	return temporal.NewNonRetryableApplicationError("held by an open hold", wf.ErrTypeHeld, nil)
}

// The old rule, kept by a run started before Step 2, part 3's change
// (timer-waits-for-hold at DefaultVersion): a timer whose move is refused
// because the Item is held is spent: unlike stale, it does not fire again
// after the next event. The replay test proves old histories replay; this
// proves such a run keeps the old rule when its held timer fires after the
// upgrade. TestTimer_HeldIsNotSpent is the new rule.
func TestHooks_TimerRefusedAsHeld_OldVersionIsSpent(t *testing.T) {
	s := orderSnapshot("open", 5, 5)
	s.Timers = map[string]wf.TimerSpec{"open": {After: time.Hour, To: "new"}}
	h := newHookHarness(t, s)
	h.env.OnGetVersion("timer-waits-for-hold", workflow.DefaultVersion, 1).Return(workflow.DefaultVersion)
	h.fireErrs = []error{held()}
	h.deliver(2*time.Hour, updateEvent(6))
	done := h.deliver(3*time.Hour, transitionEvent(7, "open", "approved"))
	h.runToCompletion()

	if len(h.fires) != 1 {
		t.Errorf("FireTimer calls = %+v, want one: a held timer is spent", h.fires)
	}
	h.wantApplied("the later transition", done, true)
}

// The common path for approve: the run is already alive (the Item sits in a
// state with a timer) when the transition request arrives, as a plain
// Update. The check runs at once, the hold resolves, and the timer is
// handled as if the hold had not been there: a denial leaves the Item in
// the state, so the timer fires on time with the latest item_seq; an
// allowed move leaves the state, so the timer is canceled.
func TestHooks_RequestReachesARunningWorkflow(t *testing.T) {
	timed := func() wf.Snapshot {
		s := orderSnapshot("open", 5, 5)
		s.Timers = map[string]wf.TimerSpec{"open": {After: 4 * time.Hour, To: "new"}}
		return s
	}

	t.Run("denied: the timer still fires", func(t *testing.T) {
		h := newHookHarness(t, timed())
		req := h.deliver(time.Hour, requestEvent(6))
		denied := h.deliver(2*time.Hour, closing(7, "transition_denied", ""))
		back := h.deliver(4*time.Hour+time.Minute, transitionEvent(8, "open", "new")) // the timer's own write
		h.runToCompletion()

		h.wantApplied("the request", req, true)
		h.wantApplied("the denial", denied, true)
		h.wantApplied("the timer's transition", back, true)
		if len(h.check) != 1 || !h.check[0].At.Equal(t0.Add(time.Hour)) ||
			!reflect.DeepEqual(h.check[0].In, checkInput(6, "corr-7", 1)) {
			t.Errorf("RunCheck calls = %+v, want one at +1h for the request", h.check)
		}
		if len(h.fires) != 1 {
			t.Fatalf("FireTimer calls = %+v, want one", h.fires)
		}
		f := h.fires[0]
		if !f.At.Equal(t0.Add(4*time.Hour)) || f.In.ExpectedItemSeq != 7 || f.In.EnteredSeq != 5 {
			t.Errorf("FireTimer = %+v, want +4h (from the entry, not the hold), expected seq 7, entered seq 5", f)
		}
		// Entering `new` runs its enrich hooks, caused by the timer's event.
		if got := sortedByHook(h.enrich); len(got) != 2 || got[0].In.CauseSeq != 8 {
			t.Errorf("RunEnrich calls = %+v, want two, caused by event 8", got)
		}
	})

	t.Run("allowed: the move cancels the timer", func(t *testing.T) {
		h := newHookHarness(t, timed())
		h.deliver(time.Hour, requestEvent(6))
		allowed := h.deliver(2*time.Hour, closing(7, "transition", "approved"))
		h.runToCompletion()

		h.wantApplied("the allowed transition", allowed, true)
		if len(h.check) != 1 {
			t.Errorf("RunCheck calls = %+v, want one", h.check)
		}
		if len(h.fires) != 0 {
			t.Errorf("FireTimer calls = %+v, want none: the Item left the timed state", h.fires)
		}
		if now := h.env.Now(); now.After(t0.Add(3 * time.Hour)) {
			t.Errorf("workflow ended at %v: it waited for a canceled timer", now)
		}
	})
}
