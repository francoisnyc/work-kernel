package api

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/genproto/googleapis/rpc/errdetails"

	"github.com/francoisnyc/work-kernel/internal/itemservice"
)

// ErrorDomain is the ErrorInfo domain of every error this API returns.
// Clients identify an error by (reason, domain), so it is frozen from Step 0,
// part 3 on (a test holds it). It is a label only, never resolved.
const ErrorDomain = "pli.francoisnyc.github.io"

// messages are the one fixed message of each ErrorInfo reason (PR #18
// review). Every error the API returns carries exactly its reason's
// message: dynamic values (the hold, the scope, the states, the fields, the
// header) go in ErrorInfo metadata, never in a message, so a message never
// names anything, the caller can see it or not.
var messages = map[string]string{
	"UNAUTHENTICATED":        "a valid bearer token is required",
	"SESSION_INVALID":        "the agent session is not valid for this token",
	"INVALID_REQUEST":        "the request is malformed",
	"NOT_FOUND":              "not found",
	"NOT_ALLOWED":            "the caller may not do this",
	"NEEDS_APPROVAL":         "the agent session needs its user's approval to write there",
	"STALE":                  "the Item has changed since it was read",
	"HELD":                   "a check is holding the Item",
	"IDEMPOTENCY_KEY_REUSED": "the request_id was used for a different request",
	"NOT_CAUGHT_UP":          "the read has not caught up with that write yet",
	"CANCELED":               "the call was canceled",
	"DEADLINE_EXCEEDED":      "the call ran out of time",
	"INTERNAL":               "internal error",
	// A rule's refusal: the kernel's rejection codes, upper-cased.
	"NO_TRANSITION":       "the Type has no such transition",
	"MISSING_FIELDS":      "the transition needs fields that are not set",
	"GUARD_FAILED":        "the transition's guard does not hold",
	"NOT_INITIAL_STATE":   "the Type does not start in that state",
	"HOP_LIMIT":           "the chain of kernel writes went too deep",
	"WIDEN_NOT_CONFIRMED": "the move lets more people see; confirm it",
	"AUDIENCE_CHANGED":    "the approval's audience changed since it was shown",
	"LINK_CARDINALITY":    "that side of the link type already has its one link",
	"ALREADY_LINKED":      "the link already exists",
	"NOT_LINKED":          "there is no such link",
	"LINK_TARGET_TYPE":    "the link type does not allow a target of that Type",
	"FIELD_NOT_WRITABLE":  "the caller may not write these fields",
	"FIELD_FROZEN":        "these fields are frozen in the Item's state",
}

// ReasonMessage returns the one fixed message of an ErrorInfo reason, and
// whether the reason is known.
func ReasonMessage(reason string) (string, bool) {
	m, ok := messages[reason]
	return m, ok
}

// apiError builds an error with a standard code and an ErrorInfo carrying a
// stable reason (AIP-193) and the reason's fixed message. Extra details, such
// as a BadRequest, follow it. An unknown reason is a bug: it answers INTERNAL.
func apiError(code connect.Code, reason string, metadata map[string]string,
	extra ...*errdetails.BadRequest) *connect.Error {
	message, ok := ReasonMessage(reason)
	if !ok {
		slog.Error("api: unknown error reason", "reason", reason)
		code, reason, message, metadata, extra = connect.CodeInternal, "INTERNAL", messages["INTERNAL"], nil, nil
	}
	ce := connect.NewError(code, errors.New(message))
	// NewErrorDetail only fails for a message it cannot marshal; these are
	// generated types, so the error is ignored.
	if d, err := connect.NewErrorDetail(&errdetails.ErrorInfo{
		Reason: reason, Domain: ErrorDomain, Metadata: metadata,
	}); err == nil {
		ce.AddDetail(d)
	}
	for _, br := range extra {
		if d, err := connect.NewErrorDetail(br); err == nil {
			ce.AddDetail(d)
		}
	}
	return ce
}

// badField is INVALID_ARGUMENT naming a field of the request, as a path
// (fields.f_priority, move.container, filter.fields[0]). The description
// may name the field, never a value or an ID.
func badField(field, description string) *connect.Error {
	return apiError(connect.CodeInvalidArgument, "INVALID_REQUEST", nil,
		&errdetails.BadRequest{FieldViolations: []*errdetails.BadRequest_FieldViolation{
			{Field: field, Description: description},
		}})
}

// badHeader is INVALID_ARGUMENT naming a request header in metadata.
func badHeader(header string) *connect.Error {
	return apiError(connect.CodeInvalidArgument, "INVALID_REQUEST", map[string]string{"header": header})
}

// notFound is the NOT_FOUND of the error table.
func notFound() *connect.Error {
	return apiError(connect.CodeNotFound, "NOT_FOUND", nil)
}

// audience is who an approval would open an agent session's content to:
// everyone in the namespace (an open scope) or the scope's readers.
func audience(open bool) string {
	return itemservice.AudienceName(open)
}

// errorFor maps a kernel error to the error table in docs/design.md. itemName
// is the Item the request named, if any: a HELD error names its hold under it,
// and a NEEDS_APPROVAL names its scope in the item's namespace.
func errorFor(err error, item string) *connect.Error {
	ns, _, _ := parseItemName(item)
	return errorWith(err, item, "readers", ns)
}

// errorIn is errorFor for a caller in namespace ns, which names a
// NEEDS_APPROVAL's scope even when the request named no Item.
func errorIn(err error, ns, item string) *connect.Error {
	return errorWith(err, item, "readers", ns)
}

// errorWith is errorFor with the request path of the reader list, for a
// malformed one (narrow.principals on a create, policy.narrow.principals on
// an update), and the caller's namespace.
func errorWith(err error, item, readersPath, ns string) *connect.Error {
	// Each case asks errors.Is / errors.As, which look through every wrapped
	// error. Order matters: a held refusal is also stale, so HELD comes
	// first; a bad session is also invalid, so SESSION_INVALID comes before
	// INVALID_REQUEST.
	var (
		held     *itemservice.HeldError
		invalid  *itemservice.InvalidError
		rejected *itemservice.RejectedError
		approval *itemservice.NeedsApprovalError
		fieldNW  *itemservice.FieldNotWritableError
	)
	switch {
	case errors.As(err, &held):
		var md map[string]string
		if item != "" {
			md = map[string]string{"hold": holdName(item, held.HoldID)}
		}
		return apiError(connect.CodeAborted, "HELD", md)
	case errors.Is(err, itemservice.ErrHeld):
		return apiError(connect.CodeAborted, "HELD", nil)
	case errors.Is(err, itemservice.ErrStale):
		return apiError(connect.CodeAborted, "STALE", nil)
	case errors.Is(err, itemservice.ErrNotFound):
		return notFound()
	case errors.Is(err, itemservice.ErrSessionInvalid):
		return apiError(connect.CodeUnauthenticated, "SESSION_INVALID", nil)
	case errors.As(err, &invalid):
		switch {
		case invalid.Path != "":
			return badField(invalid.Path, invalid.Reason)
		case invalid.Readers:
			return badField(readersPath, invalid.Reason)
		}
		return badField("fields."+invalid.Field, invalid.Reason)
	case errors.Is(err, itemservice.ErrInvalid):
		return apiError(connect.CodeInvalidArgument, "INVALID_REQUEST", nil)
	case errors.As(err, &approval):
		var md map[string]string
		if ns != "" {
			scope := namespaceName(ns)
			if approval.ScopeItem != nil {
				scope = itemName(ns, *approval.ScopeItem)
			}
			md = map[string]string{"scope": scope, "audience": audience(approval.Open)}
		}
		return apiError(connect.CodePermissionDenied, "NEEDS_APPROVAL", md)
	case errors.As(err, &fieldNW):
		return apiError(connect.CodePermissionDenied, "FIELD_NOT_WRITABLE",
			map[string]string{"fields": strings.Join(fieldNW.Fields, ",")})
	case errors.Is(err, itemservice.ErrForbidden):
		return apiError(connect.CodePermissionDenied, "NOT_ALLOWED", nil)
	// A rule's refusal answers with its code. One without a code is a bug
	// in the kernel, not the caller's problem: INTERNAL, like anything else.
	case errors.As(err, &rejected) && rejected.Code != "":
		return apiError(connect.CodeFailedPrecondition, strings.ToUpper(rejected.Code), rejected.Metadata)
	case errors.Is(err, itemservice.ErrIdempotencyKeyReuse):
		return apiError(connect.CodeAlreadyExists, "IDEMPOTENCY_KEY_REUSED", nil)
	case errors.Is(err, itemservice.ErrNotCaughtUp):
		return apiError(connect.CodeUnavailable, "NOT_CAUGHT_UP", nil)
	// The caller gave up (canceled) or ran out of time: their doing, not a
	// server fault, so these keep their own codes.
	case errors.Is(err, context.Canceled):
		return apiError(connect.CodeCanceled, "CANCELED", nil)
	case errors.Is(err, context.DeadlineExceeded):
		return apiError(connect.CodeDeadlineExceeded, "DEADLINE_EXCEEDED", nil)
	default:
		// Never send the cause: it may name tables, IDs or values. Log it.
		slog.Error("api: internal error", "error", err)
		return apiError(connect.CodeInternal, "INTERNAL", nil)
	}
}
