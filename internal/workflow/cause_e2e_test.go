// Tests for bake-off hole 1 (docs/design.md, "An effect re-checks what
// caused it"): a workflow's input is a request, never proof. Before an
// action with a side effect calls its connector, the activity asks the item
// service to confirm the cause it names. A cause that does not check out is
// refused for good, recorded on the Item once, and nothing is sent.
//
// The first test is the attack that found the hole
// (spikes/camunda-bakeoff/scripts/test2-sidedoors.py): a workflow started
// by hand on a Temporal with no sign-on, telling the kernel's own workflow
// that an Item still waiting is approved and its order action is pending.
//
// The others call the activities directly, as a forged workflow's commands
// would: the kernel's own workflow code derives a hook's key itself, so a
// made-up key or timer run can only come from a forged activity input.
//
// Tests named Residue pin what this step leaves open, as it is today, so a
// later fix shows up as a change to a test.
package workflow_test

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"

	"github.com/francoisnyc/work-kernel/internal/connstub"
	"github.com/francoisnyc/work-kernel/internal/hooks"
	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
	"github.com/francoisnyc/work-kernel/internal/typedef"
	wf "github.com/francoisnyc/work-kernel/internal/workflow"
)

// The refusal's two names: the activity's error type, and the reason code
// hook_failed records.
const (
	errTypeCauseNotConfirmed = "CauseNotConfirmed"
	reasonCauseNotConfirmed  = "cause_not_confirmed"
)

// assertCauseRefused: err is the refusal, and Temporal will not retry it.
func assertCauseRefused(t *testing.T, what string, err error) {
	t.Helper()
	// errors.As is Go's "instanceof through the wrappers": it finds the
	// first error of that type in the chain.
	var app *temporal.ApplicationError
	if !errors.As(err, &app) || app.Type() != errTypeCauseNotConfirmed || !app.NonRetryable() {
		t.Errorf("%s: error = %v, want a non-retryable %q", what, err, errTypeCauseNotConfirmed)
	}
}

// causeRefusals returns the Item's hook_failed events that record a cause
// that did not check out.
func causeRefusals(t *testing.T, id uuid.UUID) []eventRow {
	t.Helper()
	var out []eventRow
	for _, e := range hookFailedEvents(t, id) {
		if e.Payload["reason"] == reasonCauseNotConfirmed {
			out = append(out, e)
		}
	}
	return out
}

// assertOneRefusal: the Item records the refusal exactly once, as the
// hook, with the hook and the reason and nothing else (no caller-supplied
// text), naming no cause (the claimed one is not its cause), and like any
// write it has its outbox row.
func assertOneRefusal(t *testing.T, id uuid.UUID, typeID, hook string) {
	t.Helper()
	got := causeRefusals(t, id)
	if len(got) != 1 {
		t.Errorf("%s records = %+v, want exactly one", reasonCauseNotConfirmed, got)
		return
	}
	e := got[0]
	if len(e.Payload) != 2 || e.Payload["hook"] != hook {
		t.Errorf("the record's payload = %v, want only hook = %s and the reason", e.Payload, hook)
	}
	if want := "hook:" + typeID + ":" + hook; e.Actor != want {
		t.Errorf("the record's actor = %s, want %s", e.Actor, want)
	}
	if e.CausationID != "" {
		t.Errorf("the record's causation_id = %s, want none: a claimed cause is not a cause", e.CausationID)
	}
	var outbox int
	if err := pool.QueryRow(serverCtx(t), `select count(*) from outbox where item_id = $1 and item_seq = $2`,
		id, e.Seq).Scan(&outbox); err != nil || outbox != 1 {
		t.Errorf("outbox rows for the record = %d (%v), want 1", outbox, err)
	}
}

// lastEntry is the item_seq of the event that last entered a state.
func lastEntry(t *testing.T, id uuid.UUID) int64 {
	t.Helper()
	var seq int64
	if err := pool.QueryRow(serverCtx(t),
		`select item_seq from item_events where item_id = $1 and op in ('create', 'transition')
		 order by item_seq desc limit 1`, id).Scan(&seq); err != nil {
		t.Fatalf("read the last entry: %v", err)
	}
	return seq
}

func seqOf(t *testing.T, id uuid.UUID) int64 {
	t.Helper()
	var seq int64
	if err := pool.QueryRow(serverCtx(t), `select item_seq from items where id = $1`, id).Scan(&seq); err != nil {
		t.Fatalf("read item_seq: %v", err)
	}
	return seq
}

// --- the attack ---

// The bake-off's side door. A ticket is in in_progress: nobody approved
// it. A workflow started by hand says it is approved and that its invoice
// action is pending, caused by the ticket's last real event. The ERP must
// receive nothing, the ticket must not get an invoice ID, and the ticket
// records the refusal once.
func TestE2E_ForgedWorkflowStart_PlacesNoOrder(t *testing.T) {
	s := newInvoiceStack(t, invoiceOpts{budget: 10 * time.Second})
	ana := approver(t)
	id, customer := s.ticket(ana)
	entered := lastEntry(t, id) // the real entry into in_progress

	forged := wf.Input{ItemID: id.String(), Carried: &wf.Snapshot{
		TypeID: "ticket", TypeVersion: 4, State: "approved", LastAppliedSeq: 999, EnteredSeq: entered,
		PendingActions: []wf.PendingAction{{HookID: "create_invoice", CauseSeq: entered}},
	}}
	workflowID := "sidedoor-" + uuid.NewString()
	run, err := tc.ExecuteWorkflow(serverCtx(t), client.StartWorkflowOptions{ID: workflowID, TaskQueue: s.queue}, wf.Name, forged)
	if err != nil {
		t.Fatalf("start the forged workflow: %v", err) // the door is open: that is Temporal's, not this step's
	}
	if err := run.Get(serverCtx(t), nil); err != nil {
		t.Fatalf("the forged workflow: %v", err)
	}

	if got := s.erp.Invoices(customer); len(got) != 0 {
		t.Errorf("the ERP received %v for a ticket nobody approved, want nothing", got)
	}
	if f := s.fieldsOf(ana, id)["f_invoice_id"]; f != nil {
		t.Errorf("f_invoice_id = %v on a ticket nobody approved, want unset", f)
	}
	if st, _ := s.state(id); st != "in_progress" {
		t.Errorf("the ticket is %s, want in_progress still", st)
	}
	assertOneRefusal(t, id, "ticket", "create_invoice")
	if failed := hookFailedEvents(t, id); len(failed) != 1 {
		t.Errorf("hook_failed events = %+v, want only the refusal (no action_failed beside it)", failed)
	}

	// In Temporal's history: one failed activity, of the refusal's type,
	// never retried.
	var types []string
	final := true
	iter := tc.GetWorkflowHistory(serverCtx(t), workflowID, "", false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
	for iter.HasNext() {
		ev, err := iter.Next()
		if err != nil {
			t.Fatalf("read history: %v", err)
		}
		if failed := ev.GetActivityTaskFailedEventAttributes(); failed != nil {
			info := failed.GetFailure().GetApplicationFailureInfo()
			types = append(types, info.GetType())
			final = final && info.GetNonRetryable()
		}
	}
	if len(types) != 1 || types[0] != errTypeCauseNotConfirmed || !final {
		t.Errorf("failed activities = %v (all final: %v), want one non-retryable %s", types, final, errTypeCauseNotConfirmed)
	}
}

// --- the key is the kernel's, never the caller's ---

// A ticket really approved, its invoice really made. A forged activity
// input names the true cause with a made-up key, twice. Today each new key
// is a new order at the ERP. The key must be the one the kernel derives
// from the confirmed cause: anything else is refused, and one order stands.
func TestActivities_RunAction_MadeUpKeys_PlaceNoSecondOrder(t *testing.T) {
	s := newInvoiceStack(t, invoiceOpts{budget: 10 * time.Second})
	s.stopWorker()
	ana := approver(t)
	id, customer := s.ticket(ana)
	approval := s.approveByHand(ana, id)
	a := &wf.Activities{Items: s.k, Connectors: hooks.NewConnectors(s.crm.URL, s.erp.URL)}

	if err := a.RunAction(serverCtx(t), actionInputFor(id, approval)); err != nil {
		t.Fatalf("the real run: %v", err)
	}
	first := s.fieldsOf(ana, id)["f_invoice_id"]
	for _, key := range []string{"made-up-1", "made-up-2"} {
		in := actionInputFor(id, approval)
		in.IdempotencyKey = key
		assertCauseRefused(t, "key "+key, a.RunAction(serverCtx(t), in))
	}
	if got := s.erp.Invoices(customer); len(got) != 1 {
		t.Errorf("ERP invoices %v, want the one real order", got)
	}
	if f := s.fieldsOf(ana, id)["f_invoice_id"]; f != first {
		t.Errorf("f_invoice_id = %v, want the real order %v still", f, first)
	}
	assertOneRefusal(t, id, "ticket", "create_invoice") // once per Item, hook and claimed event
}

// --- on: enter ---

// The cause must be the entry into the hook's own state. Another real
// event of a ticket that really was approved (a later update, an earlier
// entry into another state) is not it, even with the key that event would
// derive.
func TestActivities_RunAction_Enter_AnotherRealEventIsNotTheCause(t *testing.T) {
	s := newInvoiceStack(t, invoiceOpts{budget: 10 * time.Second})
	s.stopWorker()
	ana := approver(t)
	id, customer := s.ticket(ana)
	inProgress := lastEntry(t, id)
	approval := s.approveByHand(ana, id)
	a := &wf.Activities{Items: s.k, Connectors: hooks.NewConnectors(s.crm.URL, s.erp.URL)}
	if err := a.RunAction(serverCtx(t), actionInputFor(id, approval)); err != nil {
		t.Fatalf("the real run: %v", err)
	}
	upd, err := s.k.Update(serverCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: "key-" + uuid.NewString(), Principal: ana, ItemID: id, Fields: map[string]any{"f_title": "Onboarding, again"},
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	for what, seq := range map[string]int64{"a later update": upd.ItemSeq, "the entry into in_progress": inProgress} {
		assertCauseRefused(t, what, a.RunAction(serverCtx(t), actionInputFor(id, seq)))
	}
	if got := s.erp.Invoices(customer); len(got) != 1 {
		t.Errorf("ERP invoices %v, want the one real order", got)
	}
}

// A retry confirms again, and an action still runs for its entry after
// the Item has moved on: the cause is an event, not the Item's state now.
// (Both hold today; they must go on holding.)
func TestActivities_RunAction_Enter_ConfirmsAgainOnRetry_AndAfterTheItemMovedOn(t *testing.T) {
	s := newInvoiceStack(t, invoiceOpts{budget: 10 * time.Second})
	s.stopWorker()
	ana := approver(t)
	id, customer := s.ticket(ana)
	approval := s.approveByHand(ana, id)
	if _, err := s.k.Transition(serverCtx(t), itemservice.TransitionRequest{
		IdempotencyKey: "key-" + uuid.NewString(), Principal: ana, ItemID: id, ToState: "resolved",
		Fields: map[string]any{"f_resolution": "fixed"},
	}); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	a := &wf.Activities{Items: s.k, Connectors: hooks.NewConnectors(s.crm.URL, s.erp.URL)}
	for i := 1; i <= 2; i++ {
		if err := a.RunAction(serverCtx(t), actionInputFor(id, approval)); err != nil {
			t.Fatalf("run #%d for the approval, the ticket now resolved: %v", i, err)
		}
	}
	s.assertOneInvoiceEach(ana, map[uuid.UUID]string{id: customer})
}

// --- what a forger can write into an Item's history is bounded ---

func TestActivities_RunAction_Refusal_RecordedOnce_AndOnlyForARealEventAndADeclaredHook(t *testing.T) {
	s := newInvoiceStack(t, invoiceOpts{budget: 10 * time.Second})
	s.stopWorker()
	ana := approver(t)
	id, customer := s.ticket(ana)
	entered := lastEntry(t, id)
	a := &wf.Activities{Items: s.k, Connectors: hooks.NewConnectors(s.crm.URL, s.erp.URL)}
	before := seqOf(t, id)

	// The same forged call, three times: refused each time, one record.
	for i := 1; i <= 3; i++ {
		assertCauseRefused(t, fmt.Sprintf("forged call #%d", i), a.RunAction(serverCtx(t), actionInputFor(id, entered)))
	}
	assertOneRefusal(t, id, "ticket", "create_invoice")
	if got := seqOf(t, id); got != before+1 {
		t.Errorf("item_seq went from %d to %d, want one event: the record", before, got)
	}

	// A made-up event: refused, and nothing is recorded (nothing checks out
	// to hang a record on).
	assertCauseRefused(t, "a made-up event", a.RunAction(serverCtx(t), actionInputFor(id, 9999)))

	// A hook the Type does not declare, its "ID" free text: refused for
	// good, and the text reaches no payload.
	in := actionInputFor(id, entered)
	in.HookID = "no_such_hook someone@example.com"
	in.IdempotencyKey = fmt.Sprintf("hook:%s:%d:%s", id, entered, in.HookID)
	err := a.RunAction(serverCtx(t), in)
	var app *temporal.ApplicationError
	if !errors.As(err, &app) || !app.NonRetryable() {
		t.Errorf("an undeclared hook: error = %v, want a non-retryable one", err)
	}

	if got := seqOf(t, id); got != before+1 {
		t.Errorf("item_seq = %d after a made-up event and an undeclared hook, want %d: no further record", got, before+1)
	}
	if got := s.erp.Invoices(customer); len(got) != 0 {
		t.Errorf("ERP invoices %v, want none", got)
	}
}

// --- the action's own records confirm the cause too ---

// hook_failed and hook_ran are how an action completes. Written for a
// cause the hook is not bound to, they are false history. The item service
// refuses them, and writes nothing.
func TestActivities_HookFailed_ForACauseThatIsNotTheHooks_WritesNothing(t *testing.T) {
	s := newInvoiceStack(t, invoiceOpts{budget: 10 * time.Second})
	s.stopWorker()
	ana := approver(t)
	id, _ := s.ticket(ana)
	entered := lastEntry(t, id) // into in_progress; create_invoice is bound to approved
	a := &wf.Activities{Items: s.k, Connectors: hooks.NewConnectors(s.crm.URL, s.erp.URL)}
	before := seqOf(t, id)

	err := a.HookFailed(serverCtx(t), wf.HookFailedInput{Hook: actionInputFor(id, entered), Reason: itemservice.ReasonActionFailed})
	assertCauseRefused(t, "hook_failed for the entry into in_progress", err)
	if got := seqOf(t, id); got != before {
		t.Errorf("item_seq went from %d to %d, want no event", before, got)
	}
}

func TestInternal_HookRan_ForACauseThatIsNotTheHooks_WritesNothing(t *testing.T) {
	m := newMailStack(t)
	m.stopRelay()
	m.stopWorker()
	m.balance(25)
	id := m.submittedByHandV3(t, 5)
	before := seqOf(t, id)

	// remind_approver belongs to the acting timer of submitted; event 1 is
	// the create, into draft.
	_, err := m.k.Internal().HookRan(serverCtx(t), itemservice.HookRanRequest{
		IdempotencyKey: fmt.Sprintf("timer:%s:remind:1:1", id), Principal: item.Principal{Actor: "hook:vacation_request:remind_approver"},
		ItemID: id, Timer: "remind", Run: 1,
		Attribution: item.Attribution{OriginatingHook: "hook:vacation_request@v3:remind_approver", CausationID: fmt.Sprintf("%s:1", id), Hop: 1},
	})
	if err == nil {
		t.Error("hook_ran for the entry into draft was written, want it refused")
	}
	if got := seqOf(t, id); got != before {
		t.Errorf("item_seq went from %d to %d, want no event", before, got)
	}
}

// Residue. A forged hook_failed that names the hook's TRUE entry checks
// out, is recorded, and the action then counts as done: a restarted run
// will not place the order. Confirming the cause cannot tell it from a
// real failure (the budget running out is known only to the workflow). It
// withholds an effect; it never makes one. Closing it means authenticating
// Temporal.
func TestResidue_AForgedHookFailedForTheTrueEntry_StillCompletesTheAction(t *testing.T) {
	s := newInvoiceStack(t, invoiceOpts{budget: 10 * time.Second})
	s.stopWorker()
	ana := approver(t)
	id, customer := s.ticket(ana)
	approval := s.approveByHand(ana, id)
	a := &wf.Activities{Items: s.k, Connectors: hooks.NewConnectors(s.crm.URL, s.erp.URL)}

	if err := a.HookFailed(serverCtx(t), wf.HookFailedInput{Hook: actionInputFor(id, approval), Reason: itemservice.ReasonBudgetExhausted}); err != nil {
		t.Fatalf("hook_failed for the true entry: %v", err)
	}
	se, err := s.k.StateEntry(serverCtx(t), id)
	if err != nil {
		t.Fatal(err)
	}
	if len(se.PendingActions) != 0 {
		t.Errorf("PendingActions = %+v; the residue as pinned is that the action counts as done", se.PendingActions)
	}
	if got := s.erp.Invoices(customer); len(got) != 0 {
		t.Errorf("ERP invoices %v, want none: nothing was sent", got)
	}
}

// --- on: reaction ---

// refund_email runs when the refund has applied: its cause is that
// reaction_applied event. An entry into a state is not it.
func TestActivities_RunAction_Reaction_AnEntryIsNotTheCause(t *testing.T) {
	m := newMailStack(t)
	m.stopRelay()
	m.stopWorker()
	m.balance(25)
	id := m.submittedByHandV3(t, 5)
	entered := lastEntry(t, id)
	a := &wf.Activities{Items: m.k, Connectors: m.connectors()}

	err := a.RunAction(serverCtx(t), wf.HookInput{ItemID: id.String(), TypeID: "vacation_request", TypeVersion: 3,
		HookID: "refund_email", CauseSeq: entered, Hop: 1,
		IdempotencyKey: fmt.Sprintf("hook:%s:%d:refund_email", id, entered)})
	assertCauseRefused(t, "refund_email for the entry into submitted", err)
	if n, to := m.sent(id, "refund_email"); n != 0 {
		t.Errorf("refund emails sent = %d to %v, for a request never approved, want none", n, to)
	}
	assertOneRefusal(t, id, "vacation_request", "refund_email")
}

// --- acting timers with a schedule (after / every) ---

func forgedRemind(id uuid.UUID, cause int64, timer string, run int) wf.HookInput {
	return wf.HookInput{ItemID: id.String(), TypeID: "vacation_request", TypeVersion: 3, HookID: "remind_approver",
		CauseSeq: cause, Hop: 1, Timer: timer, Run: run,
		IdempotencyKey: fmt.Sprintf("timer:%s:%s:%d:%d", id, timer, cause, run)}
}

// A reminder's cause is the entry into its timer's state, by one of the
// timer's own runs. Each of these is refused before anything is sent
// (today the last two are sent, and only the record afterwards fails).
func TestActivities_RunAction_Timer_TheEntryTheTimerAndTheRunAreConfirmed(t *testing.T) {
	m := newMailStack(t)
	m.stopRelay()
	m.stopWorker()
	m.balance(25)
	id := m.submittedByHandV3(t, 5)
	entered := lastEntry(t, id)
	a := &wf.Activities{Items: m.k, Connectors: m.connectors()}

	for what, in := range map[string]wf.HookInput{
		"the create, into draft":           forgedRemind(id, 1, "remind", 1),
		"a timer the state does not arm":   forgedRemind(id, entered, "nudge", 1),
		"run 3 of a timer that runs twice": forgedRemind(id, entered, "remind", 3),
		"run 0":                            forgedRemind(id, entered, "remind", 0),
	} {
		assertCauseRefused(t, what, a.RunAction(serverCtx(t), in))
	}
	if n, to := m.sent(id, "remind_approver"); n != 0 {
		t.Errorf("reminders sent = %d to %v, want none", n, to)
	}
}

// Residue. A run of a scheduled acting timer leaves no committed event
// until its hook finishes, so the item service cannot tell that the run
// was due. A forged input for a request that really entered submitted can
// send a reminder early: once per run, because the key is the kernel's.
func TestResidue_AForgedEarlyReminder_IsSent_OncePerRun(t *testing.T) {
	m := newMailStack(t)
	m.stopRelay()
	m.stopWorker()
	m.balance(25)
	id := m.submittedByHandV3(t, 5)
	entered := lastEntry(t, id)
	a := &wf.Activities{Items: m.k, Connectors: m.connectors()}

	// Run 1 is due 48 h after the entry; this is at once.
	for i := 1; i <= 2; i++ {
		if err := a.RunAction(serverCtx(t), forgedRemind(id, entered, "remind", 1)); err != nil {
			t.Fatalf("early run 1, call #%d: %v", i, err)
		}
	}
	if n, _ := m.sent(id, "remind_approver"); n != 1 {
		t.Errorf("reminders sent = %d; the residue as pinned is one, early, however often it is asked", n)
	}
}

// --- acting timers at an instant ---

// causeBellType: an acting timer at an instant with a hook and no `sets`
// (no Type of types/ has one). ActAt writes nothing for it.
const causeBellType = `
id: causebell
version: 1
initial_state: set
fields:
  - {id: f_at, type: datetime}
states:
  - id: set
    category: active
    acting:
      - {id: ring, at: f_at, hook: tell}
  - {id: "off", category: done}
transitions:
  - {from: set, to: "off"}
hooks:
  - {id: tell, on: timer, kind: action, action: email.notify, reads: [creator]}
`

var registerCauseBellOnce sync.Once

// alarmStack: the wf3alarm Type (an acting timer at an instant with `sets`
// and a hook) and causebell, an email stub, and no worker: the tests play
// the workflow's part.
func newAlarmStack(t *testing.T) (*jobStack, *connstub.Server, *wf.Activities) {
	t.Helper()
	s := newJobStack(t)
	s.stopRelay()
	s.stopWorker()
	registerCauseBellOnce.Do(func() {
		ty, err := typedef.Parse([]byte(causeBellType))
		if err != nil {
			t.Fatalf("parse causebell: %v", err)
		}
		if err := itemservice.New(pool).RegisterType(serverCtx(t), ty); err != nil {
			t.Fatalf("register causebell: %v", err)
		}
	})
	mail := connstub.NewEmail()
	t.Cleanup(mail.Close)
	return s, mail, &wf.Activities{Items: s.k,
		Connectors: hooks.NewConnectors("http://127.0.0.1:1", "http://127.0.0.1:1").WithEmail(mail.URL)}
}

// forgedTell is what the workflow gives RunAction once ActAt has acted for
// an alarm's instant.
func forgedTell(typeID string, id uuid.UUID, instant time.Time) wf.HookInput {
	return wf.HookInput{ItemID: id.String(), TypeID: typeID, TypeVersion: 1, HookID: "tell",
		CauseSeq: 1, Hop: 1, Timer: "ring",
		IdempotencyKey: fmt.Sprintf("timer:%s:ring:1:%s:hook", id, dt(instant))}
}

// A timer at an instant that writes (`sets`) leaves proof that it acted
// for that instant: its hook runs only with that proof. An alarm set for
// an hour from now has not rung, whatever a workflow says.
func TestActivities_RunAction_ActingAt_NeedsTheTimersWriteForThatInstant(t *testing.T) {
	s, mail, a := newAlarmStack(t)

	// Not rung: nothing is sent.
	later := time.Now().Add(time.Hour).UTC().Truncate(time.Microsecond)
	quiet := s.makeIn(alice, "wf3alarm", nil, map[string]any{"f_at": later.Format(time.RFC3339Nano)})
	assertCauseRefused(t, "an alarm that has not rung", a.RunAction(serverCtx(t), forgedTell("wf3alarm", quiet, later)))
	if got := mail.Messages(quiet.String()); len(got) != 0 {
		t.Errorf("emails about an alarm that has not rung: %+v, want none", got)
	}
	assertOneRefusal(t, quiet, "wf3alarm", "tell")

	// Rung (the timer's write, made here as the ActAt activity makes it):
	// the hook runs, and a retry confirms again and sends nothing more.
	past := time.Now().Add(-2 * time.Second).UTC().Truncate(time.Microsecond)
	rung := s.makeIn(alice, "wf3alarm", nil, map[string]any{"f_at": past.Format(time.RFC3339Nano)})
	res, err := s.k.Internal().ActAt(serverCtx(t), itemservice.ActAtRequest{
		ItemID: rung, Timer: "ring", EnteredSeq: 1, Instant: past, Attribution: item.Attribution{Hop: 1}})
	if err != nil || !res.Applied || !res.RunHook {
		t.Fatalf("ActAt = %+v, %v; want applied, with its hook to run", res, err)
	}
	for i := 1; i <= 2; i++ {
		if err := a.RunAction(serverCtx(t), forgedTell("wf3alarm", rung, past)); err != nil {
			t.Fatalf("tell after the alarm rang, call #%d: %v", i, err)
		}
	}
	// Another instant of the same alarm, for which the timer never acted.
	assertCauseRefused(t, "an instant the timer never acted for",
		a.RunAction(serverCtx(t), forgedTell("wf3alarm", rung, past.Add(time.Hour))))
	if got := mail.Messages(rung.String()); len(got) != 1 {
		t.Errorf("emails about the alarm that rang: %+v, want one", got)
	}
}

// Residue. A timer at an instant with a hook and no `sets` writes nothing
// when it acts, so there is no proof to ask for: a forged input for an Item
// really in the timer's state can run the hook before the instant, once
// per instant named.
func TestResidue_AHookOnlyTimerAtAnInstant_AForgedRunIsSent(t *testing.T) {
	s, mail, a := newAlarmStack(t)
	later := time.Now().Add(time.Hour).UTC().Truncate(time.Microsecond)
	bell := s.makeIn(alice, "causebell", nil, map[string]any{"f_at": later.Format(time.RFC3339Nano)})
	for i := 1; i <= 2; i++ {
		if err := a.RunAction(serverCtx(t), forgedTell("causebell", bell, later)); err != nil {
			t.Fatalf("forged tell, call #%d: %v", i, err)
		}
	}
	if got := mail.Messages(bell.String()); len(got) != 1 {
		t.Errorf("emails = %+v; the residue as pinned is one, an hour early", got)
	}
}
