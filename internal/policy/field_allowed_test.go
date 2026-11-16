// Tests for proof-of-concept Step 2, part 1: `allowed` from a principal
// field. An entry field:<id> matches the principal the field holds when the
// transition runs. An empty field matches no one. The rule for agents is
// unchanged: the item service asks for the agent and for the user it acts
// for, and both must pass; a field holding user:bob never matches agent:x.
package policy_test

import (
	"testing"

	"github.com/francoisnyc/work-kernel/internal/policy"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

func TestAuthorizeTransitionOn_FieldEntries(t *testing.T) {
	bob := map[string]any{"f_approver": "user:bob", "f_title": "user:ana"}
	cases := []struct {
		name      string
		principal string
		roles     []string
		allowed   []string
		fields    map[string]any
		want      bool
	}{
		{"the field names the principal", "user:bob", nil, []string{"field:f_approver"}, bob, true},
		{"the field names someone else", "user:ana", nil, []string{"field:f_approver"}, bob, false},
		{"an agent never matches a field holding a user", "agent:triage-bot", nil, []string{"field:f_approver"}, bob, false},
		{"the field holds an agent: that agent matches", "agent:triage-bot", nil, []string{"field:f_approver"},
			map[string]any{"f_approver": "agent:triage-bot"}, true},
		{"the field is unset: no one", "user:bob", nil, []string{"field:f_approver"}, map[string]any{}, false},
		{"the field is null: no one", "user:bob", nil, []string{"field:f_approver"},
			map[string]any{"f_approver": nil}, false},
		{"no fields at all: no one", "user:bob", nil, []string{"field:f_approver"}, nil, false},
		{"the field or a role: the role matches", "user:carol", []string{"hr"}, []string{"field:f_approver", "role:hr"}, bob, true},
		{"the field or a principal: the field matches", "user:bob", nil, []string{"principal:user:carol", "field:f_approver"}, bob, true},
		// Only the named field counts, not any field holding the principal.
		{"another field holds the principal", "user:ana", nil, []string{"field:f_approver"}, bob, false},
		// A field entry is not a principal entry, nor a role.
		{"a principal named like the entry", "field:f_approver", nil, []string{"field:f_approver"}, bob, false},
		{"a role named like the field", "user:ana", []string{"f_approver"}, []string{"field:f_approver"}, bob, false},
		// What AuthorizeTransition did still holds.
		{"no list: anyone", "user:a", nil, nil, bob, true},
		{"named as a principal", "user:a", nil, []string{"principal:user:a"}, nil, true},
		{"holds the role", "user:a", []string{"support"}, []string{"role:support"}, nil, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tr := typedef.Transition{From: "submitted", To: "approved", Allowed: c.allowed}
			if got := policy.AuthorizeTransitionOn(c.principal, c.roles, tr, c.fields); got != c.want {
				t.Errorf("AuthorizeTransitionOn(%q, %v, allowed %v, %v) = %v, want %v",
					c.principal, c.roles, c.allowed, c.fields, got, c.want)
			}
		})
	}
}
