package itemservice

import (
	"errors"

	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// The etag rule (the etag step): an Item's content_seq, from which the
// public etag is derived, moves only when a write changes something a
// caller could overwrite. item_seq still counts every event.
//
// PHASE B STUB: the contract only. The rule and its use are Phase C.

// errUndecidedOp is what changesContent answers for an event kind nobody
// decided: the write fails rather than guess.
var errUndecidedOp = errors.New("itemservice: no etag decision for this event kind")

// changesContent reports whether the event (op, payload), written on an
// Item of Type ty, changes something a caller could overwrite. It reads
// the event's shape only, never a value. payload is the event's payload as
// appendEvent gets it (field lists as []string) or as it is stored (as
// []any). An op without a decision is an error: there is no default.
func changesContent(ty *typedef.Type, op string, payload map[string]any) (bool, error) {
	return false, errUndecidedOp
}

// DecidedOps returns every event kind the etag rule has a decision for,
// sorted.
func DecidedOps() []string {
	return nil
}
