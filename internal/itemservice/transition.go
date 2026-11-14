package itemservice

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/lifecycle"
	"github.com/francoisnyc/work-kernel/internal/policy"
	"github.com/francoisnyc/work-kernel/internal/typedef"
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
func (k *Service) Transition(ctx context.Context, req TransitionRequest) (WriteResult, error) {
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
		// A timer's write is internal infrastructure, not a principal reading
		// the Item, so it is not subject to visibility (docs/design.md,
		// Policy, "Boundary"). It is still subject to `allowed`, below.
		if req.Principal.Actor != typedef.TimerPrincipal {
			if err := checkVisible(ctx, tx, req.Principal, it.ScopeID); err != nil {
				return WriteResult{}, err
			}
		}
		if err := checkExpectedSeq(it, req.ExpectedItemSeq); err != nil {
			return WriteResult{}, err
		}
		ty, err := k.loadType(ctx, tx, it.TypeID, it.TypeVersion)
		if err != nil {
			return WriteResult{}, err
		}

		// All lifecycle logic lives in lifecycle.Transition; the item service only
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
		// The move is legal. Whether this principal may make it is policy's
		// question, asked second, so ErrForbidden always means "legal, but
		// not for you".
		if err := authorize(ctx, tx, req.Principal, ty, it.State, next.State); err != nil {
			return WriteResult{}, err
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
		return appendEvent(ctx, tx, ty, req.ItemID, itemSeq, req.Principal, req.Attribution, "transition", payload, set)
	})
}

// authorize checks the transition's `allowed` list against the principal's
// roles. When an agent acts on behalf of a user, both must pass: the agent
// has the user's rights only where it also has its own.
func authorize(ctx context.Context, tx pgx.Tx, p item.Principal, ty *typedef.Type, from, to string) error {
	tr, _ := ty.Transition(from, to) // lifecycle.Transition has just found it
	if len(tr.Allowed) == 0 {
		return nil // anyone who can see the Item; no need to read roles
	}
	for _, who := range []string{p.Actor, p.OnBehalfOf} {
		if who == "" {
			continue
		}
		// Roles are read on every write, inside its transaction: no cache.
		roles, err := policy.Roles(ctx, tx, defaultNamespace, who)
		if err != nil {
			return err
		}
		if !policy.AuthorizeTransition(who, roles, tr) {
			return fmt.Errorf("%w: %s may not move this Item from %q to %q", ErrForbidden, who, from, to)
		}
	}
	return nil
}
