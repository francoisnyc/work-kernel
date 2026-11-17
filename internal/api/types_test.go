// Tests for Step 0, part 1: GetType and ListTypes. Types are read-only and
// readable by anyone in the namespace; they are global for now, so every
// namespace sees the same list under its own names.
package api_test

import (
	"slices"
	"testing"

	"connectrpc.com/connect"

	kernelv1 "github.com/francoisnyc/work-kernel/gen/kernel/v1"
)

func TestGetType_TicketV3(t *testing.T) {
	res, err := as("bob").GetType(testCtx(t), connect.NewRequest(&kernelv1.GetTypeRequest{Name: ticketV3}))
	if err != nil {
		t.Fatalf("GetType: %v", err)
	}
	ty := res.Msg.Type
	if ty.Name != ticketV3 || ty.TypeId != "ticket" || ty.Version != 3 || ty.InitialState != "triage" {
		t.Errorf("type = %s (%s v%d, starts in %s), want %s (ticket v3, triage)",
			ty.Name, ty.TypeId, ty.Version, ty.InitialState, ticketV3)
	}

	fieldOf := func(id string) *kernelv1.Field {
		for _, f := range ty.Fields {
			if f.Id == id {
				return f
			}
		}
		t.Fatalf("no field %s in %v", id, ty.Fields)
		return nil
	}
	if f := fieldOf("f_amount"); f.Type != kernelv1.FieldType_FIELD_TYPE_INT {
		t.Errorf("f_amount = %v, want INT", f.Type)
	}
	if f := fieldOf("f_on_hold"); f.Type != kernelv1.FieldType_FIELD_TYPE_BOOL {
		t.Errorf("f_on_hold = %v, want BOOL", f.Type)
	}
	if f := fieldOf("f_resolution"); f.Type != kernelv1.FieldType_FIELD_TYPE_ENUM ||
		!slices.Equal(f.Options, []string{"fixed", "wontfix", "duplicate"}) {
		t.Errorf("f_resolution = %v, want ENUM [fixed wontfix duplicate]", f)
	}

	categories := map[string]kernelv1.StateCategory{}
	for _, s := range ty.States {
		categories[s.Id] = s.Category
	}
	if categories["triage"] != kernelv1.StateCategory_STATE_CATEGORY_TRIAGE ||
		categories["approved"] != kernelv1.StateCategory_STATE_CATEGORY_ACTIVE ||
		categories["canceled"] != kernelv1.StateCategory_STATE_CATEGORY_CANCELED || len(categories) != 7 {
		t.Errorf("states = %v", ty.States)
	}

	transitionOf := func(from, to string) *kernelv1.Transition {
		for _, tr := range ty.Transitions {
			if tr.FromState == from && tr.ToState == to {
				return tr
			}
		}
		t.Fatalf("no transition %s -> %s", from, to)
		return nil
	}
	approve := transitionOf("in_progress", "approved")
	if !approve.Checked || approve.Guard != "f_on_hold != true" || approve.Escape ||
		!slices.Equal(approve.Required, []string{"f_customer_id", "f_amount"}) ||
		!slices.Equal(approve.Allowed, []string{"role:approver"}) {
		t.Errorf("approve = %v, want checked, guarded, required, allowed role:approver", approve)
	}
	if c := transitionOf("in_progress", "canceled"); !c.Escape || c.Checked {
		t.Errorf("cancel = %v, want an escape, unchecked", c)
	}
	if o := transitionOf("triage", "open"); o.Checked || o.Guard != "" || len(o.Allowed) != 0 {
		t.Errorf("triage -> open = %v, want a plain move", o)
	}
}

// A Type without states shows its one implicit state.
func TestGetType_ImplicitState(t *testing.T) {
	res, err := as("ana").GetType(testCtx(t), connect.NewRequest(&kernelv1.GetTypeRequest{
		Name: "namespaces/acme/types/message/versions/1",
	}))
	if err != nil {
		t.Fatalf("GetType: %v", err)
	}
	ty := res.Msg.Type
	if ty.InitialState != "default" || len(ty.States) != 1 ||
		ty.States[0].Category != kernelv1.StateCategory_STATE_CATEGORY_NONE || len(ty.Transitions) != 0 {
		t.Errorf("message v1 = %v, want one state default (NONE), no transitions", ty)
	}
}

func TestGetType_Refusals(t *testing.T) {
	ctx := testCtx(t)
	get := func(name string) error {
		_, err := as("ana").GetType(ctx, connect.NewRequest(&kernelv1.GetTypeRequest{Name: name}))
		return err
	}
	notFound(t, "unknown Type", get("namespaces/acme/types/ghost/versions/1"))
	notFound(t, "unknown version", get("namespaces/acme/types/ticket/versions/99"))
	invalid(t, "no version", get("namespaces/acme/types/ticket"), "name")
	invalid(t, "version not a number", get("namespaces/acme/types/ticket/versions/v3"), "name")
	invalid(t, "empty", get(""), "name")
}

func TestListTypes_EveryVersionInOrder(t *testing.T) {
	res, err := as("bea").ListTypes(testCtx(t), connect.NewRequest(&kernelv1.ListTypesRequest{
		Parent: "namespaces/globex",
	}))
	if err != nil {
		t.Fatalf("ListTypes: %v", err)
	}
	var names []string
	for _, ty := range res.Msg.Types {
		names = append(names, ty.Name)
		if len(ty.States) == 0 {
			t.Errorf("%s listed without its definition", ty.Name)
		}
	}
	want := []string{
		"namespaces/globex/types/channel/versions/1",
		"namespaces/globex/types/leave_balance/versions/1",
		"namespaces/globex/types/message/versions/1",
		"namespaces/globex/types/ticket/versions/1",
		"namespaces/globex/types/ticket/versions/2",
		"namespaces/globex/types/ticket/versions/3",
		"namespaces/globex/types/ticket/versions/4",
		"namespaces/globex/types/ticket/versions/5",
		"namespaces/globex/types/vacation_request/versions/1",
		"namespaces/globex/types/vacation_request/versions/2",
	}
	if !slices.Equal(names, want) {
		t.Errorf("ListTypes = %v\nwant %v", names, want)
	}

	_, err = as("bea").ListTypes(testCtx(t), connect.NewRequest(&kernelv1.ListTypesRequest{Parent: "globex"}))
	invalid(t, "bad parent", err, "parent")
}
