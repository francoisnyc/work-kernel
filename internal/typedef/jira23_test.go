// Tests for Jira part 2, step 3a: what a Type may declare (docs/design.md,
// "Decisions for Jira part 2"; the Phase A answers of step 3).
//
//   - children: {of, when} on a move reaction: it moves Items contained in
//     the Item, on entering a state or when an Item is created inside.
//   - by: not_creator on created_inside.
//   - kind: set, target: container: entering a state sets fields on the
//     Item's container, which consents with {sets: [fields]}.
//   - A consent key *:<id> stands for a reaction with that ID of any Type,
//     but only for a reaction that reaches the Item through containment.
//   - A create reaction's fields may read linked.<link>.<field> and
//     container.linked.<link>.<field>; the Type read from lists the fields
//     in readable_by_reactions.
//   - An enter reaction without `from` also runs when the Item is created.
//   - An acting timer at an instant: {id, at: <datetime field>, when, sets,
//     hook}.
//   - CEL: calendar(week, holidays, zone), working_after(t, seconds, cal),
//     working_between(t1, t2, cal).
//   - ParseDir reads one level of subfolders.
//
// The Types here are test Types: a job contains meters, which measure it on
// the hours of the yard the job is in. Nothing is named after a goal.
package typedef_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// hours: a week, days off and a zone, readable by reactions.
const hoursSrc = `
id: hours
version: 1
fields:
  - {id: f_name,     type: string}
  - {id: f_week,     type: string, multiple: true, max: 21}
  - {id: f_days_off, type: date,   multiple: true, max: 366}
  - {id: f_zone,     type: string}
  - {id: f_owner,    type: principal}
readable_by_reactions: [f_week, f_days_off, f_zone]
invariants:
  - {id: hours_invalid, rule: 'calendar(f_week, f_days_off, f_zone) != null'}
`

// yard: a container whose admin links it to its hours.
const yardSrc = `
id: yard
version: 1
fields:
  - {id: f_name, type: string}
links:
  - {name: hours, inverse: yards, targets: [hours], max: one}
`

// job: the host. It starts a meter when it is created, pauses it while it
// waits, and stops it when it is done; a public remark by someone else
// than its requester stops its reply meter.
const jobSrc = `
id: job
version: 1
initial_state: open
fields:
  - {id: f_title,    type: string}
  - {id: f_budget_s, type: int}
  - {id: f_due,      type: datetime, writable_by: kernel}
states:
  - {id: open,    category: todo}
  - {id: working, category: active}
  - {id: waiting, category: waiting}
  - {id: done,    category: done}
transitions:
  - {from: open,    to: working}
  - {from: working, to: waiting}
  - {from: waiting, to: working}
  - {from: working, to: done}
links:
  - {name: backup, inverse: backs, targets: [hours], max: one}
reactions:
  - START
  - PAUSE
  - {id: resume, kind: move, on: enter, state: working, from: [waiting], children: {of: [meter]}, to: running, target_from: [paused]}
  - {id: stop, kind: move, on: enter, state: done, children: {of: [meter]}, to: stopped}
  - ANSWERED
accepts_reactions:
  ACCEPTS
`

const (
	startR = `{id: start, kind: create, on: enter, state: open, type: meter, version: 1, fields: {
      f_kind: '"main"', f_target_s: 'f_budget_s', f_started_at: 'cause.at',
      f_week: 'container.linked.hours.f_week', f_days_off: 'container.linked.hours.f_days_off', f_zone: 'container.linked.hours.f_zone',
      f_due_at: 'working_after(cause.at, f_budget_s, calendar(container.linked.hours.f_week, container.linked.hours.f_days_off, container.linked.hours.f_zone))'}}`
	pauseR    = `{id: pause, kind: move, on: enter, state: waiting, children: {of: [meter], when: 'child.f_kind == "main"'}, to: paused, target_from: [running]}`
	answeredR = `{id: answered, kind: move, on: created_inside, of: [remark], when: 'child.f_public == true', by: not_creator,
     children: {of: [meter], when: 'child.f_kind == "reply"'}, to: stopped, target_from: [running]}`
	jobAccepts = "meter:show: {sets: [f_due]}\n  meter:hide: {sets: [f_due]}"
)

func job(start, pause, answered, accepts string) string {
	return strings.NewReplacer("START", start, "PAUSE", pause, "ANSWERED", answered, "ACCEPTS", accepts).Replace(jobSrc)
}

func goodJob() string { return job(startR, pauseR, answeredR, jobAccepts) }

// meter: what a job starts. It knows no job: its consent is by reaction ID
// (*:start), and it shows its due instant on whatever contains it.
const meterSrc = `
id: meter
version: 1
initial_state: running
fields:
  - {id: f_kind,       type: enum, options: [main, reply]}
  - {id: f_target_s,   type: int,      writable_by: kernel}
  - {id: f_elapsed_s,  type: int,      writable_by: kernel}
  - {id: f_started_at, type: datetime, writable_by: kernel}
  - {id: f_due_at,     type: datetime, writable_by: kernel}
  - {id: f_late_at,    type: datetime, writable_by: kernel}
  - {id: f_week,       type: string, multiple: true, max: 21, writable_by: kernel}
  - {id: f_days_off,   type: date,   multiple: true, max: 366, writable_by: kernel}
  - {id: f_zone,       type: string, writable_by: kernel}
  - {id: f_note,       type: string}
  - {id: f_owner,      type: principal}
states:
  - id: running
    category: active
    acting:
      - ACTING
  - {id: paused,  category: waiting}
  - {id: stopped, category: done}
transitions:
  - from: running
    to: paused
    sets:
      f_elapsed_s: 'working_between(f_started_at, cause.at, calendar(f_week, f_days_off, f_zone))'
      f_due_at: 'null'
  - from: paused
    to: running
    sets:
      f_started_at: 'cause.at'
      f_due_at: 'working_after(cause.at, f_target_s - f_elapsed_s, calendar(f_week, f_days_off, f_zone))'
  - {from: running, to: stopped}
  - {from: paused,  to: stopped}
reactions:
  - SHOW
  - {id: hide, kind: set, on: enter, state: paused, target: container, set: {f_due: 'null'}}
accepts_reactions:
  ACCEPTS
hooks:
  - {id: tell_late, on: timer, kind: action, action: email.notify, reads: [f_owner]}
`

const (
	lateT        = "{id: late, at: f_due_at, when: 'f_late_at == null', sets: {f_late_at: 'cause.at'}, hook: tell_late}"
	showR        = "{id: show, kind: set, on: enter, state: running, target: container, set: {f_due: 'f_due_at'}}"
	meterAccepts = `'*:start': {creates: [f_kind, f_target_s, f_started_at, f_due_at, f_week, f_days_off, f_zone]}
  '*:pause': {moves: [paused]}
  '*:resume': {moves: [running]}
  '*:stop': {moves: [stopped]}
  '*:answered': {moves: [stopped]}`
)

func meter(acting, show, accepts string) string {
	return strings.NewReplacer("ACTING", acting, "SHOW", show, "ACCEPTS", accepts).Replace(meterSrc)
}

func goodMeter() string { return meter(lateT, showR, meterAccepts) }

func step3Set() map[string]string {
	return map[string]string{
		"hours.yaml": hoursSrc, "yard.yaml": yardSrc, "job.yaml": goodJob(), "meter.yaml": goodMeter(), "remark.yaml": remarkSrc,
	}
}

// replaced returns the set with one file replaced, or removed ("").
func replaced(name, src string) map[string]string {
	out := step3Set()
	if src == "" {
		delete(out, name)
	} else {
		out[name] = src
	}
	return out
}

func mustRefuseSet(t *testing.T, name string, files map[string]string, mention string) {
	t.Helper()
	err := parseSet(t, files)
	if err == nil {
		t.Errorf("%s: loaded, want refused", name)
		return
	}
	if mention != "" && !strings.Contains(err.Error(), mention) {
		t.Errorf("%s: error %q does not mention %q", name, err, mention)
	}
}

// --- one Type: what is kept ---

func TestStep3_Parse(t *testing.T) {
	for _, j := range []*typedef.Type{mustParse(t, goodJob()), roundTrip(t, mustParse(t, goodJob()))} {
		p, ok := j.Reaction("pause")
		if !ok || p.Kind != typedef.ReactionMove || p.On != typedef.OnEnter || p.State != "waiting" || p.Link != "" ||
			p.Children == nil || !slices.Equal(p.Children.Of, []string{"meter"}) || p.Children.When != `child.f_kind == "main"` ||
			p.To != "paused" || !slices.Equal(p.TargetFrom, []string{"running"}) {
			t.Errorf("pause = %+v (children %+v)", p, p.Children)
		}
		if p.Children != nil && (p.Children.WhenProgram == nil || !slices.Equal(p.Children.Reads, []string{"f_kind"})) {
			t.Errorf("pause.children: compiled %v, reads %v; want compiled, reading f_kind", p.Children.WhenProgram != nil, p.Children.Reads)
		}
		// Without a condition: every contained Item of the Types.
		if r, _ := j.Reaction("resume"); r.Children == nil || r.Children.When != "" || r.Children.WhenProgram != nil {
			t.Errorf("resume.children = %+v, want of: [meter] and no condition", r.Children)
		}
		a, ok := j.Reaction("answered")
		if !ok || a.On != typedef.OnCreatedInside || a.By != typedef.ByNotCreator || !slices.Equal(a.Of, []string{"remark"}) ||
			a.When != "child.f_public == true" || a.Children == nil || a.Children.When != `child.f_kind == "reply"` ||
			!slices.Equal(a.ChildReads, []string{"f_public"}) || !slices.Equal(a.Children.Reads, []string{"f_kind"}) {
			t.Errorf("answered = %+v (children %+v)", a, a.Children)
		}
		s, ok := j.Reaction("start")
		want := []typedef.LinkedRead{
			{Container: true, Link: "hours", Field: "f_days_off"},
			{Container: true, Link: "hours", Field: "f_week"},
			{Container: true, Link: "hours", Field: "f_zone"},
		}
		if !ok || s.Kind != typedef.ReactionCreate || len(s.From) != 0 || !slices.Equal(s.LinkedReads, want) {
			t.Errorf("start: from %v, linked reads %+v; want no from and %+v", s.From, s.LinkedReads, want)
		}
		if got := j.AcceptsReactions["meter:show"].Sets; !slices.Equal(got, []string{"f_due"}) {
			t.Errorf("job accepts meter:show sets %v, want [f_due]", got)
		}
	}

	for _, m := range []*typedef.Type{mustParse(t, goodMeter()), roundTrip(t, mustParse(t, goodMeter()))} {
		show, ok := m.Reaction("show")
		if !ok || show.Kind != typedef.ReactionSet || show.On != typedef.OnEnter || show.State != "running" ||
			show.Target != typedef.TargetContainer || show.Set["f_due"] != "f_due_at" || show.SetPrograms["f_due"] == nil {
			t.Errorf("show = %+v", show)
		}
		st, _ := m.State("running")
		if len(st.Acting) != 1 {
			t.Fatalf("running has %d acting timers, want 1", len(st.Acting))
		}
		a := st.Acting[0]
		if a.ID != "late" || a.At != "f_due_at" || a.When != "f_late_at == null" || a.Sets["f_late_at"] != "cause.at" ||
			a.Hook != "tell_late" || a.After != "" || a.Every != "" || a.Max != 0 || a.WhenProgram == nil || a.SetPrograms["f_late_at"] == nil {
			t.Errorf("late = %+v", a)
		}
	}

	h := roundTrip(t, mustParse(t, hoursSrc))
	if !slices.Equal(h.ReadableByReactions, []string{"f_week", "f_days_off", "f_zone"}) ||
		!h.ReadableByReaction("f_zone") || h.ReadableByReaction("f_name") || h.ReadableByReaction("f_nope") {
		t.Errorf("hours: readable_by_reactions = %v", h.ReadableByReactions)
	}
}

// --- children ---

func TestChildrenReactions_RefusedAtLoad(t *testing.T) {
	enter := func(rest string) string {
		return job(startR, "{id: pause, kind: move, on: enter, state: waiting, "+rest+"}", answeredR, jobAccepts)
	}
	inside := func(rest string) string {
		return job(startR, pauseR, "{id: answered, kind: move, on: created_inside, of: [remark], "+rest+"}", jobAccepts)
	}
	cases := []struct{ name, src, mention string }{
		{"children and a link: which is the target?", enter("children: {of: [meter]}, link: backup, to: paused"), "children"},
		{"children without `of`", enter("children: {when: 'child.f_kind == \"main\"'}, to: paused"), "of"},
		{"children with an empty `of`", enter("children: {of: []}, to: paused"), "of"},
		{"no `to`", enter("children: {of: [meter]}"), "to"},
		{"a condition that does not compile", enter("children: {of: [meter], when: 'child.f_kind =='}, to: paused"), "when"},
		{"a condition reading the Item's own field", enter("children: {of: [meter], when: 'f_title == \"x\"'}, to: paused"), "f_title"},
		{"a condition that is not a bool", enter("children: {of: [meter], when: 'child.f_kind'}, to: paused"), "when"},
		{"an unknown key under children", enter("children: {of: [meter], max: 3}, to: paused"), "max"},
		{"neither a link nor children", enter("to: paused"), "link"},
		// created_inside: `from` stays the container's own states, and
		// target_from is now the children's.
		{"created_inside with children and a state", inside("children: {of: [meter]}, to: stopped, state: working"), "state"},
		{"created_inside with children and a link", inside("children: {of: [meter]}, to: stopped, link: backup"), "link"},
		{"an unknown `from` state of the container", inside("children: {of: [meter]}, to: stopped, from: [s_nope]"), "s_nope"},
		// by takes creator or not_creator.
		{"an unknown `by`", inside("children: {of: [meter]}, to: stopped, by: not_me"), "not_me"},
		// Children are moved, never created or written through `children`.
		{"children on a create", job("{id: start, kind: create, on: enter, state: open, type: meter, version: 1, children: {of: [meter]}}",
			pauseR, answeredR, jobAccepts), "children"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { mustRefuse(t, c.name, c.src, c.mention) })
	}
	// On created_inside with children, target_from is allowed (the
	// children's states), and `from` filters on the container's state.
	mustParse(t, inside("children: {of: [meter]}, to: stopped, from: [working, waiting], target_from: [running, paused]"))
	// by: not_creator also stands on a reaction that moves the container
	// itself, as by: creator does.
	mustParse(t, inside("by: not_creator, to: working, from: [waiting]"))
}

func TestParseDir_ChildrenReactions(t *testing.T) {
	if err := parseSet(t, step3Set()); err != nil {
		t.Fatalf("the five Types together: %v", err)
	}
	cases := []struct {
		name    string
		files   map[string]string
		mention string
	}{
		{"a children Type is not loaded", replaced("job.yaml", job(startR,
			strings.Replace(pauseR, "of: [meter]", "of: [meter, gauge]", 1), answeredR, jobAccepts)), "gauge"},
		{"the children's Type does not accept the move", replaced("meter.yaml",
			meter(lateT, showR, strings.Replace(meterAccepts, "'*:pause': {moves: [paused]}", "'*:pause': {moves: [stopped]}", 1))), "pause"},
		{"no consent at all for it", replaced("meter.yaml",
			meter(lateT, showR, strings.Replace(meterAccepts, "'*:pause': {moves: [paused]}\n  ", "", 1))), "pause"},
		{"`to` is no state of the children's Type", replaced("job.yaml", job(startR,
			strings.Replace(pauseR, "to: paused", "to: frozen", 1), answeredR, jobAccepts)), "frozen"},
		{"the children's Type has no such transition", replaced("job.yaml", job(startR,
			strings.Replace(pauseR, "target_from: [running]", "target_from: [stopped]", 1), answeredR, jobAccepts)), "stopped"},
		{"the condition reads a field no version of the children's Type declares", replaced("job.yaml", job(startR,
			strings.Replace(pauseR, "child.f_kind", "child.f_sort", 1), answeredR, jobAccepts)), "f_sort"},
		{"created_inside: the children's condition too", replaced("job.yaml", job(startR, pauseR,
			strings.Replace(answeredR, `child.f_kind == "reply"`, `child.f_sort == "reply"`, 1), jobAccepts)), "f_sort"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { mustRefuseSet(t, c.name, c.files, c.mention) })
	}
}

// --- consent by reaction ID ---

// *:<id> is honoured for a reaction that reaches the Item through
// containment, and only then. An exact key wins over it.
func TestConsent_WildcardOnlyThroughContainment(t *testing.T) {
	m := roundTrip(t, mustParse(t, meter(lateT, showR, meterAccepts+"\n  job:stop: {moves: [paused]}\n  rig:bump: [f_elapsed_s]")))
	// Any Type's `pause`, when it comes from the container.
	for _, typ := range []string{"job", "rig", "anything"} {
		a, ok := m.Consent(typ, "pause", true)
		if !ok || !slices.Equal(a.Moves, []string{"paused"}) {
			t.Errorf("Consent(%s, pause, contained) = %+v, %v; want moves [paused]", typ, a, ok)
		}
		if _, ok := m.Consent(typ, "pause", false); ok {
			t.Errorf("Consent(%s, pause, over a link) is given by the wildcard; over a link consent stays exact", typ)
		}
	}
	// The exact key wins, whole: job's stop may move into paused only,
	// though *:stop says stopped.
	if a, ok := m.Consent("job", "stop", true); !ok || !slices.Equal(a.Moves, []string{"paused"}) {
		t.Errorf("Consent(job, stop, contained) = %+v, %v; want the exact key's moves [paused]", a, ok)
	}
	if a, ok := m.Consent("rig", "stop", true); !ok || !slices.Equal(a.Moves, []string{"stopped"}) {
		t.Errorf("Consent(rig, stop, contained) = %+v, %v; want the wildcard's moves [stopped]", a, ok)
	}
	// An exact key serves both ways, as it always did.
	if a, ok := m.Consent("rig", "bump", false); !ok || !slices.Equal(a.Fields, []string{"f_elapsed_s"}) {
		t.Errorf("Consent(rig, bump, over a link) = %+v, %v; want fields [f_elapsed_s]", a, ok)
	}
	if _, ok := m.Consent("job", "ghost", true); ok {
		t.Error("Consent for a reaction nobody named is given")
	}
	c, ok := m.Consent("job", "start", true)
	if !ok || !c.MayCreate || !slices.Contains(c.Creates, "f_due_at") {
		t.Errorf("Consent(job, start, contained) = %+v, %v; want creates", c, ok)
	}
}

func TestConsent_WildcardKeys_RefusedAtLoad(t *testing.T) {
	for name, accepts := range map[string]string{
		"any reaction of any Type": "'*:*': {moves: [paused]}",
		"any reaction of one Type": "'job:*': {moves: [paused]}",
		"a star alone":             "'*': {moves: [paused]}",
		"a star in the ID":         "'*:pa*': {moves: [paused]}",
		// A reaction adds to a field over a link only: there consent
		// stays exact.
		"a wildcard for added fields (list)": "'*:bump': [f_elapsed_s]",
		"a wildcard for added fields (map)":  "'*:bump': {fields: [f_elapsed_s]}",
		"an unknown state":                   "'*:pause': {moves: [s_nope]}",
		"an unknown field to set":            "'*:show': {sets: [f_nope]}",
	} {
		mustRefuse(t, name, meter(lateT, showR, accepts), "")
	}
}

// A package of Types that consents by reaction ID loads without any Type
// that uses it: nothing has to name the `pause` it accepts. (An exact key
// must still name a reaction that exists, as before.)
func TestParseDir_WildcardConsentNeedsNoHost(t *testing.T) {
	if err := parseSet(t, map[string]string{"meter.yaml": goodMeter(), "hours.yaml": hoursSrc}); err != nil {
		t.Errorf("meter and hours alone: %v", err)
	}
	mustRefuseSet(t, "an exact key naming no reaction",
		replaced("meter.yaml", meter(lateT, showR, meterAccepts+"\n  job:ghost: {moves: [paused]}")), "ghost")
}

// A stranger: a Type whose reaction has the accepted ID but reaches meters
// over a link it declares, not from their container.
const rigSrc = `
id: rig
version: 1
initial_state: up
states:
  - {id: up,   category: active}
  - {id: down, category: done}
transitions:
  - {from: up, to: down}
links:
  - {name: gauge, inverse: rigs, targets: [meter], max: one}
reactions:
  - {id: pause, kind: move, on: enter, state: down, link: gauge, to: paused}
`

func TestParseDir_AStrangerOverALinkIsNotCoveredByTheWildcard(t *testing.T) {
	files := step3Set()
	files["rig.yaml"] = rigSrc
	mustRefuseSet(t, "rig:pause over a link, with only *:pause accepted", files, "rig")

	// With its own exact key it is accepted, as any link reaction is.
	files["meter.yaml"] = meter(lateT, showR, meterAccepts+"\n  rig:pause: {moves: [paused]}")
	if err := parseSet(t, files); err != nil {
		t.Errorf("rig:pause accepted by name: %v", err)
	}
	// And a second host, moving its own children, needs nothing more.
	files = step3Set()
	files["crew.yaml"] = strings.Replace(goodJob(), "id: job", "id: crew", 1)
	if err := parseSet(t, files); err != nil {
		t.Errorf("a second host with the same reaction IDs: %v", err)
	}
}

// --- set ---

func TestSetReactions_RefusedAtLoad(t *testing.T) {
	set := func(rest string) string {
		return meter(lateT, "{id: show, kind: set, on: enter, "+rest+"}", meterAccepts)
	}
	cases := []struct{ name, src, mention string }{
		{"no target", set("state: running, set: {f_due: 'f_due_at'}"), "target"},
		{"a target that is not the container", set("state: running, target: creator, set: {f_due: 'f_due_at'}"), "creator"},
		{"nothing to set", set("state: running, target: container"), "set"},
		{"no state", set("target: container, set: {f_due: 'f_due_at'}"), "state"},
		{"an unknown state", set("state: s_nope, target: container, set: {f_due: 'f_due_at'}"), "s_nope"},
		{"an expression that does not compile", set("state: running, target: container, set: {f_due: 'f_due_at +'}"), "f_due"},
		{"an expression reading an unknown field", set("state: running, target: container, set: {f_due: 'f_nope'}"), "f_nope"},
		{"an expression reading the caller", set("state: running, target: container, set: {f_due: 'caller'}"), "caller"},
		{"an expression reading a linked Item", set("state: running, target: container, set: {f_due: 'linked.x.f_due'}"), "linked"},
		{"a link: the target is the container", set("state: running, target: container, link: gauge, set: {f_due: 'f_due_at'}"), "link"},
		{"`to`: a set moves nothing", set("state: running, target: container, to: paused, set: {f_due: 'f_due_at'}"), "to"},
		{"`add`", set("state: running, target: container, add: {f_due: 'f_target_s'}, set: {f_due: 'f_due_at'}"), "add"},
		{"children", set("state: running, target: container, children: {of: [meter]}, set: {f_due: 'f_due_at'}"), "children"},
		{"create's keys", set("state: running, target: container, type: job, version: 1, set: {f_due: 'f_due_at'}"), "type"},
		{"on: created_inside", meter(lateT, "{id: show, kind: set, on: created_inside, of: [remark], target: container, set: {f_due: 'f_due_at'}}", meterAccepts), "created_inside"},
		{"`set` on a move", meter(lateT, "{id: show, kind: move, on: enter, state: running, children: {of: [meter]}, to: paused, set: {f_due: 'f_due_at'}}", meterAccepts), "set"},
		{"`target` on a create", meter(lateT, "{id: show, kind: create, on: enter, state: running, type: job, version: 1, target: container}", meterAccepts), "target"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { mustRefuse(t, c.name, c.src, c.mention) })
	}
	// cause.at and today are in scope, and `from` and `when` work as for
	// every enter reaction.
	mustParse(t, set("state: running, from: [paused], when: 'f_kind == \"main\"', target: container, set: {f_due: 'cause.at', f_title: 'string(today)'}"))
}

func TestParseDir_SetReactions(t *testing.T) {
	cases := []struct {
		name    string
		files   map[string]string
		mention string
	}{
		{"the reaction sets a field the consent does not list",
			replaced("job.yaml", job(startR, pauseR, answeredR, "meter:show: {sets: [f_title]}\n  meter:hide: {sets: [f_due]}")), "f_due"},
		{"the consent lists a field the Type does not declare",
			replaced("job.yaml", job(startR, pauseR, answeredR, "meter:show: {sets: [f_due, f_nope]}\n  meter:hide: {sets: [f_due]}")), "f_nope"},
		{"the value is not one of the field's type",
			replaced("meter.yaml", meter(lateT, strings.Replace(showR, "f_due: 'f_due_at'", "f_due: 'f_target_s'", 1), meterAccepts)), "f_due"},
		{"a consent to `sets` names no reaction",
			replaced("job.yaml", job(startR, pauseR, answeredR, jobAccepts+"\n  meter:ghost: {sets: [f_due]}")), "ghost"},
		{"a consent to `sets` names a reaction that is no set",
			replaced("job.yaml", job(startR, pauseR, answeredR, jobAccepts+"\n  meter:late: {sets: [f_due]}")), "late"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { mustRefuseSet(t, c.name, c.files, c.mention) })
	}
	// A set reaction nobody accepts loads: what contains the Item is not
	// known until it is created, and then the write is refused
	// (reaction_not_accepted). A package loads without a host.
	if err := parseSet(t, map[string]string{"meter.yaml": goodMeter(), "hours.yaml": hoursSrc}); err != nil {
		t.Errorf("meter without anything accepting its set: %v", err)
	}
	// job's f_due is kernel-written: the consent is its writer. Without
	// it nothing writes the field, and the Type is refused as before.
	mustRefuseSet(t, "a kernel-written field nothing writes",
		replaced("job.yaml", job(startR, pauseR, answeredR, "meter:other: [f_budget_s]")), "f_due")
	// job moves meters (children), a meter sets a field on its job: that
	// is no cycle, since a set enters no state.
	if err := parseSet(t, step3Set()); err != nil {
		t.Errorf("job and meter reacting to each other: %v", err)
	}
}

// --- a create reaction reads linked Items ---

func TestLinkedReads_RefusedAtLoad(t *testing.T) {
	start := func(fields string) string {
		return job("{id: start, kind: create, on: enter, state: open, type: meter, version: 1, fields: {"+fields+"}}", pauseR, answeredR, jobAccepts)
	}
	cases := []struct{ name, src, mention string }{
		{"the Item's own link is not declared", start("f_zone: 'linked.l_nope.f_zone'"), "l_nope"},
		{"a link alone, without a field", start("f_zone: 'linked.backup'"), "backup"},
		{"links, the guards' list", start("f_zone: 'links.backup[0].state'"), "links"},
		{"the container's own field", start("f_zone: 'container.f_name'"), "container"},
		{"two links away", start("f_zone: 'linked.backup.linked.other.f_zone'"), "linked"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { mustRefuse(t, c.name, c.src, c.mention) })
	}
	// A link that may have many targets has no one Item to read.
	t.Run("a link with many targets", func(t *testing.T) {
		src := strings.Replace(start("f_zone: 'linked.backup.f_zone'"), "targets: [hours], max: one", "targets: [hours]", 1)
		mustRefuse(t, "linked through a link without max: one", src, "backup")
	})
	// Only a create reaction's fields read linked Items: a `when`, a
	// transition's sets and a guard still read the Item alone.
	t.Run("in a when", func(t *testing.T) {
		mustRefuse(t, "linked in when", job(strings.Replace(startR, "state: open,", "state: open, when: 'linked.backup.f_zone != null',", 1),
			pauseR, answeredR, jobAccepts), "linked")
	})
	t.Run("in a transition's sets", func(t *testing.T) {
		src := strings.Replace(goodJob(), "{from: open,    to: working}", "{from: open, to: working, sets: {f_title: 'linked.backup.f_zone'}}", 1)
		mustRefuse(t, "linked in sets", src, "linked")
	})
	t.Run("in a guard", func(t *testing.T) {
		src := strings.Replace(goodJob(), "{from: open,    to: working}", "{from: open, to: working, guard: 'container.linked.hours.f_zone != null'}", 1)
		mustRefuse(t, "linked in a guard", src, "container")
	})

	// The Item's own link works like the container's.
	own := mustParse(t, start("f_zone: 'linked.backup.f_zone', f_week: 'container.linked.hours.f_week'"))
	r, _ := own.Reaction("start")
	want := []typedef.LinkedRead{{Link: "backup", Field: "f_zone"}, {Container: true, Link: "hours", Field: "f_week"}}
	if !slices.Equal(r.LinkedReads, want) {
		t.Errorf("linked reads = %+v, want %+v", r.LinkedReads, want)
	}
}

func TestReadableByReactions_RefusedAtLoad(t *testing.T) {
	for name, list := range map[string]string{
		"an unknown field": "[f_week, f_nope]",
		"a field twice":    "[f_week, f_week]",
		// Who someone is does not travel through a reaction's read.
		"a principal field": "[f_week, f_owner]",
	} {
		mustRefuse(t, name, strings.Replace(hoursSrc, "[f_week, f_days_off, f_zone]", list, 1), "")
	}
}

func TestParseDir_LinkedReads(t *testing.T) {
	startWith := func(fields string) string {
		return job("{id: start, kind: create, on: enter, state: open, type: meter, version: 1, fields: {"+fields+"}}", pauseR, answeredR, jobAccepts)
	}
	cases := []struct {
		name    string
		files   map[string]string
		mention string
	}{
		{"the linked Type does not let reactions read the field",
			replaced("hours.yaml", strings.Replace(hoursSrc, "[f_week, f_days_off, f_zone]", "[f_week, f_days_off]", 1)), "f_zone"},
		{"the linked Type lets reactions read nothing",
			replaced("hours.yaml", strings.Replace(hoursSrc, "readable_by_reactions: [f_week, f_days_off, f_zone]\n", "", 1)), "hours"},
		{"the linked Type does not declare the field",
			replaced("job.yaml", startWith("f_zone: 'container.linked.hours.f_tz'")), "f_tz"},
		{"no loaded Type could be the container: none declares the link",
			replaced("job.yaml", startWith("f_zone: 'container.linked.opening.f_zone'")), "opening"},
		{"no loaded Type could be the container: yard is gone", replaced("yard.yaml", ""), "hours"},
		{"the Item's own link: its target does not let the field be read",
			replaced("job.yaml", startWith("f_zone: 'linked.backup.f_name'")), "f_name"},
		{"the value read is not one of the created field's type",
			replaced("job.yaml", startWith("f_target_s: 'container.linked.hours.f_zone'")), "f_target_s"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { mustRefuseSet(t, c.name, c.files, c.mention) })
	}
	// A create reaction may now set a multi-value field: a set copied
	// whole from a linked Item (f_week, f_days_off), or one of the Item's
	// own. Principal fields stay out.
	if err := parseSet(t, step3Set()); err != nil {
		t.Errorf("the set, whose start copies two multi-value fields: %v", err)
	}
	mustRefuseSet(t, "a create reaction setting a principal field", map[string]string{
		"hours.yaml": hoursSrc, "yard.yaml": yardSrc, "remark.yaml": remarkSrc,
		"job.yaml":   startWith("f_owner: '\"user:ana\"'"),
		"meter.yaml": meter(lateT, showR, strings.Replace(meterAccepts, "creates: [f_kind,", "creates: [f_owner, f_kind,", 1)),
	}, "f_owner")
}

// --- creation enters the initial state ---

// An enter reaction without `from`, on the initial state, runs when the
// Item is created. So "A creates a B inside it when created" and "B
// creates an A inside it when created" is a cycle, refused at load.
func TestParseDir_ACycleThroughCreation(t *testing.T) {
	box := func(id, makes string) string {
		return "id: " + id + "\nversion: 1\ninitial_state: open\nfields:\n  - {id: f_name, type: string}\n" +
			"states:\n  - {id: open, category: todo}\n" +
			"reactions:\n  - {id: fill, kind: create, on: enter, state: open, type: " + makes + ", version: 1}\n" +
			"accepts_reactions:\n  '*:fill': {creates: []}\n"
	}
	mustRefuseSet(t, "two Types that create each other when created",
		map[string]string{"crate.yaml": box("crate", "carton"), "carton.yaml": box("carton", "crate")}, "cycle")
	mustRefuseSet(t, "a Type that creates itself when created", map[string]string{"crate.yaml": box("crate", "crate")}, "cycle")

	// No cycle when the chain ends: a crate fills itself with a carton,
	// and a carton creates nothing.
	plain := "id: carton\nversion: 1\nfields:\n  - {id: f_name, type: string}\naccepts_reactions:\n  '*:fill': {creates: []}\n"
	if err := parseSet(t, map[string]string{"crate.yaml": box("crate", "carton"), "carton.yaml": plain}); err != nil {
		t.Errorf("a crate that fills itself with a carton when created: %v", err)
	}
}

// --- an acting timer at an instant ---

func TestActingAt_RefusedAtLoad(t *testing.T) {
	acting := func(line string) string { return meter(line, showR, meterAccepts) }
	cases := []struct{ name, src, mention string }{
		{"at and after", acting("{id: late, at: f_due_at, after: 1h, max: 1, sets: {f_late_at: 'cause.at'}}"), "after"},
		{"at and every", acting("{id: late, at: f_due_at, every: 1h, sets: {f_late_at: 'cause.at'}}"), "every"},
		{"at and max", acting("{id: late, at: f_due_at, max: 2, sets: {f_late_at: 'cause.at'}}"), "max"},
		{"an unknown field", acting("{id: late, at: f_nope, sets: {f_late_at: 'cause.at'}}"), "f_nope"},
		{"a field that is not a datetime", acting("{id: late, at: f_target_s, sets: {f_late_at: 'cause.at'}}"), "f_target_s"},
		{"a multi-value field", acting("{id: late, at: f_days_off, sets: {f_late_at: 'cause.at'}}"), "f_days_off"},
		{"nothing to do: no sets and no hook", acting("{id: late, at: f_due_at}"), "late"},
		{"when and sets without at", acting("{id: late, after: 1h, max: 1, hook: tell_late, sets: {f_late_at: 'cause.at'}}"), "sets"},
		{"when without at", acting("{id: late, after: 1h, max: 1, hook: tell_late, when: 'f_late_at == null'}"), "when"},
		{"a condition that is not a bool", acting("{id: late, at: f_due_at, when: 'f_late_at', sets: {f_late_at: 'cause.at'}}"), "when"},
		{"a condition that does not compile", acting("{id: late, at: f_due_at, when: 'f_late_at ==', sets: {f_late_at: 'cause.at'}}"), "when"},
		{"sets an unknown field", acting("{id: late, at: f_due_at, sets: {f_nope: 'cause.at'}}"), "f_nope"},
		{"sets a value of the wrong type", acting("{id: late, at: f_due_at, sets: {f_late_at: 'f_target_s'}}"), "f_late_at"},
		{"sets a principal field", acting("{id: late, at: f_due_at, sets: {f_owner: '\"user:ana\"'}}"), "f_owner"},
		{"an expression that does not compile", acting("{id: late, at: f_due_at, sets: {f_late_at: 'cause.at +'}}"), "f_late_at"},
		// Setting its own instant would arm it again from inside itself.
		{"sets the field it waits for", acting("{id: late, at: f_due_at, sets: {f_due_at: 'cause.at'}}"), "f_due_at"},
		{"a hook that is not declared", acting("{id: late, at: f_due_at, sets: {f_late_at: 'cause.at'}, hook: h_nope}"), "h_nope"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { mustRefuse(t, c.name, c.src, c.mention) })
	}
	// A field the state freezes cannot be set while in it.
	t.Run("sets a field the state freezes", func(t *testing.T) {
		src := strings.Replace(acting(lateT), "    category: active\n    acting:", "    category: active\n    frozen: [f_late_at]\n    acting:", 1)
		mustRefuse(t, "sets a frozen field", src, "f_late_at")
	})

	// Allowed: sets without a hook; a hook without sets; no condition;
	// two timers on one state; today in scope.
	withoutTellLate := func(src string) string {
		return strings.Replace(src, "hooks:\n  - {id: tell_late, on: timer, kind: action, action: email.notify, reads: [f_owner]}\n", "", 1)
	}
	mustParse(t, withoutTellLate(acting("{id: late, at: f_due_at, sets: {f_late_at: 'cause.at'}}")))
	mustParse(t, strings.Replace(acting("{id: late, at: f_due_at, hook: tell_late}"),
		"{id: f_late_at,    type: datetime, writable_by: kernel}", "{id: f_late_at,    type: datetime}", 1))
	mustParse(t, withoutTellLate(acting("{id: late, at: f_due_at, sets: {f_late_at: 'cause.at', f_note: 'string(today)'}}\n"+
		"      - {id: begun, at: f_started_at, sets: {f_note: '\"begun\"'}}")))
}

// f_late_at is kernel-written, and the timer's sets is its only writer.
// A timer that only runs a hook leaves it without one, and the Type is
// refused as any Type with a kernel-written field nothing writes.
func TestActingAt_SetsCountsAsTheKernelWriter(t *testing.T) {
	mustParse(t, goodMeter())
	mustRefuse(t, "f_late_at with no writer", meter("{id: late, at: f_due_at, hook: tell_late}", showR, meterAccepts), "f_late_at")
}

// A state with such a timer and a duration acting timer, and with a move
// at an instant: they do not exclude each other.
func TestActingAt_BesideOtherTimers(t *testing.T) {
	src := strings.Replace(goodMeter(), "      - "+lateT, "      - "+lateT+"\n      - {id: nudge, after: 48h, max: 1, hook: nudge}", 1)
	src = strings.Replace(src, "hooks:\n", "hooks:\n  - {id: nudge, on: timer, kind: action, action: email.notify, reads: [f_owner]}\n", 1)
	src = strings.Replace(src, "    category: active\n    acting:", "    category: active\n    at: {field: f_started_at, to: stopped}\n    acting:", 1)
	src = strings.Replace(src, "  - {from: running, to: stopped}", "  - {from: running, to: stopped, allowed: ['principal:system:timer', 'role:lead']}", 1)
	mustParse(t, src)
}

// --- the calendar functions in CEL ---

func TestCalendarFunctions_CompileWhereExpressionsAre(t *testing.T) {
	// goodMeter uses them in transitions' sets, goodJob in a create
	// reaction's fields, hours in an invariant.
	mustParse(t, goodMeter())
	mustParse(t, goodJob())
	mustParse(t, hoursSrc)
	// In an acting timer's sets and a set reaction's value.
	mustParse(t, meter("{id: late, at: f_due_at, sets: {f_late_at: 'working_after(cause.at, 60, calendar(f_week, f_days_off, f_zone))'}}",
		"{id: show, kind: set, on: enter, state: running, target: container, set: {f_due: 'working_after(f_due_at, 1, calendar(f_week, f_days_off, f_zone))'}}",
		meterAccepts))

	sets := func(expr string) string {
		return strings.Replace(goodMeter(), "      f_due_at: 'null'", "      f_due_at: '"+expr+"'", 1)
	}
	const c = "calendar(f_week, f_days_off, f_zone)"
	cases := []struct{ name, src, mention string }{
		{"working_after yields an instant, not an int", strings.Replace(goodMeter(),
			"f_elapsed_s: 'working_between(f_started_at, cause.at, calendar(f_week, f_days_off, f_zone))'",
			"f_elapsed_s: 'working_after(f_started_at, 60, calendar(f_week, f_days_off, f_zone))'", 1), "f_elapsed_s"},
		{"working_between yields an int, not an instant", sets("working_between(f_started_at, cause.at, " + c + ")"), "f_due_at"},
		{"seconds that are text", sets("working_after(cause.at, \"60\", " + c + ")"), "working_after"},
		{"an instant that is an int", sets("working_after(f_target_s, 60, " + c + ")"), "working_after"},
		{"no calendar", sets("working_after(cause.at, 60)"), "working_after"},
		{"a calendar that is a string", sets("working_after(cause.at, 60, f_zone)"), "working_after"},
		{"a week that is a string", sets("working_after(cause.at, 60, calendar(f_zone, f_days_off, f_zone))"), "calendar"},
		{"holidays that are strings", sets("working_after(cause.at, 60, calendar(f_week, f_week, f_zone))"), "calendar"},
		{"a zone that is a list", sets("working_after(cause.at, 60, calendar(f_week, f_days_off, f_week))"), "calendar"},
		{"a calendar stored in a field", sets(c), "f_due_at"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { mustRefuse(t, tc.name, tc.src, tc.mention) })
	}
	// Literals work: a week written in the Type itself.
	mustParse(t, sets(`working_after(cause.at, 3600, calendar(["mon 09:00-17:00"], [], "Europe/Paris"))`))
}

// --- Types in subfolders ---

func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// A folder of Types may hold one level of subfolders: a package of Types
// with its README. IDs stay global, and the Types of every folder are
// checked together.
func TestParseDir_ReadsOneLevelOfSubfolders(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"job.v1.yaml": goodJob(), "yard.v1.yaml": yardSrc, "remark.v1.yaml": remarkSrc,
		"clock/meter.v1.yaml": goodMeter(), "clock/hours.v1.yaml": hoursSrc, "clock/README.md": "# clock\n",
		".hidden/note.v1.yaml": noteV1,
	})
	types, err := typedef.ParseDir(dir)
	if err != nil {
		t.Fatalf("ParseDir: %v", err)
	}
	var got []string
	for _, ty := range types {
		got = append(got, ty.ID)
	}
	if want := []string{"hours", "job", "meter", "remark", "yard"}; !slices.Equal(got, want) {
		t.Errorf("ParseDir = %v, want %v (subfolder read, sorted by ID; a dot folder skipped)", got, want)
	}
}

func TestParseDir_Subfolders_Refusals(t *testing.T) {
	// The same Type version in two folders: both files are named.
	dir := writeTree(t, map[string]string{"note.v1.yaml": noteV1, "pack/note.yaml": noteV1})
	_, err := typedef.ParseDir(dir)
	if err == nil || !strings.Contains(err.Error(), "note.v1.yaml") || !strings.Contains(err.Error(), filepath.Join("pack", "note.yaml")) {
		t.Errorf("a duplicate across folders: %v, want an error naming both files", err)
	}
	// A bad file in a subfolder is named with its folder.
	dir = writeTree(t, map[string]string{"note.v1.yaml": noteV1, "pack/broken.yaml": "id: broken\nversion: one\n"})
	if _, err := typedef.ParseDir(dir); err == nil || !strings.Contains(err.Error(), filepath.Join("pack", "broken.yaml")) {
		t.Errorf("a bad file in a subfolder: %v, want an error naming pack/broken.yaml", err)
	}
	// Two levels down is not read silently: a Type nobody loads is a trap.
	dir = writeTree(t, map[string]string{"note.v1.yaml": noteV1, "pack/deep/memo.yaml": strings.Replace(noteV1, "note", "memo", 1)})
	if _, err := typedef.ParseDir(dir); err == nil || !strings.Contains(err.Error(), "deep") {
		t.Errorf("a Type two folders down: %v, want an error naming the folder", err)
	}
	// What spans Types is checked across folders: a consent in the
	// subfolder names a reaction that does not exist.
	dir = writeTree(t, map[string]string{
		"job.v1.yaml": goodJob(), "yard.v1.yaml": yardSrc, "remark.v1.yaml": remarkSrc, "clock/hours.v1.yaml": hoursSrc,
		"clock/meter.v1.yaml": meter(lateT, showR, meterAccepts+"\n  job:ghost: {moves: [paused]}"),
	})
	if _, err := typedef.ParseDir(dir); err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Errorf("a consent in a subfolder naming no reaction: %v, want refused", err)
	}
}
