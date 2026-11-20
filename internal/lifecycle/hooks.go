package lifecycle

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"cel.dev/cel-go/common/types"

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
	From    string // the state left, for a transition; an after reaction depends on it
	// ClosesHold: the event closes a hold (resolves it, or an escape
	// cancels it); Allowed: by applying the requested move.
	ClosesHold, Allowed bool
}

// StartsWorkflow reports whether an event needs the Item's workflow to be
// running: it enters a state with behavior, or it opens a hold, whose check
// the workflow runs. The relay starts a workflow for such an event, and only
// notifies a running one for any other.
//
// Entering a state by an after reaction's trigger (approved -> withdrawn)
// starts it too: the run that saw the approval may have completed. So does
// closing a held reaction's hold non-allow: the run may be gone.
func StartsWorkflow(t *typedef.Type, ev Event) bool {
	if ev.Op == OpTransitionRequested {
		return true
	}
	// A hold closed any other way than allowed (denied, failed, canceled
	// by an escape) on a Type whose held reaction holds a move from this
	// state: the reaction may have committed, and its undo must run even
	// if the run that took the request is gone.
	if ev.ClosesHold && !ev.Allowed {
		for _, r := range t.Reactions {
			if r.Kind == typedef.ReactionHeld && slices.Contains(r.From, ev.From) {
				return true
			}
		}
	}
	// An undo applied runs its reaction hooks (the refund email), even in a
	// final state with no other behavior.
	if ev.Op == OpReactionApplied {
		for _, h := range t.Hooks {
			if h.On == typedef.OnReaction {
				return true
			}
		}
		return false
	}
	if ev.Entered == "" {
		return false
	}
	if _, ok := UndoOnEntry(t, ev.From, ev.Entered); ok {
		return true
	}
	return NeedsWorkflow(t, ev.Entered)
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
	// The hold's decider is a check hook or a held reaction (IDs are
	// shared): either way, what it reads is frozen.
	reads := heldReads(t, h.Hook)
	var frozen []string
	for _, id := range fieldIDs {
		if slices.Contains(reads, id) {
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

// heldReads are the fields a hold's decider reads: a check's reads, or a
// held reaction's (the fields its add expressions read).
func heldReads(t *typedef.Type, decider string) []string {
	if check, ok := t.Hook(decider); ok {
		return check.Reads
	}
	r, _ := t.Reaction(decider)
	return r.Reads
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

// --- link reactions and invariants (proof of concept, Step 2, part 2) ---

// OpReactionFailed is the op of the event that records, on the source, that
// a reaction could not be applied (an undo refused by its target, say). It
// is written once and not retried; someone must look (Later: an alert).
const OpReactionFailed = "reaction_failed"

// InvariantFailed is the Rejection code for a write that would break one of
// the Type's invariants; Rejection.Invariant names it.
const InvariantFailed Code = "invariant_failed"

// CheckInvariants decides whether fields (as a write would leave them) keep
// every invariant of the Type. The first that does not hold, in the order
// declared, refuses: a rule that cannot be evaluated does not hold. The
// reason names the invariant, never a value.
func CheckInvariants(t *typedef.Type, fields map[string]any) error {
	if len(t.Invariants) == 0 {
		return nil
	}
	vars := make(map[string]any, len(t.Fields))
	addFieldVars(vars, t, fields)
	for _, inv := range t.Invariants {
		out, _, err := inv.Program.Eval(vars)
		holds, isBool := false, false
		if err == nil {
			holds, isBool = out.Value().(bool)
		}
		if !isBool || !holds {
			return &Rejection{
				Code: InvariantFailed, Invariant: inv.ID,
				Reason: fmt.Sprintf("the write would break invariant %s of %s v%d: %s", inv.ID, t.ID, t.Version, inv.Rule),
			}
		}
	}
	return nil
}

// HeldReaction returns the held reaction on the transition from -> to, if
// any: asking for that transition opens a hold the reaction decides.
func HeldReaction(t *typedef.Type, from, to string) (typedef.Reaction, bool) {
	for _, r := range t.Reactions {
		if r.Kind == typedef.ReactionHeld && r.State == to && slices.Contains(r.From, from) {
			return r, true
		}
	}
	return typedef.Reaction{}, false
}

// UndoOf returns the reaction that undoes the held reaction heldID.
func UndoOf(t *typedef.Type, heldID string) (typedef.Reaction, bool) {
	for _, r := range t.Reactions {
		if r.Kind == typedef.ReactionAfter && r.Undoes == heldID {
			return r, true
		}
	}
	return typedef.Reaction{}, false
}

// UndoOnEntry returns the after reaction an Item entering `to` from `from`
// triggers (approved -> withdrawn runs refund), if any.
func UndoOnEntry(t *typedef.Type, from, to string) (typedef.Reaction, bool) {
	for _, r := range t.Reactions {
		if r.Kind == typedef.ReactionAfter && r.State == to && (len(r.From) == 0 || slices.Contains(r.From, from)) {
			return r, true
		}
	}
	return typedef.Reaction{}, false
}

// ReactionAdds evaluates a held reaction's add expressions over the
// source's fields: target field -> the amount to add. An expression that
// cannot be evaluated (a null field) is an error, never zero.
//
// The expressions read fields only (typedef refuses anything else), and
// only those in r.Reads are set: unset is null, a number read back from
// JSON (float64) is an int, as the schema says.
func ReactionAdds(r typedef.Reaction, fields map[string]any) (map[string]int64, error) {
	vars := make(map[string]any, len(r.Reads))
	for _, id := range r.Reads {
		v := fields[id]
		switch {
		case v == nil:
			vars[id] = types.NullValue
		default:
			if n, ok := typedef.AsInt(v); ok {
				v = n
			}
			vars[id] = v
		}
	}
	out := make(map[string]int64, len(r.Programs))
	for _, field := range slices.Sorted(maps.Keys(r.Programs)) {
		val, _, err := r.Programs[field].Eval(vars)
		if err != nil {
			return nil, fmt.Errorf("reaction %s: add %s cannot be evaluated: %v", r.ID, field, err)
		}
		n, ok := typedef.AsInt(val.Value())
		if !ok {
			return nil, fmt.Errorf("reaction %s: add %s is not a whole number", r.ID, field)
		}
		out[field] = n
	}
	return out, nil
}

// --- acting timers and field-less actions (proof of concept, Step 2, part 3) ---

// The ops of a field-less action's record and of a committed undo's record
// on its source.
const (
	OpHookRan         = "hook_ran"         // an action hook with no writes ran (for an acting timer: which run)
	OpReactionApplied = "reaction_applied" // a reaction committed (an undo, a move, a create): which, and where
	// OpReactionSkipped: a move or create reaction rightly did nothing for
	// one trigger (Jira part 2, step 2): its condition was false, the
	// creator was someone else, the target was not in `from`, or there was
	// no target.
	OpReactionSkipped = "reaction_skipped"
)

// ActingTimers returns the acting timers of a state, in the order declared.
func ActingTimers(t *typedef.Type, state string) []typedef.ActingTimer {
	s, ok := t.State(state)
	if !ok {
		return nil
	}
	// Those at an instant have no schedule: ActingAtTimers.
	var out []typedef.ActingTimer
	for _, a := range s.Acting {
		if a.At == "" {
			out = append(out, a)
		}
	}
	return out
}

// ReactionHooks returns the action hooks that run when the Item records
// that its reaction `reaction` was applied (on: reaction), in the order
// declared.
func ReactionHooks(t *typedef.Type, reaction string) []typedef.Hook {
	var out []typedef.Hook
	for _, h := range t.Hooks {
		if h.On == typedef.OnReaction && h.Reaction == reaction && h.Kind == typedef.HookAction {
			out = append(out, h)
		}
	}
	return out
}
