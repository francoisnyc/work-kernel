// Tests for Jira part 2, step 2, through the public API: what a Type shows
// of its reactions and its multi-value fields; sets as JSON arrays, set
// operations, contains and empty filters; and a reaction's moves and
// outcome records in an Item's history. (No worker runs here: the tests
// run a pending reaction through the kernel's door, as the worker would.)
package api_test

import (
	"slices"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/structpb"

	kernelv1 "github.com/francoisnyc/work-kernel/gen/kernel/v1"
	"github.com/francoisnyc/work-kernel/internal/api"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
)

const (
	ticketV6    = "namespaces/acme/types/ticket/versions/6"
	commentV2   = "namespaces/acme/types/comment/versions/2"
	checklistV1 = "namespaces/acme/types/checklist/versions/1"
)

func platV3(id string) string { return "namespaces/acme/types/" + id + "/versions/3" }

func typeNamed(t *testing.T, name string) *kernelv1.Type {
	t.Helper()
	res, err := as("ana").GetType(testCtx(t), connect.NewRequest(&kernelv1.GetTypeRequest{Name: name}))
	if err != nil {
		t.Fatalf("GetType %s: %v", name, err)
	}
	return res.Msg.Type
}

// --- what a Type shows ---

func TestGetType_ShowsMoveAndCreateReactionsAndSets(t *testing.T) {
	ticket := typeNamed(t, ticketV6)
	byID := map[string]*kernelv1.Reaction{}
	for _, r := range ticket.Reactions {
		byID[r.Id] = r
	}
	reopen := byID["reopen"]
	if reopen == nil || reopen.Kind != kernelv1.ReactionKind_REACTION_KIND_MOVE ||
		reopen.Trigger != kernelv1.ReactionTrigger_REACTION_TRIGGER_CREATED_INSIDE ||
		!slices.Equal(reopen.OfTypes, []string{"namespaces/acme/types/comment"}) || reopen.By != "creator" ||
		reopen.When == "" || reopen.ToState != "in_progress" || !slices.Equal(reopen.FromStates, []string{"waiting_for_customer"}) {
		t.Errorf("ticket@6 reopen = %v", reopen)
	}
	open := byID["open_checklist"]
	if open == nil || open.Kind != kernelv1.ReactionKind_REACTION_KIND_CREATE ||
		open.Trigger != kernelv1.ReactionTrigger_REACTION_TRIGGER_ENTER || open.State != "in_progress" ||
		!slices.Equal(open.FromStates, []string{"open"}) || open.CreateType != checklistV1 ||
		open.CreateFields["f_opened_at"] != "cause.at" {
		t.Errorf("ticket@6 open_checklist = %v", open)
	}

	var accepted *kernelv1.AcceptedReaction
	for _, a := range typeNamed(t, checklistV1).AcceptsReactions {
		if a.Reaction == "ticket:open_checklist" {
			accepted = a
		}
	}
	if accepted == nil || !accepted.Creates || !slices.Contains(accepted.CreatesFields, "f_opened_at") || len(accepted.Moves) != 0 {
		t.Errorf("checklist@1 accepts = %v, want ticket:open_checklist creating it", accepted)
	}

	for _, id := range []string{"story", "bug", "task"} {
		fields := map[string]*kernelv1.Field{}
		for _, f := range typeNamed(t, platV3(id)).Fields {
			fields[f.Id] = f
		}
		if f := fields["f_labels"]; f == nil || !f.Multiple || f.Max != 20 || f.Type != kernelv1.FieldType_FIELD_TYPE_STRING {
			t.Errorf("%s@3 f_labels = %v, want a set of strings, at most 20", id, f)
		}
		if f := fields["f_reviewers"]; f == nil || !f.Multiple || f.Max != 0 || f.Type != kernelv1.FieldType_FIELD_TYPE_PRINCIPAL {
			t.Errorf("%s@3 f_reviewers = %v, want a set of principals", id, f)
		}
		if f := fields["f_summary"]; f == nil || f.Multiple {
			t.Errorf("%s@3 f_summary = %v, want one value", id, f)
		}
	}
}

// --- multi-value fields ---

func strs(v any) []string {
	list, _ := v.([]any)
	out := make([]string, len(list))
	for i, m := range list {
		out[i], _ = m.(string)
	}
	return out
}

func TestMultiValue_API(t *testing.T) {
	plat := space(t)
	summary := "Labelled " + word()
	mk3 := func(f map[string]any) string {
		t.Helper()
		f["f_summary"] = summary
		res, err := as("ana").CreateItem(testCtx(t), connect.NewRequest(&kernelv1.CreateItemRequest{
			Parent: "namespaces/acme", RequestId: newID(), Type: platV3("story"), Container: plat, Fields: fields(t, f),
		}))
		if err != nil {
			t.Fatalf("CreateItem story@3: %v", err)
		}
		return res.Msg.Item.Name
	}
	update := func(name string, f map[string]any) (*kernelv1.Item, error) {
		res, err := as("ana").UpdateItem(testCtx(t), connect.NewRequest(&kernelv1.UpdateItemRequest{
			Name: name, RequestId: newID(), Fields: fields(t, f),
		}))
		if err != nil {
			return nil, err
		}
		return res.Msg.Item, nil
	}

	a := mk3(map[string]any{"f_labels": []any{"sso", "backend", "sso"}, "f_reviewers": []any{"user:sam"}})
	b := mk3(map[string]any{"f_labels": []any{"backend"}})
	c := mk3(map[string]any{})
	if got := strs(get(t, "ana", a).Fields.AsMap()["f_labels"]); !slices.Equal(got, []string{"backend", "sso"}) {
		t.Errorf("f_labels = %v, want [backend sso]: a sorted set", got)
	}

	// A set operation: no etag, and the answer holds the resulting set.
	it, err := update(a, map[string]any{
		"f_labels":    map[string]any{"add": []any{"urgent"}, "remove": []any{"sso"}},
		"f_reviewers": map[string]any{"add": []any{"user:lea"}},
	})
	if err != nil {
		t.Fatalf("a set operation: %v", err)
	}
	if got := strs(it.Fields.AsMap()["f_labels"]); !slices.Equal(got, []string{"backend", "urgent"}) {
		t.Errorf("after add and remove: f_labels = %v, want [backend urgent]", got)
	}
	if got := strs(it.Fields.AsMap()["f_reviewers"]); !slices.Equal(got, []string{"user:lea", "user:sam"}) {
		t.Errorf("f_reviewers = %v, want [user:lea user:sam]", got)
	}
	// History: the resulting set, and what was asked.
	evs := events(t, "ana", a)
	last := evs[len(evs)-1]
	if got := strs(last.Fields.AsMap()["f_labels"]); !slices.Equal(got, []string{"backend", "urgent"}) {
		t.Errorf("the event's f_labels = %v, want the resulting set", got)
	}
	ops, _ := last.SetOps.AsMap()["f_labels"].(map[string]any)
	if !slices.Equal(strs(ops["add"]), []string{"urgent"}) || !slices.Equal(strs(ops["remove"]), []string{"sso"}) {
		t.Errorf("the event's set_ops = %v, want f_labels add [urgent] remove [sso]", last.SetOps)
	}
	if evs[0].SetOps != nil && len(evs[0].SetOps.Fields) != 0 {
		t.Errorf("the create's set_ops = %v, want none", evs[0].SetOps)
	}

	stories := func(ff ...*kernelv1.FieldFilter) []string {
		t.Helper()
		ff = append(ff, &kernelv1.FieldFilter{Field: "f_summary", Eq: str(summary)})
		got := searchAll(t, "ana", &kernelv1.SearchItemsRequest{
			Filter: &kernelv1.ItemFilter{Types: []string{platRef("story")}, Fields: ff},
		})
		slices.Sort(got)
		return got
	}
	sorted := func(names ...string) []string { slices.Sort(names); return names }
	if got := stories(&kernelv1.FieldFilter{Field: "f_labels", Eq: str("backend")}); !slices.Equal(got, sorted(a, b)) {
		t.Errorf("labelled backend = %v, want the two", got)
	}
	if got := stories(&kernelv1.FieldFilter{Field: "f_labels", Eq: str("backend")},
		&kernelv1.FieldFilter{Field: "f_labels", Eq: str("urgent")}); !slices.Equal(got, []string{a}) {
		t.Errorf("labelled backend and urgent = %v, want %v", got, a)
	}
	if got := stories(&kernelv1.FieldFilter{Field: "f_labels", Empty: true}); !slices.Equal(got, []string{c}) {
		t.Errorf("without a label = %v, want %v", got, c)
	}
	if got := stories(&kernelv1.FieldFilter{Field: "f_reviewers", Eq: str("user:lea")}); !slices.Equal(got, []string{a}) {
		t.Errorf("reviewed by lea = %v, want %v", got, a)
	}
	// empty works on any field: the stories nobody resolved.
	if got := stories(&kernelv1.FieldFilter{Field: "f_resolved_at", Empty: true}); len(got) != 3 {
		t.Errorf("not resolved = %v, want all three", got)
	}

	// Refusals.
	_, err = update(b, map[string]any{"f_labels": "backend"})
	invalid(t, "one value where a list is wanted", err, "fields.f_labels")
	_, err = update(b, map[string]any{"f_labels": map[string]any{"add": []any{"x"}, "remove": []any{"x"}}})
	invalid(t, "a value both added and removed", err, "fields.f_labels")
	many := make([]any, 21)
	for i := range many {
		many[i] = "l" + uuid.NewString()[:6]
	}
	_, err = update(b, map[string]any{"f_labels": map[string]any{"add": many}})
	ce := apiError(t, "a twenty-second label", err, connect.CodeFailedPrecondition, "TOO_MANY_VALUES")
	if md := metadata(t, ce); md["field"] != "f_labels" || md["max"] != "20" {
		t.Errorf("metadata = %v, want field f_labels, max 20", md)
	}
	if msg, ok := api.ReasonMessage("TOO_MANY_VALUES"); !ok || msg == "" {
		t.Errorf("TOO_MANY_VALUES has no fixed message")
	}
	_, err = as("ana").SearchItems(testCtx(t), connect.NewRequest(&kernelv1.SearchItemsRequest{
		Parent: "namespaces/acme", OrderBy: "f_labels", Filter: &kernelv1.ItemFilter{Types: []string{platRef("story")}},
	}))
	invalid(t, "order_by a set", err, "order_by")
	_, err = as("ana").SearchItems(testCtx(t), connect.NewRequest(&kernelv1.SearchItemsRequest{
		Parent: "namespaces/acme", Filter: &kernelv1.ItemFilter{Types: []string{platRef("story")},
			Fields: []*kernelv1.FieldFilter{{Field: "f_labels", Eq: str("a"), Empty: true}}},
	}))
	invalid(t, "empty with eq", err, "filter.fields[0]")
}

// --- reactions in history ---

// ana's ticket waits for her; her public comment sets it back in progress,
// someone else's does not. The ticket's history says what each reaction
// did, and the checklist made when work started is in it.
func TestReactions_API_ShowInHistory(t *testing.T) {
	ticket := create(t, "ana", ticketV6, map[string]any{"f_title": "VPN " + word(), "f_assignee": "user:bob"}).Item.Name
	transition(t, "ana", ticket, "open")
	transition(t, "ana", ticket, "in_progress")
	id := itemID(t, ticket)
	inside := func(reaction string, cause uuid.UUID, seq int64) itemservice.ReactionRunRequest {
		return itemservice.ReactionRunRequest{SourceID: id, Reaction: reaction, CauseItem: cause, CauseSeq: seq, Hop: 1}
	}
	entered := int64(0)
	for _, ev := range events(t, "ana", ticket) {
		if ev.Kind == kernelv1.ItemEventKind_ITEM_EVENT_KIND_TRANSITIONED && ev.ToState == "in_progress" {
			entered = ev.ItemSeq
		}
	}
	if out, err := k.Internal().ReactCreate(testCtx(t), inside("open_checklist", id, entered)); err != nil || out.Outcome != itemservice.ReactionApplied {
		t.Fatalf("the checklist reaction: %+v, %v", out, err)
	}
	list, err := as("ana").ListItems(testCtx(t), connect.NewRequest(&kernelv1.ListItemsRequest{Parent: ticket}))
	if err != nil || len(list.Msg.Items) != 1 || list.Msg.Items[0].Type != checklistV1 ||
		list.Msg.Items[0].Creator != "reaction:ticket:open_checklist" {
		t.Fatalf("the ticket's contained Items = %v, %v; want one checklist@1 created by the reaction", list.Msg.GetItems(), err)
	}

	transition(t, "ana", ticket, "waiting_for_customer")
	// By hand, ana (not support) may not take it back.
	_, err = moveWith(t, "ana", ticket, "in_progress", nil)
	apiError(t, "the requester moves it back by hand", err, connect.CodePermissionDenied, "NOT_ALLOWED")

	comment := func(who string, public bool) uuid.UUID {
		t.Helper()
		res, err := as(who).CreateItem(testCtx(t), connect.NewRequest(&kernelv1.CreateItemRequest{
			Parent: "namespaces/acme", RequestId: newID(), Type: commentV2, Container: ticket,
			Fields: fields(t, map[string]any{"f_body": "any news?", "f_public": public}),
		}))
		if err != nil {
			t.Fatalf("comment as %s: %v", who, err)
		}
		return itemID(t, res.Msg.Item.Name)
	}
	byBob := comment("bob", true)
	if out, err := k.Internal().ReactMove(testCtx(t), inside("reopen", byBob, 1)); err != nil || out.Reason != itemservice.SkipNotBy {
		t.Fatalf("bob's comment: %+v, %v; want skipped, not_by", out, err)
	}
	byAna := comment("ana", true)
	if out, err := k.Internal().ReactMove(testCtx(t), inside("reopen", byAna, 1)); err != nil || out.Outcome != itemservice.ReactionApplied {
		t.Fatalf("ana's comment: %+v, %v; want applied", out, err)
	}
	if got := get(t, "ana", ticket); got.State != "in_progress" {
		t.Fatalf("the ticket is %s, want in_progress", got.State)
	}

	var kinds []string
	var moved *kernelv1.ItemEvent
	for _, ev := range events(t, "ana", ticket) {
		switch ev.Kind {
		case kernelv1.ItemEventKind_ITEM_EVENT_KIND_REACTION_APPLIED:
			kinds = append(kinds, "applied "+ev.Reaction)
		case kernelv1.ItemEventKind_ITEM_EVENT_KIND_REACTION_SKIPPED:
			kinds = append(kinds, "skipped "+ev.Reaction+" "+ev.Reason)
		case kernelv1.ItemEventKind_ITEM_EVENT_KIND_REACTION_FAILED:
			kinds = append(kinds, "failed "+ev.Reaction+" "+ev.Reason)
		case kernelv1.ItemEventKind_ITEM_EVENT_KIND_TRANSITIONED:
			if ev.FromState == "waiting_for_customer" {
				moved = ev
			}
		}
	}
	want := []string{"applied ticket:open_checklist", "skipped ticket:reopen NOT_BY", "applied ticket:reopen"}
	if !slices.Equal(kinds, want) {
		t.Errorf("the ticket's reaction records = %v, want %v", kinds, want)
	}
	if moved == nil || moved.Actor != "reaction:ticket:reopen" || moved.OnBehalfOf != "user:ana" ||
		moved.CausationId != byAna.String()+":1" || moved.ToState != "in_progress" {
		t.Errorf("the move = %v, want the reaction's, for ana, caused by her comment's create", moved)
	}
}

var _ = structpb.NewNullValue
