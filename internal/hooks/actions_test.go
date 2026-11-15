// Tests for Step 6, part 2: the hook actions, the code a Type's hooks name
// (crm.get_plan, erp.get_credit_status, erp.check_credit), run against the
// stub CRM and ERP.
//
// An action knows positions, not field IDs: it gets the values of the hook's
// `reads`, in order, and returns values for its `writes`, in order. The Type
// says which fields those are; the code never names one.
package hooks_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/francoisnyc/work-kernel/internal/connstub"
	"github.com/francoisnyc/work-kernel/internal/hooks"
)

type world struct {
	crm, erp *connstub.Server
	c        *hooks.Connectors
}

func newWorld(t *testing.T) world {
	t.Helper()
	crm, erp := connstub.NewCRM(), connstub.NewERP()
	t.Cleanup(crm.Close)
	t.Cleanup(erp.Close)
	crm.SetCustomer("cust-1", connstub.Customer{Plan: "enterprise"})
	erp.SetCustomer("cust-1", connstub.Customer{CreditLimit: 1000})
	return world{crm: crm, erp: erp, c: hooks.NewConnectors(crm.URL, erp.URL)}
}

func ctx(t *testing.T) context.Context {
	t.Helper()
	c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return c
}

func TestGetPlan(t *testing.T) {
	w := newWorld(t)
	res, err := w.c.Run(ctx(t), "crm.get_plan", "", []any{"cust-1"})
	if err != nil || !reflect.DeepEqual(res.Values, []any{"enterprise"}) {
		t.Errorf("crm.get_plan = %+v, %v; want [enterprise]", res, err)
	}
}

func TestGetCreditStatus(t *testing.T) {
	w := newWorld(t)
	res, err := w.c.Run(ctx(t), "erp.get_credit_status", "", []any{"cust-1"})
	if err != nil || !reflect.DeepEqual(res.Values, []any{false}) {
		t.Errorf("erp.get_credit_status = %+v, %v; want [false]", res, err)
	}
	w.erp.SetCustomer("cust-1", connstub.Customer{OnHold: true, CreditLimit: 1000})
	res, err = w.c.Run(ctx(t), "erp.get_credit_status", "", []any{"cust-1"})
	if err != nil || !reflect.DeepEqual(res.Values, []any{true}) {
		t.Errorf("erp.get_credit_status after the flip = %+v, %v; want [true]", res, err)
	}
}

// erp.check_credit answers allow, deny with a reason code, or correct with
// values for the hook's writes (the cost center the ERP keeps).
func TestCheckCredit(t *testing.T) {
	w := newWorld(t)
	cases := []struct {
		name     string
		customer connstub.Customer
		amount   any
		want     hooks.Result
	}{
		{"allow", connstub.Customer{CreditLimit: 1000}, float64(100),
			hooks.Result{Verdict: hooks.Allow}},
		{"deny: on hold", connstub.Customer{OnHold: true, CreditLimit: 1000}, float64(100),
			hooks.Result{Verdict: hooks.Deny, Reason: "customer_on_hold"}},
		{"deny: over the limit", connstub.Customer{CreditLimit: 1000}, float64(5000),
			hooks.Result{Verdict: hooks.Deny, Reason: "over_credit_limit"}},
		{"correct: the ERP's cost center", connstub.Customer{CreditLimit: 1000, CostCenter: "CC-9"}, float64(100),
			hooks.Result{Verdict: hooks.Correct, Values: []any{"CC-9"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w.erp.SetCustomer("cust-1", c.customer)
			got, err := w.c.Run(ctx(t), "erp.check_credit", "", []any{"cust-1", c.amount})
			if err != nil || !reflect.DeepEqual(got, c.want) {
				t.Errorf("erp.check_credit = %+v, %v; want %+v", got, err, c.want)
			}
		})
	}
}

// Failures come in two kinds. Permanent: retrying cannot help (an unknown
// action, bad inputs, an unknown customer); the hook gives up at once.
// Anything else (an outage, a 500, a timeout) is retried within the budget.
func TestFailures_PermanentOrRetryable(t *testing.T) {
	w := newWorld(t)
	for name, run := range map[string]func() error{
		"unknown action": func() error {
			_, err := w.c.Run(ctx(t), "crm.delete_everything", "", []any{"cust-1"})
			return err
		},
		"missing input": func() error {
			_, err := w.c.Run(ctx(t), "crm.get_plan", "", []any{nil})
			return err
		},
		"wrong number of inputs": func() error {
			_, err := w.c.Run(ctx(t), "erp.check_credit", "", []any{"cust-1"})
			return err
		},
		"unknown customer": func() error {
			_, err := w.c.Run(ctx(t), "crm.get_plan", "", []any{"nobody"})
			return err
		},
	} {
		if err := run(); !errors.Is(err, hooks.ErrPermanent) {
			t.Errorf("%s: error = %v, want ErrPermanent", name, err)
		}
	}

	w.crm.SetOutage(true)
	_, err := w.c.Run(ctx(t), "crm.get_plan", "", []any{"cust-1"})
	if err == nil || errors.Is(err, hooks.ErrPermanent) {
		t.Errorf("during an outage: error = %v, want a retryable error", err)
	}
	w.crm.SetOutage(false)

	w.erp.SetLatency(2 * time.Second)
	short, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = w.c.Run(short, "erp.get_credit_status", "", []any{"cust-1"})
	if err == nil || errors.Is(err, hooks.ErrPermanent) {
		t.Errorf("past the deadline: error = %v, want a retryable error", err)
	}
	if took := time.Since(start); took > time.Second {
		t.Errorf("gave up after %v; the call must follow its context's deadline", took)
	}
}
