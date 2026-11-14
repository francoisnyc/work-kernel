// Tests for loading a Type from YAML. No database is involved.
package typedef_test

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// The spike's ticket Type parses into fields, states with categories, and transitions.
func TestParse_TicketType(t *testing.T) {
	data, err := os.ReadFile("../../types/ticket.v1.yaml")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	ty, err := typedef.Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if ty.ID != "ticket" || ty.Version != 1 || ty.InitialState != "open" {
		t.Errorf("identity = (%q, %d, %q), want (ticket, 1, open)", ty.ID, ty.Version, ty.InitialState)
	}

	// The second return value says whether the lookup found something,
	// Go's usual alternative to returning null.
	f, ok := ty.Field("f_resolution")
	if !ok {
		t.Fatal("field f_resolution not found")
	}
	if f.Type != typedef.Enum || !reflect.DeepEqual(f.Options, []string{"fixed", "wontfix", "duplicate"}) {
		t.Errorf("f_resolution = %+v, want an enum with three options", f)
	}
	if f, ok := ty.Field("f_priority"); !ok || f.Type != typedef.Int {
		t.Errorf("f_priority = %+v (found %v), want an int field", f, ok)
	}
	if _, ok := ty.Field("f_nope"); ok {
		t.Error("unknown field was found")
	}

	wantCategories := map[string]string{
		"open": "todo", "in_progress": "active", "resolved": "done", "canceled": "canceled",
	}
	for id, want := range wantCategories {
		s, ok := ty.State(id)
		if !ok || s.Category != want {
			t.Errorf("state %s: category = %q (found %v), want %q", id, s.Category, ok, want)
		}
	}

	tr, ok := ty.Transition("open", "in_progress")
	if !ok {
		t.Fatal("transition open -> in_progress not found")
	}
	if tr.Guard != "f_assignee != null" {
		t.Errorf("guard = %q, want %q", tr.Guard, "f_assignee != null")
	}
	tr, ok = ty.Transition("in_progress", "resolved")
	if !ok || !reflect.DeepEqual(tr.Required, []string{"f_resolution"}) {
		t.Errorf("in_progress -> resolved = %+v (found %v), want required [f_resolution]", tr, ok)
	}
	if _, ok := ty.Transition("open", "resolved"); ok {
		t.Error("undeclared transition open -> resolved was found")
	}
}

// A broken Type is refused at load time, with a message that names the problem.
func TestParse_RejectsBrokenTypes(t *testing.T) {
	// A minimal valid Type; each case replaces one fragment to break it.
	const valid = `
id: doc
version: 1
initial_state: draft
fields:
  - {id: f_owner, type: string}
  - {id: f_kind, type: enum, options: [memo, report]}
states:
  - {id: draft, category: backlog}
  - {id: published, category: done}
transitions:
  - {from: draft, to: published, guard: 'f_owner != null', required: [f_owner]}
`
	if _, err := typedef.Parse([]byte(valid)); err != nil {
		t.Fatalf("the base Type should be valid: %v", err)
	}

	cases := []struct {
		name, old, new string
		wantInError    string
	}{
		{"missing id", "id: doc", "", "id"},
		{"missing version", "version: 1", "", "version"},
		{"unknown key", "version: 1", "version: 1\ncolour: blue", "colour"},
		{"unknown initial state", "initial_state: draft", "initial_state: limbo", "limbo"},
		{"unknown field type", "type: string", "type: money", "money"},
		{"duplicate field", "{id: f_kind, type: enum, options: [memo, report]}", "{id: f_owner, type: string}", "f_owner"},
		{"enum without options", ", options: [memo, report]", "", "f_kind"},
		{"unknown category", "category: backlog", "category: parked", "parked"},
		{"duplicate state", "{id: published, category: done}", "{id: draft, category: done}", "draft"},
		{"transition to unknown state", "to: published", "to: limbo", "limbo"},
		{"transition from unknown state", "from: draft", "from: limbo", "limbo"},
		{"required unknown field", "required: [f_owner]", "required: [f_ghost]", "f_ghost"},
		{"guard on unknown field", "f_owner != null", "f_ghost != null", "f_ghost"},
		{"guard syntax", "f_owner != null", "f_owner >>> 3", "guard"},
		{"guard literal of the wrong type", "f_owner != null", "f_owner == 3", "f_owner"},
		{"duplicate transition",
			"transitions:", "transitions:\n  - {from: draft, to: published}", "published"},
	}
	for _, c := range cases {
		// t.Run makes each case a named subtest, like a JUnit parameterized test.
		t.Run(c.name, func(t *testing.T) {
			if !strings.Contains(valid, c.old) {
				t.Fatalf("test bug: %q is not in the base Type", c.old)
			}
			broken := strings.Replace(valid, c.old, c.new, 1)
			_, err := typedef.Parse([]byte(broken))
			if err == nil {
				t.Fatal("Parse accepted a broken Type")
			}
			if !strings.Contains(err.Error(), c.wantInError) {
				t.Errorf("error %q does not mention %q", err, c.wantInError)
			}
		})
	}
}
