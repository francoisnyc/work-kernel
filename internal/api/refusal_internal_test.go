// Tests for Refusal (Ventoux item 5): a story's "-> refused REASON" is the
// reason the API answers with, read through the same table as errorFor's,
// so a story and pli never name a refusal differently.
package api

import (
	"errors"
	"fmt"
	"testing"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/itemservice"
)

func TestRefusal_IsErrorForsReason(t *testing.T) {
	hold := uuid.MustParse("0190c0de-0000-7000-8000-0000000000aa")
	for name, err := range map[string]error{
		"forbidden":          fmt.Errorf("%w: except creator", itemservice.ErrForbidden),
		"guard":              &itemservice.RejectedError{Code: "guard_failed"},
		"frozen":             &itemservice.RejectedError{Code: "field_frozen"},
		"invariant":          fmt.Errorf("wrapped: %w", &itemservice.RejectedError{Code: "invariant_failed"}),
		"not found":          itemservice.ErrNotFound,
		"held":               &itemservice.HeldError{HoldID: hold},
		"stale":              itemservice.ErrStale,
		"field not writable": &itemservice.FieldNotWritableError{Fields: []string{"f_used"}},
		"invalid":            &itemservice.InvalidError{Field: "f_days", Reason: "want a whole number"},
		"anything else":      errors.New("the database went away"),
	} {
		t.Run(name, func(t *testing.T) {
			ei, _ := info(t, errorFor(err, ""))
			if got := Refusal(err); got != ei.Reason {
				t.Errorf("Refusal = %q, want errorFor's reason %q", got, ei.Reason)
			}
		})
	}
}

func TestRefusal_SpotChecks(t *testing.T) {
	for err, want := range map[error]string{
		fmt.Errorf("%w: except creator", itemservice.ErrForbidden): "NOT_ALLOWED",
		&itemservice.RejectedError{Code: "guard_failed"}:           "GUARD_FAILED",
		itemservice.ErrNotFound:                                    "NOT_FOUND",
		nil:                                                        "",
	} {
		if got := Refusal(err); got != want {
			t.Errorf("Refusal(%v) = %q, want %q", err, got, want)
		}
	}
}
