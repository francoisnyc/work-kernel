// Tests for the Jira proof of concept, part 1: order_by on List and Search
// (docs/design.md, "Jira proof of concept").
//
//   - A page is sorted by one declared field, ascending or descending.
//     Items without a value come last in both directions; ties are broken
//     by Item ID, ascending in both.
//   - Strings and enum names compare byte by byte (COLLATE "C"), never by
//     the database's locale: rank keys depend on it. Ints compare as
//     numbers, dates as dates.
//   - Paging stays exact under that order: the token carries the sort
//     value, and the pages cover every Item once, with ties and unset
//     values across page boundaries. A token is bound to its order.
//   - The field may be declared by several Types: it sorts across the
//     selected Types, or across every Type that declares it when none is
//     selected, and is refused only when the declarations disagree on its
//     type. Items of a Type that does not declare it sort as unset.
package itemservice_test

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// --- the world ---

// orderTypeYAML declares the fields these tests sort and filter by. The
// field IDs carry a suffix of their own (SFX): a field named without a Type
// is looked up in every registered Type, and the test database holds the
// Types of every other test.
const orderTypeYAML = `
id: TYPE
version: 1
fields:
  - {id: f_s_SFX, type: string}
  - {id: f_n_SFX, type: int}
  - {id: f_d_SFX, type: date}
  - {id: f_e_SFX, type: enum, options: [OPTIONS]}
  - {id: f_b_SFX, type: bool}
  - {id: f_p_SFX, type: principal}
  - {id: f_x_SFX, type: XTYPE}
  - {id: f_w_SFX, type: WTYPE}
`

// orderWorld: a namespace of its own, a user, and three Types.
//   - a and b declare the same fields. Their enums have different options
//     (one type all the same); f_x is an int in a and a string in b (the
//     declarations disagree); f_w is a string in a and a principal in b
//     (one stored type for equality, as Step 1 decided).
//   - plain declares none of them.
type orderWorld struct {
	k           *itemservice.Service
	user        item.Principal
	a, b, plain string
	sfx         string
}

func newOrderWorld(t *testing.T) orderWorld {
	t.Helper()
	k := itemservice.New(pool)
	sfx := strings.ReplaceAll(uuid.NewString(), "-", "")[:10]
	w := orderWorld{
		k: k, user: in(person("sorter"), newNamespace(t, k)), sfx: sfx,
		a: "orda-" + sfx, b: "ordb-" + sfx, plain: "ordp-" + sfx,
	}
	register := func(src string) {
		t.Helper()
		ty, err := typedef.Parse([]byte(src))
		if err != nil {
			t.Fatalf("parse order test Type: %v", err)
		}
		if err := k.RegisterType(testCtx(t), ty); err != nil {
			t.Fatalf("register order test Type: %v", err)
		}
	}
	register(strings.NewReplacer("TYPE", w.a, "SFX", sfx, "OPTIONS", "P1, P3", "XTYPE", "int", "WTYPE", "string").Replace(orderTypeYAML))
	register(strings.NewReplacer("TYPE", w.b, "SFX", sfx, "OPTIONS", "P2, P4", "XTYPE", "string", "WTYPE", "principal").Replace(orderTypeYAML))
	register(fmt.Sprintf("id: %s\nversion: 1\nfields:\n  - {id: f_t_%s, type: string}\n", w.plain, sfx))
	return w
}

// f is the world's ID for a field: f("s") is its string field.
func (w orderWorld) f(name string) string { return "f_" + name + "_" + w.sfx }

// makeIn creates an Item of typ, in a container when one is given, with
// the fields named by their short names.
func (w orderWorld) makeIn(t *testing.T, typ string, container *uuid.UUID, fields map[string]any) uuid.UUID {
	t.Helper()
	named := map[string]any{}
	for name, v := range fields {
		named[w.f(name)] = v
	}
	res, err := w.k.Create(testCtx(t), itemservice.CreateRequest{
		IdempotencyKey: newKey(), Principal: w.user, TypeID: typ, TypeVersion: 1, Fields: named, ContainerID: container,
	})
	if err != nil {
		t.Fatalf("create %s %v: %v", typ, fields, err)
	}
	return res.ItemID
}

func (w orderWorld) make(t *testing.T, typ string, fields map[string]any) uuid.UUID {
	t.Helper()
	return w.makeIn(t, typ, nil, fields)
}

// box makes a container (an Item of the plain Type).
func (w orderWorld) box(t *testing.T) *uuid.UUID {
	t.Helper()
	id := w.make(t, w.plain, nil)
	return &id
}

func asc(field string) *itemservice.OrderBy { return &itemservice.OrderBy{Field: field} }
func desc(field string) *itemservice.OrderBy {
	return &itemservice.OrderBy{Field: field, Desc: true}
}

// everything is a filter that matches every Item of the world's namespace
// without naming a Type: none of its Types has a lifecycle.
var everything = itemservice.Filter{Categories: []string{"none"}}

// searchPage and listPage read one page as the world's user.
func (w orderWorld) searchPage(f itemservice.Filter, o *itemservice.OrderBy, size int) func(string) ([]uuid.UUID, string, error) {
	return func(token string) ([]uuid.UUID, string, error) {
		res, err := w.k.Search(context.Background(), itemservice.SearchRequest{
			Principal: w.user, Filter: f, OrderBy: o, Limit: size, PageToken: token,
		})
		return idsOf(res.Items), res.NextPageToken, err
	}
}

func (w orderWorld) listPage(box *uuid.UUID, f itemservice.Filter, o *itemservice.OrderBy, size int) func(string) ([]uuid.UUID, string, error) {
	return func(token string) ([]uuid.UUID, string, error) {
		res, err := w.k.List(context.Background(), itemservice.ListRequest{
			Principal: w.user, ContainerID: box, Filter: f, OrderBy: o, Limit: size, PageToken: token,
		})
		return idsOf(res.Items), res.NextPageToken, err
	}
}

// --- the expected order, computed here ---

// sortable is one Item and its sort value; nil is "no value".
type sortable struct {
	id uuid.UUID
	v  any
}

// compareValues orders two values of one kind as the kernel must: strings
// byte by byte, ints as numbers.
func compareValues(a, b any) int {
	switch x := a.(type) {
	case string:
		return strings.Compare(x, b.(string)) // bytewise in Go
	case int:
		return x - b.(int)
	}
	panic(fmt.Sprintf("compareValues: %T", a))
}

// inOrder is the order the kernel promises: by value (reversed when
// descending), Items without a value last either way, ties by ID ascending
// either way.
func inOrder(items []sortable, descending bool) []uuid.UUID {
	sorted := slices.Clone(items)
	slices.SortFunc(sorted, func(a, b sortable) int {
		switch {
		case a.v == nil && b.v != nil:
			return 1
		case a.v != nil && b.v == nil:
			return -1
		case a.v != nil:
			c := compareValues(a.v, b.v)
			if descending {
				c = -c
			}
			if c != 0 {
				return c
			}
		}
		return bytes.Compare(a.id[:], b.id[:])
	})
	ids := make([]uuid.UUID, len(sorted))
	for i, s := range sorted {
		ids[i] = s.id
	}
	return ids
}

func assertOrder(t *testing.T, what string, got, want []uuid.UUID) {
	t.Helper()
	if slices.Equal(got, want) {
		return
	}
	if !sameIDs(got, want) {
		t.Errorf("%s: %d Items, want %d: some are missing or repeated\n got %v\nwant %v", what, len(got), len(want), got, want)
		return
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("%s: the right Items in the wrong order, first at position %d\n got %v\nwant %v", what, i, got, want)
			return
		}
	}
}

// --- one field of each sortable type ---

// Each sortable type in its own order, on List and on Search, both ways,
// with two Items that have no value: they come last either way.
func TestOrderBy_EachFieldType(t *testing.T) {
	cases := []struct {
		name, field string
		// ascending is the values in the order an ascending sort gives.
		ascending []any
	}{
		// Byte order: digits, upper case, lower case, then non-ASCII. A
		// locale would give 10 9 a A aa b B é Z or similar.
		{"string, byte by byte", "s", []any{"10", "9", "A", "B", "Z", "a", "aa", "b", "é"}},
		// Numbers, not their digits: as text, 10 and 100 sort before 9.
		{"int, as numbers", "n", []any{-1, 0, 9, 10, 100}},
		{"date", "d", []any{"2025-12-31", "2026-01-15", "2026-12-04"}},
		// By name: P1 before P3, whatever order the Type lists its options in.
		{"enum, by name", "e", []any{"P1", "P3"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// A world of its own: the search below then finds these Items
			// only (the box is of another Type).
			w := newOrderWorld(t)
			box := w.box(t)
			var items []sortable
			// Created in an order that is neither ascending nor descending.
			if len(c.ascending)%7 == 0 {
				t.Fatal("test bug: the creation order would repeat values")
			}
			for i := range c.ascending {
				v := c.ascending[(i*7+2)%len(c.ascending)]
				items = append(items, sortable{w.makeIn(t, w.a, box, map[string]any{c.field: v}), v})
			}
			for range 2 {
				items = append(items, sortable{w.makeIn(t, w.a, box, nil), nil})
			}
			ofType := itemservice.Filter{TypeIDs: []string{w.a}}
			for _, descending := range []bool{false, true} {
				o := &itemservice.OrderBy{Field: w.f(c.field), Desc: descending}
				want := inOrder(items, descending)
				got, _ := pageAll(t, w.listPage(box, itemservice.Filter{}, o, 50))
				assertOrder(t, fmt.Sprintf("List, descending %v", descending), got, want)
				got, _ = pageAll(t, w.searchPage(ofType, o, 50))
				assertOrder(t, fmt.Sprintf("Search, descending %v", descending), got, want)
			}
		})
	}
}

// --- paging ---

// Keyset paging stays exact under order_by: for every page size, both
// directions, on List and Search, the pages put together are the whole
// order, every Item once. The values are few, so ties and Items without a
// value fall across every page boundary; the token must carry the sort
// value, whether it has one, and the ID.
func TestOrderBy_PagingIsExact_WithTiesAndNulls(t *testing.T) {
	w := newOrderWorld(t)
	for _, c := range []struct {
		field  string
		values []any // nil: no value
	}{
		{"s", []any{"m", "a", "m", nil, "Z", "m", "a", nil, "z", "m", "Z", nil, "a", "m", nil, "z", "m", nil, "a", "Z", nil, "m", nil}},
		// 9 and 10 swap places if the token's value is compared as text.
		{"n", []any{9, 10, 9, nil, 100, 10, 9, nil, 10, 10, 9, nil, 100, 9, nil, 10, nil}},
		{"d", []any{"2026-12-04", "2026-12-01", "2026-12-04", nil, "2026-12-01", "2026-12-04", nil, "2027-01-02", nil, "2026-12-04"}},
	} {
		box := w.box(t)
		var items []sortable
		for _, v := range c.values {
			fields := map[string]any{}
			if v != nil {
				fields[c.field] = v
			}
			items = append(items, sortable{w.makeIn(t, w.a, box, fields), v})
		}
		n := len(items)
		for _, descending := range []bool{false, true} {
			o := &itemservice.OrderBy{Field: w.f(c.field), Desc: descending}
			want := inOrder(items, descending)
			for _, size := range []int{1, 2, 3, 5, 7, n - 1, n, n + 1} {
				what := fmt.Sprintf("%s, descending %v, pages of %d", c.field, descending, size)
				got, pages := pageAll(t, w.listPage(box, itemservice.Filter{}, o, size))
				assertOrder(t, "List by "+what, got, want)
				if wantPages := (n + size - 1) / size; pages != wantPages {
					t.Errorf("List by %s: %d pages, want %d (no empty last page)", what, pages, wantPages)
				}
			}
		}
	}
}

// The same on Search, which has no container: every Item of the namespace
// that the filter matches, across two Types, paged exactly.
func TestOrderBy_PagingIsExact_OnSearch(t *testing.T) {
	w := newOrderWorld(t)
	var items []sortable
	for i, v := range []any{"m", "a", nil, "m", "Z", nil, "a", "m", nil, "z", "Z", "m", nil, "a"} {
		typ := []string{w.a, w.b}[i%2]
		fields := map[string]any{}
		if v != nil {
			fields["s"] = v
		}
		items = append(items, sortable{w.make(t, typ, fields), v})
	}
	both := itemservice.Filter{TypeIDs: []string{w.a, w.b}}
	for _, descending := range []bool{false, true} {
		o := &itemservice.OrderBy{Field: w.f("s"), Desc: descending}
		want := inOrder(items, descending)
		for _, size := range []int{1, 3, 4, len(items)} {
			got, _ := pageAll(t, w.searchPage(both, o, size))
			assertOrder(t, fmt.Sprintf("Search, descending %v, pages of %d", descending, size), got, want)
		}
	}
}

// A page token belongs to its order: used with another field, another
// direction, or no order at all, it is refused, as with any other change
// to the request.
func TestOrderBy_PageTokenIsBoundToTheOrder(t *testing.T) {
	w := newOrderWorld(t)
	box := w.box(t)
	for i := range 4 {
		w.makeIn(t, w.a, box, map[string]any{"s": fmt.Sprintf("k%d", i), "n": i})
	}
	first := func(o *itemservice.OrderBy) string {
		t.Helper()
		_, next, err := w.listPage(box, itemservice.Filter{}, o, 2)("")
		if err != nil || next == "" {
			t.Fatalf("first page ordered by %+v: token %q, %v", o, next, err)
		}
		return next
	}
	orders := map[string]*itemservice.OrderBy{
		"f_s ascending": asc(w.f("s")), "f_s descending": desc(w.f("s")), "f_n ascending": asc(w.f("n")), "no order": nil,
	}
	for from, fromOrder := range orders {
		token := first(fromOrder)
		for to, toOrder := range orders {
			_, _, err := w.listPage(box, itemservice.Filter{}, toOrder, 2)(token)
			if from == to {
				if err != nil {
					t.Errorf("a token of %q used with the same order: %v", from, err)
				}
				continue
			}
			assertInvalidAt(t, fmt.Sprintf("a token of %q used with %q", from, to), err, "page_token")
		}
	}
	// On Search too.
	both := itemservice.Filter{TypeIDs: []string{w.a}}
	_, token, err := w.searchPage(both, asc(w.f("s")), 2)("")
	if err != nil || token == "" {
		t.Fatalf("Search, first page: token %q, %v", token, err)
	}
	_, _, err = w.searchPage(both, desc(w.f("s")), 2)(token)
	assertInvalidAt(t, "Search: an ascending token used descending", err, "page_token")
}

// --- across Types ---

// One field declared by two Types sorts their Items together, whether the
// Types are named or not; Items of a Type that does not declare the field
// are not dropped: they sort as Items without a value, last.
func TestOrderBy_AcrossTypes(t *testing.T) {
	w := newOrderWorld(t)
	var items []sortable
	for i, v := range []string{"d", "b", "a", "c", "b", "e"} {
		items = append(items, sortable{w.make(t, []string{w.a, w.b}[i%2], map[string]any{"s": v}), v})
	}
	for range 2 {
		items = append(items, sortable{w.make(t, w.plain, nil), nil})
	}
	items = append(items, sortable{w.make(t, w.a, nil), nil}) // declared, not set

	for name, f := range map[string]itemservice.Filter{
		"the three Types named": {TypeIDs: []string{w.a, w.b, w.plain}},
		"no Type named":         everything,
	} {
		for _, descending := range []bool{false, true} {
			o := &itemservice.OrderBy{Field: w.f("s"), Desc: descending}
			got, _ := pageAll(t, w.searchPage(f, o, 4))
			assertOrder(t, fmt.Sprintf("%s, descending %v", name, descending), got, inOrder(items, descending))
		}
	}

	// Enums with different options are still one type: they sort by name.
	var prios []sortable
	for _, c := range []struct{ typ, v string }{{w.a, "P3"}, {w.b, "P2"}, {w.a, "P1"}, {w.b, "P4"}} {
		prios = append(prios, sortable{w.make(t, c.typ, map[string]any{"e": c.v}), c.v})
	}
	got, _ := pageAll(t, w.searchPage(itemservice.Filter{TypeIDs: []string{w.a, w.b}}, asc(w.f("e")), 500))
	if len(got) < len(prios) {
		t.Fatalf("an enum across two Types: %d Items found, want at least %d", len(got), len(prios))
	}
	// The Items made above have no priority: they come after these four.
	assertOrder(t, "an enum across two Types", got[:len(prios)], inOrder(prios, false))
}

// What order_by refuses, on Search and List alike, naming order_by.
func TestOrderBy_Refusals(t *testing.T) {
	w := newOrderWorld(t)
	w.make(t, w.a, map[string]any{"s": "x"})
	one, both := []string{w.a}, []string{w.a, w.b}
	cases := []struct {
		name  string
		types []string
		order itemservice.OrderBy
	}{
		{"no field", one, itemservice.OrderBy{}},
		{"a field the selected Type does not declare", one, itemservice.OrderBy{Field: "f_nope_" + w.sfx}},
		{"a field only an unselected Type declares", []string{w.plain}, itemservice.OrderBy{Field: w.f("s")}},
		{"a field no Type declares, no Type named", nil, itemservice.OrderBy{Field: "f_nope_" + w.sfx}},
		{"a bool", one, itemservice.OrderBy{Field: w.f("b")}},
		{"a principal", one, itemservice.OrderBy{Field: w.f("p")}},
		{"a field whose type differs across the selected Types", both, itemservice.OrderBy{Field: w.f("x")}},
		{"a field whose type differs, no Type named", nil, itemservice.OrderBy{Field: w.f("x")}},
		// One stored type for equality filters, but not one order.
		{"a string in one Type and a principal in another", both, itemservice.OrderBy{Field: w.f("w")}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := itemservice.Filter{TypeIDs: c.types}
			if c.types == nil {
				f = everything
			}
			_, err := w.k.Search(testCtx(t), itemservice.SearchRequest{Principal: w.user, Filter: f, OrderBy: &c.order})
			assertInvalidAt(t, "Search", err, "order_by")
			_, err = w.k.List(testCtx(t), itemservice.ListRequest{Principal: w.user, Filter: f, OrderBy: &c.order})
			assertInvalidAt(t, "List", err, "order_by")
		})
	}
	// The same field is fine where only one declaration is in play.
	if _, err := w.k.Search(testCtx(t), itemservice.SearchRequest{
		Principal: w.user, Filter: itemservice.Filter{TypeIDs: one}, OrderBy: asc(w.f("x")),
	}); err != nil {
		t.Errorf("order by f_x with only the Type that declares it an int: %v", err)
	}
}

// --- with the rest of a search ---

// order_by combines with words, and never shows what the caller cannot
// see: a narrowed Item is not in another user's ordered pages, and takes
// no place in them.
func TestOrderBy_WithWordsAndVisibility(t *testing.T) {
	w := newOrderWorld(t)
	bob := in(person("bob"), w.user.Namespace)
	marker := word()
	var all, open []sortable
	for i, v := range []string{"c", "a", "d", "b", "e"} {
		req := itemservice.CreateRequest{
			IdempotencyKey: newKey(), Principal: w.user, TypeID: w.a, TypeVersion: 1,
			Fields: map[string]any{w.f("s"): v, w.f("w"): marker},
		}
		if i%2 == 1 {
			req.Narrow = &itemservice.Narrow{Readers: readers(w.user)}
		}
		res, err := w.k.Create(testCtx(t), req)
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		all = append(all, sortable{res.ItemID, v})
		if i%2 == 0 {
			open = append(open, sortable{res.ItemID, v})
		}
	}
	w.make(t, w.a, map[string]any{"s": "0"}) // without the word: not found

	read := func(p item.Principal, size int) []uuid.UUID {
		got, _ := pageAll(t, func(token string) ([]uuid.UUID, string, error) {
			res, err := w.k.Search(context.Background(), itemservice.SearchRequest{
				Principal: p, Query: marker, OrderBy: desc(w.f("s")), Limit: size, PageToken: token,
			})
			return idsOf(res.Items), res.NextPageToken, err
		})
		return got
	}
	assertOrder(t, "the owner, by words", read(w.user, 2), inOrder(all, true))
	assertOrder(t, "bob, by words, pages of 1", read(bob, 1), inOrder(open, true))
	assertOrder(t, "bob, by words, pages of 2", read(bob, 2), inOrder(open, true))
}
