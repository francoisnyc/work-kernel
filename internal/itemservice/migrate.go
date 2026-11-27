package itemservice

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/lifecycle"
	"github.com/francoisnyc/work-kernel/internal/policy"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// Migrate (track 1, item 6, step 2) moves an open, quiet Item from the
// Type version it is on to the latest one, once it is checked against it.

// MigrateRequest names the Item and the version the caller expects it to
// land on, which must be the latest registered version of its Type when
// the write commits. With ValidateOnly nothing is written and no
// idempotency key is used.
type MigrateRequest struct {
	IdempotencyKey string
	Principal      item.Principal
	ItemID         uuid.UUID
	TypeID         string
	TypeVersion    int
	ValidateOnly   bool
}

// MigrateResult is the write's result; for ValidateOnly, Problems lists
// everything that keeps the Item from being migrated (none: it fits) and
// the WriteResult names the Item only.
type MigrateResult struct {
	WriteResult
	Problems []lifecycle.Misfit
}

// NotMigratableError is an ErrRejected: the Item cannot be migrated, for
// every reason in Problems. Over the API, FAILED_PRECONDITION /
// NOT_MIGRATABLE with the checks in metadata.
type NotMigratableError struct {
	Problems []lifecycle.Misfit
}

func (e *NotMigratableError) Error() string {
	return fmt.Sprintf("%v: the Item cannot be migrated: %d problems", ErrRejected, len(e.Problems))
}
func (e *NotMigratableError) Unwrap() error { return ErrRejected }

// Migrate moves the Item to the latest registered version of its Type.
//
// Who may is the latest version's to say (migrate.allowed; policy.MayMigrate):
// an Item the caller cannot see is ErrNotFound, and someone who may not is
// ErrForbidden, before any check runs. The version the request names must
// be that latest version, read in this transaction, and newer than the
// Item's. Then, under the Item's lock, everything that keeps it from being
// migrated is collected: what lifecycle.Fits finds from the Item's facts,
// a sibling holding the same value of a field the new version declares
// unique, and whatever makes the Item not quiet under either version.
//
// With problems, a real call is a *NotMigratableError and writes nothing;
// a ValidateOnly call returns them. Without, the write is one transaction
// through the write path: the Item row (type_version, item_seq), the event
// `migrated` with {from_version, to_version}, an outbox row if either
// version has listeners, the idempotency record. No field, state, reader
// or container changes, and content_seq (the etag) stays. No enter
// reaction, hook or timer of the new version runs.
func (k *Service) Migrate(ctx context.Context, req MigrateRequest) (MigrateResult, error) {
	if err := checkPublicPrincipal(req.Principal); err != nil {
		return MigrateResult{}, err
	}
	if req.TypeID == "" || req.TypeVersion < 1 {
		return MigrateResult{}, &InvalidError{Path: "type", Reason: "name the Type version to migrate to"}
	}
	if req.ValidateOnly {
		// Nothing is written: a transaction of its own, rolled back, and
		// no idempotency key. errDryRun carries the answer out of
		// BeginFunc, which commits unless its function fails.
		var out MigrateResult
		err := pgx.BeginFunc(ctx, k.pool, func(tx pgx.Tx) error {
			problems, _, _, err := k.judgeMigrate(ctx, tx, req)
			if err != nil {
				return err
			}
			out = MigrateResult{WriteResult: WriteResult{ItemID: req.ItemID}, Problems: problems}
			return errDryRun
		})
		if err != nil && !errors.Is(err, errDryRun) {
			return MigrateResult{}, err
		}
		return out, nil
	}
	if req.IdempotencyKey == "" {
		return MigrateResult{}, fmt.Errorf("%w: a migrate takes an idempotency key", ErrInvalid)
	}
	hash, err := requestHash("migrate", struct {
		ItemID      uuid.UUID
		TypeID      string
		TypeVersion int
	}{req.ItemID, req.TypeID, req.TypeVersion})
	if err != nil {
		return MigrateResult{}, err
	}
	res, err := k.write(ctx, req.Principal, req.IdempotencyKey, hash, func(tx pgx.Tx) (WriteResult, error) {
		problems, it, latest, err := k.judgeMigrate(ctx, tx, req)
		if err != nil {
			return WriteResult{}, err
		}
		if len(problems) > 0 {
			return WriteResult{}, &NotMigratableError{Problems: problems}
		}
		old, err := k.loadType(ctx, tx, it.TypeID, it.TypeVersion)
		if err != nil {
			return WriteResult{}, err
		}
		// Opaque numbers only: the two versions.
		payload := map[string]any{"from_version": it.TypeVersion, "to_version": latest.Version}
		bump, err := decideContent(latest, lifecycle.OpMigrated, payload)
		if err != nil {
			return WriteResult{}, err
		}
		if _, err := stampWrite(ctx, tx); err != nil {
			return WriteResult{}, err
		}
		seq := it.ItemSeq + 1
		if _, err := tx.Exec(ctx,
			`update items set type_version = $2, item_seq = $3, content_seq = content_seq + $4, updated_at = now() where id = $1`,
			req.ItemID, latest.Version, seq, bump.n); err != nil {
			return WriteResult{}, fmt.Errorf("update item: %w", err)
		}
		// The outbox row goes out if either version has anything listening.
		listening := latest
		if !lifecycle.HasListeners(latest) {
			listening = old
		}
		attr := item.Attribution{AgentSession: req.Principal.AgentSession}
		return appendEvent(ctx, tx, listening, req.ItemID, seq, req.Principal, attr, bump, payload, nil)
	})
	if err != nil {
		return MigrateResult{}, err
	}
	return MigrateResult{WriteResult: res}, nil
}

// errDryRun ends a ValidateOnly transaction without committing it.
var errDryRun = errors.New("itemservice: dry run")

// judgeMigrate locks the Item and answers, in this order: may the caller
// see it (ErrNotFound), may they migrate it (ErrForbidden), and what keeps
// it from being migrated (the problems, sorted). It also returns the locked
// Item and the latest version of its Type.
func (k *Service) judgeMigrate(ctx context.Context, tx pgx.Tx, req MigrateRequest) ([]lifecycle.Misfit, lockedItem, *typedef.Type, error) {
	fail := func(err error) ([]lifecycle.Misfit, lockedItem, *typedef.Type, error) {
		return nil, lockedItem{}, nil, err
	}
	p, ns := req.Principal, nsOf(req.Principal)
	it, err := lockItem(ctx, tx, ns, req.ItemID)
	if err != nil {
		return fail(err)
	}
	if err := k.checkSeesInTx(ctx, tx, p, req.ItemID); err != nil {
		return fail(err)
	}
	if err := checkInSession(ctx, tx, p, &req.ItemID); err != nil {
		return fail(err)
	}
	if req.TypeID != it.TypeID {
		return fail(&InvalidError{Path: "type", Reason: "an Item is migrated to a version of its own Type"})
	}
	latest, err := k.latestType(ctx, tx, it.TypeID)
	if err != nil {
		return fail(err)
	}
	who := policy.Who{Actor: p.Actor, OnBehalfOf: p.OnBehalfOf}
	if who.ActorRoles, err = policy.Roles(ctx, tx, ns, p.Actor); err != nil {
		return fail(err)
	}
	if !policy.MayMigrate(latest.Migrate, who) {
		return fail(fmt.Errorf("%w: the caller may not migrate Items to %s v%d", ErrForbidden, latest.ID, latest.Version))
	}
	// The target: the latest version, and one the Item is not on yet.
	// Nothing else can be judged against a version that is not the target.
	if req.TypeVersion != latest.Version || latest.Version <= it.TypeVersion {
		return []lifecycle.Misfit{{Check: lifecycle.TargetNotLatest}}, it, latest, nil
	}
	old, err := k.loadType(ctx, tx, it.TypeID, it.TypeVersion)
	if err != nil {
		return fail(err)
	}
	facts, err := migrationFacts(ctx, tx, req.ItemID, it)
	if err != nil {
		return fail(err)
	}
	problems := lifecycle.Fits(old, latest, facts)
	if f, clash, err := uniqueClash(ctx, tx, latest, req.ItemID, it.ContainerID, it.State, it.Fields); err != nil {
		return fail(err)
	} else if clash {
		problems = append(problems, lifecycle.Misfit{Check: lifecycle.MigrateNotUnique, Fields: []string{f}})
	}
	busy, err := k.notQuiet(ctx, tx, req.ItemID, it, old, latest)
	if err != nil {
		return fail(err)
	}
	problems = append(problems, busy...)
	lifecycle.SortMisfits(problems)
	return problems, it, latest, nil
}

// migrationFacts reads what lifecycle.Fits needs of the locked Item: the
// links it has as source with their targets' Types, its container's Type,
// and every state it entered, from its own events.
func migrationFacts(ctx context.Context, tx pgx.Tx, id uuid.UUID, it lockedItem) (lifecycle.MigrationFacts, error) {
	f := lifecycle.MigrationFacts{State: it.State, Fields: it.Fields, Top: it.ContainerID == nil}
	if it.ContainerID != nil {
		if err := tx.QueryRow(ctx, `select type_id from items where id = $1`, *it.ContainerID).Scan(&f.ContainerType); err != nil {
			return f, fmt.Errorf("read the container's Type: %w", err)
		}
	}
	rows, err := tx.Query(ctx,
		`select l.link_type, t.type_id from links l join items t on t.id = l.target_id
		 where l.source_id = $1 order by l.link_type, t.type_id`, id)
	if err != nil {
		return f, fmt.Errorf("read links: %w", err)
	}
	// RowToStructByPos fills the struct's fields in order, like a record.
	if f.Links, err = pgx.CollectRows(rows, pgx.RowToStructByPos[lifecycle.HeldLink]); err != nil {
		return f, fmt.Errorf("read links: %w", err)
	}
	rows, err = tx.Query(ctx,
		`select coalesce(case op when 'create' then payload->>'state' else payload->>'to' end, '')
		 from item_events where item_id = $1 and op in ('create', 'transition') order by item_seq`, id)
	if err != nil {
		return f, fmt.Errorf("read the path: %w", err)
	}
	if f.Path, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
		return f, fmt.Errorf("read the path: %w", err)
	}
	return f, nil
}

// notQuiet reports what makes the Item not quiet: an open hold; an action
// or enrich hook, a reaction or an undo not finished; an acting timer on
// its state under the version it is on (old) or the one it would move to
// (latest); an event still in the outbox.
//
// Pending work is read as the workflow's loader reads it: each entry under
// the version it was made under (step 3a). What the latest version adds
// on a state the Item already entered is not owed for that entry, so it
// holds nothing back.
//
// The Item's row is locked, and every event of an Item is written under
// that lock, so what is read here cannot change before this transaction
// ends.
func (k *Service) notQuiet(ctx context.Context, tx pgx.Tx, id uuid.UUID, it lockedItem, old, latest *typedef.Type) ([]lifecycle.Misfit, error) {
	var out []lifecycle.Misfit
	add := func(c lifecycle.MigrationCheck) {
		if !slices.ContainsFunc(out, func(m lifecycle.Misfit) bool { return m.Check == c }) {
			out = append(out, lifecycle.Misfit{Check: c, State: it.State})
		}
	}
	var n int
	if err := tx.QueryRow(ctx, `select count(*) from item_holds where item_id = $1 and status = 'open'`, id).Scan(&n); err != nil {
		return nil, fmt.Errorf("read open holds: %w", err)
	}
	if n > 0 {
		add(lifecycle.MigrateHeld)
	}
	// The event that entered the Item's state, and the hooks that wrote
	// for it: an enrich hook owed for that entry that has not is
	// unfinished.
	var entered int64
	if err := tx.QueryRow(ctx,
		`select item_seq from item_events where item_id = $1 and op in ('create', 'transition') order by item_seq desc limit 1`,
		id).Scan(&entered); err != nil {
		return nil, fmt.Errorf("read the entering event: %w", err)
	}
	rows, err := tx.Query(ctx,
		`select distinct split_part(actor, ':', 3) from item_events
		 where item_id = $1 and causation_id = $2 and actor like 'hook:%'`, id, fmt.Sprintf("%s:%d", id, entered))
	if err != nil {
		return nil, fmt.Errorf("read hooks run: %w", err)
	}
	ran, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("read hooks run: %w", err)
	}
	// Pending work, each entry judged by the version it was made under
	// (step 3a): the same readers the workflow's loader uses, so a migrate
	// that goes through leaves the loader nothing owed for an older entry.
	// What the latest version declares is owed only from the next entry.
	enrich, err := k.owedEnrich(ctx, tx, id, old, it.State, entered, ran)
	if err != nil {
		return nil, err
	}
	actions, err := k.pendingActions(ctx, tx, id, old)
	if err != nil {
		return nil, err
	}
	if len(actions) > 0 || len(enrich) > 0 {
		add(lifecycle.ActionUnfinished)
	}
	undos, err := k.pendingUndos(ctx, tx, id, old)
	if err != nil {
		return nil, err
	}
	reactions, err := k.pendingReactions(ctx, tx, id, old)
	if err != nil {
		return nil, err
	}
	if len(undos) > 0 || len(reactions) > 0 {
		add(lifecycle.ReactionUnfinished)
	}
	// An acting timer under either version (step 3b): not migrated. A
	// plain timer or a timer at an instant is reloaded by the workflow.
	for _, ty := range []*typedef.Type{old, latest} {
		if lifecycle.HasActingTimer(ty, it.State) {
			add(lifecycle.HasTimer)
		}
	}
	if err := tx.QueryRow(ctx, `select count(*) from outbox where item_id = $1 and delivered_at is null`, id).Scan(&n); err != nil {
		return nil, fmt.Errorf("read the outbox: %w", err)
	}
	if n > 0 {
		add(lifecycle.EventsUndelivered)
	}
	return out, nil
}
