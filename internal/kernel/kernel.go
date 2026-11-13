// Package kernel holds the write operations (create, update, transition, link,
// archive) and reads. Every change to an Item goes through this package.
package kernel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/policy"
)

// Go reports failures as returned values, not exceptions. These "sentinel"
// errors are compared with errors.Is, much like catching a specific
// exception class in Java.
var (
	// ErrInvalid means the request is malformed (for example, no idempotency key).
	ErrInvalid = errors.New("kernel: invalid request")
	// ErrNotFound means no Item has the given ID.
	ErrNotFound = errors.New("kernel: item not found")
	// ErrIdempotencyKeyReuse means the key was already used with a different request.
	ErrIdempotencyKeyReuse = errors.New("kernel: idempotency key reused with a different request")
)

// defaultNamespace is the only namespace in the spike. The column exists so
// multi-tenancy can arrive later without a schema change.
const defaultNamespace = "default"

// CreateRequest asks for a new Item.
type CreateRequest struct {
	IdempotencyKey string
	Principal      item.Principal
	TypeID         string
	TypeVersion    int
	State          string         // initial state; not validated until Step 2
	Fields         map[string]any // keyed by stable field ID
	ContainerID    *uuid.UUID
}

// WriteResult is returned by every write.
type WriteResult struct {
	ItemID    uuid.UUID `json:"item_id"`
	CommitSeq int64     `json:"commit_seq"`
	ItemSeq   int64     `json:"item_seq"`
}

// Kernel is stateless; all state lives in Postgres.
type Kernel struct {
	pool *pgxpool.Pool
}

// New returns a Kernel that uses the given connection pool.
func New(pool *pgxpool.Pool) *Kernel {
	return &Kernel{pool: pool}
}

// Create writes a new Item, its first event and an outbox row in one
// transaction. The same idempotency key returns the stored result.
func (k *Kernel) Create(ctx context.Context, req CreateRequest) (WriteResult, error) {
	if req.IdempotencyKey == "" || req.Principal.Actor == "" || req.TypeID == "" || req.State == "" {
		// %w wraps ErrInvalid, so callers can still match it with errors.Is,
		// like an exception with a cause.
		return WriteResult{}, fmt.Errorf("%w: idempotency key, actor, type and state are required", ErrInvalid)
	}
	fields := req.Fields
	if fields == nil {
		fields = map[string]any{}
	}
	hash, err := requestHash("create", req)
	if err != nil {
		return WriteResult{}, err
	}

	var res WriteResult
	// BeginFunc commits if the function returns nil and rolls back otherwise.
	err = pgx.BeginFunc(ctx, k.pool, func(tx pgx.Tx) error {
		// Claim the key first. If another transaction holds the same key, this
		// statement waits for it to finish, then sees its row and inserts nothing.
		// Keys are scoped by namespace and actor, so two clients never collide.
		tag, err := tx.Exec(ctx,
			`insert into idempotency (namespace_id, actor, key, request_hash, result)
			 values ($1, $2, $3, $4, '{}')
			 on conflict (namespace_id, actor, key) do nothing`,
			defaultNamespace, req.Principal.Actor, req.IdempotencyKey, hash)
		if err != nil {
			return fmt.Errorf("claim idempotency key: %w", err)
		}
		if tag.RowsAffected() == 0 {
			res, err = storedResult(ctx, tx, req.Principal.Actor, req.IdempotencyKey, hash)
			return err
		}

		id, err := uuid.NewV7()
		if err != nil {
			return fmt.Errorf("new item id: %w", err)
		}
		// A row inserted in this transaction is locked until commit, so no
		// separate SELECT ... FOR UPDATE is needed for create.
		const itemSeq = 1
		_, err = tx.Exec(ctx,
			`insert into items (id, namespace_id, type_id, type_version, container_id, state, fields, item_seq)
			 values ($1, $2, $3, $4, $5, $6, $7, $8)`,
			id, defaultNamespace, req.TypeID, req.TypeVersion, req.ContainerID, req.State, fields, itemSeq)
		if err != nil {
			return fmt.Errorf("insert item: %w", err)
		}

		commitSeq, err := nextCommitSeq(ctx, tx)
		if err != nil {
			return err
		}
		// The attribution columns (agent_session, causation_id, ...) keep their
		// defaults until agents and hooks exist to fill them.
		_, err = tx.Exec(ctx,
			`insert into item_events (item_id, item_seq, commit_seq, actor, on_behalf_of, op, payload)
			 values ($1, $2, $3, $4, nullif($5, ''), 'create', $6)`,
			id, itemSeq, commitSeq, req.Principal.Actor, req.Principal.OnBehalfOf, createPayload(req, fields))
		if err != nil {
			return fmt.Errorf("insert event: %w", err)
		}
		if err := insertEventValues(ctx, tx, id, itemSeq, fields); err != nil {
			return err
		}

		_, err = tx.Exec(ctx, `insert into outbox (item_id, item_seq) values ($1, $2)`, id, itemSeq)
		if err != nil {
			return fmt.Errorf("insert outbox: %w", err)
		}

		res = WriteResult{ItemID: id, CommitSeq: commitSeq, ItemSeq: itemSeq}
		_, err = tx.Exec(ctx,
			`update idempotency set result = $4 where namespace_id = $1 and actor = $2 and key = $3`,
			defaultNamespace, req.Principal.Actor, req.IdempotencyKey, res)
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

// nextCommitSeq takes the next commit_seq from the single counter row. The
// update locks that row until the transaction ends, so writes commit one at a
// time and commit_seq values become visible in order.
func nextCommitSeq(ctx context.Context, tx pgx.Tx) (int64, error) {
	var seq int64
	err := tx.QueryRow(ctx,
		`update commit_counter set last_seq = last_seq + 1 returning last_seq`).Scan(&seq)
	if err != nil {
		return 0, fmt.Errorf("take commit_seq: %w", err)
	}
	return seq, nil
}

// insertEventValues stores the field values an event set, beside the event
// rather than in it, so they can be erased without touching the log. An event
// that sets no field gets no row.
func insertEventValues(ctx context.Context, tx pgx.Tx, id uuid.UUID, itemSeq int64, values map[string]any) error {
	if len(values) == 0 {
		return nil
	}
	_, err := tx.Exec(ctx,
		`insert into item_event_values (item_id, item_seq, field_values) values ($1, $2, $3)`,
		id, itemSeq, values)
	if err != nil {
		return fmt.Errorf("insert event values: %w", err)
	}
	return nil
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

// requestHash fingerprints a request. encoding/json writes map keys in sorted
// order, so equal requests always produce the same bytes.
func requestHash(op string, req any) (string, error) {
	b, err := json.Marshal(struct {
		Op      string `json:"op"`
		Request any    `json:"request"`
	}{op, req})
	if err != nil {
		return "", fmt.Errorf("%w: request cannot be encoded: %v", ErrInvalid, err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// createPayload builds the event payload. It records which fields were set,
// never their values: values may hold personal data, and events are permanent.
func createPayload(req CreateRequest, fields map[string]any) map[string]any {
	// maps.Keys yields keys in random order; slices.Sorted collects and sorts them.
	p := map[string]any{
		"type_id":      req.TypeID,
		"type_version": req.TypeVersion,
		"state":        req.State,
		"field_ids":    slices.Sorted(maps.Keys(fields)),
	}
	if req.ContainerID != nil {
		p["container_id"] = req.ContainerID
	}
	return p
}

// Get returns the Item as the principal may see it.
func (k *Kernel) Get(ctx context.Context, p item.Principal, id uuid.UUID) (item.Item, error) {
	it := item.Item{ID: id}
	err := k.pool.QueryRow(ctx,
		`select type_id, type_version, container_id, scope_id, state, fields, item_seq
		 from items where id = $1`, id,
	).Scan(&it.TypeID, &it.TypeVersion, &it.ContainerID, &it.ScopeID, &it.State, &it.Fields, &it.ItemSeq)
	if errors.Is(err, pgx.ErrNoRows) {
		return item.Item{}, ErrNotFound
	}
	if err != nil {
		return item.Item{}, fmt.Errorf("read item: %w", err)
	}
	return policy.Project(p, it), nil
}
