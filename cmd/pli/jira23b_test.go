// Tests for Jira part 2, step 3b, through pli. No new command: an incident,
// its desk and its goals are Items like any other. One new flag:
//
//	--not-empty FIELD   Items where the field has a value (repeatable)
//
// which the API has had since step 3a (FieldFilter.not_empty) and the
// "ever breached" search needs. The goals themselves need the worker: the
// scenario runs them.
package main

import (
	"slices"
	"testing"
)

// The Types show what the UI and a script need: the incident's offer, the
// desk's role-written links.
func TestPli_Types_ShowOffersAndLinkRoles(t *testing.T) {
	inc := ok(t, "ana", "type", "get", "incident@1")["type"].(map[string]any)
	offers, _ := inc["offers"].([]any)
	if len(offers) != 1 {
		t.Fatalf("incident@1 offers = %v, want one", inc["offers"])
	}
	if o := offers[0].(map[string]any); o["name"] != "breach_contact" || o["field"] != "f_assignee" {
		t.Errorf("the offer = %v, want breach_contact: f_assignee", o)
	}
	desk := ok(t, "ana", "type", "get", "desk@1")["type"].(map[string]any)
	links, _ := desk["link_types"].([]any)
	if len(links) != 2 {
		t.Fatalf("desk@1 link types = %v, want two", desk["link_types"])
	}
	for _, l := range links {
		roles, _ := l.(map[string]any)["writable_by_roles"].([]any)
		if len(roles) != 1 || roles[0] != "sla_admin" {
			t.Errorf("desk link %v: writable_by_roles = %v, want [sla_admin]", l.(map[string]any)["name"], roles)
		}
	}
}

// Without the role, pli item link on a desk's calendar is refused, with
// its code and reason like every refusal.
func TestPli_Link_RoleWrittenLinkRefused(t *testing.T) {
	desk := create(t, "ana", "--type", "desk@1", "--set", "f_key=IT", "--set", "f_name=IT "+word())
	cal := create(t, "ana", "--type", "calendar@1", "--set", "f_name=office", "--set", `f_week=["mon 09:00-17:00"]`, "--set", "f_zone=Europe/Paris")
	fails(t, "ana", "PERMISSION_DENIED LINK_NOT_WRITABLE", "item", "link", desk, "calendar", cal)
	// A calendar that is none cannot exist.
	fails(t, "ana", "FAILED_PRECONDITION INVARIANT_FAILED", "item", "create", "--type", "calendar@1",
		"--set", "f_name=broken", "--set", `f_week=["mon 25:00-17:00"]`, "--set", "f_zone=Europe/Paris")
}

// --not-empty: the field has a value. Beside --empty, its opposite.
func TestPli_Search_NotEmpty(t *testing.T) {
	w := word()
	// An incident is created only inside a desk (incident@4's start block).
	desk := create(t, "ana", "--type", "desk@1", "--set", "f_key=IT", "--set", "f_name=IT "+w)
	with := create(t, "ana", "--type", "incident@1", "--in", desk, "--set", "f_title=assigned "+w, "--set", "f_priority=P2", "--set", "f_assignee=user:bob")
	without := create(t, "ana", "--type", "incident@1", "--in", desk, "--set", "f_title=unassigned "+w, "--set", "f_priority=P2")
	if got := namesOf(t, ok(t, "ana", "item", "search", w, "--type", "incident", "--not-empty", "f_assignee"), "items"); !slices.Equal(got, []string{with}) {
		t.Errorf("--not-empty f_assignee = %v, want [%s]", got, with)
	}
	if got := namesOf(t, ok(t, "ana", "item", "search", w, "--type", "incident", "--empty", "f_assignee"), "items"); !slices.Equal(got, []string{without}) {
		t.Errorf("--empty f_assignee = %v, want [%s]", got, without)
	}
	// Both of one field contradict each other: the server says so.
	fails(t, "ana", "INVALID_ARGUMENT INVALID_REQUEST", "item", "search", w, "--empty", "f_assignee", "--not-empty", "f_assignee")
	if r := pli(t, "ana", "item", "search", w, "--not-empty"); r.code != 2 {
		t.Errorf("--not-empty without a field: exit %d, want a usage error", r.code)
	}
	// The queue: incidents by their resolution due instant. Neither has a
	// goal here (no worker), so neither has one: the order is refused only
	// if the field were unknown.
	ok(t, "ana", "item", "search", w, "--type", "incident", "--order-by", "f_due_resolution")
	// Priority is required at creation.
	fails(t, "ana", "FAILED_PRECONDITION INVARIANT_FAILED", "item", "create", "--type", "incident@1", "--in", desk, "--set", "f_title=no priority "+w)
}
