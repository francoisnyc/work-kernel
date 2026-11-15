// Tests for Step 0, part 2: the new reads over the wire.
//
//   - Item carries create_time, creator, creator_on_behalf_of and category.
//   - ListItems: a container's direct children, or namespaces/{ns} for the
//     top level; the same filters as SearchItems.
//   - Filters: types (namespaces/{ns}/types/{type}, any version), states,
//     categories, field eq and int ranges with plain-JSON values (an int is
//     a string). A filter alone is a search.
//   - Paging: page_size, page_token, next_page_token; a token reused with
//     another request is INVALID_ARGUMENT on page_token.
//   - ListItemEvents: the history, oldest first, projected for the caller.
package api_test

import (
	"slices"
	"strconv"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/structpb"

	kernelv1 "github.com/francoisnyc/work-kernel/gen/kernel/v1"
)

func names(items []*kernelv1.Item) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.Name
	}
	return out
}

func sameNames(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

func str(s string) *structpb.Value { return structpb.NewStringValue(s) }

// --- Item: create time, creator, category ---

func TestItem_CreateTimeCreatorCategory(t *testing.T) {
	before := time.Now().Add(-time.Minute)
	it := create(t, "ana", ticketV1, map[string]any{"f_assignee": "user-bob"}).Item
	if it.CreateTime == nil || it.CreateTime.AsTime().Before(before) || it.Creator != "user:ana" || it.CreatorOnBehalfOf != "" {
		t.Errorf("created %v by %q for %q, want now by user:ana", it.CreateTime, it.Creator, it.CreatorOnBehalfOf)
	}
	if it.Category != kernelv1.StateCategory_STATE_CATEGORY_TODO {
		t.Errorf("category in open = %v, want TODO", it.Category)
	}
	moved := transition(t, "bob", it.Name, "in_progress").Item
	if moved.Category != kernelv1.StateCategory_STATE_CATEGORY_ACTIVE {
		t.Errorf("category in in_progress = %v, want ACTIVE", moved.Category)
	}
	if moved.Creator != "user:ana" || !moved.CreateTime.AsTime().Equal(it.CreateTime.AsTime()) {
		t.Errorf("bob's transition changed the creator or create time: %v", moved)
	}
	// ticket v3 starts in triage.
	if v3 := create(t, "ana", ticketV3, nil).Item; v3.Category != kernelv1.StateCategory_STATE_CATEGORY_TRIAGE {
		t.Errorf("ticket v3 category = %v, want TRIAGE", v3.Category)
	}
	// A Type without states is NONE.
	if msg := create(t, "ana", "namespaces/acme/types/message/versions/1", nil).Item; msg.Category != kernelv1.StateCategory_STATE_CATEGORY_NONE {
		t.Errorf("message category = %v, want NONE", msg.Category)
	}
}

// --- ListItems ---

func TestListItems_ChildrenAndTopLevel(t *testing.T) {
	ctx := testCtx(t)
	box := create(t, "ana", ticketV1, nil).Item.Name
	a := createIn(t, "ana", box, nil)
	b := createIn(t, "ana", box, nil)
	createIn(t, "ana", a, nil) // a grandchild

	res, err := as("bob").ListItems(ctx, connect.NewRequest(&kernelv1.ListItemsRequest{Parent: box}))
	if err != nil {
		t.Fatalf("ListItems: %v", err)
	}
	if got := names(res.Msg.Items); !slices.Equal(got, []string{b, a}) || res.Msg.NextPageToken != "" {
		t.Errorf("ListItems = %v (token %q), want [%s %s], newest first, one page", got, res.Msg.NextPageToken, b, a)
	}
	if got := res.Msg.Items[0]; got.Creator != "user:ana" || got.Category != kernelv1.StateCategory_STATE_CATEGORY_TODO {
		t.Errorf("a listed Item lacks creator or category: %v", got)
	}

	newest := create(t, "ana", ticketV1, nil).Item.Name
	top, err := as("bob").ListItems(ctx, connect.NewRequest(&kernelv1.ListItemsRequest{Parent: "namespaces/acme", PageSize: 1}))
	if err != nil || len(top.Msg.Items) != 1 || top.Msg.Items[0].Name != newest || top.Msg.NextPageToken == "" {
		t.Errorf("top level, page of 1 = %v, %v; want the newest top-level Item %s and a next page", top, err, newest)
	}
}

func TestListItems_Refusals(t *testing.T) {
	ctx := testCtx(t)
	private := createNarrowed(t, "ana", "user:ana")
	_, err := as("bob").ListItems(ctx, connect.NewRequest(&kernelv1.ListItemsRequest{Parent: private}))
	notFound(t, "a container bob cannot see", err)
	_, err = as("ana").ListItems(ctx, connect.NewRequest(&kernelv1.ListItemsRequest{Parent: "acme"}))
	invalid(t, "a malformed parent", err, "parent")
	_, err = as("ana").ListItems(ctx, connect.NewRequest(&kernelv1.ListItemsRequest{Parent: "namespaces/globex"}))
	notFound(t, "another namespace", err)
	_, err = as("ana").ListItems(ctx, connect.NewRequest(&kernelv1.ListItemsRequest{Parent: "namespaces/acme", PageSize: -1}))
	invalid(t, "a negative page size", err, "page_size")
}

// --- filters ---

// filtered searches acme with a filter (and no words) as ana.
func filtered(t *testing.T, f *kernelv1.ItemFilter) ([]string, error) {
	t.Helper()
	res, err := as("ana").SearchItems(testCtx(t), connect.NewRequest(&kernelv1.SearchItemsRequest{
		Parent: "namespaces/acme", Filter: f, PageSize: 500,
	}))
	if err != nil {
		return nil, err
	}
	return names(res.Msg.Items), nil
}

func TestSearchItems_Filters(t *testing.T) {
	w := word()
	mk := func(typ string, f map[string]any) string {
		f["f_title"] = w
		return create(t, "ana", typ, f).Item.Name
	}
	low := mk(ticketV1, map[string]any{"f_priority": 1, "f_resolution": "fixed"})
	high := mk(ticketV1, map[string]any{"f_priority": 7, "f_assignee": "user-bob"})
	working := transition(t, "ana", high, "in_progress").Item.Name
	v3 := mk(ticketV3, map[string]any{"f_priority": 7})
	sameTitle := &kernelv1.FieldFilter{Field: "f_title", Eq: str(w)}
	ticket := "namespaces/acme/types/ticket"

	cases := []struct {
		name string
		f    *kernelv1.ItemFilter
		want []string
	}{
		{"Type, every version", &kernelv1.ItemFilter{Types: []string{ticket}, Fields: []*kernelv1.FieldFilter{sameTitle}},
			[]string{low, working, v3}},
		{"state", &kernelv1.ItemFilter{Types: []string{ticket}, States: []string{"in_progress"},
			Fields: []*kernelv1.FieldFilter{sameTitle}}, []string{working}},
		{"category", &kernelv1.ItemFilter{Types: []string{ticket},
			Categories: []kernelv1.StateCategory{kernelv1.StateCategory_STATE_CATEGORY_TRIAGE, kernelv1.StateCategory_STATE_CATEGORY_ACTIVE},
			Fields:     []*kernelv1.FieldFilter{sameTitle}}, []string{working, v3}},
		{"int eq, as a string", &kernelv1.ItemFilter{Types: []string{ticket},
			Fields: []*kernelv1.FieldFilter{sameTitle, {Field: "f_priority", Eq: str("7")}}}, []string{working, v3}},
		{"int range", &kernelv1.ItemFilter{Types: []string{ticket},
			Fields: []*kernelv1.FieldFilter{sameTitle, {Field: "f_priority", Gte: str("2"), Lt: str("100")}}}, []string{working, v3}},
		{"enum eq", &kernelv1.ItemFilter{Types: []string{ticket},
			Fields: []*kernelv1.FieldFilter{sameTitle, {Field: "f_resolution", Eq: str("fixed")}}}, []string{low}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := filtered(t, c.f)
			if err != nil {
				t.Fatalf("SearchItems: %v", err)
			}
			if !sameNames(got, c.want) {
				t.Errorf("found %v, want %v", got, c.want)
			}
		})
	}

	// Words and a filter together.
	res, err := as("ana").SearchItems(testCtx(t), connect.NewRequest(&kernelv1.SearchItemsRequest{
		Parent: "namespaces/acme", Query: w, Filter: &kernelv1.ItemFilter{States: []string{"open"}},
	}))
	if err != nil || !sameNames(names(res.Msg.Items), []string{low}) {
		t.Errorf("words and state = %v, %v; want [%s]", res, err, low)
	}
}

func TestSearchItems_FilterRefusals(t *testing.T) {
	ticket := "namespaces/acme/types/ticket"
	cases := []struct {
		name  string
		f     *kernelv1.ItemFilter
		field string
	}{
		{"field filter without a Type", &kernelv1.ItemFilter{Fields: []*kernelv1.FieldFilter{{Field: "f_priority", Eq: str("1")}}}, "filter.types"},
		{"a Type with a version", &kernelv1.ItemFilter{Types: []string{ticketV1}}, "filter.types"},
		{"a malformed Type", &kernelv1.ItemFilter{Types: []string{"ticket"}}, "filter.types"},
		{"an unknown Type", &kernelv1.ItemFilter{Types: []string{"namespaces/acme/types/nope"}}, "filter.types"},
		{"unknown field", &kernelv1.ItemFilter{Types: []string{ticket}, Fields: []*kernelv1.FieldFilter{{Field: "f_nope", Eq: str("1")}}}, "filter.fields[0]"},
		{"range on a string", &kernelv1.ItemFilter{Types: []string{ticket}, Fields: []*kernelv1.FieldFilter{{Field: "f_title", Gt: str("a")}}}, "filter.fields[0]"},
		{"an int that is not one", &kernelv1.ItemFilter{Types: []string{ticket}, Fields: []*kernelv1.FieldFilter{{Field: "f_priority", Eq: str("seven")}}}, "filter.fields[0]"},
		{"eq and a bound", &kernelv1.ItemFilter{Types: []string{ticket}, Fields: []*kernelv1.FieldFilter{{Field: "f_priority", Eq: str("1"), Lt: str("3")}}}, "filter.fields[0]"},
		{"no condition", &kernelv1.ItemFilter{Types: []string{ticket}, Fields: []*kernelv1.FieldFilter{{Field: "f_priority"}}}, "filter.fields[0]"},
		{"unspecified category", &kernelv1.ItemFilter{Categories: []kernelv1.StateCategory{kernelv1.StateCategory_STATE_CATEGORY_UNSPECIFIED}}, "filter.categories"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := filtered(t, c.f)
			invalid(t, "SearchItems", err, c.field)
			_, err = as("ana").ListItems(testCtx(t), connect.NewRequest(&kernelv1.ListItemsRequest{
				Parent: "namespaces/acme", Filter: c.f,
			}))
			invalid(t, "ListItems", err, c.field)
		})
	}
	_, err := filtered(t, &kernelv1.ItemFilter{Types: []string{"namespaces/globex/types/ticket"}})
	notFound(t, "a Type named in another namespace", err)
	_, err = filtered(t, &kernelv1.ItemFilter{})
	invalid(t, "an empty filter and no words", err, "query")
}

// --- paging ---

func TestPaging_OverTheWire(t *testing.T) {
	ctx := testCtx(t)
	box := create(t, "ana", ticketV1, nil).Item.Name
	var made []string
	for range 5 {
		made = append(made, createIn(t, "ana", box, nil))
	}
	slices.Reverse(made) // newest first

	var got []string
	token := ""
	for pages := 1; ; pages++ {
		res, err := as("ana").ListItems(ctx, connect.NewRequest(&kernelv1.ListItemsRequest{Parent: box, PageSize: 2, PageToken: token}))
		if err != nil {
			t.Fatalf("page %d: %v", pages, err)
		}
		got = append(got, names(res.Msg.Items)...)
		token = res.Msg.NextPageToken
		if token == "" {
			if pages != 3 {
				t.Errorf("%d pages, want 3", pages)
			}
			break
		}
		if pages > 5 {
			t.Fatal("the token does not advance")
		}
	}
	if !slices.Equal(got, made) {
		t.Errorf("pages = %v, want %v", got, made)
	}

	first, err := as("ana").ListItems(ctx, connect.NewRequest(&kernelv1.ListItemsRequest{Parent: box, PageSize: 2}))
	if err != nil {
		t.Fatalf("page 1: %v", err)
	}
	_, err = as("ana").ListItems(ctx, connect.NewRequest(&kernelv1.ListItemsRequest{
		Parent: box, PageSize: 2, PageToken: first.Msg.NextPageToken, Filter: &kernelv1.ItemFilter{States: []string{"open"}},
	}))
	invalid(t, "token with another filter", err, "page_token")
	_, err = as("bob").ListItems(ctx, connect.NewRequest(&kernelv1.ListItemsRequest{
		Parent: box, PageSize: 2, PageToken: first.Msg.NextPageToken,
	}))
	invalid(t, "token from another caller", err, "page_token")
	_, err = as("ana").ListItemEvents(ctx, connect.NewRequest(&kernelv1.ListItemEventsRequest{
		Parent: box, PageSize: 2, PageToken: first.Msg.NextPageToken,
	}))
	invalid(t, "token from another read", err, "page_token")
}

// --- ListItemEvents ---

func events(t *testing.T, who, name string) []*kernelv1.ItemEvent {
	t.Helper()
	res, err := as(who).ListItemEvents(testCtx(t), connect.NewRequest(&kernelv1.ListItemEventsRequest{Parent: name, PageSize: 500}))
	if err != nil {
		t.Fatalf("ListItemEvents %s as %s: %v", name, who, err)
	}
	return res.Msg.Events
}

func TestListItemEvents_History(t *testing.T) {
	ctx := testCtx(t)
	from := create(t, "ana", ticketV1, nil).Item.Name
	to := create(t, "ana", ticketV1, nil).Item.Name
	it := createIn(t, "ana", from, map[string]any{"f_title": "printer", "f_priority": 2})
	if _, err := as("bob").UpdateItem(ctx, connect.NewRequest(&kernelv1.UpdateItemRequest{
		Name: it, RequestId: newID(), Fields: fields(t, map[string]any{"f_assignee": "user-bob", "f_priority": nil}),
	})); err != nil {
		t.Fatalf("patch: %v", err)
	}
	transition(t, "bob", it, "in_progress")
	if _, err := as("ana").UpdateItem(ctx, connect.NewRequest(&kernelv1.UpdateItemRequest{
		Name: it, RequestId: newID(), Move: &kernelv1.Move{Container: to},
	})); err != nil {
		t.Fatalf("move: %v", err)
	}
	if _, err := as("ana").UpdateItem(ctx, connect.NewRequest(&kernelv1.UpdateItemRequest{
		Name: it, RequestId: newID(),
		Policy: &kernelv1.PolicyChange{Change: &kernelv1.PolicyChange_Narrow{Narrow: &kernelv1.Readers{Principals: []string{"user:ana", "user:bob"}}}},
	})); err != nil {
		t.Fatalf("narrow: %v", err)
	}
	// bob loses the old container.
	if _, err := as("ana").UpdateItem(ctx, connect.NewRequest(&kernelv1.UpdateItemRequest{
		Name: from, RequestId: newID(),
		Policy: &kernelv1.PolicyChange{Change: &kernelv1.PolicyChange_Narrow{Narrow: &kernelv1.Readers{Principals: []string{"user:ana"}}}},
	})); err != nil {
		t.Fatalf("narrow the old container: %v", err)
	}

	evs := events(t, "bob", it)
	kinds := []kernelv1.ItemEventKind{
		kernelv1.ItemEventKind_ITEM_EVENT_KIND_CREATED, kernelv1.ItemEventKind_ITEM_EVENT_KIND_UPDATED,
		kernelv1.ItemEventKind_ITEM_EVENT_KIND_TRANSITIONED, kernelv1.ItemEventKind_ITEM_EVENT_KIND_UPDATED,
		kernelv1.ItemEventKind_ITEM_EVENT_KIND_UPDATED,
	}
	if len(evs) != len(kinds) {
		t.Fatalf("%d events, want %d: %v", len(evs), len(kinds), evs)
	}
	for i, e := range evs {
		if e.Kind != kinds[i] || e.ItemSeq != int64(i+1) || e.Name != it+"/events/"+strconv.Itoa(i+1) || e.CommitSeq == 0 || e.CreateTime == nil {
			t.Errorf("event %d = %v, want %v at item_seq %d", i, e, kinds[i], i+1)
		}
	}

	c := evs[0]
	if c.Actor != "user:ana" || c.Type != ticketV1 || c.ToState != "open" ||
		c.Container == nil || !c.Container.Restricted || c.Container.Name != "" {
		t.Errorf("create, as bob = %v; want ana, ticket v1, open, a restricted container", c)
	}
	if f := c.Fields.AsMap(); f["f_title"] != "printer" || f["f_priority"] != "2" {
		t.Errorf("create fields = %v, want f_title and f_priority \"2\" (an int as a string)", f)
	}
	p := evs[1]
	if f := p.Fields.AsMap(); len(f) != 2 || f["f_assignee"] != "user-bob" || f["f_priority"] != nil {
		t.Errorf("patch fields = %v, want f_assignee set and f_priority null", f)
	} else if _, ok := f["f_priority"]; !ok {
		t.Errorf("patch fields = %v: a cleared field must be there, as null", f)
	}
	if tr := evs[2]; tr.FromState != "open" || tr.ToState != "in_progress" || tr.Actor != "user:bob" {
		t.Errorf("transition = %v", tr)
	}
	if mv := evs[3]; !mv.Moved || mv.ContainerFrom == nil || !mv.ContainerFrom.Restricted ||
		mv.ContainerTo == nil || mv.ContainerTo.Name != to {
		t.Errorf("move, as bob = %v; want from a restricted container to %s", mv, to)
	}
	if n := evs[4]; !slices.Equal(n.GetReaders().GetPrincipals(), []string{"user:ana", "user:bob"}) {
		t.Errorf("narrowing = %v, want readers ana and bob", n)
	}

	// ana sees the old container's name.
	if c := events(t, "ana", it)[0]; c.Container == nil || c.Container.Restricted || c.Container.Name != from {
		t.Errorf("create, as ana: container %v, want %s", c.Container, from)
	}
}

// An erased value is named, not missing.
func TestListItemEvents_Erased(t *testing.T) {
	it := create(t, "ana", ticketV1, map[string]any{"f_title": "x", "f_reporter": "Jane Doe"}).Item.Name
	if _, err := pool.Exec(testCtx(t),
		`update item_event_values set field_values = field_values - 'f_reporter' where item_id = $1 and item_seq = 1`,
		itemID(t, it)); err != nil {
		t.Fatalf("erase: %v", err)
	}
	e := events(t, "ana", it)[0]
	if f := e.Fields.AsMap(); len(f) != 1 || f["f_title"] != "x" || !slices.Equal(e.ErasedFields, []string{"f_reporter"}) {
		t.Errorf("fields %v, erased %v; want f_title kept, f_reporter erased", f, e.ErasedFields)
	}
}

// A held transition shows as requested, naming its hold.
func TestListItemEvents_HeldTransition(t *testing.T) {
	it := create(t, "ana", ticketV3, map[string]any{
		"f_customer_id": "cust-1", "f_amount": 100, "f_assignee": "user-bob",
	}).Item.Name
	transition(t, "ana", it, "open")
	transition(t, "ana", it, "in_progress")
	res, err := as("ana").TransitionItem(testCtx(t), connect.NewRequest(&kernelv1.TransitionItemRequest{
		Name: it, RequestId: newID(), ToState: "approved", Wait: durationpb.New(0),
	}))
	if err != nil || res.Msg.Hold == "" {
		t.Fatalf("approve: %v, %v; want a hold", res, err)
	}
	evs := events(t, "bob", it)
	last := evs[len(evs)-1]
	if last.Kind != kernelv1.ItemEventKind_ITEM_EVENT_KIND_TRANSITION_REQUESTED || last.FromState != "in_progress" ||
		last.RequestedState != "approved" || last.Hold != res.Msg.Hold {
		t.Errorf("last event = %v, want TRANSITION_REQUESTED in_progress -> approved naming %s", last, res.Msg.Hold)
	}
}

func TestListItemEvents_Refusals(t *testing.T) {
	ctx := testCtx(t)
	private := createNarrowed(t, "ana", "user:ana")
	_, err := as("bob").ListItemEvents(ctx, connect.NewRequest(&kernelv1.ListItemEventsRequest{Parent: private}))
	notFound(t, "an Item bob cannot see", err)
	_, err = as("ana").ListItemEvents(ctx, connect.NewRequest(&kernelv1.ListItemEventsRequest{Parent: "namespaces/acme"}))
	invalid(t, "a namespace as parent", err, "parent")
}
