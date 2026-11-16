// Tests for proof-of-concept Step 2, part 1 (Phase B review): identity rules
// are declared, never written in CEL.
//
//   - allowed: [creator] matches the principal whose effective user (its
//     on_behalf_of if set, else its actor) is the Item's effective creator
//     (created_on_behalf_of if set, else created_by). It is judged once,
//     for the principal as a whole. The kernel's own actors never match.
//   - except: [creator] refuses the creator even if `allowed` lets them in.
//   - role:, principal: and field: entries keep the agent rule: the actor
//     and the user it acts for must both pass.
//
// The tables keep every row of the guards they replace (the vacation
// request's no-self-approval and requester guards), plus the case the CEL
// requester guard got wrong: an agent that created a request for ana, now
// acting for bob, is not the creator.
package policy_test

import (
	"testing"

	"github.com/francoisnyc/work-kernel/internal/policy"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

func TestIsCreator(t *testing.T) {
	cases := []struct {
		name                         string
		actor, onBehalfOf            string
		createdBy, createdOnBehalfOf string
		want                         bool
	}{
		{"the user who created it", "user:ana", "", "user:ana", "", true},
		{"another user", "user:bob", "", "user:ana", "", false},
		{"the user an agent created it for", "user:ana", "", "agent:bot", "user:ana", true},
		{"an agent acting for the user who created it", "agent:bot", "user:ana", "user:ana", "", true},
		{"another agent acting for the user an agent created it for", "agent:other", "user:ana", "agent:bot", "user:ana", true},
		{"the agent that created it for ana, now acting for bob", "agent:bot", "user:bob", "agent:bot", "user:ana", false},
		{"the agent that created it for ana, acting for itself", "agent:bot", "", "agent:bot", "user:ana", false},
		{"an agent that created it for itself, acting for itself", "agent:bot", "", "agent:bot", "", true},
		{"the timer, never", "system:timer", "", "system:timer", "", false},
		{"a hook, never", "hook:t:h", "", "hook:t:h", "", false},
		{"nobody acting", "", "", "user:ana", "", false},
		{"no creator recorded", "user:ana", "", "", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := policy.IsCreator(c.actor, c.onBehalfOf, c.createdBy, c.createdOnBehalfOf); got != c.want {
				t.Errorf("IsCreator(%q for %q; created by %q for %q) = %v, want %v",
					c.actor, c.onBehalfOf, c.createdBy, c.createdOnBehalfOf, got, c.want)
			}
		})
	}
}

// decide is the vacation request's approve and reject; requester is its
// submit, recall and withdraw.
var (
	decide    = typedef.Transition{From: "submitted", To: "approved", Allowed: []string{"field:f_approver"}, Except: []string{"creator"}}
	requester = typedef.Transition{From: "submitted", To: "draft", Allowed: []string{"creator"}}
)

type identityCase struct {
	name                         string
	actor, onBehalfOf            string
	createdBy, createdOnBehalfOf string
	approver                     string // f_approver
	want                         bool
}

func runIdentity(t *testing.T, tr typedef.Transition, cases []identityCase) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fields := map[string]any{}
			if c.approver != "" {
				fields["f_approver"] = c.approver
			}
			who := policy.Who{Actor: c.actor, OnBehalfOf: c.onBehalfOf}
			it := policy.ItemFacts{Fields: fields, CreatedBy: c.createdBy, CreatedOnBehalfOf: c.createdOnBehalfOf}
			if got := policy.MayTransition(tr, who, it); got != c.want {
				t.Errorf("MayTransition(%s -> %s, %q for %q; created by %q for %q; approver %q) = %v, want %v",
					tr.From, tr.To, c.actor, c.onBehalfOf, c.createdBy, c.createdOnBehalfOf, c.approver, got, c.want)
			}
		})
	}
}

// The decision: whoever f_approver names, except the creator. Each row of
// the old no-self-approval guard, with the approver set so that `allowed`
// would let the principal in where it can, so `except` is what refuses.
func TestMayTransition_DecideExceptCreator(t *testing.T) {
	runIdentity(t, decide, []identityCase{
		{"the manager decides a user's request", "user:bob", "", "user:ana", "", "user:bob", true},
		{"the requester decides her own request, named approver", "user:ana", "", "user:ana", "", "user:ana", false},
		{"the requester decides her own request, bob named", "user:ana", "", "user:ana", "", "user:bob", false},
		{"the user an agent created it for decides it, named approver", "user:ana", "", "agent:bot", "user:ana", "user:ana", false},
		{"the agent that created it decides it (alone, not named)", "agent:bot", "", "agent:bot", "user:ana", "user:bob", false},
		{"an agent acting for the requester decides it", "agent:bot", "user:ana", "user:ana", "", "user:ana", false},
		{"an agent acting for the requester, created by an agent for her", "agent:other", "user:ana", "agent:bot", "user:ana", "user:ana", false},
		{"the manager decides a request an agent created for its user", "user:bob", "", "agent:bot", "user:ana", "user:bob", true},
		// Not the creator, but `allowed` keeps the agent rule: agent:bot is
		// not user:bob.
		{"an agent acting for the manager", "agent:bot", "user:bob", "user:ana", "", "user:bob", false},
		// The review's rows: one Item, created by agent:bot for ana.
		{"same Item: bob, the approver", "user:bob", "", "agent:bot", "user:ana", "user:bob", true},
		{"same Item: agent:bot for ana is the creator", "agent:bot", "user:ana", "agent:bot", "user:ana", "user:ana", false},
		{"same Item: agent:bot for bob still fails field:f_approver", "agent:bot", "user:bob", "agent:bot", "user:ana", "user:bob", false},
		{"no approver yet: no one", "user:bob", "", "user:ana", "", "", false},
	})
}

// Submit, recall, withdraw: the creator only. Each row of the old requester
// guard, plus the case it got wrong.
func TestMayTransition_AllowedCreator(t *testing.T) {
	runIdentity(t, requester, []identityCase{
		{"the requester", "user:ana", "", "user:ana", "", "", true},
		{"the user an agent created it for", "user:ana", "", "agent:bot", "user:ana", "", true},
		{"an agent acting for the requester", "agent:bot", "user:ana", "user:ana", "", "", true},
		{"the manager", "user:bob", "", "user:ana", "", "user:bob", false},
		{"an agent acting for the manager", "agent:bot", "user:bob", "user:ana", "", "user:bob", false},
		// Judged once for the principal as a whole: the agent itself is not
		// "the creator", its user is, and that is enough.
		{"agent:bot for ana, on a request agent:bot created for ana", "agent:bot", "user:ana", "agent:bot", "user:ana", "", true},
		{"agent:bot for bob, on a request agent:bot created for ana", "agent:bot", "user:bob", "agent:bot", "user:ana", "", false},
		{"agent:bot alone, on a request it created for ana", "agent:bot", "", "agent:bot", "user:ana", "", false},
		{"the timer", "system:timer", "", "user:ana", "", "", false},
		{"a hook", "hook:vacation_request:hr_check", "", "user:ana", "", "", false},
	})
}

// creator combines with the other entries: any one entry that matches lets
// the principal in; role:, principal: and field: entries still need both
// the actor and its user.
func TestMayTransition_CreatorAmongOtherEntries(t *testing.T) {
	tr := typedef.Transition{From: "a", To: "b", Allowed: []string{"creator", "role:hr"}}
	cases := []struct {
		name string
		who  policy.Who
		want bool
	}{
		{"the creator, no role", policy.Who{Actor: "user:ana"}, true},
		{"an hr user", policy.Who{Actor: "user:carol", ActorRoles: []string{"hr"}}, true},
		{"an agent for an hr user, the agent without the role", policy.Who{Actor: "agent:bot", OnBehalfOf: "user:carol",
			OnBehalfOfRoles: []string{"hr"}}, false},
		{"an agent for an hr user, both with the role", policy.Who{Actor: "agent:bot", ActorRoles: []string{"hr"},
			OnBehalfOf: "user:carol", OnBehalfOfRoles: []string{"hr"}}, true},
		{"an agent for the creator", policy.Who{Actor: "agent:bot", OnBehalfOf: "user:ana"}, true},
		{"someone else", policy.Who{Actor: "user:dan"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := policy.MayTransition(tr, c.who, policy.ItemFacts{CreatedBy: "user:ana"}); got != c.want {
				t.Errorf("MayTransition(%+v) = %v, want %v", c.who, got, c.want)
			}
		})
	}
}

// except with no allowed list: anyone but the creator. The kernel's own
// actors are never the creator, so except never stops them.
func TestMayTransition_ExceptAlone(t *testing.T) {
	tr := typedef.Transition{From: "a", To: "b", Except: []string{"creator"}}
	it := policy.ItemFacts{CreatedBy: "user:ana"}
	for _, c := range []struct {
		who  policy.Who
		want bool
	}{
		{policy.Who{Actor: "user:ana"}, false},
		{policy.Who{Actor: "agent:bot", OnBehalfOf: "user:ana"}, false},
		{policy.Who{Actor: "user:bob"}, true},
		{policy.Who{Actor: "system:timer"}, true},
		{policy.Who{Actor: "hook:t:h"}, true},
	} {
		if got := policy.MayTransition(tr, c.who, it); got != c.want {
			t.Errorf("MayTransition(except creator, %+v) = %v, want %v", c.who, got, c.want)
		}
	}
}

// What AuthorizeTransition did, MayTransition still does, for the actor and
// the user it acts for alike.
func TestMayTransition_OldEntries(t *testing.T) {
	it := policy.ItemFacts{CreatedBy: "user:ana"}
	cases := []struct {
		name    string
		allowed []string
		who     policy.Who
		want    bool
	}{
		{"no list: anyone", nil, policy.Who{Actor: "user:x"}, true},
		{"role held", []string{"role:support"}, policy.Who{Actor: "user:x", ActorRoles: []string{"support"}}, true},
		{"role not held", []string{"role:support"}, policy.Who{Actor: "user:x"}, false},
		{"principal named", []string{"principal:user:x"}, policy.Who{Actor: "user:x"}, true},
		{"timer named", []string{"principal:system:timer"}, policy.Who{Actor: "system:timer"}, true},
		{"agent for a named user, agent not named", []string{"principal:user:x"},
			policy.Who{Actor: "agent:bot", OnBehalfOf: "user:x"}, false},
		{"agent and user both named", []string{"principal:user:x", "principal:agent:bot"},
			policy.Who{Actor: "agent:bot", OnBehalfOf: "user:x"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tr := typedef.Transition{From: "a", To: "b", Allowed: c.allowed}
			if got := policy.MayTransition(tr, c.who, it); got != c.want {
				t.Errorf("MayTransition(allowed %v, %+v) = %v, want %v", c.allowed, c.who, got, c.want)
			}
		})
	}
}
