// Tests for Step 7: the pure rules for action hooks (a side effect on
// entering a state) and the hook_failed event.
package lifecycle_test

import (
	"testing"

	"github.com/francoisnyc/work-kernel/internal/lifecycle"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

const invoicedType = `
id: order
version: 1
initial_state: new
fields:
  - {id: f_customer, type: string}
  - {id: f_amount,   type: int}
  - {id: f_plan,     type: enum, options: [free, pro, unknown]}
  - {id: f_invoice,  type: string}
  - {id: f_receipt,  type: string}
states:
  - {id: new,      category: triage}
  - {id: approved, category: active}
  - {id: done,     category: done}
transitions:
  - {from: new, to: approved}
  - {from: approved, to: done}
hooks:
  - {id: get_plan, on: enter, state: new, kind: enrich, action: crm.get_plan,
     reads: [f_customer], writes: [f_plan]}
  - {id: invoice, on: enter, state: approved, kind: action, action: erp.create_invoice,
     reads: [f_customer, f_amount], writes: [f_invoice]}
  - {id: receipt, on: enter, state: approved, kind: action, action: erp.create_invoice,
     reads: [f_customer, f_amount], writes: [f_receipt]}
`

func parseInvoiced(t *testing.T) *typedef.Type {
	t.Helper()
	ty, err := typedef.Parse([]byte(invoicedType))
	if err != nil {
		t.Fatalf("parse invoiced Type: %v", err)
	}
	return ty
}

// ActionHooks returns a state's action hooks in declaration order, and
// EnrichHooks no longer includes them: the workflow treats the two kinds
// differently (an action is never dropped when its state is left).
func TestActionHooks_AndEnrichHooks_AreSeparate(t *testing.T) {
	ty := parseInvoiced(t)
	got := lifecycle.ActionHooks(ty, "approved")
	if len(got) != 2 || got[0].ID != "invoice" || got[1].ID != "receipt" {
		t.Errorf("ActionHooks(approved) = %+v, want invoice then receipt", got)
	}
	if got := lifecycle.EnrichHooks(ty, "approved"); len(got) != 0 {
		t.Errorf("EnrichHooks(approved) = %+v, want none: those are actions", got)
	}
	if got := lifecycle.ActionHooks(ty, "new"); len(got) != 0 {
		t.Errorf("ActionHooks(new) = %+v, want none", got)
	}
	if got := lifecycle.EnrichHooks(ty, "new"); len(got) != 1 || got[0].ID != "get_plan" {
		t.Errorf("EnrichHooks(new) = %+v, want get_plan", got)
	}
}

// Entering a state with an action hook needs the workflow, and its events
// need an outbox row.
func TestNeedsWorkflow_ActionHooks(t *testing.T) {
	ty := parseInvoiced(t)
	for state, want := range map[string]bool{"new": true, "approved": true, "done": false} {
		if got := lifecycle.NeedsWorkflow(ty, state); got != want {
			t.Errorf("NeedsWorkflow(%q) = %v, want %v", state, got, want)
		}
	}
	if !lifecycle.StartsWorkflow(ty, lifecycle.Event{Op: "transition", Entered: "approved"}) {
		t.Error("StartsWorkflow(transition into approved) = false, want true")
	}
	if !lifecycle.HasListeners(ty) {
		t.Error("HasListeners = false for a Type with action hooks")
	}
}

// hook_failed records that an action hook gave up. It enters no state and
// starts nothing.
func TestHookFailed_StartsNothing(t *testing.T) {
	if lifecycle.OpHookFailed != "hook_failed" {
		t.Errorf("OpHookFailed = %q, want hook_failed", lifecycle.OpHookFailed)
	}
	if lifecycle.StartsWorkflow(parseInvoiced(t), lifecycle.Event{Op: lifecycle.OpHookFailed}) {
		t.Error("StartsWorkflow(hook_failed) = true, want false")
	}
}
