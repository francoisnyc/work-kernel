// Closing the holes pli check PR 2 found in types/ (the follow-up step;
// internal/check/golden_types_test.go explains each finding): asked of
// policy on the repository's own Types, the new versions refuse what the
// versions before them let in.
//
//   - incident@8: only a desk agent cancels an incident in progress. Under
//     incident@7 anyone could, its requester or an agent included.
//   - access_request@3 and vacation_request@5: while the request is a
//     draft, only its requester changes what is asked (requester_only).
//     Under access_request@2 and vacation_request@4 a colleague could.
package policy_test

import (
	"testing"

	"github.com/francoisnyc/work-kernel/internal/policy"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// repoType returns id@version from types/, failing the test if it is not there.
func repoType(t *testing.T, types []*typedef.Type, id string, version int) *typedef.Type {
	t.Helper()
	for _, ty := range types {
		if ty.ID == id && ty.Version == version {
			return ty
		}
	}
	t.Fatalf("%s@%d is not in types/", id, version)
	return nil
}

// move returns ty's transition from -> to.
func move(t *testing.T, ty *typedef.Type, from, to string) typedef.Transition {
	t.Helper()
	for _, tr := range ty.Transitions {
		if tr.From == from && tr.To == to {
			return tr
		}
	}
	t.Fatalf("%s@%d has no move %s -> %s", ty.ID, ty.Version, from, to)
	return typedef.Transition{}
}

// state returns ty's state id.
func state(t *testing.T, ty *typedef.Type, id string) typedef.State {
	t.Helper()
	st, ok := ty.State(id)
	if !ok {
		t.Fatalf("%s@%d has no state %s", ty.ID, ty.Version, id)
	}
	return st
}

func TestCheckHoles_OnlyADeskAgentCancelsAnIncidentInProgress(t *testing.T) {
	types, err := typedef.ParseDir("../../types")
	if err != nil {
		t.Fatal(err)
	}
	v7, v8 := repoType(t, types, "incident", 7), repoType(t, types, "incident", 8)
	byAna := policy.ItemFacts{CreatedBy: "user:ana", Fields: map[string]any{}}
	desk := []string{"desk_agent"}
	var (
		ana      = policy.Who{Actor: "user:ana"}
		anaAgent = policy.Who{Actor: "user:ana", ActorRoles: desk}
		olaf     = policy.Who{Actor: "user:olaf"}
		sam      = policy.Who{Actor: "user:sam", ActorRoles: desk}
		bot      = policy.Who{Actor: "agent:bot"}
		botDesk  = policy.Who{Actor: "agent:bot", ActorRoles: desk}
		botAna   = policy.Who{Actor: "agent:bot", OnBehalfOf: "user:ana"}
		botSam   = policy.Who{Actor: "agent:bot", ActorRoles: desk, OnBehalfOf: "user:sam", OnBehalfOfRoles: desk}
		kim      = policy.Who{Actor: "customer:kim"}
	)
	cases := []struct {
		name string
		who  policy.Who
		want bool
	}{
		{"sam, a desk agent", sam, true},
		{"ana, its requester", ana, false},
		{"ana, its requester, who is a desk agent", anaAgent, true},
		{"olaf, a colleague with no role", olaf, false},
		{"an agent with no role", bot, false},
		{"an agent holding desk_agent", botDesk, true},
		{"an agent acting for ana, its requester", botAna, false},
		{"an agent holding desk_agent, acting for sam", botSam, true},
		{"a customer", kim, false},
	}
	inProgress := move(t, v8, "in_progress", "canceled")
	for _, c := range cases {
		if got := policy.MayTransition(inProgress, c.who, byAna); got != c.want {
			t.Errorf("incident@8 in_progress -> canceled, %s: %v, want %v", c.name, got, c.want)
		}
	}
	// The hole incident@8 closes: under @7 the requester and an agent
	// with no role may both cancel it.
	for _, who := range []policy.Who{ana, bot} {
		if !policy.MayTransition(move(t, v7, "in_progress", "canceled"), who, byAna) {
			t.Errorf("incident@7 in_progress -> canceled, %s: refused; the test no longer shows the hole", who.Actor)
		}
	}
	// Cancelling a new incident is unchanged: its requester or a desk agent.
	isNew := move(t, v8, "new", "canceled")
	for _, c := range []struct {
		name string
		who  policy.Who
		want bool
	}{{"ana, its requester", ana, true}, {"sam, a desk agent", sam, true}, {"olaf, a colleague with no role", olaf, false}} {
		if got := policy.MayTransition(isNew, c.who, byAna); got != c.want {
			t.Errorf("incident@8 new -> canceled, %s: %v, want %v", c.name, got, c.want)
		}
	}
}

func TestCheckHoles_OnlyTheRequesterChangesWhatADraftAsks(t *testing.T) {
	types, err := typedef.ParseDir("../../types")
	if err != nil {
		t.Fatal(err)
	}
	byAna := policy.ItemFacts{CreatedBy: "user:ana"}
	var (
		ana    = policy.Who{Actor: "user:ana"}
		olaf   = policy.Who{Actor: "user:olaf"}
		botAna = policy.Who{Actor: "agent:bot", OnBehalfOf: "user:ana"}
		botOla = policy.Who{Actor: "agent:bot", OnBehalfOf: "user:olaf"}
	)
	for _, c := range []struct {
		id         string
		was, now   int
		draft      string
		fields     []string
		after      string   // a later state, where the rule no longer holds
		afterField []string // fields a colleague still writes there, as before
	}{
		{"access_request", 2, 3, "draft", []string{"f_title", "f_reason"}, "pending", []string{"f_title", "f_reason"}},
		{"vacation_request", 4, 5, "draft", []string{"f_start", "f_end", "f_days", "f_leave_type", "f_comment"}, "submitted", []string{"f_comment"}},
	} {
		was, now := repoType(t, types, c.id, c.was), repoType(t, types, c.id, c.now)
		draft := state(t, now, c.draft)
		for _, f := range c.fields {
			for _, w := range []struct {
				name string
				who  policy.Who
				want bool
			}{
				{"ana, its requester", ana, true},
				{"an agent acting for ana", botAna, true},
				{"olaf, a colleague", olaf, false},
				{"an agent acting for olaf", botOla, false},
			} {
				if got := policy.MayWriteInState(draft, f, w.who, byAna); got != w.want {
					t.Errorf("%s@%d %s, %s writes %s: %v, want %v", c.id, c.now, c.draft, w.name, f, got, w.want)
				}
			}
			// The hole the new version closes: the version before it lets
			// a colleague change the draft.
			if !policy.MayWriteInState(state(t, was, c.draft), f, olaf, byAna) {
				t.Errorf("%s@%d %s, olaf writes %s: refused; the test no longer shows the hole", c.id, c.was, c.draft, f)
			}
		}
		// Only the draft: once submitted, requester_only says nothing.
		for _, f := range c.afterField {
			if !policy.MayWriteInState(state(t, now, c.after), f, olaf, byAna) {
				t.Errorf("%s@%d %s, olaf writes %s: refused, want only the draft limited", c.id, c.now, c.after, f)
			}
		}
	}
}
