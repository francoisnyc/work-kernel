package hooks

import "github.com/francoisnyc/work-kernel/internal/hookproto"

// DeclareRemoteForTest declares a remote operation for a test, such as a
// remote enrich operation (none is real yet). Only tests see it.
func DeclareRemoteForTest(action string, kinds []hookproto.Kind, inputs, outputs []string) {
	declareRemote(action, kinds, inputs, outputs)
}
