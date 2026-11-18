// Tests for the consolidation after Step 2, item 1: a held reaction whose
// target is held by its own open hold. src's held reaction take (a -> b)
// adds f_k to the n of the pot it links to; pot's check on open -> locked
// reads n, so while pot's hold is open, n is frozen and take's write is
// refused as held. That must not leave src's hold open: RunReaction
// retries within its budget, and when the budget runs out src's hold
// resolves as error (budget_exhausted).
//
//   - The activity on a real kernel (no worker): RunReaction returns a
//     retryable error while pot's hold is open (it returned nil, leaving
//     src's hold open forever).
//   - The workflow: a RunReaction that keeps failing so resolves src's hold
//     as error, budget_exhausted, at the budget.
//   - End to end: pot's check answers within src's budget; once pot's hold
//     closes, take commits and src's hold is allowed.
package workflow_test

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.temporal.io/sdk/temporal"

	"github.com/francoisnyc/work-kernel/internal/connstub"
	"github.com/francoisnyc/work-kernel/internal/hooks"
	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
	"github.com/francoisnyc/work-kernel/internal/typedef"
	wf "github.com/francoisnyc/work-kernel/internal/workflow"
)

const potSrc = `
id: pot
version: 1
initial_state: open
fields:
  - {id: f_customer,    type: string}
  - {id: n,             type: int}
  - {id: f_cost_center, type: string}
states:
  - {id: open,   category: active}
  - {id: locked, category: done}
transitions:
  - {from: open, to: locked}
hooks:
  - {id: lock_check, on: transition, from: open, to: locked, kind: check, action: erp.check_credit,
     reads: [f_customer, n], writes: [f_cost_center]}
accepts_reactions:
  src:take: [n]
  src:give: [n]
`

const srcSrc = `
id: src
version: 1
initial_state: a
fields:
  - {id: f_k, type: int}
states:
  - {id: a, category: todo}
  - {id: b, category: done}
  - {id: c, category: canceled}
transitions:
  - {from: a, to: b}
  - {from: b, to: c}
links:
  - {name: pot, inverse: srcs, targets: [pot], max: one}
reactions:
  - {id: take, kind: held, on: enter, state: b, from: [a], link: pot, add: {n: f_k}}
  - {id: give, kind: after, on: enter, state: c, from: [b], undoes: take}
`

func registerInline(t *testing.T, src string) {
	t.Helper()
	ty, err := typedef.Parse([]byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := itemservice.New(pool).RegisterType(serverCtx(t), ty); err != nil {
		t.Fatalf("register %s: %v", ty.ID, err)
	}
}

// heldPot: a pot (for customer, n 0) whose open -> locked is held by its
// check, and a src (f_k 3) linked to it whose a -> b is held by take. It
// returns both and src's hold.
func heldPot(t *testing.T, k *itemservice.Service, customer string) (ana item.Principal, pot, src uuid.UUID, srcAsk itemservice.TransitionResult) {
	t.Helper()
	registerInline(t, potSrc)
	registerInline(t, srcSrc)
	ana = item.Principal{Actor: "user:ana-" + uuid.NewString()[:8]}
	create := func(typeID string, fields map[string]any) uuid.UUID {
		res, err := k.Create(serverCtx(t), itemservice.CreateRequest{IdempotencyKey: "key-" + uuid.NewString(),
			Principal: ana, TypeID: typeID, TypeVersion: 1, Fields: fields})
		if err != nil {
			t.Fatalf("create %s: %v", typeID, err)
		}
		return res.ItemID
	}
	pot = create("pot", map[string]any{"f_customer": customer, "n": 0})
	src = create("src", map[string]any{"f_k": 3})
	if _, err := k.Link(serverCtx(t), itemservice.LinkRequest{IdempotencyKey: "key-" + uuid.NewString(),
		Principal: ana, SourceID: src, LinkType: "pot", TargetID: pot}); err != nil {
		t.Fatalf("link: %v", err)
	}
	ask := func(id uuid.UUID, to string) itemservice.TransitionResult {
		res, err := k.Transition(serverCtx(t), itemservice.TransitionRequest{IdempotencyKey: "key-" + uuid.NewString(),
			Principal: ana, ItemID: id, ToState: to})
		if err != nil || res.Outcome != itemservice.OutcomePending {
			t.Fatalf("ask %s = %+v, %v; want pending", to, res, err)
		}
		return res
	}
	ask(pot, "locked")
	return ana, pot, src, ask(src, "b")
}

// No worker: pot's hold stays open. take is refused as held, which must
// come back as an error Temporal retries, never as nil.
func TestActivities_RunReaction_TargetHeld_IsRetryable(t *testing.T) {
	k := itemservice.New(pool)
	ana, _, src, ask := heldPot(t, k, "cust-"+uuid.NewString()[:8])
	a := &wf.Activities{Items: k}
	err := a.RunReaction(serverCtx(t), wf.ReactionInput{ItemID: src.String(), TypeID: "src", TypeVersion: 1,
		ReactionID: "take", HoldID: ask.HoldID.String(), CauseSeq: ask.ItemSeq, Hop: 1})
	var appErr *temporal.ApplicationError
	if err == nil || (errors.As(err, &appErr) && appErr.NonRetryable()) {
		t.Fatalf("RunReaction = %v; want a retryable error while the pot's hold is open", err)
	}
	if it, err := k.Get(serverCtx(t), ana, src); err != nil || it.Hold == nil || it.Hold.Status != item.HoldOpen {
		t.Errorf("src's hold = %+v, %v; want still open (to be retried)", it.Hold, err)
	}
}

// The pot's hold never closes: every RunReaction attempt fails retryably,
// so src's hold resolves as error, budget_exhausted, at the budget.
func TestReactions_TargetHeldUntilTheBudget_ResolvesAsError(t *testing.T) {
	snap := wf.Snapshot{TypeID: "src", TypeVersion: 1, Timers: map[string]wf.TimerSpec{},
		HeldReactions: map[string]string{"a->b": "take"}, Undos: map[string]string{"take": "give"},
		UndoOnEntry: map[string]string{"b->c": "give"}, HookBudget: budget,
		State: "a", LastAppliedSeq: 3, EnteredSeq: 1, EnteredAt: t0, CorrelationID: "corr-1"}
	h := newReactionHarness(t, snap)
	h.reactionErr = errors.New("pot is held") // what the fixed activity returns: retryable
	h.deliver(0, wf.Event{Seq: 4, Op: "transition_requested", From: "a", RequestedTo: "b", HoldID: hold1, CorrelationID: "corr-7"})
	h.deliver(time.Hour, wf.Event{Seq: 5, Op: "transition_failed", From: "a", HoldID: hold1, CorrelationID: "corr-7", Hop: 1})
	h.runToCompletion()
	want := []wf.ResolveReactionInput{{Reason: "budget_exhausted", Reaction: wf.ReactionInput{ItemID: itemID, TypeID: "src",
		TypeVersion: 1, ReactionID: "take", HoldID: hold1, CauseSeq: 4, CorrelationID: "corr-7", Hop: 1}}}
	if !reflect.DeepEqual(h.resolves, want) {
		t.Errorf("ResolveReaction calls = %+v\nwant %+v", h.resolves, want)
	}
	if len(h.resolves) == 1 && len(h.reactions) < 2 {
		t.Errorf("RunReaction attempts = %d, want retries within the budget", len(h.reactions))
	}
}

// End to end: pot's check answers after a second, well within src's
// budget; take is refused as held meanwhile, retried, then commits: src
// is in b and the pot's n holds f_k.
func TestE2E_TargetHeld_ClosesWithinTheBudget_SourceAllowed(t *testing.T) {
	h := newHookStack(t)
	h.erp.SetCustomer(h.customer, connstub.Customer{CreditLimit: 1000, Latency: time.Second})
	h.stopWorker()
	h.worker = wf.NewWorker(tc, h.queue, h.k, wf.WorkerOptions{
		Connectors: hooks.NewConnectors(h.crm.URL, h.erp.URL), HookBudget: 15 * time.Second,
	})
	if err := h.worker.Start(); err != nil {
		t.Fatalf("start worker: %v", err)
	}
	ana, pot, src, _ := heldPot(t, h.k, h.customer)
	get := func(id uuid.UUID) item.Item {
		it, err := h.k.Get(serverCtx(t), ana, id)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		return it
	}
	deadline := time.Now().Add(15 * time.Second)
	for get(src).State != "b" {
		if time.Now().After(deadline) {
			s, p := get(src), get(pot)
			t.Fatalf("after 15 s: src %s, hold %+v; pot %s, hold %+v; want src allowed into b once the pot's hold closed",
				s.State, s.Hold, p.State, p.Hold)
		}
		time.Sleep(100 * time.Millisecond)
	}
	p := get(pot)
	if p.State != "locked" || p.Fields["n"] != float64(3) {
		t.Errorf("pot = %s, n %v; want locked, n 3", p.State, p.Fields["n"])
	}
}
