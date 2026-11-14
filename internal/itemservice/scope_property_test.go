// Property test for Step A: random trees of Items, narrowings, reader changes,
// inherits and concurrent edits. After every step the stored visibility
// (policy_scope_id and scope_access) must equal a naive recursive evaluator
// working from parent_id, narrows and scope_readers alone, and every Item must
// sit in the right scope. At the end, Get must agree with the evaluator for
// every principal and every Item.
//
// Seeds are fixed, so a failure always replays the same way.
package itemservice_test

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
)

const (
	propertySeeds = 3
	propertySteps = 30
)

func TestProperty_ScopeAccessMatchesNaiveEvaluator(t *testing.T) {
	for seed := range propertySeeds {
		t.Run(fmt.Sprintf("seed %d", seed), func(t *testing.T) {
			runScopeProperty(t, uint64(seed))
		})
	}
}

// scopeWorld is one run's Items and principals. The admin is a reader of
// everything it narrows, so it can always see and change every Item.
type scopeWorld struct {
	t      *testing.T
	k      *itemservice.Service
	r      *rand.Rand
	admin  item.Principal
	others []item.Principal
	items  []uuid.UUID
}

func runScopeProperty(t *testing.T, seed uint64) {
	w := &scopeWorld{
		t: t, k: itemservice.New(pool), r: rand.New(rand.NewPCG(seed, 7)),
		admin: person("admin"),
	}
	for i := range 4 {
		w.others = append(w.others, person(fmt.Sprintf("u%d", i)))
	}
	w.create() // something to start from

	for step := range propertySteps {
		var what string
		switch n := w.r.IntN(100); {
		case n < 35:
			what = "create"
			w.create()
		case n < 60:
			what = "narrow or change readers"
			w.apply(w.randomItem(), w.patch(false))
		case n < 75:
			what = "inherit"
			w.apply(w.randomItem(), w.patch(true))
		default:
			what = "4 concurrent policy changes"
			w.concurrent(4)
		}
		assertAccessMatchesRecompute(t)
		w.assertPlacement()
		if t.Failed() {
			t.Fatalf("seed %d, step %d (%s): stored visibility differs from the evaluator", seed, step, what)
		}
	}
	w.assertGetAgrees()
}

func (w *scopeWorld) randomItem() uuid.UUID { return w.items[w.r.IntN(len(w.items))] }

// readers picks a random reader set that always includes the admin.
func (w *scopeWorld) readers() []string {
	out := []string{w.admin.Actor}
	for _, p := range w.others {
		if w.r.IntN(2) == 0 {
			out = append(out, p.Actor)
		}
	}
	return out
}

func (w *scopeWorld) patch(inherit bool) itemservice.PolicyPatch {
	if inherit {
		return itemservice.PolicyPatch{Inherit: true}
	}
	return itemservice.PolicyPatch{Readers: w.readers()}
}

// create adds an Item: in no container a third of the time, else in a random
// existing Item; narrowed a third of the time.
func (w *scopeWorld) create() {
	w.t.Helper()
	req := itemservice.CreateRequest{
		IdempotencyKey: newKey(), Principal: w.admin, TypeID: "ticket", TypeVersion: 1,
		Fields: map[string]any{"f_title": "p"},
	}
	if len(w.items) > 0 && w.r.IntN(3) != 0 {
		c := w.randomItem()
		req.ContainerID = &c
	}
	if w.r.IntN(3) == 0 {
		req.Narrow = &itemservice.Narrow{Readers: w.readers()}
	}
	res, err := w.k.Create(testCtx(w.t), req)
	if err != nil {
		w.t.Fatalf("Create: %v", err)
	}
	w.items = append(w.items, res.ItemID)
}

func (w *scopeWorld) apply(id uuid.UUID, p itemservice.PolicyPatch) {
	w.t.Helper()
	if _, err := w.k.Update(testCtx(w.t), itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: w.admin, ItemID: id, Policy: &p,
	}); err != nil {
		w.t.Fatalf("Update policy: %v", err)
	}
}

// concurrent runs n random policy changes at once. The random choices are
// made up front, in this goroutine, so the run stays reproducible. Every
// change must succeed: no deadlock, no lost update.
func (w *scopeWorld) concurrent(n int) {
	w.t.Helper()
	type change struct {
		id    uuid.UUID
		patch itemservice.PolicyPatch
	}
	changes := make([]change, n)
	for i := range changes {
		changes[i] = change{w.randomItem(), w.patch(w.r.IntN(4) == 0)}
	}
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for _, c := range changes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := w.k.Update(testCtx(w.t), itemservice.UpdateRequest{
				IdempotencyKey: newKey(), Principal: w.admin, ItemID: c.id, Policy: &c.patch,
			})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			w.t.Fatalf("concurrent Update policy: %v", err)
		}
	}
}

// assertPlacement checks each Item's scope: its own node if it has one, else
// its container's node, else the root.
func (w *scopeWorld) assertPlacement() {
	w.t.Helper()
	for _, id := range w.items {
		var (
			scope     uuid.UUID
			own       *uuid.UUID
			container *uuid.UUID
		)
		err := pool.QueryRow(testCtx(w.t),
			`select i.scope_id, (select id from scopes where item_id = i.id), i.container_id
			 from items i where i.id = $1`, id).Scan(&scope, &own, &container)
		if err != nil {
			w.t.Fatalf("read item: %v", err)
		}
		var want uuid.UUID
		switch {
		case own != nil:
			want = *own
		case container != nil:
			if err := pool.QueryRow(testCtx(w.t),
				`select id from scopes where item_id = $1`, *container).Scan(&want); err != nil {
				w.t.Errorf("item %s: its container %s has no node of its own (%v)", id, *container, err)
				continue
			}
		default:
			if err := pool.QueryRow(testCtx(w.t),
				`select id from scopes where namespace_id = 'default' and parent_id is null`).Scan(&want); err != nil {
				w.t.Fatalf("read root: %v", err)
			}
		}
		if scope != want {
			w.t.Errorf("item %s: scope %s, want %s", id, scope, want)
		}
	}
}

// assertGetAgrees asks Get, for every principal and every Item, and compares
// with the evaluator: visible when the Item's scope is open, or the principal
// is among its effective readers.
func (w *scopeWorld) assertGetAgrees() {
	w.t.Helper()
	open, access := expectedAccess(w.t)
	for _, id := range w.items {
		scope := scopeOf(w.t, id)
		for _, p := range append([]item.Principal{w.admin}, w.others...) {
			want := open[scope] || slices.Contains(access[scope], p.Actor)
			_, err := w.k.Get(testCtx(w.t), p, id)
			got := err == nil
			if err != nil && !errors.Is(err, itemservice.ErrNotFound) {
				w.t.Fatalf("Get: %v", err)
			}
			if got != want {
				w.t.Errorf("Get(%s, %s): visible = %v, evaluator says %v", p.Actor, id, got, want)
			}
		}
	}
}
