package kernel

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/francoisnyc/work-kernel/internal/item"
)

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
func (k *Kernel) write(ctx context.Context, p item.Principal, key, hash string,
	apply func(tx pgx.Tx) (WriteResult, error)) (WriteResult, error) {

	var res WriteResult
	// BeginFunc commits if the function returns nil and rolls back otherwise.
	err := pgx.BeginFunc(ctx, k.pool, func(tx pgx.Tx) error {
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
	})
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
		`select type_id, type_version, state, fields, item_seq
		 from items where id = $1 and namespace_id = $2 for update`,
		id, defaultNamespace,
	).Scan(&it.TypeID, &it.TypeVersion, &it.State, &it.Fields, &it.ItemSeq)
	if errors.Is(err, pgx.ErrNoRows) {
		return lockedItem{}, ErrNotFound
	}
	if err != nil {
		return lockedItem{}, fmt.Errorf("lock item: %w", err)
	}
	return it, nil
}

// appendEvent records one accepted operation: it takes the next commit_seq,
// then inserts the event, the values the event set, and the outbox row. The
// caller has already written the Item row at itemSeq.
//
// payload holds the operation's shape (states, field IDs) and never a field
// value; values go to item_event_values so they can be erased later.
func appendEvent(ctx context.Context, tx pgx.Tx, id uuid.UUID, itemSeq int64, p item.Principal,
	op string, payload map[string]any, values map[string]any) (WriteResult, error) {

	// The update locks the single counter row until the transaction ends, so
	// writes commit one at a time and commit_seq values become visible in order.
	var commitSeq int64
	err := tx.QueryRow(ctx,
		`update commit_counter set last_seq = last_seq + 1 returning last_seq`).Scan(&commitSeq)
	if err != nil {
		return WriteResult{}, fmt.Errorf("take commit_seq: %w", err)
	}

	// The attribution columns (agent_session, causation_id, ...) keep their
	// defaults until agents and hooks exist to fill them.
	_, err = tx.Exec(ctx,
		`insert into item_events (item_id, item_seq, commit_seq, actor, on_behalf_of, op, payload)
		 values ($1, $2, $3, $4, nullif($5, ''), $6, $7)`,
		id, itemSeq, commitSeq, p.Actor, p.OnBehalfOf, op, payload)
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

	_, err = tx.Exec(ctx, `insert into outbox (item_id, item_seq) values ($1, $2)`, id, itemSeq)
	if err != nil {
		return WriteResult{}, fmt.Errorf("insert outbox: %w", err)
	}
	return WriteResult{ItemID: id, CommitSeq: commitSeq, ItemSeq: itemSeq}, nil
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
