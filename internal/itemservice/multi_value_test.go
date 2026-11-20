// Tests for Jira part 2, step 2: multi-value fields in the item service
// (docs/design.md, "Decisions for Jira part 2", multi-value fields).
//
// A field declared `multiple: true` holds a set: unordered, no duplicates,
// stored and returned sorted; an empty set is an unset field.
//
//   - A patch replaces the set (a list), clears it (null or []), or changes
//     it with a set operation, {add: [...], remove: [...]}, applied under
//     the row lock: two people adding at once both land, with no etag.
//   - `max` bounds the set: FAILED_PRECONDITION / too_many_values.
//   - Filters: eq means "contains"; two eq on one field mean "contains
//     both"; empty: true means "has no value" (for any field). Ranges and
//     order_by are refused.
//   - allowed: [field:x] matches any member. frozen and writable_by apply
//     to the field as a whole.
//   - History shows the resulting set, and what a set operation asked for.
package itemservice_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// mvTypeYAML: field IDs carry the world's suffix (SFX), as in the order
// tests. ONE is the declaration of f_one: single here, multiple in the
// world's second Type.
const mvTypeYAML = `
id: TYPE
version: 1
initial_state: s_open
fields:
  - {id: f_title_SFX,     type: string}
  - {id: f_labels_SFX,    type: string,    multiple: true, max: 3}
  - {id: f_tags_SFX,      type: enum,      multiple: true, options: [red, green, blue]}
  - {id: f_reviewers_SFX, type: principal, multiple: true}
  - {id: f_days_SFX,      type: date,      multiple: true}
  - {id: f_locked_SFX,    type: string,    multiple: true, writable_by: ['role:ROLE']}
  - ONE
states:
  - {id: s_open,   category: todo}
  - {id: s_review, category: active, frozen: [f_tags_SFX]}
  - {id: s_done,   category: done}
transitions:
  - {from: s_open,   to: s_review}
  - {from: s_review, to: s_done, allowed: ['field:f_reviewers_SFX']}
`

type mvWorld struct {
	k              *itemservice.Service
	ana, bob, carl item.Principal
	a, b           string // b declares f_one as multiple
	sfx, role      string
}

func newMVWorld(t *testing.T) mvWorld {
	t.Helper()
	k := itemservice.New(pool)
	sfx := strings.ReplaceAll(uuid.NewString(), "-", "")[:10]
	ns := newNamespace(t, k)
	w := mvWorld{k: k, sfx: sfx, role: "lock-" + sfx, a: "mva-" + sfx, b: "mvb-" + sfx,
		ana: in(person("ana"), ns), bob: in(person("bob"), ns), carl: in(person("carl"), ns)}
	for typ, one := range map[string]string{
		w.a: "{id: f_one_SFX, type: string}",
		w.b: "{id: f_one_SFX, type: string, multiple: true}",
	} {
		src := strings.Replace(mvTypeYAML, "ONE", one, 1)
		registerInline(t, k, strings.NewReplacer("TYPE", typ, "SFX", sfx, "ROLE", w.role).Replace(src))
	}
	return w
}

func (w mvWorld) f(name string) string { return "f_" + name + "_" + w.sfx }

func (w mvWorld) named(fields map[string]any) map[string]any {
	out := map[string]any{}
	for name, v := range fields {
		out[w.f(name)] = v
	}
	return out
}

func (w mvWorld) create(t *testing.T, typ string, fields map[string]any) (uuid.UUID, error) {
	t.Helper()
	res, err := w.k.Create(testCtx(t), itemservice.CreateRequest{
		IdempotencyKey: newKey(), Principal: w.ana, TypeID: typ, TypeVersion: 1, Fields: w.named(fields),
	})
	return res.ItemID, err
}

func (w mvWorld) make(t *testing.T, fields map[string]any) uuid.UUID {
	t.Helper()
	id, err := w.create(t, w.a, fields)
	if err != nil {
		t.Fatalf("create %v: %v", fields, err)
	}
	return id
}

func (w mvWorld) update(ctx context.Context, p item.Principal, id uuid.UUID, fields map[string]any) error {
	_, err := w.k.Update(ctx, itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: p, ItemID: id, Fields: w.named(fields),
	})
	return err
}

func (w mvWorld) mustUpdate(t *testing.T, id uuid.UUID, fields map[string]any) {
	t.Helper()
	if err := w.update(testCtx(t), w.ana, id, fields); err != nil {
		t.Fatalf("update %v: %v", fields, err)
	}
}

// set returns the field's members as strings; nil when the field is unset.
func (w mvWorld) set(t *testing.T, id uuid.UUID, name string) []string {
	t.Helper()
	v, has := fieldsOf(t, w.k, w.ana, id)[w.f(name)]
	if !has {
		return nil
	}
	list, ok := v.([]any)
	if !ok {
		t.Fatalf("%s = %v (%T), want a list", name, v, v)
	}
	out := make([]string, len(list))
	for i, m := range list {
		out[i], _ = m.(string)
	}
	return out
}

func l(vs ...any) []any { return vs }

func add(vs ...any) map[string]any    { return map[string]any{"add": vs} }
func remove(vs ...any) map[string]any { return map[string]any{"remove": vs} }

// --- values ---

func TestMultiValue_ASetStoredSorted(t *testing.T) {
	w := newMVWorld(t)
	id := w.make(t, map[string]any{"labels": l("b", "a", "b"), "tags": l("red", "blue"), "days": l("2026-12-04", "2026-01-02")})
	for name, want := range map[string][]string{
		"labels": {"a", "b"}, "tags": {"blue", "red"}, "days": {"2026-01-02", "2026-12-04"}, "reviewers": nil,
	} {
		if got := w.set(t, id, name); !slices.Equal(got, want) {
			t.Errorf("%s = %v, want %v (sorted, no duplicates; unset when empty)", name, got, want)
		}
	}
	// Replace, then clear both ways.
	w.mustUpdate(t, id, map[string]any{"labels": l("z"), "tags": l()})
	if got := w.set(t, id, "labels"); !slices.Equal(got, []string{"z"}) {
		t.Errorf("after a replace: labels = %v, want [z]", got)
	}
	if got := w.set(t, id, "tags"); got != nil {
		t.Errorf("after []: tags = %v, want unset", got)
	}
	w.mustUpdate(t, id, map[string]any{"labels": nil})
	if got := w.set(t, id, "labels"); got != nil {
		t.Errorf("after null: labels = %v, want unset", got)
	}
	// Byte order, as every text comparison of the kernel: upper before lower.
	w.mustUpdate(t, id, map[string]any{"labels": l("b", "B", "a")})
	if got := w.set(t, id, "labels"); !slices.Equal(got, []string{"B", "a", "b"}) {
		t.Errorf("labels = %v, want [B a b] (byte order)", got)
	}
}

// add and remove change the set where it stands: adding what is there and
// removing what is not are no errors; a set emptied is unset. History shows
// the resulting set and what was asked.
func TestMultiValue_SetOperations(t *testing.T) {
	w := newMVWorld(t)
	id := w.make(t, map[string]any{"reviewers": l(w.ana.Actor)})
	w.mustUpdate(t, id, map[string]any{"reviewers": add(w.bob.Actor, w.ana.Actor)}) // 2
	want := []string{w.ana.Actor, w.bob.Actor}
	slices.Sort(want)
	if got := w.set(t, id, "reviewers"); !slices.Equal(got, want) {
		t.Errorf("after add: %v, want %v", got, want)
	}
	w.mustUpdate(t, id, map[string]any{"reviewers": map[string]any{"add": l(w.carl.Actor), "remove": l(w.ana.Actor, "user:nobody")}}) // 3
	want = []string{w.bob.Actor, w.carl.Actor}
	slices.Sort(want)
	if got := w.set(t, id, "reviewers"); !slices.Equal(got, want) {
		t.Errorf("after add and remove: %v, want %v", got, want)
	}

	events := history(t, w.k, w.ana, id)
	last := events[2]
	if got, _ := last.Values[w.f("reviewers")].([]any); len(got) != 2 {
		t.Errorf("the event's value = %v, want the resulting set of two", last.Values[w.f("reviewers")])
	}
	ops := last.SetOps[w.f("reviewers")]
	if !reflect.DeepEqual(ops["add"], l(w.carl.Actor)) || len(ops["remove"]) != 2 {
		t.Errorf("the event's set_ops = %v, want add [carl] and remove [ana, nobody] as asked", last.SetOps)
	}
	if len(events[0].SetOps) != 0 {
		t.Errorf("the create's set_ops = %v, want none: it set the whole set", events[0].SetOps)
	}
	// The payload names the field, never a member.
	_, payload := event(t, id, 3)
	if s := fmt.Sprint(payload); strings.Contains(s, w.carl.Actor) || strings.Contains(s, "user:nobody") {
		t.Errorf("the payload holds members: %v", payload)
	}

	w.mustUpdate(t, id, map[string]any{"reviewers": remove(w.bob.Actor, w.carl.Actor)}) // 4
	if got := w.set(t, id, "reviewers"); got != nil {
		t.Errorf("after removing everyone: %v, want unset", got)
	}
	_, payload = event(t, id, 4)
	if got := anyStrings(payload["cleared_field_ids"]); !slices.Equal(got, []string{w.f("reviewers")}) {
		t.Errorf("cleared_field_ids = %v, want the field: its set is empty", got)
	}
	// On an unset field add starts the set, and remove is nothing.
	w.mustUpdate(t, id, map[string]any{"labels": add("x"), "tags": remove("red")})
	if got := w.set(t, id, "labels"); !slices.Equal(got, []string{"x"}) {
		t.Errorf("add on an unset field: %v, want [x]", got)
	}
	if got := w.set(t, id, "tags"); got != nil {
		t.Errorf("remove on an unset field: %v, want unset", got)
	}
}

func TestMultiValue_Malformed(t *testing.T) {
	w := newMVWorld(t)
	id := w.make(t, nil)
	for name, c := range map[string]struct {
		field string
		v     any
	}{
		"one value where a list is wanted":  {"labels", "a"},
		"a list for a single-value field":   {"one", l("a")},
		"a member that is no option":        {"tags", l("pink")},
		"a member that is no principal":     {"reviewers", l("bob")},
		"a member that is no date":          {"days", l("tomorrow")},
		"a number among strings":            {"labels", l("a", 1)},
		"a value both added and removed":    {"labels", map[string]any{"add": l("a"), "remove": l("a")}},
		"a set operation on a single field": {"one", add("a")},
		"an unknown set operation":          {"labels", map[string]any{"toggle": l("a")}},
	} {
		assertInvalidField(t, "update with "+name, w.update(testCtx(t), w.ana, id, map[string]any{c.field: c.v}), w.f(c.field))
	}
	// A new Item has no set to change: a set operation on create is malformed.
	_, err := w.create(t, w.a, map[string]any{"labels": add("a")})
	assertInvalidField(t, "a set operation on create", err, w.f("labels"))
	if seq := itemSeqOf(t, id); seq != 1 {
		t.Errorf("item_seq = %d after refused updates, want 1", seq)
	}
}

// max bounds the set, however it is reached.
func TestMultiValue_Max(t *testing.T) {
	w := newMVWorld(t)
	tooMany := func(what string, err error) {
		t.Helper()
		assertRejectedCode(t, what, err, itemservice.CodeTooManyValues)
		var rej *itemservice.RejectedError
		if errors.As(err, &rej) && (rej.Metadata["field"] != w.f("labels") || rej.Metadata["max"] != "3") {
			t.Errorf("%s: metadata = %v, want the field and max 3", what, rej.Metadata)
		}
	}
	_, err := w.create(t, w.a, map[string]any{"labels": l("a", "b", "c", "d")})
	tooMany("create with four", err)

	id := w.make(t, map[string]any{"labels": l("a", "b")})
	tooMany("replace with four", w.update(testCtx(t), w.ana, id, map[string]any{"labels": l("a", "b", "c", "d")}))
	tooMany("add two to two", w.update(testCtx(t), w.ana, id, map[string]any{"labels": add("c", "d")}))
	if seq := itemSeqOf(t, id); seq != 1 {
		t.Errorf("item_seq = %d after refusals, want 1", seq)
	}
	// Up to the max is fine; so is adding what is already there, and
	// swapping one for another.
	w.mustUpdate(t, id, map[string]any{"labels": add("c")})
	w.mustUpdate(t, id, map[string]any{"labels": add("a", "c")})
	w.mustUpdate(t, id, map[string]any{"labels": map[string]any{"add": l("d"), "remove": l("a")}})
	if got := w.set(t, id, "labels"); !slices.Equal(got, []string{"b", "c", "d"}) {
		t.Errorf("labels = %v, want [b c d]", got)
	}
	// Four duplicates of one are one.
	w.mustUpdate(t, id, map[string]any{"labels": l("x", "x", "x", "x")})
}

// Twelve people add themselves at the same moment, with no etag: all
// twelve are there. A replace would have lost some.
func TestMultiValue_ConcurrentAddsAllLand(t *testing.T) {
	w := newMVWorld(t)
	id := w.make(t, nil)
	const n = 12
	var wg sync.WaitGroup
	errs := make(chan error, n)
	var want []string
	for i := range n {
		who := fmt.Sprintf("user:r%02d-%s", i, w.sfx)
		want = append(want, who)
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := w.update(context.Background(), w.ana, id, map[string]any{"reviewers": add(who)}); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("add: %v", err)
	}
	if got := w.set(t, id, "reviewers"); !slices.Equal(got, want) {
		t.Errorf("reviewers = %v, want all %d", got, n)
	}
}

// --- filters ---

func TestMultiValue_Filters(t *testing.T) {
	w := newMVWorld(t)
	ab := w.make(t, map[string]any{"labels": l("a", "b"), "reviewers": l(w.ana.Actor, w.bob.Actor), "one": "x"})
	bc := w.make(t, map[string]any{"labels": l("b", "c"), "reviewers": l(w.bob.Actor)})
	none := w.make(t, map[string]any{"title": "nothing set " + word()})
	find := func(ffs ...itemservice.FieldFilter) ([]uuid.UUID, error) {
		res, err := w.k.Search(testCtx(t), itemservice.SearchRequest{
			Principal: w.ana, Filter: itemservice.Filter{TypeIDs: []string{w.a}, Fields: ffs}, Limit: 100,
		})
		return idsOf(res.Items), err
	}
	labels, reviewers := w.f("labels"), w.f("reviewers")
	cases := []struct {
		name string
		ffs  []itemservice.FieldFilter
		want []uuid.UUID
	}{
		{"eq means contains", []itemservice.FieldFilter{{Field: labels, Eq: "b"}}, []uuid.UUID{ab, bc}},
		{"contains a", []itemservice.FieldFilter{{Field: labels, Eq: "a"}}, []uuid.UUID{ab}},
		{"contains nothing like it", []itemservice.FieldFilter{{Field: labels, Eq: "zz"}}, nil},
		{"a member is matched whole, not as a prefix", []itemservice.FieldFilter{{Field: reviewers, Eq: w.ana.Actor[:len(w.ana.Actor)-1]}}, nil},
		{"two eq on one field: contains both", []itemservice.FieldFilter{{Field: labels, Eq: "b"}, {Field: labels, Eq: "c"}}, []uuid.UUID{bc}},
		{"me, among the reviewers", []itemservice.FieldFilter{{Field: reviewers, Eq: "me"}}, []uuid.UUID{ab}},
		{"empty: no label", []itemservice.FieldFilter{{Field: labels, Empty: true}}, []uuid.UUID{none}},
		{"empty, on a single-value field: unset", []itemservice.FieldFilter{{Field: w.f("one"), Empty: true}}, []uuid.UUID{bc, none}},
		{"contains b and nobody reviews", []itemservice.FieldFilter{{Field: labels, Eq: "b"}, {Field: w.f("tags"), Empty: true}}, []uuid.UUID{ab, bc}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := find(c.ffs...)
			if err != nil {
				t.Fatalf("Search: %v", err)
			}
			if !sameIDs(got, c.want) {
				t.Errorf("found %v, want %v", got, c.want)
			}
		})
	}
	// A set emptied by a remove is empty to the filter too.
	w.mustUpdate(t, bc, map[string]any{"labels": remove("b", "c")})
	if got, _ := find(itemservice.FieldFilter{Field: labels, Empty: true}); !sameIDs(got, []uuid.UUID{bc, none}) {
		t.Errorf("empty after a remove: %v, want %v and %v", got, bc, none)
	}

	for name, ff := range map[string]itemservice.FieldFilter{
		"a range on a set":           {Field: labels, Gte: "a"},
		"empty with eq":              {Field: labels, Eq: "a", Empty: true},
		"empty with a bound":         {Field: w.f("title"), Gt: "a", Empty: true},
		"a member that is no option": {Field: w.f("tags"), Eq: "pink"},
		"a list as the value":        {Field: labels, Eq: l("a", "b")},
	} {
		_, err := find(ff)
		assertInvalidAt(t, name, err, "filter.fields[0]")
	}
	// A set has no order.
	_, err := w.k.Search(testCtx(t), itemservice.SearchRequest{
		Principal: w.ana, Filter: itemservice.Filter{TypeIDs: []string{w.a}}, OrderBy: asc(labels),
	})
	assertInvalidAt(t, "order_by a set", err, "order_by")
	// f_one is single in one Type and a set in the other: over both it is
	// two different things.
	_, err = w.k.Search(testCtx(t), itemservice.SearchRequest{
		Principal: w.ana, Filter: itemservice.Filter{TypeIDs: []string{w.a, w.b}, Fields: []itemservice.FieldFilter{{Field: w.f("one"), Eq: "x"}}},
	})
	assertInvalidAt(t, "single in one Type, multiple in another", err, "filter.fields[0]")
}

// The values of a set of strings are searchable as words, like a string's.
func TestMultiValue_WordSearchFindsAMember(t *testing.T) {
	w := newMVWorld(t)
	term := word()
	id := w.make(t, map[string]any{"labels": l("backend", term)})
	res, err := w.k.Search(testCtx(t), itemservice.SearchRequest{Principal: w.ana, Query: term, Limit: 10})
	if err != nil || !sameIDs(idsOf(res.Items), []uuid.UUID{id}) {
		t.Errorf("searching a label's word: %v, %v; want %v", idsOf(res.Items), err, id)
	}
	w.mustUpdate(t, id, map[string]any{"labels": remove(term)})
	res, err = w.k.Search(testCtx(t), itemservice.SearchRequest{Principal: w.ana, Query: term, Limit: 10})
	if err != nil || len(res.Items) != 0 {
		t.Errorf("after removing the label: %v, %v; want nothing", idsOf(res.Items), err)
	}
}

// --- who ---

// allowed: [field:f_reviewers] matches any member of the set.
func TestMultiValue_AllowedMatchesAnyMember(t *testing.T) {
	w := newMVWorld(t)
	id := w.make(t, map[string]any{"reviewers": l(w.ana.Actor, w.bob.Actor)})
	move := func(p item.Principal, to string) error {
		_, err := w.k.Transition(testCtx(t), itemservice.TransitionRequest{
			IdempotencyKey: newKey(), Principal: p, ItemID: id, ToState: to,
		})
		return err
	}
	if err := move(w.ana, "s_review"); err != nil {
		t.Fatalf("to review: %v", err)
	}
	assertForbidden(t, move(w.carl, "s_done"))
	// A patch may not write the field its own transition's allowed names.
	_, err := w.k.Transition(testCtx(t), itemservice.TransitionRequest{
		IdempotencyKey: newKey(), Principal: w.carl, ItemID: id, ToState: "s_done",
		Fields: w.named(map[string]any{"reviewers": add(w.carl.Actor)}),
	})
	assertNotWritable(t, "adding oneself to the reviewers in the move they allow", err, w.f("reviewers"))
	if err := move(w.bob, "s_done"); err != nil {
		t.Errorf("bob, the second reviewer, closes: %v", err)
	}
}

// frozen and writable_by apply to the field as a whole: a set operation on
// it is a write of it.
func TestMultiValue_FrozenAndWritableByApplyToTheWholeField(t *testing.T) {
	w := newMVWorld(t)
	id := w.make(t, map[string]any{"tags": l("red")})
	assertNotWritable(t, "a role-written set, added to without the role",
		w.update(testCtx(t), w.ana, id, map[string]any{"locked": add("x")}), w.f("locked"))
	grantRoleIn(t, w.ana.Namespace, w.bob, w.role)
	if err := w.update(testCtx(t), w.bob, id, map[string]any{"locked": add("x")}); err != nil {
		t.Errorf("with the role: %v", err)
	}
	if _, err := w.k.Transition(testCtx(t), itemservice.TransitionRequest{
		IdempotencyKey: newKey(), Principal: w.ana, ItemID: id, ToState: "s_review",
	}); err != nil {
		t.Fatalf("to review: %v", err)
	}
	for name, v := range map[string]any{"add": add("blue"), "remove": remove("red"), "replace": l("green"), "clear": nil} {
		assertFrozen(t, name+" on a frozen set", w.update(testCtx(t), w.ana, id, map[string]any{"tags": v}), "s_review", w.f("tags"))
	}
	if got := w.set(t, id, "tags"); !slices.Equal(got, []string{"red"}) {
		t.Errorf("tags = %v, want [red]", got)
	}
}

var _ = typedef.String
