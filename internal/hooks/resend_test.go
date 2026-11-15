// Tests for Step 7: which layer retries a request whose answer was dropped.
//
// Go's net/http resends a request on its own when a reused connection is
// closed before the answer, if the request is replayable. A POST with an
// Idempotency-Key header counts as replayable (net/http, Request.isReplayable),
// so with the default connectors a dropped invoice answer is resent inside
// the same activity attempt, and Temporal never sees the failure.
//
// WithoutResend (bench and tests only) opens a new connection for every
// request, so net/http never resends: the error reaches the caller, and the
// retry is a new Temporal attempt. Both layers send the same key.
package hooks_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/hooks"
)

// The default connectors: net/http resends on the warm connection, with the
// same key, and the ERP returns the invoice the dropped request made.
func TestDroppedAnswer_DefaultConnectors_NetHTTPResends(t *testing.T) {
	w := newWorld(t)
	w.erp.SetDropFirstAnswer(true)
	if _, err := w.c.Run(ctx(t), "erp.get_credit_status", "", []any{"cust-1"}); err != nil { // warms a connection
		t.Fatalf("warm-up: %v", err)
	}
	res, err := w.c.Run(ctx(t), "erp.create_invoice", "k-1", []any{"cust-1", float64(100)})
	if err != nil {
		t.Fatalf("create_invoice: %v; want net/http to resend the dropped request", err)
	}
	got := w.erp.Invoices("cust-1")
	if len(got) != 1 || !reflect.DeepEqual(res.Values, []any{got[0]}) || w.erp.DroppedAnswers() != 1 {
		t.Errorf("result %v, ERP invoices %v, dropped %d; want one invoice, returned, after one dropped answer",
			res.Values, got, w.erp.DroppedAnswers())
	}
}

// WithoutResend: the dropped answer is a retryable error for the caller.
// The caller's retry, with the same key, gets the invoice already made.
func TestDroppedAnswer_WithoutResend_TheCallerRetries(t *testing.T) {
	w := newWorld(t)
	w.erp.SetDropFirstAnswer(true)
	c := w.c.WithoutResend()
	if _, err := c.Run(ctx(t), "erp.get_credit_status", "", []any{"cust-1"}); err != nil {
		t.Fatalf("warm-up: %v", err)
	}
	_, err := c.Run(ctx(t), "erp.create_invoice", "k-1", []any{"cust-1", float64(100)})
	if err == nil || errors.Is(err, hooks.ErrPermanent) {
		t.Fatalf("create_invoice: error %v, want a retryable error for the caller", err)
	}
	if got := w.erp.Invoices("cust-1"); len(got) != 1 {
		t.Fatalf("ERP invoices %v after the dropped answer, want one", got)
	}
	res, err := c.Run(ctx(t), "erp.create_invoice", "k-1", []any{"cust-1", float64(100)})
	if got := w.erp.Invoices("cust-1"); err != nil || len(got) != 1 || !reflect.DeepEqual(res.Values, []any{got[0]}) {
		t.Errorf("retry = %v, %v; ERP invoices %v; want the invoice already made", res.Values, err, got)
	}
	// The original connectors are unchanged.
	if w.c == c {
		t.Error("WithoutResend changed the connectors it was called on")
	}
}
