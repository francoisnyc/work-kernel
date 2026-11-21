// Adversarial review of Jira part 2, step 4a (docs/reviews/2026-11-21-customers.md).
// Each test states what the design promises and fails while the code does
// otherwise. The world is the step's own (custWorld, customers_test.go).
package itemservice_test

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
)

// Leak (low). design.md: "a customer sees a link only if it is listed and
// its target is visible to them". A Search link condition is not held to
// the list: kim names the unlisted link `relates` and learns which of her
// tickets relates to which, though Get hides that link from her.
func TestReview_Search_ALinkConditionOnAnUnlistedLinkName_RevealsTheLink(t *testing.T) {
	w := newCustWorld(t)
	kiosk := w.mk(t, w.lea, "kiosk", 1, nil, map[string]any{"f_name": "k"})
	t1 := w.mk(t, w.kim, "kticket", 1, &kiosk, map[string]any{"f_title": "one"})
	t2 := w.mk(t, w.kim, "kticket", 1, &kiosk, map[string]any{"f_title": "two"})
	w.link(t, w.sam, t1, "relates", t2) // relates is not in kticket's customer_fields

	if it, _ := w.k.Get(testCtx(t), w.kim, t1); len(it.Links) != 0 {
		t.Fatalf("precondition: Get shows kim %+v; the link is unlisted", it.Links)
	}
	res, err := w.k.Search(testCtx(t), itemservice.SearchRequest{Principal: w.kim, Filter: itemservice.Filter{
		TypeIDs: []string{"kticket"},
		Links:   []itemservice.LinkFilter{{LinkType: "relates", Direction: item.LinkOutgoing, ItemID: t2}},
	}})
	var inv *itemservice.InvalidError
	if err == nil {
		for _, it := range res.Items {
			if it.ID == t1 {
				t.Errorf("kim's search on the unlisted link `relates` found t1: the link she cannot see is revealed")
			}
		}
	} else if !errors.As(err, &inv) {
		t.Errorf("Search: %v", err)
	}
}

// Leak (low). links_truncated counts links a customer never sees
// (unlisted names, restricted ends): with 201 links to bugs she cannot
// see, kim gets no link and links_truncated = true, which tells her that
// her incident has more than 200 hidden links.
func TestReview_Get_LinksTruncated_CountsLinksTheCustomerCannotSee(t *testing.T) {
	w := newCustWorld(t)
	inc := w.incident(t, w.kim, w.desk)
	for range itemservice.MaxLinksShown + 1 {
		bug := w.mk(t, w.sam, "bug", 3, nil, map[string]any{"f_summary": "cause"})
		w.link(t, w.sam, inc, "caused_by", bug)
	}
	it, err := w.k.Get(testCtx(t), w.kim, inc)
	if err != nil {
		t.Fatal(err)
	}
	if len(it.Links) != 0 || it.LinksTruncated {
		t.Errorf("kim: %d links, links_truncated %v; want none and false (the links are not hers to see, nor their number)",
			len(it.Links), it.LinksTruncated)
	}
}

// Hardening. requester makes kim the requester, but a narrowing that
// leaves her out is accepted with it: an incident "for kim" that kim can
// never see, nor cancel, nor reply to. Refuse the combination (or require
// the requester among the readers), as a policy change that would hide an
// Item from the one making it is refused.
func TestReview_Requester_WithANarrowingThatHidesTheCustomer_IsAccepted(t *testing.T) {
	w := newCustWorld(t)
	res, err := w.k.Create(testCtx(t), itemservice.CreateRequest{IdempotencyKey: newKey(), Principal: w.sam, TypeID: "incident", TypeVersion: 2,
		ContainerID: &w.desk, Fields: map[string]any{"f_title": "for kim", "f_priority": "P3"}, Requester: "customer:kim",
		Narrow: &itemservice.Narrow{Readers: []string{w.sam.Actor}}})
	if err == nil {
		if !w.sees(t, w.kim, res.ItemID) {
			t.Errorf("an incident opened for kim is hidden from kim by its own narrowing; want the create refused")
		}
		return
	}
	var inv *itemservice.InvalidError
	if !errors.As(err, &inv) {
		t.Errorf("create: %v; want an InvalidError if refused", err)
	}
}

// Leak (low; the counter predates this step, a customer is its first
// outside reader). Every write answers its commit_seq, a counter shared by
// every Item and namespace. Two of kim's writes tell her how many writes
// everybody else made in between: here, that lee filed five replies.
// design.md accepts that item_seq and the etag show "that something hidden
// changed" on an Item she sees, not a count of other people's activity.
func TestReview_CommitSeq_CountsOtherCustomersWrites(t *testing.T) {
	w := newCustWorld(t)
	kims, lees := w.incident(t, w.kim, w.desk), w.incident(t, w.lee, w.desk)
	reply := func(p item.Principal, in uuid.UUID) int64 {
		t.Helper()
		res, err := w.k.Create(testCtx(t), itemservice.CreateRequest{IdempotencyKey: newKey(), Principal: p, TypeID: "reply",
			TypeVersion: 1, ContainerID: &in, Fields: map[string]any{"f_body": "x"}})
		if err != nil {
			t.Fatal(err)
		}
		return res.CommitSeq
	}
	before := reply(w.kim, kims)
	for range 5 {
		reply(w.lee, lees)
	}
	after := reply(w.kim, kims)
	if after-before-1 == 5 {
		t.Errorf("kim's commit_seq moved from %d to %d: she can count lee's five writes", before, after)
	}
}
