package itemservice

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/lifecycle"
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

// ErrNotBuilt answers Migrate until it is built (a stub of Phase B).
var ErrNotBuilt = errors.New("itemservice: migrate is not built yet")

// Migrate is a stub: not built yet.
func (k *Service) Migrate(ctx context.Context, req MigrateRequest) (MigrateResult, error) {
	return MigrateResult{}, ErrNotBuilt
}
