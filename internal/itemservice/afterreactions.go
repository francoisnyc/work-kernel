package itemservice

import (
	"context"
	"errors"

	"github.com/google/uuid"
)

// Move and create reactions (Jira part 2, step 2). Phase B: the contract.

// The outcomes of a move or create reaction for one trigger. Exactly one is
// recorded on the Item that declares the reaction, as a field-less event
// caused by the trigger.
const (
	ReactionApplied = "applied" // the target write committed (reaction_applied)
	ReactionFailed  = "failed"  // the target refused it (reaction_failed, Reason its code); not retried
	ReactionSkipped = "skipped" // it rightly did nothing (reaction_skipped, Reason says why)
)

// Why a reaction was skipped.
const (
	SkipConditionFalse = "condition_false" // its `when` does not hold
	SkipNotBy          = "not_by"          // the Item inside was created by someone else than `by` says
	SkipNotInFrom      = "not_in_from"     // the target is not in a state the reaction moves from
	SkipNoTarget       = "no_target"       // its link has no target
)

// ReactionRunRequest runs one move or create reaction for one trigger.
type ReactionRunRequest struct {
	// SourceID is the Item whose Type declares the reaction; Reaction its ID.
	SourceID uuid.UUID
	Reaction string
	// The triggering event. on: enter: the event of SourceID that entered
	// the state. on: created_inside: the create event (seq 1) of the Item
	// created inside SourceID.
	CauseItem uuid.UUID
	CauseSeq  int64
	// Attribution of the reaction's writes: the cause's correlation ID, and
	// its hop + 1.
	CorrelationID string
	Hop           int
}

// ReactionOutcome is what the reaction did, as recorded.
type ReactionOutcome struct {
	Outcome string // ReactionApplied, ReactionFailed or ReactionSkipped
	Reason  string // failed: the refusal's code; skipped: why
	// Target is the Item written (moved or created); uuid.Nil if none.
	Target uuid.UUID
	// Record is the outcome's event on SourceID.
	Record WriteResult
}

// PendingReaction is a move or create reaction not yet finished for one
// trigger: what the workflow needs to run it, opaque IDs only.
type PendingReaction struct {
	Reaction      string
	CauseItem     uuid.UUID
	CauseSeq      int64
	CorrelationID string
	Hop           int // of the cause
}

var errNotBuilt = errors.New("itemservice: not implemented (Phase B stub)")

// ReactMove runs a move reaction for one trigger: it judges the
// conditions, makes the move on the target under the key
// <reaction>:<source>:<cause>:<target>, and records the outcome on the
// source. Safe to call again: each write is keyed.
//
// Phase B stub: not implemented.
func (in *Internal) ReactMove(ctx context.Context, req ReactionRunRequest) (ReactionOutcome, error) {
	return ReactionOutcome{}, errNotBuilt
}

// ReactCreate runs a create reaction for one trigger, like ReactMove.
//
// Phase B stub: not implemented.
func (in *Internal) ReactCreate(ctx context.Context, req ReactionRunRequest) (ReactionOutcome, error) {
	return ReactionOutcome{}, errNotBuilt
}
