// Tests for proof of concept Step 1: search filters on links and principal
// fields, and principal fields themselves.
//
// A link filter keeps Items linked to or from an Item, by the name the
// RESULT Items see: outgoing takes a declared name, incoming an inverse
// name; the name matches every Type that declares it in that direction. The
// filter's Item must be one the caller can see (else NOT_FOUND); the results
// are only Items the caller can see.
//
// A principal field holds user:<id> or agent:<id>. It grants nothing. "me"
// in an equality filter on one is the caller: for an agent, the user it
// acts for, else the agent itself.
package itemservice_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

func (w linkWorld) search(t *testing.T, p item.Principal, f itemservice.Filter) ([]uuid.UUID, error) {
	t.Helper()
	res, err := w.k.Search(testCtx(t), itemservice.SearchRequest{Principal: p, Filter: f, Limit: 500})
	if err != nil {
		return nil, err
	}
	return idsOf(res.Items), nil
}

func (w linkWorld) mustSearch(t *testing.T, p item.Principal, f itemservice.Filter) []uuid.UUID {
	t.Helper()
	ids, err := w.search(t, p, f)
	if err != nil {
		t.Fatalf("Search %+v as %s: %v", f, p.Actor, err)
	}
	return ids
}

func assertSameIDs(t *testing.T, what string, got []uuid.UUID, want ...uuid.UUID) {
	t.Helper()
	if !sameIDs(got, want) {
		t.Errorf("%s = %v, want %v (any order)", what, got, want)
	}
}

func linked(name string, dir item.LinkDirection, id uuid.UUID) []itemservice.LinkFilter {
	return []itemservice.LinkFilter{{LinkType: name, Direction: dir, ItemID: id}}
}

// "Items that block y1" and "Items blocked by x", by the names the results
// see. Every Type that declares the name matches.
func TestSearch_LinkFilter(t *testing.T) {
	w := newLinkWorld(t)
	x, z, y1, y2, unrelated := w.open(t), w.open(t), w.open(t), w.open(t), w.open(t)
	w.mustLink(t, w.ana, x, "blocks", y1)
	w.mustLink(t, w.ana, x, "blocks", y2)
	w.mustLink(t, w.ana, z, "blocks", y1)
	w.mustLink(t, w.ana, x, "about", unrelated)
	res, err := w.k.Create(testCtx(t), itemservice.CreateRequest{ // another Type that declares blocks
		IdempotencyKey: newKey(), Principal: w.ana, TypeID: w.other, TypeVersion: 1,
		Fields: map[string]any{"f_title": "other"},
	})
	if err != nil {
		t.Fatalf("create other: %v", err)
	}
	o := res.ItemID
	w.mustLink(t, w.ana, o, "blocks", y1)

	assertSameIDs(t, "blocks y1",
		w.mustSearch(t, w.ana, itemservice.Filter{Links: linked("blocks", item.LinkOutgoing, y1)}), x, z, o)
	assertSameIDs(t, "blocked_by x",
		w.mustSearch(t, w.ana, itemservice.Filter{Links: linked("blocked_by", item.LinkIncoming, x)}), y1, y2)
	assertSameIDs(t, "blocks y1, of the world's Type only",
		w.mustSearch(t, w.ana, itemservice.Filter{TypeIDs: []string{w.typ}, Links: linked("blocks", item.LinkOutgoing, y1)}), x, z)
	assertSameIDs(t, "mentioned_by x",
		w.mustSearch(t, w.ana, itemservice.Filter{Links: linked("mentioned_by", item.LinkIncoming, x)}), unrelated)
	// Two link conditions: both must hold.
	both := append(linked("blocks", item.LinkOutgoing, y1), linked("blocks", item.LinkOutgoing, y2)...)
	assertSameIDs(t, "blocks y1 and y2", w.mustSearch(t, w.ana, itemservice.Filter{Links: both}), x)

	// A name no Type declares in that direction (unknown, or known only in
	// the other direction) is refused: Types only grow, so a refused name
	// can only start working later, never stop. It catches typos.
	for _, f := range [][]itemservice.LinkFilter{
		linked("blocked_by", item.LinkOutgoing, x),
		linked("blocks", item.LinkIncoming, x),
		linked("no_such_link", item.LinkOutgoing, y1),
		append(linked("blocks", item.LinkOutgoing, y1), linked("blokcs", item.LinkOutgoing, y2)...),
	} {
		_, err := w.search(t, w.ana, itemservice.Filter{Links: f})
		assertInvalidAt(t, fmt.Sprintf("%+v", f[len(f)-1]), err, "filter.links")
	}
	_, err = w.search(t, w.ana, itemservice.Filter{Links: append(linked("blocks", item.LinkOutgoing, y1),
		linked("blokcs", item.LinkOutgoing, y2)...)})
	var ie *itemservice.InvalidError
	if errors.As(err, &ie) && ie.Path != "filter.links[1]" {
		t.Errorf("the typo in the second condition is refused at %q, want filter.links[1]", ie.Path)
	}

	// With words, and in List.
	assertSameIDs(t, "words + blocked_by x", searchLinked(t, w, x), y1, y2)
	list, err := w.k.List(testCtx(t), itemservice.ListRequest{
		Principal: w.ana, Filter: itemservice.Filter{Links: linked("blocked_by", item.LinkIncoming, x)}, Limit: 500,
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	assertSameIDs(t, "List at the top level, blocked_by x", idsOf(list.Items), y1, y2)
}

// searchLinked searches y1's and y2's titles' common word "linked" with a
// link filter.
func searchLinked(t *testing.T, w linkWorld, x uuid.UUID) []uuid.UUID {
	t.Helper()
	res, err := w.k.Search(testCtx(t), itemservice.SearchRequest{
		Principal: w.ana, Query: "linked", Limit: 500,
		Filter: itemservice.Filter{Links: linked("blocked_by", item.LinkIncoming, x)},
	})
	if err != nil {
		t.Fatalf("Search with words: %v", err)
	}
	return idsOf(res.Items)
}

// A malformed link condition is INVALID at filter.links[i].
func TestSearch_LinkFilter_Malformed(t *testing.T) {
	w := newLinkWorld(t)
	x := w.open(t)
	for name, lf := range map[string]itemservice.LinkFilter{
		"no name":         {Direction: item.LinkOutgoing, ItemID: x},
		"no direction":    {LinkType: "blocks", ItemID: x},
		"a bad direction": {LinkType: "blocks", Direction: "sideways", ItemID: x},
		"no item":         {LinkType: "blocks", Direction: item.LinkOutgoing},
	} {
		_, err := w.search(t, w.ana, itemservice.Filter{Links: []itemservice.LinkFilter{lf}})
		assertInvalidAt(t, name, err, "filter.links")
	}
}

// Visibility on both ends: the filter's Item must be visible (else
// NOT_FOUND, like a missing one), and results are only what the caller
// sees.
func TestSearch_LinkFilter_Visibility(t *testing.T) {
	w := newLinkWorld(t)
	y := w.open(t)
	visible := w.open(t)
	hidden := w.make(t, w.ana, "s_open", itemOpts{narrow: readers(w.ana)})
	w.mustLink(t, w.ana, visible, "blocks", y)
	w.mustLink(t, w.ana, hidden, "blocks", y)
	w.mustLink(t, w.ana, hidden, "blocks", visible)

	assertSameIDs(t, "blocks y as ana", w.mustSearch(t, w.ana, itemservice.Filter{Links: linked("blocks", item.LinkOutgoing, y)}), visible, hidden)
	assertSameIDs(t, "blocks y as bob", w.mustSearch(t, w.bob, itemservice.Filter{Links: linked("blocks", item.LinkOutgoing, y)}), visible)

	_, err := w.search(t, w.bob, itemservice.Filter{Links: linked("blocked_by", item.LinkIncoming, hidden)})
	assertNotFound(t, "bob filters on a hidden Item", err)
	_, err = w.search(t, w.ana, itemservice.Filter{Links: linked("blocked_by", item.LinkIncoming, uuid.New())})
	assertNotFound(t, "a filter on a missing Item", err)

	// For an agent, an Item outside its session is hidden.
	box := w.open(t)
	agent := scoped(t, w.k, w.ana, []uuid.UUID{box}, item.PowerRead)
	_, err = w.search(t, agent, itemservice.Filter{Links: linked("blocks", item.LinkOutgoing, y)})
	assertNotFound(t, "an agent filters on an Item outside its session", err)
}

// --- principal fields ---

// A principal field takes user:<id> or agent:<id>, on create and update.
func TestPrincipalField_Checked(t *testing.T) {
	w := newLinkWorld(t)
	for _, v := range []any{"bob", "group:support", "system:timer", "hook:x:y", "me", "user:", 7} {
		_, err := w.k.Create(testCtx(t), itemservice.CreateRequest{
			IdempotencyKey: newKey(), Principal: w.ana, TypeID: w.typ, TypeVersion: 1,
			Fields: map[string]any{"f_assignee": v},
		})
		assertInvalidField(t, fmt.Sprintf("create with f_assignee = %v", v), err, "f_assignee")
	}
	x := w.make(t, w.ana, "s_open", itemOpts{fields: map[string]any{"f_assignee": w.bob.Actor}})
	_, err := w.k.Update(testCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: w.ana, ItemID: x, Fields: map[string]any{"f_assignee": "system:timer"},
	})
	assertInvalidField(t, "update to system:timer", err, "f_assignee")
	update(t, w.k, x, map[string]any{"f_assignee": "agent:triage-bot"})
	update(t, w.k, x, map[string]any{"f_assignee": nil})
}

// Assigning grants nothing: an Item narrowed to ana and assigned to bob
// stays hidden from bob, and bob's "assigned to me" leaves it out.
func TestPrincipalField_GrantsNothing(t *testing.T) {
	w := newLinkWorld(t)
	secret := w.make(t, w.ana, "s_open", itemOpts{narrow: readers(w.ana), fields: map[string]any{"f_assignee": w.bob.Actor}})
	mine := w.make(t, w.ana, "s_open", itemOpts{fields: map[string]any{"f_assignee": w.bob.Actor}})
	assertHidden(t, w.k, w.bob, secret)
	assertSameIDs(t, "bob's assigned to me", w.mustSearch(t, w.bob, meFilter(w.typ)), mine)
}

func meFilter(typ string) itemservice.Filter {
	return itemservice.Filter{TypeIDs: []string{typ}, Fields: []itemservice.FieldFilter{{Field: "f_assignee", Eq: "me"}}}
}

// "me" is the caller: a user, the user an agent acts for, or an agent
// acting for itself. A principal can also be named.
func TestSearch_AssignedToMe(t *testing.T) {
	w := newLinkWorld(t)
	agentSelf := in(item.Principal{Actor: "agent:self-" + uuid.NewString()[:8]}, "default")
	agentSelf = withSession(t, w.k, agentSelf)
	toAna := w.make(t, w.ana, "s_open", itemOpts{fields: map[string]any{"f_assignee": w.ana.Actor}})
	toBob := w.make(t, w.ana, "s_open", itemOpts{fields: map[string]any{"f_assignee": w.bob.Actor}})
	toAgent := w.make(t, w.ana, "s_open", itemOpts{fields: map[string]any{"f_assignee": agentSelf.Actor}})
	w.make(t, w.ana, "s_open", itemOpts{}) // assigned to nobody

	assertSameIDs(t, "ana's", w.mustSearch(t, w.ana, meFilter(w.typ)), toAna)
	assertSameIDs(t, "bob's", w.mustSearch(t, w.bob, meFilter(w.typ)), toBob)
	forAna := agentFor(t, w.k, w.ana)
	forAna.Namespace = "default"
	assertSameIDs(t, "an agent acting for ana", w.mustSearch(t, forAna, meFilter(w.typ)), toAna)
	assertSameIDs(t, "an agent acting for itself", w.mustSearch(t, agentSelf, meFilter(w.typ)), toAgent)

	named := itemservice.Filter{TypeIDs: []string{w.typ}, Fields: []itemservice.FieldFilter{{Field: "f_assignee", Eq: w.bob.Actor}}}
	assertSameIDs(t, "assigned to bob, by name", w.mustSearch(t, w.ana, named), toBob)

	// Only a principal or "me" on a principal field; equality only.
	for name, ff := range map[string]itemservice.FieldFilter{
		"a bare name":  {Field: "f_assignee", Eq: "bob"},
		"a group":      {Field: "f_assignee", Eq: "group:support"},
		"not a string": {Field: "f_assignee", Eq: 3},
		"a range":      {Field: "f_assignee", Gt: "user:a"},
	} {
		_, err := w.search(t, w.ana, itemservice.Filter{TypeIDs: []string{w.typ}, Fields: []itemservice.FieldFilter{ff}})
		assertInvalidAt(t, name, err, "filter.fields")
	}
	// On a string field "me" is just a string.
	if _, err := w.search(t, w.ana, itemservice.Filter{TypeIDs: []string{w.typ},
		Fields: []itemservice.FieldFilter{{Field: "f_title", Eq: "me"}}}); err != nil {
		t.Errorf(`"me" on a string field: %v`, err)
	}
}

// A field that is a string in one version and a principal in another
// filters across both (eq only); the value must then be a principal or "me".
func TestSearch_StringAndPrincipalVersionsFilterTogether(t *testing.T) {
	k := itemservice.New(pool)
	ns := newNamespace(t, k)
	ana := in(person("ana"), ns)
	id := "who-" + uuid.NewString()[:8]
	for v, kind := range map[int]string{1: "string", 2: "principal"} {
		ty, err := typedef.Parse(fmt.Appendf(nil, "id: %s\nversion: %d\nfields:\n  - {id: f_who, type: %s}\n", id, v, kind))
		if err != nil {
			t.Fatalf("parse v%d: %v", v, err)
		}
		if err := k.RegisterType(testCtx(t), ty); err != nil {
			t.Fatalf("register v%d: %v", v, err)
		}
	}
	mk := func(v int, who string) uuid.UUID {
		res, err := k.Create(testCtx(t), itemservice.CreateRequest{
			IdempotencyKey: newKey(), Principal: ana, TypeID: id, TypeVersion: v, Fields: map[string]any{"f_who": who},
		})
		if err != nil {
			t.Fatalf("create v%d: %v", v, err)
		}
		return res.ItemID
	}
	v1, v2 := mk(1, ana.Actor), mk(2, ana.Actor)
	mk(1, "someone else")
	filter := func(eq any) itemservice.Filter {
		return itemservice.Filter{TypeIDs: []string{id}, Fields: []itemservice.FieldFilter{{Field: "f_who", Eq: eq}}}
	}
	res, err := k.Search(testCtx(t), itemservice.SearchRequest{Principal: ana, Filter: filter("me"), Limit: 500})
	if err != nil {
		t.Fatalf("Search me: %v", err)
	}
	assertSameIDs(t, "me across versions", idsOf(res.Items), v1, v2)
	_, err = k.Search(testCtx(t), itemservice.SearchRequest{Principal: ana, Filter: filter("someone else")})
	assertInvalidAt(t, "a plain string on a field that is a principal in some version", err, "filter.fields")
	var ie *itemservice.InvalidError
	if !errors.As(err, &ie) {
		return
	}
	if strings.Contains(ie.Reason, "someone else") {
		t.Errorf("reason %q repeats the value", ie.Reason)
	}
}
