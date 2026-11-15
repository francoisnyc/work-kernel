// Tests for Step 6, part 2: the pure rules for hooks and holds. The item
// service, the relay and the workflow all ask these functions; none of them
// decides on its own what a hook or a hold means.
package lifecycle_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/lifecycle"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

const orderType = `
id: order
version: 1
initial_state: new
fields:
  - {id: f_customer, type: string}
  - {id: f_plan,     type: enum, options: [free, pro, unknown]}
  - {id: f_on_hold,  type: bool}
  - {id: f_amount,   type: int}
  - {id: f_ref,      type: string}
  - {id: f_note,     type: string}
states:
  - {id: new,      category: triage}
  - {id: open,     category: todo}
  - {id: approved, category: active}
  - {id: dropped,  category: canceled}
transitions:
  - {from: new, to: open}
  - {from: open, to: new}
  - {from: open, to: approved, guard: 'f_on_hold != true && f_amount > 0'}
  - {from: open, to: dropped, escape: true}
hooks:
  - {id: get_plan, on: enter, state: new, kind: enrich, action: crm.get_plan,
     reads: [f_customer], writes: [f_plan], fallback: {f_plan: unknown}}
  - {id: get_status, on: enter, state: new, kind: enrich, action: erp.get_credit_status,
     reads: [f_customer], writes: [f_on_hold]}
  - {id: check, on: transition, from: open, to: approved, kind: check, action: erp.check_credit,
     reads: [f_customer, f_amount], writes: [f_ref]}
`

func parseOrder(t *testing.T) *typedef.Type {
	t.Helper()
	ty, err := typedef.Parse([]byte(orderType))
	if err != nil {
		t.Fatalf("parse order Type: %v", err)
	}
	return ty
}

// --- what listens, what starts a workflow ---

// A Type with hooks and no timer has listeners: its events must reach the
// workflow that runs the hooks.
func TestHasListeners_Hooks(t *testing.T) {
	if !lifecycle.HasListeners(parseOrder(t)) {
		t.Error("HasListeners = false for a Type with hooks")
	}
	ty, err := typedef.Parse([]byte(`
id: gate
version: 1
initial_state: open
fields:
  - {id: f_customer, type: string}
  - {id: f_amount,   type: int}
  - {id: f_ref,      type: string}
states:
  - {id: open, category: todo}
  - {id: done, category: done}
transitions:
  - {from: open, to: done}
hooks:
  - {id: check, on: transition, from: open, to: done, kind: check, action: erp.check_credit,
     reads: [f_customer, f_amount], writes: [f_ref]}
`))
	if err != nil {
		t.Fatalf("parse a Type with only a check hook: %v", err)
	}
	if !lifecycle.HasListeners(ty) {
		t.Error("HasListeners = false for a Type whose only hook is a check")
	}
}

// Entering a state with enrich hooks needs a workflow, like entering a state
// with a timer.
func TestNeedsWorkflow_EnrichHooks(t *testing.T) {
	ty := parseOrder(t)
	for state, want := range map[string]bool{"new": true, "open": false, "approved": false} {
		if got := lifecycle.NeedsWorkflow(ty, state); got != want {
			t.Errorf("NeedsWorkflow(%q) = %v, want %v", state, got, want)
		}
	}
}

// StartsWorkflow is what the relay asks of each event: entering a state with
// behavior starts one, and so does a transition request, which opens a hold
// without entering any state. Only a transition or a create enters a state.
func TestStartsWorkflow(t *testing.T) {
	ty := parseOrder(t)
	cases := []struct {
		name string
		ev   lifecycle.Event
		want bool
	}{
		{"create into a state with enrich hooks", lifecycle.Event{Op: "create", Entered: "new"}, true},
		{"transition into a state with enrich hooks", lifecycle.Event{Op: "transition", Entered: "new"}, true},
		{"transition into a quiet state", lifecycle.Event{Op: "transition", Entered: "open"}, false},
		{"a transition request opens a hold", lifecycle.Event{Op: lifecycle.OpTransitionRequested}, true},
		{"a denial enters no state", lifecycle.Event{Op: lifecycle.OpTransitionDenied}, false},
		{"a failed check enters no state", lifecycle.Event{Op: lifecycle.OpTransitionFailed}, false},
		{"an update", lifecycle.Event{Op: "update"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := lifecycle.StartsWorkflow(ty, c.ev); got != c.want {
				t.Errorf("StartsWorkflow(%+v) = %v, want %v", c.ev, got, c.want)
			}
		})
	}
}

// --- which hooks run ---

func TestEnrichHooks_InDeclarationOrder(t *testing.T) {
	ty := parseOrder(t)
	got := lifecycle.EnrichHooks(ty, "new")
	if len(got) != 2 || got[0].ID != "get_plan" || got[1].ID != "get_status" {
		t.Errorf("EnrichHooks(new) = %+v, want get_plan then get_status", got)
	}
	if got := lifecycle.EnrichHooks(ty, "open"); len(got) != 0 {
		t.Errorf("EnrichHooks(open) = %+v, want none", got)
	}
}

func TestCheckFor(t *testing.T) {
	ty := parseOrder(t)
	h, ok := lifecycle.CheckFor(ty, "open", "approved")
	if !ok || h.ID != "check" {
		t.Errorf("CheckFor(open, approved) = %+v, %v; want the check hook", h, ok)
	}
	for _, tr := range [][2]string{{"new", "open"}, {"open", "dropped"}, {"open", "limbo"}} {
		if h, ok := lifecycle.CheckFor(ty, tr[0], tr[1]); ok {
			t.Errorf("CheckFor(%s, %s) = %+v; want no check", tr[0], tr[1], h)
		}
	}
}

// --- what a hold allows ---

func assertHeld(t *testing.T, err error, wantInReason ...string) {
	t.Helper()
	var rej *lifecycle.Rejection
	if !errors.As(err, &rej) || rej.Code != lifecycle.Held {
		t.Fatalf("error = %v, want a Rejection with code %q", err, lifecycle.Held)
	}
	for _, want := range wantInReason {
		if !strings.Contains(rej.Reason, want) {
			t.Errorf("reason %q does not mention %q", rej.Reason, want)
		}
	}
}

// While a hold is open, the fields its check reads are frozen: an update that
// touches any of them is refused, naming them. Other fields stay editable.
func TestUpdateDuringHold_FreezesTheCheckedFields(t *testing.T) {
	ty := parseOrder(t)
	h := lifecycle.Hold{Hook: "check", From: "open", To: "approved"}

	if err := lifecycle.UpdateDuringHold(ty, h, []string{"f_note", "f_plan"}); err != nil {
		t.Errorf("update of unfrozen fields: %v", err)
	}
	assertHeld(t, lifecycle.UpdateDuringHold(ty, h, []string{"f_amount"}), "f_amount")
	assertHeld(t, lifecycle.UpdateDuringHold(ty, h, []string{"f_note", "f_customer"}), "f_customer")
}

// While a hold is open, only a declared escape may leave the state.
func TestTransitionDuringHold_OnlyAnEscape(t *testing.T) {
	ty := parseOrder(t)
	h := lifecycle.Hold{Hook: "check", From: "open", To: "approved"}

	if err := lifecycle.TransitionDuringHold(ty, h, "dropped"); err != nil {
		t.Errorf("escape to dropped: %v", err)
	}
	assertHeld(t, lifecycle.TransitionDuringHold(ty, h, "new"))
	// A second request for the held transition itself is refused too.
	assertHeld(t, lifecycle.TransitionDuringHold(ty, h, "approved"))
}

// --- CEL guards, evaluated ---

// Ints read back from JSON arrive as float64; the guard sees them as ints.
// A comparison that cannot be evaluated (an unset field in `>`) fails the
// guard; it is never a different error.
func TestTransition_CELGuard_Evaluation(t *testing.T) {
	ty := parseOrder(t)
	ok := map[string]any{"f_amount": float64(10)}
	if _, err := lifecycle.Transition(ty, "open", ok, "approved"); err != nil {
		t.Errorf("amount 10.0, not on hold: %v", err)
	}
	for name, fields := range map[string]map[string]any{
		"on hold":      {"f_amount": float64(10), "f_on_hold": true},
		"amount zero":  {"f_amount": 0},
		"amount unset": {},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := lifecycle.Transition(ty, "open", fields, "approved")
			var rej *lifecycle.Rejection
			if !errors.As(err, &rej) || rej.Code != lifecycle.GuardFailed {
				t.Errorf("error = %v, want a Rejection with code %q", err, lifecycle.GuardFailed)
			}
		})
	}
}
