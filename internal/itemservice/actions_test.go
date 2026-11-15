// Tests for Step 7: action hooks in the item service (ticket v4's
// create_invoice, on entering approved).
//
//   - An action hook, once its state is entered, always runs to completion,
//     even after the Item has moved on. StateEntry reports every action hook
//     not yet completed, for any entry of its state, not just the current
//     one. It finds them from the events: an action is complete for an
//     entry once an event names the hook as originating_hook and the entry
//     as causation_id (its write-back, or hook_failed).
//   - An action hook's write-back is accepted whatever state the Item is in.
//   - Internal().HookFailed records that an action hook gave up: a
//     hook_failed event with the hook and a reason code, nothing else. It
//     changes no field and no state.
package itemservice_test

import (
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
)

var (
	createInvoiceV4 = item.Principal{Actor: "hook:ticket:create_invoice"}
	erpCheckV4      = item.Principal{Actor: "hook:ticket:erp_check"}
)

// v4Cause is the attribution a hook of ticket v4 writes with.
func v4Cause(hookID string, id uuid.UUID, seq int64, hop int) item.Attribution {
	return item.Attribution{
		OriginatingHook: "hook:ticket@v4:" + hookID,
		CausationID:     fmt.Sprintf("%s:%d", id, seq),
		CorrelationID:   approveCorr,
		Hop:             hop,
	}
}

// v4Approve asks for in_progress -> approved as p and lets erp_check allow
// it. It returns the item_seq of the approval, the event that enters
// approved (written by the check, hop 1, in the approval's correlation).
func v4Approve(t *testing.T, k *itemservice.Service, p item.Principal, id uuid.UUID) int64 {
	t.Helper()
	res, err := approve(t, k, p, id, newKey(), 0)
	if err != nil || res.Outcome != itemservice.OutcomePending {
		t.Fatalf("approve = %+v, %v; want pending", res, err)
	}
	done, err := k.Internal().ResolveHold(testCtx(t), itemservice.ResolveHoldRequest{
		IdempotencyKey: "hold:" + res.HoldID.String(), Principal: erpCheckV4, ItemID: id, HoldID: res.HoldID,
		Verdict: itemservice.VerdictAllow, Attribution: v4Cause("erp_check", id, res.ItemSeq, 1),
	})
	if err != nil {
		t.Fatalf("resolve the hold: %v", err)
	}
	return done.ItemSeq
}

// v4Approved registers ticket v4 and walks a new ticket to approved:
// 1 create (triage), 2 open, 3 in_progress, 4 transition_requested,
// 5 transition into approved.
func v4Approved(t *testing.T, k *itemservice.Service) (uuid.UUID, item.Principal) {
	t.Helper()
	registerFromFile(t, k, "ticket.v4.yaml")
	ana := person("ana")
	grantRole(t, ana, "approver")
	res, err := k.Create(testCtx(t), itemservice.CreateRequest{
		IdempotencyKey: newKey(), Principal: ana, TypeID: "ticket", TypeVersion: 4,
		Fields: map[string]any{
			"f_title": "Onboarding", "f_customer_id": "cust-1", "f_amount": 100, "f_assignee": "user-bob",
		},
	})
	if err != nil {
		t.Fatalf("create ticket v4: %v", err)
	}
	moveAs(t, k, ana, res.ItemID, "open", nil)
	moveAs(t, k, ana, res.ItemID, "in_progress", nil)
	if seq := v4Approve(t, k, ana, res.ItemID); seq != 5 {
		t.Fatalf("approval at item_seq %d, want 5", seq)
	}
	return res.ItemID, ana
}

func moveAs(t *testing.T, k *itemservice.Service, p item.Principal, id uuid.UUID, to string, fields map[string]any) {
	t.Helper()
	if _, err := k.Transition(testCtx(t), itemservice.TransitionRequest{
		IdempotencyKey: newKey(), Principal: p, ItemID: id, ToState: to, Fields: fields,
	}); err != nil {
		t.Fatalf("transition to %s: %v", to, err)
	}
}

func stateEntry(t *testing.T, k *itemservice.Service, id uuid.UUID) itemservice.StateEntry {
	t.Helper()
	se, err := k.StateEntry(testCtx(t), id)
	if err != nil {
		t.Fatalf("StateEntry: %v", err)
	}
	return se
}

// writeInvoice writes f_invoice_id as create_invoice, caused by entry seq.
func writeInvoice(t *testing.T, k *itemservice.Service, id uuid.UUID, entry int64, invoice string) (itemservice.WriteResult, error) {
	t.Helper()
	return k.Internal().Update(testCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: fmt.Sprintf("hook:%s:%d:create_invoice", id, entry), Principal: createInvoiceV4,
		ItemID: id, Fields: map[string]any{"f_invoice_id": invoice}, Attribution: v4Cause("create_invoice", id, entry, 2),
	})
}

func hookFailed(t *testing.T, k *itemservice.Service, p item.Principal, id uuid.UUID, entry int64,
	reason string) (itemservice.WriteResult, error) {
	t.Helper()
	_, hookID, _ := item.HookParts(p.Actor)
	return k.Internal().HookFailed(testCtx(t), itemservice.HookFailedRequest{
		IdempotencyKey: fmt.Sprintf("hook_failed:%s:%d:%s", id, entry, hookID), Principal: p, ItemID: id,
		Reason: reason, Attribution: v4Cause(hookID, id, entry, 2),
	})
}

func fieldsOf(t *testing.T, k *itemservice.Service, p item.Principal, id uuid.UUID) map[string]any {
	t.Helper()
	it, err := k.Get(testCtx(t), p, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	return it.Fields
}

func onBehalfOf(t *testing.T, id uuid.UUID, seq int64) string {
	t.Helper()
	var who string
	if err := pool.QueryRow(testCtx(t),
		`select coalesce(on_behalf_of, '') from item_events where item_id = $1 and item_seq = $2`, id, seq,
	).Scan(&who); err != nil {
		t.Fatalf("read on_behalf_of of event %d: %v", seq, err)
	}
	return who
}

// --- which actions are pending ---

// Entering approved leaves create_invoice pending for that entry. It stays
// pending after the ticket is resolved: leaving the state does not drop it.
// The hook's write-back is accepted in resolved, and completes it.
func TestStateEntry_PendingAction_SurvivesLeavingTheState(t *testing.T) {
	k := itemservice.New(pool)
	id, ana := v4Approved(t, k)

	want := []itemservice.PendingAction{{Hook: "create_invoice", CauseSeq: 5, CorrelationID: approveCorr, Hop: 1}}
	if se := stateEntry(t, k, id); se.State != "approved" || !reflect.DeepEqual(se.PendingActions, want) {
		t.Fatalf("after the approval: state %q, pending %+v; want approved, %+v", se.State, se.PendingActions, want)
	}

	moveAs(t, k, ana, id, "resolved", map[string]any{"f_resolution": "fixed"}) // item_seq 6
	if se := stateEntry(t, k, id); se.State != "resolved" || !reflect.DeepEqual(se.PendingActions, want) {
		t.Fatalf("after resolving: state %q, pending %+v; want resolved, still %+v", se.State, se.PendingActions, want)
	}

	res, err := writeInvoice(t, k, id, 5, "INV-1")
	if err != nil {
		t.Fatalf("the action's write-back in resolved: %v", err)
	}
	if res.ItemSeq != 7 {
		t.Errorf("write-back at item_seq %d, want 7", res.ItemSeq)
	}
	if se := stateEntry(t, k, id); len(se.PendingActions) != 0 {
		t.Errorf("after the write-back: pending %+v, want none", se.PendingActions)
	}
	actor, a := attribution(t, id, 7)
	if actor != createInvoiceV4.Actor || onBehalfOf(t, id, 7) != ana.Actor ||
		a.OriginatingHook != "hook:ticket@v4:create_invoice" || a.CausationID != fmt.Sprintf("%s:5", id) || a.Hop != 2 {
		t.Errorf("write-back recorded as %s, %+v; want create_invoice on behalf of %s, caused by event 5, hop 2",
			actor, a, ana.Actor)
	}
	if f := fieldsOf(t, k, ana, id); f["f_invoice_id"] != "INV-1" {
		t.Errorf("f_invoice_id = %v, want INV-1", f["f_invoice_id"])
	}
}

// Each entry into approved is its own business event: re-entering it makes
// a new pending action, caused by the new entry, while the old one is done.
func TestStateEntry_ReenteringTheState_IsANewPendingAction(t *testing.T) {
	k := itemservice.New(pool)
	id, ana := v4Approved(t, k)
	if _, err := writeInvoice(t, k, id, 5, "INV-1"); err != nil { // 6
		t.Fatalf("write-back: %v", err)
	}
	moveAs(t, k, ana, id, "resolved", map[string]any{"f_resolution": "fixed"}) // 7
	moveAs(t, k, ana, id, "open", nil)                                         // 8
	moveAs(t, k, ana, id, "in_progress", nil)                                  // 9
	second := v4Approve(t, k, ana, id)                                         // 10 request, 11 approval
	if second != 11 {
		t.Fatalf("second approval at item_seq %d, want 11", second)
	}
	want := []itemservice.PendingAction{{Hook: "create_invoice", CauseSeq: 11, CorrelationID: approveCorr, Hop: 1}}
	if se := stateEntry(t, k, id); !reflect.DeepEqual(se.PendingActions, want) {
		t.Errorf("pending %+v, want only the second entry's %+v", se.PendingActions, want)
	}
}

// A Type without action hooks has none pending.
func TestStateEntry_NoActionHooks_NothingPending(t *testing.T) {
	k := itemservice.New(pool)
	id, _ := v3Ticket(t, k)
	if se := stateEntry(t, k, id); len(se.PendingActions) != 0 {
		t.Errorf("pending %+v for a v3 ticket, want none", se.PendingActions)
	}
}

// --- hook_failed ---

// hook_failed is an event on the Item: the hook and a reason code, recorded
// as the hook on behalf of the human at the root. It changes nothing else,
// and it completes the action for that entry, so a reload does not rerun it.
func TestHookFailed_IsVisibleAndCompletesTheAction(t *testing.T) {
	k := itemservice.New(pool)
	id, ana := v4Approved(t, k)
	before := fieldsOf(t, k, ana, id)

	res, err := hookFailed(t, k, createInvoiceV4, id, 5, "budget_exhausted")
	if err != nil {
		t.Fatalf("HookFailed: %v", err)
	}
	if res.ItemSeq != 6 || res.CommitSeq == 0 {
		t.Errorf("HookFailed = %+v, want item_seq 6 and a commit_seq", res)
	}
	op, payload := event(t, id, 6)
	wantPayload := map[string]any{"hook": "create_invoice", "reason": "budget_exhausted"}
	if op != "hook_failed" || !reflect.DeepEqual(payload, wantPayload) {
		t.Errorf("event 6 = %s %v, want hook_failed %v", op, payload, wantPayload)
	}
	actor, a := attribution(t, id, 6)
	if actor != createInvoiceV4.Actor || onBehalfOf(t, id, 6) != ana.Actor || a.CausationID != fmt.Sprintf("%s:5", id) {
		t.Errorf("hook_failed recorded as %s, %+v; want create_invoice on behalf of %s, caused by event 5",
			actor, a, ana.Actor)
	}
	se := stateEntry(t, k, id)
	if se.State != "approved" || len(se.PendingActions) != 0 {
		t.Errorf("after hook_failed: state %q, pending %+v; want approved, none", se.State, se.PendingActions)
	}
	if after := fieldsOf(t, k, ana, id); !reflect.DeepEqual(after, before) {
		t.Errorf("fields changed: %v -> %v", before, after)
	}

	// The same key again writes nothing more.
	again, err := hookFailed(t, k, createInvoiceV4, id, 5, "budget_exhausted")
	if err != nil || again != res {
		t.Errorf("HookFailed again = %+v, %v; want the stored %+v", again, err, res)
	}
	assertEventCount(t, id, 6)
}

// key_conflict: the action ran again for the same entry and the ERP made a
// second, different invoice. It is recorded even though the hook already
// wrote its first result, and the first invoice ID stays.
func TestHookFailed_KeyConflict_AfterTheWriteBack(t *testing.T) {
	k := itemservice.New(pool)
	id, ana := v4Approved(t, k)
	if _, err := writeInvoice(t, k, id, 5, "INV-1"); err != nil { // 6
		t.Fatalf("write-back: %v", err)
	}
	// The second run's write-back, with another invoice, under the same key.
	if _, err := writeInvoice(t, k, id, 5, "INV-2"); !errors.Is(err, itemservice.ErrIdempotencyKeyReuse) {
		t.Fatalf("second write-back: error %v, want ErrIdempotencyKeyReuse", err)
	}
	if _, err := hookFailed(t, k, createInvoiceV4, id, 5, "key_conflict"); err != nil {
		t.Fatalf("HookFailed(key_conflict): %v", err)
	}
	if op, payload := event(t, id, 7); op != "hook_failed" || payload["reason"] != "key_conflict" {
		t.Errorf("event 7 = %s %v, want hook_failed for key_conflict", op, payload)
	}
	if f := fieldsOf(t, k, ana, id); f["f_invoice_id"] != "INV-1" {
		t.Errorf("f_invoice_id = %v, want the first invoice, INV-1", f["f_invoice_id"])
	}
}

// Only an action hook records hook_failed, with a reason code from a fixed
// set: budget_exhausted, key_conflict, action_failed. An enrich hook has its
// fallback and a check its transition_failed; the timer has neither.
func TestHookFailed_Refusals(t *testing.T) {
	k := itemservice.New(pool)
	id, _ := v4Approved(t, k)

	cases := []struct {
		name string
		call func() error
	}{
		{"an enrich hook", func() error {
			_, err := hookFailed(t, k, item.Principal{Actor: "hook:ticket:get_plan"}, id, 1, "budget_exhausted")
			return err
		}},
		{"a check", func() error {
			_, err := hookFailed(t, k, erpCheckV4, id, 4, "budget_exhausted")
			return err
		}},
		{"an undeclared hook", func() error {
			_, err := hookFailed(t, k, item.Principal{Actor: "hook:ticket:ghost"}, id, 5, "budget_exhausted")
			return err
		}},
		{"the timer", func() error {
			_, err := k.Internal().HookFailed(testCtx(t), itemservice.HookFailedRequest{
				IdempotencyKey: newKey(), Principal: item.Principal{Actor: "system:timer"}, ItemID: id,
				Reason: "budget_exhausted", Attribution: item.Attribution{
					OriginatingHook: "timer:ticket@v4:approved", CausationID: fmt.Sprintf("%s:5", id), Hop: 1,
				},
			})
			return err
		}},
		{"a reason outside the set", func() error {
			_, err := hookFailed(t, k, createInvoiceV4, id, 5, "the ERP said no to Ana")
			return err
		}},
		{"no reason", func() error {
			_, err := hookFailed(t, k, createInvoiceV4, id, 5, "")
			return err
		}},
		{"no key", func() error {
			_, err := k.Internal().HookFailed(testCtx(t), itemservice.HookFailedRequest{
				Principal: createInvoiceV4, ItemID: id, Reason: "budget_exhausted",
				Attribution: v4Cause("create_invoice", id, 5, 2),
			})
			return err
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := c.call(); !errors.Is(err, itemservice.ErrInvalid) {
				t.Errorf("error = %v, want ErrInvalid", err)
			}
		})
	}
	assertEventCount(t, id, 5)

	if _, err := hookFailed(t, k, createInvoiceV4, id, 5, "action_failed"); err != nil {
		t.Errorf("HookFailed(action_failed): %v", err)
	}
}
