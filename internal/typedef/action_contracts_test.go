// Tests for Ventoux V1: a hook is checked against its action's contract when
// its Type loads (stage 2, each Type on its own; no network). Arity was
// checked before V1; V1 adds:
//
//   - the hook's kind must be one the action serves: ACTION_KIND, at
//     hooks[i].kind;
//   - each read goes into an input of its type, and each output into a
//     field of its type: WRONG_FIELD_TYPE, at hooks[i].reads[k] or
//     hooks[i].writes[k].
//
// The types (docs/design.md, "Action contracts"): a string input takes a
// string or an enum field; an enum input an enum field whose options are all
// among its values. A string output goes into a string field; an enum output
// into a string field, or an enum field holding every one of its values.
// Otherwise the types are equal: int, bool, date, datetime, and principal,
// which created_by, created_on_behalf_of, creator and container:<offer>
// are. Nullability is not checked at load: any field may be unset.
//
// No cascades: a read or write that is already a problem (an unknown field,
// a multi-value field, a write of created_by), an unknown action, or a hook
// whose arity is wrong gets no type problem on top.
package typedef_test

import (
	"strings"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// One case per kind of contract problem, each the only problem of its Type.
// Positions were computed independently (PyYAML) from the path and the
// anchoring rule. old is the start of the message: what is wrong, before
// what to do.
var contractCases = []problemCase{
	{
		name: "a check naming an enrich action", code: typedef.CodeActionKind,
		src: `id: memo
version: 1
initial_state: open
fields:
  - {id: f_title, type: string}
  - {id: f_plan, type: string}
states:
  - {id: open, category: todo}
  - {id: done, category: done}
transitions:
  - {from: open, to: done}
hooks:
  - {id: plan, on: transition, from: open, to: done, kind: check, action: crm.get_plan, reads: [f_title], writes: [f_plan]}
`,
		path: "hooks[0].kind", line: 13, col: 54, typeID: "memo", version: 1,
		old: `action crm.get_plan@1 is for enrich hooks, not check`,
		fix: [][2]string{{`on: transition, from: open, to: done, kind: check`, `on: enter, state: open, kind: enrich`}},
	},
	{
		name: "an int field into a string input", code: typedef.CodeWrongFieldType,
		src: `id: memo
version: 1
initial_state: open
fields:
  - {id: f_title, type: string}
  - {id: f_count, type: int}
  - {id: f_plan, type: string}
states:
  - {id: open, category: todo}
  - {id: done, category: done}
hooks:
  - {id: plan, on: enter, state: open, kind: enrich, action: crm.get_plan, reads: [f_count], writes: [f_plan]}
`,
		path: "hooks[0].reads[0]", line: 12, col: 84, typeID: "memo", version: 1,
		old: `reads f_count (int) into input customerId of crm.get_plan@1, which takes a string`,
		fix: [][2]string{{`reads: [f_count]`, `reads: [f_title]`}},
	},
	{
		name: "a principal into a string input", code: typedef.CodeWrongFieldType,
		src: `id: memo
version: 1
initial_state: open
fields:
  - {id: f_title, type: string}
  - {id: f_ok, type: bool}
states:
  - {id: open, category: todo}
  - {id: done, category: done}
hooks:
  - {id: credit, on: enter, state: open, kind: enrich, action: erp.get_credit_status, reads: [created_by], writes: [f_ok]}
`,
		path: "hooks[0].reads[0]", line: 11, col: 95, typeID: "memo", version: 1,
		old: `reads created_by (principal) into input customerId of erp.get_credit_status@1, which takes a string`,
		fix: [][2]string{{`reads: [created_by]`, `reads: [f_title]`}},
	},
	{
		name: "a string field into a principal input", code: typedef.CodeWrongFieldType,
		src: `id: memo
version: 1
initial_state: open
fields:
  - {id: f_title, type: string}
states:
  - {id: open, category: todo}
  - {id: done, category: done}
hooks:
  - {id: mail, on: enter, state: done, kind: action, action: email.notify, reads: [f_title]}
`,
		path: "hooks[0].reads[0]", line: 10, col: 84, typeID: "memo", version: 1,
		old: `reads f_title (string) into input to of email.notify@1, which takes a principal`,
		fix: [][2]string{{`reads: [f_title]`, `reads: [creator]`}},
	},
	{
		name: "an enum output into an enum field without all its values", code: typedef.CodeWrongFieldType,
		src: `id: memo
version: 1
initial_state: open
fields:
  - {id: f_title, type: string}
  - {id: f_plan, type: enum, options: [free, pro]}
states:
  - {id: open, category: todo}
  - {id: done, category: done}
hooks:
  - {id: plan, on: enter, state: open, kind: enrich, action: crm.get_plan, reads: [f_title], writes: [f_plan]}
`,
		path: "hooks[0].writes[0]", line: 11, col: 103, typeID: "memo", version: 1,
		old: `writes output plan of crm.get_plan@1 (enum: free, pro, enterprise) into f_plan, which does not have enterprise`,
		fix: [][2]string{{`options: [free, pro]`, `options: [free, pro, enterprise]`}},
	},
	{
		name: "a string output into an enum field", code: typedef.CodeWrongFieldType,
		src: `id: memo
version: 1
initial_state: open
fields:
  - {id: f_customer, type: string}
  - {id: f_amount, type: int}
  - {id: f_level, type: enum, options: [a, b]}
states:
  - {id: open, category: todo}
  - {id: done, category: done}
transitions:
  - {from: open, to: done}
hooks:
  - {id: credit, on: transition, from: open, to: done, kind: check, action: erp.check_credit, reads: [f_customer, f_amount], writes: [f_level]}
`,
		path: "hooks[0].writes[0]", line: 14, col: 135, typeID: "memo", version: 1,
		old: `writes output costCenter of erp.check_credit@1 (string) into f_level (enum)`,
		fix: [][2]string{{`{id: f_level, type: enum, options: [a, b]}`, `{id: f_level, type: string}`}},
	},
	{
		name: "a bool output into a string field", code: typedef.CodeWrongFieldType,
		src: `id: memo
version: 1
initial_state: open
fields:
  - {id: f_customer, type: string}
  - {id: f_note, type: string}
states:
  - {id: open, category: todo}
  - {id: done, category: done}
hooks:
  - {id: credit, on: enter, state: open, kind: enrich, action: erp.get_credit_status, reads: [f_customer], writes: [f_note]}
`,
		path: "hooks[0].writes[0]", line: 11, col: 117, typeID: "memo", version: 1,
		old: `writes output onHold of erp.get_credit_status@1 (bool) into f_note (string)`,
		fix: [][2]string{{`{id: f_note, type: string}`, `{id: f_note, type: bool}`}},
	},
	{
		name: "a principal output into a string field", code: typedef.CodeWrongFieldType,
		src: `id: memo
version: 1
initial_state: open
fields:
  - {id: f_title, type: string}
  - {id: f_owner, type: principal}
states:
  - {id: open, category: todo}
  - {id: done, category: done}
transitions:
  - {from: open, to: done}
hooks:
  - {id: who, on: transition, from: open, to: done, kind: check, action: hr.manager_of, reads: [created_on_behalf_of, created_by], writes: [f_title]}
`,
		path: "hooks[0].writes[0]", line: 13, col: 141, typeID: "memo", version: 1,
		old: `writes output manager of hr.manager_of@1 (principal) into f_title (string)`,
		fix: [][2]string{{`writes: [f_title]`, `writes: [f_owner]`}},
	},
}

// Each contract problem comes back alone, with its code, path, position,
// Type, severity and message.
func TestActionContracts_OneProblemEach(t *testing.T) {
	for _, c := range contractCases {
		t.Run(c.name, func(t *testing.T) {
			ps := problemsOf(t, mustNotParse(t, c.src))
			if len(ps) != 1 {
				t.Fatalf("%d problems, want 1:\n%v", len(ps), ps)
			}
			checkProblem(t, ps[0], c)
		})
	}
}

// The fixtures are honest: with the one problem fixed, each Type loads.
func TestActionContracts_TheFixLoads(t *testing.T) {
	for _, c := range contractCases {
		src := c.src
		for _, r := range c.fix {
			if !strings.Contains(src, r[0]) {
				t.Fatalf("%s: test bug: %q is not in the source", c.name, r[0])
			}
			src = strings.Replace(src, r[0], r[1], 1)
		}
		if _, err := typedef.Parse([]byte(src)); err != nil {
			t.Errorf("%s, fixed: %v", c.name, err)
		}
	}
}

// Every pair the rules allow loads: an enum field into a string input, an
// enum output into a string field and into an enum field with more values,
// the creator attributes and principal fields into principal inputs, a
// principal output into a principal field, int and bool on both sides, an
// explicit major.
func TestActionContracts_WhatFits(t *testing.T) {
	src := `id: fits
version: 1
initial_state: open
fields:
  - {id: f_kind,     type: enum, options: [a, b]}
  - {id: f_customer, type: string}
  - {id: f_amount,   type: int}
  - {id: f_text,     type: string}
  - {id: f_plan,     type: enum, options: [free, pro, enterprise, unknown]}
  - {id: f_ok,       type: bool}
  - {id: f_ref,      type: string}
  - {id: f_owner,    type: principal}
  - {id: f_approver, type: principal, writable_by: kernel}
states:
  - {id: open,     category: todo}
  - {id: asked,    category: waiting}
  - {id: approved, category: active}
  - {id: done,     category: done}
transitions:
  - {from: open, to: asked}
  - {from: asked, to: approved}
  - {from: approved, to: done}
hooks:
  - {id: plan_text, on: enter, state: open, kind: enrich, action: crm.get_plan, reads: [f_kind], writes: [f_text]}
  - {id: plan, on: enter, state: open, kind: enrich, action: crm.get_plan@1, reads: [f_customer], writes: [f_plan]}
  - {id: credit, on: enter, state: open, kind: enrich, action: erp.get_credit_status, reads: [f_customer], writes: [f_ok]}
  - {id: who, on: transition, from: open, to: asked, kind: check, action: hr.manager_of, reads: [created_on_behalf_of, created_by], writes: [f_approver]}
  - {id: limit, on: transition, from: asked, to: approved, kind: check, action: erp.check_credit, reads: [f_kind, f_amount], writes: [f_ref]}
  - {id: invoice, on: enter, state: approved, kind: action, action: erp.create_invoice, reads: [f_customer, f_amount], writes: [f_ref]}
  - {id: tell_owner, on: enter, state: done, kind: action, action: email.notify, reads: [f_owner]}
  - {id: tell_creator, on: enter, state: done, kind: action, action: email.notify, reads: [creator]}
`
	if _, err := typedef.Parse([]byte(src)); err != nil {
		t.Errorf("a Type whose hooks all fit their contracts: %v", err)
	}
}

// A principal offered by the container is a principal: email.notify may
// read it (sla_goal's breach mail does).
func TestActionContracts_AnOfferIsAPrincipal(t *testing.T) {
	src := `id: goal
version: 1
initial_state: open
fields:
  - {id: f_title, type: string}
states:
  - id: open
    category: todo
    acting:
      - {id: remind, after: 48h, max: 1, hook: tell}
  - {id: done, category: done}
hooks:
  - {id: tell, on: timer, kind: action, action: email.notify, reads: ['container:contact']}
`
	if _, err := typedef.Parse([]byte(src)); err != nil {
		t.Errorf("email.notify reading container:contact: %v", err)
	}
	wrong := strings.Replace(src, "action: email.notify, reads: ['container:contact']",
		"action: erp.create_invoice, reads: ['container:contact', 'container:contact'], writes: [f_title]", 1)
	ps := problemsOf(t, mustNotParse(t, wrong))
	if len(ps) != 2 || ps[0].Code != typedef.CodeWrongFieldType || ps[0].Path != "hooks[0].reads[0]" ||
		ps[1].Code != typedef.CodeWrongFieldType || ps[1].Path != "hooks[0].reads[1]" {
		t.Errorf("a container's principal into a string and an int input: want two WRONG_FIELD_TYPE on the reads, got\n%v", ps)
	}
}

// What is already a problem gets no contract problem on top; independent
// problems are each reported.
func TestActionContracts_NoCascades(t *testing.T) {
	base := `id: memo
version: 1
initial_state: open
fields:
  - {id: f_title, type: string}
  - {id: f_count, type: int}
  - {id: f_tags, type: string, multiple: true}
  - {id: f_plan, type: string}
states:
  - {id: open, category: todo}
  - {id: done, category: done}
transitions:
  - {from: open, to: done}
hooks:
  - HOOK
`
	cases := []struct {
		name, hook string
		want       []typedef.Code
		paths      []string
	}{
		{"an unknown action", "{id: h, on: enter, state: open, kind: enrich, action: crm.nope, reads: [f_count], writes: [f_plan]}",
			[]typedef.Code{typedef.CodeUnknownAction}, []string{"hooks[0].action"}},
		{"the wrong arity and a mistyped read", "{id: h, on: enter, state: open, kind: enrich, action: crm.get_plan, reads: [f_count, f_title], writes: [f_plan]}",
			[]typedef.Code{typedef.CodeActionArity}, []string{"hooks[0].reads"}},
		{"an unknown field", "{id: h, on: enter, state: open, kind: enrich, action: crm.get_plan, reads: [f_ghost], writes: [f_plan]}",
			[]typedef.Code{typedef.CodeUnknownField}, []string{"hooks[0].reads[0]"}},
		{"a multi-value field", "{id: h, on: enter, state: open, kind: enrich, action: crm.get_plan, reads: [f_tags], writes: [f_plan]}",
			[]typedef.Code{typedef.CodeWrongFieldType}, []string{"hooks[0].reads[0]"}},
		{"a write of created_by", "{id: h, on: enter, state: open, kind: enrich, action: crm.get_plan, reads: [f_title], writes: [created_by]}",
			[]typedef.Code{typedef.CodeInvalidValue}, []string{"hooks[0].writes[0]"}},
		{"the wrong kind and a mistyped read", "{id: h, on: transition, from: open, to: done, kind: check, action: crm.get_plan, reads: [f_count], writes: [f_plan]}",
			[]typedef.Code{typedef.CodeActionKind, typedef.CodeWrongFieldType}, []string{"hooks[0].kind", "hooks[0].reads[0]"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ps := problemsOf(t, mustNotParse(t, strings.Replace(base, "HOOK", c.hook, 1)))
			if len(ps) != len(c.want) {
				t.Fatalf("%d problems, want %v at %v:\n%v", len(ps), c.want, c.paths, ps)
			}
			for i := range ps {
				if ps[i].Code != c.want[i] || ps[i].Path != c.paths[i] {
					t.Errorf("problem %d = %s at %s, want %s at %s", i, ps[i].Code, ps[i].Path, c.want[i], c.paths[i])
				}
			}
		})
	}
}
