// Tests for the two pure functions the relay and the workflow share:
// TimerFor and NeedsWorkflow.
package lifecycle_test

import (
	"testing"
	"time"

	"github.com/francoisnyc/work-kernel/internal/lifecycle"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

func parseTimed(t *testing.T) *typedef.Type {
	t.Helper()
	ty, err := typedef.Parse([]byte(`
id: doc
version: 1
initial_state: draft
states:
  - {id: draft, category: backlog}
  - {id: review, category: active, timer: {after: 4h, to: draft}}
  - {id: published, category: done}
transitions:
  - {from: draft, to: review}
  - {from: review, to: draft}
  - {from: review, to: published}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return ty
}

// TimerFor returns the timer of a state that has one, and reports "none"
// for a state without one and for a state the Type does not declare.
func TestTimerFor(t *testing.T) {
	ty := parseTimed(t)

	timer, ok := lifecycle.TimerFor(ty, "review")
	if !ok || timer.Duration != 4*time.Hour || timer.To != "draft" {
		t.Errorf("TimerFor(review) = %+v, %v; want 4h to draft, true", timer, ok)
	}
	for _, state := range []string{"draft", "published", "limbo", ""} {
		if timer, ok := lifecycle.TimerFor(ty, state); ok {
			t.Errorf("TimerFor(%q) = %+v, true; want no timer", state, timer)
		}
	}
}

// NeedsWorkflow is the one rule for "does entering this state need a
// workflow?". Today: the state has a timer.
func TestNeedsWorkflow(t *testing.T) {
	ty := parseTimed(t)
	cases := map[string]bool{"review": true, "draft": false, "published": false, "limbo": false, "": false}
	for state, want := range cases {
		if got := lifecycle.NeedsWorkflow(ty, state); got != want {
			t.Errorf("NeedsWorkflow(%q) = %v, want %v", state, got, want)
		}
	}
}

// A Type without a lifecycle never needs a workflow.
func TestNeedsWorkflow_TypeWithoutStates(t *testing.T) {
	ty, err := typedef.Parse([]byte("id: message\nversion: 1\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if lifecycle.NeedsWorkflow(ty, typedef.ImplicitState) {
		t.Error("NeedsWorkflow is true for the implicit state")
	}
}
