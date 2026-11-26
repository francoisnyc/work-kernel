package itemservice

import (
	"context"
	"fmt"
	"strings"

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
	// ReasonNoRecipient (Jira part 2, step 3b): the hook reads an offer of
	// its container and there is nobody to tell.
	ReasonNoRecipient = "no_recipient"
)

// HookFailedRequest records that an action hook gave up for one entry of its
// state. The hook is the principal; the entry is the attribution's cause.
type HookFailedRequest struct {
	IdempotencyKey string
	Principal      item.Principal
	ItemID         uuid.UUID
	Reason         string
	Attribution    item.Attribution
	// Timer and Run (Step 2, part 3): the acting timer's run that gave up,
	// recorded in the payload; empty and 0 for an action on entering a state.
	Timer string
	Run   int
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
	case ReasonBudgetExhausted, ReasonKeyConflict, ReasonActionFailed, ReasonNoRecipient:
	default:
		return WriteResult{}, fmt.Errorf("%w: hook_failed reason %q is not one of %s, %s, %s, %s",
			ErrInvalid, req.Reason, ReasonBudgetExhausted, ReasonKeyConflict, ReasonActionFailed, ReasonNoRecipient)
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
		if err := checkRun(ty, h, req.Timer, req.Run); err != nil {
			return WriteResult{}, err
		}
		// A malformed record first (ErrInvalid); then the cause it names.
		if err := confirmBound(ty, h, kc); err != nil {
			return WriteResult{}, err
		}
		payload := map[string]any{"hook": h.ID, "reason": req.Reason}
		if req.Timer != "" {
			payload["timer"], payload["run"] = req.Timer, req.Run
		}
		bump, err := decideContent(ty, lifecycle.OpHookFailed, payload)
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
//
// ty is the version the Item is on now. Each entry is judged by the version
// it was made under (entryVersions): that version's action hooks, and its
// records (hook:<type>@v<n>:<id>).
func (k *Service) pendingActions(ctx context.Context, q rowsQuerier, id uuid.UUID, ty *typedef.Type) ([]PendingAction, error) {
	ev, err := k.readEntryVersions(ctx, q, id, ty)
	if err != nil {
		return nil, err
	}
	versions, err := ev.all(ctx)
	if err != nil {
		return nil, err
	}
	hasActions := false
	for _, v := range versions {
		for _, h := range v.Hooks {
			hasActions = hasActions || h.Kind == typedef.HookAction
		}
	}
	if !hasActions {
		return nil, nil
	}

	// Every event that entered a state, oldest first.
	rows, err := q.Query(ctx,
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
	rows, err = q.Query(ctx,
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

	// Every applied undo that runs hooks (on: reaction), oldest first: each
	// is an entry of those hooks, as entering a state is of an action's.
	rows, err = q.Query(ctx,
		`select item_seq, coalesce(payload->>'reaction', ''), coalesce(correlation_id, ''), hop
		 from item_events
		 where item_id = $1 and op = $2
		 order by item_seq`, id, lifecycle.OpReactionApplied)
	if err != nil {
		return nil, fmt.Errorf("read applied reactions: %w", err)
	}
	applied, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (entry, error) {
		var e entry
		return e, r.Scan(&e.seq, &e.state, &e.corr, &e.hop)
	})
	if err != nil {
		return nil, fmt.Errorf("read applied reactions: %w", err)
	}

	var out []PendingAction
	pending := func(eTy *typedef.Type, h typedef.Hook, e entry) {
		key := done{
			origin: fmt.Sprintf("hook:%s@v%d:%s", eTy.ID, eTy.Version, h.ID),
			cause:  fmt.Sprintf("%s:%d", id, e.seq),
		}
		if !completed[key] {
			out = append(out, PendingAction{Hook: h.ID, CauseSeq: e.seq, CorrelationID: e.corr, Hop: e.hop})
		}
	}
	for _, e := range entries {
		eTy, err := ev.at(ctx, e.seq)
		if err != nil {
			return nil, err
		}
		for _, h := range lifecycle.ActionHooks(eTy, e.state) {
			pending(eTy, h, e)
		}
	}
	for _, e := range applied {
		eTy, err := ev.at(ctx, e.seq)
		if err != nil {
			return nil, err
		}
		// The payload names the reaction as <type>:<reaction>.
		_, reaction, _ := strings.Cut(e.state, ":")
		for _, h := range lifecycle.ReactionHooks(eTy, reaction) {
			pending(eTy, h, e)
		}
	}
	return out, nil
}
