// Tests for "sent back to the requester" over the wire (docs/roadmap.md,
// track 1, item 5, step 5). The contract grows by one field,
// State.waits_on_requester (7), and a customer's waiting_on: me is no
// longer refused: it answers with her own requests that wait on her, as
// she sees them. role:<name> stays refused for her (waiting_test.go).
//
// The file's name starts with "waiting": it registers Types when it runs,
// after TestListTypes_EveryVersionInOrder (types_test.go) has listed them.
package api_test

import (
	"slices"
	"sync"
	"testing"

	"connectrpc.com/connect"

	kernelv1 "github.com/francoisnyc/work-kernel/gen/kernel/v1"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// apisb_desk owns apisb_req. The team (apisb_team: bob) asks the
// requester a question (open -> asked, where it waits on the requester);
// the requester answers by sending it back to open.
const apiSBDeskSrc = `
id: apisb_desk
version: 1
own: [apisb_req]
`

const apiSBReqSrc = `
id: apisb_req
version: 1
initial_state: open
fields:
  - {id: f_title, type: string}
  - {id: f_notes, type: string}
states:
  - {id: open,  category: waiting}
  - {id: asked, category: waiting, waits_on: requester}
  - {id: done,  category: done}
transitions:
  - {from: open,  to: asked, allowed: ['role:apisb_team']}
  - {from: open,  to: done,  allowed: ['role:apisb_team']}
  - {from: asked, to: open,  allowed: [creator]}
customer_fields: [f_title]
`

const (
	apiSBDeskV1 = "namespaces/acme/types/apisb_desk/versions/1"
	apiSBReqV1  = "namespaces/acme/types/apisb_req/versions/1"
)

var apiSBOnce sync.Once

func apiSBSetup(t *testing.T) {
	t.Helper()
	apiSBOnce.Do(func() {
		for _, src := range []string{apiSBDeskSrc, apiSBReqSrc} {
			ty, err := typedef.Parse([]byte(src))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if err := k.RegisterType(testCtx(t), ty); err != nil {
				t.Fatalf("register %s: %v", ty.ID, err)
			}
		}
		if _, err := pool.Exec(testCtx(t), `insert into principal_roles (namespace_id, principal, role)
			values ('acme', 'user:bob', 'apisb_team') on conflict do nothing`); err != nil {
			t.Fatalf("grant apisb_team: %v", err)
		}
	})
}

func TestGetType_ShowsWaitsOnRequester(t *testing.T) {
	apiSBSetup(t)
	res, err := as("ana").GetType(testCtx(t), connect.NewRequest(&kernelv1.GetTypeRequest{Name: apiSBReqV1}))
	if err != nil {
		t.Fatalf("GetType: %v", err)
	}
	for _, st := range res.Msg.Type.States {
		if want := st.Id == "asked"; st.WaitsOnRequester != want {
			t.Errorf("state %s: waits_on_requester = %v, want %v", st.Id, st.WaitsOnRequester, want)
		}
	}
	// A customer reads it too: Types are not secret.
	res, err = asCustomer("kim", "").GetType(testCtx(t), connect.NewRequest(&kernelv1.GetTypeRequest{Name: apiSBReqV1}))
	if err != nil {
		t.Fatalf("kim's GetType: %v", err)
	}
	if i := slices.IndexFunc(res.Msg.Type.States, func(s *kernelv1.State) bool { return s.Id == "asked" }); i < 0 || !res.Msg.Type.States[i].WaitsOnRequester {
		t.Errorf("kim's GetType does not show asked waiting on the requester")
	}
}

func TestSentBack_OverTheWire(t *testing.T) {
	apiSBSetup(t)
	ctx := testCtx(t)
	desk := mkIn(t, "bob", apiSBDeskV1, "", nil)
	w := word()
	ask := func(customer, title string) string {
		t.Helper()
		res, err := customerCreates(t, asCustomer(customer, ""), apiSBReqV1, desk, map[string]any{"f_title": title + " " + w})
		if err != nil {
			t.Fatalf("%s opens %s: %v", customer, title, err)
		}
		return res.Item.Name
	}
	kims, kimsOpen, lees := ask("kim", "printer"), ask("kim", "badge"), ask("lee", "screen")
	if _, err := as("bob").UpdateItem(ctx, connect.NewRequest(&kernelv1.UpdateItemRequest{
		Name: kims, RequestId: newID(), Fields: fields(t, map[string]any{"f_notes": "staff only"}),
	})); err != nil {
		t.Fatalf("bob writes f_notes: %v", err)
	}
	transition(t, "bob", kims, "asked")
	transition(t, "bob", lees, "asked")

	view := func(customer string) []*kernelv1.Item {
		t.Helper()
		res, err := asCustomer(customer, "").SearchItems(ctx, connect.NewRequest(&kernelv1.SearchItemsRequest{
			Parent: "namespaces/acme", Query: w, Filter: &kernelv1.ItemFilter{WaitingOn: "me"}, OrderBy: "waiting_since",
		}))
		if err != nil {
			t.Fatalf("%s's view: %v", customer, err)
		}
		return res.Msg.Items
	}
	got := view("kim")
	if !sameNames(names(got), []string{kims}) {
		t.Fatalf("kim's view = %v, want her printer request only (not %s, still open; not lee's)", names(got), kimsOpen)
	}
	it := got[0]
	if _, has := it.Fields.GetFields()["f_notes"]; has || it.Fields.GetFields()["f_title"] == nil {
		t.Errorf("kim's row fields = %v, want f_title only", it.Fields)
	}
	if !slices.Equal(it.Moves, []string{"open"}) || it.StateTime == nil {
		t.Errorf("kim's row: moves %v, state_time %v; want [open] and a time", it.Moves, it.StateTime)
	}
	if got := view("lee"); !sameNames(names(got), []string{lees}) {
		t.Errorf("lee's view = %v, want hers only", names(got))
	}
	count, err := asCustomer("kim", "").CountItems(ctx, connect.NewRequest(&kernelv1.CountItemsRequest{
		Parent: "namespaces/acme", Query: w, Filter: &kernelv1.ItemFilter{WaitingOn: "me"},
	}))
	if err != nil || count.Msg.Count != 1 || count.Msg.Capped {
		t.Errorf("kim's count = %v, error %v; want 1", count.Msg, err)
	}
	// bob, in the team, does not wait on it: the question is kim's to answer.
	staff, err := as("bob").SearchItems(ctx, connect.NewRequest(&kernelv1.SearchItemsRequest{
		Parent: "namespaces/acme", Query: w, Filter: &kernelv1.ItemFilter{WaitingOn: "me"},
	}))
	if err != nil || !sameNames(names(staff.Msg.Items), []string{kimsOpen}) {
		t.Errorf("bob's view = %v, error %v; want kim's open request only", names(staff.Msg.GetItems()), err)
	}
	// kim answers: it leaves her view and waits on the team again.
	if _, err := asCustomer("kim", "").TransitionItem(ctx, connect.NewRequest(&kernelv1.TransitionItemRequest{
		Name: kims, RequestId: newID(), ToState: "open",
	})); err != nil {
		t.Fatalf("kim answers: %v", err)
	}
	if got := view("kim"); len(got) != 0 {
		t.Errorf("after kim answers, her view = %v, want nothing", names(got))
	}
}
