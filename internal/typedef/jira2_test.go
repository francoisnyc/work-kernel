// Tests for Jira part 2, step 1: what a Type may declare (docs/design.md,
// "Decisions for Jira part 2", primitives 1, 2 and 4).
//
//   - datetime: a field type holding an instant. A value is sent as an RFC
//     3339 string with an offset and at most microseconds; it is stored and
//     returned in one fixed-width UTC form (DateTimeLayout), so byte order
//     is time order. CEL sees a timestamp.
//   - at: a state's timer at the instant one of its datetime fields holds,
//     with the load rules of a duration timer. A state has `timer` or `at`,
//     not both.
//   - sets: computed field effects on a transition, CEL expressions over
//     the fields, cause.at and today, with the load rules that keep a
//     declared effect from being refused on every run.
//   - The PLAT Types gain sprint@2 (f_end_at, closing itself) and story,
//     bug and task @2 (f_resolved_at); version 1 of each still loads.
package typedef_test

import (
	"encoding/json"
	"maps"
	"strings"
	"testing"
	"time"

	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// --- datetime ---

const datetimeSrc = `
id: meeting
version: 1
initial_state: s_planned
fields:
  - {id: f_title,    type: string}
  - {id: f_starts_at, type: datetime}
  - {id: f_ends_at,   type: datetime}
  - {id: f_day,       type: date}
states:
  - {id: s_planned, category: todo}
  - {id: s_held,    category: done}
transitions:
  - from: s_planned
    to: s_held
    guard: 'f_starts_at != null && f_ends_at > f_starts_at && f_starts_at >= f_day'
`

// A datetime field parses, survives the stored JSON, and a guard compares
// it as a timestamp, with another datetime and with a date (00:00 UTC).
func TestDateTime_FieldParsesAndGuardsCompareIt(t *testing.T) {
	ty := mustParse(t, datetimeSrc)
	if f, ok := ty.Field("f_starts_at"); !ok || f.Type != typedef.DateTime {
		t.Fatalf("f_starts_at = %+v, want type datetime", f)
	}
	data, err := json.Marshal(ty)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	back, err := typedef.FromJSON(data)
	if err != nil {
		t.Fatalf("FromJSON: %v", err)
	}
	if f, _ := back.Field("f_ends_at"); f.Type != typedef.DateTime {
		t.Errorf("f_ends_at after the round trip = %+v, want type datetime", f)
	}
	// A guard that treats it as a string does not compile.
	mustRefuse(t, "a datetime compared to a string",
		strings.Replace(datetimeSrc, "f_ends_at > f_starts_at", `f_ends_at > "2026-12-04"`, 1), "f_ends_at")
	// Options are for enums.
	mustRefuse(t, "a datetime with options",
		strings.Replace(datetimeSrc, "{id: f_ends_at,   type: datetime}", "{id: f_ends_at, type: datetime, options: [a]}", 1), "f_ends_at")
}

// One stored form: UTC, six fractional digits, Z. Any offset is accepted
// and normalised; the caller's offset is not kept.
func TestNormalizeDateTime(t *testing.T) {
	for in, want := range map[string]string{
		"2026-12-04T17:00:00Z":             "2026-12-04T17:00:00.000000Z",
		"2026-12-04T18:00:00+01:00":        "2026-12-04T17:00:00.000000Z",
		"2026-12-04T12:00:00-05:00":        "2026-12-04T17:00:00.000000Z",
		"2026-12-04T17:00:00.5Z":           "2026-12-04T17:00:00.500000Z",
		"2026-12-04T17:00:00.123456Z":      "2026-12-04T17:00:00.123456Z",
		"2026-12-04T17:00:00.000000Z":      "2026-12-04T17:00:00.000000Z",
		"2026-12-31T23:30:00.000001-01:00": "2027-01-01T00:30:00.000001Z",
		"0001-01-01T00:00:00Z":             "0001-01-01T00:00:00.000000Z",
		"9999-12-31T23:59:59.999999Z":      "9999-12-31T23:59:59.999999Z",
	} {
		got, ok := typedef.NormalizeDateTime(in)
		if !ok || got != want {
			t.Errorf("NormalizeDateTime(%q) = %q, %v; want %q", in, got, ok, want)
		}
		if len(want) != len(typedef.DateTimeLayout) {
			t.Fatalf("the test's own expectation %q is not fixed-width", want)
		}
	}
}

// Refused: no offset, finer than a microsecond (never rounded), a leap
// second, a year a four-digit form cannot hold once in UTC, a date alone,
// anything that is not a string.
func TestNormalizeDateTime_Refuses(t *testing.T) {
	for _, in := range []any{
		"2026-12-04T17:00:00",            // no offset
		"2026-12-04 17:00:00Z",           // a space is not RFC 3339's T
		"2026-12-04T17:00:00.1234567Z",   // finer than a microsecond
		"2026-12-04T17:00:00.123456789Z", // nanoseconds
		"2026-12-31T23:59:60Z",           // a leap second
		"2026-12-04",                     // a date
		"2026-13-01T00:00:00Z",           // no such month
		"2026-02-30T00:00:00Z",           // no such day
		"0001-01-01T00:00:00+01:00",      // the year 0 in UTC
		"9999-12-31T23:59:59-01:00",      // the year 10000 in UTC
		"", "tomorrow", 1796403600, true, nil,
	} {
		if got, ok := typedef.NormalizeDateTime(in); ok {
			t.Errorf("NormalizeDateTime(%v) = %q, want refused", in, got)
		}
	}
}

// ParseDateTime reads the stored form back, to the microsecond.
func TestParseDateTime(t *testing.T) {
	got, ok := typedef.ParseDateTime("2026-12-04T17:00:00.123456Z")
	want := time.Date(2026, 12, 4, 17, 0, 0, 123456000, time.UTC)
	if !ok || !got.Equal(want) {
		t.Errorf("ParseDateTime = %v, %v; want %v", got, ok, want)
	}
	for _, bad := range []any{"2026-12-04", "2026-12-04T17:00:00", 5, nil} {
		if _, ok := typedef.ParseDateTime(bad); ok {
			t.Errorf("ParseDateTime(%v): accepted", bad)
		}
	}
}

// CheckFields accepts what NormalizeDateTime accepts, in any offset, and
// refuses the rest; null clears, as for any field.
func TestDateTime_CheckFields(t *testing.T) {
	ty := mustParse(t, datetimeSrc)
	for _, ok := range []any{"2026-12-04T17:00:00Z", "2026-12-04T18:00:00+01:00", "2026-12-04T17:00:00.000001Z"} {
		if err := ty.CheckFields(map[string]any{"f_starts_at": ok}, false); err != nil {
			t.Errorf("CheckFields(f_starts_at = %v): %v", ok, err)
		}
	}
	for _, bad := range []any{"2026-12-04T17:00:00", "2026-12-04", "2026-12-04T17:00:00.0000001Z", 12, true} {
		err := ty.CheckFields(map[string]any{"f_starts_at": bad}, false)
		if err == nil {
			t.Errorf("CheckFields(f_starts_at = %v): accepted", bad)
		} else if !strings.Contains(err.Error(), "f_starts_at") {
			t.Errorf("CheckFields(f_starts_at = %v): %q does not name the field", bad, err)
		}
	}
	if err := ty.CheckFields(map[string]any{"f_starts_at": nil}, true); err != nil {
		t.Errorf("CheckFields(f_starts_at = null): %v", err)
	}
}

// --- at: a timer at a field's instant ---

// atSrc: STATE is the active state's declaration, CLOSE the closing
// transition.
const atSrc = `
id: iteration
version: 1
initial_state: s_planned
fields:
  - {id: f_name,   type: string}
  - {id: f_end,    type: date}
  - {id: f_end_at, type: datetime}
states:
  - {id: s_planned, category: backlog}
  - STATE
  - {id: s_closed,  category: done}
transitions:
  - {from: s_planned, to: s_active}
  - CLOSE
`

const (
	atState = "{id: s_active, category: active, at: {field: f_end_at, to: s_closed}}"
	atClose = "{from: s_active, to: s_closed}"
)

func atType(state, closing string) string {
	return strings.NewReplacer("STATE", state, "CLOSE", closing).Replace(atSrc)
}

// The declaration is kept as written and survives the stored JSON. The
// timer acts as system:timer, so an `allowed` list must name it.
func TestAt_Parse(t *testing.T) {
	ty := mustParse(t, atType(atState, atClose))
	active, _ := ty.State("s_active")
	if active.At == nil || active.At.Field != "f_end_at" || active.At.To != "s_closed" {
		t.Fatalf("At = %+v, want f_end_at to s_closed", active.At)
	}
	if active.Timer != nil {
		t.Errorf("the state also has a duration timer: %+v", active.Timer)
	}
	if s, _ := ty.State("s_planned"); s.At != nil {
		t.Errorf("s_planned has an at-timer: %+v", s.At)
	}
	data, err := json.Marshal(ty)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	back, err := typedef.FromJSON(data)
	if err != nil {
		t.Fatalf("FromJSON: %v", err)
	}
	if s, _ := back.State("s_active"); s.At == nil || *s.At != *active.At {
		t.Errorf("At after the round trip = %+v", s.At)
	}
	mustParse(t, atType(atState,
		"{from: s_active, to: s_closed, allowed: ['role:lead', 'principal:system:timer']}"))
}

func TestAt_RefusedAtLoad(t *testing.T) {
	state := func(at string) string { return "{id: s_active, category: active, " + at + "}" }
	cases := []struct {
		name, state, closing, mention string
	}{
		{"no field", state("at: {to: s_closed}"), atClose, "field"},
		{"no target state", state("at: {field: f_end_at}"), atClose, "to"},
		{"an unknown field", state("at: {field: f_nope, to: s_closed}"), atClose, "f_nope"},
		// A date has no time zone: which instant ends that day is not
		// decided. Datetime fields only.
		{"a date field", state("at: {field: f_end, to: s_closed}"), atClose, "f_end"},
		{"a string field", state("at: {field: f_name, to: s_closed}"), atClose, "f_name"},
		{"a move the Type does not declare", state("at: {field: f_end_at, to: s_planned}"), atClose, "s_planned"},
		{"an unknown target state", state("at: {field: f_end_at, to: s_nope}"), atClose, "s_nope"},
		{"both a duration timer and an at-timer",
			state("timer: {after: 4h, to: s_closed}, at: {field: f_end_at, to: s_closed}"), atClose, "s_active"},
		{"an allowed list that leaves the timer out", atState,
			"{from: s_active, to: s_closed, allowed: ['role:lead']}", "principal:system:timer"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mustRefuse(t, c.name, atType(c.state, c.closing), c.mention)
		})
	}
}

// --- sets: computed field effects ---

// setsSrc: DONE is the done state's declaration, T the transition under
// test, HOOKS optional hooks.
const setsSrc = `
id: chore
version: 1
initial_state: s_open
fields:
  - {id: f_note,        type: string}
  - {id: f_resolution,  type: enum, options: [fixed, wontfix]}
  - {id: f_points,      type: int}
  - {id: f_billable,    type: bool}
  - {id: f_closed_on,   type: date}
  - {id: f_started_at,  type: datetime}
  - {id: f_resolved_at, type: datetime, writable_by: kernel}
  - {id: f_grade,       type: int, writable_by: ['role:lead']}
  - {id: f_reviewer,    type: principal}
states:
  - {id: s_open, category: todo}
  - DONE
transitions:
  - T
  - {from: s_done, to: s_open}
HOOKS
`

func setsType(done, transition, hooks string) string {
	return strings.NewReplacer("DONE", done, "T", transition, "HOOKS", hooks).Replace(setsSrc)
}

func closing(sets string) string {
	return "{from: s_open, to: s_done, sets: {" + sets + "}}"
}

// Effects are kept as written and survive the stored JSON. A field written
// by `sets` is written by the Type, not the caller: it may be one only the
// kernel or a role writes. null unsets.
func TestSets_Parse(t *testing.T) {
	want := map[string]string{
		"f_resolved_at": "cause.at",
		"f_closed_on":   "today",
		"f_points":      "f_points == null ? 1 : f_points * 2",
		"f_billable":    "f_points != null",
		"f_resolution":  `"fixed"`,
		"f_note":        "null",
		"f_grade":       "3",
		"f_started_at":  "f_started_at == null ? cause.at : f_started_at",
	}
	var parts []string
	for id, expr := range want {
		parts = append(parts, id+": '"+expr+"'")
	}
	ty := mustParse(t, setsType(doneState, closing(strings.Join(parts, ", ")), ""))
	tr, ok := ty.Transition("s_open", "s_done")
	if !ok || !maps.Equal(tr.Sets, want) {
		t.Fatalf("Sets = %v, want %v", tr.Sets, want)
	}
	data, err := json.Marshal(ty)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	back, err := typedef.FromJSON(data)
	if err != nil {
		t.Fatalf("FromJSON: %v", err)
	}
	if tr, _ := back.Transition("s_open", "s_done"); !maps.Equal(tr.Sets, want) {
		t.Errorf("Sets after the round trip = %v", tr.Sets)
	}
	// A move may clear some fields and set others.
	mustParse(t, setsType(doneState, "{from: s_open, to: s_done, clears: [f_note], sets: {f_resolved_at: 'cause.at'}}", ""))
}

// An effect that could never run, or that contradicts its own transition,
// is refused when the Type loads.
func TestSets_RefusedAtLoad(t *testing.T) {
	cases := []struct {
		name, done, transition, mention string
	}{
		{"an unknown field", doneState, closing("f_nope: 'cause.at'"), "f_nope"},
		{"a field the same transition requires", doneState,
			"{from: s_open, to: s_done, required: [f_note], sets: {f_note: '\"x\"'}}", "f_note"},
		{"a field the same transition clears", doneState,
			"{from: s_open, to: s_done, clears: [f_note], sets: {f_note: 'null'}}", "f_note"},
		{"a field its own allowed names", doneState,
			"{from: s_open, to: s_done, allowed: ['field:f_reviewer'], sets: {f_reviewer: 'null'}}", "f_reviewer"},
		// Who acted is identity, not data: it waits for a declared keyword.
		{"a principal field", doneState, closing("f_reviewer: '\"user:ana\"'"), "f_reviewer"},
		{"an expression that does not compile", doneState, closing("f_points: 'f_points +'"), "f_points"},
		{"an unknown variable", doneState, closing("f_points: 'f_nope + 1'"), "f_nope"},
		{"a timestamp for an int", doneState, closing("f_points: 'cause.at'"), "f_points"},
		{"an int for a datetime", doneState, closing("f_resolved_at: '5'"), "f_resolved_at"},
		{"a string for a date", doneState, closing("f_closed_on: '\"2026-12-04\"'"), "f_closed_on"},
		{"a bool for an enum", doneState, closing("f_resolution: 'true'"), "f_resolution"},
		// Identity stays out of CEL, in effects as in guards.
		{"reading the caller", doneState, closing("f_note: 'caller'"), "caller"},
		// Links are not in scope for an effect (Later).
		{"reading links", doneState, closing("f_points: 'size(links.blocks)'"), "links"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mustRefuse(t, c.name, setsType(c.done, c.transition, ""), c.mention)
		})
	}
	// A field frozen in the state the move leaves.
	t.Run("a field the state it leaves freezes", func(t *testing.T) {
		src := strings.Replace(setsType(doneState, closing("f_note: 'null'"), ""),
			"{id: s_open, category: todo}", "{id: s_open, category: todo, frozen: [f_note]}", 1)
		mustRefuse(t, "sets on a frozen field", src, "f_note")
	})
}

// A transition that a check holds may not set fields, as it may not clear
// them: which of a held move's two writes would compute, and at which
// event's time, is not designed (Later).
func TestSets_OnAHeldTransition_Refused(t *testing.T) {
	check := "hooks:\n  - {id: recheck, on: transition, from: s_open, to: s_done, kind: check, action: erp.check_credit, reads: [f_note, f_points], writes: [f_note]}\n"
	mustParse(t, setsType(doneState, "{from: s_open, to: s_done}", check))
	mustRefuse(t, "sets on a transition a check holds",
		setsType(doneState, closing("f_resolved_at: 'cause.at'"), check), "recheck")
}

// `cause` joins the reserved names: no field may take it.
func TestCause_IsAReservedName(t *testing.T) {
	src := strings.Replace(setsType(doneState, "{from: s_open, to: s_done}", ""),
		"{id: f_note,        type: string}", "{id: cause, type: string}", 1)
	mustRefuse(t, "a field named cause", src, "cause")
}

// A guard does not read cause.at in this step: guards keep `today`.
func TestCauseAt_IsNotAGuardVariable(t *testing.T) {
	mustRefuse(t, "a guard reading cause.at",
		setsType(doneState, "{from: s_open, to: s_done, guard: 'f_started_at < cause.at'}", ""), "cause")
}

// --- the PLAT Types, version 2 ---

// sprint@2 closes itself at f_end_at; story, bug and task @2 record when
// they were resolved. Version 1 of each still loads: Items stay on the
// version they were created with.
func TestPLATTypes_Version2(t *testing.T) {
	types, err := typedef.ParseDir("../../types")
	if err != nil {
		t.Fatalf("ParseDir: %v", err)
	}
	version := func(id string, v int) *typedef.Type {
		t.Helper()
		for _, ty := range types {
			if ty.ID == id && ty.Version == v {
				return ty
			}
		}
		t.Fatalf("types/ has no %s@%d", id, v)
		return nil
	}

	version("sprint", 1)
	sprint := version("sprint", 2)
	if f, ok := sprint.Field("f_end_at"); !ok || f.Type != typedef.DateTime {
		t.Errorf("sprint@2: f_end_at = %+v, want a datetime", f)
	}
	for _, id := range []string{"f_name", "f_start", "f_end", "f_goal"} {
		if _, ok := sprint.Field(id); !ok {
			t.Errorf("sprint@2 lost %s", id)
		}
	}
	active, _ := sprint.State("active")
	if active.At == nil || active.At.Field != "f_end_at" || active.At.To != "closed" {
		t.Errorf("sprint@2: active's at-timer = %+v, want f_end_at to closed", active.At)
	}
	// Closing by hand still works: the closing move has no allowed list.
	if tr, ok := sprint.Transition("active", "closed"); !ok || len(tr.Allowed) != 0 {
		t.Errorf("sprint@2: active -> closed = %+v, want it open to anyone", tr)
	}

	for _, id := range []string{"story", "bug", "task"} {
		version(id, 1)
		ty := version(id, 2)
		f, ok := ty.Field("f_resolved_at")
		if !ok || f.Type != typedef.DateTime || f.WritableBy != typedef.WritableByKernel {
			t.Errorf("%s@2: f_resolved_at = %+v, want a datetime only the kernel writes", id, f)
		}
		for _, from := range []string{"in_review", "in_progress"} {
			tr, _ := ty.Transition(from, "done")
			if tr.Sets["f_resolved_at"] != "cause.at" {
				t.Errorf("%s@2: %s -> done sets %v, want f_resolved_at = cause.at", id, from, tr.Sets)
			}
		}
		reopen, _ := ty.Transition("done", "todo")
		if reopen.Sets["f_resolved_at"] != "null" {
			t.Errorf("%s@2: done -> todo sets %v, want f_resolved_at = null", id, reopen.Sets)
		}
		if len(reopen.Clears) != 1 || reopen.Clears[0] != "f_resolution" {
			t.Errorf("%s@2: done -> todo clears %v, want [f_resolution]", id, reopen.Clears)
		}
		// Still nothing listening: an effect is computed in the write.
		for _, s := range ty.States {
			if s.Timer != nil || s.At != nil || len(s.Acting) > 0 {
				t.Errorf("%s@2: state %s has a timer; the issue Types have none", id, s.ID)
			}
		}
		if len(ty.Hooks) > 0 || len(ty.Reactions) > 0 {
			t.Errorf("%s@2 has hooks or reactions; a drag must stay one row and one event", id)
		}
	}
}
