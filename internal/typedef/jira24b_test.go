// Tests for Jira 2.4b, PR 1 (building blocks): what a Type may newly
// declare, and the load rules, by kernel change number (the Phase A in
// docs/proposals/2026-11-21-jira-2-4b-phase-a.md, Part 2 wins).
//
// Test Types: a rack contains jars, a jar contains beans. Nothing is named
// after a request or an approval.
package typedef_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/typedef"
)

const rack4bSrc = `
id: rack
version: 1
fields:
  - {id: f_name,    type: string}
  - {id: f_quota,   type: int}
  - {id: f_boss,    type: principal}
  - {id: f_keepers, type: principal, multiple: true, max: 8}
  - {id: f_secret,  type: string}
readable_by_reactions: [f_name, f_quota, f_boss, f_keepers]
PLACEMENT
`

func rack4b(placement string) string { return strings.Replace(rack4bSrc, "PLACEMENT", placement, 1) }

const jar4bSrc = `
id: jar
version: 1
initial_state: draft
fields:
  - {id: f_title,   type: string}
  - {id: f_needed,  type: int,       writable_by: kernel}
  - {id: f_owner,   type: principal, writable_by: kernel}
  - {id: f_keepers, type: principal, multiple: true, max: 8, writable_by: kernel}
states:
  - {id: draft,    category: triage}
  - {id: open,     category: active}
  - {id: full,     category: done}
  - {id: spilled,  category: canceled}
  - {id: archived, category: done}
transitions:
  - from: draft
    to: open
    sets: {f_needed: 'container.f_quota', f_owner: 'container.f_boss', f_keepers: 'container.f_keepers'}
  - {from: open, to: full,    guard: 'children.bean.filter(c, c.state == "yes").size() >= f_needed'}
  - {from: open, to: spilled, guard: 'children.bean.exists(c, c.state == "no")'}
  - {from: full, to: archived, allowed: [nobody]}
reactions:
  - {id: make_beans, kind: create, on: enter, state: open, type: bean, version: 1, for_each: f_keepers,
     fields: {f_holder: 'each', f_label: 'container.f_name'}}
accepts_reactions:
  'bean:tip':      {moves: [full]}
  'bean:tip_loud': {moves: [full]}
`

const bean4bSrc = `
id: bean
version: 1
initial_state: waiting
fields:
  - {id: f_holder, type: principal, writable_by: kernel, unique_among_siblings: [waiting, yes]}
  - {id: f_next,   type: principal}
  - {id: f_label,  type: string, writable_by: kernel}
states:
  - {id: waiting, category: active}
  - {id: yes,     category: done}
  - {id: no,      category: done}
transitions:
  - {from: waiting, to: yes, allowed: ['field:f_holder'], except: [container_creator], guard: 'container.state == "open"'}
  - {from: waiting, to: no,  allowed: ['field:f_holder'], except: [container_creator], guard: 'container.state == "open"'}
  - {from: waiting, to: waiting, allowed: ['field:f_holder'], required: [f_next], sets: {f_holder: 'f_next'}, guard: 'f_next != f_holder'}
reactions:
  - {id: tip,      kind: move, on: enter, state: yes, target: container, to: full, target_from: [open], on_guard: skip}
  - {id: tip_loud, kind: move, on: enter, state: yes, target: container, to: full, target_from: [open]}
accepts_reactions:
  'jar:make_beans': {creates: [f_holder, f_label]}
placement_writable_by: [kernel]
`

func jar4bFamily() map[string]string {
	return map[string]string{"rack.yaml": rack4b("placement_writable_by: ['role:rack_admin']"), "jar.yaml": jar4bSrc, "bean.yaml": bean4bSrc}
}

func TestJira24b_TheTestFamilyLoads(t *testing.T) {
	if err := parseSet(t, jar4bFamily()); err != nil {
		t.Fatalf("rack, jar and bean: %v", err)
	}
	b := mustParse(t, bean4bSrc)
	for _, ty := range []*typedef.Type{b, roundTrip(t, b)} {
		f, _ := ty.Field("f_holder")
		if !slices.Equal(f.UniqueAmongSiblings, []string{"waiting", "yes"}) || !slices.Equal(ty.PlacementWritableBy, []string{"kernel"}) {
			t.Errorf("bean: unique %v, placement %v", f.UniqueAmongSiblings, ty.PlacementWritableBy)
		}
	}
}

// --- 1. container.<field> in sets and create reactions ---

func TestJira24b_1_ContainerFieldReads(t *testing.T) {
	j := mustParse(t, jar4bSrc)
	if tr, ok := transitionOf(j, "draft", "open"); !ok || tr.Sets["f_needed"] != "container.f_quota" {
		t.Errorf("draft -> open sets = %+v", tr.Sets)
	}
	// Across the set: some loaded Type must let reactions read the field.
	files := jar4bFamily()
	files["jar.yaml"] = strings.Replace(jar4bSrc, "'container.f_quota'", "'container.f_secret'", 1)
	mustRefuseSet(t, "a container field no loaded Type lets reactions read", files, "f_secret")
	files = jar4bFamily()
	files["jar.yaml"] = strings.Replace(jar4bSrc, "'container.f_name'", "'container.f_secret'", 1)
	mustRefuseSet(t, "a create reaction reading a container field nobody lets it read", files, "f_secret")
	// Only fields: container.state is a guard's, not an effect's.
	mustRefuse(t, "an effect reading the container's state",
		strings.Replace(jar4bSrc, "'container.f_quota'", "'container.state'", 1), "container.state")
}

// --- 2. a principal copied by plain reference ---

func TestJira24b_2_APrincipalByPlainReferenceOnly(t *testing.T) {
	mustParse(t, jar4bSrc)  // f_owner from container.f_boss, f_keepers (a set) from container.f_keepers
	mustParse(t, rack4bSrc) // readable_by_reactions now takes principal fields
	cases := []struct{ name, src, mention string }{
		{"a computed principal", strings.Replace(jar4bSrc, "f_owner: 'container.f_boss'", "f_owner: 'container.f_boss == null ? container.f_boss : container.f_boss'", 1), "f_owner"},
		{"a literal principal", strings.Replace(jar4bSrc, "f_owner: 'container.f_boss'", `f_owner: '"user:ana"'`, 1), "f_owner"},
		{"a set into one", strings.Replace(jar4bSrc, "f_owner: 'container.f_boss'", "f_owner: 'container.f_keepers'", 1), "f_owner"},
		{"one into a set", strings.Replace(jar4bSrc, "f_keepers: 'container.f_keepers'", "f_keepers: 'container.f_boss'", 1), "f_keepers"},
		{"a string into a principal", strings.Replace(jar4bSrc, "f_owner: 'container.f_boss'", "f_owner: 'f_title'", 1), "f_owner"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { mustRefuse(t, c.name, c.src, c.mention) })
	}
	// A create reaction's principal is `each` or a plain reference.
	files := jar4bFamily()
	files["jar.yaml"] = strings.Replace(jar4bSrc, "f_holder: 'each'", `f_holder: 'each == "" ? each : each'`, 1)
	mustRefuseSet(t, "a create reaction computing a principal", files, "f_holder")
}

// --- 3. for_each ---

func TestJira24b_3_ForEach(t *testing.T) {
	j := mustParse(t, jar4bSrc)
	if r, ok := reactionOf(j, "make_beans"); !ok || r.ForEach != "f_keepers" || r.Fields["f_holder"] != "each" {
		t.Errorf("make_beans = %+v", r)
	}
	cases := []struct{ name, src, mention string }{
		{"for_each on a single-value field", strings.Replace(jar4bSrc, "for_each: f_keepers", "for_each: f_owner", 1), "f_owner"},
		{"for_each on an undeclared field", strings.Replace(jar4bSrc, "for_each: f_keepers", "for_each: f_nope", 1), "f_nope"},
		{"each without for_each", strings.Replace(jar4bSrc, "for_each: f_keepers,", "", 1), "each"},
		{"for_each on a move reaction", strings.Replace(bean4bSrc, "on_guard: skip}", "on_guard: skip, for_each: f_holder}", 1), "for_each"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { mustRefuse(t, c.name, c.src, c.mention) })
	}
}

// --- 4. guards read the container and the children ---

func TestJira24b_4_GuardsReadContainerAndChildren(t *testing.T) {
	mustParse(t, jar4bSrc)
	mustParse(t, bean4bSrc)
	cases := []struct{ name, src, mention string }{
		// state and category only: a guard judges where things stand, not
		// another Item's fields.
		{"a container field", strings.Replace(bean4bSrc, `container.state == "open"`, `container.f_title == "x"`, 1), "f_title"},
		{"a child's field", strings.Replace(jar4bSrc, `c.state == "yes"`, `c.f_holder == "x"`, 1), "f_holder"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { mustRefuse(t, c.name, c.src, c.mention) })
	}
	// children.<type> names a loaded Type.
	files := jar4bFamily()
	files["jar.yaml"] = strings.Replace(jar4bSrc, "children.bean.filter", "children.pea.filter", 1)
	mustRefuseSet(t, "children of a Type that is not loaded", files, "pea")
}

// --- 5. a move reaction aimed at the container ---

func TestJira24b_5_AMoveReactionAtTheContainer(t *testing.T) {
	b := mustParse(t, bean4bSrc)
	if r, ok := reactionOf(b, "tip"); !ok || r.Target != typedef.TargetContainer || r.To != "full" {
		t.Errorf("tip = %+v", r)
	}
	mustRefuse(t, "target container with a link", strings.Replace(bean4bSrc, "target: container, to: full, target_from: [open], on_guard",
		"target: container, link: nope, to: full, target_from: [open], on_guard", 1), "target")
	mustRefuse(t, "target container on a create reaction", strings.Replace(jar4bSrc, "for_each: f_keepers,", "for_each: f_keepers, target: container,", 1), "target")
	// The container's Type, when loaded, consents.
	files := jar4bFamily()
	files["jar.yaml"] = strings.Replace(jar4bSrc, "  'bean:tip':      {moves: [full]}\n", "", 1)
	mustRefuseSet(t, "a container Type that does not consent", files, "tip")
}

// --- 6. on_guard: skip ---

func TestJira24b_6_OnGuard(t *testing.T) {
	b := mustParse(t, bean4bSrc)
	if r, _ := reactionOf(b, "tip"); r.OnGuard != typedef.OnGuardSkip {
		t.Errorf("tip on_guard = %q", r.OnGuard)
	}
	mustRefuse(t, "an unknown on_guard", strings.Replace(bean4bSrc, "on_guard: skip", "on_guard: ignore", 1), "ignore")
	mustRefuse(t, "on_guard on a create reaction", strings.Replace(jar4bSrc, "for_each: f_keepers,", "for_each: f_keepers, on_guard: skip,", 1), "on_guard")
}

// --- 7. container_creator ---

func TestJira24b_7_ContainerCreator(t *testing.T) {
	mustParse(t, bean4bSrc)
	// In except only: allowed does not name who created another Item.
	mustRefuse(t, "container_creator in allowed",
		strings.Replace(bean4bSrc, "allowed: ['field:f_holder'], except: [container_creator], guard: 'container.state == \"open\"'}\n  - {from: waiting, to: no",
			"allowed: [container_creator], guard: 'container.state == \"open\"'}\n  - {from: waiting, to: no", 1), "container_creator")
}

// --- 9. unique_among_siblings ---

func TestJira24b_9_UniqueAmongSiblings(t *testing.T) {
	cases := []struct{ name, from, to, mention string }{
		{"on a string field", "{id: f_label,  type: string, writable_by: kernel}", "{id: f_label,  type: string, writable_by: kernel, unique_among_siblings: [waiting]}", "f_label"},
		{"an unknown state", "unique_among_siblings: [waiting, yes]", "unique_among_siblings: [waiting, maybe]", "maybe"},
		{"no state", "unique_among_siblings: [waiting, yes]", "unique_among_siblings: []", "unique_among_siblings"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { mustRefuse(t, c.name, strings.Replace(bean4bSrc, c.from, c.to, 1), c.mention) })
	}
}

// --- 10. placement_writable_by ---

func TestJira24b_10_PlacementWritableBy(t *testing.T) {
	if r := mustParse(t, rack4b("placement_writable_by: ['role:rack_admin', 'role:lead']")); !slices.Equal(r.PlacementWritableBy, []string{"role:rack_admin", "role:lead"}) {
		t.Errorf("rack placement = %v", r.PlacementWritableBy)
	}
	cases := []struct{ name, placement, mention string }{
		{"kernel with a role", "placement_writable_by: [kernel, 'role:x']", "kernel"},
		{"a principal", "placement_writable_by: ['principal:user:ana']", "principal:user:ana"},
		{"a malformed role", "placement_writable_by: ['role:']", "role:"},
		{"empty", "placement_writable_by: []", "placement_writable_by"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { mustRefuse(t, c.name, rack4b(c.placement), c.mention) })
	}
}

// --- 11. a hand-over: sets writes a field its own allowed names ---

func TestJira24b_11_HandOver(t *testing.T) {
	b := mustParse(t, bean4bSrc)
	tr, ok := transitionOf(b, "waiting", "waiting")
	if !ok || tr.Sets["f_holder"] != "f_next" {
		t.Errorf("waiting -> waiting = %+v", tr)
	}
	// Only by plain reference: a computed value is still refused there.
	mustRefuse(t, "a computed hand-over", strings.Replace(bean4bSrc, "sets: {f_holder: 'f_next'}", "sets: {f_holder: 'f_next == null ? f_holder : f_next'}", 1), "f_holder")
}

// --- 12. nobody ---

func TestJira24b_12_Nobody(t *testing.T) {
	j := mustParse(t, jar4bSrc)
	if tr, _ := transitionOf(j, "full", "archived"); !slices.Equal(tr.Allowed, []string{typedef.AllowedNobody}) {
		t.Errorf("full -> archived allowed = %v", tr.Allowed)
	}
	mustRefuse(t, "nobody beside another entry", strings.Replace(jar4bSrc, "allowed: [nobody]", "allowed: [nobody, 'role:x']", 1), "nobody")
	mustRefuse(t, "nobody in except", strings.Replace(jar4bSrc, "allowed: [nobody]", "except: [nobody]", 1), "nobody")
}

// --- desk@3, sla_goal@2, sla_policy@2 ---

func TestJira24b_TypeFiles(t *testing.T) {
	types, err := typedef.ParseDir("../../types")
	if err != nil {
		t.Fatalf("ParseDir: %v", err)
	}
	find := func(id string, v int) *typedef.Type {
		for _, ty := range types {
			if ty.ID == id && ty.Version == v {
				return ty
			}
		}
		t.Fatalf("types/ has no %s@%d", id, v)
		return nil
	}
	desk2, desk3 := find("desk", 2), find("desk", 3)
	if !slices.Equal(desk3.PlacementWritableBy, []string{"role:sla_admin"}) || len(desk2.PlacementWritableBy) != 0 {
		t.Errorf("desk@3 placement %v (want [role:sla_admin]); desk@2 %v (want none)", desk3.PlacementWritableBy, desk2.PlacementWritableBy)
	}
	if !slices.Equal(desk3.Own, desk2.Own) || !slices.Equal(desk3.CustomerFields, desk2.CustomerFields) || len(desk3.Links) != len(desk2.Links) {
		t.Error("desk@3 is desk@2 plus who places it")
	}
	pol := find("sla_policy", 2)
	if f, ok := pol.Field("f_approval_s"); !ok || f.Type != typedef.Int || !slices.Equal(f.WritableByRoles, []string{"sla_admin"}) {
		t.Errorf("sla_policy@2 f_approval_s = %+v, %v", f, ok)
	}
	goal := find("sla_goal", 2)
	if f, _ := goal.Field("f_metric"); !slices.Contains(f.Options, "approval") || !slices.Contains(f.Options, "resolution") {
		t.Errorf("sla_goal@2 metrics = %v", f.Options)
	}
	for _, tr := range goal.Transitions {
		if !slices.Equal(tr.Allowed, []string{typedef.AllowedNobody}) {
			t.Errorf("sla_goal@2 %s -> %s allowed %v; want [nobody]: no person moves a goal", tr.From, tr.To, tr.Allowed)
		}
	}
	if !slices.ContainsFunc(goal.Invariants, func(i typedef.Invariant) bool { return strings.Contains(i.Rule, "f_metric != null") }) {
		t.Error("sla_goal@2 has no invariant refusing a goal without a metric")
	}
	for _, id := range []string{"show_approval", "hide_approval", "clear_approval", "cancel_approval"} {
		if r, ok := reactionOf(goal, id); !ok || r.Target != typedef.TargetContainer || r.Set["f_due_approval"] == "" {
			t.Errorf("sla_goal@2 %s = %+v", id, r)
		}
	}
	// @1 is untouched.
	if g1 := find("sla_goal", 1); slices.ContainsFunc(g1.Transitions, func(tr typedef.Transition) bool { return len(tr.Allowed) > 0 }) {
		t.Error("sla_goal@1 changed")
	}
}
