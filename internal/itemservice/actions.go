package itemservice

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/lifecycle"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// Action hooks (Step 7): a hook with a side effect outside the kernel, run
// on entering a state. Once the state is entered, the action runs to
// completion, even after the Item has moved on. It completes with its
// write-back (an ordinary hook Update) or with hook_failed.

// HookFailed reasons. Codes, never free text.
const (
	ReasonBudgetExhausted = "budget_exhausted" // the hook's budget ran out
	ReasonKeyConflict     = "key_conflict"     // the same key came back with a different result: a duplicate
	ReasonActionFailed    = "action_failed"    // failed for good (unknown customer, bad input)
)

// HookFailedRequest records that an action hook gave up for one entry of its
// state. The hook is the principal; the entry is the attribution's cause.
type HookFailedRequest struct {
	IdempotencyKey string
	Principal      item.Principal
	ItemID         uuid.UUID
	Reason         string
	Attribution    item.Attribution
}

// HookFailed writes a hook_failed event: the hook and the reason, nothing
// else. It changes no field and no state, but it takes an item_seq like any
// write, so the failure is visible in the Item's history. Only an action
// hook may write it: an enrich hook has its fallback, a check its
// transition_failed.
func (in *Internal) HookFailed(ctx context.Context, req HookFailedRequest) (WriteResult, error) {
	k := in.k
	if req.IdempotencyKey == "" {
		return WriteResult{}, fmt.Errorf("%w: an idempotency key is required", ErrInvalid)
	}
	switch req.Reason {
	case ReasonBudgetExhausted, ReasonKeyConflict, ReasonActionFailed:
	default:
		return WriteResult{}, fmt.Errorf("%w: hook_failed reason %q is not one of %s, %s, %s",
			ErrInvalid, req.Reason, ReasonBudgetExhausted, ReasonKeyConflict, ReasonActionFailed)
	}
	kc, err := checkKernel(req.Principal, req.ItemID, req.Attribution)
	if err != nil {
		return WriteResult{}, err
	}
	if kc.hookID == "" {
		return WriteResult{}, fmt.Errorf("%w: only an action hook records hook_failed, not %s", ErrInvalid, req.Principal.Actor)
	}
	if req.Principal, err = k.inItemsNamespace(ctx, req.Principal, req.ItemID); err != nil {
		return WriteResult{}, err
	}
	hash, err := requestHash("hook_failed", req)
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
		if h.Kind != typedef.HookAction {
			return WriteResult{}, fmt.Errorf("%w: %s is a %s hook; only an action hook records hook_failed",
				ErrInvalid, kc.hookID, h.Kind)
		}
		itemSeq := it.ItemSeq + 1
		if _, err := tx.Exec(ctx,
			`update items set item_seq = $2, updated_at = now() where id = $1`, req.ItemID, itemSeq); err != nil {
			return WriteResult{}, fmt.Errorf("update item: %w", err)
		}
		payload := map[string]any{"hook": h.ID, "reason": req.Reason}
		return appendEvent(ctx, tx, ty, req.ItemID, itemSeq, recorded, req.Attribution, lifecycle.OpHookFailed, payload, nil)
	})
}

// PendingAction is an action hook not yet completed for one entry of its
// state: what the workflow needs to run it, opaque IDs only.
type PendingAction struct {
	Hook          string
	CauseSeq      int64 // the event that entered the hook's state
	CorrelationID string
	Hop           int // of that event
}

// pendingActions finds, from the events, every action hook not yet completed
// for any entry of its state. An action is complete for an entry once an
// event names the hook as originating_hook and the entry as causation_id:
// its write-back, or hook_failed. It reads the Item's whole history, which
// is fine for the spike (see docs/journal.md, Later).
func (k *Service) pendingActions(ctx context.Context, id uuid.UUID, ty *typedef.Type) ([]PendingAction, error) {
	hasActions := false
	for _, h := range ty.Hooks {
		hasActions = hasActions || h.Kind == typedef.HookAction
	}
	if !hasActions {
		return nil, nil
	}

	// Every event that entered a state, oldest first.
	rows, err := k.pool.Query(ctx,
		`select item_seq, coalesce(case op when 'create' then payload->>'state' else payload->>'to' end, ''),
		        coalesce(correlation_id, ''), hop
		 from item_events
		 where item_id = $1 and op in ('create', 'transition')
		 order by item_seq`, id)
	if err != nil {
		return nil, fmt.Errorf("read state entries: %w", err)
	}
	type entry struct {
		seq   int64
		state string
		corr  string
		hop   int
	}
	entries, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (entry, error) {
		var e entry
		return e, r.Scan(&e.seq, &e.state, &e.corr, &e.hop)
	})
	if err != nil {
		return nil, fmt.Errorf("read state entries: %w", err)
	}

	// What the hooks have already done: (originating_hook, causation_id).
	rows, err = k.pool.Query(ctx,
		`select originating_hook, causation_id from item_events
		 where item_id = $1 and originating_hook like 'hook:%' and causation_id is not null`, id)
	if err != nil {
		return nil, fmt.Errorf("read hook events: %w", err)
	}
	type done struct{ origin, cause string }
	doneRows, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (done, error) {
		var d done
		return d, r.Scan(&d.origin, &d.cause)
	})
	if err != nil {
		return nil, fmt.Errorf("read hook events: %w", err)
	}
	completed := map[done]bool{}
	for _, d := range doneRows {
		completed[d] = true
	}

	var out []PendingAction
	for _, e := range entries {
		for _, h := range lifecycle.ActionHooks(ty, e.state) {
			key := done{
				origin: fmt.Sprintf("hook:%s@v%d:%s", ty.ID, ty.Version, h.ID),
				cause:  fmt.Sprintf("%s:%d", id, e.seq),
			}
			if !completed[key] {
				out = append(out, PendingAction{Hook: h.ID, CauseSeq: e.seq, CorrelationID: e.corr, Hop: e.hop})
			}
		}
	}
	return out, nil
}
