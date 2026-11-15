// Tests for Step 7: the stub ERP's invoice endpoint, the side effect the
// on_complete(approval) hook causes.
//
//	ERP POST /invoices  {"customer_id", "amount"}, header Idempotency-Key
//	                    → {"invoice_id": "INV-…"}
//
// The ERP honours idempotency keys like a real one: the same key with the
// same request returns the invoice it already made; the same key with a
// different request is 409 key_conflict. Without a key, every request makes
// an invoice.
//
// To force timeouts that matter, the stub can commit the invoice and then
// answer late, or not at all, to the first request for each customer. A
// latency applied before the effect (SetLatency) never makes a duplicate:
// the client gives up before anything was created.
package connstub_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/francoisnyc/work-kernel/internal/connstub"
)

// invoice POSTs one invoice request. key "" sends no Idempotency-Key header.
func invoice(t *testing.T, ctx context.Context, url, key, customer string, amount int) (int, map[string]any, error) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"customer_id": customer, "amount": amount})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url+"/invoices", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out, nil
}

func newERP(t *testing.T) *connstub.Server {
	t.Helper()
	erp := connstub.NewERP()
	t.Cleanup(erp.Close)
	erp.SetCustomer("cust-1", connstub.Customer{CreditLimit: 1000})
	erp.SetCustomer("cust-2", connstub.Customer{CreditLimit: 1000})
	return erp
}

// The same key twice returns the same invoice and makes one; another key
// makes another.
func TestERP_Invoice_SameKeySameInvoice(t *testing.T) {
	erp := newERP(t)
	ctx := context.Background()

	code, first, err := invoice(t, ctx, erp.URL, "k-1", "cust-1", 100)
	if err != nil || code != http.StatusOK || first["invoice_id"] == nil || first["invoice_id"] == "" {
		t.Fatalf("first request = %d %v, %v; want 200 with an invoice_id", code, first, err)
	}
	code, again, err := invoice(t, ctx, erp.URL, "k-1", "cust-1", 100)
	if err != nil || code != http.StatusOK || again["invoice_id"] != first["invoice_id"] {
		t.Errorf("same key again = %d %v, %v; want 200 with %v", code, again, err, first["invoice_id"])
	}
	if got := erp.Invoices("cust-1"); len(got) != 1 {
		t.Errorf("invoices for cust-1 = %v, want one", got)
	}

	_, other, _ := invoice(t, ctx, erp.URL, "k-2", "cust-1", 100)
	if other["invoice_id"] == first["invoice_id"] {
		t.Errorf("another key returned the same invoice %v", other["invoice_id"])
	}
	if got := erp.Invoices("cust-1"); len(got) != 2 {
		t.Errorf("invoices for cust-1 after a second key = %v, want two", got)
	}
}

// The same key with a different request is a conflict, and makes nothing.
func TestERP_Invoice_SameKeyDifferentRequest_IsAConflict(t *testing.T) {
	erp := newERP(t)
	ctx := context.Background()
	if code, _, _ := invoice(t, ctx, erp.URL, "k-1", "cust-1", 100); code != http.StatusOK {
		t.Fatalf("first request = %d, want 200", code)
	}
	code, body, err := invoice(t, ctx, erp.URL, "k-1", "cust-1", 999)
	if err != nil || code != http.StatusConflict || body["error"] != "key_conflict" {
		t.Errorf("same key, other amount = %d %v, %v; want 409 key_conflict", code, body, err)
	}
	if got := erp.Invoices("cust-1"); len(got) != 1 {
		t.Errorf("invoices for cust-1 = %v, want one", got)
	}
}

// Without a key, every request makes an invoice: what the control run of
// the duplicate test relies on.
func TestERP_Invoice_NoKey_EveryRequestMakesOne(t *testing.T) {
	erp := newERP(t)
	ctx := context.Background()
	for range 3 {
		if code, _, _ := invoice(t, ctx, erp.URL, "", "cust-1", 100); code != http.StatusOK {
			t.Fatalf("request without a key = %d, want 200", code)
		}
	}
	if got := erp.Invoices("cust-1"); len(got) != 3 {
		t.Errorf("invoices for cust-1 = %v, want three", got)
	}
	if n := erp.InvoiceCount(); n != 3 {
		t.Errorf("InvoiceCount = %d, want 3", n)
	}
}

// Requests with the same key that overlap make one invoice, and every one of
// them is answered with it.
func TestERP_Invoice_ConcurrentSameKey_MakesOne(t *testing.T) {
	erp := newERP(t)
	erp.SetLatency(50 * time.Millisecond) // so the requests overlap
	var (
		wg  sync.WaitGroup
		mu  sync.Mutex
		ids = map[any]int{}
	)
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			code, body, err := invoice(t, context.Background(), erp.URL, "k-same", "cust-1", 100)
			mu.Lock()
			defer mu.Unlock()
			if err != nil || code != http.StatusOK {
				t.Errorf("concurrent request = %d %v, %v; want 200", code, body, err)
				return
			}
			ids[body["invoice_id"]]++
		}()
	}
	wg.Wait()
	if len(ids) != 1 {
		t.Errorf("answers named invoices %v, want one invoice for all", ids)
	}
	if got := erp.Invoices("cust-1"); len(got) != 1 {
		t.Errorf("invoices for cust-1 = %v, want one", got)
	}
}

// Commit, then answer late: the client gives up, but the invoice exists. A
// retry with the same key gets it at once; the stub counts the late answer.
// Only the first request for each customer is late.
func TestERP_Invoice_FirstAnswerLate_TheInvoiceStands(t *testing.T) {
	erp := newERP(t)
	erp.SetFirstAnswerDelay(500 * time.Millisecond)

	short, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, _, err := invoice(t, short, erp.URL, "k-1", "cust-1", 100)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first request: error %v, want the client's deadline", err)
	}
	if got := erp.Invoices("cust-1"); len(got) != 1 {
		t.Fatalf("invoices after the timed-out request = %v, want one: the effect came before the delay", got)
	}

	start := time.Now()
	code, body, err := invoice(t, context.Background(), erp.URL, "k-1", "cust-1", 100)
	if err != nil || code != http.StatusOK || body["invoice_id"] != erp.Invoices("cust-1")[0] {
		t.Errorf("retry = %d %v, %v; want 200 with the invoice already made", code, body, err)
	}
	if took := time.Since(start); took > 300*time.Millisecond {
		t.Errorf("the retry took %v; only the first answer per customer is late", took)
	}
	eventually(t, time.Second, "the late answer is counted", func() bool { return erp.LateAnswers() == 1 })

	// Another customer's first request is late too.
	short2, cancel2 := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel2()
	if _, _, err := invoice(t, short2, erp.URL, "k-2", "cust-2", 100); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("cust-2's first request: error %v, want the client's deadline", err)
	}
}

// Commit, then drop the connection without an answer, for the first request
// for each customer.
func TestERP_Invoice_FirstAnswerDropped_TheInvoiceStands(t *testing.T) {
	erp := newERP(t)
	erp.SetDropFirstAnswer(true)

	if _, _, err := invoice(t, context.Background(), erp.URL, "k-1", "cust-1", 100); err == nil {
		t.Fatal("first request was answered; want the connection dropped")
	}
	if got := erp.Invoices("cust-1"); len(got) != 1 {
		t.Fatalf("invoices after the dropped answer = %v, want one", got)
	}
	code, body, err := invoice(t, context.Background(), erp.URL, "k-1", "cust-1", 100)
	if err != nil || code != http.StatusOK || body["invoice_id"] != erp.Invoices("cust-1")[0] {
		t.Errorf("retry = %d %v, %v; want 200 with the invoice already made", code, body, err)
	}
	if n := erp.DroppedAnswers(); n != 1 {
		t.Errorf("DroppedAnswers = %d, want 1", n)
	}
}

// An unknown customer is 404 and an outage 503, both before any effect.
func TestERP_Invoice_UnknownCustomerAndOutage_MakeNothing(t *testing.T) {
	erp := newERP(t)
	ctx := context.Background()
	if code, _, _ := invoice(t, ctx, erp.URL, "k-1", "nobody", 100); code != http.StatusNotFound {
		t.Errorf("unknown customer = %d, want 404", code)
	}
	erp.SetOutage(true)
	if code, _, _ := invoice(t, ctx, erp.URL, "k-2", "cust-1", 100); code != http.StatusServiceUnavailable {
		t.Errorf("during an outage = %d, want 503", code)
	}
	if n := erp.InvoiceCount(); n != 0 {
		t.Errorf("InvoiceCount = %d, want 0", n)
	}
}

func eventually(t *testing.T, within time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("not within %v: %s", within, what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
