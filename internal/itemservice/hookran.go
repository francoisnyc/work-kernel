package itemservice

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/lifecycle"
	"github.com/francoisnyc/work-kernel/internal/typedef"
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

// HookRan writes hook_ran (no field, no state change). Only an action hook
// that writes nothing records it; a hook on: timer names its acting timer
// and run.
func (in *Internal) HookRan(ctx context.Context, req HookRanRequest) (WriteResult, error) {
	k := in.k
	if req.IdempotencyKey == "" {
		return WriteResult{}, fmt.Errorf("%w: an idempotency key is required", ErrInvalid)
	}
	kc, err := checkKernel(req.Principal, req.ItemID, req.Attribution)
	if err != nil {
		return WriteResult{}, err
	}
	if kc.hookID == "" {
		return WriteResult{}, fmt.Errorf("%w: only an action hook records hook_ran, not %s", ErrInvalid, req.Principal.Actor)
	}
	if req.Principal, err = k.inItemsNamespace(ctx, req.Principal, req.ItemID); err != nil {
		return WriteResult{}, err
	}
	hash, err := requestHash("hook_ran", req)
	if err != nil {
		return WriteResult{}, err
	}
	return k.write(ctx, req.Principal, req.IdempotencyKey, hash, func(tx pgx.Tx) (WriteResult, error) {
		it, err := lockItem(ctx, tx, nsOf(req.Principal), req.ItemID)
		if err != nil {
			return WriteResult{}, err
		}
		recorded, err := admit(ctx, tx, req.Principal, req.ItemID, it, kc)
		if err != nil {
			return WriteResult{}, err
		}
		ty, err := k.loadType(ctx, tx, it.TypeID, it.TypeVersion)
		if err != nil {
			return WriteResult{}, err
		}
		h, ok := ty.Hook(kc.hookID)
		if !ok {
			return WriteResult{}, fmt.Errorf("%w: hook %s is not declared by %s v%d", ErrInvalid, kc.hookID, ty.ID, ty.Version)
		}
		if h.Kind != typedef.HookAction || len(h.Writes) > 0 {
			return WriteResult{}, fmt.Errorf("%w: %s records that it ran with its write-back; "+
				"hook_ran is for an action hook that writes nothing", ErrInvalid, h.ID)
		}
		if err := checkRun(ty, h, req.Timer, req.Run); err != nil {
			return WriteResult{}, err
		}
		// A malformed record first (ErrInvalid); then the cause it names.
		if err := confirmBound(ty, h, kc); err != nil {
			return WriteResult{}, err
		}
		// No field and no move: no hold or frozen field can refuse it.
		payload := map[string]any{"hook": h.ID}
		if req.Timer != "" {
			payload["timer"], payload["run"] = req.Timer, req.Run
		}
		bump, err := decideContent(ty, lifecycle.OpHookRan, payload)
		if err != nil {
			return WriteResult{}, err
		}
		itemSeq := it.ItemSeq + 1
		if _, err := tx.Exec(ctx,
			`update items set item_seq = $2, content_seq = content_seq + $3, updated_at = now() where id = $1`,
			req.ItemID, itemSeq, bump.n); err != nil {
			return WriteResult{}, fmt.Errorf("update item: %w", err)
		}
		return appendEvent(ctx, tx, ty, req.ItemID, itemSeq, recorded, req.Attribution, bump, payload, nil)
	})
}

// checkRun checks the acting timer and run an action hook's event names: a
// hook on: timer names one of its own acting timers and a run from 1 to its
// max; any other hook names none.
func checkRun(ty *typedef.Type, h typedef.Hook, timer string, run int) error {
	if h.On != typedef.OnTimer {
		if timer != "" || run != 0 {
			return fmt.Errorf("%w: %s is not run by an acting timer: it names no timer or run", ErrInvalid, h.ID)
		}
		return nil
	}
	for _, s := range ty.States {
		for _, a := range s.Acting {
			if a.ID != timer || a.Hook != h.ID {
				continue
			}
			// At an instant there are no numbered runs: the key names
			// the instant.
			if a.At != "" {
				if run != 0 {
					return fmt.Errorf("%w: acting timer %s acts at an instant: it has no run %d", ErrInvalid, timer, run)
				}
				return nil
			}
			if run < 1 || run > a.Max {
				return fmt.Errorf("%w: acting timer %s runs 1 to %d times, not run %d", ErrInvalid, timer, a.Max, run)
			}
			return nil
		}
	}
	return fmt.Errorf("%w: %s is run by an acting timer: name it (not %q)", ErrInvalid, h.ID, timer)
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
func (in *Internal) ReactionApplied(ctx context.Context, req ReactionAppliedRequest) (WriteResult, error) {
	k := in.k
	if req.HoldID == uuid.Nil {
		return WriteResult{}, fmt.Errorf("%w: reaction_applied: the hold is required", ErrInvalid)
	}
	kc, err := checkKernel(req.Principal, req.SourceID, req.Attribution)
	if err != nil {
		return WriteResult{}, err
	}
	if req.Principal, err = k.inItemsNamespace(ctx, req.Principal, req.SourceID); err != nil {
		return WriteResult{}, err
	}
	srcTy, r, err := k.reactionOf(ctx, req.SourceID, kc, typedef.ReactionAfter)
	if err != nil {
		return WriteResult{}, err
	}
	h, err := k.reactionHold(ctx, req.SourceID, req.HoldID, r.Undoes)
	if err != nil {
		return WriteResult{}, err
	}
	ns := nsOf(req.Principal)
	notApplied := rejected(CodeNotApplied, "%s gave nothing back for hold %s: there is nothing to record", r.ID, req.HoldID)
	resv, ok, err := reservation(ctx, k.pool, ns, srcTy.ID, r.Undoes, req.SourceID, h.requestedSeq)
	if err != nil {
		return WriteResult{}, err
	}
	if !ok {
		return WriteResult{}, notApplied
	}
	// The undo's idempotency record is written with its refund, so it
	// exists exactly when the refund committed; it holds the refund event.
	undoKey := resv.key + ":" + r.ID
	var refund WriteResult
	err = k.pool.QueryRow(ctx,
		`select result from idempotency where namespace_id = $1 and actor = $2 and key = $3 and result ? 'item_id'`,
		ns, req.Principal.Actor, undoKey).Scan(&refund)
	if errors.Is(err, pgx.ErrNoRows) {
		return WriteResult{}, notApplied
	}
	if err != nil {
		return WriteResult{}, fmt.Errorf("read the undo: %w", err)
	}
	hash, err := requestHash("reaction_applied", req)
	if err != nil {
		return WriteResult{}, err
	}
	return k.write(ctx, req.Principal, undoKey+":applied", hash, func(tx pgx.Tx) (WriteResult, error) {
		it, err := lockItem(ctx, tx, ns, req.SourceID)
		if err != nil {
			return WriteResult{}, err
		}
		recorded, err := admit(ctx, tx, req.Principal, req.SourceID, it, kc)
		if err != nil {
			return WriteResult{}, err
		}
		ty, err := k.loadType(ctx, tx, it.TypeID, it.TypeVersion)
		if err != nil {
			return WriteResult{}, err
		}
		payload := map[string]any{
			"reaction": typedef.ReactionKey(ty.ID, r.ID),
			"target":   fmt.Sprintf("%s:%d", refund.ItemID, refund.ItemSeq),
		}
		bump, err := decideContent(ty, lifecycle.OpReactionApplied, payload)
		if err != nil {
			return WriteResult{}, err
		}
		itemSeq := it.ItemSeq + 1
		if _, err := tx.Exec(ctx, `update items set item_seq = $2, content_seq = content_seq + $3, updated_at = now() where id = $1`,
			req.SourceID, itemSeq, bump.n); err != nil {
			return WriteResult{}, fmt.Errorf("update item: %w", err)
		}
		return appendEvent(ctx, tx, ty, req.SourceID, itemSeq, recorded, req.Attribution, bump, payload, nil)
	})
}

// CodeNotApplied refuses reaction_applied for an undo that did not commit.
const CodeNotApplied = "not_applied"
