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
// Clients identify an error by (reason, domain), so this must not change
// once someone depends on it: docs/journal.md, Later, confirms it before
// Step 0, part 3 sets the buf breaking baseline.
const ErrorDomain = "pli.dev"

// ReasonMessage returns the one fixed message of an ErrorInfo reason, and
// whether the reason is known. Every error the API returns carries exactly
// this message: dynamic values go in ErrorInfo metadata, never in a message.
func ReasonMessage(reason string) (string, bool) {
	return "", false // not built yet: Step 0, part 2
}

// notFoundMessage is the one message of every NOT_FOUND, whatever the
// kernel's error said: hidden and missing must read the same.
const notFoundMessage = "not found"

// apiError builds an error with a standard code and an ErrorInfo carrying a
// stable reason (AIP-193). Extra details, such as a BadRequest, follow it.
func apiError(code connect.Code, reason, message string, metadata map[string]string,
	extra ...*errdetails.BadRequest) *connect.Error {
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
// (fields.f_priority, move.container).
func badField(field, description string) *connect.Error {
	return apiError(connect.CodeInvalidArgument, "INVALID_REQUEST", field+": "+description, nil,
		&errdetails.BadRequest{FieldViolations: []*errdetails.BadRequest_FieldViolation{
			{Field: field, Description: description},
		}})
}

// notFound is the NOT_FOUND of the error table.
func notFound() *connect.Error {
	return apiError(connect.CodeNotFound, "NOT_FOUND", notFoundMessage, nil)
}

// errorFor maps a kernel error to the error table in docs/design.md. itemName
// is the Item the request named, if any: a HELD error names its hold under it.
func errorFor(err error, itemName string) *connect.Error {
	return errorWith(err, itemName, "readers")
}

// errorWith is errorFor with the request path of the reader list, for a
// malformed one: narrow.principals on a create, policy.narrow.principals on
// an update.
func errorWith(err error, itemName, readersPath string) *connect.Error {
	// Each case asks errors.Is / errors.As, which look through every wrapped
	// error. Order matters: a held refusal is also stale, so HELD comes first.
	var (
		held     *itemservice.HeldError
		invalid  *itemservice.InvalidError
		rejected *itemservice.RejectedError
	)
	switch {
	case errors.As(err, &held):
		var md map[string]string
		if itemName != "" {
			md = map[string]string{"hold": holdName(itemName, held.HoldID)}
		}
		return apiError(connect.CodeAborted, "HELD", held.Error(), md)
	case errors.Is(err, itemservice.ErrHeld):
		return apiError(connect.CodeAborted, "HELD", err.Error(), nil)
	case errors.Is(err, itemservice.ErrStale):
		return apiError(connect.CodeAborted, "STALE", err.Error(), nil)
	case errors.Is(err, itemservice.ErrNotFound):
		return notFound()
	case errors.As(err, &invalid):
		field := readersPath
		if !invalid.Readers {
			field = "fields." + invalid.Field
		}
		return badField(field, invalid.Reason)
	case errors.Is(err, itemservice.ErrInvalid):
		return apiError(connect.CodeInvalidArgument, "INVALID_REQUEST", err.Error(), nil)
	case errors.Is(err, itemservice.ErrNeedsApproval):
		return apiError(connect.CodePermissionDenied, "NEEDS_APPROVAL", err.Error(), nil)
	case errors.Is(err, itemservice.ErrForbidden):
		return apiError(connect.CodePermissionDenied, "NOT_ALLOWED", err.Error(), nil)
	case errors.As(err, &rejected):
		return apiError(connect.CodeFailedPrecondition, strings.ToUpper(rejected.Code), rejected.Reason, nil)
	case errors.Is(err, itemservice.ErrRejected):
		return apiError(connect.CodeFailedPrecondition, "REJECTED", err.Error(), nil)
	case errors.Is(err, itemservice.ErrIdempotencyKeyReuse):
		return apiError(connect.CodeAlreadyExists, "IDEMPOTENCY_KEY_REUSED", err.Error(), nil)
	case errors.Is(err, itemservice.ErrNotCaughtUp):
		return apiError(connect.CodeUnavailable, "NOT_CAUGHT_UP", err.Error(), nil)
	// The caller gave up (canceled) or ran out of time: their doing, not a
	// server fault, so these keep their own codes.
	case errors.Is(err, context.Canceled):
		return apiError(connect.CodeCanceled, "CANCELED", "the call was canceled", nil)
	case errors.Is(err, context.DeadlineExceeded):
		return apiError(connect.CodeDeadlineExceeded, "DEADLINE_EXCEEDED", "the call ran out of time", nil)
	default:
		// Never send the cause: it may name tables, IDs or values. Log it.
		slog.Error("api: internal error", "error", err)
		return apiError(connect.CodeInternal, "INTERNAL", "internal error", nil)
	}
}
