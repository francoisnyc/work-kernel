// Tests for Step 6, part 1: hook principals are a kind of their own,
// hook:<type>:<hook-id>, with no version, so `allowed` matches across Type
// versions. The version lives on originating_hook instead.
package item_test

import (
	"testing"

	"github.com/francoisnyc/work-kernel/internal/item"
)

func TestKindOf_Hook(t *testing.T) {
	good := []string{"hook:ticket:crm_plan", "hook:ticket:erp-check"}
	for _, id := range good {
		if k, ok := item.KindOf(id); !ok || k != item.KindHook {
			t.Errorf("KindOf(%q) = %q, %v; want %q, true", id, k, ok, item.KindHook)
		}
	}
	bad := []string{
		"hook:",                   // nothing after the kind
		"hook:ticket",             // no hook ID
		"hook:ticket:",            // empty hook ID
		"hook::crm_plan",          // empty Type
		"hook:ticket@v1:crm_plan", // the version belongs on originating_hook, not the actor
		"hook:ticket:crm_plan:x",  // one Type, one hook ID, nothing more
	}
	for _, id := range bad {
		if k, ok := item.KindOf(id); ok {
			t.Errorf("KindOf(%q) = %q, true; want not a valid principal", id, k)
		}
	}
}

// HookParts splits a hook principal into its Type and hook ID, so the item
// service can check that a hook acts only on Items of its own Type.
func TestHookParts(t *testing.T) {
	typeID, hookID, ok := item.HookParts("hook:ticket:crm_plan")
	if !ok || typeID != "ticket" || hookID != "crm_plan" {
		t.Errorf("HookParts = %q, %q, %v; want ticket, crm_plan, true", typeID, hookID, ok)
	}
	for _, id := range []string{"user:alice", "system:timer", "hook:ticket", "ticket:crm_plan"} {
		if _, _, ok := item.HookParts(id); ok {
			t.Errorf("HookParts(%q) ok = true, want false", id)
		}
	}
}

// --- proof of concept, Step 2, part 2: reaction principals ---

// A link reaction acts as reaction:<type>:<id>, a kind of its own, with no
// version (the version is on originating_hook, reaction:<type>@v<n>:<id>).
func TestKindOf_Reaction(t *testing.T) {
	for _, id := range []string{"reaction:vacation_request:deduct", "reaction:vacation_request:refund"} {
		if k, ok := item.KindOf(id); !ok || k != item.KindReaction {
			t.Errorf("KindOf(%q) = %q, %v; want %q, true", id, k, ok, item.KindReaction)
		}
	}
	bad := []string{
		"reaction:",                           // nothing after the kind
		"reaction:vacation_request",           // no reaction ID
		"reaction:vacation_request:",          // empty reaction ID
		"reaction::deduct",                    // empty Type
		"reaction:vacation_request@v2:deduct", // the version belongs on originating_hook
		"reaction:vacation_request:deduct:x",  // one Type, one reaction ID, nothing more
	}
	for _, id := range bad {
		if k, ok := item.KindOf(id); ok {
			t.Errorf("KindOf(%q) = %q, true; want not a valid principal", id, k)
		}
	}
}

func TestReactionParts(t *testing.T) {
	typeID, reactionID, ok := item.ReactionParts("reaction:vacation_request:deduct")
	if !ok || typeID != "vacation_request" || reactionID != "deduct" {
		t.Errorf("ReactionParts = %q, %q, %v; want vacation_request, deduct, true", typeID, reactionID, ok)
	}
	for _, id := range []string{"user:alice", "hook:vacation_request:deduct", "reaction:vacation_request", "vacation_request:deduct"} {
		if _, _, ok := item.ReactionParts(id); ok {
			t.Errorf("ReactionParts(%q) ok = true, want false", id)
		}
	}
}

// A reaction is a well-formed actor (which door it may use is the item
// service's rule). It is never a valid on_behalf_of: that is the human at
// the root.
func TestPrincipalValid_Reaction(t *testing.T) {
	if !(item.Principal{Actor: "reaction:vacation_request:deduct"}).Valid() {
		t.Error("a reaction actor is not valid")
	}
	if (item.Principal{Actor: "user:ana", OnBehalfOf: "reaction:vacation_request:deduct"}).Valid() {
		t.Error("a reaction as on_behalf_of is valid")
	}
	if (item.Principal{Actor: "reaction:vacation_request:deduct", AgentSession: "s1"}).Valid() {
		t.Error("a reaction with an agent session is valid")
	}
}
