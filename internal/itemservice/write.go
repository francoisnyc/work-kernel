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
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// errRetry is returned from inside a write when it lost a race it cannot
// finish without risking a deadlock; write runs the whole transaction again.
var errRetry = errors.New("itemservice: retry the transaction")

// maxRetries bounds how often write reruns a transaction that hit errRetry.
const maxRetries = 3

// write runs one write operation in one transaction, wrapped in the
// idempotency protocol shared by every operation:
//
//  1. claim the key; if it was already used, return the stored result;
//  2. run apply, which changes the Item and appends its event;
//  3. store the result under the key.
//
// If apply returns an error the transaction rolls back, which also releases
// the key, so a rejected request can be retried with the same key.
//
// apply is a function value, like passing a lambda to a template method.
func (k *Service) write(ctx context.Context, p item.Principal, key, hash string,
	apply func(tx pgx.Tx) (WriteResult, error)) (WriteResult, error) {

	var res WriteResult
	// BeginFunc commits if the function returns nil and rolls back otherwise.
	run := func(tx pgx.Tx) error {
		// set_config(..., true) is SET LOCAL: the timeout ends with this
		// transaction, so the pooled connection goes back unchanged.
		_, err := tx.Exec(ctx, `select set_config('statement_timeout', $1, true)`,
			fmt.Sprintf("%dms", k.writeTimeout.Milliseconds()))
		if err != nil {
			return fmt.Errorf("set statement timeout: %w", err)
		}
		// An agent's session must be one minted for this agent and user.
		if err := checkSession(ctx, tx, p); err != nil {
			return err
		}
		// Claim the key first. If another transaction holds the same key, this
		// statement waits for it to finish, then sees its row and inserts nothing.
		// Keys are scoped by namespace and actor, so two clients never collide.
		tag, err := tx.Exec(ctx,
			`insert into idempotency (namespace_id, actor, key, request_hash, result)
			 values ($1, $2, $3, $4, '{}')
			 on conflict (namespace_id, actor, key) do nothing`,
			defaultNamespace, p.Actor, key, hash)
		if err != nil {
			return fmt.Errorf("claim idempotency key: %w", err)
		}
		if tag.RowsAffected() == 0 {
			res, err = storedResult(ctx, tx, p.Actor, key, hash)
			return err
		}

		res, err = apply(tx)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx,
			`update idempotency set result = $4 where namespace_id = $1 and actor = $2 and key = $3`,
			defaultNamespace, p.Actor, key, res)
		if err != nil {
			return fmt.Errorf("store idempotency result: %w", err)
		}
		return nil
	}
	// A transaction that lost a race it cannot finish safely (errRetry)
	// is rolled back and run again from the start: everything it read is
	// read afresh, and its idempotency key was released with the rollback.
	var err error
	for attempt := 0; ; attempt++ {
		err = pgx.BeginFunc(ctx, k.pool, run)
		if !errors.Is(err, errRetry) || attempt == maxRetries {
			break
		}
	}
	if err != nil {
		return WriteResult{}, err
	}
	return res, nil
}

// storedResult returns the result recorded for an actor's idempotency key, or
// ErrIdempotencyKeyReuse if the key was used with a different request.
func storedResult(ctx context.Context, tx pgx.Tx, actor, key, hash string) (WriteResult, error) {
	var (
		storedHash string
		res        WriteResult
	)
	err := tx.QueryRow(ctx,
		`select request_hash, result from idempotency
		 where namespace_id = $1 and actor = $2 and key = $3`,
		defaultNamespace, actor, key,
	).Scan(&storedHash, &res)
	if err != nil {
		return WriteResult{}, fmt.Errorf("read idempotency record: %w", err)
	}
	if storedHash != hash {
		return WriteResult{}, ErrIdempotencyKeyReuse
	}
	return res, nil
}

// lockedItem is the part of an Item row a write needs.
type lockedItem struct {
	TypeID      string
	TypeVersion int
	ContainerID *uuid.UUID
	ScopeID     uuid.UUID
	State       string
	Fields      map[string]any
	ItemSeq     int64
}

// lockItem reads the Item row and locks it until the transaction ends. Two
// writes to one Item therefore run one after the other, which is what keeps
// item_seq free of gaps.
func lockItem(ctx context.Context, tx pgx.Tx, id uuid.UUID) (lockedItem, error) {
	var it lockedItem
	err := tx.QueryRow(ctx,
		`select type_id, type_version, container_id, scope_id, state, fields, item_seq
		 from items where id = $1 and namespace_id = $2 for update`,
		id, defaultNamespace,
	).Scan(&it.TypeID, &it.TypeVersion, &it.ContainerID, &it.ScopeID, &it.State, &it.Fields, &it.ItemSeq)
	if errors.Is(err, pgx.ErrNoRows) {
		return lockedItem{}, ErrNotFound
	}
	if err != nil {
		return lockedItem{}, fmt.Errorf("lock item: %w", err)
	}
	return it, nil
}

// checkPrincipal refuses a principal that is not typed: the actor must be
// user:, agent: or system:, and on_behalf_of, when set, a user:.
func checkPrincipal(p item.Principal) error {
	if !p.Valid() {
		return fmt.Errorf("%w: principals are typed IDs (user:<id>, agent:<id>, system:<id>), "+
			"and on_behalf_of must be a user", ErrInvalid)
	}
	return nil
}

// checkAttribution refuses malformed attribution before any work is done.
func checkAttribution(a item.Attribution) error {
	if a.Hop < 0 {
		return fmt.Errorf("%w: hop cannot be negative", ErrInvalid)
	}
	return nil
}

// checkOptions validates the optional parts shared by Update and Transition.
func checkOptions(expectedItemSeq int64, a item.Attribution) error {
	if expectedItemSeq < 0 {
		return fmt.Errorf("%w: expected item_seq cannot be negative", ErrInvalid)
	}
	return checkAttribution(a)
}

// checkExpectedSeq is the if-match. It runs under the row lock, so the
// item_seq it compares cannot change before the write. Zero means "no check".
func checkExpectedSeq(it lockedItem, expected int64) error {
	if expected != 0 && expected != it.ItemSeq {
		return fmt.Errorf("%w: expected item_seq %d, but the Item is at %d", ErrStale, expected, it.ItemSeq)
	}
	return nil
}

// appendEvent records one accepted operation: it inserts the event (which
// takes its commit_seq), the values the event set, and, when ty has
// listeners, the outbox row. The caller has already written the Item row at
// itemSeq.
//
// payload holds the operation's shape (states, field IDs) and never a field
// value; values go to item_event_values so they can be erased later.
func appendEvent(ctx context.Context, tx pgx.Tx, ty *typedef.Type, id uuid.UUID, itemSeq int64, p item.Principal,
	a item.Attribution, op string, payload map[string]any, values map[string]any) (WriteResult, error) {

	// nullif stores an empty Go string as null: "not set".
	// commit_seq comes from the commit_seq sequence and xid from
	// pg_current_xact_id(), both column defaults. Taking a sequence number
	// locks nothing, so writes to different Items no longer queue behind each
	// other. The numbers are not in commit order; min_seq waits on xid instead
	// (see Search).
	var commitSeq int64
	err := tx.QueryRow(ctx,
		`insert into item_events (item_id, item_seq, actor, on_behalf_of,
		                          agent_session, originating_hook, causation_id, correlation_id, hop,
		                          op, payload)
		 values ($1, $2, $3, nullif($4, ''),
		         nullif($5, ''), nullif($6, ''), nullif($7, ''), nullif($8, ''), $9,
		         $10, $11)
		 returning commit_seq`,
		id, itemSeq, p.Actor, p.OnBehalfOf,
		a.AgentSession, a.OriginatingHook, a.CausationID, a.CorrelationID, a.Hop,
		op, payload).Scan(&commitSeq)
	if err != nil {
		return WriteResult{}, fmt.Errorf("insert event: %w", err)
	}

	// An event that sets no field gets no values row.
	if len(values) > 0 {
		_, err = tx.Exec(ctx,
			`insert into item_event_values (item_id, item_seq, field_values) values ($1, $2, $3)`,
			id, itemSeq, values)
		if err != nil {
			return WriteResult{}, fmt.Errorf("insert event values: %w", err)
		}
	}

	// Pay for what you use: with nothing listening, there is nothing to send.
	if lifecycle.HasListeners(ty) {
		_, err = tx.Exec(ctx, `insert into outbox (item_id, item_seq) values ($1, $2)`, id, itemSeq)
		if err != nil {
			return WriteResult{}, fmt.Errorf("insert outbox: %w", err)
		}
	}
	return WriteResult{ItemID: id, CommitSeq: commitSeq, ItemSeq: itemSeq}, nil
}

// applyPatch applies a field patch to an Item's current fields without
// modifying either map. It returns the fields after the patch, the values the
// patch set, and the fields it cleared (a nil value in the patch clears).
// Go functions can return several values; there is no need for a result class.
func applyPatch(current, patch map[string]any) (merged, set, cleared map[string]any) {
	merged = maps.Clone(current)
	if merged == nil {
		merged = map[string]any{}
	}
	set = map[string]any{}
	cleared = map[string]any{}
	for id, v := range patch {
		if v == nil {
			delete(merged, id)
			cleared[id] = nil
		} else {
			merged[id] = v
			set[id] = v
		}
	}
	return merged, set, cleared
}

// sortedKeys returns the map's keys in order. Go maps iterate in random
// order, so anything stored or compared must be sorted first. The result is
// never nil, so it encodes as [] rather than null.
func sortedKeys(m map[string]any) []string {
	keys := slices.Sorted(maps.Keys(m))
	if keys == nil {
		keys = []string{}
	}
	return keys
}
