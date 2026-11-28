// Tests for Exits (pli check, PR 1): the one list of what can move an
// Item out of a state, read from the Type and nothing else.
package lifecycle_test

import (
	"os"
	"reflect"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/lifecycle"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

func typeFile(t *testing.T, path string) *typedef.Type {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	ty, err := typedef.Parse(data)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return ty
}

// exitMove is the exit of the Type's transition from -> to.
func exitMove(t *testing.T, ty *typedef.Type, from, to string) lifecycle.Exit {
	t.Helper()
	for i, tr := range ty.Transitions {
		if tr.From == from && tr.To == to {
			return lifecycle.Exit{Kind: lifecycle.ExitMove, To: to, Transition: i}
		}
	}
	t.Fatalf("%s@%d has no transition %s -> %s", ty.ID, ty.Version, from, to)
	return lifecycle.Exit{}
}

func TestExits(t *testing.T) {
	vacation := typeFile(t, "../../types/vacation_request.v4.yaml")
	sprint := typeFile(t, "../../types/sprint.v3.yaml")
	ticket := typeFile(t, "../../types/ticket.v9.yaml")

	cases := []struct {
		name  string
		ty    *typedef.Type
		state string
		want  []lifecycle.Exit
	}{
		{
			// Five moves in the Type's order, then the timer.
			name: "moves then the timer", ty: vacation, state: "submitted",
			want: []lifecycle.Exit{
				exitMove(t, vacation, "submitted", "approved"),
				exitMove(t, vacation, "submitted", "rejected"),
				exitMove(t, vacation, "submitted", "draft"),
				exitMove(t, vacation, "submitted", "withdrawn"),
				exitMove(t, vacation, "submitted", "expired"),
				{Kind: lifecycle.ExitTimer, To: "expired", Transition: -1},
			},
		},
		{
			name: "a move then the at-timer", ty: sprint, state: "active",
			want: []lifecycle.Exit{
				exitMove(t, sprint, "active", "closed"),
				{Kind: lifecycle.ExitAt, To: "closed", Transition: -1},
			},
		},
		{
			// reopen moves the ticket itself when a public comment is
			// created inside it; other move reactions move children.
			name: "moves then a move reaction on the Item itself", ty: ticket, state: "waiting_for_customer",
			want: []lifecycle.Exit{
				exitMove(t, ticket, "waiting_for_customer", "in_progress"),
				exitMove(t, ticket, "waiting_for_customer", "canceled"),
				{Kind: lifecycle.ExitReaction, To: "in_progress", Transition: -1, Reaction: "reopen"},
			},
		},
		{name: "a done state with no exit", ty: vacation, state: "rejected", want: []lifecycle.Exit{}},
		{name: "a state the Type does not have", ty: vacation, state: "nowhere", want: []lifecycle.Exit{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := lifecycle.Exits(c.ty, c.state)
			if got == nil {
				t.Fatal("Exits returned nil, want a list (empty when there are none)")
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("Exits(%s@%d, %s)\n got %+v\nwant %+v", c.ty.ID, c.ty.Version, c.state, got, c.want)
			}
		})
	}
}

// Exits is pure: the same Type and state give the same list, and the Type
// is not changed.
func TestExitsIsPure(t *testing.T) {
	ty := typeFile(t, "../../types/ticket.v9.yaml")
	before := len(ty.Transitions)
	first := lifecycle.Exits(ty, "waiting_for_customer")
	second := lifecycle.Exits(ty, "waiting_for_customer")
	if !reflect.DeepEqual(first, second) || len(first) == 0 {
		t.Errorf("two calls differ or are empty: %+v, %+v", first, second)
	}
	if len(ty.Transitions) != before {
		t.Errorf("Exits changed the Type's transitions")
	}
}
