// Package kernel holds the write operations (create, update, transition, link,
// archive) and reads. Every change to an Item goes through this package.
package kernel

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/francoisnyc/work-kernel/internal/item"
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

var errNotImplemented = errors.New("kernel: not implemented")

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
	return WriteResult{}, errNotImplemented
}

// Get returns the Item as the principal may see it.
func (k *Kernel) Get(ctx context.Context, p item.Principal, id uuid.UUID) (item.Item, error) {
	return item.Item{}, errNotImplemented
}
