// Tests for Step 3, commit 2: a state may carry one timer. This is a spike
// stand-in; real SLAs become standalone objects later.
package typedef_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/francoisnyc/work-kernel/internal/typedef"
)

const timedType = `
id: doc
version: 1
initial_state: draft
fields:
  - {id: f_owner, type: string}
states:
  - {id: draft, category: backlog}
  - {id: review, category: active, timer: {after: 4h, to: draft}}
  - {id: published, category: done}
transitions:
  - {from: draft, to: review}
  - {from: review, to: draft}
  - {from: review, to: published, guard: 'f_owner is set'}
`

// A state's timer parses into a duration and a target state; other states have none.
func TestParse_StateTimer(t *testing.T) {
	ty, err := typedef.Parse([]byte(timedType))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	review, _ := ty.State("review")
	// Timer is a pointer: nil means "this state has no timer".
	if review.Timer == nil {
		t.Fatal("state review has no timer")
	}
	if review.Timer.Duration != 4*time.Hour || review.Timer.To != "draft" {
		t.Errorf("timer = %+v, want 4h to draft", *review.Timer)
	}
	for _, id := range []string{"draft", "published"} {
		if s, _ := ty.State(id); s.Timer != nil {
			t.Errorf("state %s has a timer: %+v", id, *s.Timer)
		}
	}
}

// The timer survives the JSON form the kernel stores in Postgres.
func TestFromJSON_StateTimer_RoundTrips(t *testing.T) {
	ty, err := typedef.Parse([]byte(timedType))
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
	review, _ := back.State("review")
	if review.Timer == nil || review.Timer.Duration != 4*time.Hour || review.Timer.To != "draft" {
		t.Errorf("timer after the round trip = %+v, want 4h to draft", review.Timer)
	}
}

// A timer may only make a move the Type declares from that state, so it can
// never do what a person could not. Broken timers are refused at load time.
func TestParse_RejectsBrokenTimers(t *testing.T) {
	const good = "timer: {after: 4h, to: draft}"
	cases := []struct {
		name, timer string
		wantInError string
	}{
		{"no transition to the target", "timer: {after: 4h, to: review}", "review"},
		{"target is not a state", "timer: {after: 4h, to: limbo}", "limbo"},
		{"no target", "timer: {after: 4h}", "to"},
		{"duration is not a duration", "timer: {after: soon, to: draft}", "soon"},
		{"no duration", "timer: {to: draft}", "after"},
		{"zero duration", "timer: {after: 0s, to: draft}", "after"},
		{"negative duration", "timer: {after: -1h, to: draft}", "after"},
		{"unknown key", "timer: {after: 4h, to: draft, repeat: true}", "repeat"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			broken := strings.Replace(timedType, good, c.timer, 1)
			_, err := typedef.Parse([]byte(broken))
			if err == nil {
				t.Fatal("Parse accepted a broken timer")
			}
			if !strings.Contains(err.Error(), c.wantInError) {
				t.Errorf("error %q does not mention %q", err, c.wantInError)
			}
		})
	}
}

// The spike's ticket: work left in_progress for 48 hours goes back to open.
func TestParse_TicketType_HasOneTimer(t *testing.T) {
	data, err := os.ReadFile("../../types/ticket.v1.yaml")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	ty, err := typedef.Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	for _, s := range ty.States {
		switch {
		case s.ID == "in_progress":
			if s.Timer == nil || s.Timer.Duration != 48*time.Hour || s.Timer.To != "open" {
				t.Errorf("in_progress timer = %+v, want 48h to open", s.Timer)
			}
		case s.Timer != nil:
			t.Errorf("state %s has a timer: %+v", s.ID, *s.Timer)
		}
	}
}
