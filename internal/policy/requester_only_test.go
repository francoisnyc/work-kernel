// Tests for "only the requester" for drafts (docs/roadmap.md, track 1,
// item 4): while an Item is in a state that lists a field under
// requester_only, only its requester writes that field on the public door.
// The requester is IsRequester's: the effective user (the user an agent
// acts for, else the actor) is the Item's effective creator
// (created_on_behalf_of, else created_by). MayWriteInState answers that
// part alone; writable_by is MayWriteField's, asked as well.
package policy_test

import (
	"testing"

	"github.com/francoisnyc/work-kernel/internal/policy"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

func TestMayWriteInState(t *testing.T) {
	draft := typedef.State{ID: "draft", Category: "todo", RequesterOnly: []string{"f_amount", "f_vendor"}}
	review := typedef.State{ID: "review", Category: "waiting", Frozen: []string{"f_amount", "f_vendor"}}
	var (
		byAna       = policy.ItemFacts{CreatedBy: "user:ana"}
		botForAna   = policy.ItemFacts{CreatedBy: "agent:bot", CreatedOnBehalfOf: "user:ana"}
		byBot       = policy.ItemFacts{CreatedBy: "agent:bot"}
		byKim       = policy.ItemFacts{CreatedBy: "customer:kim"}
		byReaction  = policy.ItemFacts{CreatedBy: "reaction:r1"}
		staff       = []string{"staff"}
		ana         = policy.Who{Actor: "user:ana", ActorRoles: staff}
		olaf        = policy.Who{Actor: "user:olaf", ActorRoles: staff}
		bot         = policy.Who{Actor: "agent:bot"}
		botAna      = policy.Who{Actor: "agent:bot", OnBehalfOf: "user:ana", OnBehalfOfRoles: staff}
		botOlaf     = policy.Who{Actor: "agent:bot", OnBehalfOf: "user:olaf", OnBehalfOfRoles: staff}
		otherForAna = policy.Who{Actor: "agent:other", OnBehalfOf: "user:ana"}
		kim         = policy.Who{Actor: "customer:kim"}
		hook        = policy.Who{Actor: "hook:purchase:check"}
	)
	cases := []struct {
		name string
		st   typedef.State
		id   string
		who  policy.Who
		it   policy.ItemFacts
		want bool
	}{
		// The bake-offs' attack: a colleague (olaf, staff like ana) edits
		// ana's draft before she submits it.
		{"a colleague, on ana's draft", draft, "f_amount", olaf, byAna, false},
		{"ana, on her own draft", draft, "f_amount", ana, byAna, true},
		{"an agent acting alone, on ana's draft", draft, "f_amount", bot, byAna, false},
		{"an agent acting for ana, on her draft", draft, "f_vendor", botAna, byAna, true},
		{"an agent acting for olaf, on ana's draft", draft, "f_amount", botOlaf, byAna, false},

		// The requester is the effective creator, not whoever took part.
		{"ana, on a draft an agent filed for her", draft, "f_amount", ana, botForAna, true},
		{"another agent for ana, on a draft filed for her", draft, "f_amount", otherForAna, botForAna, true},
		{"the agent that filed it for ana, now acting alone", draft, "f_amount", bot, botForAna, false},
		{"the agent, on a draft it filed for itself", draft, "f_amount", bot, byBot, true},
		{"ana, on a draft the agent filed for itself", draft, "f_amount", ana, byBot, false},
		{"a customer, on her own request", draft, "f_amount", kim, byKim, true},
		{"a customer, on ana's draft", draft, "f_amount", kim, byAna, false},

		// Only the listed fields, only in the listed state.
		{"a colleague, a field the state does not list", draft, "f_note", olaf, byAna, true},
		{"a colleague, in a state that lists nothing", review, "f_amount", olaf, byAna, true},

		// Fail closed: an Item the kernel created has no person as its
		// requester, and the kernel's own actors are no one's requester.
		{"a person, on an Item a reaction created", draft, "f_amount", ana, byReaction, false},
		{"a hook (the kernel), on ana's draft", draft, "f_amount", hook, byAna, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := policy.MayWriteInState(c.st, c.id, c.who, c.it); got != c.want {
				t.Errorf("MayWriteInState(%s, %s, %+v, %+v) = %v, want %v", c.st.ID, c.id, c.who, c.it, got, c.want)
			}
		})
	}
}
