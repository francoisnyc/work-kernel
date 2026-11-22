// End-to-end tests for proof-of-concept Step 2, part 3: the vacation
// request v3 run by the real worker, with the email stub, on a scaled clock
// (a day lasts DevDay), plus the activities against the real kernel.
//
//   - A submitted request reminds its approver twice (at 2 and 3 days) and
//     expires at 5 days, emailing the requester; no refund email.
//   - Approve: the approval email; withdraw: the refund and its email, once.
//   - A denied approval (insufficient_balance): no refund email.
//   - LoadItem scales only the Type's durations, records the day, and
//     reports acting runs and reaction hooks; a field-less action records
//     hook_ran and sends once; a committed undo records reaction_applied.
package workflow_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/connstub"
	"github.com/francoisnyc/work-kernel/internal/hooks"
	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
	wf "github.com/francoisnyc/work-kernel/internal/workflow"
)

// e2eDay is the length of a day in these tests.
const e2eDay = time.Second

type mailStack struct {
	*leaveStack
	mail *connstub.Server
}

func newMailStack(t *testing.T) *mailStack {
	t.Helper()
	registerTypeFile(t, "vacation_request.v3.yaml")
	m := &mailStack{leaveStack: newLeaveStack(t), mail: connstub.NewEmail()}
	t.Cleanup(m.mail.Close)
	m.stopWorker()
	m.worker = wf.NewWorker(tc, m.queue, m.k, wf.WorkerOptions{
		Connectors: m.connectors(), HookBudget: testBudget, DevDay: e2eDay, DevTypes: []string{"vacation_request"},
	})
	if err := m.worker.Start(); err != nil {
		t.Fatalf("start worker: %v", err)
	}
	return m
}

func (m *mailStack) connectors() *hooks.Connectors {
	return m.remoteHR().WithEmail(m.mail.URL)
}

// submittedV3: ana's v3 request, submitted with --wait (HR names bob).
func (m *mailStack) submittedV3(days int) uuid.UUID {
	m.t.Helper()
	res, err := m.k.Create(serverCtx(m.t), itemservice.CreateRequest{IdempotencyKey: "key-" + uuid.NewString(),
		Principal: m.ana, TypeID: "vacation_request", TypeVersion: 3,
		Fields: map[string]any{"f_start": "2099-07-01", "f_end": "2099-07-05", "f_days": days, "f_leave_type": "annual"}})
	if err != nil {
		m.t.Fatalf("create v3: %v", err)
	}
	if out := m.submit(m.ana, res.ItemID); out.Outcome != itemservice.OutcomeApplied {
		m.t.Fatalf("submit = %+v, want applied", out)
	}
	return res.ItemID
}

// sent counts the messages of a template for the request.
func (m *mailStack) sent(id uuid.UUID, template string) (n int, to []string) {
	for _, msg := range m.mail.Messages(id.String()) {
		if msg.Template == template {
			n++
			to = append(to, msg.To)
		}
	}
	return n, to
}

func TestE2E_V3_RemindsThenExpires(t *testing.T) {
	m := newMailStack(t)
	m.balance(25)
	id := m.submittedV3(5)
	eventually(t, 15*time.Second, "the request expired", func() bool { return m.item(m.ana, id).State == "expired" })
	eventually(t, 5*time.Second, "the expiry email", func() bool { n, _ := m.sent(id, "notify_expired"); return n == 1 })
	staysFor(t, 2*time.Second, "two reminders to bob, one expiry email to ana, no refund email", func() bool {
		r, rto := m.sent(id, "remind_approver")
		e, eto := m.sent(id, "notify_expired")
		f, _ := m.sent(id, "refund_email")
		return r == 2 && rto[0] == m.bob.Actor && rto[1] == m.bob.Actor && e == 1 && eto[0] == m.ana.Actor && f == 0
	})
}

func TestE2E_V3_ApproveThenWithdraw(t *testing.T) {
	m := newMailStack(t)
	bal := m.balance(25)
	id := m.submittedV3(5)
	if res := m.transition(m.bob, id, "approved", itemservice.MaxWait); res.Outcome != itemservice.OutcomeApplied {
		t.Fatalf("approve = %+v", res)
	}
	eventually(t, 5*time.Second, "the approval email", func() bool { n, _ := m.sent(id, "notify_approved"); return n == 1 })
	m.transition(m.ana, id, "withdrawn", 0)
	eventually(t, 10*time.Second, "the refund and its email", func() bool {
		n, _ := m.sent(id, "refund_email")
		return m.used(bal) == 0 && n == 1
	})
	staysFor(t, 7*e2eDay, "one approval email, one refund email, nothing else", func() bool {
		a, _ := m.sent(id, "notify_approved")
		f, fto := m.sent(id, "refund_email")
		e, _ := m.sent(id, "notify_expired")
		return a == 1 && f == 1 && fto[0] == m.ana.Actor && e == 0
	})
}

func TestE2E_V3_DeniedApprovalSendsNoRefundEmail(t *testing.T) {
	m := newMailStack(t)
	m.balance(4)
	id := m.submittedV3(5)
	if res := m.transition(m.bob, id, "approved", itemservice.MaxWait); res.Outcome != itemservice.OutcomeDenied {
		t.Fatalf("approve = %+v, want denied", res)
	}
	staysFor(t, time.Second, "no refund email", func() bool { n, _ := m.sent(id, "refund_email"); return n == 0 })
}

// --- the activities against the real kernel ---

func TestActivities_LoadItem_ScaledClock(t *testing.T) {
	m := newMailStack(t)
	m.stopRelay()
	m.stopWorker()
	m.balance(25)
	id := m.submittedByHandV3(t, 5)

	scaled := &wf.Activities{Items: m.k, HookBudget: 15 * time.Minute, DevDay: 2 * time.Second,
		DevTypes: []string{"vacation_request"}}
	snap, err := scaled.LoadItem(serverCtx(t), id.String())
	if err != nil {
		t.Fatalf("LoadItem: %v", err)
	}
	if got := snap.Timers["submitted"]; got.After != 10*time.Second || got.To != "expired" {
		t.Errorf("expiry = %+v, want 5 days of 2 s, to expired", got)
	}
	if got := snap.Acting["submitted"]; len(got) != 1 ||
		got[0] != (wf.ActingSpec{ID: "remind", After: 4 * time.Second, Every: 2 * time.Second, Max: 2, Hook: "remind_approver"}) {
		t.Errorf("acting = %+v, want remind at 4 s, every 2 s, 2 runs", got)
	}
	if snap.HookBudget != 15*time.Minute || snap.DevDay != 2*time.Second {
		t.Errorf("budget %v, day %v; want the budget unscaled (15m) and the day recorded (2s)", snap.HookBudget, snap.DevDay)
	}
	if snap.ReactionHooks["refund"] == nil || snap.ReactionHooks["refund"][0] != "refund_email" {
		t.Errorf("reaction hooks = %v", snap.ReactionHooks)
	}
	real := &wf.Activities{Items: m.k}
	if snap, _ := real.LoadItem(serverCtx(t), id.String()); snap.Timers["submitted"].After != 120*time.Hour || snap.DevDay != 0 {
		t.Errorf("real time: expiry %v, day %v; want 120h and none", snap.Timers["submitted"].After, snap.DevDay)
	}
}

// A field-less action: email.notify sends once and hook_ran records it,
// once, whatever the retries.
func TestActivities_RunAction_Fieldless(t *testing.T) {
	m := newMailStack(t)
	m.stopRelay()
	m.stopWorker()
	m.balance(25)
	id := m.submittedByHandV3(t, 5)
	a := &wf.Activities{Items: m.k, Connectors: m.connectors()}
	entered := m.item(m.ana, id).ItemSeq
	in := wf.HookInput{ItemID: id.String(), TypeID: "vacation_request", TypeVersion: 3, HookID: "remind_approver",
		CauseSeq: entered, Hop: 1, Timer: "remind", Run: 1,
		IdempotencyKey: fmt.Sprintf("timer:%s:remind:%d:1", id, entered)}
	for i := 0; i < 2; i++ {
		if err := a.RunAction(serverCtx(t), in); err != nil {
			t.Fatalf("RunAction #%d: %v", i+1, err)
		}
	}
	if n, to := m.sent(id, "remind_approver"); n != 1 || to[0] != m.bob.Actor {
		t.Errorf("reminders sent = %d to %v, want one to bob", n, to)
	}
	page, err := m.k.ListEvents(serverCtx(t), itemservice.ListEventsRequest{
		Principal: item.Principal{Actor: m.ana.Actor, Namespace: "default"}, ItemID: id})
	if err != nil {
		t.Fatal(err)
	}
	ran := 0
	for _, e := range page.Events {
		if e.Op == "hook_ran" && e.Hook == "remind_approver" && e.Timer == "remind" && e.Run == 1 {
			ran++
		}
	}
	if ran != 1 {
		t.Errorf("hook_ran events = %d, want 1", ran)
	}
}

// The Undo activity records reaction_applied after a committed refund.
func TestActivities_Undo_RecordsReactionApplied(t *testing.T) {
	m := newMailStack(t)
	m.stopRelay()
	m.stopWorker()
	m.balance(25)
	id := m.submittedByHandV3(t, 5)
	a := &wf.Activities{Items: m.k, Connectors: m.connectors()}
	ask := m.transition(m.bob, id, "approved", 0)
	if err := a.RunReaction(serverCtx(t), wf.ReactionInput{ItemID: id.String(), TypeID: "vacation_request", TypeVersion: 3,
		ReactionID: "deduct", HoldID: ask.HoldID.String(), CauseSeq: ask.ItemSeq, Hop: 1}); err != nil {
		t.Fatalf("RunReaction: %v", err)
	}
	withdraw := m.transition(m.ana, id, "withdrawn", 0)
	if err := a.Undo(serverCtx(t), wf.ReactionInput{ItemID: id.String(), TypeID: "vacation_request", TypeVersion: 3,
		ReactionID: "refund", HoldID: ask.HoldID.String(), CauseSeq: withdraw.ItemSeq, Hop: 1}); err != nil {
		t.Fatalf("Undo: %v", err)
	}
	se, err := m.k.StateEntry(serverCtx(t), id)
	if err != nil {
		t.Fatal(err)
	}
	if len(se.PendingUndos) != 0 {
		t.Errorf("PendingUndos = %+v, want none: refund and reaction_applied both done", se.PendingUndos)
	}
	found := false
	for _, p := range se.PendingActions {
		found = found || p.Hook == "refund_email"
	}
	if !found {
		t.Errorf("PendingActions = %+v, want the refund email pending (reaction_applied committed)", se.PendingActions)
	}
}

// submittedByHandV3: ana's v3 request in submitted, the HR check played by
// hand (no worker).
func (m *mailStack) submittedByHandV3(t *testing.T, days int) uuid.UUID {
	t.Helper()
	res, err := m.k.Create(serverCtx(t), itemservice.CreateRequest{IdempotencyKey: "key-" + uuid.NewString(),
		Principal: m.ana, TypeID: "vacation_request", TypeVersion: 3,
		Fields: map[string]any{"f_start": "2099-07-01", "f_end": "2099-07-05", "f_days": days, "f_leave_type": "annual"}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	ask := m.transition(m.ana, res.ItemID, "submitted", 0)
	if _, err := m.k.Internal().ResolveHold(serverCtx(t), itemservice.ResolveHoldRequest{
		IdempotencyKey: "hold:" + ask.HoldID.String(), Principal: item.Principal{Actor: "hook:vacation_request:hr_check"},
		ItemID: res.ItemID, HoldID: ask.HoldID, Verdict: itemservice.VerdictCorrect,
		Fields: map[string]any{"f_approver": m.bob.Actor},
		Attribution: item.Attribution{OriginatingHook: "hook:vacation_request@v3:hr_check",
			CausationID: fmt.Sprintf("%s:%d", res.ItemID, ask.ItemSeq), Hop: 1}}); err != nil {
		t.Fatalf("HR: %v", err)
	}
	return res.ItemID
}
