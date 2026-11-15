// Tests for Step 7: the third kind of hook, `action`, and the Types that
// use it.
//
// An action hook runs on entering a state, like an enrich hook, but it has
// a side effect outside the kernel (an invoice in the ERP). So it gets an
// idempotency key, it has no fallback (there is no "default invoice"), and
// it writes back what it made (the invoice ID). Once its state is entered it
// always runs to completion, even if the Item has moved on.
package typedef_test

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/typedef"
)

const actionType = `
id: order
version: 1
initial_state: open
fields:
  - {id: f_customer, type: string}
  - {id: f_amount,   type: int}
  - {id: f_invoice,  type: string}
  - {id: f_plan,     type: enum, options: [free, pro, unknown]}
states:
  - {id: open,     category: todo}
  - {id: approved, category: active}
  - {id: done,     category: done}
transitions:
  - {from: open, to: approved}
  - {from: approved, to: done}
hooks:
  - {id: invoice, on: enter, state: approved, kind: action, action: erp.create_invoice,
     reads: [f_customer, f_amount], writes: [f_invoice]}
`

func TestParse_ActionHook(t *testing.T) {
	ty, err := typedef.Parse([]byte(actionType))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	got, ok := ty.Hook("invoice")
	want := typedef.Hook{
		ID: "invoice", On: typedef.OnEnter, State: "approved", Kind: typedef.HookAction,
		Action: "erp.create_invoice", Reads: []string{"f_customer", "f_amount"}, Writes: []string{"f_invoice"},
	}
	if !ok || !reflect.DeepEqual(got, want) {
		t.Errorf("invoice = %+v (found %v)\nwant %+v", got, ok, want)
	}
	if back := roundTrip(t, ty); !reflect.DeepEqual(back.Hooks, ty.Hooks) {
		t.Errorf("hooks after a JSON round trip = %+v\nwant %+v", back.Hooks, ty.Hooks)
	}
}

func TestParse_RejectsBrokenActionHooks(t *testing.T) {
	cases := []struct {
		name, old, new, wantInError string
	}{
		{"an action on a transition", "on: enter, state: approved, kind: action",
			"on: transition, from: open, to: approved, kind: action", "action"},
		// There is no default invoice: a fallback would claim an effect that
		// never happened.
		{"a fallback on an action", "writes: [f_invoice]}", "writes: [f_invoice], fallback: {f_invoice: none}}",
			"fallback"},
		// What it writes back records that the effect happened.
		{"an action that writes nothing", "writes: [f_invoice]}", "writes: []}", "writes"},
		{"action arity does not match", "reads: [f_customer, f_amount]", "reads: [f_customer]",
			"erp.create_invoice"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if !strings.Contains(actionType, c.old) {
				t.Fatalf("test bug: %q is not in the base Type", c.old)
			}
			_, err := typedef.Parse([]byte(strings.Replace(actionType, c.old, c.new, 1)))
			if err == nil {
				t.Fatal("Parse accepted a broken action hook")
			}
			if !strings.Contains(err.Error(), c.wantInError) {
				t.Errorf("error %q does not mention %q", err, c.wantInError)
			}
		})
	}
}

// withoutPrograms copies transitions without their compiled guards, which
// are pointers and never compare equal.
func withoutPrograms(trs []typedef.Transition) []typedef.Transition {
	out := make([]typedef.Transition, len(trs))
	for i, tr := range trs {
		tr.Program = nil
		out[i] = tr
	}
	return out
}

// ticket v4 is v3 plus the invoice: f_invoice_id, and an action hook on
// entering approved. The lifecycle and the other hooks are v3's.
func TestTicketV4_TheInvoiceHook(t *testing.T) {
	v3, v4 := parseFile(t, "ticket.v3.yaml"), parseFile(t, "ticket.v4.yaml")
	if v4.ID != "ticket" || v4.Version != 4 || v4.InitialState != v3.InitialState {
		t.Fatalf("v4 = %s v%d starting in %q; want ticket v4 starting in %q", v4.ID, v4.Version, v4.InitialState, v3.InitialState)
	}
	if !reflect.DeepEqual(v4.States, v3.States) {
		t.Errorf("v4 states = %+v\nwant v3's %+v", v4.States, v3.States)
	}
	if !reflect.DeepEqual(withoutPrograms(v4.Transitions), withoutPrograms(v3.Transitions)) {
		t.Errorf("v4 transitions = %+v\nwant v3's %+v", v4.Transitions, v3.Transitions)
	}
	wantFields := append(append([]typedef.Field(nil), v3.Fields...), typedef.Field{ID: "f_invoice_id", Type: typedef.String})
	if !reflect.DeepEqual(v4.Fields, wantFields) {
		t.Errorf("v4 fields = %+v\nwant v3's plus f_invoice_id: %+v", v4.Fields, wantFields)
	}
	wantHooks := append(append([]typedef.Hook(nil), v3.Hooks...), typedef.Hook{
		ID: "create_invoice", On: typedef.OnEnter, State: "approved", Kind: typedef.HookAction,
		Action: "erp.create_invoice", Reads: []string{"f_customer_id", "f_amount"}, Writes: []string{"f_invoice_id"},
	})
	if !reflect.DeepEqual(v4.Hooks, wantHooks) {
		t.Errorf("v4 hooks = %+v\nwant v3's plus create_invoice: %+v", v4.Hooks, wantHooks)
	}
}

// ticket_a is variant A, for the bench only (testdata, not types/): every
// check as a hold. It is ticket v3 without the prefetch (no enrich hooks)
// and without the guard on approve; the ERP check is the only answer.
func TestTicketA_EveryCheckAsAHold(t *testing.T) {
	data, err := os.ReadFile("../../testdata/ticket_a.v1.yaml")
	if err != nil {
		t.Fatalf("read ticket_a: %v", err)
	}
	a, err := typedef.Parse(data)
	if err != nil {
		t.Fatalf("parse ticket_a: %v", err)
	}
	v3 := parseFile(t, "ticket.v3.yaml")
	if a.ID != "ticket_a" || a.Version != 1 || a.InitialState != v3.InitialState {
		t.Errorf("ticket_a = %s v%d starting in %q; want ticket_a v1 starting in %q", a.ID, a.Version, a.InitialState, v3.InitialState)
	}
	if !reflect.DeepEqual(a.States, v3.States) || !reflect.DeepEqual(a.Fields, v3.Fields) {
		t.Errorf("ticket_a states and fields differ from v3's")
	}
	for _, tr := range a.Transitions {
		want, ok := v3.Transition(tr.From, tr.To)
		if !ok {
			t.Errorf("ticket_a has %s -> %s, which v3 does not", tr.From, tr.To)
			continue
		}
		if tr.From == "in_progress" && tr.To == "approved" {
			want.Guard = "" // the only difference: no guard on the prefetched fact
		}
		tr.Program, want.Program = nil, nil
		if !reflect.DeepEqual(tr, want) {
			t.Errorf("ticket_a %s -> %s = %+v\nwant %+v", tr.From, tr.To, tr, want)
		}
	}
	if len(a.Transitions) != len(v3.Transitions) {
		t.Errorf("ticket_a has %d transitions, v3 %d", len(a.Transitions), len(v3.Transitions))
	}
	if len(a.Hooks) != 1 {
		t.Fatalf("ticket_a hooks = %+v, want only the ERP check", a.Hooks)
	}
	check, _ := v3.Hook("erp_check")
	if !reflect.DeepEqual(a.Hooks[0], check) {
		t.Errorf("ticket_a hook = %+v\nwant v3's erp_check %+v", a.Hooks[0], check)
	}
}
