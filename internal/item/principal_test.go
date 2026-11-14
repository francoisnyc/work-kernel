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
