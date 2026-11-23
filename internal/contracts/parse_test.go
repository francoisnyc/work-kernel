// Tests for Ventoux V1: a contract file is checked when it is read
// (contracts.Parse). A file that breaks a rule is an error naming what is
// wrong; the item service and the worker refuse to start with one, and
// go test finds it first (TestCatalogue_TheRepositoryLoads).
//
// The format is the hook protocol's descriptor operation (hookproto.Operation)
// plus its connector and major, with the contract's additions: the inputs
// and outputs in order, a description everywhere, risk, and on inputs
// x-pli-trust. The schemas use a subset of JSON Schema: each property's type
// is string, integer or boolean, alone or with "null"; a string may carry
// format (pli-principal, date or date-time) or enum.
package contracts_test

import (
	"reflect"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/contracts"
)

func TestParse_AValidContract(t *testing.T) {
	c := mustParse(t, validFile, []byte(validJSON))
	if c.Action() != "acme.lookup@1" || c.Connector != "acme" || c.Major != 1 || c.Name != "lookup" {
		t.Errorf("action %q, connector %q, major %d, name %q", c.Action(), c.Connector, c.Major, c.Name)
	}
	ins := []contracts.Value{
		{Name: "customerId", Type: contracts.TypeString, Nullable: true, Description: "The customer's ID.", Trust: "trusted"},
		// No x-pli-trust: any.
		{Name: "owner", Type: contracts.TypePrincipal, Description: "Who owns the account.", Trust: "any"},
	}
	if got := c.InputValues(); !reflect.DeepEqual(got, ins) {
		t.Errorf("inputs = %+v\nwant %+v", got, ins)
	}
	outs := []contracts.Value{
		{Name: "tier", Type: contracts.TypeEnum, Options: []string{"gold", "silver"}, Description: "The customer's tier."},
	}
	if got := c.OutputValues(); !reflect.DeepEqual(got, outs) {
		t.Errorf("outputs = %+v\nwant %+v", got, outs)
	}
}

// Each JSON Schema form reads as the field type it matches.
func TestParse_ValueTypes(t *testing.T) {
	cases := []struct {
		property map[string]any
		want     contracts.Value
	}{
		{map[string]any{"type": "string"}, contracts.Value{Type: contracts.TypeString}},
		{map[string]any{"type": "integer"}, contracts.Value{Type: contracts.TypeInt}},
		{map[string]any{"type": "boolean"}, contracts.Value{Type: contracts.TypeBool}},
		{map[string]any{"type": []any{"boolean", "null"}}, contracts.Value{Type: contracts.TypeBool, Nullable: true}},
		{map[string]any{"type": []any{"null", "integer"}}, contracts.Value{Type: contracts.TypeInt, Nullable: true}},
		{map[string]any{"type": "string", "enum": []any{"a", "b"}}, contracts.Value{Type: contracts.TypeEnum, Options: []string{"a", "b"}}},
		{map[string]any{"type": "string", "format": "pli-principal"}, contracts.Value{Type: contracts.TypePrincipal}},
		{map[string]any{"type": "string", "format": "date"}, contracts.Value{Type: contracts.TypeDate}},
		{map[string]any{"type": "string", "format": "date-time"}, contracts.Value{Type: contracts.TypeDateTime}},
	}
	for _, tc := range cases {
		data := changed(t, func(m map[string]any) {
			tc.property["description"] = "The tier."
			m["outputSchema"].(map[string]any)["properties"].(map[string]any)["tier"] = tc.property
		})
		c := mustParse(t, validFile, data)
		want := tc.want
		want.Name, want.Description = "tier", "The tier."
		if got := c.OutputValues(); len(got) != 1 || !reflect.DeepEqual(got[0], want) {
			t.Errorf("%v reads as %+v, want %+v", tc.property, got, want)
		}
	}
}

// An action with no outputs (email.notify) and a contract with no reasons
// are valid.
func TestParse_NoOutputsNoReasons(t *testing.T) {
	data := changed(t, func(m map[string]any) {
		m["kinds"] = []any{"action"}
		m["sideEffects"] = "some"
		m["risk"] = "medium"
		delete(m, "errors")
		delete(m, "outputs")
		delete(m, "outputSchema")
	})
	c := mustParse(t, validFile, data)
	if len(c.OutputValues()) != 0 || len(c.Errors) != 0 {
		t.Errorf("outputs %v, errors %v; want none", c.OutputValues(), c.Errors)
	}
}

// Every rule of the format, broken once, is an error naming the problem.
func TestParse_Refuses(t *testing.T) {
	in := func(m map[string]any, name string) map[string]any { return prop(m, "inputSchema", name) }
	out := func(m map[string]any, name string) map[string]any { return prop(m, "outputSchema", name) }
	cases := []struct {
		name   string
		file   string // validFile when empty
		change func(m map[string]any)
		want   []string // the error names each
	}{
		// The file and what it holds.
		{name: "not JSON", change: nil, want: []string{validFile}},
		{name: "a file name that is not connector.operation@major.json", file: "lookup.json",
			change: func(map[string]any) {}, want: []string{"lookup.json"}},
		{name: "a file named for another action", file: "acme.lookup@2.json",
			change: func(map[string]any) {}, want: []string{"acme.lookup@2.json", "acme.lookup@1"}},
		{name: "an unknown key", change: func(m map[string]any) { m["retries"] = 3 }, want: []string{"retries"}},
		{name: "an unknown key in a property", change: func(m map[string]any) { in(m, "owner")["pattern"] = "^user:" },
			want: []string{"pattern"}},
		// Names and major.
		{name: "a connector name in capitals", file: "Acme.lookup@1.json",
			change: func(m map[string]any) { m["connector"] = "Acme" }, want: []string{"Acme"}},
		{name: "an operation name with a dot", file: "acme.look.up@1.json",
			change: func(m map[string]any) { m["name"] = "look.up" }, want: []string{"look.up"}},
		{name: "major 0", file: "acme.lookup@0.json", change: func(m map[string]any) { m["major"] = 0 }, want: []string{"major"}},
		// Kinds and side effects.
		{name: "no kinds", change: func(m map[string]any) { m["kinds"] = []any{} }, want: []string{"kinds"}},
		{name: "an unknown kind", change: func(m map[string]any) { m["kinds"] = []any{"poke"} }, want: []string{"poke"}},
		{name: "a kind twice", change: func(m map[string]any) { m["kinds"] = []any{"check", "check"} }, want: []string{"check"}},
		{name: "an action without side effects", change: func(m map[string]any) {
			m["kinds"] = []any{"action"}
			delete(m, "errors")
		}, want: []string{"sideEffects"}},
		{name: "an enrich operation with side effects", change: func(m map[string]any) {
			m["kinds"] = []any{"enrich"}
			m["sideEffects"] = "some"
			delete(m, "errors")
		}, want: []string{"sideEffects"}},
		{name: "an action that is also a check", change: func(m map[string]any) {
			m["kinds"] = []any{"check", "action"}
			m["sideEffects"] = "some"
		}, want: []string{"action"}},
		{name: "side effects neither none nor some", change: func(m map[string]any) { m["sideEffects"] = "maybe" },
			want: []string{"maybe"}},
		// Asynchronous completion is specified, not built.
		{name: "async optional", change: func(m map[string]any) { m["async"] = "optional" }, want: []string{"async"}},
		{name: "no async", change: func(m map[string]any) { delete(m, "async") }, want: []string{"async"}},
		// Reasons: only a check denies, with codes.
		{name: "reasons on an enrich operation", change: func(m map[string]any) { m["kinds"] = []any{"enrich"} },
			want: []string{"errors"}},
		{name: "a reason that is not a code", change: func(m map[string]any) { m["errors"] = []any{"Unknown customer"} },
			want: []string{"Unknown customer"}},
		{name: "a reason twice", change: func(m map[string]any) { m["errors"] = []any{"unknown_customer", "unknown_customer"} },
			want: []string{"unknown_customer"}},
		// Risk.
		{name: "no risk", change: func(m map[string]any) { delete(m, "risk") }, want: []string{"risk"}},
		{name: "an unknown risk", change: func(m map[string]any) { m["risk"] = "extreme" }, want: []string{"extreme"}},
		// Descriptions are required everywhere.
		{name: "no description", change: func(m map[string]any) { delete(m, "description") }, want: []string{"description"}},
		{name: "an input without a description", change: func(m map[string]any) { delete(in(m, "owner"), "description") },
			want: []string{"owner", "description"}},
		{name: "an output without a description", change: func(m map[string]any) { delete(out(m, "tier"), "description") },
			want: []string{"tier", "description"}},
		// The order lists and the schemas name the same values.
		{name: "an input the order does not list", change: func(m map[string]any) { m["inputs"] = []any{"customerId"} },
			want: []string{"owner"}},
		{name: "an input the schema does not have", change: func(m map[string]any) {
			m["inputs"] = []any{"customerId", "owner", "region"}
		}, want: []string{"region"}},
		{name: "an input listed twice", change: func(m map[string]any) {
			m["inputs"] = []any{"customerId", "owner", "owner"}
		}, want: []string{"owner"}},
		{name: "an output the order does not list", change: func(m map[string]any) { delete(m, "outputs") },
			want: []string{"tier"}},
		{name: "an input schema that is not an object", change: func(m map[string]any) {
			m["inputSchema"].(map[string]any)["type"] = "array"
		}, want: []string{"inputSchema"}},
		{name: "an enrich operation with no output", change: func(m map[string]any) {
			m["kinds"] = []any{"enrich"}
			delete(m, "errors")
			delete(m, "outputs")
			delete(m, "outputSchema")
		}, want: []string{"output"}},
		// Types: the subset only.
		{name: "a number", change: func(m map[string]any) { out(m, "tier")["type"] = "number"; delete(out(m, "tier"), "enum") },
			want: []string{"number"}},
		{name: "an array", change: func(m map[string]any) { in(m, "owner")["type"] = "array"; delete(in(m, "owner"), "format") },
			want: []string{"array"}},
		{name: "an object", change: func(m map[string]any) { in(m, "owner")["type"] = "object"; delete(in(m, "owner"), "format") },
			want: []string{"object"}},
		{name: "two types", change: func(m map[string]any) { in(m, "customerId")["type"] = []any{"string", "integer"} },
			want: []string{"customerId"}},
		{name: "null alone", change: func(m map[string]any) { in(m, "customerId")["type"] = []any{"null"} },
			want: []string{"customerId"}},
		{name: "no type", change: func(m map[string]any) { delete(in(m, "customerId"), "type") },
			want: []string{"customerId"}},
		{name: "a format on an integer", change: func(m map[string]any) { in(m, "owner")["type"] = "integer" },
			want: []string{"owner", "format"}},
		{name: "an unknown format", change: func(m map[string]any) { in(m, "owner")["format"] = "email" },
			want: []string{"email"}},
		{name: "an enum on a boolean", change: func(m map[string]any) { out(m, "tier")["type"] = "boolean" },
			want: []string{"tier", "enum"}},
		{name: "an empty enum", change: func(m map[string]any) { out(m, "tier")["enum"] = []any{} },
			want: []string{"tier", "enum"}},
		{name: "an enum value twice", change: func(m map[string]any) { out(m, "tier")["enum"] = []any{"gold", "gold"} },
			want: []string{"gold"}},
		{name: "an enum with a format", change: func(m map[string]any) { out(m, "tier")["format"] = "date" },
			want: []string{"tier"}},
		// Trust: inputs only, any or trusted.
		{name: "an unknown trust", change: func(m map[string]any) { in(m, "owner")["x-pli-trust"] = "maybe" },
			want: []string{"maybe"}},
		{name: "trust on an output", change: func(m map[string]any) { out(m, "tier")["x-pli-trust"] = "trusted" },
			want: []string{"tier", "x-pli-trust"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			file := c.file
			if file == "" {
				file = validFile
			}
			data := []byte("{not json")
			if c.change != nil {
				data = changed(t, c.change)
			}
			_, err := contracts.Parse(file, data)
			wantError(t, c.name, err, c.want...)
		})
	}
}
