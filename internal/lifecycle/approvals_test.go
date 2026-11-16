// Tests for proof-of-concept Step 2, part 1: guards that see who is acting,
// who created the Item and today's date, and fields frozen per state. The
// function stays pure: the item service passes who and today in (Input),
// so the same answer comes from the same input on every door.
package lifecycle_test

import (
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/francoisnyc/work-kernel/internal/lifecycle"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

func vacationType(t *testing.T) *typedef.Type {
	t.Helper()
	data, err := os.ReadFile("../../types/vacation_request.v1.yaml")
	if err != nil {
		t.Fatalf("read vacation_request.v1.yaml: %v", err)
	}
	ty, err := typedef.Parse(data)
	if err != nil {
		t.Fatalf("parse vacation_request v1: %v", err)
	}
	return ty
}

func date(s string) time.Time {
	d, err := time.Parse(time.DateOnly, s)
	if err != nil {
		panic(err)
	}
	return d
}

// terms is a valid request: five days from 2099-07-01, decided on 2099-06-01.
func terms() map[string]any {
	return map[string]any{
		"f_start": "2099-07-01", "f_end": "2099-07-05", "f_days": float64(5), "f_leave_type": "annual",
		"f_approver": "user:bob",
	}
}

var today = date("2099-06-01")

func assertGuardFailed(t *testing.T, what string, err error) {
	t.Helper()
	var rej *lifecycle.Rejection
	if !errors.As(err, &rej) || rej.Code != lifecycle.GuardFailed {
		t.Errorf("%s: error = %v, want guard_failed", what, err)
	}
}

// No self-approval: the approver may decide unless they are the requester:
// the creator, the user it was created for, or someone acting for either.
// An empty string is null to the guard; null == null holds in CEL, which
// the guard must not trip over when neither side involves an agent.
func TestApproveGuard_NoSelfApproval(t *testing.T) {
	ty := vacationType(t)
	cases := []struct {
		name                         string
		caller, callerOBO            string
		createdBy, createdOnBehalfOf string
		ok                           bool
	}{
		{"the manager decides a user's request (both on_behalf_of null)", "user:bob", "", "user:ana", "", true},
		{"the requester decides her own request", "user:ana", "", "user:ana", "", false},
		{"the user an agent created it for decides it", "user:ana", "", "agent:bot", "user:ana", false},
		{"the agent that created it decides it", "agent:bot", "", "agent:bot", "user:ana", false},
		{"an agent acting for the requester decides it", "agent:bot", "user:ana", "user:ana", "", false},
		{"an agent acting for the requester, created by an agent for her", "agent:other", "user:ana", "agent:bot", "user:ana", false},
		{"the manager decides a request an agent created for its user", "user:bob", "", "agent:bot", "user:ana", true},
		// The guard does not decide who may approve: `allowed` does, after it.
		{"an agent acting for the manager passes the guard", "agent:bot", "user:bob", "user:ana", "", true},
	}
	for _, c := range cases {
		for _, to := range []string{"approved", "rejected"} {
			in := lifecycle.Input{
				Fields: terms(), Caller: c.caller, CallerOnBehalfOf: c.callerOBO,
				CreatedBy: c.createdBy, CreatedOnBehalfOf: c.createdOnBehalfOf, Today: today,
			}
			_, err := lifecycle.TransitionWith(ty, "submitted", in, to)
			switch {
			case c.ok && err != nil:
				t.Errorf("%s (-> %s): %v, want allowed by the guard", c.name, to, err)
			case !c.ok:
				assertGuardFailed(t, c.name+" -> "+to, err)
			}
		}
	}
}

// Without who is acting (the old entry points), the approve guard does not
// hold: it fails safe.
func TestApproveGuard_WithoutWhoFailsSafe(t *testing.T) {
	ty := vacationType(t)
	_, err := lifecycle.Transition(ty, "submitted", terms(), "approved")
	assertGuardFailed(t, "approve with no caller", err)
}

// Submit: end on or after start (a one-day leave has start == end), start
// today or later (today is a UTC date given by the caller, its time of day
// ignored), days above zero, and the terms set.
func TestSubmitGuard_DatesAndDays(t *testing.T) {
	ty := vacationType(t)
	submit := func(fields map[string]any, today time.Time) error {
		_, err := lifecycle.TransitionWith(ty, "draft", lifecycle.Input{
			Fields: fields, Caller: "user:ana", CreatedBy: "user:ana", Today: today,
		}, "submitted")
		return err
	}
	with := func(k string, v any) map[string]any {
		f := terms()
		delete(f, "f_approver") // a draft has none yet; the HR check sets it
		f[k] = v
		return f
	}
	if err := submit(with("f_days", float64(5)), today); err != nil {
		t.Errorf("valid request: %v", err)
	}
	if err := submit(with("f_end", "2099-07-01"), today); err != nil {
		t.Errorf("one-day leave (end == start): %v", err)
	}
	if err := submit(with("f_start", "2099-06-01"), today); err != nil {
		t.Errorf("starting today: %v", err)
	}
	lateInTheDay := time.Date(2099, 6, 1, 23, 59, 0, 0, time.UTC)
	if err := submit(with("f_start", "2099-06-01"), lateInTheDay); err != nil {
		t.Errorf("starting today, asked at 23:59 UTC: %v", err)
	}
	assertGuardFailed(t, "start yesterday", submit(with("f_start", "2099-05-31"), today))
	assertGuardFailed(t, "end before start", submit(with("f_end", "2099-06-30"), today))
	assertGuardFailed(t, "zero days", submit(with("f_days", float64(0)), today))
	assertGuardFailed(t, "negative days", submit(with("f_days", float64(-2)), today))

	_, err := lifecycle.TransitionWith(ty, "draft", lifecycle.Input{
		Fields: map[string]any{"f_start": "2099-07-01"}, Caller: "user:ana", CreatedBy: "user:ana", Today: today,
	}, "submitted")
	var rej *lifecycle.Rejection
	if !errors.As(err, &rej) || rej.Code != lifecycle.MissingFields ||
		!slices.Equal(rej.Missing, []string{"f_days", "f_end", "f_leave_type"}) {
		t.Errorf("submit without the terms = %v, want missing_fields f_days, f_end, f_leave_type", err)
	}
}

// Only the requester (or an agent acting for them) recalls or withdraws.
func TestRequesterGuard_RecallAndWithdraw(t *testing.T) {
	ty := vacationType(t)
	moves := [][2]string{{"submitted", "draft"}, {"submitted", "withdrawn"}, {"draft", "withdrawn"}}
	cases := []struct {
		name              string
		caller, callerOBO string
		createdBy, cobo   string
		ok                bool
	}{
		{"the requester", "user:ana", "", "user:ana", "", true},
		{"the user an agent created it for", "user:ana", "", "agent:bot", "user:ana", true},
		{"an agent acting for the requester", "agent:bot", "user:ana", "user:ana", "", true},
		{"the manager", "user:bob", "", "user:ana", "", false},
		{"an agent acting for the manager", "agent:bot", "user:bob", "user:ana", "", false},
	}
	for _, c := range cases {
		for _, mv := range moves {
			_, err := lifecycle.TransitionWith(ty, mv[0], lifecycle.Input{
				Fields: terms(), Caller: c.caller, CallerOnBehalfOf: c.callerOBO,
				CreatedBy: c.createdBy, CreatedOnBehalfOf: c.cobo, Today: today,
			}, mv[1])
			what := c.name + ": " + mv[0] + " -> " + mv[1]
			if c.ok && err != nil {
				t.Errorf("%s: %v, want allowed", what, err)
			}
			if !c.ok {
				assertGuardFailed(t, what, err)
			}
		}
	}
}

// A state's frozen fields may not be named by any write while the Item is
// in it. The refusal names the state and the frozen fields among those
// named (sorted), never a value.
func TestCheckFrozen(t *testing.T) {
	ty := vacationType(t)
	for _, state := range []string{"submitted", "approved", "rejected"} {
		err := lifecycle.CheckFrozen(ty, state, []string{"f_days", "f_comment", "f_approver"})
		var rej *lifecycle.Rejection
		if !errors.As(err, &rej) {
			t.Fatalf("%s: f_days and f_approver: error = %v, want a *Rejection", state, err)
		}
		if rej.Code != lifecycle.FieldFrozen || rej.State != state ||
			!slices.Equal(rej.Frozen, []string{"f_approver", "f_days"}) {
			t.Errorf("%s: rejection = %+v, want field_frozen in %s naming f_approver, f_days", state, rej, state)
		}
		if strings.Contains(rej.Reason, "user:") {
			t.Errorf("%s: reason %q names a value", state, rej.Reason)
		}
		if err := lifecycle.CheckFrozen(ty, state, []string{"f_comment"}); err != nil {
			t.Errorf("%s: f_comment is not frozen: %v", state, err)
		}
	}
	for _, state := range []string{"draft", "withdrawn"} {
		if err := lifecycle.CheckFrozen(ty, state, []string{"f_days", "f_start", "f_approver"}); err != nil {
			t.Errorf("%s freezes nothing: %v", state, err)
		}
	}
	if err := lifecycle.CheckFrozen(ty, "submitted", nil); err != nil {
		t.Errorf("a write naming no field (a plain transition): %v", err)
	}
}
