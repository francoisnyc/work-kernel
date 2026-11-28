// Tests for Ventoux V2b: typedef.Check runs every stage on draft Types, as
// ParseDir does on a folder, with the registered Type versions (their stored
// JSON definitions) loaded beside them. Nothing is registered: Check is
// pure, and the item service's ValidateTypes calls it.
//
//   - A draft's problems name its Source and are positioned in its YAML.
//   - A draft that declares a registered id@version must be identical to it
//     (the same JSON, as RegisterType compares them): else VERSION_CHANGED.
//   - Stage (c) runs on the registered versions alone, then with the
//     drafts; only the problems that are new are reported. A new problem
//     that lands on a registered version has no Source and no position,
//     only its Type: the caller names it.
//   - A registered definition that cannot be read is an error, not
//     Problems: the kernel's own store is broken, not the draft.
package typedef_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// checkCase is a problem only Check can find: it needs registered versions.
type checkCase struct {
	name       string
	registered []string          // YAML, stored as RegisterType stores it
	drafts     map[string]string // source name -> YAML
	source     string            // the problem's Source ("" for a registered version)
	problemCase
}

// One case per code that only Check can have.
var checkCases = []checkCase{
	{
		name:        "a draft of a registered version with another definition",
		registered:  []string{"id: memo\nversion: 1\n"},
		drafts:      map[string]string{"memo.v1.yaml": "id: memo\nversion: 1\nfields:\n  - {id: f_title, type: string}\n"},
		source:      "memo.v1.yaml",
		problemCase: problemCase{code: typedef.CodeVersionChanged, path: "version", line: 2, col: 1, typeID: "memo", version: 1, old: "memo v1 is already registered with a different definition"},
	},
}

// def is a Type's stored definition: Parse, then json.Marshal, as the item
// service stores it (RegisterType).
func def(t *testing.T, src string) []byte {
	t.Helper()
	ty, err := typedef.Parse([]byte(src))
	if err != nil {
		t.Fatalf("test bug: a registered Type does not parse: %v\n%s", err, src)
	}
	data, err := json.Marshal(ty)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func defs(t *testing.T, srcs ...string) [][]byte {
	t.Helper()
	var out [][]byte
	for _, s := range srcs {
		out = append(out, def(t, s))
	}
	return out
}

// sources turns a map of drafts into Sources, ordered by name.
func sources(drafts map[string]string) []typedef.Source {
	var out []typedef.Source
	// maps.Keys is an iterator; slices.Sorted collects and sorts it.
	for _, name := range slices.Sorted(maps.Keys(drafts)) {
		out = append(out, typedef.Source{Name: name, Content: []byte(drafts[name])})
	}
	return out
}

// repoTypes reads types/ and its subfolders as Sources named by their path
// below types/, and as the definitions RegisterType would store.
func repoTypes(t *testing.T) ([]typedef.Source, [][]byte) {
	t.Helper()
	var (
		srcs []typedef.Source
		regs [][]byte
	)
	for _, pattern := range []string{"../../types/*.yaml", "../../types/*/*.yaml"} {
		files, err := filepath.Glob(pattern)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range files {
			data, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			name, _ := filepath.Rel("../../types", f)
			srcs = append(srcs, typedef.Source{Name: filepath.ToSlash(name), Content: data})
			regs = append(regs, def(t, string(data)))
		}
	}
	if len(srcs) < 40 {
		t.Fatalf("found %d files in types/; the test is not looking where it should", len(srcs))
	}
	return srcs, regs
}

func checkProblems(t *testing.T, srcs []typedef.Source, registered [][]byte) typedef.Problems {
	t.Helper()
	types, err := typedef.Check(srcs, registered)
	if types != nil {
		t.Error("Check returned Types with its problems; want nil")
	}
	return problemsOf(t, err)
}

func checkClean(t *testing.T, srcs []typedef.Source, registered [][]byte) []*typedef.Type {
	t.Helper()
	types, err := typedef.Check(srcs, registered)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	return types
}

func keys(types []*typedef.Type) []string {
	var out []string
	for _, ty := range types {
		out = append(out, fmt.Sprintf("%s@%d", ty.ID, ty.Version))
	}
	return out
}

func TestCheck_OneOfEachKind(t *testing.T) {
	for _, c := range checkCases {
		t.Run(c.name, func(t *testing.T) {
			ps := checkProblems(t, sources(c.drafts), defs(t, c.registered...))
			if len(ps) != 1 {
				t.Fatalf("%d problems, want 1:\n%v", len(ps), ps)
			}
			if ps[0].Source != c.source {
				t.Errorf("source = %q, want %q", ps[0].Source, c.source)
			}
			checkProblem(t, ps[0], c.problemCase)
		})
	}
}

// With nothing registered, Check finds what ParseDir finds in the same
// files, positioned the same, with each Source the name it was given.
func TestCheck_AsParseDir(t *testing.T) {
	for _, c := range dirCases {
		if c.code == typedef.CodeFolderTooDeep { // the folder walk's, not Check's
			continue
		}
		t.Run(c.name, func(t *testing.T) {
			ps := checkProblems(t, sources(c.files), nil)
			if len(ps) != 1 {
				t.Fatalf("%d problems, want 1:\n%v", len(ps), ps)
			}
			if ps[0].Source != c.source {
				t.Errorf("source = %q, want %q", ps[0].Source, c.source)
			}
			checkProblem(t, ps[0], c.problemCase)
		})
	}
}

// Every file of types/ passes, with nothing registered and with every file
// registered already (each draft identical to its registered version), and
// Check returns the drafts as ParseDir returns the folder.
func TestCheck_TheRepositoryTypes(t *testing.T) {
	srcs, regs := repoTypes(t)
	want, err := typedef.ParseDir("../../types")
	if err != nil {
		t.Fatalf("ParseDir: %v", err)
	}
	for what, registered := range map[string][][]byte{"nothing registered": nil, "all registered": regs} {
		got := checkClean(t, srcs, registered)
		if !slices.Equal(keys(got), keys(want)) {
			t.Errorf("%s: Check returned\n%v\nwant ParseDir's\n%v", what, keys(got), keys(want))
		}
	}
}

// Identical means the same JSON definition: comments, key order, flow or
// block style and blank lines do not matter.
func TestCheck_IdenticalIsTheSameDefinition(t *testing.T) {
	registered := `id: memo
version: 1
initial_state: open
states:
  - id: open
    category: todo
  - id: done
    category: done
transitions:
  - from: open
    to: done
`
	draft := `# The same memo, written another way.
version: 1
id: memo

transitions: [{to: done, from: open}]
states: [{category: todo, id: open}, {id: done, category: done}]
initial_state: open
`
	got := checkClean(t, sources(map[string]string{"memo.yaml": draft}), defs(t, registered))
	if !slices.Equal(keys(got), []string{"memo@1"}) {
		t.Errorf("Check returned %v, want [memo@1]", keys(got))
	}
}

// Check returns the drafts only, sorted by ID then version, never the
// registered versions beside them.
func TestCheck_ReturnsTheDraftsOnly(t *testing.T) {
	drafts := map[string]string{
		"b.yaml": "id: memo\nversion: 2\n",
		"a.yaml": "id: memo\nversion: 3\n",
		"c.yaml": "id: alpha\nversion: 1\n",
	}
	got := checkClean(t, sources(drafts), defs(t, "id: memo\nversion: 1\n", "id: task\nversion: 1\n"))
	if want := []string{"alpha@1", "memo@2", "memo@3"}; !slices.Equal(keys(got), want) {
		t.Errorf("Check returned %v, want %v", keys(got), want)
	}
}

// A draft is checked against the registered versions: a link to a Type
// that is only registered resolves, and a reaction needs the registered
// target's consent.
func TestCheck_DraftsMeetTheRegistered(t *testing.T) {
	memo := map[string]string{"memo.yaml": "id: memo\nversion: 1\nlinks:\n  - {name: about, inverse: mentioned_by, targets: [task]}\n"}
	checkClean(t, sources(memo), defs(t, "id: task\nversion: 1\n"))
	ps := checkProblems(t, sources(memo), nil)
	if len(ps) != 1 || ps[0].Code != typedef.CodeUnknownType || ps[0].Source != "memo.yaml" {
		t.Errorf("nothing registered: problems = %v, want UNKNOWN_TYPE in memo.yaml", ps)
	}

	part := map[string]string{"part.yaml": pdPart}
	checkClean(t, sources(part), defs(t, pdWhole))
	ps = checkProblems(t, sources(part), defs(t, "id: whole\nversion: 1\n"+pdOpenDone))
	if len(ps) != 1 {
		t.Fatalf("whole without consent: %d problems, want 1:\n%v", len(ps), ps)
	}
	checkProblem(t, ps[0], problemCase{code: typedef.CodeConsentMissing, path: "reactions[0]", line: 12, col: 5,
		typeID: "part", version: 1, old: "which does not accept part:finish"})
	if ps[0].Source != "part.yaml" {
		t.Errorf("source = %q, want part.yaml", ps[0].Source)
	}
}

// A draft can break a registered version: whole v2 no longer consents to
// part's reaction, and the problem is part v1's. It has no Source and no
// position, only its Type and its path.
func TestCheck_ANewProblemOnARegisteredVersion(t *testing.T) {
	wholeV2 := strings.Replace(pdWhole, "version: 1", "version: 2", 1)
	wholeV2 = wholeV2[:strings.Index(wholeV2, "accepts_reactions:")]
	ps := checkProblems(t, sources(map[string]string{"whole.v2.yaml": wholeV2}), defs(t, pdPart, pdWhole))
	if len(ps) != 1 {
		t.Fatalf("%d problems, want 1:\n%v", len(ps), ps)
	}
	if ps[0].Source != "" {
		t.Errorf("source = %q, want none: the problem is a registered version's", ps[0].Source)
	}
	checkProblem(t, ps[0], problemCase{code: typedef.CodeConsentMissing, path: "reactions[0]", line: 0, col: 0,
		typeID: "part", version: 1, old: "moves Items of whole v2 into done, which does not accept part:finish"})
}

// A problem across versions lands on the later one, which may be the
// registered one: memo v2 is registered, and a draft memo v1 gives the link
// another inverse.
func TestCheck_AnInverseChangedOnTheLaterRegisteredVersion(t *testing.T) {
	v1 := "id: memo\nversion: 1\nlinks:\n  - {name: blocks, inverse: blocked_by, targets: [memo]}\n"
	v2 := "id: memo\nversion: 2\nlinks:\n  - {name: blocks, inverse: waits_for, targets: [memo]}\n"
	ps := checkProblems(t, sources(map[string]string{"memo.v1.yaml": v1}), defs(t, v2))
	if len(ps) != 1 {
		t.Fatalf("%d problems, want 1:\n%v", len(ps), ps)
	}
	if ps[0].Source != "" {
		t.Errorf("source = %q, want none", ps[0].Source)
	}
	checkProblem(t, ps[0], problemCase{code: typedef.CodeInverseChanged, path: "links[0].inverse", line: 0, col: 0,
		typeID: "memo", version: 2, old: "link blocks has inverse blocked_by in v1 and waits_for in v2"})
}

// A problem the registered versions have on their own is not the drafts':
// it is not reported. A new one of the same kind is.
func TestCheck_OldProblemsAreNotReported(t *testing.T) {
	// Registered memo links to a Type nobody registered (stage c, alone).
	registered := defs(t, "id: memo\nversion: 1\nlinks:\n  - {name: about, inverse: mentioned_by, targets: [ghost]}\n")
	checkClean(t, sources(map[string]string{"note.yaml": "id: note\nversion: 1\n"}), registered)

	ps := checkProblems(t, sources(map[string]string{
		"note.yaml": "id: note\nversion: 1\nlinks:\n  - {name: about, inverse: noted_by, targets: [phantom]}\n",
	}), registered)
	if len(ps) != 1 || ps[0].Code != typedef.CodeUnknownType || ps[0].TypeID != "note" || ps[0].Source != "note.yaml" {
		t.Errorf("problems = %v, want only note's UNKNOWN_TYPE", ps)
	}
}

// Two drafts of one version: the second is DUPLICATE_VERSION, as in a
// folder, whatever is registered.
func TestCheck_TwoDraftsOfOneVersion(t *testing.T) {
	srcs := []typedef.Source{
		{Name: "a.yaml", Content: []byte("id: memo\nversion: 1\n")},
		{Name: "b.yaml", Content: []byte("id: memo\nversion: 1\n")},
	}
	for what, registered := range map[string][][]byte{"nothing registered": nil, "memo v1 registered": defs(t, "id: memo\nversion: 1\n")} {
		ps := checkProblems(t, srcs, registered)
		if len(ps) != 1 || ps[0].Code != typedef.CodeDuplicateVersion || ps[0].Source != "b.yaml" || ps[0].Path != "version" {
			t.Errorf("%s: problems = %v, want DUPLICATE_VERSION at b.yaml version", what, ps)
		}
	}
}

// VERSION_CHANGED is a stage (c) problem: it waits until every draft is
// sound on its own.
func TestCheck_TogetherOnlyWhenEachIsSound(t *testing.T) {
	drafts := map[string]string{
		"memo.yaml": "id: memo\nversion: 1\nfields:\n  - {id: f_title, type: string}\n",
		"note.yaml": "id: note\nversion: 1\nfields:\n  - {id: today, type: date}\n",
	}
	registered := defs(t, "id: memo\nversion: 1\n")
	ps := checkProblems(t, sources(drafts), registered)
	if len(ps) != 1 || ps[0].Code != typedef.CodeReservedName || ps[0].Source != "note.yaml" {
		t.Errorf("problems = %v, want only RESERVED_NAME in note.yaml", ps)
	}
	drafts["note.yaml"] = "id: note\nversion: 1\nfields:\n  - {id: f_today, type: date}\n"
	ps = checkProblems(t, sources(drafts), registered)
	if len(ps) != 1 || ps[0].Code != typedef.CodeVersionChanged || ps[0].Source != "memo.yaml" {
		t.Errorf("problems = %v, want only VERSION_CHANGED in memo.yaml", ps)
	}
}

// Actions are checked against the compiled-in catalog, as at load (the hook
// protocol): a major it does not declare is UNKNOWN_ACTION, with no network.
func TestCheck_ActionMajors(t *testing.T) {
	_, regs := repoTypes(t)
	v3, err := os.ReadFile("../../types/vacation_request.v3.yaml")
	if err != nil {
		t.Fatal(err)
	}
	// A draft of the next version: vacation_request@4 is registered since
	// track 1, item 6, step 1, and vacation_request@5 since the follow-up
	// step after pli check PR 2.
	v4 := strings.Replace(string(v3), "\nversion: 3\n", "\nversion: 6\n", 1)
	if v4 == string(v3) || !strings.Contains(v4, "    action: hr.manager_of\n") {
		t.Fatal("test bug: vacation_request.v3.yaml is not as this test expects")
	}
	for major, wantProblem := range map[string]bool{"": false, "@1": false, "@2": true} {
		draft := strings.Replace(v4, "    action: hr.manager_of\n", "    action: hr.manager_of"+major+"\n", 1)
		_, err := typedef.Check([]typedef.Source{{Name: "vacation_request.v4.yaml", Content: []byte(draft)}}, regs)
		if !wantProblem {
			if err != nil {
				t.Errorf("hr.manager_of%s: %v", major, err)
			}
			continue
		}
		ps := problemsOf(t, err)
		if len(ps) != 1 {
			t.Fatalf("hr.manager_of%s: %d problems, want 1:\n%v", major, len(ps), ps)
		}
		checkProblem(t, ps[0], problemCase{code: typedef.CodeUnknownAction, path: "hooks[0].action", line: 104, col: 5,
			typeID: "vacation_request", version: 6, old: "hr.manager_of@2"})
	}
}

// A registered definition that cannot be read is the store's fault, not the
// draft's: an error, not Problems, that says so ("registered") and names
// the Type when its JSON does ("memo").
func TestCheck_AnUnreadableRegisteredVersion(t *testing.T) {
	draft := sources(map[string]string{"memo.yaml": "id: memo\nversion: 1\n"})
	for what, c := range map[string]struct {
		def  []byte
		want string
	}{
		"not JSON":         {[]byte("{"), "registered"},
		"not a sound Type": {[]byte(`{"id": "memo", "version": 0}`), "registered memo"},
	} {
		types, err := typedef.Check(draft, [][]byte{c.def})
		var ps typedef.Problems
		if err == nil || errors.As(err, &ps) || types != nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: Check = %v, %v; want no Types and an error, not Problems, that says %q",
				what, keys(types), err, c.want)
		}
	}
}
