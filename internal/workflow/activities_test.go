// Tests for how the activities report a missing Item, against the real kernel.
package workflow_test

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	"go.temporal.io/sdk/temporal"

	"github.com/francoisnyc/work-kernel/internal/kernel"
	wf "github.com/francoisnyc/work-kernel/internal/workflow"
)

// Both activities turn kernel.ErrNotFound into a non-retryable error of type
// NotFound. Left as a plain error it would be retried forever.
func TestActivities_MissingItem_IsANonRetryableNotFound(t *testing.T) {
	a := &wf.Activities{Kernel: kernel.New(pool)}
	missing := uuid.NewString()

	_, loadErr := a.LoadItem(serverCtx(t), missing)
	fireErr := a.FireTimer(serverCtx(t), wf.FireTimerInput{
		ItemID: missing, TypeID: "job", TypeVersion: 1, FromState: "running", ToState: "queued",
		ExpectedItemSeq: 2, EnteredSeq: 2, IdempotencyKey: "timer:" + missing + ":2:2",
	})

	for name, err := range map[string]error{"LoadItem": loadErr, "FireTimer": fireErr} {
		var app *temporal.ApplicationError
		if !errors.As(err, &app) || app.Type() != wf.ErrTypeNotFound || !app.NonRetryable() {
			t.Errorf("%s error = %v, want a non-retryable %q", name, err, wf.ErrTypeNotFound)
		}
	}
}
