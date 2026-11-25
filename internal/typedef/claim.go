package typedef

import "slices"

// Track 1 item 5, step 2, "a claim that binds": a state's `claim` names the
// principal field that holds who claimed the Item, and a transition's
// `release` marks the move that gives the claim up. What they mean is
// packages policy's and itemservice's; this file refuses, at load, a
// claim nobody could make or that would strand an Item.

// ClaimIn returns the claim field of a state, "" when it has none.
func (t *Type) ClaimIn(state string) string {
	s, _ := t.State(state)
	return s.Claim
}

// checkClaim validates every state's claim and every transition's release:
//
//   - the field: declared (UNKNOWN_FIELD), a principal holding one person
//     (WRONG_FIELD_TYPE), that people write (WRITES_KERNEL_FIELD), and that
//     the state lists neither in frozen nor in requester_only (CONFLICT:
//     a claimed state already keeps it for its claimant);
//   - the state: not the initial state (CONFLICT: nobody moves an Item
//     into it), with at least one release out (MISSING_COUNTERPART: else a
//     claimant who leaves strands the Item);
//   - each move into it, which a person must make: not held by a check or
//     a held reaction (HELD_TRANSITION, Later), not a timer's
//     (TIMER_NOT_ALLOWED), and saying nothing of the claim field itself in
//     given, sets or clears (CONFLICT: the kernel records the mover);
//   - a release: out of a claimed state (KEY_NOT_APPLICABLE), to another
//     state (CONFLICT), clearing the claim field (MISSING_COUNTERPART).
//
// A move a reaction makes into a claimed state is refused on the reaction
// (checkReaction, checkMoveInto), as a held target is.
func (t *Type) checkClaim(r reporter) {
	// refused: the states whose claim is itself refused; their releases
	// are not judged against it (one problem, no cascade).
	refused := map[string]bool{}
	for i, s := range t.States {
		if s.Claim == "" {
			continue
		}
		refused[s.ID] = true
		p := at("states", i, "claim")
		f, ok := t.Field(s.Claim)
		switch {
		case !ok:
			r.fail(CodeUnknownField, p, "Declare the field, or name another", "state %s: claim field %q is not declared", s.ID, s.Claim)
			continue
		case f.Type != Principal || f.Multiple:
			r.fail(CodeWrongFieldType, p, "Name a principal field that holds one person",
				"state %s: claim field %s must be a principal field holding one person", s.ID, s.Claim)
			continue
		case f.WritableBy == WritableByKernel:
			r.fail(CodeWritesKernelField, p, "Name a field people write: a claim is its claimant's write",
				"state %s: claim field %s is writable_by kernel; nobody could claim", s.ID, s.Claim)
			continue
		case slices.Contains(s.Frozen, s.Claim):
			r.fail(CodeConflict, p, "Remove it from frozen: a claimed state already keeps the field from every write",
				"state %s: claim field %s is also frozen there", s.ID, s.Claim)
			continue
		case slices.Contains(s.RequesterOnly, s.Claim):
			r.fail(CodeConflict, p, "Remove it from requester_only: the field is its claimant's, not the requester's",
				"state %s: claim field %s is also requester_only there", s.ID, s.Claim)
			continue
		case t.InitialState == s.ID:
			r.fail(CodeConflict, p, "Claim in a state Items are moved into, not the one they start in",
				"state %s: the initial state may not be claimed; nobody moves an Item into it", s.ID)
			continue
		}
		refused[s.ID] = false
		released := false
		for j, tr := range t.Transitions {
			q := at("transitions", j)
			name := tr.From + " -> " + tr.To
			if tr.From == s.ID && tr.Release {
				released = true
			}
			if tr.To != s.ID || tr.From == s.ID || t.ClaimIn(tr.From) == s.Claim {
				continue
			}
			// A move into the claimed state: a claim.
			if h, held := t.heldBy(tr.From, tr.To); held {
				r.fail(CodeHeldTransition, q, "Claim by a move that is not held",
					"transition %s: it claims (state %s has claim %s) and is held by %s; a held move may not claim (Later)", name, s.ID, s.Claim, h)
				continue
			}
			from, _ := t.State(tr.From)
			if (from.Timer != nil && from.Timer.To == tr.To) || (from.At != nil && from.At.To == tr.To) {
				r.fail(CodeTimerNotAllowed, q, "Let the timer move it to a state that is not claimed",
					"transition %s: state %s's timer makes this move into %s, which is claimed; a timer claims nothing", name, tr.From, s.ID)
				continue
			}
			// A slice of pairs, not a map: Go iterates a map in random
			// order, and the problems must come out in the same order.
			for _, l := range []struct {
				key  string
				list []string
			}{{"given", tr.Given}, {"clears", tr.Clears}} {
				if k := slices.Index(l.list, s.Claim); k >= 0 {
					r.fail(CodeConflict, at(q, l.key, k), "Remove it: the kernel records whoever makes the move",
						"transition %s: %s names %s, the claim field of state %s; the move itself records its claimant", name, l.key, s.Claim, s.ID)
				}
			}
			if _, set := tr.Sets[s.Claim]; set {
				r.fail(CodeConflict, at(q, "sets"), "Remove it: the kernel records whoever makes the move",
					"transition %s: sets names %s, the claim field of state %s; the move itself records its claimant", name, s.Claim, s.ID)
			}
		}
		if !released {
			r.fail(CodeMissingCounterpart, p, "Add a move out of the state with release: true that clears the claim field",
				"state %s: it is claimed and no move out of it is a release; a claimant who leaves would strand the Item", s.ID)
		}
	}
	for j, tr := range t.Transitions {
		if !tr.Release {
			continue
		}
		q := at("transitions", j, "release")
		name := tr.From + " -> " + tr.To
		claim := t.ClaimIn(tr.From)
		switch {
		case refused[tr.From]:
		case claim == "":
			r.fail(CodeKeyNotApplicable, q, "Remove release, or add claim to the state it leaves",
				"transition %s: release, but state %s has no claim", name, tr.From)
		case tr.From == tr.To:
			r.fail(CodeConflict, q, "Release into another state", "transition %s: a release must leave the claimed state", name)
		case !slices.Contains(tr.Clears, claim):
			r.fail(CodeMissingCounterpart, q, "Add the claim field to the transition's clears",
				"transition %s: a release must clear the claim field %s", name, claim)
		}
	}
}
