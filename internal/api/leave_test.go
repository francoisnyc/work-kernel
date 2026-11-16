// Tests for proof-of-concept Step 2, part 2, through the public API: the
// leave balance and the vacation request v2.
//
//   - GetType shows everything new: writable_by_roles, a link's
//     writable_by and resolve, invariants, reactions (with checked on the
//     transition a held reaction holds) and accepts_reactions. Types are
//     not secret, and an agent reading a Type should see what a transition
//     will do.
//   - A role-written field is PERMISSION_DENIED / FIELD_NOT_WRITABLE; an
//     invariant is FAILED_PRECONDITION / INVARIANT_FAILED (metadata
//     invariant); a kernel-set link is PERMISSION_DENIED /
//     LINK_NOT_WRITABLE (metadata link_type), on LinkItems and UnlinkItems.
//   - History shows a reaction's write with both the result (fields) and
//     the delta (added), the link set on entering submitted
//     (link_type, link_target on TRANSITIONED), and REACTION_FAILED.
//
// The HR check and the reactions are played through the kernel's internal
// door, as the worker plays them.
package api_test

import (
	"fmt"
	"slices"
	"testing"

	"connectrpc.com/connect"

	kernelv1 "github.com/francoisnyc/work-kernel/gen/kernel/v1"
	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
)

const (
	vacationV2   = "namespaces/acme/types/vacation_request/versions/2"
	leaveBalance = "namespaces/acme/types/leave_balance/versions/1"
)

var deductP = item.Principal{Actor: "reaction:vacation_request:deduct"}

func grantHana(t *testing.T) {
	t.Helper()
	if _, err := pool.Exec(testCtx(t), `insert into principal_roles (namespace_id, principal, role)
		values ('acme', 'user:hana', 'hr_admin') on conflict do nothing`); err != nil {
		t.Fatalf("grant hr_admin to hana: %v", err)
	}
}

// openBalanceFor: hana closes the owner's open balances (earlier tests
// made some) and opens a new one.
func openBalanceFor(t *testing.T, owner string, allowance int) string {
	t.Helper()
	grantHana(t)
	rows, err := pool.Query(testCtx(t), `select id from items where type_id = 'leave_balance' and state = 'open'
		and namespace_id = 'acme' and fields->>'f_owner' = $1`, owner)
	if err != nil {
		t.Fatal(err)
	}
	var old []string
	for rows.Next() {
		var id string
		_ = rows.Scan(&id)
		old = append(old, "namespaces/acme/items/"+id)
	}
	rows.Close()
	for _, name := range old {
		transition(t, "hana", name, "closed")
	}
	return create(t, "hana", leaveBalance, map[string]any{"f_owner": owner, "f_allowance": allowance}).Item.Name
}

// submittedV2 creates ana's v2 request and walks it into submitted (HR
// names bob), returning its name and the transition's history event.
func submittedV2(t *testing.T, days int) string {
	t.Helper()
	name := create(t, "ana", vacationV2, map[string]any{
		"f_start": "2099-07-01", "f_end": "2099-07-05", "f_days": days, "f_leave_type": "annual",
	}).Item.Name
	res := transition(t, "ana", name, "submitted")
	id, holdID := itemID(t, name), itemID(t, res.Hold)
	if _, err := k.Internal().ResolveHold(testCtx(t), itemservice.ResolveHoldRequest{
		IdempotencyKey: "hold:" + holdID.String(), Principal: hrCheck, ItemID: id, HoldID: holdID,
		Verdict: itemservice.VerdictCorrect, Fields: map[string]any{"f_approver": "user:bob"},
		Attribution: item.Attribution{OriginatingHook: "hook:vacation_request@v2:hr_check",
			CausationID: fmt.Sprintf("%s:%d", id, res.ItemSeq), Hop: 1},
	}); err != nil {
		t.Fatalf("HR correct: %v", err)
	}
	return name
}

func history(t *testing.T, who, name string) []*kernelv1.ItemEvent {
	t.Helper()
	res, err := as(who).ListItemEvents(testCtx(t), connect.NewRequest(&kernelv1.ListItemEventsRequest{Parent: name}))
	if err != nil {
		t.Fatalf("ListItemEvents %s as %s: %v", name, who, err)
	}
	return res.Msg.Events
}

func TestGetType_LeaveBalance(t *testing.T) {
	res, err := as("ana").GetType(testCtx(t), connect.NewRequest(&kernelv1.GetTypeRequest{Name: leaveBalance}))
	if err != nil {
		t.Fatalf("GetType: %v", err)
	}
	ty := res.Msg.Type
	for _, f := range ty.Fields {
		switch f.Id {
		case "f_owner", "f_allowance":
			if !slices.Equal(f.WritableByRoles, []string{"hr_admin"}) || f.WritableBy != kernelv1.FieldWriter_FIELD_WRITER_UNSPECIFIED {
				t.Errorf("%s = %v, want writable by role hr_admin", f.Id, f)
			}
		case "f_used":
			if f.WritableBy != kernelv1.FieldWriter_FIELD_WRITER_KERNEL || len(f.WritableByRoles) != 0 {
				t.Errorf("f_used = %v, want writable by the kernel", f)
			}
		}
	}
	var invariants []string
	for _, inv := range ty.Invariants {
		invariants = append(invariants, inv.Id)
		if inv.Rule == "" {
			t.Errorf("invariant %s has no rule", inv.Id)
		}
	}
	if !slices.Equal(invariants, []string{"balance_incomplete", "insufficient_balance"}) {
		t.Errorf("invariants = %v", invariants)
	}
	accepted := map[string][]string{}
	for _, a := range ty.AcceptsReactions {
		accepted[a.Reaction] = a.Fields
	}
	if !slices.Equal(accepted["vacation_request:deduct"], []string{"f_used"}) ||
		!slices.Equal(accepted["vacation_request:refund"], []string{"f_used"}) || len(accepted) != 2 {
		t.Errorf("accepts_reactions = %v", accepted)
	}
}

func TestGetType_VacationRequestV2(t *testing.T) {
	res, err := as("ana").GetType(testCtx(t), connect.NewRequest(&kernelv1.GetTypeRequest{Name: vacationV2}))
	if err != nil {
		t.Fatalf("GetType: %v", err)
	}
	ty := res.Msg.Type
	if len(ty.LinkTypes) != 1 {
		t.Fatalf("link types = %v, want balance", ty.LinkTypes)
	}
	l := ty.LinkTypes[0]
	if l.Name != "balance" || l.WritableBy != kernelv1.FieldWriter_FIELD_WRITER_KERNEL || l.Resolve == nil ||
		l.Resolve.OnEnter != "submitted" || l.Resolve.Field != "f_owner" || l.Resolve.Equals != "creator" || l.Resolve.State != "open" {
		t.Errorf("balance link = %v", l)
	}
	byID := map[string]*kernelv1.Reaction{}
	for _, r := range ty.Reactions {
		byID[r.Id] = r
	}
	if d := byID["deduct"]; d == nil || d.Kind != kernelv1.ReactionKind_REACTION_KIND_HELD || d.State != "approved" ||
		!slices.Equal(d.FromStates, []string{"submitted"}) || d.LinkType != "balance" || d.Add["f_used"] != "f_days" {
		t.Errorf("deduct = %v", d)
	}
	if r := byID["refund"]; r == nil || r.Kind != kernelv1.ReactionKind_REACTION_KIND_AFTER || r.State != "withdrawn" ||
		!slices.Equal(r.FromStates, []string{"approved"}) || r.Undoes != "deduct" {
		t.Errorf("refund = %v", r)
	}
	for _, tr := range ty.Transitions {
		if tr.FromState == "submitted" && tr.ToState == "approved" && !tr.Checked {
			t.Error("submitted -> approved is not shown as checked: deduct holds it")
		}
		if tr.FromState == "submitted" && tr.ToState == "withdrawn" && !tr.Escape {
			t.Error("submitted -> withdrawn is not an escape")
		}
	}
}

func TestLeave_PublicRefusals(t *testing.T) {
	ctx := testCtx(t)
	grantHana(t)
	// Not hr_admin: the role-written fields are refused, naming them.
	_, err := as("ana").CreateItem(ctx, connect.NewRequest(&kernelv1.CreateItemRequest{
		Parent: "namespaces/acme", RequestId: newID(), Type: leaveBalance,
		Fields: fields(t, map[string]any{"f_owner": "user:ana", "f_allowance": 99}),
	}))
	ce := apiError(t, "ana creates a balance", err, connect.CodePermissionDenied, "FIELD_NOT_WRITABLE")
	if md := metadata(t, ce); md["fields"] != "f_allowance,f_owner" {
		t.Errorf("metadata = %v, want fields f_allowance,f_owner", md)
	}
	// Without them, the invariant refuses the Item.
	_, err = as("ana").CreateItem(ctx, connect.NewRequest(&kernelv1.CreateItemRequest{
		Parent: "namespaces/acme", RequestId: newID(), Type: leaveBalance, Fields: fields(t, map[string]any{}),
	}))
	ce = apiError(t, "ana creates an empty balance", err, connect.CodeFailedPrecondition, "INVARIANT_FAILED")
	if md := metadata(t, ce); md["invariant"] != "balance_incomplete" {
		t.Errorf("metadata = %v, want invariant balance_incomplete", md)
	}
	// Nobody writes f_used.
	bal := openBalanceFor(t, "user:ana", 25)
	_, err = as("hana").UpdateItem(ctx, connect.NewRequest(&kernelv1.UpdateItemRequest{
		Name: bal, RequestId: newID(), Fields: fields(t, map[string]any{"f_used": 0}),
	}))
	apiError(t, "hana sets f_used", err, connect.CodePermissionDenied, "FIELD_NOT_WRITABLE")

	// The balance link: neither linked nor unlinked through the API.
	req := submittedV2(t, 5)
	draft := create(t, "ana", vacationV2, map[string]any{"f_days": 1}).Item.Name
	_, err = linkItems("ana", draft, "balance", bal, "")
	ce = apiError(t, "ana links a balance", err, connect.CodePermissionDenied, "LINK_NOT_WRITABLE")
	if md := metadata(t, ce); md["link_type"] != "balance" {
		t.Errorf("metadata = %v, want link_type balance", md)
	}
	_, err = as("ana").UnlinkItems(ctx, connect.NewRequest(&kernelv1.UnlinkItemsRequest{
		Name: req, RequestId: newID(), LinkType: "balance", Target: bal,
	}))
	apiError(t, "ana unlinks her balance", err, connect.CodePermissionDenied, "LINK_NOT_WRITABLE")
}

// History: the transition into submitted names the link; the deduction
// shows the result and the delta.
func TestLeave_History(t *testing.T) {
	bal := openBalanceFor(t, "user:ana", 25)
	name := submittedV2(t, 5)
	events := history(t, "ana", name)
	last := events[len(events)-1]
	if last.Kind != kernelv1.ItemEventKind_ITEM_EVENT_KIND_TRANSITIONED || last.ToState != "submitted" ||
		last.LinkType != "balance" || last.LinkTarget.GetName() != bal {
		t.Errorf("the entering event = %v, want TRANSITIONED into submitted naming the balance link", last)
	}

	res := transition(t, "bob", name, "approved")
	if res.Outcome != kernelv1.TransitionOutcome_TRANSITION_OUTCOME_PENDING || res.Hold == "" {
		t.Fatalf("approve = %v, want PENDING with deduct's hold", res)
	}
	id, holdID := itemID(t, name), itemID(t, res.Hold)
	cause := item.Attribution{OriginatingHook: "reaction:vacation_request@v2:deduct",
		CausationID: fmt.Sprintf("%s:%d", id, res.ItemSeq), Hop: 1}
	if _, err := k.Internal().React(testCtx(t), itemservice.ReactRequest{
		Principal: deductP, SourceID: id, HoldID: holdID, Attribution: cause}); err != nil {
		t.Fatalf("React: %v", err)
	}
	events = history(t, "hana", bal)
	last = events[len(events)-1]
	if last.Kind != kernelv1.ItemEventKind_ITEM_EVENT_KIND_UPDATED || last.Actor != deductP.Actor ||
		last.Fields.GetFields()["f_used"].GetNumberValue() != 5 || last.Added.GetFields()["f_used"].GetNumberValue() != 5 {
		t.Errorf("the deduction = %v, want UPDATED by deduct, f_used 5, added 5", last)
	}
}

// reaction_failed is REACTION_FAILED, naming the reaction and its code.
func TestLeave_History_ReactionFailed(t *testing.T) {
	openBalanceFor(t, "user:ana", 25)
	name := submittedV2(t, 5)
	id := itemID(t, name)
	if _, err := k.Internal().ReactionFailed(testCtx(t), itemservice.ReactionFailedRequest{
		IdempotencyKey: "reaction_failed:test-" + newID(), Principal: item.Principal{Actor: "reaction:vacation_request:refund"},
		SourceID: id, Reason: "reaction_not_accepted",
		Attribution: item.Attribution{OriginatingHook: "reaction:vacation_request@v2:refund",
			CausationID: fmt.Sprintf("%s:3", id), Hop: 1},
	}); err != nil {
		t.Fatalf("ReactionFailed: %v", err)
	}
	events := history(t, "ana", name)
	last := events[len(events)-1]
	if last.Kind != kernelv1.ItemEventKind_ITEM_EVENT_KIND_REACTION_FAILED || last.Reaction != "vacation_request:refund" ||
		last.Reason != "reaction_not_accepted" {
		t.Errorf("event = %v, want REACTION_FAILED naming vacation_request:refund and its code", last)
	}
}
