// Tests for how the activities report a missing Item, against the real kernel.
package workflow_test

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	"go.temporal.io/sdk/temporal"

	"github.com/francoisnyc/work-kernel/internal/itemservice"
	wf "github.com/francoisnyc/work-kernel/internal/workflow"
)

// Both activities turn itemservice.ErrNotFound into a non-retryable error of type
// NotFound. Left as a plain error it would be retried forever.
func TestActivities_MissingItem_IsANonRetryableNotFound(t *testing.T) {
	a := &wf.Activities{Items: itemservice.New(pool)}
	missing := uuid.NewString()

	_, loadErr := a.LoadItem(serverCtx(t), missing)
	fireErr := a.FireTimer(serverCtx(t), wf.FireTimerInput{
		ItemID: missing, TypeID: "job", TypeVersion: 1, FromState: "running", ToState: "queued",
		ExpectedItemSeq: 2, EnteredSeq: 2, Hop: 1, IdempotencyKey: "timer:" + missing + ":2:2",
	})

	for name, err := range map[string]error{"LoadItem": loadErr, "FireTimer": fireErr} {
		var app *temporal.ApplicationError
		if !errors.As(err, &app) || app.Type() != wf.ErrTypeNotFound || !app.NonRetryable() {
			t.Errorf("%s error = %v, want a non-retryable %q", name, err, wf.ErrTypeNotFound)
		}
	}
}

// Step 6: the item service's ErrInvalid (here hop 0, which no kernel write
// may carry) is final too. Left as a plain error, Temporal would retry it
// forever, billed per attempt.
func TestActivities_InvalidTimerWrite_IsNonRetryable(t *testing.T) {
	a := &wf.Activities{Items: itemservice.New(pool)}
	id := uuid.NewString()
	err := a.FireTimer(serverCtx(t), wf.FireTimerInput{
		ItemID: id, TypeID: "job", TypeVersion: 1, FromState: "running", ToState: "queued",
		ExpectedItemSeq: 2, EnteredSeq: 2, Hop: 0, IdempotencyKey: "timer:" + id + ":2:2",
	})
	var app *temporal.ApplicationError
	if !errors.As(err, &app) || app.Type() != wf.ErrTypeInvalid || !app.NonRetryable() {
		t.Errorf("FireTimer error = %v, want a non-retryable %q", err, wf.ErrTypeInvalid)
	}
}
