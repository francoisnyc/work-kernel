package lifecycle_test

import (
	"testing"

	"github.com/francoisnyc/work-kernel/internal/lifecycle"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// BoundTo: which event causes a hook is the Type's to say.
func TestBoundTo(t *testing.T) {
	ty := &typedef.Type{ID: "leave", States: []typedef.State{
		{ID: "draft"},
		{ID: "submitted", Acting: []typedef.ActingTimer{{ID: "remind", Hook: "remind_approver"}}},
		{ID: "approved"},
	}}
	enter := typedef.Hook{ID: "notify_approved", On: typedef.OnEnter, State: "approved"}
	timer := typedef.Hook{ID: "remind_approver", On: typedef.OnTimer}
	reaction := typedef.Hook{ID: "refund_email", On: typedef.OnReaction, Reaction: "refund"}
	check := typedef.Hook{ID: "hr_check", On: typedef.OnTransition, From: "draft", To: "submitted"}

	entered := func(op, state string) lifecycle.Cause { return lifecycle.Cause{Op: op, Entered: state} }
	applied := func(reaction string) lifecycle.Cause {
		return lifecycle.Cause{Op: lifecycle.OpReactionApplied, Reaction: reaction}
	}
	for _, c := range []struct {
		name  string
		hook  typedef.Hook
		cause lifecycle.Cause
		want  bool
	}{
		{"enter: the entry into its state", enter, entered("transition", "approved"), true},
		{"enter: a create into its state", enter, entered("create", "approved"), true},
		{"enter: the entry into another state", enter, entered("transition", "submitted"), false},
		{"enter: an event that entered no state", enter, lifecycle.Cause{Op: "update"}, false},
		{"timer: the entry into its timer's state", timer, entered("transition", "submitted"), true},
		{"timer: the entry into a state without the timer", timer, entered("create", "draft"), false},
		{"timer: an event that entered no state", timer, lifecycle.Cause{Op: "update"}, false},
		{"reaction: its reaction applied", reaction, applied("leave:refund"), true},
		{"reaction: another reaction applied", reaction, applied("leave:deduct"), false},
		{"reaction: an entry", reaction, entered("transition", "approved"), false},
		{"transition: the request", check, lifecycle.Cause{Op: lifecycle.OpTransitionRequested}, true},
		{"transition: an entry", check, entered("transition", "submitted"), false},
	} {
		if got := lifecycle.BoundTo(ty, c.hook, c.cause); got != c.want {
			t.Errorf("%s: BoundTo = %v, want %v", c.name, got, c.want)
		}
	}

	if a, ok := lifecycle.ActingTimer(ty, "submitted", "remind"); !ok || a.Hook != "remind_approver" {
		t.Errorf("ActingTimer(submitted, remind) = %+v, %v; want the timer", a, ok)
	}
	if _, ok := lifecycle.ActingTimer(ty, "draft", "remind"); ok {
		t.Error("ActingTimer(draft, remind) found a timer draft does not arm")
	}
}
