// Tests for Step 4: a transition's `allowed` list, checked when the Type loads.
package typedef_test

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// A Type whose timer uses a transition that has an `allowed` list. The list
// names the timer, so the Type is valid.
const allowedType = `
id: request
version: 1
initial_state: open
states:
  - {id: open,        category: todo}
  - {id: in_progress, category: active, timer: {after: 48h, to: open}}
  - {id: done,        category: done}
transitions:
  - {from: open,        to: in_progress, allowed: ['role:support']}
  - {from: in_progress, to: open,        allowed: ['role:support', 'principal:system:timer']}
  - {from: in_progress, to: done,        allowed: ['principal:user:owner']}
`

// Entries are role:<name> or principal:<id>, kept as written. They survive
// the JSON form the item service stores.
func TestParse_Allowed(t *testing.T) {
	ty, err := typedef.Parse([]byte(allowedType))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := map[string][]string{
		"open -> in_progress": {"role:support"},
		"in_progress -> open": {"role:support", "principal:system:timer"},
		"in_progress -> done": {"principal:user:owner"},
	}
	check := func(ty *typedef.Type) {
		t.Helper()
		for _, tr := range ty.Transitions {
			name := tr.From + " -> " + tr.To
			if !reflect.DeepEqual(tr.Allowed, want[name]) {
				t.Errorf("%s: allowed = %v, want %v", name, tr.Allowed, want[name])
			}
		}
	}
	check(ty)

	data, err := json.Marshal(ty)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	back, err := typedef.FromJSON(data)
	if err != nil {
		t.Fatalf("FromJSON: %v", err)
	}
	check(back)
}

// An entry that is neither role:<name> nor principal:<id> is refused at load
// time, so a typo cannot silently lock everyone out (or let everyone in).
func TestParse_RejectsBrokenAllowedEntries(t *testing.T) {
	const good = "allowed: ['principal:user:owner']"
	cases := []struct {
		name, allowed string
		wantInError   string
	}{
		{"a bare name", "allowed: [support]", "support"},
		{"an unknown kind", "allowed: ['group:support']", "group:support"},
		{"a role with no name", "allowed: ['role:']", "role:"},
		{"a principal with no ID", "allowed: ['principal:']", "principal:"},
		// Step A: principal entries are typed IDs (user:, agent:, system:).
		{"an untyped principal", "allowed: ['principal:owner']", "principal:owner"},
		{"a principal of unknown type", "allowed: ['principal:robot:7']", "principal:robot:7"},
		{"an empty entry", "allowed: ['']", "allowed"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			broken := strings.Replace(allowedType, good, c.allowed, 1)
			_, err := typedef.Parse([]byte(broken))
			if err == nil {
				t.Fatal("Parse accepted a broken `allowed` entry")
			}
			if !strings.Contains(err.Error(), c.wantInError) || !strings.Contains(err.Error(), "in_progress -> done") {
				t.Errorf("error %q does not mention %q and the transition", err, c.wantInError)
			}
		})
	}
}

// A timer makes its move as system:timer, with no silent exemption. If the
// transition it uses has an `allowed` list, the list must name the timer, or
// the Type is refused at load: otherwise the timer would fire and be forbidden.
func TestParse_TimerMustBeListedInAllowed(t *testing.T) {
	const good = "allowed: ['role:support', 'principal:system:timer']"

	t.Run("listed: accepted", func(t *testing.T) {
		if _, err := typedef.Parse([]byte(allowedType)); err != nil {
			t.Errorf("Parse: %v", err)
		}
	})
	t.Run("not listed: refused", func(t *testing.T) {
		broken := strings.Replace(allowedType, good, "allowed: ['role:support']", 1)
		_, err := typedef.Parse([]byte(broken))
		if err == nil {
			t.Fatal("Parse accepted a timer that is not allowed to make its own move")
		}
		for _, want := range []string{"in_progress", "principal:system:timer"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q does not mention %q", err, want)
			}
		}
	})
	// A role cannot stand in for the timer: it has to be named as a principal.
	t.Run("a role named like the timer: refused", func(t *testing.T) {
		broken := strings.Replace(allowedType, good, "allowed: ['role:system:timer']", 1)
		if _, err := typedef.Parse([]byte(broken)); err == nil {
			t.Error("Parse accepted role:system:timer in place of principal:system:timer")
		}
	})
	t.Run("no list on the timer's transition: accepted", func(t *testing.T) {
		open := strings.Replace(allowedType, ",        "+good, "", 1)
		if open == allowedType {
			t.Fatal("the test did not change the Type")
		}
		if _, err := typedef.Parse([]byte(open)); err != nil {
			t.Errorf("Parse: %v", err)
		}
	})
	// The same check guards the stored form, which is what the item service loads.
	t.Run("not listed, in the JSON form: refused", func(t *testing.T) {
		ty, err := typedef.Parse([]byte(allowedType))
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		data, err := json.Marshal(ty)
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		broken := strings.Replace(string(data), `,"principal:system:timer"`, "", 1)
		if broken == string(data) {
			t.Fatal("the test did not change the JSON")
		}
		if _, err := typedef.FromJSON([]byte(broken)); err == nil {
			t.Error("FromJSON accepted a timer that is not allowed to make its own move")
		}
	})
}

// The ticket Type declares no `allowed` list, so Steps 1 to 3 behave as before.
func TestParse_TicketType_DeclaresNoAllowed(t *testing.T) {
	data, err := os.ReadFile("../../types/ticket.v1.yaml")
	if err != nil {
		t.Fatalf("read ticket Type: %v", err)
	}
	ty, err := typedef.Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	for _, tr := range ty.Transitions {
		if len(tr.Allowed) != 0 {
			t.Errorf("transition %s -> %s: allowed = %v, want none", tr.From, tr.To, tr.Allowed)
		}
	}
}
