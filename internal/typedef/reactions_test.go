// Tests for proof-of-concept Step 2, part 2: the Type side of link
// reactions, invariants, role-written fields and kernel-set links.
//
// One Type (Parse) checks its own declarations: a reaction's trigger, link,
// expressions and undo; invariants; writable_by roles; a link's
// writable_by and resolve; the shape of accepts_reactions. A set of Types
// loaded together (ParseDir, CheckReactions) checks what spans Types: each
// reaction's target accepts it for exactly the int fields it writes, each
// accepted reaction exists and follows a link to the accepting Type, and a
// resolve names a principal field and a state of its target.
package typedef_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// pledge and jar: paying a pledge takes its amount from the jar it links
// to (held), dropping a paid pledge gives it back.
const pledgeSrc = `
id: pledge
version: 1
initial_state: open
fields:
  - {id: f_amount, type: int}
  - {id: f_note,   type: string}
states:
  - {id: open,    category: todo}
  - {id: filed,   category: active}
  - {id: paid,    category: done}
  - {id: dropped, category: canceled}
transitions:
  - {from: open,  to: filed}
  - {from: filed, to: paid}
  - {from: filed, to: dropped, escape: true}
  - {from: paid,  to: dropped}
links:
  - {name: jar, inverse: pledges, targets: [jar], max: one, writable_by: kernel,
     resolve: {on_enter: filed, field: f_holder, equals: creator, state: active}}
reactions:
  - {id: take, kind: held, on: enter, state: paid, from: [filed], link: jar, add: {f_level: f_amount}}
  - {id: give_back, kind: after, on: enter, state: dropped, from: [paid], undoes: take}
`

const jarSrc = `
id: jar
version: 1
initial_state: active
fields:
  - {id: f_holder, type: principal, writable_by: ['role:jar_admin']}
  - {id: f_cap,    type: int,       writable_by: ['role:jar_admin', 'role:owner']}
  - {id: f_level,  type: int,       writable_by: kernel}
  - {id: f_label,  type: string}
states:
  - {id: active,  category: active}
  - {id: retired, category: done}
transitions:
  - {from: active, to: retired}
invariants:
  - {id: no_cap,   rule: 'f_cap != null'}
  - {id: over_cap, rule: 'f_level == null || f_level <= f_cap'}
accepts_reactions:
  pledge:take:      [f_level]
  pledge:give_back: [f_level]
`

// swap replaces old (which must occur) with new in src.
func swap(t *testing.T, src, old, new string) string {
	t.Helper()
	if !strings.Contains(src, old) {
		t.Fatalf("test bug: %q is not in the source", old)
	}
	return strings.Replace(src, old, new, 1)
}

const (
	takeLine = "  - {id: take, kind: held, on: enter, state: paid, from: [filed], link: jar, add: {f_level: f_amount}}"
	giveLine = "  - {id: give_back, kind: after, on: enter, state: dropped, from: [paid], undoes: take}"
	jarLink  = `  - {name: jar, inverse: pledges, targets: [jar], max: one, writable_by: kernel,
     resolve: {on_enter: filed, field: f_holder, equals: creator, state: active}}`
)

func TestReactions_Load(t *testing.T) {
	ty := mustParse(t, pledgeSrc)
	take, ok := ty.Reaction("take")
	if !ok || take.Kind != typedef.ReactionHeld || take.State != "paid" || !slices.Equal(take.From, []string{"filed"}) ||
		take.Link != "jar" || take.Add["f_level"] != "f_amount" {
		t.Errorf("take = %+v", take)
	}
	if !slices.Equal(take.Reads, []string{"f_amount"}) {
		t.Errorf("take reads %v, want [f_amount]: the fields its expressions read are frozen during its hold", take.Reads)
	}
	if take.Programs["f_level"] == nil {
		t.Error("take's add expression is not compiled")
	}
	give, ok := ty.Reaction("give_back")
	if !ok || give.Kind != typedef.ReactionAfter || give.Undoes != "take" {
		t.Errorf("give_back = %+v", give)
	}
	l, _ := ty.Link("jar")
	if l.WritableBy != typedef.WritableByKernel || l.Resolve == nil ||
		*l.Resolve != (typedef.LinkResolve{OnEnter: "filed", Field: "f_holder", Equals: "creator", State: "active"}) {
		t.Errorf("link jar = %+v, resolve %+v", l, l.Resolve)
	}

	jar := mustParse(t, jarSrc)
	if f, _ := jar.Field("f_holder"); !slices.Equal(f.WritableByRoles, []string{"jar_admin"}) || f.WritableBy != typedef.WritableByAnyone {
		t.Errorf("f_holder: writable_by %q, roles %v; want roles [jar_admin]", f.WritableBy, f.WritableByRoles)
	}
	if f, _ := jar.Field("f_cap"); !slices.Equal(f.WritableByRoles, []string{"jar_admin", "owner"}) {
		t.Errorf("f_cap roles = %v, want [jar_admin owner]", f.WritableByRoles)
	}
	if got := jar.RoleWritten([]string{"f_label", "f_holder", "f_level", "f_cap"}); !slices.Equal(got, []string{"f_cap", "f_holder"}) {
		t.Errorf("RoleWritten = %v, want [f_cap f_holder]", got)
	}
	if len(jar.Invariants) != 2 || jar.Invariants[0].ID != "no_cap" || jar.Invariants[1].Program == nil {
		t.Errorf("invariants = %+v, want no_cap then over_cap, compiled", jar.Invariants)
	}
	if !jar.Accepts("pledge:take", []string{"f_level"}) || !jar.Accepts("pledge:give_back", []string{"f_level"}) {
		t.Error("jar does not accept the pledge's reactions on f_level")
	}
	if jar.Accepts("pledge:take", []string{"f_level", "f_label"}) || jar.Accepts("pledge:steal", []string{"f_level"}) ||
		jar.Accepts("other:take", []string{"f_level"}) {
		t.Error("jar accepts more than it declares")
	}
}

// The Types are stored as JSON and read back with FromJSON: everything new
// survives, and the expressions are compiled again.
func TestReactions_JSONRoundTrip(t *testing.T) {
	for _, src := range []string{pledgeSrc, jarSrc} {
		ty := mustParse(t, src)
		data, err := json.Marshal(ty)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		back, err := typedef.FromJSON(data)
		if err != nil {
			t.Fatalf("FromJSON %s: %v", ty.ID, err)
		}
		again, _ := json.Marshal(back)
		if string(again) != string(data) {
			t.Errorf("%s changed in the round trip:\n%s\n%s", ty.ID, data, again)
		}
		if ty.ID == "pledge" {
			if take, _ := back.Reaction("take"); take.Programs["f_level"] == nil || len(take.Reads) != 1 {
				t.Errorf("after the round trip, take = %+v", take)
			}
		} else if back.Invariants[0].Program == nil {
			t.Error("after the round trip, invariants are not compiled")
		}
	}
}

func TestReactions_Refused(t *testing.T) {
	// A valid check hook (it loads on its own, on open -> filed).
	hook := "\nhooks:\n  - {id: HOOKID, on: transition, from: FROM, to: TO, kind: check, action: erp.check_credit, reads: [f_note, f_amount], writes: [f_note]}\n"
	withHook := func(id, from, to string) string {
		h := strings.NewReplacer("HOOKID", id, "FROM", from, "TO", to).Replace(hook)
		return pledgeSrc + h
	}
	cases := []struct{ name, src, mention string }{
		{"unknown kind", swap(t, pledgeSrc, "kind: held", "kind: maybe"), "maybe"},
		{"on transition", swap(t, pledgeSrc, "{id: take, kind: held, on: enter", "{id: take, kind: held, on: transition"), "take"},
		{"unknown state", swap(t, pledgeSrc, "state: paid, from: [filed]", "state: payed, from: [filed]"), "payed"},
		{"unknown from state", swap(t, pledgeSrc, "from: [filed], link", "from: [field], link"), "field"},
		{"held: from -> state is no transition", swap(t, pledgeSrc, "from: [filed], link", "from: [open], link"), "take"},
		{"held: no from", swap(t, pledgeSrc, "from: [filed], link", "link"), "take"},
		{"held: undeclared link", swap(t, pledgeSrc, "link: jar, add", "link: cup, add"), "cup"},
		{"held: link of many", swap(t, pledgeSrc, "max: one, writable_by", "max: many, writable_by"), "jar"},
		{"held: no add", swap(t, pledgeSrc, ", add: {f_level: f_amount}}", "}"), "take"},
		{"held: add does not compile", swap(t, pledgeSrc, "add: {f_level: f_amount}", "add: {f_level: 'f_amount +'}"), "take"},
		{"held: add is not an int", swap(t, pledgeSrc, "add: {f_level: f_amount}", "add: {f_level: f_note}"), "take"},
		{"held: add reads an undeclared field", swap(t, pledgeSrc, "add: {f_level: f_amount}", "add: {f_level: f_nope}"), "f_nope"},
		{"held: add reads today", swap(t, pledgeSrc, "add: {f_level: f_amount}", "add: {f_level: 'today == today ? 1 : 0'}"), "take"},
		{"held: add reads links", swap(t, pledgeSrc, "add: {f_level: f_amount}", "add: {f_level: 'size(links.jar)'}"), "take"},
		{"held: undoes something", swap(t, pledgeSrc, "add: {f_level: f_amount}}", "add: {f_level: f_amount}, undoes: give_back}"), "take"},
		{"held without an undo", swap(t, pledgeSrc, giveLine, ""), "take"},
		{"after with a free patch (Later)", swap(t, pledgeSrc, "undoes: take}", "add: {f_level: f_amount}}"), "give_back"},
		{"after undoing an unknown reaction", swap(t, pledgeSrc, "undoes: take}", "undoes: tak}"), "tak"},
		{"after undoing an after reaction", swap(t, pledgeSrc, giveLine, giveLine+
			"\n  - {id: give_more, kind: after, on: enter, state: dropped, from: [paid], undoes: give_back}"), "give_more"},
		{"two undos for one held reaction", swap(t, pledgeSrc, giveLine, giveLine+
			"\n  - {id: give_twice, kind: after, on: enter, state: open, from: [paid], undoes: take}"), "take"},
		{"after with a link", swap(t, pledgeSrc, "undoes: take}", "undoes: take, link: jar}"), "give_back"},
		{"duplicate reaction", swap(t, pledgeSrc, giveLine, giveLine+"\n"+takeLine), "take"},
		{"reaction id not a code", swap(t, swap(t, pledgeSrc, "{id: take,", "{id: Take!,"), "undoes: take}", "undoes: Take!}"), "Take!"},
		{"held reaction and check on one transition", withHook("check", "filed", "paid"), "filed -> paid"},
		{"reaction id is a hook id", withHook("take", "open", "filed"), `"take"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { mustRefuse(t, c.name, c.src, c.mention) })
	}
}

func TestInvariants_Refused(t *testing.T) {
	inv := "  - {id: over_cap, rule: 'f_level == null || f_level <= f_cap'}"
	cases := []struct{ name, src, mention string }{
		{"duplicate id", swap(t, jarSrc, inv, inv+"\n"+inv), "over_cap"},
		{"id not a code", swap(t, jarSrc, "{id: over_cap,", "{id: Over Cap,"), "Over Cap"},
		{"no rule", swap(t, jarSrc, "rule: 'f_level == null || f_level <= f_cap'", "rule: ''"), "over_cap"},
		{"does not compile", swap(t, jarSrc, "f_level <= f_cap'", "f_level <='"), "over_cap"},
		{"not a bool", swap(t, jarSrc, "rule: 'f_level == null || f_level <= f_cap'", "rule: 'f_level + 1'"), "over_cap"},
		{"undeclared field", swap(t, jarSrc, "f_level <= f_cap'", "f_level <= f_max'"), "over_cap"},
		{"reads today", swap(t, jarSrc, "rule: 'f_cap != null'", "rule: 'today == today'"), "no_cap"},
		{"reads links", swap(t, jarSrc, "rule: 'f_cap != null'", "rule: 'size(links.x) == 0'"), "no_cap"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { mustRefuse(t, c.name, c.src, c.mention) })
	}
}

func TestWritableByRoles_Refused(t *testing.T) {
	line := "  - {id: f_label,  type: string}"
	field := func(wb string) string {
		return swap(t, jarSrc, line, "  - {id: f_label,  type: string, writable_by: "+wb+"}")
	}
	cases := []struct{ name, src, mention string }{
		{"an empty role", field("['role:']"), "f_label"},
		{"not a role", field("['principal:user:ana']"), "f_label"},
		{"kernel in a list", field("[kernel, 'role:x']"), "f_label"},
		{"kernel alone in a list", field("[kernel]"), "f_label"},
		{"a bad role name", field("['role:Bad Name']"), "f_label"},
		{"an empty list", field("[]"), "f_label"},
		{"a duplicate role", field("['role:x', 'role:x']"), "f_label"},
		// The list form keeps the strictness of every other key: a typo in
		// a field is refused.
		{"an unknown key in a field", swap(t, jarSrc, line, "  - {id: f_label,  type: string, typo: 1}"), "typo"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { mustRefuse(t, c.name, c.src, c.mention) })
	}
}

func TestKernelLinks_Refused(t *testing.T) {
	link := func(l string) string { return swap(t, pledgeSrc, jarLink, l) }
	cases := []struct{ name, src, mention string }{
		{"writable_by kernel without a resolve", link(
			"  - {name: jar, inverse: pledges, targets: [jar], max: one, writable_by: kernel}"), "jar"},
		{"an unknown writable_by", link(
			"  - {name: jar, inverse: pledges, targets: [jar], max: one, writable_by: anyone,\n" +
				"     resolve: {on_enter: filed, field: f_holder, equals: creator, state: active}}"), "jar"},
		{"resolve on a link anyone writes", link(
			"  - {name: jar, inverse: pledges, targets: [jar], max: one,\n" +
				"     resolve: {on_enter: filed, field: f_holder, equals: creator, state: active}}"), "jar"},
		{"resolve on a link of many", swap(t, swap(t, pledgeSrc, "max: one, writable_by", "max: many, writable_by"),
			takeLine+"\n", ""), "jar"},
		{"resolve with two target Types", link(
			"  - {name: jar, inverse: pledges, targets: [jar, cup], max: one, writable_by: kernel,\n" +
				"     resolve: {on_enter: filed, field: f_holder, equals: creator, state: active}}"), "jar"},
		{"resolve on entering an unknown state", swap(t, pledgeSrc, "on_enter: filed", "on_enter: field"), "field"},
		{"resolve equals something else", swap(t, pledgeSrc, "equals: creator", "equals: approver"), "approver"},
		{"resolve without a field", swap(t, pledgeSrc, "field: f_holder, ", ""), "jar"},
		{"resolve without a state", swap(t, pledgeSrc, ", state: active}}", "}}"), "jar"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { mustRefuse(t, c.name, c.src, c.mention) })
	}
}

func TestAcceptsReactions_Refused(t *testing.T) {
	accepted := "  pledge:take:      [f_level]"
	cases := []struct{ name, src, mention string }{
		{"a key without a reaction", swap(t, jarSrc, accepted, "  pledge:      [f_level]"), "pledge"},
		{"a key with a version", swap(t, jarSrc, accepted, "  pledge@1:take:      [f_level]"), "pledge@1"},
		{"a key with too many parts", swap(t, jarSrc, accepted, "  pledge:take:x:      [f_level]"), "pledge:take:x"},
		{"an undeclared field", swap(t, jarSrc, accepted, "  pledge:take:      [f_lvl]"), "f_lvl"},
		// Reactions only add, and add is on ints.
		{"a field that is not an int", swap(t, jarSrc, accepted, "  pledge:take:      [f_label]"), "f_label"},
		{"no fields", swap(t, jarSrc, accepted, "  pledge:take:      []"), "pledge:take"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { mustRefuse(t, c.name, c.src, c.mention) })
	}
}

// --- across Types (ParseDir) ---

func writeTypes(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestParseDir_Reactions(t *testing.T) {
	if _, err := typedef.ParseDir(writeTypes(t, map[string]string{"pledge.v1.yaml": pledgeSrc, "jar.v1.yaml": jarSrc})); err != nil {
		t.Fatalf("pledge and jar: %v", err)
	}
	tin := `
id: tin
version: 1
fields:
  - {id: f_level, type: int, writable_by: kernel}
accepts_reactions:
  pledge:take: [f_level]
`
	jarAccepts := "  pledge:take:      [f_level]\n  pledge:give_back: [f_level]"
	cases := []struct {
		name    string
		pledge  string
		jar     string
		extra   map[string]string
		mention string
	}{
		{"the target does not accept the undo", pledgeSrc,
			swap(t, jarSrc, jarAccepts, "  pledge:take:      [f_level]"), nil, "give_back"},
		{"the target does not accept the held reaction", pledgeSrc,
			swap(t, jarSrc, jarAccepts, "  pledge:give_back: [f_level]"), nil, "take"},
		{"the reaction writes a field the target does not accept", swap(t, pledgeSrc, "add: {f_level: f_amount}",
			"add: {f_level: f_amount, f_cap: f_amount}"), jarSrc, nil, "f_cap"},
		{"the reaction writes a field the target does not declare", swap(t, pledgeSrc, "add: {f_level: f_amount}",
			"add: {f_level: f_amount, f_gone: f_amount}"), jarSrc, nil, "f_gone"},
		{"the target accepts an unknown reaction", pledgeSrc,
			swap(t, jarSrc, jarAccepts, jarAccepts+"\n  pledge:steal:     [f_level]"), nil, "pledge:steal"},
		{"the target accepts a reaction of an unknown Type", pledgeSrc,
			swap(t, jarSrc, jarAccepts, jarAccepts+"\n  bucket:take:      [f_level]"), nil, "bucket:take"},
		{"a Type accepts a reaction whose link does not point to it", pledgeSrc, jarSrc,
			map[string]string{"tin.v1.yaml": tin}, "tin"},
		{"resolve names a field the target does not declare", swap(t, pledgeSrc, "field: f_holder", "field: f_keeper"),
			jarSrc, nil, "f_keeper"},
		{"resolve names a field that is not a principal", swap(t, pledgeSrc, "field: f_holder", "field: f_label"),
			jarSrc, nil, "f_label"},
		{"resolve names a state the target does not declare", swap(t, pledgeSrc, "state: active}}", "state: full}}"),
			jarSrc, nil, "full"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			files := map[string]string{"pledge.v1.yaml": c.pledge, "jar.v1.yaml": c.jar}
			for k, v := range c.extra {
				files[k] = v
			}
			_, err := typedef.ParseDir(writeTypes(t, files))
			if err == nil {
				t.Fatalf("ParseDir accepted it, want refused")
			}
			if !strings.Contains(err.Error(), c.mention) {
				t.Errorf("error %q does not mention %q", err, c.mention)
			}
		})
	}
}

// The repository's Types: the vacation request v2 and the leave balance.
func TestRepositoryTypes_VacationV2AndLeaveBalance(t *testing.T) {
	types, err := typedef.ParseDir("../../types")
	if err != nil {
		t.Fatalf("ParseDir: %v", err)
	}
	byName := map[string]*typedef.Type{}
	for _, ty := range types {
		byName[fmt.Sprintf("%s@%d", ty.ID, ty.Version)] = ty
	}
	v2, lb := byName["vacation_request@2"], byName["leave_balance@1"]
	if v2 == nil || lb == nil {
		t.Fatalf("types/ lacks vacation_request v2 or leave_balance v1")
	}
	if deduct, ok := v2.Reaction("deduct"); !ok || deduct.Kind != typedef.ReactionHeld || deduct.Link != "balance" ||
		!slices.Equal(deduct.Reads, []string{"f_days"}) {
		t.Errorf("deduct = %+v", deduct)
	}
	if refund, ok := v2.Reaction("refund"); !ok || refund.Undoes != "deduct" {
		t.Errorf("refund = %+v", refund)
	}
	if tr, _ := v2.Transition("submitted", "withdrawn"); !tr.Escape {
		t.Error("submitted -> withdrawn is not an escape")
	}
	if tr, _ := v2.Transition("submitted", "draft"); tr.Escape {
		t.Error("recall is an escape; it should wait for the approval")
	}
	if tr, ok := v2.Transition("approved", "withdrawn"); !ok || !slices.Equal(tr.Allowed, []string{"creator"}) ||
		tr.Guard != "f_start >= today" {
		t.Errorf("approved -> withdrawn = %+v, %v", tr, ok)
	}
	if !lb.Accepts("vacation_request:deduct", []string{"f_used"}) || !lb.Accepts("vacation_request:refund", []string{"f_used"}) {
		t.Error("leave_balance does not accept deduct and refund on f_used")
	}
	var ids []string
	for _, inv := range lb.Invariants {
		ids = append(ids, inv.ID)
	}
	if !slices.Equal(ids, []string{"balance_incomplete", "insufficient_balance"}) {
		t.Errorf("leave_balance invariants = %v", ids)
	}
	if got := lb.RoleWritten([]string{"f_owner", "f_allowance", "f_used"}); !slices.Equal(got, []string{"f_allowance", "f_owner"}) {
		t.Errorf("RoleWritten = %v, want [f_allowance f_owner]", got)
	}
}
