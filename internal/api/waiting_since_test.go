// Tests for "oldest waiting first" over the wire (docs/roadmap.md, track 1,
// item 5, step 3a): order_by takes one name that is no field,
// waiting_since, on SearchItems and ListItems, and only together with
// filter.waiting_on. The contract grows by that one value: no new field,
// message or error. The rule itself is tested in internal/itemservice.
package api_test

import (
	"slices"
	"testing"

	"connectrpc.com/connect"

	kernelv1 "github.com/francoisnyc/work-kernel/gen/kernel/v1"
)

func TestOrderBy_WaitingSince(t *testing.T) {
	apiWaitSetup(t)
	ctx := testCtx(t)
	// Three requests for the team, made one after the other: each entered
	// its waiting state when it was made.
	var made []string
	for _, title := range []string{"first", "second", "third"} {
		made = append(made, create(t, "sam-acme", apiWaitV1, map[string]any{"f_title": title + " " + word()}).Item.Name)
	}
	// Other runs leave open apiwq Items behind (-count): what this run
	// made is what is compared, in the order the pages give it.
	mine := func(items []*kernelv1.Item) []string {
		var got []string
		for _, name := range names(items) {
			if slices.Contains(made, name) {
				got = append(got, name)
			}
		}
		return got
	}
	oldest := []string{made[0], made[1], made[2]}
	newest := []string{made[2], made[1], made[0]}

	for _, c := range []struct {
		who, on, order string
		want           []string
	}{
		{"bob", "me", "waiting_since", oldest},
		{"bob", "me", "waiting_since asc", oldest},
		{"bob", "me", "waiting_since desc", newest},
		{"sam-acme", "role:apiwq_team", "waiting_since", oldest},
		{"sam-acme", "role:apiwq_team", "waiting_since desc", newest},
		{"bob", "me", "", newest}, // no order_by: newest created first, as before
	} {
		res, err := as(c.who).SearchItems(ctx, connect.NewRequest(&kernelv1.SearchItemsRequest{
			Parent: "namespaces/acme", Filter: waitingFilter(c.on), PageSize: 500, OrderBy: c.order,
		}))
		if err != nil {
			t.Errorf("%s searches waiting on %s, order_by %q: %v", c.who, c.on, c.order, err)
			continue
		}
		if got := mine(res.Msg.Items); !slices.Equal(got, c.want) {
			t.Errorf("%s, waiting on %s, order_by %q: this test's three = %v, want %v", c.who, c.on, c.order, got, c.want)
		}
		list, err := as(c.who).ListItems(ctx, connect.NewRequest(&kernelv1.ListItemsRequest{
			Parent: "namespaces/acme", Filter: waitingFilter(c.on), PageSize: 500, OrderBy: c.order,
		}))
		if err != nil {
			t.Errorf("%s lists waiting on %s, order_by %q: %v", c.who, c.on, c.order, err)
			continue
		}
		if got := mine(list.Msg.Items); !slices.Equal(got, c.want) {
			t.Errorf("%s lists, waiting on %s, order_by %q: this test's three = %v, want %v", c.who, c.on, c.order, got, c.want)
		}
	}

	// Paged one at a time, oldest first: each of the three once, in order.
	var paged []string
	token := ""
	for page := 0; page < 500; page++ {
		res, err := as("bob").SearchItems(ctx, connect.NewRequest(&kernelv1.SearchItemsRequest{
			Parent: "namespaces/acme", Filter: waitingFilter("me"), PageSize: 1, OrderBy: "waiting_since", PageToken: token,
		}))
		if err != nil {
			t.Fatalf("bob's page %d, oldest waiting first: %v", page, err)
		}
		paged = append(paged, mine(res.Msg.Items)...)
		if token = res.Msg.NextPageToken; token == "" {
			break
		}
	}
	if !slices.Equal(paged, oldest) {
		t.Errorf("bob's view a page each, oldest waiting first: this test's three = %v, want %v", paged, oldest)
	}

	// Without waiting_on it is INVALID_ARGUMENT on order_by: waiting_since
	// is not a field of any Type.
	for _, order := range []string{"waiting_since", "waiting_since desc"} {
		_, err := as("bob").SearchItems(ctx, connect.NewRequest(&kernelv1.SearchItemsRequest{
			Parent: "namespaces/acme", Filter: &kernelv1.ItemFilter{Types: []string{apiWaitType}}, OrderBy: order,
		}))
		invalid(t, "SearchItems order_by "+order+" without waiting_on", err, "order_by")
		_, err = as("bob").ListItems(ctx, connect.NewRequest(&kernelv1.ListItemsRequest{
			Parent: "namespaces/acme", OrderBy: order,
		}))
		invalid(t, "ListItems order_by "+order+" without waiting_on", err, "order_by")
	}
	// The direction is asc or desc, as for a field.
	_, err := as("bob").SearchItems(ctx, connect.NewRequest(&kernelv1.SearchItemsRequest{
		Parent: "namespaces/acme", Filter: waitingFilter("me"), OrderBy: "waiting_since oldest",
	}))
	invalid(t, "order_by waiting_since oldest", err, "order_by")
}
