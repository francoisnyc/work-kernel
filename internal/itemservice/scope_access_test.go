// Tests for Step A: visibility kept in scopes.open and scope_access, and a
// scope node per container, so narrowing a container changes no descendant.
//
// The behavior tests of Step 4 (visibility_test.go) stay as they are; in
// particular TestItemNarrowedLater_IsHiddenAtOnce and
// TestReaderRemoved_IsHiddenAtOnce prove that scope_access is updated in the
// same transaction as the change.
package itemservice_test

import (
	"fmt"
	"maps"
	"math/rand/v2"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
)

// --- helpers ---

func scopeOf(t *testing.T, id uuid.UUID) uuid.UUID {
	t.Helper()
	var s uuid.UUID
	if err := pool.QueryRow(testCtx(t), `select scope_id from items where id = $1`, id).Scan(&s); err != nil {
		t.Fatalf("read scope of %s: %v", id, err)
	}
	return s
}

// accessOf returns the principals scope_access lists for a scope, sorted.
func accessOf(t *testing.T, scope uuid.UUID) []string {
	t.Helper()
	rows, err := pool.Query(testCtx(t), `select principal from scope_access where scope_id = $1 order by principal`, scope)
	if err != nil {
		t.Fatalf("read scope_access: %v", err)
	}
	defer rows.Close()
	var ps []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			t.Fatalf("scan: %v", err)
		}
		ps = append(ps, p)
	}
	return ps
}

func isOpen(t *testing.T, scope uuid.UUID) bool {
	t.Helper()
	var open bool
	if err := pool.QueryRow(testCtx(t), `select open from scopes where id = $1`, scope).Scan(&open); err != nil {
		t.Fatalf("read scopes.open: %v", err)
	}
	return open
}

func sorted(ps ...string) []string {
	out := slices.Clone(ps)
	slices.Sort(out)
	return out
}

// expectedAccess recomputes, from scopes and scope_readers alone, what
// scope_access and scopes.open should hold for every scope in the namespace:
// a scope is open when no ancestor (itself included) narrows, and otherwise
// readable by the intersection of the readers of every narrowing ancestor.
func expectedAccess(t *testing.T) (open map[uuid.UUID]bool, access map[uuid.UUID][]string) {
	t.Helper()
	type node struct {
		parent  *uuid.UUID
		narrows bool
	}
	nodes := map[uuid.UUID]node{}
	rows, err := pool.Query(testCtx(t), `select id, parent_id, narrows from scopes where namespace_id = 'default'`)
	if err != nil {
		t.Fatalf("read scopes: %v", err)
	}
	for rows.Next() {
		var (
			id uuid.UUID
			n  node
		)
		if err := rows.Scan(&id, &n.parent, &n.narrows); err != nil {
			t.Fatalf("scan: %v", err)
		}
		nodes[id] = n
	}
	rows.Close()
	readers := map[uuid.UUID]map[string]bool{}
	rows, err = pool.Query(testCtx(t), `select scope_id, principal from scope_readers`)
	if err != nil {
		t.Fatalf("read scope_readers: %v", err)
	}
	for rows.Next() {
		var (
			id uuid.UUID
			p  string
		)
		if err := rows.Scan(&id, &p); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if readers[id] == nil {
			readers[id] = map[string]bool{}
		}
		readers[id][p] = true
	}
	rows.Close()

	open = map[uuid.UUID]bool{}
	access = map[uuid.UUID][]string{}
	for id := range nodes {
		var allowed map[string]bool // nil means "no narrowing ancestor yet"
		for cur := &id; cur != nil; cur = nodes[*cur].parent {
			if !nodes[*cur].narrows {
				continue
			}
			if allowed == nil {
				allowed = maps.Clone(readers[*cur])
				if allowed == nil {
					allowed = map[string]bool{}
				}
				continue
			}
			for p := range allowed {
				if !readers[*cur][p] {
					delete(allowed, p)
				}
			}
		}
		open[id] = allowed == nil
		if allowed != nil {
			access[id] = slices.Sorted(maps.Keys(allowed))
		}
	}
	return open, access
}

// assertAccessMatchesRecompute checks the stored table against expectedAccess
// for every scope.
func assertAccessMatchesRecompute(t *testing.T) {
	t.Helper()
	open, access := expectedAccess(t)
	for id, wantOpen := range open {
		if got := isOpen(t, id); got != wantOpen {
			t.Errorf("scope %s: open = %v, want %v", id, got, wantOpen)
		}
		got := accessOf(t, id)
		if len(got) == 0 && len(access[id]) == 0 {
			continue
		}
		if !slices.Equal(got, access[id]) {
			t.Errorf("scope %s: scope_access = %v, want %v", id, got, access[id])
		}
	}
}

// --- the schema ---

func TestSchema_ScopesOpenAndScopeAccess(t *testing.T) {
	n := count(t, `select count(*) from information_schema.columns
	               where table_name = 'scopes' and column_name = 'open' and data_type = 'boolean' and is_nullable = 'NO'`)
	if n != 1 {
		t.Error("scopes.open (boolean, not null) does not exist")
	}
	n = count(t, `select count(*) from information_schema.table_constraints
	              where table_name = 'scope_access' and constraint_type = 'PRIMARY KEY'`)
	if n != 1 {
		t.Error("table scope_access with a primary key does not exist")
	}
	// The root scope is open and has no access rows.
	root := count(t, `select count(*) from scopes where parent_id is null and open`)
	if root != 1 {
		t.Errorf("open root scopes = %d, want 1", root)
	}
}

// --- what scope_access holds ---

// A narrowed Item's scope lists exactly its readers; a plain Item's scope is open.
func TestScopeAccess_NarrowedAndPlainItems(t *testing.T) {
	k := itemservice.New(pool)
	alice, bob := person("alice"), person("bob")
	plain := createAs(t, k, alice, "plain", nil, nil)
	narrowed := createAs(t, k, alice, "narrowed", &itemservice.Narrow{Readers: readers(alice, bob)}, nil)

	if !isOpen(t, scopeOf(t, plain.ItemID)) {
		t.Error("a plain Item's scope is not open")
	}
	s := scopeOf(t, narrowed.ItemID)
	if isOpen(t, s) {
		t.Error("a narrowed Item's scope is open")
	}
	if got, want := accessOf(t, s), sorted(alice.Actor, bob.Actor); !slices.Equal(got, want) {
		t.Errorf("scope_access = %v, want %v", got, want)
	}
}

// Nested narrowing intersects: a child narrowed to {alice, carol} inside a
// container narrowed to {alice, bob} is readable by alice only.
func TestScopeAccess_NestedNarrowingIntersects(t *testing.T) {
	k := itemservice.New(pool)
	alice, bob, carol := person("alice"), person("bob"), person("carol")
	space := createAs(t, k, alice, "space", &itemservice.Narrow{Readers: readers(alice, bob)}, nil)
	child := createAs(t, k, alice, "child", &itemservice.Narrow{Readers: readers(alice, carol)}, &space.ItemID)

	if got := accessOf(t, scopeOf(t, child.ItemID)); !slices.Equal(got, []string{alice.Actor}) {
		t.Errorf("child scope_access = %v, want only %s", got, alice.Actor)
	}
	assertAccessMatchesRecompute(t)
}

// --- a scope node per container ---

// An Item gets its own scope node when it gets its first child. The node
// inherits (narrows = false, open like its parent), and the child shares it.
// The container gets no event for it.
func TestContainer_FirstChildCreatesTheContainersNode(t *testing.T) {
	k := itemservice.New(pool)
	alice := person("alice")
	space := createAs(t, k, alice, "space", nil, nil)
	before := scopeOf(t, space.ItemID)

	child := createAs(t, k, alice, "child", nil, &space.ItemID)

	node := scopeOf(t, space.ItemID)
	if node == before {
		t.Fatal("the container is still in its parent's scope after getting a child")
	}
	var parent uuid.UUID
	var narrows bool
	if err := pool.QueryRow(testCtx(t), `select parent_id, narrows from scopes where id = $1`, node).Scan(&parent, &narrows); err != nil {
		t.Fatalf("read node: %v", err)
	}
	if parent != before || narrows || !isOpen(t, node) {
		t.Errorf("node: parent %s, narrows %v, open %v; want parent %s, not narrowing, open",
			parent, narrows, isOpen(t, node), before)
	}
	if got := scopeOf(t, child.ItemID); got != node {
		t.Errorf("child's scope = %s, want the container's node %s", got, node)
	}
	assertNothingWritten(t, space.ItemID, 1)

	// A second child reuses the node: no new scope.
	scopesBefore := count(t, `select count(*) from scopes`)
	createAs(t, k, alice, "child 2", nil, &space.ItemID)
	if n := count(t, `select count(*) from scopes`); n != scopesBefore {
		t.Errorf("the second child added %d scopes, want 0", n-scopesBefore)
	}
	if got := scopeOf(t, space.ItemID); got != node {
		t.Errorf("the second child moved the container to %s, want %s", got, node)
	}
}

// Narrowing a container flips its node in place: no descendant row changes
// (not its scope_id, not its updated_at), however deep.
func TestContainer_NarrowingChangesNoDescendantRow(t *testing.T) {
	k := itemservice.New(pool)
	alice, bob := person("alice"), person("bob")
	space := createAs(t, k, alice, "space", nil, nil)
	child := createAs(t, k, alice, "child", nil, &space.ItemID)
	grandchild := createAs(t, k, alice, "grandchild", nil, &child.ItemID)

	type row struct {
		scope   uuid.UUID
		updated time.Time
	}
	read := func(id uuid.UUID) row {
		var r row
		if err := pool.QueryRow(testCtx(t), `select scope_id, updated_at from items where id = $1`, id).Scan(&r.scope, &r.updated); err != nil {
			t.Fatalf("read item: %v", err)
		}
		return r
	}
	beforeChild, beforeGrand := read(child.ItemID), read(grandchild.ItemID)

	setPolicy(t, k, alice, space.ItemID, itemservice.PolicyPatch{Readers: readers(alice)})

	if got := read(child.ItemID); !got.updated.Equal(beforeChild.updated) || got.scope != beforeChild.scope {
		t.Errorf("child row changed: %+v -> %+v", beforeChild, got)
	}
	if got := read(grandchild.ItemID); !got.updated.Equal(beforeGrand.updated) || got.scope != beforeGrand.scope {
		t.Errorf("grandchild row changed: %+v -> %+v", beforeGrand, got)
	}
	assertHidden(t, k, bob, grandchild.ItemID)
	assertAccessMatchesRecompute(t)
}

// Inherit on a container flips its node back; the node stays, since the
// container still has children.
func TestContainer_InheritKeepsTheNode(t *testing.T) {
	k := itemservice.New(pool)
	alice, bob := person("alice"), person("bob")
	space := createAs(t, k, alice, "space", &itemservice.Narrow{Readers: readers(alice)}, nil)
	child := createAs(t, k, alice, "child", nil, &space.ItemID)
	node := scopeOf(t, space.ItemID)

	setPolicy(t, k, alice, space.ItemID, itemservice.PolicyPatch{Inherit: true})

	if got := scopeOf(t, space.ItemID); got != node {
		t.Errorf("container scope = %s, want its node %s kept", got, node)
	}
	if !isOpen(t, node) {
		t.Error("after inherit, the container's node is not open")
	}
	assertVisible(t, k, bob, child.ItemID)
	assertAccessMatchesRecompute(t)
}

// --- the read path uses one lookup ---

// Get and writes for a principal who reads one scope do not depend on how
// many narrowed scopes exist: the answer comes from the Item's own scope.
// (Timing is the bench's job; this checks the answers stay right.)
func TestScopeAccess_ManyNarrowedScopesDoNotChangeAnswers(t *testing.T) {
	k := itemservice.New(pool)
	heavy, light := person("heavy"), person("light")
	for i := range 50 {
		createAs(t, k, heavy, fmt.Sprintf("private %d", i), &itemservice.Narrow{Readers: readers(heavy)}, nil)
	}
	public := createAs(t, k, light, "public", nil, nil)
	private := createAs(t, k, heavy, "private", &itemservice.Narrow{Readers: readers(heavy)}, nil)

	assertVisible(t, k, light, public.ItemID)
	assertHidden(t, k, light, private.ItemID)
	assertVisible(t, k, heavy, private.ItemID)
	assertVisible(t, k, heavy, public.ItemID)
	update(t, k, public.ItemID, map[string]any{"f_title": "still public"}) // a write by alice in an open scope
}

// --- concurrent policy changes ---

// Two writers change the readers of a container and of a narrowed child in
// it, at the same time, many times over. Both always succeed (no deadlock),
// and scope_access ends up exactly what a recompute from scratch gives.
func TestScopeAccess_ConcurrentChangesOnContainerAndChild(t *testing.T) {
	k := itemservice.New(pool)
	admin := person("admin")
	others := []item.Principal{person("a"), person("b"), person("c"), person("d")}
	space := createAs(t, k, admin, "space", &itemservice.Narrow{Readers: readers(admin)}, nil)
	child := createAs(t, k, admin, "child", &itemservice.Narrow{Readers: readers(admin)}, &space.ItemID)
	createAs(t, k, admin, "grandchild", nil, &child.ItemID) // gives the child a node with a child below

	// Each round picks a random reader set (always including admin, who makes
	// the change and may not lock themselves out).
	pick := func(r *rand.Rand) []string {
		out := []string{admin.Actor}
		for _, p := range others {
			if r.IntN(2) == 0 {
				out = append(out, p.Actor)
			}
		}
		return out
	}

	const rounds = 25
	errs := make(chan error, 2*rounds)
	var wg sync.WaitGroup
	for w, target := range []uuid.UUID{space.ItemID, child.ItemID} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := rand.New(rand.NewPCG(uint64(w), 42))
			for range rounds {
				_, err := k.Update(testCtx(t), itemservice.UpdateRequest{
					IdempotencyKey: newKey(), Principal: admin, ItemID: target,
					Policy: &itemservice.PolicyPatch{Readers: pick(r)},
				})
				if err != nil {
					errs <- err
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if strings.Contains(err.Error(), "deadlock") {
			t.Errorf("deadlock: %v", err)
		} else {
			t.Errorf("Update: %v", err)
		}
	}
	assertAccessMatchesRecompute(t)
}
