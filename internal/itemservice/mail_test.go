// Tests for proof-of-concept Step 2, part 3, through the item service:
// field-less actions and the refund email.
//
//   - Internal().HookRan records that an action hook with no writes ran
//     (email.notify): op hook_ran, payload {hook[, timer, run]}, no field,
//     no state, under the hook's key (a retry records nothing more). It is
//     exempt from ErrHeld and from frozen fields by construction. Only a
//     field-less action hook writes it; an acting timer's hook names its
//     timer and run (at most the timer's max).
//   - Hooks read creator: created_on_behalf_of, else created_by.
//   - Internal().ReactionApplied records on the request that an undo
//     committed (payload: the reaction and the target event), under
//     <refund key>:applied, and refuses (not_applied) when nothing was
//     given back. A pending undo is done only once both the refund and its
//     reaction_applied exist.
//   - The refund email (on: reaction) is a pending action from the moment
//     reaction_applied commits until hook_ran records it: a run after a
//     crash in that window sends it.
//   - StateEntry.ActingRuns counts an acting timer's completed runs
//     (hook_ran or hook_failed) for the current entry; hook_failed names
//     the run.
package itemservice_test

import (
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
)

// mailWorld is the leave world with vacation_request v3.
type mailWorld struct {
	leaveWorld
}

func newMailWorld(t *testing.T) mailWorld {
	t.Helper()
	w := mailWorld{newLeaveWorld(t)}
	registerFromFile(t, w.k, "vacation_request.v3.yaml")
	return w
}

func v3Hook(id string) item.Principal { return item.Principal{Actor: "hook:vacation_request:" + id} }

func vr3Cause(kind, name string, id uuid.UUID, seq int64, hop int) item.Attribution {
	return item.Attribution{
		OriginatingHook: fmt.Sprintf("%s:vacation_request@v3:%s", kind, name),
		CausationID:     fmt.Sprintf("%s:%d", id, seq), Hop: hop,
	}
}

// submittedV3: ana's v3 request of `days`, in submitted (HR names bob);
// returns it and the event that entered submitted.
func (w mailWorld) submittedV3(t *testing.T, days int) (uuid.UUID, int64) {
	t.Helper()
	res, err := w.k.Create(testCtx(t), itemservice.CreateRequest{
		IdempotencyKey: newKey(), Principal: w.ana, TypeID: "vacation_request", TypeVersion: 3, Fields: leaveTerms(days),
	})
	if err != nil {
		t.Fatalf("create v3: %v", err)
	}
	id := res.ItemID
	ask := w.askSubmit(t, w.ana, id)
	entered, err := w.k.Internal().ResolveHold(testCtx(t), itemservice.ResolveHoldRequest{
		IdempotencyKey: "hold:" + ask.HoldID.String(), Principal: hrCheck, ItemID: id, HoldID: ask.HoldID,
		Verdict: itemservice.VerdictCorrect, Fields: map[string]any{"f_approver": w.bob.Actor},
		Attribution: vr3Cause("hook", "hr_check", id, ask.ItemSeq, 1),
	})
	if err != nil || w.state(t, id) != "submitted" {
		t.Fatalf("HR: %v, state %s", err, w.state(t, id))
	}
	return id, entered.ItemSeq
}

func (w mailWorld) hookRan(t *testing.T, hook string, id uuid.UUID, cause int64, timer string, run int) (itemservice.WriteResult, error) {
	t.Helper()
	key := fmt.Sprintf("hook:%s:%d:%s", id, cause, hook)
	if timer != "" {
		key = fmt.Sprintf("timer:%s:%s:%d:%d", id, timer, cause, run)
	}
	return w.k.Internal().HookRan(testCtx(t), itemservice.HookRanRequest{
		IdempotencyKey: key, Principal: v3Hook(hook), ItemID: id, Timer: timer, Run: run,
		Attribution: vr3Cause("hook", hook, id, cause, hopOf(t, id, cause)+1),
	})
}

// approveV3 plays deduct for v3: React, then allow (or deny with the
// refusal). It returns the approval request.
func (w mailWorld) approveV3(t *testing.T, id uuid.UUID) itemservice.TransitionResult {
	t.Helper()
	ask := w.askApproval(t, w.bob, id)
	cause := vr3Cause("reaction", "deduct", id, ask.ItemSeq, 1)
	_, err := w.k.Internal().React(testCtx(t), itemservice.ReactRequest{Principal: deductP, SourceID: id, HoldID: ask.HoldID, Attribution: cause})
	verdict, reason := itemservice.VerdictAllow, ""
	if err != nil {
		var ok bool
		if reason, ok = itemservice.DenyReason(err); !ok {
			t.Fatalf("React: %v", err)
		}
		verdict = itemservice.VerdictDeny
	}
	if _, err := w.k.Internal().ResolveHold(testCtx(t), itemservice.ResolveHoldRequest{
		IdempotencyKey: "hold:" + ask.HoldID.String(), Principal: deductP, ItemID: id, HoldID: ask.HoldID,
		Verdict: verdict, Reason: reason, Attribution: cause}); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	return ask
}

func (w mailWorld) undoV3(t *testing.T, id, hold uuid.UUID, cause int64) (itemservice.UndoResult, error) {
	t.Helper()
	return w.k.Internal().Undo(testCtx(t), itemservice.UndoRequest{Principal: refundP, SourceID: id, HoldID: hold,
		Attribution: vr3Cause("reaction", "refund", id, cause, hopOf(t, id, cause)+1)})
}

func (w mailWorld) applied(t *testing.T, id, hold uuid.UUID, cause int64) (itemservice.WriteResult, error) {
	t.Helper()
	return w.k.Internal().ReactionApplied(testCtx(t), itemservice.ReactionAppliedRequest{Principal: refundP, SourceID: id,
		HoldID: hold, Attribution: vr3Cause("reaction", "refund", id, cause, hopOf(t, id, cause)+1)})
}

// get reads the request as ana (its creator).
func (w mailWorld) get(t *testing.T, id uuid.UUID) item.Item {
	t.Helper()
	it, err := w.k.Get(testCtx(t), w.ana, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	return it
}

func (w mailWorld) entry(t *testing.T, id uuid.UUID) itemservice.StateEntry {
	t.Helper()
	se, err := w.k.StateEntry(testCtx(t), id)
	if err != nil {
		t.Fatalf("StateEntry: %v", err)
	}
	return se
}

// --- hook_ran ---

// A reminder: one field-less event, as the hook, caused by the entry, with
// its timer and run; a retry records nothing more; history shows it.
func TestHookRan_RecordsAFieldlessEvent(t *testing.T) {
	w := newMailWorld(t)
	w.balance(t, w.ana, 25)
	id, entered := w.submittedV3(t, 5)
	before := w.get(t, id)

	res, err := w.hookRan(t, "remind_approver", id, entered, "remind", 1)
	if err != nil {
		t.Fatalf("HookRan: %v", err)
	}
	op, payload := event(t, id, res.ItemSeq)
	if op != "hook_ran" || payload["hook"] != "remind_approver" || payload["timer"] != "remind" || payload["run"] != float64(1) {
		t.Errorf("event = %s %v; want hook_ran naming remind_approver, timer remind, run 1", op, payload)
	}
	if _, ok := payload["field_ids"]; ok {
		t.Errorf("payload %v names fields: hook_ran changes none", payload)
	}
	if n := count(t, `select count(*) from item_event_values where item_id = $1 and item_seq = $2`, id, res.ItemSeq); n != 0 {
		t.Errorf("values rows = %d, want none", n)
	}
	after := w.get(t, id)
	if after.State != before.State || fmt.Sprint(after.Fields) != fmt.Sprint(before.Fields) || after.ItemSeq != before.ItemSeq+1 {
		t.Errorf("Item changed: %s %v -> %s %v", before.State, before.Fields, after.State, after.Fields)
	}
	actor, a := attribution(t, id, res.ItemSeq)
	if actor != "hook:vacation_request:remind_approver" || a.OriginatingHook != "hook:vacation_request@v3:remind_approver" ||
		a.CausationID != fmt.Sprintf("%s:%d", id, entered) {
		t.Errorf("attribution = %s %+v", actor, a)
	}
	again, err := w.hookRan(t, "remind_approver", id, entered, "remind", 1)
	if err != nil || again != res {
		t.Errorf("HookRan again = %+v, %v; want %+v", again, err, res)
	}
	assertEventCount(t, id, int(res.ItemSeq))

	page, err := w.k.ListEvents(testCtx(t), itemservice.ListEventsRequest{Principal: inDefault(w.ana), ItemID: id})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	last := page.Events[len(page.Events)-1]
	if last.Op != "hook_ran" || last.Hook != "remind_approver" || last.Timer != "remind" || last.Run != 1 {
		t.Errorf("history = %+v", last)
	}
}

// hook_ran names no field and makes no move: an open hold and frozen fields
// do not refuse it (submitted freezes the terms; the approval holds it).
func TestHookRan_ExemptFromHeldAndFrozen(t *testing.T) {
	w := newMailWorld(t)
	w.balance(t, w.ana, 25)
	id, entered := w.submittedV3(t, 5)
	w.askApproval(t, w.bob, id) // deduct's hold is open
	if h := w.holdNow(t, id); h.Status != item.HoldOpen {
		t.Fatalf("hold = %+v, want open", h)
	}
	if _, err := w.hookRan(t, "remind_approver", id, entered, "remind", 1); err != nil {
		t.Errorf("a reminder during the hold, in a state that freezes fields: %v, want it recorded", err)
	}
}

func TestHookRan_IsChecked(t *testing.T) {
	w := newMailWorld(t)
	w.balance(t, w.ana, 25)
	id, entered := w.submittedV3(t, 5)
	ok := itemservice.HookRanRequest{IdempotencyKey: newKey(), Principal: v3Hook("remind_approver"), ItemID: id,
		Timer: "remind", Run: 1, Attribution: vr3Cause("hook", "remind_approver", id, entered, 1)}
	cases := []struct {
		name   string
		mutate func(r *itemservice.HookRanRequest)
	}{
		{"a check hook", func(r *itemservice.HookRanRequest) {
			r.Principal, r.Attribution.OriginatingHook, r.Timer, r.Run = hrCheck, "hook:vacation_request@v3:hr_check", "", 0
		}},
		{"an undeclared hook", func(r *itemservice.HookRanRequest) {
			r.Principal, r.Attribution.OriginatingHook = v3Hook("nope"), "hook:vacation_request@v3:nope"
		}},
		{"the timer", func(r *itemservice.HookRanRequest) {
			r.Principal, r.Attribution.OriginatingHook = item.Principal{Actor: "system:timer"}, "timer:vacation_request@v3:submitted"
		}},
		{"a reaction", func(r *itemservice.HookRanRequest) {
			r.Principal, r.Attribution.OriginatingHook = refundP, "reaction:vacation_request@v3:refund"
		}},
		{"a timer hook without its timer", func(r *itemservice.HookRanRequest) { r.Timer, r.Run = "", 0 }},
		{"another timer", func(r *itemservice.HookRanRequest) { r.Timer = "nudge" }},
		{"run 0", func(r *itemservice.HookRanRequest) { r.Run = 0 }},
		{"a run beyond max (2)", func(r *itemservice.HookRanRequest) { r.Run = 3 }},
		{"an enter hook with a timer", func(r *itemservice.HookRanRequest) {
			r.Principal, r.Attribution.OriginatingHook = v3Hook("notify_approved"), "hook:vacation_request@v3:notify_approved"
		}},
		{"no key", func(r *itemservice.HookRanRequest) { r.IdempotencyKey = "" }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := ok
			r.IdempotencyKey = newKey()
			c.mutate(&r)
			if _, err := w.k.Internal().HookRan(testCtx(t), r); !errors.Is(err, itemservice.ErrInvalid) {
				t.Errorf("HookRan = %v, want ErrInvalid", err)
			}
		})
	}
	if _, err := w.k.Internal().HookRan(testCtx(t), ok); err != nil {
		t.Errorf("the good request: %v", err)
	}
}

// An action hook that writes back what it made (an invoice) proves it ran
// with its write-back, not hook_ran.
func TestHookRan_NotForAnActionThatWrites(t *testing.T) {
	k := itemservice.New(pool)
	registerInline(t, k, `
id: order
version: 1
initial_state: open
fields:
  - {id: f_customer, type: string}
  - {id: f_amount,   type: int}
  - {id: f_invoice,  type: string}
states:
  - {id: open, category: todo}
  - {id: done, category: done}
transitions:
  - {from: open, to: done}
hooks:
  - {id: invoice, on: enter, state: done, kind: action, action: erp.create_invoice,
     reads: [f_customer, f_amount], writes: [f_invoice]}
`)
	ana := person("ana")
	res, err := k.Create(testCtx(t), itemservice.CreateRequest{IdempotencyKey: newKey(), Principal: ana, TypeID: "order", TypeVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	_, err = k.Internal().HookRan(testCtx(t), itemservice.HookRanRequest{IdempotencyKey: newKey(),
		Principal: item.Principal{Actor: "hook:order:invoice"}, ItemID: res.ItemID,
		Attribution: item.Attribution{OriginatingHook: "hook:order@v1:invoice", CausationID: fmt.Sprintf("%s:1", res.ItemID), Hop: 1}})
	if !errors.Is(err, itemservice.ErrInvalid) {
		t.Errorf("HookRan for an action that writes: %v, want ErrInvalid", err)
	}
}

// --- creator ---

func TestHookInput_Creator(t *testing.T) {
	w := newMailWorld(t)
	w.balance(t, w.ana, 25)
	id, entered := w.submittedV3(t, 5)
	in, err := w.k.Internal().HookInput(testCtx(t), itemservice.HookInputRequest{Principal: v3Hook("notify_approved"), ItemID: id,
		Attribution: vr3Cause("hook", "notify_approved", id, entered, 1)})
	if err != nil || in["creator"] != w.ana.Actor || len(in) != 1 {
		t.Errorf("notify_approved's input = %v, %v; want only creator = %s", in, err, w.ana.Actor)
	}
	// For an agent's request on ana's behalf, creator is ana.
	agent := withSession(t, w.k, item.Principal{Actor: "agent:" + uuid.NewString()[:8], OnBehalfOf: w.ana.Actor})
	res, err := w.k.Create(testCtx(t), itemservice.CreateRequest{IdempotencyKey: newKey(), Principal: agent,
		TypeID: "vacation_request", TypeVersion: 3, Fields: leaveTerms(1)})
	if err != nil {
		t.Fatal(err)
	}
	in, err = w.k.Internal().HookInput(testCtx(t), itemservice.HookInputRequest{Principal: v3Hook("notify_approved"), ItemID: res.ItemID,
		Attribution: vr3Cause("hook", "notify_approved", res.ItemID, 1, 1)})
	if err != nil || in["creator"] != w.ana.Actor {
		t.Errorf("for the agent's request: %v, %v; want creator %s", in, err, w.ana.Actor)
	}
}

// --- reaction_applied and the refund email ---

// After a committed refund: reaction_applied on the request, keyed by the
// refund; the undo is done only now; the refund email is pending until
// hook_ran records it.
func TestReactionApplied_AfterACommittedUndo(t *testing.T) {
	w := newMailWorld(t)
	bal := w.balance(t, w.ana, 25)
	id, _ := w.submittedV3(t, 5)
	ask := w.approveV3(t, id)
	withdraw, err := w.move(t, w.ana, id, "withdrawn", nil)
	if err != nil {
		t.Fatalf("withdraw: %v", err)
	}
	undone, err := w.undoV3(t, id, ask.HoldID, withdraw.ItemSeq)
	if err != nil || !undone.Undone {
		t.Fatalf("Undo = %+v, %v", undone, err)
	}
	// The refund committed, reaction_applied not yet (a crash between them):
	// the undo is still pending, so a new run finishes it.
	if p := w.entry(t, id).PendingUndos; len(p) != 1 || p[0].HoldID != ask.HoldID {
		t.Errorf("PendingUndos before reaction_applied = %+v, want the undo still pending", p)
	}

	res, err := w.applied(t, id, ask.HoldID, withdraw.ItemSeq)
	if err != nil {
		t.Fatalf("ReactionApplied: %v", err)
	}
	op, payload := event(t, id, res.ItemSeq)
	if op != "reaction_applied" || payload["reaction"] != "vacation_request:refund" ||
		payload["target"] != fmt.Sprintf("%s:%d", bal, undone.ItemSeq) {
		t.Errorf("event = %s %v; want reaction_applied of vacation_request:refund on %s:%d", op, payload, bal, undone.ItemSeq)
	}
	if n := idempotencyRows(t, refundP, undone.Key+":applied"); n != 1 {
		t.Errorf("rows for %s:applied = %d, want 1", undone.Key, n)
	}
	again, err := w.applied(t, id, ask.HoldID, withdraw.ItemSeq)
	if err != nil || again != res {
		t.Errorf("ReactionApplied again = %+v, %v; want %+v", again, err, res)
	}
	se := w.entry(t, id)
	if len(se.PendingUndos) != 0 {
		t.Errorf("PendingUndos after reaction_applied = %+v, want none", se.PendingUndos)
	}
	// Crash window: reaction_applied committed, the email not sent yet.
	want := itemservice.PendingAction{Hook: "refund_email", CauseSeq: res.ItemSeq, Hop: hopOf(t, id, res.ItemSeq)}
	if !slices.Contains(se.PendingActions, want) {
		t.Errorf("PendingActions = %+v, want %+v", se.PendingActions, want)
	}
	if _, err := w.hookRan(t, "refund_email", id, res.ItemSeq, "", 0); err != nil {
		t.Fatalf("HookRan refund_email: %v", err)
	}
	for _, a := range w.entry(t, id).PendingActions {
		if a.Hook == "refund_email" {
			t.Errorf("refund_email still pending after hook_ran: %+v", a)
		}
	}
}

// Nothing given back, nothing to say: an undo that undid nothing (the
// approval was denied before anything was taken) cannot be recorded as
// applied, and no refund email can follow.
func TestReactionApplied_NotWithoutACommittedUndo(t *testing.T) {
	w := newMailWorld(t)
	w.balance(t, w.ana, 4)
	id, _ := w.submittedV3(t, 5)
	ask := w.approveV3(t, id) // denied: insufficient_balance
	if h := w.holdNow(t, id); h.Status != item.HoldDenied {
		t.Fatalf("hold = %+v, want denied", h)
	}
	closing := w.itemSeq(t, id)
	if res, err := w.undoV3(t, id, ask.HoldID, closing); err != nil || res.Undone {
		t.Fatalf("Undo = %+v, %v; want nothing undone", res, err)
	}
	seq := w.itemSeq(t, id)
	_, err := w.applied(t, id, ask.HoldID, closing)
	assertRejectedCode(t, "reaction_applied without a refund", err, itemservice.CodeNotApplied)
	if w.itemSeq(t, id) != seq {
		t.Error("a refused reaction_applied wrote an event")
	}
	for _, a := range w.entry(t, id).PendingActions {
		if a.Hook == "refund_email" {
			t.Errorf("a refund email is pending without a refund: %+v", a)
		}
	}
}

// The entry emails: notify_approved is pending once approved, until hook_ran.
func TestPendingActions_ApprovalEmail(t *testing.T) {
	w := newMailWorld(t)
	w.balance(t, w.ana, 25)
	id, _ := w.submittedV3(t, 5)
	w.approveV3(t, id)
	se := w.entry(t, id)
	if se.State != "approved" {
		t.Fatalf("state %s", se.State)
	}
	want := itemservice.PendingAction{Hook: "notify_approved", CauseSeq: se.EnteredSeq, Hop: se.Hop}
	if !slices.Contains(se.PendingActions, want) {
		t.Errorf("PendingActions = %+v, want %+v", se.PendingActions, want)
	}
	if _, err := w.hookRan(t, "notify_approved", id, se.EnteredSeq, "", 0); err != nil {
		t.Fatalf("HookRan: %v", err)
	}
	if p := w.entry(t, id).PendingActions; slices.ContainsFunc(p, func(a itemservice.PendingAction) bool { return a.Hook == "notify_approved" }) {
		t.Errorf("notify_approved still pending: %+v", p)
	}
}

// --- acting runs ---

func TestStateEntry_ActingRuns(t *testing.T) {
	w := newMailWorld(t)
	w.balance(t, w.ana, 25)
	id, entered := w.submittedV3(t, 5)
	if got := w.entry(t, id).ActingRuns["remind"]; got != 0 {
		t.Errorf("runs before any = %d, want 0", got)
	}
	if _, err := w.hookRan(t, "remind_approver", id, entered, "remind", 1); err != nil {
		t.Fatal(err)
	}
	if got := w.entry(t, id).ActingRuns["remind"]; got != 1 {
		t.Errorf("runs after one = %d, want 1", got)
	}
	// A run that gave up counts too, and its hook_failed names the run.
	failed, err := w.k.Internal().HookFailed(testCtx(t), itemservice.HookFailedRequest{
		IdempotencyKey: fmt.Sprintf("hook_failed:timer:%s:remind:%d:2", id, entered), Principal: v3Hook("remind_approver"),
		ItemID: id, Reason: itemservice.ReasonBudgetExhausted, Timer: "remind", Run: 2,
		Attribution: vr3Cause("hook", "remind_approver", id, entered, 1)})
	if err != nil {
		t.Fatalf("HookFailed: %v", err)
	}
	if _, payload := event(t, id, failed.ItemSeq); payload["timer"] != "remind" || payload["run"] != float64(2) {
		t.Errorf("hook_failed payload = %v, want timer remind, run 2", payload)
	}
	if got := w.entry(t, id).ActingRuns["remind"]; got != 2 {
		t.Errorf("runs = %d, want 2", got)
	}
	// A new entry counts from 0: recall and resubmit.
	if _, err := w.move(t, w.ana, id, "draft", nil); err != nil {
		t.Fatal(err)
	}
	ask := w.askSubmit(t, w.ana, id)
	if _, err := w.k.Internal().ResolveHold(testCtx(t), itemservice.ResolveHoldRequest{
		IdempotencyKey: "hold:" + ask.HoldID.String(), Principal: hrCheck, ItemID: id, HoldID: ask.HoldID,
		Verdict: itemservice.VerdictCorrect, Fields: map[string]any{"f_approver": w.bob.Actor},
		Attribution: vr3Cause("hook", "hr_check", id, ask.ItemSeq, 1)}); err != nil {
		t.Fatal(err)
	}
	if got := w.entry(t, id).ActingRuns["remind"]; got != 0 {
		t.Errorf("runs after a new entry = %d, want 0", got)
	}
}
