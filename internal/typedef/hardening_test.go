// The hardening step after Jira 2.4: placement rules and goals no person
// completes, in new versions of the existing Types. incident@3 is
// incident@2 with its goals on sla_goal@3 and placed by desk agents;
// reply@2 and note@2 stay in their incident; sla_goal@3 is sla_goal@2,
// placed by the kernel; comment@3 is comment@2, placed by the kernel. The
// older versions are unchanged.
package typedef_test

import (
	"maps"
	"slices"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/typedef"
)

func TestHardening_TypeFiles(t *testing.T) {
	types, err := typedef.ParseDir("../../types")
	if err != nil {
		t.Fatalf("ParseDir: %v", err)
	}
	find := func(id string, v int) *typedef.Type {
		for _, ty := range types {
			if ty.ID == id && ty.Version == v {
				return ty
			}
		}
		t.Fatalf("types/ has no %s@%d", id, v)
		return nil
	}
	kernel := []string{"kernel"}

	// incident@3: incident@2, placed by desk agents, its goals on sla_goal@3.
	inc2, inc3 := find("incident", 2), find("incident", 3)
	if !slices.Equal(inc3.PlacementWritableBy, []string{"role:desk_agent"}) || len(inc2.PlacementWritableBy) != 0 {
		t.Errorf("incident@3 placement %v (want [role:desk_agent]); incident@2 %v (want none)", inc3.PlacementWritableBy, inc2.PlacementWritableBy)
	}
	if !slices.Equal(inc3.Shared, inc2.Shared) || !slices.Equal(inc3.CustomerFields, inc2.CustomerFields) ||
		len(inc3.Fields) != len(inc2.Fields) || len(inc3.Transitions) != len(inc2.Transitions) || len(inc3.Reactions) != len(inc2.Reactions) {
		t.Error("incident@3 is incident@2 plus who places it and its goals' version")
	}
	goal3 := find("sla_goal", 3)
	for _, r := range inc3.Reactions {
		if r.Kind == typedef.ReactionCreate && r.Type == "sla_goal" && r.Version != 3 {
			t.Errorf("incident@3 %s creates sla_goal@%d; want @3", r.ID, r.Version)
		}
		// Every reaction the incident aims at its goals is one sla_goal@3
		// accepts, by reaction ID through containment.
		a, _ := goal3.Consent(inc3.ID, r.ID, true)
		switch {
		case r.Kind == typedef.ReactionCreate && r.Type == "sla_goal":
			if !a.MayCreate {
				t.Errorf("sla_goal@3 does not accept incident@3's %s creating it", r.ID)
			}
		case r.Kind == typedef.ReactionMove && r.Children != nil && slices.Contains(r.Children.Of, "sla_goal"):
			if !slices.Contains(a.Moves, r.To) {
				t.Errorf("sla_goal@3 does not accept incident@3's %s moving it to %s", r.ID, r.To)
			}
		}
	}
	// And every set reaction of sla_goal@3 the incident shows is accepted.
	for _, id := range []string{"show_first_response", "hide_first_response", "clear_first_response", "cancel_first_response",
		"show_resolution", "hide_resolution", "clear_resolution", "cancel_resolution"} {
		r, ok := goal3.Reaction(id)
		a, _ := inc3.Consent(goal3.ID, id, true)
		if !ok || len(a.Sets) == 0 || !slices.Equal(a.Sets, slices.Sorted(maps.Keys(r.Set))) {
			t.Errorf("incident@3 accepts sla_goal:%s for %v; the reaction sets %v", id, a.Sets, r.Set)
		}
	}

	// sla_goal@3: sla_goal@2, placed by the kernel, still no person's to move.
	goal2 := find("sla_goal", 2)
	if !slices.Equal(goal3.PlacementWritableBy, kernel) || len(goal2.PlacementWritableBy) != 0 {
		t.Errorf("sla_goal@3 placement %v; @2 %v", goal3.PlacementWritableBy, goal2.PlacementWritableBy)
	}
	for _, tr := range goal3.Transitions {
		if !slices.Equal(tr.Allowed, []string{typedef.AllowedNobody}) {
			t.Errorf("sla_goal@3 %s -> %s allowed %v; want [nobody]", tr.From, tr.To, tr.Allowed)
		}
	}
	if !slices.ContainsFunc(goal3.Invariants, func(i typedef.Invariant) bool { return i.ID == "metric_missing" }) ||
		len(goal3.Reactions) != len(goal2.Reactions) || len(goal3.AcceptsReactions) != len(goal2.AcceptsReactions) {
		t.Error("sla_goal@3 is sla_goal@2 plus who places it")
	}

	// reply@2, note@2, comment@3: their previous version, placed by the kernel.
	for _, c := range []struct {
		id       string
		old, new int
	}{{"reply", 1, 2}, {"note", 1, 2}, {"comment", 2, 3}} {
		o, n := find(c.id, c.old), find(c.id, c.new)
		if !slices.Equal(n.PlacementWritableBy, kernel) || len(o.PlacementWritableBy) != 0 {
			t.Errorf("%s@%d placement %v; @%d %v", c.id, c.new, n.PlacementWritableBy, c.old, o.PlacementWritableBy)
		}
		if len(n.Fields) != len(o.Fields) || len(n.Transitions) != len(o.Transitions) || !slices.Equal(n.CustomerFields, o.CustomerFields) {
			t.Errorf("%s@%d is %s@%d plus who places it", c.id, c.new, c.id, c.old)
		}
	}
}
