// Tests for Jira 2.4b, PR 1, through the public API: GetType shows the new
// rules (for_each, on_guard, unique_among_siblings, placement_writable_by;
// nobody in allowed), and the two new refusals answer their reasons:
// PERMISSION_DENIED / PLACEMENT_NOT_WRITABLE and FAILED_PRECONDITION /
// NOT_UNIQUE.
package api_test

import (
	"slices"
	"testing"

	"connectrpc.com/connect"

	kernelv1 "github.com/francoisnyc/work-kernel/gen/kernel/v1"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

const (
	deskV3    = "namespaces/acme/types/desk/versions/3"
	slaGoalV2 = "namespaces/acme/types/sla_goal/versions/2"
)

const api4bJar = `
id: apijar
version: 1
initial_state: open
fields:
  - {id: f_keepers, type: principal, multiple: true, max: 8}
states:
  - {id: open, category: active}
  - {id: done, category: done}
transitions:
  - {from: open, to: done}
reactions:
  - {id: make, kind: create, on: enter, state: open, type: apibean, version: 1, for_each: f_keepers, fields: {f_holder: 'each'}}
accepts_reactions:
  'apibean:tip': {moves: [done]}
`

const api4bBean = `
id: apibean
version: 1
initial_state: waiting
fields:
  - {id: f_holder, type: principal, writable_by: kernel, unique_among_siblings: [waiting]}
  - {id: f_next,   type: principal}
states:
  - {id: waiting, category: active}
  - {id: yes,     category: done}
transitions:
  - {from: waiting, to: yes, allowed: ['field:f_holder']}
  - {from: waiting, to: waiting, allowed: ['field:f_holder'], required: [f_next], sets: {f_holder: 'f_next'}}
reactions:
  - {id: tip, kind: move, on: enter, state: yes, target: container, to: done, target_from: [open], on_guard: skip}
accepts_reactions:
  'apijar:make': {creates: [f_holder]}
placement_writable_by: [kernel]
`

func register4b(t *testing.T) {
	t.Helper()
	for _, src := range []string{api4bBean, api4bJar} {
		ty, err := typedef.Parse([]byte(src))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if err := k.RegisterType(testCtx(t), ty); err != nil {
			t.Fatalf("register: %v", err)
		}
	}
}

func getType(t *testing.T, name string) *kernelv1.Type {
	t.Helper()
	res, err := as("ana").GetType(testCtx(t), connect.NewRequest(&kernelv1.GetTypeRequest{Name: name}))
	if err != nil {
		t.Fatalf("GetType %s: %v", name, err)
	}
	return res.Msg.Type
}

func TestJira24b_API_GetTypeShowsTheNewRules(t *testing.T) {
	register4b(t)
	jar, bean := getType(t, "namespaces/acme/types/apijar/versions/1"), getType(t, "namespaces/acme/types/apibean/versions/1")
	if len(jar.Reactions) != 1 || jar.Reactions[0].ForEach != "f_keepers" {
		t.Errorf("apijar's reaction: %+v; want for_each f_keepers", jar.Reactions)
	}
	if len(bean.Reactions) != 1 || bean.Reactions[0].OnGuard != "skip" || bean.Reactions[0].Target != "container" {
		t.Errorf("apibean's reaction: %+v; want on_guard skip, target container", bean.Reactions)
	}
	var holder *kernelv1.Field
	for _, f := range bean.Fields {
		if f.Id == "f_holder" {
			holder = f
		}
	}
	if holder == nil || !slices.Equal(holder.UniqueAmongSiblings, []string{"waiting"}) {
		t.Errorf("f_holder: %+v; want unique_among_siblings [waiting]", holder)
	}
	if !slices.Equal(bean.PlacementWritableBy, []string{"kernel"}) {
		t.Errorf("apibean placement: %v", bean.PlacementWritableBy)
	}
	if d := getType(t, deskV3); !slices.Equal(d.PlacementWritableBy, []string{"role:sla_admin"}) {
		t.Errorf("desk@3 placement: %v", d.PlacementWritableBy)
	}
	for _, tr := range getType(t, slaGoalV2).Transitions {
		if !slices.Equal(tr.Allowed, []string{"nobody"}) {
			t.Errorf("sla_goal@2 %s -> %s allowed %v", tr.FromState, tr.ToState, tr.Allowed)
		}
	}
}

func TestJira24b_API_TheNewRefusals(t *testing.T) {
	register4b(t)
	// PLACEMENT_NOT_WRITABLE: ana narrows a desk@3.
	desk := mkIn(t, "hana", deskV3, "", map[string]any{"f_key": "AR", "f_name": "access " + word()})
	_, err := as("ana").UpdateItem(testCtx(t), connect.NewRequest(&kernelv1.UpdateItemRequest{Name: desk, RequestId: newID(),
		Policy: &kernelv1.PolicyChange{Change: &kernelv1.PolicyChange_Narrow{Narrow: &kernelv1.Readers{Principals: []string{"user:ana"}}}}}))
	apiError(t, "ana narrows a desk@3", err, connect.CodePermissionDenied, "PLACEMENT_NOT_WRITABLE")

	// NOT_UNIQUE: ana hands her bean over to bob, who holds one.
	jar := mkIn(t, "hana", "namespaces/acme/types/apijar/versions/1", "", map[string]any{"f_keepers": []any{"user:ana", "user:bob"}})
	id := itemID(t, jar)
	if out, err := k.Internal().ReactCreate(testCtx(t), runReaction(id, "make", id, 1)); err != nil || out.Outcome != itemservice.ReactionApplied {
		t.Fatalf("make: %+v, %v", out, err)
	}
	res, err := as("ana").SearchItems(testCtx(t), connect.NewRequest(&kernelv1.SearchItemsRequest{Parent: "namespaces/acme",
		Filter: &kernelv1.ItemFilter{Types: []string{"namespaces/acme/types/apibean"}, Links: nil}}))
	if err != nil {
		t.Fatal(err)
	}
	var anas string
	for _, it := range res.Msg.Items {
		if it.Container == jar && it.Fields.AsMap()["f_holder"] == "user:ana" {
			anas = it.Name
		}
	}
	if anas == "" {
		t.Fatal("no bean for ana")
	}
	_, err = as("ana").TransitionItem(testCtx(t), connect.NewRequest(&kernelv1.TransitionItemRequest{Name: anas, RequestId: newID(),
		ToState: "waiting", Fields: fields(t, map[string]any{"f_next": "user:bob"})}))
	apiError(t, "ana hands over to bob", err, connect.CodeFailedPrecondition, "NOT_UNIQUE")
}
