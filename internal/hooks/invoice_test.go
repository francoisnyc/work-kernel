// Tests for Step 7: erp.create_invoice, the first action with a side effect.
//
// An action with a side effect must pass an idempotency key to the
// connector itself, so that a retry after a timeout finds the invoice it
// already made instead of making another. Run takes the key; read-only
// actions ignore it. The control run of the duplicate test uses connectors
// that do not send keys (WithoutKeys), to prove the test can see duplicates.
package hooks_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/hooks"
)

// The action reads the customer and the amount, and writes the invoice ID.
func TestCreateInvoice_Arity(t *testing.T) {
	reads, writes, ok := hooks.Arity("erp.create_invoice")
	if !ok || reads != 2 || writes != 1 {
		t.Errorf("Arity(erp.create_invoice) = %d, %d, %v; want 2, 1, true", reads, writes, ok)
	}
}

// The same key twice is one invoice, and both runs return its ID.
func TestCreateInvoice_SameKey_OneInvoice(t *testing.T) {
	w := newWorld(t)
	first, err := w.c.Run(ctx(t), "erp.create_invoice", "hook:item-1:5:create_invoice", []any{"cust-1", float64(100)})
	if err != nil || len(first.Values) != 1 || first.Values[0] == "" {
		t.Fatalf("create_invoice = %+v, %v; want one invoice ID", first, err)
	}
	again, err := w.c.Run(ctx(t), "erp.create_invoice", "hook:item-1:5:create_invoice", []any{"cust-1", float64(100)})
	if err != nil || !reflect.DeepEqual(again.Values, first.Values) {
		t.Errorf("create_invoice again = %+v, %v; want %v", again, err, first.Values)
	}
	if got := w.erp.Invoices("cust-1"); len(got) != 1 || got[0] != first.Values[0] {
		t.Errorf("ERP invoices = %v, want just %v", got, first.Values[0])
	}

	// Another entry (another key) is another invoice.
	other, err := w.c.Run(ctx(t), "erp.create_invoice", "hook:item-1:9:create_invoice", []any{"cust-1", float64(100)})
	if err != nil || reflect.DeepEqual(other.Values, first.Values) {
		t.Errorf("create_invoice with another key = %+v, %v; want a new invoice", other, err)
	}
}

// An action with a side effect refuses to run without a key: a retry could
// not be told from a new request.
func TestCreateInvoice_WithoutAKey_IsPermanent(t *testing.T) {
	w := newWorld(t)
	_, err := w.c.Run(ctx(t), "erp.create_invoice", "", []any{"cust-1", float64(100)})
	if !errors.Is(err, hooks.ErrPermanent) {
		t.Errorf("create_invoice without a key: error = %v, want ErrPermanent", err)
	}
	if n := w.erp.InvoiceCount(); n != 0 {
		t.Errorf("InvoiceCount = %d, want 0: nothing may be sent", n)
	}
}

// The ERP answers 409 when a key comes back with a different request (here,
// another amount). That is a conflict, not something to retry: it is
// ErrKeyConflict, which is also permanent.
func TestCreateInvoice_KeyConflict(t *testing.T) {
	w := newWorld(t)
	key := "hook:item-1:5:create_invoice"
	if _, err := w.c.Run(ctx(t), "erp.create_invoice", key, []any{"cust-1", float64(100)}); err != nil {
		t.Fatalf("create_invoice: %v", err)
	}
	_, err := w.c.Run(ctx(t), "erp.create_invoice", key, []any{"cust-1", float64(250)})
	if !errors.Is(err, hooks.ErrKeyConflict) || !errors.Is(err, hooks.ErrPermanent) {
		t.Errorf("same key, other amount: error = %v, want ErrKeyConflict (and ErrPermanent)", err)
	}
}

// Read-only actions take no key; an empty one is fine.
func TestReadOnlyActions_NeedNoKey(t *testing.T) {
	w := newWorld(t)
	if _, err := w.c.Run(ctx(t), "crm.get_plan", "", []any{"cust-1"}); err != nil {
		t.Errorf("crm.get_plan without a key: %v", err)
	}
}

// The control: connectors that never send the key. The same key twice makes
// two invoices, which is what the duplicate test must be able to see.
func TestWithoutKeys_MakesDuplicates(t *testing.T) {
	w := newWorld(t)
	control := w.c.WithoutKeys()
	key := "hook:item-1:5:create_invoice"
	for range 2 {
		if _, err := control.Run(ctx(t), "erp.create_invoice", key, []any{"cust-1", float64(100)}); err != nil {
			t.Fatalf("create_invoice without keys: %v", err)
		}
	}
	if got := w.erp.Invoices("cust-1"); len(got) != 2 {
		t.Errorf("ERP invoices = %v, want two", got)
	}
	// The original connectors still send keys.
	for range 2 {
		if _, err := w.c.Run(ctx(t), "erp.create_invoice", "k-other", []any{"cust-1", float64(100)}); err != nil {
			t.Fatalf("create_invoice: %v", err)
		}
	}
	if got := w.erp.Invoices("cust-1"); len(got) != 3 {
		t.Errorf("ERP invoices = %v, want three", got)
	}
}
