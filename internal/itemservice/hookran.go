package itemservice

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/item"
)

// Field-less actions and applied reactions (proof of concept, Step 2,
// part 3).
//
// An action that makes nothing to write back (email.notify: zero writes)
// records that it ran with hook_ran: an event on the Item that names the
// hook, and for an acting timer's run the timer and the run, and changes no
// field and no state. So it is exempt from frozen fields and from ErrHeld
// by construction (both are about named fields and moves). It is written
// under the hook's own key: a retry records nothing twice, and it is the
// proof the action ran, as a write-back is for an action that writes.
//
// After a committed undo, the Undo activity records reaction_applied on the
// source: the reaction, and the target event it wrote. The kernel writes it
// only if the undo's write committed, so an action hook on that event
// (on: reaction) runs only when something was given back.

// HookRanRequest records that an action hook with no writes ran.
type HookRanRequest struct {
	IdempotencyKey string // the hook's key: hook:<item>:<entry>:<hook>, or timer:<item>:<timer>:<entry>:<n>
	Principal      item.Principal
	ItemID         uuid.UUID
	Timer          string // the acting timer that ran it, if any
	Run            int    // that timer's run, from 1; 0 without a timer
	Attribution    item.Attribution
}

// HookRan writes hook_ran (no field, no state change).
//
// Phase B stub: not implemented yet.
func (in *Internal) HookRan(ctx context.Context, req HookRanRequest) (WriteResult, error) {
	return WriteResult{}, errors.New("itemservice: HookRan is not implemented")
}

// ReactionAppliedRequest records on the source that an undo committed.
type ReactionAppliedRequest struct {
	Principal   item.Principal // the undo reaction
	SourceID    uuid.UUID
	HoldID      uuid.UUID // whose reservation the undo reversed
	Attribution item.Attribution
}

// ReactionApplied writes reaction_applied on the source, under the key
// <refund key>:applied, only if the undo's write committed (else
// not_applied, and nothing is written). Its payload names the reaction and
// the target event the undo wrote.
//
// Phase B stub: not implemented yet.
func (in *Internal) ReactionApplied(ctx context.Context, req ReactionAppliedRequest) (WriteResult, error) {
	return WriteResult{}, errors.New("itemservice: ReactionApplied is not implemented")
}

// CodeNotApplied refuses reaction_applied for an undo that did not commit.
const CodeNotApplied = "not_applied"
