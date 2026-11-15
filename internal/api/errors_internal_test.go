// Tests for Step 0, part 1: the error table of docs/design.md, "Errors".
// Every kernel refusal maps to a standard code and an ErrorInfo with a stable
// reason; nothing else leaks. This is an internal test (package api) because
// the mapping is not exported.
package api

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/genproto/googleapis/rpc/errdetails"

	"github.com/francoisnyc/work-kernel/internal/itemservice"
)

func info(t *testing.T, ce *connect.Error) (*errdetails.ErrorInfo, []*errdetails.BadRequest_FieldViolation) {
	t.Helper()
	var ei *errdetails.ErrorInfo
	var fvs []*errdetails.BadRequest_FieldViolation
	for _, d := range ce.Details() {
		v, err := d.Value()
		if err != nil {
			t.Fatalf("detail: %v", err)
		}
		switch v := v.(type) {
		case *errdetails.ErrorInfo:
			ei = v
		case *errdetails.BadRequest:
			fvs = append(fvs, v.FieldViolations...)
		}
	}
	if ei == nil {
		t.Fatalf("no ErrorInfo in %v", ce)
	}
	return ei, fvs
}

func TestErrorFor_TheTable(t *testing.T) {
	const itemName = "namespaces/acme/items/0190c0de-0000-7000-8000-000000000001"
	hold := uuid.MustParse("0190c0de-0000-7000-8000-0000000000aa")
	cases := []struct {
		name   string
		err    error
		code   connect.Code
		reason string
	}{
		{"invalid", fmt.Errorf("%w: wait out of range", itemservice.ErrInvalid), connect.CodeInvalidArgument, "INVALID_REQUEST"},
		{"invalid field", &itemservice.InvalidError{Field: "f_priority", Reason: "want a whole number"},
			connect.CodeInvalidArgument, "INVALID_REQUEST"},
		{"not found", itemservice.ErrNotFound, connect.CodeNotFound, "NOT_FOUND"},
		{"forbidden", fmt.Errorf("%w: allowed", itemservice.ErrForbidden), connect.CodePermissionDenied, "NOT_ALLOWED"},
		{"needs approval", &itemservice.NeedsApprovalError{Scope: uuid.New()}, connect.CodePermissionDenied, "NEEDS_APPROVAL"},
		{"guard", &itemservice.RejectedError{Code: "guard_failed", Reason: "guard f_assignee != null does not hold"},
			connect.CodeFailedPrecondition, "GUARD_FAILED"},
		{"missing fields", &itemservice.RejectedError{Code: "missing_fields"}, connect.CodeFailedPrecondition, "MISSING_FIELDS"},
		{"hop limit", fmt.Errorf("wrapped: %w", &itemservice.RejectedError{Code: "hop_limit"}),
			connect.CodeFailedPrecondition, "HOP_LIMIT"},
		{"stale", fmt.Errorf("%w: at 3, not 2", itemservice.ErrStale), connect.CodeAborted, "STALE"},
		// A held refusal is also an ErrStale: HELD must win.
		{"held", &itemservice.HeldError{HoldID: hold}, connect.CodeAborted, "HELD"},
		{"idempotency key reuse", itemservice.ErrIdempotencyKeyReuse, connect.CodeAlreadyExists, "IDEMPOTENCY_KEY_REUSED"},
		{"not caught up", itemservice.ErrNotCaughtUp, connect.CodeUnavailable, "NOT_CAUGHT_UP"},
		{"anything else", errors.New("pq: relation items_secret does not exist"), connect.CodeInternal, "INTERNAL"},
		// Step 0, part 2.
		{"no transition", &itemservice.RejectedError{Code: "no_transition"}, connect.CodeFailedPrecondition, "NO_TRANSITION"},
		{"widen not confirmed", &itemservice.RejectedError{Code: "widen_not_confirmed"},
			connect.CodeFailedPrecondition, "WIDEN_NOT_CONFIRMED"},
		{"session invalid", fmt.Errorf("get: %w", itemservice.ErrSessionInvalid), connect.CodeUnauthenticated, "SESSION_INVALID"},
		// A rejection without a code is a bug in the kernel, not the caller's
		// problem: INTERNAL, never a made-up REJECTED.
		{"bare rejection", fmt.Errorf("%w: something", itemservice.ErrRejected), connect.CodeInternal, "INTERNAL"},
		{"rejection without a code", &itemservice.RejectedError{Reason: "no code"}, connect.CodeInternal, "INTERNAL"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ce := errorFor(c.err, itemName)
			if ce.Code() != c.code {
				t.Errorf("code = %v, want %v", ce.Code(), c.code)
			}
			ei, _ := info(t, ce)
			if ei.Reason != c.reason || ei.Domain != ErrorDomain {
				t.Errorf("ErrorInfo = %s/%s, want %s/%s", ei.Domain, ei.Reason, ErrorDomain, c.reason)
			}
			// Step 0, part 2: one fixed message per reason.
			if want, ok := ReasonMessage(c.reason); !ok || ce.Message() != want {
				t.Errorf("message = %q, want the fixed message of %s (%q, known %v)", ce.Message(), c.reason, want, ok)
			}
		})
	}
}

// HELD names the hold; nothing else carries metadata in part 1.
func TestErrorFor_HeldNamesTheHold(t *testing.T) {
	const itemName = "namespaces/acme/items/0190c0de-0000-7000-8000-000000000001"
	hold := uuid.MustParse("0190c0de-0000-7000-8000-0000000000aa")
	ei, _ := info(t, errorFor(fmt.Errorf("update: %w", &itemservice.HeldError{HoldID: hold}), itemName))
	if want := itemName + "/holds/" + hold.String(); ei.Metadata["hold"] != want {
		t.Errorf("metadata = %v, want hold = %s", ei.Metadata, want)
	}
}

// A field error names the field as a path in the request: fields.<id>.
func TestErrorFor_FieldViolation(t *testing.T) {
	_, fvs := info(t, errorFor(&itemservice.InvalidError{Field: "f_priority", Reason: "want a whole number"}, ""))
	if len(fvs) != 1 || fvs[0].Field != "fields.f_priority" || fvs[0].Description == "" {
		t.Errorf("field violations = %v, want one for fields.f_priority with a description", fvs)
	}
}

// NOT_FOUND says the same thing whatever the kernel's error text was, and
// INTERNAL says nothing at all: neither may leak what the caller can't see.
func TestErrorFor_NothingLeaks(t *testing.T) {
	a := errorFor(itemservice.ErrNotFound, "")
	b := errorFor(fmt.Errorf("%w: container 0190c0de-0000-7000-8000-000000000002 is narrowed", itemservice.ErrNotFound), "")
	if a.Message() != b.Message() {
		t.Errorf("NOT_FOUND messages differ: %q vs %q", a.Message(), b.Message())
	}
	ia, _ := info(t, a)
	ib, _ := info(t, b)
	if len(ia.Metadata) != 0 || len(ib.Metadata) != 0 {
		t.Errorf("NOT_FOUND carries metadata: %v, %v", ia.Metadata, ib.Metadata)
	}

	internal := errorFor(errors.New("pq: relation items_secret does not exist"), "")
	if strings.Contains(internal.Message(), "items_secret") {
		t.Errorf("INTERNAL message %q leaks the cause", internal.Message())
	}
}

// Step 0, part 2 (PR #18 review), replacing part 1's "a rule's refusal
// keeps its reason text": the message is the reason's fixed one, and what the
// caller needs to fix the Item travels as metadata (field IDs and states,
// never values).
func TestErrorFor_RejectionDetailsGoInMetadata(t *testing.T) {
	ce := errorFor(&itemservice.RejectedError{
		Code: "missing_fields", Reason: "f_resolution is required", Metadata: map[string]string{"fields": "f_resolution"},
	}, "")
	if strings.Contains(ce.Message(), "f_resolution") {
		t.Errorf("message %q carries the details; they belong in metadata", ce.Message())
	}
	if ei, _ := info(t, ce); ei.Metadata["fields"] != "f_resolution" {
		t.Errorf("metadata = %v, want fields = f_resolution", ei.Metadata)
	}
	guard := errorFor(&itemservice.RejectedError{
		Code: "guard_failed", Metadata: map[string]string{"from_state": "open", "to_state": "in_progress"},
	}, "")
	if ei, _ := info(t, guard); ei.Metadata["from_state"] != "open" || ei.Metadata["to_state"] != "in_progress" {
		t.Errorf("guard metadata = %v, want from_state open, to_state in_progress", ei.Metadata)
	}
}

// idLike matches what must never be in a message: a UUID, a typed principal,
// a resource name, a session ID, a hold.
var idLike = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}|\b(user|agent|group|system|hook):|namespaces/|ses_|holds/`)

// Every reason the API can answer has one fixed message, and no message
// contains an ID. Every rejection code of the kernel is a known reason.
func TestReasonMessage_EveryReasonFixedAndFreeOfIDs(t *testing.T) {
	reasons := []string{
		"UNAUTHENTICATED", "SESSION_INVALID", "INVALID_REQUEST", "NOT_FOUND", "NOT_ALLOWED", "NEEDS_APPROVAL",
		"STALE", "HELD", "IDEMPOTENCY_KEY_REUSED", "NOT_CAUGHT_UP", "CANCELED", "DEADLINE_EXCEEDED", "INTERNAL",
	}
	for _, code := range []string{
		itemservice.CodeNoTransition, itemservice.CodeMissingFields, itemservice.CodeGuardFailed,
		itemservice.CodeNotInitialState, itemservice.CodeHopLimit, itemservice.CodeWidenNotConfirmed,
	} {
		reasons = append(reasons, strings.ToUpper(code))
	}
	for _, r := range reasons {
		msg, ok := ReasonMessage(r)
		if !ok || msg == "" {
			t.Errorf("%s has no fixed message", r)
			continue
		}
		if idLike.MatchString(msg) {
			t.Errorf("%s's message %q contains something like an ID", r, msg)
		}
	}
	if _, ok := ReasonMessage("REJECTED"); ok {
		t.Error("REJECTED is still a reason; an uncoded rejection is INTERNAL")
	}
}

// A canceled or timed-out call keeps its own code rather than INTERNAL.
func TestErrorFor_ContextErrors(t *testing.T) {
	if c := errorFor(fmt.Errorf("read: %w", context.Canceled), "").Code(); c != connect.CodeCanceled {
		t.Errorf("canceled: code %v", c)
	}
	if c := errorFor(fmt.Errorf("read: %w", context.DeadlineExceeded), "").Code(); c != connect.CodeDeadlineExceeded {
		t.Errorf("deadline: code %v", c)
	}
}
