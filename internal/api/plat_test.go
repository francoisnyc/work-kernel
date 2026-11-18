// Tests for the Jira proof of concept, part 1, through the public API: the
// PLAT software project's Types (types/) and the kernel features it needs
// (docs/design.md, "Jira proof of concept").
//
//   - order_by on SearchItems and ListItems, AIP-132 style, with page
//     tokens bound to it. The board is a search for the Items linked to a
//     sprint, ordered by rank: never a listing of the sprint as a container.
//   - Field filters across Types ("my open work"), on dates, and string
//     ranges.
//   - An epic's progress: a rollup on GetItem, counting what the caller can
//     see; its guard over the issues that link to it.
//   - Done requires a resolution; reopening clears it, and history says so.
//   - Watching is a Subscription Item and a link; no Watch call.
package api_test

import (
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/structpb"

	kernelv1 "github.com/francoisnyc/work-kernel/gen/kernel/v1"
)

// platType is a PLAT Type version's name; platRef names the Type in a
// filter (no version).
func platType(id string) string { return "namespaces/acme/types/" + id + "/versions/1" }
func platRef(id string) string  { return "namespaces/acme/types/" + id }

var issueTypes = []string{platRef("story"), platRef("bug"), platRef("task")}

// mk creates an Item of a PLAT Type as who, in a container when one is
// given, readable only by readers when any are given.
func mk(t *testing.T, who, typ, container string, f map[string]any, readers ...string) string {
	t.Helper()
	req := &kernelv1.CreateItemRequest{
		Parent: "namespaces/acme", RequestId: newID(), Type: platType(typ), Container: container, Fields: fields(t, f),
	}
	if len(readers) > 0 {
		req.Narrow = &kernelv1.Readers{Principals: readers}
	}
	res, err := as(who).CreateItem(testCtx(t), connect.NewRequest(req))
	if err != nil {
		t.Fatalf("CreateItem %s as %s: %v", typ, who, err)
	}
	return res.Msg.Item.Name
}

// space makes a project container.
func space(t *testing.T) string {
	t.Helper()
	return mk(t, "ana", "space", "", map[string]any{"f_key": "PLAT", "f_name": "Platform " + word()})
}

// moveWith transitions with a patch.
func moveWith(t *testing.T, who, name, to string, f map[string]any) (*kernelv1.TransitionItemResponse, error) {
	t.Helper()
	res, err := as(who).TransitionItem(testCtx(t), connect.NewRequest(&kernelv1.TransitionItemRequest{
		Name: name, RequestId: newID(), ToState: to, Fields: fields(t, f),
	}))
	if err != nil {
		return nil, err
	}
	return res.Msg, nil
}

// searchAll pages through a search as who and returns the names in order.
func searchAll(t *testing.T, who string, req *kernelv1.SearchItemsRequest) []string {
	t.Helper()
	req.Parent = "namespaces/acme"
	var all []string
	for pages := 0; ; pages++ {
		res, err := as(who).SearchItems(testCtx(t), connect.NewRequest(req))
		if err != nil {
			t.Fatalf("SearchItems %v as %s: %v", req, who, err)
		}
		all = append(all, names(res.Msg.Items)...)
		if res.Msg.NextPageToken == "" {
			return all
		}
		if pages > 200 {
			t.Fatal("more than 200 pages: the token does not advance")
		}
		req.PageToken = res.Msg.NextPageToken
	}
}

func linkedBy(linkType, item string) []*kernelv1.LinkFilter {
	return []*kernelv1.LinkFilter{{
		LinkType: linkType, Direction: kernelv1.LinkDirection_LINK_DIRECTION_OUTGOING, Item: item,
	}}
}

// --- the board ---

// The board: the Items linked to the sprint by `sprint`, ordered by rank,
// paged. Stories, bugs and tasks are contained in PLAT, so listing the
// sprint as a container shows none of them. Rank order is byte order: an
// upper-case key sorts before a lower-case one.
func TestPLAT_TheBoardIsASearchOrderedByRank(t *testing.T) {
	plat := space(t)
	sprint := mk(t, "ana", "sprint", plat, map[string]any{"f_name": "Sprint 12", "f_end": "2026-12-04"})
	card := func(typ, rank string) string {
		f := map[string]any{"f_summary": "card " + rank}
		if rank != "" {
			f["f_rank"] = rank
		}
		name := mk(t, "ana", typ, plat, f)
		mustLinkItems(t, "ana", name, "sprint", sprint)
		return name
	}
	b, a, upperB, c, unranked := card("story", "b"), card("bug", "a"), card("task", "B"), card("story", "c"), card("bug", "")
	backlog := mk(t, "ana", "story", plat, map[string]any{"f_summary": "not in the sprint", "f_rank": "aa"})

	onBoard := &kernelv1.ItemFilter{Links: linkedBy("sprint", sprint)}
	got := searchAll(t, "ana", &kernelv1.SearchItemsRequest{Filter: onBoard, OrderBy: "f_rank", PageSize: 2})
	if want := []string{upperB, a, b, c, unranked}; !slices.Equal(got, want) {
		t.Errorf("the board by rank, pages of 2:\n got %v\nwant %v", got, want)
	}
	got = searchAll(t, "ana", &kernelv1.SearchItemsRequest{Filter: onBoard, OrderBy: "f_rank desc", PageSize: 2})
	if want := []string{c, b, a, upperB, unranked}; !slices.Equal(got, want) {
		t.Errorf("the board by rank descending (unranked still last):\n got %v\nwant %v", got, want)
	}
	if slices.Contains(got, backlog) {
		t.Error("the board shows a story that is not linked to the sprint")
	}

	// The sprint contains nothing: its Items are linked to it.
	res, err := as("ana").ListItems(testCtx(t), connect.NewRequest(&kernelv1.ListItemsRequest{Parent: sprint}))
	if err != nil {
		t.Fatalf("ListItems of the sprint: %v", err)
	}
	if len(res.Msg.Items) != 0 {
		t.Errorf("listing the sprint as a container shows %v, want nothing", names(res.Msg.Items))
	}
	// The backlog: PLAT's own children of the three issue Types, ordered by
	// rank, whether they are in a sprint or not.
	listed, err := as("ana").ListItems(testCtx(t), connect.NewRequest(&kernelv1.ListItemsRequest{
		Parent: plat, Filter: &kernelv1.ItemFilter{Types: issueTypes}, OrderBy: "f_rank",
	}))
	if err != nil {
		t.Fatalf("ListItems of PLAT by rank: %v", err)
	}
	if want := []string{upperB, a, backlog, b, c, unranked}; !slices.Equal(names(listed.Msg.Items), want) {
		t.Errorf("PLAT's backlog by rank:\n got %v\nwant %v", names(listed.Msg.Items), want)
	}

	// The neighbour above a card: the highest rank below its own, one row
	// (a string range; this is what `pli rank X before Y` asks).
	above, err := as("ana").ListItems(testCtx(t), connect.NewRequest(&kernelv1.ListItemsRequest{
		Parent: plat, OrderBy: "f_rank desc", PageSize: 1,
		Filter: &kernelv1.ItemFilter{Types: issueTypes, Fields: []*kernelv1.FieldFilter{{Field: "f_rank", Lt: str("b")}}},
	}))
	if err != nil {
		t.Fatalf("the neighbour above b: %v", err)
	}
	if got := names(above.Msg.Items); !slices.Equal(got, []string{backlog}) {
		t.Errorf("the neighbour above b = %v, want the story ranked aa", got)
	}
}

// order_by's shape: one field, optionally asc or desc; anything else, an
// unsortable or unknown field, or a field the Types in play disagree on,
// is INVALID_ARGUMENT naming order_by. A page token is bound to it.
func TestOrderBy_API(t *testing.T) {
	plat := space(t)
	for _, rank := range []string{"a", "b", "c"} {
		mk(t, "ana", "story", plat, map[string]any{"f_summary": "s", "f_rank": rank, "f_priority": "P2"})
	}
	list := func(orderBy, token string, types ...string) (*kernelv1.ListItemsResponse, error) {
		res, err := as("ana").ListItems(testCtx(t), connect.NewRequest(&kernelv1.ListItemsRequest{
			Parent: plat, Filter: &kernelv1.ItemFilter{Types: types}, OrderBy: orderBy, PageSize: 2, PageToken: token,
		}))
		if err != nil {
			return nil, err
		}
		return res.Msg, nil
	}
	plain, err := list("f_rank", "", issueTypes...)
	if err != nil {
		t.Fatalf("order_by f_rank: %v", err)
	}
	explicit, err := list("f_rank asc", "", issueTypes...)
	if err != nil || !slices.Equal(names(explicit.Items), names(plain.Items)) {
		t.Errorf("order_by \"f_rank asc\" = %v, %v; want the same page as \"f_rank\"", explicit, err)
	}

	for what, orderBy := range map[string]string{
		"an unknown direction":     "f_rank sideways",
		"three words":              "f_rank desc please",
		"two fields":               "f_rank, f_priority",
		"a field no Type declares": "f_nope",
		"a principal field":        "f_assignee",
	} {
		_, err := list(orderBy, "", issueTypes...)
		invalid(t, what, err, "order_by")
	}
	// No Type named: ticket declares f_priority an int, the PLAT Types an
	// enum. The declarations disagree, so the sort is refused.
	_, err = as("ana").SearchItems(testCtx(t), connect.NewRequest(&kernelv1.SearchItemsRequest{
		Parent: "namespaces/acme", OrderBy: "f_priority",
		Filter: &kernelv1.ItemFilter{Categories: []kernelv1.StateCategory{kernelv1.StateCategory_STATE_CATEGORY_TODO}},
	}))
	invalid(t, "f_priority across ticket (int) and the PLAT Types (enum)", err, "order_by")

	if plain.NextPageToken == "" {
		t.Fatal("three stories in pages of 2: no next page token")
	}
	if _, err := list("f_rank", plain.NextPageToken, issueTypes...); err != nil {
		t.Errorf("the token with its own order: %v", err)
	}
	_, err = list("f_rank desc", plain.NextPageToken, issueTypes...)
	invalid(t, "an ascending token used descending", err, "page_token")
	_, err = list("", plain.NextPageToken, issueTypes...)
	invalid(t, "an ordered token used without order_by", err, "page_token")
}

// "My open work" (scenario A, step 7): assignee = me, the todo and active
// categories, across story, bug and task, ordered by priority.
func TestPLAT_MyOpenWorkByPriority(t *testing.T) {
	plat := space(t)
	mine := func(typ, priority, state string) string {
		name := mk(t, "ana", typ, plat, map[string]any{"f_summary": "work", "f_assignee": "user:sam", "f_priority": priority})
		switch state {
		case "in_progress":
			transition(t, "ana", name, "in_progress")
		case "done":
			transition(t, "ana", name, "in_progress")
			if _, err := moveWith(t, "ana", name, "done", map[string]any{"f_resolution": "fixed"}); err != nil {
				t.Fatalf("close: %v", err)
			}
		}
		return name
	}
	p3, p1, p2 := mine("story", "P3", "todo"), mine("bug", "P1", "in_progress"), mine("task", "P2", "todo")
	mine("story", "P1", "done")                                                                                     // not open
	mk(t, "ana", "story", plat, map[string]any{"f_summary": "ana's", "f_assignee": "user:ana", "f_priority": "P1"}) // not sam's

	got := searchAll(t, "sam-acme", &kernelv1.SearchItemsRequest{
		OrderBy: "f_priority",
		Filter: &kernelv1.ItemFilter{
			Types:      issueTypes,
			Fields:     []*kernelv1.FieldFilter{{Field: "f_assignee", Eq: str("me")}},
			Categories: []kernelv1.StateCategory{kernelv1.StateCategory_STATE_CATEGORY_TODO, kernelv1.StateCategory_STATE_CATEGORY_ACTIVE},
		},
	})
	// Other tests assign nothing to sam in the PLAT Types.
	if want := []string{p1, p2, p3}; !slices.Equal(got, want) {
		t.Errorf("sam's open work by priority:\n got %v\nwant %v", got, want)
	}
}

// --- the epic ---

func counts(it *kernelv1.Item, rollup string) map[string]int64 {
	if r, ok := it.Rollups[rollup]; ok {
		return r.Counts
	}
	return nil
}

func sameCounts(got, want map[string]int64) bool {
	if len(got) != len(want) {
		return false
	}
	for k, v := range want {
		if got[k] != v {
			return false
		}
	}
	return true
}

// An epic's progress is on GetItem, counts only what the caller can see,
// and is on no other answer. Its guard sees every issue: it does not close
// while one is open.
func TestPLAT_EpicProgressAndGuard(t *testing.T) {
	plat := space(t)
	epic := mk(t, "ana", "epic", plat, map[string]any{"f_summary": "Single sign-on", "f_owner": "user:ana"})
	issue := func(who, typ string, readers ...string) string {
		name := mk(t, who, typ, plat, map[string]any{"f_summary": "part of the epic"}, readers...)
		mustLinkItems(t, who, name, "epic", epic)
		return name
	}
	issue("ana", "story")
	working := issue("ana", "story")
	transition(t, "ana", working, "in_progress")
	fixed := issue("ana", "bug")
	transition(t, "ana", fixed, "in_progress")
	if _, err := moveWith(t, "ana", fixed, "done", map[string]any{"f_resolution": "fixed"}); err != nil {
		t.Fatalf("close the bug: %v", err)
	}
	issue("bob", "task", "user:bob") // only bob sees it

	if got, want := counts(get(t, "ana", epic), "progress"), map[string]int64{"todo": 1, "active": 1, "done": 1}; !sameCounts(got, want) {
		t.Errorf("the epic's progress as ana = %v, want %v (not bob's private task)", got, want)
	}
	if got, want := counts(get(t, "bob", epic), "progress"), map[string]int64{"todo": 2, "active": 1, "done": 1}; !sameCounts(got, want) {
		t.Errorf("the epic's progress as bob = %v, want %v", got, want)
	}

	// Only GetItem carries rollups: not a write's answer, not a search.
	upd, err := as("ana").UpdateItem(testCtx(t), connect.NewRequest(&kernelv1.UpdateItemRequest{
		Name: epic, RequestId: newID(), Fields: fields(t, map[string]any{"f_summary": "SSO"}),
	}))
	if err != nil {
		t.Fatalf("UpdateItem: %v", err)
	}
	if len(upd.Msg.Item.Rollups) != 0 {
		t.Errorf("UpdateItem's answer carries rollups %v", upd.Msg.Item.Rollups)
	}
	found, err := as("ana").SearchItems(testCtx(t), connect.NewRequest(&kernelv1.SearchItemsRequest{
		Parent: "namespaces/acme", Filter: &kernelv1.ItemFilter{Links: []*kernelv1.LinkFilter{{
			LinkType: "issues", Direction: kernelv1.LinkDirection_LINK_DIRECTION_INCOMING, Item: working,
		}}},
	}))
	if err != nil || len(found.Msg.Items) != 1 || found.Msg.Items[0].Name != epic {
		t.Fatalf("search for the epic of a story: %v, %v", found, err)
	}
	if len(found.Msg.Items[0].Rollups) != 0 {
		t.Errorf("a search result carries rollups %v", found.Msg.Items[0].Rollups)
	}

	transition(t, "ana", epic, "in_progress")
	_, err = moveWith(t, "ana", epic, "done", nil)
	ce := apiError(t, "close the epic with open issues", err, connect.CodeFailedPrecondition, "GUARD_FAILED")
	if md := metadata(t, ce); md["from_state"] != "in_progress" || md["to_state"] != "done" {
		t.Errorf("metadata = %v, want from_state in_progress, to_state done", md)
	}
}

// --- done requires a resolution; reopening clears it ---

func TestPLAT_ResolutionRequiredThenCleared(t *testing.T) {
	plat := space(t)
	story := mk(t, "ana", "story", plat, map[string]any{"f_summary": "Log in with SSO"})
	transition(t, "ana", story, "in_progress")

	_, err := moveWith(t, "ana", story, "done", nil)
	ce := apiError(t, "done without a resolution", err, connect.CodeFailedPrecondition, "MISSING_FIELDS")
	if md := metadata(t, ce); md["fields"] != "f_resolution" {
		t.Errorf("metadata fields = %q, want f_resolution", md["fields"])
	}
	done, err := moveWith(t, "ana", story, "done", map[string]any{"f_resolution": "fixed"})
	if err != nil {
		t.Fatalf("done with a resolution: %v", err)
	}
	if got := done.Item.Fields.AsMap()["f_resolution"]; got != "fixed" {
		t.Errorf("f_resolution = %v after done, want fixed", got)
	}

	reopened, err := moveWith(t, "ana", story, "todo", nil)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if v, set := reopened.Item.Fields.AsMap()["f_resolution"]; set {
		t.Errorf("f_resolution = %v after reopening, want it cleared", v)
	}
	// History shows the clear on the reopening event: the field, null.
	evs := events(t, "ana", story)
	last := evs[len(evs)-1]
	if last.Kind != kernelv1.ItemEventKind_ITEM_EVENT_KIND_TRANSITIONED || last.FromState != "done" || last.ToState != "todo" {
		t.Fatalf("the last event = %v, want the transition done -> todo", last)
	}
	v, named := last.Fields.GetFields()["f_resolution"]
	if _, null := v.GetKind().(*structpb.Value_NullValue); !named || !null {
		t.Errorf("the reopening event's fields = %v, want f_resolution: null", last.Fields)
	}
	if n := len(evs); n != 4 {
		t.Errorf("%d events, want 4: create, start, done, reopen (the clear is not a write of its own)", n)
	}

	transition(t, "ana", story, "in_progress")
	_, err = moveWith(t, "ana", story, "done", nil)
	apiError(t, "done again without a resolution", err, connect.CodeFailedPrecondition, "MISSING_FIELDS")
}

// --- the sprint: dates, closed by hand ---

func TestPLAT_SprintDatesAndClosing(t *testing.T) {
	plat := space(t)
	name := "Sprint " + word()
	sprint := mk(t, "ana", "sprint", plat, map[string]any{
		"f_name": name, "f_start": "2026-11-23", "f_end": "2026-12-04", "f_goal": "Ship SSO",
	})
	later := mk(t, "ana", "sprint", plat, map[string]any{"f_name": name, "f_start": "2026-12-07", "f_end": "2026-12-18"})

	sprints := func(ff ...*kernelv1.FieldFilter) []string {
		t.Helper()
		ff = append(ff, &kernelv1.FieldFilter{Field: "f_name", Eq: str(name)})
		return searchAll(t, "ana", &kernelv1.SearchItemsRequest{
			OrderBy: "f_end desc", Filter: &kernelv1.ItemFilter{Types: []string{platRef("sprint")}, Fields: ff},
		})
	}
	if got := sprints(); !slices.Equal(got, []string{later, sprint}) {
		t.Errorf("the sprints by end date, latest first = %v, want %v then %v", got, later, sprint)
	}
	if got := sprints(&kernelv1.FieldFilter{Field: "f_end", Gte: str("2026-12-01"), Lt: str("2026-12-10")}); !slices.Equal(got, []string{sprint}) {
		t.Errorf("the sprints ending in early December = %v, want %v", got, sprint)
	}
	if got := sprints(&kernelv1.FieldFilter{Field: "f_end", Eq: str("2026-12-18")}); !slices.Equal(got, []string{later}) {
		t.Errorf("the sprints ending on 2026-12-18 = %v, want %v", got, later)
	}
	_, err := as("ana").SearchItems(testCtx(t), connect.NewRequest(&kernelv1.SearchItemsRequest{
		Parent: "namespaces/acme", Filter: &kernelv1.ItemFilter{
			Types: []string{platRef("sprint")}, Fields: []*kernelv1.FieldFilter{{Field: "f_end", Gte: str("12/01/2026")}},
		},
	}))
	invalid(t, "a date that is not ISO 8601", err, "filter.fields[0]")

	// Closed by hand: planned -> active -> closed. No timer in part 1.
	if got := get(t, "ana", sprint); got.State != "planned" || got.Category != kernelv1.StateCategory_STATE_CATEGORY_BACKLOG {
		t.Errorf("a new sprint is %s (%v), want planned (backlog)", got.State, got.Category)
	}
	transition(t, "ana", sprint, "active")
	if got := transition(t, "ana", sprint, "closed"); got.Item.Category != kernelv1.StateCategory_STATE_CATEGORY_DONE {
		t.Errorf("a closed sprint's category = %v, want done", got.Item.Category)
	}
}

// --- comments ---

// A comment is an Item contained in what it comments on.
func TestPLAT_CommentIsAContainedItem(t *testing.T) {
	plat := space(t)
	story := mk(t, "ana", "story", plat, map[string]any{"f_summary": "Log in with SSO"})
	comment := mk(t, "bob", "comment", story, map[string]any{"f_body": "Blocked on the IdP " + word()})
	res, err := as("ana").ListItems(testCtx(t), connect.NewRequest(&kernelv1.ListItemsRequest{Parent: story}))
	if err != nil {
		t.Fatalf("ListItems of the story: %v", err)
	}
	if got := names(res.Msg.Items); !slices.Equal(got, []string{comment}) {
		t.Errorf("the story's children = %v, want its comment", got)
	}
	if it := res.Msg.Items[0]; it.Creator != "user:bob" || it.Category != kernelv1.StateCategory_STATE_CATEGORY_NONE {
		t.Errorf("the comment: creator %s, category %v; want user:bob, none (not work)", it.Creator, it.Category)
	}
}

// --- watching ---

// Watching is a Subscription Item, readable by its owner, linked to what it
// watches. An Item the caller cannot see is NOT_FOUND, as for any link.
// Unlinking unwatches. The API has no Watch call.
func TestPLAT_WatchingIsASubscriptionAndALink(t *testing.T) {
	plat := space(t)
	story := mk(t, "ana", "story", plat, map[string]any{"f_summary": "Log in with SSO"})
	secret := mk(t, "ana", "story", plat, map[string]any{"f_summary": "Not for bob"}, "user:ana")

	sub := mk(t, "bob", "subscription", "", nil, "user:bob")
	_, err := linkItems("bob", sub, "watches", secret, "")
	notFound(t, "bob watches a story he cannot see", err)
	mustLinkItems(t, "bob", sub, "watches", story)
	// A ticket too: the link targets any Type.
	sub2 := mk(t, "bob", "subscription", "", nil, "user:bob")
	mustLinkItems(t, "bob", sub2, "watches", ticket5(t, "ana"))

	assertWireLinks(t, "the story as bob", get(t, "bob", story).Links, "INCOMING watched_by "+sub)
	assertWireLinks(t, "the story as ana, who cannot see bob's subscription", get(t, "ana", story).Links)
	_, err = as("ana").GetItem(testCtx(t), connect.NewRequest(&kernelv1.GetItemRequest{Name: sub}))
	notFound(t, "ana reads bob's subscription", err)

	// What bob watches: his subscriptions, a search.
	watching := searchAll(t, "bob", &kernelv1.SearchItemsRequest{
		Filter: &kernelv1.ItemFilter{Links: linkedBy("watches", story)},
	})
	if !slices.Equal(watching, []string{sub}) {
		t.Errorf("bob's subscriptions to the story = %v, want %v", watching, sub)
	}

	if _, err := unlinkItems("bob", sub, "watches", story); err != nil {
		t.Fatalf("unwatch: %v", err)
	}
	assertWireLinks(t, "the story as bob, after unwatching", get(t, "bob", story).Links)
	if got := get(t, "bob", sub); len(got.Links) != 0 {
		t.Errorf("the subscription after unwatching links to %v; want it kept, watching nothing", got.Links)
	}

	methods := kernelv1.File_kernel_v1_kernel_proto.Services().Get(0).Methods()
	for i := range methods.Len() {
		if name := string(methods.Get(i).Name()); strings.Contains(strings.ToLower(name), "watch") {
			t.Errorf("the API has a %s call: watching is user space", name)
		}
	}
}

// --- the Types, as GetType shows them ---

func TestPLAT_TypesShowTheirDeclarations(t *testing.T) {
	typeOf := func(id string) *kernelv1.Type {
		t.Helper()
		res, err := as("ana").GetType(testCtx(t), connect.NewRequest(&kernelv1.GetTypeRequest{Name: platType(id)}))
		if err != nil {
			t.Fatalf("GetType %s: %v", id, err)
		}
		return res.Msg.Type
	}

	epic := typeOf("epic")
	if len(epic.Incoming) != 1 || epic.Incoming[0].Name != "issues" || epic.Incoming[0].Link != "epic" ||
		!slices.Equal(epic.Incoming[0].From, issueTypes) {
		t.Errorf("epic incoming = %v, want issues: the epic link from story, bug and task", epic.Incoming)
	}
	if len(epic.Rollups) != 1 || epic.Rollups[0].Id != "progress" || epic.Rollups[0].Over != "issues" || epic.Rollups[0].CountBy != "category" {
		t.Errorf("epic rollups = %v, want progress over issues by category", epic.Rollups)
	}

	for _, id := range []string{"story", "bug", "task"} {
		var reopen *kernelv1.Transition
		for _, tr := range typeOf(id).Transitions {
			if tr.FromState == "done" && tr.ToState == "todo" {
				reopen = tr
			}
		}
		if reopen == nil || !slices.Equal(reopen.Clears, []string{"f_resolution"}) {
			t.Errorf("%s: done -> todo = %v, want it to clear f_resolution", id, reopen)
		}
	}

	var watches *kernelv1.LinkType
	for _, l := range typeOf("subscription").LinkTypes {
		if l.Name == "watches" {
			watches = l
		}
	}
	if watches == nil || !slices.Equal(watches.Targets, []string{"*"}) || !watches.MaxOne || watches.Inverse != "watched_by" {
		t.Errorf("subscription watches = %v, want any target (*), at most one, inverse watched_by", watches)
	}
}
