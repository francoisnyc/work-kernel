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
// "Behavior" means a timer, enrich hooks or action hooks. A hold is opened by an event
// that enters no state; StartsWorkflow covers it. Approvals and link
// reactions will extend these functions, not their callers.
func NeedsWorkflow(t *typedef.Type, state string) bool {
	_, hasTimer := TimerFor(t, state)
	return hasTimer || len(EnrichHooks(t, state)) > 0 || len(ActionHooks(t, state)) > 0
}

// HasListeners reports whether anything listens to the events of Items of
// Type t. When nothing does, the item service writes no outbox row for them:
// an Item that keeps every default costs its row and its event, nothing more.
// It is decided per Type version, not per event: once some state has
// behavior, a workflow may be running, and it must hear every event.
//
// Today the only listener is the workflow, so this is "some state has a
// timer, or the Type has hooks or reactions" (a check hook or a held
// reaction needs the workflow too, though it is entered by no state).
// Subscriptions will widen it. Whatever they need must be added here, or
// their events are silently never sent.
//
// Two readers do not need the outbox and are not counted: Watch reads
// item_events directly, and search is a column of the Item's own row.
func HasListeners(t *typedef.Type) bool {
	// Hooks and reactions run in the workflow. (A Type that only accepts
	// reactions needs none: the source's workflow writes to it.)
	if len(t.Hooks) > 0 || len(t.Reactions) > 0 {
		return true
	}
	for _, s := range t.States {
		if NeedsWorkflow(t, s.ID) {
			return true
		}
	}
	return false
}
