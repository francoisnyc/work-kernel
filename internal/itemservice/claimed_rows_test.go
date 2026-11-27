// Tests for a team's claimed rows and why a move is withheld (the Inbox
// for people, PR 2a). The owner's rules (Phase A, 2026-11-27):
//
//   - Filter.Claimed, with WaitingOn "role:<name>" only, reads that role's
//     claimed rows instead of its queue: the open Items in a state with a
//     claim whose waiting moves name the role, whoever holds the claim (an
//     empty claim field counts). Without it the queue is as before: what
//     nobody has claimed. Anything else is an *InvalidError at
//     filter.claimed. Order and paging as for the queue (waiting_since is
//     when the Item was claimed); Count takes it; a page token is bound to
//     it. Visibility is unchanged: the holder is the claim field's value,
//     read like any field.
//   - Item.Withheld is filled exactly where Item.Moves is: the moves
//     `allowed` lets the caller in to that `except` or the claim then
//     refuses, each with its reason (policy.Withheld). Elsewhere nil.
//
// The world is caller_moves_test.go's: mv_case, whose state review has a
// claim (f_claimed_by) and moves for role:mv_team out of it. ana asks, bob
// owns, fay and gus are the team, gus made the box, lea leads, olaf holds
// nothing.
package itemservice_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
	"github.com/francoisnyc/work-kernel/internal/policy"
)

const mvTeam = "role:mv_team"

// claimedRows is the team's claimed rows among mv_case, as p reads them.
func (w movesWorld) claimedRows(t *testing.T, p item.Principal) []item.Item {
	t.Helper()
	res, err := w.k.Search(testCtx(t), itemservice.SearchRequest{Principal: p, Limit: 500,
		Filter: itemservice.Filter{WaitingOn: mvTeam, Claimed: true, TypeIDs: []string{"mv_case"}}})
	if err != nil {
		t.Fatalf("Search the claimed rows as %s: %v", p.Actor, err)
	}
	return res.Items
}

func findItem(items []item.Item, id uuid.UUID) (item.Item, bool) {
	i := slices.IndexFunc(items, func(it item.Item) bool { return it.ID == id })
	if i < 0 {
		return item.Item{}, false
	}
	return items[i], true
}

// The claimed rows hold the claimed cases only, with their holder; the
// queue still holds none of them.
func TestClaimedRows_TheClaimedCasesOnly(t *testing.T) {
	w := newMovesWorld(t)
	open, review, parked := w.in(t, "open"), w.in(t, "review"), w.in(t, "parked")

	rows := w.claimedRows(t, w.gus)
	got, ok := findItem(rows, review)
	if !ok {
		t.Fatalf("gus reads the team's claimed rows: %d Items, without the case fay claimed", len(rows))
	}
	if h := got.Fields["f_claimed_by"]; h != w.fay.Actor {
		t.Errorf("the claimed row's f_claimed_by = %v, want %s: the holder is read like any field", h, w.fay.Actor)
	}
	for _, id := range []uuid.UUID{open, parked} {
		if _, in := findItem(rows, id); in {
			t.Errorf("the claimed rows hold %s, which nobody claimed", id)
		}
	}
	if len(rows) != 1 {
		t.Errorf("the claimed rows hold %d Items, want 1", len(rows))
	}

	// Without Claimed: the queue as before, the claimed case not in it.
	if _, in := w.movesIn(t, w.gus, mvTeam, review); in {
		t.Error("the team's queue (claimed false) holds the claimed case; it holds what nobody has claimed")
	}
	if _, in := w.movesIn(t, w.gus, mvTeam, open); !in {
		t.Error("the team's queue lost the open case")
	}
}

// Who reads the claimed rows, and what each may do there: the claimant
// moves it on; a colleague may release it (step 2's rule) and is withheld
// the rest, claimed_by_other; whoever holds no role sees the row with
// nothing listed.
func TestClaimedRows_MovesAndWithheldByReader(t *testing.T) {
	w := newMovesWorld(t)
	review := w.in(t, "review")
	gusBot := withSession(t, w.k, item.Principal{Actor: "agent:mv-bot-" + uuid.NewString()[:8], OnBehalfOf: w.gus.Actor, Namespace: w.ns})
	w.grant(t, item.Principal{Actor: gusBot.Actor}, "mv_team")
	other := func(to string) item.WithheldMove {
		return item.WithheldMove{ToState: to, Reason: policy.ReasonClaimedByOther}
	}
	for _, c := range []struct {
		what     string
		who      item.Principal
		moves    []string
		withheld []item.WithheldMove
	}{
		{"fay, the claimant", w.fay, []string{"open", "done", "canceled"}, []item.WithheldMove{}},
		{"gus, her colleague", w.gus, []string{"open"}, []item.WithheldMove{other("done"), other("canceled")}},
		{"an agent in the team, for gus", gusBot, []string{"open"}, []item.WithheldMove{other("done"), other("canceled")}},
		{"lea, who leads (the release only names her)", w.lea, []string{"open"}, []item.WithheldMove{}},
		{"olaf, who holds nothing", w.olaf, []string{}, []item.WithheldMove{}},
	} {
		it, ok := findItem(w.claimedRows(t, c.who), review)
		if !ok {
			t.Errorf("%s: the claimed case is not among the team's claimed rows", c.what)
			continue
		}
		if !slices.Equal(it.Moves, c.moves) || it.Moves == nil {
			t.Errorf("%s: moves = %v, want %v", c.what, it.Moves, c.moves)
		}
		if !slices.Equal(it.Withheld, c.withheld) || it.Withheld == nil {
			t.Errorf("%s: withheld = %v, want %v", c.what, it.Withheld, c.withheld)
		}
	}
}

// An empty claim field still makes a claimed row (an Item migrated into a
// claimed state before anyone held it). No write the kernel offers leaves
// the field empty in review, so the test empties it directly, standing in
// for that migration.
func TestClaimedRows_AnEmptyClaimFieldCounts(t *testing.T) {
	w := newMovesWorld(t)
	review := w.in(t, "review")
	if _, err := pool.Exec(context.Background(),
		`update items set fields = fields - 'f_claimed_by' where id = $1`, review); err != nil {
		t.Fatalf("empty the claim field: %v", err)
	}
	it, ok := findItem(w.claimedRows(t, w.gus), review)
	if !ok {
		t.Fatal("a claimed case whose claim field is empty is not among the claimed rows")
	}
	want := []item.WithheldMove{
		{ToState: "done", Reason: policy.ReasonClaimedByOther},
		{ToState: "canceled", Reason: policy.ReasonClaimedByOther},
	}
	if !slices.Equal(it.Withheld, want) {
		t.Errorf("held by nobody: withheld = %v, want %v", it.Withheld, want)
	}
}

// Oldest claimed first with waiting_since, paged; List takes the filter
// too; Count counts the claimed rows; a page token of the claimed rows is
// not one of the queue's.
func TestClaimedRows_OrderPagingCount(t *testing.T) {
	w := newMovesWorld(t)
	a, b, c := w.in(t, "open"), w.in(t, "open"), w.in(t, "open")
	for _, id := range []uuid.UUID{c, a, b} { // claimed in this order
		w.move(t, w.fay, id, "review", nil)
	}
	req := itemservice.SearchRequest{Principal: w.gus, Limit: 2, OrderBy: oldestWaiting,
		Filter: itemservice.Filter{WaitingOn: mvTeam, Claimed: true, TypeIDs: []string{"mv_case"}}}
	first, err := w.k.Search(testCtx(t), req)
	if err != nil {
		t.Fatalf("the first page: %v", err)
	}
	if len(first.Items) != 2 || first.Items[0].ID != c || first.Items[1].ID != a || first.NextPageToken == "" {
		t.Fatalf("the first page: %v (token %q), want c then a, and a token", idsOf(first.Items), first.NextPageToken)
	}
	next := req
	next.PageToken = first.NextPageToken
	second, err := w.k.Search(testCtx(t), next)
	if err != nil || len(second.Items) != 1 || second.Items[0].ID != b || second.NextPageToken != "" {
		t.Fatalf("the second page: %v (token %q, error %v), want b alone and no token", idsOf(second.Items), second.NextPageToken, err)
	}
	// The same token with the queue: refused, as a token of another request.
	queue := next
	queue.Filter.Claimed = false
	var inv *itemservice.InvalidError
	if _, err := w.k.Search(testCtx(t), queue); !errors.As(err, &inv) || inv.Path != "page_token" {
		t.Errorf("a token of the claimed rows used with the queue: error = %v, want an *InvalidError at page_token", err)
	}

	listed, err := w.k.List(testCtx(t), itemservice.ListRequest{Principal: w.gus, ContainerID: &w.box,
		Filter: itemservice.Filter{WaitingOn: mvTeam, Claimed: true}})
	if err != nil || len(listed.Items) != 3 {
		t.Errorf("List the box's claimed rows: %d Items, error %v; want 3", len(listed.Items), err)
	}

	n, err := w.k.Count(testCtx(t), itemservice.CountRequest{Principal: w.gus,
		Filter: itemservice.Filter{WaitingOn: mvTeam, Claimed: true, TypeIDs: []string{"mv_case"}}})
	if err != nil || n.Count != 3 || n.Capped {
		t.Errorf("Count the claimed rows: %+v, error %v; want 3", n, err)
	}
	q, err := w.k.Count(testCtx(t), itemservice.CountRequest{Principal: w.gus,
		Filter: itemservice.Filter{WaitingOn: mvTeam, TypeIDs: []string{"mv_case"}}})
	if err != nil || q.Count != 0 {
		t.Errorf("Count the queue, all three claimed: %+v, error %v; want 0", q, err)
	}
}

// Claimed goes with waiting_on role:<name> only; a customer has no
// role's queue, claimed or not.
func TestClaimedRows_Refused(t *testing.T) {
	w := newMovesWorld(t)
	kim := customer("kim", w.ns)
	for _, c := range []struct {
		what string
		who  item.Principal
		f    itemservice.Filter
		path string
	}{
		{"without waiting_on", w.gus, itemservice.Filter{Claimed: true}, "filter.claimed"},
		{"with waiting_on me", w.gus, itemservice.Filter{WaitingOn: "me", Claimed: true}, "filter.claimed"},
		{"a customer, waiting_on me", kim, itemservice.Filter{WaitingOn: "me", Claimed: true}, "filter.claimed"},
		{"a customer, a role's queue", kim, itemservice.Filter{WaitingOn: mvTeam, Claimed: true}, "filter.waiting_on"},
	} {
		var inv *itemservice.InvalidError
		_, err := w.k.Search(testCtx(t), itemservice.SearchRequest{Principal: c.who, Filter: c.f})
		if !errors.As(err, &inv) || inv.Path != c.path {
			t.Errorf("Search, %s: error = %v, want an *InvalidError at %s", c.what, err, c.path)
		}
		_, err = w.k.Count(testCtx(t), itemservice.CountRequest{Principal: c.who, Filter: c.f})
		if !errors.As(err, &inv) || inv.Path != c.path {
			t.Errorf("Count, %s: error = %v, want an *InvalidError at %s", c.what, err, c.path)
		}
	}
}

// Withheld in the queue and in "me": own_request, own_container; nothing
// for a move the caller holds no role for.
func TestWithheld_InTheQueue(t *testing.T) {
	w := newMovesWorld(t)
	// fay asks for a case herself: she may not claim it (except creator).
	fays := w.create(t, itemservice.CreateRequest{
		Principal: w.fay, TypeID: "mv_case", TypeVersion: 1, ContainerID: &w.box,
		Fields: map[string]any{"f_title": "fay's own " + word(), "f_owner": w.bob.Actor},
	})
	parked := w.in(t, "parked")
	open := w.in(t, "open")
	for _, c := range []struct {
		what     string
		who      item.Principal
		on       string
		id       uuid.UUID
		withheld []item.WithheldMove
	}{
		{"fay, her own case, the queue", w.fay, mvTeam, fays,
			[]item.WithheldMove{{ToState: "review", Reason: policy.ReasonOwnRequest}}},
		{"gus, who made the box, a parked case", w.gus, mvTeam, parked,
			[]item.WithheldMove{{ToState: "open", Reason: policy.ReasonOwnContainer}}},
		{"fay, someone else's open case", w.fay, "me", open, []item.WithheldMove{}},
		// ana asked: she holds no role, so claiming is not withheld from her.
		{"ana, who asked, reading the queue", w.ana, mvTeam, open, []item.WithheldMove{}},
		{"olaf, who holds nothing", w.olaf, mvTeam, open, []item.WithheldMove{}},
	} {
		res, err := w.k.Search(testCtx(t), itemservice.SearchRequest{Principal: c.who, Limit: 500,
			Filter: itemservice.Filter{WaitingOn: c.on, TypeIDs: []string{"mv_case"}}})
		if err != nil {
			t.Fatalf("%s: %v", c.what, err)
		}
		it, ok := findItem(res.Items, c.id)
		if !ok {
			t.Errorf("%s: the case is not in the view (%s)", c.what, c.on)
			continue
		}
		if it.Withheld == nil || !slices.Equal(it.Withheld, c.withheld) {
			t.Errorf("%s: withheld = %v, want %v (moves %v)", c.what, it.Withheld, c.withheld, it.Moves)
		}
		for _, wm := range it.Withheld {
			if slices.Contains(it.Moves, wm.ToState) {
				t.Errorf("%s: %s is both a move and withheld", c.what, wm.ToState)
			}
		}
	}
}

// Withheld is filled only where Moves is: nil on Get, on Search and List
// without waiting_on.
func TestWithheld_OnlyInAWaitingOnAnswer(t *testing.T) {
	w := newMovesWorld(t)
	review := w.in(t, "review")
	got, err := w.k.Get(testCtx(t), w.gus, review)
	if err != nil || got.Withheld != nil {
		t.Errorf("Get: Withheld = %v, error %v; want nil", got.Withheld, err)
	}
	res, err := w.k.Search(testCtx(t), itemservice.SearchRequest{Principal: w.gus, Filter: itemservice.Filter{TypeIDs: []string{"mv_case"}}})
	if err != nil || len(res.Items) != 1 || res.Items[0].Withheld != nil {
		t.Errorf("Search without waiting_on: %d Items, error %v; want the case with nil Withheld", len(res.Items), err)
	}
	listed, err := w.k.List(testCtx(t), itemservice.ListRequest{Principal: w.gus, ContainerID: &w.box})
	if err != nil || len(listed.Items) != 1 || listed.Items[0].Withheld != nil {
		t.Errorf("List without waiting_on: %d Items, error %v; want the case with nil Withheld", len(listed.Items), err)
	}
}
