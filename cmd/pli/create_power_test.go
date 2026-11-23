// Tests for starting work, step 1 (docs/plans/starting-work.md, 1.0): an
// agent started without the create power may not item create.
package main

import "testing"

func TestPli_AgentCreateNeedsTheCreatePower(t *testing.T) {
	session := ok(t, "bot", "agent", "start", "--for", "user:ana", "--scopes", "namespace",
		"--powers", "read,edit_fields")["session"].(map[string]any)["id"].(string)
	fails(t, "bot", "PERMISSION_DENIED NOT_ALLOWED", "--session", session, "item", "create", "--type", "ticket@1")
	// The same agent still updates.
	name := create(t, "ana", "--type", "ticket@1")
	ok(t, "bot", "--session", session, "item", "update", name, "--set", "f_title=bot was here")
}
