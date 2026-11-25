package policy

import "github.com/francoisnyc/work-kernel/internal/typedef"

// "Waiting on me" (docs/roadmap.md, track 1, item 5, step 1): which open
// Items wait on whom, read from the moves out of an Item's state in its own
// Type version. The three functions below are the one place the rule is
// written; the item service's waiting_on filter is held to them by a test.
//
// STUBS (Phase B): the tests in waiting_test.go are the spec. Nothing is
// decided here yet.

// WaitingMoves returns the moves out of state, in ty, that count as work
// waiting on someone.
func WaitingMoves(ty *typedef.Type, state string) []typedef.Transition {
	return nil
}

// WaitsOn reports whether an Item of ty, in state, waits on who.
func WaitsOn(ty *typedef.Type, state string, who Who, it ItemFacts) bool {
	return false
}

// WaitsOnRole reports whether an Item of ty, in state, is in the queue of
// the role (a bare name: finance).
func WaitsOnRole(ty *typedef.Type, state, role string) bool {
	return false
}
