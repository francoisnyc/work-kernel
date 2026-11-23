package hooks

import (
	"slices"

	"github.com/francoisnyc/work-kernel/internal/contracts"
	"github.com/francoisnyc/work-kernel/internal/hookproto"
)

// DeclareRemoteForTest declares a remote operation for a test, such as a
// remote enrich operation (none is real yet). Only tests see it: it lives
// in a _test.go file, so no production path can declare an action.
//
// Its inputs and outputs are names without types: the contract's type
// checks (descriptor types, answer values) skip an untyped value. A
// contract from contracts/ is always typed (contracts.Parse refuses one
// that is not).
func DeclareRemoteForTest(actionName string, kinds []hookproto.Kind, inputs, outputs []string) {
	ref, ok := parseAction(actionName)
	if !ok {
		panic("hooks: DeclareRemoteForTest: bad action name " + actionName)
	}
	untyped := func(names []string) *hookproto.Schema {
		props := map[string]hookproto.Property{}
		for _, n := range names {
			props[n] = hookproto.Property{}
		}
		return &hookproto.Schema{Type: "object", Properties: props}
	}
	c := contracts.Contract{Connector: ref.connector, Major: ref.major, Operation: hookproto.Operation{
		Name: ref.operation, Kinds: kinds, Inputs: inputs, Outputs: outputs,
		InputSchema: *untyped(inputs), OutputSchema: untyped(outputs),
		SideEffects: hookproto.SideEffectsNone, Async: hookproto.AsyncForbidden,
	}}
	catalogMu.Lock()
	defer catalogMu.Unlock()
	catalog[ref.key()] = fromContract(c, action{})
}

// CompiledInForTest returns the actions the worker has code for, sorted:
// <connector>.<operation>@<major>.
func CompiledInForTest() []string {
	catalogMu.RLock()
	defer catalogMu.RUnlock()
	var out []string
	for name, a := range catalog {
		if !a.remote {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}
