package kernel

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/lifecycle"
)

// TransitionRequest moves an Item to another lifecycle state.
type TransitionRequest struct {
	IdempotencyKey string
	Principal      item.Principal
	ItemID         uuid.UUID
	ToState        string
}

// Transition moves an Item to another state if its Type allows it, and
// records a "transition" event. An illegal move returns ErrRejected with the
// reason and writes nothing.
func (k *Kernel) Transition(ctx context.Context, req TransitionRequest) (WriteResult, error) {
	if req.IdempotencyKey == "" || req.Principal.Actor == "" || req.ToState == "" {
		return WriteResult{}, fmt.Errorf("%w: idempotency key, actor and target state are required", ErrInvalid)
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
		ty, err := loadType(ctx, tx, it.TypeID, it.TypeVersion)
		if err != nil {
			return WriteResult{}, err
		}

		// All lifecycle logic lives in lifecycle.Transition; the kernel only
		// asks and records. Two %w verbs wrap both errors, so callers can match
		// ErrRejected with errors.Is and read the *lifecycle.Rejection with errors.As.
		next, err := lifecycle.Transition(ty, it.State, it.Fields, req.ToState)
		if err != nil {
			return WriteResult{}, fmt.Errorf("%w: %w", ErrRejected, err)
		}

		itemSeq := it.ItemSeq + 1
		_, err = tx.Exec(ctx,
			`update items set state = $2, item_seq = $3, updated_at = now() where id = $1`,
			req.ItemID, next.State, itemSeq)
		if err != nil {
			return WriteResult{}, fmt.Errorf("update item: %w", err)
		}

		payload := map[string]any{"from": it.State, "to": next.State}
		return appendEvent(ctx, tx, req.ItemID, itemSeq, req.Principal, "transition", payload, nil)
	})
}
