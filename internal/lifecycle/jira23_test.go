// Tests for Jira part 2, step 3a, in the lifecycle's pure functions.
//
//   - Working time in effects: a transition's `sets` computes an elapsed
//     time and a due instant with working_between and working_after on a
//     calendar built from the Item's own fields, always from cause.at.
//   - Creation enters the initial state: an enter reaction without `from`
//     runs then.
//   - An acting timer at an instant: which a state has, and what it sets
//     (cause.at is the instant itself).
//   - A reaction that moves children: which contained Items match.
//   - A reaction that sets: the values, in the target's stored form.
//   - A create reaction's fields read linked Items.
package lifecycle_test

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/francoisnyc/work-kernel/internal/lifecycle"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

const calExpr = "calendar(f_week, f_days_off, f_zone)"

// gauge measures working time against a target. Elapsed time and the due
// instant are computed by its own transitions, from cause.at.
var gaugeType = `
id: gauge
version: 1
initial_state: running
fields:
  - {id: f_kind,       type: enum, options: [main, reply]}
  - {id: f_target_s,   type: int}
  - {id: f_elapsed_s,  type: int}
  - {id: f_resumed_at, type: datetime}
  - {id: f_due_at,     type: datetime}
  - {id: f_late_at,    type: datetime}
  - {id: f_outcome,    type: enum, options: [met, missed]}
  - {id: f_week,       type: string, multiple: true}
  - {id: f_days_off,   type: date,   multiple: true}
  - {id: f_zone,       type: string}
  - {id: f_note,       type: string}
states:
  - id: running
    category: active
    acting:
      - {id: late, at: f_due_at, when: 'f_late_at == null', sets: {f_late_at: 'cause.at', f_note: 'string(today)'}}
      - {id: sum,  at: f_resumed_at, sets: {f_elapsed_s: 'f_elapsed_s + f_target_s'}}
  - {id: paused,  category: waiting}
  - {id: stopped, category: done}
transitions:
  - from: running
    to: paused
    sets:
      f_elapsed_s: '(f_elapsed_s == null ? 0 : f_elapsed_s) + working_between(f_resumed_at, cause.at, CAL)'
      f_due_at: 'null'
      f_late_at: 'f_due_at == null || cause.at >= f_due_at ? f_late_at : null'
  - from: paused
    to: running
    sets:
      f_resumed_at: 'cause.at'
      f_due_at: 'working_after(cause.at, f_target_s - f_elapsed_s, CAL)'
  - from: running
    to: stopped
    sets:
      f_elapsed_s: '(f_elapsed_s == null ? 0 : f_elapsed_s) + working_between(f_resumed_at, cause.at, CAL)'
      f_outcome: '(f_elapsed_s == null ? 0 : f_elapsed_s) + working_between(f_resumed_at, cause.at, CAL) <= f_target_s ? "met" : "missed"'
      f_late_at: 'f_due_at == null || cause.at >= f_due_at ? f_late_at : null'
      f_due_at: 'null'
  - from: paused
    to: stopped
    sets:
      f_outcome: 'f_elapsed_s <= f_target_s ? "met" : "missed"'
reactions:
  - {id: show, kind: set, on: enter, state: running, target: container, set: {f_due: 'f_due_at', f_label: 'f_kind', f_seen_at: 'cause.at'}}
  - {id: hide, kind: set, on: enter, state: paused, from: [running], target: container, set: {f_due: 'null'}}
  - {id: sum,  kind: set, on: enter, state: stopped, target: container, set: {f_spent_s: 'f_elapsed_s + 0'}}
`

func gaugeTy(t *testing.T) *typedef.Type {
	t.Helper()
	return parseYAML(t, strings.ReplaceAll(gaugeType, "CAL", calExpr))
}

// The office: Monday to Friday, nine to five, in Paris; Christmas off. As
// the fields store them.
func office() map[string]any {
	return map[string]any{
		"f_week":     []any{"fri 09:00-17:00", "mon 09:00-17:00", "thu 09:00-17:00", "tue 09:00-17:00", "wed 09:00-17:00"},
		"f_days_off": []any{"2026-12-25"},
		"f_zone":     "Europe/Paris",
	}
}

func with(base map[string]any, more map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range more {
		out[k] = v
	}
	return out
}

func instant(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse("2006-01-02 15:04:05", s)
	if err != nil {
		t.Fatalf("bad test instant %q: %v", s, err)
	}
	return v
}

// stored is an instant as a datetime field stores it.
func stored(t *testing.T, s string) string {
	t.Helper()
	return instant(t, s).Format("2006-01-02T15:04:05.000000Z")
}

func intOf(t *testing.T, v any) int64 {
	t.Helper()
	n, ok := typedef.AsInt(v)
	if !ok {
		t.Fatalf("%v (%T) is not an int", v, v)
	}
	return n
}

func move(t *testing.T, ty *typedef.Type, from, to string, fields map[string]any, at string) lifecycle.Result {
	t.Helper()
	res, err := lifecycle.TransitionWith(ty, from, lifecycle.Input{Fields: fields, CauseAt: instant(t, at)}, to)
	if err != nil {
		t.Fatalf("%s -> %s at %s: %v", from, to, at, err)
	}
	return res
}

// --- working time in a transition's effects ---

// A gauge of four working hours, started Monday 10:00 in Paris (09:00
// UTC), paused at 11:30, resumed Tuesday at 08:00 (before the office
// opens), stopped at 10:00. Every number comes from cause.at.
func TestSets_PauseResumeStop_OnWorkingTime(t *testing.T) {
	ty := gaugeTy(t)
	running := with(office(), map[string]any{
		"f_kind": "main", "f_target_s": float64(4 * 3600),
		"f_resumed_at": stored(t, "2026-11-23 09:00:00"), "f_due_at": stored(t, "2026-11-23 13:00:00"),
	})

	// Paused Monday 11:30 Paris: an hour and a half are gone, and no
	// instant is due while it waits.
	p := move(t, ty, "running", "paused", running, "2026-11-23 10:30:00")
	if got := intOf(t, p.Sets["f_elapsed_s"]); got != 5400 {
		t.Errorf("paused: f_elapsed_s = %d, want 5400", got)
	}
	if v, named := p.Sets["f_due_at"]; !named || v != nil {
		t.Errorf("paused: f_due_at = %v, named %v; want unset", v, named)
	}
	if v, named := p.Sets["f_late_at"]; !named || v != nil {
		t.Errorf("paused: f_late_at = %v, named %v; want named and still unset", v, named)
	}

	// Resumed Tuesday 08:00 Paris (07:00 UTC): two and a half hours are
	// left, counted from the opening: due at 11:30 Paris.
	paused := with(running, map[string]any{"f_elapsed_s": float64(5400), "f_due_at": nil})
	r := move(t, ty, "paused", "running", paused, "2026-11-24 07:00:00")
	if r.Sets["f_resumed_at"] != stored(t, "2026-11-24 07:00:00") || r.Sets["f_due_at"] != stored(t, "2026-11-24 10:30:00") {
		t.Errorf("resumed: f_resumed_at %v, f_due_at %v; want 07:00Z and 10:30Z", r.Sets["f_resumed_at"], r.Sets["f_due_at"])
	}

	// Stopped Tuesday 10:00 Paris: one more hour worked, 9000 s of 14400.
	again := with(paused, map[string]any{"f_resumed_at": r.Sets["f_resumed_at"], "f_due_at": r.Sets["f_due_at"]})
	s := move(t, ty, "running", "stopped", again, "2026-11-24 09:00:00")
	if got := intOf(t, s.Sets["f_elapsed_s"]); got != 9000 || s.Sets["f_outcome"] != "met" {
		t.Errorf("stopped at 10:00: f_elapsed_s %d, f_outcome %v; want 9000, met", got, s.Sets["f_outcome"])
	}
	// Stopped at 12:00 Paris instead: 16200 s, over the target.
	s = move(t, ty, "running", "stopped", again, "2026-11-24 11:00:00")
	if got := intOf(t, s.Sets["f_elapsed_s"]); got != 16200 || s.Sets["f_outcome"] != "missed" {
		t.Errorf("stopped at 12:00: f_elapsed_s %d, f_outcome %v; want 16200, missed", got, s.Sets["f_outcome"])
	}
	// Exactly on the due instant is met.
	s = move(t, ty, "running", "stopped", again, "2026-11-24 10:30:00")
	if got := intOf(t, s.Sets["f_elapsed_s"]); got != 14400 || s.Sets["f_outcome"] != "met" {
		t.Errorf("stopped on the due instant: f_elapsed_s %d, f_outcome %v; want 14400, met", got, s.Sets["f_outcome"])
	}
	// Stopped while paused: what had elapsed decides.
	s = move(t, ty, "paused", "stopped", paused, "2026-12-01 09:00:00")
	if s.Sets["f_outcome"] != "met" {
		t.Errorf("stopped while paused: f_outcome %v, want met", s.Sets["f_outcome"])
	}
}

// The weekend and the day off do not count; a second is a second.
func TestSets_WorkingTime_SkipsWhatIsNotWorked(t *testing.T) {
	ty := gaugeTy(t)
	// Resumed Thursday 24 December 16:00 Paris with two hours left:
	// Friday is off, then the weekend: due Monday 28 at 10:00 Paris.
	paused := with(office(), map[string]any{"f_kind": "main", "f_target_s": float64(7200), "f_elapsed_s": float64(0)})
	r := move(t, ty, "paused", "running", paused, "2026-12-24 15:00:00")
	if r.Sets["f_due_at"] != stored(t, "2026-12-28 09:00:00") {
		t.Errorf("due = %v, want Monday 28 December 09:00Z", r.Sets["f_due_at"])
	}
	// Paused on that Monday at 09:00:05 Paris: one hour and five seconds.
	running := with(paused, map[string]any{"f_resumed_at": stored(t, "2026-12-24 15:00:00"), "f_due_at": r.Sets["f_due_at"]})
	p := move(t, ty, "running", "paused", running, "2026-12-28 08:00:05")
	if got := intOf(t, p.Sets["f_elapsed_s"]); got != 3605 {
		t.Errorf("f_elapsed_s = %d, want 3605", got)
	}
}

// The same pause, judged late by a worker that was stopped, computes the
// same numbers: the effect reads cause.at, and there is no other clock in
// it. (Input.Today, the service's clock, is not read.)
func TestSets_WorkingTime_DependsOnCauseAtOnly(t *testing.T) {
	ty := gaugeTy(t)
	fields := with(office(), map[string]any{
		"f_kind": "main", "f_target_s": float64(14400),
		"f_resumed_at": stored(t, "2026-11-23 09:00:00"), "f_due_at": stored(t, "2026-11-23 13:00:00"),
	})
	cause := instant(t, "2026-11-23 10:30:00")
	var first map[string]any
	for i, today := range []time.Time{{}, cause, cause.Add(10 * time.Second), cause.Add(40 * 24 * time.Hour)} {
		res, err := lifecycle.TransitionWith(ty, "running", lifecycle.Input{Fields: fields, CauseAt: cause, Today: today}, "paused")
		if err != nil {
			t.Fatalf("pause judged with today = %v: %v", today, err)
		}
		if i == 0 {
			first = res.Sets
			continue
		}
		if fmt.Sprint(res.Sets) != fmt.Sprint(first) {
			t.Errorf("judged with today = %v: %v, want %v", today, res.Sets, first)
		}
	}
}

// A late mark made by a timer that fired before the pause was heard of is
// taken back: the pause's cause is before the due instant. One made after
// it stays.
func TestSets_ALateMarkBeforeItsTimeIsTakenBack(t *testing.T) {
	ty := gaugeTy(t)
	fields := with(office(), map[string]any{
		"f_kind": "main", "f_target_s": float64(14400),
		"f_resumed_at": stored(t, "2026-11-23 09:00:00"), "f_due_at": stored(t, "2026-11-23 13:00:00"),
		"f_late_at": stored(t, "2026-11-23 13:00:00"),
	})
	for _, to := range []string{"paused", "stopped"} {
		early := move(t, ty, "running", to, fields, "2026-11-23 12:59:59")
		if v, named := early.Sets["f_late_at"]; !named || v != nil {
			t.Errorf("-> %s caused before the due instant: f_late_at = %v, named %v; want unset", to, v, named)
		}
		for _, at := range []string{"2026-11-23 13:00:00", "2026-11-23 15:00:00"} {
			late := move(t, ty, "running", to, fields, at)
			if late.Sets["f_late_at"] != stored(t, "2026-11-23 13:00:00") {
				t.Errorf("-> %s caused at %s: f_late_at = %v, want it kept", to, at, late.Sets["f_late_at"])
			}
		}
	}
}

// What cannot be computed refuses the move, naming the field: no time
// left to count, a calendar that is none, an end before its start.
func TestSets_WorkingTime_EffectFailed(t *testing.T) {
	ty := gaugeTy(t)
	base := with(office(), map[string]any{"f_kind": "main", "f_target_s": float64(3600), "f_elapsed_s": float64(0)})
	cases := []struct {
		name, from, to, field, at string
		fields                    map[string]any
	}{
		{"nothing left to count", "paused", "running", "f_due_at", "2026-11-23 09:00:00",
			with(base, map[string]any{"f_elapsed_s": float64(3600)})},
		{"more elapsed than the target", "paused", "running", "f_due_at", "2026-11-23 09:00:00",
			with(base, map[string]any{"f_elapsed_s": float64(4000)})},
		{"no zone", "paused", "running", "f_due_at", "2026-11-23 09:00:00", with(base, map[string]any{"f_zone": nil})},
		{"an unknown zone", "paused", "running", "f_due_at", "2026-11-23 09:00:00", with(base, map[string]any{"f_zone": "Mars/Olympus_Mons"})},
		{"no week", "paused", "running", "f_due_at", "2026-11-23 09:00:00", with(base, map[string]any{"f_week": nil})},
		{"a week that does not parse", "paused", "running", "f_due_at", "2026-11-23 09:00:00",
			with(base, map[string]any{"f_week": []any{"monday nine to five"}})},
		{"paused before it was resumed", "running", "paused", "f_elapsed_s", "2026-11-23 08:00:00",
			with(base, map[string]any{"f_resumed_at": stored(t, "2026-11-23 09:00:00")})},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := lifecycle.TransitionWith(ty, c.from, lifecycle.Input{Fields: c.fields, CauseAt: instant(t, c.at)}, c.to)
			var rej *lifecycle.Rejection
			if !errors.As(err, &rej) || rej.Code != lifecycle.EffectFailed || rej.Field != c.field {
				t.Fatalf("err = %v, want effect_failed on %s", err, c.field)
			}
			if containsAny(rej.Error(), "Mars", "monday nine") {
				t.Errorf("the reason holds a value: %q", rej.Error())
			}
		})
	}
	// No holidays at all is a calendar: an unset set is an empty list.
	if _, err := lifecycle.TransitionWith(ty, "paused", lifecycle.Input{
		Fields: with(base, map[string]any{"f_days_off": nil}), CauseAt: instant(t, "2026-11-23 09:00:00")}, "running"); err != nil {
		t.Errorf("a calendar without holidays: %v", err)
	}
}

// An invariant can hold a calendar to being one.
func TestInvariant_ACalendarMustBeOne(t *testing.T) {
	ty := parseYAML(t, `
id: opening
version: 1
fields:
  - {id: f_week,     type: string, multiple: true, max: 21}
  - {id: f_days_off, type: date,   multiple: true, max: 366}
  - {id: f_zone,     type: string}
invariants:
  - {id: opening_invalid, rule: 'calendar(f_week, f_days_off, f_zone) != null'}
`)
	if err := lifecycle.CheckInvariants(ty, office()); err != nil {
		t.Errorf("the office: %v", err)
	}
	if err := lifecycle.CheckInvariants(ty, map[string]any{"f_week": []any{"sun 00:00-24:00"}, "f_zone": "UTC"}); err != nil {
		t.Errorf("one day, no holidays: %v", err)
	}
	for name, fields := range map[string]map[string]any{
		"nothing at all":        {},
		"no working time":       {"f_zone": "UTC"},
		"no zone":               {"f_week": []any{"mon 09:00-17:00"}},
		"an unknown zone":       with(office(), map[string]any{"f_zone": "Europe/Atlantis"}),
		"a line that is no day": with(office(), map[string]any{"f_week": []any{"mon 09:00-17:00", "someday 09:00-17:00"}}),
		"overlapping intervals": with(office(), map[string]any{"f_week": []any{"mon 09:00-13:00", "mon 12:00-17:00"}}),
	} {
		var rej *lifecycle.Rejection
		if err := lifecycle.CheckInvariants(ty, fields); !errors.As(err, &rej) || rej.Code != lifecycle.InvariantFailed {
			t.Errorf("%s: %v, want invariant_failed", name, err)
		}
	}
}

// --- creation enters the initial state ---

const starterType = `
id: starter
version: 1
initial_state: open
fields:
  - {id: f_title, type: string}
states:
  - {id: open,    category: todo}
  - {id: working, category: active}
  - {id: idle,    category: waiting}
transitions:
  - {from: open,    to: working}
  - {from: working, to: open}
  - {from: working, to: idle}
reactions:
  - {id: begin,   kind: create, on: enter, state: open, type: gauge, version: 1}
  - {id: reopen,  kind: create, on: enter, state: open, from: [working], type: gauge, version: 1}
  - {id: tell,    kind: set,    on: enter, state: open, target: container, set: {f_label: 'f_title'}}
  - {id: pause,   kind: move,   on: enter, state: idle, children: {of: [gauge], when: 'child.f_kind == "main"'}, to: paused}
  - {id: answer,  kind: move,   on: created_inside, of: [remark], children: {of: [gauge]}, to: stopped}
`

func TestEnterReactions_CreationEntersTheInitialState(t *testing.T) {
	ty := parseYAML(t, starterType)
	for _, c := range []struct {
		from, to string
		want     []string
	}{
		{"", "open", []string{"begin", "tell"}},                  // created: those without `from`
		{"working", "open", []string{"begin", "reopen", "tell"}}, // entered again: all three
		{"working", "idle", []string{"pause"}},
		{"", "working", nil}, // a create enters the initial state only
	} {
		if got := ids(lifecycle.EnterReactions(ty, c.from, c.to)); !slices.Equal(got, c.want) {
			t.Errorf("EnterReactions(%q -> %q) = %v, want %v", c.from, c.to, got, c.want)
		}
	}
	if !lifecycle.StartsWorkflow(ty, lifecycle.Event{Op: "create", Entered: "open"}) {
		t.Error("creating a starter does not start a workflow; its reactions run then")
	}
	if !lifecycle.HasListeners(ty) {
		t.Error("HasListeners = false")
	}
	// A Type with nothing on its initial state starts none, as before.
	quiet := parseYAML(t, strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(starterType, "state: open, type", "state: working, type"), "state: open, target", "state: working, target"), "state: open, from: [working]", "state: idle, from: [working]"))
	if lifecycle.StartsWorkflow(quiet, lifecycle.Event{Op: "create", Entered: "open"}) {
		t.Error("creating an Item whose initial state has no behavior starts a workflow")
	}
	if got := lifecycle.EnterReactions(quiet, "", "open"); len(got) != 0 {
		t.Errorf("EnterReactions at creation = %v, want none", ids(got))
	}
}

// A set reaction is run like a move or a create: on entering its state,
// and such a state needs a workflow.
func TestSetReactions_AreEnterReactions(t *testing.T) {
	ty := gaugeTy(t)
	if got := ids(lifecycle.EnterReactions(ty, "paused", "running")); !slices.Equal(got, []string{"show"}) {
		t.Errorf("EnterReactions(paused -> running) = %v, want [show]", got)
	}
	if got := ids(lifecycle.EnterReactions(ty, "", "running")); !slices.Equal(got, []string{"show"}) {
		t.Errorf("EnterReactions(created) = %v, want [show]", got)
	}
	if got := ids(lifecycle.EnterReactions(ty, "running", "paused")); !slices.Equal(got, []string{"hide"}) {
		t.Errorf("EnterReactions(running -> paused) = %v, want [hide]", got)
	}
	for _, s := range []string{"running", "paused", "stopped"} {
		if !lifecycle.NeedsWorkflow(ty, s) {
			t.Errorf("NeedsWorkflow(%s) = false; a set reaction runs on entering it", s)
		}
	}
}

// --- an acting timer at an instant ---

func TestActingAtTimers(t *testing.T) {
	ty := gaugeTy(t)
	got := lifecycle.ActingAtTimers(ty, "running")
	if len(got) != 2 || got[0].ID != "late" || got[0].At != "f_due_at" || got[1].ID != "sum" || got[1].At != "f_resumed_at" {
		t.Errorf("ActingAtTimers(running) = %+v, want late then sum", got)
	}
	if got := lifecycle.ActingAtTimers(ty, "paused"); len(got) != 0 {
		t.Errorf("ActingAtTimers(paused) = %+v, want none", got)
	}
	// They are not duration timers: ActingTimers, which the workflow runs
	// on a schedule counted from the entry, does not list them.
	if got := lifecycle.ActingTimers(ty, "running"); len(got) != 0 {
		t.Errorf("ActingTimers(running) = %+v, want none: both are at an instant", got)
	}
	// Such a state needs a workflow even while the field is unset: the run
	// must be there to hear it being set.
	only := parseYAML(t, `
id: alarm
version: 1
initial_state: set
fields:
  - {id: f_at,   type: datetime}
  - {id: f_rang, type: datetime}
states:
  - id: set
    category: active
    acting:
      - {id: ring, at: f_at, sets: {f_rang: 'cause.at'}}
`)
	if !lifecycle.NeedsWorkflow(only, "set") || !lifecycle.HasListeners(only) ||
		!lifecycle.StartsWorkflow(only, lifecycle.Event{Op: "create", Entered: "set"}) {
		t.Error("a state whose only behavior is an acting timer at an instant needs a workflow, from creation")
	}
}

func TestActingEffect(t *testing.T) {
	ty := gaugeTy(t)
	late := lifecycle.ActingAtTimers(ty, "running")
	if len(late) != 2 {
		t.Fatalf("ActingAtTimers(running) = %+v, want two", late)
	}
	due := instant(t, "2026-11-23 13:00:00")
	fields := with(office(), map[string]any{"f_kind": "main", "f_target_s": float64(14400), "f_due_at": stored(t, "2026-11-23 13:00:00")})

	// cause.at is the instant itself, whatever the time it is applied at;
	// today is its UTC date.
	sets, applies, err := lifecycle.ActingEffect(ty, late[0], fields, due)
	if err != nil || !applies {
		t.Fatalf("ActingEffect = %v, %v, %v; want it to apply", sets, applies, err)
	}
	if len(sets) != 2 || sets["f_late_at"] != stored(t, "2026-11-23 13:00:00") || sets["f_note"] != "2026-11-23T00:00:00Z" {
		t.Errorf("sets = %v, want f_late_at = the instant and f_note = its date", sets)
	}
	// The condition: already marked late, it does nothing.
	sets, applies, err = lifecycle.ActingEffect(ty, late[0], with(fields, map[string]any{"f_late_at": stored(t, "2026-11-23 13:00:00")}), due)
	if err != nil || applies || len(sets) != 0 {
		t.Errorf("already late: %v, %v, %v; want it not to apply", sets, applies, err)
	}
	// No condition: it applies. An expression that cannot be computed is
	// an effect_failed naming the field.
	_, applies, err = lifecycle.ActingEffect(ty, late[1], with(fields, map[string]any{"f_elapsed_s": float64(60)}), due)
	if err != nil || !applies {
		t.Errorf("a timer without a condition: applies %v, %v", applies, err)
	}
	_, _, err = lifecycle.ActingEffect(ty, late[1], fields, due) // f_elapsed_s is unset
	var rej *lifecycle.Rejection
	if !errors.As(err, &rej) || rej.Code != lifecycle.EffectFailed || rej.Field != "f_elapsed_s" {
		t.Errorf("arithmetic on an unset field: %v, want effect_failed on f_elapsed_s", err)
	}
}

// --- children ---

func TestChildMatches(t *testing.T) {
	host := parseYAML(t, starterType)
	gauge := gaugeTy(t)
	remark := parseYAML(t, "id: remark\nversion: 1\nfields:\n  - {id: f_body, type: string}\n  - {id: f_kind, type: string}\n")
	old := parseYAML(t, "id: gauge\nversion: 7\nfields:\n  - {id: f_note, type: string}\n") // a version without f_kind

	pause, _ := host.Reaction("pause")
	answer, _ := host.Reaction("answer")
	for _, c := range []struct {
		name   string
		r      typedef.Reaction
		child  *typedef.Type
		fields map[string]any
		want   bool
	}{
		{"of the Type, and the condition holds", pause, gauge, map[string]any{"f_kind": "main"}, true},
		{"the condition does not hold", pause, gauge, map[string]any{"f_kind": "reply"}, false},
		{"the field is unset", pause, gauge, map[string]any{}, false},
		{"a version that does not declare the field: null", pause, old, map[string]any{"f_note": "x"}, false},
		{"another Type, though its field matches", pause, remark, map[string]any{"f_kind": "main"}, false},
		{"no condition: every child of the Type", answer, gauge, map[string]any{}, true},
		{"no condition: still only the Types named", answer, remark, map[string]any{}, false},
	} {
		if got := lifecycle.ChildMatches(c.r, c.child, c.fields); got != c.want {
			t.Errorf("%s: ChildMatches = %v, want %v", c.name, got, c.want)
		}
	}
}

// --- set ---

const hostType = `
id: host
version: 1
fields:
  - {id: f_due,     type: datetime, writable_by: kernel}
  - {id: f_label,   type: enum, options: [main, other], writable_by: kernel}
  - {id: f_seen_at, type: date, writable_by: kernel}
  - {id: f_spent_s, type: int, writable_by: kernel}
accepts_reactions:
  gauge:show: {sets: [f_due, f_label, f_seen_at]}
  gauge:hide: {sets: [f_due]}
  gauge:sum:  {sets: [f_spent_s]}
`

func TestSetValues(t *testing.T) {
	ty, host := gaugeTy(t), parseYAML(t, hostType)
	show, _ := ty.Reaction("show")
	hide, _ := ty.Reaction("hide")
	sum, _ := ty.Reaction("sum")
	midnight := instant(t, "2026-11-23 00:00:00")
	fields := map[string]any{"f_kind": "main", "f_due_at": stored(t, "2026-11-23 13:00:00")}

	// Values in the target's stored form: an instant, an option, a date.
	got, err := lifecycle.SetValues(ty, show, host, lifecycle.Input{Fields: fields, CauseAt: midnight})
	if err != nil {
		t.Fatalf("SetValues(show): %v", err)
	}
	if len(got) != 3 || got["f_due"] != stored(t, "2026-11-23 13:00:00") || got["f_label"] != "main" || got["f_seen_at"] != "2026-11-23" {
		t.Errorf("show = %v", got)
	}
	// null unsets: the field is named, with nil.
	got, err = lifecycle.SetValues(ty, hide, host, lifecycle.Input{Fields: fields, CauseAt: midnight})
	if v, named := got["f_due"]; err != nil || !named || v != nil || len(got) != 1 {
		t.Errorf("hide = %v, %v; want f_due named with nil", got, err)
	}
	// An unset source field is null, which unsets too.
	got, err = lifecycle.SetValues(ty, show, host, lifecycle.Input{Fields: map[string]any{"f_kind": "main"}, CauseAt: midnight})
	if v, named := got["f_due"]; err != nil || !named || v != nil {
		t.Errorf("show with f_due_at unset = %v, %v; want f_due named with nil", got, err)
	}

	var rej *lifecycle.Rejection
	for name, c := range map[string]struct {
		r      typedef.Reaction
		in     lifecycle.Input
		field  string
		secret string
	}{
		"a value that is no option of the target's enum": {show,
			lifecycle.Input{Fields: map[string]any{"f_kind": "reply"}, CauseAt: midnight}, "f_label", "reply"},
		"an instant that is not a date": {show,
			lifecycle.Input{Fields: fields, CauseAt: midnight.Add(time.Hour)}, "f_seen_at", "01:00"},
		"arithmetic on an unset field": {sum, lifecycle.Input{Fields: fields, CauseAt: midnight}, "f_spent_s", ""},
	} {
		_, err := lifecycle.SetValues(ty, c.r, host, c.in)
		if !errors.As(err, &rej) || rej.Code != lifecycle.EffectFailed || rej.Field != c.field {
			t.Errorf("%s: %v, want effect_failed on %s", name, err, c.field)
			continue
		}
		if c.secret != "" && containsAny(rej.Error(), c.secret) {
			t.Errorf("%s: the reason holds a value: %q", name, rej.Error())
		}
	}
}

// --- a create reaction reads linked Items ---

const workType = `
id: work
version: 1
initial_state: open
fields:
  - {id: f_title,    type: string}
  - {id: f_budget_s, type: int}
states:
  - {id: open, category: todo}
links:
  - {name: backup, inverse: backs, targets: [opening], max: one}
reactions:
  - id: start
    kind: create
    on: enter
    state: open
    type: gauge
    version: 1
    fields:
      f_kind: '"main"'
      f_target_s: 'f_budget_s'
      f_resumed_at: 'cause.at'
      f_week: 'container.linked.hours.f_week'
      f_days_off: 'container.linked.hours.f_days_off'
      f_zone: 'container.linked.hours.f_zone'
      f_due_at: 'working_after(cause.at, f_budget_s, calendar(container.linked.hours.f_week, container.linked.hours.f_days_off, container.linked.hours.f_zone))'
      f_note: 'linked.backup.f_zone'
`

func strs(t *testing.T, v any) []string {
	t.Helper()
	switch l := v.(type) {
	case []string:
		return l
	case []any:
		out := make([]string, 0, len(l))
		for _, e := range l {
			s, ok := e.(string)
			if !ok {
				t.Fatalf("%v holds a %T, want strings", v, e)
			}
			out = append(out, s)
		}
		return out
	}
	t.Fatalf("%v (%T) is not a list", v, v)
	return nil
}

func TestCreateFields_ReadLinkedItems(t *testing.T) {
	work, gauge := parseYAML(t, workType), gaugeTy(t)
	start, _ := work.Reaction("start")
	in := lifecycle.Input{
		Fields:  map[string]any{"f_title": "fix the gate", "f_budget_s": float64(7200)},
		CauseAt: instant(t, "2026-11-20 15:00:00"), // Friday 16:00 in Paris
		Linked: lifecycle.LinkedFields{
			Container: map[string]map[string]any{"hours": office()},
			Own:       map[string]map[string]any{"backup": {"f_zone": "Asia/Tokyo"}},
		},
	}
	got, err := lifecycle.CreateFields(work, start, gauge, in)
	if err != nil {
		t.Fatalf("CreateFields: %v", err)
	}
	// The calendar is copied whole, in the stored form of a set (sorted),
	// and the due instant is computed on it: Friday 16:00 + 2h is Monday
	// 10:00 in Paris.
	if !slices.Equal(strs(t, got["f_week"]), strs(t, office()["f_week"])) ||
		!slices.Equal(strs(t, got["f_days_off"]), []string{"2026-12-25"}) || got["f_zone"] != "Europe/Paris" {
		t.Errorf("the calendar copied = %v, %v, %v", got["f_week"], got["f_days_off"], got["f_zone"])
	}
	if got["f_due_at"] != stored(t, "2026-11-23 09:00:00") || got["f_resumed_at"] != stored(t, "2026-11-20 15:00:00") {
		t.Errorf("f_due_at %v, f_resumed_at %v; want Monday 09:00Z and the cause's time", got["f_due_at"], got["f_resumed_at"])
	}
	if got["f_kind"] != "main" || intOf(t, got["f_target_s"]) != 7200 || got["f_note"] != "Asia/Tokyo" {
		t.Errorf("fields = %v", got)
	}

	var rej *lifecycle.Rejection
	// A linked field that is unset reads as null: the copy is left unset,
	// and what needs it cannot be computed.
	noZone := in
	noZone.Linked.Container = map[string]map[string]any{"hours": with(office(), map[string]any{"f_zone": nil})}
	if _, err := lifecycle.CreateFields(work, start, gauge, noZone); !errors.As(err, &rej) || rej.Code != lifecycle.EffectFailed || rej.Field != "f_due_at" {
		t.Errorf("a calendar without a zone: %v, want effect_failed on f_due_at", err)
	}
	// Nothing linked at all: the item service refuses before it gets
	// here; the pure function still fails rather than invent a value.
	none := in
	none.Linked = lifecycle.LinkedFields{}
	if _, err := lifecycle.CreateFields(work, start, gauge, none); !errors.As(err, &rej) || rej.Code != lifecycle.EffectFailed {
		t.Errorf("nothing linked: %v, want effect_failed", err)
	}
}
