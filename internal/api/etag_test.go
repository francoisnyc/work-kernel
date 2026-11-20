// Tests for the etag step, through the API. The etag is derived from the
// Item's content_seq, not from item_seq: it changes only when something a
// caller could overwrite changes (the state, a field a caller may write, a
// link, the readers, the container). Outcome records, hook records and
// writes of fields only the kernel writes leave it alone.
//
// Its text is c<content_seq> ("c7"): opaque to clients, and no longer a
// number, so it cannot be taken for item_seq. A decimal etag from before
// this step is STALE, like any etag this server never made.
//
// No worker runs here: the kernel's writes are made through its internal
// door, as the worker makes them. The end-to-end test is in
// internal/workflow.
package api_test

import (
	"regexp"
	"strconv"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	kernelv1 "github.com/francoisnyc/work-kernel/gen/kernel/v1"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
)

var etagForm = regexp.MustCompile(`^c[1-9][0-9]*$`)

func updateWith(t *testing.T, who, name, etag string, f map[string]any) (*kernelv1.UpdateItemResponse, error) {
	t.Helper()
	res, err := as(who).UpdateItem(testCtx(t), connect.NewRequest(&kernelv1.UpdateItemRequest{
		Name: name, RequestId: newID(), Etag: etag, Fields: fields(t, f),
	}))
	if err != nil {
		return nil, err
	}
	return res.Msg, nil
}

// goalShows runs a new goal's own set reactions, as the worker does after
// the goal is created: the one of its metric writes the due instant on the
// incident; the others say condition_false.
func goalShows(t *testing.T, goal uuid.UUID) {
	t.Helper()
	ty, err := k.Type(testCtx(t), "sla_goal", 1)
	if err != nil {
		t.Fatalf("the sla_goal Type: %v", err)
	}
	applied := 0
	for _, r := range ty.Reactions {
		if r.State != "running" {
			continue
		}
		out, err := k.Internal().ReactSet(testCtx(t), runReaction(goal, r.ID, goal, 1))
		if err != nil {
			t.Fatalf("ReactSet %s: %v", r.ID, err)
		}
		if out.Outcome == itemservice.ReactionApplied {
			applied++
		}
	}
	if applied != 1 {
		t.Fatalf("%d set reactions applied for a new goal, want exactly one", applied)
	}
}

// The etag's text: c and the content's count, from 1.
func TestEtag_Form(t *testing.T) {
	created := create(t, "ana", ticketV1, map[string]any{"f_title": "Printer on fire"})
	if created.Item.Etag != "c1" || created.ItemSeq != 1 {
		t.Errorf("a new Item: etag %q, item_seq %d; want c1 and 1", created.Item.Etag, created.ItemSeq)
	}
	updated, err := updateWith(t, "ana", created.Item.Name, created.Item.Etag, map[string]any{"f_priority": 1})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Item.Etag != "c2" || updated.ItemSeq != 2 {
		t.Errorf("after one update: etag %q, item_seq %d; want c2 and 2", updated.Item.Etag, updated.ItemSeq)
	}
	if got := get(t, "bob", created.Item.Name).Etag; got != "c2" || !etagForm.MatchString(got) {
		t.Errorf("GetItem etag = %q, want c2", got)
	}
}

// The measurement of Jira 2.3b: ana reads an incident right after its
// create; its goals are created and write their due instants on it (four
// events, all the kernel's); her save with the etag she read is accepted.
func TestEtag_KernelWritesLeaveItAlone_TheEarlySaveIsAccepted(t *testing.T) {
	desk := itDesk(t)
	f := map[string]any{"f_title": "VPN down " + word(), "f_priority": "P4"}
	res, err := as("ana").CreateItem(testCtx(t), connect.NewRequest(&kernelv1.CreateItemRequest{
		Parent: "namespaces/acme", RequestId: newID(), Type: incidentV1, Container: desk, Fields: fields(t, f),
	}))
	if err != nil {
		t.Fatalf("create the incident: %v", err)
	}
	inc, read := res.Msg.Item.Name, res.Msg.Item.Etag
	if read != "c1" {
		t.Fatalf("a new incident's etag = %q, want c1", read)
	}

	id := itemID(t, inc)
	for _, reaction := range []string{"sla_start_first_response", "sla_start_resolution"} {
		out, err := k.Internal().ReactCreate(testCtx(t), runReaction(id, reaction, id, 1))
		if err != nil || out.Outcome != itemservice.ReactionApplied {
			t.Fatalf("%s: %+v, %v", reaction, out, err)
		}
		goalShows(t, out.Target)
	}
	now := get(t, "ana", inc)
	due := now.Fields.AsMap()
	if due["f_due_first_response"] == nil || due["f_due_resolution"] == nil {
		t.Fatalf("the due instants are not on the incident: %v", due)
	}
	if n := countEvents(t, inc); n != 5 {
		t.Fatalf("the incident has %d events, want 5 (create, two outcome records, two due instants)", n)
	}
	if now.Etag != read {
		t.Errorf("the etag after the kernel's four writes = %q, want it unchanged (%q)", now.Etag, read)
	}

	saved, err := updateWith(t, "ana", inc, read, map[string]any{"f_impact": "minor"})
	if err != nil {
		t.Fatalf("ana's save with the etag read at the create: %v; want it accepted", err)
	}
	if saved.Item.Etag != "c2" || saved.ItemSeq != 6 {
		t.Errorf("after the save: etag %q, item_seq %d; want c2 and 6", saved.Item.Etag, saved.ItemSeq)
	}
	// The two are different things: the etag is not item_seq as text.
	if saved.Item.Etag == strconv.FormatInt(saved.ItemSeq, 10) {
		t.Errorf("the etag %q is item_seq as text", saved.Item.Etag)
	}
}

// Two people change the same field of the same Item with the etag both
// read: the second is refused STALE. So is any other write of theirs with
// that etag: another field, a transition, a link.
func TestEtag_TwoPeople_TheSecondIsStale(t *testing.T) {
	ctx := testCtx(t)
	name, other := ticket5(t, "ana"), ticket5(t, "ana")
	read := get(t, "ana", name).Etag // ana and bob both read this

	if _, err := updateWith(t, "bob", name, read, map[string]any{"f_title": "bob's title"}); err != nil {
		t.Fatalf("bob's save: %v", err)
	}
	_, err := updateWith(t, "ana", name, read, map[string]any{"f_title": "ana's title"})
	apiError(t, "ana's save of the same field", err, connect.CodeAborted, "STALE")
	if got := get(t, "ana", name).Fields.AsMap()["f_title"]; got != "bob's title" {
		t.Errorf("f_title = %v, want bob's: the refused save wrote nothing", got)
	}
	_, err = updateWith(t, "ana", name, read, map[string]any{"f_priority": 1})
	apiError(t, "ana's save of another field", err, connect.CodeAborted, "STALE")
	_, err = as("ana").TransitionItem(ctx, connect.NewRequest(&kernelv1.TransitionItemRequest{
		Name: name, RequestId: newID(), Etag: read, ToState: "open",
	}))
	apiError(t, "ana's transition", err, connect.CodeAborted, "STALE")
	_, err = linkItems("ana", name, "blocks", other, read)
	apiError(t, "ana's link", err, connect.CodeAborted, "STALE")

	// With the etag she reads now, each goes through, and each changes it.
	etag := get(t, "ana", name).Etag
	if etag != "c2" {
		t.Fatalf("the etag after bob's save = %q, want c2", etag)
	}
	linked, err := linkItems("ana", name, "blocks", other, etag)
	if err != nil || linked.Item.Etag != "c3" {
		t.Fatalf("ana's link with the current etag = %v, %v; want the source at c3", linked.GetItem().GetEtag(), err)
	}
	if got := get(t, "ana", other).Etag; got != "c1" {
		t.Errorf("the link's target has etag %q, want c1: a link writes nothing on its target", got)
	}
	moved, err := as("ana").TransitionItem(ctx, connect.NewRequest(&kernelv1.TransitionItemRequest{
		Name: name, RequestId: newID(), Etag: "c3", ToState: "open",
	}))
	if err != nil || moved.Msg.Item.Etag != "c4" {
		t.Errorf("ana's transition with the current etag = %v, %v; want c4", moved.Msg.GetItem().GetEtag(), err)
	}
}

// A transition between the read and the save: STALE.
func TestEtag_ATransitionInBetween_IsStale(t *testing.T) {
	created := create(t, "ana", ticketV1, map[string]any{"f_title": "Printer on fire", "f_assignee": "user-bob"})
	transition(t, "bob", created.Item.Name, "in_progress")
	_, err := updateWith(t, "ana", created.Item.Name, created.Item.Etag, map[string]any{"f_priority": 1})
	apiError(t, "a save after someone's transition", err, connect.CodeAborted, "STALE")
}

// A hold that opens and is denied changed nothing a caller wrote: the etag
// read before it is still good. One that is allowed changed the state.
func TestEtag_Holds(t *testing.T) {
	approve := func(name string) string {
		res, err := as("ana").TransitionItem(testCtx(t), connect.NewRequest(&kernelv1.TransitionItemRequest{
			Name: name, RequestId: newID(), ToState: "approved",
		}))
		if err != nil || res.Msg.Outcome != kernelv1.TransitionOutcome_TRANSITION_OUTCOME_PENDING {
			t.Fatalf("approve = %v, %v; want PENDING", res, err)
		}
		return res.Msg.Hold
	}

	denied := v3Ticket(t)
	read := get(t, "ana", denied).Etag
	if read != "c3" {
		t.Fatalf("a ticket walked to in_progress has etag %q, want c3", read)
	}
	hold := approve(denied)
	if got := get(t, "ana", denied).Etag; got != read {
		t.Errorf("the etag while the hold is open = %q, want %q: the request wrote no field", got, read)
	}
	resolve(t, denied, hold, itemservice.VerdictDeny, "credit_hold")
	if _, err := updateWith(t, "ana", denied, read, map[string]any{"f_amount": 5}); err != nil {
		t.Errorf("a save with the etag read before a denied hold: %v; want it accepted", err)
	}

	allowed := v3Ticket(t)
	read = get(t, "ana", allowed).Etag
	resolve(t, allowed, approve(allowed), itemservice.VerdictAllow, "")
	_, err := updateWith(t, "ana", allowed, read, map[string]any{"f_title": "late"})
	apiError(t, "a save with the etag read before an allowed hold", err, connect.CodeAborted, "STALE")
}

// An etag this server never made is STALE: a decimal one from before this
// step (even the Item's own item_seq), and anything else.
func TestEtag_OldAndMadeUpEtagsAreStale(t *testing.T) {
	created := create(t, "ana", ticketV1, map[string]any{"f_title": "Printer on fire"}) // item_seq 1, content_seq 1
	for _, etag := range []string{"1", "2", "0", "-1", "c0", "c-1", "c", "C1", "c01", "c1x", " c1", "c1 ", "c+1", "abc", `"c1"`, `W/"c1"`} {
		_, err := updateWith(t, "ana", created.Item.Name, etag, map[string]any{"f_priority": 1})
		apiError(t, "UpdateItem with etag "+strconv.Quote(etag), err, connect.CodeAborted, "STALE")
	}
	if n := countEvents(t, created.Item.Name); n != 1 {
		t.Errorf("%d events after the refusals, want 1", n)
	}
	// No etag is no check, as before.
	if _, err := updateWith(t, "ana", created.Item.Name, "", map[string]any{"f_priority": 1}); err != nil {
		t.Errorf("a save without an etag: %v", err)
	}
}

// SearchItems and ListItems show the etag GetItem shows.
func TestEtag_SearchAndListShowTheSameEtag(t *testing.T) {
	ctx := testCtx(t)
	desk := itDesk(t)
	inc, first, _ := startedIncident(t, desk, nil)
	goalShows(t, first)
	saved, err := updateWith(t, "ana", inc, "c1", map[string]any{"f_impact": "minor"})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	want := get(t, "ana", inc).Etag
	if want != "c2" {
		t.Fatalf("GetItem etag = %q, want c2", want)
	}
	title, _ := saved.Item.Fields.AsMap()["f_title"].(string)
	found, err := as("ana").SearchItems(ctx, connect.NewRequest(&kernelv1.SearchItemsRequest{
		Parent: "namespaces/acme", Query: title[len("VPN down "):], MinSeq: saved.CommitSeq,
	}))
	if err != nil {
		t.Fatalf("SearchItems: %v", err)
	}
	listed, err := as("ana").ListItems(ctx, connect.NewRequest(&kernelv1.ListItemsRequest{Parent: desk}))
	if err != nil {
		t.Fatalf("ListItems: %v", err)
	}
	for what, items := range map[string][]*kernelv1.Item{"SearchItems": found.Msg.Items, "ListItems": listed.Msg.Items} {
		seen := false
		for _, it := range items {
			if it.Name == inc {
				seen = true
				if it.Etag != want {
					t.Errorf("%s shows etag %q, GetItem %q", what, it.Etag, want)
				}
			}
		}
		if !seen {
			t.Errorf("%s did not return the incident", what)
		}
	}
}
