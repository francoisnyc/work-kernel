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
//   - Inherit removes the Item's own narrowing: it reads like its container
//     again.
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
	// Readers are users or agents. group: is reserved until groups exist,
	// and system: principals read nothing through scopes.
	for _, r := range readers {
		if !item.IsKind(r, item.KindUser, item.KindAgent) {
			return nil, fmt.Errorf("%w: reader %q must be user:<id> or agent:<id>", ErrInvalid, r)
		}
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
// It runs inside the write's transaction, after the Item row is locked.
func checkVisible(ctx context.Context, tx pgx.Tx, p item.Principal, scopeID uuid.UUID) error {
	ok, err := policy.CanSee(ctx, tx, p, scopeID)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotFound
	}
	return nil
}

func rootScope(ctx context.Context, tx pgx.Tx) (uuid.UUID, error) {
	var id uuid.UUID
	err := tx.QueryRow(ctx,
		`select id from scopes where namespace_id = $1 and parent_id is null`, defaultNamespace).Scan(&id)
	if err != nil {
		return uuid.Nil, fmt.Errorf("read root scope: %w", err)
	}
	return id, nil
}

// childScope returns the scope a new child of the container goes into: the
// container's own node, created here when this is its first child. With no
// container it is the namespace's root scope. A missing container is
// ErrNotFound.
//
// Most calls find the node and take only a FOR SHARE lock on the container,
// so children of one channel do not queue behind each other. Only the path
// that creates the node locks the container FOR UPDATE; the unique index on
// scopes.item_id backs that up.
func childScope(ctx context.Context, tx pgx.Tx, containerID *uuid.UUID) (uuid.UUID, error) {
	if containerID == nil {
		return rootScope(ctx, tx)
	}
	// read returns the container's scope and whether that scope is a node
	// belonging to the container. With a lock, it locks the container row in
	// a statement of its own first. If it waited for another writer, a joined
	// statement would re-check the join against the row's new version, which
	// may point at a node created after the statement began: the join would
	// find nothing and the container would seem to be gone. The read after
	// the lock is a new statement and sees everything committed before it.
	read := func(lock string) (scope uuid.UUID, owns bool, err error) {
		if lock != "" {
			var one int
			err = tx.QueryRow(ctx, `select 1 from items where id = $1 and namespace_id = $2 `+lock,
				*containerID, defaultNamespace).Scan(&one)
			if errors.Is(err, pgx.ErrNoRows) {
				return uuid.Nil, false, ErrNotFound
			}
			if err != nil {
				return uuid.Nil, false, fmt.Errorf("lock container: %w", err)
			}
		}
		err = tx.QueryRow(ctx,
			`select i.scope_id, s.item_id is not distinct from i.id
			 from items i join scopes s on s.id = i.scope_id
			 where i.id = $1 and i.namespace_id = $2`,
			*containerID, defaultNamespace).Scan(&scope, &owns)
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, false, ErrNotFound
		}
		if err != nil {
			return uuid.Nil, false, fmt.Errorf("read container: %w", err)
		}
		return scope, owns, nil
	}

	// A first look without a lock decides which lock to take, so two first
	// children never both hold FOR SHARE and then both ask for FOR UPDATE.
	_, owns, err := read("")
	if err != nil {
		return uuid.Nil, err
	}
	if owns {
		// FOR SHARE keeps the node from being removed until this commits.
		scope, owns, err := read("for share")
		if err != nil {
			return uuid.Nil, err
		}
		if owns {
			return scope, nil
		}
		// The node was removed in between (an inherit on a container that
		// had no children yet). Making one now would mean asking for FOR
		// UPDATE while holding FOR SHARE: two children doing that wait on
		// each other. Start the transaction again instead; its first look
		// will then take the FOR UPDATE path directly.
		return uuid.Nil, errRetry
	}

	parent, owns, err := read("for update")
	if err != nil {
		return uuid.Nil, err
	}
	if owns {
		return parent, nil // another first child made it while we waited
	}
	node, err := insertNode(ctx, tx, parent, *containerID, false, nil)
	if err != nil {
		return uuid.Nil, err
	}
	// Moving the container into its own node is bookkeeping: no event.
	if _, err := tx.Exec(ctx, `update items set scope_id = $2 where id = $1`, *containerID, node); err != nil {
		return uuid.Nil, fmt.Errorf("move container into its node: %w", err)
	}
	return node, nil
}

// landingScope is the scope a child of the container lands under as things
// stand: the container's current scope, or the root with no container. A
// missing container is ErrNotFound.
func landingScope(ctx context.Context, tx pgx.Tx, containerID *uuid.UUID) (uuid.UUID, error) {
	if containerID == nil {
		return rootScope(ctx, tx)
	}
	var scope uuid.UUID
	err := tx.QueryRow(ctx, `select scope_id from items where id = $1 and namespace_id = $2`,
		*containerID, defaultNamespace).Scan(&scope)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrNotFound
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("read container scope: %w", err)
	}
	return scope, nil
}

// insertNode creates the scope node that belongs to the Item owner, under
// parent, and fills its open flag and scope_access.
func insertNode(ctx context.Context, tx pgx.Tx, parent, owner uuid.UUID, narrows bool, readers []string) (uuid.UUID, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.Nil, fmt.Errorf("new scope id: %w", err)
	}
	// policy_scope_id starts null; recomputeAccess sets it.
	_, err = tx.Exec(ctx,
		`insert into scopes (id, namespace_id, parent_id, narrows, item_id) values ($1, $2, $3, $4, $5)`,
		id, defaultNamespace, parent, narrows, owner)
	if err != nil {
		return uuid.Nil, fmt.Errorf("insert scope: %w", err)
	}
	if err := replaceReaders(ctx, tx, id, readers); err != nil {
		return uuid.Nil, err
	}
	return id, recomputeAccess(ctx, tx, id)
}

func replaceReaders(ctx context.Context, tx pgx.Tx, scopeID uuid.UUID, readers []string) error {
	if _, err := tx.Exec(ctx, `delete from scope_readers where scope_id = $1`, scopeID); err != nil {
		return fmt.Errorf("delete scope readers: %w", err)
	}
	if len(readers) == 0 {
		return nil
	}
	// unnest turns the array into one row per reader.
	_, err := tx.Exec(ctx,
		`insert into scope_readers (scope_id, principal) select $1, unnest($2::text[])`, scopeID, readers)
	if err != nil {
		return fmt.Errorf("insert scope readers: %w", err)
	}
	return nil
}

// recomputeAccess locks the node's subtree and rewrites its access.
func recomputeAccess(ctx context.Context, tx pgx.Tx, root uuid.UUID) error {
	ids, err := lockSubtree(ctx, tx, root)
	if err != nil {
		return err
	}
	return writeAccess(ctx, tx, root, ids, nil)
}

// lockSubtree locks the node and every scope below it, top-down (ancestor
// before descendant), and returns their IDs in that order. Two policy changes
// in one subtree therefore take their locks in the same order: they run one
// after the other and cannot deadlock. A policy change must call it before it
// touches the node: updating the row first and locking it FOR UPDATE later
// would strengthen a lock already held, and if another change is queued on
// that row in between, the two wait on each other. The node's own ancestors
// are read, not locked; a change to one of them recomputes this subtree again
// once it gets the locks. While the locks are held no scope can be added
// below (a new child scope's foreign key needs its parent row).
func lockSubtree(ctx context.Context, tx pgx.Tx, root uuid.UUID) ([]uuid.UUID, error) {
	// The subtree is listed before the locks are taken, so a scope added in
	// between (a narrowing or a first child below) would be missed and keep a
	// stale pointer. List it again once locked, and repeat until it holds
	// still: no scope can be added under a locked one (its parent_id foreign
	// key needs a share lock on the parent row).
	ids, err := subtreeOf(ctx, tx, root)
	if err != nil {
		return nil, err
	}
	for {
		// Postgres sorts first and locks rows as it returns them, so the
		// order by position in ids is the lock order.
		_, err = tx.Exec(ctx,
			`select id from scopes where id = any($1) order by array_position($1, id) for update`, ids)
		if err != nil {
			return nil, fmt.Errorf("lock subtree: %w", err)
		}
		again, err := subtreeOf(ctx, tx, root)
		if err != nil {
			return nil, err
		}
		if slices.Equal(again, ids) {
			return ids, nil
		}
		ids = again
	}
}

// subtreeOf lists the scope and every scope below it, top-down.
func subtreeOf(ctx context.Context, tx pgx.Tx, root uuid.UUID) ([]uuid.UUID, error) {
	rows, err := tx.Query(ctx,
		`with recursive t (id, depth) as (
		     select $1::uuid, 0
		     union all
		     select s.id, t.depth + 1 from scopes s join t on s.parent_id = t.id
		 )
		 select id from t order by depth, id`, root)
	if err != nil {
		return nil, fmt.Errorf("read subtree: %w", err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return nil, fmt.Errorf("read subtree: %w", err)
	}
	return ids, nil
}

// writeAccess rewrites scopes.policy_scope_id and scope_access for a subtree already
// locked by lockSubtree: from the root's parent down, each scope gets its
// parent's access, narrowed by its own readers when it narrows. rootNarrows,
// when not nil, is the root's new narrows flag, written in the same UPDATE.
//
// Each scope row is updated once per call, and a policy change sets the
// root's flag here rather than in an UPDATE of its own. A second UPDATE of a
// row this transaction already changed re-checks its foreign keys, which
// takes a share lock on the parent scope; a change to the parent holds that
// row FOR UPDATE while it waits for this subtree, and the two would deadlock.
func writeAccess(ctx context.Context, tx pgx.Tx, root uuid.UUID, ids []uuid.UUID, rootNarrows *bool) error {

	type node struct {
		parent  uuid.UUID
		narrows bool
	}
	nodes := map[uuid.UUID]node{}
	rows, err := tx.Query(ctx, `select id, parent_id, narrows from scopes where id = any($1)`, ids)
	if err != nil {
		return fmt.Errorf("read subtree nodes: %w", err)
	}
	for rows.Next() {
		var (
			id uuid.UUID
			n  node
		)
		if err := rows.Scan(&id, &n.parent, &n.narrows); err != nil {
			return fmt.Errorf("read subtree nodes: %w", err)
		}
		nodes[id] = n
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read subtree nodes: %w", err)
	}
	if rootNarrows != nil {
		n := nodes[root]
		n.narrows = *rootNarrows
		nodes[root] = n
	}
	readers := map[uuid.UUID]map[string]bool{}
	rows, err = tx.Query(ctx, `select scope_id, principal from scope_readers where scope_id = any($1)`, ids)
	if err != nil {
		return fmt.Errorf("read subtree readers: %w", err)
	}
	for rows.Next() {
		var (
			id uuid.UUID
			p  string
		)
		if err := rows.Scan(&id, &p); err != nil {
			return fmt.Errorf("read subtree readers: %w", err)
		}
		if readers[id] == nil {
			readers[id] = map[string]bool{}
		}
		readers[id][p] = true
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read subtree readers: %w", err)
	}

	// What the root's parent grants: its pointer (uuid.Nil when open) and
	// the effective readers of the scope it points to.
	var (
		parentPtr    *uuid.UUID
		parentAccess []string
	)
	err = tx.QueryRow(ctx,
		`select p.policy_scope_id,
		        coalesce(array(select principal from scope_access where scope_id = p.policy_scope_id), '{}')
		 from scopes p where p.id = $1`, nodes[root].parent).Scan(&parentPtr, &parentAccess)
	if err != nil {
		return fmt.Errorf("read parent scope: %w", err)
	}

	// Walk top-down: ids is ordered by depth, so a parent is done before its
	// children. An inheriting scope takes its parent's pointer; a narrowing
	// scope points at itself, and its effective readers are its own readers,
	// intersected with what it inherits (a nil set means open).
	ptr := map[uuid.UUID]uuid.UUID{} // uuid.Nil = open
	// access holds the effective readers of every narrowing scope a scope
	// here may point to: the ones computed below, and the one the root's
	// parent points to (outside the subtree). Without the latter, a
	// narrowing scope under an inheriting root would intersect with nothing.
	access := map[uuid.UUID]map[string]bool{}
	var outside uuid.UUID // what the root's parent points to; uuid.Nil = open
	if parentPtr != nil {
		outside = *parentPtr
		access[outside] = map[string]bool{}
		for _, p := range parentAccess {
			access[outside][p] = true
		}
	}
	for _, id := range ids {
		n := nodes[id]
		inheritedPtr := outside
		if id != root {
			inheritedPtr = ptr[n.parent]
		}
		inherited := access[inheritedPtr]
		if !n.narrows {
			ptr[id] = inheritedPtr
			continue
		}
		ptr[id] = id
		mine := map[string]bool{}
		for p := range readers[id] {
			if inheritedPtr == uuid.Nil || inherited[p] {
				mine[p] = true
			}
		}
		access[id] = mine
	}

	// Each scope row is written at most once, and only when it changes:
	// a reader change on an already narrowed scope moves no pointer.
	// Empty, not nil, slices: pgx sends a nil slice as NULL.
	upIDs, upPtrs := []uuid.UUID{}, []*uuid.UUID{}
	for _, id := range ids {
		var p *uuid.UUID
		if ptr[id] != uuid.Nil {
			v := ptr[id]
			p = &v
		}
		upIDs, upPtrs = append(upIDs, id), append(upPtrs, p)
	}
	_, err = tx.Exec(ctx,
		`update scopes s set policy_scope_id = u.ptr,
		                     narrows = case when s.id = $3 then $4 else s.narrows end
		 from unnest($1::uuid[], $2::uuid[]) as u (id, ptr)
		 where s.id = u.id
		   and (s.policy_scope_id is distinct from u.ptr or (s.id = $3 and s.narrows <> $4))`,
		upIDs, upPtrs, root, nodes[root].narrows)
	if err != nil {
		return fmt.Errorf("set pointers: %w", err)
	}

	// Only narrowing scopes hold rows: clear the subtree's, write theirs.
	if _, err := tx.Exec(ctx, `delete from scope_access where scope_id = any($1)`, ids); err != nil {
		return fmt.Errorf("clear scope_access: %w", err)
	}
	scopeCol, principalCol := []uuid.UUID{}, []string{}
	for _, id := range ids {
		for p := range access[id] {
			scopeCol = append(scopeCol, id)
			principalCol = append(principalCol, p)
		}
	}
	if len(scopeCol) > 0 {
		_, err = tx.Exec(ctx,
			`insert into scope_access (scope_id, principal) select unnest($1::uuid[]), unnest($2::text[])`,
			scopeCol, principalCol)
		if err != nil {
			return fmt.Errorf("write scope_access: %w", err)
		}
	}
	return nil
}

// applyPolicy carries out a policy patch on a locked Item and returns the
// Item's scope afterwards, and a node the caller must remove with removeNode
// once it has moved the Item out of it (uuid.Nil when there is none). readers is the patch's reader list as returned by
// checkPolicyPatch (nil for inherit).
//
// An Item has a node of its own once it is narrowed or has a child. A patch
// changes that node in place (its flag and readers), so no descendant row
// changes; only scope_access below it is recomputed. The one exception is
// inherit on an Item that owns a node but has no children: the node is no
// longer needed; the caller removes it after moving the Item out.
func applyPolicy(ctx context.Context, tx pgx.Tx, id uuid.UUID, it lockedItem, patch *PolicyPatch, readers []string) (scope, remove uuid.UUID, err error) {
	var node uuid.UUID
	err = tx.QueryRow(ctx, `select id from scopes where item_id = $1`, id).Scan(&node)
	owns := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, uuid.Nil, fmt.Errorf("read the Item's node: %w", err)
	}

	switch {
	case !patch.Inherit && owns:
		ids, err := lockSubtree(ctx, tx, node) // before touching the node
		if err != nil {
			return uuid.Nil, uuid.Nil, err
		}
		if err := replaceReaders(ctx, tx, node, readers); err != nil {
			return uuid.Nil, uuid.Nil, err
		}
		narrows := true
		return node, uuid.Nil, writeAccess(ctx, tx, node, ids, &narrows)

	case !patch.Inherit:
		// No node yet, so no children either: the new node has nothing below it.
		node, err := insertNode(ctx, tx, it.ScopeID, id, true, readers)
		return node, uuid.Nil, err

	case !owns:
		return it.ScopeID, uuid.Nil, nil // inherit on an Item that is not narrowed

	default:
		var hasChildren bool
		err := tx.QueryRow(ctx, `select exists (select 1 from items where container_id = $1)`, id).Scan(&hasChildren)
		if err != nil {
			return uuid.Nil, uuid.Nil, fmt.Errorf("look for children: %w", err)
		}
		// Without children the node goes and the Item moves up to the parent
		// scope, whose row its foreign key will share-lock. Take that lock
		// now, before the node's: locks stay top-down (ancestor first), so a
		// concurrent change to the parent cannot hold it while waiting for us.
		var parent uuid.UUID
		if err := tx.QueryRow(ctx, `select parent_id from scopes where id = $1`, node).Scan(&parent); err != nil {
			return uuid.Nil, uuid.Nil, fmt.Errorf("read parent scope: %w", err)
		}
		if !hasChildren {
			if _, err := tx.Exec(ctx, `select 1 from scopes where id = $1 for key share`, parent); err != nil {
				return uuid.Nil, uuid.Nil, fmt.Errorf("lock parent scope: %w", err)
			}
		}
		ids, err := lockSubtree(ctx, tx, node) // before touching the node
		if err != nil {
			return uuid.Nil, uuid.Nil, err
		}
		if hasChildren {
			if err := replaceReaders(ctx, tx, node, nil); err != nil {
				return uuid.Nil, uuid.Nil, err
			}
			narrows := false
			return node, uuid.Nil, writeAccess(ctx, tx, node, ids, &narrows)
		}
		// No children: the node is no longer needed. The caller moves the
		// Item to the parent scope in its one UPDATE of the Item row, then
		// removes the node. Updating the row here as well would make that
		// second UPDATE re-check the row's foreign keys, share-locking the
		// container's Item row, which a concurrent change to the container
		// holds FOR UPDATE while it waits for this subtree.
		return parent, node, nil
	}
}

// removeNode deletes a node no Item uses any more, with its readers and rows.
func removeNode(ctx context.Context, tx pgx.Tx, node uuid.UUID) error {
	for _, q := range []string{
		`delete from scope_access where scope_id = $1`,
		`delete from scope_readers where scope_id = $1`,
		`delete from scopes where id = $1`,
	} {
		if _, err := tx.Exec(ctx, q, node); err != nil {
			return fmt.Errorf("remove scope: %w", err)
		}
	}
	return nil
}

// lockForMove takes the namespace-wide move lock, then the moved Item's row
// and the destination's, in ID order.
func lockForMove(ctx context.Context, tx pgx.Tx, id uuid.UUID, to *uuid.UUID) error {
	// An advisory lock is a named lock with no row behind it; the _xact_
	// form is released when the transaction ends.
	if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtext('move:' || $1))`, defaultNamespace); err != nil {
		return fmt.Errorf("take the move lock: %w", err)
	}
	ids := []uuid.UUID{id}
	if to != nil && *to != id {
		ids = append(ids, *to)
	}
	_, err := tx.Exec(ctx,
		`select id from items where id = any($1) and namespace_id = $2 order by id for update`, ids, defaultNamespace)
	if err != nil {
		return fmt.Errorf("lock Items for the move: %w", err)
	}
	return nil
}

// move puts the locked Item under a new container (nil = the top level) and
// returns the Item's scope afterwards. The destination must be visible to
// the principal (else ErrNotFound) and must not be the Item or below it
// (ErrInvalid). If the Item has a node of its own, the node moves with it
// and the pointers below are recomputed; no descendant Item row changes.
func (k *Service) move(ctx context.Context, tx pgx.Tx, p item.Principal, id uuid.UUID, to *uuid.UUID) (uuid.UUID, error) {
	target, err := landingScope(ctx, tx, to)
	if err != nil {
		return uuid.Nil, err
	}
	if to != nil {
		if err := checkVisible(ctx, tx, p, target); err != nil {
			return uuid.Nil, err
		}
		var cycle bool
		err := tx.QueryRow(ctx,
			`with recursive up (id, container_id) as (
			     select id, container_id from items where id = $1
			     union all
			     select i.id, i.container_id from items i join up on i.id = up.container_id
			 )
			 select exists (select 1 from up where id = $2)`, *to, id).Scan(&cycle)
		if err != nil {
			return uuid.Nil, fmt.Errorf("check for a cycle: %w", err)
		}
		if cycle {
			return uuid.Nil, fmt.Errorf("%w: an Item cannot move into itself or anything below it", ErrInvalid)
		}
	}
	if err := checkAgentWrite(ctx, tx, p, target); err != nil {
		return uuid.Nil, err
	}

	parent, err := childScope(ctx, tx, to)
	if err != nil {
		return uuid.Nil, err
	}
	var node uuid.UUID
	err = tx.QueryRow(ctx, `select id from scopes where item_id = $1`, id).Scan(&node)
	if errors.Is(err, pgx.ErrNoRows) {
		return parent, nil // no node of its own: the Item simply lands in parent
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("read the Item's node: %w", err)
	}
	// The node changes parent. Share-lock the new parent first (its foreign
	// key needs it), then lock the node's subtree: ancestor before descendant.
	if _, err := tx.Exec(ctx, `select 1 from scopes where id = $1 for key share`, parent); err != nil {
		return uuid.Nil, fmt.Errorf("lock the new parent scope: %w", err)
	}
	ids, err := lockSubtree(ctx, tx, node)
	if err != nil {
		return uuid.Nil, err
	}
	if _, err := tx.Exec(ctx, `update scopes set parent_id = $2 where id = $1`, node, parent); err != nil {
		return uuid.Nil, fmt.Errorf("move scope: %w", err)
	}
	return node, writeAccess(ctx, tx, node, ids, nil)
}
