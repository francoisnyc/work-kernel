// Tests for Jira part 2, step 1, through the public API: date-time values,
// filters and order_by; what a Type shows of its timers and computed
// effects; a story's resolution time. (A sprint closing itself needs the
// worker: internal/workflow's end-to-end tests and the scenario prove it.)
//
// The Types are the PLAT Types' version 2 (types/): sprint@2 with f_end_at
// and its at-timer, story, bug and task @2 with f_resolved_at.
package api_test

import (
	"slices"
	"testing"
	"time"

	"connectrpc.com/connect"

	kernelv1 "github.com/francoisnyc/work-kernel/gen/kernel/v1"
	"github.com/francoisnyc/work-kernel/internal/api"
)

func platV2(id string) string { return "namespaces/acme/types/" + id + "/versions/2" }

// mk2 creates an Item of a PLAT Type's version 2 as who, in a container.
func mk2(t *testing.T, who, typ, container string, f map[string]any) (string, error) {
	t.Helper()
	res, err := as(who).CreateItem(testCtx(t), connect.NewRequest(&kernelv1.CreateItemRequest{
		Parent: "namespaces/acme", RequestId: newID(), Type: platV2(typ), Container: container, Fields: fields(t, f),
	}))
	if err != nil {
		return "", err
	}
	return res.Msg.Item.Name, nil
}

func mustMk2(t *testing.T, who, typ, container string, f map[string]any) string {
	t.Helper()
	name, err := mk2(t, who, typ, container, f)
	if err != nil {
		t.Fatalf("CreateItem %s@2 as %s: %v", typ, who, err)
	}
	return name
}

// stored is an instant as the API returns a datetime field.
func stored(at time.Time) string { return at.UTC().Format("2006-01-02T15:04:05.000000Z") }

// --- date-time values ---

// A datetime is sent as an RFC 3339 string with any offset and comes back
// in one form, UTC with six fractional digits, from every call that
// returns the Item, and in history.
func TestDateTime_API_SentWithAnOffsetReturnedInUTC(t *testing.T) {
	plat := space(t)
	sprint := mustMk2(t, "ana", "sprint", plat, map[string]any{
		"f_name": "Sprint " + word(), "f_end": "2026-12-04", "f_end_at": "2026-12-04T18:00:00+01:00",
	})
	if got := get(t, "ana", sprint).Fields.AsMap()["f_end_at"]; got != "2026-12-04T17:00:00.000000Z" {
		t.Errorf("GetItem: f_end_at = %v, want 2026-12-04T17:00:00.000000Z", got)
	}
	res, err := as("ana").UpdateItem(testCtx(t), connect.NewRequest(&kernelv1.UpdateItemRequest{
		Name: sprint, RequestId: newID(), Fields: fields(t, map[string]any{"f_end_at": "2026-12-11T12:30:00.25-05:00"}),
	}))
	if err != nil {
		t.Fatalf("UpdateItem: %v", err)
	}
	if got := res.Msg.Item.Fields.AsMap()["f_end_at"]; got != "2026-12-11T17:30:00.250000Z" {
		t.Errorf("UpdateItem: f_end_at = %v, want 2026-12-11T17:30:00.250000Z", got)
	}
	evs := events(t, "ana", sprint)
	if got := evs[len(evs)-1].Fields.AsMap()["f_end_at"]; got != "2026-12-11T17:30:00.250000Z" {
		t.Errorf("the update event's f_end_at = %v, want the returned form", got)
	}
	// The date beside it is untouched: a date is not an instant.
	if got := get(t, "ana", sprint).Fields.AsMap()["f_end"]; got != "2026-12-04" {
		t.Errorf("f_end = %v, want 2026-12-04", got)
	}
}

// Without an offset, or not an instant at all: INVALID_ARGUMENT naming the
// field, on create and on update.
func TestDateTime_API_Refusals(t *testing.T) {
	plat := space(t)
	sprint := mustMk2(t, "ana", "sprint", plat, map[string]any{"f_name": "Sprint " + word()})
	for name, v := range map[string]any{
		"no offset": "2026-12-04T17:00:00", "a date": "2026-12-04", "nanoseconds": "2026-12-04T17:00:00.123456789Z",
		"a number": 1796403600, "words": "friday",
	} {
		_, err := mk2(t, "ana", "sprint", plat, map[string]any{"f_name": "S", "f_end_at": v})
		invalid(t, "CreateItem with "+name, err, "fields.f_end_at")
		_, err = as("ana").UpdateItem(testCtx(t), connect.NewRequest(&kernelv1.UpdateItemRequest{
			Name: sprint, RequestId: newID(), Fields: fields(t, map[string]any{"f_end_at": v}),
		}))
		invalid(t, "UpdateItem with "+name, err, "fields.f_end_at")
	}
}

// Ranges and order_by on a datetime, with bounds in any offset.
func TestDateTime_API_FilterRangeAndOrderBy(t *testing.T) {
	plat := space(t)
	name := "Sprint " + word()
	mk := func(end any) string {
		f := map[string]any{"f_name": name}
		if end != nil {
			f["f_end_at"] = end
		}
		return mustMk2(t, "ana", "sprint", plat, f)
	}
	// As sent, the texts sort b, a, c; as instants a, b, c.
	a := mk("2026-12-04T23:00:00+06:00") // 17:00Z
	b := mk("2026-12-04T18:00:00Z")
	c := mk("2026-12-04T15:00:00-05:00") // 20:00Z
	none := mk(nil)

	sprints := func(order string, ff ...*kernelv1.FieldFilter) []string {
		t.Helper()
		ff = append(ff, &kernelv1.FieldFilter{Field: "f_name", Eq: str(name)})
		return searchAll(t, "ana", &kernelv1.SearchItemsRequest{
			OrderBy: order, PageSize: 2,
			Filter: &kernelv1.ItemFilter{Types: []string{platRef("sprint")}, Fields: ff},
		})
	}
	if got := sprints("f_end_at"); !slices.Equal(got, []string{a, b, c, none}) {
		t.Errorf("by end, earliest first = %v, want %v (no end last)", got, []string{a, b, c, none})
	}
	if got := sprints("f_end_at desc"); !slices.Equal(got, []string{c, b, a, none}) {
		t.Errorf("by end, latest first = %v, want %v (no end last)", got, []string{c, b, a, none})
	}
	if got := sprints("f_end_at", &kernelv1.FieldFilter{
		Field: "f_end_at", Gte: str("2026-12-04T19:00:00+01:00"), Lt: str("2026-12-04T20:00:00Z"),
	}); !slices.Equal(got, []string{b}) {
		t.Errorf("ending from 18:00Z to before 20:00Z = %v, want %v", got, b)
	}
	if got := sprints("f_end_at", &kernelv1.FieldFilter{Field: "f_end_at", Eq: str("2026-12-04T17:00:00Z")}); !slices.Equal(got, []string{a}) {
		t.Errorf("ending at 17:00Z = %v, want %v", got, a)
	}
	_, err := as("ana").SearchItems(testCtx(t), connect.NewRequest(&kernelv1.SearchItemsRequest{
		Parent: "namespaces/acme", Filter: &kernelv1.ItemFilter{
			Types: []string{platRef("sprint")}, Fields: []*kernelv1.FieldFilter{{Field: "f_end_at", Gte: str("2026-12-04T19:00:00")}},
		},
	}))
	invalid(t, "a bound without an offset", err, "filter.fields[0]")
}

// --- what a Type shows ---

// Types are not secret: a client sees the datetime field type, a state's
// timers (after a duration, and at a field's instant) and a transition's
// computed effects.
func TestGetType_ShowsTimersAndEffects(t *testing.T) {
	typeOf := func(name string) *kernelv1.Type {
		t.Helper()
		res, err := as("ana").GetType(testCtx(t), connect.NewRequest(&kernelv1.GetTypeRequest{Name: name}))
		if err != nil {
			t.Fatalf("GetType %s: %v", name, err)
		}
		return res.Msg.Type
	}
	stateOf := func(ty *kernelv1.Type, id string) *kernelv1.State {
		t.Helper()
		for _, s := range ty.States {
			if s.Id == id {
				return s
			}
		}
		t.Fatalf("%s has no state %s", ty.Name, id)
		return nil
	}

	sprint := typeOf(platV2("sprint"))
	var endAt *kernelv1.Field
	for _, f := range sprint.Fields {
		if f.Id == "f_end_at" {
			endAt = f
		}
	}
	if endAt == nil || endAt.Type != kernelv1.FieldType_FIELD_TYPE_DATETIME {
		t.Errorf("sprint@2: f_end_at = %v, want a DATETIME field", endAt)
	}
	active := stateOf(sprint, "active")
	if active.At == nil || active.At.Field != "f_end_at" || active.At.ToState != "closed" || active.Timer != nil {
		t.Errorf("sprint@2: active = %v, want at {f_end_at, closed} and no duration timer", active)
	}
	if s := stateOf(sprint, "planned"); s.At != nil || s.Timer != nil {
		t.Errorf("sprint@2: planned = %v, want no timer", s)
	}
	// Version 1 has neither.
	if s := stateOf(typeOf(platType("sprint")), "active"); s.At != nil || s.Timer != nil {
		t.Errorf("sprint@1: active = %v, want no timer", s)
	}

	// A duration timer shows too: ticket@1's in_progress goes back to open
	// after 48 h.
	working := stateOf(typeOf(ticketV1), "in_progress")
	if working.Timer == nil || working.Timer.After != "48h" || working.Timer.ToState != "open" || working.At != nil {
		t.Errorf("ticket@1: in_progress = %v, want timer {48h, open}", working)
	}

	for _, id := range []string{"story", "bug", "task"} {
		ty := typeOf(platV2(id))
		for _, tr := range ty.Transitions {
			switch {
			case tr.ToState == "done":
				if len(tr.Sets) != 1 || tr.Sets["f_resolved_at"] != "cause.at" {
					t.Errorf("%s@2: %s -> done sets %v, want f_resolved_at: cause.at", id, tr.FromState, tr.Sets)
				}
			case tr.FromState == "done" && tr.ToState == "todo":
				if len(tr.Sets) != 1 || tr.Sets["f_resolved_at"] != "null" || !slices.Equal(tr.Clears, []string{"f_resolution"}) {
					t.Errorf("%s@2: done -> todo = %v, want sets f_resolved_at: null and clears f_resolution", id, tr)
				}
			default:
				if len(tr.Sets) != 0 {
					t.Errorf("%s@2: %s -> %s sets %v, want nothing", id, tr.FromState, tr.ToState, tr.Sets)
				}
			}
		}
		for _, f := range ty.Fields {
			if f.Id == "f_resolved_at" && (f.Type != kernelv1.FieldType_FIELD_TYPE_DATETIME ||
				f.WritableBy != kernelv1.FieldWriter_FIELD_WRITER_KERNEL) {
				t.Errorf("%s@2: f_resolved_at = %v, want a DATETIME only the kernel writes", id, f)
			}
		}
	}
}

// --- a story's resolution time ---

// Done sets f_resolved_at to the time of the transition's own event;
// reopening unsets it; nobody sets it by hand; a filter and an order find
// stories by it.
func TestPLAT_ResolvedAtIsSetByDoneAndClearedByReopen(t *testing.T) {
	plat := space(t)
	summary := "Resolve " + word()
	story := mustMk2(t, "ana", "story", plat, map[string]any{"f_summary": summary})
	other := mustMk2(t, "ana", "story", plat, map[string]any{"f_summary": summary})
	transition(t, "ana", story, "in_progress")

	_, err := as("ana").UpdateItem(testCtx(t), connect.NewRequest(&kernelv1.UpdateItemRequest{
		Name: story, RequestId: newID(), Fields: fields(t, map[string]any{"f_resolved_at": "2026-12-04T17:00:00Z"}),
	}))
	apiError(t, "f_resolved_at by hand", err, connect.CodePermissionDenied, "FIELD_NOT_WRITABLE")
	_, err = moveWith(t, "ana", story, "done", map[string]any{"f_resolution": "fixed", "f_resolved_at": "2026-12-04T17:00:00Z"})
	invalid(t, "a patch naming the computed field", err, "fields.f_resolved_at")

	before := time.Now().Add(-time.Second)
	done, err := moveWith(t, "ana", story, "done", map[string]any{"f_resolution": "fixed"})
	if err != nil {
		t.Fatalf("done: %v", err)
	}
	evs := events(t, "ana", story)
	last := evs[len(evs)-1]
	at := last.CreateTime.AsTime()
	if got := done.Item.Fields.AsMap()["f_resolved_at"]; got != stored(at) {
		t.Errorf("f_resolved_at = %v, want the done event's time %s", got, stored(at))
	}
	if at.Before(before) || at.After(time.Now().Add(time.Second)) {
		t.Errorf("the done event's time %v is not now", at)
	}
	if got := last.Fields.AsMap()["f_resolved_at"]; got != stored(at) {
		t.Errorf("the done event's fields = %v, want f_resolved_at with its own time", last.Fields)
	}
	if n := len(evs); n != 3 {
		t.Errorf("%d events, want 3: create, start, done (the effect is not a write of its own)", n)
	}

	resolved := func(ff ...*kernelv1.FieldFilter) []string {
		t.Helper()
		ff = append(ff, &kernelv1.FieldFilter{Field: "f_summary", Eq: str(summary)})
		return searchAll(t, "ana", &kernelv1.SearchItemsRequest{
			OrderBy: "f_resolved_at desc",
			Filter:  &kernelv1.ItemFilter{Types: []string{platRef("story")}, Fields: ff},
		})
	}
	if got := resolved(); !slices.Equal(got, []string{story, other}) {
		t.Errorf("by resolution time = %v, want the resolved story, then the open one", got)
	}
	since := &kernelv1.FieldFilter{Field: "f_resolved_at", Gte: str(before.UTC().Format(time.RFC3339Nano))}
	if got := resolved(since); !slices.Equal(got, []string{story}) {
		t.Errorf("resolved since a moment ago = %v, want %v", got, story)
	}

	reopened, err := moveWith(t, "ana", story, "todo", nil)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	f := reopened.Item.Fields.AsMap()
	if _, set := f["f_resolved_at"]; set {
		t.Errorf("f_resolved_at = %v after reopening, want it unset", f["f_resolved_at"])
	}
	if _, set := f["f_resolution"]; set {
		t.Errorf("f_resolution = %v after reopening, want it cleared", f["f_resolution"])
	}
	if got := resolved(since); len(got) != 0 {
		t.Errorf("resolved since a moment ago, after the reopen = %v, want none", got)
	}
}

// A version 1 story has no such field and no effect: Items stay on the
// version they were created with.
func TestPLAT_Version1StoriesAreUnchanged(t *testing.T) {
	plat := space(t)
	story := mk(t, "ana", "story", plat, map[string]any{"f_summary": "Old " + word()})
	transition(t, "ana", story, "in_progress")
	done, err := moveWith(t, "ana", story, "done", map[string]any{"f_resolution": "fixed"})
	if err != nil {
		t.Fatalf("done: %v", err)
	}
	if v, set := done.Item.Fields.AsMap()["f_resolved_at"]; set {
		t.Errorf("a story@1 has f_resolved_at = %v", v)
	}
}

// --- the refusal's shape ---

// EFFECT_FAILED is a known reason with a fixed message.
func TestEffectFailed_IsAKnownReason(t *testing.T) {
	if msg, ok := api.ReasonMessage("EFFECT_FAILED"); !ok || msg == "" {
		t.Errorf("ReasonMessage(EFFECT_FAILED) = %q, %v; want a fixed message", msg, ok)
	}
}
