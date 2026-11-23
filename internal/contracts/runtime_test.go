// Tests for Ventoux V1, at run time: the worker checks what a connector
// says against the contract.
//
//   - Value.Check: an output a connector answered must have its declared
//     type (as JSON decodes it: a whole float64 is an integer). A mismatch
//     is the connector breaking its contract (connector_mismatch).
//   - Contract.Matches: a connector's descriptor must fit the contract. The
//     operation's kinds include the contract's; its input and output names
//     are the contract's; each property's type list is the contract's;
//     format and enum, when the descriptor gives them, are the contract's (a
//     descriptor written before V1 has neither); sideEffects is the
//     contract's; its errors are among the contract's.
package contracts_test

import (
	"testing"

	"github.com/francoisnyc/work-kernel/internal/contracts"
	"github.com/francoisnyc/work-kernel/internal/hookproto"
)

func TestValueCheck(t *testing.T) {
	v := func(typ string, nullable bool, options ...string) contracts.Value {
		return contracts.Value{Name: "x", Type: typ, Nullable: nullable, Options: options}
	}
	cases := []struct {
		value contracts.Value
		ok    []any
		bad   []any
	}{
		{v(contracts.TypeString, false), []any{"x", ""}, []any{5, true, nil, []any{"x"}}},
		{v(contracts.TypeString, true), []any{"x", nil}, []any{5}},
		{v(contracts.TypeInt, false), []any{5, int64(5), float64(5), float64(-3)}, []any{5.5, "5", nil, true}},
		{v(contracts.TypeBool, false), []any{true, false}, []any{"true", 1, nil}},
		{v(contracts.TypeEnum, false, "gold", "silver"), []any{"gold"}, []any{"bronze", "", 1, nil}},
		{v(contracts.TypePrincipal, false), []any{"user:ana", "agent:bot"}, []any{"ana", "", "user:", 7, nil}},
		{v(contracts.TypeDate, false), []any{"2026-12-01"}, []any{"2026-13-01", "2026-12-01T09:00:00Z", "tomorrow", nil}},
		{v(contracts.TypeDateTime, false), []any{"2026-12-01T09:00:00Z", "2026-12-01T09:00:00+01:00"},
			[]any{"2026-12-01", "2026-12-01T09:00:00", nil}},
	}
	for _, c := range cases {
		for _, x := range c.ok {
			if err := c.value.Check(x); err != nil {
				t.Errorf("%s (nullable %v): %#v refused: %v", c.value.Type, c.value.Nullable, x, err)
			}
		}
		for _, x := range c.bad {
			if err := c.value.Check(x); err == nil {
				t.Errorf("%s (nullable %v): %#v accepted", c.value.Type, c.value.Nullable, x)
			}
		}
	}
}

// descriptorOf is what a connector built from the contract would serve.
func descriptorOf(t *testing.T) (contracts.Contract, hookproto.Operation) {
	t.Helper()
	c := mustParse(t, validFile, []byte(validJSON))
	return c, c.Operation
}

func TestMatches_TheContractsOwnOperation(t *testing.T) {
	c, op := descriptorOf(t)
	if err := c.Matches(op); err != nil {
		t.Errorf("the contract's own operation: %v", err)
	}
}

// A descriptor written before V1 gives no description, order, risk, format,
// enum or trust: it still matches.
func TestMatches_ADescriptorFromBeforeV1(t *testing.T) {
	c := mustParse(t, validFile, []byte(validJSON))
	op := hookproto.Operation{
		Name: "lookup", Kinds: []hookproto.Kind{hookproto.KindCheck},
		InputSchema: hookproto.Schema{Type: "object", Properties: map[string]hookproto.Property{
			"customerId": {Type: hookproto.TypeList{"string", "null"}},
			"owner":      {Type: hookproto.TypeList{"string"}},
		}},
		OutputSchema: &hookproto.Schema{Type: "object", Properties: map[string]hookproto.Property{
			"tier": {Type: hookproto.TypeList{"string"}},
		}},
		SideEffects: "none", Async: "forbidden", Errors: []string{"unknown_customer"},
	}
	if err := c.Matches(op); err != nil {
		t.Errorf("a descriptor without the contract's additions: %v", err)
	}
	// Fewer reasons and more kinds than the contract are fine.
	op.Errors, op.Kinds = nil, []hookproto.Kind{hookproto.KindCheck, hookproto.KindEnrich}
	if err := c.Matches(op); err != nil {
		t.Errorf("no reasons, an extra kind: %v", err)
	}
}

func TestMatches_Refuses(t *testing.T) {
	setIn := func(op *hookproto.Operation, name string, p hookproto.Property) {
		props := map[string]hookproto.Property{}
		for k, v := range op.InputSchema.Properties {
			props[k] = v
		}
		props[name] = p
		op.InputSchema.Properties = props
	}
	setOut := func(op *hookproto.Operation, name string, p hookproto.Property) {
		props := map[string]hookproto.Property{}
		for k, v := range op.OutputSchema.Properties {
			props[k] = v
		}
		props[name] = p
		schema := *op.OutputSchema
		schema.Properties = props
		op.OutputSchema = &schema
	}
	cases := []struct {
		name   string
		change func(op *hookproto.Operation)
		want   string
	}{
		{"a kind missing", func(op *hookproto.Operation) { op.Kinds = []hookproto.Kind{hookproto.KindEnrich} }, "check"},
		{"another input", func(op *hookproto.Operation) {
			setIn(op, "region", hookproto.Property{Type: hookproto.TypeList{"string"}})
		}, "region"},
		{"an input missing", func(op *hookproto.Operation) {
			op.InputSchema.Properties = map[string]hookproto.Property{"customerId": op.InputSchema.Properties["customerId"]}
		}, "owner"},
		{"another output", func(op *hookproto.Operation) {
			setOut(op, "level", hookproto.Property{Type: hookproto.TypeList{"string"}})
		}, "level"},
		{"an input of another type", func(op *hookproto.Operation) {
			setIn(op, "customerId", hookproto.Property{Type: hookproto.TypeList{"integer", "null"}})
		}, "customerId"},
		{"an input that may not be null", func(op *hookproto.Operation) {
			setIn(op, "customerId", hookproto.Property{Type: hookproto.TypeList{"string"}})
		}, "customerId"},
		{"another format", func(op *hookproto.Operation) {
			setIn(op, "owner", hookproto.Property{Type: hookproto.TypeList{"string"}, Format: "date"})
		}, "owner"},
		{"another enum", func(op *hookproto.Operation) {
			setOut(op, "tier", hookproto.Property{Type: hookproto.TypeList{"string"}, Enum: []string{"gold", "silver", "bronze"}})
		}, "tier"},
		{"side effects", func(op *hookproto.Operation) { op.SideEffects = "some" }, "sideEffects"},
		{"a reason the contract does not declare", func(op *hookproto.Operation) {
			op.Errors = []string{"unknown_customer", "closed_account"}
		}, "closed_account"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			contract, op := descriptorOf(t)
			c.change(&op)
			wantError(t, c.name, contract.Matches(op), c.want)
		})
	}
}
