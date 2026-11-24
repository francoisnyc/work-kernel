package lifecycle

import "github.com/francoisnyc/work-kernel/internal/typedef"

// An effect re-checks what caused it (docs/design.md, "Architecture
// style"). A hook hangs off the lifecycle, so the event that causes it is
// decided by the Type alone: this file says which event that is.

// Cause is the shape of the event a hook names as its cause: IDs and state
// names, never a value.
type Cause struct {
	Op string
	// Entered is the state the event entered (a create, or a transition);
	// empty for an event that entered none.
	Entered string
	// Reaction is a reaction_applied event's reaction, <type>:<id>.
	Reaction string
}

// BoundTo reports whether c is the event hook h is bound to:
//
//   - on: enter      the event that entered the hook's state
//   - on: reaction   the reaction_applied event of the hook's reaction
//   - on: timer      the event that entered a state with an acting timer
//     that runs the hook (which timer, and which run: ActingTimer)
//   - on: transition the request for a transition (its hold says which)
func BoundTo(t *typedef.Type, h typedef.Hook, c Cause) bool {
	switch h.On {
	case typedef.OnTransition:
		return c.Op == OpTransitionRequested
	case typedef.OnReaction:
		return c.Op == OpReactionApplied && c.Reaction == typedef.ReactionKey(t.ID, h.Reaction)
	case typedef.OnTimer:
		s, ok := t.State(c.Entered)
		if !ok {
			return false
		}
		for _, a := range s.Acting {
			if a.Hook == h.ID {
				return true
			}
		}
		return false
	default:
		return c.Entered != "" && c.Entered == h.State
	}
}

// ActingTimer returns the acting timer named id of a state, of either
// sort (on a schedule, or at an instant).
func ActingTimer(t *typedef.Type, state, id string) (typedef.ActingTimer, bool) {
	s, ok := t.State(state)
	if !ok {
		return typedef.ActingTimer{}, false
	}
	for _, a := range s.Acting {
		if a.ID == id {
			return a, true
		}
	}
	return typedef.ActingTimer{}, false
}
