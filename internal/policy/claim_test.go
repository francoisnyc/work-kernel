// Tests for the claim's part of "may this principal make this move?"
// (docs/roadmap.md, track 1, item 5, step 2): MayLeaveClaimed, asked
// besides MayTransition, and what it changes in who an Item waits on.
//
// While an Item is in a state with `claim: f`, a move out of it is its
// claimant's only: the principal's effective user (the user an agent acts
// for, else the actor) is the person f holds. Three moves pass whoever
// asks: one out of a state with no claim; a release (`release: true`),
// which `allowed` alone decides; the kernel's own. A move that clears the
// field without being a release is still the claimant's.
//
// In the view: a claimed Item waits on its claimant only; a release is not
// work that waits on anyone; a role's queue holds no claimed Item.
package policy_test

import (
	"testing"

	"github.com/francoisnyc/work-kernel/internal/policy"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

var (
	claimedState   = typedef.State{ID: "review", Category: "active", Claim: "f_claimed_by"}
	unclaimedState = typedef.State{ID: "queue", Category: "waiting"}
	byGus          = policy.ItemFacts{CreatedBy: "user:ana", Fields: map[string]any{"f_claimed_by": "user:gus"}}
)

func TestMayLeaveClaimed(t *testing.T) {
	decide := typedef.Transition{From: "review", To: "approved", Allowed: []string{"role:finance"}}
	tidy := typedef.Transition{From: "review", To: "rejected", Allowed: []string{"role:finance"}, Clears: []string{"f_claimed_by"}}
	release := typedef.Transition{From: "review", To: "queue", Allowed: []string{"role:finance"}, Release: true, Clears: []string{"f_claimed_by"}}
	claim := typedef.Transition{From: "queue", To: "review", Allowed: []string{"role:finance"}}
	cases := []struct {
		name  string
		st    typedef.State
		tr    typedef.Transition
		who   policy.Who
		facts policy.ItemFacts
		want  bool
	}{
		{"the claimant decides", claimedState, decide, gus, byGus, true},
		{"a colleague does not", claimedState, decide, fay, byGus, false},
		{"a colleague does not, by a move that clears the claim", claimedState, tidy, fay, byGus, false},
		{"the claimant does, by a move that clears the claim", claimedState, tidy, gus, byGus, true},
		{"a colleague releases", claimedState, release, fay, byGus, true},
		{"the claimant releases", claimedState, release, gus, byGus, true},
		{"a state with no claim: anyone", unclaimedState, claim, fay, byGus, true},

		{"an agent acting for the claimant", claimedState, decide,
			policy.Who{Actor: "agent:bot", OnBehalfOf: "user:gus"}, byGus, true},
		{"an agent acting for a colleague", claimedState, decide,
			policy.Who{Actor: "agent:bot", OnBehalfOf: "user:fay"}, byGus, false},
		{"an agent on its own, not the claimant", claimedState, decide, policy.Who{Actor: "agent:bot"}, byGus, false},
		{"an agent on its own, the claimant", claimedState, decide, policy.Who{Actor: "agent:bot"},
			policy.ItemFacts{Fields: map[string]any{"f_claimed_by": "agent:bot"}}, true},
		{"an agent that is the claimant, now acting for someone", claimedState, decide,
			policy.Who{Actor: "agent:bot", OnBehalfOf: "user:fay"},
			policy.ItemFacts{Fields: map[string]any{"f_claimed_by": "agent:bot"}}, false},

		{"the timer", claimedState, decide, policy.Who{Actor: "system:timer"}, byGus, true},
		{"a hook", claimedState, decide, policy.Who{Actor: "hook:purchase:check"}, byGus, true},

		// Fail closed: a claim field that names nobody lets nobody decide.
		{"the claim field is unset", claimedState, decide, gus, policy.ItemFacts{Fields: map[string]any{}}, false},
		{"the claim field is null", claimedState, decide, gus, policy.ItemFacts{Fields: map[string]any{"f_claimed_by": nil}}, false},
		{"no fields at all", claimedState, decide, gus, policy.ItemFacts{}, false},
		{"the claim field is unset: a release still passes", claimedState, release, fay, policy.ItemFacts{}, true},
		// It is the claim field that counts, not any field naming them.
		{"another field names them", claimedState, decide, fay,
			policy.ItemFacts{Fields: map[string]any{"f_claimed_by": "user:gus", "f_manager": "user:fay"}}, false},
		{"they created it", claimedState, decide, fay,
			policy.ItemFacts{CreatedBy: "user:fay", Fields: map[string]any{"f_claimed_by": "user:gus"}}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := policy.MayLeaveClaimed(c.st, c.tr, c.who, c.facts); got != c.want {
				t.Errorf("MayLeaveClaimed = %v, want %v", got, c.want)
			}
		})
	}
}

// claimType: a queue finance claims from, and a claimed review with a
// decision, a decision that clears the claim, and a release that a lead
// may make too.
func claimType() *typedef.Type {
	return &typedef.Type{ID: "ct", Version: 1, InitialState: "queue",
		States: []typedef.State{unclaimedState, claimedState, {ID: "approved", Category: "done"}, {ID: "rejected", Category: "canceled"}},
		Transitions: []typedef.Transition{
			{From: "queue", To: "review", Allowed: []string{"role:finance"}},
			{From: "review", To: "queue", Allowed: []string{"role:finance", "role:lead"}, Release: true, Clears: []string{"f_claimed_by"}},
			{From: "review", To: "approved", Allowed: []string{"role:finance"}},
			{From: "review", To: "rejected", Allowed: []string{"role:finance"}, Clears: []string{"f_claimed_by"}},
		}}
}

// Who a claimed Item waits on: its claimant, while they may still make
// the move; nobody else, and no role's queue.
func TestWaitsOn_AClaimedItemWaitsOnItsClaimant(t *testing.T) {
	ty := claimType()
	lead := policy.Who{Actor: "user:olaf", ActorRoles: []string{"lead"}}
	unclaimed := policy.ItemFacts{CreatedBy: "user:ana"}

	if !policy.WaitsOn(ty, "queue", fay, unclaimed) || !policy.WaitsOn(ty, "queue", gus, unclaimed) {
		t.Errorf("in the queue: it must wait on fay and on gus, both in finance")
	}
	if !policy.WaitsOnRole(ty, "queue", "finance") {
		t.Errorf("in the queue: it must be in the finance queue")
	}

	if !policy.WaitsOn(ty, "review", gus, byGus) {
		t.Errorf("claimed by gus: it must wait on gus")
	}
	if policy.WaitsOn(ty, "review", fay, byGus) {
		t.Errorf("claimed by gus: it waits on fay, his colleague; want only on its claimant")
	}
	// The lead is named on the release only, and a release is not work.
	if policy.WaitsOn(ty, "review", lead, byGus) {
		t.Errorf("claimed by gus: it waits on the lead, who may only release it")
	}
	for _, role := range []string{"finance", "lead"} {
		if policy.WaitsOnRole(ty, "review", role) {
			t.Errorf("claimed: it is in the queue of %s; a role's queue holds what nobody has claimed", role)
		}
	}
	// A claimant who left the role can no longer act: nothing waits on them.
	if policy.WaitsOn(ty, "review", policy.Who{Actor: "user:gus"}, byGus) {
		t.Errorf("claimed by gus, who left finance: it still waits on him")
	}

	// The release is not among the moves that count; the two decisions are.
	var to []string
	for _, tr := range policy.WaitingMoves(ty, "review") {
		to = append(to, tr.To)
	}
	if len(to) != 2 || to[0] != "approved" || to[1] != "rejected" {
		t.Errorf("WaitingMoves(review) go to %v, want [approved rejected]: a release is not work", to)
	}
}
