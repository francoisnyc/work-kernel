package itemservice

// Jira part 2, step 3b: a hook reads an offer of its Item's container
// (container:<name>), and a link only a role writes.

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/policy"
)

// offered resolves container:<name> for a hook of the Item: the principal
// held, now, by the field that the Type of the Item's container offers
// under that name. It is read when the hook runs, so it is whoever the
// field names at that moment.
//
// ErrNoRecipient when there is nobody to tell: the Item has no container,
// the container's Type offers no such name, the field is unset, or the
// principal it names cannot see the container. The value is read AS that
// principal, through visibility and policy.Project: nobody is told of an
// Item they could not open.
func (k *Service) offered(ctx context.Context, itemID uuid.UUID, name string) (string, error) {
	none := func(why string) (string, error) {
		// Why, never who: the error may be logged.
		return "", fmt.Errorf("%w: container:%s: %s", ErrNoRecipient, name, why)
	}
	var container *uuid.UUID
	if err := k.pool.QueryRow(ctx, `select container_id from items where id = $1`, itemID).Scan(&container); err != nil {
		return "", fmt.Errorf("read the Item's container: %w", err)
	}
	if container == nil {
		return none("the Item has no container")
	}
	var (
		typeID, ns string
		version    int
		fields     map[string]any
	)
	err := k.pool.QueryRow(ctx, // the kernel's own read: by ID
		`select type_id, type_version, namespace_id, fields from items where id = $1`, *container).
		Scan(&typeID, &version, &ns, &fields)
	if errors.Is(err, pgx.ErrNoRows) {
		return none("the container is gone")
	}
	if err != nil {
		return "", fmt.Errorf("read the container: %w", err)
	}
	ty, err := k.loadType(ctx, k.pool, typeID, version)
	if err != nil {
		return "", err
	}
	field, ok := ty.Offers[name]
	if !ok {
		return none("the container's Type offers no such name")
	}
	who, _ := fields[field].(string)
	if who == "" {
		return none("the offered field is not set")
	}
	// Read again as the recipient: only what they may see leaves.
	recipient := item.Principal{Actor: who, Namespace: ns}
	seen, _, err := k.visibleItem(ctx, recipient, *container)
	if errors.Is(err, ErrNotFound) || errors.Is(err, ErrInvalid) || errors.Is(err, ErrForbidden) {
		return none("the principal offered cannot see the container")
	}
	if err != nil {
		return "", err
	}
	projected, ok := policy.Project(recipient, []uuid.UUID{*seen.ScopeID}, seen, ty)
	if !ok {
		return none("the principal offered cannot see the container")
	}
	if got, _ := projected.Fields[field].(string); got == who {
		return who, nil
	}
	return none("the offered field is not set")
}
