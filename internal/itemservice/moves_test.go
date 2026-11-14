// Tests for Step A2: moving an Item to another container is a change to its
// container part, made through Update (a Move patch). It needs the Item and
// the destination to be visible, takes locks in the usual order, recomputes
// scope pointers and writes one event. A move that widens who can see
// anything in the moved subtree must be confirmed (ConfirmWiden).
package itemservice_test

import (
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
)

// moveTo moves an Item (nil = top level) and returns the error.
func moveTo(t *testing.T, k *itemservice.Service, p item.Principal, id uuid.UUID, to *uuid.UUID, confirm bool) (itemservice.WriteResult, error) {
	t.Helper()
	return k.Update(testCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: p, ItemID: id,
		Move: &itemservice.Move{To: to, ConfirmWiden: confirm},
	})
}

func containerOf(t *testing.T, id uuid.UUID) *uuid.UUID {
	t.Helper()
	var c *uuid.UUID
	if err := pool.QueryRow(testCtx(t), `select container_id from items where id = $1`, id).Scan(&c); err != nil {
		t.Fatalf("read container: %v", err)
	}
	return c
}

// A move into a narrower container needs no confirmation. It writes one
// "update" event whose payload names the old and new containers (opaque
// IDs), and the Item now reads like its new container.
func TestMove_IntoANarrowerContainer(t *testing.T) {
	k := itemservice.New(pool)
	admin, bob := person("admin"), person("bob")
	src := createAs(t, k, admin, "source", nil, nil)
	dst := createAs(t, k, admin, "destination", &itemservice.Narrow{Readers: readers(admin)}, nil)
	it := createAs(t, k, admin, "moving", nil, &src.ItemID)
	assertVisible(t, k, bob, it.ItemID)

	res, err := moveTo(t, k, admin, it.ItemID, &dst.ItemID, false)
	if err != nil {
		t.Fatalf("move: %v", err)
	}
	if res.ItemSeq != 2 {
		t.Errorf("item_seq = %d, want 2", res.ItemSeq)
	}
	if c := containerOf(t, it.ItemID); c == nil || *c != dst.ItemID {
		t.Errorf("container = %v, want %s", c, dst.ItemID)
	}
	assertHidden(t, k, bob, it.ItemID)

	op, payload, _ := eventPayload(t, it.ItemID, 2)
	if op != "update" || payload["container_from"] != src.ItemID.String() || payload["container_to"] != dst.ItemID.String() {
		t.Errorf("event: op %q, payload %v; want update with container_from %s and container_to %s",
			op, payload, src.ItemID, dst.ItemID)
	}
	if n := count(t, `select count(*) from item_events where item_id = $1`, it.ItemID); n != 2 {
		t.Errorf("events = %d, want 2 (create and one move)", n)
	}
	assertAccessMatchesRecompute(t)
}

// A move that lets more people see the Item is refused unless confirmed,
// writes nothing when refused, and goes through when confirmed.
func TestMove_Widening_NeedsConfirmation(t *testing.T) {
	k := itemservice.New(pool)
	admin, bob := person("admin"), person("bob")
	secret := createAs(t, k, admin, "secret space", &itemservice.Narrow{Readers: readers(admin)}, nil)
	open := createAs(t, k, admin, "open space", nil, nil)
	it := createAs(t, k, admin, "moving", nil, &secret.ItemID)

	_, err := moveTo(t, k, admin, it.ItemID, &open.ItemID, false)
	if !errors.Is(err, itemservice.ErrInvalid) {
		t.Fatalf("unconfirmed widening move: error = %v, want ErrInvalid", err)
	}
	assertNothingWritten(t, it.ItemID, 1)
	assertHidden(t, k, bob, it.ItemID)

	if _, err := moveTo(t, k, admin, it.ItemID, &open.ItemID, true); err != nil {
		t.Fatalf("confirmed widening move: %v", err)
	}
	assertVisible(t, k, bob, it.ItemID)
	assertAccessMatchesRecompute(t)
}

// Moving to the top level is a move like any other: from a narrowed
// container it widens, so it needs confirmation, and the payload's
// container_to is null.
func TestMove_ToTheTopLevel(t *testing.T) {
	k := itemservice.New(pool)
	admin := person("admin")
	secret := createAs(t, k, admin, "secret space", &itemservice.Narrow{Readers: readers(admin)}, nil)
	it := createAs(t, k, admin, "moving", nil, &secret.ItemID)

	if _, err := moveTo(t, k, admin, it.ItemID, nil, false); !errors.Is(err, itemservice.ErrInvalid) {
		t.Errorf("unconfirmed move to the top level: error = %v, want ErrInvalid", err)
	}
	res, err := moveTo(t, k, admin, it.ItemID, nil, true)
	if err != nil {
		t.Fatalf("confirmed move to the top level: %v", err)
	}
	if c := containerOf(t, it.ItemID); c != nil {
		t.Errorf("container = %s, want none", *c)
	}
	_, payload, raw := eventPayload(t, it.ItemID, res.ItemSeq)
	if v, ok := payload["container_to"]; !ok || v != nil {
		t.Errorf("payload %s: want container_to null", raw)
	}
	assertAccessMatchesRecompute(t)
}

// Moving a container moves its whole subtree: its children read like their
// new surroundings, with no row of theirs changed, and widening is judged
// over the subtree.
func TestMove_AContainerMovesItsSubtree(t *testing.T) {
	k := itemservice.New(pool)
	admin, bob := person("admin"), person("bob")
	secret := createAs(t, k, admin, "secret space", &itemservice.Narrow{Readers: readers(admin)}, nil)
	folder := createAs(t, k, admin, "folder", nil, &secret.ItemID)
	child := createAs(t, k, admin, "child", nil, &folder.ItemID)
	grandchild := createAs(t, k, admin, "grandchild", nil, &child.ItemID)
	assertHidden(t, k, bob, grandchild.ItemID)

	if _, err := moveTo(t, k, admin, folder.ItemID, nil, false); !errors.Is(err, itemservice.ErrInvalid) {
		t.Fatalf("unconfirmed widening move of a container: error = %v, want ErrInvalid", err)
	}
	if _, err := moveTo(t, k, admin, folder.ItemID, nil, true); err != nil {
		t.Fatalf("move: %v", err)
	}
	assertVisible(t, k, bob, child.ItemID)
	assertVisible(t, k, bob, grandchild.ItemID)
	assertNothingWritten(t, child.ItemID, 1)
	assertNothingWritten(t, grandchild.ItemID, 1)
	assertAccessMatchesRecompute(t)
}

// A narrowed Item keeps its own readers when it moves; in a narrower
// container it reads like the intersection.
func TestMove_ANarrowedItemKeepsItsReaders(t *testing.T) {
	k := itemservice.New(pool)
	admin, bob, carol := person("admin"), person("bob"), person("carol")
	it := createAs(t, k, admin, "narrowed", &itemservice.Narrow{Readers: readers(admin, bob)}, nil)
	dst := createAs(t, k, admin, "for admin and carol", &itemservice.Narrow{Readers: readers(admin, carol)}, nil)

	if _, err := moveTo(t, k, admin, it.ItemID, &dst.ItemID, false); err != nil {
		t.Fatalf("move: %v", err)
	}
	assertVisible(t, k, admin, it.ItemID)
	assertHidden(t, k, bob, it.ItemID)   // not a reader of the destination
	assertHidden(t, k, carol, it.ItemID) // not a reader of the Item
	assertAccessMatchesRecompute(t)
}

// A destination the principal cannot see does not exist for them; an Item
// they cannot see does not either. Nothing is written.
func TestMove_HiddenItemOrDestination_IsNotFound(t *testing.T) {
	k := itemservice.New(pool)
	alice, bob := person("alice"), person("bob")
	hidden := createAs(t, k, alice, "alice only", &itemservice.Narrow{Readers: readers(alice)}, nil)
	bobs := createAs(t, k, bob, "bob's", nil, nil)

	if _, err := moveTo(t, k, bob, bobs.ItemID, &hidden.ItemID, true); !errors.Is(err, itemservice.ErrNotFound) {
		t.Errorf("move into a hidden container: error = %v, want ErrNotFound", err)
	}
	if _, err := moveTo(t, k, bob, hidden.ItemID, nil, true); !errors.Is(err, itemservice.ErrNotFound) {
		t.Errorf("move of a hidden Item: error = %v, want ErrNotFound", err)
	}
	assertNothingWritten(t, bobs.ItemID, 1)
	assertNothingWritten(t, hidden.ItemID, 1)
}

// An Item cannot move into itself or anything below it.
func TestMove_IntoItselfOrADescendant_IsInvalid(t *testing.T) {
	k := itemservice.New(pool)
	admin := person("admin")
	top := createAs(t, k, admin, "top", nil, nil)
	mid := createAs(t, k, admin, "mid", nil, &top.ItemID)
	low := createAs(t, k, admin, "low", nil, &mid.ItemID)

	for name, to := range map[string]uuid.UUID{"itself": top.ItemID, "a child": mid.ItemID, "a grandchild": low.ItemID} {
		if _, err := moveTo(t, k, admin, top.ItemID, &to, true); !errors.Is(err, itemservice.ErrInvalid) {
			t.Errorf("move into %s: error = %v, want ErrInvalid", name, err)
		}
	}
	assertNothingWritten(t, top.ItemID, 1)
}

// A move and a field change in one Update are one write: one event.
func TestMove_WithFields_IsOneWrite(t *testing.T) {
	k := itemservice.New(pool)
	admin := person("admin")
	dst := createAs(t, k, admin, "destination", nil, nil)
	it := createAs(t, k, admin, "moving", nil, nil)

	res, err := k.Update(testCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: admin, ItemID: it.ItemID,
		Fields: map[string]any{"f_title": "moved"},
		Move:   &itemservice.Move{To: &dst.ItemID},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if res.ItemSeq != 2 {
		t.Errorf("item_seq = %d, want 2", res.ItemSeq)
	}
	_, payload, _ := eventPayload(t, it.ItemID, 2)
	if b, _ := json.Marshal(payload["field_ids"]); string(b) != `["f_title"]` || payload["container_to"] != dst.ItemID.String() {
		t.Errorf("payload = %v, want field_ids [f_title] and container_to %s", payload, dst.ItemID)
	}
}

// Two Items moved into each other at the same moment: never a deadlock and
// never a cycle. One move wins; the other finds it would move an Item into
// its own descendant and is ErrInvalid. Repeated, to catch the races.
func TestMove_CrossingMoves_NoDeadlockNoCycle(t *testing.T) {
	k := itemservice.New(pool)
	admin := person("admin")
	for round := range 10 {
		a := createAs(t, k, admin, "a", nil, nil)
		b := createAs(t, k, admin, "b", nil, nil)
		createAs(t, k, admin, "child of a", nil, &a.ItemID) // both are containers with nodes
		createAs(t, k, admin, "child of b", nil, &b.ItemID)

		errs := make([]error, 2)
		var wg sync.WaitGroup
		for i, pair := range [][2]uuid.UUID{{a.ItemID, b.ItemID}, {b.ItemID, a.ItemID}} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, errs[i] = moveTo(t, k, admin, pair[0], &pair[1], true)
			}()
		}
		wg.Wait()

		ok := 0
		for _, err := range errs {
			switch {
			case err == nil:
				ok++
			case errors.Is(err, itemservice.ErrInvalid):
			default:
				t.Fatalf("round %d: crossing move: %v", round, err)
			}
		}
		if ok != 1 {
			t.Fatalf("round %d: %d moves succeeded, want exactly 1 (errors %v)", round, ok, errs)
		}
		// No cycle: walking up from either never comes back to it.
		for _, start := range []uuid.UUID{a.ItemID, b.ItemID} {
			seen := map[uuid.UUID]bool{}
			for cur := containerOf(t, start); cur != nil; cur = containerOf(t, *cur) {
				if *cur == start || seen[*cur] {
					t.Fatalf("round %d: container cycle through %s", round, start)
				}
				seen[*cur] = true
			}
		}
	}
	assertAccessMatchesRecompute(t)
}
