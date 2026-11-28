package lifecycle

import "github.com/francoisnyc/work-kernel/internal/typedef"

// What can move an Item out of a state, read from its own Type version
// (pli check, docs/roadmap.md, track 2). It is the one list of exits:
// pli check builds its graph from it, so the checker reads the same
// rules as the item service and the worker.

// ExitKind says what makes an exit.
type ExitKind string

const (
	// ExitMove is a declared transition, made by whoever its allowed lets in.
	ExitMove ExitKind = "move"
	// ExitTimer is the state's timer (`timer: {after, to}`).
	ExitTimer ExitKind = "timer"
	// ExitAt is the state's timer at a field's time (`at: {field, to}`).
	ExitAt ExitKind = "at"
	// ExitReaction is a move reaction that moves the Item itself (on:
	// created_inside, from this state).
	ExitReaction ExitKind = "reaction"
)

// Exit is one way out of a state.
type Exit struct {
	Kind ExitKind
	To   string
	// Transition is the index of the move in the Type's transitions;
	// -1 for every other kind.
	Transition int
	// Reaction is the reaction's id, for ExitReaction; empty otherwise.
	Reaction string
}

// Exits lists the ways out of state, in this order: the moves in the
// Type's order, the timer, the at-timer, then the move reactions in the
// Type's order. Never nil: empty when there are none, or when the Type
// has no such state. A move reaction that follows a link or moves
// children moves another Item, not this one, and is not listed.
func Exits(t *typedef.Type, state string) []Exit {
	return nil // Phase B stub
}
