// Starting work, step 2: incident@4's start block (in: [desk]) judges
// pli's creates of every incident version. In a desk, as before; at the
// top level, refused with the code and reason every refusal prints.
package main

import "testing"

func TestPli_Start_AnIncidentOnlyInADesk(t *testing.T) {
	desk := create(t, "ana", "--type", "desk@1", "--set", "f_key=IT", "--set", "f_name=IT "+word())
	for _, typ := range []string{"incident@1", "incident@3", "incident@4"} {
		create(t, "ana", "--type", typ, "--in", desk, "--set", "f_title=in a desk "+word(), "--set", "f_priority=P3")
		fails(t, "ana", "PERMISSION_DENIED NOT_ALLOWED", "item", "create", "--type", typ,
			"--set", "f_title=nowhere "+word(), "--set", "f_priority=P3")
	}
}
