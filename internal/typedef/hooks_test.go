// Tests for Step 6, part 2: hooks declared in a Type version. A hook names
// code (its action) but never contains it. Two kinds:
//
//   - enrich, on entering a state: reads some fields, calls out, writes
//     others. It never holds the Item. An optional fallback is written when
//     its budget runs out.
//   - check, on a transition: turns a request for that transition into a
//     hold. The hook allows, denies, corrects (allows with a field patch) or
//     errors. The fields it reads are frozen while the hold is open.
//
// A transition may be declared an escape: it may leave a held state, and it
// cancels the hold.
package typedef_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/typedef"
)

const hookedType = `
id: order
version: 1
initial_state: new
fields:
  - {id: f_customer, type: string}
  - {id: f_plan,     type: enum, options: [free, pro, enterprise, unknown]}
  - {id: f_on_hold,  type: bool}
  - {id: f_amount,   type: int}
  - {id: f_ref,      type: string}
states:
  - {id: new,      category: triage}
  - {id: open,     category: todo}
  - {id: approved, category: active}
  - {id: dropped,  category: canceled}
transitions:
  - {from: new, to: open}
  - {from: open, to: approved, guard: 'f_on_hold != true', allowed: ['role:approver']}
  - {from: open, to: dropped, escape: true}
hooks:
  - {id: get_plan, on: enter, state: new, kind: enrich, action: crm.get_plan,
     reads: [f_customer], writes: [f_plan], fallback: {f_plan: unknown}}
  - {id: get_status, on: enter, state: new, kind: enrich, action: erp.get_credit_status,
     reads: [f_customer], writes: [f_on_hold]}
  - {id: check, on: transition, from: open, to: approved, kind: check, action: erp.check_credit,
     reads: [f_customer, f_amount], writes: [f_ref]}
`

func TestParse_Hooks(t *testing.T) {
	ty, err := typedef.Parse([]byte(hookedType))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(ty.Hooks) != 3 {
		t.Fatalf("hooks = %+v, want three", ty.Hooks)
	}
	plan, ok := ty.Hook("get_plan")
	if !ok {
		t.Fatal("hook get_plan not found")
	}
	want := typedef.Hook{
		ID: "get_plan", On: typedef.OnEnter, State: "new", Kind: typedef.HookEnrich, Action: "crm.get_plan",
		Reads: []string{"f_customer"}, Writes: []string{"f_plan"}, Fallback: map[string]any{"f_plan": "unknown"},
	}
	if !reflect.DeepEqual(plan, want) {
		t.Errorf("get_plan = %+v\nwant %+v", plan, want)
	}
	check, _ := ty.Hook("check")
	if check.On != typedef.OnTransition || check.From != "open" || check.To != "approved" ||
		check.Kind != typedef.HookCheck || !reflect.DeepEqual(check.Reads, []string{"f_customer", "f_amount"}) {
		t.Errorf("check = %+v, want a check on open -> approved reading f_customer and f_amount", check)
	}
	if _, ok := ty.Hook("nope"); ok {
		t.Error("an undeclared hook was found")
	}
	drop, _ := ty.Transition("open", "dropped")
	if !drop.Escape {
		t.Error("open -> dropped is declared an escape, but Escape is false")
	}
	if tr, _ := ty.Transition("new", "open"); tr.Escape {
		t.Error("new -> open is not an escape, but Escape is true")
	}
}

// roundTrip stores a Type the way the item service does (JSON) and reads it back.
func roundTrip(t *testing.T, ty *typedef.Type) *typedef.Type {
	t.Helper()
	data, err := json.Marshal(ty)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	back, err := typedef.FromJSON(data)
	if err != nil {
		t.Fatalf("FromJSON: %v", err)
	}
	return back
}

// Hooks are part of the stored form, like the rest of the Type.
func TestFromJSON_KeepsHooks(t *testing.T) {
	ty, err := typedef.Parse([]byte(hookedType))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	back := roundTrip(t, ty)
	if !reflect.DeepEqual(back.Hooks, ty.Hooks) {
		t.Errorf("hooks after a JSON round trip = %+v\nwant %+v", back.Hooks, ty.Hooks)
	}
	if tr, _ := back.Transition("open", "dropped"); !tr.Escape {
		t.Error("escape lost in the JSON round trip")
	}
}

// A broken hook declaration refuses the Type at load, naming the problem.
func TestParse_RejectsBrokenHooks(t *testing.T) {
	if _, err := typedef.Parse([]byte(hookedType)); err != nil {
		t.Fatalf("the base Type should be valid: %v", err)
	}
	cases := []struct {
		name, old, new, wantInError string
	}{
		{"duplicate hook id", "id: get_status,", "id: get_plan,", "get_plan"},
		{"hook id that cannot be part of a principal", "id: get_status,", "id: 'get:status',", "get:status"},
		{"unknown attach point", "on: enter, state: new, kind: enrich, action: crm.get_plan",
			"on: exit, state: new, kind: enrich, action: crm.get_plan", "exit"},
		{"enter hook on an unknown state", "state: new, kind: enrich, action: crm.get_plan",
			"state: limbo, kind: enrich, action: crm.get_plan", "limbo"},
		{"check hook on an undeclared transition", "from: open, to: approved, kind: check",
			"from: new, to: approved, kind: check", "new -> approved"},
		{"a check on entering a state", "on: enter, state: new, kind: enrich, action: crm.get_plan",
			"on: enter, state: new, kind: check, action: crm.get_plan", "check"},
		{"an enrich hook on a transition", "from: open, to: approved, kind: check",
			"from: open, to: approved, kind: enrich", "enrich"},
		{"unknown kind", "kind: enrich, action: crm.get_plan", "kind: poke, action: crm.get_plan", "poke"},
		{"no action", "action: erp.get_credit_status,", "action: '',", "action"},
		// An action is code: the Type may only name one that exists, and must
		// give it as many reads and writes as it takes and returns.
		{"unknown action", "action: erp.get_credit_status,", "action: erp.nope,", "erp.nope"},
		{"action arity does not match the reads", "reads: [f_customer, f_amount]", "reads: [f_customer]",
			"erp.check_credit"},
		{"action arity does not match the writes", "writes: [f_plan], fallback", "writes: [f_plan, f_ref], fallback",
			"crm.get_plan"},
		{"reads an unknown field", "reads: [f_customer, f_amount]", "reads: [f_customer, f_ghost]", "f_ghost"},
		{"writes an unknown field", "writes: [f_ref]", "writes: [f_ghost]", "f_ghost"},
		{"a check that reads nothing", "reads: [f_customer, f_amount]", "reads: []", "reads"},
		{"an enrich hook that writes nothing", "writes: [f_on_hold]", "writes: []", "writes"},
		{"fallback for a field the hook does not write", "fallback: {f_plan: unknown}",
			"fallback: {f_on_hold: false}", "f_on_hold"},
		{"fallback value outside the schema", "fallback: {f_plan: unknown}", "fallback: {f_plan: gold}", "f_plan"},
		{"fallback on a check", "writes: [f_ref]}", "writes: [f_ref], fallback: {f_ref: x}}", "fallback"},
		// A hook never approves anything on its own authority: the requester's
		// `allowed` is what the hold applies, so naming a hook there is a mistake.
		{"a checked transition whose allowed names a hook", "allowed: ['role:approver']",
			"allowed: ['role:approver', 'principal:hook:order:check']", "principal:hook:order:check"},
		{"two checks on one transition", "hooks:\n",
			"hooks:\n  - {id: check2, on: transition, from: open, to: approved, kind: check, action: x.y, reads: [f_amount]}\n",
			"open -> approved"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if !strings.Contains(hookedType, c.old) {
				t.Fatalf("test bug: %q is not in the base Type", c.old)
			}
			_, err := typedef.Parse([]byte(strings.Replace(hookedType, c.old, c.new, 1)))
			if err == nil {
				t.Fatal("Parse accepted a broken hook declaration")
			}
			if !strings.Contains(err.Error(), c.wantInError) {
				t.Errorf("error %q does not mention %q", err, c.wantInError)
			}
		})
	}
}

// ticket v3 is the Type of the holds-and-connectors spike: v2's lifecycle
// with a triage state in front, an approve step guarded by the prefetched
// on_hold fact and checked by the ERP, and cancel as the escape.
func TestTicketV3_HoldsAndConnectors(t *testing.T) {
	v3 := parseFile(t, "ticket.v3.yaml")
	if v3.ID != "ticket" || v3.Version != 3 || v3.InitialState != "triage" {
		t.Fatalf("v3 = %s v%d starting in %q; want ticket v3 starting in triage", v3.ID, v3.Version, v3.InitialState)
	}
	for id, typ := range map[string]typedef.FieldType{
		"f_customer_id": typedef.String, "f_plan": typedef.Enum, "f_on_hold": typedef.Bool,
		"f_amount": typedef.Int, "f_cost_center": typedef.String,
	} {
		if f, ok := v3.Field(id); !ok || f.Type != typ {
			t.Errorf("field %s = %+v (found %v), want type %s", id, f, ok, typ)
		}
	}
	if f, _ := v3.Field("f_plan"); !strings.Contains(strings.Join(f.Options, ","), "unknown") {
		t.Errorf("f_plan options = %v, want them to include unknown", f.Options)
	}
	if s, ok := v3.State("triage"); !ok || s.Category != "triage" {
		t.Errorf("triage = %+v (found %v), want category triage", s, ok)
	}

	approve, ok := v3.Transition("in_progress", "approved")
	if !ok {
		t.Fatal("v3 has no transition in_progress -> approved")
	}
	if approve.Guard != "f_on_hold != true" || !reflect.DeepEqual(approve.Allowed, []string{"role:approver"}) {
		t.Errorf("approve = %+v, want guard 'f_on_hold != true' and allowed [role:approver]", approve)
	}
	if cancel, ok := v3.Transition("in_progress", "canceled"); !ok || !cancel.Escape {
		t.Errorf("in_progress -> canceled = %+v (found %v), want an escape", cancel, ok)
	}

	wantHooks := map[string]typedef.Hook{
		"get_plan": {ID: "get_plan", On: typedef.OnEnter, State: "triage", Kind: typedef.HookEnrich,
			Action: "crm.get_plan", Reads: []string{"f_customer_id"}, Writes: []string{"f_plan"},
			Fallback: map[string]any{"f_plan": "unknown"}},
		"get_credit_status": {ID: "get_credit_status", On: typedef.OnEnter, State: "triage", Kind: typedef.HookEnrich,
			Action: "erp.get_credit_status", Reads: []string{"f_customer_id"}, Writes: []string{"f_on_hold"}},
		"erp_check": {ID: "erp_check", On: typedef.OnTransition, From: "in_progress", To: "approved",
			Kind: typedef.HookCheck, Action: "erp.check_credit",
			Reads: []string{"f_customer_id", "f_amount"}, Writes: []string{"f_cost_center"}},
	}
	if len(v3.Hooks) != len(wantHooks) {
		t.Errorf("v3 hooks = %+v, want %d", v3.Hooks, len(wantHooks))
	}
	for id, want := range wantHooks {
		if got, ok := v3.Hook(id); !ok || !reflect.DeepEqual(got, want) {
			t.Errorf("hook %s = %+v (found %v)\nwant %+v", id, got, ok, want)
		}
	}

	// v1 and v2 keep their own lifecycle; neither has hooks.
	for _, name := range []string{"ticket.v1.yaml", "ticket.v2.yaml"} {
		if ty := parseFile(t, name); len(ty.Hooks) != 0 {
			t.Errorf("%s has hooks %+v; it must stay as it was", name, ty.Hooks)
		}
	}
}
