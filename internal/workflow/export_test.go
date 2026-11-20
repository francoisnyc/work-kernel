package workflow

import (
	"time"

	"go.temporal.io/sdk/workflow"
)

// ReactionActivityOptions exposes, to the tests, the options the workflow
// runs a queued reaction's activity with (RunAfterReaction), for a hook
// budget (zero: the default) and no per-attempt override: a test that
// bounds how long a lost attempt takes to come back derives the bound from
// these, not from a number of its own.
func ReactionActivityOptions(budget time.Duration) workflow.ActivityOptions {
	if budget == 0 {
		budget = DefaultHookBudget
	}
	return hookOptions(budget, 0)
}
