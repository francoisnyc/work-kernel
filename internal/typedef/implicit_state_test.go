// Tests for the PR #5 follow-up: a Type without a lifecycle has one implicit
// state (docs/design.md, "Kernel rules for size").
package typedef_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// A chat message: fields only, no lifecycle.
const messageType = `
id: message
version: 1
fields:
  - {id: f_body, type: string}
`

// A Type that declares no states parses, and gets exactly one state.
func TestParse_TypeWithoutStates_GetsOneImplicitState(t *testing.T) {
	ty, err := typedef.Parse([]byte(messageType))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(ty.States) != 1 {
		t.Fatalf("states = %+v, want exactly one implicit state", ty.States)
	}
	s := ty.States[0]
	if s.ID != "default" || s.Category != "none" {
		t.Errorf("implicit state = %+v, want id default, category none", s)
	}
	if ty.InitialState != "default" {
		t.Errorf("initial_state = %q, want default", ty.InitialState)
	}
	if len(ty.Transitions) != 0 {
		t.Errorf("transitions = %+v, want none", ty.Transitions)
	}
}

// The kernel stores a Type as JSON; the implicit state survives the round trip
// and is not added twice.
func TestFromJSON_TypeWithoutStates_RoundTrips(t *testing.T) {
	ty, err := typedef.Parse([]byte(messageType))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	stored, err := json.Marshal(ty)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	back, err := typedef.FromJSON(stored)
	if err != nil {
		t.Fatalf("FromJSON: %v", err)
	}
	if len(back.States) != 1 || back.States[0].ID != "default" || back.InitialState != "default" {
		t.Errorf("after the round trip: states %+v, initial_state %q; want one state, default",
			back.States, back.InitialState)
	}
}

// Without states there is no lifecycle to describe: naming an initial state or
// a transition is a mistake, caught at load time.
func TestParse_TypeWithoutStates_RejectsLifecycleKeys(t *testing.T) {
	cases := []struct {
		name, extra string
		wantInError string
	}{
		{"initial_state", "initial_state: open", "initial_state"},
		{"transitions", "transitions:\n  - {from: default, to: default}", "transition"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := typedef.Parse([]byte(messageType + c.extra + "\n"))
			if err == nil {
				t.Fatal("Parse accepted lifecycle keys on a Type with no states")
			}
			if !strings.Contains(err.Error(), c.wantInError) {
				t.Errorf("error %q does not mention %q", err, c.wantInError)
			}
		})
	}
}
