// Tests for Item.Moves (docs/roadmap.md, track 1, item 5, step 4, PR 1):
// the moves the caller may make, said by the kernel so that a screen does
// no who-may logic of its own. The owner's rule:
//
//   - Moves lists the to-state of each transition out of the Item's state
//     that the caller may make for who they are, in the Type's order.
//   - It is judged by the move's own rules, the ones a transition is judged
//     by: `allowed`, `except`, the state's claim and a release; for an
//     agent, the agent and the user it acts for. A move open to anyone is
//     listed; the kernel's own moves never are.
//   - Guards, required and given fields are not evaluated: a listed move
//     may still be refused when asked, as the waiting_on view says.
//   - Only Search and List with Filter.WaitingOn fill it. There, an empty
//     list means the caller may make none (a role's queue asked by someone
//     outside it). On every other read it is nil and says nothing.
//
// The second test holds the list to what Transition itself lets through.
package itemservice_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// mv_case has one move of each kind of who-may: a role with except, anyone,
// a field, the creator, a claim with its release, the creator beside a
// role, and except: [container_creator]. No guard and no required field,
// so that a refusal is always about who is asking.
const mvCaseSrc = `
id: mv_case
version: 1
initial_state: open
fields:
  - {id: f_title,      type: string}
  - {id: f_owner,      type: principal}
  - {id: f_claimed_by, type: principal, writable_by: ['role:mv_team']}
states:
  - {id: open,     category: waiting}
  - {id: review,   category: active, claim: f_claimed_by}
  - {id: parked,   category: waiting}
  - {id: done,     category: done}
  - {id: canceled, category: canceled}
transitions:
  - {from: open,   to: review,   allowed: ['role:mv_team'], except: [creator]}
  - {from: open,   to: parked}
  - {from: open,   to: done,     allowed: ['field:f_owner']}
  - {from: open,   to: canceled, allowed: [creator]}
  - {from: review, to: open,     allowed: ['role:mv_team', 'role:mv_lead'], release: true, clears: [f_claimed_by]}
  - {from: review, to: done,     allowed: ['role:mv_team'], except: [creator]}
  - {from: review, to: canceled, allowed: [creator, 'role:mv_team']}
  - {from: parked, to: open,     allowed: ['role:mv_team'], except: [container_creator]}
  - {from: parked, to: done,     allowed: ['field:f_owner']}
`

// movesWorld: ana asks, bob owns (f_owner), fay and gus are the team, lea
// leads it, olaf holds nothing. Every case sits in a box gus created.
type movesWorld struct {
	*waitWorld
	lea item.Principal
	box uuid.UUID
}

func newMovesWorld(t *testing.T) movesWorld {
	t.Helper()
	w := movesWorld{waitWorld: newWaitWorld(t)}
	ty, err := typedef.Parse([]byte(mvCaseSrc))
	if err != nil {
		t.Fatalf("parse mv_case: %v", err)
	}
	if err := w.k.RegisterType(testCtx(t), ty); err != nil {
		t.Fatalf("register mv_case: %v", err)
	}
	w.lea = in(person("lea"), w.ns)
	w.grant(t, w.fay, "mv_team")
	w.grant(t, w.gus, "mv_team")
	w.grant(t, w.lea, "mv_lead")
	w.box = w.create(t, itemservice.CreateRequest{
		Principal: w.gus, TypeID: "wq_desk", TypeVersion: 1, Fields: map[string]any{"f_name": "Cases"},
	})
	return w
}

// in makes a fresh case in state: open; review, claimed by fay; or parked.
func (w movesWorld) in(t *testing.T, state string) uuid.UUID {
	t.Helper()
	id := w.create(t, itemservice.CreateRequest{
		Principal: w.ana, TypeID: "mv_case", TypeVersion: 1, ContainerID: &w.box,
		Fields: map[string]any{"f_title": "case " + word(), "f_owner": w.bob.Actor},
	})
	switch state {
	case "open":
	case "review":
		w.move(t, w.fay, id, "review", nil)
	case "parked":
		w.move(t, w.olaf, id, "parked", nil)
	default:
		t.Fatalf("no such state to make: %s", state)
	}
	return id
}

// movesIn is the Item's Moves as p reads it in the view `on` (me, or
// role:<name>), and whether the Item is in that view at all.
func (w movesWorld) movesIn(t *testing.T, p item.Principal, on string, id uuid.UUID) (moves []string, inView bool) {
	t.Helper()
	req := itemservice.SearchRequest{Principal: p, Filter: itemservice.Filter{WaitingOn: on, TypeIDs: []string{"mv_case"}}, Limit: 500}
	res, err := w.k.Search(testCtx(t), req)
	if err != nil {
		t.Fatalf("Search waiting on %s as %s: %v", on, p.Actor, err)
	}
	for _, it := range res.Items {
		if it.ID == id {
			if it.Moves == nil {
				t.Errorf("as %s, waiting on %s: Moves is nil; in a waiting_on answer it is a list, empty when the caller may make none", p.Actor, on)
			}
			return it.Moves, true
		}
	}
	return nil, false
}

// The list, case by case.
func TestCallerMoves_WhatTheCallerMayMake(t *testing.T) {
	w := newMovesWorld(t)
	open, review, parked := w.in(t, "open"), w.in(t, "review"), w.in(t, "parked")
	agentFor := func(user item.Principal, role bool) item.Principal {
		a := item.Principal{Actor: "agent:mv-bot-" + uuid.NewString()[:8], OnBehalfOf: user.Actor, Namespace: w.ns}
		if role {
			w.grant(t, item.Principal{Actor: a.Actor}, "mv_team")
		}
		return withSession(t, w.k, a)
	}
	const me, queue = "me", "role:mv_team"

	for _, c := range []struct {
		what string
		who  item.Principal
		on   string
		id   uuid.UUID
		want []string // nil: the Item is not in the view
	}{
		// An open case: the team claims it, or parks it (anyone may).
		{"fay, in the team", w.fay, me, open, []string{"review", "parked"}},
		// bob, whom f_owner names, closes it; he cannot claim it.
		{"bob, the owner", w.bob, me, open, []string{"parked", "done"}},
		// ana asked for it: it does not wait on her. In the team's queue,
		// which anyone may read, her moves are her own: park, or cancel.
		{"ana, who asked, in her own view", w.ana, me, open, nil},
		{"ana, who asked, reading the team's queue", w.ana, queue, open, []string{"parked", "canceled"}},
		// olaf holds nothing: the one move open to anyone.
		{"olaf, reading the team's queue", w.olaf, queue, open, []string{"parked"}},
		// An agent acting for fay: the agent and fay must both pass.
		{"an agent in the team, for fay", agentFor(w.fay, true), me, open, []string{"review", "parked"}},
		{"an agent outside the team, for fay", agentFor(w.fay, false), queue, open, []string{"parked"}},
		{"an agent in the team, for olaf", agentFor(w.olaf, true), queue, open, []string{"parked"}},

		// Claimed by fay: every move out is hers, the release included.
		{"fay, the claimant", w.fay, me, review, []string{"open", "done", "canceled"}},
		// gus, her colleague: not in his view, and not in the queue.
		{"gus, her colleague", w.gus, me, review, nil},
		{"the team's queue holds no claimed case", w.gus, queue, review, nil},
		// ana may cancel an open case; a claimed one is the claimant's.
		{"ana, who asked", w.ana, me, review, nil},

		// Parked: the team reopens it, but never whoever created its box
		// (gus); the owner closes it.
		{"fay", w.fay, me, parked, []string{"open"}},
		{"gus, who created the box, reading the queue", w.gus, queue, parked, []string{}},
		{"bob, the owner", w.bob, me, parked, []string{"done"}},
		{"olaf, reading the queue", w.olaf, queue, parked, []string{}},
	} {
		got, inView := w.movesIn(t, c.who, c.on, c.id)
		switch {
		case c.want == nil && inView:
			t.Errorf("%s: the case is in the view (%s) with moves %v, want it left out", c.what, c.on, got)
		case c.want != nil && !inView:
			t.Errorf("%s: the case is not in the view (%s), want it there with moves %v", c.what, c.on, c.want)
		case c.want != nil && !slices.Equal(got, c.want):
			t.Errorf("%s: moves = %v, want %v", c.what, got, c.want)
		}
	}
}

// Moves is filled only where it was asked for: by Search and List with
// Filter.WaitingOn. Elsewhere it is nil, on the very same Item.
func TestCallerMoves_OnlyInAWaitingOnAnswer(t *testing.T) {
	w := newMovesWorld(t)
	open := w.in(t, "open")
	got, err := w.k.Get(testCtx(t), w.fay, open)
	if err != nil || got.Moves != nil {
		t.Errorf("Get: Moves = %v, error %v; want nil", got.Moves, err)
	}
	cases := itemservice.Filter{TypeIDs: []string{"mv_case"}}
	res, err := w.k.Search(testCtx(t), itemservice.SearchRequest{Principal: w.fay, Filter: cases})
	if err != nil || len(res.Items) != 1 || res.Items[0].Moves != nil {
		t.Errorf("Search without waiting_on: %d Items, error %v, Moves %v; want the case with nil Moves", len(res.Items), err, movesOfAll(res.Items))
	}
	listed, err := w.k.List(testCtx(t), itemservice.ListRequest{Principal: w.fay, ContainerID: &w.box})
	if err != nil || len(listed.Items) != 1 || listed.Items[0].Moves != nil {
		t.Errorf("List without waiting_on: %d Items, error %v, Moves %v; want the case with nil Moves", len(listed.Items), err, movesOfAll(listed.Items))
	}
	// List with waiting_on fills it, as Search does.
	listed, err = w.k.List(testCtx(t), itemservice.ListRequest{Principal: w.fay, ContainerID: &w.box, Filter: itemservice.Filter{WaitingOn: "me"}})
	if err != nil || len(listed.Items) != 1 || !slices.Equal(listed.Items[0].Moves, []string{"review", "parked"}) {
		t.Errorf("List with waiting_on me: %d Items, error %v, Moves %v; want the case with [review parked]", len(listed.Items), err, movesOfAll(listed.Items))
	}
}

func movesOfAll(items []item.Item) [][]string {
	out := make([][]string, len(items))
	for i, it := range items {
		out[i] = it.Moves
	}
	return out
}

// The list is what Transition lets through. For every person and agent,
// every state and both views: where the case is in the view, its Moves are
// exactly the moves that the same caller, asking Transition on a fresh
// case in that state, is not refused for who they are (ErrForbidden:
// PERMISSION_DENIED over the API). mv_case has no guard and no required
// field, so nothing else can refuse a move.
func TestCallerMoves_AreWhatTransitionLetsThrough(t *testing.T) {
	w := newMovesWorld(t)
	ty := w.types["mv_case@1"]
	if ty == nil {
		var err error
		if ty, err = w.k.Type(testCtx(t), "mv_case", 1); err != nil {
			t.Fatalf("read mv_case: %v", err)
		}
	}
	agent := func(user item.Principal, role bool) item.Principal {
		a := item.Principal{Actor: "agent:mv-bot-" + uuid.NewString()[:8], Namespace: w.ns}
		if user.Actor != "" {
			a.OnBehalfOf = user.Actor
		}
		if role {
			w.grant(t, item.Principal{Actor: a.Actor}, "mv_team")
		}
		return withSession(t, w.k, a)
	}
	callers := map[string]item.Principal{
		"ana (asked)": w.ana, "bob (owner)": w.bob, "fay (team, claimant)": w.fay, "gus (team, made the box)": w.gus,
		"lea (lead)": w.lea, "olaf (nobody)": w.olaf,
		"agent in the team for fay":      agent(w.fay, true),
		"agent outside the team for fay": agent(w.fay, false),
		"agent in the team for olaf":     agent(w.olaf, true),
		"agent in the team, on its own":  agent(item.Principal{}, true),
		"agent in the team for ana":      agent(w.ana, true),
	}
	compared := 0
	for name, p := range callers {
		for _, state := range []string{"open", "review", "parked"} {
			// What Transition lets this caller through, one fresh case a move.
			var through []string
			for _, tr := range ty.Transitions {
				if tr.From != state {
					continue
				}
				id := w.in(t, state)
				_, err := w.k.Transition(testCtx(t), itemservice.TransitionRequest{
					IdempotencyKey: newKey(), Principal: p, ItemID: id, ToState: tr.To,
				})
				switch {
				case err == nil:
					through = append(through, tr.To)
				case errors.Is(err, itemservice.ErrForbidden):
				default:
					t.Fatalf("%s, %s -> %s: %v; want applied, or refused for who is asking", name, state, tr.To, err)
				}
			}
			if through == nil {
				through = []string{}
			}
			seen := w.in(t, state)
			for _, on := range []string{"me", "role:mv_team"} {
				got, inView := w.movesIn(t, p, on, seen)
				if !inView {
					continue
				}
				compared++
				if !slices.Equal(got, through) {
					t.Errorf("%s, a case in %s, waiting on %s: Moves = %v, but Transition lets through %v", name, state, on, got, through)
				}
			}
			// Whatever waits on someone is a case they can move.
			if _, inMine := w.movesIn(t, p, "me", seen); inMine && len(through) == 0 {
				t.Errorf("%s: a case in %s waits on them, and Transition lets them make no move", name, state)
			}
		}
	}
	// 11 callers, 3 states, 2 views; a claimed case is in few views.
	if compared < 30 {
		t.Errorf("compared %d views, want at least 30: the test no longer sees the cases", compared)
	}
}
