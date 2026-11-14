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

// HasListeners reports whether anything listens to the events of Items of
// Type t. When nothing does, the item service writes no outbox row for them:
// an Item that keeps every default costs its row and its event, nothing more.
// It is decided per Type version, not per event: once some state has
// behavior, a workflow may be running, and it must hear every event.
//
// Today the only listener is the workflow, so this is "some state has a
// timer". Hooks, link reactions and subscriptions will widen it. Whatever
// they need must be added here, or their events are silently never sent.
//
// Two readers do not need the outbox and are not counted: Watch reads
// item_events directly, and search is a column of the Item's own row.
func HasListeners(t *typedef.Type) bool {
	for _, s := range t.States {
		if NeedsWorkflow(t, s.ID) {
			return true
		}
	}
	return false
}
