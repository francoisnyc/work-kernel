// Tests for the one pure transition function. No database, no Temporal: the
// same function serves the kernel now and the workflow in Step 3.
package lifecycle_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/lifecycle"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

const docType = `
id: doc
version: 1
initial_state: draft
fields:
  - {id: f_owner, type: string}
  - {id: f_score, type: int}
  - {id: f_approved, type: bool}
  - {id: f_kind, type: enum, options: [memo, report]}
states:
  - {id: draft, category: backlog}
  - {id: review, category: active}
  - {id: published, category: done}
  - {id: dropped, category: canceled}
transitions:
  - {from: draft, to: review, guard: 'f_owner is set'}
  - {from: review, to: draft}
  - from: review
    to: published
    guard: 'f_approved == true && f_score == 3 && f_kind != "memo"'
    required: [f_owner]
  - {from: draft, to: dropped, guard: 'f_owner != "user-root"'}
`

func mustParse(t *testing.T) *typedef.Type {
	t.Helper()
	ty, err := typedef.Parse([]byte(docType))
	if err != nil {
		t.Fatalf("parse doc Type: %v", err)
	}
	return ty
}

// Shorthand for a field map.
type fields = map[string]any

func TestTransition_Legal(t *testing.T) {
	ty := mustParse(t)
	cases := []struct {
		name         string
		from         string
		fields       fields
		to           string
		wantCategory string
	}{
		{"no guard", "review", nil, "draft", "backlog"},
		{"'is set' guard holds", "draft", fields{"f_owner": "user-1"}, "review", "active"},
		{"all conditions of an && guard hold", "review",
			fields{"f_owner": "user-1", "f_approved": true, "f_score": 3, "f_kind": "report"}, "published", "done"},
		// Numbers read back from Postgres JSON arrive as float64.
		{"int guard matches a float64 value", "review",
			fields{"f_owner": "user-1", "f_approved": true, "f_score": float64(3), "f_kind": "report"}, "published", "done"},
		{"!= holds when the field is unset", "review",
			fields{"f_owner": "user-1", "f_approved": true, "f_score": 3}, "published", "done"},
		{"!= holds for another value", "draft", fields{"f_owner": "user-2"}, "dropped", "canceled"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, err := lifecycle.Transition(ty, c.from, c.fields, c.to)
			if err != nil {
				t.Fatalf("Transition: %v", err)
			}
			if res.State != c.to || res.Category != c.wantCategory {
				t.Errorf("result = %+v, want state %q in category %q", res, c.to, c.wantCategory)
			}
		})
	}
}

func TestTransition_Illegal_IsRejectedWithAReason(t *testing.T) {
	ty := mustParse(t)
	cases := []struct {
		name         string
		from         string
		fields       fields
		to           string
		wantCode     lifecycle.Code
		wantInReason string
	}{
		{"no such transition", "draft", fields{"f_owner": "user-1"}, "published", lifecycle.NoTransition, "published"},
		{"unknown target state", "draft", nil, "limbo", lifecycle.NoTransition, "limbo"},
		{"unknown current state", "limbo", nil, "draft", lifecycle.NoTransition, "limbo"},
		{"same state", "draft", nil, "draft", lifecycle.NoTransition, "draft"},
		{"'is set' guard fails", "draft", nil, "review", lifecycle.GuardFailed, "f_owner is set"},
		{"'is set' fails on null", "draft", fields{"f_owner": nil}, "review", lifecycle.GuardFailed, "f_owner is set"},
		{"== fails on another value", "review",
			fields{"f_owner": "user-1", "f_approved": false, "f_score": 3}, "published", lifecycle.GuardFailed, "f_approved"},
		{"== fails when the field is unset", "review",
			fields{"f_owner": "user-1", "f_approved": true}, "published", lifecycle.GuardFailed, "f_score"},
		{"!= fails on the excluded value", "review",
			fields{"f_owner": "user-1", "f_approved": true, "f_score": 3, "f_kind": "memo"}, "published", lifecycle.GuardFailed, "f_kind"},
		{"required field missing", "review",
			fields{"f_approved": true, "f_score": 3}, "published", lifecycle.MissingFields, "f_owner"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := lifecycle.Transition(ty, c.from, c.fields, c.to)
			if err == nil {
				t.Fatal("Transition accepted an illegal move")
			}
			// errors.As finds an error of a given type in the chain and copies it
			// out, like catching a specific exception class and reading its fields.
			var rej *lifecycle.Rejection
			if !errors.As(err, &rej) {
				t.Fatalf("error %v is not a *lifecycle.Rejection", err)
			}
			if rej.Code != c.wantCode {
				t.Errorf("code = %q, want %q (reason: %s)", rej.Code, c.wantCode, rej.Reason)
			}
			if !strings.Contains(rej.Reason, c.wantInReason) {
				t.Errorf("reason %q does not mention %q", rej.Reason, c.wantInReason)
			}
		})
	}
}

// Reasons end up in logs and API errors, so they name field IDs, never values.
func TestTransition_ReasonNeverContainsFieldValues(t *testing.T) {
	ty := mustParse(t)
	_, err := lifecycle.Transition(ty, "review",
		fields{"f_owner": "jane.doe@example.com", "f_approved": false, "f_score": 3}, "published")
	if err == nil {
		t.Fatal("Transition accepted an illegal move")
	}
	if strings.Contains(err.Error(), "jane.doe@example.com") {
		t.Errorf("reason leaks a field value: %v", err)
	}
}
