// Tests for lifecycle.Fits (track 1, item 6, step 2): whether an Item on
// one version of a Type fits a newer one, decided from both versions and
// the Item's facts alone.
//
// Fits reports every misfit, in a fixed order, each naming what the Type
// declares and never a value:
//
//   - closed: the Item's state is done or canceled in its own version.
//   - state_missing: the new version has no state of that ID.
//   - field_missing: a field that holds a value is not in the new version.
//   - field_changed: a field that holds a value has another type, holds
//     one value where it held several (or the reverse), or holds an enum
//     value the new version no longer offers.
//   - invariant_failed: an invariant of the new version does not hold.
//   - link_changed: a link the Item has is not declared in the new version
//     with the same inverse, cardinality and target Types.
//   - container_not_admitted: the new version's start.in does not admit
//     where the Item is.
//   - claim_empty: the state has a claim in the new version and the claim
//     field holds nobody.
//   - path_not_declared: a move the Item made is not a move the new version
//     declares.
//   - path_missing_fields: the fields the new version requires for the
//     Item's last move are not set now.
//
// Guards of past moves are not run again, and who made them is not judged.
// A field that holds no value, writable_by, frozen and requester_only are
// not checked: a migrate writes no field.
package lifecycle_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/lifecycle"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

func migType(t *testing.T, src string) *typedef.Type {
	t.Helper()
	ty, err := typedef.Parse([]byte(src))
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, src)
	}
	return ty
}

// migBase is a request: draft, review, approved, rejected.
const migBase = `
id: req
version: %VERSION%
initial_state: draft
fields:
  - {id: f_amount, type: int}
  - {id: f_note, type: string}
  - {id: f_kind, type: enum, options: [small, large]}
  - {id: f_tags, type: string, multiple: true, max: 5}
  - {id: f_owner, type: principal}
states:
  - {id: draft, category: todo}
  - {id: review, category: active}
  - {id: approved, category: done}
  - {id: rejected, category: canceled}
transitions:
  - {from: draft, to: review}
  - {from: review, to: approved}
  - {from: review, to: rejected}
`

func migV(version string, edits ...string) string {
	src := strings.Replace(migBase, "%VERSION%", version, 1)
	for i := 0; i+1 < len(edits); i += 2 {
		if !strings.Contains(src, edits[i]) {
			panic("test bug: the base Type has no " + edits[i])
		}
		src = strings.Replace(src, edits[i], edits[i+1], 1)
	}
	return src
}

// inReview is an Item in review that came from draft.
func inReview(fields map[string]any) lifecycle.MigrationFacts {
	return lifecycle.MigrationFacts{State: "review", Fields: fields, Top: true, Path: []string{"draft", "review"}}
}

func checksOf(ms []lifecycle.Misfit) []lifecycle.MigrationCheck {
	var out []lifecycle.MigrationCheck
	for _, m := range ms {
		out = append(out, m.Check)
	}
	return out
}

// wantMisfit checks that ms holds exactly one misfit, and returns it.
func wantMisfit(t *testing.T, ms []lifecycle.Misfit, check lifecycle.MigrationCheck) lifecycle.Misfit {
	t.Helper()
	if len(ms) != 1 || ms[0].Check != check {
		t.Fatalf("misfits = %+v, want one %s", ms, check)
	}
	return ms[0]
}

// The same definition under a new version number fits, whatever the Item
// holds; so does a version that only adds.
func TestFits_AVersionThatOnlyAdds(t *testing.T) {
	v1 := migType(t, migV("1"))
	full := map[string]any{"f_amount": 6000, "f_note": "x", "f_kind": "large", "f_tags": []any{"a"}, "f_owner": "user:ana"}
	if ms := lifecycle.Fits(v1, migType(t, migV("2")), inReview(full)); len(ms) != 0 {
		t.Errorf("the same definition: %+v, want none", ms)
	}
	// The first bake-off's change: a state and moves added, a guard added.
	v2 := migType(t, migV("2",
		"  - {id: approved, category: done}", "  - {id: cfo_review, category: waiting}\n  - {id: approved, category: done}",
		"  - {from: review, to: approved}", "  - {from: review, to: approved, guard: 'f_amount <= 5000'}\n"+
			"  - {from: review, to: cfo_review, guard: 'f_amount > 5000'}\n  - {from: cfo_review, to: approved}",
		"  - {id: f_note, type: string}", "  - {id: f_note, type: string}\n  - {id: f_new, type: string}"))
	if ms := lifecycle.Fits(v1, v2, inReview(full)); len(ms) != 0 {
		t.Errorf("a version that adds a state, moves, a guard and a field: %+v, want none", ms)
	}
	// A guard of a past move is not run again: the Item is where it is.
	guarded := migType(t, migV("2", "  - {from: draft, to: review}", "  - {from: draft, to: review, guard: 'f_amount > 100000'}"))
	if ms := lifecycle.Fits(v1, guarded, inReview(full)); len(ms) != 0 {
		t.Errorf("a new guard on a move already made: %+v, want none (guards are not run again)", ms)
	}
}

// A closed Item stays where it is.
func TestFits_Closed(t *testing.T) {
	v1, v2 := migType(t, migV("1")), migType(t, migV("2"))
	for state, path := range map[string][]string{"approved": {"draft", "review", "approved"}, "rejected": {"draft", "review", "rejected"}} {
		m := wantMisfit(t, lifecycle.Fits(v1, v2, lifecycle.MigrationFacts{State: state, Top: true, Path: path}), lifecycle.MigrateClosed)
		if m.State != state {
			t.Errorf("closed names the state %q, want %q", m.State, state)
		}
	}
}

func TestFits_StateMissing(t *testing.T) {
	v1 := migType(t, migV("1"))
	v2 := migType(t, migV("2", "  - {id: review, category: active}", "  - {id: checking, category: active}",
		"  - {from: draft, to: review}", "  - {from: draft, to: checking}",
		"  - {from: review, to: approved}", "  - {from: checking, to: approved}",
		"  - {from: review, to: rejected}", "  - {from: checking, to: rejected}"))
	ms := lifecycle.Fits(v1, v2, inReview(nil))
	if len(ms) == 0 || ms[0].Check != lifecycle.StateMissing || ms[0].State != "review" {
		t.Fatalf("misfits = %+v, want state_missing for review first", ms)
	}
	// With no such state there is no path to judge in the new version:
	// the one problem says it.
	if len(ms) != 1 {
		t.Errorf("misfits = %+v, want state_missing alone", ms)
	}
}

func TestFits_Fields(t *testing.T) {
	v1 := migType(t, migV("1"))
	cases := []struct {
		name   string
		v2     string
		fields map[string]any
		check  lifecycle.MigrationCheck // "" for a fit
		field  string
	}{
		{"a field dropped that holds a value", migV("2", "  - {id: f_note, type: string}\n", ""),
			map[string]any{"f_note": "kept"}, lifecycle.FieldMissing, "f_note"},
		{"a field dropped that holds nothing", migV("2", "  - {id: f_note, type: string}\n", ""),
			map[string]any{"f_amount": 5}, "", ""},
		{"another type", migV("2", "{id: f_amount, type: int}", "{id: f_amount, type: string}"),
			map[string]any{"f_amount": 5}, lifecycle.FieldChanged, "f_amount"},
		{"another type, holding nothing", migV("2", "{id: f_amount, type: int}", "{id: f_amount, type: string}"),
			map[string]any{"f_note": "x"}, "", ""},
		{"one value where it held several", migV("2", "{id: f_tags, type: string, multiple: true, max: 5}", "{id: f_tags, type: string}"),
			map[string]any{"f_tags": []any{"a", "b"}}, lifecycle.FieldChanged, "f_tags"},
		{"several values where it held one", migV("2", "{id: f_note, type: string}", "{id: f_note, type: string, multiple: true}"),
			map[string]any{"f_note": "x"}, lifecycle.FieldChanged, "f_note"},
		{"more values than the new max", migV("2", "max: 5}", "max: 1}"),
			map[string]any{"f_tags": []any{"a", "b"}}, lifecycle.FieldChanged, "f_tags"},
		{"an enum value no longer offered", migV("2", "options: [small, large]", "options: [small, medium]"),
			map[string]any{"f_kind": "large"}, lifecycle.FieldChanged, "f_kind"},
		{"an enum value still offered", migV("2", "options: [small, large]", "options: [small, medium]"),
			map[string]any{"f_kind": "small"}, "", ""},
		{"a string becomes a principal, holding one", migV("2", "{id: f_note, type: string}", "{id: f_note, type: principal}"),
			map[string]any{"f_note": "user:ana"}, lifecycle.FieldChanged, "f_note"},
		// Who may write a field is not this check's: a migrate writes none.
		{"a field closed to everyone but the kernel", migV("2", "{id: f_note, type: string}", "{id: f_note, type: string, writable_by: ['role:nobody_has_it']}"),
			map[string]any{"f_note": "x"}, "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ms := lifecycle.Fits(v1, migType(t, c.v2), inReview(c.fields))
			if c.check == "" {
				if len(ms) != 0 {
					t.Errorf("misfits = %+v, want none", ms)
				}
				return
			}
			m := wantMisfit(t, ms, c.check)
			if !slices.Equal(m.Fields, []string{c.field}) {
				t.Errorf("the misfit names fields %v, want [%s]", m.Fields, c.field)
			}
		})
	}
	// Several fields of one kind are one misfit, the IDs sorted.
	v2 := migType(t, migV("2", "  - {id: f_note, type: string}\n", "", "  - {id: f_amount, type: int}\n", ""))
	m := wantMisfit(t, lifecycle.Fits(v1, v2, inReview(map[string]any{"f_note": "x", "f_amount": 1})), lifecycle.FieldMissing)
	if !slices.Equal(m.Fields, []string{"f_amount", "f_note"}) {
		t.Errorf("fields = %v, want [f_amount f_note]", m.Fields)
	}
}

func TestFits_Invariants(t *testing.T) {
	v1 := migType(t, migV("1"))
	v2 := migType(t, migV("2")+"invariants:\n  - {id: amount_capped, rule: 'f_amount == null || f_amount <= 5000'}\n"+
		"  - {id: note_given, rule: 'f_note != null'}\n")
	if ms := lifecycle.Fits(v1, v2, inReview(map[string]any{"f_amount": 100, "f_note": "x"})); len(ms) != 0 {
		t.Errorf("both invariants hold: %+v", ms)
	}
	ms := lifecycle.Fits(v1, v2, inReview(map[string]any{"f_amount": 6000}))
	if got := checksOf(ms); !slices.Equal(got, []lifecycle.MigrationCheck{lifecycle.MigrateInvariant, lifecycle.MigrateInvariant}) {
		t.Fatalf("misfits = %+v, want invariant_failed twice (every invariant that fails)", ms)
	}
	if ms[0].Invariant != "amount_capped" || ms[1].Invariant != "note_given" {
		t.Errorf("invariants named %q, %q; want amount_capped, note_given (the Type's order)", ms[0].Invariant, ms[1].Invariant)
	}
}

const migLinked = `
id: req
version: %VERSION%
initial_state: draft
states:
  - {id: draft, category: todo}
links:
  - {name: blocks, inverse: blocked_by, targets: [req, other]}
  - {name: parent, inverse: children, targets: [req], max: one}
`

func TestFits_Links(t *testing.T) {
	v := func(version string, edits ...string) *typedef.Type {
		src := strings.Replace(migLinked, "%VERSION%", version, 1)
		for i := 0; i+1 < len(edits); i += 2 {
			if !strings.Contains(src, edits[i]) {
				t.Fatalf("test bug: no %s", edits[i])
			}
			src = strings.Replace(src, edits[i], edits[i+1], 1)
		}
		return migType(t, src)
	}
	v1 := v("1")
	held := lifecycle.MigrationFacts{State: "draft", Top: true, Path: []string{"draft"},
		Links: []lifecycle.HeldLink{{Name: "blocks", TargetType: "other"}, {Name: "parent", TargetType: "req"}}}
	cases := []struct {
		name string
		v2   *typedef.Type
		link string // "" for a fit
	}{
		{"the same links", v("2"), ""},
		{"a link type dropped that the Item has", v("2", "  - {name: parent, inverse: children, targets: [req], max: one}\n", ""), "parent"},
		{"a target Type dropped that the Item links to", v("2", "targets: [req, other]", "targets: [req]"), "blocks"},
		{"a target Type dropped that the Item does not link to", v("2", "targets: [req, other]", "targets: [other]"), ""},
		{"max one where it was many", v("2", "{name: blocks, inverse: blocked_by, targets: [req, other]}", "{name: blocks, inverse: blocked_by, targets: [req, other], max: one}"), "blocks"},
		{"many where it was max one", v("2", ", max: one}", "}"), "parent"},
		{"inverse_max one where it was many", v("2", "{name: blocks, inverse: blocked_by, targets: [req, other]}", "{name: blocks, inverse: blocked_by, targets: [req, other], inverse_max: one}"), "blocks"},
		{"a link type added", v("2", "links:\n", "links:\n  - {name: about, inverse: mentioned_by, targets: [req]}\n"), ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ms := lifecycle.Fits(v1, c.v2, held)
			if c.link == "" {
				if len(ms) != 0 {
					t.Errorf("misfits = %+v, want none", ms)
				}
				return
			}
			if m := wantMisfit(t, ms, lifecycle.LinkChanged); m.LinkType != c.link {
				t.Errorf("link_changed names %q, want %q", m.LinkType, c.link)
			}
		})
	}
	// An Item with no link of a type is not held back by that type's change.
	none := lifecycle.MigrationFacts{State: "draft", Top: true, Path: []string{"draft"}}
	if ms := lifecycle.Fits(v1, v("2", "  - {name: parent, inverse: children, targets: [req], max: one}\n", ""), none); len(ms) != 0 {
		t.Errorf("an Item with no links, a link type dropped: %+v, want none", ms)
	}
}

func TestFits_Container(t *testing.T) {
	v1 := migType(t, migV("1"))
	inDesk := migType(t, migV("2")+"start:\n  in: [desk]\n")
	deskOrTop := migType(t, migV("2")+"start:\n  in: [desk, top]\n")
	at := func(top bool, container string) lifecycle.MigrationFacts {
		f := inReview(nil)
		f.Top, f.ContainerType = top, container
		return f
	}
	cases := []struct {
		name string
		v2   *typedef.Type
		f    lifecycle.MigrationFacts
		fits bool
	}{
		{"in a desk, in: [desk]", inDesk, at(false, "desk"), true},
		{"in a hall, in: [desk]", inDesk, at(false, "hall"), false},
		{"at the top level, in: [desk]", inDesk, at(true, ""), false},
		{"at the top level, in: [desk, top]", deskOrTop, at(true, ""), true},
		{"in a hall, no start block", migType(t, migV("2")), at(false, "hall"), true},
		// allowed is about who creates, not where an Item may be.
		{"in a hall, a block with allowed only", migType(t, migV("2")+"start:\n  allowed: [nobody]\n"), at(false, "hall"), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ms := lifecycle.Fits(v1, c.v2, c.f)
			if c.fits {
				if len(ms) != 0 {
					t.Errorf("misfits = %+v, want none", ms)
				}
				return
			}
			wantMisfit(t, ms, lifecycle.ContainerNotAdmited)
		})
	}
}

// A state that gains a claim: an Item there with nobody in the claim
// field would be stuck (an empty claim lets nobody through).
func TestFits_Claim(t *testing.T) {
	v1 := migType(t, migV("1"))
	v2 := migType(t, migV("2", "  - {id: review, category: active}", "  - {id: review, category: active, claim: f_owner}",
		"  - {from: review, to: rejected}", "  - {from: review, to: rejected}\n  - {from: review, to: draft, release: true, clears: [f_owner]}"))
	m := wantMisfit(t, lifecycle.Fits(v1, v2, inReview(map[string]any{"f_amount": 1})), lifecycle.ClaimEmpty)
	if m.State != "review" || !slices.Equal(m.Fields, []string{"f_owner"}) {
		t.Errorf("claim_empty names state %q, fields %v; want review, [f_owner]", m.State, m.Fields)
	}
	if ms := lifecycle.Fits(v1, v2, inReview(map[string]any{"f_owner": "user:fay"})); len(ms) != 0 {
		t.Errorf("a claimant is named: %+v, want none", ms)
	}
	// In a state the new version does not claim, the field may be empty.
	draft := lifecycle.MigrationFacts{State: "draft", Top: true, Path: []string{"draft"}}
	if ms := lifecycle.Fits(v1, v2, draft); len(ms) != 0 {
		t.Errorf("in draft, which has no claim: %+v, want none", ms)
	}
}

// The path: every move the Item made is a move the new version declares,
// and what the new version requires for the last one is set now.
func TestFits_Path(t *testing.T) {
	v1 := migType(t, migV("1"))
	// The new version puts a check between draft and review.
	between := migType(t, migV("2", "  - {id: review, category: active}", "  - {id: check, category: active}\n  - {id: review, category: active}",
		"  - {from: draft, to: review}", "  - {from: draft, to: check}\n  - {from: check, to: review}"))
	m := wantMisfit(t, lifecycle.Fits(v1, between, inReview(nil)), lifecycle.PathNotDeclared)
	if m.State != "draft" || m.ToState != "review" {
		t.Errorf("path_not_declared names %q -> %q, want draft -> review", m.State, m.ToState)
	}
	// An Item still in draft made no move: it fits, and will meet the check.
	if ms := lifecycle.Fits(v1, between, lifecycle.MigrationFacts{State: "draft", Top: true, Path: []string{"draft"}}); len(ms) != 0 {
		t.Errorf("an Item still in draft: %+v, want none", ms)
	}

	// A longer path, with a loop: each step is judged, each undeclared one
	// reported once.
	loop := migType(t, migV("1", "  - {from: review, to: rejected}", "  - {from: review, to: rejected}\n  - {from: review, to: draft}"))
	noBack := migType(t, migV("2"))
	again := lifecycle.MigrationFacts{State: "review", Top: true, Path: []string{"draft", "review", "draft", "review", "draft", "review"}}
	m = wantMisfit(t, lifecycle.Fits(loop, noBack, again), lifecycle.PathNotDeclared)
	if m.State != "review" || m.ToState != "draft" {
		t.Errorf("path_not_declared names %q -> %q, want review -> draft, once", m.State, m.ToState)
	}

	// The first state: an Item that started where the new version does
	// not start is not on a path the new version declares.
	startsInReview := migType(t, migV("2", "initial_state: draft", "initial_state: review"))
	m = wantMisfit(t, lifecycle.Fits(v1, startsInReview, lifecycle.MigrationFacts{State: "draft", Top: true, Path: []string{"draft"}}), lifecycle.PathNotDeclared)
	if m.State != "" || m.ToState != "draft" {
		t.Errorf("path_not_declared for the first state names %q -> %q, want \"\" -> draft", m.State, m.ToState)
	}

	// The last move's required fields, as the new version declares them.
	requires := migType(t, migV("2", "  - {from: draft, to: review}", "  - {from: draft, to: review, required: [f_note, f_amount]}"))
	m = wantMisfit(t, lifecycle.Fits(v1, requires, inReview(map[string]any{"f_amount": 5})), lifecycle.PathMissingFields)
	if m.State != "review" || !slices.Equal(m.Fields, []string{"f_note"}) {
		t.Errorf("path_missing_fields names state %q, fields %v; want review, [f_note]", m.State, m.Fields)
	}
	if ms := lifecycle.Fits(v1, requires, inReview(map[string]any{"f_amount": 5, "f_note": "x"})); len(ms) != 0 {
		t.Errorf("both required fields set: %+v, want none", ms)
	}
	// Only the last move: a field an earlier move requires may have been
	// cleared since.
	earlier := migType(t, migV("2", "  - {from: draft, to: review}", "  - {from: draft, to: review, required: [f_note]}",
		"  - {id: approved, category: done}", "  - {id: second, category: active}\n  - {id: approved, category: done}",
		"  - {from: review, to: approved}", "  - {from: review, to: approved}\n  - {from: review, to: second}"))
	v1b := migType(t, migV("1", "  - {id: approved, category: done}", "  - {id: second, category: active}\n  - {id: approved, category: done}",
		"  - {from: review, to: approved}", "  - {from: review, to: approved}\n  - {from: review, to: second}"))
	second := lifecycle.MigrationFacts{State: "second", Top: true, Path: []string{"draft", "review", "second"}}
	if ms := lifecycle.Fits(v1b, earlier, second); len(ms) != 0 {
		t.Errorf("a field an earlier move requires is not set: %+v, want none (only the last move is asked)", ms)
	}
}

// Every misfit is reported, in the documented order.
func TestFits_EveryMisfit_InOrder(t *testing.T) {
	v1 := migType(t, migV("1"))
	v2 := migType(t, migV("2",
		"  - {id: f_note, type: string}\n", "",
		"{id: f_amount, type: int}", "{id: f_amount, type: string}",
		"  - {id: review, category: active}", "  - {id: review, category: active, claim: f_owner}",
		"  - {from: draft, to: review}", "  - {from: draft, to: check}\n  - {from: check, to: review, required: [f_kind]}",
		"  - {id: draft, category: todo}", "  - {id: draft, category: todo}\n  - {id: check, category: active}",
		"  - {from: review, to: rejected}", "  - {from: review, to: rejected}\n  - {from: review, to: draft, release: true, clears: [f_owner]}")+
		"invariants:\n  - {id: kind_given, rule: 'f_kind != null'}\nstart:\n  in: [desk]\n")
	ms := lifecycle.Fits(v1, v2, inReview(map[string]any{"f_note": "x", "f_amount": 5}))
	want := []lifecycle.MigrationCheck{lifecycle.FieldMissing, lifecycle.FieldChanged, lifecycle.MigrateInvariant,
		lifecycle.ContainerNotAdmited, lifecycle.ClaimEmpty, lifecycle.PathNotDeclared}
	if got := checksOf(ms); !slices.Equal(got, want) {
		t.Errorf("misfits = %v\nwant     %v", got, want)
	}
}
