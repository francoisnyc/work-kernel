// Tests for "waiting on me" (docs/roadmap.md, track 1, item 5, step 1):
// which open Items wait on whom, as one pure rule beside MayTransition.
//
// The owner's rule (Phase A): an Item waits on the people that a move out
// of its current state NAMES, in the Item's own Type version:
//
//   - a move counts when its `allowed` names a role, a person
//     (principal:user:…, principal:agent:…) or a field (field:<id>);
//   - an empty `allowed` is nobody's queue ("anyone may" is not "someone
//     must"); a move for the creator only, for nobody, or for the kernel's
//     own actors (the timer, a hook) does not count;
//   - `creator` never makes an Item wait on its requester, even on a move
//     that also names a role: it waits on the role's holders;
//   - a state in category done or canceled never waits (a reopen is not
//     work);
//   - `except` is applied to the person asking; guards, required and given
//     fields are not evaluated;
//   - an agent waits only where it and the user it acts for both pass
//     (MayTransition's rule); a customer and the kernel's actors never;
//   - a role's queue (WaitsOnRole) is the moves that name that role,
//     whoever asks: `except` is a person's matter, not a role's.
//
// Everything the rule says waits on someone is a move MayTransition would
// let them make (the property test at the end).
package policy_test

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/policy"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// waitType: a small Type whose state "open" (category waiting) has the
// given moves out of it, each to a state of its own, and whose state
// "closed" (done) has one move a role may make (a reopen).
func waitType(moves ...typedef.Transition) *typedef.Type {
	ty := &typedef.Type{ID: "wt", Version: 1, InitialState: "open", States: []typedef.State{
		{ID: "open", Category: "waiting"},
		{ID: "closed", Category: "done"},
		{ID: "dropped", Category: "canceled"},
	}}
	for i, m := range moves {
		m.From = "open"
		if m.To == "" {
			m.To = fmt.Sprintf("s%d", i)
			ty.States = append(ty.States, typedef.State{ID: m.To, Category: "active"})
		}
		ty.Transitions = append(ty.Transitions, m)
	}
	ty.Transitions = append(ty.Transitions,
		typedef.Transition{From: "closed", To: "open", Allowed: []string{"role:finance"}},
		typedef.Transition{From: "dropped", To: "open", Allowed: []string{"field:f_owner"}})
	return ty
}

func names(entries ...string) typedef.Transition { return typedef.Transition{Allowed: entries} }

func excepting(tr typedef.Transition, entries ...string) typedef.Transition {
	tr.Except = entries
	return tr
}

var (
	fay     = policy.Who{Actor: "user:fay", ActorRoles: []string{"finance"}}
	gus     = policy.Who{Actor: "user:gus", ActorRoles: []string{"finance"}}
	bob     = policy.Who{Actor: "user:bob"}
	ana     = policy.Who{Actor: "user:ana"}
	anasOwn = policy.ItemFacts{CreatedBy: "user:ana", Fields: map[string]any{"f_owner": "user:bob"}}
)

func TestWaitsOn_WhoAMoveNames(t *testing.T) {
	cases := []struct {
		name  string
		moves []typedef.Transition
		who   policy.Who
		facts policy.ItemFacts
		want  bool
	}{
		{"a role: its holder", []typedef.Transition{names("role:finance")}, fay, anasOwn, true},
		{"a role: someone without it", []typedef.Transition{names("role:finance")}, bob, anasOwn, false},
		{"a field: the person it names", []typedef.Transition{names("field:f_owner")}, bob, anasOwn, true},
		{"a field: someone else", []typedef.Transition{names("field:f_owner")}, fay, anasOwn, false},
		{"a field that is unset: nobody", []typedef.Transition{names("field:f_owner")}, bob,
			policy.ItemFacts{CreatedBy: "user:ana"}, false},
		{"a field holding several people: each of them", []typedef.Transition{names("field:f_owners")}, bob,
			policy.ItemFacts{CreatedBy: "user:ana", Fields: map[string]any{"f_owners": []any{"user:gus", "user:bob"}}}, true},
		{"a person named outright", []typedef.Transition{names("principal:user:bob")}, bob, anasOwn, true},
		{"a person named outright: someone else", []typedef.Transition{names("principal:user:bob")}, fay, anasOwn, false},
		{"one move of several names them", []typedef.Transition{names("creator"), names(), names("role:finance")},
			fay, anasOwn, true},

		// What does not count.
		{"an empty allowed is nobody's queue", []typedef.Transition{names()}, fay, anasOwn, false},
		{"an empty allowed: not the requester's either", []typedef.Transition{names()}, ana, anasOwn, false},
		{"a move for the creator only: the requester does not wait", []typedef.Transition{names("creator")}, ana, anasOwn, false},
		{"creator beside a role: the role's holder waits", []typedef.Transition{names("creator", "role:finance")},
			fay, anasOwn, true},
		{"creator beside a role: the requester does not", []typedef.Transition{names("creator", "role:finance")},
			ana, anasOwn, false},
		{"nobody", []typedef.Transition{names("nobody")}, fay, anasOwn, false},
		{"the timer's move", []typedef.Transition{names("principal:system:timer")}, fay, anasOwn, false},
		{"a hook's move", []typedef.Transition{names("principal:hook:wt:check")}, fay, anasOwn, false},

		// except is the asker's.
		{"except creator: the requester in finance does not wait",
			[]typedef.Transition{excepting(names("role:finance"), "creator")}, gus,
			policy.ItemFacts{CreatedBy: "user:gus"}, false},
		{"except creator: a colleague does",
			[]typedef.Transition{excepting(names("role:finance"), "creator")}, fay,
			policy.ItemFacts{CreatedBy: "user:gus"}, true},
		{"except creator on one move, not on another that names them",
			[]typedef.Transition{excepting(names("role:finance"), "creator"), names("role:finance")}, gus,
			policy.ItemFacts{CreatedBy: "user:gus"}, true},
		{"except container_creator: whoever made the container does not wait",
			[]typedef.Transition{excepting(names("role:finance"), "container_creator")}, gus,
			policy.ItemFacts{CreatedBy: "user:ana", ContainerCreatedBy: "user:gus"}, false},

		// Agents: the agent and its user both pass, as for a move.
		{"an agent acting for a role holder, without the role", []typedef.Transition{names("role:finance")},
			policy.Who{Actor: "agent:bot", OnBehalfOf: "user:fay", OnBehalfOfRoles: []string{"finance"}}, anasOwn, false},
		{"an agent holding the role, acting for a holder", []typedef.Transition{names("role:finance")},
			policy.Who{Actor: "agent:bot", ActorRoles: []string{"finance"}, OnBehalfOf: "user:fay", OnBehalfOfRoles: []string{"finance"}},
			anasOwn, true},
		{"an agent holding the role, acting for someone without it", []typedef.Transition{names("role:finance")},
			policy.Who{Actor: "agent:bot", ActorRoles: []string{"finance"}, OnBehalfOf: "user:bob"}, anasOwn, false},
		{"an agent acting for the person a field names", []typedef.Transition{names("field:f_owner")},
			policy.Who{Actor: "agent:bot", OnBehalfOf: "user:bob"}, anasOwn, false},
		{"an agent on its own, holding the role", []typedef.Transition{names("role:finance")},
			policy.Who{Actor: "agent:bot", ActorRoles: []string{"finance"}}, anasOwn, true},

		// Never a customer, never the kernel.
		{"a customer, even named by a field", []typedef.Transition{names("field:f_owner")},
			policy.Who{Actor: "customer:kim"},
			policy.ItemFacts{CreatedBy: "customer:kim", Fields: map[string]any{"f_owner": "customer:kim"}}, false},
		{"an agent acting for a customer", []typedef.Transition{names("role:finance")},
			policy.Who{Actor: "agent:bot", ActorRoles: []string{"finance"}, OnBehalfOf: "customer:kim"}, anasOwn, false},
		{"the timer", []typedef.Transition{names("principal:system:timer", "role:finance")},
			policy.Who{Actor: "system:timer"}, anasOwn, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := policy.WaitsOn(waitType(c.moves...), "open", c.who, c.facts); got != c.want {
				t.Errorf("WaitsOn = %v, want %v", got, c.want)
			}
		})
	}
}

// A reopen is not work: a state in category done or canceled never waits,
// whoever its moves name. A state the Type does not have waits on nobody.
func TestWaitsOn_DoneAndCanceledNeverWait(t *testing.T) {
	ty := waitType(names("role:finance"))
	if !policy.WaitsOn(ty, "open", fay, anasOwn) {
		t.Errorf("open (waiting): WaitsOn = false, want true: the setup must wait on fay")
	}
	for _, state := range []string{"closed", "dropped", "no_such_state"} {
		if policy.WaitsOn(ty, state, fay, anasOwn) || policy.WaitsOn(ty, state, bob, anasOwn) {
			t.Errorf("%s: WaitsOn = true, want false", state)
		}
		if policy.WaitsOnRole(ty, state, "finance") {
			t.Errorf("%s: WaitsOnRole(finance) = true, want false", state)
		}
		if moves := policy.WaitingMoves(ty, state); len(moves) != 0 {
			t.Errorf("%s: WaitingMoves = %v, want none", state, moves)
		}
	}
}

// WaitingMoves: the moves out of a state that name someone, in the Type's
// order; the others are left out.
func TestWaitingMoves_OnlyTheMovesThatNameSomeone(t *testing.T) {
	ty := waitType(
		names("role:finance"),               // s0
		names(),                             // s1: anyone
		names("creator"),                    // s2
		names("creator", "field:f_owner"),   // s3
		names("nobody"),                     // s4
		names("principal:system:timer"),     // s5
		names("principal:user:bob"),         // s6
		names("principal:hook:wt:check"),    // s7
		names("principal:agent:triage-bot"), // s8
	)
	var got []string
	for _, tr := range policy.WaitingMoves(ty, "open") {
		got = append(got, tr.To)
	}
	if want := []string{"s0", "s3", "s6", "s8"}; !slices.Equal(got, want) {
		t.Errorf("WaitingMoves(open) go to %v, want %v", got, want)
	}
}

// A role's queue: the Items a move names the role for. Whoever asks, and
// whoever created the Item: `except` is judged for a person, in WaitsOn.
func TestWaitsOnRole(t *testing.T) {
	cases := []struct {
		name  string
		moves []typedef.Transition
		role  string
		want  bool
	}{
		{"a move names the role", []typedef.Transition{names("role:finance")}, "finance", true},
		{"a move names another role", []typedef.Transition{names("role:cfo")}, "finance", false},
		{"beside a field", []typedef.Transition{names("field:f_owner", "role:finance")}, "finance", true},
		{"only a field", []typedef.Transition{names("field:f_owner")}, "finance", false},
		{"except does not empty a queue", []typedef.Transition{excepting(names("role:finance"), "creator")}, "finance", true},
		{"a move for nobody", []typedef.Transition{names("nobody", "role:finance")}, "finance", false},
		{"an empty allowed", []typedef.Transition{names()}, "finance", false},
		{"the role with its prefix is not a role's name", []typedef.Transition{names("role:finance")}, "role:finance", false},
		{"no role", []typedef.Transition{names("role:finance")}, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := policy.WaitsOnRole(waitType(c.moves...), "open", c.role); got != c.want {
				t.Errorf("WaitsOnRole(%q) = %v, want %v", c.role, got, c.want)
			}
		})
	}
}

// The property the view rests on, over generated Types, people and Items:
//
//   - whatever waits on someone is a move MayTransition lets them make, in
//     a state that is not done or canceled (so the view never shows work
//     its reader would be refused for who they are; guards aside);
//   - and it is exactly the rule in this file's header, written here a
//     second time, plainly: some move out of the state names someone, and
//     with `creator` struck from its list MayTransition still lets them in.
func TestWaitsOn_Property_ImpliesMayTransition(t *testing.T) {
	// A fixed seed: a failure names its case and can be run again.
	rng := rand.New(rand.NewPCG(5, 2026))
	entries := []string{"creator", "nobody", "role:finance", "role:cfo", "field:f_owner", "field:f_second",
		"principal:user:bob", "principal:system:timer", "principal:hook:wt:check", "principal:agent:bot"}
	excepts := [][]string{nil, nil, {"creator"}, {"container_creator"}, {"creator", "container_creator"}}
	people := []string{"user:ana", "user:bob", "user:fay", "user:gus", "agent:bot", "customer:kim", "system:timer"}
	roles := [][]string{nil, {"finance"}, {"cfo"}, {"finance", "cfo"}}
	categories := []string{"triage", "backlog", "todo", "active", "waiting", "done", "canceled", "none"}
	pick := func(from []string) string { return from[rng.IntN(len(from))] }

	waited := 0
	for i := range 5000 {
		category := pick(categories)
		ty := &typedef.Type{ID: "wt", Version: 1, InitialState: "s", States: []typedef.State{{ID: "s", Category: category}}}
		for m := range 1 + rng.IntN(3) {
			var list []string
			for range rng.IntN(4) {
				if e := pick(entries); !slices.Contains(list, e) {
					list = append(list, e)
				}
			}
			to := fmt.Sprintf("t%d", m)
			ty.States = append(ty.States, typedef.State{ID: to, Category: "active"})
			ty.Transitions = append(ty.Transitions,
				typedef.Transition{From: "s", To: to, Allowed: list, Except: excepts[rng.IntN(len(excepts))]})
		}
		who := policy.Who{Actor: pick(people), ActorRoles: roles[rng.IntN(len(roles))]}
		if strings.HasPrefix(who.Actor, "agent:") && rng.IntN(2) == 0 {
			who.OnBehalfOf = pick([]string{"user:ana", "user:bob", "user:fay", "customer:kim"})
			who.OnBehalfOfRoles = roles[rng.IntN(len(roles))]
		}
		facts := policy.ItemFacts{
			CreatedBy: pick(people[:5]), ContainerCreatedBy: pick([]string{"", "user:gus", "user:fay"}),
			Fields: map[string]any{},
		}
		if rng.IntN(3) > 0 {
			facts.Fields["f_owner"] = pick(people[:5])
		}
		if rng.IntN(3) == 0 {
			facts.Fields["f_second"] = []any{pick(people[:5]), pick(people[:5])}
		}

		// The rule, written again.
		want := false
		mayMove := false
		for _, tr := range ty.Transitions {
			if policy.MayTransition(tr, who, facts) {
				mayMove = true
			}
			named := slices.DeleteFunc(slices.Clone(tr.Allowed), func(e string) bool {
				return e == "creator" || strings.HasPrefix(e, "principal:system:") || strings.HasPrefix(e, "principal:hook:")
			})
			if len(named) == 0 || slices.Contains(named, "nobody") {
				continue
			}
			strict := tr
			strict.Allowed = named
			if category != "done" && category != "canceled" && policy.MayTransition(strict, who, facts) {
				want = true
			}
		}
		// The kernel's own actors wait on nothing, whatever names them.
		if strings.HasPrefix(who.Actor, "system:") {
			want = false
		}

		got := policy.WaitsOn(ty, "s", who, facts)
		describe := func() string {
			return fmt.Sprintf("case %d: state category %s, moves %+v, who %+v, facts %+v", i, category, moveLists(ty), who, facts)
		}
		if got && !mayMove {
			t.Fatalf("%s:\nWaitsOn = true, but MayTransition lets them make no move out of the state", describe())
		}
		if got != want {
			t.Fatalf("%s:\nWaitsOn = %v, want %v", describe(), got, want)
		}
		if got {
			waited++
		}
	}
	// The generator must reach the interesting half: an all-false run
	// would prove nothing.
	if waited < 200 {
		t.Errorf("only %d of 5000 generated cases wait on someone: the generator is too thin to test the rule", waited)
	}
}

func moveLists(ty *typedef.Type) []string {
	var out []string
	for _, tr := range ty.Transitions {
		out = append(out, fmt.Sprintf("allowed %v except %v", tr.Allowed, tr.Except))
	}
	return out
}
