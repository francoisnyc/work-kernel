// Tests for why a move is withheld (the Inbox for people, PR 2a):
// policy.Withheld, beside Moves.
//
// The owner's rule (Phase A, 2026-11-27): a move out of the Item's state
// is withheld from the caller when its `allowed` lets them in (read
// without `except`) but MayMove refuses it. The reason is a code only:
//
//   - own_request: `except: [creator]`, and they took part in creating
//     the Item (themselves, or the user an agent acts for);
//   - own_container: `except: [container_creator]`, and they took part in
//     creating its container;
//   - claimed_by_other: the state has a claim they do not hold (someone
//     else does, or nobody).
//
// `except` is MayTransition's and is asked before the claim, so a move
// refused by both is own_request (or own_container). A move `allowed` does
// not let them make is someone else's and is not listed; nothing is listed
// for a customer, whose moves are only the creator's. The kernel's actors
// leave a claimed state freely, so nothing is withheld from them by it.
//
// The invariant at the end runs over every Type in types/: Moves and
// Withheld never share a move, together they are every move `allowed`
// lets the caller in to, and each reason is the one the pieces give.
package policy_test

import (
	"fmt"
	"slices"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/policy"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// desk is shaped like equipment_request: a desk agent takes a request
// from the queue (not their own), and the claimant decides it or hands it
// back; the requester withdraws it while it waits. An approval-like move
// out of "queue" is closed to the container's creator.
func desk() *typedef.Type {
	return &typedef.Type{ID: "desk", Version: 1, InitialState: "queue",
		Fields: []typedef.Field{{ID: "f_handler", Type: "principal"}},
		States: []typedef.State{
			{ID: "queue", Category: "waiting"},
			{ID: "in_hand", Category: "active", Claim: "f_handler"},
			{ID: "fulfilled", Category: "done"},
			{ID: "declined", Category: "canceled"},
			{ID: "withdrawn", Category: "canceled"},
			{ID: "escalated", Category: "active"},
		},
		Transitions: []typedef.Transition{
			{From: "queue", To: "in_hand", Allowed: []string{"role:desk_agent"}, Except: []string{"creator"}},
			{From: "queue", To: "escalated", Allowed: []string{"role:desk_agent"}, Except: []string{"container_creator"}},
			{From: "queue", To: "withdrawn", Allowed: []string{"creator"}},
			{From: "in_hand", To: "fulfilled", Allowed: []string{"role:desk_agent"}},
			{From: "in_hand", To: "declined", Allowed: []string{"role:desk_agent"}, Except: []string{"creator"}},
			{From: "in_hand", To: "queue", Allowed: []string{"role:desk_agent"}, Release: true, Clears: []string{"f_handler"}},
		},
	}
}

var (
	sam      = policy.Who{Actor: "user:sam", ActorRoles: []string{"desk_agent"}}
	lea      = policy.Who{Actor: "user:lea", ActorRoles: []string{"desk_agent"}}
	outsider = policy.Who{Actor: "user:ana"}
	samsBot  = policy.Who{Actor: "agent:bot", ActorRoles: []string{"desk_agent"}, OnBehalfOf: "user:sam", OnBehalfOfRoles: []string{"desk_agent"}}
)

func withheldMove(to, reason string) item.WithheldMove {
	return item.WithheldMove{ToState: to, Reason: reason}
}

func TestWithheld_EachReason(t *testing.T) {
	byAna := policy.ItemFacts{CreatedBy: "user:ana", Fields: map[string]any{}}
	bySam := policy.ItemFacts{CreatedBy: "user:sam", Fields: map[string]any{}}
	inSamsContainer := policy.ItemFacts{CreatedBy: "user:ana", ContainerCreatedBy: "user:sam", Fields: map[string]any{}}
	heldBySam := policy.ItemFacts{CreatedBy: "user:ana", Fields: map[string]any{"f_handler": "user:sam"}}
	heldByNobody := policy.ItemFacts{CreatedBy: "user:ana", Fields: map[string]any{}}
	samsHeldBySam := policy.ItemFacts{CreatedBy: "user:sam", Fields: map[string]any{"f_handler": "user:sam"}}
	samsHeldByLea := policy.ItemFacts{CreatedBy: "user:sam", Fields: map[string]any{"f_handler": "user:lea"}}

	cases := []struct {
		name  string
		state string
		who   policy.Who
		facts policy.ItemFacts
		want  []item.WithheldMove
	}{
		{"a desk agent, someone else's request: nothing withheld", "queue", sam, byAna, nil},
		{"his own request: taking it is withheld, own_request", "queue", sam, bySam,
			[]item.WithheldMove{withheldMove("in_hand", policy.ReasonOwnRequest)}},
		{"an agent acting for him, on his own request: own_request", "queue", samsBot, bySam,
			[]item.WithheldMove{withheldMove("in_hand", policy.ReasonOwnRequest)}},
		{"his own container's request: escalating it is withheld, own_container", "queue", sam, inSamsContainer,
			[]item.WithheldMove{withheldMove("escalated", policy.ReasonOwnContainer)}},
		{"held by sam, read by lea: deciding is withheld, claimed_by_other; her release is a move", "in_hand", lea, heldBySam,
			[]item.WithheldMove{withheldMove("fulfilled", policy.ReasonClaimedByOther), withheldMove("declined", policy.ReasonClaimedByOther)}},
		{"held by sam, read by sam: nothing withheld", "in_hand", sam, heldBySam, nil},
		{"held by sam, read by his agent: nothing withheld", "in_hand", samsBot, heldBySam, nil},
		{"held by nobody: deciding is withheld from everyone, claimed_by_other", "in_hand", sam, heldByNobody,
			[]item.WithheldMove{withheldMove("fulfilled", policy.ReasonClaimedByOther), withheldMove("declined", policy.ReasonClaimedByOther)}},
		{"his own request, held by him: except comes first, own_request", "in_hand", sam, samsHeldBySam,
			[]item.WithheldMove{withheldMove("declined", policy.ReasonOwnRequest)}},
		{"his own request, held by lea: own_request before claimed_by_other", "in_hand", sam, samsHeldByLea,
			[]item.WithheldMove{withheldMove("fulfilled", policy.ReasonClaimedByOther), withheldMove("declined", policy.ReasonOwnRequest)}},

		// Not withheld: someone else's move.
		{"an outsider holds no role: nothing is withheld, nothing listed", "queue", outsider, bySam, nil},
		{"an outsider, a claimed Item: nothing listed", "in_hand", outsider, heldBySam, nil},
		{"the requester's withdraw is not withheld from a desk agent", "queue", lea, byAna, nil},
		{"a customer: never listed", "queue", policy.Who{Actor: "customer:kim"}, policy.ItemFacts{CreatedBy: "customer:kim"}, nil},
		{"the timer leaves a claimed state freely", "in_hand", policy.Who{Actor: "system:timer"}, heldBySam, nil},
		{"a state with no moves out", "fulfilled", sam, heldBySam, nil},
		{"a state the Type does not have", "nowhere", sam, byAna, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := policy.Withheld(desk(), c.state, c.who, c.facts)
			if got == nil {
				t.Fatal("Withheld = nil, want a list (empty when nothing is withheld)")
			}
			if !slices.Equal(got, c.want) && !(len(got) == 0 && len(c.want) == 0) {
				t.Errorf("Withheld = %v, want %v", got, c.want)
			}
		})
	}
}

// The moves stay as they were: Withheld lists, it changes nothing.
func TestWithheld_MovesUnchanged(t *testing.T) {
	heldBySam := policy.ItemFacts{CreatedBy: "user:ana", Fields: map[string]any{"f_handler": "user:sam"}}
	if got := policy.Moves(desk(), "in_hand", lea, heldBySam); !slices.Equal(got, []string{"queue"}) {
		t.Errorf("lea's moves on sam's claimed request = %v, want [queue] (a colleague may release, step 2's rule)", got)
	}
	bySam := policy.ItemFacts{CreatedBy: "user:sam", Fields: map[string]any{}}
	if got := policy.Moves(desk(), "queue", sam, bySam); !slices.Equal(got, []string{"escalated", "withdrawn"}) {
		t.Errorf("sam's moves on his own request = %v, want [escalated withdrawn]", got)
	}
}

// The invariant, over every Type in types/ and a set of principals and
// Items: Moves and Withheld never share a move; every move in either is
// one `allowed` lets the caller in to (read without except); every such
// move is in exactly one of them; each reason is the first refusal
// MayMove's pieces give.
func TestWithheld_Invariant_EveryTypeInTypes(t *testing.T) {
	types, err := typedef.ParseDir("../../types")
	if err != nil {
		t.Fatal(err)
	}
	// Every role any Type names, so staff hold whatever is asked.
	var allRoles []string
	for _, ty := range types {
		for _, tr := range ty.Transitions {
			for _, e := range tr.Allowed {
				if r, ok := cutRole(e); ok && !slices.Contains(allRoles, r) {
					allRoles = append(allRoles, r)
				}
			}
		}
	}
	principals := []struct {
		name string
		who  policy.Who
	}{
		{"staff", policy.Who{Actor: "user:sam", ActorRoles: allRoles}},
		{"an agent for a user", policy.Who{Actor: "agent:bot", ActorRoles: allRoles, OnBehalfOf: "user:sam", OnBehalfOfRoles: allRoles}},
		{"a colleague", policy.Who{Actor: "user:lea", ActorRoles: allRoles}},
		{"an outsider", policy.Who{Actor: "user:out"}},
		{"a customer", policy.Who{Actor: "customer:kim"}},
	}
	// Items: created by sam or by someone else, in sam's container or
	// not, every principal field naming sam, and the claim held by sam
	// (the claimant), by lea, or by nobody.
	type variant struct {
		name                 string
		createdBy, container string
		holder               string
	}
	var variants []variant
	for _, by := range []string{"user:sam", "user:ana", "customer:kim"} {
		for _, c := range []string{"", "user:sam"} {
			for _, h := range []string{"user:sam", "user:lea", ""} {
				variants = append(variants, variant{fmt.Sprintf("created by %s, container %q, held by %q", by, c, h), by, c, h})
			}
		}
	}

	seen := map[string]int{}
	for _, ty := range types {
		for _, st := range ty.States {
			for _, v := range variants {
				facts := policy.ItemFacts{CreatedBy: v.createdBy, ContainerCreatedBy: v.container, Fields: map[string]any{}}
				for _, f := range ty.Fields {
					if f.Type == "principal" {
						facts.Fields[f.ID] = "user:sam"
					}
				}
				if st.Claim != "" {
					if v.holder == "" {
						delete(facts.Fields, st.Claim)
					} else {
						facts.Fields[st.Claim] = v.holder
					}
				}
				for _, p := range principals {
					where := fmt.Sprintf("%s@%d, state %s, %s, %s", ty.ID, ty.Version, st.ID, v.name, p.name)
					moves := policy.Moves(ty, st.ID, p.who, facts)
					withheld := policy.Withheld(ty, st.ID, p.who, facts)
					if withheld == nil {
						t.Fatalf("%s: Withheld = nil, want a list", where)
					}
					if policy.CustomerOf(principalOf(p.who)) != "" && len(withheld) > 0 {
						t.Fatalf("%s: a customer is withheld %v; never listed for one", where, withheld)
					}
					var want []item.WithheldMove
					for _, tr := range ty.Transitions {
						if tr.From != st.ID {
							continue
						}
						open := tr
						open.Except = nil
						inMoves := slices.Contains(moves, tr.To)
						i := slices.IndexFunc(withheld, func(w item.WithheldMove) bool { return w.ToState == tr.To })
						if inMoves && i >= 0 {
							t.Fatalf("%s: %s is both a move and withheld (%v)", where, tr.To, withheld[i])
						}
						if (inMoves || i >= 0) && !policy.MayTransition(open, p.who, facts) {
							t.Fatalf("%s: %s is listed (moves %v, withheld %v), but allowed does not let them in", where, tr.To, moves, withheld)
						}
						if policy.CustomerOf(principalOf(p.who)) != "" || !policy.MayTransition(open, p.who, facts) || inMoves {
							continue
						}
						// Allowed lets them in and MayMove refuses: withheld,
						// with the first refusal.
						reason := policy.ReasonClaimedByOther
						switch {
						case slices.Contains(tr.Except, typedef.AllowedCreator) &&
							policy.TookPartInCreation(p.who.Actor, p.who.OnBehalfOf, facts.CreatedBy, facts.CreatedOnBehalfOf):
							reason = policy.ReasonOwnRequest
						case slices.Contains(tr.Except, typedef.AllowedContainerCreator) &&
							policy.TookPartInCreation(p.who.Actor, p.who.OnBehalfOf, facts.ContainerCreatedBy, facts.ContainerCreatedOnBehalfOf):
							reason = policy.ReasonOwnContainer
						}
						want = append(want, withheldMove(tr.To, reason))
					}
					if !slices.Equal(withheld, want) && !(len(withheld) == 0 && len(want) == 0) {
						t.Fatalf("%s: Withheld = %v, want %v (moves %v)", where, withheld, want, moves)
					}
					for _, w := range withheld {
						seen[w.Reason]++
					}
				}
			}
		}
	}
	// The Types in types/ must reach every reason, or the run proves
	// little: equipment_request (except creator, a claim), approval
	// (except container_creator).
	for _, r := range []string{policy.ReasonOwnRequest, policy.ReasonOwnContainer, policy.ReasonClaimedByOther} {
		if seen[r] == 0 {
			t.Errorf("no move in types/ was withheld for %s: the run does not test that reason", r)
		}
	}
}

func cutRole(e string) (string, bool) {
	if len(e) > len(typedef.AllowedRolePrefix) && e[:len(typedef.AllowedRolePrefix)] == typedef.AllowedRolePrefix {
		return e[len(typedef.AllowedRolePrefix):], true
	}
	return "", false
}

func principalOf(w policy.Who) item.Principal {
	return item.Principal{Actor: w.Actor, OnBehalfOf: w.OnBehalfOf}
}
