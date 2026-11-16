// Tests for proof-of-concept Step 2, part 1, through the public API: the
// vacation request, its new refusals and the Type's new attributes.
//
//   - A kernel-written field (f_approver) is PERMISSION_DENIED /
//     FIELD_NOT_WRITABLE, with metadata fields, on create, update and a
//     transition's patch.
//   - A frozen field is FAILED_PRECONDITION / FIELD_FROZEN, with metadata
//     fields and state.
//   - A date is an ISO 8601 string; anything else is INVALID_ARGUMENT naming
//     the field.
//   - GetType shows the date type, writable_by, frozen and field: entries.
//
// The HR check is played through the kernel's internal door, as the worker
// would play it.
package api_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect"

	kernelv1 "github.com/francoisnyc/work-kernel/gen/kernel/v1"
	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
)

const vacationV1 = "namespaces/acme/types/vacation_request/versions/1"

var hrCheck = item.Principal{Actor: "hook:vacation_request:hr_check"}

func vacationTerms() map[string]any {
	return map[string]any{
		"f_start": "2099-07-01", "f_end": "2099-07-05", "f_days": 5, "f_leave_type": "annual",
	}
}

// submitted creates ana's request and walks it into submitted, with HR
// naming approver (a principal ID).
func submittedVacation(t *testing.T, approver string) string {
	t.Helper()
	name := create(t, "ana", vacationV1, vacationTerms()).Item.Name
	res := transition(t, "ana", name, "submitted")
	if res.Outcome != kernelv1.TransitionOutcome_TRANSITION_OUTCOME_PENDING || res.Hold == "" {
		t.Fatalf("submit = %v, want PENDING with the HR check's hold", res)
	}
	id, holdID := itemID(t, name), itemID(t, res.Hold)
	_, err := k.Internal().ResolveHold(testCtx(t), itemservice.ResolveHoldRequest{
		IdempotencyKey: "hold:" + holdID.String(), Principal: hrCheck, ItemID: id, HoldID: holdID,
		Verdict: itemservice.VerdictCorrect, Fields: map[string]any{"f_approver": approver},
		Attribution: item.Attribution{
			OriginatingHook: "hook:vacation_request@v1:hr_check",
			CausationID:     fmt.Sprintf("%s:%d", id, res.ItemSeq), Hop: 1,
		},
	})
	if err != nil {
		t.Fatalf("HR correct: %v", err)
	}
	return name
}

func metadata(t *testing.T, ce *connect.Error) map[string]string {
	t.Helper()
	if info := errorInfo(t, ce); info != nil {
		return info.Metadata
	}
	return nil
}

func TestVacation_ApproverIsNotWritable(t *testing.T) {
	ctx := testCtx(t)
	withApprover := vacationTerms()
	withApprover["f_approver"] = "user:ana"
	_, err := as("ana").CreateItem(ctx, connect.NewRequest(&kernelv1.CreateItemRequest{
		Parent: "namespaces/acme", RequestId: newID(), Type: vacationV1, Fields: fields(t, withApprover),
	}))
	ce := apiError(t, "create with f_approver", err, connect.CodePermissionDenied, "FIELD_NOT_WRITABLE")
	if md := metadata(t, ce); md["fields"] != "f_approver" {
		t.Errorf("metadata = %v, want fields f_approver", md)
	}

	name := create(t, "ana", vacationV1, vacationTerms()).Item.Name
	_, err = as("ana").UpdateItem(ctx, connect.NewRequest(&kernelv1.UpdateItemRequest{
		Name: name, RequestId: newID(), Fields: fields(t, map[string]any{"f_approver": "user:bob"}),
	}))
	apiError(t, "update f_approver", err, connect.CodePermissionDenied, "FIELD_NOT_WRITABLE")

	_, err = as("ana").TransitionItem(ctx, connect.NewRequest(&kernelv1.TransitionItemRequest{
		Name: name, RequestId: newID(), ToState: "submitted",
		Fields: fields(t, map[string]any{"f_approver": "user:ana"}),
	}))
	apiError(t, "submit with f_approver", err, connect.CodePermissionDenied, "FIELD_NOT_WRITABLE")
	if it := get(t, "ana", name); it.Hold != nil || it.State != "draft" {
		t.Errorf("after the refusals: state %s, hold %v; want draft and no hold", it.State, it.Hold)
	}
}

func TestVacation_FrozenAndDecided(t *testing.T) {
	ctx := testCtx(t)
	name := submittedVacation(t, "user:bob")
	if it := get(t, "ana", name); it.State != "submitted" || it.Fields.AsMap()["f_approver"] != "user:bob" {
		t.Fatalf("submitted: state %s, fields %v", it.State, it.Fields.AsMap())
	}
	_, err := as("ana").UpdateItem(ctx, connect.NewRequest(&kernelv1.UpdateItemRequest{
		Name: name, RequestId: newID(), Fields: fields(t, map[string]any{"f_days": 20, "f_comment": "x"}),
	}))
	ce := apiError(t, "edit f_days when submitted", err, connect.CodeFailedPrecondition, "FIELD_FROZEN")
	if md := metadata(t, ce); md["fields"] != "f_days" || md["state"] != "submitted" {
		t.Errorf("metadata = %v, want fields f_days, state submitted", md)
	}

	// ana asks to approve her own request: the guard (before `allowed`).
	_, err = as("ana").TransitionItem(ctx, connect.NewRequest(&kernelv1.TransitionItemRequest{
		Name: name, RequestId: newID(), ToState: "approved",
	}))
	apiError(t, "ana approves her own", err, connect.CodeFailedPrecondition, "GUARD_FAILED")
	// sam is neither requester nor approver.
	_, err = as("sam-acme").TransitionItem(ctx, connect.NewRequest(&kernelv1.TransitionItemRequest{
		Name: name, RequestId: newID(), ToState: "approved",
	}))
	apiError(t, "sam approves", err, connect.CodePermissionDenied, "NOT_ALLOWED")

	if res := transition(t, "bob", name, "approved"); res.Outcome != kernelv1.TransitionOutcome_TRANSITION_OUTCOME_APPLIED ||
		res.Item.State != "approved" || res.Item.Category != kernelv1.StateCategory_STATE_CATEGORY_DONE {
		t.Errorf("bob approves = %v, want APPLIED into approved (DONE)", res)
	}
	_, err = as("ana").UpdateItem(ctx, connect.NewRequest(&kernelv1.UpdateItemRequest{
		Name: name, RequestId: newID(), Fields: fields(t, map[string]any{"f_end": "2099-07-20"}),
	}))
	ce = apiError(t, "edit f_end when approved", err, connect.CodeFailedPrecondition, "FIELD_FROZEN")
	if md := metadata(t, ce); md["fields"] != "f_end" || md["state"] != "approved" {
		t.Errorf("metadata = %v, want fields f_end, state approved", md)
	}
}

func TestVacation_Rejected(t *testing.T) {
	name := submittedVacation(t, "user:bob")
	res := transition(t, "bob", name, "rejected")
	if res.Item.State != "rejected" || res.Item.Category != kernelv1.StateCategory_STATE_CATEGORY_CANCELED {
		t.Errorf("bob rejects = %v, want rejected (CANCELED)", res)
	}
}

// A date is YYYY-MM-DD and a real day; anything else names the field.
func TestVacation_DateValues(t *testing.T) {
	for _, bad := range []any{"2099-13-01", "01/07/2099", "2099-07-01T00:00:00Z", 20990701} {
		f := vacationTerms()
		f["f_start"] = bad
		_, err := as("ana").CreateItem(testCtx(t), connect.NewRequest(&kernelv1.CreateItemRequest{
			Parent: "namespaces/acme", RequestId: newID(), Type: vacationV1, Fields: fields(t, f),
		}))
		ce := apiError(t, fmt.Sprintf("f_start %v", bad), err, connect.CodeInvalidArgument, "INVALID_REQUEST")
		if v := violations(t, ce); !slices.Equal(v, []string{"fields.f_start"}) {
			t.Errorf("f_start %v: violations = %v, want fields.f_start", bad, v)
		}
	}
	it := create(t, "ana", vacationV1, vacationTerms()).Item
	if got := it.Fields.AsMap()["f_start"]; got != "2099-07-01" {
		t.Errorf("f_start read back = %v, want 2099-07-01", got)
	}
}

func TestGetType_VacationRequest(t *testing.T) {
	res, err := as("ana").GetType(testCtx(t), connect.NewRequest(&kernelv1.GetTypeRequest{Name: vacationV1}))
	if err != nil {
		t.Fatalf("GetType: %v", err)
	}
	ty := res.Msg.Type
	byID := map[string]*kernelv1.Field{}
	for _, f := range ty.Fields {
		byID[f.Id] = f
	}
	if f := byID["f_start"]; f == nil || f.Type != kernelv1.FieldType_FIELD_TYPE_DATE ||
		f.WritableBy != kernelv1.FieldWriter_FIELD_WRITER_UNSPECIFIED {
		t.Errorf("f_start = %v, want DATE, writable by anyone", f)
	}
	if f := byID["f_approver"]; f == nil || f.Type != kernelv1.FieldType_FIELD_TYPE_PRINCIPAL ||
		f.WritableBy != kernelv1.FieldWriter_FIELD_WRITER_KERNEL {
		t.Errorf("f_approver = %v, want PRINCIPAL, writable by the kernel", f)
	}
	terms := []string{"f_start", "f_end", "f_days", "f_leave_type", "f_approver"}
	for _, s := range ty.States {
		var want []string
		if s.Id == "submitted" || s.Id == "approved" || s.Id == "rejected" {
			want = terms
		}
		if !slices.Equal(s.Frozen, want) {
			t.Errorf("state %s frozen = %v, want %v", s.Id, s.Frozen, want)
		}
	}
	for _, tr := range ty.Transitions {
		if tr.FromState == "submitted" && (tr.ToState == "approved" || tr.ToState == "rejected") &&
			!slices.Equal(tr.Allowed, []string{"field:f_approver"}) {
			t.Errorf("%s -> %s allowed = %v, want [field:f_approver]", tr.FromState, tr.ToState, tr.Allowed)
		}
		if tr.FromState == "draft" && tr.ToState == "submitted" && !tr.Checked {
			t.Error("draft -> submitted is not shown as checked")
		}
	}
	if !strings.Contains(ty.Name, "vacation_request") {
		t.Errorf("name = %s", ty.Name)
	}
}
