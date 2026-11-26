// Tests for "sent back to the requester" (docs/roadmap.md, track 1, item
// 5, step 5; the owner's decisions of its Phase A): a state with
// `waits_on: requester` waits on the Item's requester, the person who
// asked for it (policy.IsRequester: created_on_behalf_of, else
// created_by), besides whoever its moves name.
//
//   - the requester, staff or customer, whether they created it themselves
//     or an agent created it for them;
//   - an agent acting for the requester (the user it acts for is the
//     requester);
//   - not someone else: a colleague, another customer, the agent that
//     created it on someone's behalf, the kernel's actors;
//   - not in a state without the key;
//   - beside the moves: a state that waits on its requester and names a
//     role too waits on both, and the role's queue holds it;
//   - the requester's own moves are unchanged (MayMove, Moves).
package policy_test

import (
	"slices"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/policy"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// sentBackType: a request goes to review; the team sends it back, and it
// waits on its requester in `back`, where only the requester sends it to
// review again. `both` also waits on the team's role; `plain` is like
// back without the key.
func sentBackType() *typedef.Type {
	return &typedef.Type{ID: "sbt", Version: 1, InitialState: "draft", States: []typedef.State{
		{ID: "draft", Category: "todo"},
		{ID: "review", Category: "waiting"},
		{ID: "back", Category: "waiting", WaitsOn: typedef.WaitsOnRequester},
		{ID: "both", Category: "waiting", WaitsOn: typedef.WaitsOnRequester},
		{ID: "plain", Category: "waiting"},
		{ID: "done", Category: "done"},
	}, Transitions: []typedef.Transition{
		{From: "draft", To: "review", Allowed: []string{"creator"}},
		{From: "review", To: "back", Allowed: []string{"role:team"}},
		{From: "back", To: "review", Allowed: []string{"creator"}},
		{From: "both", To: "review", Allowed: []string{"creator"}},
		{From: "both", To: "done", Allowed: []string{"role:team"}},
		{From: "plain", To: "review", Allowed: []string{"creator"}},
	}}
}

func TestSentBack_WaitsOnTheRequester(t *testing.T) {
	ty := sentBackType()
	byAna := policy.ItemFacts{CreatedBy: "user:ana"}
	forAna := policy.ItemFacts{CreatedBy: "agent:prep-bot", CreatedOnBehalfOf: "user:ana"}
	byKim := policy.ItemFacts{CreatedBy: "customer:kim"}
	forKim := policy.ItemFacts{CreatedBy: "agent:portal-bot", CreatedOnBehalfOf: "customer:kim"}
	ana := policy.Who{Actor: "user:ana"}
	bob := policy.Who{Actor: "user:bob", ActorRoles: []string{"team"}}
	kim := policy.Who{Actor: "customer:kim"}
	lee := policy.Who{Actor: "customer:lee"}
	anasBot := policy.Who{Actor: "agent:prep-bot", OnBehalfOf: "user:ana"}
	kimsBot := policy.Who{Actor: "agent:portal-bot", OnBehalfOf: "customer:kim"}
	timer := policy.Who{Actor: "principal:system:timer"}

	for _, c := range []struct {
		what  string
		state string
		who   policy.Who
		it    policy.ItemFacts
		want  bool
	}{
		{"ana, who created it, in back", "back", ana, byAna, true},
		{"ana, for whom an agent created it", "back", ana, forAna, true},
		{"an agent acting for ana, her request", "back", anasBot, byAna, true},
		{"kim, a customer, who created it", "back", kim, byKim, true},
		{"kim, for whom the portal's agent created it", "back", kim, forKim, true},
		{"the portal's agent acting for kim", "back", kimsBot, byKim, true},

		{"bob, a colleague in the team: back names no move for him", "back", bob, byAna, false},
		{"lee, another customer", "back", lee, byKim, false},
		{"an agent acting for ana, kim's request", "back", anasBot, byKim, false},
		{"the agent that created it for ana, acting for nobody", "back", policy.Who{Actor: "agent:prep-bot"}, forAna, false},
		{"the timer", "back", timer, byAna, false},

		{"ana, in a state without the key", "plain", ana, byAna, false},
		{"kim, in a state without the key", "plain", kim, byKim, false},
		{"ana, in review, which waits on the team", "review", ana, byAna, false},
		{"ana, in a done state", "done", ana, byAna, false},

		{"ana, in a state that waits on her and on the team", "both", ana, byAna, true},
		{"bob, in that state: the team's move names him", "both", bob, byAna, true},
		{"kim, in that state", "both", kim, byKim, true},
	} {
		if got := policy.WaitsOn(ty, c.state, c.who, c.it); got != c.want {
			t.Errorf("%s: WaitsOn = %v, want %v", c.what, got, c.want)
		}
	}
}

func TestSentBack_WaitsOnRequester(t *testing.T) {
	ty := sentBackType()
	for state, want := range map[string]bool{"back": true, "both": true, "plain": false, "review": false, "done": false, "nope": false} {
		if got := policy.WaitsOnRequester(ty, state); got != want {
			t.Errorf("WaitsOnRequester(%s) = %v, want %v", state, got, want)
		}
	}
}

// A role's queue is unchanged: it holds what the role's moves name, and a
// state that waits on its requester only is in no role's queue.
func TestSentBack_ARolesQueue(t *testing.T) {
	ty := sentBackType()
	for state, want := range map[string]bool{"back": false, "both": true, "review": true} {
		if got := policy.WaitsOnRole(ty, state, "team"); got != want {
			t.Errorf("WaitsOnRole(%s, team) = %v, want %v", state, got, want)
		}
	}
}

// The key adds no move: the requester's moves are what the Type gives
// the creator, and a colleague's what it gives the team.
func TestSentBack_TheMovesAreUnchanged(t *testing.T) {
	ty := sentBackType()
	byKim := policy.ItemFacts{CreatedBy: "customer:kim"}
	for _, c := range []struct {
		what, state string
		who         policy.Who
		want        []string
	}{
		{"kim in back", "back", policy.Who{Actor: "customer:kim"}, []string{"review"}},
		{"kim in both", "both", policy.Who{Actor: "customer:kim"}, []string{"review"}},
		{"bob in both", "both", policy.Who{Actor: "user:bob", ActorRoles: []string{"team"}}, []string{"done"}},
		{"bob in back", "back", policy.Who{Actor: "user:bob", ActorRoles: []string{"team"}}, []string{}},
	} {
		if got := policy.Moves(ty, c.state, c.who, byKim); !slices.Equal(got, c.want) {
			t.Errorf("%s: Moves = %v, want %v", c.what, got, c.want)
		}
	}
}
