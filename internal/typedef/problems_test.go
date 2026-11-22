// Tests for Ventoux item 2a (docs/plans/ideas-from-ventoux.md): a Type
// definition is checked in full, and every problem comes back as data a tool
// can act on, {source, path, line, column, severity, code, message}, not as
// the first problem in a Go error string.
//
// The stages, the anchoring rule for positions and the Error() format are in
// problems.go. Each case below was checked against the code before item 2a:
// it was refused, its message contained `old` (each message keeps its old
// text as its start and adds what to do after it), and with `fix` applied it
// loaded. So each case has exactly one problem, and any extra one is a
// cascade the check should have dropped.
package typedef_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/typedef"
)

type problemCase struct {
	name      string
	code      typedef.Code
	src       string
	path      string
	line, col int
	typeID    string
	version   int
	old       string      // the message contains this text, from before item 2a
	fix       [][2]string // replacements (old, new) that make the Type load
}

// One case per code that a single Type can have. Positions were computed
// independently (PyYAML) from the path and the anchoring rule.
var oneTypeCases = []problemCase{
	{
		name: "a key typedef does not know", code: typedef.CodeUnknownKey,
		src: `id: memo
version: 1
initial_state: open
states:
  - {id: open, category: todo}
  - {id: done, category: done}
transitions:
  - {from: open, to: done, gaurd: 'true'}
`,
		path: "transitions[0].gaurd", line: 8, col: 28, typeID: "memo", version: 1,
		old: `"gaurd"`,
		fix: [][2]string{{`gaurd`, `guard`}},
	},
	{
		name: "text where a number goes", code: typedef.CodeWrongShape,
		src: `id: memo
version: one
`,
		path: "version", line: 2, col: 1, typeID: "", version: 0,
		old: `version`,
		fix: [][2]string{{`version: one`, `version: 1`}},
	},
	{
		name: "a field without an id", code: typedef.CodeRequired,
		src: `id: memo
version: 1
initial_state: open
fields:
  - {type: string}
states:
  - {id: open, category: todo}
  - {id: done, category: done}
`,
		path: "fields[0].id", line: 5, col: 5, typeID: "memo", version: 1,
		old: `a field has no id`,
		fix: [][2]string{{`{type: string}`, `{id: f_a, type: string}`}},
	},
	{
		name: "a field declared twice", code: typedef.CodeDuplicate,
		src: `id: memo
version: 1
initial_state: open
fields:
  - {id: f_title, type: string}
  - {id: f_title, type: int}
states:
  - {id: open, category: todo}
  - {id: done, category: done}
`,
		path: "fields[1].id", line: 6, col: 6, typeID: "memo", version: 1,
		old: `duplicate field "f_title"`,
		fix: [][2]string{{`{id: f_title, type: int}`, `{id: f_count, type: int}`}},
	},
	{
		name: "a reserved field name", code: typedef.CodeReservedName,
		src: `id: memo
version: 1
initial_state: open
fields:
  - {id: today, type: date}
states:
  - {id: open, category: todo}
  - {id: done, category: done}
`,
		path: "fields[0].id", line: 5, col: 6, typeID: "memo", version: 1,
		old: `"today" is a reserved name`,
		fix: [][2]string{{`id: today`, `id: f_today`}},
	},
	{
		name: "an invariant ID that is not a code", code: typedef.CodeInvalidName,
		src: `id: memo
version: 1
initial_state: open
states:
  - {id: open, category: todo}
  - {id: done, category: done}
invariants:
  - {id: NotACode, rule: 'true'}
`,
		path: "invariants[0].id", line: 8, col: 6, typeID: "memo", version: 1,
		old: `invariant "NotACode": the ID is a refusal code`,
		fix: [][2]string{{`NotACode`, `not_a_code`}},
	},
	{
		name: "an unknown category", code: typedef.CodeInvalidValue,
		src: `id: memo
version: 1
initial_state: open
states:
  - {id: open, category: doing}
`,
		path: "states[0].category", line: 5, col: 16, typeID: "memo", version: 1,
		old: `state open: unknown category "doing"`,
		fix: [][2]string{{`doing`, `active`}},
	},
	{
		name: "options on a string field", code: typedef.CodeKeyNotApplicable,
		src: `id: memo
version: 1
initial_state: open
fields:
  - {id: f_title, type: string, options: [a, b]}
states:
  - {id: open, category: todo}
  - {id: done, category: done}
`,
		path: "fields[0].options", line: 5, col: 33, typeID: "memo", version: 1,
		old: `field f_title: only enum fields take options`,
		fix: [][2]string{{`, options: [a, b]`, ``}},
	},
	{
		name: "a required field that is not declared", code: typedef.CodeUnknownField,
		src: `id: memo
version: 1
initial_state: open
states:
  - {id: open, category: todo}
  - {id: done, category: done}
transitions:
  - {from: open, to: done, required: [f_nope]}
`,
		path: "transitions[0].required[0]", line: 8, col: 39, typeID: "memo", version: 1,
		old: `required field "f_nope" is not declared`,
		fix: [][2]string{{`, required: [f_nope]`, ``}},
	},
	{
		name: "a transition to an unknown state", code: typedef.CodeUnknownState,
		src: `id: memo
version: 1
initial_state: open
states:
  - {id: open, category: todo}
  - {id: done, category: done}
transitions:
  - {from: open, to: closed}
`,
		path: "transitions[0].to", line: 8, col: 18, typeID: "memo", version: 1,
		old: `unknown state "closed"`,
		fix: [][2]string{{`to: closed`, `to: done`}},
	},
	{
		name: "a timer with no transition to take", code: typedef.CodeUnknownTransition,
		src: `id: memo
version: 1
initial_state: open
states:
  - {id: open, category: todo, timer: {after: 4h, to: done}}
  - {id: done, category: done}
`,
		path: "states[0].timer.to", line: 5, col: 51, typeID: "memo", version: 1,
		old: `timer moves to "done", but there is no transition open -> done`,
		fix: [][2]string{{`  - {id: done, category: done}
`, `  - {id: done, category: done}
transitions:
  - {from: open, to: done}
`}},
	},
	{
		name: "a rollup over an unknown link", code: typedef.CodeUnknownLink,
		src: `id: memo
version: 1
initial_state: open
states:
  - {id: open, category: todo}
  - {id: done, category: done}
rollups:
  - {id: open_tasks, over: nope, count_by: category}
`,
		path: "rollups[0].over", line: 8, col: 22, typeID: "memo", version: 1,
		old: `counts over "nope", which is not a link name`,
		fix: [][2]string{{`rollups:`, `links:
  - {name: nope, inverse: nope_of, targets: [memo]}
rollups:`}},
	},
	{
		name: "an acting timer naming an unknown hook", code: typedef.CodeUnknownHook,
		src: `id: memo
version: 1
initial_state: open
fields:
  - {id: f_title, type: string}
states:
  - id: open
    category: todo
    acting:
      - {id: remind, after: 48h, max: 1, hook: nope}
  - {id: done, category: done}
`,
		path: "states[0].acting[0].hook", line: 10, col: 42, typeID: "memo", version: 1,
		old: `acting timer "remind": unknown hook "nope"`,
		fix: [][2]string{{`  - {id: done, category: done}
`, `  - {id: done, category: done}
hooks:
  - {id: nope, on: timer, kind: action, action: email.notify, reads: [f_title]}
`}},
	},
	{
		name: "a hook on an unknown reaction", code: typedef.CodeUnknownReaction,
		src: `id: memo
version: 1
initial_state: open
fields:
  - {id: f_title, type: string}
states:
  - {id: open, category: todo}
  - {id: done, category: done}
hooks:
  - {id: mail, on: reaction, reaction: nope, kind: action, action: email.notify, reads: [f_title]}
`,
		path: "hooks[0].reaction", line: 10, col: 30, typeID: "memo", version: 1,
		old: `unknown reaction "nope"`,
		fix: [][2]string{{`hooks:
  - {id: mail, on: reaction, reaction: nope, kind: action, action: email.notify, reads: [f_title]}
`, ``}},
	},
	{
		name: "an unknown action", code: typedef.CodeUnknownAction,
		src: `id: memo
version: 1
initial_state: open
fields:
  - {id: f_title, type: string}
  - {id: f_plan, type: string, writable_by: kernel}
states:
  - {id: open, category: todo}
  - {id: done, category: done}
hooks:
  - {id: plan, on: enter, state: open, kind: enrich, action: crm.nope, reads: [f_title], writes: [f_plan]}
`,
		path: "hooks[0].action", line: 11, col: 54, typeID: "memo", version: 1,
		old: `unknown action "crm.nope"`,
		fix: [][2]string{{`crm.nope`, `crm.get_plan`}},
	},
	{
		name: "reads that do not match the action", code: typedef.CodeActionArity,
		src: `id: memo
version: 1
initial_state: open
fields:
  - {id: f_title, type: string}
  - {id: f_plan, type: string, writable_by: kernel}
states:
  - {id: open, category: todo}
  - {id: done, category: done}
hooks:
  - {id: plan, on: enter, state: open, kind: enrich, action: crm.get_plan, reads: [f_title, f_title], writes: [f_plan]}
`,
		path: "hooks[0].reads", line: 11, col: 76, typeID: "memo", version: 1,
		old: `action crm.get_plan takes 1 reads and 1 writes; the hook declares 2 and 1`,
		fix: [][2]string{{`reads: [f_title, f_title]`, `reads: [f_title]`}},
	},
	{
		name: "an at-timer on a string field", code: typedef.CodeWrongFieldType,
		src: `id: memo
version: 1
initial_state: open
fields:
  - {id: f_title, type: string}
states:
  - {id: open, category: todo, at: {field: f_title, to: done}}
  - {id: done, category: done}
transitions:
  - {from: open, to: done, allowed: ['principal:system:timer']}
`,
		path: "states[0].at.field", line: 7, col: 37, typeID: "memo", version: 1,
		old: `at names field "f_title", a string; it must be a datetime`,
		fix: [][2]string{{`  - {id: f_title, type: string}
`, `  - {id: f_title, type: string}
  - {id: f_due, type: datetime}
`}, {`field: f_title`, `field: f_due`}},
	},
	{
		name: "a guard that does not compile", code: typedef.CodeExpressionInvalid,
		src: `id: memo
version: 1
initial_state: open
fields:
  - {id: f_title, type: string}
states:
  - {id: open, category: todo}
  - {id: done, category: done}
transitions:
  - {from: open, to: done, guard: 'f_title =='}
`,
		path: "transitions[0].guard", line: 10, col: 28, typeID: "memo", version: 1,
		old: `guard "f_title ==": `,
		fix: [][2]string{{`'f_title =='`, `'f_title == "x"'`}},
	},
	{
		name: "a guard that is not a bool", code: typedef.CodeExpressionType,
		src: `id: memo
version: 1
initial_state: open
fields:
  - {id: f_count, type: int}
states:
  - {id: open, category: todo}
  - {id: done, category: done}
transitions:
  - {from: open, to: done, guard: 'f_count + 1'}
`,
		path: "transitions[0].guard", line: 10, col: 28, typeID: "memo", version: 1,
		old: `a guard must yield a bool, not int`,
		fix: [][2]string{{`'f_count + 1'`, `'f_count + 1 > 2'`}},
	},
	{
		name: "a guard reading a container's field", code: typedef.CodeReadNotAllowed,
		src: `id: memo
version: 1
initial_state: open
fields:
  - {id: f_title, type: string}
states:
  - {id: open, category: todo}
  - {id: done, category: done}
transitions:
  - {from: open, to: done, guard: 'container.f_title == "x"'}
`,
		path: "transitions[0].guard", line: 10, col: 28, typeID: "memo", version: 1,
		old: `it reads container.f_title; a guard reads the container's state and category only`,
		fix: [][2]string{{`container.f_title`, `f_title`}},
	},
	{
		name: "clearing a frozen field", code: typedef.CodeWritesFrozenField,
		src: `id: memo
version: 1
initial_state: open
fields:
  - {id: f_title, type: string}
states:
  - {id: open, category: todo, frozen: [f_title]}
  - {id: done, category: done}
transitions:
  - {from: open, to: done, clears: [f_title]}
`,
		path: "transitions[0].clears[0]", line: 10, col: 37, typeID: "memo", version: 1,
		old: `clears "f_title", which state open freezes`,
		fix: [][2]string{{`, frozen: [f_title]`, ``}},
	},
	{
		name: "clearing a kernel-written field", code: typedef.CodeWritesKernelField,
		src: `id: memo
version: 1
initial_state: open
fields:
  - {id: f_title, type: string}
  - {id: f_plan, type: string, writable_by: kernel}
states:
  - {id: open, category: todo}
  - {id: done, category: done}
transitions:
  - {from: open, to: done, clears: [f_plan]}
hooks:
  - {id: plan, on: enter, state: open, kind: enrich, action: crm.get_plan, reads: [f_title], writes: [f_plan]}
`,
		path: "transitions[0].clears[0]", line: 11, col: 37, typeID: "memo", version: 1,
		old: `clears "f_plan", which only the kernel writes`,
		fix: [][2]string{{`, clears: [f_plan]`, ``}},
	},
	{
		name: "a held transition that clears", code: typedef.CodeHeldTransition,
		src: `id: memo
version: 1
initial_state: open
fields:
  - {id: f_title, type: string}
  - {id: f_ok, type: string, writable_by: kernel}
states:
  - {id: open, category: todo}
  - {id: done, category: done}
transitions:
  - {from: open, to: done, clears: [f_title]}
hooks:
  - {id: credit, on: transition, from: open, to: done, kind: check, action: erp.get_credit_status, reads: [f_title], writes: [f_ok]}
`,
		path: "transitions[0].clears", line: 11, col: 28, typeID: "memo", version: 1,
		old: `it clears fields and is held by the check credit`,
		fix: [][2]string{{`, clears: [f_title]`, ``}},
	},
	{
		name: "requires and clears one field", code: typedef.CodeConflict,
		src: `id: memo
version: 1
initial_state: open
fields:
  - {id: f_title, type: string}
states:
  - {id: open, category: todo}
  - {id: done, category: done}
transitions:
  - {from: open, to: done, required: [f_title], clears: [f_title]}
`,
		path: "transitions[0].clears[0]", line: 10, col: 58, typeID: "memo", version: 1,
		old: `requires and clears "f_title"`,
		fix: [][2]string{{`required: [f_title], `, ``}},
	},
	{
		name: "two checks on one transition", code: typedef.CodeOnlyOneAllowed,
		src: `id: memo
version: 1
initial_state: open
fields:
  - {id: f_title, type: string}
  - {id: f_ok, type: string, writable_by: kernel}
states:
  - {id: open, category: todo}
  - {id: done, category: done}
transitions:
  - {from: open, to: done}
hooks:
  - {id: credit, on: transition, from: open, to: done, kind: check, action: erp.get_credit_status, reads: [f_title], writes: [f_ok]}
  - {id: credit2, on: transition, from: open, to: done, kind: check, action: erp.get_credit_status, reads: [f_title], writes: [f_ok]}
`,
		path: "hooks[1]", line: 14, col: 5, typeID: "memo", version: 1,
		old: `already has the check "credit"`,
		fix: [][2]string{{`  - {id: credit2, on: transition, from: open, to: done, kind: check, action: erp.get_credit_status, reads: [f_title], writes: [f_ok]}
`, ``}},
	},
	{
		name: "a kernel field no hook writes", code: typedef.CodeMissingCounterpart,
		src: `id: memo
version: 1
initial_state: open
fields:
  - {id: f_plan, type: string, writable_by: kernel}
states:
  - {id: open, category: todo}
  - {id: done, category: done}
`,
		path: "fields[0].writable_by", line: 5, col: 32, typeID: "memo", version: 1,
		old: `field f_plan is writable_by kernel, but no hook writes it`,
		fix: [][2]string{{`, writable_by: kernel`, ``}},
	},
	{
		name: "a timer its transition does not allow", code: typedef.CodeTimerNotAllowed,
		src: `id: memo
version: 1
initial_state: open
states:
  - {id: open, category: todo, timer: {after: 4h, to: done}}
  - {id: done, category: done}
transitions:
  - {from: open, to: done, allowed: ['role:lead']}
`,
		path: "transitions[0].allowed", line: 8, col: 28, typeID: "memo", version: 1,
		old: "whose `allowed` list must include principal:system:timer",
		fix: [][2]string{{`'role:lead'`, `'role:lead', 'principal:system:timer'`}},
	},
}

// problemsOf returns the Problems inside err, failing the test if err is
// not (or does not wrap) a Problems. errors.As is Go's instanceof-and-cast
// for errors: it walks the wrap chain and fills ps on a match.
func problemsOf(t *testing.T, err error) typedef.Problems {
	t.Helper()
	if err == nil {
		t.Fatal("no error, want Problems")
	}
	var ps typedef.Problems
	if !errors.As(err, &ps) {
		t.Fatalf("error %T %q is not a typedef.Problems", err, err)
	}
	return ps
}

// checkProblem compares one problem with a case. The source is checked by
// the caller.
func checkProblem(t *testing.T, p typedef.Problem, c problemCase) {
	t.Helper()
	if p.Code != c.code {
		t.Errorf("code = %s, want %s (message %q)", p.Code, c.code, p.Message)
	}
	if p.Path != c.path {
		t.Errorf("path = %q, want %q", p.Path, c.path)
	}
	if p.Line != c.line || p.Column != c.col {
		t.Errorf("position = %d:%d, want %d:%d", p.Line, p.Column, c.line, c.col)
	}
	if p.TypeID != c.typeID || p.Version != c.version {
		t.Errorf("Type = %q v%d, want %q v%d", p.TypeID, p.Version, c.typeID, c.version)
	}
	if p.Severity != typedef.SeverityError {
		t.Errorf("severity = %q, want ERROR", p.Severity)
	}
	if !strings.Contains(p.Message, c.old) {
		t.Errorf("message %q does not contain %q", p.Message, c.old)
	}
	// What is wrong, then what to do: the message goes on after the old text.
	if i := strings.Index(p.Message, c.old); i >= 0 && strings.TrimSpace(p.Message[i+len(c.old):]) == "" && c.code != typedef.CodeUnknownKey && c.code != typedef.CodeWrongShape {
		t.Errorf("message %q says what is wrong but not what to do", p.Message)
	}
}

// Each kind of problem a single Type can have comes back alone, with its
// code, path, position, Type, severity and message.
func TestProblems_OneOfEachKind(t *testing.T) {
	for _, c := range oneTypeCases {
		t.Run(c.name, func(t *testing.T) {
			ty, err := typedef.Parse([]byte(c.src))
			if ty != nil {
				t.Error("Parse returned a Type with its problems; want nil")
			}
			ps := problemsOf(t, err)
			if len(ps) != 1 {
				t.Fatalf("%d problems, want 1:\n%v", len(ps), ps)
			}
			if ps[0].Source != "" {
				t.Errorf("source = %q, want none from Parse", ps[0].Source)
			}
			checkProblem(t, ps[0], c)
		})
	}
}

// The fixtures are honest: with the one problem fixed, each Type loads.
func TestProblems_TheFixLoads(t *testing.T) {
	for _, c := range oneTypeCases {
		src := c.src
		for _, r := range c.fix {
			src = strings.Replace(src, r[0], r[1], 1)
		}
		if _, err := typedef.Parse([]byte(src)); err != nil {
			t.Errorf("%s, fixed: %v", c.name, err)
		}
	}
}

// Every problem of a Type, in one answer, in YAML order: four independent
// problems in a Type that once stopped at the first.
func TestProblems_EveryProblemOfAType(t *testing.T) {
	src := `id: memo
version: 1
initial_state: open
fields:
  - {id: f_title, type: string}
  - {id: f_plan, type: string, writable_by: kernel}
  - {id: f_count, type: int}
states:
  - {id: open, category: todo}
  - {id: done, category: finished}
transitions:
  - {from: open, to: done, guard: 'f_count + 1'}
  - {from: done, to: closed}
`
	ps := problemsOf(t, mustNotParse(t, src))
	want := []struct {
		code      typedef.Code
		path      string
		line, col int
	}{
		{typedef.CodeMissingCounterpart, "fields[1].writable_by", 6, 32},
		{typedef.CodeInvalidValue, "states[1].category", 10, 16},
		{typedef.CodeExpressionType, "transitions[0].guard", 12, 28},
		{typedef.CodeUnknownState, "transitions[1].to", 13, 18},
	}
	if len(ps) != len(want) {
		t.Fatalf("%d problems, want %d:\n%v", len(ps), len(want), ps)
	}
	for i, w := range want {
		p := ps[i]
		if p.Code != w.code || p.Path != w.path || p.Line != w.line || p.Column != w.col {
			t.Errorf("problem %d = %s %s %d:%d, want %s %s %d:%d", i, p.Code, p.Path, p.Line, p.Column, w.code, w.path, w.line, w.col)
		}
	}
}

func mustNotParse(t *testing.T, src string) error {
	t.Helper()
	_, err := typedef.Parse([]byte(src))
	if err == nil {
		t.Fatal("Parse accepted the Type, want problems")
	}
	return err
}

// A declaration with a problem still counts as declared, so nothing that
// only follows from it is reported: one problem each, not a cascade.
func TestProblems_NoCascade(t *testing.T) {
	cases := []struct {
		name string
		src  string
		code typedef.Code
		path string
	}{
		{"a field of an unknown type, required and read by a guard", `id: memo
version: 1
initial_state: open
fields:
  - {id: f_due, type: dat}
states:
  - {id: open, category: todo}
  - {id: done, category: done}
transitions:
  - {from: open, to: done, required: [f_due], guard: 'f_due != null'}
`, typedef.CodeInvalidValue, "fields[0].type"},
		{"a state declared twice, then used", `id: memo
version: 1
initial_state: open
states:
  - {id: open, category: todo}
  - {id: open, category: active}
  - {id: done, category: done}
transitions:
  - {from: open, to: done}
`, typedef.CodeDuplicate, "states[1].id"},
		{"a state of an unknown category, then entered", `id: memo
version: 1
initial_state: open
states:
  - {id: open, category: todo}
  - {id: done, category: finished}
transitions:
  - {from: open, to: done}
`, typedef.CodeInvalidValue, "states[1].category"},
		{"a hook with an unknown action, then named by a timer", `id: memo
version: 1
initial_state: open
fields:
  - {id: f_title, type: string}
states:
  - id: open
    category: todo
    acting:
      - {id: remind, after: 48h, max: 1, hook: nag}
  - {id: done, category: done}
hooks:
  - {id: nag, on: timer, kind: action, action: email.nope, reads: [f_title]}
`, typedef.CodeUnknownAction, "hooks[0].action"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ps := problemsOf(t, mustNotParse(t, c.src))
			if len(ps) != 1 || ps[0].Code != c.code || ps[0].Path != c.path {
				t.Errorf("problems = %v, want one %s at %s", ps, c.code, c.path)
			}
		})
	}
}

// Stage (a) first: a file that is not YAML gives one problem, with the
// parser's line and no column, and nothing else is checked.
func TestProblems_SyntaxErrorAlone(t *testing.T) {
	ps := problemsOf(t, mustNotParse(t, "id: memo\nversion: 1\nfields: [\n  {id: a\n"))
	if len(ps) != 1 {
		t.Fatalf("%d problems, want 1:\n%v", len(ps), ps)
	}
	p := ps[0]
	if p.Code != typedef.CodeYAMLSyntax || p.Path != "" || p.Line != 3 || p.Column != 0 || p.TypeID != "" {
		t.Errorf("problem = %+v, want YAML_SYNTAX at line 3, no column, no path, no Type", p)
	}
	if !strings.Contains(p.Message, "did not find expected") {
		t.Errorf("message %q does not carry the parser's words", p.Message)
	}
}

// Every unknown key is reported, at every depth (yaml.v3 alone stops at the
// first custom decoder that fails), and stage (b) does not run: the
// duplicate field is not reported.
func TestProblems_EveryUnknownKey(t *testing.T) {
	src := `id: memo
version: 1
gaurd: x
fields:
  - {id: f_a, type: string, colour: red}
  - {id: f_a, type: string}
links:
  - {name: blocks, inverse: blocked_by, targets: [memo], maxx: one}
`
	ps := problemsOf(t, mustNotParse(t, src))
	var got []string
	for _, p := range ps {
		got = append(got, string(p.Code)+" "+p.Path)
	}
	want := []string{"UNKNOWN_KEY gaurd", "UNKNOWN_KEY fields[0].colour", "UNKNOWN_KEY links[0].maxx"}
	if !slices.Equal(got, want) {
		t.Errorf("problems = %v, want %v", got, want)
	}
}

// Every value of the wrong shape is reported, and stage (b) does not run.
func TestProblems_EveryWrongShape(t *testing.T) {
	src := `id: memo
version: one
fields: {id: f_a, type: string}
states:
  - {id: open, category: todo}
  - {id: open, category: todo}
`
	ps := problemsOf(t, mustNotParse(t, src))
	var got []string
	for _, p := range ps {
		got = append(got, string(p.Code)+" "+p.Path)
	}
	want := []string{"WRONG_SHAPE version", "WRONG_SHAPE fields"}
	if !slices.Equal(got, want) {
		t.Errorf("problems = %v, want %v", got, want)
	}
}

// Error() lists one problem per line, so a log still reads well and a
// caller that only checks err != nil keeps working.
func TestProblems_ErrorLines(t *testing.T) {
	ps := typedef.Problems{
		{Source: "types/memo.v1.yaml", Path: "fields[1].id", Line: 6, Column: 6, TypeID: "memo", Version: 1,
			Severity: typedef.SeverityError, Code: typedef.CodeDuplicate, Message: `duplicate field "f_title"; rename one`},
		{Line: 3, Severity: typedef.SeverityError, Code: typedef.CodeYAMLSyntax, Message: "did not find expected ','"},
		{Path: "states[0].category", TypeID: "memo", Version: 2,
			Severity: typedef.SeverityError, Code: typedef.CodeInvalidValue, Message: `state open: unknown category "doing"; use one of ...`},
	}
	want := `types/memo.v1.yaml:6:6: DUPLICATE typedef memo v1: duplicate field "f_title"; rename one
3: YAML_SYNTAX did not find expected ','
INVALID_VALUE typedef memo v2: state open: unknown category "doing"; use one of ...`
	if got := ps.Error(); got != want {
		t.Errorf("Error() =\n%s\nwant\n%s", got, want)
	}
}

// A stored definition (JSON, from Postgres) has paths but no positions.
func TestProblems_FromJSON(t *testing.T) {
	_, err := typedef.FromJSON([]byte(`{"id":"memo","version":1,"initial_state":"open","states":[{"id":"open","category":"doing"}]}`))
	ps := problemsOf(t, err)
	if len(ps) != 1 {
		t.Fatalf("%d problems, want 1:\n%v", len(ps), ps)
	}
	p := ps[0]
	if p.Code != typedef.CodeInvalidValue || p.Path != "states[0].category" || p.Line != 0 || p.Column != 0 ||
		p.TypeID != "memo" || p.Version != 1 {
		t.Errorf("problem = %+v, want INVALID_VALUE at states[0].category, no position, memo v1", p)
	}
}
