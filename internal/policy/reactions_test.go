// Tests for proof-of-concept Step 2, part 2: the policy side of link
// reactions and role-written fields.
//
//   - A reaction (reaction:<type>:<id>) is a kernel actor like system: and
//     hook:. It never counts as the requester or as having taken part in
//     creating the Item, even when it carries the creator as on_behalf_of
//     (the kernel fills on_behalf_of from the cause, for attribution only).
//   - writable_by: [role:<name>, ...] lets only holders of one of the roles
//     write the field on the public door; an agent and the user it acts for
//     must both hold one, the same rule as `allowed`.
package policy_test

import (
	"testing"

	"github.com/francoisnyc/work-kernel/internal/policy"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

const deduct = "reaction:vacation_request:deduct"

func TestIsRequester_ReactionIsNeverTheRequester(t *testing.T) {
	cases := []struct {
		name                         string
		actor, onBehalfOf            string
		createdBy, createdOnBehalfOf string
	}{
		{"a reaction on behalf of the creator", deduct, "user:ana", "user:ana", ""},
		{"a reaction on behalf of the user an agent created it for", deduct, "user:ana", "agent:bot", "user:ana"},
		{"a reaction acting alone", deduct, "", "user:ana", ""},
		{"a reaction recorded as the creator", deduct, "", deduct, ""},
		// The same rows held for the other kernel actors before; they still do.
		{"a hook on behalf of the creator", "hook:vacation_request:hr_check", "user:ana", "user:ana", ""},
		{"the timer on behalf of the creator", "system:timer", "user:ana", "user:ana", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if policy.IsRequester(c.actor, c.onBehalfOf, c.createdBy, c.createdOnBehalfOf) {
				t.Errorf("IsRequester(%q for %q; created by %q for %q) = true, want false: a kernel actor is never the requester",
					c.actor, c.onBehalfOf, c.createdBy, c.createdOnBehalfOf)
			}
		})
	}
}

func TestTookPartInCreation_ReactionNever(t *testing.T) {
	cases := []struct {
		name                         string
		actor, onBehalfOf            string
		createdBy, createdOnBehalfOf string
	}{
		{"a reaction on behalf of the creator", deduct, "user:ana", "user:ana", ""},
		{"a reaction on behalf of the user an agent created it for", deduct, "user:ana", "agent:bot", "user:ana"},
		{"a reaction recorded as the creator", deduct, "", deduct, ""},
		{"a hook on behalf of the creator", "hook:vacation_request:hr_check", "user:ana", "user:ana", ""},
		{"the timer on behalf of the creator", "system:timer", "user:ana", "user:ana", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if policy.TookPartInCreation(c.actor, c.onBehalfOf, c.createdBy, c.createdOnBehalfOf) {
				t.Errorf("TookPartInCreation(%q for %q; created by %q for %q) = true, want false",
					c.actor, c.onBehalfOf, c.createdBy, c.createdOnBehalfOf)
			}
		})
	}
}

// MayTransition with `allowed: [creator]` or `except: [creator]`: a
// reaction carrying the creator as on_behalf_of matches neither.
func TestMayTransition_ReactionIsNotTheCreator(t *testing.T) {
	it := policy.ItemFacts{CreatedBy: "user:ana"}
	who := policy.Who{Actor: deduct, OnBehalfOf: "user:ana"}
	if policy.MayTransition(typedef.Transition{Allowed: []string{typedef.AllowedCreator}}, who, it) {
		t.Error("allowed: [creator] let a reaction in on behalf of the creator")
	}
	if !policy.MayTransition(typedef.Transition{Except: []string{typedef.AllowedCreator}}, who, it) {
		t.Error("except: [creator] refused a reaction on behalf of the creator: it created nothing")
	}
}

func TestMayWriteRoleField(t *testing.T) {
	admin := typedef.Field{ID: "f_allowance", Type: typedef.Int, WritableByRoles: []string{"hr_admin"}}
	either := typedef.Field{ID: "f_owner", Type: typedef.Principal, WritableByRoles: []string{"hr_admin", "payroll"}}
	open := typedef.Field{ID: "f_note", Type: typedef.String}
	cases := []struct {
		name  string
		field typedef.Field
		who   policy.Who
		want  bool
	}{
		{"a user holding the role", admin, policy.Who{Actor: "user:hana", ActorRoles: []string{"hr_admin"}}, true},
		{"a user without it", admin, policy.Who{Actor: "user:ana"}, false},
		{"a user with another role", admin, policy.Who{Actor: "user:ana", ActorRoles: []string{"approver"}}, false},
		{"one of several roles", either, policy.Who{Actor: "user:pat", ActorRoles: []string{"payroll"}}, true},
		{"a role named like the field", admin, policy.Who{Actor: "user:ana", ActorRoles: []string{"f_allowance"}}, false},
		{"an agent holding it, for a user holding it", admin, policy.Who{
			Actor: "agent:bot", ActorRoles: []string{"hr_admin"}, OnBehalfOf: "user:hana", OnBehalfOfRoles: []string{"hr_admin"},
		}, true},
		{"an agent without it, for a user holding it", admin, policy.Who{
			Actor: "agent:bot", OnBehalfOf: "user:hana", OnBehalfOfRoles: []string{"hr_admin"},
		}, false},
		{"an agent holding it, for a user without it", admin, policy.Who{
			Actor: "agent:bot", ActorRoles: []string{"hr_admin"}, OnBehalfOf: "user:ana",
		}, false},
		{"a field without roles: anyone", open, policy.Who{Actor: "user:ana"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := policy.MayWriteRoleField(c.field, c.who); got != c.want {
				t.Errorf("MayWriteRoleField(%s, %+v) = %v, want %v", c.field.ID, c.who, got, c.want)
			}
		})
	}
}
