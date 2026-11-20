// Tests for Jira part 2, step 2: what a Type may declare (docs/design.md,
// "Decisions for Jira part 2", primitive 3 and multi-value fields).
//
//   - kind: move, on: enter: entering a state moves the Item a max-one link
//     points to. The target Type consents: accepts_reactions:
//     {<type>:<id>: {moves: [<state>]}}.
//   - kind: move, on: created_inside: an Item of one of `of` created inside
//     an Item of this Type moves that Item itself. Declared on the
//     container, so it needs no consent. `when` reads the created Item's
//     fields as child.<id>; `by: creator` asks that the container's creator
//     created it; `from` is the container's own states.
//   - kind: create, on: enter: entering a state creates an Item of
//     type@version inside this one, its fields CEL over this Item's fields
//     plus cause.at. The created Type consents: {creates: [fields]}.
//   - accepts_reactions takes the old list (the fields a reaction may
//     write) or a map with fields, moves and creates.
//   - multiple: true: a field holding a set (string, enum, principal, date).
//
// One Type (Parse) checks its own declarations; a set loaded together
// (ParseDir) checks what spans Types: consent, the Types and fields a
// reaction names, a held target transition, and cycles.
package typedef_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// desk is a container that reacts: a public remark by its creator, made
// inside it while it waits, sets it working again; starting work on it
// creates a todo inside it.
const deskSrc = `
id: desk
version: 1
initial_state: open
fields:
  - {id: f_title,    type: string}
  - {id: f_assignee, type: principal}
states:
  - {id: open,    category: todo}
  - {id: working, category: active}
  - {id: waiting, category: waiting}
  - {id: done,    category: done}
transitions:
  - {from: open,    to: working}
  - {from: working, to: waiting}
  - {from: waiting, to: working, allowed: ['role:support'], guard: 'f_assignee != null'}
  - {from: working, to: done}
reactions:
  - REOPEN
  - CHECKLIST
`

const (
	reopenR    = "{id: reopen, kind: move, on: created_inside, of: [remark], when: 'child.f_public == true', by: creator, to: working, from: [waiting]}"
	checklistR = `{id: checklist, kind: create, on: enter, state: working, from: [open], type: todo, version: 1, fields: {f_title: '"Check: " + f_title', f_opened_at: 'cause.at'}}`
)

func desk(reopen, checklist string) string {
	return strings.NewReplacer("REOPEN", reopen, "CHECKLIST", checklist).Replace(deskSrc)
}

const remarkSrc = `
id: remark
version: 1
fields:
  - {id: f_body,   type: string}
  - {id: f_public, type: bool}
`

// A todo is created by desk's reaction only: the consent is the writer its
// kernel-written field must have.
const todoSrc = `
id: todo
version: 1
fields:
  - {id: f_title,     type: string}
  - {id: f_opened_at, type: datetime, writable_by: kernel}
accepts_reactions:
  desk:checklist: {creates: [f_title, f_opened_at]}
`

// part and whole: a part that is done finishes the whole it belongs to.
const partSrc = `
id: part
version: 1
initial_state: open
fields:
  - {id: f_name, type: string}
  - {id: f_ok,   type: bool}
states:
  - {id: open, category: todo}
  - {id: done, category: done}
transitions:
  - {from: open, to: done}
links:
  - {name: whole, inverse: parts, targets: [whole], max: one}
reactions:
  - FINISH
`

const finishR = "{id: finish, kind: move, on: enter, state: done, when: 'f_ok == true', link: whole, to: done, target_from: [building]}"

func part(finish string) string { return strings.Replace(partSrc, "FINISH", finish, 1) }

const wholeSrc = `
id: whole
version: 1
initial_state: building
fields:
  - {id: f_name, type: string}
  - {id: f_count, type: int}
states:
  - {id: building, category: active}
  - {id: done,     category: done}
transitions:
  - {from: building, to: done, allowed: ['role:lead']}
accepts_reactions:
  ACCEPTS
`

func whole(accepts string) string { return strings.Replace(wholeSrc, "ACCEPTS", accepts, 1) }

const wholeAccepts = "part:finish: {moves: [done]}"

// --- one Type ---

// The declarations are kept as written and survive the stored JSON.
func TestMoveAndCreateReactions_Parse(t *testing.T) {
	check := func(ty *typedef.Type) {
		t.Helper()
		r, ok := ty.Reaction("reopen")
		if !ok || r.Kind != typedef.ReactionMove || r.On != typedef.OnCreatedInside || !slices.Equal(r.Of, []string{"remark"}) ||
			r.When != "child.f_public == true" || r.By != typedef.ByCreator || r.To != "working" ||
			!slices.Equal(r.From, []string{"waiting"}) {
			t.Errorf("reopen = %+v", r)
		}
		c, ok := ty.Reaction("checklist")
		if !ok || c.Kind != typedef.ReactionCreate || c.On != typedef.OnEnter || c.State != "working" ||
			!slices.Equal(c.From, []string{"open"}) || c.Type != "todo" || c.Version != 1 ||
			c.Fields["f_title"] != `"Check: " + f_title` || c.Fields["f_opened_at"] != "cause.at" {
			t.Errorf("checklist = %+v", c)
		}
	}
	ty := mustParse(t, desk(reopenR, checklistR))
	check(ty)
	data, err := json.Marshal(ty)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	back, err := typedef.FromJSON(data)
	if err != nil {
		t.Fatalf("FromJSON: %v", err)
	}
	check(back)

	p := mustParse(t, part(finishR))
	f, ok := p.Reaction("finish")
	if !ok || f.Kind != typedef.ReactionMove || f.On != typedef.OnEnter || f.State != "done" || f.Link != "whole" ||
		f.To != "done" || !slices.Equal(f.TargetFrom, []string{"building"}) || f.When != "f_ok == true" {
		t.Errorf("finish = %+v", f)
	}
	// Without a condition, and from any state of the target.
	mustParse(t, part("{id: finish, kind: move, on: enter, state: done, link: whole, to: done}"))
}

func TestMoveReactions_RefusedAtLoad(t *testing.T) {
	inside := func(rest string) string {
		return desk("{id: reopen, kind: move, on: created_inside, "+rest+"}", checklistR)
	}
	enter := func(rest string) string { return part("{id: finish, kind: move, on: enter, " + rest + "}") }
	cases := []struct{ name, src, mention string }{
		// created_inside
		{"no `of`", inside("to: working, from: [waiting]"), "of"},
		{"no `to`", inside("of: [remark], from: [waiting]"), "to"},
		{"an unknown state to move to", inside("of: [remark], to: s_nope"), "s_nope"},
		{"a `from` state with no transition to `to`", inside("of: [remark], to: working, from: [done]"), "done"},
		{"an unknown `from` state", inside("of: [remark], to: working, from: [s_nope]"), "s_nope"},
		{"a state: created_inside enters none", inside("of: [remark], to: working, state: waiting"), "state"},
		{"a link: it moves the Item itself", inside("of: [remark], to: working, link: parent"), "link"},
		{"target_from: `from` says it", inside("of: [remark], to: working, target_from: [waiting]"), "target_from"},
		{"an unknown `by`", inside("of: [remark], to: working, by: anyone"), "anyone"},
		{"a `when` that does not compile", inside("of: [remark], to: working, when: 'child.f_public =='"), "when"},
		// The condition is about the Item created, named child: the
		// container's own fields are not in scope.
		{"a `when` reading the container's own field", inside("of: [remark], to: working, when: 'f_title == \"x\"'"), "f_title"},
		{"a `when` reading the caller", inside("of: [remark], to: working, when: 'caller == \"user:ana\"'"), "caller"},
		// on: enter
		{"no link", enter("state: done, to: done"), "link"},
		{"an unknown link", enter("state: done, link: l_nope, to: done"), "l_nope"},
		{"no state", enter("link: whole, to: done"), "state"},
		{"no `to`", enter("state: done, link: whole"), "to"},
		{"`of`: on: enter is triggered by a state", enter("state: done, link: whole, to: done, of: [remark]"), "of"},
		{"`by`: identity is judged on created_inside only", enter("state: done, link: whole, to: done, by: creator"), "by"},
		{"a `when` that is not a bool", enter("state: done, link: whole, to: done, when: 'f_name'"), "when"},
		{"a `when` reading child", enter("state: done, link: whole, to: done, when: 'child.f_public'"), "child"},
		{"an `add`: a move writes no field", enter("state: done, link: whole, to: done, add: {f_name: f_ok}"), "add"},
		{"`undoes`", enter("state: done, link: whole, to: done, undoes: finish"), "undoes"},
		{"create's keys on a move", enter("state: done, link: whole, to: done, type: todo, version: 1"), "type"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { mustRefuse(t, c.name, c.src, c.mention) })
	}
	// A link that may have many targets: which one would move?
	t.Run("a link with many targets", func(t *testing.T) {
		src := strings.Replace(part(finishR), "targets: [whole], max: one", "targets: [whole]", 1)
		mustRefuse(t, "a link without max: one", src, "whole")
	})
	// container is reserved: it will name containment where a link is named.
	t.Run("a link named container", func(t *testing.T) {
		src := strings.Replace(part(finishR), "{name: whole, inverse: parts", "{name: container, inverse: parts", 1)
		mustRefuse(t, "a link named container", src, "container")
	})
}

func TestCreateReactions_RefusedAtLoad(t *testing.T) {
	create := func(rest string) string {
		return desk(reopenR, "{id: checklist, kind: create, on: enter, state: working, "+rest+"}")
	}
	cases := []struct{ name, src, mention string }{
		{"no type", create("version: 1, fields: {f_title: f_title}"), "type"},
		{"no version", create("type: todo, fields: {f_title: f_title}"), "version"},
		{"an unknown state", desk(reopenR, "{id: checklist, kind: create, on: enter, state: s_nope, type: todo, version: 1}"), "s_nope"},
		{"an expression that does not compile", create("type: todo, version: 1, fields: {f_title: 'f_title +'}"), "f_title"},
		{"an expression reading an unknown field", create("type: todo, version: 1, fields: {f_title: 'f_nope'}"), "f_nope"},
		{"an expression reading the caller", create("type: todo, version: 1, fields: {f_title: 'caller'}"), "caller"},
		{"`to`: a create moves nothing", create("type: todo, version: 1, to: done"), "to"},
		{"on: created_inside (not built)", desk(reopenR, "{id: checklist, kind: create, on: created_inside, of: [remark], type: todo, version: 1}"), "created_inside"},
		{"`by`", create("type: todo, version: 1, by: creator"), "by"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { mustRefuse(t, c.name, c.src, c.mention) })
	}
	// A create with no fields is fine: an empty Item.
	mustParse(t, create("type: todo, version: 1"))
}

// --- consent ---

// accepts_reactions takes the list it always took (the fields a reaction
// may write) or a map; both survive the stored JSON.
func TestAcceptsReactions_ListOrMap(t *testing.T) {
	jar := mustParse(t, jarSrc) // the list form, as before
	if !jar.Accepts("pledge:take", []string{"f_level"}) || jar.AcceptsMove("pledge:take", "retired") ||
		jar.AcceptsCreate("pledge:take", nil) {
		t.Errorf("the list form is the fields a reaction may write, and nothing else")
	}
	src := whole("part:finish: {moves: [done]}\n  part:stamp: {fields: [f_count]}\n  part:spawn: {creates: [f_name]}\n  part:blank: {creates: []}")
	for _, ty := range []*typedef.Type{mustParse(t, src), roundTrip(t, mustParse(t, src))} {
		if !ty.AcceptsMove("part:finish", "done") || ty.AcceptsMove("part:finish", "building") || ty.AcceptsMove("part:stamp", "done") {
			t.Errorf("AcceptsMove: want part:finish into done only")
		}
		if !ty.Accepts("part:stamp", []string{"f_count"}) || ty.Accepts("part:finish", []string{"f_count"}) {
			t.Errorf("Accepts: want part:stamp writing f_count only")
		}
		if !ty.AcceptsCreate("part:spawn", []string{"f_name"}) || !ty.AcceptsCreate("part:spawn", nil) ||
			ty.AcceptsCreate("part:finish", nil) || !ty.AcceptsCreate("part:blank", nil) || ty.AcceptsCreate("part:blank", []string{"f_name"}) {
			t.Errorf("AcceptsCreate: want part:spawn with f_name or nothing, part:blank with nothing")
		}
	}
	for name, bad := range map[string]string{
		"an unknown state":       "part:finish: {moves: [s_nope]}",
		"an unknown field":       "part:finish: {creates: [f_nope]}",
		"an unknown key":         "part:finish: {deletes: [done]}",
		"a key that is no <t:r>": "finish: {moves: [done]}",
	} {
		mustRefuse(t, name, whole(bad), "")
	}
}

// --- across Types ---

func parseSet(t *testing.T, files map[string]string) error {
	t.Helper()
	_, err := typedef.ParseDir(writeFiles(t, files))
	return err
}

func TestParseDir_MoveAndCreateReactions(t *testing.T) {
	good := map[string]string{
		"desk.yaml": desk(reopenR, checklistR), "remark.yaml": remarkSrc, "todo.yaml": todoSrc,
		"part.yaml": part(finishR), "whole.yaml": whole(wholeAccepts),
	}
	if err := parseSet(t, good); err != nil {
		t.Fatalf("the five Types together: %v", err)
	}
	with := func(name, src string) map[string]string {
		out := map[string]string{}
		for k, v := range good {
			out[k] = v
		}
		if src == "" {
			delete(out, name)
		} else {
			out[name] = src
		}
		return out
	}
	cases := []struct {
		name    string
		files   map[string]string
		mention string
	}{
		// A move on entering a state needs the target Type's consent, for
		// that state.
		{"the target does not accept the move", with("whole.yaml", whole("part:other: {moves: [done]}")), "finish"},
		{"the target accepts another state", with("whole.yaml", strings.Replace(whole("part:finish: {moves: [building]}"),
			"{from: building, to: done, allowed: ['role:lead']}", "{from: building, to: done}\n  - {from: done, to: building}", 1)), "finish"},
		{"the target accepts fields, not the move", with("whole.yaml", whole("part:finish: [f_name]")), "finish"},
		{"the target Type is not loaded", with("whole.yaml", ""), "whole"},
		{"the target has no such transition", with("part.yaml", part(strings.Replace(finishR, "target_from: [building]", "target_from: [done]", 1))), "done"},
		{"an accepted move names no reaction", with("whole.yaml", whole(wholeAccepts+"\n  part:ghost: {moves: [done]}")), "ghost"},
		// The move a reaction asks for may not be one a check holds.
		{"the target's transition is held by a check", with("whole.yaml", whole(wholeAccepts)+
			"hooks:\n  - {id: recheck, on: transition, from: building, to: done, kind: check, action: erp.check_credit, reads: [f_name, f_name], writes: [f_name]}\n"), "recheck"},
		// created_inside names Types and their fields.
		{"an `of` Type is not loaded", with("remark.yaml", ""), "remark"},
		{"`when` reads a field the created Type does not declare",
			with("desk.yaml", desk(strings.Replace(reopenR, "child.f_public == true", "child.f_secret == true", 1), checklistR)), "f_secret"},
		// A create names a Type version that accepts it, and its fields.
		{"the created Type is not loaded", with("todo.yaml", ""), "todo"},
		{"no such version of the created Type", with("desk.yaml", desk(reopenR, strings.Replace(checklistR, "version: 1", "version: 7", 1))), "todo"},
		{"the created Type does not accept it", with("todo.yaml", strings.Replace(todoSrc, "desk:checklist", "desk:other", 1)), "checklist"},
		{"it sets a field the consent does not list", with("todo.yaml", strings.Replace(todoSrc, "{creates: [f_title, f_opened_at]}", "{creates: [f_opened_at]}", 1)), "f_title"},
		{"it sets a field the created Type does not declare",
			with("desk.yaml", desk(reopenR, strings.Replace(checklistR, "f_opened_at: 'cause.at'", "f_nope: 'cause.at'", 1))), "f_nope"},
		{"a string for a datetime field",
			with("desk.yaml", desk(reopenR, strings.Replace(checklistR, "f_opened_at: 'cause.at'", "f_opened_at: 'f_title'", 1))), "f_opened_at"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := parseSet(t, c.files)
			if err == nil {
				t.Fatalf("accepted, want refused at load")
			}
			if !strings.Contains(err.Error(), c.mention) {
				t.Errorf("error %q does not mention %q", err, c.mention)
			}
		})
	}
}

// Reactions can chain. A cycle that can be seen in the declarations is
// refused, whatever the conditions on the way: MaxHop is the stop at run
// time, not a design.
func TestParseDir_ReactionCyclesAreRefused(t *testing.T) {
	// whole done moves its first part done; part done moves its whole done.
	wholeBack := strings.Replace(whole(wholeAccepts), "accepts_reactions:",
		"links:\n  - {name: first, inverse: first_of, targets: [part], max: one}\n"+
			"reactions:\n  - {id: settle, kind: move, on: enter, state: done, link: first, to: done}\naccepts_reactions:", 1)
	partBack := part(finishR) + "accepts_reactions:\n  whole:settle: {moves: [done]}\n"
	err := parseSet(t, map[string]string{"part.yaml": partBack, "whole.yaml": wholeBack})
	if err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Errorf("two Types moving each other into done: %v, want a cycle refused", err)
	}
	// Without the way back it loads.
	if err := parseSet(t, map[string]string{"part.yaml": part(finishR), "whole.yaml": whole(wholeAccepts)}); err != nil {
		t.Errorf("one way only: %v", err)
	}

	// desk entering working creates a todo inside it; a todo created inside
	// a desk moves the desk to working.
	loop := desk("{id: reopen, kind: move, on: created_inside, of: [todo], to: working, from: [waiting]}", checklistR)
	err = parseSet(t, map[string]string{"desk.yaml": loop, "todo.yaml": todoSrc})
	if err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Errorf("a desk that creates what moves it: %v, want a cycle refused", err)
	}
}

// --- multi-value fields ---

const multiSrc = `
id: card
version: 1
initial_state: s_open
fields:
  - {id: f_title,     type: string}
  - {id: f_labels,    type: string,    multiple: true, max: 3}
  - {id: f_tags,      type: enum,      multiple: true, options: [red, green, blue]}
  - {id: f_reviewers, type: principal, multiple: true}
  - {id: f_days,      type: date,      multiple: true}
  - {id: f_points,    type: int}
states:
  - {id: s_open, category: todo}
  - {id: s_done, category: done}
transitions:
  - from: s_open
    to: s_done
    guard: 'f_labels != null && "ready" in f_labels && size(f_reviewers) > 0'
    allowed: ['field:f_reviewers']
    required: [f_tags]
`

func TestMultiValue_FieldParses(t *testing.T) {
	ty := roundTrip(t, mustParse(t, multiSrc))
	for id, max := range map[string]int{"f_labels": 3, "f_tags": 0, "f_reviewers": 0, "f_days": 0} {
		if f, ok := ty.Field(id); !ok || !f.Multiple || f.Max != max {
			t.Errorf("%s = %+v, want multiple with max %d", id, f, max)
		}
	}
	if f, _ := ty.Field("f_title"); f.Multiple {
		t.Error("f_title is multiple")
	}
}

func TestMultiValue_RefusedAtLoad(t *testing.T) {
	field := func(decl string) string {
		return strings.Replace(multiSrc, "  - {id: f_points,    type: int}", "  - "+decl, 1)
	}
	cases := []struct{ name, src, mention string }{
		{"a set of ints", field("{id: f_x, type: int, multiple: true}"), "f_x"},
		{"a set of bools", field("{id: f_x, type: bool, multiple: true}"), "f_x"},
		{"a set of datetimes", field("{id: f_x, type: datetime, multiple: true}"), "f_x"},
		{"max without multiple", field("{id: f_x, type: string, max: 3}"), "f_x"},
		{"a negative max", field("{id: f_x, type: string, multiple: true, max: -1}"), "f_x"},
		// A guard that treats the set as one value does not compile.
		{"a guard comparing the set to a string", strings.Replace(multiSrc, `"ready" in f_labels`, `f_labels == "ready"`, 1), "f_labels"},
		// Not designed for sets yet (Later): each is refused, not half-built.
		{"`sets` on a multi-value field", strings.Replace(multiSrc, "required: [f_tags]", "required: [f_tags]\n    sets: {f_labels: 'null'}", 1), "f_labels"},
		{"a timer at a multi-value field", strings.Replace(multiSrc, "{id: s_open, category: todo}", "{id: s_open, category: todo, at: {field: f_days, to: s_done}}", 1), "f_days"},
		{"a hook reading a multi-value field", multiSrc + "hooks:\n  - {id: get_plan, on: enter, state: s_open, kind: enrich, action: crm.get_plan, reads: [f_labels], writes: [f_title]}\n", "f_labels"},
		{"a hook writing a multi-value field", multiSrc + "hooks:\n  - {id: get_plan, on: enter, state: s_open, kind: enrich, action: crm.get_plan, reads: [f_title], writes: [f_labels]}\n", "f_labels"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { mustRefuse(t, c.name, c.src, c.mention) })
	}
}

// A value is a list of values of the field's type, or a set operation.
func TestMultiValue_CheckFields(t *testing.T) {
	ty := mustParse(t, multiSrc)
	l := func(vs ...any) []any { return vs }
	good := map[string]any{
		"f_labels":    l("b", "a", "a"), // duplicates and order are the service's to tidy
		"f_tags":      l("red", "blue"),
		"f_reviewers": l("user:ana", "agent:bot"),
		"f_days":      l("2026-12-04"),
	}
	if err := ty.CheckFields(good, false); err != nil {
		t.Errorf("lists: %v", err)
	}
	for name, v := range map[string]any{
		"empty list":          l(),
		"null":                nil,
		"add":                 map[string]any{"add": l("a")},
		"remove":              map[string]any{"remove": l("a")},
		"add and remove":      map[string]any{"add": l("a"), "remove": l("b")},
		"four, over the max":  l("a", "b", "c", "d"), // max depends on the stored set too: the service's to judge
		"an empty string too": l(""),
	} {
		if err := ty.CheckFields(map[string]any{"f_labels": v}, true); err != nil {
			t.Errorf("f_labels = %s: %v", name, err)
		}
	}
	bad := []struct {
		name, field string
		v           any
	}{
		{"one value where a list is wanted", "f_labels", "a"},
		{"a list for a single-value field", "f_title", l("a")},
		{"a number in a list of strings", "f_labels", l("a", 3)},
		{"a list in a list", "f_labels", l(l("a"))},
		{"null in a list", "f_labels", l("a", nil)},
		{"a member that is no option", "f_tags", l("red", "pink")},
		{"a member that is no principal", "f_reviewers", l("user:ana", "bob")},
		{"a member that is no date", "f_days", l("2026-13-01")},
		{"a set operation with an unknown key", "f_labels", map[string]any{"add": l("a"), "replace": l("b")}},
		{"an empty set operation", "f_labels", map[string]any{}},
		{"a value both added and removed", "f_labels", map[string]any{"add": l("a", "b"), "remove": l("b")}},
		{"a set operation that adds no list", "f_labels", map[string]any{"add": "a"}},
		{"a set operation with a bad member", "f_tags", map[string]any{"remove": l("pink")}},
		{"a set operation on a single-value field", "f_title", map[string]any{"add": l("a")}},
	}
	for _, c := range bad {
		err := ty.CheckFields(map[string]any{c.field: c.v}, true)
		if err == nil {
			t.Errorf("%s: accepted", c.name)
		} else if !strings.Contains(err.Error(), c.field) {
			t.Errorf("%s: %q does not name %s", c.name, err, c.field)
		}
	}
}

// Going from one value to several is a new field ID: a field may not be
// single in one version of a Type and multiple in another.
func TestParseDir_SingleAndMultipleDisagree(t *testing.T) {
	v2 := strings.Replace(strings.Replace(multiSrc, "version: 1", "version: 2", 1),
		"{id: f_title,     type: string}", "{id: f_title,     type: string, multiple: true}", 1)
	err := parseSet(t, map[string]string{"card.v1.yaml": multiSrc, "card.v2.yaml": v2})
	if err == nil || !strings.Contains(err.Error(), "f_title") {
		t.Errorf("f_title single in v1 and multiple in v2: %v, want refused", err)
	}
	same := strings.Replace(multiSrc, "version: 1", "version: 2", 1)
	if err := parseSet(t, map[string]string{"card.v1.yaml": multiSrc, "card.v2.yaml": same}); err != nil {
		t.Errorf("two versions that agree: %v", err)
	}
}

// --- the demo Types ---

func TestDemoTypes_Jira22(t *testing.T) {
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

	ticket := version("ticket", 6)
	if s, ok := ticket.State("waiting_for_customer"); !ok || s.Category != "waiting" {
		t.Errorf("ticket@6: waiting_for_customer = %+v, want a waiting state", s)
	}
	back, ok := ticket.Transition("waiting_for_customer", "in_progress")
	if !ok || !slices.Equal(back.Allowed, []string{"role:support"}) || back.Guard != "f_assignee != null" {
		t.Errorf("ticket@6: waiting_for_customer -> in_progress = %+v, want support only, with an assignee", back)
	}
	reopen, ok := ticket.Reaction("reopen")
	if !ok || reopen.Kind != typedef.ReactionMove || reopen.On != typedef.OnCreatedInside || !slices.Equal(reopen.Of, []string{"comment"}) ||
		reopen.By != typedef.ByCreator || reopen.To != "in_progress" || !slices.Equal(reopen.From, []string{"waiting_for_customer"}) ||
		!strings.Contains(reopen.When, "child.f_public") {
		t.Errorf("ticket@6: reopen = %+v", reopen)
	}
	open, ok := ticket.Reaction("open_checklist")
	if !ok || open.Kind != typedef.ReactionCreate || open.State != "in_progress" || !slices.Equal(open.From, []string{"open"}) ||
		open.Type != "checklist" || open.Version != 1 || open.Fields["f_opened_at"] != "cause.at" {
		t.Errorf("ticket@6: open_checklist = %+v", open)
	}

	// The comment stays generic: one more field, no reaction, nothing listening.
	comment := version("comment", 2)
	if f, ok := comment.Field("f_public"); !ok || f.Type != typedef.Bool {
		t.Errorf("comment@2: f_public = %+v, want a bool", f)
	}
	if len(comment.Reactions) != 0 || len(comment.Hooks) != 0 {
		t.Errorf("comment@2 declares reactions or hooks; the rule belongs to the container")
	}
	version("comment", 1)

	checklist := version("checklist", 1)
	if !checklist.AcceptsCreate("ticket:open_checklist", []string{"f_title", "f_opened_at"}) {
		t.Errorf("checklist@1 does not accept ticket:open_checklist creating it with f_title and f_opened_at")
	}

	for _, id := range []string{"story", "bug", "task"} {
		ty := version(id, 3)
		if f, ok := ty.Field("f_labels"); !ok || f.Type != typedef.String || !f.Multiple || f.Max != 20 {
			t.Errorf("%s@3: f_labels = %+v, want a set of strings, at most 20", id, f)
		}
		if f, ok := ty.Field("f_reviewers"); !ok || f.Type != typedef.Principal || !f.Multiple {
			t.Errorf("%s@3: f_reviewers = %+v, want a set of principals", id, f)
		}
		if _, ok := ty.Field("f_resolved_at"); !ok {
			t.Errorf("%s@3 lost f_resolved_at", id)
		}
		if len(ty.Hooks) > 0 || len(ty.Reactions) > 0 {
			t.Errorf("%s@3 has hooks or reactions; a drag must stay one row and one event", id)
		}
		version(id, 2)
	}
}
