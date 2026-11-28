// Who put an Item in its state, over the wire (the Inbox for people, PR
// 3a): Item.state_actor (18) and Item.state_actor_on_behalf_of (19),
// additive, on every read and every write's answer. The rule is tested in
// internal/itemservice.
package api_test

import (
	"testing"

	"connectrpc.com/connect"

	kernelv1 "github.com/francoisnyc/work-kernel/gen/kernel/v1"
)

func TestStateActor_OverTheWire(t *testing.T) {
	ctx := testCtx(t)
	w := word()
	created := create(t, "ana", ticketV1, map[string]any{"f_title": "state actor " + w, "f_assignee": "someone"}).Item
	if created.StateActor != "user:ana" || created.StateActorOnBehalfOf != "" {
		t.Errorf("CreateItem's answer: state actor %q for %q, want user:ana", created.StateActor, created.StateActorOnBehalfOf)
	}

	// The bot, in a session for ana, moves it: the agent, for ana.
	session := startSession(t, "bot", "user:ana", []string{"namespaces/acme"}, "read", "transition:*")
	moved, err := asAgent("bot", session).TransitionItem(ctx, connect.NewRequest(&kernelv1.TransitionItemRequest{
		Name: created.Name, RequestId: newID(), ToState: "in_progress",
	}))
	if err != nil {
		t.Fatalf("the bot moves it: %v", err)
	}
	check := func(what string, it *kernelv1.Item, by, behalf string) {
		t.Helper()
		if it.GetStateActor() != by || it.GetStateActorOnBehalfOf() != behalf {
			t.Errorf("%s: state actor %q for %q, want %q for %q", what, it.GetStateActor(), it.GetStateActorOnBehalfOf(), by, behalf)
		}
	}
	check("the move's answer", moved.Msg.Item, "agent:triage-bot", "user:ana")
	check("GetItem", get(t, "bob", created.Name), "agent:triage-bot", "user:ana")
	found, err := as("bob").SearchItems(ctx, connect.NewRequest(&kernelv1.SearchItemsRequest{Parent: "namespaces/acme", Query: w}))
	if err != nil || len(found.Msg.Items) != 1 {
		t.Fatalf("SearchItems: %d Items, error %v", len(found.Msg.GetItems()), err)
	}
	check("SearchItems", found.Msg.Items[0], "agent:triage-bot", "user:ana")

	// bob moves it back: his, for nobody.
	back := transition(t, "bob", created.Name, "open").Item
	check("bob's move's answer", back, "user:bob", "")
	check("GetItem after bob's move", get(t, "ana", created.Name), "user:bob", "")

	// An update is not a move.
	upd, err := as("ana").UpdateItem(ctx, connect.NewRequest(&kernelv1.UpdateItemRequest{
		Name: created.Name, RequestId: newID(), Fields: fields(t, map[string]any{"f_priority": 2}),
	}))
	if err != nil {
		t.Fatalf("ana updates it: %v", err)
	}
	check("the update's answer", upd.Msg.Item, "user:bob", "")
}
