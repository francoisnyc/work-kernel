package kernel

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/lifecycle"
)

// TransitionRequest moves an Item to another lifecycle state. Fields is an
// optional patch with the same meaning as in UpdateRequest: it is applied
// first, and the transition's guards see the fields as they are after it.
type TransitionRequest struct {
	IdempotencyKey string
	Principal      item.Principal
	ItemID         uuid.UUID
	ToState        string
	Fields         map[string]any // optional; keyed by stable field ID
	// ExpectedItemSeq is the same optional if-match as on UpdateRequest.
	ExpectedItemSeq int64
	Attribution     item.Attribution // optional; what caused this write
}

// Transition moves an Item to another state if its Type allows it, and
// records a "transition" event. With a Fields patch, the patch and the move
// are one write: one row lock, one event. An illegal move or an invalid patch
// returns ErrRejected with the reason and writes nothing.
func (k *Kernel) Transition(ctx context.Context, req TransitionRequest) (WriteResult, error) {
	if req.IdempotencyKey == "" || req.Principal.Actor == "" || req.ToState == "" {
		return WriteResult{}, fmt.Errorf("%w: idempotency key, actor and target state are required", ErrInvalid)
	}
	if err := checkOptions(req.ExpectedItemSeq, req.Attribution); err != nil {
		return WriteResult{}, err
	}
	hash, err := requestHash("transition", req)
	if err != nil {
		return WriteResult{}, err
	}

	return k.write(ctx, req.Principal, req.IdempotencyKey, hash, func(tx pgx.Tx) (WriteResult, error) {
		// The decision depends on the current state and fields, so it is made
		// under the row lock: no other write can slip in between check and write.
		it, err := lockItem(ctx, tx, req.ItemID)
		if err != nil {
			return WriteResult{}, err
		}
		if err := checkExpectedSeq(it, req.ExpectedItemSeq); err != nil {
			return WriteResult{}, err
		}
		ty, err := loadType(ctx, tx, it.TypeID, it.TypeVersion)
		if err != nil {
			return WriteResult{}, err
		}

		// All lifecycle logic lives in lifecycle.Transition; the kernel only
		// asks and records. Two %w verbs wrap both errors, so callers can match
		// ErrRejected with errors.Is and read the *lifecycle.Rejection with errors.As.
		//
		// The guards are checked against the fields as the patch leaves them.
		if err := ty.CheckFields(req.Fields, true); err != nil {
			return WriteResult{}, fmt.Errorf("%w: %v", ErrRejected, err)
		}
		merged, set, cleared := applyPatch(it.Fields, req.Fields)
		next, err := lifecycle.Transition(ty, it.State, merged, req.ToState)
		if err != nil {
			return WriteResult{}, fmt.Errorf("%w: %w", ErrRejected, err)
		}

		itemSeq := it.ItemSeq + 1
		_, err = tx.Exec(ctx,
			`update items set state = $2, fields = $3, item_seq = $4, updated_at = now() where id = $1`,
			req.ItemID, next.State, merged, itemSeq)
		if err != nil {
			return WriteResult{}, fmt.Errorf("update item: %w", err)
		}

		// A plain transition keeps its two-key payload; the field lists appear
		// only when the request carried a patch.
		payload := map[string]any{"from": it.State, "to": next.State}
		if len(req.Fields) > 0 {
			payload["field_ids"] = sortedKeys(set)
			if len(cleared) > 0 {
				payload["cleared_field_ids"] = sortedKeys(cleared)
			}
		}
		return appendEvent(ctx, tx, req.ItemID, itemSeq, req.Principal, req.Attribution, "transition", payload, set)
	})
}
