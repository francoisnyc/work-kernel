// Package hooks holds the code that a Type's hooks name: the actions. A
// Type never contains code; it names an action (crm.get_plan) and says which
// fields it reads and writes. Each action has a contract (package
// contracts, the catalogue): a Type whose hook does not fit it is refused
// when it loads (package typedef reads the same catalogue), and every
// connector's answers are held to it here.
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
	"slices"
	"sync"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/contracts"
	"github.com/francoisnyc/work-kernel/internal/hookproto"
	"github.com/francoisnyc/work-kernel/internal/item"
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

// errNotFound is a 404 from a connector: permanent, and an action may turn
// it into an answer (HR does not know someone).
var errNotFound = fmt.Errorf("%w: not found", ErrPermanent)

// ErrKeyConflict is the connector saying that an idempotency key came back
// with a different request. It is permanent too: errors.Is finds both.
var ErrKeyConflict = fmt.Errorf("hooks: idempotency key used with a different request (%w)", ErrPermanent)

// permanent wraps a message as ErrPermanent, so errors.Is finds it.
func permanent(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrPermanent, fmt.Sprintf(format, args...))
}

// action is one declared action: its contract (package contracts, the
// catalogue: <connector>.<operation>@<major>, its kinds, its inputs and
// outputs in the order a hook's reads and writes map to, whether it
// changes the outside world), and, when it is compiled in, its code. A
// remote action has no code here: configuration says where its connector
// is. An action with a side effect gets the hook's idempotency key and must
// pass it to the connector.
type action struct {
	contract        contracts.Contract
	reads, writes   int
	inputs, outputs []string
	kinds           []hookproto.Kind
	sideEffect      bool
	remote          bool // no code here: a connector service, by configuration
	run             func(ctx context.Context, c *Connectors, key string, args []any) (Result, error)
	// runCall, when set, is used instead of run: the action needs to know
	// which hook of which Item runs it (a message names both).
	runCall func(ctx context.Context, c *Connectors, key string, args []any, call Call) (Result, error)
}

// fromContract builds a catalog entry from a contract, with code or none.
func fromContract(c contracts.Contract, code action) action {
	code.contract = c
	code.kinds, code.inputs, code.outputs = c.Kinds, c.Inputs, c.Outputs
	code.reads, code.writes = len(c.Inputs), len(c.Outputs)
	code.sideEffect = c.SideEffects == hookproto.SideEffectsSome
	code.remote = code.run == nil && code.runCall == nil
	return code
}

// Call says which hook of which Item runs an action. email.notify sends the
// hook's ID as the message's template and the Item's ID with it.
//
// Since the hook protocol, it also carries what the envelope says besides
// the inputs (pli.hooks/v1): RunCall builds a hookproto.Request from it.
// Fields left empty are sent empty.
type Call struct {
	HookID string
	ItemID string

	Kind          hookproto.Kind // empty: from the action's declaration
	TypeID        string
	TypeVersion   int
	From, To      string // a check's transition
	EventID       string // <item>:<seq>, the cause
	EventType     string // a hookproto.Event* constant
	CorrelationID string
	Attempt       int
}

// code is the worker's code for the compiled-in actions. Everything else
// about an action is its contract (contracts/): the HR check, since the
// hook protocol a remote connector, has a contract and no code here.
var code = map[string]action{
	"crm.get_plan@1":          {run: getPlan},
	"erp.get_credit_status@1": {run: getCreditStatus},
	"erp.check_credit@1":      {run: checkCredit},
	"erp.create_invoice@1":    {run: createInvoice},
	// email.notify (Step 2, part 3): one message to one principal, through
	// the email stub. It reads the recipient and writes nothing: the
	// hook_ran event records that it ran.
	"email.notify@1": {runCall: notify},
}

// catalog is every action a Type may name, by <connector>.<operation>@<major>:
// each contract of the catalogue, with its code when it is compiled in.
// Tests may add to it (export_test.go); catalogMu guards it.
var (
	catalogMu sync.RWMutex
	catalog   = buildCatalog()
)

func buildCatalog() map[string]action {
	out := map[string]action{}
	for _, c := range contracts.Default().All() {
		out[c.Action()] = fromContract(c, code[c.Action()])
	}
	for name := range code {
		if _, ok := out[name]; !ok {
			panic("hooks: compiled-in action " + name + " has no contract in contracts/")
		}
	}
	return out
}

// lookup returns the declared action an action name names.
func lookup(name string) (actionRef, action, bool) {
	ref, ok := parseAction(name)
	if !ok {
		return actionRef{}, action{}, false
	}
	catalogMu.RLock()
	defer catalogMu.RUnlock()
	a, ok := catalog[ref.key()]
	return ref, a, ok
}

// Arity reports how many reads and writes an action takes. ok is false for
// an action that does not exist, at the major named (none means 1).
func Arity(name string) (reads, writes int, ok bool) {
	_, a, ok := lookup(name)
	return a.reads, a.writes, ok
}

// Connectors runs a Type's actions: the compiled-in ones (the CRM, the ERP
// and the email service, each at a base URL in the spike) and the remote
// connectors configuration names (WithConnection, WithConfigFile).
//
// The With methods return a changed copy; the copies share the descriptor
// cache and the connectors file.
type Connectors struct {
	crm, erp string
	email    string // the email service; set with WithEmail
	http     *http.Client
	noKeys   bool                  // the control run: never send the idempotency key
	conns    map[string]Connection // connector@major -> remote connection (WithConnection)
	file     *configFile           // the connectors file (WithConfigFile), or nil
	shared   *shared
}

// NewConnectors returns connectors for the CRM and ERP at these base URLs.
func NewConnectors(crmURL, erpURL string) *Connectors {
	// No client timeout: every call follows its context's deadline, which
	// the activity sets.
	return &Connectors{crm: crmURL, erp: erpURL, http: newClient(nil), shared: newShared()}
}

// newClient never follows a redirect: a connector answers, or fails.
func newClient(transport http.RoundTripper) *http.Client {
	return &http.Client{Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// WithEmail returns connectors that also send messages through the email
// service at this base URL.
func (c *Connectors) WithEmail(emailURL string) *Connectors {
	withEmail := *c
	withEmail.email = emailURL
	return &withEmail
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
	fresh.http = newClient(&http.Transport{DisableKeepAlives: true})
	return &fresh
}

// Run calls one action with the values of its hook's reads. key is the
// hook's idempotency key: an action with a side effect refuses to run
// without one, since a retry could not be told from a new request.
// Read-only actions ignore it.
func (c *Connectors) Run(ctx context.Context, name, key string, args []any) (Result, error) {
	return c.RunCall(ctx, name, key, args, Call{})
}

// RunCall is Run for a given hook of a given Item. It builds the hook
// protocol's envelope (the args by the action's input names), gives it to
// the connector the action's connector@major resolves to (a configured
// remote one, else the compiled-in one), and reads the answer back as a
// Result (the patch by the action's output names, in order).
//
// A read-only action called without a key (tests, the bench) gets its
// request ID as the key: nothing to replay. The worker always passes the
// hook's real key.
func (c *Connectors) RunCall(ctx context.Context, name, key string, args []any, call Call) (Result, error) {
	ref, a, ok := lookup(name)
	if !ok {
		return Result{}, permanent("unknown action %q", name)
	}
	if len(args) != a.reads {
		return Result{}, permanent("%s takes %d inputs, got %d", name, a.reads, len(args))
	}
	if a.sideEffect && key == "" {
		return Result{}, permanent("%s has a side effect and needs an idempotency key", name)
	}
	conn, connRef, err := c.resolve(ref.connectorAtMajor())
	if err != nil {
		return Result{}, err
	}
	req := c.envelope(ref, a, key, args, call, connRef)
	if deadline, ok := ctx.Deadline(); ok {
		req.Deadline = deadline
	}
	res, err := conn.Handle(ctx, req)
	if err != nil {
		return Result{}, err
	}
	return result(name, a, res)
}

// envelope builds the request for one call.
func (c *Connectors) envelope(ref actionRef, a action, key string, args []any, call Call, connRef string) hookproto.Request {
	requestID := uuid.NewString()
	if key == "" {
		key = requestID
	}
	kind := call.Kind
	if kind == "" {
		kind = a.kinds[0]
	}
	attempt := call.Attempt
	if attempt == 0 {
		attempt = 1
	}
	inputs := make(map[string]any, len(a.inputs))
	for i, name := range a.inputs {
		inputs[name] = args[i]
	}
	req := hookproto.Request{
		Protocol: hookproto.Protocol, Kind: kind, RequestID: requestID, IdempotencyKey: key, Attempt: attempt,
		Hook: hookproto.HookRef{Binding: fmt.Sprintf("%s@%d#%s", call.TypeID, call.TypeVersion, call.HookID),
			Connector: ref.connectorAtMajor(), Operation: ref.operation},
		Event:      hookproto.Event{ID: call.EventID, Type: call.EventType},
		Item:       hookproto.ItemRef{ID: call.ItemID, Type: call.TypeID, TypeVersion: call.TypeVersion},
		Inputs:     inputs,
		Trace:      hookproto.Trace{CorrelationID: call.CorrelationID, CausationID: call.EventID},
		Connection: hookproto.ConnectionRef{Slot: ref.connector, Ref: connRef},
	}
	if call.ItemID != "" {
		req.Event.Subject = "item/" + call.ItemID
	}
	if call.From != "" || call.To != "" {
		req.Transition = &hookproto.Transition{From: call.From, To: call.To}
	}
	return req
}

// result reads an answer back as a Result: the outcome as a verdict, the
// patch as values in the order of the action's outputs (the hook's
// writes). An answer missing an output is permanent: it cannot be written.
// The contract holds every connector, compiled in or remote (Ventoux V1):
// an output of another type, or a deny reason it does not declare, is
// ErrConnectorMismatch.
func result(name string, a action, res hookproto.Response) (Result, error) {
	values := func() ([]any, error) {
		if len(a.outputs) == 0 {
			return nil, nil
		}
		types := a.contract.OutputValues()
		out := make([]any, len(a.outputs))
		for i, o := range a.outputs {
			v, ok := res.Patch[o]
			if !ok {
				return nil, permanent("%s answered %s without the output %s", name, res.Outcome, o)
			}
			if i < len(types) {
				if err := types[i].Check(v); err != nil {
					return nil, fmt.Errorf("%w: %s answered %v, against its contract", ErrConnectorMismatch, name, err)
				}
			}
			out[i] = v
		}
		return out, nil
	}
	switch res.Outcome {
	case hookproto.OutcomeAllow:
		return Result{Verdict: Allow}, nil
	case hookproto.OutcomeDeny:
		code := ""
		if res.Reason != nil {
			code = res.Reason.Code
		}
		if !slices.Contains(a.contract.Errors, code) {
			return Result{}, fmt.Errorf("%w: %s denied with %q, which its contract does not declare", ErrConnectorMismatch, name, code)
		}
		return Result{Verdict: Deny, Reason: code}, nil
	case hookproto.OutcomeCorrect:
		v, err := values()
		return Result{Verdict: Correct, Values: v}, err
	case hookproto.OutcomeOK:
		v, err := values()
		return Result{Values: v}, err
	}
	return Result{}, permanent("%s answered outcome %q, which v1 does not take", name, res.Outcome)
}

// notify sends one message: the recipient (a principal), the template (the
// hook's ID), the Item and the key. A retry sends the same key, and the
// service stores it once. Only people and agents get mail: a recipient
// that is not a user or agent principal is permanent.
func notify(ctx context.Context, c *Connectors, key string, args []any, call Call) (Result, error) {
	if c.email == "" {
		return Result{}, permanent("email.notify: no email service is configured")
	}
	to, _ := args[0].(string)
	if kind, ok := item.KindOf(to); !ok || (kind != item.KindUser && kind != item.KindAgent) {
		return Result{}, permanent("email.notify: the recipient must be a user or agent principal, got %v", args[0])
	}
	if call.HookID == "" || call.ItemID == "" {
		return Result{}, permanent("email.notify: no hook or Item to send for")
	}
	req := map[string]any{"to": to, "template": call.HookID, "item": call.ItemID, "key": key}
	var body struct {
		ID string `json:"id"`
	}
	if err := c.call(ctx, key, http.MethodPost, c.email+"/messages", req, &body); err != nil {
		return Result{}, err
	}
	return Result{}, nil
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
	case resp.StatusCode == http.StatusNotFound:
		return fmt.Errorf("%w: %s %s", errNotFound, method, req.URL.Path)
	case resp.StatusCode == http.StatusBadRequest:
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
