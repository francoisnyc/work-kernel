// Property test for Jira part 2, step 4a: the customer rule, in its two SQL
// forms, against a naive evaluator.
//
// The rule has two forms: a predicate over one Item row (Get, links,
// rollups, history references, write checks) and a relation, `mine`, that
// drives a customer's Search and List. Random portals, incidents (by
// customers, by staff for a customer, by staff alone), replies, notes and
// comments, narrowings whose readers mix staff and customers, inherits and
// moves between portals, an old desk and the top level. At the end, for
// every principal (staff and customers) and every Item of the namespace:
//
//   - Get agrees with the evaluator (the predicate form);
//   - Search over every Type finds exactly what the evaluator says, paged
//     two at a time (the relation form);
//   - List of every container, and of the top level, holds exactly the
//     visible children; a container the principal cannot see is
//     ErrNotFound.
//
// The evaluator works from Items, Types and scopes alone: the naive scope
// evaluator (narrowing intersected up the scope tree) and, for a customer,
// the rule written out in Go. Seeds are fixed, so a failure replays.
package itemservice_test

import (
	"errors"
	"fmt"
	"maps"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
)

const (
	customerPropertySeeds = 3
	customerPropertySteps = 60
)

func TestProperty_CustomerRule_GetSearchAndListMatchTheEvaluator(t *testing.T) {
	for seed := range customerPropertySeeds {
		t.Run(fmt.Sprintf("seed %d", seed), func(t *testing.T) { runCustomerProperty(t, uint64(seed)) })
	}
}

type custProp struct {
	w         custWorld
	r         *rand.Rand
	staff     []item.Principal // staff[0] is lea: a reader of everything she narrows
	customers []item.Principal
	portals   []uuid.UUID // desk@2, then the old desk@1
	incidents []uuid.UUID
	all       []uuid.UUID
}

func runCustomerProperty(t *testing.T, seed uint64) {
	w := newCustWorld(t)
	p := &custProp{w: w, r: rand.New(rand.NewPCG(seed, 11)),
		staff:     []item.Principal{w.lea, w.sam, w.eve},
		customers: []item.Principal{w.kim, w.lee, customer("max", w.ns)},
		portals:   []uuid.UUID{w.desk, w.desk2, w.oldDesk},
	}
	p.all = append(p.all, w.desk, w.desk2, w.oldDesk, w.space, w.story, w.cal, w.policy)
	for step := range customerPropertySteps {
		var what string
		switch n := p.r.IntN(100); {
		case n < 30:
			what = "an incident"
			p.newIncident(t)
		case n < 55:
			what = "a reply, a note or a comment"
			p.newChild(t)
		case n < 75:
			what = "narrow"
			p.narrow(t)
		case n < 85:
			what = "inherit"
			p.inherit(t)
		default:
			what = "move an incident"
			p.moveIncident(t)
		}
		if t.Failed() {
			t.Fatalf("seed %d, step %d (%s) failed", seed, step, what)
		}
	}
	p.check(t)
}

func (p *custProp) pick(ids []uuid.UUID) uuid.UUID { return ids[p.r.IntN(len(ids))] }

func (p *custProp) newIncident(t *testing.T) {
	t.Helper()
	w := p.w
	desk := p.pick(p.portals[:2])
	req := itemservice.CreateRequest{IdempotencyKey: newKey(), TypeID: "incident", TypeVersion: 2, ContainerID: &desk,
		Fields: map[string]any{"f_title": "vpn " + word(), "f_priority": "P3"}}
	switch p.r.IntN(3) {
	case 0: // a customer opens it
		req.Principal = p.customers[p.r.IntN(len(p.customers))]
	case 1: // staff open it for a customer
		req.Principal = p.staff[0]
		req.Requester = p.customers[p.r.IntN(len(p.customers))].Actor
	default: // staff open it, anywhere a desk is
		req.Principal = p.staff[p.r.IntN(len(p.staff))]
		d := p.pick(p.portals)
		req.ContainerID = &d
	}
	res, err := w.k.Create(testCtx(t), req)
	if errors.Is(err, itemservice.ErrNotFound) || isRequesterRefusal(err) {
		return // the portal was narrowed away from that customer: a fair refusal
	}
	if err != nil {
		t.Fatalf("create an incident (%s for %q): %v", req.Principal.Actor, req.Requester, err)
	}
	p.incidents = append(p.incidents, res.ItemID)
	p.all = append(p.all, res.ItemID)
}

func isRequesterRefusal(err error) bool {
	var inv *itemservice.InvalidError
	return errors.As(err, &inv) && inv.Path == "requester"
}

func (p *custProp) newChild(t *testing.T) {
	t.Helper()
	if len(p.incidents) == 0 {
		return
	}
	in := p.pick(p.incidents)
	typ, version, fields := "reply", 1, map[string]any{"f_body": "vpn " + word()}
	switch p.r.IntN(3) {
	case 1:
		typ = "note"
	case 2:
		typ, version, fields = "comment", 2, map[string]any{"f_body": "vpn", "f_public": true}
	}
	who := p.staff[0]
	if typ == "reply" && p.r.IntN(2) == 0 {
		who = p.customers[p.r.IntN(len(p.customers))] // may be refused: not theirs
	}
	res, err := p.w.k.Create(testCtx(t), itemservice.CreateRequest{IdempotencyKey: newKey(), Principal: who, TypeID: typ, TypeVersion: version,
		ContainerID: &in, Fields: fields})
	if item.IsKind(who.Actor, item.KindCustomer) && errors.Is(err, itemservice.ErrNotFound) {
		return
	}
	if err != nil {
		t.Fatalf("%s creates a %s: %v", who.Actor, typ, err)
	}
	p.all = append(p.all, res.ItemID)
}

func (p *custProp) readers() []string {
	out := []string{p.staff[0].Actor}
	for _, q := range append(slices.Clone(p.staff[1:]), p.customers...) {
		if p.r.IntN(2) == 0 {
			out = append(out, q.Actor)
		}
	}
	return out
}

func (p *custProp) narrow(t *testing.T) {
	t.Helper()
	target := p.pick(append(slices.Clone(p.portals), p.incidents...))
	if _, err := p.w.k.Update(testCtx(t), itemservice.UpdateRequest{IdempotencyKey: newKey(), Principal: p.staff[0], ItemID: target,
		Policy: &itemservice.PolicyPatch{Readers: p.readers()}}); err != nil {
		t.Fatalf("narrow: %v", err)
	}
}

func (p *custProp) inherit(t *testing.T) {
	t.Helper()
	target := p.pick(append(slices.Clone(p.portals), p.incidents...))
	_, err := p.w.k.Update(testCtx(t), itemservice.UpdateRequest{IdempotencyKey: newKey(), Principal: p.staff[0], ItemID: target,
		Policy: &itemservice.PolicyPatch{Inherit: true}})
	if err != nil && !errors.Is(err, itemservice.ErrInvalid) && !errors.Is(err, itemservice.ErrRejected) {
		t.Fatalf("inherit: %v", err)
	}
}

func (p *custProp) moveIncident(t *testing.T) {
	t.Helper()
	if len(p.incidents) == 0 {
		return
	}
	targets := append(slices.Clone(p.portals), p.w.space)
	to := p.pick(targets)
	_, err := p.w.k.Update(testCtx(t), itemservice.UpdateRequest{IdempotencyKey: newKey(), Principal: p.staff[0], ItemID: p.pick(p.incidents),
		Move: &itemservice.Move{To: &to, ConfirmWiden: true}})
	if err != nil {
		t.Fatalf("move: %v", err)
	}
}

// --- the evaluator ---

type evalItem struct {
	id, scope                    uuid.UUID
	typeID                       string
	version                      int
	container                    *uuid.UUID
	createdBy, createdOnBehalfOf string
}

func (p *custProp) loadItems(t *testing.T) map[uuid.UUID]evalItem {
	t.Helper()
	rows, err := pool.Query(testCtx(t),
		`select id, scope_id, type_id, type_version, container_id, created_by, coalesce(created_on_behalf_of, '')
		 from items where namespace_id = $1`, p.w.ns)
	if err != nil {
		t.Fatalf("read items: %v", err)
	}
	defer rows.Close()
	out := map[uuid.UUID]evalItem{}
	for rows.Next() {
		var e evalItem
		if err := rows.Scan(&e.id, &e.scope, &e.typeID, &e.version, &e.container, &e.createdBy, &e.createdOnBehalfOf); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out[e.id] = e
	}
	return out
}

// visible is the rule, written out naively.
func (p *custProp) visible(t *testing.T, items map[uuid.UUID]evalItem, open map[uuid.UUID]bool, access map[uuid.UUID][]string,
	who item.Principal, id uuid.UUID) bool {
	t.Helper()
	it := items[id]
	if !open[it.scope] && !slices.Contains(access[it.scope], who.Actor) {
		return false
	}
	if !item.IsKind(who.Actor, item.KindCustomer) {
		return true
	}
	declares := func(e evalItem) (own, shared []string) {
		ty, err := p.w.k.Type(testCtx(t), e.typeID, e.version)
		if err != nil {
			t.Fatalf("Type %s@%d: %v", e.typeID, e.version, err)
		}
		return ty.Own, ty.Shared
	}
	requester := func(e evalItem) string {
		if e.createdOnBehalfOf != "" {
			return e.createdOnBehalfOf
		}
		return e.createdBy
	}
	// Their own request: in a container whose Type owns its Type.
	ownRequest := func(e evalItem) bool {
		if e.container == nil || requester(e) != who.Actor {
			return false
		}
		own, _ := declares(items[*e.container])
		return slices.Contains(own, e.typeID)
	}
	if own, _ := declares(it); len(own) > 0 {
		return true // a portal
	}
	if ownRequest(it) {
		return true
	}
	if it.container == nil {
		return false
	}
	c := items[*it.container]
	_, shared := declares(c)
	return slices.Contains(shared, it.typeID) && ownRequest(c)
}

func (p *custProp) check(t *testing.T) {
	t.Helper()
	w := p.w
	items := p.loadItems(t)
	open, access := expectedAccessIn(t, w.ns)
	everyone := append(slices.Clone(p.staff), p.customers...)
	for _, who := range everyone {
		var want []uuid.UUID
		for id := range items {
			if p.visible(t, items, open, access, who, id) {
				want = append(want, id)
			}
		}
		// Get: the predicate form.
		for id := range items {
			got, exp := w.sees(t, who, id), slices.Contains(want, id)
			if got != exp {
				it := items[id]
				t.Errorf("Get(%s, %s@%d %s): visible = %v, evaluator says %v", who.Actor, it.typeID, it.version, id, got, exp)
			}
		}
		// Search: the relation form, two at a time.
		if got := w.searchAll(t, who, "", anyType, 2); !sameIDs(got, want) {
			t.Errorf("Search(%s): %d Items, evaluator says %d", who.Actor, len(got), len(want))
		}
		// List: every container, and the top level.
		var top []uuid.UUID
		for _, id := range want {
			if items[id].container == nil {
				top = append(top, id)
			}
		}
		if got := w.listAll(t, who, nil, 2); !sameIDs(got, top) {
			t.Errorf("List(%s, top level): %d Items, evaluator says %d", who.Actor, len(got), len(top))
		}
		for c := range items {
			_, err := w.k.List(testCtx(t), itemservice.ListRequest{Principal: who, ContainerID: &c, Limit: 1})
			if !slices.Contains(want, c) {
				if !errors.Is(err, itemservice.ErrNotFound) {
					t.Errorf("List(%s, a container they cannot see): %v; want ErrNotFound", who.Actor, err)
				}
				continue
			}
			var children []uuid.UUID
			for _, id := range want {
				if items[id].container != nil && *items[id].container == c {
					children = append(children, id)
				}
			}
			if got := w.listAll(t, who, &c, 2); !sameIDs(got, children) {
				t.Errorf("List(%s, %s@%d): %d children, evaluator says %d", who.Actor, items[c].typeID, items[c].version, len(got), len(children))
			}
		}
	}
}

// expectedAccessIn is expectedAccess (scope_access_test.go) for one
// namespace: from parent_id, narrows and scope_readers alone.
func expectedAccessIn(t *testing.T, ns string) (open map[uuid.UUID]bool, access map[uuid.UUID][]string) {
	t.Helper()
	type node struct {
		parent  *uuid.UUID
		narrows bool
	}
	nodes := map[uuid.UUID]node{}
	rows, err := pool.Query(testCtx(t), `select id, parent_id, narrows from scopes where namespace_id = $1`, ns)
	if err != nil {
		t.Fatalf("read scopes: %v", err)
	}
	for rows.Next() {
		var (
			id uuid.UUID
			n  node
		)
		if err := rows.Scan(&id, &n.parent, &n.narrows); err != nil {
			t.Fatalf("scan: %v", err)
		}
		nodes[id] = n
	}
	rows.Close()
	readers := map[uuid.UUID]map[string]bool{}
	rows, err = pool.Query(testCtx(t),
		`select r.scope_id, r.principal from scope_readers r join scopes s on s.id = r.scope_id where s.namespace_id = $1`, ns)
	if err != nil {
		t.Fatalf("read scope_readers: %v", err)
	}
	for rows.Next() {
		var (
			id uuid.UUID
			q  string
		)
		if err := rows.Scan(&id, &q); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if readers[id] == nil {
			readers[id] = map[string]bool{}
		}
		readers[id][q] = true
	}
	rows.Close()
	open, access = map[uuid.UUID]bool{}, map[uuid.UUID][]string{}
	for id := range nodes {
		var allowed map[string]bool
		for cur := &id; cur != nil; cur = nodes[*cur].parent {
			if !nodes[*cur].narrows {
				continue
			}
			if allowed == nil {
				allowed = maps.Clone(readers[*cur])
				if allowed == nil {
					allowed = map[string]bool{}
				}
				continue
			}
			for q := range allowed {
				if !readers[*cur][q] {
					delete(allowed, q)
				}
			}
		}
		open[id] = allowed == nil
		if allowed != nil {
			access[id] = slices.Sorted(maps.Keys(allowed))
		}
	}
	return open, access
}
