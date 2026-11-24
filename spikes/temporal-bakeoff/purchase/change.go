package purchase

import "go.temporal.io/sdk/workflow"

// Deployed stands in for "which version of this package is running", so the
// bake-off can start the program as it was before the owner's change, with
// the change made carelessly, and with the change made the way Temporal
// documents (ptapp and ptreplay take --purchase-code). A real team has only
// the last: the first two exist to measure what the change does to
// requests in flight. Set once at start, never while workflows run.
var Deployed = "patched"

// cfoStepApplies says whether this execution takes the CFO step.
//
// Changing a workflow's code changes what a replay of its recorded history
// does, and Temporal fails a replay that no longer matches. GetVersion is
// the documented fix: it records, in each execution's history, which
// version of this branch it took. An execution that passed this point
// before the change keeps the old branch on replay; one that reaches it
// afterwards (new, or in flight) takes the new one.
func cfoStepApplies(ctx workflow.Context) bool {
	switch Deployed {
	case "before":
		return false
	case "naive": // the change without versioning: do not do this
		return true
	}
	return workflow.GetVersion(ctx, "cfo-after-finance", workflow.DefaultVersion, 1) == 1
}
