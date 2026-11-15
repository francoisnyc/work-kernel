package lifecycle

import (
	"fmt"
	"slices"
	"strings"

	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// The ops of the events a hold writes. A request enters no state; the
// resolution is an ordinary "transition" (allowed or corrected), or one of
// the other two.
const (
	OpTransitionRequested = "transition_requested" // a checked transition was asked for: a hold opens
	OpTransitionDenied    = "transition_denied"    // the check said no, or the requester no longer may
	OpTransitionFailed    = "transition_failed"    // the check could not answer (budget ran out, failed for good)
	OpHookFailed          = "hook_failed"          // an action hook gave up (budget ran out, key conflict, failed for good)
)

// Held is the Rejection code for a write an open hold refuses.
const Held Code = "held"

// Event is what the relay knows of one event: its op, and the state it
// entered ("" if none).
type Event struct {
	Op      string
	Entered string
}

// StartsWorkflow reports whether an event needs the Item's workflow to be
// running: it enters a state with behavior, or it opens a hold, whose check
// the workflow runs. The relay starts a workflow for such an event, and only
// notifies a running one for any other.
func StartsWorkflow(t *typedef.Type, ev Event) bool {
	if ev.Op == OpTransitionRequested {
		return true
	}
	return ev.Entered != "" && NeedsWorkflow(t, ev.Entered)
}

// EnrichHooks returns the enrich hooks that run on entering a state, in the
// order the Type declares them. Those of a state already left may be
// dropped: the check is the backstop.
func EnrichHooks(t *typedef.Type, state string) []typedef.Hook {
	return enterHooks(t, state, typedef.HookEnrich)
}

// ActionHooks returns the action hooks that run on entering a state, in the
// order the Type declares them. They are never dropped: once the state is
// entered, each runs to completion.
func ActionHooks(t *typedef.Type, state string) []typedef.Hook {
	return enterHooks(t, state, typedef.HookAction)
}

func enterHooks(t *typedef.Type, state string, kind typedef.HookKind) []typedef.Hook {
	var out []typedef.Hook
	for _, h := range t.Hooks {
		if h.On == typedef.OnEnter && h.State == state && h.Kind == kind {
			out = append(out, h)
		}
	}
	return out
}

// CheckFor returns the check on a transition. ok is false for a transition
// without one, which is made at once.
func CheckFor(t *typedef.Type, from, to string) (typedef.Hook, bool) {
	for _, h := range t.Hooks {
		if h.On == typedef.OnTransition && h.From == from && h.To == to {
			return h, true
		}
	}
	return typedef.Hook{}, false
}

// Hold is an open hold, as far as the rules need to know it: which check
// holds which requested transition.
type Hold struct {
	Hook     string
	From, To string
}

// UpdateDuringHold decides whether a write may change these fields while
// the hold is open. The fields the check reads are frozen: an approval binds
// the inputs it was asked about.
func UpdateDuringHold(t *typedef.Type, h Hold, fieldIDs []string) error {
	check, _ := t.Hook(h.Hook)
	var frozen []string
	for _, id := range fieldIDs {
		if slices.Contains(check.Reads, id) {
			frozen = append(frozen, id)
		}
	}
	if len(frozen) == 0 {
		return nil
	}
	slices.Sort(frozen)
	return &Rejection{
		Code: Held,
		Reason: fmt.Sprintf("fields %s are frozen while %s checks %s -> %s",
			strings.Join(frozen, ", "), h.Hook, h.From, h.To),
	}
}

// TransitionDuringHold decides whether the Item may move to `to` while the
// hold is open. Only a declared escape may; it cancels the hold.
func TransitionDuringHold(t *typedef.Type, h Hold, to string) error {
	if tr, ok := t.Transition(h.From, to); ok && tr.Escape {
		return nil
	}
	return &Rejection{
		Code:   Held,
		Reason: fmt.Sprintf("%s is checking %s -> %s; only an escape may leave %s now", h.Hook, h.From, h.To, h.From),
	}
}
