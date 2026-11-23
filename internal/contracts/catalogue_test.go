// Tests for Ventoux V1: the repository's catalogue, contracts/. It holds one
// contract for each action the hook protocol step left in the worker's
// catalog, matching how today's Types use them (so no Type changes), with
// the values agreed in Phase A:
//
//   - risk: low for the lookups and checks, medium for a message, high for
//     an invoice;
//   - trust (declared, not enforced until Ventoux item 4): every input of a
//     check or an action is trusted, since it decides a move or causes an
//     effect; an enrich input is any;
//   - reasons: the codes each check answers today.
package contracts_test

import (
	"bytes"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/contracts"
	"github.com/francoisnyc/work-kernel/internal/hookproto"
)

// The repository's contracts load, the lock agrees with them, and the
// compiled-in catalogue is the same.
func TestCatalogue_TheRepositoryLoads(t *testing.T) {
	cat, err := contracts.Load(os.DirFS("../../contracts"))
	if err != nil {
		t.Fatalf("Load(contracts/): %v", err)
	}
	var onDisk, compiled []string
	for _, c := range cat.All() {
		onDisk = append(onDisk, c.Action()+" "+c.Digest())
	}
	for _, c := range contracts.Default().All() {
		compiled = append(compiled, c.Action()+" "+c.Digest())
	}
	if len(onDisk) == 0 || !reflect.DeepEqual(onDisk, compiled) {
		t.Errorf("contracts/ = %v\ncompiled in = %v", onDisk, compiled)
	}
}

// The lock holds exactly one line per contract.
func TestCatalogue_TheLock(t *testing.T) {
	data, err := os.ReadFile("../../contracts/contracts.lock")
	if err != nil {
		t.Fatal(err)
	}
	lock, err := contracts.ParseLock(data)
	if err != nil {
		t.Fatalf("ParseLock: %v", err)
	}
	all := contracts.Default().All()
	if len(lock) != len(all) {
		t.Errorf("%d lock lines for %d contracts", len(lock), len(all))
	}
	for _, c := range all {
		if lock[c.Action()] != c.Digest() {
			t.Errorf("%s: lock %q, digest %q", c.Action(), lock[c.Action()], c.Digest())
		}
	}
	if !bytes.HasPrefix(data, []byte("#")) {
		t.Error("the lock should start with its comment, saying what it is")
	}
}

type val struct {
	name, typ string
	nullable  bool
	trust     string
	options   []string
}

func TestCatalogue_TheSixActions(t *testing.T) {
	cases := []struct {
		action      string
		kinds       []hookproto.Kind
		inputs      []val
		outputs     []val
		sideEffects string
		risk        string
		reasons     []string
	}{
		{"crm.get_plan@1", []hookproto.Kind{hookproto.KindEnrich},
			[]val{{name: "customerId", typ: "string", trust: "any"}},
			[]val{{name: "plan", typ: "enum", options: []string{"free", "pro", "enterprise"}}},
			"none", "low", nil},
		{"email.notify@1", []hookproto.Kind{hookproto.KindAction},
			[]val{{name: "to", typ: "principal", trust: "trusted"}},
			nil,
			"some", "medium", nil},
		{"erp.check_credit@1", []hookproto.Kind{hookproto.KindCheck},
			[]val{{name: "customerId", typ: "string", trust: "trusted"}, {name: "amount", typ: "int", trust: "trusted"}},
			[]val{{name: "costCenter", typ: "string"}},
			"none", "low", []string{"customer_on_hold", "over_credit_limit"}},
		{"erp.create_invoice@1", []hookproto.Kind{hookproto.KindAction},
			[]val{{name: "customerId", typ: "string", trust: "trusted"}, {name: "amount", typ: "int", trust: "trusted"}},
			[]val{{name: "invoiceId", typ: "string"}},
			"some", "high", nil},
		{"erp.get_credit_status@1", []hookproto.Kind{hookproto.KindEnrich},
			[]val{{name: "customerId", typ: "string", trust: "any"}},
			[]val{{name: "onHold", typ: "bool"}},
			"none", "low", nil},
		{"hr.manager_of@1", []hookproto.Kind{hookproto.KindCheck},
			[]val{{name: "onBehalfOf", typ: "principal", nullable: true, trust: "trusted"},
				{name: "createdBy", typ: "principal", nullable: true, trust: "trusted"}},
			[]val{{name: "manager", typ: "principal"}},
			"none", "low", []string{"unknown_employee", "no_eligible_approver"}},
	}
	all := contracts.Default().All()
	var got []string
	for _, c := range all {
		got = append(got, c.Action())
	}
	var want []string
	for _, c := range cases {
		want = append(want, c.action)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("the catalogue = %v\nwant %v (ordered by action)", got, want)
	}
	values := func(vs []contracts.Value) []val {
		var out []val
		for _, v := range vs {
			out = append(out, val{name: v.Name, typ: v.Type, nullable: v.Nullable, trust: v.Trust, options: v.Options})
		}
		return out
	}
	for i, w := range cases {
		c := all[i]
		t.Run(w.action, func(t *testing.T) {
			if !reflect.DeepEqual(c.Kinds, w.kinds) {
				t.Errorf("kinds = %v, want %v", c.Kinds, w.kinds)
			}
			if got := values(c.InputValues()); !reflect.DeepEqual(got, w.inputs) {
				t.Errorf("inputs = %+v\nwant %+v", got, w.inputs)
			}
			if got := values(c.OutputValues()); !reflect.DeepEqual(got, w.outputs) {
				t.Errorf("outputs = %+v\nwant %+v", got, w.outputs)
			}
			if c.SideEffects != w.sideEffects || c.Risk != w.risk || c.Async != "forbidden" {
				t.Errorf("sideEffects %q, risk %q, async %q; want %q, %q, forbidden", c.SideEffects, c.Risk, c.Async, w.sideEffects, w.risk)
			}
			if !reflect.DeepEqual(c.Errors, w.reasons) {
				t.Errorf("reasons = %v, want %v", c.Errors, w.reasons)
			}
			// Descriptions are for a person choosing an action: each says
			// something.
			if strings.TrimSpace(c.Description) == "" {
				t.Error("no description")
			}
			for _, v := range append(c.InputValues(), c.OutputValues()...) {
				if strings.TrimSpace(v.Description) == "" {
					t.Errorf("%s has no description", v.Name)
				}
			}
		})
	}
}
