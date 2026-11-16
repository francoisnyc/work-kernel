// Tests for proof-of-concept Step 2, part 2, part one of three: the leave
// balance and the request's balance link, through the item service.
//
//   - leave_balance (types/leave_balance.v1.yaml): only hr_admin writes
//     f_owner and f_allowance (writable_by roles, FieldNotWritableError
//     otherwise; an agent and its user must both hold the role); nobody
//     writes f_used through the public door; the invariants hold on every
//     write (invariant_failed, metadata invariant), so a non-admin cannot
//     create an empty balance.
//   - vacation_request v2's `balance` link is set by the kernel in the
//     write that enters submitted (here: the HR check's ResolveHold): the
//     unique open leave_balance whose f_owner is the person the request is
//     for. None or several deny the submit (no_balance,
//     ambiguous_balance). A resubmit keeps the same target, or replaces it,
//     in the same write, recorded on the transition event. The public door
//     can neither link nor unlink it (LinkNotWritableError), after
//     NOT_FOUND.
//   - The candidates are read before the locks and read again after them;
//     a change in between starts the write again (test hook point
//     "resolve_candidates_read", between the two reads).
//
// The HR check is played through ResolveHold, as in part 1.
package itemservice_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
)

// --- the world ---

type leaveWorld struct {
	k                    *itemservice.Service
	clock                *testClock
	ana, bob, dana, hana item.Principal // hana holds hr_admin
}

// newLeaveWorld: the clock says 2099-06-01, the leave balance and the
// vacation request v2 are registered, and four fresh users.
func newLeaveWorld(t *testing.T) leaveWorld {
	t.Helper()
	clock := &testClock{now: time.Date(2099, 6, 1, 9, 0, 0, 0, time.UTC)}
	k := itemservice.NewWithOptions(pool, itemservice.Options{CacheTypes: true, Now: clock.Now})
	registerFromFile(t, k, "leave_balance.v1.yaml")
	registerFromFile(t, k, "vacation_request.v2.yaml")
	w := leaveWorld{k: k, clock: clock, ana: person("ana"), bob: person("bob"), dana: person("dana"), hana: person("hana")}
	grantRole(t, w.hana, "hr_admin")
	return w
}

// balance: hana creates an open balance for owner, narrowed to the owner
// and hana.
func (w leaveWorld) balance(t *testing.T, owner item.Principal, allowance int) uuid.UUID {
	t.Helper()
	res, err := w.k.Create(testCtx(t), itemservice.CreateRequest{
		IdempotencyKey: newKey(), Principal: w.hana, TypeID: "leave_balance", TypeVersion: 1,
		Fields: map[string]any{"f_owner": owner.Actor, "f_allowance": allowance},
		Narrow: &itemservice.Narrow{Readers: []string{owner.Actor, w.hana.Actor}},
	})
	if err != nil {
		t.Fatalf("hana creates a balance for %s: %v", owner.Actor, err)
	}
	return res.ItemID
}

func (w leaveWorld) closeBalance(t *testing.T, id uuid.UUID) {
	t.Helper()
	if _, err := w.k.Transition(testCtx(t), itemservice.TransitionRequest{
		IdempotencyKey: newKey(), Principal: w.hana, ItemID: id, ToState: "closed",
	}); err != nil {
		t.Fatalf("hana closes the balance: %v", err)
	}
}

// used reads a balance's f_used as hana: nil when unset.
func (w leaveWorld) used(t *testing.T, bal uuid.UUID) any {
	t.Helper()
	it, err := w.k.Get(testCtx(t), w.hana, bal)
	if err != nil {
		t.Fatalf("Get balance: %v", err)
	}
	return it.Fields["f_used"]
}

// assertUsed: f_used is want; unset counts as 0 (a balance never deducted).
func assertUsed(t *testing.T, w leaveWorld, bal uuid.UUID, want int) {
	t.Helper()
	v := w.used(t, bal)
	if v == nil && want == 0 {
		return
	}
	if got, ok := typedefInt(v); !ok || got != int64(want) {
		t.Errorf("f_used = %v, want %d", w.used(t, bal), want)
	}
}

func typedefInt(v any) (int64, bool) {
	switch x := v.(type) {
	case int:
		return int64(x), true
	case int64:
		return x, true
	case float64:
		return int64(x), x == float64(int64(x))
	}
	return 0, false
}

func leaveTerms(days int) map[string]any {
	return map[string]any{"f_start": "2099-07-01", "f_end": "2099-07-10", "f_days": days, "f_leave_type": "annual"}
}

// request creates a v2 vacation request as p.
func (w leaveWorld) request(t *testing.T, p item.Principal, days int) uuid.UUID {
	t.Helper()
	res, err := w.k.Create(testCtx(t), itemservice.CreateRequest{
		IdempotencyKey: newKey(), Principal: p, TypeID: "vacation_request", TypeVersion: 2, Fields: leaveTerms(days),
	})
	if err != nil {
		t.Fatalf("create vacation request v2: %v", err)
	}
	return res.ItemID
}

func (w leaveWorld) move(t *testing.T, p item.Principal, id uuid.UUID, to string, patch map[string]any) (itemservice.TransitionResult, error) {
	t.Helper()
	return w.k.Transition(testCtx(t), itemservice.TransitionRequest{
		IdempotencyKey: newKey(), Principal: p, ItemID: id, ToState: to, Fields: patch,
	})
}

func hrCauseV2(id uuid.UUID, seq int64) item.Attribution {
	return item.Attribution{OriginatingHook: "hook:vacation_request@v2:hr_check", CausationID: fmt.Sprintf("%s:%d", id, seq), Hop: 1}
}

// askSubmit asks for draft -> submitted (the HR check's hold).
func (w leaveWorld) askSubmit(t *testing.T, p item.Principal, id uuid.UUID) itemservice.TransitionResult {
	t.Helper()
	res, err := w.move(t, p, id, "submitted", nil)
	if err != nil || res.Outcome != itemservice.OutcomePending {
		t.Fatalf("submit = %+v, %v; want pending (the HR check)", res, err)
	}
	return res
}

// hrNames plays the HR check: correct with f_approver = approver. It
// returns the resolution's result and how the hold ended.
func (w leaveWorld) hrNames(t *testing.T, id uuid.UUID, req itemservice.TransitionResult, approver item.Principal) (itemservice.WriteResult, *item.Hold) {
	t.Helper()
	res, err := w.k.Internal().ResolveHold(testCtx(t), itemservice.ResolveHoldRequest{
		IdempotencyKey: "hold:" + req.HoldID.String(), Principal: hrCheck, ItemID: id, HoldID: req.HoldID,
		Verdict: itemservice.VerdictCorrect, Fields: map[string]any{"f_approver": approver.Actor},
		Attribution: hrCauseV2(id, req.ItemSeq),
	})
	if err != nil {
		t.Fatalf("HR correct: %v", err)
	}
	return res, w.holdNow(t, id)
}

// holdNow reads the Item's latest hold, as the kernel (hana cannot see
// every request), straight from the table.
func (w leaveWorld) holdNow(t *testing.T, id uuid.UUID) *item.Hold {
	t.Helper()
	var h item.Hold
	err := pool.QueryRow(testCtx(t),
		`select id, hook, from_state, to_state, status, coalesce(reason, '') from item_holds
		 where item_id = $1 order by requested_seq desc limit 1`, id,
	).Scan(&h.ID, &h.Hook, &h.From, &h.To, &h.Status, &h.Reason)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		t.Fatalf("read hold: %v", err)
	}
	return &h
}

// submitted walks a new request of `days` into submitted, HR naming
// approver, and checks it got there.
func (w leaveWorld) submitted(t *testing.T, requester, approver item.Principal, days int) uuid.UUID {
	t.Helper()
	id := w.request(t, requester, days)
	_, h := w.hrNames(t, id, w.askSubmit(t, requester, id), approver)
	if h.Status != item.HoldAllowed {
		t.Fatalf("submit: hold %+v, want allowed", h)
	}
	return id
}

func (w leaveWorld) state(t *testing.T, id uuid.UUID) string {
	t.Helper()
	var s string
	if err := pool.QueryRow(testCtx(t), `select state from items where id = $1`, id).Scan(&s); err != nil {
		t.Fatalf("read state: %v", err)
	}
	return s
}

// balanceLinks lists the request's balance links, from the table.
func balanceLinks(t *testing.T, id uuid.UUID) []uuid.UUID {
	t.Helper()
	rows, err := pool.Query(testCtx(t),
		`select target_id from links where source_id = $1 and link_type = 'balance' order by target_id`, id)
	if err != nil {
		t.Fatalf("read links: %v", err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		t.Fatalf("read links: %v", err)
	}
	return out
}

func assertInvariantFailed(t *testing.T, what string, err error, invariant string) {
	t.Helper()
	assertRejectedCode(t, what, err, itemservice.CodeInvariantFailed)
	var rej *itemservice.RejectedError
	if errors.As(err, &rej) && rej.Metadata["invariant"] != invariant {
		t.Errorf("%s: metadata invariant = %q, want %q", what, rej.Metadata["invariant"], invariant)
	}
}

// --- the balance: who writes what ---

func TestLeaveBalance_HRCreatesAndSetsTheAllowance(t *testing.T) {
	w := newLeaveWorld(t)
	bal := w.balance(t, w.ana, 25)
	it, err := w.k.Get(testCtx(t), w.ana, bal)
	if err != nil {
		t.Fatalf("ana reads her balance: %v", err)
	}
	if it.State != "open" || it.Fields["f_owner"] != w.ana.Actor || it.Fields["f_used"] != nil {
		t.Errorf("balance = %s %v, want open, owned by ana, f_used unset", it.State, it.Fields)
	}
	if err := updateFields(t, w.k, w.hana, bal, map[string]any{"f_allowance": 30}); err != nil {
		t.Errorf("hana raises the allowance: %v", err)
	}
	if _, err := w.k.Get(testCtx(t), w.bob, bal); !errors.Is(err, itemservice.ErrNotFound) {
		t.Errorf("bob reads ana's balance: %v, want ErrNotFound (narrowed to ana and hana)", err)
	}
}

func updateFields(t *testing.T, k *itemservice.Service, p item.Principal, id uuid.UUID, fields map[string]any) error {
	t.Helper()
	_, err := k.Update(testCtx(t), itemservice.UpdateRequest{IdempotencyKey: newKey(), Principal: p, ItemID: id, Fields: fields})
	return err
}

func createBalance(t *testing.T, w leaveWorld, p item.Principal, fields map[string]any) error {
	t.Helper()
	_, err := w.k.Create(testCtx(t), itemservice.CreateRequest{
		IdempotencyKey: newKey(), Principal: p, TypeID: "leave_balance", TypeVersion: 1, Fields: fields,
	})
	return err
}

// f_owner and f_allowance are hr_admin's: anyone else is refused, naming
// the fields, on create and update. An agent needs the role itself, and so
// does the user it acts for.
func TestLeaveBalance_OnlyHRWritesOwnerAndAllowance(t *testing.T) {
	w := newLeaveWorld(t)
	assertNotWritable(t, "ana creates with an owner", createBalance(t, w, w.ana, map[string]any{"f_owner": w.ana.Actor}), "f_owner")
	assertNotWritable(t, "ana creates with both", createBalance(t, w, w.ana,
		map[string]any{"f_owner": w.ana.Actor, "f_allowance": 99}), "f_allowance", "f_owner")

	bal := w.balance(t, w.ana, 25)
	assertNotWritable(t, "ana raises her allowance", updateFields(t, w.k, w.ana, bal, map[string]any{"f_allowance": 99}), "f_allowance")
	assertNotWritable(t, "ana gives her balance away", updateFields(t, w.k, w.ana, bal, map[string]any{"f_owner": w.bob.Actor}), "f_owner")

	// An agent acting for hana: the agent lacks the role, so no.
	agent := withSession(t, w.k, item.Principal{Actor: "agent:" + uuid.NewString()[:8], OnBehalfOf: w.hana.Actor})
	assertNotWritable(t, "an agent without the role, for hana",
		updateFields(t, w.k, agent, bal, map[string]any{"f_allowance": 26}), "f_allowance")
	// Both hold it: yes.
	grantRole(t, item.Principal{Actor: agent.Actor}, "hr_admin")
	if err := updateFields(t, w.k, agent, bal, map[string]any{"f_allowance": 26}); err != nil {
		t.Errorf("an agent with the role, for hana: %v", err)
	}
	// The agent has it, its user does not: no.
	forAna := withSession(t, w.k, item.Principal{Actor: agent.Actor, OnBehalfOf: w.ana.Actor})
	assertNotWritable(t, "an agent with the role, for ana",
		updateFields(t, w.k, forAna, bal, map[string]any{"f_allowance": 99}), "f_allowance")
	assertUsed(t, w, bal, 0)
}

// f_used is the kernel's: refused to everyone on the public door,
// hr_admin included.
func TestLeaveBalance_NobodyWritesUsed(t *testing.T) {
	w := newLeaveWorld(t)
	assertNotWritable(t, "hana creates with f_used", createBalance(t, w, w.hana,
		map[string]any{"f_owner": w.ana.Actor, "f_allowance": 25, "f_used": 0}), "f_used")
	bal := w.balance(t, w.ana, 25)
	assertNotWritable(t, "hana sets f_used", updateFields(t, w.k, w.hana, bal, map[string]any{"f_used": 3}), "f_used")
	assertNotWritable(t, "ana sets f_used", updateFields(t, w.k, w.ana, bal, map[string]any{"f_used": 0}), "f_used")
}

// A non-admin cannot write the owner or the allowance, and the invariant
// refuses a balance without them: so they cannot create one at all.
func TestLeaveBalance_NonAdminCannotCreateAnEmptyBalance(t *testing.T) {
	w := newLeaveWorld(t)
	before := count(t, `select count(*) from items where type_id = 'leave_balance'`)
	assertInvariantFailed(t, "ana creates an empty balance", createBalance(t, w, w.ana, map[string]any{}), "balance_incomplete")
	assertInvariantFailed(t, "hana creates one without an allowance",
		createBalance(t, w, w.hana, map[string]any{"f_owner": w.ana.Actor}), "balance_incomplete")
	assertInvariantFailed(t, "hana creates one without an owner",
		createBalance(t, w, w.hana, map[string]any{"f_allowance": 25}), "balance_incomplete")
	assertInvariantFailed(t, "hana creates a negative allowance",
		createBalance(t, w, w.hana, map[string]any{"f_owner": w.ana.Actor, "f_allowance": -1}), "balance_incomplete")
	if after := count(t, `select count(*) from items where type_id = 'leave_balance'`); after != before {
		t.Errorf("balances = %d, want %d: a refused create writes nothing", after, before)
	}
	bal := w.balance(t, w.ana, 25)
	assertInvariantFailed(t, "hana clears the allowance",
		updateFields(t, w.k, w.hana, bal, map[string]any{"f_allowance": nil}), "balance_incomplete")
	// The transition's patch is a door too.
	_, err := w.k.Transition(testCtx(t), itemservice.TransitionRequest{
		IdempotencyKey: newKey(), Principal: w.hana, ItemID: bal, ToState: "closed", Fields: map[string]any{"f_allowance": -5},
	})
	assertInvariantFailed(t, "hana closes it with a negative allowance", err, "balance_incomplete")
	if st := w.state(t, bal); st != "open" {
		t.Errorf("state = %s, want open: the refused transition wrote nothing", st)
	}
}

// --- the balance link ---

// Entering submitted sets the link in the same write, to ana's open
// balance; the transition event records it; Get shows it.
func TestBalanceLink_SetOnEnteringSubmitted(t *testing.T) {
	w := newLeaveWorld(t)
	bal := w.balance(t, w.ana, 25)
	id := w.request(t, w.ana, 5)
	res, h := w.hrNames(t, id, w.askSubmit(t, w.ana, id), w.bob)
	if h.Status != item.HoldAllowed || w.state(t, id) != "submitted" {
		t.Fatalf("submit: hold %+v, state %s; want allowed, submitted", h, w.state(t, id))
	}
	if got := balanceLinks(t, id); !slices.Equal(got, []uuid.UUID{bal}) {
		t.Errorf("balance links = %v, want [%s]", got, bal)
	}
	op, payload := event(t, id, res.ItemSeq)
	if op != "transition" || payload["to"] != "submitted" || payload["link_type"] != "balance" ||
		payload["link_target"] != bal.String() || payload["link_replaced"] != nil {
		t.Errorf("the entering event = %s %v; want a transition into submitted naming the balance link", op, payload)
	}
	if n := count(t, `select count(*) from item_events where item_id = $1`, id); n != int(res.ItemSeq) {
		t.Errorf("events = %d, want %d: the link is in the transition's event, not one of its own", n, res.ItemSeq)
	}
	it, err := w.k.Get(testCtx(t), w.ana, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	want := item.Link{LinkType: "balance", Direction: item.LinkOutgoing, Other: bal}
	if !slices.Contains(it.Links, want) {
		t.Errorf("links = %+v, want %+v", it.Links, want)
	}
	// History shows it on the transition.
	page, err := w.k.ListEvents(testCtx(t), itemservice.ListEventsRequest{Principal: w.ana, ItemID: id})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	last := page.Events[len(page.Events)-1]
	if last.LinkType != "balance" || last.LinkTarget == nil || last.LinkTarget.ID != bal {
		t.Errorf("history: %+v, want the transition naming the balance link", last)
	}
	// bob cannot see the balance: the link is restricted for him.
	it, err = w.k.Get(testCtx(t), w.bob, id)
	if err != nil {
		t.Fatalf("bob Get: %v", err)
	}
	if !slices.Contains(it.Links, item.Link{LinkType: "balance", Direction: item.LinkOutgoing, Restricted: true}) {
		t.Errorf("bob's links = %+v, want the balance restricted", it.Links)
	}
}

// For whom the request is: an agent's request for ana finds ana's balance.
func TestBalanceLink_ForWhomTheRequestIs(t *testing.T) {
	w := newLeaveWorld(t)
	bal := w.balance(t, w.ana, 25)
	agent := withSession(t, w.k, item.Principal{Actor: "agent:" + uuid.NewString()[:8], OnBehalfOf: w.ana.Actor})
	res, err := w.k.Create(testCtx(t), itemservice.CreateRequest{
		IdempotencyKey: newKey(), Principal: agent, TypeID: "vacation_request", TypeVersion: 2, Fields: leaveTerms(5),
	})
	if err != nil {
		t.Fatalf("the agent creates ana's request: %v", err)
	}
	if _, h := w.hrNames(t, res.ItemID, w.askSubmit(t, w.ana, res.ItemID), w.bob); h.Status != item.HoldAllowed {
		t.Fatalf("submit: hold %+v", h)
	}
	if got := balanceLinks(t, res.ItemID); !slices.Equal(got, []uuid.UUID{bal}) {
		t.Errorf("balance links = %v, want ana's balance %s", got, bal)
	}
}

// No open balance, or several: the submit is denied, named; the request
// stays in draft with no link and no approver.
func TestBalanceLink_NoneOrSeveralDenyTheSubmit(t *testing.T) {
	cases := []struct {
		name   string
		setup  func(t *testing.T, w leaveWorld)
		reason string
	}{
		{"no balance at all", func(t *testing.T, w leaveWorld) {}, "no_balance"},
		{"only someone else's", func(t *testing.T, w leaveWorld) { w.balance(t, w.bob, 25) }, "no_balance"},
		{"only a closed one", func(t *testing.T, w leaveWorld) { w.closeBalance(t, w.balance(t, w.ana, 25)) }, "no_balance"},
		{"two open ones", func(t *testing.T, w leaveWorld) { w.balance(t, w.ana, 25); w.balance(t, w.ana, 10) }, "ambiguous_balance"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newLeaveWorld(t)
			c.setup(t, w)
			id := w.request(t, w.ana, 5)
			_, h := w.hrNames(t, id, w.askSubmit(t, w.ana, id), w.bob)
			if h.Status != item.HoldDenied || h.Reason != c.reason {
				t.Errorf("hold = %+v, want denied %s", h, c.reason)
			}
			it, _ := w.k.Get(testCtx(t), w.ana, id)
			if it.State != "draft" || it.Fields["f_approver"] != nil || len(balanceLinks(t, id)) != 0 {
				t.Errorf("after the denial: %s, approver %v, links %v; want draft, none, none",
					it.State, it.Fields["f_approver"], balanceLinks(t, id))
			}
		})
	}
	// A closed balance beside an open one: the open one.
	w := newLeaveWorld(t)
	w.closeBalance(t, w.balance(t, w.ana, 25))
	open := w.balance(t, w.ana, 20)
	id := w.submitted(t, w.ana, w.bob, 5)
	if got := balanceLinks(t, id); !slices.Equal(got, []uuid.UUID{open}) {
		t.Errorf("balance links = %v, want the open balance %s", got, open)
	}
}

// Recall and resubmit: the same balance again changes nothing and is not
// recorded; a different one (HR closed the old one and opened a new one)
// replaces it in the same write, recorded on the transition.
func TestBalanceLink_ResubmitKeepsOrReplaces(t *testing.T) {
	w := newLeaveWorld(t)
	first := w.balance(t, w.ana, 25)
	id := w.submitted(t, w.ana, w.bob, 5)

	if _, err := w.move(t, w.ana, id, "draft", nil); err != nil {
		t.Fatalf("recall: %v", err)
	}
	res, _ := w.hrNames(t, id, w.askSubmit(t, w.ana, id), w.bob)
	_, payload := event(t, id, res.ItemSeq)
	if _, ok := payload["link_type"]; ok {
		t.Errorf("resubmit to the same balance recorded a link change: %v", payload)
	}
	if got := balanceLinks(t, id); !slices.Equal(got, []uuid.UUID{first}) {
		t.Errorf("after resubmit: links %v, want [%s]", got, first)
	}

	if _, err := w.move(t, w.ana, id, "draft", nil); err != nil {
		t.Fatalf("recall again: %v", err)
	}
	w.closeBalance(t, first)
	second := w.balance(t, w.ana, 30)
	res, h := w.hrNames(t, id, w.askSubmit(t, w.ana, id), w.bob)
	if h.Status != item.HoldAllowed {
		t.Fatalf("resubmit: hold %+v", h)
	}
	_, payload = event(t, id, res.ItemSeq)
	if payload["link_type"] != "balance" || payload["link_target"] != second.String() || payload["link_replaced"] != first.String() {
		t.Errorf("replacing event payload = %v; want link_target %s, link_replaced %s", payload, second, first)
	}
	if got := balanceLinks(t, id); !slices.Equal(got, []uuid.UUID{second}) {
		t.Errorf("after the replacement: links %v, want only [%s]", got, second)
	}
	page, err := w.k.ListEvents(testCtx(t), itemservice.ListEventsRequest{Principal: w.ana, ItemID: id})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	last := page.Events[len(page.Events)-1]
	// ana can see both balances: neither is restricted for her.
	if last.LinkReplaced == nil || last.LinkReplaced.ID != first || last.LinkTarget == nil || last.LinkTarget.ID != second {
		t.Errorf("history: replaced %+v, target %+v; want %s and %s", last.LinkReplaced, last.LinkTarget, first, second)
	}
}

// The public door can neither set nor remove a kernel-set link, whoever
// asks; an Item the caller cannot see is NOT_FOUND first.
func TestBalanceLink_ThePublicDoorCannotLinkOrUnlink(t *testing.T) {
	w := newLeaveWorld(t)
	bal := w.balance(t, w.ana, 25)
	id := w.submitted(t, w.ana, w.bob, 5)
	draft := w.request(t, w.ana, 3)
	link := func(p item.Principal, src uuid.UUID, add bool) error {
		req := itemservice.LinkRequest{IdempotencyKey: newKey(), Principal: p, SourceID: src, LinkType: "balance", TargetID: bal}
		var err error
		if add {
			_, err = w.k.Link(testCtx(t), req)
		} else {
			_, err = w.k.Unlink(testCtx(t), req)
		}
		return err
	}
	for _, c := range []struct {
		name string
		p    item.Principal
		src  uuid.UUID
		add  bool
	}{
		{"ana links her draft", w.ana, draft, true},
		{"ana unlinks her submitted request", w.ana, id, false},
		{"hana links ana's draft", w.hana, draft, true},
	} {
		err := link(c.p, c.src, c.add)
		var lnw *itemservice.LinkNotWritableError
		if !errors.As(err, &lnw) || lnw.LinkType != "balance" || !errors.Is(err, itemservice.ErrForbidden) {
			t.Errorf("%s: %v, want a LinkNotWritableError (an ErrForbidden) naming balance", c.name, err)
		}
	}
	// hana cannot see ana's draft: hidden first. (hana sees the balance.)
	hidden := w.request(t, w.ana, 3)
	if _, err := w.k.Update(testCtx(t), itemservice.UpdateRequest{IdempotencyKey: newKey(), Principal: w.ana, ItemID: hidden,
		Policy: &itemservice.PolicyPatch{Readers: []string{w.ana.Actor}}}); err != nil {
		t.Fatalf("narrow: %v", err)
	}
	if err := link(w.hana, hidden, true); !errors.Is(err, itemservice.ErrNotFound) {
		t.Errorf("hana links a request she cannot see: %v, want ErrNotFound", err)
	}
	if got := balanceLinks(t, id); !slices.Equal(got, []uuid.UUID{bal}) {
		t.Errorf("links = %v, want unchanged", got)
	}
	if len(balanceLinks(t, draft)) != 0 {
		t.Error("the draft got a balance link")
	}
}

// The candidates are read before the locks. If they change before the
// locks are taken (hana gives the only candidate to bob), the write reads
// them again under the locks, starts again, and does not link to a
// balance that is no longer ana's.
func TestBalanceLink_CandidatesChangeBeforeTheLocks(t *testing.T) {
	w := newLeaveWorld(t)
	bal := w.balance(t, w.ana, 25)
	id := w.request(t, w.ana, 5)
	req := w.askSubmit(t, w.ana, id)

	var once sync.Once
	fired := false
	restore := itemservice.SetTestHook(func(ctx context.Context, tx pgx.Tx, point string) error {
		if point != "resolve_candidates_read" {
			return nil
		}
		var err error
		once.Do(func() {
			fired = true
			// Another connection: this transaction holds no lock on the balance yet.
			err = updateFields(t, w.k, w.hana, bal, map[string]any{"f_owner": w.bob.Actor})
		})
		return err
	})
	defer restore()

	_, h := w.hrNames(t, id, req, w.bob)
	if !fired {
		t.Fatal(`the test hook point "resolve_candidates_read" was never reached`)
	}
	if h.Status != item.HoldDenied || h.Reason != "no_balance" {
		t.Errorf("hold = %+v, want denied no_balance: the only candidate is bob's now", h)
	}
	if len(balanceLinks(t, id)) != 0 {
		t.Errorf("links = %v, want none", balanceLinks(t, id))
	}
}
