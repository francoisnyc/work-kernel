package itemservice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/lifecycle"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// Jira part 2, step 3: reactions that move children or set a field on the
// container, a create reaction's reads of linked Items, and acting timers
// at an instant (docs/design.md, "Jira part 2, step 3a as built").

// MaxReactionTargets is the most contained Items one reaction moves for
// one trigger. More match: nothing is moved, and the reaction fails with
// CodeTooManyTargets.
const MaxReactionTargets = 8

// Why a step 3 reaction failed.
const (
	CodeTooManyTargets  = "too_many_targets"  // more children match than MaxReactionTargets
	CodeNoContainer     = "no_container"      // the source is contained in nothing
	CodeReadNotAccepted = "read_not_accepted" // the linked Item's Type version does not let reactions read the field
)

// --- a reaction that moves children ---

// childRecord is one child's outcome record, as read back.
type childRecord struct {
	out  ReactionOutcome
	more bool
}

// childRecords reads the records this reaction wrote for this trigger that
// name a target, by target.
func (k *Service) childRecords(ctx context.Context, tg trigger) (map[uuid.UUID]childRecord, error) {
	rows, err := k.pool.Query(ctx,
		`select item_seq, commit_seq, op, payload from item_events
		 where item_id = $1 and causation_id = $2 and op in ($3, $4, $5) and payload ->> 'reaction' = $6
		 order by item_seq`,
		tg.req.SourceID, tg.causation(), lifecycle.OpReactionApplied, lifecycle.OpReactionFailed, lifecycle.OpReactionSkipped, tg.key())
	if err != nil {
		return nil, fmt.Errorf("read the reaction's records: %w", err)
	}
	defer rows.Close()
	out := map[uuid.UUID]childRecord{}
	for rows.Next() {
		var rec childRecord
		var op string
		var payload []byte
		if err := rows.Scan(&rec.out.Record.ItemSeq, &rec.out.Record.CommitSeq, &op, &payload); err != nil {
			return nil, fmt.Errorf("read the reaction's records: %w", err)
		}
		var pl struct {
			Reason, Target string
			More           bool
		}
		if err := json.Unmarshal(payload, &pl); err != nil {
			return nil, fmt.Errorf("decode the reaction's record: %w", err)
		}
		target, err := uuid.Parse(pl.Target)
		if err != nil {
			continue // the one record of a reaction that moved nothing
		}
		rec.out.Record.ItemID = tg.req.SourceID
		rec.out.Reason, rec.out.Target, rec.more = pl.Reason, target, pl.More
		rec.out.Outcome = map[string]string{
			lifecycle.OpReactionApplied: ReactionApplied, lifecycle.OpReactionFailed: ReactionFailed,
			lifecycle.OpReactionSkipped: ReactionSkipped,
		}[op]
		out[target] = rec
	}
	return out, rows.Err()
}

// childrenOutcome is a finished children reaction's outcome: one entry per
// child, in the order of their IDs; the reaction's own is the last.
func childrenOutcome(recs map[uuid.UUID]childRecord) ReactionOutcome {
	ids := slices.SortedFunc(maps.Keys(recs), func(a, b uuid.UUID) int { return strings.Compare(a.String(), b.String()) })
	var each []ReactionOutcome
	for _, id := range ids {
		each = append(each, recs[id].out)
	}
	out := each[len(each)-1]
	out.Each = each
	return out
}

// moveChildren runs a move reaction on the Items contained in the source:
// those of one of children.of for which children.when holds, in a state
// the reaction moves from. Each is one write of its own (one Item per
// write), under the key <reaction>:<source>:<cause>:<child>, followed by
// one record of its own on the source. Every record but the last says
// `more`: the one that does not finishes the reaction, so a crash between
// two children leaves it pending, and running it again moves no child
// twice (the keys) and records none twice.
//
// More than MaxReactionTargets children to move: none is moved, and the
// reaction fails once, too_many_targets. None: one record, no_target.
func (k *Service) moveChildren(ctx context.Context, tg trigger, mc *moveCause) (ReactionOutcome, error) {
	recs, err := k.childRecords(ctx, tg)
	if err != nil {
		return ReactionOutcome{}, err
	}
	rows, err := k.pool.Query(ctx,
		`select id, type_id, type_version, state, fields from items
		 where container_id = $1 and namespace_id = $2 and type_id = any($3)`,
		tg.req.SourceID, tg.ns, tg.r.Children.Of)
	if err != nil {
		return ReactionOutcome{}, fmt.Errorf("read the contained Items: %w", err)
	}
	type child struct {
		id      uuid.UUID
		typeID  string
		version int
		state   string
		fields  map[string]any
	}
	all, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (child, error) {
		var c child
		return c, r.Scan(&c.id, &c.typeID, &c.version, &c.state, &c.fields)
	})
	if err != nil {
		return ReactionOutcome{}, fmt.Errorf("read the contained Items: %w", err)
	}
	slices.SortFunc(all, func(a, b child) int { return strings.Compare(a.id.String(), b.id.String()) })

	moveKey := func(id uuid.UUID) string {
		return fmt.Sprintf("%s:%s:%s:%s", tg.key(), tg.req.SourceID, tg.causation(), id)
	}
	// The children this reaction deals with: those it already recorded or
	// moved (an earlier run that did not finish), and those it would move
	// now. A child in another state is passed over: not this reaction's.
	var todo []uuid.UUID
	for _, c := range all {
		if _, recorded := recs[c.id]; recorded {
			todo = append(todo, c.id)
			continue
		}
		var moved bool
		if err := k.pool.QueryRow(ctx,
			`select exists(select 1 from idempotency where namespace_id = $1 and actor = $2 and key = $3)`,
			tg.ns, tg.principal().Actor, moveKey(c.id)).Scan(&moved); err != nil {
			return ReactionOutcome{}, fmt.Errorf("read the move's key: %w", err)
		}
		if moved {
			todo = append(todo, c.id)
			continue
		}
		ty, err := k.loadType(ctx, k.pool, c.typeID, c.version)
		if err != nil {
			return ReactionOutcome{}, err
		}
		if !lifecycle.ChildMatches(tg.r, ty, c.fields) {
			continue
		}
		froms := tg.r.TargetFrom
		if len(froms) == 0 {
			for _, tr := range ty.Transitions {
				if tr.To == tg.r.To {
					froms = append(froms, tr.From)
				}
			}
		}
		if slices.Contains(froms, c.state) {
			todo = append(todo, c.id)
		}
	}
	switch {
	case len(todo) == 0:
		return k.record(ctx, tg, ReactionSkipped, SkipNoTarget, uuid.Nil)
	case len(todo) > MaxReactionTargets && len(recs) == 0:
		return k.record(ctx, tg, ReactionFailed, CodeTooManyTargets, uuid.Nil)
	}

	mc.froms, mc.consent, mc.consentType, mc.consentID, mc.contained = tg.r.TargetFrom, true, tg.ty.ID, tg.r.ID, true
	for i, id := range todo {
		if _, recorded := recs[id]; recorded {
			continue
		}
		outcome, reason := ReactionApplied, ""
		_, _, err := k.transition(ctx, TransitionRequest{
			IdempotencyKey: moveKey(id), Principal: tg.principal(), ItemID: id, ToState: tg.r.To,
			Attribution: tg.attribution(tg.req.Hop),
		}, &kernelCause{reactionID: tg.r.ID, typeID: tg.ty.ID, typeVersion: tg.ty.Version, move: mc})
		switch {
		case err == nil:
		case errors.Is(err, errNotInFrom):
			// It left the state since it was read: someone else moved it.
			outcome, reason = ReactionSkipped, SkipNotInFrom
		default:
			refused, isRefusal := refusal(err)
			if !isRefusal {
				return ReactionOutcome{}, err // may pass: the caller retries
			}
			outcome, reason = ReactionFailed, refused
		}
		if err := k.writeRecord(ctx, tg, outcome, reason, id, id.String(), i < len(todo)-1); err != nil {
			return ReactionOutcome{}, err
		}
	}
	if recs, err = k.childRecords(ctx, tg); err != nil {
		return ReactionOutcome{}, err
	}
	// The last child's record finishes the reaction. If the children
	// changed between two runs so that no record says so, say it now.
	if !slices.ContainsFunc(slices.Collect(maps.Values(recs)), func(r childRecord) bool { return !r.more }) {
		if _, err := k.record(ctx, tg, ReactionSkipped, SkipNotInFrom, uuid.Nil); err != nil {
			return ReactionOutcome{}, err
		}
	}
	return childrenOutcome(recs), nil
}

// --- a create reaction reads linked Items ---

// errRefused carries a reaction's failure out of a write that must be
// rolled back: the reason to record.
type errRefused struct{ reason string }

func (e *errRefused) Error() string { return "the reaction is refused: " + e.reason }

// linkedFor reads, inside the reaction's write, the linked Items a create
// reaction's fields read: the Item one link away from the source, or from
// the source's container. The source, its container and each linked Item
// are locked FOR SHARE, in the order of their IDs, so none of what was
// read can change before the write commits; if any of them changed
// between the unlocked read and the locks, the transaction starts again.
//
// No container, no link, or a field the linked Item's Type version does
// not let reactions read: errRefused with the reason.
func (k *Service) linkedFor(ctx context.Context, tx pgx.Tx, tg trigger) (lifecycle.LinkedFields, error) {
	var out lifecycle.LinkedFields
	read := func() (container *uuid.UUID, targets map[typedef.LinkedRead]uuid.UUID, err error) {
		if err := tx.QueryRow(ctx, `select container_id from items where id = $1`, tg.req.SourceID).Scan(&container); err != nil {
			return nil, nil, fmt.Errorf("read the source's container: %w", err)
		}
		targets = map[typedef.LinkedRead]uuid.UUID{}
		for _, lr := range tg.r.LinkedReads {
			owner := tg.req.SourceID
			if lr.Container {
				if container == nil {
					return nil, nil, &errRefused{CodeNoContainer}
				}
				owner = *container
			}
			at := typedef.LinkedRead{Container: lr.Container, Link: lr.Link}
			if _, known := targets[at]; known {
				continue
			}
			var target uuid.UUID
			err := tx.QueryRow(ctx, `select target_id from links where source_id = $1 and link_type = $2`, owner, lr.Link).Scan(&target)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, nil, &errRefused{CodeNotLinked}
			}
			if err != nil {
				return nil, nil, fmt.Errorf("read link: %w", err)
			}
			targets[at] = target
		}
		return container, targets, nil
	}
	container, targets, err := read()
	if err != nil {
		return out, err
	}
	ids := []uuid.UUID{tg.req.SourceID}
	if container != nil {
		ids = append(ids, *container)
	}
	ids = append(ids, slices.Collect(maps.Values(targets))...)
	slices.SortFunc(ids, func(a, b uuid.UUID) int { return strings.Compare(a.String(), b.String()) })
	for _, id := range slices.Compact(ids) {
		if _, err := tx.Exec(ctx, `select 1 from items where id = $1 for share`, id); err != nil {
			return out, fmt.Errorf("lock a linked Item: %w", err)
		}
	}
	container2, targets2, err := read()
	if err != nil {
		return out, err
	}
	if (container == nil) != (container2 == nil) || (container != nil && *container != *container2) || !maps.Equal(targets, targets2) {
		return out, errRetry
	}

	out.Own, out.Container = map[string]map[string]any{}, map[string]map[string]any{}
	out.OwnTypes, out.ContainerTypes = map[string]*typedef.Type{}, map[string]*typedef.Type{}
	for _, lr := range tg.r.LinkedReads {
		target := targets[typedef.LinkedRead{Container: lr.Container, Link: lr.Link}]
		var typeID string
		var version int
		var fields map[string]any
		if err := tx.QueryRow(ctx, `select type_id, type_version, fields from items where id = $1`, target).
			Scan(&typeID, &version, &fields); err != nil {
			return out, fmt.Errorf("read the linked Item: %w", err)
		}
		ty, err := k.loadType(ctx, tx, typeID, version)
		if err != nil {
			return out, err
		}
		// Consent, at write time: the linked Item's own Type version.
		if !ty.ReadableByReaction(lr.Field) {
			return out, &errRefused{CodeReadNotAccepted}
		}
		into, tys := out.Own, out.OwnTypes
		if lr.Container {
			into, tys = out.Container, out.ContainerTypes
		}
		if into[lr.Link] == nil {
			into[lr.Link], tys[lr.Link] = map[string]any{}, ty
		}
		into[lr.Link][lr.Field] = fields[lr.Field] // nil when unset: null to the expression
	}
	return out, nil
}

// --- a reaction that sets fields on the container ---

// ReactSet runs a set reaction for one trigger, like ReactMove: it sets the
// reaction's fields on the source's container in one write, under the key
// <reaction>:<source>:<cause>:set, then records the outcome on the source.
//
// The container's Type must accept the reaction for the fields it sets
// (accepts_reactions: sets), which also makes it the writer of a
// kernel-written field. Frozen fields and invariants apply as for any
// write. No container: skipped, no_target.
func (in *Internal) ReactSet(ctx context.Context, req ReactionRunRequest) (ReactionOutcome, error) {
	k := in.k
	tg, err := k.loadTrigger(ctx, req, typedef.ReactionSet)
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
	if tg.container == nil {
		return k.record(ctx, tg, ReactionSkipped, SkipNoTarget, uuid.Nil)
	}
	target := *tg.container

	key := fmt.Sprintf("%s:%s:%s:set", tg.key(), req.SourceID, tg.causation())
	hash, err := requestHash("react_set", req)
	if err != nil {
		return ReactionOutcome{}, err
	}
	attr := tg.attribution(req.Hop)
	_, err = k.write(ctx, tg.principal(), key, hash, func(tx pgx.Tx) (WriteResult, error) {
		// The source is read under a lock too, so what it shows is what
		// it holds: both rows, in the order of their IDs.
		var fields map[string]any
		readSource := func() error {
			var container *uuid.UUID
			err := tx.QueryRow(ctx, `select fields, container_id from items where id = $1 for share`, req.SourceID).Scan(&fields, &container)
			if err == nil && (container == nil || *container != target) {
				return errRetry // moved since the trigger was read: start again
			}
			return err
		}
		var it lockedItem
		lockTarget := func() (err error) { it, err = lockItem(ctx, tx, tg.ns, target); return err }
		steps := []func() error{readSource, lockTarget}
		if strings.Compare(target.String(), req.SourceID.String()) < 0 {
			slices.Reverse(steps)
		}
		for _, step := range steps {
			if err := step(); err != nil {
				return WriteResult{}, err
			}
		}
		ty, err := k.loadType(ctx, tx, it.TypeID, it.TypeVersion)
		if err != nil {
			return WriteResult{}, err
		}
		// Consent, at write time, by the container's own Type version. A
		// contained Item reaches its container through containment.
		ids := slices.Sorted(maps.Keys(tg.r.Set))
		accepted, _ := ty.Consent(tg.ty.ID, tg.r.ID, true)
		for _, id := range ids {
			if !slices.Contains(accepted.Sets, id) {
				return WriteResult{}, rejected(CodeReactionNotAccepted, "%s v%d does not accept %s setting %s",
					ty.ID, ty.Version, tg.key(), id)
			}
		}
		values, err := lifecycle.SetValues(tg.ty, tg.r, ty, lifecycle.Input{Fields: fields, CauseAt: tg.causeAt})
		if err != nil {
			return WriteResult{}, fromLifecycle(err)
		}
		if err := fromLifecycle(lifecycle.CheckFrozen(ty, it.State, ids)); err != nil {
			return WriteResult{}, err
		}
		hold, err := readOpenHold(ctx, tx, target)
		if err != nil {
			return WriteResult{}, err
		}
		if hold != nil {
			if err := lifecycle.UpdateDuringHold(ty, hold.rule(), ids); err != nil {
				return WriteResult{}, heldError(hold, err)
			}
		}
		merged, set, cleared := applyPatch(it.Fields, values)
		if err := checkInvariants(ty, merged); err != nil {
			return WriteResult{}, err
		}
		payload := map[string]any{"field_ids": sortedKeys(set)}
		if len(cleared) > 0 {
			payload["cleared_field_ids"] = sortedKeys(cleared)
		}
		bump, err := decideContent(ty, "update", payload)
		if err != nil {
			return WriteResult{}, err
		}
		itemSeq := it.ItemSeq + 1
		if _, err := tx.Exec(ctx,
			`update items set fields = $2, item_seq = $3, content_seq = content_seq + $4, updated_at = now() where id = $1`,
			target, merged, itemSeq, bump.n); err != nil {
			return WriteResult{}, fmt.Errorf("update item: %w", err)
		}
		return appendEvent(ctx, tx, ty, target, itemSeq, tg.recorded(), attr, bump, payload, set)
	})
	if err != nil {
		if reason, refused := refusal(err); refused {
			return k.record(ctx, tg, ReactionFailed, reason, target)
		}
		return ReactionOutcome{}, err
	}
	return k.record(ctx, tg, ReactionApplied, "", target)
}

// --- an acting timer at an instant ---

// ActingInstant is what the workflow may know of one acting timer at an
// instant: the instant its field holds, if any.
type ActingInstant struct {
	Timer string
	Set   bool
	At    time.Time
}

// ActingInstants reads, for the Item's current state, the instant of each
// acting timer at an instant, in the Type's order, and nothing else: the
// caller names no field. A kernel read for the worker only, like AtInstant;
// it is not subject to visibility. ErrNotFound if the Item does not exist.
func (k *Service) ActingInstants(ctx context.Context, id uuid.UUID) ([]ActingInstant, error) {
	var (
		typeID, state string
		version       int
		fields        map[string]any
	)
	err := k.pool.QueryRow(ctx, `select type_id, type_version, state, fields from items where id = $1`, id).
		Scan(&typeID, &version, &state, &fields)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read item: %w", err)
	}
	ty, err := k.loadType(ctx, k.pool, typeID, version)
	if err != nil {
		return nil, err
	}
	var out []ActingInstant
	for _, a := range lifecycle.ActingAtTimers(ty, state) {
		at, set := typedef.ParseDateTime(fields[a.At])
		out = append(out, ActingInstant{Timer: a.ID, Set: set, At: at})
	}
	return out, nil
}

// ActAtRequest makes an acting timer at an instant act.
type ActAtRequest struct {
	ItemID uuid.UUID
	Timer  string
	// EnteredSeq is the event that entered the state the timer belongs to.
	EnteredSeq int64
	// Instant is the instant the timer was armed for: the value its field
	// held when the workflow read it.
	Instant time.Time
	// Attribution: the entering event's correlation ID, and its hop + 1.
	// The item service sets the cause (the entering event) and the
	// originating timer itself.
	Attribution item.Attribution
}

// ActAtResult is what ActAt did.
type ActAtResult struct {
	WriteResult
	// Applied is false when nothing was written: the timer's `when` did
	// not hold, or it has no `sets`.
	Applied bool
	// RunHook is true when the timer declares a hook and it should now
	// run: its `when` held (whether or not it has sets).
	RunHook bool
}

// errNothingToWrite ends ActAt's transaction without a write and without
// using up its key: the timer's condition did not hold.
var errNothingToWrite = errors.New("itemservice: the acting timer has nothing to write")

// ActAt applies an acting timer at an instant: in one write by
// system:timer, without a transition, it sets the timer's `sets`, each
// evaluated with cause.at = Instant. The key is
// timer:<item>:<timer>:<entered seq>:<instant>.
//
//   - Before the instant, by the write's own time: ErrNotYet with the
//     remainder, nothing written, the key not used up.
//   - The Item left the state, was entered again since, or the field no
//     longer holds Instant: ErrStale, nothing written.
//   - `when` does not hold: nothing written, Applied false.
func (in *Internal) ActAt(ctx context.Context, req ActAtRequest) (ActAtResult, error) {
	k := in.k
	invalid := func(format string, args ...any) (ActAtResult, error) {
		return ActAtResult{}, fmt.Errorf("%w: acting timer: "+format, append([]any{ErrInvalid}, args...)...)
	}
	if req.Timer == "" || req.EnteredSeq < 1 || req.Instant.IsZero() {
		return invalid("a timer, the entering event and the instant are required")
	}
	if req.Attribution.Hop < 1 {
		return invalid("a kernel write is at least one hop from its cause")
	}
	if req.Attribution.Hop > MaxHop {
		return ActAtResult{}, rejected(CodeHopLimit, "hop limit: hop %d is deeper than %d", req.Attribution.Hop, MaxHop)
	}
	var ns, typeID string
	var version int
	err := k.pool.QueryRow(ctx, `select namespace_id, type_id, type_version from items where id = $1`, req.ItemID).
		Scan(&ns, &typeID, &version)
	if errors.Is(err, pgx.ErrNoRows) {
		return ActAtResult{}, ErrNotFound
	}
	if err != nil {
		return ActAtResult{}, fmt.Errorf("read item: %w", err)
	}
	ty, err := k.loadType(ctx, k.pool, typeID, version)
	if err != nil {
		return ActAtResult{}, err
	}
	// The Type must declare the timer, in some state; which state the Item
	// is in now is judged under the lock.
	declared := slices.ContainsFunc(ty.States, func(s typedef.State) bool {
		return slices.ContainsFunc(s.Acting, func(a typedef.ActingTimer) bool { return a.ID == req.Timer && a.At != "" })
	})
	if !declared {
		return invalid("%s v%d declares no acting timer at an instant named %q", typeID, version, req.Timer)
	}

	p := item.Principal{Actor: timerActor, Namespace: ns}
	stamp, _ := typedef.FormatDateTime(req.Instant)
	key := fmt.Sprintf("timer:%s:%s:%d:%s", req.ItemID, req.Timer, req.EnteredSeq, stamp)
	hash, err := requestHash("act_at", req)
	if err != nil {
		return ActAtResult{}, err
	}
	attr := req.Attribution
	attr.OriginatingHook = fmt.Sprintf("timer:%s@v%d:%s", typeID, version, req.Timer)
	attr.CausationID = fmt.Sprintf("%s:%d", req.ItemID, req.EnteredSeq)

	var hook string
	res, err := k.write(ctx, p, key, hash, func(tx pgx.Tx) (WriteResult, error) {
		it, err := lockItem(ctx, tx, ns, req.ItemID)
		if err != nil {
			return WriteResult{}, err
		}
		var timer *typedef.ActingTimer
		for _, a := range lifecycle.ActingAtTimers(ty, it.State) {
			if a.ID == req.Timer {
				timer = &a
			}
		}
		if timer == nil {
			return WriteResult{}, fmt.Errorf("%w: the Item is in %q, where timer %s is not armed", ErrStale, it.State, req.Timer)
		}
		hook = timer.Hook
		// The entry the timer was armed for is still the current one.
		var entered int64
		if err := tx.QueryRow(ctx,
			`select item_seq from item_events where item_id = $1 and op in ('create', 'transition')
			 order by item_seq desc limit 1`, req.ItemID).Scan(&entered); err != nil {
			return WriteResult{}, fmt.Errorf("read the entering event: %w", err)
		}
		if entered != req.EnteredSeq {
			return WriteResult{}, fmt.Errorf("%w: the Item entered %q again since timer %s was armed", ErrStale, it.State, req.Timer)
		}
		instant, set := typedef.ParseDateTime(it.Fields[timer.At])
		if !set || !instant.Equal(req.Instant) {
			return WriteResult{}, fmt.Errorf("%w: %s no longer holds the instant timer %s was armed for", ErrStale, timer.At, req.Timer)
		}
		// Never before the instant, by the time this write would carry.
		at, err := stampWrite(ctx, tx)
		if err != nil {
			return WriteResult{}, err
		}
		if remaining := instant.Sub(at); remaining > 0 {
			return WriteResult{}, &NotYetError{Remaining: remaining}
		}
		values, applies, err := lifecycle.ActingEffect(ty, *timer, it.Fields, instant)
		if err != nil {
			return WriteResult{}, fromLifecycle(err)
		}
		if !applies {
			hook = ""
			return WriteResult{}, errNothingToWrite
		}
		if len(values) == 0 {
			return WriteResult{}, errNothingToWrite // a hook alone: nothing to write, the hook runs
		}
		ids := slices.Sorted(maps.Keys(values))
		if err := fromLifecycle(lifecycle.CheckFrozen(ty, it.State, ids)); err != nil {
			return WriteResult{}, err
		}
		// Like every acting timer it does not wait for a hold; but the
		// fields an open check reads stay frozen for it too.
		hold, err := readOpenHold(ctx, tx, req.ItemID)
		if err != nil {
			return WriteResult{}, err
		}
		if hold != nil {
			if err := lifecycle.UpdateDuringHold(ty, hold.rule(), ids); err != nil {
				return WriteResult{}, heldError(hold, err)
			}
		}
		merged, set2, cleared := applyPatch(it.Fields, values)
		if err := checkInvariants(ty, merged); err != nil {
			return WriteResult{}, err
		}
		// Attribution only: the human at the root of the entering event.
		recorded := p
		var actor, onBehalfOf string
		if err := tx.QueryRow(ctx,
			`select actor, coalesce(on_behalf_of, '') from item_events where item_id = $1 and item_seq = $2`,
			req.ItemID, req.EnteredSeq).Scan(&actor, &onBehalfOf); err != nil {
			return WriteResult{}, fmt.Errorf("read cause: %w", err)
		}
		if recorded.OnBehalfOf = onBehalfOf; onBehalfOf == "" && item.IsKind(actor, item.KindUser, item.KindCustomer) {
			recorded.OnBehalfOf = actor
		}
		payload := map[string]any{"field_ids": sortedKeys(set2), "timer": timer.ID}
		if len(cleared) > 0 {
			payload["cleared_field_ids"] = sortedKeys(cleared)
		}
		bump, err := decideContent(ty, "update", payload)
		if err != nil {
			return WriteResult{}, err
		}
		itemSeq := it.ItemSeq + 1
		if _, err := tx.Exec(ctx,
			`update items set fields = $2, item_seq = $3, content_seq = content_seq + $4, updated_at = now() where id = $1`,
			req.ItemID, merged, itemSeq, bump.n); err != nil {
			return WriteResult{}, fmt.Errorf("update item: %w", err)
		}
		return appendEvent(ctx, tx, ty, req.ItemID, itemSeq, recorded, attr, bump, payload, set2)
	})
	switch {
	case errors.Is(err, errNothingToWrite):
		return ActAtResult{RunHook: hook != ""}, nil
	case err != nil:
		return ActAtResult{}, err
	}
	if hook == "" {
		// The key answered instead of the write: the hook is the one of
		// the timer as the Type declares it.
		for _, s := range ty.States {
			for _, a := range s.Acting {
				if a.ID == req.Timer && a.At != "" {
					hook = a.Hook
				}
			}
		}
	}
	return ActAtResult{WriteResult: res, Applied: res.ItemSeq != 0, RunHook: hook != ""}, nil
}
