// Tests for Step 5: HasListeners, the one rule for "does anything listen to
// this Type's events?". When nothing does, the item service writes no outbox row.
package lifecycle_test

import (
	"testing"

	"github.com/francoisnyc/work-kernel/internal/lifecycle"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

func TestHasListeners(t *testing.T) {
	cases := []struct {
		name string
		def  string
		want bool
	}{
		{"no states at all", "id: message\nversion: 1\n", false},
		{"states and transitions, no timer", `
id: note
version: 1
initial_state: draft
states:
  - {id: draft, category: backlog}
  - {id: done, category: done}
transitions:
  - {from: draft, to: done}
`, false},
		// One timer anywhere is enough: every event of the Type may reach a
		// running workflow, not only the events that enter the timer's state.
		{"one timer, on a state that is not the initial one", `
id: doc
version: 1
initial_state: draft
states:
  - {id: draft, category: backlog}
  - {id: review, category: active, timer: {after: 4h, to: draft}}
transitions:
  - {from: draft, to: review}
  - {from: review, to: draft}
`, true},
		{"a timer on the initial state", `
id: ping
version: 1
initial_state: waiting
states:
  - {id: waiting, category: waiting, timer: {after: 1m, to: done}}
  - {id: done, category: done}
transitions:
  - {from: waiting, to: done}
`, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ty, err := typedef.Parse([]byte(c.def))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if got := lifecycle.HasListeners(ty); got != c.want {
				t.Errorf("HasListeners = %v, want %v", got, c.want)
			}
		})
	}
}

// HasListeners and NeedsWorkflow agree: a Type has listeners exactly when
// one of its states needs a workflow. If a later rule widens one, this test
// shows whether the other must follow.
func TestHasListeners_AgreesWithNeedsWorkflow(t *testing.T) {
	ty := parseTimed(t)
	any := false
	for _, s := range ty.States {
		any = any || lifecycle.NeedsWorkflow(ty, s.ID)
	}
	if got := lifecycle.HasListeners(ty); got != any {
		t.Errorf("HasListeners = %v, but some state needs a workflow: %v", got, any)
	}
}
