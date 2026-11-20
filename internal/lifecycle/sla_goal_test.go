// Tests for Jira part 2, step 3b: what sla_goal@1's own transitions
// compute (types/sla/sla_goal.v1.yaml), through the pure function. No
// database, no clock but cause.at.
//
//   - Met or missed is decided on instants (the Phase C review of step 3a,
//     confirmed in step 3b's Phase A, item 7): stopped while running, the
//     goal is missed if cause.at > f_due_at, so stopped exactly at the
//     instant it is met; stopped while paused, it is missed if
//     f_breached_at is still set (the pause's own correction has already
//     taken back a mark made too early).
//   - A pause keeps the elapsed working time and unsets the due instant; a
//     resume computes the due instant from what is left, on the calendar
//     the goal was created with; a goal resumed after its breach keeps the
//     instant it missed.
//   - Canceling records no outcome.
package lifecycle_test

import (
	"os"
	"testing"
	"time"

	"github.com/francoisnyc/work-kernel/internal/lifecycle"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

func slaGoal(t *testing.T) *typedef.Type {
	t.Helper()
	data, err := os.ReadFile("../../types/sla/sla_goal.v1.yaml")
	if err != nil {
		t.Fatalf("read sla_goal@1: %v", err)
	}
	ty, err := typedef.Parse(data)
	if err != nil {
		t.Fatalf("parse sla_goal@1: %v", err)
	}
	return ty
}

// A resolution goal of four working hours on the Paris office calendar
// (Monday to Friday, 09:00 to 17:00), started Monday 23 November 2026 at
// 10:00 Paris (09:00 UTC): due at 14:00 Paris, 13:00 UTC.
func runningGoal(t *testing.T, more map[string]any) map[string]any {
	t.Helper()
	fields := map[string]any{
		"f_metric": "resolution", "f_target_s": float64(14400),
		"f_started_at": stored(t, "2026-11-23 09:00:00"), "f_due_at": stored(t, "2026-11-23 13:00:00"),
		"f_week": []any{"fri 09:00-17:00", "mon 09:00-17:00", "thu 09:00-17:00", "tue 09:00-17:00", "wed 09:00-17:00"},
		"f_zone": "Europe/Paris",
	}
	for k, v := range more {
		fields[k] = v
	}
	return fields
}

func goalMove(t *testing.T, from, to string, fields map[string]any, at string) map[string]any {
	t.Helper()
	res, err := lifecycle.TransitionWith(slaGoal(t), from, lifecycle.Input{Fields: fields, CauseAt: instant(t, at)}, to)
	if err != nil {
		t.Fatalf("%s -> %s at %s: %v", from, to, at, err)
	}
	return res.Sets
}

// applied is the fields after a move's effects (nil unsets).
func applied(fields, sets map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range fields {
		out[k] = v
	}
	for k, v := range sets {
		if v == nil {
			delete(out, k)
		} else {
			out[k] = v
		}
	}
	return out
}

func TestSLAGoal_StoppedWhileRunning_OutcomeOnInstants(t *testing.T) {
	breached := map[string]any{"f_breached_at": stored(t, "2026-11-23 13:00:00")}
	cases := []struct {
		name    string
		at      string
		plus    time.Duration // added to at
		more    map[string]any
		outcome string
		mark    bool // f_breached_at after the stop
	}{
		{"well before the due instant", "2026-11-23 11:00:00", 0, nil, "met", false},
		{"a microsecond before", "2026-11-23 12:59:59", 999999 * time.Microsecond, nil, "met", false},
		// Exactly at the instant is met: cause.at > f_due_at is false. And
		// a met goal never carries a breach mark: the timer may already
		// have written it at that same instant, and the stop clears it
		// (cause.at <= f_due_at). The mail it sent is accepted.
		{"exactly at the due instant", "2026-11-23 13:00:00", 0, nil, "met", false},
		{"exactly at the due instant, the mark already written", "2026-11-23 13:00:00", 0, breached, "met", false},
		{"a microsecond after, the timer not yet heard of", "2026-11-23 13:00:00", time.Microsecond, nil, "missed", false},
		{"after, marked", "2026-11-23 15:00:00", 0, breached, "missed", true},
		// A mark written by a timer that fired before this stop was heard
		// of, when the stop's cause is before the instant: taken back.
		{"before the instant, with a mark made too early", "2026-11-23 12:00:00", 0, breached, "met", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, err := lifecycle.TransitionWith(slaGoal(t), "running",
				lifecycle.Input{Fields: runningGoal(t, c.more), CauseAt: instant(t, c.at).Add(c.plus)}, "completed")
			if err != nil {
				t.Fatalf("stop at %s + %v: %v", c.at, c.plus, err)
			}
			sets := res.Sets
			after := applied(runningGoal(t, c.more), sets)
			if after["f_outcome"] != c.outcome {
				t.Errorf("f_outcome = %v, want %s", after["f_outcome"], c.outcome)
			}
			if _, marked := after["f_breached_at"]; marked != c.mark {
				t.Errorf("f_breached_at after the stop = %v, want set: %v", after["f_breached_at"], c.mark)
			}
			if _, marked := after["f_breached_at"]; marked && after["f_outcome"] == "met" {
				t.Errorf("a met goal carries a breach mark: %v", after["f_breached_at"])
			}
			if _, due := after["f_due_at"]; due {
				t.Errorf("f_due_at = %v after the stop, want unset", after["f_due_at"])
			}
		})
	}
}

// A paused goal has no due instant: the breach mark, corrected by the
// pause, is what says whether it was missed.
func TestSLAGoal_StoppedWhilePaused_MissedIfBreached(t *testing.T) {
	// Paused at 11:30 Paris, in time.
	paused := applied(runningGoal(t, nil), goalMove(t, "running", "paused", runningGoal(t, nil), "2026-11-23 10:30:00"))
	if _, due := paused["f_due_at"]; due || intOf(t, paused["f_elapsed_s"]) != 5400 {
		t.Fatalf("paused in time: f_due_at %v, f_elapsed_s %v; want unset and 5400", paused["f_due_at"], paused["f_elapsed_s"])
	}
	if got := applied(paused, goalMove(t, "paused", "completed", paused, "2026-11-30 09:00:00")); got["f_outcome"] != "met" {
		t.Errorf("stopped while paused, never breached, a week later: %v, want met", got["f_outcome"])
	}

	// Paused after the breach: the mark stays, and the stop is missed.
	late := runningGoal(t, map[string]any{"f_breached_at": stored(t, "2026-11-23 13:00:00")})
	paused = applied(late, goalMove(t, "running", "paused", late, "2026-11-23 14:00:00"))
	if paused["f_breached_at"] != stored(t, "2026-11-23 13:00:00") {
		t.Fatalf("paused after the breach: f_breached_at = %v, want kept", paused["f_breached_at"])
	}
	if got := applied(paused, goalMove(t, "paused", "completed", paused, "2026-11-23 14:05:00")); got["f_outcome"] != "missed" {
		t.Errorf("stopped while paused, breached before the pause: %v, want missed", got["f_outcome"])
	}

	// Paused exactly at the due instant, the mark already written: the
	// pause was caused at or before the instant, so it clears the mark.
	paused = applied(late, goalMove(t, "running", "paused", late, "2026-11-23 13:00:00"))
	if _, marked := paused["f_breached_at"]; marked {
		t.Errorf("a pause exactly at the due instant kept the mark: %v", paused["f_breached_at"])
	}
	if got := applied(paused, goalMove(t, "paused", "completed", paused, "2026-11-24 09:00:00")); got["f_outcome"] != "met" {
		t.Errorf("completed while paused, paused exactly at the instant: %v, want met", got["f_outcome"])
	}

	// The race: the timer marked it, then a pause caused BEFORE the due
	// instant is heard of. The pause takes the mark back; stopped later,
	// the goal is met.
	paused = applied(late, goalMove(t, "running", "paused", late, "2026-11-23 12:30:00"))
	if _, marked := paused["f_breached_at"]; marked {
		t.Fatalf("a pause caused before the instant kept the mark: %v", paused["f_breached_at"])
	}
	if got := applied(paused, goalMove(t, "paused", "completed", paused, "2026-11-24 09:00:00")); got["f_outcome"] != "met" {
		t.Errorf("stopped while paused after the correction: %v, want met", got["f_outcome"])
	}
}

func TestSLAGoal_PauseAndResume_OnTheGoalsOwnCalendar(t *testing.T) {
	// Paused Monday 11:30 Paris after 1 h 30 of work; resumed Tuesday at
	// 08:00 Paris, before the office opens: 2 h 30 are left, from 09:00.
	paused := applied(runningGoal(t, nil), goalMove(t, "running", "paused", runningGoal(t, nil), "2026-11-23 10:30:00"))
	resumed := applied(paused, goalMove(t, "paused", "running", paused, "2026-11-24 07:00:00"))
	if resumed["f_started_at"] != stored(t, "2026-11-24 07:00:00") || resumed["f_due_at"] != stored(t, "2026-11-24 10:30:00") {
		t.Errorf("resumed: f_started_at %v, f_due_at %v; want the resume's instant and Tuesday 11:30 Paris",
			resumed["f_started_at"], resumed["f_due_at"])
	}
	// A second pause adds to the elapsed time.
	again := applied(resumed, goalMove(t, "running", "paused", resumed, "2026-11-24 09:00:00"))
	if intOf(t, again["f_elapsed_s"]) != 5400+3600 {
		t.Errorf("f_elapsed_s after a second pause = %v, want 9000", again["f_elapsed_s"])
	}

	// Resumed after its breach, a goal keeps the instant it missed as its
	// due instant: nothing is left to compute, and the host's queue goes
	// on showing it overdue.
	late := runningGoal(t, map[string]any{"f_breached_at": stored(t, "2026-11-23 13:00:00")})
	paused = applied(late, goalMove(t, "running", "paused", late, "2026-11-23 14:00:00"))
	resumed = applied(paused, goalMove(t, "paused", "running", paused, "2026-11-24 09:00:00"))
	if resumed["f_due_at"] != stored(t, "2026-11-23 13:00:00") {
		t.Errorf("resumed after the breach: f_due_at = %v, want the missed instant", resumed["f_due_at"])
	}
}

func TestSLAGoal_Canceled_NoOutcome(t *testing.T) {
	for _, from := range []string{"running", "paused"} {
		fields := runningGoal(t, nil)
		if from == "paused" {
			fields = applied(fields, goalMove(t, "running", "paused", fields, "2026-11-23 10:30:00"))
		}
		got := applied(fields, goalMove(t, from, "canceled", fields, "2026-11-23 11:00:00"))
		if _, ok := got["f_outcome"]; ok {
			t.Errorf("canceled from %s: f_outcome = %v, want none", from, got["f_outcome"])
		}
		if _, ok := got["f_due_at"]; ok {
			t.Errorf("canceled from %s: f_due_at = %v, want unset", from, got["f_due_at"])
		}
	}
	// Nothing leaves the two ends.
	for _, mv := range [][2]string{{"completed", "running"}, {"canceled", "running"}, {"completed", "canceled"}} {
		if _, err := lifecycle.TransitionWith(slaGoal(t), mv[0], lifecycle.Input{Fields: runningGoal(t, nil)}, mv[1]); err == nil {
			t.Errorf("%s -> %s is allowed", mv[0], mv[1])
		}
	}
}
