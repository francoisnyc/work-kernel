// Tests for Step A: visibility kept in scopes.policy_scope_id and scope_access,
// and a scope node per container, so narrowing a container changes no descendant.
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

// accessOf returns the effective readers of a scope, sorted: the
// scope_access rows of the scope its policy_scope_id points to. (Step A
// pointer design: an inheriting scope holds no rows of its own.)
func accessOf(t *testing.T, scope uuid.UUID) []string {
	t.Helper()
	rows, err := pool.Query(testCtx(t),
		`select a.principal from scopes s join scope_access a on a.scope_id = s.policy_scope_id
		 where s.id = $1 order by a.principal`, scope)
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

// isOpen reports whether everyone in the namespace reads the scope: nothing
// at or above it narrows, so its policy_scope_id is null.
func isOpen(t *testing.T, scope uuid.UUID) bool {
	t.Helper()
	var open bool
	if err := pool.QueryRow(testCtx(t), `select policy_scope_id is null from scopes where id = $1`, scope).Scan(&open); err != nil {
		t.Fatalf("read scopes.policy_scope_id: %v", err)
	}
	return open
}

// expectedPointers computes, from parent_id and narrows alone, the nearest
// narrowing scope at or above each scope (uuid.Nil when none).
func expectedPointers(t *testing.T) map[uuid.UUID]uuid.UUID {
	t.Helper()
	parent := map[uuid.UUID]*uuid.UUID{}
	narrows := map[uuid.UUID]bool{}
	rows, err := pool.Query(testCtx(t), `select id, parent_id, narrows from scopes where namespace_id = 'default'`)
	if err != nil {
		t.Fatalf("read scopes: %v", err)
	}
	for rows.Next() {
		var (
			id uuid.UUID
			p  *uuid.UUID
			n  bool
		)
		if err := rows.Scan(&id, &p, &n); err != nil {
			t.Fatalf("scan: %v", err)
		}
		parent[id], narrows[id] = p, n
	}
	rows.Close()
	out := map[uuid.UUID]uuid.UUID{}
	for id := range parent {
		out[id] = uuid.Nil
		for cur := &id; cur != nil; cur = parent[*cur] {
			if narrows[*cur] {
				out[id] = *cur
				break
			}
		}
	}
	return out
}

func sorted(ps ...string) []string {
	out := slices.Clone(ps)
	slices.Sort(out)
	return out
}

// expectedAccess recomputes, from scopes and scope_readers alone, what
// the effective access and openness should be for every scope in the namespace:
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
	// Load the stored form in two queries: every pointer, and every
	// scope_access row.
	stored := map[uuid.UUID]uuid.UUID{} // uuid.Nil = no pointer
	rows, err := pool.Query(testCtx(t), `select id, policy_scope_id from scopes where namespace_id = 'default'`)
	if err != nil {
		t.Fatalf("read pointers: %v", err)
	}
	for rows.Next() {
		var (
			id  uuid.UUID
			ptr *uuid.UUID
		)
		if err := rows.Scan(&id, &ptr); err != nil {
			t.Fatalf("scan: %v", err)
		}
		stored[id] = uuid.Nil
		if ptr != nil {
			stored[id] = *ptr
		}
	}
	rows.Close()
	rowsOf := map[uuid.UUID][]string{}
	rows, err = pool.Query(testCtx(t), `select scope_id, principal from scope_access order by principal`)
	if err != nil {
		t.Fatalf("read scope_access: %v", err)
	}
	for rows.Next() {
		var (
			id uuid.UUID
			p  string
		)
		if err := rows.Scan(&id, &p); err != nil {
			t.Fatalf("scan: %v", err)
		}
		rowsOf[id] = append(rowsOf[id], p)
	}
	rows.Close()

	// The stored form: each pointer is the nearest narrowing scope, and only
	// narrowing scopes hold scope_access rows.
	for id, want := range expectedPointers(t) {
		if stored[id] != want {
			t.Errorf("scope %s: policy_scope_id = %v, want %v (uuid.Nil = none)", id, stored[id], want)
		}
	}
	if n := count(t, `select count(*) from scope_access a join scopes s on s.id = a.scope_id where not s.narrows`); n != 0 {
		t.Errorf("%d scope_access rows belong to scopes that do not narrow", n)
	}
	// The effective answer: open, or exactly the intersected readers.
	open, access := expectedAccess(t)
	for id, wantOpen := range open {
		ptr := stored[id]
		if got := ptr == uuid.Nil; got != wantOpen {
			t.Errorf("scope %s: open = %v, want %v", id, got, wantOpen)
		}
		var got []string
		if ptr != uuid.Nil {
			got = rowsOf[ptr]
		}
		if len(got) == 0 && len(access[id]) == 0 {
			continue
		}
		if !slices.Equal(got, access[id]) {
			t.Errorf("scope %s: effective readers = %v, want %v", id, got, access[id])
		}
	}
}

// --- the schema ---

func TestSchema_ScopesOpenAndScopeAccess(t *testing.T) {
	// Step A pointer design: scopes.policy_scope_id replaces scopes.open.
	n := count(t, `select count(*) from information_schema.columns
	               where table_name = 'scopes' and column_name = 'policy_scope_id' and data_type = 'uuid'`)
	if n != 1 {
		t.Error("scopes.policy_scope_id (uuid) does not exist")
	}
	if n := count(t, `select count(*) from information_schema.columns where table_name = 'scopes' and column_name = 'open'`); n != 0 {
		t.Error("scopes.open still exists")
	}
	n = count(t, `select count(*) from information_schema.table_constraints
	              where table_name = 'scope_access' and constraint_type = 'PRIMARY KEY'`)
	if n != 1 {
		t.Error("table scope_access with a primary key does not exist")
	}
	// Each namespace's root scope is open and has no access rows. Other tests
	// make namespaces besides "default" (Step 0, part 1), so count per namespace.
	root := count(t, `select count(*) from scopes where parent_id is null and policy_scope_id is null
	                  and namespace_id = 'default'`)
	if root != 1 {
		t.Errorf("open root scopes in default = %d, want 1", root)
	}
	if closed := count(t, `select count(*) from scopes where parent_id is null and policy_scope_id is not null`); closed != 0 {
		t.Errorf("root scopes that are not open = %d, want 0", closed)
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

// Eight first children created at the same moment still give the container
// exactly one node, and all eight share it. The node is unique per container
// and the path that creates it locks the container row FOR UPDATE.
func TestContainer_ConcurrentFirstChildren_OneNode(t *testing.T) {
	k := itemservice.New(pool)
	alice := person("alice")
	space := createAs(t, k, alice, "space", nil, nil)
	scopesBefore := count(t, `select count(*) from scopes`)

	const children = 8
	ids := make([]uuid.UUID, children)
	errs := make(chan error, children)
	start := make(chan struct{}) // closed to release every goroutine at once
	var wg sync.WaitGroup
	for i := range children {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			res, err := k.Create(testCtx(t), itemservice.CreateRequest{
				IdempotencyKey: newKey(), Principal: alice, TypeID: "ticket", TypeVersion: 1,
				ContainerID: &space.ItemID, Fields: map[string]any{"f_title": fmt.Sprintf("child %d", i)},
			})
			ids[i] = res.ItemID
			if err != nil {
				errs <- err
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("Create child: %v", err)
	}

	// scopes.item_id names the Item a node belongs to, unique per Item.
	n := count(t, `select count(*) from pg_indexes where tablename = 'scopes'
	               and indexdef like 'CREATE UNIQUE INDEX%(item_id)%'`)
	if n != 1 {
		t.Errorf("unique indexes on scopes (item_id) = %d, want 1", n)
	}
	if n := count(t, `select count(*) from scopes where item_id = $1`, space.ItemID); n != 1 {
		t.Fatalf("scope nodes belonging to the container = %d, want 1", n)
	}
	if added := count(t, `select count(*) from scopes`) - scopesBefore; added != 1 {
		t.Errorf("scopes added by 8 first children = %d, want 1", added)
	}
	node := scopeOf(t, space.ItemID)
	var owner uuid.UUID
	if err := pool.QueryRow(testCtx(t), `select item_id from scopes where id = $1`, node).Scan(&owner); err != nil || owner != space.ItemID {
		t.Fatalf("the container's scope belongs to %s (err %v), want the container", owner, err)
	}
	for i, id := range ids {
		if id != uuid.Nil && scopeOf(t, id) != node {
			t.Errorf("child %d is not in the container's node", i)
		}
	}
	assertNothingWritten(t, space.ItemID, 1)
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

// --- what narrowing writes (the pointer design) ---

// Narrowing a container with N inheriting descendant scopes to R readers
// writes R scope_access rows, not (N + 1) × R: the descendants only point at
// the container's scope. A reader change rewrites the same R rows. (With a
// copy on every scope, N = 1,000 and R = 50 wrote 50,050 rows.)
func TestScopeAccess_NarrowingWritesRowsOnlyForTheNarrowingScope(t *testing.T) {
	k := itemservice.New(pool)
	owner := person("owner")
	space := createAs(t, k, owner, "space", nil, nil)
	const n, r = 40, 5
	for i := range n {
		child := createAs(t, k, owner, fmt.Sprintf("child %d", i), nil, &space.ItemID)
		createAs(t, k, owner, "grandchild", nil, &child.ItemID) // gives the child a node
	}
	names := []string{owner.Actor}
	for i := 1; i < r; i++ {
		names = append(names, person(fmt.Sprintf("reader%d", i)).Actor)
	}
	subtreeRows := func() int {
		return count(t, `with recursive t as (
		                     select scope_id as id from items where id = $1
		                     union all
		                     select s.id from scopes s join t on s.parent_id = t.id)
		                 select count(*) from scope_access where scope_id in (select id from t)`, space.ItemID)
	}

	setPolicy(t, k, owner, space.ItemID, itemservice.PolicyPatch{Readers: names})
	if got := subtreeRows(); got != r {
		t.Errorf("rows after narrowing = %d, want %d (only the container's scope)", got, r)
	}
	setPolicy(t, k, owner, space.ItemID, itemservice.PolicyPatch{Readers: names[:2]})
	if got := subtreeRows(); got != 2 {
		t.Errorf("rows after a reader change = %d, want 2", got)
	}
	// Every inheriting scope below points at the container's node.
	node := scopeOf(t, space.ItemID)
	n2 := count(t, `select count(*) from scopes where parent_id = $1 and policy_scope_id = $1`, node)
	if n2 != n {
		t.Errorf("child scopes pointing at the container = %d, want %d", n2, n)
	}
	assertAccessMatchesRecompute(t)
}

// Regression (found by the move property test): a recompute whose subtree
// root is an inheriting scope must keep the rows of a narrowing scope below
// it. writeAccess once looked up what such a scope inherits only among the
// scopes it had just computed, found nothing for the narrowing scope above
// the root, and wrote the scope no rows at all.
func TestScopeAccess_NarrowingBelowAnInheritingRootKeepsItsRows(t *testing.T) {
	k := itemservice.New(pool)
	admin, bob, carol := person("admin"), person("bob"), person("carol")
	// space (narrows: admin, bob) > folder (inherits) > report (narrows: admin, bob, carol)
	space := createAs(t, k, admin, "space", &itemservice.Narrow{Readers: readers(admin, bob)}, nil)
	folder := createAs(t, k, admin, "folder", nil, &space.ItemID)
	report := createAs(t, k, admin, "report", &itemservice.Narrow{Readers: readers(admin, bob, carol)}, &folder.ItemID)
	want := sorted(admin.Actor, bob.Actor)
	if got := accessOf(t, scopeOf(t, report.ItemID)); !slices.Equal(got, want) {
		t.Fatalf("before: report readers = %v, want %v", got, want)
	}

	// Inherit on the folder: it has a child, so its node stays and its
	// subtree is recomputed from the folder, an inheriting root.
	setPolicy(t, k, admin, folder.ItemID, itemservice.PolicyPatch{Inherit: true})

	if got := accessOf(t, scopeOf(t, report.ItemID)); !slices.Equal(got, want) {
		t.Errorf("after: report readers = %v, want %v", got, want)
	}
	assertVisible(t, k, bob, report.ItemID)
	assertHidden(t, k, carol, report.ItemID)
	assertAccessMatchesRecompute(t)
}
