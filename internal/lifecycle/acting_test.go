// Tests for proof-of-concept Step 2, part 3: the pure rules of acting
// timers and reaction hooks.
//
//   - ActingTimers lists a state's acting timers; a state with one has
//     behavior (NeedsWorkflow), so entering it starts the workflow.
//   - ReactionHooks lists the action hooks a reaction_applied event of an
//     undo runs; that event starts the workflow when there are any, since
//     the run that saw the withdrawal may have completed.
package lifecycle_test

import (
	"testing"

	"github.com/francoisnyc/work-kernel/internal/lifecycle"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

func vacationV3(t *testing.T) *typedef.Type {
	t.Helper()
	types, err := typedef.ParseDir("../../types")
	if err != nil {
		t.Fatalf("ParseDir: %v", err)
	}
	for _, ty := range types {
		if ty.ID == "vacation_request" && ty.Version == 3 {
			return ty
		}
	}
	t.Fatal("types/ has no vacation_request v3")
	return nil
}

func TestActingTimers(t *testing.T) {
	v3 := vacationV3(t)
	got := lifecycle.ActingTimers(v3, "submitted")
	if len(got) != 1 || got[0].ID != "remind" || got[0].Hook != "remind_approver" {
		t.Errorf("ActingTimers(submitted) = %+v, want remind", got)
	}
	if got := lifecycle.ActingTimers(v3, "approved"); len(got) != 0 {
		t.Errorf("ActingTimers(approved) = %+v, want none", got)
	}
	// An acting timer alone is behavior: a state with one and nothing else.
	ty := parseYAML(t, `
id: note
version: 1
initial_state: draft
fields:
  - {id: f_owner, type: principal}
states:
  - {id: draft, category: todo}
  - id: review
    category: active
    acting:
      - {id: nudge, after: 1h, max: 1, hook: nudge}
transitions:
  - {from: draft, to: review}
hooks:
  - {id: nudge, on: timer, kind: action, action: email.notify, reads: [f_owner]}
`)
	if !lifecycle.NeedsWorkflow(ty, "review") {
		t.Error("NeedsWorkflow(review) = false, want true: it has an acting timer")
	}
	if lifecycle.NeedsWorkflow(ty, "draft") {
		t.Error("NeedsWorkflow(draft) = true, want false")
	}
	if !lifecycle.StartsWorkflow(ty, lifecycle.Event{Op: "transition", From: "draft", Entered: "review"}) {
		t.Error("entering review does not start the workflow")
	}
}

func TestReactionHooks(t *testing.T) {
	v3 := vacationV3(t)
	got := lifecycle.ReactionHooks(v3, "refund")
	if len(got) != 1 || got[0].ID != "refund_email" {
		t.Errorf("ReactionHooks(refund) = %+v, want refund_email", got)
	}
	if got := lifecycle.ReactionHooks(v3, "deduct"); len(got) != 0 {
		t.Errorf("ReactionHooks(deduct) = %+v, want none", got)
	}
	// The applied event starts the workflow: the request is withdrawn, a
	// final state with no timer, and the email must still be sent.
	if !lifecycle.StartsWorkflow(v3, lifecycle.Event{Op: lifecycle.OpReactionApplied}) {
		t.Error("reaction_applied does not start the workflow")
	}
	// v2 has no reaction hooks: its applied events need no workflow.
	types, _ := typedef.ParseDir("../../types")
	for _, ty := range types {
		if ty.ID == "vacation_request" && ty.Version == 2 &&
			lifecycle.StartsWorkflow(ty, lifecycle.Event{Op: lifecycle.OpReactionApplied}) {
			t.Error("reaction_applied of v2 (no reaction hooks) starts the workflow")
		}
	}
}
