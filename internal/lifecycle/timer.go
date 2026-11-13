package lifecycle

import "github.com/francoisnyc/work-kernel/internal/typedef"

// TimerFor returns the timer an Item of Type t runs while it is in the given
// state. The second value is false when the state has no timer or is not a
// state of the Type. Like Transition, it is pure, so the workflow may call it.
func TimerFor(t *typedef.Type, state string) (typedef.Timer, bool) {
	s, ok := t.State(state)
	if !ok || s.Timer == nil {
		return typedef.Timer{}, false
	}
	return *s.Timer, true
}

// NeedsWorkflow reports whether an Item entering the given state needs a
// workflow: the state has behavior that plays out over time. It is the one
// place that rule lives; the relay asks it to choose between starting a
// workflow and only notifying one that already runs.
//
// Today "behavior" means a timer. Holds, approvals and link reactions will
// extend this function, not its callers.
func NeedsWorkflow(t *typedef.Type, state string) bool {
	_, hasTimer := TimerFor(t, state)
	return hasTimer
}
