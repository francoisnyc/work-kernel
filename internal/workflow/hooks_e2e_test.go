// End-to-end tests for Step 6, part 2: hooks, holds and the bounded wait,
// with the real kernel on Postgres, the real relay, the workflow on a
// Temporal dev server, a worker in this process, and stub HTTP servers for
// the CRM and the ERP.
//
// They include the step's "done when": a stale prefetched fact is caught by
// the hold; kernel writes do not wait on a stub outage or on the workers;
// every hook write carries its attribution, and the chain can be rebuilt.
// The latency numbers (p95 under 3 s with D) come from cmd/bench.
package workflow_test

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/connstub"
	"github.com/francoisnyc/work-kernel/internal/hooks"
	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
	"github.com/francoisnyc/work-kernel/internal/typedef"
	wf "github.com/francoisnyc/work-kernel/internal/workflow"
)

// testBudget is the hooks' budget in these tests: long enough for a stub
// answering in milliseconds, short enough to wait out in a test.
const testBudget = 2 * time.Second

// hookStack is a stack whose worker runs hooks against stub servers.
type hookStack struct {
	*stack
	crm, erp *connstub.Server
	customer string // a customer no other test uses
}

func newHookStack(t *testing.T) *hookStack {
	t.Helper()
	if _, err := pool.Exec(serverCtx(t), `update outbox set delivered_at = now() where delivered_at is null`); err != nil {
		t.Fatalf("clear outbox: %v", err)
	}
	registerV3(t)
	h := &hookStack{
		stack:    &stack{t: t, k: itemservice.New(pool), queue: "items-" + uuid.NewString()},
		crm:      connstub.NewCRM(),
		erp:      connstub.NewERP(),
		customer: "cust-" + uuid.NewString()[:8],
	}
	h.crm.SetCustomer(h.customer, connstub.Customer{Plan: "enterprise"})
	h.erp.SetCustomer(h.customer, connstub.Customer{CreditLimit: 1000})
	h.startHookWorker()
	h.startRelay()
	t.Cleanup(func() {
		h.stopRelay()
		h.stopWorker()
		h.crm.Close()
		h.erp.Close()
	})
	return h
}

func (h *hookStack) startHookWorker() {
	h.t.Helper()
	h.worker = wf.NewWorker(tc, h.queue, h.k, wf.WorkerOptions{
		Connectors: hooks.NewConnectors(h.crm.URL, h.erp.URL),
		HookBudget: testBudget,
	})
	if err := h.worker.Start(); err != nil {
		h.t.Fatalf("start worker: %v", err)
	}
}

func registerV3(t *testing.T) {
	t.Helper()
	data, err := os.ReadFile("../../types/ticket.v3.yaml")
	if err != nil {
		t.Fatalf("read ticket v3: %v", err)
	}
	ty, err := typedef.Parse(data)
	if err != nil {
		t.Fatalf("parse ticket v3: %v", err)
	}
	if err := itemservice.New(pool).RegisterType(serverCtx(t), ty); err != nil {
		t.Fatalf("register ticket v3: %v", err)
	}
}

// approver returns a new user with the approver role.
func approver(t *testing.T) item.Principal {
	t.Helper()
	p := item.Principal{Actor: "user:ana-" + uuid.NewString()[:8]}
	if _, err := pool.Exec(serverCtx(t),
		`insert into principal_roles (namespace_id, principal, role) values ('default', $1, 'approver')`,
		p.Actor); err != nil {
		t.Fatalf("grant approver: %v", err)
	}
	return p
}

// newTicket creates a v3 ticket for the stack's customer, in triage.
func (h *hookStack) newTicket(p item.Principal) uuid.UUID {
	h.t.Helper()
	res, err := h.k.Create(serverCtx(h.t), itemservice.CreateRequest{
		IdempotencyKey: "key-" + uuid.NewString(), Principal: p, TypeID: "ticket", TypeVersion: 3,
		Fields: map[string]any{
			"f_title": "Onboarding", "f_customer_id": h.customer, "f_amount": 100, "f_assignee": "user-bob",
		},
		Attribution: item.Attribution{CorrelationID: "corr-" + uuid.NewString()[:8]},
	})
	if err != nil {
		h.t.Fatalf("create ticket v3: %v", err)
	}
	return res.ItemID
}

func (h *hookStack) fields(p item.Principal, id uuid.UUID) map[string]any {
	h.t.Helper()
	it, err := h.k.Get(serverCtx(h.t), p, id)
	if err != nil {
		h.t.Fatalf("Get: %v", err)
	}
	return it.Fields
}

func (h *hookStack) hold(p item.Principal, id uuid.UUID) (string, *item.Hold) {
	h.t.Helper()
	it, err := h.k.Get(serverCtx(h.t), p, id)
	if err != nil {
		h.t.Fatalf("Get: %v", err)
	}
	return it.State, it.Hold
}

// prefetched waits until both enrich hooks have written.
func (h *hookStack) prefetched(p item.Principal, id uuid.UUID) {
	h.t.Helper()
	eventually(h.t, 15*time.Second, "f_plan and f_on_hold prefetched", func() bool {
		f := h.fields(p, id)
		return f["f_plan"] != nil && f["f_on_hold"] != nil
	})
}

// toInProgress moves the ticket triage -> open -> in_progress as p.
func (h *hookStack) toInProgress(p item.Principal, id uuid.UUID) {
	h.t.Helper()
	for _, to := range []string{"open", "in_progress"} {
		if _, err := h.k.Transition(serverCtx(h.t), itemservice.TransitionRequest{
			IdempotencyKey: "key-" + uuid.NewString(), Principal: p, ItemID: id, ToState: to,
		}); err != nil {
			h.t.Fatalf("transition to %s: %v", to, err)
		}
	}
}

func (h *hookStack) approve(p item.Principal, id uuid.UUID, wait time.Duration) (itemservice.TransitionResult, time.Duration) {
	h.t.Helper()
	start := time.Now()
	res, err := h.k.Transition(serverCtx(h.t), itemservice.TransitionRequest{
		IdempotencyKey: "key-" + uuid.NewString(), Principal: p, ItemID: id, ToState: "approved", Wait: wait,
	})
	if err != nil {
		h.t.Fatalf("approve: %v", err)
	}
	return res, time.Since(start)
}

// --- prefetch ---

// Entering triage runs both enrich hooks. Each writes its field as the hook,
// on behalf of the human who created the ticket, caused by the create.
func TestE2E_Prefetch_FillsPlanAndOnHold(t *testing.T) {
	h := newHookStack(t)
	ana := approver(t)
	id := h.newTicket(ana)
	h.prefetched(ana, id)

	f := h.fields(ana, id)
	if f["f_plan"] != "enterprise" || f["f_on_hold"] != false {
		t.Errorf("prefetched f_plan %v, f_on_hold %v; want enterprise and false", f["f_plan"], f["f_on_hold"])
	}
	for _, hook := range []string{"get_plan", "get_credit_status"} {
		ev := hookEvent(t, id, "hook:ticket:"+hook)
		if ev.OnBehalfOf != ana.Actor || ev.OriginatingHook != "hook:ticket@v3:"+hook ||
			ev.CausationID != id.String()+":1" || ev.Hop != 1 {
			t.Errorf("%s's event = %+v; want on behalf of %s, caused by %s:1, hop 1", hook, ev, ana.Actor, id)
		}
	}
}

// During a CRM and ERP outage the hooks give up when their budget runs out:
// f_plan falls back to unknown, f_on_hold stays unset. The Item is never held.
func TestE2E_Prefetch_DuringAnOutage_FallsBack(t *testing.T) {
	h := newHookStack(t)
	h.crm.SetOutage(true)
	h.erp.SetOutage(true)
	ana := approver(t)
	id := h.newTicket(ana)

	eventually(t, 15*time.Second, "f_plan falls back to unknown", func() bool {
		return h.fields(ana, id)["f_plan"] == "unknown"
	})
	staysFor(t, testBudget+time.Second, "f_on_hold stays unset", func() bool {
		return h.fields(ana, id)["f_on_hold"] == nil
	})
	// The Item was never held: people moved it meanwhile without waiting.
	h.toInProgress(ana, id)
}

// --- the hold ---

// With the stub answering in 300 ms, the approval is applied within the
// caller's 3 s wait, by the hook, on behalf of the approver.
func TestE2E_ApproveWithinTheWait(t *testing.T) {
	h := newHookStack(t)
	h.erp.SetLatency(300 * time.Millisecond)
	ana := approver(t)
	id := h.newTicket(ana)
	h.prefetched(ana, id)
	h.toInProgress(ana, id)

	res, took := h.approve(ana, id, 3*time.Second)
	if res.Outcome != itemservice.OutcomeApplied {
		t.Fatalf("approve = %+v after %v, want applied within the wait", res, took)
	}
	if state, hold := h.hold(ana, id); state != "approved" || hold.Status != item.HoldAllowed {
		t.Errorf("Item in %q with hold %+v; want approved, hold allowed", state, hold)
	}
	ev := hookEvent(t, id, "hook:ticket:erp_check")
	if ev.OnBehalfOf != ana.Actor || ev.Op != "transition" {
		t.Errorf("erp_check's event = %+v; want a transition on behalf of %s", ev, ana.Actor)
	}
}

// Variant D's backstop: the prefetch said "not on hold", then the ERP put
// the customer on hold. The guard passes on the stale fact; the hold's
// check asks the ERP and denies.
func TestE2E_StalePrefetch_IsCaughtByTheHold(t *testing.T) {
	h := newHookStack(t)
	ana := approver(t)
	id := h.newTicket(ana)
	h.prefetched(ana, id)
	if h.fields(ana, id)["f_on_hold"] != false {
		t.Fatal("the prefetch should have read on_hold false")
	}
	h.erp.SetCustomer(h.customer, connstub.Customer{OnHold: true, CreditLimit: 1000}) // the fact goes stale
	h.toInProgress(ana, id)

	res, took := h.approve(ana, id, 3*time.Second)
	if res.Outcome != itemservice.OutcomeDenied || res.Reason != "customer_on_hold" {
		t.Fatalf("approve = %+v after %v, want denied for customer_on_hold", res, took)
	}
	if state, hold := h.hold(ana, id); state != "in_progress" || hold.Status != item.HoldDenied {
		t.Errorf("Item in %q with hold %+v; want in_progress, hold denied", state, hold)
	}
}

// The ERP corrects: it supplies the cost center, written with the move.
func TestE2E_TheCheckCorrects(t *testing.T) {
	h := newHookStack(t)
	h.erp.SetCustomer(h.customer, connstub.Customer{CreditLimit: 1000, CostCenter: "CC-9"})
	ana := approver(t)
	id := h.newTicket(ana)
	h.prefetched(ana, id)
	h.toInProgress(ana, id)

	res, _ := h.approve(ana, id, 3*time.Second)
	if res.Outcome != itemservice.OutcomeApplied {
		t.Fatalf("approve = %+v, want applied", res)
	}
	if f := h.fields(ana, id); f["f_cost_center"] != "CC-9" {
		t.Errorf("f_cost_center = %v, want CC-9", f["f_cost_center"])
	}
}

// When the ERP is down for longer than the budget, the hold resolves as
// error, visible on the Item with the reason. The caller got "pending".
func TestE2E_CheckBudgetRunsOut_HoldShowsTheError(t *testing.T) {
	h := newHookStack(t)
	ana := approver(t)
	id := h.newTicket(ana)
	h.prefetched(ana, id)
	h.toInProgress(ana, id)
	h.erp.SetOutage(true)

	res, took := h.approve(ana, id, 0)
	if res.Outcome != itemservice.OutcomePending || took > 500*time.Millisecond {
		t.Errorf("approve = %+v after %v, want pending at once", res, took)
	}
	eventually(t, 15*time.Second, "the hold resolves as error", func() bool {
		_, hold := h.hold(ana, id)
		return hold != nil && hold.Status == item.HoldError
	})
	state, hold := h.hold(ana, id)
	if state != "in_progress" || hold.Reason != "budget_exhausted" {
		t.Errorf("Item in %q with hold %+v; want in_progress, error for budget_exhausted", state, hold)
	}
}

// Kernel writes never wait on the workers or the connectors. With the worker
// stopped, a request opens its hold and answers pending at once; a wait
// is bounded by the caller's wait; ordinary writes are unaffected. Once a
// worker is back, the hold resolves.
func TestE2E_WorkersDown_WritesDoNotWait(t *testing.T) {
	h := newHookStack(t)
	ana := approver(t)
	id := h.newTicket(ana)
	h.prefetched(ana, id)
	h.toInProgress(ana, id)
	h.stopWorker()

	res, took := h.approve(ana, id, 0)
	if res.Outcome != itemservice.OutcomePending || took > 500*time.Millisecond {
		t.Errorf("approve with the worker down = %+v after %v, want pending at once", res, took)
	}
	start := time.Now()
	if _, err := h.k.Update(serverCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: "key-" + uuid.NewString(), Principal: ana, ItemID: id,
		Fields: map[string]any{"f_title": "still writable"},
	}); err != nil {
		t.Fatalf("update with the worker down: %v", err)
	}
	if took := time.Since(start); took > 500*time.Millisecond {
		t.Errorf("an update with the worker down took %v", took)
	}

	// A second ticket: its request waits exactly as long as asked.
	other := h.newTicket(ana)
	h.toInProgress(ana, other) // its prefetch is pending too; f_on_hold unset passes the guard
	res, took = h.approve(ana, other, time.Second)
	if res.Outcome != itemservice.OutcomePending || took < time.Second || took > 2*time.Second {
		t.Errorf("approve with a 1 s wait = %+v after %v, want pending after about 1 s", res, took)
	}

	h.startHookWorker()
	for _, it := range []uuid.UUID{id, other} {
		h.waitState(it, "approved", 30*time.Second)
	}
}

// --- the chain ---

// eventRow is one row of item_events, as an audit reads it.
type eventRow struct {
	Seq             int64
	Op              string
	Actor           string
	OnBehalfOf      string
	OriginatingHook string
	CausationID     string
	CorrelationID   string
	Hop             int
	Payload         map[string]any
}

func events(t *testing.T, id uuid.UUID) map[int64]eventRow {
	t.Helper()
	rows, err := pool.Query(serverCtx(t),
		`select item_seq, op, actor, coalesce(on_behalf_of, ''), coalesce(originating_hook, ''),
		        coalesce(causation_id, ''), coalesce(correlation_id, ''), hop, payload
		 from item_events where item_id = $1`, id)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	defer rows.Close()
	out := map[int64]eventRow{}
	for rows.Next() {
		var e eventRow
		var raw []byte
		if err := rows.Scan(&e.Seq, &e.Op, &e.Actor, &e.OnBehalfOf, &e.OriginatingHook,
			&e.CausationID, &e.CorrelationID, &e.Hop, &raw); err != nil {
			t.Fatalf("scan event: %v", err)
		}
		_ = json.Unmarshal(raw, &e.Payload)
		out[e.Seq] = e
	}
	return out
}

// hookEvent returns the Item's one event written by actor.
func hookEvent(t *testing.T, id uuid.UUID, actor string) eventRow {
	t.Helper()
	var found []eventRow
	for _, e := range events(t, id) {
		if e.Actor == actor {
			found = append(found, e)
		}
	}
	if len(found) != 1 {
		t.Fatalf("events by %s = %+v, want exactly one", actor, found)
	}
	return found[0]
}

// Every kernel write names its cause, and following causation_id from any
// event leads back, one hop at a time, to a person's write. Each hook write
// records the hook, its origin and the human at the root.
func TestE2E_TheChainCanBeRebuilt(t *testing.T) {
	h := newHookStack(t)
	ana := approver(t)
	id := h.newTicket(ana)
	h.prefetched(ana, id)
	h.toInProgress(ana, id)
	if res, _ := h.approve(ana, id, 3*time.Second); res.Outcome != itemservice.OutcomeApplied {
		t.Fatalf("approve = %+v, want applied", res)
	}

	all := events(t, id)
	kernelWrites := 0
	for seq, e := range all {
		if e.Hop == 0 {
			if e.Actor != ana.Actor || e.OriginatingHook != "" || e.CausationID != "" {
				t.Errorf("event %d = %+v: a person's write names no cause", seq, e)
			}
			continue
		}
		kernelWrites++
		if !strings.HasPrefix(e.Actor, "hook:ticket:") || e.OnBehalfOf != ana.Actor ||
			!strings.HasPrefix(e.OriginatingHook, "hook:ticket@v3:") {
			t.Errorf("event %d = %+v: want a hook, on behalf of %s, with its origin", seq, e, ana.Actor)
		}
		// Walk back to the root.
		cur, steps := e, 0
		for cur.Hop > 0 {
			idPart, seqPart, _ := strings.Cut(cur.CausationID, ":")
			causeSeq, err := strconv.ParseInt(seqPart, 10, 64)
			cause, ok := all[causeSeq]
			if idPart != id.String() || err != nil || !ok || cause.Hop != cur.Hop-1 ||
				cause.CorrelationID != cur.CorrelationID {
				t.Fatalf("event %d: cause %q is not an event of this Item one hop up, in the same correlation",
					cur.Seq, cur.CausationID)
			}
			cur, steps = cause, steps+1
		}
		if cur.Actor != ana.Actor || steps != e.Hop {
			t.Errorf("event %d leads back to %+v in %d steps; want %s's write in %d", seq, cur, steps, ana.Actor, e.Hop)
		}
	}
	// Two prefetches and the approval.
	if kernelWrites != 3 {
		t.Errorf("kernel writes = %d, want 3 (get_plan, get_credit_status, erp_check)", kernelWrites)
	}
	resolved := hookEvent(t, id, "hook:ticket:erp_check")
	if cause := all[mustSeq(t, resolved.CausationID)]; cause.Op != "transition_requested" {
		t.Errorf("the approval's cause = %+v, want the transition_requested event", cause)
	}
}

func mustSeq(t *testing.T, causation string) int64 {
	t.Helper()
	_, seqPart, _ := strings.Cut(causation, ":")
	n, err := strconv.ParseInt(seqPart, 10, 64)
	if err != nil {
		t.Fatalf("causation %q: %v", causation, err)
	}
	return n
}
