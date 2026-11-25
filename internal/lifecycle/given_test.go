// Tests for "required means given by the mover" (track 1, item 3): a
// transition's `given` lists fields of its `required` that the move's own
// patch must set (Input.Given), with a value. A value already on the Item
// does not count. A missing given field is MissingFields, with the missing
// required fields, sorted, in one list; it is judged where required is,
// before the guard. A move with no patch (Input.Given nil: the kernel's
// own moves) never gives anything: it fails closed.
package lifecycle_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/lifecycle"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

const givenType = `
id: given_chore
version: 1
initial_state: s_review
fields:
  - {id: f_amount, type: int}
  - {id: f_reason, type: string}
  - {id: f_note,   type: string}
states:
  - {id: s_review,   category: active}
  - {id: s_rejected, category: canceled, frozen: [f_reason]}
  - {id: s_returned, category: todo}
  - {id: s_parked,   category: backlog}
transitions:
  - {from: s_review, to: s_rejected, required: [f_reason], given: [f_reason]}
  - {from: s_review, to: s_returned, required: [f_amount, f_reason], given: [f_reason], guard: 'f_amount > 0'}
  - {from: s_review, to: s_parked,   required: [f_amount]}
`

func parseGiven(t *testing.T) *typedef.Type {
	t.Helper()
	ty, err := typedef.Parse([]byte(givenType))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return ty
}

// rejection: err is a MissingFields Rejection naming missing, with this
// reason.
func wantMissing(t *testing.T, what string, err error, missing []string, reason string) {
	t.Helper()
	var rej *lifecycle.Rejection
	if !errors.As(err, &rej) || rej.Code != lifecycle.MissingFields {
		t.Errorf("%s: error = %v, want MissingFields", what, err)
		return
	}
	if !slices.Equal(rej.Missing, missing) {
		t.Errorf("%s: Missing = %v, want %v", what, rej.Missing, missing)
	}
	if rej.Reason != reason {
		t.Errorf("%s: reason = %q, want %q", what, rej.Reason, reason)
	}
}

// A value already on the Item does not give it; the move's own patch does.
func TestGiven_OnlyTheMovesPatchGivesIt(t *testing.T) {
	ty := parseGiven(t)
	fields := map[string]any{"f_reason": "planted"}

	_, err := lifecycle.TransitionWith(ty, "s_review", lifecycle.Input{Fields: fields}, "s_rejected")
	wantMissing(t, "a planted reason, no patch", err, []string{"f_reason"},
		`transition from "s_review" to "s_rejected" needs these fields given in the move: f_reason`)

	_, err = lifecycle.TransitionWith(ty, "s_review",
		lifecycle.Input{Fields: fields, Given: map[string]bool{"f_note": true}}, "s_rejected")
	wantMissing(t, "a patch of another field", err, []string{"f_reason"},
		`transition from "s_review" to "s_rejected" needs these fields given in the move: f_reason`)

	res, err := lifecycle.TransitionWith(ty, "s_review",
		lifecycle.Input{Fields: fields, Given: map[string]bool{"f_reason": true}}, "s_rejected")
	if err != nil || res.State != "s_rejected" {
		t.Errorf("f_reason given in the move: %+v, %v; want s_rejected", res, err)
	}
}

// Given needs a value: named in the patch but unset (a null) is not given.
func TestGiven_NeedsAValue(t *testing.T) {
	ty := parseGiven(t)
	_, err := lifecycle.TransitionWith(ty, "s_review",
		lifecycle.Input{Fields: map[string]any{}, Given: map[string]bool{"f_reason": true}}, "s_rejected")
	wantMissing(t, "given but unset", err, []string{"f_reason"},
		`transition from "s_review" to "s_rejected" needs these fields given in the move: f_reason`)
}

// Both kinds missing: one list, sorted, and a reason that says which is
// which. Only required fields missing: today's reason, word for word.
func TestGiven_TheRefusalNamesBothKinds(t *testing.T) {
	ty := parseGiven(t)
	_, err := lifecycle.TransitionWith(ty, "s_review", lifecycle.Input{Fields: map[string]any{}}, "s_returned")
	wantMissing(t, "neither", err, []string{"f_amount", "f_reason"},
		`transition from "s_review" to "s_returned" needs these fields set: f_amount; and given in the move: f_reason`)

	_, err = lifecycle.TransitionWith(ty, "s_review", lifecycle.Input{Fields: map[string]any{}}, "s_parked")
	wantMissing(t, "required only", err, []string{"f_amount"},
		`transition from "s_review" to "s_parked" needs these fields set: f_amount`)
}

// Judged where required is: before the guard.
func TestGiven_BeforeTheGuard(t *testing.T) {
	ty := parseGiven(t)
	_, err := lifecycle.TransitionWith(ty, "s_review",
		lifecycle.Input{Fields: map[string]any{"f_amount": -1, "f_reason": "planted"}}, "s_returned")
	wantMissing(t, "the guard fails and f_reason is not given", err, []string{"f_reason"},
		`transition from "s_review" to "s_returned" needs these fields given in the move: f_reason`)

	_, err = lifecycle.TransitionWith(ty, "s_review",
		lifecycle.Input{Fields: map[string]any{"f_amount": -1, "f_reason": "mine"}, Given: map[string]bool{"f_reason": true}}, "s_returned")
	var rej *lifecycle.Rejection
	if !errors.As(err, &rej) || rej.Code != lifecycle.GuardFailed {
		t.Errorf("given, the guard fails: %v, want GuardFailed", err)
	}
}

// The kernel's own moves carry no patch: through Transition (no Input) a
// given field is never given, whatever the Item holds. Fails closed.
func TestGiven_AMoveWithNoPatchFailsClosed(t *testing.T) {
	ty := parseGiven(t)
	_, err := lifecycle.Transition(ty, "s_review", map[string]any{"f_reason": "planted"}, "s_rejected")
	wantMissing(t, "a move with no patch", err, []string{"f_reason"},
		`transition from "s_review" to "s_rejected" needs these fields given in the move: f_reason`)
	if err != nil && !strings.Contains(err.Error(), "given in the move") {
		t.Errorf("error = %q, want it to say given in the move", err)
	}
}
