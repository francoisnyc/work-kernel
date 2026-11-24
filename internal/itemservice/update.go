package itemservice

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/lifecycle"
	"github.com/francoisnyc/work-kernel/internal/policy"
	"github.com/francoisnyc/work-kernel/internal/typedef"
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
	// ExpectedContentSeq is the if-match behind the public etag: when set,
	// the write happens only if the Item's content_seq is still this one,
	// and fails with ErrStale otherwise. Zero means "no check". It is left
	// out of the idempotency hash when zero (omitempty), so a request
	// without it hashes as it did before the field existed.
	ExpectedContentSeq int64            `json:",omitempty"`
	Attribution        item.Attribution // optional; what caused this write
	// Policy is an optional change to who may read the Item.
	Policy *PolicyPatch
	// Move is an optional change of the Item's container. A request carries
	// a Fields patch, a Policy patch or a Move, or Fields with one of the
	// others; any combination is one write and one event.
	Move *Move
}

// Move changes an Item's container: To names the new container, or nil for
// the top level. A move that lets more people see anything in the moved
// subtree is refused unless ConfirmWiden is set.
type Move struct {
	To           *uuid.UUID
	ConfirmWiden bool
}

// Update applies a field patch, a policy patch or both to an Item and records
// one "update" event. It is the public door: the kernel's own actors use
// Internal().Update.
func (k *Service) Update(ctx context.Context, req UpdateRequest) (WriteResult, error) {
	if err := checkPublic(req.Principal, req.Attribution); err != nil {
		return WriteResult{}, err
	}
	if err := k.refuseCustomer(ctx, req.Principal, req.ItemID); err != nil {
		return WriteResult{}, err
	}
	return k.update(ctx, req, nil)
}

// update is Update's body for both doors. kc is nil for the public door, and
// the checked cause of a kernel write for the internal one.
func (k *Service) update(ctx context.Context, req UpdateRequest, kc *kernelCause) (WriteResult, error) {
	if req.IdempotencyKey == "" || req.Principal.Actor == "" || (len(req.Fields) == 0 && req.Policy == nil && req.Move == nil) {
		return WriteResult{}, fmt.Errorf("%w: idempotency key, actor and a field, policy or move are required", ErrInvalid)
	}
	if req.Policy != nil && req.Move != nil {
		return WriteResult{}, fmt.Errorf("%w: a policy change and a move are separate updates", ErrInvalid)
	}
	if err := checkPrincipal(req.Principal); err != nil {
		return WriteResult{}, err
	}
	if err := checkOptions(req.ExpectedItemSeq, req.ExpectedContentSeq, req.Attribution); err != nil {
		return WriteResult{}, err
	}
	attr, err := sessionAttribution(req.Principal, req.Attribution)
	if err != nil {
		return WriteResult{}, err
	}
	var readers []string
	if req.Policy != nil {
		if readers, err = checkPolicyPatch(req.Policy, req.Principal); err != nil {
			return WriteResult{}, err
		}
	}
	hash, err := requestHash("update", req)
	if err != nil {
		return WriteResult{}, err
	}

	return k.write(ctx, req.Principal, req.IdempotencyKey, hash, func(tx pgx.Tx) (WriteResult, error) {
		// A field unique among siblings (Jira 2.4b) is judged under its
		// container's lock, taken first (the lock order, jira24b.go).
		uniqueCheck := func(lockedItem) error { return nil }
		if req.Move == nil && len(req.Fields) > 0 {
			var err error
			if uniqueCheck, err = k.lockUniqueContainer(ctx, tx, nsOf(req.Principal), req.ItemID); err != nil {
				return WriteResult{}, err
			}
		}
		if req.Move != nil {
			// Moves take one namespace-wide lock: row locks alone cannot stop
			// moves on disjoint pairs from closing a cycle between them. Then
			// the Item rows involved, in ID order, so crossing moves queue.
			if err := lockForMove(ctx, tx, nsOf(req.Principal), req.ItemID, req.Move.To); err != nil {
				return WriteResult{}, err
			}
		}
		// Validation needs the Item's Type, so it runs after the row is locked.
		it, err := lockItem(ctx, tx, nsOf(req.Principal), req.ItemID)
		if err != nil {
			return WriteResult{}, err
		}
		// Visibility comes before every other check, so a principal who
		// cannot see the Item learns nothing about it: not even "stale".
		// (A kernel write is checked against its cause instead.)
		recorded, err := admit(ctx, tx, req.Principal, req.ItemID, it, kc)
		if err != nil {
			return WriteResult{}, err
		}
		if err := checkExpectedSeq(it, req.ExpectedItemSeq, req.ExpectedContentSeq); err != nil {
			return WriteResult{}, err
		}
		if err := uniqueCheck(it); err != nil {
			return WriteResult{}, err
		}
		ty, err := k.loadType(ctx, tx, it.TypeID, it.TypeVersion)
		if err != nil {
			return WriteResult{}, err
		}
		// Who may move or narrow it (Jira 2.4b): the Type's to say, by its
		// latest version whatever version the Item is pinned to (step 2b).
		if kc == nil && (req.Policy != nil || req.Move != nil) {
			if err := k.checkPlacement(ctx, tx, req.Principal, it.TypeID); err != nil {
				return WriteResult{}, err
			}
		}
		if err := ty.CheckFields(req.Fields, true); err != nil {
			return WriteResult{}, fieldsInvalid(err)
		}
		// Date-time values are stored in one form. req is this closure's
		// own copy from here on (a struct is copied on assignment).
		req := req
		req.Fields = ty.NormalizeFields(req.Fields)
		// Multi-value fields: a list becomes its set, a set operation is
		// applied to the set as it stands under the lock.
		var setOps map[string]typedef.SetOp
		if req.Fields, setOps, err = applySets(ty, it.Fields, req.Fields, false); err != nil {
			return WriteResult{}, err
		}
		if err := checkHookWrite(ty, kc, req.Fields); err != nil {
			return WriteResult{}, err
		}
		// Not writable, then frozen: before an open hold is asked.
		var writer *policy.Who
		if kc == nil {
			if writer, err = publicWriter(ctx, tx, ty, req.Principal, req.Fields); err != nil {
				return WriteResult{}, err
			}
		}
		if err := checkPatch(ty, it.State, nil, req.Fields, writer); err != nil {
			return WriteResult{}, err
		}
		// While a hold is open, the fields its check reads are frozen: the
		// check's answer is about the values it read.
		if len(req.Fields) > 0 {
			hold, err := readOpenHold(ctx, tx, req.ItemID)
			if err != nil {
				return WriteResult{}, err
			}
			if hold != nil {
				ids := slices.Sorted(maps.Keys(req.Fields))
				if err := lifecycle.UpdateDuringHold(ty, hold.rule(), ids); err != nil {
					return WriteResult{}, heldError(hold, err)
				}
			}
		}
		if len(req.Fields) > 0 {
			if err := checkAgentWrite(ctx, tx, req.Principal, it.ScopeID, &req.ItemID); err != nil {
				return WriteResult{}, err
			}
		}
		before, err := effectiveReaders(ctx, tx, it.ScopeID)
		if err != nil {
			return WriteResult{}, err
		}

		merged, set, cleared := applyPatch(it.Fields, req.Fields)

		scopeID, container, remove := it.ScopeID, it.ContainerID, uuid.Nil
		switch {
		case req.Policy != nil:
			if scopeID, remove, err = applyPolicy(ctx, tx, req.ItemID, it, req.Policy, readers); err != nil {
				return WriteResult{}, err
			}
		case req.Move != nil:
			if scopeID, err = k.move(ctx, tx, req.Principal, req.ItemID, req.Move.To); err != nil {
				return WriteResult{}, err
			}
			container = req.Move.To
		}
		if req.Policy != nil || req.Move != nil {
			// Who can read the Item after the change. In a narrow-only tree
			// nothing below the Item is wider than the Item itself, so the
			// Item's own readers decide whether the subtree widened.
			after, err := effectiveReaders(ctx, tx, scopeID)
			if err != nil {
				return WriteResult{}, err
			}
			if widens(before, after) {
				if req.Move != nil && !req.Move.ConfirmWiden {
					return WriteResult{}, rejected(CodeWidenNotConfirmed,
						"this move lets more people see the Item; confirm it with ConfirmWiden")
				}
				if req.Policy != nil {
					if err := checkAgentWiden(ctx, tx, req.Principal, it.ScopeID, &req.ItemID); err != nil {
						return WriteResult{}, err
					}
				}
			}
		}

		// No two siblings hold the same value of a unique field.
		if len(req.Fields) > 0 || req.Move != nil {
			if err := checkUnique(ctx, tx, ty, req.ItemID, container, it.State, merged); err != nil {
				return WriteResult{}, err
			}
		}
		// Last: the fields as the write leaves them keep the invariants.
		if len(req.Fields) > 0 {
			if err := checkInvariants(ty, merged); err != nil {
				return WriteResult{}, err
			}
		}

		payload := map[string]any{"field_ids": sortedKeys(set)}
		if len(cleared) > 0 {
			payload["cleared_field_ids"] = sortedKeys(cleared)
		}
		// The policy change is recorded as reader IDs only: no names.
		if req.Policy != nil {
			if req.Policy.Inherit {
				payload["inherit"] = true
			} else {
				payload["readers"] = readers
			}
		}
		// A move names both containers by opaque ID, for audit and replay.
		// Anything that shows history to a principal later must pass them
		// through policy.Project (docs/design.md, Policy).
		if req.Move != nil {
			payload["container_from"] = it.ContainerID
			payload["container_to"] = req.Move.To
		}
		addSetOps(payload, setOps)
		bump, err := decideContent(ty, "update", payload)
		if err != nil {
			return WriteResult{}, err
		}
		itemSeq := it.ItemSeq + 1
		_, err = tx.Exec(ctx,
			`update items set fields = $2, item_seq = $3, content_seq = content_seq + $6, scope_id = $4, container_id = $5,
			                  updated_at = now()
			 where id = $1`,
			req.ItemID, merged, itemSeq, scopeID, container, bump.n)
		if err != nil {
			return WriteResult{}, fmt.Errorf("update item: %w", err)
		}
		if remove != uuid.Nil {
			if err := removeNode(ctx, tx, remove); err != nil {
				return WriteResult{}, err
			}
		}
		res, err := appendEvent(ctx, tx, ty, req.ItemID, itemSeq, recorded, attr, bump, payload, set)
		if err != nil {
			return WriteResult{}, err
		}
		return res, storeSetOps(ctx, tx, req.ItemID, itemSeq, setOps)
	})
}
