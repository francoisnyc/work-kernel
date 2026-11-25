// Tests for "required means given by the mover" (docs/roadmap.md, track 1,
// item 3; the owner's decisions of Phase A): a transition's `given` lists
// fields of its `required` that the move's own patch must set. Field by
// field. Refused at load, where it could never be met or contradicts the
// Type:
//
//   - a field it does not declare (UNKNOWN_FIELD), twice (DUPLICATE), or
//     not in the same move's required (MISSING_COUNTERPART);
//   - a field no patch may name: only the kernel writes it
//     (WRITES_KERNEL_FIELD), the state the move leaves freezes it
//     (WRITES_FROZEN_FIELD), the move's own allowed names it (CONFLICT), or
//     the state the move leaves keeps it for the requester while the move
//     excepts the requester (CONFLICT; track 1 item 4's requester_only);
//   - a held move, by a check or a held reaction (HELD_TRANSITION, as for
//     clears and sets: supporting it is Later);
//   - a move the kernel makes, which carries no patch: a state's timer or
//     at-timer (TIMER_NOT_ALLOWED), the Type's own move reaction, or
//     another Type's (CONFLICT, on the reaction, as for a held target).
//
// The stored definition keeps the list; a transition without one stores
// nothing new.
package typedef_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// givenSrc is a Type whose review state has REVIEW's extra keys and whose
// review -> rejected move has MOVE's (YAML flow, inside the braces).
const givenSrc = `
id: gv
version: 1
initial_state: draft
fields:
  - {id: f_amount, type: int}
  - {id: f_reason, type: string}
  - {id: f_note,   type: string}
  - {id: f_owner,  type: principal}
  - {id: f_level,  type: int, writable_by: kernel}
  - {id: f_end_at, type: datetime}
states:
  - {id: draft,    category: todo}
  - {id: review,   category: waiting REVIEW}
  - {id: rejected, category: canceled, frozen: [f_reason]}
transitions:
  - {from: draft,  to: review, allowed: [creator]}
  - {from: review, to: rejected MOVE}
  - {from: review, to: draft, sets: {f_level: '0'}}
`

func given(review, move string) string {
	return strings.NewReplacer(" REVIEW", review, " MOVE", move).Replace(givenSrc)
}

func TestGiven_Parse(t *testing.T) {
	ty := mustParse(t, given("", ", required: [f_amount, f_reason], given: [f_reason]"))
	data, err := json.Marshal(ty) // as the item service stores it
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(data), `"given":["f_reason"]`) {
		t.Errorf("stored definition %s does not keep given: [f_reason]", data)
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

// A transition without given stores nothing new: every registered version
// keeps its stored definition byte for byte.
func TestGiven_AbsentStoresNothingNew(t *testing.T) {
	data, err := json.Marshal(mustParse(t, given("", ", required: [f_reason]")))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "given") {
		t.Errorf("stored definition %s names given", data)
	}
}

// What loads: a field the move's target freezes (the move entering a state
// may set what it freezes); a field the state left keeps for the requester,
// when the requester may make the move, or may hold its role.
func TestGiven_Loads(t *testing.T) {
	for name, src := range map[string]string{
		"frozen in the target":              given("", ", required: [f_reason], given: [f_reason]"),
		"requester_only, the creator moves": given(", requester_only: [f_note]", ", allowed: [creator], required: [f_note], given: [f_note]"),
		"requester_only, a role moves":      given(", requester_only: [f_note]", ", allowed: ['role:lead'], required: [f_note], given: [f_note]"),
	} {
		t.Run(name, func(t *testing.T) { mustParse(t, src) })
	}
}

// wantOneProblem: err holds exactly one problem, with this code at this
// path, whose message says each of says.
func wantOneProblem(t *testing.T, err error, code typedef.Code, path string, says ...string) {
	t.Helper()
	ps := problemsOf(t, err)
	if len(ps) != 1 {
		t.Fatalf("problems = %v, want one", ps)
	}
	p := ps[0]
	if p.Code != code || p.Path != path {
		t.Errorf("problem = %s at %s, want %s at %s (%s)", p.Code, p.Path, code, path, p.Message)
	}
	for _, s := range says {
		if !strings.Contains(p.Message, s) {
			t.Errorf("message %q does not say %q", p.Message, s)
		}
	}
}

func TestGiven_Problems(t *testing.T) {
	cases := []struct {
		name, review, move, path string
		code                     typedef.Code
		says                     []string
	}{
		{"a field it does not declare", "", ", required: [f_reason], given: [f_nope]",
			"transitions[1].given[0]", typedef.CodeUnknownField, []string{"f_nope", "given"}},
		{"a field listed twice", "", ", required: [f_reason], given: [f_reason, f_reason]",
			"transitions[1].given[1]", typedef.CodeDuplicate, []string{"f_reason"}},
		{"a field the move does not require", "", ", required: [f_note], given: [f_reason]",
			"transitions[1].given[0]", typedef.CodeMissingCounterpart, []string{"f_reason", "required"}},
		{"a field only the kernel writes", "", ", required: [f_level], given: [f_level]",
			"transitions[1].given[0]", typedef.CodeWritesKernelField, []string{"f_level", "kernel"}},
		{"a field the state it leaves freezes", ", frozen: [f_note]", ", required: [f_note], given: [f_note]",
			"transitions[1].given[0]", typedef.CodeWritesFrozenField, []string{"f_note", "review"}},
		{"a field its own allowed names", "", ", allowed: ['field:f_owner'], required: [f_owner], given: [f_owner]",
			"transitions[1].given[0]", typedef.CodeConflict, []string{"f_owner", "allowed"}},
		{"a field the state it leaves keeps for the requester, who may not make the move", ", requester_only: [f_note]",
			", allowed: ['role:lead'], except: [creator], required: [f_note], given: [f_note]",
			"transitions[1].given[0]", typedef.CodeConflict, []string{"f_note", "requester_only", "review"}},
		{"the same, anyone but the requester", ", requester_only: [f_note]", ", except: [creator], required: [f_note], given: [f_note]",
			"transitions[1].given[0]", typedef.CodeConflict, []string{"f_note", "requester_only"}},
		{"a move the state's timer makes", ", timer: {after: 48h, to: rejected}", ", required: [f_reason], given: [f_reason]",
			"transitions[1].given", typedef.CodeTimerNotAllowed, []string{"timer", "f_reason"}},
		{"a move the state's at-timer makes", ", at: {field: f_end_at, to: rejected}", ", required: [f_reason], given: [f_reason]",
			"transitions[1].given", typedef.CodeTimerNotAllowed, []string{"timer", "f_reason"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := typedef.Parse([]byte(given(c.review, c.move)))
			wantOneProblem(t, err, c.code, c.path, c.says...)
		})
	}
}

// A held move: by a check, or by a held reaction. Refused, as clears and
// sets are on a held move (the owner's decision: supporting it is Later).
func TestGiven_RefusedOnAHeldMove(t *testing.T) {
	check := given("", ", required: [f_reason], given: [f_reason]") +
		"hooks:\n  - {id: recheck, on: transition, from: review, to: rejected, kind: check, action: erp.check_credit, reads: [f_note, f_amount], writes: [f_note]}\n"
	_, err := typedef.Parse([]byte(check))
	wantOneProblem(t, err, typedef.CodeHeldTransition, "transitions[1].given", "recheck")

	held := swap(t, pledgeSrc, "{from: filed, to: paid}", "{from: filed, to: paid, required: [f_note], given: [f_note]}")
	_, err = typedef.Parse([]byte(held))
	wantOneProblem(t, err, typedef.CodeHeldTransition, "transitions[1].given", "take")
}

// The kernel's own move reactions carry no patch: a reaction may not make a
// move whose given it could never meet. The Type's own (created_inside,
// moving the container) and another Type's (moving its target), each
// refused on the reaction, where a held target transition is.
func TestGiven_RefusedOnAReactionsMove(t *testing.T) {
	good := map[string]string{
		"desk.yaml": desk(reopenR, checklistR), "remark.yaml": remarkSrc, "todo.yaml": todoSrc,
		"part.yaml": part(finishR), "whole.yaml": whole(wholeAccepts),
	}
	if err := parseSet(t, good); err != nil {
		t.Fatalf("the five Types together: %v", err)
	}
	cases := []struct {
		name, file, src, problemFile, path string
		says                               []string
	}{
		{"the Type's own reaction moves it", "desk.yaml",
			strings.Replace(desk(reopenR, checklistR), "guard: 'f_assignee != null'}", "guard: 'f_assignee != null', required: [f_title], given: [f_title]}", 1),
			"desk.yaml", "reactions[0].to", []string{"reopen", "f_title", "given"}},
		{"another Type's reaction moves it", "whole.yaml",
			strings.Replace(whole(wholeAccepts), "{from: building, to: done, allowed: ['role:lead']}",
				"{from: building, to: done, allowed: ['role:lead'], required: [f_name], given: [f_name]}", 1),
			"part.yaml", "reactions[0].to", []string{"finish", "f_name", "given"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			files := map[string]string{}
			for k, v := range good {
				files[k] = v
			}
			if !strings.Contains(c.src, "given:") {
				t.Fatalf("test bug: the swap did not apply")
			}
			files[c.file] = c.src
			ps := problemsOf(t, parseSet(t, files))
			if len(ps) != 1 {
				t.Fatalf("problems = %v, want one", ps)
			}
			p := ps[0]
			if p.Code != typedef.CodeConflict || p.Path != c.path || !strings.HasSuffix(p.Source, c.problemFile) {
				t.Errorf("problem = %s at %s in %s, want CONFLICT at %s in %s (%s)", p.Code, p.Path, p.Source, c.path, c.problemFile, p.Message)
			}
			for _, s := range c.says {
				if !strings.Contains(p.Message, s) {
					t.Errorf("message %q does not say %q", p.Message, s)
				}
			}
		})
	}
}

// The published JSON Schema says what given means.
func TestGiven_SchemaSaysIt(t *testing.T) {
	data, err := os.ReadFile("../../schema/type.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"given": {"description": "Required fields the move itself must give`) {
		t.Errorf("the schema's transition has no given saying the move itself must give the fields")
	}
}
