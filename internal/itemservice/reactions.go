package itemservice

import (
	"context"
	"errors"
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

// Link reactions (proof of concept, Step 2, part 2; docs/design.md).
//
// A held reaction decides a hold: the workflow calls React, which writes the
// reaction's `add` on the Item the source links to (one write, one Item: the
// target), then resolves the source's hold through ResolveHold, allow if
// React committed, deny with DenyReason(err) if the target refused. React
// commits only while that very hold is open (it locks the source FOR SHARE
// and the target FOR UPDATE, in UUID order), so a deduction never outlives
// its hold.
//
// Undo reverses a committed reaction write by the delta it recorded
// (item_event_values.field_deltas), under the key <reservation key>:<undo>:
// once a hold closes any other way than allowed, and when the source leaves
// the state the hold entered by the undo's trigger (approved -> withdrawn).
// Without a committed reservation it writes nothing.

// Rejection codes of reactions, link resolution and invariants.
const (
	CodeInvariantFailed       = "invariant_failed"        // a write would break an invariant; metadata invariant
	CodeLinkUnresolved        = "link_unresolved"         // entering a resolve state found 0 or >1 targets; metadata link_type, reason
	CodeReactionNotAccepted   = "reaction_not_accepted"   // the target's Type version does not accept the reaction for those fields
	CodeNotUndoable           = "not_undoable"            // Undo of a hold that is open, or allowed and not left by the undo's trigger
	CodeReactionAmountInvalid = "reaction_amount_invalid" // a held reaction's add cannot be evaluated on the source (a null amount)
)

// LinkNotWritableError is an ErrForbidden: the public door may not add or
// remove a link of a type only the kernel writes (writable_by: kernel).
type LinkNotWritableError struct {
	LinkType string
	// Roles, when set, are the roles whose holders write the link (Jira
	// part 2, step 3b); empty for a link only the kernel sets.
	Roles []string
}

func (e *LinkNotWritableError) Error() string {
	if len(e.Roles) > 0 {
		return fmt.Sprintf("%v: links %s are set by holders of a role only", ErrForbidden, e.LinkType)
	}
	return fmt.Sprintf("%v: links %s are set by the kernel only", ErrForbidden, e.LinkType)
}
func (e *LinkNotWritableError) Unwrap() error { return ErrForbidden }

// ReactRequest asks for a held reaction's write on its target. The key is
// derived by the kernel: <reaction>:<source>:<requested_seq>:<target>.
type ReactRequest struct {
	Principal item.Principal // reaction:<type>:<id>, a held reaction of the source's Type
	SourceID  uuid.UUID
	HoldID    uuid.UUID // the hold the reaction decides; React commits only while it is open
	// Attribution: originating_hook reaction:<type>@v<n>:<id>, causation_id
	// the hold's transition_requested event, hop that event's hop + 1.
	Attribution item.Attribution
}

// ReactResult names the write React made on the target, or would have
// made: a retry returns the first result.
type ReactResult struct {
	WriteResult           // the target's event
	Target      uuid.UUID // the Item the reaction wrote
	Key         string    // the reservation key
}

// React applies a held reaction's write to its target.
func (in *Internal) React(ctx context.Context, req ReactRequest) (ReactResult, error) {
	k := in.k
	if req.HoldID == uuid.Nil {
		return ReactResult{}, fmt.Errorf("%w: react: the hold is required", ErrInvalid)
	}
	kc, err := checkKernel(req.Principal, req.SourceID, req.Attribution)
	if err != nil {
		return ReactResult{}, err
	}
	if req.Principal, err = k.inItemsNamespace(ctx, req.Principal, req.SourceID); err != nil {
		return ReactResult{}, err
	}
	srcTy, r, err := k.reactionOf(ctx, req.SourceID, kc, typedef.ReactionHeld)
	if err != nil {
		return ReactResult{}, err
	}
	h, err := k.reactionHold(ctx, req.SourceID, req.HoldID, r.ID)
	if err != nil {
		return ReactResult{}, err
	}
	if h.requestedSeq != kc.causeSeq {
		return ReactResult{}, fmt.Errorf("%w: react: caused by the request (event %d), not event %d",
			ErrInvalid, h.requestedSeq, kc.causeSeq)
	}
	// The target, as linked now; checked again under the locks.
	var target uuid.UUID
	err = k.pool.QueryRow(ctx, `select target_id from links where source_id = $1 and link_type = $2`,
		req.SourceID, r.Link).Scan(&target)
	if errors.Is(err, pgx.ErrNoRows) {
		return ReactResult{}, rejected(CodeNotLinked, "the source no longer links to a target by %s", r.Link)
	}
	if err != nil {
		return ReactResult{}, fmt.Errorf("read link: %w", err)
	}
	key := fmt.Sprintf("%s:%s:%d:%s", r.ID, req.SourceID, h.requestedSeq, target)
	hash, err := requestHash("react", req)
	if err != nil {
		return ReactResult{}, err
	}
	ns := nsOf(req.Principal)
	res, err := k.write(ctx, req.Principal, key, hash, func(tx pgx.Tx) (WriteResult, error) {
		// The source FOR SHARE (an escape, which takes it FOR UPDATE, waits
		// for this commit) and the target FOR UPDATE, in UUID order.
		var srcFields map[string]any
		var tgt lockedItem
		for _, id := range sortedIDs(req.SourceID, target) {
			if id == req.SourceID {
				if err := tx.QueryRow(ctx, `select fields from items where id = $1 for share`, id).Scan(&srcFields); err != nil {
					return WriteResult{}, fmt.Errorf("lock source: %w", err)
				}
				continue
			}
			if tgt, err = lockItem(ctx, tx, ns, id); err != nil {
				return WriteResult{}, err
			}
		}
		// Only while this very hold is open: a deduction never outlives it.
		var status string
		if err := tx.QueryRow(ctx, `select status from item_holds where id = $1`, req.HoldID).Scan(&status); err != nil {
			return WriteResult{}, fmt.Errorf("read hold: %w", err)
		}
		if status != item.HoldOpen {
			return WriteResult{}, fmt.Errorf("%w: hold %s is %s; there is nothing left to decide", ErrStale, req.HoldID, status)
		}
		var linked bool
		if err := tx.QueryRow(ctx,
			`select exists (select 1 from links where source_id = $1 and link_type = $2 and target_id = $3)`,
			req.SourceID, r.Link, target).Scan(&linked); err != nil {
			return WriteResult{}, fmt.Errorf("read link: %w", err)
		}
		if !linked {
			return WriteResult{}, rejected(CodeNotLinked, "the source no longer links to a target by %s", r.Link)
		}
		// A link the kernel resolved must still be the one resolution would
		// pick: the target is locked, so its state and owner hold until the
		// commit. Anything else (none, several, another Item) denies; the
		// requester resubmits, which resolves again. A candidate created
		// meanwhile is not locked (a phantom), as at resolution.
		if l, ok := srcTy.Link(r.Link); ok && l.Resolve != nil {
			var creator string
			if err := tx.QueryRow(ctx, `select coalesce(created_on_behalf_of, created_by) from items where id = $1`,
				req.SourceID).Scan(&creator); err != nil {
				return WriteResult{}, fmt.Errorf("read creator: %w", err)
			}
			now, err := resolveCandidates(ctx, tx, ns, l, creator)
			if err != nil {
				return WriteResult{}, err
			}
			if len(now) != 1 || now[0] != target {
				return WriteResult{}, rejected(CodeLinkTargetChanged,
					"link %s no longer resolves to its target (%d candidates now): resubmit to resolve it again", r.Link, len(now))
			}
		}
		adds, err := lifecycle.ReactionAdds(r, srcFields)
		if err != nil {
			return WriteResult{}, rejected(CodeReactionAmountInvalid, "%v", err)
		}
		recorded, err := causeRoot(ctx, tx, req.Principal, req.SourceID, kc.causeSeq)
		if err != nil {
			return WriteResult{}, err
		}
		return k.applyAdd(ctx, tx, target, tgt, typedef.ReactionKey(srcTy.ID, r.ID), adds, recorded, req.Attribution, nil)
	})
	if err != nil {
		return ReactResult{}, err
	}
	return ReactResult{WriteResult: res, Target: target, Key: key}, nil
}

// reactionOf reads the source's Type (outside the write: a version never
// changes) and the reaction acting, which must be one of that version's,
// of the given kind.
func (k *Service) reactionOf(ctx context.Context, source uuid.UUID, kc *kernelCause,
	kind typedef.ReactionKind) (*typedef.Type, typedef.Reaction, error) {
	if kc.reactionID == "" {
		return nil, typedef.Reaction{}, fmt.Errorf("%w: only a %s reaction may do this", ErrInvalid, kind)
	}
	var typeID string
	var version int
	if err := k.pool.QueryRow(ctx, `select type_id, type_version from items where id = $1`, source).
		Scan(&typeID, &version); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, typedef.Reaction{}, ErrNotFound
		}
		return nil, typedef.Reaction{}, fmt.Errorf("read source: %w", err)
	}
	if kc.typeID != typeID || kc.typeVersion != version {
		return nil, typedef.Reaction{}, fmt.Errorf("%w: reaction %s ran for %s v%d, on an Item of %s v%d",
			ErrInvalid, kc.reactionID, kc.typeID, kc.typeVersion, typeID, version)
	}
	ty, err := k.loadType(ctx, k.pool, typeID, version)
	if err != nil {
		return nil, typedef.Reaction{}, err
	}
	r, ok := ty.Reaction(kc.reactionID)
	if !ok || r.Kind != kind {
		return nil, typedef.Reaction{}, fmt.Errorf("%w: %s v%d declares no %s reaction %s", ErrInvalid, typeID, version, kind, kc.reactionID)
	}
	return ty, r, nil
}

// reactionHoldRow is a hold as a reaction needs it.
type reactionHoldRow struct {
	decider              string
	from, to, status     string
	requestedSeq, closed int64 // closed: the event that closed it, 0 while open
}

// reactionHold reads the source's hold, which must be decided by decider.
func (k *Service) reactionHold(ctx context.Context, source, holdID uuid.UUID, decider string) (reactionHoldRow, error) {
	var h reactionHoldRow
	err := k.pool.QueryRow(ctx,
		`select hook, from_state, to_state, status, requested_seq, coalesce(resolved_seq, 0)
		 from item_holds where id = $1 and item_id = $2`, holdID, source,
	).Scan(&h.decider, &h.from, &h.to, &h.status, &h.requestedSeq, &h.closed)
	if errors.Is(err, pgx.ErrNoRows) {
		return h, fmt.Errorf("%w: no hold %s on this Item", ErrNotFound, holdID)
	}
	if err != nil {
		return h, fmt.Errorf("read hold: %w", err)
	}
	if h.decider != decider {
		return h, fmt.Errorf("%w: hold %s is decided by %s, not %s", ErrInvalid, holdID, h.decider, decider)
	}
	return h, nil
}

// causeRoot is how a reaction's write is recorded: the reaction as actor,
// and as on_behalf_of the human at the root of its cause, an event of the
// source (its on_behalf_of, else its actor if a user). Attribution only.
func causeRoot(ctx context.Context, q policy.Querier, p item.Principal, source uuid.UUID, seq int64) (item.Principal, error) {
	var actor, onBehalfOf string
	err := q.QueryRow(ctx,
		`select actor, coalesce(on_behalf_of, '') from item_events where item_id = $1 and item_seq = $2`,
		source, seq).Scan(&actor, &onBehalfOf)
	if errors.Is(err, pgx.ErrNoRows) {
		return item.Principal{}, fmt.Errorf("%w: the cause is not an event of the source", ErrInvalid)
	}
	if err != nil {
		return item.Principal{}, fmt.Errorf("read cause: %w", err)
	}
	root := onBehalfOf
	if root == "" && item.IsKind(actor, item.KindUser, item.KindCustomer) {
		root = actor
	}
	return item.Principal{Actor: p.Actor, OnBehalfOf: root, Namespace: p.Namespace}, nil
}

// applyAdd writes a reaction's additions on its locked target: consent,
// frozen fields, an open hold's reads, then the sums and the invariants,
// last. One event on the target (op update): field_ids and
// added_field_ids in the payload, the results in item_event_values and
// the amounts in field_deltas.
func (k *Service) applyAdd(ctx context.Context, tx pgx.Tx, id uuid.UUID, tgt lockedItem, reaction string,
	adds map[string]int64, recorded item.Principal, attr item.Attribution, extra map[string]any) (WriteResult, error) {
	ty, err := k.loadType(ctx, tx, tgt.TypeID, tgt.TypeVersion)
	if err != nil {
		return WriteResult{}, err
	}
	ids := slices.Sorted(maps.Keys(adds))
	if !ty.Accepts(reaction, ids) {
		return WriteResult{}, rejected(CodeReactionNotAccepted, "%s v%d does not accept %s writing %v",
			ty.ID, ty.Version, reaction, ids)
	}
	patch := map[string]any{}
	for _, f := range ids {
		patch[f] = adds[f]
	}
	if err := checkPatch(ty, tgt.State, nil, patch, nil); err != nil {
		return WriteResult{}, err
	}
	hold, err := readOpenHold(ctx, tx, id)
	if err != nil {
		return WriteResult{}, err
	}
	if hold != nil {
		if err := lifecycle.UpdateDuringHold(ty, hold.rule(), ids); err != nil {
			return WriteResult{}, heldError(hold, err)
		}
	}
	set, deltas := map[string]any{}, map[string]any{}
	for _, f := range ids {
		cur, _ := typedef.AsInt(tgt.Fields[f]) // unset is 0
		sum := cur + adds[f]
		if sum > maxExactInt || sum < -maxExactInt {
			return WriteResult{}, fmt.Errorf("%w: %s would leave the range of a whole number", ErrInvalid, f)
		}
		set[f], deltas[f] = sum, adds[f]
	}
	merged, _, _ := applyPatch(tgt.Fields, set)
	if err := checkInvariants(ty, merged); err != nil {
		return WriteResult{}, err
	}
	payload := map[string]any{"field_ids": ids, "added_field_ids": ids}
	maps.Copy(payload, extra)
	bump, err := decideContent(ty, "update", payload)
	if err != nil {
		return WriteResult{}, err
	}
	itemSeq := tgt.ItemSeq + 1
	if _, err := tx.Exec(ctx,
		`update items set fields = $2, item_seq = $3, content_seq = content_seq + $4, updated_at = now() where id = $1`,
		id, merged, itemSeq, bump.n); err != nil {
		return WriteResult{}, fmt.Errorf("update item: %w", err)
	}
	res, err := appendEvent(ctx, tx, ty, id, itemSeq, recorded, attr, bump, payload, set)
	if err != nil {
		return WriteResult{}, err
	}
	if _, err := tx.Exec(ctx, `update item_event_values set field_deltas = $3 where item_id = $1 and item_seq = $2`,
		id, itemSeq, deltas); err != nil {
		return WriteResult{}, fmt.Errorf("store deltas: %w", err)
	}
	return res, nil
}

// maxExactInt is the largest whole number a field holds (JSON's float64).
const maxExactInt = 1<<53 - 1

func sortedIDs(ids ...uuid.UUID) []uuid.UUID {
	slices.SortFunc(ids, compareUUID)
	return slices.Compact(ids)
}

// UndoRequest asks to reverse the reservation a held reaction made for a
// hold.
type UndoRequest struct {
	Principal item.Principal // reaction:<type>:<id>, the undo of the hold's reaction
	SourceID  uuid.UUID
	HoldID    uuid.UUID // whose reservation to reverse: closed non-allow, or the hold that entered the state left
	// Attribution: originating_hook reaction:<type>@v<n>:<id>, causation_id
	// the event that closed the hold (or left the state), hop + 1.
	Attribution item.Attribution
}

// UndoResult says what Undo did. Undone is false when there was nothing to
// reverse (no committed reservation): then nothing was written.
type UndoResult struct {
	WriteResult // the target's refund event, when Undone
	Undone      bool
	Target      uuid.UUID
	Key         string // <reservation key>:<undo id>
}

// Undo reverses a held reaction's committed write, at most once per
// reservation, whichever path asks.
func (in *Internal) Undo(ctx context.Context, req UndoRequest) (UndoResult, error) {
	k := in.k
	if req.HoldID == uuid.Nil {
		return UndoResult{}, fmt.Errorf("%w: undo: the hold is required", ErrInvalid)
	}
	kc, err := checkKernel(req.Principal, req.SourceID, req.Attribution)
	if err != nil {
		return UndoResult{}, err
	}
	if req.Principal, err = k.inItemsNamespace(ctx, req.Principal, req.SourceID); err != nil {
		return UndoResult{}, err
	}
	srcTy, r, err := k.reactionOf(ctx, req.SourceID, kc, typedef.ReactionAfter)
	if err != nil {
		return UndoResult{}, err
	}
	h, err := k.reactionHold(ctx, req.SourceID, req.HoldID, r.Undoes)
	if err != nil {
		return UndoResult{}, err
	}
	if _, due, err := undoDue(ctx, k.pool, req.SourceID, h, r); err != nil {
		return UndoResult{}, err
	} else if !due {
		return UndoResult{}, rejected(CodeNotUndoable,
			"hold %s is %s and its state was not left by %s's trigger: there is nothing to undo yet", req.HoldID, h.status, r.ID)
	}
	ns := nsOf(req.Principal)
	resv, ok, err := reservation(ctx, k.pool, ns, srcTy.ID, r.Undoes, req.SourceID, h.requestedSeq)
	if err != nil || !ok {
		return UndoResult{}, err // no committed deduction: nothing to do, nothing written
	}
	key := resv.key + ":" + r.ID
	hash, err := requestHash("undo", req)
	if err != nil {
		return UndoResult{}, err
	}
	res, err := k.write(ctx, req.Principal, key, hash, func(tx pgx.Tx) (WriteResult, error) {
		tgt, err := lockItem(ctx, tx, ns, resv.target)
		if err != nil {
			return WriteResult{}, err
		}
		var raw map[string]any
		if err := tx.QueryRow(ctx,
			`select coalesce(field_deltas, '{}') from item_event_values where item_id = $1 and item_seq = $2`,
			resv.target, resv.seq).Scan(&raw); err != nil {
			return WriteResult{}, fmt.Errorf("read the reservation's deltas: %w", err)
		}
		adds := map[string]int64{}
		for f, v := range raw {
			n, _ := typedef.AsInt(v)
			adds[f] = -n
		}
		if len(adds) == 0 {
			return WriteResult{}, fmt.Errorf("%w: the reservation recorded no delta", ErrInvalid)
		}
		recorded, err := causeRoot(ctx, tx, req.Principal, req.SourceID, kc.causeSeq)
		if err != nil {
			return WriteResult{}, err
		}
		return k.applyAdd(ctx, tx, resv.target, tgt, typedef.ReactionKey(srcTy.ID, r.ID), adds, recorded, req.Attribution,
			map[string]any{"undoes": fmt.Sprintf("%s:%d", resv.target, resv.seq)})
	})
	if err != nil {
		return UndoResult{}, err
	}
	return UndoResult{WriteResult: res, Undone: true, Target: resv.target, Key: key}, nil
}

// undoDue says whether a reaction's hold may be undone now, and which event
// asks for it: the event that closed it any other way than allowed, or,
// once allowed, the event that left the state it entered by the undo's
// trigger.
func undoDue(ctx context.Context, q policy.Querier, source uuid.UUID, h reactionHoldRow, undo typedef.Reaction) (int64, bool, error) {
	switch h.status {
	case item.HoldOpen:
		return 0, false, nil
	case item.HoldAllowed:
		if len(undo.From) > 0 && !slices.Contains(undo.From, h.to) {
			return 0, false, nil
		}
		var seq int64
		err := q.QueryRow(ctx,
			`select item_seq from item_events
			 where item_id = $1 and item_seq > $2 and op = 'transition'
			   and payload ->> 'from' = $3 and payload ->> 'to' = $4
			 order by item_seq limit 1`, source, h.closed, h.to, undo.State).Scan(&seq)
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, false, nil
		}
		if err != nil {
			return 0, false, fmt.Errorf("read the undo's trigger: %w", err)
		}
		// Only the first leaving after this hold's entry: a later entry
		// into the state was some other hold's.
		var reentered bool
		if err := q.QueryRow(ctx,
			`select exists (select 1 from item_events where item_id = $1 and item_seq > $2 and item_seq < $3
			                and payload ->> 'to' = $4)`, source, h.closed, seq, h.to).Scan(&reentered); err != nil {
			return 0, false, fmt.Errorf("read the undo's trigger: %w", err)
		}
		return seq, !reentered, nil
	default: // denied, error, canceled
		return h.closed, true, nil
	}
}

// reservationRow is a held reaction's committed write.
type reservationRow struct {
	key    string
	target uuid.UUID
	seq    int64 // the target's event
}

// reservation finds the committed write of held reaction `held` for one
// request of source (its key: <held>:<source>:<requested seq>:<target>).
// Its idempotency record is written in the same transaction as the write,
// so it exists exactly when the write committed.
func reservation(ctx context.Context, q policy.Querier, ns, typeID, held string, source uuid.UUID,
	requestedSeq int64) (reservationRow, bool, error) {
	prefix := fmt.Sprintf("%s:%s:%d:", held, source, requestedSeq)
	var r reservationRow
	var res WriteResult
	err := q.QueryRow(ctx,
		`select key, result from idempotency
		 where namespace_id = $1 and actor = $2 and starts_with(key, $3) and result ? 'item_id'`,
		ns, item.KindReaction+":"+typeID+":"+held, prefix).Scan(&r.key, &res)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, false, nil
	}
	if err != nil {
		return r, false, fmt.Errorf("read reservation: %w", err)
	}
	r.target, r.seq = res.ItemID, res.ItemSeq
	return r, true, nil
}

// ReactionFailedRequest records on the source that a reaction gave up.
type ReactionFailedRequest struct {
	IdempotencyKey string // reaction_failed:<the reaction's own key>
	Principal      item.Principal
	SourceID       uuid.UUID
	Reason         string // a code
	Attribution    item.Attribution
}

// ReactionFailed writes reaction_failed on the source (no field or state
// change). It is not retried: someone must look.
func (in *Internal) ReactionFailed(ctx context.Context, req ReactionFailedRequest) (WriteResult, error) {
	k := in.k
	if req.IdempotencyKey == "" {
		return WriteResult{}, fmt.Errorf("%w: an idempotency key is required", ErrInvalid)
	}
	if !reasonCode.MatchString(req.Reason) {
		return WriteResult{}, fmt.Errorf("%w: reaction_failed needs a reason code, not free text", ErrInvalid)
	}
	kc, err := checkKernel(req.Principal, req.SourceID, req.Attribution)
	if err != nil {
		return WriteResult{}, err
	}
	if kc.reactionID == "" {
		return WriteResult{}, fmt.Errorf("%w: only a reaction records reaction_failed", ErrInvalid)
	}
	if req.Principal, err = k.inItemsNamespace(ctx, req.Principal, req.SourceID); err != nil {
		return WriteResult{}, err
	}
	hash, err := requestHash("reaction_failed", req)
	if err != nil {
		return WriteResult{}, err
	}
	return k.write(ctx, req.Principal, req.IdempotencyKey, hash, func(tx pgx.Tx) (WriteResult, error) {
		it, err := lockItem(ctx, tx, nsOf(req.Principal), req.SourceID)
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
		if _, ok := ty.Reaction(kc.reactionID); !ok {
			return WriteResult{}, fmt.Errorf("%w: %s v%d declares no reaction %s", ErrInvalid, ty.ID, ty.Version, kc.reactionID)
		}
		payload := map[string]any{"reaction": typedef.ReactionKey(ty.ID, kc.reactionID), "reason": req.Reason}
		bump, err := decideContent(ty, lifecycle.OpReactionFailed, payload)
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

// DenyReason turns a target's refusal of a held reaction into the hold's
// deny reason: an invariant's ID for INVARIANT_FAILED, else the refusal's
// code. ok is false for an error that is not a refusal (retry it).
func DenyReason(err error) (reason string, ok bool) {
	var rej *RejectedError
	if !errors.As(err, &rej) || rej.Code == "" {
		return "", false
	}
	if rej.Code == CodeInvariantFailed && rej.Metadata["invariant"] != "" {
		return rej.Metadata["invariant"], true
	}
	return rej.Code, true
}

// PendingUndo is an undo not yet done: a reservation committed for a hold
// that closed non-allow, or whose state was left by the undo's trigger.
type PendingUndo struct {
	Reaction      string    // the undo reaction's ID
	HoldID        uuid.UUID // the hold whose reservation it reverses
	CauseSeq      int64     // the event that closed the hold, or left the state
	CorrelationID string
	Hop           int // of that event
}

// pendingUndos finds the undos not yet done for the Item's holds decided
// by a held reaction: a deduction committed, its undo due (the hold closed
// non-allow, or its state left by the undo's trigger), and the undo's key
// not yet used. A workflow run that starts after a crash resumes them.
func (k *Service) pendingUndos(ctx context.Context, id uuid.UUID, ty *typedef.Type) ([]PendingUndo, error) {
	var held []string
	for _, r := range ty.Reactions {
		if r.Kind == typedef.ReactionHeld {
			held = append(held, r.ID)
		}
	}
	if len(held) == 0 {
		return nil, nil
	}
	var ns string
	if err := k.pool.QueryRow(ctx, `select namespace_id from items where id = $1`, id).Scan(&ns); err != nil {
		return nil, fmt.Errorf("read namespace: %w", err)
	}
	rows, err := k.pool.Query(ctx,
		`select id, hook, from_state, to_state, status, requested_seq, coalesce(resolved_seq, 0)
		 from item_holds where item_id = $1 and hook = any($2) and status <> 'open' order by requested_seq`, id, held)
	if err != nil {
		return nil, fmt.Errorf("read holds: %w", err)
	}
	type holdRow struct {
		id uuid.UUID
		reactionHoldRow
	}
	holds, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (holdRow, error) {
		var h holdRow
		err := r.Scan(&h.id, &h.decider, &h.from, &h.to, &h.status, &h.requestedSeq, &h.closed)
		return h, err
	})
	if err != nil {
		return nil, fmt.Errorf("read holds: %w", err)
	}
	var out []PendingUndo
	for _, h := range holds {
		undo, ok := lifecycle.UndoOf(ty, h.decider)
		if !ok {
			continue
		}
		cause, due, err := undoDue(ctx, k.pool, id, h.reactionHoldRow, undo)
		if err != nil {
			return nil, err
		}
		if !due {
			continue
		}
		resv, ok, err := reservation(ctx, k.pool, ns, ty.ID, h.decider, id, h.requestedSeq)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue // nothing was taken
		}
		// Done once the refund committed and, when hooks run on it (the
		// refund email), once reaction_applied recorded it too: a crash
		// between the two leaves the undo pending, and the new run's Undo
		// (a no-op under its key) records it.
		keys := []string{resv.key + ":" + undo.ID}
		if len(lifecycle.ReactionHooks(ty, undo.ID)) > 0 {
			keys = append(keys, resv.key+":"+undo.ID+":applied")
		}
		var found int
		if err := k.pool.QueryRow(ctx,
			`select count(*) from idempotency where namespace_id = $1 and actor = $2 and key = any($3)`,
			ns, item.KindReaction+":"+ty.ID+":"+undo.ID, keys).Scan(&found); err != nil {
			return nil, fmt.Errorf("read the undo's key: %w", err)
		}
		if found == len(keys) {
			continue
		}
		p := PendingUndo{Reaction: undo.ID, HoldID: h.id, CauseSeq: cause}
		if err := k.pool.QueryRow(ctx,
			`select coalesce(correlation_id, ''), hop from item_events where item_id = $1 and item_seq = $2`,
			id, cause).Scan(&p.CorrelationID, &p.Hop); err != nil {
			return nil, fmt.Errorf("read the undo's cause: %w", err)
		}
		out = append(out, p)
	}
	return out, nil
}
