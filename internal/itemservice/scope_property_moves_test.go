// Property test for Step A2: the Step A property test with moves added. After
// every step the stored visibility must equal the naive evaluator. At the end,
// for every principal and every Item it can see, Get must agree with the
// evaluator, and the Item's container (and scope) must be visible to that
// principal too: Get never names a container its reader cannot see.
package itemservice_test

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"testing"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
)

func TestProperty_MovesKeepScopeAccessExact(t *testing.T) {
	for seed := range propertySeeds {
		t.Run(fmt.Sprintf("seed %d", seed), func(t *testing.T) {
			w := &scopeWorld{
				t: t, k: itemservice.New(pool), r: rand.New(rand.NewPCG(uint64(seed), 11)),
				admin: person("admin"),
			}
			for i := range 4 {
				w.others = append(w.others, person(fmt.Sprintf("u%d", i)))
			}
			w.create()
			for step := range propertySteps {
				var what string
				switch n := w.r.IntN(100); {
				case n < 30:
					what = "create"
					w.create()
				case n < 50:
					what = "narrow or change readers"
					w.apply(w.randomItem(), w.patch(false))
				case n < 60:
					what = "inherit"
					w.apply(w.randomItem(), w.patch(true))
				default:
					what = "move"
					w.move()
				}
				assertAccessMatchesRecompute(t)
				w.assertPlacement()
				if t.Failed() {
					t.Fatalf("seed %d, step %d (%s): stored visibility differs from the evaluator", seed, step, what)
				}
			}
			w.assertGetAgrees()
			w.assertContainersVisible()
		})
	}
}

// move moves a random Item to a random other Item or to the top level,
// confirmed. A move into itself or a descendant must be ErrInvalid and
// change nothing; any other move must succeed.
func (w *scopeWorld) move() {
	w.t.Helper()
	id := w.randomItem()
	var to *uuid.UUID
	if w.r.IntN(4) != 0 {
		c := w.randomItem()
		to = &c
	}
	_, err := w.k.Update(testCtx(w.t), itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: w.admin, ItemID: id,
		Move: &itemservice.Move{To: to, ConfirmWiden: true},
	})
	cycle := to != nil && w.isAtOrBelow(*to, id)
	switch {
	case cycle && errors.Is(err, itemservice.ErrInvalid):
	case cycle:
		w.t.Fatalf("move of %s into its own subtree: error = %v, want ErrInvalid", id, err)
	case err != nil:
		w.t.Fatalf("move: %v", err)
	}
}

// isAtOrBelow reports whether a is b or lies below b in the container tree.
func (w *scopeWorld) isAtOrBelow(a, b uuid.UUID) bool {
	for cur := &a; cur != nil; cur = containerOf(w.t, *cur) {
		if *cur == b {
			return true
		}
	}
	return false
}

// assertContainersVisible: whenever Get returns an Item to a principal, the
// container it names is one that principal can Get too.
func (w *scopeWorld) assertContainersVisible() {
	w.t.Helper()
	for _, id := range w.items {
		for _, p := range append([]item.Principal{w.admin}, w.others...) {
			it, err := w.k.Get(testCtx(w.t), p, id)
			if errors.Is(err, itemservice.ErrNotFound) {
				continue
			}
			if err != nil {
				w.t.Fatalf("Get: %v", err)
			}
			if it.ContainerID == nil {
				continue
			}
			if _, err := w.k.Get(testCtx(w.t), p, *it.ContainerID); err != nil {
				w.t.Errorf("Get(%s, %s) names container %s, which it cannot see (%v)", p.Actor, id, *it.ContainerID, err)
			}
		}
	}
}
