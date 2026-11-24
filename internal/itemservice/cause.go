package itemservice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/lifecycle"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// An effect re-checks what caused it (docs/design.md, "Architecture
// style"; bake-off hole 1). A workflow's input is a request, never proof:
// anyone who reaches Temporal can start the kernel's workflow, or schedule
// its activities, with any input. So before an action hook calls its
// connector, the worker asks here whether the cause it was given is real.

// ErrCauseNotConfirmed: the cause an action hook names does not check out.
// It is final: the same request can never be confirmed.
var ErrCauseNotConfirmed = errors.New("itemservice: cause not confirmed")

// ReasonCauseNotConfirmed is the hook_failed reason that records it on the
// Item. Only ConfirmCause writes it; HookFailed does not accept it.
const ReasonCauseNotConfirmed = "cause_not_confirmed"

// ConfirmCauseRequest is everything a workflow says about one run of an
// action hook. Every field of it may be forged.
type ConfirmCauseRequest struct {
	ItemID      uuid.UUID
	TypeID      string
	TypeVersion int
	Hook        string
	CauseSeq    int64  // the event said to have caused the run
	Timer       string // the acting timer said to run it, if any
	Run         int    // that timer's run, from 1; 0 without one, or at an instant
	// IdempotencyKey is the key the connector would get. It must be the
	// one the kernel derives from the cause, or one cause could be used
	// under many keys, each a new order.
	IdempotencyKey string
}

// ConfirmCause confirms the cause of one run of an action hook, from
// committed state only:
//
//   - the Type and version named are the Item's;
//   - the hook is an action hook of that Type;
//   - the event named is an event of this Item, and the one the hook is
//     bound to (lifecycle.BoundTo): the entry into its state, its
//     reaction's reaction_applied, or the entry into its timer's state;
//   - for an acting timer: the timer is that state's and runs this hook,
//     and the run is one of its runs; at an instant with `sets`, the timer's
//     own write for that instant exists;
//   - the idempotency key is the one the kernel derives from all that.
//
// The Item's state now is not asked: an action runs for its entry even
// after the Item has moved on.
//
// Anything else is ErrCauseNotConfirmed. It is recorded on the Item, once
// per hook and event named, as hook_failed with ReasonCauseNotConfirmed,
// but only when the event is real and the Item's Type declares the hook:
// otherwise there is nothing true to say it about, and what was named is
// the caller's text. A missing Item is ErrNotFound.
//
// What it cannot confirm (docs/roadmap.md, the bake-off's holes): that a
// scheduled acting timer's run was due, and that a timer at an instant
// without `sets` acted. Neither leaves an event before its hook has run.
func (in *Internal) ConfirmCause(ctx context.Context, req ConfirmCauseRequest) error {
	k := in.k
	refuse := func(format string, args ...any) error {
		return fmt.Errorf("%w: "+format, append([]any{ErrCauseNotConfirmed}, args...)...)
	}
	var ns, typeID string
	var version int
	err := k.pool.QueryRow(ctx, `select namespace_id, type_id, type_version from items where id = $1`, req.ItemID).
		Scan(&ns, &typeID, &version)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("read item: %w", err)
	}
	ty, err := k.loadType(ctx, k.pool, typeID, version)
	if err != nil {
		return err
	}
	h, ok := ty.Hook(req.Hook)
	if !ok || h.Kind != typedef.HookAction {
		return refuse("%s v%d declares no action hook of that name", typeID, version)
	}
	cause, found, err := readCause(ctx, k.pool, req.ItemID, req.CauseSeq)
	if err != nil {
		return err
	}
	if !found {
		return refuse("event %d is no event of this Item", req.CauseSeq)
	}

	why, err := k.judgeCause(ctx, ns, ty, h, cause, req)
	if err != nil {
		return err
	}
	if why == "" {
		return nil
	}
	if err := k.recordCauseRefusal(ctx, ns, ty, h, req.ItemID, req.CauseSeq); err != nil {
		return err // not recorded: let the caller try again
	}
	return refuse("%s for event %d: %s", h.ID, req.CauseSeq, why)
}

// judgeCause says why the request's cause does not check out; "" if it
// does. The event is known to exist and the hook to be the Type's.
func (k *Service) judgeCause(ctx context.Context, ns string, ty *typedef.Type, h typedef.Hook, cause lifecycle.Cause,
	req ConfirmCauseRequest) (string, error) {
	if req.TypeID != ty.ID || req.TypeVersion != ty.Version {
		return fmt.Sprintf("the Item is of %s v%d, not the Type and version named", ty.ID, ty.Version), nil
	}
	if !lifecycle.BoundTo(ty, h, cause) {
		return "it is not the event the hook is bound to", nil
	}
	wrongKey := "the idempotency key is not the one the kernel derives from the cause"
	id := req.ItemID.String()

	if h.On != typedef.OnTimer {
		if req.Timer != "" || req.Run != 0 {
			return "the hook is not run by an acting timer", nil
		}
		if req.IdempotencyKey != lifecycle.HookKey(id, req.CauseSeq, h.ID) {
			return wrongKey, nil
		}
		return "", nil
	}

	timer, ok := lifecycle.ActingTimer(ty, cause.Entered, req.Timer)
	if !ok || timer.Hook != h.ID {
		return "the state entered arms no such acting timer for the hook", nil
	}
	if timer.At == "" {
		// On a schedule: one of its numbered runs.
		if req.Run < 1 || req.Run > timer.Max {
			return fmt.Sprintf("acting timer %s runs 1 to %d times", timer.ID, timer.Max), nil
		}
		if req.IdempotencyKey != lifecycle.TimerRunKey(id, timer.ID, req.CauseSeq, req.Run) {
			return wrongKey, nil
		}
		return "", nil
	}

	// At an instant: no numbered runs; the key names the instant.
	if req.Run != 0 {
		return fmt.Sprintf("acting timer %s acts at an instant: it has no numbered runs", timer.ID), nil
	}
	// The instant is whatever the key names between the timer's prefix
	// and ":hook"; the key must then be exactly the one derived from it.
	stamp, _ := strings.CutSuffix(strings.TrimPrefix(req.IdempotencyKey, lifecycle.TimerAtKey(id, timer.ID, req.CauseSeq, "")), ":hook")
	at, err := time.Parse(typedef.DateTimeLayout, stamp)
	if err != nil || at.UTC().Format(typedef.DateTimeLayout) != stamp ||
		req.IdempotencyKey != lifecycle.TimerAtHookKey(id, timer.ID, req.CauseSeq, stamp) {
		return wrongKey, nil
	}
	if len(timer.Sets) == 0 {
		return "", nil // it writes nothing when it acts: no proof to ask for
	}
	// ActAt's idempotency record is written with its event, in one
	// transaction: it exists exactly when the timer acted for that instant.
	var proof bool
	if err := k.pool.QueryRow(ctx,
		`select exists (select 1 from idempotency
		                where namespace_id = $1 and actor = $2 and key = $3 and result ? 'item_id')`,
		ns, timerActor, lifecycle.TimerAtKey(id, timer.ID, req.CauseSeq, stamp)).Scan(&proof); err != nil {
		return "", fmt.Errorf("read the timer's write: %w", err)
	}
	if !proof {
		return fmt.Sprintf("acting timer %s has not acted for that instant", timer.ID), nil
	}
	return "", nil
}

// recordCauseRefusal writes the refusal on the Item, as the hook: a write
// like any other (the Item locked, its event and outbox row, an
// idempotency key). The key makes it once per hook and event named. The
// payload is the hook and the reason; the event names no cause, because
// the one claimed is not its cause (and an action counts as done for an
// entry once an event of its hook names that entry: pendingActions).
func (k *Service) recordCauseRefusal(ctx context.Context, ns string, ty *typedef.Type, h typedef.Hook,
	itemID uuid.UUID, causeSeq int64) error {
	p := item.Principal{Actor: fmt.Sprintf("%s:%s:%s", item.KindHook, ty.ID, h.ID), Namespace: ns}
	key := fmt.Sprintf("%s:%s:%s:%d", ReasonCauseNotConfirmed, itemID, h.ID, causeSeq)
	hash, err := requestHash(ReasonCauseNotConfirmed, key)
	if err != nil {
		return err
	}
	_, err = k.write(ctx, p, key, hash, func(tx pgx.Tx) (WriteResult, error) {
		it, err := lockItem(ctx, tx, ns, itemID)
		if err != nil {
			return WriteResult{}, err
		}
		payload := map[string]any{"hook": h.ID, "reason": ReasonCauseNotConfirmed}
		bump, err := decideContent(ty, lifecycle.OpHookFailed, payload)
		if err != nil {
			return WriteResult{}, err
		}
		itemSeq := it.ItemSeq + 1
		if _, err := tx.Exec(ctx,
			`update items set item_seq = $2, content_seq = content_seq + $3, updated_at = now() where id = $1`,
			itemID, itemSeq, bump.n); err != nil {
			return WriteResult{}, fmt.Errorf("update item: %w", err)
		}
		attr := item.Attribution{OriginatingHook: fmt.Sprintf("%s:%s@v%d:%s", item.KindHook, ty.ID, ty.Version, h.ID)}
		return appendEvent(ctx, tx, ty, itemID, itemSeq, p, attr, bump, payload, nil)
	})
	return err
}

// readCause reads the shape of one event of an Item; found is false when
// the Item has no such event.
func readCause(ctx context.Context, q querier, itemID uuid.UUID, seq int64) (lifecycle.Cause, bool, error) {
	var op string
	var payload []byte
	err := q.QueryRow(ctx, `select op, payload from item_events where item_id = $1 and item_seq = $2`, itemID, seq).
		Scan(&op, &payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return lifecycle.Cause{}, false, nil
	}
	if err != nil {
		return lifecycle.Cause{}, false, fmt.Errorf("read cause: %w", err)
	}
	c, err := causeOf(op, payload)
	return c, err == nil, err
}

// causeOf reduces an event to what a hook's binding is judged on.
func causeOf(op string, payload []byte) (lifecycle.Cause, error) {
	var shape struct {
		State    string `json:"state"`
		To       string `json:"to"`
		Reaction string `json:"reaction"`
	}
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &shape); err != nil {
			return lifecycle.Cause{}, fmt.Errorf("decode cause: %w", err)
		}
	}
	c := lifecycle.Cause{Op: op, Reaction: shape.Reaction}
	switch op {
	case "create":
		c.Entered = shape.State
	case "transition":
		c.Entered = shape.To
	}
	return c, nil
}

// confirmBound is the same check for an action hook's own records (its
// write-back, hook_ran, hook_failed), made under the Item's lock by a
// write that admit has let in: written for an event the hook is not bound
// to, they would be false history. Checks and enrich hooks are not asked
// (their holds and keys bound them; see docs/roadmap.md).
func confirmBound(ty *typedef.Type, h typedef.Hook, kc *kernelCause) error {
	if h.Kind != typedef.HookAction || lifecycle.BoundTo(ty, h, kc.cause) {
		return nil
	}
	return fmt.Errorf("%w: event %d is not the event %s is bound to", ErrCauseNotConfirmed, kc.causeSeq, h.ID)
}
