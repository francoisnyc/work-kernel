package itemservice

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/policy"
)

// PolicyPatch changes an Item's policy part through Update. Set exactly one:
//
//   - Readers narrows the Item to these readers, replacing any earlier list.
//   - Inherit removes the Item's own narrowing: it goes back to its
//     container's scope.
//
// Either way the Item can only be as visible as its container, never more.
type PolicyPatch struct {
	Readers []string // opaque principal IDs
	Inherit bool
}

// checkReaders validates a reader list and returns it sorted, without
// duplicates. A list that leaves out the principal making the change is
// refused: nobody hides an Item from themselves. (Until grants exist there
// would be no way back.)
func checkReaders(readers []string, p item.Principal) ([]string, error) {
	if len(readers) == 0 {
		return nil, fmt.Errorf("%w: a narrowed Item needs at least one reader", ErrInvalid)
	}
	if slices.Contains(readers, "") {
		return nil, fmt.Errorf("%w: a reader ID is empty", ErrInvalid)
	}
	if !slices.Contains(readers, p.Actor) ||
		(p.OnBehalfOf != "" && !slices.Contains(readers, p.OnBehalfOf)) {
		return nil, fmt.Errorf("%w: this would hide the Item from you", ErrInvalid)
	}
	// Clone first: sorting in place would reorder the caller's slice.
	sorted := slices.Clone(readers)
	slices.Sort(sorted)
	return slices.Compact(sorted), nil // Compact drops adjacent duplicates
}

// checkPolicyPatch validates a patch and returns its readers in stored form.
func checkPolicyPatch(patch *PolicyPatch, p item.Principal) ([]string, error) {
	if patch.Inherit {
		if len(patch.Readers) > 0 {
			return nil, fmt.Errorf("%w: a policy patch sets readers or inherit, not both", ErrInvalid)
		}
		return nil, nil
	}
	if len(patch.Readers) == 0 {
		return nil, fmt.Errorf("%w: a policy patch sets readers or inherit", ErrInvalid)
	}
	return checkReaders(patch.Readers, p)
}

// checkVisible returns ErrNotFound if the principal cannot read the scope.
// The scopes are read inside the write's transaction, after the Item row is
// locked, so the answer cannot go stale before the write.
func checkVisible(ctx context.Context, tx pgx.Tx, p item.Principal, scopeID uuid.UUID) error {
	visible, err := policy.VisibleScopes(ctx, tx, defaultNamespace, p)
	if err != nil {
		return err
	}
	if !slices.Contains(visible, scopeID) {
		return ErrNotFound
	}
	return nil
}

// parentScope returns the scope an Item inherits: its container's scope, or
// the namespace's root scope when it has no container. A missing container is
// ErrNotFound. With lock set, the container row is share-locked so its scope
// cannot change until this transaction ends.
func parentScope(ctx context.Context, tx pgx.Tx, containerID *uuid.UUID, lock bool) (uuid.UUID, error) {
	var (
		id  uuid.UUID
		err error
	)
	if containerID == nil {
		err = tx.QueryRow(ctx,
			`select id from scopes where namespace_id = $1 and parent_id is null`, defaultNamespace).Scan(&id)
	} else {
		q := `select scope_id from items where id = $1 and namespace_id = $2`
		if lock {
			q += ` for share`
		}
		err = tx.QueryRow(ctx, q, *containerID, defaultNamespace).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, ErrNotFound
		}
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("read parent scope: %w", err)
	}
	return id, nil
}

// insertScope creates a narrowing scope node under parent and returns its ID.
func insertScope(ctx context.Context, tx pgx.Tx, parent uuid.UUID, readers []string) (uuid.UUID, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.Nil, fmt.Errorf("new scope id: %w", err)
	}
	_, err = tx.Exec(ctx,
		`insert into scopes (id, namespace_id, parent_id, narrows) values ($1, $2, $3, true)`,
		id, defaultNamespace, parent)
	if err != nil {
		return uuid.Nil, fmt.Errorf("insert scope: %w", err)
	}
	if err := insertReaders(ctx, tx, id, readers); err != nil {
		return uuid.Nil, err
	}
	return id, nil
}

func insertReaders(ctx context.Context, tx pgx.Tx, scopeID uuid.UUID, readers []string) error {
	// unnest turns the array into one row per reader.
	_, err := tx.Exec(ctx,
		`insert into scope_readers (scope_id, principal) select $1, unnest($2::text[])`, scopeID, readers)
	if err != nil {
		return fmt.Errorf("insert scope readers: %w", err)
	}
	return nil
}

// descendants selects every Item below $1 in the container tree, with its scope.
const descendants = `
	with recursive d as (
	    select id, scope_id from items where container_id = $1
	    union all
	    select i.id, i.scope_id from items i join d on i.container_id = d.id
	)`

// applyPolicy carries out a policy patch on a locked Item and returns the
// Item's scope afterwards. readers is the patch's reader list as returned by
// checkPolicyPatch (nil for inherit).
//
// An Item is narrowed when it has a scope node of its own, that is, when its
// scope differs from the one it would inherit. Items below it in the
// container tree that inherit its scope must follow it, so the patch may
// re-point their scope_id. That is bookkeeping, not a change to those Items:
// they get no event and keep their item_seq.
func applyPolicy(ctx context.Context, tx pgx.Tx, id uuid.UUID, it lockedItem, patch *PolicyPatch, readers []string) (uuid.UUID, error) {
	inherited, err := parentScope(ctx, tx, it.ContainerID, false)
	if err != nil {
		return uuid.Nil, err
	}
	narrowed := it.ScopeID != inherited

	switch {
	case !patch.Inherit && narrowed:
		// Replace the reader list of the Item's own node.
		if _, err := tx.Exec(ctx, `delete from scope_readers where scope_id = $1`, it.ScopeID); err != nil {
			return uuid.Nil, fmt.Errorf("delete scope readers: %w", err)
		}
		return it.ScopeID, insertReaders(ctx, tx, it.ScopeID, readers)

	case !patch.Inherit:
		// Give the Item a node of its own, then move what hangs below the
		// Item under that node: narrowed descendants' nodes first, then the
		// descendants that simply inherit.
		node, err := insertScope(ctx, tx, it.ScopeID, readers)
		if err != nil {
			return uuid.Nil, err
		}
		_, err = tx.Exec(ctx, descendants+
			` update scopes set parent_id = $3 where parent_id = $2 and id in (select scope_id from d)`,
			id, it.ScopeID, node)
		if err != nil {
			return uuid.Nil, fmt.Errorf("move descendant scopes: %w", err)
		}
		_, err = tx.Exec(ctx, descendants+
			` update items set scope_id = $3 where scope_id = $2 and id in (select id from d)`,
			id, it.ScopeID, node)
		if err != nil {
			return uuid.Nil, fmt.Errorf("move descendants: %w", err)
		}
		return node, nil

	case narrowed:
		// Remove the Item's own node. Everything that used it, or hung below
		// it, moves up to the scope the Item inherits.
		node := it.ScopeID
		if _, err := tx.Exec(ctx, `update items set scope_id = $2 where scope_id = $1`, node, inherited); err != nil {
			return uuid.Nil, fmt.Errorf("move Items out of the scope: %w", err)
		}
		if _, err := tx.Exec(ctx, `update scopes set parent_id = $2 where parent_id = $1`, node, inherited); err != nil {
			return uuid.Nil, fmt.Errorf("move child scopes: %w", err)
		}
		if _, err := tx.Exec(ctx, `delete from scope_readers where scope_id = $1`, node); err != nil {
			return uuid.Nil, fmt.Errorf("delete scope readers: %w", err)
		}
		if _, err := tx.Exec(ctx, `delete from scopes where id = $1`, node); err != nil {
			return uuid.Nil, fmt.Errorf("delete scope: %w", err)
		}
		return inherited, nil

	default:
		// Inherit on an Item that is not narrowed: nothing to change.
		return inherited, nil
	}
}
