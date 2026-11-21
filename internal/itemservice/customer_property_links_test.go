// Property test for the fixes of the 2026-11-21 review of Jira part 2,
// step 4a: the customer rule with agents acting for customers, links, and
// words, against a naive evaluator that knows each Type version's
// customer_fields.
//
// Random portals and incidents (by customers, by staff for a customer, by
// staff alone), replies, notes and comments; a kiosk of tickets (kticket:
// `blocks` and the rollup over it listed for customers, `relates` and
// f_secret not) linked at random by staff; narrowings whose readers mix
// staff, customers and portal bots; inherits and moves. Visible fields hold
// shared words (alpha, bravo, charlie), the hidden f_secret holds zulu.
// At the end, for every principal (staff, customers, a portal bot per
// customer):
//
//   - Get agrees with the evaluator, and so do a ticket's links both ways
//     (names, ends, restricted), with links_truncated false;
//   - Search over every Type and List of every container hold exactly the
//     visible Items;
//   - a link condition on every name and direction, on every visible
//     ticket, finds exactly the evaluator's Items, or is refused as an
//     unknown name when the customer may not see the name;
//   - a word search finds exactly the visible Items whose fields the
//     principal sees hold the word.
//
// The whole test must stay under a minute.
package itemservice_test

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

const (
	linksPropertySeeds = 3
	linksPropertySteps = 70
)

var (
	propWords  = []string{"alpha", "bravo", "charlie"}
	secretWord = "zulu"
)

func TestProperty_CustomerRule_AgentsLinksAndWords(t *testing.T) {
	began := time.Now()
	for seed := range linksPropertySeeds {
		t.Run(fmt.Sprintf("seed %d", seed), func(t *testing.T) { runLinksProperty(t, uint64(seed)) })
	}
	if took := time.Since(began); took > time.Minute {
		t.Errorf("the property test took %v; it must stay under a minute", took)
	}
}

type linksProp struct {
	w         custWorld
	r         *rand.Rand
	staff     []item.Principal
	customers []item.Principal
	bots      []item.Principal // bots[i] acts for customers[i]
	portals   []uuid.UUID
	kiosk     uuid.UUID
	incidents []uuid.UUID
	tickets   []uuid.UUID
}

func runLinksProperty(t *testing.T, seed uint64) {
	w := newCustWorld(t)
	p := &linksProp{w: w, r: rand.New(rand.NewPCG(seed, 23)),
		staff:     []item.Principal{w.lea, w.sam, w.eve},
		customers: []item.Principal{w.kim, w.lee, customer("max", w.ns)},
		portals:   []uuid.UUID{w.desk, w.desk2, w.oldDesk},
	}
	for i, c := range p.customers {
		p.bots = append(p.bots, w.portalBot(t, fmt.Sprintf("agent:pb%d-%s", i, word()), c))
	}
	p.kiosk = w.mk(t, w.lea, "kiosk", 1, nil, map[string]any{"f_name": "kiosk " + p.word()})
	for step := range linksPropertySteps {
		var what string
		switch n := p.r.IntN(100); {
		case n < 15:
			what = "an incident"
			p.newIncident(t)
		case n < 30:
			what = "a ticket"
			p.newTicket(t)
		case n < 45:
			what = "a reply, a note or a comment"
			p.newChild(t)
		case n < 60:
			what = "a link"
			p.newLink(t)
		case n < 78:
			what = "narrow"
			p.narrow(t)
		case n < 88:
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

func (p *linksProp) pick(ids []uuid.UUID) uuid.UUID { return ids[p.r.IntN(len(ids))] }
func (p *linksProp) word() string                   { return propWords[p.r.IntN(len(propWords))] }
func (p *linksProp) customer() int                  { return p.r.IntN(len(p.customers)) }

func (p *linksProp) newIncident(t *testing.T) {
	t.Helper()
	desk := p.pick(p.portals[:2])
	req := itemservice.CreateRequest{IdempotencyKey: newKey(), TypeID: "incident", TypeVersion: 2, ContainerID: &desk,
		Fields: map[string]any{"f_title": p.word() + " " + word(), "f_priority": "P3"}}
	switch p.r.IntN(4) {
	case 0:
		req.Principal = p.customers[p.customer()]
	case 1:
		req.Principal = p.bots[p.customer()] // a portal bot files for its customer
	case 2:
		req.Principal, req.Requester = p.staff[0], p.customers[p.customer()].Actor
	default:
		req.Principal = p.staff[p.r.IntN(len(p.staff))]
		d := p.pick(p.portals)
		req.ContainerID = &d
	}
	res, err := p.w.k.Create(testCtx(t), req)
	if errors.Is(err, itemservice.ErrNotFound) || isRequesterRefusal(err) || errors.Is(err, itemservice.ErrApprovalUnavailable) ||
		errors.Is(err, itemservice.ErrNeedsApproval) {
		return // narrowed away from them, or the bot read narrower than it writes: fair refusals
	}
	if err != nil {
		t.Fatalf("create an incident (%s for %q): %v", req.Principal.Actor, req.Requester, err)
	}
	p.incidents = append(p.incidents, res.ItemID)
}

// newTicket: a customer's ticket in the kiosk (staff may add a secret), or
// a staff member's with a secret.
func (p *linksProp) newTicket(t *testing.T) {
	t.Helper()
	who := p.staff[0]
	fields := map[string]any{"f_title": p.word() + " " + word()}
	if p.r.IntN(2) == 0 {
		who = p.customers[p.customer()]
	} else if p.r.IntN(2) == 0 {
		fields["f_secret"] = secretWord + " " + p.word()
	}
	res, err := p.w.k.Create(testCtx(t), itemservice.CreateRequest{IdempotencyKey: newKey(), Principal: who, TypeID: "kticket",
		TypeVersion: 1, ContainerID: &p.kiosk, Fields: fields})
	if errors.Is(err, itemservice.ErrNotFound) {
		return
	}
	if err != nil {
		t.Fatalf("%s creates a ticket: %v", who.Actor, err)
	}
	p.tickets = append(p.tickets, res.ItemID)
	if item.IsKind(who.Actor, item.KindCustomer) && p.r.IntN(2) == 0 {
		if _, err := p.w.k.Update(testCtx(t), itemservice.UpdateRequest{IdempotencyKey: newKey(), Principal: p.staff[0],
			ItemID: res.ItemID, Fields: map[string]any{"f_secret": secretWord}}); err != nil {
			t.Fatalf("a secret on a customer's ticket: %v", err)
		}
	}
}

func (p *linksProp) newChild(t *testing.T) {
	t.Helper()
	if len(p.incidents) == 0 {
		return
	}
	in := p.pick(p.incidents)
	typ, version, fields := "reply", 1, map[string]any{"f_body": p.word()}
	switch p.r.IntN(3) {
	case 1:
		typ = "note"
		fields["f_body"] = secretWord
	case 2:
		typ, version, fields = "comment", 2, map[string]any{"f_body": secretWord, "f_public": true}
	}
	who := p.staff[0]
	if typ == "reply" {
		switch p.r.IntN(3) {
		case 0:
			who = p.customers[p.customer()]
		case 1:
			who = p.bots[p.customer()]
		}
	}
	_, err := p.w.k.Create(testCtx(t), itemservice.CreateRequest{IdempotencyKey: newKey(), Principal: who, TypeID: typ, TypeVersion: version,
		ContainerID: &in, Fields: fields})
	if who != p.staff[0] && (errors.Is(err, itemservice.ErrNotFound) || errors.Is(err, itemservice.ErrApprovalUnavailable)) {
		return
	}
	if err != nil {
		t.Fatalf("%s creates a %s: %v", who.Actor, typ, err)
	}
}

func (p *linksProp) newLink(t *testing.T) {
	t.Helper()
	if len(p.tickets) < 2 {
		return
	}
	a, b := p.pick(p.tickets), p.pick(p.tickets)
	if a == b {
		return
	}
	name := "blocks"
	if p.r.IntN(2) == 0 {
		name = "relates"
	}
	_, err := p.w.k.Link(testCtx(t), itemservice.LinkRequest{IdempotencyKey: newKey(), Principal: p.staff[0], SourceID: a, LinkType: name, TargetID: b})
	if err != nil && !errors.Is(err, itemservice.ErrRejected) {
		t.Fatalf("link: %v", err)
	}
}

func (p *linksProp) readers() []string {
	out := []string{p.staff[0].Actor}
	everyone := append(append(slices.Clone(p.staff[1:]), p.customers...), p.bots...)
	for _, q := range everyone {
		if p.r.IntN(2) == 0 {
			out = append(out, q.Actor)
		}
	}
	return out
}

func (p *linksProp) targets() []uuid.UUID {
	return append(append(append(slices.Clone(p.portals), p.kiosk), p.incidents...), p.tickets...)
}

func (p *linksProp) narrow(t *testing.T) {
	t.Helper()
	if _, err := p.w.k.Update(testCtx(t), itemservice.UpdateRequest{IdempotencyKey: newKey(), Principal: p.staff[0],
		ItemID: p.pick(p.targets()), Policy: &itemservice.PolicyPatch{Readers: p.readers()}}); err != nil {
		t.Fatalf("narrow: %v", err)
	}
}

func (p *linksProp) inherit(t *testing.T) {
	t.Helper()
	_, err := p.w.k.Update(testCtx(t), itemservice.UpdateRequest{IdempotencyKey: newKey(), Principal: p.staff[0],
		ItemID: p.pick(p.targets()), Policy: &itemservice.PolicyPatch{Inherit: true}})
	if err != nil && !errors.Is(err, itemservice.ErrInvalid) && !errors.Is(err, itemservice.ErrRejected) {
		t.Fatalf("inherit: %v", err)
	}
}

func (p *linksProp) moveIncident(t *testing.T) {
	t.Helper()
	if len(p.incidents) == 0 {
		return
	}
	to := p.pick(append(slices.Clone(p.portals), p.w.space))
	if _, err := p.w.k.Update(testCtx(t), itemservice.UpdateRequest{IdempotencyKey: newKey(), Principal: p.staff[0],
		ItemID: p.pick(p.incidents), Move: &itemservice.Move{To: &to, ConfirmWiden: true}}); err != nil {
		t.Fatalf("move: %v", err)
	}
}

// --- the evaluator ---

type linkRow struct {
	source, target uuid.UUID
	name           string
}

type linksWorld struct {
	items  map[uuid.UUID]evalItem
	fields map[uuid.UUID]map[string]any
	links  []linkRow
	open   map[uuid.UUID]bool
	access map[uuid.UUID][]string
	types  map[string]*typedef.Type // "id@version"
}

func (p *linksProp) load(t *testing.T) linksWorld {
	t.Helper()
	lw := linksWorld{items: map[uuid.UUID]evalItem{}, fields: map[uuid.UUID]map[string]any{}, types: map[string]*typedef.Type{}}
	rows, err := pool.Query(testCtx(t),
		`select id, scope_id, type_id, type_version, container_id, created_by, coalesce(created_on_behalf_of, ''), fields
		 from items where namespace_id = $1`, p.w.ns)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var e evalItem
		var f map[string]any
		if err := rows.Scan(&e.id, &e.scope, &e.typeID, &e.version, &e.container, &e.createdBy, &e.createdOnBehalfOf, &f); err != nil {
			t.Fatal(err)
		}
		lw.items[e.id], lw.fields[e.id] = e, f
		key := fmt.Sprintf("%s@%d", e.typeID, e.version)
		if lw.types[key] == nil {
			ty, err := p.w.k.Type(testCtx(t), e.typeID, e.version)
			if err != nil {
				t.Fatal(err)
			}
			lw.types[key] = ty
		}
	}
	rows.Close()
	rows, err = pool.Query(testCtx(t),
		`select l.source_id, l.target_id, l.link_type from links l join items i on i.id = l.source_id where i.namespace_id = $1`, p.w.ns)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var l linkRow
		if err := rows.Scan(&l.source, &l.target, &l.name); err != nil {
			t.Fatal(err)
		}
		lw.links = append(lw.links, l)
	}
	rows.Close()
	lw.open, lw.access = expectedAccessIn(t, p.w.ns)
	return lw
}

func (lw linksWorld) typeOf(id uuid.UUID) *typedef.Type {
	e := lw.items[id]
	return lw.types[fmt.Sprintf("%s@%d", e.typeID, e.version)]
}

// customerOf is the customer a principal is or acts for.
func customerOfP(who item.Principal) string {
	if item.IsKind(who.Actor, item.KindCustomer) {
		return who.Actor
	}
	if item.IsKind(who.Actor, item.KindAgent) && item.IsKind(who.OnBehalfOf, item.KindCustomer) {
		return who.OnBehalfOf
	}
	return ""
}

// visible is the rule written out: the scope for the actor and the person
// it acts for, then for a customer the customer rule.
func (lw linksWorld) visible(who item.Principal, id uuid.UUID) bool {
	it := lw.items[id]
	if !lw.open[it.scope] {
		if !slices.Contains(lw.access[it.scope], who.Actor) {
			return false
		}
		if who.OnBehalfOf != "" && !slices.Contains(lw.access[it.scope], who.OnBehalfOf) {
			return false
		}
	}
	cust := customerOfP(who)
	if cust == "" {
		return true
	}
	requester := func(e evalItem) string {
		if e.createdOnBehalfOf != "" {
			return e.createdOnBehalfOf
		}
		return e.createdBy
	}
	ownRequest := func(e evalItem) bool {
		return e.container != nil && requester(e) == cust && slices.Contains(lw.typeOf(*e.container).Own, e.typeID)
	}
	if len(lw.typeOf(id).Own) > 0 || ownRequest(it) {
		return true
	}
	if it.container == nil {
		return false
	}
	c := lw.items[*it.container]
	return slices.Contains(lw.typeOf(c.id).Shared, it.typeID) && ownRequest(c)
}

// expectLinks is what Get shows of an Item's links, as sorted strings.
func (lw linksWorld) expectLinks(who item.Principal, id uuid.UUID) []string {
	cust := customerOfP(who) != ""
	var out []string
	for _, l := range lw.links {
		switch {
		case l.source == id:
			seen := lw.visible(who, l.target)
			if cust {
				if seen && lw.typeOf(id).CustomerSees(l.name) {
					out = append(out, l.name+"|out|"+l.target.String())
				}
				continue
			}
			if seen {
				out = append(out, l.name+"|out|"+l.target.String())
			} else {
				out = append(out, l.name+"|out|restricted")
			}
		case l.target == id:
			if !lw.visible(who, l.source) || (cust && !lw.typeOf(l.source).CustomerSees(l.name)) {
				continue
			}
			lt, _ := lw.typeOf(l.source).Link(l.name)
			out = append(out, lt.Inverse+"|in|"+l.source.String())
		}
	}
	sort.Strings(out)
	return out
}

func gotLinks(it item.Item) []string {
	var out []string
	for _, l := range it.Links {
		other := l.Other.String()
		if l.Restricted {
			other = "restricted"
		}
		dir := "out"
		if l.Direction == item.LinkIncoming {
			dir = "in"
		}
		out = append(out, l.LinkType+"|"+dir+"|"+other)
	}
	sort.Strings(out)
	return out
}

// matchesWord reports whether the Item's fields the principal sees hold
// the word as a token (the 'simple' configuration splits on spaces here).
func (lw linksWorld) matchesWord(who item.Principal, id uuid.UUID, word string) bool {
	ty, cust := lw.typeOf(id), customerOfP(who) != ""
	for f, v := range lw.fields[id] {
		s, ok := v.(string)
		if !ok || (cust && !ty.CustomerSees(f)) {
			continue
		}
		if slices.Contains(strings.Fields(strings.ToLower(s)), word) {
			return true
		}
	}
	return false
}

func (p *linksProp) check(t *testing.T) {
	t.Helper()
	w, lw := p.w, p.load(t)
	everyone := append(append(slices.Clone(p.staff), p.customers...), p.bots...)
	for _, who := range everyone {
		var want []uuid.UUID
		for id := range lw.items {
			if lw.visible(who, id) {
				want = append(want, id)
			}
		}
		// Get, and links on what is visible.
		for id := range lw.items {
			it, err := w.k.Get(testCtx(t), who, id)
			if err != nil && !errors.Is(err, itemservice.ErrNotFound) {
				t.Fatalf("Get(%s): %v", who.Actor, err)
			}
			if got, exp := err == nil, slices.Contains(want, id); got != exp {
				t.Errorf("Get(%s, %s@%d): visible = %v, evaluator says %v", who.Actor, lw.items[id].typeID, lw.items[id].version, got, exp)
				continue
			}
			if err != nil {
				continue
			}
			if g, e := gotLinks(it), lw.expectLinks(who, id); !slices.Equal(g, e) {
				t.Errorf("Get(%s, %s) links = %v, evaluator says %v", who.Actor, lw.items[id].typeID, g, e)
			}
			if it.LinksTruncated {
				t.Errorf("Get(%s): links_truncated with %d links", who.Actor, len(it.Links))
			}
		}
		if got := w.searchAll(t, who, "", anyType, 3); !sameIDs(got, want) {
			t.Errorf("Search(%s): %d Items, evaluator says %d", who.Actor, len(got), len(want))
		}
		for c := range lw.items {
			if !slices.Contains(want, c) {
				continue
			}
			var children []uuid.UUID
			for _, id := range want {
				if lw.items[id].container != nil && *lw.items[id].container == c {
					children = append(children, id)
				}
			}
			if got := w.listAll(t, who, &c, 3); !sameIDs(got, children) {
				t.Errorf("List(%s, %s): %d children, evaluator says %d", who.Actor, lw.items[c].typeID, len(got), len(children))
			}
		}
		p.checkLinkConditions(t, lw, who, want)
		for _, word := range append(slices.Clone(propWords), secretWord) {
			var exp []uuid.UUID
			for _, id := range want {
				if lw.matchesWord(who, id, word) {
					exp = append(exp, id)
				}
			}
			if got := w.searchAll(t, who, word, itemservice.Filter{}, 50); !sameIDs(got, exp) {
				t.Errorf("Search(%s, %q): %d Items, evaluator says %d", who.Actor, word, len(got), len(exp))
			}
		}
	}
}

// checkLinkConditions asks, on every ticket the principal sees, for each
// link name in each direction.
func (p *linksProp) checkLinkConditions(t *testing.T, lw linksWorld, who item.Principal, want []uuid.UUID) {
	t.Helper()
	cust := customerOfP(who) != ""
	conds := []struct {
		name, declared string
		dir            item.LinkDirection
	}{
		{"blocks", "blocks", item.LinkOutgoing}, {"relates", "relates", item.LinkOutgoing},
		{"blocked_by", "blocks", item.LinkIncoming}, {"related_to", "relates", item.LinkIncoming},
	}
	for _, x := range p.tickets {
		if !slices.Contains(want, x) {
			continue
		}
		for _, c := range conds {
			listed := lw.types["kticket@1"].CustomerSees(c.declared)
			var exp []uuid.UUID
			for _, l := range lw.links {
				if l.name != c.declared {
					continue
				}
				// Outgoing: the Items m with m -> x; incoming: x -> m.
				m, src := l.source, l.source
				if c.dir == item.LinkOutgoing && l.target != x {
					continue
				}
				if c.dir == item.LinkIncoming {
					if l.source != x {
						continue
					}
					m = l.target
				}
				if !slices.Contains(want, m) || (cust && !lw.typeOf(src).CustomerSees(l.name)) {
					continue
				}
				exp = append(exp, m)
			}
			res, err := p.w.k.Search(testCtx(t), itemservice.SearchRequest{Principal: who, Limit: 500, Filter: itemservice.Filter{
				TypeIDs: []string{"kticket"}, Links: []itemservice.LinkFilter{{LinkType: c.name, Direction: c.dir, ItemID: x}}}})
			if cust && !listed {
				var inv *itemservice.InvalidError
				if !errors.As(err, &inv) {
					t.Errorf("%s: a condition on %s (not listed): %v; want refused as an unknown name", who.Actor, c.name, err)
				}
				continue
			}
			if err != nil {
				t.Fatalf("%s: a condition on %s: %v", who.Actor, c.name, err)
			}
			var got []uuid.UUID
			for _, it := range res.Items {
				got = append(got, it.ID)
			}
			if !sameIDs(got, exp) {
				t.Errorf("%s: Items with %s %s: %d, evaluator says %d", who.Actor, c.name, x, len(got), len(exp))
			}
		}
	}
}
