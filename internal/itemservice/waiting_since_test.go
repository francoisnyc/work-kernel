// Tests for "oldest waiting first" (docs/roadmap.md, track 1, item 5, step
// 3a): order_by waiting_since on the waiting_on view. The owner's rule:
//
//   - waiting_since is no field: it orders the view by when each Item
//     entered the state it is in (items.state_entered_at), oldest first;
//     with desc, newest first.
//   - It is accepted only together with waiting_on (me, or role:<name>);
//     without it the request is malformed at order_by.
//   - The default order does not change: newest created first.
//   - A release puts the Item back in the queue as of now: it goes to the
//     back. (Since it first entered the queue: Later.)
//   - Paging is by (the time, the Item's ID): two Items that entered their
//     state at the same instant come in ID order, each once.
//   - The view is step 1's: never an Item its reader cannot see, taken out
//     before the page is cut, and every Item as its reader sees it.
package itemservice_test

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
)

var (
	oldestWaiting = &itemservice.OrderBy{Field: "waiting_since"}
	newestWaiting = &itemservice.OrderBy{Field: "waiting_since", Desc: true}
)

// viewInOrder reads the whole view, limit Items a page, and returns the
// Items in the order the pages gave them. An Item seen twice fails.
func (w *waitWorld) viewInOrder(t *testing.T, p item.Principal, f itemservice.Filter, order *itemservice.OrderBy, limit int) []uuid.UUID {
	t.Helper()
	req := itemservice.SearchRequest{Principal: p, Filter: f, OrderBy: order, Limit: limit}
	var ids []uuid.UUID
	for page := 0; ; page++ {
		res, err := w.k.Search(testCtx(t), req)
		if err != nil {
			t.Fatalf("page %d of %s's view (%+v, order %+v): %v", page, p.Actor, f, order, err)
		}
		if limit > 0 && len(res.Items) > limit {
			t.Fatalf("page %d holds %d Items, want at most %d", page, len(res.Items), limit)
		}
		for _, it := range res.Items {
			if slices.Contains(ids, it.ID) {
				t.Fatalf("page %d repeats %s", page, it.ID)
			}
			ids = append(ids, it.ID)
		}
		if req.PageToken = res.NextPageToken; req.PageToken == "" {
			return ids
		}
		if page > 100 {
			t.Fatal("more than 100 pages")
		}
	}
}

// names turns IDs into the test's names for them, for a readable failure.
func named(ids []uuid.UUID, names map[uuid.UUID]string) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		if out[i] = names[id]; out[i] == "" {
			out[i] = id.String()
		}
	}
	return out
}

// assertOrderInvalid: the request is malformed at order_by.
func assertOrderInvalid(t *testing.T, what string, err error) {
	t.Helper()
	var inv *itemservice.InvalidError
	if !errors.Is(err, itemservice.ErrInvalid) || !errors.As(err, &inv) || inv.Path != "order_by" {
		t.Errorf("%s: error = %v, want an *InvalidError (ErrInvalid) at order_by", what, err)
	}
}

// threeQueued makes three purchases p1, p2, p3 in that order, and sends
// them to the finance queue in another: p2, then p3, then p1. So newest
// created first is p3, p2, p1, and oldest waiting first is p2, p3, p1.
func (w claimWorld) threeQueued(t *testing.T) (p1, p2, p3 uuid.UUID, names map[uuid.UUID]string) {
	t.Helper()
	var ids []uuid.UUID
	for range 3 {
		id := w.create(t, itemservice.CreateRequest{
			Principal: w.ana, TypeID: "cl_purchase", TypeVersion: 1,
			Fields: map[string]any{"f_amount": 2500, "f_manager": w.bob.Actor},
		})
		w.move(t, w.ana, id, "manager_review", nil)
		ids = append(ids, id)
	}
	p1, p2, p3 = ids[0], ids[1], ids[2]
	for _, id := range []uuid.UUID{p2, p3, p1} {
		w.move(t, w.bob, id, "finance_queue", nil)
	}
	return p1, p2, p3, map[uuid.UUID]string{p1: "p1", p2: "p2", p3: "p3"}
}

// Oldest waiting first, and newest with desc: for the caller (me) and for
// a role's queue; whatever the page size; and the default order as before.
func TestWaitingSince_OldestWaitingFirst(t *testing.T) {
	w := newClaimWorld(t)
	p1, p2, p3, names := w.threeQueued(t)
	me, queue := itemservice.Filter{WaitingOn: "me"}, itemservice.Filter{WaitingOn: "role:cl_finance"}

	for _, c := range []struct {
		what  string
		who   item.Principal
		f     itemservice.Filter
		order *itemservice.OrderBy
		want  []uuid.UUID
	}{
		{"fay, oldest waiting first", w.fay, me, oldestWaiting, []uuid.UUID{p2, p3, p1}},
		{"fay, newest waiting first (desc)", w.fay, me, newestWaiting, []uuid.UUID{p1, p3, p2}},
		{"the finance queue asked by olaf, oldest waiting first", w.olaf, queue, oldestWaiting, []uuid.UUID{p2, p3, p1}},
		{"the finance queue asked by olaf, newest waiting first", w.olaf, queue, newestWaiting, []uuid.UUID{p1, p3, p2}},
		{"fay, no order_by: newest created first, as before", w.fay, me, nil, []uuid.UUID{p3, p2, p1}},
		{"the finance queue, no order_by: newest created first, as before", w.olaf, queue, nil, []uuid.UUID{p3, p2, p1}},
	} {
		for _, limit := range []int{0, 1, 2} { // 0: the default page size
			if got := w.viewInOrder(t, c.who, c.f, c.order, limit); !slices.Equal(got, c.want) {
				t.Errorf("%s, %d a page = %v, want %v", c.what, limit, named(got, names), named(c.want, names))
			}
		}
	}

	// With the other filters: the Type, and a state.
	f := itemservice.Filter{WaitingOn: "me", TypeIDs: []string{"cl_purchase"}, States: []string{"finance_queue"}}
	if got := w.viewInOrder(t, w.fay, f, oldestWaiting, 2); !slices.Equal(got, []uuid.UUID{p2, p3, p1}) {
		t.Errorf("fay's cl_purchases in the queue, oldest waiting first = %v, want p2, p3, p1", named(got, names))
	}

	// List takes it too: the top level's Items that wait on fay.
	listed, err := w.k.List(testCtx(t), itemservice.ListRequest{Principal: w.fay, Filter: me, OrderBy: oldestWaiting})
	if err != nil {
		t.Fatalf("List the top level waiting on fay, oldest first: %v", err)
	}
	var got []uuid.UUID
	for _, it := range listed.Items {
		got = append(got, it.ID)
	}
	if !slices.Equal(got, []uuid.UUID{p2, p3, p1}) {
		t.Errorf("List, oldest waiting first = %v, want p2, p3, p1", named(got, names))
	}
}

// waiting_since orders the waiting_on view and nothing else: without
// waiting_on the request is malformed at order_by, on Search and on List,
// whatever the other filters. Nothing is a field called waiting_since.
func TestWaitingSince_OnlyWithWaitingOn(t *testing.T) {
	w := newClaimWorld(t)
	w.threeQueued(t)
	for name, f := range map[string]itemservice.Filter{
		"no filter":     {},
		"a Type filter": {TypeIDs: []string{"cl_purchase"}},
		"a state":       {States: []string{"finance_queue"}},
	} {
		for _, order := range []*itemservice.OrderBy{oldestWaiting, newestWaiting} {
			// (A Search with neither words nor a filter is refused before
			// its order is read: that case is List's.)
			if name != "no filter" {
				_, err := w.k.Search(testCtx(t), itemservice.SearchRequest{Principal: w.fay, Filter: f, OrderBy: order})
				assertOrderInvalid(t, "Search by waiting_since with "+name, err)
			}
			_, err := w.k.List(testCtx(t), itemservice.ListRequest{Principal: w.fay, Filter: f, OrderBy: order})
			assertOrderInvalid(t, "List by waiting_since with "+name, err)
		}
	}
	// With waiting_on it is accepted even when the view is empty.
	res, err := w.k.Search(testCtx(t), itemservice.SearchRequest{
		Principal: w.olaf, Filter: itemservice.Filter{WaitingOn: "me"}, OrderBy: oldestWaiting,
	})
	if err != nil || len(res.Items) != 0 {
		t.Errorf("olaf, whom nothing names, oldest waiting first: %d Items, error %v; want an empty view", len(res.Items), err)
	}
	// A customer has no waiting_on view, with or without the order.
	kim := item.Principal{Actor: "customer:kim", Namespace: w.ns}
	_, err = w.k.Search(testCtx(t), itemservice.SearchRequest{
		Principal: kim, Filter: itemservice.Filter{WaitingOn: "me"}, OrderBy: oldestWaiting,
	})
	assertWaitingOnInvalid(t, "a customer asks for her view, oldest waiting first", err)
}

// A release goes to the back of the queue: the Item waits on the team as
// of its release. And a claimed Item is in its claimant's view as of the
// claim.
func TestWaitingSince_AReleaseGoesToTheBack(t *testing.T) {
	w := newClaimWorld(t)
	_, p2, p3, names := w.threeQueued(t)
	p1 := func() uuid.UUID {
		for id, n := range names {
			if n == "p1" {
				return id
			}
		}
		return uuid.Nil
	}()
	queue := itemservice.Filter{WaitingOn: "role:cl_finance"}
	if got := w.viewInOrder(t, w.olaf, queue, oldestWaiting, 0); !slices.Equal(got, []uuid.UUID{p2, p3, p1}) {
		t.Fatalf("the queue before any claim = %v, want p2, p3, p1", named(got, names))
	}

	// gus claims the oldest, p2, then puts it back.
	w.move(t, w.gus, p2, "finance_review", nil)
	if got := w.viewInOrder(t, w.olaf, queue, oldestWaiting, 0); !slices.Equal(got, []uuid.UUID{p3, p1}) {
		t.Errorf("the queue while gus holds p2 = %v, want p3, p1", named(got, names))
	}
	if got := w.viewInOrder(t, w.gus, itemservice.Filter{WaitingOn: "me"}, oldestWaiting, 0); !slices.Equal(got, []uuid.UUID{p3, p1, p2}) {
		t.Errorf("gus's own view, oldest waiting first = %v, want p3, p1, then p2, which he has just claimed", named(got, names))
	}
	w.move(t, w.gus, p2, "finance_queue", nil)
	if got := w.viewInOrder(t, w.olaf, queue, oldestWaiting, 0); !slices.Equal(got, []uuid.UUID{p3, p1, p2}) {
		t.Errorf("the queue after gus released p2 = %v, want p3, p1, p2: a released Item goes to the back", named(got, names))
	}
	if got := w.viewInOrder(t, w.olaf, queue, newestWaiting, 0); !slices.Equal(got, []uuid.UUID{p2, p1, p3}) {
		t.Errorf("the queue, newest waiting first, after the release = %v, want p2, p1, p3", named(got, names))
	}
}

// Two Items that entered their state at the same instant: they come in ID
// order, in both directions (as order_by a field breaks its ties), each
// exactly once whatever the page size. The instant is a fixed clock's
// (Options.Clock, the stories' simulated time).
func TestWaitingSince_AStablePageWhenTimesTie(t *testing.T) {
	w := newClaimWorld(t)
	at := time.Date(2027, 3, 1, 9, 0, 0, 0, time.UTC)
	clock := at
	frozen := itemservice.NewWithOptions(pool, itemservice.Options{Clock: func() time.Time { return clock }})

	// Five purchases enter the queue at 09:00 exactly, one an hour before
	// and one an hour after.
	enter := func() uuid.UUID {
		id := w.create(t, itemservice.CreateRequest{
			Principal: w.ana, TypeID: "cl_purchase", TypeVersion: 1,
			Fields: map[string]any{"f_amount": 2500, "f_manager": w.bob.Actor},
		})
		w.move(t, w.ana, id, "manager_review", nil)
		if _, err := frozen.Transition(testCtx(t), itemservice.TransitionRequest{
			IdempotencyKey: newKey(), Principal: w.bob, ItemID: id, ToState: "finance_queue",
		}); err != nil {
			t.Fatalf("bob sends it to the queue at %s: %v", clock, err)
		}
		if got := stateEnteredAt(t, id); !got.Equal(clock) {
			t.Fatalf("a purchase sent to the queue at %s has state_entered_at %s", clock, got)
		}
		return id
	}
	var tied []uuid.UUID
	for range 5 {
		tied = append(tied, enter())
	}
	clock = at.Add(-time.Hour)
	early := enter()
	clock = at.Add(time.Hour)
	late := enter()
	slices.SortFunc(tied, func(a, b uuid.UUID) int { return strings.Compare(a.String(), b.String()) })

	asc := append(append([]uuid.UUID{early}, tied...), late)
	desc := append(append([]uuid.UUID{late}, tied...), early)
	me := itemservice.Filter{WaitingOn: "me"}
	for _, limit := range []int{1, 2, 3, 0} {
		if got := w.viewInOrder(t, w.fay, me, oldestWaiting, limit); !slices.Equal(got, asc) {
			t.Errorf("oldest waiting first, %d a page = %v, want the early one, the five tied in ID order, the late one: %v", limit, got, asc)
		}
		if got := w.viewInOrder(t, w.fay, me, newestWaiting, limit); !slices.Equal(got, desc) {
			t.Errorf("newest waiting first, %d a page = %v, want the late one, the five tied in ID order, the early one: %v", limit, got, desc)
		}
	}
}

// A page token names a place in one order of one request: a token of
// oldest-first is refused with newest-first, with a field's order and with
// none. And a token is unsigned: one whose value is no time is refused as
// a token, never passed to the database.
func TestWaitingSince_PageTokens(t *testing.T) {
	w := newClaimWorld(t)
	w.threeQueued(t)
	req := itemservice.SearchRequest{Principal: w.fay, Filter: itemservice.Filter{WaitingOn: "me"}, OrderBy: oldestWaiting, Limit: 1}
	first, err := w.k.Search(testCtx(t), req)
	if err != nil || first.NextPageToken == "" {
		t.Fatalf("the first page of three: token %q, error %v", first.NextPageToken, err)
	}
	assertTokenRefused := func(what string, r itemservice.SearchRequest) {
		t.Helper()
		var inv *itemservice.InvalidError
		if _, err := w.k.Search(testCtx(t), r); !errors.As(err, &inv) || inv.Path != "page_token" {
			t.Errorf("%s: error = %v, want an *InvalidError at page_token", what, err)
		}
	}
	for what, order := range map[string]*itemservice.OrderBy{
		"newest waiting first": newestWaiting, "ordered by a field": {Field: "f_amount"}, "no order": nil,
	} {
		other := req
		other.OrderBy, other.PageToken = order, first.NextPageToken
		assertTokenRefused("a token of oldest waiting first, used with "+what, other)
	}

	// The same token with its value replaced by something that is no time.
	raw, err := base64.RawURLEncoding.DecodeString(first.NextPageToken)
	if err != nil {
		t.Fatalf("the token is not base64: %v", err)
	}
	var tok map[string]any
	if err := json.Unmarshal(raw, &tok); err != nil {
		t.Fatalf("the token is not JSON: %v", err)
	}
	if v, ok := tok["v"].(string); !ok || v == "" {
		t.Fatalf("the token carries no sort value (v): %s", raw)
	} else if _, err := time.Parse(time.RFC3339Nano, v); err != nil {
		t.Errorf("the token's value %q is not an RFC 3339 time: %v", v, err)
	}
	tok["v"] = "yesterday'; drop table items; --"
	forged, _ := json.Marshal(tok)
	bad := req
	bad.PageToken = base64.RawURLEncoding.EncodeToString(forged)
	assertTokenRefused("a token whose value is no time", bad)
}

// The view is step 1's, in another order: an Item its reader cannot see
// is taken out before the page is cut, and what comes back is the Item as
// its reader sees it (policy.Project): whole, for staff.
func TestWaitingSince_NeverAnItemTheCallerCannotSee(t *testing.T) {
	w := newClaimWorld(t)
	// Oldest in the queue: three purchases fay cannot read.
	var hidden []uuid.UUID
	for range 3 {
		id := w.create(t, itemservice.CreateRequest{
			Principal: w.ana, TypeID: "cl_purchase", TypeVersion: 1,
			Fields: map[string]any{"f_amount": 9000, "f_manager": w.bob.Actor},
			Narrow: &itemservice.Narrow{Readers: readers(w.ana, w.bob, w.gus)},
		})
		w.move(t, w.ana, id, "manager_review", nil)
		w.move(t, w.bob, id, "finance_queue", nil)
		hidden = append(hidden, id)
	}
	mine1 := w.queued(t, "cl_purchase")
	mine2 := w.queued(t, "cl_purchase")

	for _, f := range []itemservice.Filter{{WaitingOn: "me"}, {WaitingOn: "role:cl_finance"}} {
		res, err := w.k.Search(testCtx(t), itemservice.SearchRequest{Principal: w.fay, Filter: f, OrderBy: oldestWaiting, Limit: 2})
		if err != nil {
			t.Fatalf("fay's first page (%s): %v", f.WaitingOn, err)
		}
		var got []uuid.UUID
		for _, it := range res.Items {
			got = append(got, it.ID)
		}
		if !slices.Equal(got, []uuid.UUID{mine1, mine2}) {
			t.Errorf("fay's first page of two (%s), oldest waiting first = %v, want her two (%v, %v): "+
				"the three older ones she cannot see must not take room in the page", f.WaitingOn, got, mine1, mine2)
		}
		if len(res.Items) > 0 {
			if it := res.Items[0]; it.TypeID != "cl_purchase" || it.State != "finance_queue" || it.Fields["f_manager"] != w.bob.Actor {
				t.Errorf("the first Item = %s in %s with %v, want a cl_purchase in finance_queue, whole", it.TypeID, it.State, it.Fields)
			}
		}
	}
	// gus reads all five: the hidden three are the oldest.
	want := append(slices.Clone(hidden), mine1, mine2)
	// (gus asked for none of them, so `except: [creator]` leaves all five.)
	if got := w.viewInOrder(t, w.gus, itemservice.Filter{WaitingOn: "me"}, oldestWaiting, 2); !slices.Equal(got, want) {
		t.Errorf("gus's view, oldest waiting first = %v, want %v", got, want)
	}
}
