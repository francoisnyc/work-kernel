package itemservice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/lifecycle"
	"github.com/francoisnyc/work-kernel/internal/policy"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// Move and create reactions (Jira part 2, step 2; docs/design.md, "Jira
// part 2, step 2 as built").
//
// One reaction, for one trigger, is two writes of one Item each: the
// target write (a move, or a create), under a key of its own, then one
// OUTCOME RECORD on the Item that declares the reaction: a field-less
// event caused by the trigger. The record is what makes a reaction
// finished. Until it exists the reaction is pending (StateEntry), so a
// restart runs it again; each write is keyed, so running it again changes
// nothing that is already done.

// The outcomes of a move or create reaction for one trigger.
const (
	ReactionApplied = "applied" // the target write committed (reaction_applied)
	ReactionFailed  = "failed"  // the target refused it (reaction_failed, Reason its code); not retried
	ReactionSkipped = "skipped" // it rightly did nothing (reaction_skipped, Reason says why)
)

// Why a reaction was skipped.
const (
	SkipConditionFalse = "condition_false" // its `when` does not hold
	SkipNotBy          = "not_by"          // the Item inside was created by someone else than `by` says
	SkipNotInFrom      = "not_in_from"     // the target is not in a state the reaction moves from
	SkipNoTarget       = "no_target"       // its link has no target
)

// ReactionRunRequest runs one move or create reaction for one trigger.
type ReactionRunRequest struct {
	// SourceID is the Item whose Type declares the reaction; Reaction its ID.
	SourceID uuid.UUID
	Reaction string
	// The triggering event. on: enter: the event of SourceID that entered
	// the state. on: created_inside: the create event (seq 1) of the Item
	// created inside SourceID.
	CauseItem uuid.UUID
	CauseSeq  int64
	// Attribution of the reaction's writes: the cause's correlation ID, and
	// its hop + 1.
	CorrelationID string
	Hop           int
}

// ReactionOutcome is what the reaction did, as recorded.
type ReactionOutcome struct {
	Outcome string // ReactionApplied, ReactionFailed or ReactionSkipped
	Reason  string // failed: the refusal's code; skipped: why
	// Target is the Item written (moved or created), or the Item that
	// refused; uuid.Nil if none.
	Target uuid.UUID
	// Record is the outcome's event on SourceID.
	Record WriteResult
	// Each (a reaction that moves children; Jira part 2, step 3) holds one
	// outcome per child moved or refused, in the order of their IDs. The
	// fields above are then the last of them, or the one record that says
	// nothing was moved.
	Each []ReactionOutcome
}

// PendingReaction is a move or create reaction not yet finished for one
// trigger: what the workflow needs to run it, opaque IDs only.
type PendingReaction struct {
	Reaction      string
	CauseItem     uuid.UUID
	CauseSeq      int64
	CorrelationID string
	Hop           int // of the cause
}

// moveCause is what a reaction's move carries into the transition code: it
// stands in for `allowed` (the source's own rule, or the target Type's
// consent), and brings the cause's time.
type moveCause struct {
	recorded item.Principal // the reaction, on behalf of the cause's human
	causeAt  time.Time      // the triggering event's time: cause.at in the target's effects
	froms    []string       // the target is moved only from these states; empty: any
	consent  string         // the reaction key the target's Type must accept; "" when it moves itself
}

// errNotInFrom is the transition code's way of saying "the target is not in
// a state this reaction moves from": nothing was written.
var errNotInFrom = errors.New("itemservice: the target is not in a state the reaction moves from")

// trigger is a reaction's checked trigger: the reaction, the Types
// involved, and the triggering event.
type trigger struct {
	req     ReactionRunRequest
	ns      string
	ty      *typedef.Type // the source's
	r       typedef.Reaction
	source  item.Item // as read now, outside any lock: created_by, fields
	fields  map[string]any
	causeAt time.Time
	// The cause's actor and the human at its root.
	actor, onBehalfOf string
	// created_inside: the Item created inside, and its Type.
	childType   *typedef.Type
	childFields map[string]any
}

func (tg trigger) key() string       { return typedef.ReactionKey(tg.ty.ID, tg.r.ID) }
func (tg trigger) causation() string { return fmt.Sprintf("%s:%d", tg.req.CauseItem, tg.req.CauseSeq) }
func (tg trigger) principal() item.Principal {
	return item.Principal{Actor: item.KindReaction + ":" + tg.key(), Namespace: tg.ns}
}

// recorded is how the reaction's writes are recorded: the reaction as
// actor, the human at the root of the cause as on_behalf_of (attribution
// only, never checked).
func (tg trigger) recorded() item.Principal {
	p := tg.principal()
	p.OnBehalfOf = tg.onBehalfOf
	if p.OnBehalfOf == "" && item.IsKind(tg.actor, item.KindUser) {
		p.OnBehalfOf = tg.actor
	}
	return p
}

// attribution of a write at this hop.
func (tg trigger) attribution(hop int) item.Attribution {
	return item.Attribution{
		OriginatingHook: fmt.Sprintf("%s:%s@v%d:%s", item.KindReaction, tg.ty.ID, tg.ty.Version, tg.r.ID),
		CausationID:     tg.causation(), CorrelationID: tg.req.CorrelationID, Hop: hop,
	}
}

// loadTrigger checks that the request names a real trigger of a reaction
// of the given kind, and reads what judging it needs. Anything else is
// ErrInvalid: the caller is broken, and nothing is written.
func (k *Service) loadTrigger(ctx context.Context, req ReactionRunRequest, kind typedef.ReactionKind) (trigger, error) {
	tg := trigger{req: req}
	invalid := func(format string, args ...any) (trigger, error) {
		return tg, fmt.Errorf("%w: reaction: "+format, append([]any{ErrInvalid}, args...)...)
	}
	if req.Hop < 1 {
		return invalid("a kernel write is at least one hop from its cause")
	}
	var typeID string
	var version int
	var createdBy, createdFor string
	err := k.pool.QueryRow(ctx,
		`select namespace_id, type_id, type_version, fields, created_by, coalesce(created_on_behalf_of, '')
		 from items where id = $1`, req.SourceID,
	).Scan(&tg.ns, &typeID, &version, &tg.fields, &createdBy, &createdFor)
	if errors.Is(err, pgx.ErrNoRows) {
		return tg, ErrNotFound
	}
	if err != nil {
		return tg, fmt.Errorf("read source: %w", err)
	}
	tg.source = item.Item{ID: req.SourceID, CreatedBy: createdBy, CreatedOnBehalfOf: createdFor}
	if tg.ty, err = k.loadType(ctx, k.pool, typeID, version); err != nil {
		return tg, err
	}
	var ok bool
	if tg.r, ok = tg.ty.Reaction(req.Reaction); !ok || tg.r.Kind != kind {
		return invalid("%s v%d declares no %s reaction %q", typeID, version, kind, req.Reaction)
	}

	// The triggering event.
	var op string
	var payload []byte
	err = k.pool.QueryRow(ctx,
		`select op, payload, created_at, actor, coalesce(on_behalf_of, '') from item_events
		 where item_id = $1 and item_seq = $2`, req.CauseItem, req.CauseSeq,
	).Scan(&op, &payload, &tg.causeAt, &tg.actor, &tg.onBehalfOf)
	if errors.Is(err, pgx.ErrNoRows) {
		return invalid("the cause %s is no event", tg.causation())
	}
	if err != nil {
		return tg, fmt.Errorf("read cause: %w", err)
	}
	var shape struct {
		State     string `json:"state"`
		From      string `json:"from"`
		To        string `json:"to"`
		Container string `json:"container_id"`
		TypeID    string `json:"type_id"`
		Version   int    `json:"type_version"`
	}
	if err := json.Unmarshal(payload, &shape); err != nil {
		return tg, fmt.Errorf("decode cause: %w", err)
	}
	switch tg.r.On {
	case typedef.OnCreatedInside:
		// The cause is the create of an Item of one of `of`, made inside
		// the source.
		if op != "create" || req.CauseSeq != 1 || shape.Container != req.SourceID.String() ||
			!slices.Contains(tg.r.Of, shape.TypeID) {
			return invalid("%s is not the creation, inside this Item, of an Item %s reacts to", tg.causation(), tg.r.ID)
		}
		if tg.childType, err = k.loadType(ctx, k.pool, shape.TypeID, shape.Version); err != nil {
			return tg, err
		}
		if err := k.pool.QueryRow(ctx, `select fields from items where id = $1`, req.CauseItem).Scan(&tg.childFields); err != nil {
			return tg, fmt.Errorf("read the Item created inside: %w", err)
		}
	default:
		// The cause is the source's own event that entered the state,
		// from a state the reaction runs from.
		entered := shape.To
		if op == "create" {
			entered = shape.State
		}
		runs := slices.ContainsFunc(lifecycle.EnterReactions(tg.ty, shape.From, entered),
			func(r typedef.Reaction) bool { return r.ID == tg.r.ID })
		if req.CauseItem != req.SourceID || (op != "transition" && op != "create") || !runs {
			return invalid("%s is not an entry that runs %s", tg.causation(), tg.r.ID)
		}
	}
	return tg, nil
}

// outcomeOf reads the outcome record of this reaction for this trigger, if
// there is one.
func (k *Service) outcomeOf(ctx context.Context, tg trigger) (ReactionOutcome, bool, error) {
	var out ReactionOutcome
	var op string
	var payload []byte
	err := k.pool.QueryRow(ctx,
		`select item_seq, commit_seq, op, payload from item_events
		 where item_id = $1 and causation_id = $2 and op in ($3, $4, $5) and payload ->> 'reaction' = $6
		 order by item_seq limit 1`,
		tg.req.SourceID, tg.causation(), lifecycle.OpReactionApplied, lifecycle.OpReactionFailed, lifecycle.OpReactionSkipped,
		tg.key(),
	).Scan(&out.Record.ItemSeq, &out.Record.CommitSeq, &op, &payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, false, nil
	}
	if err != nil {
		return out, false, fmt.Errorf("read the reaction's record: %w", err)
	}
	out.Record.ItemID = tg.req.SourceID
	var pl struct {
		Reason string `json:"reason"`
		Target string `json:"target"`
	}
	if err := json.Unmarshal(payload, &pl); err != nil {
		return out, false, fmt.Errorf("decode the reaction's record: %w", err)
	}
	out.Reason = pl.Reason
	out.Target, _ = uuid.Parse(pl.Target)
	out.Outcome = map[string]string{
		lifecycle.OpReactionApplied: ReactionApplied, lifecycle.OpReactionFailed: ReactionFailed,
		lifecycle.OpReactionSkipped: ReactionSkipped,
	}[op]
	return out, true, nil
}

// record writes the reaction's one outcome record on the source: a
// field-less event caused by the trigger, under a key of its own. If the
// record already exists (a retry, or two runs at once) it is left as it
// is, and what it says is returned: the first outcome stands.
func (k *Service) record(ctx context.Context, tg trigger, outcome, reason string, target uuid.UUID) (ReactionOutcome, error) {
	op := map[string]string{
		ReactionApplied: lifecycle.OpReactionApplied, ReactionFailed: lifecycle.OpReactionFailed,
		ReactionSkipped: lifecycle.OpReactionSkipped,
	}[outcome]
	payload := map[string]any{"reaction": tg.key()}
	if reason != "" {
		payload["reason"] = reason
	}
	if target != uuid.Nil {
		payload["target"] = target.String()
	}
	// The key and the hash name the trigger, not the outcome: one record
	// per reaction and trigger, whatever a later attempt would conclude.
	key := fmt.Sprintf("%s:%s:%s:done", tg.key(), tg.req.SourceID, tg.causation())
	hash, err := requestHash("reaction_record", []string{tg.key(), tg.req.SourceID.String(), tg.causation()})
	if err != nil {
		return ReactionOutcome{}, err
	}
	// A reaction one hop too deep still gets its record (hop_limit): the
	// record itself sits at the limit.
	attr := tg.attribution(min(tg.req.Hop, MaxHop))
	_, err = k.write(ctx, tg.principal(), key, hash, func(tx pgx.Tx) (WriteResult, error) {
		it, err := lockItem(ctx, tx, tg.ns, tg.req.SourceID)
		if err != nil {
			return WriteResult{}, err
		}
		itemSeq := it.ItemSeq + 1
		if _, err := tx.Exec(ctx, `update items set item_seq = $2, updated_at = now() where id = $1`,
			tg.req.SourceID, itemSeq); err != nil {
			return WriteResult{}, fmt.Errorf("update item: %w", err)
		}
		return appendEvent(ctx, tx, tg.ty, tg.req.SourceID, itemSeq, tg.recorded(), attr, op, payload, nil)
	})
	if err != nil {
		return ReactionOutcome{}, err
	}
	out, found, err := k.outcomeOf(ctx, tg)
	if err != nil {
		return ReactionOutcome{}, err
	}
	if !found {
		return ReactionOutcome{}, fmt.Errorf("the reaction's record was written but cannot be read")
	}
	return out, nil
}

// refusal turns a target's refusal into the reaction's failure reason: an
// invariant's ID, else the refusal's code. ok is false for an error that
// is no refusal (the database is away, the target is held): retry it.
func refusal(err error) (string, bool) {
	if errors.Is(err, ErrHeld) || errors.Is(err, ErrStale) {
		return "", false
	}
	if reason, ok := DenyReason(err); ok {
		return reason, true
	}
	if errors.Is(err, ErrForbidden) {
		return "not_allowed", true
	}
	return "", false
}

// ReactMove runs a move reaction for one trigger: it judges the
// conditions, makes the move on the target under the key
// <reaction>:<source>:<cause>:<target>, and records the outcome on the
// source. Safe to call again: each write is keyed, and a recorded outcome
// is returned as it is.
//
// A target that refuses (a guard, a required field, an invariant, no such
// transition) is an outcome, ReactionFailed, not an error. An error is
// something that may pass (the database is away, the target is held): the
// caller retries.
func (in *Internal) ReactMove(ctx context.Context, req ReactionRunRequest) (ReactionOutcome, error) {
	k := in.k
	tg, err := k.loadTrigger(ctx, req, typedef.ReactionMove)
	if err != nil {
		return ReactionOutcome{}, err
	}
	if out, done, err := k.outcomeOf(ctx, tg); err != nil || done {
		return out, err
	}
	if req.Hop > MaxHop {
		return k.record(ctx, tg, ReactionFailed, CodeHopLimit, uuid.Nil)
	}

	mc := &moveCause{recorded: tg.recorded(), causeAt: tg.causeAt}
	target := req.SourceID
	if tg.r.On == typedef.OnCreatedInside {
		// The condition is about the Item created; who made it is judged
		// against the container's creator, as `allowed: [creator]` is.
		if !lifecycle.ChildWhen(tg.r, tg.childType, tg.childFields) {
			return k.record(ctx, tg, ReactionSkipped, SkipConditionFalse, uuid.Nil)
		}
		if tg.r.By == typedef.ByCreator &&
			!policy.IsRequester(tg.actor, tg.onBehalfOf, tg.source.CreatedBy, tg.source.CreatedOnBehalfOf) {
			return k.record(ctx, tg, ReactionSkipped, SkipNotBy, uuid.Nil)
		}
		mc.froms = tg.r.From
	} else {
		if !lifecycle.ReactionWhen(tg.ty, tg.r, tg.fields) {
			return k.record(ctx, tg, ReactionSkipped, SkipConditionFalse, uuid.Nil)
		}
		err := k.pool.QueryRow(ctx, `select target_id from links where source_id = $1 and link_type = $2`,
			req.SourceID, tg.r.Link).Scan(&target)
		if errors.Is(err, pgx.ErrNoRows) {
			return k.record(ctx, tg, ReactionSkipped, SkipNoTarget, uuid.Nil)
		}
		if err != nil {
			return ReactionOutcome{}, fmt.Errorf("read link: %w", err)
		}
		mc.froms, mc.consent = tg.r.TargetFrom, tg.key()
	}

	_, _, err = k.transition(ctx, TransitionRequest{
		IdempotencyKey: fmt.Sprintf("%s:%s:%s:%s", tg.key(), req.SourceID, tg.causation(), target),
		Principal:      tg.principal(), ItemID: target, ToState: tg.r.To,
		Attribution: tg.attribution(req.Hop),
	}, &kernelCause{reactionID: tg.r.ID, typeID: tg.ty.ID, typeVersion: tg.ty.Version, move: mc})
	switch {
	case err == nil:
		return k.record(ctx, tg, ReactionApplied, "", target)
	case errors.Is(err, errNotInFrom):
		return k.record(ctx, tg, ReactionSkipped, SkipNotInFrom, uuid.Nil)
	}
	if reason, refused := refusal(err); refused {
		return k.record(ctx, tg, ReactionFailed, reason, target)
	}
	return ReactionOutcome{}, err
}

// ReactCreate runs a create reaction for one trigger, like ReactMove: it
// creates an Item of the reaction's Type version inside the source, under
// the key <reaction>:<source>:<cause>:create, then records the outcome.
//
// The created Item has no human creator: created_by is the reaction and
// created_on_behalf_of stays empty, so nobody is its "creator" to a rule.
// Its create event carries the attribution (the human at the root of the
// cause). Its readers are its container's.
func (in *Internal) ReactCreate(ctx context.Context, req ReactionRunRequest) (ReactionOutcome, error) {
	k := in.k
	tg, err := k.loadTrigger(ctx, req, typedef.ReactionCreate)
	if err != nil {
		return ReactionOutcome{}, err
	}
	if out, done, err := k.outcomeOf(ctx, tg); err != nil || done {
		return out, err
	}
	if req.Hop > MaxHop {
		return k.record(ctx, tg, ReactionFailed, CodeHopLimit, uuid.Nil)
	}
	if !lifecycle.ReactionWhen(tg.ty, tg.r, tg.fields) {
		return k.record(ctx, tg, ReactionSkipped, SkipConditionFalse, uuid.Nil)
	}
	container := req.SourceID
	if tg.r.Link != "" {
		err := k.pool.QueryRow(ctx, `select target_id from links where source_id = $1 and link_type = $2`,
			req.SourceID, tg.r.Link).Scan(&container)
		if errors.Is(err, pgx.ErrNoRows) {
			return k.record(ctx, tg, ReactionSkipped, SkipNoTarget, uuid.Nil)
		}
		if err != nil {
			return ReactionOutcome{}, fmt.Errorf("read link: %w", err)
		}
	}
	created, err := k.loadType(ctx, k.pool, tg.r.Type, tg.r.Version)
	if err != nil {
		return ReactionOutcome{}, err
	}
	// Consent, at write time: the created Type accepts this reaction, for
	// the fields it sets.
	if !created.AcceptsCreate(tg.key(), slices.Sorted(maps.Keys(tg.r.Fields))) {
		return k.record(ctx, tg, ReactionFailed, CodeReactionNotAccepted, uuid.Nil)
	}
	// The fields, from the source's and the cause's time.
	fields, err := lifecycle.CreateFields(tg.ty, tg.r, created, lifecycle.Input{Fields: tg.fields, CauseAt: tg.causeAt})
	if err != nil {
		if reason, refused := refusal(fromLifecycle(err)); refused {
			return k.record(ctx, tg, ReactionFailed, reason, uuid.Nil)
		}
		return ReactionOutcome{}, err
	}
	if err := checkInvariants(created, fields); err != nil {
		if reason, refused := refusal(err); refused {
			return k.record(ctx, tg, ReactionFailed, reason, uuid.Nil)
		}
		return ReactionOutcome{}, err
	}

	key := fmt.Sprintf("%s:%s:%s:create", tg.key(), req.SourceID, tg.causation())
	hash, err := requestHash("react_create", req)
	if err != nil {
		return ReactionOutcome{}, err
	}
	attr := tg.attribution(req.Hop)
	res, err := k.write(ctx, tg.principal(), key, hash, func(tx pgx.Tx) (WriteResult, error) {
		id, err := uuid.NewV7()
		if err != nil {
			return WriteResult{}, fmt.Errorf("new item id: %w", err)
		}
		// In its container's scope: the container's readers are its own.
		scopeID, err := childScope(ctx, tx, tg.ns, &container)
		if err != nil {
			return WriteResult{}, err
		}
		at, err := stampWrite(ctx, tx)
		if err != nil {
			return WriteResult{}, err
		}
		const itemSeq = 1
		_, err = tx.Exec(ctx,
			`insert into items (id, namespace_id, type_id, type_version, container_id, scope_id, state, fields, item_seq,
			                    created_at, created_by, created_on_behalf_of)
			 values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, null)`,
			id, tg.ns, created.ID, created.Version, container, scopeID, created.InitialState, fields, itemSeq,
			at, tg.principal().Actor)
		if err != nil {
			return WriteResult{}, fmt.Errorf("insert item: %w", err)
		}
		payload := map[string]any{
			"type_id": created.ID, "type_version": created.Version, "state": created.InitialState,
			"field_ids": sortedKeys(fields), "container_id": container,
		}
		res, err := appendEvent(ctx, tx, created, id, itemSeq, tg.recorded(), attr, "create", payload, fields)
		if err != nil {
			return WriteResult{}, err
		}
		return res, k.tellContainer(ctx, tx, created, &container, id, itemSeq)
	})
	if err != nil {
		if reason, refused := refusal(err); refused {
			return k.record(ctx, tg, ReactionFailed, reason, uuid.Nil)
		}
		return ReactionOutcome{}, err
	}
	return k.record(ctx, tg, ReactionApplied, "", res.ItemID)
}

// ReactionGaveUp records that a move or create reaction could not be
// finished: the workflow's budget for it ran out, or it failed for good.
// It writes the reaction's outcome record as failed, with the reason (a
// code), unless an outcome is already recorded, which then stands.
func (in *Internal) ReactionGaveUp(ctx context.Context, req ReactionRunRequest, reason string) (ReactionOutcome, error) {
	if !reasonCode.MatchString(reason) {
		return ReactionOutcome{}, fmt.Errorf("%w: a reaction's failure needs a reason code, not free text", ErrInvalid)
	}
	tg, err := in.k.loadAnyTrigger(ctx, req)
	if err != nil {
		return ReactionOutcome{}, err
	}
	if out, done, err := in.k.outcomeOf(ctx, tg); err != nil || done {
		return out, err
	}
	return in.k.record(ctx, tg, ReactionFailed, reason, uuid.Nil)
}

// loadAnyTrigger is loadTrigger for a reaction of either kind.
func (k *Service) loadAnyTrigger(ctx context.Context, req ReactionRunRequest) (trigger, error) {
	tg, err := k.loadTrigger(ctx, req, typedef.ReactionMove)
	if err != nil && errors.Is(err, ErrInvalid) {
		return k.loadTrigger(ctx, req, typedef.ReactionCreate)
	}
	return tg, err
}

// tellContainer gives a newly created Item an outbox row when its
// container's Type reacts to Items of its Type being created inside it
// (on: created_inside), and the Item's own Type has no listeners (else
// appendEvent wrote the row already). The relay then tells the container's
// workflow. One read of the container per create; a container that
// declares nothing costs the new Item nothing.
func (k *Service) tellContainer(ctx context.Context, tx pgx.Tx, ty *typedef.Type, container *uuid.UUID, id uuid.UUID, itemSeq int64) error {
	if container == nil || lifecycle.HasListeners(ty) {
		return nil
	}
	reacts, err := k.containerReacts(ctx, tx, *container, ty.ID)
	if err != nil || !reacts {
		return err
	}
	if _, err := tx.Exec(ctx, `insert into outbox (item_id, item_seq) values ($1, $2)`, id, itemSeq); err != nil {
		return fmt.Errorf("insert outbox: %w", err)
	}
	return nil
}

// containerReacts reports whether the container's Type has a reaction
// triggered by the creation of an Item of childType inside it.
func (k *Service) containerReacts(ctx context.Context, q policy.Querier, container uuid.UUID, childType string) (bool, error) {
	var typeID string
	var version int
	err := q.QueryRow(ctx, `select type_id, type_version from items where id = $1`, container).Scan(&typeID, &version)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read the container's Type: %w", err)
	}
	ty, err := k.loadType(ctx, q, typeID, version)
	if err != nil {
		return false, err
	}
	return lifecycle.ReactsToChildren(ty, childType), nil
}

// pendingReactions finds, from the events, every move or create reaction
// of the Item not yet finished: each trigger (an entry into a reaction's
// state from a state it runs from; an Item of one of its `of` Types created
// inside) with no outcome record. For any entry, not just the current one:
// a reaction is never dropped because the Item moved on, or the worker
// died. Oldest trigger first, then the Type's order.
func (k *Service) pendingReactions(ctx context.Context, id uuid.UUID, ty *typedef.Type) ([]PendingReaction, error) {
	var enters, insides bool
	var of []string
	for _, r := range ty.Reactions {
		if r.Kind != typedef.ReactionMove && r.Kind != typedef.ReactionCreate {
			continue
		}
		if r.On == typedef.OnCreatedInside {
			insides, of = true, append(of, r.Of...)
		} else {
			enters = true
		}
	}
	if !enters && !insides {
		return nil, nil
	}
	// The outcome records there are: "<reaction key>|<cause>".
	rows, err := k.pool.Query(ctx,
		`select payload ->> 'reaction' || '|' || causation_id from item_events
		 where item_id = $1 and op in ($2, $3, $4) and causation_id is not null`,
		id, lifecycle.OpReactionApplied, lifecycle.OpReactionFailed, lifecycle.OpReactionSkipped)
	if err != nil {
		return nil, fmt.Errorf("read reaction records: %w", err)
	}
	recorded, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("read reaction records: %w", err)
	}
	done := func(r typedef.Reaction, causeItem uuid.UUID, seq int64) bool {
		return slices.Contains(recorded, fmt.Sprintf("%s|%s:%d", typedef.ReactionKey(ty.ID, r.ID), causeItem, seq))
	}

	type cause struct {
		at        time.Time
		item      uuid.UUID
		seq       int64
		from, to  string // an entry
		childType string // a child's create
		corr      string
		hop       int
	}
	var causes []cause
	if enters {
		rows, err := k.pool.Query(ctx,
			`select created_at, item_seq, coalesce(payload ->> 'from', ''), coalesce(payload ->> 'to', ''),
			        coalesce(correlation_id, ''), hop
			 from item_events where item_id = $1 and op = 'transition' order by item_seq`, id)
		if err != nil {
			return nil, fmt.Errorf("read state entries: %w", err)
		}
		entries, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (cause, error) {
			c := cause{item: id}
			return c, r.Scan(&c.at, &c.seq, &c.from, &c.to, &c.corr, &c.hop)
		})
		if err != nil {
			return nil, fmt.Errorf("read state entries: %w", err)
		}
		causes = append(causes, entries...)
	}
	if insides {
		// Items created inside this one (not moved in later): their create
		// event names this Item as the container.
		rows, err := k.pool.Query(ctx,
			`select e.created_at, c.id, c.type_id, coalesce(e.correlation_id, ''), e.hop
			 from items c join item_events e on e.item_id = c.id and e.item_seq = 1
			 where c.container_id = $1 and c.type_id = any($2) and e.payload ->> 'container_id' = $1::text
			 order by e.created_at, c.id`, id, of)
		if err != nil {
			return nil, fmt.Errorf("read the Items created inside: %w", err)
		}
		children, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (cause, error) {
			c := cause{seq: 1}
			return c, r.Scan(&c.at, &c.item, &c.childType, &c.corr, &c.hop)
		})
		if err != nil {
			return nil, fmt.Errorf("read the Items created inside: %w", err)
		}
		causes = append(causes, children...)
	}
	// Go's sort is stable only when asked: SortStableFunc keeps the order
	// above for equal times.
	slices.SortStableFunc(causes, func(a, b cause) int { return a.at.Compare(b.at) })

	var pending []PendingReaction
	for _, c := range causes {
		rs := lifecycle.EnterReactions(ty, c.from, c.to)
		if c.childType != "" {
			rs = lifecycle.InsideReactions(ty, c.childType)
		}
		for _, r := range rs {
			if !done(r, c.item, c.seq) {
				pending = append(pending, PendingReaction{
					Reaction: r.ID, CauseItem: c.item, CauseSeq: c.seq, CorrelationID: c.corr, Hop: c.hop,
				})
			}
		}
	}
	return pending, nil
}
