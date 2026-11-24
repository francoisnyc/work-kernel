// Tests for fields closed to agents by default: who may write a field on
// the public door, from its writable_by (people, agents, role:<name>;
// absent means [people]; kernel is no one's here). An agent and the user
// it acts for must both pass: the agent as agents or a role it holds, its
// user as people or a role they hold. A customer is a person; an agent
// acting for a customer is judged as that customer (the customer rules
// decide which fields).
package policy_test

import (
	"testing"

	"github.com/francoisnyc/work-kernel/internal/policy"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

func TestMayWriteField(t *testing.T) {
	var (
		closed = typedef.Field{ID: "f_shut"}
		open   = typedef.Field{ID: "f_open", WritableByPeople: true, WritableByAgents: true}
		bots   = typedef.Field{ID: "f_bots", WritableByAgents: true}
		people = typedef.Field{ID: "f_people", WritableByPeople: true}
		role   = typedef.Field{ID: "f_role", WritableByRoles: []string{"writer"}}
		mixed  = typedef.Field{ID: "f_mixed", WritableByPeople: true, WritableByRoles: []string{"writer"}}
		botsOr = typedef.Field{ID: "f_bots_or", WritableByAgents: true, WritableByRoles: []string{"writer"}}
		kernel = typedef.Field{ID: "f_kernel", WritableBy: typedef.WritableByKernel}
	)
	writer := []string{"writer"}
	var (
		ana          = policy.Who{Actor: "user:ana"}
		wren         = policy.Who{Actor: "user:wren", ActorRoles: writer}
		kim          = policy.Who{Actor: "customer:kim"}
		bot          = policy.Who{Actor: "agent:bot"}
		botForAna    = policy.Who{Actor: "agent:bot", OnBehalfOf: "user:ana"}
		botForKim    = policy.Who{Actor: "agent:bot", OnBehalfOf: "customer:kim"}
		scribe       = policy.Who{Actor: "agent:scribe", ActorRoles: writer}
		scribeForAna = policy.Who{Actor: "agent:scribe", ActorRoles: writer, OnBehalfOf: "user:ana"}
		scribeForWr  = policy.Who{Actor: "agent:scribe", ActorRoles: writer, OnBehalfOf: "user:wren", OnBehalfOfRoles: writer}
	)
	cases := []struct {
		name string
		f    typedef.Field
		who  policy.Who
		want bool
	}{
		{"closed: a person", closed, ana, true},
		{"closed: a customer", closed, kim, true},
		{"closed: an agent", closed, bot, false},
		{"closed: an agent for a person", closed, botForAna, false},
		{"closed: an agent holding a role", closed, scribe, false},
		{"closed: an agent for a customer (the customer rules)", closed, botForKim, true},

		{"[people]: a person", people, ana, true},
		{"[people]: an agent", people, bot, false},

		{"[people, agents]: a person", open, ana, true},
		{"[people, agents]: an agent", open, bot, true},
		{"[people, agents]: an agent for a person", open, botForAna, true},

		{"[agents]: a person", bots, ana, false},
		{"[agents]: a customer", bots, kim, false},
		{"[agents]: an agent", bots, bot, true},
		{"[agents]: an agent for a person", bots, botForAna, false},
		{"[agents]: an agent for a customer", bots, botForKim, false},

		{"role: a person without it", role, ana, false},
		{"role: a person holding it", role, wren, true},
		{"role: an agent without it", role, bot, false},
		{"role: an agent holding it", role, scribe, true},
		{"role: an agent holding it, for a person without it", role, scribeForAna, false},
		{"role: an agent holding it, for a person holding it", role, scribeForWr, true},

		{"[people, role]: a person without the role", mixed, ana, true},
		{"[people, role]: an agent without it", mixed, bot, false},
		{"[people, role]: an agent holding it", mixed, scribe, true},
		{"[people, role]: an agent holding it, for a person", mixed, scribeForAna, true},

		{"[agents, role]: an agent", botsOr, bot, true},
		{"[agents, role]: a person holding the role", botsOr, wren, true},
		{"[agents, role]: a person without it", botsOr, ana, false},
		{"[agents, role]: an agent for a person holding it", botsOr, scribeForWr, true},

		{"kernel: a person", kernel, ana, false},
		{"kernel: an agent", kernel, bot, false},
	}
	for _, c := range cases {
		if got := policy.MayWriteField(c.f, c.who); got != c.want {
			t.Errorf("%s: MayWriteField = %v, want %v", c.name, got, c.want)
		}
	}
}
