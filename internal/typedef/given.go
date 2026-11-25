package typedef

import (
	"slices"
	"strings"
)

// Track 1 item 3, "required means given by the mover": a transition's
// `given` lists fields of its `required` that the move's own patch must
// set. What that means is package lifecycle's; this file refuses, at load,
// a `given` that contradicts the Type or that no move could ever meet.

// givenOn returns what the transition from -> to asks to be given, if the
// Type declares it.
func (t *Type) givenOn(from, to string) []string {
	tr, ok := t.Transition(from, to)
	if !ok {
		return nil
	}
	return tr.Given
}

// checkGiven validates each transition's given, field by field:
//   - a declared field (UNKNOWN_FIELD), once (DUPLICATE), that the same
//     move requires (MISSING_COUNTERPART);
//   - one a patch may name: not a kernel field (WRITES_KERNEL_FIELD), not
//     frozen in the state the move leaves (WRITES_FROZEN_FIELD), not named
//     by the move's own allowed (CONFLICT), and not kept for the requester
//     in the state it leaves when the move excepts the requester
//     (CONFLICT);
//
// and the move as a whole: not held by a check or a held reaction
// (HELD_TRANSITION: which of a held move's two steps the patch belongs to
// is Later, as for clears and sets), and not one a state's timer makes
// (TIMER_NOT_ALLOWED: a timer gives no fields). A move a reaction makes is
// refused on the reaction (checkReaction, checkMoveInto).
func (t *Type) checkGiven(r reporter) {
	for i, tr := range t.Transitions {
		if len(tr.Given) == 0 {
			continue
		}
		p := at("transitions", i)
		name := tr.From + " -> " + tr.To
		from, _ := t.State(tr.From)
		seen := map[string]bool{}
		for j, id := range tr.Given {
			f, ok := t.Field(id)
			q := at(p, "given", j)
			switch {
			case !ok:
				r.fail(CodeUnknownField, q, "Declare the field, or remove it from given",
					"transition %s: given field %q is not declared", name, id)
			case seen[id]:
				r.fail(CodeDuplicate, q, "Remove the second one", "transition %s: gives %q twice", name, id)
			case !slices.Contains(tr.Required, id):
				r.fail(CodeMissingCounterpart, q, "Add it to the transition's required as well",
					"transition %s: gives %s, which its required does not list", name, id)
			case f.WritableBy == WritableByKernel:
				r.fail(CodeWritesKernelField, q, "Give a field people write, or remove it from given",
					"transition %s: gives %s, which only the kernel writes; no move's patch may name it", name, id)
			case slices.Contains(from.Frozen, id):
				r.fail(CodeWritesFrozenField, q, "Stop freezing it in the state the move leaves, or remove it from given",
					"transition %s: gives %s, which state %s freezes; no move's patch may name it", name, id, tr.From)
			case slices.Contains(tr.Allowed, AllowedFieldPrefix+id):
				r.fail(CodeConflict, q, "Give another field: the move may not set who may make it",
					"transition %s: gives %s, which its own allowed list names", name, id)
			case slices.Contains(from.RequesterOnly, id) && slices.Contains(tr.Except, AllowedCreator):
				r.fail(CodeConflict, q, "Let the requester make the move, or remove the field from requester_only or from given",
					"transition %s: gives %s, which state %s keeps for the requester (requester_only), and the move excepts the requester",
					name, id, tr.From)
			}
			seen[id] = true
		}
		if h, held := t.heldBy(tr.From, tr.To); held {
			r.fail(CodeHeldTransition, at(p, "given"), "Remove given, or ask for it on another transition that is not held",
				"transition %s: it asks for fields given in the move and is held by %s; a held transition may not (Later)", name, h)
			continue
		}
		timed := (from.Timer != nil && from.Timer.To == tr.To) || (from.At != nil && from.At.To == tr.To)
		if timed {
			r.fail(CodeTimerNotAllowed, at(p, "given"), "Remove given, or let the timer use another transition",
				"transition %s: state %s's timer makes this move, and a timer gives no fields; it can never give %s",
				name, tr.From, strings.Join(tr.Given, ", "))
		}
	}
}
