// Tests for the Jira proof of concept, part 1: field filters across Types,
// on dates, and string ranges (docs/design.md, "Jira proof of concept").
//
//   - A field filter no longer needs exactly one Type: it works across the
//     selected Types, or across every registered Type that declares the
//     field when none is selected. It is refused only when no Type in play
//     declares the field, or when the declarations disagree on its type (a
//     string and a principal stay one type for equality, as in Step 1).
//   - A date field takes equality and ranges.
//   - A string field takes ranges, compared byte by byte: finding a card's
//     neighbour by rank needs "the keys below this one".
package itemservice_test

import (
	"testing"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/itemservice"
)

// find runs a filter-only search as the world's user and returns the IDs.
func (w orderWorld) find(t *testing.T, f itemservice.Filter) []uuid.UUID {
	t.Helper()
	res, err := w.k.Search(testCtx(t), itemservice.SearchRequest{Principal: w.user, Filter: f, Limit: 500})
	if err != nil {
		t.Fatalf("Search %+v: %v", f, err)
	}
	return idsOf(res.Items)
}

func (w orderWorld) eq(field string, v any) []itemservice.FieldFilter {
	return []itemservice.FieldFilter{{Field: w.f(field), Eq: v}}
}

// --- across Types ---

// One field filter over two Types that declare the field: named together,
// named with a Type that does not declare it, or not named at all.
func TestFilters_FieldAcrossTypes(t *testing.T) {
	w := newOrderWorld(t)
	inA := w.make(t, w.a, map[string]any{"s": "red", "n": 3})
	inB := w.make(t, w.b, map[string]any{"s": "red", "n": 5})
	w.make(t, w.a, map[string]any{"s": "blue", "n": 5})
	w.make(t, w.plain, nil)

	cases := []struct {
		name   string
		types  []string
		fields []itemservice.FieldFilter
		want   []uuid.UUID
	}{
		{"both Types named", []string{w.a, w.b}, w.eq("s", "red"), []uuid.UUID{inA, inB}},
		{"no Type named", nil, w.eq("s", "red"), []uuid.UUID{inA, inB}},
		{"one Type named", []string{w.b}, w.eq("s", "red"), []uuid.UUID{inB}},
		{"with a Type that does not declare it", []string{w.a, w.plain}, w.eq("s", "red"), []uuid.UUID{inA}},
		{"an int range, no Type named", nil, []itemservice.FieldFilter{{Field: w.f("n"), Gte: 3, Lt: 5}}, []uuid.UUID{inA}},
		{"two fields, both Types", []string{w.a, w.b},
			[]itemservice.FieldFilter{{Field: w.f("s"), Eq: "red"}, {Field: w.f("n"), Gt: 3}}, []uuid.UUID{inB}},
		// Enums with different options are one type; each Type's own
		// options are accepted.
		{"an enum option only one Type has", []string{w.a, w.b}, w.eq("e", "P4"), nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := w.find(t, itemservice.Filter{TypeIDs: c.types, Fields: c.fields})
			if !sameIDs(got, c.want) {
				t.Errorf("found %v, want %v", got, c.want)
			}
			// List takes the same filter (the top level).
			res, err := w.k.List(testCtx(t), itemservice.ListRequest{
				Principal: w.user, Filter: itemservice.Filter{TypeIDs: c.types, Fields: c.fields}, Limit: 500,
			})
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if !sameIDs(idsOf(res.Items), c.want) {
				t.Errorf("List found %v, want %v", idsOf(res.Items), c.want)
			}
		})
	}
}

// The refusals that remain: nobody in play declares the field, or the
// declarations disagree. Each names the field filter.
func TestFilters_FieldAcrossTypes_Refusals(t *testing.T) {
	w := newOrderWorld(t)
	cases := []struct {
		name   string
		types  []string
		fields []itemservice.FieldFilter
	}{
		{"no selected Type declares the field", []string{w.plain}, w.eq("s", "red")},
		{"no registered Type declares the field", nil,
			[]itemservice.FieldFilter{{Field: "f_nope_" + w.sfx, Eq: "red"}}},
		{"an int in one selected Type, a string in the other", []string{w.a, w.b}, w.eq("x", 1)},
		{"an int in one Type, a string in another, none named", nil, w.eq("x", "1")},
		{"an enum value no Type in play has", []string{w.a, w.b}, w.eq("e", "P9")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := itemservice.Filter{TypeIDs: c.types, Fields: c.fields}
			_, err := w.k.Search(testCtx(t), itemservice.SearchRequest{Principal: w.user, Filter: f})
			assertInvalidAt(t, "Search", err, "filter.fields[0]")
			_, err = w.k.List(testCtx(t), itemservice.ListRequest{Principal: w.user, Filter: f})
			assertInvalidAt(t, "List", err, "filter.fields[0]")
		})
	}
	// With only the Type that declares it an int, the same field filters.
	one := w.make(t, w.a, map[string]any{"x": 1})
	if got := w.find(t, itemservice.Filter{TypeIDs: []string{w.a}, Fields: w.eq("x", 1)}); !sameIDs(got, []uuid.UUID{one}) {
		t.Errorf("f_x with only the int Type: found %v, want %v", got, one)
	}
}

// A string in one Type and a principal in another is one stored type for
// equality, across Types as it was across versions: the value must be a
// principal, and "me" is the caller.
func TestFilters_StringAndPrincipalAcrossTypes(t *testing.T) {
	w := newOrderWorld(t)
	mine := w.make(t, w.b, map[string]any{"w": w.user.Actor})
	alsoMine := w.make(t, w.a, map[string]any{"w": w.user.Actor}) // a string that reads like a principal
	w.make(t, w.b, map[string]any{"w": "user:someone-else"})

	both := []string{w.a, w.b}
	for name, v := range map[string]any{"the principal": w.user.Actor, "me": "me"} {
		got := w.find(t, itemservice.Filter{TypeIDs: both, Fields: w.eq("w", v)})
		if !sameIDs(got, []uuid.UUID{mine, alsoMine}) {
			t.Errorf("f_w = %s across both Types: found %v, want %v and %v", name, got, mine, alsoMine)
		}
	}
	_, err := w.k.Search(testCtx(t), itemservice.SearchRequest{
		Principal: w.user, Filter: itemservice.Filter{TypeIDs: both, Fields: w.eq("w", "red")},
	})
	assertInvalidAt(t, "a bare string where one Type holds principals", err, "filter.fields[0]")

	// An agent's "me" is the user it acts for.
	agent := scoped(t, w.k, w.user, []uuid.UUID{uuid.Nil}, "read") // the whole namespace
	res, err := w.k.Search(testCtx(t), itemservice.SearchRequest{
		Principal: agent, Filter: itemservice.Filter{Fields: w.eq("w", "me")},
	})
	if err != nil {
		t.Fatalf("Search as the agent: %v", err)
	}
	if !sameIDs(idsOf(res.Items), []uuid.UUID{mine, alsoMine}) {
		t.Errorf("an agent's me, no Type named: found %v, want %v and %v", idsOf(res.Items), mine, alsoMine)
	}
}

// --- dates ---

// A date field filters by equality and by range, as dates.
func TestFilters_Date(t *testing.T) {
	w := newOrderWorld(t)
	day := func(d string) uuid.UUID { return w.make(t, w.a, map[string]any{"d": d}) }
	nov, dec1, dec4, jan := day("2026-11-30"), day("2026-12-01"), day("2026-12-04"), day("2027-01-02")
	other := w.make(t, w.b, map[string]any{"d": "2026-12-04"})
	w.make(t, w.a, nil) // no date: matches no condition

	d := w.f("d")
	cases := []struct {
		name  string
		types []string
		ff    itemservice.FieldFilter
		want  []uuid.UUID
	}{
		{"eq", []string{w.a}, itemservice.FieldFilter{Field: d, Eq: "2026-12-04"}, []uuid.UUID{dec4}},
		{"eq across Types", nil, itemservice.FieldFilter{Field: d, Eq: "2026-12-04"}, []uuid.UUID{dec4, other}},
		{"gt", []string{w.a}, itemservice.FieldFilter{Field: d, Gt: "2026-12-01"}, []uuid.UUID{dec4, jan}},
		{"gte", []string{w.a}, itemservice.FieldFilter{Field: d, Gte: "2026-12-01"}, []uuid.UUID{dec1, dec4, jan}},
		{"lt", []string{w.a}, itemservice.FieldFilter{Field: d, Lt: "2026-12-01"}, []uuid.UUID{nov}},
		{"lte", []string{w.a}, itemservice.FieldFilter{Field: d, Lte: "2026-12-01"}, []uuid.UUID{nov, dec1}},
		{"a range", []string{w.a}, itemservice.FieldFilter{Field: d, Gte: "2026-12-01", Lt: "2027-01-01"}, []uuid.UUID{dec1, dec4}},
		{"a range across a year end", []string{w.a}, itemservice.FieldFilter{Field: d, Gt: "2026-12-04", Lte: "2027-01-02"}, []uuid.UUID{jan}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := w.find(t, itemservice.Filter{TypeIDs: c.types, Fields: []itemservice.FieldFilter{c.ff}})
			if !sameIDs(got, c.want) {
				t.Errorf("found %v, want %v", got, c.want)
			}
		})
	}
	// A value that is not a calendar date is malformed, never "no match".
	for name, v := range map[string]any{
		"not a date": "tomorrow", "a month 13": "2026-13-01", "a day 31 in November": "2026-11-31",
		"a timestamp": "2026-12-01T00:00:00Z", "a number": 20261201,
	} {
		for _, ff := range []itemservice.FieldFilter{{Field: d, Eq: v}, {Field: d, Gte: v}} {
			_, err := w.k.Search(testCtx(t), itemservice.SearchRequest{
				Principal: w.user, Filter: itemservice.Filter{TypeIDs: []string{w.a}, Fields: []itemservice.FieldFilter{ff}},
			})
			assertInvalidAt(t, name, err, "filter.fields[0]")
		}
	}
}

// --- string ranges ---

// A string field takes ranges, byte by byte: upper case sorts before lower
// case. This is how an app finds the rank keys below or above a card's.
func TestFilters_StringRange_IsBytewise(t *testing.T) {
	w := newOrderWorld(t)
	s := func(v string) uuid.UUID { return w.make(t, w.a, map[string]any{"s": v}) }
	upA, upB, a, aa, b := s("A"), s("B"), s("a"), s("aa"), s("b")
	w.make(t, w.a, nil)

	f := w.f("s")
	cases := []struct {
		name string
		ff   itemservice.FieldFilter
		want []uuid.UUID
	}{
		{"above a", itemservice.FieldFilter{Field: f, Gt: "a"}, []uuid.UUID{aa, b}},
		{"a and above", itemservice.FieldFilter{Field: f, Gte: "a"}, []uuid.UUID{a, aa, b}},
		// Under a locale, B would sort after a.
		{"below a", itemservice.FieldFilter{Field: f, Lt: "a"}, []uuid.UUID{upA, upB}},
		{"a and below", itemservice.FieldFilter{Field: f, Lte: "a"}, []uuid.UUID{upA, upB, a}},
		{"between B and aa", itemservice.FieldFilter{Field: f, Gt: "B", Lt: "aa"}, []uuid.UUID{a}},
		{"from B to aa", itemservice.FieldFilter{Field: f, Gte: "B", Lte: "aa"}, []uuid.UUID{upB, a, aa}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := w.find(t, itemservice.Filter{TypeIDs: []string{w.a}, Fields: []itemservice.FieldFilter{c.ff}})
			if !sameIDs(got, c.want) {
				t.Errorf("found %v, want %v", got, c.want)
			}
		})
	}

	// The neighbour above a card: the highest key below its own, one row.
	res, err := w.k.Search(testCtx(t), itemservice.SearchRequest{
		Principal: w.user, Limit: 1, OrderBy: desc(f),
		Filter: itemservice.Filter{TypeIDs: []string{w.a}, Fields: []itemservice.FieldFilter{{Field: f, Lt: "aa"}}},
	})
	if err != nil {
		t.Fatalf("the neighbour above aa: %v", err)
	}
	if got := idsOf(res.Items); len(got) != 1 || got[0] != a {
		t.Errorf("the neighbour above aa: found %v, want %v", got, a)
	}

	// Ranges stay refused where order means nothing.
	for name, ff := range map[string]itemservice.FieldFilter{
		"an enum":     {Field: w.f("e"), Lt: "P3"},
		"a bool":      {Field: w.f("b"), Gte: true},
		"a principal": {Field: w.f("p"), Gt: "user:a"},
		"a string in one Type and a principal in another": {Field: w.f("w"), Gt: "a"},
	} {
		types := []string{w.a}
		if name == "a string in one Type and a principal in another" {
			types = []string{w.a, w.b}
		}
		_, err := w.k.Search(testCtx(t), itemservice.SearchRequest{
			Principal: w.user, Filter: itemservice.Filter{TypeIDs: types, Fields: []itemservice.FieldFilter{ff}},
		})
		assertInvalidAt(t, "a range on "+name, err, "filter.fields[0]")
	}
}
