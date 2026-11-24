// Tests for starting work, step 2 (docs/plans/starting-work.md, 1.2 and
// 1.4): MayStart judges a start block's allowed list. Every entry reads
// the container, never the new Item.
//
//   - No block, or an empty allowed: anyone (who sees the container: the
//     caller's check, made before).
//   - role:, principal:user: and field: match as in MayTransition, by the
//     same code: for the actor and for the user it acts for, both must
//     pass. A field: entry matches only the principal the container's
//     field holds (any member of a set): a field holding user:bob never
//     matches agent:x.
//   - container_creator: anyone who took part in creating the container
//     (TookPartInCreation), judged once for the principal as a whole.
//   - nobody: no person, no agent, no customer.
//   - A customer, or an agent acting for one, is not judged by allowed:
//     their gate is own and shared. nobody refuses them too: fail closed.
package policy_test

import (
	"testing"

	"github.com/francoisnyc/work-kernel/internal/policy"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

func allowed(entries ...string) *typedef.Start {
	return &typedef.Start{In: []string{"room"}, Allowed: entries}
}

func TestMayStart(t *testing.T) {
	room := policy.ContainerFacts{
		Fields:    map[string]any{"f_lead": "user:bob", "f_leads": []any{"user:bob", "user:carl"}, "f_bot": "agent:bot"},
		CreatedBy: "user:dana",
	}
	forAna := policy.ContainerFacts{CreatedBy: "agent:maker", CreatedOnBehalfOf: "user:ana"}
	lead := []string{"lead"}
	cases := []struct {
		name  string
		start *typedef.Start
		who   policy.Who
		c     policy.ContainerFacts
		want  bool
	}{
		// No rule.
		{"no block", nil, policy.Who{Actor: "user:eve"}, room, true},
		{"a block with in only", &typedef.Start{In: []string{"room"}}, policy.Who{Actor: "user:eve"}, room, true},
		{"an empty allowed", allowed(), policy.Who{Actor: "user:eve"}, room, true},

		// role:
		{"role: a holder", allowed("role:lead"), policy.Who{Actor: "user:lea", ActorRoles: lead}, room, true},
		{"role: someone without it", allowed("role:lead"), policy.Who{Actor: "user:eve"}, room, false},
		{"role: an agent and its user both hold it", allowed("role:lead"),
			policy.Who{Actor: "agent:x", ActorRoles: lead, OnBehalfOf: "user:lea", OnBehalfOfRoles: lead}, room, true},
		{"role: the user holds it, the agent does not", allowed("role:lead"),
			policy.Who{Actor: "agent:x", OnBehalfOf: "user:lea", OnBehalfOfRoles: lead}, room, false},
		{"role: the agent holds it, the user does not", allowed("role:lead"),
			policy.Who{Actor: "agent:x", ActorRoles: lead, OnBehalfOf: "user:eve"}, room, false},

		// principal:user:
		{"principal: that user", allowed("principal:user:ana"), policy.Who{Actor: "user:ana"}, room, true},
		{"principal: another user", allowed("principal:user:ana"), policy.Who{Actor: "user:eve"}, room, false},
		{"principal: an agent acting for that user", allowed("principal:user:ana"),
			policy.Who{Actor: "agent:x", OnBehalfOf: "user:ana"}, room, false},

		// field: the container's field.
		{"field: the principal it holds", allowed("field:f_lead"), policy.Who{Actor: "user:bob"}, room, true},
		{"field: someone else", allowed("field:f_lead"), policy.Who{Actor: "user:eve"}, room, false},
		{"field: a member of a set", allowed("field:f_leads"), policy.Who{Actor: "user:carl"}, room, true},
		{"field: not a member of the set", allowed("field:f_leads"), policy.Who{Actor: "user:eve"}, room, false},
		{"field: unset", allowed("field:f_none"), policy.Who{Actor: "user:bob"}, room, false},
		{"field: the top level has no fields", allowed("field:f_lead"), policy.Who{Actor: "user:bob"}, policy.ContainerFacts{}, false},
		{"field: an agent acting for the principal it holds", allowed("field:f_lead"),
			policy.Who{Actor: "agent:x", OnBehalfOf: "user:bob"}, room, false},
		{"field: the agent it holds, acting for itself", allowed("field:f_bot"), policy.Who{Actor: "agent:bot"}, room, true},
		{"field: the agent it holds, acting for a user it does not hold", allowed("field:f_bot"),
			policy.Who{Actor: "agent:bot", OnBehalfOf: "user:bob"}, room, false},
		{"field: never the new Item's fields", allowed("field:f_lead"), policy.Who{Actor: "user:eve"},
			policy.ContainerFacts{Fields: map[string]any{"f_other": "user:eve"}}, false},

		// container_creator: judged once.
		{"container_creator: who created it", allowed("container_creator"), policy.Who{Actor: "user:dana"}, room, true},
		{"container_creator: someone else", allowed("container_creator"), policy.Who{Actor: "user:eve"}, room, false},
		{"container_creator: an agent acting for who created it", allowed("container_creator"),
			policy.Who{Actor: "agent:x", OnBehalfOf: "user:dana"}, room, true},
		{"container_creator: the user an agent created it for", allowed("container_creator"), policy.Who{Actor: "user:ana"}, forAna, true},
		{"container_creator: the agent that created it, acting for someone else", allowed("container_creator"),
			policy.Who{Actor: "agent:maker", OnBehalfOf: "user:bob"}, forAna, true},
		{"container_creator: at the top level, nobody", allowed("container_creator"), policy.Who{Actor: "user:dana"}, policy.ContainerFacts{}, false},

		// Several entries: any one lets the principal in.
		{"several entries, the second matches", allowed("role:lead", "field:f_lead", "container_creator"), policy.Who{Actor: "user:bob"}, room, true},
		{"several entries, none matches", allowed("role:lead", "field:f_lead", "container_creator"), policy.Who{Actor: "user:eve"}, room, false},

		// nobody.
		{"nobody: a user", allowed("nobody"), policy.Who{Actor: "user:dana", ActorRoles: lead}, room, false},
		{"nobody: an agent", allowed("nobody"), policy.Who{Actor: "agent:x", OnBehalfOf: "user:dana"}, room, false},
		{"nobody: a customer", allowed("nobody"), policy.Who{Actor: "customer:kim"}, room, false},
		{"nobody: an agent acting for a customer", allowed("nobody"), policy.Who{Actor: "agent:portal", OnBehalfOf: "customer:kim"}, room, false},

		// Customers: their gate is own and shared, not allowed.
		{"a customer, whatever allowed says", allowed("role:lead"), policy.Who{Actor: "customer:kim"}, room, true},
		{"an agent acting for a customer", allowed("principal:user:ana"), policy.Who{Actor: "agent:portal", OnBehalfOf: "customer:kim"}, room, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := policy.MayStart(c.start, c.who, c.c); got != c.want {
				t.Errorf("MayStart(%v, %+v) = %v, want %v", c.start, c.who, got, c.want)
			}
		})
	}
}

// MayStart and MayTransition share their entry matching: for role:,
// principal: and field: entries, and nobody, they agree for every
// principal that is not a customer, the container's fields standing for
// the Item's.
func TestMayStart_SharesMatchingWithMayTransition(t *testing.T) {
	fields := map[string]any{"f_lead": "user:bob", "f_leads": []any{"user:bob", "agent:x"}}
	lists := [][]string{
		{"role:lead"}, {"principal:user:ana"}, {"field:f_lead"}, {"field:f_leads"}, {"nobody"},
		{"role:lead", "field:f_lead"}, {"principal:user:ana", "field:f_leads"},
	}
	lead := []string{"lead"}
	whos := []policy.Who{
		{Actor: "user:ana"}, {Actor: "user:bob"}, {Actor: "user:lea", ActorRoles: lead}, {Actor: "user:eve"},
		{Actor: "agent:x"}, {Actor: "agent:x", OnBehalfOf: "user:bob"}, {Actor: "agent:x", OnBehalfOf: "user:ana"},
		{Actor: "agent:x", ActorRoles: lead, OnBehalfOf: "user:lea", OnBehalfOfRoles: lead},
		{Actor: "agent:y", ActorRoles: lead, OnBehalfOf: "user:lea", OnBehalfOfRoles: lead},
		{Actor: "agent:x", OnBehalfOf: "user:lea", OnBehalfOfRoles: lead},
	}
	disagree := 0
	for _, list := range lists {
		for _, who := range whos {
			start := policy.MayStart(allowed(list...), who, policy.ContainerFacts{Fields: fields})
			move := policy.MayTransition(typedef.Transition{Allowed: list}, who, policy.ItemFacts{Fields: fields})
			if start != move {
				disagree++
				t.Errorf("allowed %v, %+v: MayStart = %v, MayTransition = %v", list, who, start, move)
			}
		}
	}
	if disagree > 0 {
		t.Logf("%d of %d combinations disagree", disagree, len(lists)*len(whos))
	}
}
