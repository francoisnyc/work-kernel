// Tests for presentation hints on a Type, at load (the Inbox for people,
// PR 1a: docs/plans/inbox-for-people.md and the owner's decisions of its
// Phase A, 2026-11-27). Hints only: they enforce nothing and decide
// nothing.
//
//   - `label` and `help` on a field, a state and a transition: plain text
//     (one line, no control characters, no space at either end, none of
//     < > or a backtick); a label is 1 to 60 characters, a help 1 to 280
//     (characters, not bytes). Otherwise INVALID_VALUE.
//   - The same label twice among the states, among the fields, or among
//     the moves out of one state: DUPLICATE, on the second (compared
//     without regard to case: a reader cannot tell Approve from approve).
//   - `primary: true` on a transition: at most one per source state
//     (ONLY_ONE_ALLOWED, on the second); never on a move no person can
//     make, one only a reaction or the timer makes (KEY_NOT_APPLICABLE).
//   - `summary: [field ids]` on the Type: fields it declares
//     (UNKNOWN_FIELD), each once (DUPLICATE), at most 3 (INVALID_VALUE).
//   - `due: <field>` on a state: a field it declares (UNKNOWN_FIELD) that
//     holds a datetime (WRONG_FIELD_TYPE).
//
// The stored definition keeps the keys; a Type without them stores
// nothing new, so no registered version changes.
package typedef_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// hintsSrc is an approval with every hint. Cases change it with
// strings.Replace on the exact text below.
const hintsSrc = `
id: hw
version: 1
initial_state: open
summary: [f_title, f_owner]
fields:
  - {id: f_title,  type: string,    label: Request, help: What the person asks for}
  - {id: f_owner,  type: principal, label: Approver}
  - {id: f_due_at, type: datetime,  label: Decision due}
  - {id: f_day,    type: date}
  - {id: f_count,  type: int}
states:
  - {id: open,    category: waiting, label: Waiting for a decision, help: The approver decides, due: f_due_at}
  - {id: done,    category: done,    label: Approved, timer: {after: 24h, to: closed}}
  - {id: dropped, category: canceled, label: Refused}
  - {id: closed,  category: done,    label: Closed}
transitions:
  - {from: open,    to: done,    allowed: ['field:f_owner'], label: Approve, help: Pays it out, primary: true}
  - {from: open,    to: dropped, allowed: ['field:f_owner'], label: Refuse}
  - {from: done,    to: closed,  allowed: ['principal:system:timer']}
  - {from: dropped, to: open,    allowed: [nobody]}
`

// hints returns hintsSrc with each pair of old, new replaced once.
func hints(t *testing.T, pairs ...string) string {
	t.Helper()
	src := hintsSrc
	for i := 0; i+1 < len(pairs); i += 2 {
		if !strings.Contains(src, pairs[i]) {
			t.Fatalf("hintsSrc has no %q", pairs[i])
		}
		src = strings.Replace(src, pairs[i], pairs[i+1], 1)
	}
	return src
}

func TestHints_Parse(t *testing.T) {
	ty := mustParse(t, hints(t))
	if got := strings.Join(ty.Summary, ","); got != "f_title,f_owner" {
		t.Errorf("summary = %q, want f_title,f_owner", got)
	}
	if f, _ := ty.Field("f_title"); f.Label != "Request" || f.Help != "What the person asks for" {
		t.Errorf("f_title label, help = %q, %q", f.Label, f.Help)
	}
	if f, _ := ty.Field("f_day"); f.Label != "" || f.Help != "" {
		t.Errorf("f_day label, help = %q, %q, want none", f.Label, f.Help)
	}
	if s, _ := ty.State("open"); s.Label != "Waiting for a decision" || s.Help != "The approver decides" || s.Due != "f_due_at" {
		t.Errorf("open label, help, due = %q, %q, %q", s.Label, s.Help, s.Due)
	}
	if s, _ := ty.State("done"); s.Label != "Approved" || s.Help != "" || s.Due != "" {
		t.Errorf("done label, help, due = %q, %q, %q", s.Label, s.Help, s.Due)
	}
	if tr, _ := ty.Transition("open", "done"); tr.Label != "Approve" || tr.Help != "Pays it out" || !tr.Primary {
		t.Errorf("open -> done label, help, primary = %q, %q, %v", tr.Label, tr.Help, tr.Primary)
	}
	if tr, _ := ty.Transition("open", "dropped"); tr.Label != "Refuse" || tr.Help != "" || tr.Primary {
		t.Errorf("open -> dropped label, help, primary = %q, %q, %v", tr.Label, tr.Help, tr.Primary)
	}

	data, err := json.Marshal(ty) // as the item service stores it
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, want := range []string{
		`"summary":["f_title","f_owner"]`,
		`"label":"Request","help":"What the person asks for"`,
		`"label":"Waiting for a decision","help":"The approver decides","due":"f_due_at"`,
		`"label":"Approve","help":"Pays it out","primary":true`,
	} {
		if !strings.Contains(string(data), want) {
			t.Errorf("stored definition %s does not keep %s", data, want)
		}
	}
	if n := strings.Count(string(data), `"primary"`); n != 1 {
		t.Errorf("stored definition names primary %d times, want once", n)
	}
	back, err := typedef.FromJSON(data)
	if err != nil {
		t.Fatalf("FromJSON(%s): %v", data, err)
	}
	again, err := json.Marshal(back)
	if err != nil || string(again) != string(data) {
		t.Errorf("after the round trip the stored definition is\n  %s\nwant\n  %s", again, data)
	}
}

// hintKeys are the stored names of the hints; a Type without hints names
// none of them.
var hintKeys = []string{`"label"`, `"help"`, `"primary"`, `"summary"`, `"due"`}

// A Type without hints stores nothing new: every Type in types/ keeps its
// stored definition byte for byte, so no registered version changes. The
// four versions PR 1b added with hints (the owner's approved change) are
// the only ones that store them, and they do.
func TestHints_AbsentStoresNothingNew(t *testing.T) {
	bare := mustParse(t, `
id: hw_bare
version: 1
initial_state: open
fields:
  - {id: f_title, type: string}
states:
  - {id: open, category: todo}
  - {id: done, category: done}
transitions:
  - {from: open, to: done, primary: false}
`)
	types, err := typedef.ParseDir("../../types")
	if err != nil {
		t.Fatalf("ParseDir: %v", err)
	}
	// The hint keys each of PR 1b's versions stores.
	withHints := map[string][]string{
		"approval@2":          {`"label"`, `"primary"`},
		"access_request@2":    {`"label"`, `"primary"`, `"summary"`, `"due"`},
		"equipment_request@2": {`"label"`, `"primary"`},
		"incident@7":          {`"label"`, `"primary"`, `"summary"`, `"due"`},
	}
	seen := 0
	for _, ty := range append([]*typedef.Type{bare}, types...) {
		data, err := json.Marshal(ty)
		if err != nil {
			t.Fatal(err)
		}
		if keys, ok := withHints[fmt.Sprintf("%s@%d", ty.ID, ty.Version)]; ok {
			seen++
			for _, k := range keys {
				if !strings.Contains(string(data), k) {
					t.Errorf("%s@%d: stored definition does not name %s", ty.ID, ty.Version, k)
				}
			}
			continue
		}
		for _, k := range hintKeys {
			if strings.Contains(string(data), k) {
				t.Errorf("%s@%d: stored definition names %s", ty.ID, ty.Version, k)
			}
		}
	}
	if seen != len(withHints) {
		t.Errorf("found %d of the %d versions with hints in types/", seen, len(withHints))
	}
}

// A draft that adds a hint to a registered version is another definition:
// VERSION_CHANGED, as for any other key. The registered version itself, as
// stored before hints existed, is the same version.
func TestHints_AddedToARegisteredVersionIsAChange(t *testing.T) {
	bare := `
id: hw_reg
version: 1
initial_state: open
states:
  - {id: open, category: todo}
  - {id: done, category: done}
transitions:
  - {from: open, to: done}
`
	// The stored form of hw_reg@1, as json.Marshal wrote it before hints.
	stored := []byte(`{"id":"hw_reg","version":1,"initial_state":"open","states":[{"id":"open","category":"todo"},{"id":"done","category":"done"}],"transitions":[{"from":"open","to":"done"}]}`)
	if _, err := typedef.Check([]typedef.Source{{Name: "hw_reg.v1.yaml", Content: []byte(bare)}}, [][]byte{stored}); err != nil {
		t.Errorf("the registered version as it was: %v, want no problem", err)
	}
	labelled := strings.Replace(bare, "{from: open, to: done}", "{from: open, to: done, label: Finish}", 1)
	_, err := typedef.Check([]typedef.Source{{Name: "hw_reg.v1.yaml", Content: []byte(labelled)}}, [][]byte{stored})
	ps := problemsOf(t, err)
	if len(ps) != 1 || ps[0].Code != typedef.CodeVersionChanged {
		t.Errorf("problems = %v, want one VERSION_CHANGED", ps)
	}
}

func TestHints_Loads(t *testing.T) {
	long := strings.Repeat("é", 60) // 60 characters, 120 bytes
	for name, src := range map[string]string{
		"every hint": hints(t),
		"no hints at all": `
id: hw
version: 1
initial_state: open
states:
  - {id: open, category: todo}
  - {id: done, category: done}
transitions:
  - {from: open, to: done}
`,
		"a label of 60 characters":                  hints(t, "label: Approve,", "label: "+long+","),
		"a help of 280 characters":                  hints(t, "help: Pays it out,", "help: "+strings.Repeat("a", 280)+","),
		"punctuation in a label":                    hints(t, "label: Approve,", `label: "Approve (once, for good): yes & no / maybe?",`),
		"the same label on moves out of two states": hints(t, "{from: dropped, to: open,    allowed: [nobody]}", "{from: dropped, to: open,    allowed: [nobody], label: Refuse}"),
		"a state and a move with one label":         hints(t, "label: Refuse}", "label: Refused}"),
		"a primary out of each of two states":       hints(t, "{from: dropped, to: open,    allowed: [nobody]}", "{from: dropped, to: open,    allowed: ['role:hw_team'], primary: true}"),
		"primary: false beside a primary":           hints(t, "label: Refuse}", "label: Refuse, primary: false}"),
		"a primary for the requester":               hints(t, "allowed: ['field:f_owner'], label: Approve", "allowed: [creator], label: Approve"),
		"a primary anyone on staff makes":           hints(t, "allowed: ['field:f_owner'], label: Approve", "label: Approve"),
		"a summary of 3 fields":                     hints(t, "summary: [f_title, f_owner]", "summary: [f_title, f_owner, f_count]"),
		"a summary naming a kernel field":           hints(t, "summary: [f_title, f_owner]", "summary: [f_due_at]"),
		"an empty summary":                          hints(t, "summary: [f_title, f_owner]", "summary: []"),
		"due on a done state":                       hints(t, "label: Closed}", "label: Closed, due: f_due_at}"),
	} {
		t.Run(name, func(t *testing.T) { mustParse(t, src) })
	}
}

func TestHints_Problems(t *testing.T) {
	long := strings.Repeat("é", 61)
	cases := []struct {
		name, src, path string
		code            typedef.Code
		says            []string
	}{
		// Plain text: a label.
		{"a label of 61 characters", hints(t, "label: Approve,", "label: "+long+","),
			"transitions[0].label", typedef.CodeInvalidValue, []string{"60"}},
		{"an empty label", hints(t, "label: Approver}", `label: ""}`),
			"fields[1].label", typedef.CodeInvalidValue, []string{"f_owner"}},
		{"a label of two lines", hints(t, "label: Approved,", `label: "Approved\nfor good",`),
			"states[1].label", typedef.CodeInvalidValue, []string{"done"}},
		{"a tab in a label", hints(t, "label: Approved,", `label: "Approved\tnow",`),
			"states[1].label", typedef.CodeInvalidValue, nil},
		{"a space before a label", hints(t, "label: Refuse}", `label: " Refuse"}`),
			"transitions[1].label", typedef.CodeInvalidValue, nil},
		{"a space after a label", hints(t, "label: Refuse}", `label: "Refuse "}`),
			"transitions[1].label", typedef.CodeInvalidValue, nil},
		{"markup in a label", hints(t, "label: Refused}", `label: "<b>Refused</b>"}`),
			"states[2].label", typedef.CodeInvalidValue, []string{"plain text"}},
		{"a > in a label", hints(t, "label: Refused}", `label: "Refused > closed"}`),
			"states[2].label", typedef.CodeInvalidValue, nil},
		{"a backtick in a label", hints(t, "label: Request,", "label: \"`f_title`\","),
			"fields[0].label", typedef.CodeInvalidValue, nil},
		// Plain text: a help.
		{"a help of 281 characters", hints(t, "help: Pays it out,", "help: "+strings.Repeat("a", 281)+","),
			"transitions[0].help", typedef.CodeInvalidValue, []string{"280"}},
		{"an empty help", hints(t, "help: The approver decides,", `help: "",`),
			"states[0].help", typedef.CodeInvalidValue, nil},
		{"a help of two lines", hints(t, "help: What the person asks for}", `help: "What\nthey ask"}`),
			"fields[0].help", typedef.CodeInvalidValue, nil},
		{"markup in a help", hints(t, "help: Pays it out,", `help: "Pays <i>it</i> out",`),
			"transitions[0].help", typedef.CodeInvalidValue, nil},
		// The same label twice.
		{"two states with one label", hints(t, "label: Closed}", "label: Approved}"),
			"states[3].label", typedef.CodeDuplicate, []string{"Approved", "done", "closed"}},
		{"two states with one label, in another case", hints(t, "label: Closed}", "label: approved}"),
			"states[3].label", typedef.CodeDuplicate, []string{"approved"}},
		{"two fields with one label", hints(t, "label: Decision due}", "label: Approver}"),
			"fields[2].label", typedef.CodeDuplicate, []string{"Approver", "f_owner", "f_due_at"}},
		{"two moves out of one state with one label", hints(t, "label: Refuse}", "label: Approve}"),
			"transitions[1].label", typedef.CodeDuplicate, []string{"Approve", "open"}},
		// primary.
		{"two primaries out of one state", hints(t, "label: Refuse}", "label: Refuse, primary: true}"),
			"transitions[1].primary", typedef.CodeOnlyOneAllowed, []string{"open"}},
		{"a primary only the timer makes", hints(t, "allowed: ['principal:system:timer']}", "allowed: ['principal:system:timer'], primary: true}"),
			"transitions[2].primary", typedef.CodeKeyNotApplicable, []string{"done", "closed"}},
		{"a primary only a reaction makes", hints(t, "allowed: [nobody]}", "allowed: [nobody], primary: true}"),
			"transitions[3].primary", typedef.CodeKeyNotApplicable, []string{"dropped", "open"}},
		// summary.
		{"a summary naming a field the Type lacks", hints(t, "summary: [f_title, f_owner]", "summary: [f_title, f_nope]"),
			"summary[1]", typedef.CodeUnknownField, []string{"f_nope"}},
		{"a summary naming a field twice", hints(t, "summary: [f_title, f_owner]", "summary: [f_title, f_title]"),
			"summary[1]", typedef.CodeDuplicate, []string{"f_title"}},
		{"a summary of 4 fields", hints(t, "summary: [f_title, f_owner]", "summary: [f_title, f_owner, f_count, f_day]"),
			"summary", typedef.CodeInvalidValue, []string{"3"}},
		// due.
		{"due naming a field the Type lacks", hints(t, "due: f_due_at}", "due: f_nope}"),
			"states[0].due", typedef.CodeUnknownField, []string{"f_nope"}},
		{"due naming a date field", hints(t, "due: f_due_at}", "due: f_day}"),
			"states[0].due", typedef.CodeWrongFieldType, []string{"f_day", "datetime"}},
		{"due naming a text field", hints(t, "due: f_due_at}", "due: f_title}"),
			"states[0].due", typedef.CodeWrongFieldType, []string{"f_title", "datetime"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := typedef.Parse([]byte(c.src))
			wantOneProblem(t, err, c.code, c.path, c.says...)
		})
	}
}

// Stage (a): a hint of the wrong shape.
func TestHints_WrongShape(t *testing.T) {
	for name, c := range map[string]struct{ src, path string }{
		"a label that is a list":   {hints(t, "label: Approve,", "label: [Approve],"), "transitions[0].label"},
		"a primary that is a word": {hints(t, "primary: true", "primary: maybe"), "transitions[0].primary"},
		"a summary that is a word": {hints(t, "summary: [f_title, f_owner]", "summary: f_title"), "summary"},
		"a due that is a list":     {hints(t, "due: f_due_at}", "due: [f_due_at]}"), "states[0].due"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := typedef.Parse([]byte(c.src))
			wantOneProblem(t, err, typedef.CodeWrongShape, c.path)
		})
	}
}
