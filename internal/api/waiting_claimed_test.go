// Tests for a team's claimed rows and withheld moves over the wire (the
// Inbox for people, PR 2a; the owner's decisions of its Phase A,
// 2026-11-27). kernel.v1 grows by additive fields only:
//
//   - ItemFilter.claimed (8): with waiting_on role:<name>, that role's
//     claimed rows instead of its queue; else INVALID_ARGUMENT at
//     filter.claimed. SearchItems, ListItems and CountItems.
//   - State.claim (11): the principal field holding who claimed an Item in
//     that state, in GetType and ListTypes.
//   - Item.withheld (17), WithheldMove and WithheldReason: filled exactly
//     where Item.moves is.
//
// The rules are tested in internal/policy and internal/itemservice. The
// file uses waiting_claim_test.go's apicl (ana and bob are its team), and
// its own apiwh for a move withheld from its requester.
package api_test

import (
	"slices"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	kernelv1 "github.com/francoisnyc/work-kernel/gen/kernel/v1"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// apiwh: a queue the apicl team takes from, except what they asked for.
const apiWithheldSrc = `
id: apiwh
version: 1
initial_state: queue
fields:
  - {id: f_title, type: string}
states:
  - {id: queue, category: waiting}
  - {id: taken, category: active}
transitions:
  - {from: queue, to: taken, allowed: ['role:apicl_team'], except: [creator]}
`

const apiWithheldV1 = "namespaces/acme/types/apiwh/versions/1"

var apiWithheldOnce sync.Once

func apiWithheldSetup(t *testing.T) {
	t.Helper()
	apiClaimSetup(t)
	apiWithheldOnce.Do(func() {
		ty, err := typedef.Parse([]byte(apiWithheldSrc))
		if err != nil {
			t.Fatalf("parse apiwh: %v", err)
		}
		if err := k.RegisterType(testCtx(t), ty); err != nil {
			t.Fatalf("register apiwh: %v", err)
		}
	})
}

func TestStateClaim_OverTheWire(t *testing.T) {
	apiClaimSetup(t)
	res, err := as("ana").GetType(testCtx(t), connect.NewRequest(&kernelv1.GetTypeRequest{Name: apiClaimV1}))
	if err != nil {
		t.Fatalf("GetType apicl: %v", err)
	}
	for _, st := range res.Msg.Type.States {
		want := ""
		if st.Id == "review" {
			want = "f_claimed_by"
		}
		if st.Claim != want {
			t.Errorf("GetType apicl: state %s claim %q, want %q", st.Id, st.Claim, want)
		}
	}
	// A customer reads Types too: the claim is the Type's, not an Item's.
	cres, err := asCustomer("kim", "").GetType(testCtx(t), connect.NewRequest(&kernelv1.GetTypeRequest{Name: apiClaimV1}))
	if err != nil {
		t.Fatalf("kim's GetType apicl: %v", err)
	}
	if i := slices.IndexFunc(cres.Msg.Type.States, func(s *kernelv1.State) bool { return s.Id == "review" }); i < 0 ||
		cres.Msg.Type.States[i].Claim != "f_claimed_by" {
		t.Errorf("kim's GetType apicl: review has no claim f_claimed_by")
	}
	// ListTypes says the same.
	list, err := as("ana").ListTypes(testCtx(t), connect.NewRequest(&kernelv1.ListTypesRequest{Parent: "namespaces/acme"}))
	if err != nil {
		t.Fatalf("ListTypes: %v", err)
	}
	found := false
	for _, ty := range list.Msg.Types {
		if ty.Name != apiClaimV1 {
			continue
		}
		for _, st := range ty.States {
			if st.Id == "review" {
				found = st.Claim == "f_claimed_by"
			}
		}
	}
	if !found {
		t.Error("ListTypes: apicl's review has no claim f_claimed_by")
	}
}

func TestClaimedRows_OverTheWire(t *testing.T) {
	apiClaimSetup(t)
	ctx := testCtx(t)
	w := word()
	name := create(t, "sam-acme", apiClaimV1, map[string]any{"f_title": "claimed " + w}).Item.Name
	transition(t, "bob", name, "review")

	team := func(claimed bool) *kernelv1.ItemFilter {
		return &kernelv1.ItemFilter{WaitingOn: "role:apicl_team", Claimed: claimed}
	}
	// ana reads the claimed rows: bob holds it; she may release it, and
	// finishing it is withheld from her, claimed by another.
	res, err := as("ana").SearchItems(ctx, connect.NewRequest(&kernelv1.SearchItemsRequest{
		Parent: "namespaces/acme", Query: w, Filter: team(true),
	}))
	if err != nil {
		t.Fatalf("ana reads the team's claimed rows: %v", err)
	}
	if len(msg(res).Items) != 1 {
		t.Fatalf("the team's claimed rows for the word: %d Items, want 1", len(msg(res).Items))
	}
	it := msg(res).Items[0]
	if h := it.Fields.GetFields()["f_claimed_by"].GetStringValue(); h != "user:bob" {
		t.Errorf("the claimed row's f_claimed_by = %q, want user:bob", h)
	}
	if !slices.Equal(it.Moves, []string{"queue"}) {
		t.Errorf("ana's moves on bob's claimed row = %v, want [queue]", it.Moves)
	}
	want := []*kernelv1.WithheldMove{{ToState: "done", Reason: kernelv1.WithheldReason_WITHHELD_REASON_CLAIMED_BY_OTHER}}
	if !sameWithheld(it.Withheld, want) {
		t.Errorf("ana's withheld on bob's claimed row = %v, want %v", it.Withheld, want)
	}

	// The queue (claimed false) no longer holds it.
	queue, err := as("ana").SearchItems(ctx, connect.NewRequest(&kernelv1.SearchItemsRequest{
		Parent: "namespaces/acme", Query: w, Filter: team(false),
	}))
	if err != nil || len(msg(queue).Items) != 0 {
		t.Errorf("the team's queue for the word: %d Items, error %v; want none", len(msg(queue).GetItems()), err)
	}

	// ListItems and CountItems take the flag.
	listed, err := as("ana").ListItems(ctx, connect.NewRequest(&kernelv1.ListItemsRequest{
		Parent: "namespaces/acme", PageSize: 500,
		Filter: &kernelv1.ItemFilter{Types: []string{"namespaces/acme/types/apicl"}, WaitingOn: "role:apicl_team", Claimed: true},
	}))
	if err != nil || !slices.ContainsFunc(msg(listed).Items, func(it *kernelv1.Item) bool { return it.Name == name }) {
		t.Errorf("ListItems, the claimed rows: error %v; want the case among them", err)
	}
	n, err := as("ana").CountItems(ctx, connect.NewRequest(&kernelv1.CountItemsRequest{
		Parent: "namespaces/acme", Query: w, Filter: team(true),
	}))
	if err != nil || msg(n).Count != 1 {
		t.Errorf("CountItems, the claimed rows for the word: %d, error %v; want 1", msg(n).Count, err)
	}

	// bob, who holds it: his moves, nothing withheld.
	res, err = as("bob").SearchItems(ctx, connect.NewRequest(&kernelv1.SearchItemsRequest{
		Parent: "namespaces/acme", Query: w, Filter: team(true),
	}))
	if err != nil || len(msg(res).Items) != 1 || !slices.Equal(msg(res).Items[0].Moves, []string{"queue", "done"}) ||
		len(msg(res).Items[0].Withheld) != 0 {
		t.Errorf("bob reads his claimed row: %v, error %v; want moves [queue done], nothing withheld", msg(res).GetItems(), err)
	}

	// Withheld is empty wherever moves is: GetItem, a search without
	// waiting_on.
	got, err := as("ana").GetItem(ctx, connect.NewRequest(&kernelv1.GetItemRequest{Name: name}))
	if err != nil || len(msg(got).GetItem().GetWithheld()) != 0 {
		t.Errorf("GetItem: withheld %v, error %v; want none", msg(got).GetItem().GetWithheld(), err)
	}
}

func TestWithheld_OwnRequest_OverTheWire(t *testing.T) {
	apiWithheldSetup(t)
	w := word()
	create(t, "ana", apiWithheldV1, map[string]any{"f_title": "ana's own " + w})
	res, err := as("ana").SearchItems(testCtx(t), connect.NewRequest(&kernelv1.SearchItemsRequest{
		Parent: "namespaces/acme", Query: w, Filter: &kernelv1.ItemFilter{WaitingOn: "role:apicl_team"},
	}))
	if err != nil || len(msg(res).Items) != 1 {
		t.Fatalf("ana reads her team's queue: %d Items, error %v; want her own request", len(msg(res).GetItems()), err)
	}
	it := msg(res).Items[0]
	want := []*kernelv1.WithheldMove{{ToState: "taken", Reason: kernelv1.WithheldReason_WITHHELD_REASON_OWN_REQUEST}}
	if len(it.Moves) != 0 || !sameWithheld(it.Withheld, want) {
		t.Errorf("ana, her own request: moves %v, withheld %v; want no move, withheld %v", it.Moves, it.Withheld, want)
	}
	// bob, her colleague: his to take, nothing withheld.
	res, err = as("bob").SearchItems(testCtx(t), connect.NewRequest(&kernelv1.SearchItemsRequest{
		Parent: "namespaces/acme", Query: w, Filter: &kernelv1.ItemFilter{WaitingOn: "role:apicl_team"},
	}))
	if err != nil || len(msg(res).Items) != 1 || !slices.Equal(msg(res).Items[0].Moves, []string{"taken"}) ||
		len(msg(res).Items[0].Withheld) != 0 {
		t.Errorf("bob reads the queue: %v, error %v; want moves [taken], nothing withheld", msg(res).GetItems(), err)
	}
}

func TestClaimed_Refused_OverTheWire(t *testing.T) {
	apiClaimSetup(t)
	ctx := testCtx(t)
	for _, c := range []struct {
		what string
		f    *kernelv1.ItemFilter
	}{
		{"claimed without waiting_on", &kernelv1.ItemFilter{Claimed: true, Types: []string{"namespaces/acme/types/apicl"}}},
		{"claimed with waiting_on me", &kernelv1.ItemFilter{Claimed: true, WaitingOn: "me"}},
	} {
		_, err := as("ana").SearchItems(ctx, connect.NewRequest(&kernelv1.SearchItemsRequest{Parent: "namespaces/acme", Filter: c.f}))
		invalid(t, "SearchItems, "+c.what, err, "filter.claimed")
		_, err = as("ana").CountItems(ctx, connect.NewRequest(&kernelv1.CountItemsRequest{Parent: "namespaces/acme", Filter: c.f}))
		invalid(t, "CountItems, "+c.what, err, "filter.claimed")
	}
	// A customer: waiting on herself, claimed.
	_, err := asCustomer("kim", "").SearchItems(ctx, connect.NewRequest(&kernelv1.SearchItemsRequest{
		Parent: "namespaces/acme", Filter: &kernelv1.ItemFilter{Claimed: true, WaitingOn: "me"},
	}))
	invalid(t, "kim, claimed, waiting on me", err, "filter.claimed")
}

func sameWithheld(got, want []*kernelv1.WithheldMove) bool {
	return slices.EqualFunc(got, want, func(a, b *kernelv1.WithheldMove) bool { return proto.Equal(a, b) })
}
