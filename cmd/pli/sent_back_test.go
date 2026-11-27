// Tests for "sent back to the requester" in pli (docs/roadmap.md, track 1,
// item 5, step 5): `type get` shows which states wait on their requester
// (State.waits_on_requester), as GetType answers it. incident@6 is the
// Type in types/ whose waiting_for_customer does.
package main

import "testing"

func TestPli_TypeGet_ShowsWaitsOnRequester(t *testing.T) {
	ty := ok(t, "bob", "type", "get", "incident@6")["type"].(map[string]any)
	waits := map[string]bool{}
	for _, s := range ty["states"].([]any) {
		st := s.(map[string]any)
		on, _ := st["waits_on_requester"].(bool)
		waits[st["id"].(string)] = on
	}
	if len(waits) == 0 {
		t.Fatalf("type get incident@6 printed no states: %v", ty)
	}
	for id, on := range waits {
		if want := id == "waiting_for_customer"; on != want {
			t.Errorf("state %s: waits_on_requester = %v, want %v", id, on, want)
		}
	}
	// The version before says nothing of it.
	old := ok(t, "bob", "type", "get", "incident@5")["type"].(map[string]any)
	for _, s := range old["states"].([]any) {
		if on, _ := s.(map[string]any)["waits_on_requester"].(bool); on {
			t.Errorf("incident@5 state %v waits on its requester; only @6 should", s.(map[string]any)["id"])
		}
	}
}
