// Tests for the consolidation after Step 2, item 4: a reaction's write on
// the balance records causation_id <request>:<seq>, naming another Item.
// History shows it only to a reader who can see that Item; to anyone else
// it is omitted (no "restricted": an ID-shaped field should not hint).
// ListEvents is the one path that returns events; the API reads it.
package itemservice_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
)

// narrowedDeduction: ana's balance (visible to everyone), and her request
// narrowed to ana and bob, approved by bob: the deduction lands on the
// balance, caused by the request.
func narrowedDeduction(t *testing.T, w leaveWorld) (bal, req uuid.UUID) {
	t.Helper()
	bal = w.visibleBalance(t, w.ana, 25)
	res, err := w.k.Create(testCtx(t), itemservice.CreateRequest{IdempotencyKey: newKey(), Principal: w.ana,
		TypeID: "vacation_request", TypeVersion: 2, Fields: leaveTerms(5),
		Narrow: &itemservice.Narrow{Readers: []string{w.ana.Actor, w.bob.Actor}}})
	if err != nil {
		t.Fatalf("create narrowed request: %v", err)
	}
	req = res.ItemID
	if _, h := w.hrNames(t, req, w.askSubmit(t, w.ana, req), w.bob); h.Status != item.HoldAllowed {
		t.Fatalf("submit: hold %+v", h)
	}
	if _, h := w.approve(t, w.bob, req); h.Status != item.HoldAllowed {
		t.Fatalf("approve: hold %+v", h)
	}
	assertUsed(t, w, bal, 5)
	return bal, req
}

func historyJSON(t *testing.T, w leaveWorld, p item.Principal, id uuid.UUID) string {
	t.Helper()
	page, err := w.k.ListEvents(testCtx(t), itemservice.ListEventsRequest{Principal: inDefault(p), ItemID: id})
	if err != nil {
		t.Fatalf("ListEvents as %s: %v", p.Actor, err)
	}
	data, err := json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestListEvents_CausationNamingAHiddenItem_Omitted(t *testing.T) {
	w := newLeaveWorld(t)
	bal, req := narrowedDeduction(t, w)
	if _, err := w.k.Get(testCtx(t), w.dana, req); err == nil {
		t.Fatal("test bug: dana can see the narrowed request")
	}
	if got := historyJSON(t, w, w.dana, bal); strings.Contains(got, req.String()) {
		t.Errorf("dana (who cannot see the request) reads the balance's history and finds its ID:\n%s", got)
	}
	// Control: ana sees both, so the causation stays.
	if got := historyJSON(t, w, w.ana, bal); !strings.Contains(got, req.String()) {
		t.Errorf("ana (who sees the request) does not find its ID in the balance's history:\n%s", got)
	}
}
