// Tests for the Jira proof of concept, part 1: a link type that may target
// an Item of any Type (targets: ['*']). Watching is user space: a
// Subscription Item links to what it watches by `watches`; there is no
// Watch call, and nothing here is named "watch" in the kernel. Everything
// else about the link is Step 1's: the caller must see both ends, hidden
// and missing are the same answer, cardinality is the database's, and an
// incoming link from an Item the reader cannot see is left out.
package itemservice_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

const subscriptionTypeYAML = `
id: TYPE
version: 1
links:
  - {name: watches, inverse: watched_by, targets: ['*'], max: one}
`

// subscribe creates a Subscription as p, readable by p alone, and returns it.
func subscribe(t *testing.T, k *itemservice.Service, typ string, p item.Principal) uuid.UUID {
	t.Helper()
	res, err := k.Create(testCtx(t), itemservice.CreateRequest{
		IdempotencyKey: newKey(), Principal: p, TypeID: typ, TypeVersion: 1,
		Narrow: &itemservice.Narrow{Readers: readers(p)},
	})
	if err != nil {
		t.Fatalf("create a subscription: %v", err)
	}
	return res.ItemID
}

func registerSubscription(t *testing.T, k *itemservice.Service) string {
	t.Helper()
	id := "sub-" + uuid.NewString()[:8]
	ty, err := typedef.Parse([]byte(strings.Replace(subscriptionTypeYAML, "TYPE", id, 1)))
	if err != nil {
		t.Fatalf("parse the subscription Type: %v", err)
	}
	if err := k.RegisterType(testCtx(t), ty); err != nil {
		t.Fatalf("register the subscription Type: %v", err)
	}
	return id
}

// A subscription watches an Item of any Type: Types its own Type never
// heard of, one with a lifecycle and one without, even another
// subscription.
func TestAnyTarget_LinksToItemsOfAnyType(t *testing.T) {
	w := newLinkWorld(t)
	sub := registerSubscription(t, w.k)
	plain, err := w.k.Create(testCtx(t), itemservice.CreateRequest{
		IdempotencyKey: newKey(), Principal: w.ana, TypeID: w.other, TypeVersion: 1,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	for name, target := range map[string]uuid.UUID{
		"an Item with a lifecycle": w.open(t),
		"an Item without one":      plain.ItemID,
		"another subscription":     subscribe(t, w.k, sub, w.ana),
	} {
		s := subscribe(t, w.k, sub, w.ana)
		res, err := w.link(t, w.ana, s, "watches", target)
		if err != nil {
			t.Errorf("watch %s: %v", name, err)
			continue
		}
		// One event on the subscription, none on what it watches.
		if res.ItemSeq != 2 {
			t.Errorf("watch %s: the subscription's item_seq = %d, want 2", name, res.ItemSeq)
		}
		got, _ := w.linksOf(t, w.ana, s)
		assertLinks(t, "the subscription watching "+name, got, out("watches", target))
	}
}

// Watching an Item you cannot see is NOT_FOUND, like a missing one, and
// nothing is written. Unlinking unwatches; the subscription stays.
func TestAnyTarget_HiddenIsNotFound_UnlinkUnwatches(t *testing.T) {
	w := newLinkWorld(t)
	sub := registerSubscription(t, w.k)
	secret := w.make(t, w.ana, "s_open", itemOpts{narrow: readers(w.ana)})
	shared := w.open(t)

	bobs := subscribe(t, w.k, sub, w.bob)
	_, hidden := w.link(t, w.bob, bobs, "watches", secret)
	_, missing := w.link(t, w.bob, bobs, "watches", uuid.New())
	for what, err := range map[string]error{"a hidden Item": hidden, "a missing Item": missing} {
		if !errors.Is(err, itemservice.ErrNotFound) {
			t.Errorf("bob watches %s: %v, want ErrNotFound", what, err)
		}
	}
	if seq := itemSeqOf(t, bobs); seq != 1 {
		t.Errorf("the subscription's item_seq = %d after two refusals, want 1", seq)
	}

	w.mustLink(t, w.bob, bobs, "watches", shared)
	// At most one: a subscription watches one Item.
	_, err := w.link(t, w.bob, bobs, "watches", w.open(t))
	assertRefusedMeta(t, "a second watched Item", err, itemservice.CodeLinkCardinality,
		map[string]string{"link_type": "watches", "side": "source"})

	if _, err := w.unlink(t, w.bob, bobs, "watches", shared); err != nil {
		t.Fatalf("unwatch: %v", err)
	}
	got, _ := w.linksOf(t, w.bob, bobs)
	assertLinks(t, "the subscription after unwatching", got)
	if _, err := w.k.Get(testCtx(t), w.bob, bobs); err != nil {
		t.Errorf("the subscription after unwatching: %v, want it still there", err)
	}
}

// Who watches an Item is shown only to those who can see the subscription:
// each watcher sees their own, nobody sees the others'. Links never change
// who can see anything.
func TestAnyTarget_WatchersAreVisibleOnlyThroughTheirSubscriptions(t *testing.T) {
	w := newLinkWorld(t)
	sub := registerSubscription(t, w.k)
	page := w.open(t)
	anas, bobs := subscribe(t, w.k, sub, w.ana), subscribe(t, w.k, sub, w.bob)
	w.mustLink(t, w.ana, anas, "watches", page)
	w.mustLink(t, w.bob, bobs, "watches", page)

	got, _ := w.linksOf(t, w.ana, page)
	assertLinks(t, "the page as ana", got, inc("watched_by", anas))
	got, _ = w.linksOf(t, w.bob, page)
	assertLinks(t, "the page as bob", got, inc("watched_by", bobs))
	assertHidden(t, w.k, w.bob, anas)

	// "What do I watch": the subscriptions linked to the page, as a search.
	mine := w.mustSearch(t, w.ana, itemservice.Filter{Links: linked("watches", item.LinkOutgoing, page)})
	assertSameIDs(t, "ana's subscriptions to the page", mine, anas)
	// "What watches this", from the page's side.
	watching := w.mustSearch(t, w.bob, itemservice.Filter{Links: linked("watched_by", item.LinkIncoming, page)})
	assertSameIDs(t, "the subscriptions bob sees on the page", watching, bobs)
}
