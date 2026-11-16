// Package hooks holds the code that a Type's hooks name: the actions. A
// Type never contains code; it names an action (crm.get_plan) and says which
// fields it reads and writes. Each action registers its arity, and a Type
// whose hook disagrees is refused when it loads (package typedef asks Arity).
//
// An action knows positions, not field IDs: it gets the values of the hook's
// `reads`, in order, and returns values for its `writes`, in order. So the
// same action can serve any Type that maps fields to it.
//
// This package only talks to the outside world. Reading the Item, writing
// back through the item service and retrying are the job of the workflow's
// activities.
package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
)

// Verdict is a check's answer.
type Verdict string

const (
	Allow   Verdict = "allow"
	Deny    Verdict = "deny"
	Correct Verdict = "correct" // allow, with values for the hook's writes
)

// Result is what an action returns. An enrich action fills Values; a check
// fills Verdict, and Reason (a code) for a denial or Values for a correction.
type Result struct {
	Verdict Verdict
	Reason  string
	Values  []any
}

// ErrPermanent marks a failure that retrying cannot fix: an unknown action,
// bad inputs, an unknown customer. Every other error is worth retrying.
var ErrPermanent = errors.New("hooks: permanent failure")

// ErrKeyConflict is the connector saying that an idempotency key came back
// with a different request. It is permanent too: errors.Is finds both.
var ErrKeyConflict = fmt.Errorf("hooks: idempotency key used with a different request (%w)", ErrPermanent)

// permanent wraps a message as ErrPermanent, so errors.Is finds it.
func permanent(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrPermanent, fmt.Sprintf(format, args...))
}

// action is one registered action: how many values it reads and writes,
// whether it changes the outside world, and its code. An action with a side
// effect gets the hook's idempotency key and must pass it to the connector.
type action struct {
	reads, writes int
	sideEffect    bool
	run           func(ctx context.Context, c *Connectors, key string, args []any) (Result, error)
}

// catalog is every action a Type may name. A package-level map is fine here:
// it is filled once, at startup, and only read afterwards.
var catalog = map[string]action{
	"crm.get_plan":          {reads: 1, writes: 1, run: getPlan},
	"erp.get_credit_status": {reads: 1, writes: 1, run: getCreditStatus},
	"erp.check_credit":      {reads: 2, writes: 1, run: checkCredit},
	"erp.create_invoice":    {reads: 2, writes: 1, sideEffect: true, run: createInvoice},
	"hr.manager_of":         {reads: 2, writes: 1, run: managerOf},
}

// Arity reports how many reads and writes an action takes. ok is false for
// an action that does not exist.
func Arity(name string) (reads, writes int, ok bool) {
	a, ok := catalog[name]
	return a.reads, a.writes, ok
}

// Connectors calls the CRM and the ERP. In the product each would be a
// Connection with its own credential; in the spike, a base URL.
type Connectors struct {
	crm, erp string
	hr       string // the HR directory; set with WithHR
	http     *http.Client
	noKeys   bool // the control run: never send the idempotency key
}

// NewConnectors returns connectors for the CRM and ERP at these base URLs.
func NewConnectors(crmURL, erpURL string) *Connectors {
	// No client timeout: every call follows its context's deadline, which
	// the activity sets.
	return &Connectors{crm: crmURL, erp: erpURL, http: &http.Client{}}
}

// WithHR returns connectors that also call the HR directory at this base URL.
func (c *Connectors) WithHR(hrURL string) *Connectors {
	withHR := *c
	withHR.hr = hrURL
	return &withHR
}

// WithoutKeys returns connectors that never send an idempotency key. It is
// the control run of the duplicate test: with it, a retry after a timeout
// makes a second invoice, which the test must be able to see.
func (c *Connectors) WithoutKeys() *Connectors {
	control := *c // a copy: c itself still sends keys
	control.noKeys = true
	return &control
}

// WithoutResend returns connectors that open a new connection for every
// request. net/http resends a replayable request (a POST with an
// Idempotency-Key counts) by itself when a reused connection drops before
// the answer; without reuse it never does, so a dropped answer reaches the
// caller and the retry is a new Temporal attempt. Bench and tests only.
func (c *Connectors) WithoutResend() *Connectors {
	fresh := *c
	fresh.http = &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	return &fresh
}

// Run calls one action with the values of its hook's reads. key is the
// hook's idempotency key: an action with a side effect refuses to run
// without one, since a retry could not be told from a new request.
// Read-only actions ignore it.
func (c *Connectors) Run(ctx context.Context, name, key string, args []any) (Result, error) {
	a, ok := catalog[name]
	if !ok {
		return Result{}, permanent("unknown action %q", name)
	}
	if len(args) != a.reads {
		return Result{}, permanent("%s takes %d inputs, got %d", name, a.reads, len(args))
	}
	if a.sideEffect && key == "" {
		return Result{}, permanent("%s has a side effect and needs an idempotency key", name)
	}
	if c.noKeys {
		key = ""
	}
	return a.run(ctx, c, key, args)
}

// --- the actions ---

func getPlan(ctx context.Context, c *Connectors, _ string, args []any) (Result, error) {
	customer, err := customerID(args[0])
	if err != nil {
		return Result{}, err
	}
	var body struct {
		Plan string `json:"plan"`
	}
	if err := c.call(ctx, "", http.MethodGet, c.crm+"/customers/"+url.PathEscape(customer)+"/plan", nil, &body); err != nil {
		return Result{}, err
	}
	return Result{Values: []any{body.Plan}}, nil
}

func getCreditStatus(ctx context.Context, c *Connectors, _ string, args []any) (Result, error) {
	customer, err := customerID(args[0])
	if err != nil {
		return Result{}, err
	}
	var body struct {
		OnHold bool `json:"on_hold"`
	}
	if err := c.call(ctx, "", http.MethodGet, c.erp+"/customers/"+url.PathEscape(customer)+"/credit", nil, &body); err != nil {
		return Result{}, err
	}
	return Result{Values: []any{body.OnHold}}, nil
}

func checkCredit(ctx context.Context, c *Connectors, _ string, args []any) (Result, error) {
	customer, err := customerID(args[0])
	if err != nil {
		return Result{}, err
	}
	amount, ok := asInt(args[1])
	if !ok {
		return Result{}, permanent("erp.check_credit: the amount must be a whole number")
	}
	req := map[string]any{"customer_id": customer, "amount": amount}
	var body struct {
		Verdict    Verdict `json:"verdict"`
		Reason     string  `json:"reason"`
		CostCenter string  `json:"cost_center"`
	}
	if err := c.call(ctx, "", http.MethodPost, c.erp+"/credit-checks", req, &body); err != nil {
		return Result{}, err
	}
	switch body.Verdict {
	case Allow:
		return Result{Verdict: Allow}, nil
	case Deny:
		return Result{Verdict: Deny, Reason: body.Reason}, nil
	case Correct:
		return Result{Verdict: Correct, Values: []any{body.CostCenter}}, nil
	}
	return Result{}, permanent("erp.check_credit: unknown verdict %q", body.Verdict)
}

// createInvoice makes an invoice in the ERP for the customer and amount, and
// returns its ID. The key goes in the Idempotency-Key header: a retry gets
// the invoice already made, never a second one.
func createInvoice(ctx context.Context, c *Connectors, key string, args []any) (Result, error) {
	customer, err := customerID(args[0])
	if err != nil {
		return Result{}, err
	}
	amount, ok := asInt(args[1])
	if !ok {
		return Result{}, permanent("erp.create_invoice: the amount must be a whole number")
	}
	var body struct {
		InvoiceID string `json:"invoice_id"`
	}
	req := map[string]any{"customer_id": customer, "amount": amount}
	if err := c.call(ctx, key, http.MethodPost, c.erp+"/invoices", req, &body); err != nil {
		return Result{}, err
	}
	if body.InvoiceID == "" {
		return Result{}, errors.New("erp.create_invoice: the answer names no invoice")
	}
	return Result{Values: []any{body.InvoiceID}}, nil
}

// managerOf is the HR check: given the requester (created_on_behalf_of,
// else created_by) it answers correct with their manager as the approver,
// or deny. Contract stub (Step 2, part 1, Phase B).
func managerOf(context.Context, *Connectors, string, []any) (Result, error) {
	return Result{}, errors.New("hr.manager_of: not implemented")
}

// --- plumbing ---

// call sends one request and decodes the JSON answer. 400 and 404 are
// permanent, 409 is a key conflict; any other failure (network, timeout,
// 5xx) may be retried. A non-empty key is sent as the Idempotency-Key header.
func (c *Connectors) call(ctx context.Context, key, method, target string, in, out any) error {
	var body bytes.Buffer
	if in != nil {
		if err := json.NewEncoder(&body).Encode(in); err != nil {
			return permanent("encode request: %v", err)
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, target, &body)
	if err != nil {
		return permanent("build request: %v", err)
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, req.URL.Path, err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusConflict:
		return fmt.Errorf("%w: %s %s", ErrKeyConflict, method, req.URL.Path)
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusBadRequest:
		return permanent("%s %s: %s", method, req.URL.Path, resp.Status)
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("%s %s: %s", method, req.URL.Path, resp.Status)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("%s %s: decode answer: %w", method, req.URL.Path, err)
	}
	return nil
}

func customerID(v any) (string, error) {
	s, ok := v.(string)
	if !ok || s == "" {
		return "", permanent("the customer ID must be a non-empty string")
	}
	return s, nil
}

// asInt accepts the forms an int field arrives in: a Go int, or a whole
// float64 (numbers read back from JSON).
func asInt(v any) (int64, bool) {
	switch x := v.(type) {
	case int:
		return int64(x), true
	case int64:
		return x, true
	case float64:
		if x == math.Trunc(x) && math.Abs(x) < 1<<53 {
			return int64(x), true
		}
	}
	return 0, false
}
