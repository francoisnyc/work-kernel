// Tests for "waiting on me" over the wire (docs/roadmap.md, track 1, item
// 5, step 1): ItemFilter.waiting_on, on SearchItems and ListItems. "me" is
// the caller; "role:<name>" a role's queue, which any member of staff may
// ask for. Anything else, and a customer asking, is INVALID_ARGUMENT on
// filter.waiting_on. The rule itself is tested in internal/policy and
// internal/itemservice; here, that the contract carries it.
package api_test

import (
	"sync"
	"testing"

	"connectrpc.com/connect"

	kernelv1 "github.com/francoisnyc/work-kernel/gen/kernel/v1"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// apiwq: one waiting state; the team, or the owner a field names, closes
// it. No other test makes one, and only this file grants apiwq_team.
const apiWaitSrc = `
id: apiwq
version: 1
initial_state: open
fields:
  - {id: f_title, type: string}
  - {id: f_owner, type: principal}
states:
  - {id: open,   category: waiting}
  - {id: closed, category: done}
transitions:
  - {from: open, to: closed, allowed: ['role:apiwq_team', 'field:f_owner']}
`

const (
	apiWaitType = "namespaces/acme/types/apiwq"
	apiWaitV1   = apiWaitType + "/versions/1"
)

var apiWaitOnce sync.Once

// apiWaitSetup registers apiwq and puts bob in its team, once.
func apiWaitSetup(t *testing.T) {
	t.Helper()
	apiWaitOnce.Do(func() {
		ty, err := typedef.Parse([]byte(apiWaitSrc))
		if err != nil {
			t.Fatalf("parse apiwq: %v", err)
		}
		if err := k.RegisterType(testCtx(t), ty); err != nil {
			t.Fatalf("register apiwq: %v", err)
		}
		if _, err := pool.Exec(testCtx(t), `insert into principal_roles (namespace_id, principal, role)
			values ('acme', 'user:bob', 'apiwq_team') on conflict do nothing`); err != nil {
			t.Fatalf("grant apiwq_team: %v", err)
		}
	})
}

// waitingFilter: apiwq Items waiting on `on`.
func waitingFilter(on string) *kernelv1.ItemFilter {
	return &kernelv1.ItemFilter{Types: []string{apiWaitType}, WaitingOn: on}
}

func TestSearchItems_WaitingOn(t *testing.T) {
	apiWaitSetup(t)
	ctx := testCtx(t)
	// sam asks for both; the first names ana as its owner.
	anas := create(t, "sam-acme", apiWaitV1, map[string]any{"f_title": "for ana " + word(), "f_owner": "user:ana"}).Item.Name
	teams := create(t, "sam-acme", apiWaitV1, map[string]any{"f_title": "for the team " + word()}).Item.Name
	closed := create(t, "sam-acme", apiWaitV1, map[string]any{"f_title": "closed " + word(), "f_owner": "user:ana"}).Item.Name
	transition(t, "bob", closed, "closed")

	has := func(who, on string, want ...string) {
		t.Helper()
		res, err := as(who).SearchItems(ctx, connect.NewRequest(&kernelv1.SearchItemsRequest{
			Parent: "namespaces/acme", Filter: waitingFilter(on), PageSize: 500,
		}))
		if err != nil {
			t.Fatalf("%s searches waiting on %s: %v", who, on, err)
		}
		// Other runs of this test leave open apiwq Items behind (-count):
		// what this run made is what is compared.
		var got []string
		for _, name := range names(res.Msg.Items) {
			if name == anas || name == teams || name == closed {
				got = append(got, name)
			}
		}
		if !sameNames(got, want) {
			t.Errorf("%s, waiting on %s: of this test's three, got %v, want %v", who, on, got, want)
		}
	}
	has("bob", "me", anas, teams)                   // in the team
	has("ana", "me", anas)                          // named by f_owner
	has("sam-acme", "me")                           // asked for them; no part in deciding
	has("sam-acme", "role:apiwq_team", anas, teams) // anyone may ask for a role's queue
	has("ana", "role:apiwq_nobody")

	// waiting_on alone is a search: no words, no other filter.
	res, err := as("ana").SearchItems(ctx, connect.NewRequest(&kernelv1.SearchItemsRequest{
		Parent: "namespaces/acme", Filter: &kernelv1.ItemFilter{WaitingOn: "me"}, PageSize: 500,
	}))
	if err != nil {
		t.Fatalf("ana searches with waiting_on alone: %v", err)
	}
	found := false
	for _, it := range res.Msg.Items {
		found = found || it.Name == anas
		if it.Name == teams || it.Name == closed {
			t.Errorf("ana's view across every Type holds %s, which does not wait on her", it.Name)
		}
	}
	if !found {
		t.Errorf("ana's view across every Type (%d Items) does not hold the Item that names her", len(res.Msg.Items))
	}

	// The Item comes as any read gives it.
	for _, it := range res.Msg.Items {
		if it.Name == anas && (it.State != "open" || it.Category != kernelv1.StateCategory_STATE_CATEGORY_WAITING ||
			it.Fields.AsMap()["f_owner"] != "user:ana" || it.Etag == "") {
			t.Errorf("the Item in ana's view = %v, want it whole: open, WAITING, its fields, an etag", it)
		}
	}
}

func TestListItems_WaitingOn(t *testing.T) {
	apiWaitSetup(t)
	ctx := testCtx(t)
	box := create(t, "ana", ticketV1, nil).Item.Name
	inside := mkIn(t, "sam-acme", apiWaitV1, box, map[string]any{"f_title": "inside " + word(), "f_owner": "user:ana"})
	mkIn(t, "sam-acme", apiWaitV1, box, map[string]any{"f_title": "inside, nobody's " + word(), "f_owner": "user:hana"})
	create(t, "sam-acme", apiWaitV1, map[string]any{"f_title": "outside " + word(), "f_owner": "user:ana"})

	res, err := as("ana").ListItems(ctx, connect.NewRequest(&kernelv1.ListItemsRequest{
		Parent: box, Filter: &kernelv1.ItemFilter{WaitingOn: "me"},
	}))
	if err != nil {
		t.Fatalf("ana lists the box waiting on her: %v", err)
	}
	if got := names(res.Msg.Items); !sameNames(got, []string{inside}) {
		t.Errorf("the box's children waiting on ana = %v, want [%s]", got, inside)
	}
}

func TestWaitingOn_Refusals(t *testing.T) {
	apiWaitSetup(t)
	ctx := testCtx(t)
	for _, on := range []string{"everyone", "role:", "role:Not A Name", "user:ana", "principal:user:ana", "group:finance", "ME"} {
		_, err := as("ana").SearchItems(ctx, connect.NewRequest(&kernelv1.SearchItemsRequest{
			Parent: "namespaces/acme", Filter: &kernelv1.ItemFilter{WaitingOn: on},
		}))
		invalid(t, "SearchItems waiting on "+on, err, "filter.waiting_on")
		_, err = as("ana").ListItems(ctx, connect.NewRequest(&kernelv1.ListItemsRequest{
			Parent: "namespaces/acme", Filter: &kernelv1.ItemFilter{WaitingOn: on},
		}))
		invalid(t, "ListItems waiting on "+on, err, "filter.waiting_on")
	}

	// A customer has no role's queue. Her own view (step 5) is no refusal:
	// waiting_sent_back_test.go tests what is in it.
	_, err := asCustomer("kim", "").SearchItems(ctx, connect.NewRequest(&kernelv1.SearchItemsRequest{
		Parent: "namespaces/acme", Filter: &kernelv1.ItemFilter{WaitingOn: "role:apiwq_team"},
	}))
	invalid(t, "a customer's SearchItems waiting on role:apiwq_team", err, "filter.waiting_on")
	if _, err := asCustomer("kim", "").SearchItems(ctx, connect.NewRequest(&kernelv1.SearchItemsRequest{
		Parent: "namespaces/acme", Filter: &kernelv1.ItemFilter{WaitingOn: "me"},
	})); err != nil {
		t.Errorf("a customer's SearchItems waiting on me: %v, want her view", err)
	}

	// A page token is bound to waiting_on, like to the rest of its request.
	for range 2 {
		create(t, "sam-acme", apiWaitV1, map[string]any{"f_title": "paged " + word()})
	}
	first, err := as("bob").SearchItems(ctx, connect.NewRequest(&kernelv1.SearchItemsRequest{
		Parent: "namespaces/acme", Filter: waitingFilter("me"), PageSize: 1,
	}))
	if err != nil {
		t.Fatalf("bob's first page of one: %v", err)
	}
	if first.Msg.NextPageToken == "" || len(first.Msg.Items) != 1 {
		t.Fatalf("bob's first page of one: %d Items, token %q; want one Item and a token", len(first.Msg.Items), first.Msg.NextPageToken)
	}
	second, err := as("bob").SearchItems(ctx, connect.NewRequest(&kernelv1.SearchItemsRequest{
		Parent: "namespaces/acme", Filter: waitingFilter("me"), PageSize: 1, PageToken: first.Msg.NextPageToken,
	}))
	if err != nil {
		t.Fatalf("bob's second page: %v", err)
	}
	if len(second.Msg.Items) != 1 || second.Msg.Items[0].Name == first.Msg.Items[0].Name {
		t.Errorf("bob's second page = %v, want the next Item after %s", names(second.Msg.Items), first.Msg.Items[0].Name)
	}
	_, err = as("bob").SearchItems(ctx, connect.NewRequest(&kernelv1.SearchItemsRequest{
		Parent: "namespaces/acme", Filter: waitingFilter("role:apiwq_team"), PageSize: 1, PageToken: first.Msg.NextPageToken,
	}))
	invalid(t, "a token of waiting on me, used for the role's queue", err, "page_token")
}
