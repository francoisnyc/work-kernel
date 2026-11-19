package itemservice

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/lifecycle"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// Typed refusals. The sentinels in service.go say what kind of refusal an
// error is (errors.Is); these types carry what a caller needs to act on it
// (errors.As), so nobody parses error text. An Unwrap method links each one
// to its sentinel, the way a Java exception subclass is caught by a catch of
// its parent.

// Rejection codes: why the Type's rules refuse valid input. The lifecycle's
// own codes pass through unchanged.
const (
	CodeNoTransition      = string(lifecycle.NoTransition)  // the Type declares no such move
	CodeMissingFields     = string(lifecycle.MissingFields) // a required field is not set
	CodeGuardFailed       = string(lifecycle.GuardFailed)   // the guard does not hold
	CodeNotInitialState   = "not_initial_state"             // a create names a state the Type does not start in
	CodeHopLimit          = "hop_limit"                     // a chain of kernel writes went too deep
	CodeWidenNotConfirmed = "widen_not_confirmed"           // a move widens who can see; the caller must confirm it
	CodeAudienceChanged   = "audience_changed"              // an approval's audience is not the one the approver was shown
	CodeFieldFrozen       = string(lifecycle.FieldFrozen)   // a write names a field the Item's state freezes
	CodeEffectFailed      = string(lifecycle.EffectFailed)  // a transition's `sets` expression failed (metadata: field)
)

// FieldNotWritableError is an ErrForbidden naming the fields a write may not
// set: fields only the kernel writes (writable_by: kernel), refused on the
// public door, and fields a transition's own `allowed` names through
// field:<id>, refused in that transition's patch on every door. Fields are
// IDs, sorted.
type FieldNotWritableError struct {
	Fields []string
	Reason string
}

func (e *FieldNotWritableError) Error() string { return fmt.Sprintf("%v: %s", ErrForbidden, e.Reason) }
func (e *FieldNotWritableError) Unwrap() error { return ErrForbidden }

// RejectedError is an ErrRejected with its code: the request is well formed
// and the rules say no. Reason names field IDs and states, never values.
type RejectedError struct {
	Code   string
	Reason string
	// Metadata carries the refusal's details for the caller, keyed like the
	// API's ErrorInfo metadata: from_state and to_state for a transition,
	// fields (comma-separated IDs) for missing fields. Never a value.
	Metadata map[string]string
	cause    error // the lifecycle's Rejection, when it decided
}

func (e *RejectedError) Error() string { return fmt.Sprintf("%v: %s", ErrRejected, e.Reason) }

// Unwrap returns both the sentinel and the cause; errors.Is and errors.As
// search both.
func (e *RejectedError) Unwrap() []error {
	if e.cause != nil {
		return []error{ErrRejected, e.cause}
	}
	return []error{ErrRejected}
}

// rejected builds a RejectedError.
func rejected(code, format string, args ...any) error {
	return &RejectedError{Code: code, Reason: fmt.Sprintf(format, args...)}
}

// fromLifecycle turns the lifecycle's refusal into a RejectedError with its
// code; any other error passes through.
func fromLifecycle(err error) error {
	var rej *lifecycle.Rejection
	if errors.As(err, &rej) {
		md := map[string]string{}
		if rej.From != "" || rej.To != "" {
			md["from_state"], md["to_state"] = rej.From, rej.To
		}
		if len(rej.Missing) > 0 {
			md["fields"] = strings.Join(rej.Missing, ",")
		}
		if rej.Code == lifecycle.FieldFrozen {
			md["fields"], md["state"] = strings.Join(rej.Frozen, ","), rej.State
		}
		if rej.Code == lifecycle.InvariantFailed {
			md = map[string]string{"invariant": rej.Invariant}
		}
		return &RejectedError{Code: string(rej.Code), Reason: rej.Reason, Metadata: md, cause: rej}
	}
	return err
}

// InvalidError is an ErrInvalid that names what is malformed: a field value
// (Field, its field ID) or the reader list (Readers).
type InvalidError struct {
	Field   string // the field ID, for a field value
	Readers bool   // the reader list of a Narrow or a policy patch
	// Path names any other malformed part of the request as the API's
	// request path: page_token, filter.types, filter.fields[0], scopes,
	// powers.
	Path   string
	Reason string
}

func (e *InvalidError) Error() string { return fmt.Sprintf("%v: %s", ErrInvalid, e.Reason) }
func (e *InvalidError) Unwrap() error { return ErrInvalid }

// fieldsInvalid turns the Type's verdict on a set of field values into an
// InvalidError naming the field.
func fieldsInvalid(err error) error {
	var fe *typedef.FieldError
	if errors.As(err, &fe) {
		return &InvalidError{Field: fe.Field, Reason: fe.Error()}
	}
	return fmt.Errorf("%w: %v", ErrInvalid, err)
}

// readersInvalid is a malformed reader list.
func readersInvalid(format string, args ...any) error {
	return &InvalidError{Readers: true, Reason: fmt.Sprintf(format, args...)}
}

// HeldError is an ErrHeld (an ErrStale) naming the open hold that refuses
// the write.
type HeldError struct {
	HoldID uuid.UUID
	Reason string
}

func (e *HeldError) Error() string {
	return fmt.Sprintf("%v: hold %s: %s", ErrHeld, e.HoldID, e.Reason)
}
func (e *HeldError) Unwrap() error { return ErrHeld }

// ErrNotYet answers a timer at a field's instant that fired early: the
// write's time is still before the instant. Nothing is written and the key
// is not used up; the workflow arms again for the remainder. It is none of
// ErrRejected, ErrStale or ErrInvalid.
var ErrNotYet = errors.New("not yet")

// NotYetError is ErrNotYet with how long remains until the instant, by the
// item service's clock. Never zero or negative.
type NotYetError struct {
	Remaining time.Duration
}

func (e *NotYetError) Error() string {
	return fmt.Sprintf("%v: the instant is %v away", ErrNotYet, e.Remaining)
}
func (e *NotYetError) Unwrap() error { return ErrNotYet }

// AtInstant is what the workflow may know of an Item's timer at a field's
// instant: the instant held by the field its current state names in `at`.
// It is the one field value that reaches the workflow (docs/design.md).
type AtInstant struct {
	State   string
	ItemSeq int64
	// Set is false when the state declares no `at`, or its field is unset.
	Set bool
	At  time.Time
}

// AtInstant reads the instant of the field the Item's current state names
// in `at`, and nothing else. A kernel read, for the worker only.
//
// Phase B stub: not implemented.
func (k *Service) AtInstant(ctx context.Context, id uuid.UUID) (AtInstant, error) {
	return AtInstant{}, errors.New("itemservice: AtInstant is not implemented")
}
