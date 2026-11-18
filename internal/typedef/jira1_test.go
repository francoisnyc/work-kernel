// Tests for the Jira proof of concept, part 1: what a Type may declare
// (docs/design.md, "Jira proof of concept").
//
//   - incoming: a Type names the links other Types declare towards it, so
//     its guards (and rollups) can read them. One Type's file checks the
//     shape; the set loaded at start checks the other Types (CheckIncoming,
//     called by ParseDir): each `from` Type is loaded, some version of it
//     declares the link with this Type among its targets, and the incoming
//     name is the inverse every such declaration gives.
//   - rollups: counts over a link name the Type can read, by category or
//     by state.
//   - clears: fields a transition unsets in the same write, with the load
//     rules that keep a declared clear from being refused on every run.
//   - targets: ['*']: a link type that may point to an Item of any Type.
//   - The PLAT Types load from types/ with the shape the design gives them.
package typedef_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// storySrc is a Type whose Items link to an epic; ID and INVERSE are
// replaced per test.
const storySrc = `
id: ID
version: 1
initial_state: s_open
fields:
  - {id: f_summary, type: string}
links:
  - {name: epic, inverse: INVERSE, targets: [epic], max: one}
states:
  - {id: s_open, category: todo}
  - {id: s_done, category: done}
transitions:
  - {from: s_open, to: s_done}
`

func storyLike(id, inverse string) string {
	return strings.NewReplacer("ID", id, "INVERSE", inverse).Replace(storySrc)
}

// epicSrc names the stories and bugs that link to it by `epic`, guards its
// closing on them and counts them.
const epicSrc = `
id: epic
version: 1
initial_state: e_open
fields:
  - {id: f_summary, type: string}
links:
  - {name: depends_on, inverse: needed_by, targets: [epic]}
incoming:
  - {name: issues, link: epic, from: [story, bug]}
rollups:
  - {id: progress, over: issues, count_by: category}
states:
  - {id: e_open, category: todo}
  - {id: e_done, category: done}
transitions:
  - from: e_open
    to: e_done
    guard: 'links.issues.all(i, i.category in ["done", "canceled"])'
`

const incomingLine = "  - {name: issues, link: epic, from: [story, bug]}"

// --- incoming ---

// The declaration is kept as written, a guard may read it as links.<name>
// (it is recorded among the links the guard reads), and it survives the
// JSON form the item service stores.
func TestIncoming_ParsesAndGuardsReadIt(t *testing.T) {
	ty := mustParse(t, epicSrc)
	want := typedef.Incoming{Name: "issues", Link: "epic", From: []string{"story", "bug"}}
	if len(ty.Incoming) != 1 || ty.Incoming[0].Name != want.Name || ty.Incoming[0].Link != want.Link ||
		!slices.Equal(ty.Incoming[0].From, want.From) {
		t.Fatalf("Incoming = %+v, want [%+v]", ty.Incoming, want)
	}
	tr, _ := ty.Transition("e_open", "e_done")
	if !slices.Equal(tr.GuardLinks, []string{"issues"}) {
		t.Errorf("GuardLinks = %v, want [issues]: the guard reads the incoming links", tr.GuardLinks)
	}

	data, err := json.Marshal(ty)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	back, err := typedef.FromJSON(data)
	if err != nil {
		t.Fatalf("FromJSON: %v", err)
	}
	if len(back.Incoming) != 1 || back.Incoming[0].Name != "issues" || len(back.Rollups) != 1 {
		t.Errorf("after the round trip: incoming %+v, rollups %+v", back.Incoming, back.Rollups)
	}
	tr, _ = back.Transition("e_open", "e_done")
	if !slices.Equal(tr.GuardLinks, []string{"issues"}) {
		t.Errorf("GuardLinks after the round trip = %v, want [issues]", tr.GuardLinks)
	}
}

// What one Type's file can tell is wrong with its incoming declarations.
func TestIncoming_RefusedInOneType(t *testing.T) {
	cases := []struct {
		name, line, mention string
	}{
		{"no name", "  - {link: epic, from: [story]}", "incoming"},
		{"no link", "  - {name: issues, from: [story]}", "issues"},
		{"no from", "  - {name: issues, link: epic}", "issues"},
		{"an empty from Type", `  - {name: issues, link: epic, from: [story, ""]}`, "issues"},
		{"a name that is not a link name", "  - {name: Issues!, link: epic, from: [story]}", "Issues!"},
		{"a name the Type declares as a link", "  - {name: depends_on, link: epic, from: [story]}", "depends_on"},
		{"a name the Type declares as an inverse", "  - {name: needed_by, link: epic, from: [story]}", "needed_by"},
		{"the same name twice", incomingLine + "\n  - {name: issues, link: parent, from: [task]}", "issues"},
		// Its own links towards itself are already readable by their inverse.
		{"from the Type itself", "  - {name: issues, link: epic, from: [epic]}", "epic"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mustRefuse(t, c.name, swap(t, epicSrc, incomingLine, c.line), c.mention)
		})
	}
	// A guard may not read a name the Type cannot read.
	mustRefuse(t, "a guard reading an undeclared incoming name",
		swap(t, epicSrc, "links.issues.all", "links.tasks.all"), "tasks")
}

// What only the whole set can tell: ParseDir checks incoming declarations
// against the other Types, and CheckIncoming is that check on its own.
func TestParseDir_IncomingIsCheckedAcrossTypes(t *testing.T) {
	load := func(t *testing.T, files map[string]string) ([]*typedef.Type, error) {
		t.Helper()
		dir := t.TempDir()
		for name, body := range files {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return typedef.ParseDir(dir)
	}
	noLink := "id: bug\nversion: 1\nfields:\n  - {id: f_summary, type: string}\n"
	elsewhere := swap(t, storyLike("bug", "issues"), "targets: [epic]", "targets: [bug]")

	refused := []struct {
		name    string
		files   map[string]string
		mention []string
	}{
		{"a from Type that is not loaded",
			map[string]string{"epic.yaml": epicSrc, "story.yaml": storyLike("story", "issues")},
			[]string{"issues", "bug"}},
		{"a from Type that does not declare the link",
			map[string]string{"epic.yaml": epicSrc, "story.yaml": storyLike("story", "issues"), "bug.yaml": noLink},
			[]string{"issues", "bug"}},
		{"a from Type whose link does not target this Type",
			map[string]string{"epic.yaml": epicSrc, "story.yaml": storyLike("story", "issues"), "bug.yaml": elsewhere},
			[]string{"issues", "bug"}},
		{"a from Type that gives the link another inverse",
			map[string]string{"epic.yaml": epicSrc, "story.yaml": storyLike("story", "issues"),
				"bug.yaml": storyLike("bug", "children")},
			[]string{"issues", "children"}},
	}
	for _, c := range refused {
		t.Run(c.name, func(t *testing.T) {
			_, err := load(t, c.files)
			if err == nil {
				t.Fatal("ParseDir accepted the set, want an error")
			}
			for _, m := range c.mention {
				if !strings.Contains(err.Error(), m) {
					t.Errorf("error %q does not mention %q", err, m)
				}
			}
		})
	}

	complete := map[string]string{
		"epic.yaml": epicSrc, "story.yaml": storyLike("story", "issues"), "bug.yaml": storyLike("bug", "issues"),
	}
	types, err := load(t, complete)
	if err != nil {
		t.Fatalf("ParseDir on a complete set: %v", err)
	}
	if err := typedef.CheckIncoming(types); err != nil {
		t.Errorf("CheckIncoming on a complete set: %v", err)
	}
	// Without the bug Type (sorted: bug, epic, story) the same check fails.
	if err := typedef.CheckIncoming(types[1:]); err == nil {
		t.Error("CheckIncoming accepted epic without the bug Type")
	}

	// Some version must declare the link, not every one: Items of a version
	// without it simply never link.
	complete["bug.v2.yaml"] = swap(t, storyLike("bug", "issues"), "version: 1", "version: 2")
	complete["bug.yaml"] = noLink
	if _, err := load(t, complete); err != nil {
		t.Errorf("a from Type declaring the link in one version only: %v", err)
	}
}

// --- rollups ---

// A rollup counts over any link name the Type can read: a declared link,
// the inverse of a link to itself, or an incoming name; by category or by
// state.
func TestRollups_Parse(t *testing.T) {
	ty := mustParse(t, epicSrc)
	if len(ty.Rollups) != 1 || ty.Rollups[0] != (typedef.Rollup{ID: "progress", Over: "issues", CountBy: typedef.CountByCategory}) {
		t.Fatalf("Rollups = %+v, want progress over issues by category", ty.Rollups)
	}
	line := "  - {id: progress, over: issues, count_by: category}"
	for _, ok := range []string{
		"  - {id: progress, over: issues, count_by: state}",
		"  - {id: needs, over: depends_on, count_by: category}",
		"  - {id: needed, over: needed_by, count_by: state}",
		line + "\n  - {id: by_state, over: issues, count_by: state}",
	} {
		if _, err := typedef.Parse([]byte(swap(t, epicSrc, line, ok))); err != nil {
			t.Errorf("rollups %q: %v", ok, err)
		}
	}
	for _, c := range []struct{ name, line, mention string }{
		{"no id", "  - {over: issues, count_by: category}", "rollup"},
		{"an id used twice", line + "\n  - {id: progress, over: issues, count_by: state}", "progress"},
		{"no link name", "  - {id: progress, count_by: category}", "progress"},
		{"a link name the Type cannot read", "  - {id: progress, over: stories, count_by: category}", "stories"},
		{"no count_by", "  - {id: progress, over: issues}", "progress"},
		{"an unknown count_by", "  - {id: progress, over: issues, count_by: points}", "points"},
	} {
		t.Run(c.name, func(t *testing.T) {
			mustRefuse(t, c.name, swap(t, epicSrc, line, c.line), c.mention)
		})
	}
}

// --- clears ---

// clearSrc: closing needs a resolution; reopening clears it. TRANSITION is
// the reopening transition, STATE_DONE the done state's declaration.
const clearSrc = `
id: chore
version: 1
initial_state: s_open
fields:
  - {id: f_resolution, type: enum, options: [fixed, wontfix]}
  - {id: f_note,       type: string}
  - {id: f_reviewer,   type: principal}
  - {id: f_stamp,      type: string, writable_by: kernel}
  - {id: f_grade,      type: int, writable_by: ['role:lead']}
states:
  - {id: s_open, category: todo}
  - STATE_DONE
transitions:
  - {from: s_open, to: s_done, required: [f_resolution]}
  - TRANSITION
hooks:
  - {id: stamp, on: enter, state: s_done, kind: enrich, action: crm.get_plan, reads: [f_note], writes: [f_stamp]}
`

func clearType(t *testing.T, done, transition string) string {
	t.Helper()
	return strings.NewReplacer("STATE_DONE", done, "TRANSITION", transition).Replace(clearSrc)
}

const doneState = "{id: s_done, category: done}"

// A transition's clears are kept as written and survive the stored JSON.
func TestClears_Parse(t *testing.T) {
	ty := mustParse(t, clearType(t, doneState, "{from: s_done, to: s_open, clears: [f_resolution, f_note]}"))
	tr, ok := ty.Transition("s_done", "s_open")
	if !ok || !slices.Equal(tr.Clears, []string{"f_resolution", "f_note"}) {
		t.Fatalf("Clears = %v, want [f_resolution f_note]", tr.Clears)
	}
	data, err := json.Marshal(ty)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	back, err := typedef.FromJSON(data)
	if err != nil {
		t.Fatalf("FromJSON: %v", err)
	}
	if tr, _ := back.Transition("s_done", "s_open"); !slices.Equal(tr.Clears, []string{"f_resolution", "f_note"}) {
		t.Errorf("Clears after the round trip = %v", tr.Clears)
	}
	// A field only a role writes may be cleared: the caller is checked when
	// the move runs, like a patch.
	mustParse(t, clearType(t, doneState, "{from: s_done, to: s_open, clears: [f_grade]}"))
}

// A clear that could never run, or that contradicts its own transition, is
// refused when the Type loads.
func TestClears_RefusedAtLoad(t *testing.T) {
	cases := []struct {
		name, done, transition, mention string
	}{
		{"an unknown field", doneState, "{from: s_done, to: s_open, clears: [f_nope]}", "f_nope"},
		{"a field listed twice", doneState, "{from: s_done, to: s_open, clears: [f_note, f_note]}", "f_note"},
		{"a field the same transition requires", doneState,
			"{from: s_done, to: s_open, required: [f_note], clears: [f_note]}", "f_note"},
		{"a field the state it leaves freezes", "{id: s_done, category: done, frozen: [f_resolution]}",
			"{from: s_done, to: s_open, clears: [f_resolution]}", "f_resolution"},
		{"a field its own allowed names", doneState,
			"{from: s_done, to: s_open, allowed: ['field:f_reviewer'], clears: [f_reviewer]}", "f_reviewer"},
		{"a field only the kernel writes", doneState, "{from: s_done, to: s_open, clears: [f_stamp]}", "f_stamp"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mustRefuse(t, c.name, clearType(t, c.done, c.transition), c.mention)
		})
	}
}

// A transition that a check holds may not clear fields. A held move is
// written in two steps (the request with its patch, then the move when the
// hold resolves), and which step would clear, and what a denied hold leaves
// behind, is not designed: refused at load until it is (Later).
func TestClears_OnAHeldTransition_Refused(t *testing.T) {
	check := "  - {id: recheck, on: transition, from: s_done, to: s_open, kind: check, action: erp.check_credit, reads: [f_note, f_grade], writes: [f_note]}\n"
	mustParse(t, clearType(t, doneState, "{from: s_done, to: s_open}")+check)
	mustRefuse(t, "clears on a transition a check holds",
		clearType(t, doneState, "{from: s_done, to: s_open, clears: [f_resolution]}")+check, "recheck")
}

// --- a link type that may target any Type ---

const subscriptionSrc = `
id: subscription
version: 1
links:
  - {name: watches, inverse: watched_by, targets: TARGETS, max: one}
`

// '*' alone means any Type: the Type loads on its own, no target needs to
// be in the set, and it cannot be mixed with named Types.
func TestLinkTarget_AnyType(t *testing.T) {
	ty := mustParse(t, swap(t, subscriptionSrc, "TARGETS", "['*']"))
	l, ok := ty.Link("watches")
	if !ok || !slices.Equal(l.Targets, []string{typedef.AnyType}) {
		t.Fatalf("watches = %+v, want targets [*]", l)
	}
	if err := typedef.CheckLinkTargets([]*typedef.Type{ty}); err != nil {
		t.Errorf("CheckLinkTargets on a Type that targets any Type: %v", err)
	}
	mustRefuse(t, "'*' beside a named Type", swap(t, subscriptionSrc, "TARGETS", "['*', task]"), "*")
	mustRefuse(t, "'*' twice", swap(t, subscriptionSrc, "TARGETS", "['*', '*']"), "*")

	// Any Type may name the subscriptions that watch it.
	watched := mustParse(t, `
id: page
version: 1
fields:
  - {id: f_title, type: string}
incoming:
  - {name: watched_by, link: watches, from: [subscription]}
rollups:
  - {id: watchers, over: watched_by, count_by: state}
`)
	if err := typedef.CheckIncoming([]*typedef.Type{watched, ty}); err != nil {
		t.Errorf("CheckIncoming, a link that targets any Type: %v", err)
	}
}

// --- the PLAT Types ---

// latest returns the highest loaded version of a Type.
func latest(t *testing.T, types []*typedef.Type, id string) *typedef.Type {
	t.Helper()
	var found *typedef.Type
	for _, ty := range types {
		if ty.ID == id && (found == nil || ty.Version > found.Version) {
			found = ty
		}
	}
	if found == nil {
		t.Fatalf("types/ has no Type %q", id)
	}
	return found
}

// types/ holds the PLAT project's Types as docs/design.md lists them ("The
// PLAT Types"), and the whole directory still loads together.
func TestPLATTypes_Load(t *testing.T) {
	types, err := typedef.ParseDir("../../types")
	if err != nil {
		t.Fatalf("ParseDir: %v", err)
	}

	// story, bug and task: one lifecycle, one set of fields and links.
	wantFields := map[string]typedef.FieldType{
		"f_summary": typedef.String, "f_description": typedef.String, "f_assignee": typedef.Principal,
		"f_priority": typedef.Enum, "f_points": typedef.Int, "f_resolution": typedef.Enum, "f_rank": typedef.String,
	}
	wantStates := map[string]string{
		"todo": "todo", "in_progress": "active", "in_review": "active", "done": "done", "canceled": "canceled",
	}
	issues := []string{"story", "bug", "task"}
	for _, id := range issues {
		ty := latest(t, types, id)
		if ty.InitialState != "todo" {
			t.Errorf("%s: initial state %q, want todo", id, ty.InitialState)
		}
		for fid, ft := range wantFields {
			if f, ok := ty.Field(fid); !ok || f.Type != ft {
				t.Errorf("%s: field %s = %+v, want type %s", id, fid, f, ft)
			}
		}
		if f, _ := ty.Field("f_priority"); !slices.Equal(f.Options, []string{"P1", "P2", "P3", "P4"}) {
			t.Errorf("%s: f_priority options %v, want P1..P4 (they sort by name)", id, f.Options)
		}
		for sid, cat := range wantStates {
			if s, ok := ty.State(sid); !ok || s.Category != cat {
				t.Errorf("%s: state %s = %+v, want category %s", id, sid, s, cat)
			}
		}
		for _, l := range []struct {
			name, inverse string
			targets       []string
			max           typedef.Cardinality
		}{
			{"epic", "issues", []string{"epic"}, typedef.One},
			{"sprint", "sprint_issues", []string{"sprint"}, typedef.One},
			{"blocks", "blocked_by", issues, ""},
			{"relates", "related_by", issues, ""},
			{"duplicates", "duplicated_by", issues, typedef.One},
		} {
			got, ok := ty.Link(l.name)
			if !ok || got.Inverse != l.inverse || !slices.Equal(got.Targets, l.targets) || got.SourceSingle() != (l.max == typedef.One) {
				t.Errorf("%s: link %s = %+v, want inverse %s, targets %v, max %q", id, l.name, got, l.inverse, l.targets, l.max)
			}
		}
		// Done needs a resolution, from in_review and from in_progress;
		// reopening clears it.
		for _, from := range []string{"in_review", "in_progress"} {
			if tr, ok := ty.Transition(from, "done"); !ok || !slices.Equal(tr.Required, []string{"f_resolution"}) {
				t.Errorf("%s: %s -> done = %+v, want it to require f_resolution", id, from, tr)
			}
		}
		if tr, ok := ty.Transition("done", "todo"); !ok || !slices.Equal(tr.Clears, []string{"f_resolution"}) {
			t.Errorf("%s: done -> todo = %+v, want it to clear f_resolution", id, tr)
		}
		for _, move := range [][2]string{
			{"todo", "in_progress"}, {"in_progress", "in_review"}, {"in_review", "in_progress"},
			{"todo", "canceled"}, {"in_progress", "canceled"}, {"in_review", "canceled"}, {"canceled", "todo"},
		} {
			if _, ok := ty.Transition(move[0], move[1]); !ok {
				t.Errorf("%s: no transition %s -> %s", id, move[0], move[1])
			}
		}
		if len(ty.Hooks) != 0 {
			t.Errorf("%s declares hooks: a drag must cost no outbox row", id)
		}
	}

	epic := latest(t, types, "epic")
	if len(epic.Incoming) != 1 || epic.Incoming[0].Name != "issues" || epic.Incoming[0].Link != "epic" ||
		!slices.Equal(epic.Incoming[0].From, issues) {
		t.Errorf("epic: incoming = %+v, want issues: epic links from story, bug and task", epic.Incoming)
	}
	if len(epic.Rollups) != 1 || epic.Rollups[0] != (typedef.Rollup{ID: "progress", Over: "issues", CountBy: typedef.CountByCategory}) {
		t.Errorf("epic: rollups = %+v, want progress over issues by category", epic.Rollups)
	}
	if tr, ok := epic.Transition("in_progress", "done"); !ok || !slices.Equal(tr.GuardLinks, []string{"issues"}) {
		t.Errorf("epic: in_progress -> done = %+v, want a guard that reads its issues", tr)
	}
	if f, ok := epic.Field("f_owner"); !ok || f.Type != typedef.Principal {
		t.Errorf("epic: f_owner = %+v, want a principal field", f)
	}

	sprint := latest(t, types, "sprint")
	for sid, cat := range map[string]string{"planned": "backlog", "active": "active", "closed": "done"} {
		if s, ok := sprint.State(sid); !ok || s.Category != cat {
			t.Errorf("sprint: state %s = %+v, want category %s", sid, s, cat)
		}
	}
	for _, fid := range []string{"f_start", "f_end"} {
		if f, ok := sprint.Field(fid); !ok || f.Type != typedef.Date {
			t.Errorf("sprint: %s = %+v, want a date field", fid, f)
		}
	}
	for _, s := range sprint.States {
		if s.Timer != nil || len(s.Acting) > 0 {
			t.Errorf("sprint: state %s has a timer; the sprint is closed by hand in part 1", s.ID)
		}
	}

	// The container, comments and subscriptions have no lifecycle.
	for id, fields := range map[string][]string{
		"space": {"f_key", "f_name"}, "comment": {"f_body"}, "subscription": nil,
	} {
		ty := latest(t, types, id)
		if ty.InitialState != typedef.ImplicitState {
			t.Errorf("%s: initial state %q, want none declared", id, ty.InitialState)
		}
		for _, fid := range fields {
			if f, ok := ty.Field(fid); !ok || f.Type != typedef.String {
				t.Errorf("%s: %s = %+v, want a string field", id, fid, f)
			}
		}
	}
	watches, ok := latest(t, types, "subscription").Link("watches")
	if !ok || watches.Inverse != "watched_by" || !slices.Equal(watches.Targets, []string{typedef.AnyType}) || !watches.SourceSingle() {
		t.Errorf("subscription: watches = %+v, want inverse watched_by, any target, at most one", watches)
	}
}
