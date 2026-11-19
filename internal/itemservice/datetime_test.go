// Tests for Jira part 2, step 1: date-time fields in the item service
// (docs/design.md, "Decisions for Jira part 2", primitive 1).
//
//   - A value is sent as an RFC 3339 string with an offset. It is stored,
//     returned and recorded in one form: UTC, six fractional digits, Z. The
//     caller's offset is not kept. Without an offset, or finer than a
//     microsecond, it is malformed (ErrInvalid, naming the field).
//   - Filters take equality and ranges, with bounds in any offset; order_by
//     sorts by the instant, Items without one last, with exact paging.
//   - A date and a datetime declared under one field ID disagree.
//   - Guards compare it as a timestamp.
package itemservice_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// dtTypeYAML: a meeting with a start and an end. The field IDs carry the
// world's suffix, as in orderTypeYAML. ODD is the type of f_odd: a datetime
// in one Type, a date in the other.
const dtTypeYAML = `
id: TYPE
version: 1
initial_state: s_planned
fields:
  - {id: f_title_SFX, type: string}
  - {id: f_from_SFX,  type: datetime}
  - {id: f_to_SFX,    type: datetime}
  - {id: f_odd_SFX,   type: ODD}
states:
  - {id: s_planned, category: todo}
  - {id: s_held,    category: done}
transitions:
  - from: s_planned
    to: s_held
    guard: 'f_to_SFX > f_from_SFX'
`

type dtWorld struct {
	k    *itemservice.Service
	user item.Principal
	a, b string // b declares f_odd as a date
	sfx  string
}

func newDTWorld(t *testing.T) dtWorld {
	t.Helper()
	k := itemservice.New(pool)
	sfx := strings.ReplaceAll(uuid.NewString(), "-", "")[:10]
	w := dtWorld{k: k, user: in(person("planner"), newNamespace(t, k)), sfx: sfx, a: "dta-" + sfx, b: "dtb-" + sfx}
	for typ, odd := range map[string]string{w.a: "datetime", w.b: "date"} {
		ty, err := typedef.Parse([]byte(strings.NewReplacer("TYPE", typ, "SFX", sfx, "ODD", odd).Replace(dtTypeYAML)))
		if err != nil {
			t.Fatalf("parse datetime test Type: %v", err)
		}
		if err := k.RegisterType(testCtx(t), ty); err != nil {
			t.Fatalf("register datetime test Type: %v", err)
		}
	}
	return w
}

func (w dtWorld) f(name string) string { return "f_" + name + "_" + w.sfx }

func (w dtWorld) create(t *testing.T, typ string, fields map[string]any) (uuid.UUID, error) {
	t.Helper()
	named := map[string]any{}
	for name, v := range fields {
		named[w.f(name)] = v
	}
	res, err := w.k.Create(testCtx(t), itemservice.CreateRequest{
		IdempotencyKey: newKey(), Principal: w.user, TypeID: typ, TypeVersion: 1, Fields: named,
	})
	return res.ItemID, err
}

func (w dtWorld) make(t *testing.T, fields map[string]any) uuid.UUID {
	t.Helper()
	id, err := w.create(t, w.a, fields)
	if err != nil {
		t.Fatalf("create %v: %v", fields, err)
	}
	return id
}

func (w dtWorld) update(t *testing.T, id uuid.UUID, fields map[string]any) error {
	t.Helper()
	named := map[string]any{}
	for name, v := range fields {
		named[w.f(name)] = v
	}
	_, err := w.k.Update(testCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: w.user, ItemID: id, Fields: named,
	})
	return err
}

// --- values ---

// Whatever offset a value is sent in, on create, update and a transition's
// patch, it is stored, returned and recorded as UTC with six fractional
// digits. null clears it.
func TestDateTime_StoredAndReturnedInOneForm(t *testing.T) {
	w := newDTWorld(t)
	id := w.make(t, map[string]any{"from": "2026-12-04T18:00:00+01:00"})
	if got := fieldsOf(t, w.k, w.user, id)[w.f("from")]; got != "2026-12-04T17:00:00.000000Z" {
		t.Errorf("after create: f_from = %v, want 2026-12-04T17:00:00.000000Z", got)
	}
	if err := w.update(t, id, map[string]any{"from": "2026-12-04T12:00:00.5-05:00"}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if got := fieldsOf(t, w.k, w.user, id)[w.f("from")]; got != "2026-12-04T17:00:00.500000Z" {
		t.Errorf("after update: f_from = %v, want 2026-12-04T17:00:00.500000Z", got)
	}
	// A transition's patch: the guard compares the instants, not the texts
	// (19:00+02:00 is 17:00Z, before 17:30Z although "19" sorts after "17").
	if _, err := w.k.Transition(testCtx(t), itemservice.TransitionRequest{
		IdempotencyKey: newKey(), Principal: w.user, ItemID: id, ToState: "s_held",
		Fields: map[string]any{w.f("from"): "2026-12-04T19:00:00+02:00", w.f("to"): "2026-12-04T17:30:00Z"},
	}); err != nil {
		t.Fatalf("transition with a patch: %v", err)
	}
	fields := fieldsOf(t, w.k, w.user, id)
	if fields[w.f("from")] != "2026-12-04T17:00:00.000000Z" || fields[w.f("to")] != "2026-12-04T17:30:00.000000Z" {
		t.Errorf("after the transition: %v", fields)
	}
	// History holds the stored form too.
	events := history(t, w.k, w.user, id)
	if got := events[0].Values[w.f("from")]; got != "2026-12-04T17:00:00.000000Z" {
		t.Errorf("the create event's value = %v, want the stored form", got)
	}
	if got := events[2].Values[w.f("to")]; got != "2026-12-04T17:30:00.000000Z" {
		t.Errorf("the transition event's value = %v, want the stored form", got)
	}
	if err := w.update(t, id, map[string]any{"to": nil}); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if v, set := fieldsOf(t, w.k, w.user, id)[w.f("to")]; set {
		t.Errorf("f_to = %v after null, want it unset", v)
	}
}

// The guard reads instants: an end before the start does not hold.
func TestDateTime_GuardComparesInstants(t *testing.T) {
	w := newDTWorld(t)
	// As text "18:00+03:00" sorts after "16:00Z"; as instants it is 15:00Z.
	id := w.make(t, map[string]any{"from": "2026-12-04T16:00:00Z", "to": "2026-12-04T18:00:00+03:00"})
	_, err := w.k.Transition(testCtx(t), itemservice.TransitionRequest{
		IdempotencyKey: newKey(), Principal: w.user, ItemID: id, ToState: "s_held",
	})
	assertRejectedCode(t, "an end before the start", err, itemservice.CodeGuardFailed)
}

// A malformed value is ErrInvalid naming the field, on create and update;
// nothing is written.
func TestDateTime_MalformedValuesAreRefused(t *testing.T) {
	w := newDTWorld(t)
	id := w.make(t, nil)
	for name, v := range map[string]any{
		"no offset":                 "2026-12-04T17:00:00",
		"a date":                    "2026-12-04",
		"finer than a microsecond":  "2026-12-04T17:00:00.1234567Z",
		"a leap second":             "2026-12-31T23:59:60Z",
		"no such day":               "2026-02-30T12:00:00Z",
		"a year past 9999 in UTC":   "9999-12-31T23:59:59-01:00",
		"a number":                  1796403600,
		"words":                     "next friday",
		"a space instead of the T":  "2026-12-04 17:00:00Z",
		"an empty string":           "",
		"a bool":                    true,
		"a lower-case t and z form": "2026-12-04t17:00:00z",
	} {
		_, err := w.create(t, w.a, map[string]any{"from": v})
		assertInvalidField(t, "create with "+name, err, w.f("from"))
		assertInvalidField(t, "update with "+name, w.update(t, id, map[string]any{"from": v}), w.f("from"))
	}
	if seq := itemSeqOf(t, id); seq != 1 {
		t.Errorf("item_seq = %d after refused updates, want 1", seq)
	}
}

// --- filters ---

func TestFilters_DateTime(t *testing.T) {
	w := newDTWorld(t)
	at := func(v string) uuid.UUID { return w.make(t, map[string]any{"from": v}) }
	// Four instants, sent in offsets that sort differently as text.
	early := at("2026-12-04T09:00:00+02:00") // 07:00Z
	noon := at("2026-12-04T12:00:00Z")
	half := at("2026-12-04T07:00:00.5-05:00") // 12:00:00.5Z
	late := at("2026-12-05T01:00:00+03:00")   // 2026-12-04 22:00Z
	w.make(t, nil)                            // no instant: matches no condition

	f := w.f("from")
	cases := []struct {
		name string
		ff   itemservice.FieldFilter
		want []uuid.UUID
	}{
		{"eq, same form", itemservice.FieldFilter{Field: f, Eq: "2026-12-04T12:00:00Z"}, []uuid.UUID{noon}},
		{"eq, another offset", itemservice.FieldFilter{Field: f, Eq: "2026-12-04T13:00:00+01:00"}, []uuid.UUID{noon}},
		{"eq, to the microsecond", itemservice.FieldFilter{Field: f, Eq: "2026-12-04T12:00:00.500000Z"}, []uuid.UUID{half}},
		{"gt", itemservice.FieldFilter{Field: f, Gt: "2026-12-04T12:00:00Z"}, []uuid.UUID{half, late}},
		{"gte", itemservice.FieldFilter{Field: f, Gte: "2026-12-04T12:00:00Z"}, []uuid.UUID{noon, half, late}},
		{"lt", itemservice.FieldFilter{Field: f, Lt: "2026-12-04T12:00:00Z"}, []uuid.UUID{early}},
		{"lte, bound in another offset", itemservice.FieldFilter{Field: f, Lte: "2026-12-04T14:00:00+02:00"}, []uuid.UUID{early, noon}},
		{"a range", itemservice.FieldFilter{Field: f, Gte: "2026-12-04T08:00:00Z", Lt: "2026-12-04T22:00:00Z"}, []uuid.UUID{noon, half}},
		// By the calendar the bound is the 5th; by the instant it is the 4th, 23:00Z.
		{"a range whose bound crosses midnight", itemservice.FieldFilter{Field: f, Gt: "2026-12-04T12:00:00.5Z", Lte: "2026-12-05T00:00:00+01:00"}, []uuid.UUID{late}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, err := w.k.Search(testCtx(t), itemservice.SearchRequest{
				Principal: w.user, Filter: itemservice.Filter{TypeIDs: []string{w.a}, Fields: []itemservice.FieldFilter{c.ff}}, Limit: 100,
			})
			if err != nil {
				t.Fatalf("Search: %v", err)
			}
			if got := idsOf(res.Items); !sameIDs(got, c.want) {
				t.Errorf("found %v, want %v", got, c.want)
			}
			// The same on List.
			lres, err := w.k.List(testCtx(t), itemservice.ListRequest{
				Principal: w.user, Filter: itemservice.Filter{TypeIDs: []string{w.a}, Fields: []itemservice.FieldFilter{c.ff}}, Limit: 100,
			})
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if got := idsOf(lres.Items); !sameIDs(got, c.want) {
				t.Errorf("List found %v, want %v", got, c.want)
			}
		})
	}
	// A bound that is not an instant is malformed, never "no match".
	for name, v := range map[string]any{
		"no offset": "2026-12-04T12:00:00", "a date": "2026-12-04", "words": "noon", "a number": 12,
		"finer than a microsecond": "2026-12-04T12:00:00.0000001Z",
	} {
		for _, ff := range []itemservice.FieldFilter{{Field: f, Eq: v}, {Field: f, Gte: v}} {
			_, err := w.k.Search(testCtx(t), itemservice.SearchRequest{
				Principal: w.user, Filter: itemservice.Filter{TypeIDs: []string{w.a}, Fields: []itemservice.FieldFilter{ff}},
			})
			assertInvalidAt(t, name, err, "filter.fields[0]")
		}
	}
}

// --- order_by ---

// Sorted by the instant, both ways, on List and Search, Items without one
// last, and paged exactly with ties and unset values across the pages.
func TestOrderBy_DateTime(t *testing.T) {
	w := newDTWorld(t)
	// sent form -> stored form. As sent, the texts sort in another order.
	values := [][2]string{
		{"2026-12-04T23:30:00+02:00", "2026-12-04T21:30:00.000000Z"},
		{"2026-12-04T12:00:00Z", "2026-12-04T12:00:00.000000Z"},
		{"2026-12-04T07:00:00.000001-05:00", "2026-12-04T12:00:00.000001Z"},
		{"2026-12-04T12:00:00Z", "2026-12-04T12:00:00.000000Z"}, // a tie
		{"2025-12-31T23:59:59.999999Z", "2025-12-31T23:59:59.999999Z"},
		{"2027-01-01T00:00:00+14:00", "2026-12-31T10:00:00.000000Z"},
		{"2026-12-04T12:00:00+00:00", "2026-12-04T12:00:00.000000Z"}, // another
	}
	var items []sortable
	for i, v := range values {
		items = append(items, sortable{w.make(t, map[string]any{"from": v[0]}), v[1]})
		if i%3 == 0 {
			items = append(items, sortable{w.make(t, nil), nil})
		}
	}
	ofType := itemservice.Filter{TypeIDs: []string{w.a}}
	page := func(list bool, o *itemservice.OrderBy, size int) func(string) ([]uuid.UUID, string, error) {
		return func(token string) ([]uuid.UUID, string, error) {
			if list {
				res, err := w.k.List(testCtx(t), itemservice.ListRequest{
					Principal: w.user, Filter: ofType, OrderBy: o, Limit: size, PageToken: token})
				return idsOf(res.Items), res.NextPageToken, err
			}
			res, err := w.k.Search(testCtx(t), itemservice.SearchRequest{
				Principal: w.user, Filter: ofType, OrderBy: o, Limit: size, PageToken: token})
			return idsOf(res.Items), res.NextPageToken, err
		}
	}
	for _, descending := range []bool{false, true} {
		o := &itemservice.OrderBy{Field: w.f("from"), Desc: descending}
		want := inOrder(items, descending)
		for _, size := range []int{1, 2, 3, len(items), len(items) + 1} {
			for _, list := range []bool{true, false} {
				got, _ := pageAll(t, page(list, o, size))
				assertOrder(t, fmt.Sprintf("list %v, descending %v, pages of %d", list, descending, size), got, want)
			}
		}
	}
}

// --- a date and a datetime under one field ID ---

// f_odd is a datetime in one Type and a date in the other: a filter or an
// order over both is refused, as for any two types; over one it works.
func TestDateTime_AndDateDisagreeAcrossTypes(t *testing.T) {
	w := newDTWorld(t)
	a := w.make(t, map[string]any{"odd": "2026-12-04T12:00:00Z"})
	if _, err := w.create(t, w.b, map[string]any{"odd": "2026-12-04"}); err != nil {
		t.Fatalf("create with the date: %v", err)
	}
	odd := w.f("odd")
	for _, types := range [][]string{{w.a, w.b}, nil} {
		f := itemservice.Filter{TypeIDs: types, Fields: []itemservice.FieldFilter{{Field: odd, Eq: "2026-12-04T12:00:00Z"}}}
		_, err := w.k.Search(testCtx(t), itemservice.SearchRequest{Principal: w.user, Filter: f})
		assertInvalidAt(t, fmt.Sprintf("filter over %v", types), err, "filter.fields[0]")

		f = itemservice.Filter{TypeIDs: types}
		if types == nil {
			f = itemservice.Filter{Categories: []string{"todo"}}
		}
		_, err = w.k.Search(testCtx(t), itemservice.SearchRequest{Principal: w.user, Filter: f, OrderBy: asc(odd)})
		assertInvalidAt(t, fmt.Sprintf("order over %v", types), err, "order_by")
	}
	res, err := w.k.Search(testCtx(t), itemservice.SearchRequest{
		Principal: w.user, OrderBy: asc(odd),
		Filter: itemservice.Filter{TypeIDs: []string{w.a}, Fields: []itemservice.FieldFilter{{Field: odd, Gte: "2026-12-04T00:00:00Z"}}},
	})
	if err != nil || !sameIDs(idsOf(res.Items), []uuid.UUID{a}) {
		t.Errorf("over the datetime Type alone: %v, %v; want %v", idsOf(res.Items), err, a)
	}
	// The date Type refuses an instant as a bound, and the other way round.
	_, err = w.k.Search(testCtx(t), itemservice.SearchRequest{Principal: w.user,
		Filter: itemservice.Filter{TypeIDs: []string{w.b}, Fields: []itemservice.FieldFilter{{Field: odd, Eq: "2026-12-04T12:00:00Z"}}}})
	assertInvalidAt(t, "an instant for a date", err, "filter.fields[0]")
	_, err = w.k.Search(testCtx(t), itemservice.SearchRequest{Principal: w.user,
		Filter: itemservice.Filter{TypeIDs: []string{w.a}, Fields: []itemservice.FieldFilter{{Field: odd, Eq: "2026-12-04"}}}})
	assertInvalidAt(t, "a date for an instant", err, "filter.fields[0]")
}
