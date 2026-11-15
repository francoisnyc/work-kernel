// End-to-end tests for Step 7: ticket v4's on_complete(approval) hook,
// create_invoice, with the real kernel, relay and workflow, a worker in this
// process, and the stub ERP.
//
// They include the step's "done when" at test size: no duplicate invoices
// across forced timeouts (late answers, dropped answers, a worker killed
// mid-activity). The 1,000-timeout run is a bench mode (cmd/bench). A control
// run without keys must show duplicates, or the test proves nothing.
//
// Duplicates are counted on the ERP side, never from the kernel: a kernel
// that silently kept the first invoice ID would hide the second invoice.
package workflow_test

import (
	"errors"
	"fmt"
	"os"
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

type invoiceOpts struct {
	budget         time.Duration // each hook's budget
	attemptTimeout time.Duration // wf.WorkerOptions.HookAttemptTimeout; zero for the default
	withoutKeys    bool          // the control run: connectors that send no Idempotency-Key
}

// invoiceStack is a stack for ticket v4, with stub CRM and ERP.
type invoiceStack struct {
	*stack
	crm, erp *connstub.Server
	opts     invoiceOpts
}

func newInvoiceStack(t *testing.T, opts invoiceOpts) *invoiceStack {
	t.Helper()
	if _, err := pool.Exec(serverCtx(t), `update outbox set delivered_at = now() where delivered_at is null`); err != nil {
		t.Fatalf("clear outbox: %v", err)
	}
	registerV4(t)
	s := &invoiceStack{
		stack: &stack{t: t, k: itemservice.New(pool), queue: "items-" + uuid.NewString()},
		crm:   connstub.NewCRM(),
		erp:   connstub.NewERP(),
		opts:  opts,
	}
	s.startInvoiceWorker()
	s.startRelay()
	t.Cleanup(func() {
		s.stopRelay()
		s.stopWorker()
		s.crm.Close()
		s.erp.Close()
	})
	return s
}

func (s *invoiceStack) startInvoiceWorker() {
	s.t.Helper()
	conn := hooks.NewConnectors(s.crm.URL, s.erp.URL)
	if s.opts.withoutKeys {
		conn = conn.WithoutKeys()
	}
	s.worker = wf.NewWorker(tc, s.queue, s.k, wf.WorkerOptions{
		Connectors: conn, HookBudget: s.opts.budget, HookAttemptTimeout: s.opts.attemptTimeout,
	})
	if err := s.worker.Start(); err != nil {
		s.t.Fatalf("start worker: %v", err)
	}
}

func registerV4(t *testing.T) {
	t.Helper()
	data, err := os.ReadFile("../../types/ticket.v4.yaml")
	if err != nil {
		t.Fatalf("read ticket v4: %v", err)
	}
	ty, err := typedef.Parse(data)
	if err != nil {
		t.Fatalf("parse ticket v4: %v", err)
	}
	if err := itemservice.New(pool).RegisterType(serverCtx(t), ty); err != nil {
		t.Fatalf("register ticket v4: %v", err)
	}
}

// ticket creates a v4 ticket for a customer no other ticket uses, and moves
// it to in_progress. It returns the ticket and the customer.
func (s *invoiceStack) ticket(p item.Principal) (uuid.UUID, string) {
	s.t.Helper()
	customer := "cust-" + uuid.NewString()[:8]
	s.crm.SetCustomer(customer, connstub.Customer{Plan: "pro"})
	s.erp.SetCustomer(customer, connstub.Customer{CreditLimit: 1000})
	res, err := s.k.Create(serverCtx(s.t), itemservice.CreateRequest{
		IdempotencyKey: "key-" + uuid.NewString(), Principal: p, TypeID: "ticket", TypeVersion: 4,
		Fields: map[string]any{
			"f_title": "Onboarding", "f_customer_id": customer, "f_amount": 100, "f_assignee": "user-bob",
		},
		Attribution: item.Attribution{CorrelationID: "corr-" + uuid.NewString()[:8]},
	})
	if err != nil {
		s.t.Fatalf("create ticket v4: %v", err)
	}
	for _, to := range []string{"open", "in_progress"} {
		if _, err := s.k.Transition(serverCtx(s.t), itemservice.TransitionRequest{
			IdempotencyKey: "key-" + uuid.NewString(), Principal: p, ItemID: res.ItemID, ToState: to,
		}); err != nil {
			s.t.Fatalf("transition to %s: %v", to, err)
		}
	}
	return res.ItemID, customer
}

func (s *invoiceStack) requestApproval(p item.Principal, id uuid.UUID, wait time.Duration) itemservice.TransitionResult {
	s.t.Helper()
	res, err := s.k.Transition(serverCtx(s.t), itemservice.TransitionRequest{
		IdempotencyKey: "key-" + uuid.NewString(), Principal: p, ItemID: id, ToState: "approved", Wait: wait,
	})
	if err != nil {
		s.t.Fatalf("approve: %v", err)
	}
	return res
}

// approveByHand asks for the approval and settles the hold as erp_check
// would, without a worker. It returns the item_seq of the approval.
func (s *invoiceStack) approveByHand(p item.Principal, id uuid.UUID) int64 {
	s.t.Helper()
	req := s.requestApproval(p, id, 0)
	if req.Outcome != itemservice.OutcomePending {
		s.t.Fatalf("approve = %+v, want pending", req)
	}
	res, err := s.k.Internal().ResolveHold(serverCtx(s.t), itemservice.ResolveHoldRequest{
		IdempotencyKey: "hold:" + req.HoldID.String(), Principal: item.Principal{Actor: "hook:ticket:erp_check"},
		ItemID: id, HoldID: req.HoldID, Verdict: itemservice.VerdictAllow,
		Attribution: item.Attribution{
			OriginatingHook: "hook:ticket@v4:erp_check", CausationID: fmt.Sprintf("%s:%d", id, req.ItemSeq), Hop: 1,
		},
	})
	if err != nil {
		s.t.Fatalf("resolve the hold by hand: %v", err)
	}
	return res.ItemSeq
}

func (s *invoiceStack) fieldsOf(p item.Principal, id uuid.UUID) map[string]any {
	s.t.Helper()
	it, err := s.k.Get(serverCtx(s.t), p, id)
	if err != nil {
		s.t.Fatalf("Get: %v", err)
	}
	return it.Fields
}

func (s *invoiceStack) invoiced(p item.Principal, id uuid.UUID) bool {
	return s.fieldsOf(p, id)["f_invoice_id"] != nil
}

// hookFailedEvents returns the Item's hook_failed events.
func hookFailedEvents(t *testing.T, id uuid.UUID) []eventRow {
	t.Helper()
	var out []eventRow
	for _, e := range events(t, id) {
		if e.Op == "hook_failed" {
			out = append(out, e)
		}
	}
	return out
}

// assertOneInvoiceEach checks, on the ERP side, that each ticket's customer
// has exactly one invoice, and that it is the one the ticket records.
func (s *invoiceStack) assertOneInvoiceEach(p item.Principal, tickets map[uuid.UUID]string) {
	s.t.Helper()
	dups := 0
	for id, customer := range tickets {
		got := s.erp.Invoices(customer)
		if len(got) != 1 {
			dups += len(got) - 1
			s.t.Errorf("ticket %s: ERP invoices %v, want exactly one", id, got)
			continue
		}
		if f := s.fieldsOf(p, id)["f_invoice_id"]; f != got[0] {
			s.t.Errorf("ticket %s: f_invoice_id %v, want the ERP's %s", id, f, got[0])
		}
		if failed := hookFailedEvents(s.t, id); len(failed) != 0 {
			s.t.Errorf("ticket %s: hook_failed events %+v, want none", id, failed)
		}
	}
	if dups > 0 {
		s.t.Errorf("%d duplicate invoices across %d tickets", dups, len(tickets))
	}
}

// --- one invoice per approval ---

// The approval enters approved; create_invoice makes one invoice and writes
// its ID back, as the hook, on behalf of the approver, caused by the
// approval, two hops below the request.
func TestE2E_Invoice_OnApproval_OneInvoice(t *testing.T) {
	s := newInvoiceStack(t, invoiceOpts{budget: 10 * time.Second})
	ana := approver(t)
	id, customer := s.ticket(ana)

	if res := s.requestApproval(ana, id, 3*time.Second); res.Outcome != itemservice.OutcomeApplied {
		t.Fatalf("approve = %+v, want applied", res)
	}
	eventually(t, 15*time.Second, "f_invoice_id written", func() bool { return s.invoiced(ana, id) })

	got := s.erp.Invoices(customer)
	if f := s.fieldsOf(ana, id)["f_invoice_id"]; len(got) != 1 || f != got[0] {
		t.Errorf("ERP invoices %v, f_invoice_id %v; want one invoice, recorded on the ticket", got, f)
	}
	approval := hookEvent(t, id, "hook:ticket:erp_check")
	ev := hookEvent(t, id, "hook:ticket:create_invoice")
	if ev.Op != "update" || ev.OnBehalfOf != ana.Actor || ev.OriginatingHook != "hook:ticket@v4:create_invoice" ||
		ev.CausationID != fmt.Sprintf("%s:%d", id, approval.Seq) || ev.Hop != approval.Hop+1 ||
		ev.CorrelationID != approval.CorrelationID {
		t.Errorf("create_invoice's event = %+v; want an update on behalf of %s, caused by the approval %+v",
			ev, ana.Actor, approval)
	}
	staysFor(t, time.Second, "still one invoice", func() bool { return len(s.erp.Invoices(customer)) == 1 })
}

// The ticket is resolved before the invoice hook starts (no worker yet). The
// action is not dropped: once a worker is back, the invoice is made for the
// approval, and written back while the ticket is resolved.
func TestE2E_Invoice_ResolvedBeforeTheHookStarts_StillCreated(t *testing.T) {
	s := newInvoiceStack(t, invoiceOpts{budget: 10 * time.Second})
	s.stopWorker()
	ana := approver(t)
	id, customer := s.ticket(ana)
	approval := s.approveByHand(ana, id)
	if _, err := s.k.Transition(serverCtx(t), itemservice.TransitionRequest{
		IdempotencyKey: "key-" + uuid.NewString(), Principal: ana, ItemID: id, ToState: "resolved",
		Fields: map[string]any{"f_resolution": "fixed"},
	}); err != nil {
		t.Fatalf("resolve the ticket: %v", err)
	}
	if n := s.erp.InvoiceCount(); n != 0 {
		t.Fatalf("InvoiceCount = %d before any worker ran, want 0", n)
	}

	s.startInvoiceWorker()
	eventually(t, 30*time.Second, "f_invoice_id written", func() bool { return s.invoiced(ana, id) })

	if got := s.erp.Invoices(customer); len(got) != 1 {
		t.Errorf("ERP invoices %v, want one", got)
	}
	ev := hookEvent(t, id, "hook:ticket:create_invoice")
	if ev.CausationID != fmt.Sprintf("%s:%d", id, approval) {
		t.Errorf("create_invoice caused by %q, want the approval, event %d", ev.CausationID, approval)
	}
	if state, _ := s.state(id); state != "resolved" {
		t.Errorf("ticket in %q, want resolved: the action writes fields, never the state", state)
	}
}

// --- forced timeouts: no duplicates ---

// approveAll asks for n approvals at once (wait 0) and returns each ticket's
// customer. erp_check allows them; each entry into approved runs the action.
func (s *invoiceStack) approveAll(p item.Principal, n int) map[uuid.UUID]string {
	s.t.Helper()
	tickets := map[uuid.UUID]string{}
	for range n {
		id, customer := s.ticket(p)
		tickets[id] = customer
	}
	for id := range tickets {
		if res := s.requestApproval(p, id, 0); res.Outcome != itemservice.OutcomePending {
			s.t.Fatalf("approve %s = %+v, want pending", id, res)
		}
	}
	return tickets
}

func (s *invoiceStack) waitAllInvoiced(p item.Principal, tickets map[uuid.UUID]string, within time.Duration) {
	s.t.Helper()
	eventually(s.t, within, fmt.Sprintf("all %d tickets invoiced", len(tickets)), func() bool {
		for id := range tickets {
			if !s.invoiced(p, id) {
				return false
			}
		}
		return true
	})
}

// Every first invoice request is committed by the ERP and answered after
// the attempt has timed out, or not answered at all. Each retry uses the
// same key and gets the invoice already made: one invoice per ticket.
func TestE2E_Invoice_ForcedTimeouts_NoDuplicates(t *testing.T) {
	t.Run("late answers", func(t *testing.T) {
		const n = 50
		s := newInvoiceStack(t, invoiceOpts{budget: 30 * time.Second, attemptTimeout: 200 * time.Millisecond})
		s.erp.SetFirstAnswerDelay(time.Second) // longer than an attempt may take
		ana := approver(t)
		tickets := s.approveAll(ana, n)
		s.waitAllInvoiced(ana, tickets, 90*time.Second)

		s.assertOneInvoiceEach(ana, tickets)
		if late := s.erp.LateAnswers(); late != n {
			t.Errorf("late answers = %d, want %d: every invoice had one timed-out attempt", late, n)
		}
	})
	t.Run("dropped answers", func(t *testing.T) {
		const n = 20
		s := newInvoiceStack(t, invoiceOpts{budget: 30 * time.Second})
		s.erp.SetDropFirstAnswer(true)
		ana := approver(t)
		tickets := s.approveAll(ana, n)
		s.waitAllInvoiced(ana, tickets, 90*time.Second)

		s.assertOneInvoiceEach(ana, tickets)
		if dropped := s.erp.DroppedAnswers(); dropped != n {
			t.Errorf("dropped answers = %d, want %d", dropped, n)
		}
	})
}

// The control: the same run with connectors that send no key. Every ticket
// must end with two invoices. If this test passed with one, the duplicate
// test above would prove nothing.
func TestE2E_Invoice_ControlRunWithoutKeys_HasDuplicates(t *testing.T) {
	const n = 10
	s := newInvoiceStack(t, invoiceOpts{budget: 30 * time.Second, attemptTimeout: 200 * time.Millisecond, withoutKeys: true})
	s.erp.SetFirstAnswerDelay(time.Second)
	ana := approver(t)
	tickets := s.approveAll(ana, n)
	s.waitAllInvoiced(ana, tickets, 90*time.Second)

	for id, customer := range tickets {
		if got := s.erp.Invoices(customer); len(got) != 2 {
			t.Errorf("ticket %s without keys: ERP invoices %v, want two (a duplicate)", id, got)
		}
	}
}

// The real-world case: the worker dies while the ERP is answering. Every
// invoice is committed, then the worker is stopped before any answer
// arrives. A new worker retries each action with the same key: still one
// invoice per ticket.
func TestE2E_Invoice_WorkerKilledMidActivity_NoDuplicates(t *testing.T) {
	const n = 50
	// The answer delay must outlast the time it takes all 50 approvals to
	// reach the ERP (the relay delivers one Item at a time), so that every
	// attempt is still in flight when the worker stops. A retry comes only
	// after the attempt timeout, so this test takes about 40 s.
	s := newInvoiceStack(t, invoiceOpts{budget: 120 * time.Second, attemptTimeout: 20 * time.Second})
	s.erp.SetFirstAnswerDelay(15 * time.Second)
	ana := approver(t)
	tickets := s.approveAll(ana, n)

	eventually(t, 15*time.Second, "every invoice committed, its answer pending", func() bool {
		return s.erp.InvoiceCount() == n
	})
	s.stopWorker()
	s.startInvoiceWorker()
	s.waitAllInvoiced(ana, tickets, 90*time.Second)

	s.assertOneInvoiceEach(ana, tickets)
	if late := s.erp.LateAnswers(); late != n {
		t.Errorf("late answers = %d, want %d: the stop must interrupt every attempt in flight", late, n)
	}
}

// --- giving up, visibly ---

// The ERP's invoices are down for longer than the budget. The action gives
// up and records hook_failed (budget_exhausted) on the ticket, as the hook,
// caused by the approval. No invoice, no f_invoice_id, and no rerun.
func TestE2E_Invoice_BudgetRunsOut_HookFailedIsVisible(t *testing.T) {
	s := newInvoiceStack(t, invoiceOpts{budget: 3 * time.Second})
	s.stopWorker()
	ana := approver(t)
	id, _ := s.ticket(ana)
	approval := s.approveByHand(ana, id) // the check is settled; only the action is left
	s.erp.SetOutage(true)
	s.startInvoiceWorker()

	eventually(t, 30*time.Second, "hook_failed recorded", func() bool { return len(hookFailedEvents(t, id)) == 1 })
	ev := hookFailedEvents(t, id)[0]
	if ev.Actor != "hook:ticket:create_invoice" || ev.OnBehalfOf != ana.Actor ||
		ev.CausationID != fmt.Sprintf("%s:%d", id, approval) ||
		ev.Payload["hook"] != "create_invoice" || ev.Payload["reason"] != "budget_exhausted" || len(ev.Payload) != 2 {
		t.Errorf("hook_failed = %+v; want create_invoice, budget_exhausted, caused by event %d, on behalf of %s",
			ev, approval, ana.Actor)
	}
	if s.invoiced(ana, id) || s.erp.InvoiceCount() != 0 {
		t.Errorf("f_invoice_id %v, ERP invoices %d; want neither", s.fieldsOf(ana, id)["f_invoice_id"], s.erp.InvoiceCount())
	}
	calls := s.erp.Calls()
	staysFor(t, 3*time.Second, "the action is not run again", func() bool {
		return s.erp.Calls() == calls && len(hookFailedEvents(t, id)) == 1
	})
}

// --- key conflicts, at the activity ---

// actionInputFor is what the workflow gives RunAction for an approval's
// entry. These tests call the activity directly (no worker), so they can run
// the same attempt twice.
func actionInputFor(id uuid.UUID, approval int64) wf.HookInput {
	return wf.HookInput{
		ItemID: id.String(), TypeID: "ticket", TypeVersion: 4, HookID: "create_invoice",
		CauseSeq: approval, Hop: 2,
		IdempotencyKey: fmt.Sprintf("hook:%s:%d:create_invoice", id, approval),
	}
}

func assertKeyConflict(t *testing.T, what string, err error) {
	t.Helper()
	var app *temporal.ApplicationError
	if !errors.As(err, &app) || app.Type() != wf.ErrTypeKeyConflict || !app.NonRetryable() {
		t.Errorf("%s: error = %v, want a non-retryable %q", what, err, wf.ErrTypeKeyConflict)
	}
}

// The connector ignores keys (here: connectors that send none). The second
// run of the same attempt makes a second invoice, and its write-back is the
// same key with a different request. That is a duplicate, never "already
// written": a non-retryable key conflict.
func TestActivities_RunAction_SecondDifferentResult_IsAKeyConflict(t *testing.T) {
	s := newInvoiceStack(t, invoiceOpts{budget: 10 * time.Second})
	s.stopWorker()
	ana := approver(t)
	id, customer := s.ticket(ana)
	approval := s.approveByHand(ana, id)
	a := &wf.Activities{Items: s.k, Connectors: hooks.NewConnectors(s.crm.URL, s.erp.URL).WithoutKeys()}

	if err := a.RunAction(serverCtx(t), actionInputFor(id, approval)); err != nil {
		t.Fatalf("first run: %v", err)
	}
	first := s.fieldsOf(ana, id)["f_invoice_id"]
	assertKeyConflict(t, "second run", a.RunAction(serverCtx(t), actionInputFor(id, approval)))
	if got := s.erp.Invoices(customer); len(got) != 2 {
		t.Errorf("ERP invoices %v, want two: the conflict reports a real duplicate", got)
	}
	if f := s.fieldsOf(ana, id)["f_invoice_id"]; f != first {
		t.Errorf("f_invoice_id = %v, want the first invoice %v", f, first)
	}
}

// The ERP answers 409 when the same key comes back with a different
// request (the amount changed between two runs): a key conflict too.
func TestActivities_RunAction_ERPConflict_IsAKeyConflict(t *testing.T) {
	s := newInvoiceStack(t, invoiceOpts{budget: 10 * time.Second})
	s.stopWorker()
	ana := approver(t)
	id, customer := s.ticket(ana)
	approval := s.approveByHand(ana, id)
	a := &wf.Activities{Items: s.k, Connectors: hooks.NewConnectors(s.crm.URL, s.erp.URL)}

	if err := a.RunAction(serverCtx(t), actionInputFor(id, approval)); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if _, err := s.k.Update(serverCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: "key-" + uuid.NewString(), Principal: ana, ItemID: id, Fields: map[string]any{"f_amount": 250},
	}); err != nil {
		t.Fatalf("change the amount: %v", err)
	}
	assertKeyConflict(t, "run after the amount changed", a.RunAction(serverCtx(t), actionInputFor(id, approval)))
	if got := s.erp.Invoices(customer); len(got) != 1 {
		t.Errorf("ERP invoices %v, want one", got)
	}
}
