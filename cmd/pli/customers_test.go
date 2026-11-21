// Tests for Jira part 2, step 4a, through pli: one new flag,
//
//	item create --requester customer:<id>   open an Item for a customer
//
// which sends CreateItemRequest.requester. Everything else a customer meets
// is what the API refuses: pli shows the refusal's code and reason, as for
// every refusal.
package main

import "testing"

func TestPli_Create_Requester(t *testing.T) {
	desk := create(t, "ana", "--type", "desk@2", "--set", "f_key=IT", "--set", "f_name=portal "+word())
	got := itemOf(t, ok(t, "ana", "item", "create", "--type", "incident@2", "--in", desk, "--requester", "customer:kim",
		"--set", "f_title=phoned in "+word(), "--set", "f_priority=P2"))
	if got["creator"] != "user:ana" || got["creator_on_behalf_of"] != "customer:kim" {
		t.Errorf("created %v; want creator user:ana for customer:kim", got)
	}
	// A requester the server refuses: INVALID_ARGUMENT, as the server says.
	fails(t, "ana", "INVALID_ARGUMENT INVALID_REQUEST", "item", "create", "--type", "incident@2", "--in", desk,
		"--requester", "user:bob", "--set", "f_title=x", "--set", "f_priority=P2")
	// The flag needs a value.
	if r := pli(t, "ana", "item", "create", "--type", "incident@2", "--in", desk, "--requester"); r.code != 2 {
		t.Errorf("--requester without a value: exit %d, want a usage error", r.code)
	}
}
