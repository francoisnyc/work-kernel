// Tests for proof-of-concept Step 2, part 1: the HR check, hr.manager_of,
// against the stub HR directory.
//
// It reads two values, in order: created_on_behalf_of and created_by (the
// vacation request's hook declares them so). The requester is the first if
// set, else the second. It answers correct with the requester's manager
// (the hook writes it to f_approver), or deny with a code:
//
//   - unknown_employee: the directory does not know the requester;
//   - no_eligible_approver: the requester's manager is the requester.
//
// It has no side effect: no idempotency key is needed.
package hooks_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/connstub"
	"github.com/francoisnyc/work-kernel/internal/hooks"
)

func newHR(t *testing.T) (*connstub.Server, *hooks.Connectors) {
	t.Helper()
	w := newWorld(t)
	hr := connstub.NewHR()
	t.Cleanup(hr.Close)
	hr.SetManager("user:ana", "user:bob")
	hr.SetManager("user:bob", "user:dana")
	hr.SetManager("user:dana", "user:dana")
	return hr, w.c.WithHR(hr.URL)
}

func TestManagerOf_Arity(t *testing.T) {
	reads, writes, ok := hooks.Arity("hr.manager_of")
	if !ok || reads != 2 || writes != 1 {
		t.Errorf("Arity(hr.manager_of) = %d, %d, %v; want 2 reads, 1 write", reads, writes, ok)
	}
}

func TestManagerOf_Answers(t *testing.T) {
	_, c := newHR(t)
	cases := []struct {
		name string
		args []any // created_on_behalf_of, created_by
		want hooks.Result
	}{
		{"ana's own request: her manager", []any{nil, "user:ana"},
			hooks.Result{Verdict: hooks.Correct, Values: []any{"user:bob"}}},
		{"an agent's request for ana: ana's manager, not the agent's", []any{"user:ana", "agent:triage-bot"},
			hooks.Result{Verdict: hooks.Correct, Values: []any{"user:bob"}}},
		{"bob's request: his manager", []any{nil, "user:bob"},
			hooks.Result{Verdict: hooks.Correct, Values: []any{"user:dana"}}},
		{"dana manages herself: nobody may approve", []any{nil, "user:dana"},
			hooks.Result{Verdict: hooks.Deny, Reason: "no_eligible_approver"}},
		{"erin is not in the directory", []any{nil, "user:erin"},
			hooks.Result{Verdict: hooks.Deny, Reason: "unknown_employee"}},
		{"an agent acting for itself is not an employee", []any{nil, "agent:triage-bot"},
			hooks.Result{Verdict: hooks.Deny, Reason: "unknown_employee"}},
		{"an empty on_behalf_of is unset", []any{"", "user:ana"},
			hooks.Result{Verdict: hooks.Correct, Values: []any{"user:bob"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := c.Run(ctx(t), "hr.manager_of", "", tc.args)
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Errorf("hr.manager_of(%v) = %+v, %v; want %+v", tc.args, got, err, tc.want)
			}
		})
	}
}

// No requester at all, or not a string, is a broken call: permanent.
func TestManagerOf_BadInputIsPermanent(t *testing.T) {
	_, c := newHR(t)
	for _, args := range [][]any{{nil, nil}, {nil, ""}, {nil, float64(3)}, {true, "user:ana"}} {
		_, err := c.Run(ctx(t), "hr.manager_of", "", args)
		if !errors.Is(err, hooks.ErrPermanent) {
			t.Errorf("hr.manager_of(%v) error = %v, want ErrPermanent", args, err)
		}
	}
}

// An HR outage is worth retrying; connectors without an HR directory are
// misconfigured, which retrying cannot fix.
func TestManagerOf_OutageRetryableNoDirectoryPermanent(t *testing.T) {
	hr, c := newHR(t)
	hr.SetOutage(true)
	_, err := c.Run(ctx(t), "hr.manager_of", "", []any{nil, "user:ana"})
	if err == nil || errors.Is(err, hooks.ErrPermanent) {
		t.Errorf("during an HR outage: error = %v, want a retryable error", err)
	}

	w := newWorld(t) // connectors with no HR directory
	_, err = w.c.Run(ctx(t), "hr.manager_of", "", []any{nil, "user:ana"})
	if !errors.Is(err, hooks.ErrPermanent) {
		t.Errorf("without an HR directory: error = %v, want ErrPermanent", err)
	}
}
