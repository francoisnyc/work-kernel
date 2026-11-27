// Tests for a team's claimed rows in pli (the Inbox for people, PR 2a):
//
//	inbox [WORDS...] --role NAME --claimed
//	FILTERS: ... [--claimed]
//
// --claimed is one more FILTER (ItemFilter.claimed): the server judges it,
// so pli sends it as given and the server refuses it without waiting_on
// role:<name>. A waiting-on answer prints withheld beside moves (protojson,
// like every Item). What the claimed rows hold is tested on the stack
// (scripts/scenario.sh), where sam and lea are desk agents; here nobody
// holds a role with a claim.
package main

import (
	"strings"
	"testing"
)

func TestPli_Claimed(t *testing.T) {
	// With a role's queue: the claimed rows, an answer with items (none
	// here: nobody in this test world can claim).
	out := ok(t, "ana", "inbox", word(), "--role", "desk_agent", "--claimed")
	if _, has := out["items"]; has && len(anyList(out["items"])) != 0 {
		t.Errorf("inbox --role desk_agent --claimed: %v; want no Items in this world", out["items"])
	}
	ok(t, "ana", "item", "search", "--waiting-on", "role:desk_agent", "--claimed")
	ok(t, "ana", "item", "count", "--waiting-on", "role:desk_agent", "--claimed")

	// Without one: sent, and refused by the server at filter.claimed.
	for _, args := range [][]string{
		{"inbox", "--claimed"},
		{"item", "search", "--type", "ticket", "--claimed"},
		{"item", "search", "--waiting-on", "me", "--claimed"},
		{"item", "count", "--claimed"},
	} {
		r := fails(t, "ana", "INVALID_ARGUMENT INVALID_REQUEST", args...)
		if !strings.Contains(r.stderr, "filter.claimed") {
			t.Errorf("pli %v: stderr %q; want it to name filter.claimed", args, r.stderr)
		}
	}
}

func TestPli_Claimed_InTheUsage(t *testing.T) {
	r := pli(t, "ana", "help")
	if !strings.Contains(r.stdout+r.stderr, "[--claimed]") {
		t.Errorf("pli help does not list --claimed among the FILTERS")
	}
}
