package itemservice

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/item"
)

// Link reactions (proof of concept, Step 2, part 2; docs/design.md).
//
// A held reaction decides a hold: the workflow calls React, which writes the
// reaction's `add` on the Item the source links to (one write, one Item: the
// target), then resolves the source's hold through ResolveHold, allow if
// React committed, deny with DenyReason(err) if the target refused. React
// commits only while that very hold is open (it locks the source FOR SHARE
// and the target FOR UPDATE, in UUID order), so a deduction never outlives
// its hold.
//
// Undo reverses a committed reaction write by the delta it recorded
// (item_event_values.field_deltas), under the key <reservation key>:<undo>:
// once a hold closes any other way than allowed, and when the source leaves
// the state the hold entered by the undo's trigger (approved -> withdrawn).
// Without a committed reservation it writes nothing.

// Rejection codes of reactions, link resolution and invariants.
const (
	CodeInvariantFailed     = "invariant_failed"      // a write would break an invariant; metadata invariant
	CodeLinkUnresolved      = "link_unresolved"       // entering a resolve state found 0 or >1 targets; metadata link_type, reason
	CodeReactionNotAccepted = "reaction_not_accepted" // the target's Type version does not accept the reaction for those fields
	CodeNotUndoable         = "not_undoable"          // Undo of a hold that is open, or allowed and not left by the undo's trigger
)

// LinkNotWritableError is an ErrForbidden: the public door may not add or
// remove a link of a type only the kernel writes (writable_by: kernel).
type LinkNotWritableError struct {
	LinkType string
}

func (e *LinkNotWritableError) Error() string {
	return fmt.Sprintf("%v: links %s are set by the kernel only", ErrForbidden, e.LinkType)
}
func (e *LinkNotWritableError) Unwrap() error { return ErrForbidden }

// ReactRequest asks for a held reaction's write on its target. The key is
// derived by the kernel: <reaction>:<source>:<requested_seq>:<target>.
type ReactRequest struct {
	Principal item.Principal // reaction:<type>:<id>, a held reaction of the source's Type
	SourceID  uuid.UUID
	HoldID    uuid.UUID // the hold the reaction decides; React commits only while it is open
	// Attribution: originating_hook reaction:<type>@v<n>:<id>, causation_id
	// the hold's transition_requested event, hop that event's hop + 1.
	Attribution item.Attribution
}

// ReactResult names the write React made on the target, or would have
// made: a retry returns the first result.
type ReactResult struct {
	WriteResult           // the target's event
	Target      uuid.UUID // the Item the reaction wrote
	Key         string    // the reservation key
}

// React applies a held reaction's write to its target.
//
// Phase B stub: not implemented yet.
func (in *Internal) React(ctx context.Context, req ReactRequest) (ReactResult, error) {
	return ReactResult{}, errors.New("itemservice: React is not implemented")
}

// UndoRequest asks to reverse the reservation a held reaction made for a
// hold.
type UndoRequest struct {
	Principal item.Principal // reaction:<type>:<id>, the undo of the hold's reaction
	SourceID  uuid.UUID
	HoldID    uuid.UUID // whose reservation to reverse: closed non-allow, or the hold that entered the state left
	// Attribution: originating_hook reaction:<type>@v<n>:<id>, causation_id
	// the event that closed the hold (or left the state), hop + 1.
	Attribution item.Attribution
}

// UndoResult says what Undo did. Undone is false when there was nothing to
// reverse (no committed reservation): then nothing was written.
type UndoResult struct {
	WriteResult // the target's refund event, when Undone
	Undone      bool
	Target      uuid.UUID
	Key         string // <reservation key>:<undo id>
}

// Undo reverses a held reaction's committed write, at most once per
// reservation, whichever path asks.
//
// Phase B stub: not implemented yet.
func (in *Internal) Undo(ctx context.Context, req UndoRequest) (UndoResult, error) {
	return UndoResult{}, errors.New("itemservice: Undo is not implemented")
}

// ReactionFailedRequest records on the source that a reaction gave up.
type ReactionFailedRequest struct {
	IdempotencyKey string // reaction_failed:<the reaction's own key>
	Principal      item.Principal
	SourceID       uuid.UUID
	Reason         string // a code
	Attribution    item.Attribution
}

// ReactionFailed writes reaction_failed on the source (no field or state
// change). It is not retried: someone must look.
//
// Phase B stub: not implemented yet.
func (in *Internal) ReactionFailed(ctx context.Context, req ReactionFailedRequest) (WriteResult, error) {
	return WriteResult{}, errors.New("itemservice: ReactionFailed is not implemented")
}

// DenyReason turns a target's refusal of a held reaction into the hold's
// deny reason: an invariant's ID for INVARIANT_FAILED, else the refusal's
// code. ok is false for an error that is not a refusal (retry it).
//
// Phase B stub: not implemented yet.
func DenyReason(err error) (reason string, ok bool) {
	return "", false
}

// PendingUndo is an undo not yet done: a reservation committed for a hold
// that closed non-allow, or whose state was left by the undo's trigger.
type PendingUndo struct {
	Reaction      string    // the undo reaction's ID
	HoldID        uuid.UUID // the hold whose reservation it reverses
	CauseSeq      int64     // the event that closed the hold, or left the state
	CorrelationID string
	Hop           int // of that event
}
