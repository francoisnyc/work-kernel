package hooks

import (
	"slices"

	"github.com/francoisnyc/work-kernel/internal/hookproto"
)

// DeclareRemoteForTest declares a remote operation for a test, such as a
// remote enrich operation (none is real yet). Only tests see it.
func DeclareRemoteForTest(action string, kinds []hookproto.Kind, inputs, outputs []string) {
	declareRemote(action, kinds, inputs, outputs)
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
