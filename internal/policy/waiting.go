package policy

import (
	"cmp"
	"slices"
	"strings"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// "Waiting on me" (docs/roadmap.md, track 1, item 5, step 1): which open
// Items wait on whom, read from the moves out of an Item's state in its
// own Type version. The functions below are the one place the rule is
// written. The item service's waiting_on filter builds its SQL from
// WaitingMoves and NamedEntries, and a test holds the view to WaitsOn and
// WaitsOnRole; the reading says the same in plain language.
//
// The rule (the owner's, Phase A):
//
//   - a move counts when its `allowed` names someone: a role, a person
//     (principal:user:…, principal:agent:…) or a field (field:<id>);
//   - an empty `allowed` is nobody's queue ("anyone may" is not "someone
//     must"), and a move for nobody, for the creator only or for the
//     kernel's own actors does not count;
//   - `creator` never makes an Item wait on its requester, even beside a
//     role: the Item waits on the role's holders;
//   - a state in category done or canceled never waits: a reopen is not
//     work;
//   - guards, required and given fields are not looked at: the move may
//     still be refused when it is asked for.

// The two state categories that never wait (typedef's fixed list).
const (
	categoryDone     = "done"
	categoryCanceled = "canceled"
)

// NamedEntries returns the entries of a move's `allowed` that name someone
// an Item can wait on: role:, field: and principal: entries, leaving out
// creator and the kernel's own actors (the timer, a hook). A move for
// nobody names no one.
func NamedEntries(tr typedef.Transition) []string {
	if slices.Contains(tr.Allowed, typedef.AllowedNobody) {
		return nil
	}
	var named []string
	for _, e := range tr.Allowed {
		if e == typedef.AllowedCreator {
			continue
		}
		// CutPrefix returns the rest and whether the prefix was there.
		if p, ok := strings.CutPrefix(e, typedef.AllowedPrincipalPrefix); ok && item.IsKernelActor(p) {
			continue
		}
		named = append(named, e)
	}
	return named
}

// WaitingMoves returns the moves out of state, in ty, that count as work
// waiting on someone, in the Type's order: none for a state in category
// done or canceled, or one ty does not have.
func WaitingMoves(ty *typedef.Type, state string) []typedef.Transition {
	st, ok := ty.State(state)
	if !ok || st.Category == categoryDone || st.Category == categoryCanceled {
		return nil
	}
	var moves []typedef.Transition
	for _, tr := range ty.Transitions {
		// Giving a claim up is not work waiting on anyone.
		if tr.From == state && !tr.Release && len(NamedEntries(tr)) > 0 {
			moves = append(moves, tr)
		}
	}
	return moves
}

// WaitsOn reports whether an Item of ty, in state, waits on who: one of
// its waiting moves names them, and MayTransition would let them make it
// for who they are. It shares MayTransition's matching, so `except`, the
// rule for agents (the agent and its user both pass) and the rule for
// customers (never: they take only the creator's moves) are the move's
// own. The kernel's actors wait on nothing.
func WaitsOn(ty *typedef.Type, state string, who Who, it ItemFacts) bool {
	if item.IsKernelActor(who.Actor) {
		return false
	}
	st, _ := ty.State(state)
	for _, tr := range WaitingMoves(ty, state) {
		// tr is a copy (range copies each element), so narrowing its list
		// to the named entries changes nothing in the Type.
		tr.Allowed = NamedEntries(tr)
		if MayTransition(tr, who, it) && MayLeaveClaimed(st, tr, who, it) {
			return true
		}
	}
	return false
}

// WaitsOnRole reports whether an Item of ty, in state, is in the queue of
// the role (a bare name: finance): one of its waiting moves names it.
// `except` is not asked: it is judged for a person, in WaitsOn.
func WaitsOnRole(ty *typedef.Type, state, role string) bool {
	// A claimed Item waits on its claimant, not on the team: a role's queue
	// holds what nobody has claimed yet.
	if st, _ := ty.State(state); role == "" || st.Claim != "" {
		return false
	}
	for _, tr := range WaitingMoves(ty, state) {
		if slices.Contains(tr.Allowed, typedef.AllowedRolePrefix+role) {
			return true
		}
	}
	return false
}

// MayLeaveClaimed answers the claim's part of "may this principal make
// this move?" (track 1 item 5, step 2), asked besides MayTransition. st is
// the state the move leaves. While an Item is in a state with `claim`, a
// move out of it is its claimant's only: the principal's effective user
// (the user an agent acts for, else the actor) is the person the claim
// field holds. A claim field that holds nobody lets nobody through.
//
// Three moves pass whoever asks: one out of a state with no claim; a
// release, which `allowed` alone decides; and the kernel's own (the timer,
// a hook), so that an escalation can take a claim back.
func MayLeaveClaimed(st typedef.State, tr typedef.Transition, who Who, it ItemFacts) bool {
	if st.Claim == "" || tr.Release || item.IsKernelActor(who.Actor) {
		return true
	}
	claimant, _ := it.Fields[st.Claim].(string)
	return claimant != "" && claimant == cmp.Or(who.OnBehalfOf, who.Actor)
}

// MayMove answers "may this principal make this move, for who they are?":
// the transition's `allowed` and `except` (MayTransition) and the claim of
// the state it leaves (MayLeaveClaimed). It is the one answer the item
// service's authorize and an Item's Moves both read. Guards, required and
// given fields are not its business.
func MayMove(st typedef.State, tr typedef.Transition, who Who, it ItemFacts) bool {
	return MayTransition(tr, who, it) && MayLeaveClaimed(st, tr, who, it)
}

// Moves lists the to-states of the transitions out of state that who may
// make (MayMove), in the Type's order: never nil, empty when there are
// none. An agent session's powers and scopes are not its business: the
// item service checks those on the write.
func Moves(ty *typedef.Type, state string, who Who, it ItemFacts) []string {
	st, _ := ty.State(state)
	moves := []string{}
	for _, tr := range ty.Transitions {
		if tr.From == state && MayMove(st, tr, who, it) {
			moves = append(moves, tr.To)
		}
	}
	return moves
}
