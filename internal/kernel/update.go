package kernel

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/francoisnyc/work-kernel/internal/item"
)

// UpdateRequest changes field values on an Item. Fields is a patch: a value
// sets the field, nil clears it, and fields not mentioned are left alone.
type UpdateRequest struct {
	IdempotencyKey string
	Principal      item.Principal
	ItemID         uuid.UUID
	Fields         map[string]any // keyed by stable field ID
	// ExpectedItemSeq is an optional if-match: when set, the write happens only
	// if the Item is still at this item_seq, and fails with ErrStale otherwise.
	// Zero means "no check" (an Item's first item_seq is 1).
	ExpectedItemSeq int64
	Attribution     item.Attribution // optional; what caused this write
}

// Update applies a field patch to an Item and records an "update" event.
func (k *Kernel) Update(ctx context.Context, req UpdateRequest) (WriteResult, error) {
	if req.IdempotencyKey == "" || req.Principal.Actor == "" || len(req.Fields) == 0 {
		return WriteResult{}, fmt.Errorf("%w: idempotency key, actor and at least one field are required", ErrInvalid)
	}
	if err := checkOptions(req.ExpectedItemSeq, req.Attribution); err != nil {
		return WriteResult{}, err
	}
	hash, err := requestHash("update", req)
	if err != nil {
		return WriteResult{}, err
	}

	return k.write(ctx, req.Principal, req.IdempotencyKey, hash, func(tx pgx.Tx) (WriteResult, error) {
		// Validation needs the Item's Type, so it runs after the row is locked.
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
		if err := ty.CheckFields(req.Fields, true); err != nil {
			return WriteResult{}, fmt.Errorf("%w: %v", ErrRejected, err)
		}

		merged, set, cleared := applyPatch(it.Fields, req.Fields)

		itemSeq := it.ItemSeq + 1
		_, err = tx.Exec(ctx,
			`update items set fields = $2, item_seq = $3, updated_at = now() where id = $1`,
			req.ItemID, merged, itemSeq)
		if err != nil {
			return WriteResult{}, fmt.Errorf("update item: %w", err)
		}

		payload := map[string]any{"field_ids": sortedKeys(set)}
		if len(cleared) > 0 {
			payload["cleared_field_ids"] = sortedKeys(cleared)
		}
		return appendEvent(ctx, tx, req.ItemID, itemSeq, req.Principal, req.Attribution, "update", payload, set)
	})
}
