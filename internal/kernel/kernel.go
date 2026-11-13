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
	// ErrInvalid means the request is malformed (for example, no idempotency
	// key) or names a Type that is not registered.
	ErrInvalid = errors.New("kernel: invalid request")
	// ErrNotFound means no Item has the given ID.
	ErrNotFound = errors.New("kernel: item not found")
	// ErrRejected means the request is well formed but the Item's Type refuses
	// it: an unknown or mistyped field, or an illegal transition. The error
	// text carries the reason.
	ErrRejected = errors.New("kernel: rejected")
	// ErrStale means the request named an expected item_seq and the Item has
	// moved since. Unlike ErrRejected, the write may well be fine: the caller
	// should re-read the Item and try again.
	ErrStale = errors.New("kernel: stale")
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
	State          string         // optional; if set it must be the Type's initial state
	Fields         map[string]any // keyed by stable field ID
	ContainerID    *uuid.UUID
	Attribution    item.Attribution // optional; what caused this write
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
	if req.IdempotencyKey == "" || req.Principal.Actor == "" || req.TypeID == "" {
		// %w wraps ErrInvalid, so callers can still match it with errors.Is,
		// like an exception with a cause.
		return WriteResult{}, fmt.Errorf("%w: idempotency key, actor and type are required", ErrInvalid)
	}
	if err := checkAttribution(req.Attribution); err != nil {
		return WriteResult{}, err
	}
	hash, err := requestHash("create", req)
	if err != nil {
		return WriteResult{}, err
	}

	// A Type version never changes once registered, so it can be read
	// outside the write transaction.
	ty, err := loadType(ctx, k.pool, req.TypeID, req.TypeVersion)
	if err != nil {
		return WriteResult{}, err
	}
	// The Type chooses the initial state; a request may name it but not another.
	if req.State != "" && req.State != ty.InitialState {
		return WriteResult{}, fmt.Errorf("%w: type %s v%d starts in state %q, not %q",
			ErrRejected, ty.ID, ty.Version, ty.InitialState, req.State)
	}
	if err := ty.CheckFields(req.Fields, false); err != nil {
		return WriteResult{}, fmt.Errorf("%w: %v", ErrRejected, err)
	}
	fields := req.Fields
	if fields == nil {
		fields = map[string]any{}
	}

	return k.write(ctx, req.Principal, req.IdempotencyKey, hash, func(tx pgx.Tx) (WriteResult, error) {
		id, err := uuid.NewV7()
		if err != nil {
			return WriteResult{}, fmt.Errorf("new item id: %w", err)
		}
		// A row inserted in this transaction is locked until commit, so no
		// separate SELECT ... FOR UPDATE is needed for create.
		const itemSeq = 1
		_, err = tx.Exec(ctx,
			`insert into items (id, namespace_id, type_id, type_version, container_id, state, fields, item_seq)
			 values ($1, $2, $3, $4, $5, $6, $7, $8)`,
			id, defaultNamespace, ty.ID, ty.Version, req.ContainerID, ty.InitialState, fields, itemSeq)
		if err != nil {
			return WriteResult{}, fmt.Errorf("insert item: %w", err)
		}

		// The payload records which fields were set, never their values:
		// values may hold personal data, and events are permanent.
		payload := map[string]any{
			"type_id":      ty.ID,
			"type_version": ty.Version,
			"state":        ty.InitialState,
			"field_ids":    sortedKeys(fields),
		}
		if req.ContainerID != nil {
			payload["container_id"] = req.ContainerID
		}
		return appendEvent(ctx, tx, id, itemSeq, req.Principal, req.Attribution, "create", payload, fields)
	})
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

// Get returns the Item as the principal may see it.
func (k *Kernel) Get(ctx context.Context, p item.Principal, id uuid.UUID) (item.Item, error) {
	it := item.Item{ID: id}
	err := k.pool.QueryRow(ctx,
		`select type_id, type_version, container_id, scope_id, state, fields, item_seq
		 from items where id = $1 and namespace_id = $2`, id, defaultNamespace,
	).Scan(&it.TypeID, &it.TypeVersion, &it.ContainerID, &it.ScopeID, &it.State, &it.Fields, &it.ItemSeq)
	if errors.Is(err, pgx.ErrNoRows) {
		return item.Item{}, ErrNotFound
	}
	if err != nil {
		return item.Item{}, fmt.Errorf("read item: %w", err)
	}
	return policy.Project(p, it), nil
}
