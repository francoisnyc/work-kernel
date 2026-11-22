// Tests for the hook protocol spike, in the workflow: a check whose remote
// connector is misconfigured fails at once, like any permanent failure, but
// its hold names the cause: connector_unconfigured (no connection for its
// connector@major), connector_unauthorized (the connector refused the
// worker's signature) or connector_mismatch (the connector at the URL is
// not the declared one, or answered a reason it does not declare). A
// misconfiguration shows at once instead of using up the budget. Anything
// retryable still ends as budget_exhausted.
package workflow_test

import (
	"reflect"
	"testing"
	"time"

	"go.temporal.io/sdk/temporal"

	wf "github.com/francoisnyc/work-kernel/internal/workflow"
)

func TestHooks_ConnectorFaultsHaveTheirOwnReasons(t *testing.T) {
	for errType, reason := range map[string]string{
		wf.ErrTypeConnectorUnconfigured: wf.ReasonConnectorUnconfigured,
		wf.ErrTypeConnectorUnauthorized: wf.ReasonConnectorUnauthorized,
		wf.ErrTypeConnectorMismatch:     wf.ReasonConnectorMismatch,
		wf.ErrTypeHookFailed:            "check_failed", // unchanged
	} {
		t.Run(reason, func(t *testing.T) {
			h := newHookHarness(t, orderSnapshot("open", 5, 5))
			h.checkErr["check"] = temporal.NewNonRetryableApplicationError("the connector", errType, nil)
			h.deliver(0, requestEvent(6))
			h.deliver(time.Hour, closing(7, "transition_failed", ""))
			h.runToCompletion()

			if len(h.check) != 1 {
				t.Errorf("RunCheck calls = %d, want 1: a misconfiguration is not retried", len(h.check))
			}
			want := wf.ResolveInput{Hook: checkInput(6, "corr-7", 1), Verdict: "error", Reason: reason}
			if len(h.resolves) != 1 || !reflect.DeepEqual(h.resolves[0].In, want) || !h.resolves[0].At.Equal(t0) {
				t.Errorf("ResolveHold calls = %+v, want one at once: %+v", h.resolves, want)
			}
		})
	}
}

// The reasons are the codes design.md names; the PoC app's timeline turns
// them into words.
func TestHooks_ConnectorReasonCodes(t *testing.T) {
	if wf.ReasonConnectorUnconfigured != "connector_unconfigured" || wf.ReasonConnectorUnauthorized != "connector_unauthorized" ||
		wf.ReasonConnectorMismatch != "connector_mismatch" {
		t.Errorf("reasons %q %q %q", wf.ReasonConnectorUnconfigured, wf.ReasonConnectorUnauthorized, wf.ReasonConnectorMismatch)
	}
}
