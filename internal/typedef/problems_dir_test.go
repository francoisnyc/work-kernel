// Tests for Ventoux item 2a: ParseDir reports every problem of every file,
// then, when each Type is sound on its own, every problem of the Types
// together (stage c). See problems_test.go for the single-Type cases.
package typedef_test

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// The YAML both sides of a move reaction share: open -> done.
const pdOpenDone = `initial_state: open
states:
  - {id: open, category: todo}
  - {id: done, category: done}
transitions:
  - {from: open, to: done}
`

// pdPart moves the whole it belongs to into done when it is done.
const pdPart = `id: part
version: 1
` + pdOpenDone + `links:
  - {name: whole, inverse: parts, targets: [whole], max: one}
reactions:
  - {id: finish, kind: move, on: enter, state: done, link: whole, to: done}
`

// pdWhole consents to it.
const pdWhole = `id: whole
version: 1
` + pdOpenDone + `accepts_reactions:
  part:finish: {moves: [done]}
`

type dirProblemCase struct {
	name  string
	files map[string]string
	// The problem's source, relative to the folder ("" for none).
	source string
	problemCase
	// Replacements per file that make the folder load.
	fixFiles map[string][][2]string
}

// One case per code that only a folder of Types can have. Each was checked
// against the code before item 2a, as in problems_test.go.
var dirCases = []dirProblemCase{
	{
		name: "a link to a Type that is not loaded",
		files: map[string]string{"memo.v1.yaml": `id: memo
version: 1
links:
  - {name: about, inverse: mentioned_by, targets: [ghost]}
`},
		source:      "memo.v1.yaml",
		problemCase: problemCase{code: typedef.CodeUnknownType, path: "links[0].targets[0]", line: 4, col: 52, typeID: "memo", version: 1, old: `link about: target Type "ghost" is not loaded`},
		fixFiles:    map[string][][2]string{"memo.v1.yaml": {{"[ghost]", "[memo]"}}},
	},
	{
		name: "a move the target Type does not accept",
		files: map[string]string{"part.v1.yaml": pdPart, "whole.v1.yaml": `id: whole
version: 1
` + pdOpenDone},
		source:      "part.v1.yaml",
		problemCase: problemCase{code: typedef.CodeConsentMissing, path: "reactions[0]", line: 12, col: 5, typeID: "part", version: 1, old: "reaction finish moves Items of whole v1 into done, which does not accept part:finish for it"},
		fixFiles:    map[string][][2]string{"whole.v1.yaml": {{"transitions:\n  - {from: open, to: done}\n", "transitions:\n  - {from: open, to: done}\naccepts_reactions:\n  part:finish: {moves: [done]}\n"}}},
	},
	{
		name: "a link that changes its inverse in v2",
		files: map[string]string{
			"memo.v1.yaml": "id: memo\nversion: 1\nlinks:\n  - {name: blocks, inverse: blocked_by, targets: [memo]}\n",
			"memo.v2.yaml": "id: memo\nversion: 2\nlinks:\n  - {name: blocks, inverse: waits_for, targets: [memo]}\n",
		},
		source:      "memo.v2.yaml",
		problemCase: problemCase{code: typedef.CodeInverseChanged, path: "links[0].inverse", line: 4, col: 20, typeID: "memo", version: 2, old: "link blocks has inverse blocked_by in v1 and waits_for in v2"},
		fixFiles:    map[string][][2]string{"memo.v2.yaml": {{"waits_for", "blocked_by"}}},
	},
	{
		name: "an incoming name that is not the link's inverse",
		files: map[string]string{
			"memo.v1.yaml": "id: memo\nversion: 1\nlinks:\n  - {name: blocks, inverse: blocked_by, targets: [task]}\n",
			"task.v1.yaml": "id: task\nversion: 1\nincoming:\n  - {name: waits_for, link: blocks, from: [memo]}\n",
		},
		source:      "task.v1.yaml",
		problemCase: problemCase{code: typedef.CodeInverseMismatch, path: "incoming[0].name", line: 4, col: 6, typeID: "task", version: 1, old: `memo v1 declares link blocks with the inverse "blocked_by"; the incoming name must be that inverse`},
		fixFiles:    map[string][][2]string{"task.v1.yaml": {{"waits_for", "blocked_by"}}},
	},
	{
		name: "a field that becomes multiple in v2",
		files: map[string]string{
			"memo.v1.yaml": "id: memo\nversion: 1\nfields:\n  - {id: f_tags, type: string}\n",
			"memo.v2.yaml": "id: memo\nversion: 2\nfields:\n  - {id: f_tags, type: string, multiple: true}\n",
		},
		source:      "memo.v2.yaml",
		problemCase: problemCase{code: typedef.CodeShapeChanged, path: "fields[0].multiple", line: 4, col: 32, typeID: "memo", version: 2, old: "field f_tags holds one value in one version and several in another"},
		fixFiles:    map[string][][2]string{"memo.v2.yaml": {{", multiple: true", ""}}},
	},
	{
		// A cycle belongs to no one Type: it is reported on the first
		// reaction of the cycle as its message spells it.
		name: "two Types moving each other into done",
		files: map[string]string{
			"part.v1.yaml": pdPart + "accepts_reactions:\n  whole:settle: {moves: [done]}\n",
			"whole.v1.yaml": pdWhole + `links:
  - {name: first, inverse: first_of, targets: [part], max: one}
reactions:
  - {id: settle, kind: move, on: enter, state: done, link: first, to: done}
`,
		},
		source:      "part.v1.yaml",
		problemCase: problemCase{code: typedef.CodeReactionCycle, path: "reactions[0]", line: 12, col: 5, typeID: "part", version: 1, old: "reactions form a cycle: part enters done -> whole enters done -> part enters done"},
		fixFiles: map[string][][2]string{
			"part.v1.yaml":  {{"accepts_reactions:\n  whole:settle: {moves: [done]}\n", ""}},
			"whole.v1.yaml": {{"links:\n  - {name: first, inverse: first_of, targets: [part], max: one}\nreactions:\n  - {id: settle, kind: move, on: enter, state: done, link: first, to: done}\n", ""}},
		},
	},
	{
		// Reported on the file read second, in path order.
		name:        "two files declaring memo v1",
		files:       map[string]string{"a.yaml": "id: memo\nversion: 1\n", "b.yaml": "id: memo\nversion: 1\n"},
		source:      "b.yaml",
		problemCase: problemCase{code: typedef.CodeDuplicateVersion, path: "version", line: 2, col: 1, typeID: "memo", version: 1, old: "memo v1 is declared by both"},
		fixFiles:    map[string][][2]string{"b.yaml": {{"version: 1", "version: 2"}}},
	},
	{
		name:        "a folder two levels down",
		files:       map[string]string{"memo.v1.yaml": "id: memo\nversion: 1\n", "sub/deeper/x.yaml": "id: x\nversion: 1\n"},
		source:      "sub/deeper",
		problemCase: problemCase{code: typedef.CodeFolderTooDeep, old: "Types are read from a folder and its subfolders, one level deep"},
		fixFiles:    nil, // the fix moves the file; see TestParseDirProblems_TheFixLoads
	},
	{
		// Starting work, step 2: a portal would offer what the kernel
		// refuses. On the container's own entry.
		name: "own naming a Type created only in other containers",
		files: map[string]string{
			"room.v1.yaml": "id: room\nversion: 1\n",
			"hall.v1.yaml": "id: hall\nversion: 1\nown: [ask]\n",
			"ask.v1.yaml":  "id: ask\nversion: 1\nstart:\n  in: [room]\n",
		},
		source:      "hall.v1.yaml",
		problemCase: problemCase{code: typedef.Code("START_NOT_IN"), path: "own[0]", line: 3, col: 7, typeID: "hall", version: 1, old: "admits customers to ask, whose latest version (v1) is created only in room"},
		fixFiles:    map[string][][2]string{"ask.v1.yaml": {{"[room]", "[room, hall]"}}},
	},
}

// Each kind of problem only a folder can have comes back alone, on the file
// it belongs to, as ParseDir joined its path.
func TestParseDirProblems_OneOfEachKind(t *testing.T) {
	for _, c := range dirCases {
		t.Run(c.name, func(t *testing.T) {
			dir := writeTree(t, c.files)
			types, err := typedef.ParseDir(dir)
			if types != nil {
				t.Error("ParseDir returned Types with its problems; want nil")
			}
			ps := problemsOf(t, err)
			if len(ps) != 1 {
				t.Fatalf("%d problems, want 1:\n%v", len(ps), ps)
			}
			if want := filepath.Join(dir, filepath.FromSlash(c.source)); ps[0].Source != want {
				t.Errorf("source = %q, want %q", ps[0].Source, want)
			}
			checkProblem(t, ps[0], c.problemCase)
		})
	}
}

// The fixtures are honest: with the one problem fixed, each folder loads.
func TestParseDirProblems_TheFixLoads(t *testing.T) {
	for _, c := range dirCases {
		files := map[string]string{}
		for name, src := range c.files {
			for _, r := range c.fixFiles[name] {
				src = strings.Replace(src, r[0], r[1], 1)
			}
			files[name] = src
		}
		if c.problemCase.code == typedef.CodeFolderTooDeep {
			files["sub/x.yaml"] = files["sub/deeper/x.yaml"]
			delete(files, "sub/deeper/x.yaml")
		}
		if _, err := typedef.ParseDir(writeTree(t, files)); err != nil {
			t.Errorf("%s, fixed: %v", c.name, err)
		}
	}
}

// Every file is read, and every file's problems come back, sorted by
// source, then position: a syntax error in one file, two problems in
// another, one in a subfolder.
func TestParseDirProblems_EveryFile(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"b.v1.yaml": `id: b
version: 1
initial_state: open
states:
  - {id: open, category: doing}
transitions:
  - {from: open, to: closed}
`,
		"a.v1.yaml":     "id: a\nversion: 1\nfields: [\n",
		"good.v1.yaml":  "id: good\nversion: 1\n",
		"sub/c.v1.yaml": "id: c\nversion: 0\n",
	})
	ps := problemsOf(t, typedefParseDirErr(t, dir))
	var got []string
	for _, p := range ps {
		rel, _ := filepath.Rel(dir, p.Source)
		got = append(got, filepath.ToSlash(rel)+" "+string(p.Code)+" "+p.Path)
	}
	want := []string{
		"a.v1.yaml YAML_SYNTAX ",
		"b.v1.yaml INVALID_VALUE states[0].category",
		"b.v1.yaml UNKNOWN_STATE transitions[0].to",
		"sub/c.v1.yaml INVALID_VALUE version",
	}
	if !slices.Equal(got, want) {
		t.Errorf("problems =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func typedefParseDirErr(t *testing.T, dir string) error {
	t.Helper()
	_, err := typedef.ParseDir(dir)
	if err == nil {
		t.Fatal("ParseDir accepted the folder, want problems")
	}
	return err
}

// Stage (c) runs only when every Type is sound on its own: a link to a
// Type that is not loaded is not reported while another file has a
// problem of its own. Without that file's problem, it is.
func TestParseDirProblems_TogetherOnlyWhenEachIsSound(t *testing.T) {
	files := map[string]string{
		"memo.v1.yaml": "id: memo\nversion: 1\nlinks:\n  - {name: about, inverse: mentioned_by, targets: [ghost]}\n",
		"note.v1.yaml": "id: note\nversion: 1\nfields:\n  - {id: today, type: date}\n",
	}
	ps := problemsOf(t, typedefParseDirErr(t, writeTree(t, files)))
	if len(ps) != 1 || ps[0].Code != typedef.CodeReservedName {
		t.Errorf("problems = %v, want only RESERVED_NAME in note.v1.yaml", ps)
	}

	files["note.v1.yaml"] = "id: note\nversion: 1\nfields:\n  - {id: f_today, type: date}\n"
	ps = problemsOf(t, typedefParseDirErr(t, writeTree(t, files)))
	if len(ps) != 1 || ps[0].Code != typedef.CodeUnknownType {
		t.Errorf("problems = %v, want only UNKNOWN_TYPE in memo.v1.yaml", ps)
	}
}
