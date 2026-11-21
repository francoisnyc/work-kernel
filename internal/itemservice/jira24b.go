package itemservice

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

// Jira 2.4b, PR 1: the building blocks in the item service (see
// typedef/jira24b.go for what a Type declares).
//
// The lock order (docs/design.md, "Locks"), everywhere:
//
//  1. the container, FOR SHARE, when the write reads it (a guard reads its
//     state, an effect its fields), or FOR NO KEY UPDATE when the write
//     judges a field unique among its siblings: two such writes in one
//     container then run one after the other;
//  2. the Item together with its linked rows, in one pass in UUID order,
//     as before (lockForTransition);
//  3. its children, FOR SHARE, in UUID order, when a guard reads them.
//
// A container that changes before its lock (the Item moved) and a set of
// children that changes before theirs start the transaction again
// (errRetry), as links do.

// aroundNeeds is what a write into a state reads around the Item: the
// union over every transition into it, so that it does not depend on the
// state the Item is in before it is locked.
type aroundNeeds struct {
	container bool     // a guard reads its state, or an effect its fields
	exclusive bool     // a field is unique among siblings: lock the container FOR NO KEY UPDATE
	creator   bool     // except: [container_creator] reads who created it
	children  []string // the Types of children a guard reads, sorted
}

func (n aroundNeeds) any() bool {
	return n.container || n.exclusive || n.creator || len(n.children) > 0
}

func needsFor(ty *typedef.Type, to string) aroundNeeds {
	var n aroundNeeds
	for _, tr := range ty.Transitions {
		if tr.To != to {
			continue
		}
		n.container = n.container || tr.GuardContainer || len(tr.SetsContainer) > 0
		n.creator = n.creator || slices.Contains(tr.Except, typedef.AllowedContainerCreator)
		n.children = append(n.children, tr.GuardChildren...)
	}
	n.exclusive = len(ty.UniqueFields()) > 0
	slices.Sort(n.children)
	n.children = slices.Compact(n.children)
	return n
}

// containerRow is the container as a write reads it.
type containerRow struct {
	ID                        uuid.UUID
	TypeID                    string
	TypeVersion               int
	State                     string
	Fields                    map[string]any
	CreatedBy, CreatedForUser string
}

// lockContainer locks the container (FOR SHARE, or FOR NO KEY UPDATE when
// exclusive) and reads it.
func lockContainer(ctx context.Context, tx pgx.Tx, ns string, id uuid.UUID, exclusive bool) (containerRow, error) {
	mode := "for share"
	if exclusive {
		mode = "for no key update"
	}
	c := containerRow{ID: id}
	err := tx.QueryRow(ctx,
		`select type_id, type_version, state, fields, created_by, coalesce(created_on_behalf_of, '')
		 from items where id = $1 and namespace_id = $2 `+mode, id, ns,
	).Scan(&c.TypeID, &c.TypeVersion, &c.State, &c.Fields, &c.CreatedBy, &c.CreatedForUser)
	if errors.Is(err, pgx.ErrNoRows) {
		return containerRow{}, ErrNotFound
	}
	if err != nil {
		return containerRow{}, fmt.Errorf("lock container: %w", err)
	}
	return c, nil
}

// aroundView is what a transition reads around its Item, under the locks.
type aroundView struct {
	around    *lifecycle.Around // nil: no guard into the state reads it
	container lifecycle.ContainerFields
	// Who created the container: except: [container_creator].
	containerCreatedBy, containerCreatedFor string
}

// facts are what `allowed` and `except` read: the Item's, and its
// container's creator.
func (v aroundView) facts(it lockedItem) policy.ItemFacts {
	f := facts(it)
	f.ContainerCreatedBy, f.ContainerCreatedOnBehalfOf = v.containerCreatedBy, v.containerCreatedFor
	return f
}

// lockAround takes step 1 of the lock order for a write into `to` on the
// Item id: it reads the Item's container unlocked and locks it. check, run
// once the Item itself is locked, makes sure the container is still the
// one locked (else errRetry) and reads the children (step 3).
func (k *Service) lockAround(ctx context.Context, tx pgx.Tx, ns string, id uuid.UUID, ty *typedef.Type,
	to string) (check func(it lockedItem) (aroundView, error), err error) {
	n := needsFor(ty, to)
	if !n.any() {
		return func(lockedItem) (aroundView, error) { return aroundView{}, nil }, nil
	}
	var before *uuid.UUID
	if err := tx.QueryRow(ctx, `select container_id from items where id = $1`, id).Scan(&before); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("read container: %w", err)
	}
	var c *containerRow
	if before != nil && (n.container || n.exclusive) {
		row, err := lockContainer(ctx, tx, ns, *before, n.exclusive)
		if err != nil {
			return nil, err
		}
		c = &row
	}
	return func(it lockedItem) (aroundView, error) {
		if !sameID(it.ContainerID, before) {
			return aroundView{}, errRetry // moved before the lock: read again
		}
		var v aroundView
		if it.ContainerID != nil && c == nil && n.creator {
			// Only who created it: set once, never changed; no lock.
			row := containerRow{}
			err := tx.QueryRow(ctx, `select created_by, coalesce(created_on_behalf_of, '') from items where id = $1`,
				*it.ContainerID).Scan(&row.CreatedBy, &row.CreatedForUser)
			if err != nil {
				return aroundView{}, fmt.Errorf("read container: %w", err)
			}
			c = &row
		}
		if c != nil {
			v.containerCreatedBy, v.containerCreatedFor = c.CreatedBy, c.CreatedForUser
			if c.TypeID != "" {
				cty, err := k.loadType(ctx, tx, c.TypeID, c.TypeVersion)
				if err != nil {
					return aroundView{}, err
				}
				v.container = lifecycle.ContainerFields{Type: cty, Fields: c.Fields}
			}
		}
		if n.container || len(n.children) > 0 {
			v.around = &lifecycle.Around{}
			if c != nil && c.TypeID != "" {
				st, _ := v.container.Type.State(c.State)
				v.around.Container = &lifecycle.LinkedItem{State: c.State, Category: st.Category}
			}
			if len(n.children) > 0 {
				children, err := k.lockChildren(ctx, tx, id, n.children)
				if err != nil {
					return aroundView{}, err
				}
				v.around.Children = children
			}
		}
		return v, nil
	}, nil
}

func sameID(a, b *uuid.UUID) bool {
	return (a == nil) == (b == nil) && (a == nil || *a == *b)
}

// lockChildren locks, FOR SHARE in UUID order, the Items of these Types
// contained in id (step 3), and reads their state and category, whoever
// can see them: a guard judges the data, not the reader. The container is
// locked FOR UPDATE already, so no child can be created or moved in (both
// take its key share); one moved out between the read and the lock starts
// the transaction again.
func (k *Service) lockChildren(ctx context.Context, tx pgx.Tx, id uuid.UUID, types []string) (map[string][]lifecycle.LinkedItem, error) {
	read := func(lock bool) ([]uuid.UUID, []childState, error) {
		q := `select id, type_id, type_version, state from items where container_id = $1 and type_id = any($2) order by id`
		if lock {
			q += ` for share`
		}
		rows, err := tx.Query(ctx, q, id, types)
		if err != nil {
			return nil, nil, fmt.Errorf("read children: %w", err)
		}
		got, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (childState, error) {
			var c childState
			return c, r.Scan(&c.id, &c.typeID, &c.version, &c.state)
		})
		if err != nil {
			return nil, nil, fmt.Errorf("read children: %w", err)
		}
		ids := make([]uuid.UUID, 0, len(got))
		for _, c := range got {
			ids = append(ids, c.id)
		}
		return ids, got, nil
	}
	before, _, err := read(false)
	if err != nil {
		return nil, err
	}
	after, got, err := read(true)
	if err != nil {
		return nil, err
	}
	if !slices.Equal(before, after) {
		return nil, errRetry
	}
	out := map[string][]lifecycle.LinkedItem{}
	for _, c := range got {
		cty, err := k.loadType(ctx, tx, c.typeID, c.version)
		if err != nil {
			return nil, err
		}
		st, _ := cty.State(c.state)
		out[c.typeID] = append(out[c.typeID], lifecycle.LinkedItem{State: c.state, Category: st.Category})
	}
	return out, nil
}

type childState struct {
	id      uuid.UUID
	typeID  string
	version int
	state   string
}

// --- 9. unique among siblings ---

// checkUnique refuses a write that leaves the Item (id; uuid.Nil for one
// being created) in state with fields, inside container, holding the value
// of a unique field another Item of its Type there holds in one of that
// field's states. The caller holds the container FOR NO KEY UPDATE, so two
// such writes in one container are judged one after the other.
func checkUnique(ctx context.Context, tx pgx.Tx, ty *typedef.Type, id uuid.UUID, container *uuid.UUID,
	state string, fields map[string]any) error {
	if container == nil || len(ty.UniqueFields()) == 0 {
		return nil
	}
	rows, err := tx.Query(ctx, `select state, fields from items where container_id = $1 and type_id = $2 and id <> $3`,
		*container, ty.ID, id)
	if err != nil {
		return fmt.Errorf("read siblings: %w", err)
	}
	siblings, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (lifecycle.Sibling, error) {
		var s lifecycle.Sibling
		return s, r.Scan(&s.State, &s.Fields)
	})
	if err != nil {
		return fmt.Errorf("read siblings: %w", err)
	}
	if f, clash := lifecycle.SiblingClash(ty, state, fields, siblings); clash {
		return rejected(CodeNotUnique, "another %s in this container holds the same %s", ty.ID, f)
	}
	return nil
}

// lockUniqueContainer takes step 1 of the lock order for an update of an
// Item whose Type has a unique field: its container FOR NO KEY UPDATE. The
// check it returns, run once the Item is locked, makes sure the container
// is still the one locked (else errRetry).
func (k *Service) lockUniqueContainer(ctx context.Context, tx pgx.Tx, ns string, id uuid.UUID) (func(lockedItem) error, error) {
	var typeID string
	var version int
	var before *uuid.UUID
	err := tx.QueryRow(ctx, `select type_id, type_version, container_id from items where id = $1 and namespace_id = $2`,
		id, ns).Scan(&typeID, &version, &before)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read item: %w", err)
	}
	ty, err := k.loadType(ctx, tx, typeID, version)
	if err != nil {
		return nil, err
	}
	if before == nil || len(ty.UniqueFields()) == 0 {
		return func(lockedItem) error { return nil }, nil
	}
	if _, err := lockContainer(ctx, tx, ns, *before, true); err != nil {
		return nil, err
	}
	return func(it lockedItem) error {
		if !sameID(it.ContainerID, before) {
			return errRetry
		}
		return nil
	}, nil
}

// --- 10. who places an Item ---

// checkPlacement refuses a move or a change of readers (narrowing, or
// inheriting again) of an Item of ty by a principal the Type does not let
// place it: placement_writable_by kernel lets no one; a list of roles,
// their holders (an agent and the user it acts for must both hold one).
// A Type that says nothing lets anyone who sees the Item, as before. The
// kernel's own writes are not this rule's question.
func checkPlacement(ctx context.Context, tx pgx.Tx, p item.Principal, ty *typedef.Type) error {
	roles, limited := ty.PlacementRoles()
	if !limited || item.IsKernelActor(p.Actor) {
		return nil
	}
	refuse := &PlacementNotWritableError{TypeID: ty.ID}
	if len(roles) == 0 {
		return refuse
	}
	holds := func(who string) (bool, error) {
		have, err := policy.Roles(ctx, tx, nsOf(p), who)
		if err != nil {
			return false, err
		}
		return slices.ContainsFunc(roles, func(r string) bool { return slices.Contains(have, r) }), nil
	}
	ok, err := holds(p.Actor)
	if err != nil || !ok {
		return cmpErr(err, refuse)
	}
	if p.OnBehalfOf != "" {
		if ok, err = holds(p.OnBehalfOf); err != nil || !ok {
			return cmpErr(err, refuse)
		}
	}
	return nil
}

// cmpErr is err if there is one, else the refusal.
func cmpErr(err error, refuse error) error {
	if err != nil {
		return err
	}
	return refuse
}

// --- 3. for_each ---

// memberPart names one member in a key and a record's key: a hash, so that
// a member (which may be free text) is never written where an opaque ID
// belongs.
func memberPart(member string) string {
	sum := sha256.Sum256([]byte(member))
	return hex.EncodeToString(sum[:8])
}
