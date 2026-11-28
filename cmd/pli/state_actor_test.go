// Who put an Item in its state, in pli (the Inbox for people, PR 3a):
// `item get` (and every Item answer) prints state_actor and, when an agent
// acted for someone, state_actor_on_behalf_of, as protojson does.
package main

import "testing"

func TestPli_StateActor(t *testing.T) {
	w := word()
	name := itemOf(t, ok(t, "ana", "item", "create", "--type", "ticket@1", "--set", "f_title="+w, "--set", "f_assignee=someone"))["name"].(string)
	got := itemOf(t, ok(t, "ana", "item", "get", name))
	if got["state_actor"] != "user:ana" {
		t.Errorf("item get after ana's create: state_actor %v, want user:ana", got["state_actor"])
	}
	if _, has := got["state_actor_on_behalf_of"]; has {
		t.Errorf("item get after ana's create prints state_actor_on_behalf_of %v; a person's own act has none", got["state_actor_on_behalf_of"])
	}

	// The bot, in a session for ana, moves it.
	ok(t, "bot", "agent", "start", "--for", "user:ana", "--scopes", "namespace", "--powers", "read,transition:*")
	moved := itemOf(t, ok(t, "bot", "item", "transition", name, "--to", "in_progress"))
	got = itemOf(t, ok(t, "ana", "item", "get", name))
	for what, it := range map[string]map[string]any{"the move's answer": moved, "item get": got} {
		if it["state_actor"] != "agent:triage-bot" || it["state_actor_on_behalf_of"] != "user:ana" {
			t.Errorf("%s after the bot's move: state_actor %v for %v, want agent:triage-bot for user:ana", what, it["state_actor"], it["state_actor_on_behalf_of"])
		}
	}
}
