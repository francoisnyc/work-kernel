// Tests for Jira part 2, step 1, in the pure lifecycle function.
//
// Computed field effects: a legal move returns what its `sets` expressions
// evaluate to, in each field's stored form (nil to unset). They read one
// view, the guard's: the fields as the patch leaves them, before the
// clears, plus cause.at (the time of the event being written) and today,
// which to an effect is the UTC date of cause.at (never Input.Today, the
// service's clock that guards read): one write sees one now.
// They do not see each other's results. A failing expression refuses the
// move (effect_failed, naming the field).
//
// Timers at a field's instant: AtTimerFor, and a state with one needs a
// workflow even while the field is unset (it must hear the field being set).
package lifecycle_test

import (
	"errors"
	"maps"
	"testing"
	"time"

	"github.com/francoisnyc/work-kernel/internal/lifecycle"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

const setsType = `
id: chore
version: 1
initial_state: s_open
fields:
  - {id: f_note,        type: string}
  - {id: f_resolution,  type: enum, options: [fixed, wontfix]}
  - {id: f_points,      type: int}
  - {id: f_double,      type: int}
  - {id: f_billable,    type: bool}
  - {id: f_closed_on,   type: date}
  - {id: f_started_at,  type: datetime}
  - {id: f_resolved_at, type: datetime, writable_by: kernel}
  - {id: f_due_at,      type: datetime}
states:
  - {id: s_open,   category: todo}
  - {id: s_done,   category: done}
  - {id: s_parked, category: waiting}
transitions:
  - from: s_open
    to: s_done
    guard: 'f_points != null'
    clears: [f_note]
    sets:
      f_resolved_at: 'cause.at'
      f_closed_on:   'today'
      f_double:      'f_points * 2'
      f_billable:    'f_note != null'
      f_started_at:  'f_started_at == null ? cause.at : f_started_at'
      f_due_at:      'cause.at + duration("36h")'
  - from: s_done
    to: s_open
    sets: {f_resolved_at: 'null', f_closed_on: 'null', f_double: 'null'}
  # Each of these can fail when it runs.
  - {from: s_open,   to: s_parked, sets: {f_double: 'f_points * 2'}}
  - {from: s_parked, to: s_open,   sets: {f_resolution: 'f_note'}}
  - {from: s_parked, to: s_done,   sets: {f_closed_on: 'cause.at'}}
`

var (
	causeAt = time.Date(2026, 12, 4, 17, 30, 15, 123456000, time.UTC)
	// The service's clock, a day later than cause.at's date on purpose:
	// guards read it, effects never do, and the tests tell them apart.
	someDay = time.Date(2026, 12, 5, 9, 0, 0, 0, time.UTC)
)

func setsTy(t *testing.T) *typedef.Type {
	t.Helper()
	ty, err := typedef.Parse([]byte(setsType))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return ty
}

// A legal move returns its effects, evaluated on the guard's view: f_note
// is still there (the clear comes after), f_points is the patched value.
func TestTransition_ReturnsWhatItSets(t *testing.T) {
	ty := setsTy(t)
	fields := map[string]any{"f_note": "ship it", "f_points": float64(3)} // as read back from JSON
	res, err := lifecycle.TransitionWith(ty, "s_open",
		lifecycle.Input{Fields: fields, Today: someDay, CauseAt: causeAt}, "s_done")
	if err != nil {
		t.Fatalf("close: %v", err)
	}
	want := map[string]any{
		"f_resolved_at": "2026-12-04T17:30:15.123456Z",
		"f_closed_on":   "2026-12-04", // the date of cause.at, not Input.Today
		"f_double":      int64(6),
		"f_billable":    true, // f_note is read before it is cleared
		"f_started_at":  "2026-12-04T17:30:15.123456Z",
		"f_due_at":      "2026-12-06T05:30:15.123456Z",
	}
	if len(res.Sets) != len(want) {
		t.Fatalf("Sets = %v, want %v", res.Sets, want)
	}
	for id, w := range want {
		got := res.Sets[id]
		if n, ok := typedef.AsInt(got); ok && id == "f_double" {
			got = n
		}
		if got != w {
			t.Errorf("Sets[%s] = %v (%T), want %v", id, res.Sets[id], res.Sets[id], w)
		}
	}
	if len(res.Clears) != 1 || res.Clears[0] != "f_note" {
		t.Errorf("Clears = %v, want [f_note]", res.Clears)
	}
	// Pure: the fields it was given are untouched.
	if !maps.Equal(fields, map[string]any{"f_note": "ship it", "f_points": float64(3)}) {
		t.Errorf("TransitionWith changed its argument: %v", fields)
	}
}

// An expression reads the stored value of a datetime field as a timestamp:
// a start already recorded is kept.
func TestTransition_SetsReadStoredDateTimes(t *testing.T) {
	ty := setsTy(t)
	res, err := lifecycle.TransitionWith(ty, "s_open", lifecycle.Input{
		Fields: map[string]any{"f_points": float64(1), "f_started_at": "2026-12-01T08:00:00.000000Z"},
		Today:  someDay, CauseAt: causeAt,
	}, "s_done")
	if err != nil {
		t.Fatalf("close: %v", err)
	}
	if got := res.Sets["f_started_at"]; got != "2026-12-01T08:00:00.000000Z" {
		t.Errorf("f_started_at = %v, want the start already recorded", got)
	}
	if got := res.Sets["f_billable"]; got != false {
		t.Errorf("f_billable = %v, want false (no note)", got)
	}
}

// null unsets: the result names the field with a nil value, whether or not
// it had one (the caller records only what had a value).
func TestTransition_SetsNullUnsets(t *testing.T) {
	ty := setsTy(t)
	res, err := lifecycle.TransitionWith(ty, "s_done", lifecycle.Input{
		Fields: map[string]any{"f_resolved_at": "2026-12-04T17:30:15.123456Z", "f_double": float64(6)},
		Today:  someDay, CauseAt: causeAt,
	}, "s_open")
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	for _, id := range []string{"f_resolved_at", "f_closed_on", "f_double"} {
		v, named := res.Sets[id]
		if !named || v != nil {
			t.Errorf("Sets[%s] = %v, named %v; want it named with nil", id, v, named)
		}
	}
}

// A move without effects returns none.
func TestTransition_NoSets(t *testing.T) {
	ty, err := typedef.Parse([]byte(clearsType))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	res, err := lifecycle.Transition(ty, "s_open", map[string]any{"f_resolution": "fixed"}, "s_done")
	if err != nil || len(res.Sets) != 0 {
		t.Errorf("close = %+v, %v; want no sets", res, err)
	}
}

// The guard is judged first: a move it refuses evaluates nothing.
func TestTransition_GuardBeforeSets(t *testing.T) {
	ty := setsTy(t)
	_, err := lifecycle.TransitionWith(ty, "s_open",
		lifecycle.Input{Fields: map[string]any{}, Today: someDay, CauseAt: causeAt}, "s_done")
	var rej *lifecycle.Rejection
	if !errors.As(err, &rej) || rej.Code != lifecycle.GuardFailed {
		t.Fatalf("err = %v, want guard_failed", err)
	}
}

// An effect that cannot be computed refuses the move, naming its field and
// never a value: arithmetic on an unset field, a string that is no option
// of the enum, a timestamp that is not a date (not 00:00 UTC), and a
// cause.at nobody gave.
func TestTransition_EffectFailed(t *testing.T) {
	ty := setsTy(t)
	cases := []struct {
		name, from, to, field string
		in                    lifecycle.Input
	}{
		{"arithmetic on an unset field", "s_open", "s_parked", "f_double",
			lifecycle.Input{Fields: map[string]any{}, Today: someDay, CauseAt: causeAt}},
		{"a string that is no option", "s_parked", "s_open", "f_resolution",
			lifecycle.Input{Fields: map[string]any{"f_note": "secret words"}, Today: someDay, CauseAt: causeAt}},
		{"an instant that is not a date", "s_parked", "s_done", "f_closed_on",
			lifecycle.Input{Fields: map[string]any{}, Today: someDay, CauseAt: causeAt}},
		{"no cause.at", "s_open", "s_done", "",
			lifecycle.Input{Fields: map[string]any{"f_points": float64(1)}, Today: someDay}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := lifecycle.TransitionWith(ty, c.from, c.in, c.to)
			var rej *lifecycle.Rejection
			if !errors.As(err, &rej) || rej.Code != lifecycle.EffectFailed {
				t.Fatalf("err = %v, want effect_failed", err)
			}
			if c.field != "" && rej.Field != c.field {
				t.Errorf("Field = %q, want %q", rej.Field, c.field)
			}
			if rej.From != c.from || rej.To != c.to {
				t.Errorf("From, To = %q, %q; want %q, %q", rej.From, rej.To, c.from, c.to)
			}
			if got := rej.Error(); containsAny(got, "secret words", "2026-12-04") {
				t.Errorf("the reason holds a value: %q", got)
			}
		})
	}
	// An enum set to one of its options is fine.
	res, err := lifecycle.TransitionWith(ty, "s_parked",
		lifecycle.Input{Fields: map[string]any{"f_note": "wontfix"}, Today: someDay, CauseAt: causeAt}, "s_open")
	if err != nil || res.Sets["f_resolution"] != "wontfix" {
		t.Errorf("an option: %+v, %v", res, err)
	}
	// An instant at 00:00 UTC is a date.
	midnight := time.Date(2026, 12, 4, 0, 0, 0, 0, time.UTC)
	res, err = lifecycle.TransitionWith(ty, "s_parked",
		lifecycle.Input{Fields: map[string]any{}, Today: someDay, CauseAt: midnight}, "s_done")
	if err != nil || res.Sets["f_closed_on"] != "2026-12-04" {
		t.Errorf("midnight as a date: %+v, %v", res, err)
	}
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if len(sub) > 0 && len(s) >= len(sub) {
			for i := 0; i+len(sub) <= len(s); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
		}
	}
	return false
}

// --- timers at a field's instant ---

const atType = `
id: iteration
version: 1
initial_state: s_planned
fields:
  - {id: f_name,   type: string}
  - {id: f_end_at, type: datetime}
states:
  - {id: s_planned, category: backlog}
  - {id: s_active,  category: active, at: {field: f_end_at, to: s_closed}}
  - {id: s_closed,  category: done}
transitions:
  - {from: s_planned, to: s_active}
  - {from: s_active,  to: s_closed}
`

func TestAtTimerFor(t *testing.T) {
	ty, err := typedef.Parse([]byte(atType))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	at, ok := lifecycle.AtTimerFor(ty, "s_active")
	if !ok || at.Field != "f_end_at" || at.To != "s_closed" {
		t.Errorf("AtTimerFor(s_active) = %+v, %v; want f_end_at to s_closed", at, ok)
	}
	for _, s := range []string{"s_planned", "s_closed", "s_nope"} {
		if at, ok := lifecycle.AtTimerFor(ty, s); ok {
			t.Errorf("AtTimerFor(%s) = %+v, want none", s, at)
		}
	}
	// It is not a duration timer.
	if _, ok := lifecycle.TimerFor(ty, "s_active"); ok {
		t.Error("TimerFor(s_active): an at-timer is not a duration timer")
	}
}

// A state with an at-timer needs a workflow, set or unset: the run must be
// there to hear the field being set. So the Type has listeners, and its
// events go to the outbox.
func TestAtTimer_NeedsWorkflowAndListeners(t *testing.T) {
	ty, err := typedef.Parse([]byte(atType))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !lifecycle.NeedsWorkflow(ty, "s_active") {
		t.Error("NeedsWorkflow(s_active) = false, want true")
	}
	if lifecycle.NeedsWorkflow(ty, "s_planned") || lifecycle.NeedsWorkflow(ty, "s_closed") {
		t.Error("NeedsWorkflow is true for a state without behavior")
	}
	if !lifecycle.HasListeners(ty) {
		t.Error("HasListeners = false, want true")
	}
	if !lifecycle.StartsWorkflow(ty, lifecycle.Event{Op: "transition", From: "s_planned", Entered: "s_active"}) {
		t.Error("entering s_active does not start a workflow")
	}
}

// Effects alone are computed in the write: a Type with `sets` and nothing
// else has no listeners.
func TestSets_AreNotListeners(t *testing.T) {
	if lifecycle.HasListeners(setsTy(t)) {
		t.Error("HasListeners = true for a Type whose only feature is sets")
	}
}

// Around midnight UTC an effect's today follows cause.at to the
// microsecond, whatever day the service's clock (Input.Today) says it is.
func TestTransition_SetsTodayIsTheDateOfCauseAt(t *testing.T) {
	ty := setsTy(t)
	fields := map[string]any{"f_points": float64(1)}
	for _, c := range []struct {
		causeAt, today time.Time
		want           string
	}{
		{time.Date(2026, 12, 4, 23, 59, 59, 999999000, time.UTC), time.Date(2026, 12, 5, 0, 0, 0, 0, time.UTC), "2026-12-04"},
		{time.Date(2026, 12, 5, 0, 0, 0, 0, time.UTC), time.Date(2026, 12, 4, 23, 59, 59, 0, time.UTC), "2026-12-05"},
		// The date is UTC's, whatever zone the time value carries.
		{time.Date(2026, 12, 5, 1, 30, 0, 0, time.FixedZone("CET", 3600)), time.Date(2026, 12, 5, 0, 30, 0, 0, time.UTC), "2026-12-05"},
		{time.Date(2026, 12, 5, 0, 30, 0, 0, time.FixedZone("CET", 3600)), time.Date(2026, 12, 5, 0, 30, 0, 0, time.UTC), "2026-12-04"},
		// No service clock at all: an effect does not need it.
		{time.Date(2026, 12, 31, 23, 59, 59, 0, time.UTC), time.Time{}, "2026-12-31"},
	} {
		res, err := lifecycle.TransitionWith(ty, "s_open",
			lifecycle.Input{Fields: fields, Today: c.today, CauseAt: c.causeAt}, "s_done")
		if err != nil {
			t.Fatalf("cause.at %v: %v", c.causeAt, err)
		}
		if got := res.Sets["f_closed_on"]; got != c.want {
			t.Errorf("cause.at %v, service clock %v: today = %v, want %s", c.causeAt, c.today, got, c.want)
		}
	}
}
