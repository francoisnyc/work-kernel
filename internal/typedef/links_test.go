// Tests for proof of concept Step 1: link types and principal fields in a
// Type (docs/design.md, "Links, containment and people").
//
// A Type declares its link types like its fields: a declared name (from the
// source), an inverse name (from the target), the Types a target may be of,
// and at most one or many on each side. Guards read linked Items as
// links.<name>: a declared name for outgoing links, and the inverse name of
// a link type that targets the Type itself for incoming ones.
package typedef_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// linkType is a Type that links to itself (blocks, duplicate_of, claims)
// and to another Type (epic). LINKS and GUARD are replaced per test.
const linkType = `
id: task
version: 1
initial_state: s_open
fields:
  - {id: f_title, type: string}
  - {id: f_owner, type: principal}
links:
LINKS
states:
  - {id: s_open, category: todo}
  - {id: s_done, category: done}
transitions:
  - {from: s_open, to: s_done, guard: 'GUARD'}
`

const taskLinks = `  - {name: blocks,       inverse: blocked_by, targets: [task]}
  - {name: duplicate_of, inverse: duplicates, targets: [task], max: one}
  - {name: claims,       inverse: claimed_by, targets: [task], inverse_max: one}
  - {name: epic,         inverse: stories,    targets: [epic], max: one}`

func taskType(links, guard string) []byte {
	s := strings.Replace(linkType, "LINKS", links, 1)
	return []byte(strings.Replace(s, "GUARD", guard, 1))
}

// Each link type keeps its names, targets and cardinality; an unwritten
// cardinality is many. Only declared names are link types: an inverse name
// is not.
func TestParse_LinkTypes(t *testing.T) {
	ty, err := typedef.Parse(taskType(taskLinks, "true"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	cases := []struct {
		name, inverse  string
		targets        []string
		srcOne, dstOne bool
	}{
		{"blocks", "blocked_by", []string{"task"}, false, false},
		{"duplicate_of", "duplicates", []string{"task"}, true, false},
		{"claims", "claimed_by", []string{"task"}, false, true},
		{"epic", "stories", []string{"epic"}, true, false},
	}
	for _, c := range cases {
		l, ok := ty.Link(c.name)
		if !ok {
			t.Errorf("Link(%q) not found", c.name)
			continue
		}
		if l.Inverse != c.inverse || !slices.Equal(l.Targets, c.targets) {
			t.Errorf("Link(%q) = %+v, want inverse %q, targets %v", c.name, l, c.inverse, c.targets)
		}
		if l.SourceSingle() != c.srcOne || l.TargetSingle() != c.dstOne {
			t.Errorf("Link(%q): SourceSingle = %v, TargetSingle = %v; want %v, %v",
				c.name, l.SourceSingle(), l.TargetSingle(), c.srcOne, c.dstOne)
		}
	}
	for _, inverse := range []string{"blocked_by", "duplicates", "claimed_by", "stories"} {
		if _, ok := ty.Link(inverse); ok {
			t.Errorf("Link(%q) found: an inverse name is not a link type", inverse)
		}
	}
	// Explicit "many" is the same as nothing written.
	ty, err = typedef.Parse(taskType(`  - {name: blocks, inverse: blocked_by, targets: [task], max: many, inverse_max: many}`, "true"))
	if err != nil {
		t.Fatalf("Parse with explicit many: %v", err)
	}
	if l, _ := ty.Link("blocks"); l.SourceSingle() || l.TargetSingle() {
		t.Errorf("explicit many: %+v reads as single", l)
	}
}

// A malformed link type is refused when the Type loads, with a message that
// names it.
func TestParse_LinkTypes_Refused(t *testing.T) {
	cases := []struct {
		name, links, mention string
	}{
		{"no name", `  - {inverse: blocked_by, targets: [task]}`, "name"},
		{"no inverse", `  - {name: blocks, targets: [task]}`, "blocks"},
		{"inverse equals name", `  - {name: blocks, inverse: blocks, targets: [task]}`, "blocks"},
		{"duplicate name", "  - {name: blocks, inverse: blocked_by, targets: [task]}\n" +
			"  - {name: blocks, inverse: held_up_by, targets: [task]}", "blocks"},
		{"duplicate inverse", "  - {name: blocks, inverse: blocked_by, targets: [task]}\n" +
			"  - {name: stops, inverse: blocked_by, targets: [task]}", "blocked_by"},
		{"a name is another's inverse", "  - {name: blocks, inverse: blocked_by, targets: [task]}\n" +
			"  - {name: blocked_by, inverse: unblocks, targets: [task]}", "blocked_by"},
		{"no targets", `  - {name: blocks, inverse: blocked_by}`, "blocks"},
		{"empty target", `  - {name: blocks, inverse: blocked_by, targets: [""]}`, "blocks"},
		{"unknown max", `  - {name: blocks, inverse: blocked_by, targets: [task], max: two}`, "blocks"},
		{"unknown inverse_max", `  - {name: blocks, inverse: blocked_by, targets: [task], inverse_max: few}`, "blocks"},
		{"unknown key", `  - {name: blocks, inverse: blocked_by, targets: [task], cascade: true}`, "cascade"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := typedef.Parse(taskType(c.links, "true"))
			if err == nil {
				t.Fatal("Parse accepted it")
			}
			if !strings.Contains(err.Error(), c.mention) {
				t.Errorf("error %q does not mention %q", err, c.mention)
			}
		})
	}
}

// Types are stored as JSON; link types survive the round trip, and the
// guard's link names are found again when it is read back.
func TestParse_LinkTypes_JSONRoundTrip(t *testing.T) {
	ty, err := typedef.Parse(taskType(taskLinks, `links.blocked_by.all(b, b.category == "done")`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	data, err := json.Marshal(ty)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	back, err := typedef.FromJSON(data)
	if err != nil {
		t.Fatalf("FromJSON: %v", err)
	}
	if !reflect.DeepEqual(back.Links, ty.Links) {
		t.Errorf("links after the round trip = %+v, want %+v", back.Links, ty.Links)
	}
	tr, _ := back.Transition("s_open", "s_done")
	if !slices.Equal(tr.GuardLinks, []string{"blocked_by"}) {
		t.Errorf("GuardLinks after the round trip = %v, want [blocked_by]", tr.GuardLinks)
	}
}

// Every link target must be a Type of the set loaded at start: one Type
// file cannot know the others, so ParseDir checks it.
func TestParseDir_LinkTargetsMustExist(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("task.v1.yaml", string(taskType(taskLinks, "true")))
	_, err := typedef.ParseDir(dir)
	if err == nil || !strings.Contains(err.Error(), "epic") {
		t.Fatalf("ParseDir without the epic Type: error = %v, want one naming epic", err)
	}

	write("epic.v1.yaml", "id: epic\nversion: 1\nfields:\n  - {id: f_title, type: string}\n")
	types, err := typedef.ParseDir(dir)
	if err != nil {
		t.Fatalf("ParseDir with every target: %v", err)
	}
	if err := typedef.CheckLinkTargets(types); err != nil {
		t.Errorf("CheckLinkTargets on a complete set: %v", err)
	}
	if err := typedef.CheckLinkTargets(types[1:]); err == nil { // task alone (sorted: epic, task)
		t.Error("CheckLinkTargets accepted task without epic")
	}
}

// A principal field holds user:<id> or agent:<id>, nothing else: group: is
// reserved until groups exist, and the kernel's own actors are not people.
func TestParse_PrincipalField(t *testing.T) {
	ty, err := typedef.Parse(taskType(taskLinks, "f_owner != null"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	f, ok := ty.Field("f_owner")
	if !ok || f.Type != typedef.Principal {
		t.Fatalf("f_owner = %+v, want a principal field", f)
	}
	for _, v := range []string{"user:ana", "agent:triage-bot"} {
		if err := ty.CheckFields(map[string]any{"f_owner": v}, false); err != nil {
			t.Errorf("CheckFields(f_owner = %q): %v", v, err)
		}
	}
	for _, v := range []any{"ana", "user:", "group:support", "system:timer", "hook:task:x", "me", 3, true} {
		err := ty.CheckFields(map[string]any{"f_owner": v}, false)
		var fe *typedef.FieldError
		if !errors.As(err, &fe) || fe.Field != "f_owner" {
			t.Errorf("CheckFields(f_owner = %v) = %v, want a FieldError naming f_owner", v, err)
			continue
		}
		// The reason names the field, never the value (personal data).
		if s, isString := v.(string); isString && len(s) > 5 && strings.Contains(fe.Reason, s) {
			t.Errorf("reason %q repeats the value %q", fe.Reason, s)
		}
	}
	// Null clears it on an update, like any field.
	if err := ty.CheckFields(map[string]any{"f_owner": nil}, true); err != nil {
		t.Errorf("CheckFields(f_owner = null) on update: %v", err)
	}
	// "options" are for enums only.
	bad := strings.Replace(string(taskType(taskLinks, "true")),
		"{id: f_owner, type: principal}", "{id: f_owner, type: principal, options: [user:ana]}", 1)
	if _, err := typedef.Parse([]byte(bad)); err == nil {
		t.Error("a principal field with options was accepted")
	}
}

// A guard reads links.<name>: a list of the linked Items' state and
// category. The names a guard reads are recorded, so only those transitions
// read and lock linked Items.
func TestParse_GuardsReadLinks(t *testing.T) {
	cases := []struct {
		guard string
		want  []string
	}{
		{`links.blocked_by.all(b, b.category in ["done", "canceled"])`, []string{"blocked_by"}},
		{`size(links.blocks) == 0`, []string{"blocks"}},
		{`links.blocks.exists(b, b.state == "s_open")`, []string{"blocks"}},
		{`size(links.duplicate_of) == 1 || links.blocked_by.all(b, b.category == "done")`,
			[]string{"blocked_by", "duplicate_of"}},
		{`size(links.epic) == 1`, []string{"epic"}},             // outgoing to another Type
		{`size(links.claimed_by) == 0`, []string{"claimed_by"}}, // incoming, self-targeting
		{`f_owner != null`, nil},
	}
	for _, c := range cases {
		ty, err := typedef.Parse(taskType(taskLinks, c.guard))
		if err != nil {
			t.Errorf("guard %s: Parse: %v", c.guard, err)
			continue
		}
		tr, _ := ty.Transition("s_open", "s_done")
		if !slices.Equal(tr.GuardLinks, c.want) {
			t.Errorf("guard %s: GuardLinks = %v, want %v", c.guard, tr.GuardLinks, c.want)
		}
	}
	// A transition with no guard reads no links.
	ty, err := typedef.Parse([]byte(strings.Replace(string(taskType(taskLinks, "x")), ", guard: 'x'", "", 1)))
	if err != nil {
		t.Fatalf("Parse without a guard: %v", err)
	}
	if tr, _ := ty.Transition("s_open", "s_done"); len(tr.GuardLinks) != 0 {
		t.Errorf("no guard: GuardLinks = %v, want none", tr.GuardLinks)
	}
}

// A guard may only name links the Type can see: its declared names, and
// the inverses of link types that target the Type itself. Incoming links
// declared by other Types (an epic's stories) are not visible in Step 1.
func TestParse_GuardsReadLinks_Refused(t *testing.T) {
	for _, guard := range []string{
		`size(links.nope) == 0`,    // no such link
		`size(links.stories) == 0`, // epic's incoming: declared here, but it targets epic, not task
		`size(links) == 0`,         // links is not a value of its own
		`links.blocks == 1`,        // a list, not an int
	} {
		if _, err := typedef.Parse(taskType(taskLinks, guard)); err == nil {
			t.Errorf("guard %s was accepted", guard)
		}
	}
	// A Type with no link types has no links to read.
	noLinks := strings.Replace(linkType, "links:\nLINKS\n", "", 1)
	if _, err := typedef.Parse([]byte(strings.Replace(noLinks, "GUARD", `size(links.blocks) == 0`, 1))); err == nil {
		t.Error("a Type with no link types accepted a guard reading links")
	}
}

// Ticket v5 is v4 plus links and a principal assignee; it may not be
// resolved while a ticket that is neither done nor canceled blocks it.
func TestTicketV5_IsV4WithLinksAndAPrincipalAssignee(t *testing.T) {
	v4 := parseFile(t, "ticket.v4.yaml")
	data, err := os.ReadFile("../../types/ticket.v5.yaml")
	if err != nil {
		t.Fatalf("read ticket.v5.yaml: %v", err)
	}
	v5, err := typedef.Parse(data)
	if err != nil {
		t.Fatalf("parse ticket v5: %v", err)
	}
	if v5.ID != "ticket" || v5.Version != 5 || v5.InitialState != v4.InitialState {
		t.Errorf("v5 = %s v%d starting in %q", v5.ID, v5.Version, v5.InitialState)
	}
	if len(v5.Fields) != len(v4.Fields) {
		t.Errorf("v5 has %d fields, v4 %d", len(v5.Fields), len(v4.Fields))
	}
	for _, f := range v4.Fields {
		g, ok := v5.Field(f.ID)
		want := f.Type
		if f.ID == "f_assignee" {
			want = typedef.Principal
		}
		if !ok || g.Type != want {
			t.Errorf("v5 field %s = %+v (found %v), want type %s", f.ID, g, ok, want)
		}
	}
	blocks, ok := v5.Link("blocks")
	if !ok || blocks.Inverse != "blocked_by" || !slices.Equal(blocks.Targets, []string{"ticket"}) ||
		blocks.SourceSingle() || blocks.TargetSingle() {
		t.Errorf("v5 blocks = %+v (found %v), want blocks/blocked_by, many to many, to ticket", blocks, ok)
	}
	dup, ok := v5.Link("duplicate_of")
	if !ok || dup.Inverse != "duplicates" || !dup.SourceSingle() || dup.TargetSingle() {
		t.Errorf("v5 duplicate_of = %+v (found %v), want at most one from the source", dup, ok)
	}
	for _, tr := range v4.Transitions {
		got, ok := v5.Transition(tr.From, tr.To)
		if !ok {
			t.Errorf("v4 transition %s -> %s is missing from v5", tr.From, tr.To)
			continue
		}
		wantLinks := []string(nil)
		if tr.To == "resolved" {
			wantLinks = []string{"blocked_by"}
		}
		if !slices.Equal(got.GuardLinks, wantLinks) {
			t.Errorf("v5 %s -> %s reads links %v, want %v", tr.From, tr.To, got.GuardLinks, wantLinks)
		}
	}
	if len(v5.Transitions) != len(v4.Transitions) || len(v5.Hooks) != len(v4.Hooks) {
		t.Errorf("v5 has %d transitions and %d hooks, v4 %d and %d",
			len(v5.Transitions), len(v5.Hooks), len(v4.Transitions), len(v4.Hooks))
	}
}

// A link name keeps its inverse across the versions of a Type, so (Type,
// link) names one inverse for incoming links and filters. ParseDir refuses
// a version that disagrees, naming the Type, the link and both inverses.
func TestParseDir_LinkInverseAgreesAcrossVersions(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	version := func(v int, inverse string) string {
		return strings.NewReplacer("VERSION", strconv.Itoa(v), "INVERSE", inverse).Replace(
			"id: chore\nversion: VERSION\nlinks:\n  - {name: blocks, inverse: INVERSE, targets: [chore]}\n")
	}
	write("chore.v1.yaml", version(1, "blocked_by"))
	write("chore.v2.yaml", version(2, "blocked_by"))
	if _, err := typedef.ParseDir(dir); err != nil {
		t.Fatalf("two versions with the same inverse: %v", err)
	}
	write("chore.v3.yaml", version(3, "held_up_by"))
	_, err := typedef.ParseDir(dir)
	if err == nil {
		t.Fatal("ParseDir accepted a version with another inverse")
	}
	for _, s := range []string{"chore", "blocks", "blocked_by", "held_up_by"} {
		if !strings.Contains(err.Error(), s) {
			t.Errorf("error %q does not name %s", err, s)
		}
	}
	// The check on its own: one Type's versions against another's.
	a, _ := typedef.Parse([]byte(version(1, "blocked_by")))
	b, _ := typedef.Parse([]byte(version(4, "held_up_by")))
	if err := typedef.CheckLinkInverses([]*typedef.Type{a, b}); err == nil {
		t.Error("CheckLinkInverses accepted two inverses for chore's blocks")
	}
}
