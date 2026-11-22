// Package conformance is the hook protocol's conformance test: any
// connector, compiled in Go or not, runs it against itself over HTTP
// (pli.hooks/v1). It is test code that lives outside a _test file so other
// packages can import it.
//
// It checks, black-box:
//
//   - the descriptor at /.well-known/pli-connector: its shape, and that the
//     cases fit it;
//   - each case's answer: the envelope, the echoed request ID, an outcome
//     valid for the kind, a patch only of declared outputs, a deny reason
//     the descriptor declares, the version the descriptor reports;
//   - idempotency: a replayed request gets the identical answer; a resend
//     (new request ID, same key) gets the same answer; the same key with
//     other inputs is 422; a failure is never stored, so after one the same
//     key with other inputs is answered (this is what makes a rerun safe);
//   - signing: unsigned, tampered, wrongly signed, stale or future requests,
//     and requests signed only in schemes it does not know, are 401;
//     entries it cannot read are ignored;
//   - the protocol: another protocol version, a malformed body, a kind the
//     operation does not take are 400; an unknown operation is 404; unknown
//     envelope fields are ignored; every refusal is a problem+json body.
//
// dryRun and async completion are not checked: v1 does not build them.
package conformance

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/francoisnyc/work-kernel/internal/hookproto"
)

// Case is one request a connector answers, and what it should answer.
type Case struct {
	Name      string
	Kind      hookproto.Kind
	Operation string
	Inputs    map[string]any
	// Want is the answer: its Outcome, Patch and Reason.Code are compared.
	// Nil when WantStatus is set.
	Want *hookproto.Response
	// WantStatus is a refusal's HTTP status (400 for inputs the operation
	// cannot take); zero means 200 and Want.
	WantStatus int
}

// Target is the connector under test.
type Target struct {
	URL    string // base URL
	Secret string // the connection's whsec_ secret
	// Cases must include two answered (200) cases of one operation with
	// different inputs: the idempotency checks use them.
	Cases []Case
	// Outage makes the connector fail transiently (5xx) while on. Every
	// connector needs a way to do this in its tests; the check that
	// failures are not stored depends on it.
	Outage func(on bool)
}

// Run runs the checks and reports each problem as a test error.
func Run(t *testing.T, target Target) {
	t.Helper()
	for _, p := range Check(target) {
		t.Error(p)
	}
}

// Check runs the checks and returns the problems found, none for a
// conformant connector.
func Check(target Target) []string {
	c := &checker{target: target, client: &http.Client{
		Timeout: 10 * time.Second,
		// A redirect is never followed: a connector answers or fails.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
	c.run()
	return c.problems
}

type checker struct {
	target   Target
	client   *http.Client
	secret   hookproto.Secret
	desc     hookproto.Descriptor
	problems []string
}

func (c *checker) fail(format string, args ...any) {
	c.problems = append(c.problems, fmt.Sprintf(format, args...))
}

func (c *checker) run() {
	s, err := hookproto.ParseSecret(c.target.Secret)
	if err != nil {
		c.fail("Target.Secret: %v", err)
		return
	}
	c.secret = s
	if !c.descriptor() {
		return
	}
	answered := c.cases()
	a, b, ok := pair(c.target.Cases, answered)
	if !ok {
		c.fail("the cases need two answered (200) cases of one operation with different inputs")
		return
	}
	c.idempotency(a, b)
	c.failuresNotStored(a, b)
	c.signatures(a)
	c.protocol(a)
}

// --- the descriptor ---

func (c *checker) descriptor() bool {
	res, err := c.client.Get(c.target.URL + hookproto.DescriptorPath)
	if err != nil {
		c.fail("GET %s: %v", hookproto.DescriptorPath, err)
		return false
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		c.fail("GET %s = %d, want 200", hookproto.DescriptorPath, res.StatusCode)
		return false
	}
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, hookproto.ContentType) {
		c.fail("descriptor Content-Type %q, want %s", ct, hookproto.ContentType)
	}
	if err := json.Unmarshal(body, &c.desc); err != nil {
		c.fail("descriptor is not JSON: %v", err)
		return false
	}
	d := c.desc
	if d.Connector == "" || strings.ContainsAny(d.Connector, "@.#/ ") {
		c.fail("descriptor connector %q: want a plain name (no @ . # / or space)", d.Connector)
	}
	if _, err := hookproto.Major(d.Version); err != nil {
		c.fail("descriptor version %q is not semver: %v", d.Version, err)
	}
	if !slices.Contains(d.SupportedProtocolVersions, hookproto.Protocol) {
		c.fail("descriptor supportedProtocolVersions %v does not list %s", d.SupportedProtocolVersions, hookproto.Protocol)
	}
	if len(d.Operations) == 0 {
		c.fail("descriptor declares no operation")
	}
	seen := map[string]bool{}
	for _, op := range d.Operations {
		if op.Name == "" || seen[op.Name] {
			c.fail("descriptor operation %q: empty or duplicate name", op.Name)
		}
		seen[op.Name] = true
		if len(op.Kinds) == 0 {
			c.fail("operation %s declares no kind", op.Name)
		}
		for _, k := range op.Kinds {
			if k != hookproto.KindCheck && k != hookproto.KindEnrich && k != hookproto.KindAction {
				c.fail("operation %s: unknown kind %q", op.Name, k)
			}
		}
		if op.InputSchema.Type != "object" {
			c.fail("operation %s: inputSchema type %q, want object", op.Name, op.InputSchema.Type)
		}
		if op.OutputSchema != nil && op.OutputSchema.Type != "object" {
			c.fail("operation %s: outputSchema type %q, want object", op.Name, op.OutputSchema.Type)
		}
		for _, r := range op.InputSchema.Required {
			if _, ok := op.InputSchema.Properties[r]; !ok {
				c.fail("operation %s: required input %q is not a property", op.Name, r)
			}
		}
		if op.SideEffects != "none" && op.SideEffects != "some" {
			c.fail("operation %s: sideEffects %q, want none or some", op.Name, op.SideEffects)
		}
		if op.Async != "forbidden" && op.Async != "optional" && op.Async != "required" {
			c.fail("operation %s: async %q, want forbidden, optional or required", op.Name, op.Async)
		}
		if slices.Contains(op.Kinds, hookproto.KindCheck) && op.SideEffects != "none" {
			c.fail("operation %s is a check with side effects: a check never changes anything", op.Name)
		}
	}
	for _, cs := range c.target.Cases {
		op, ok := c.desc.Operation(cs.Operation)
		if !ok {
			c.fail("case %s: operation %q is not in the descriptor", cs.Name, cs.Operation)
			continue
		}
		if !slices.Contains(op.Kinds, cs.Kind) {
			c.fail("case %s: operation %s does not take kind %s", cs.Name, cs.Operation, cs.Kind)
		}
		for name := range cs.Inputs {
			if _, ok := op.InputSchema.Properties[name]; !ok && cs.WantStatus == 0 {
				c.fail("case %s: input %q is not in %s's inputSchema", cs.Name, name, cs.Operation)
			}
		}
	}
	return true
}

// --- the cases ---

// cases sends each case under its own key and checks the answer. It
// returns the answers of the cases answered 200, by case index.
func (c *checker) cases() map[int]hookproto.Response {
	answered := map[int]hookproto.Response{}
	for i, cs := range c.target.Cases {
		req := c.request(cs, newID("case"))
		status, res, prob := c.send(req, nil)
		want := cs.WantStatus
		if want == 0 {
			want = http.StatusOK
		}
		if status != want {
			c.fail("case %s: status %d, want %d (problem %+v)", cs.Name, status, want, prob)
			continue
		}
		if status != http.StatusOK {
			continue // send checked the problem body
		}
		if c.answer(cs.Name, cs, req, res) {
			answered[i] = res
		}
	}
	return answered
}

// answer checks one 200 answer against the protocol, the descriptor and
// the case. It reports whether it was right.
func (c *checker) answer(label string, cs Case, req hookproto.Request, res hookproto.Response) bool {
	n := len(c.problems)
	if res.Protocol != hookproto.Protocol {
		c.fail("%s: answer protocol %q, want %s", label, res.Protocol, hookproto.Protocol)
	}
	if res.RequestID != req.RequestID {
		c.fail("%s: answer requestId %q, want the request's %q", label, res.RequestID, req.RequestID)
	}
	valid := map[hookproto.Kind][]hookproto.Outcome{
		hookproto.KindCheck:  {hookproto.OutcomeAllow, hookproto.OutcomeDeny, hookproto.OutcomeCorrect},
		hookproto.KindEnrich: {hookproto.OutcomeOK},
		hookproto.KindAction: {hookproto.OutcomeOK},
	}
	if !slices.Contains(valid[cs.Kind], res.Outcome) {
		c.fail("%s: outcome %q is not one a %s answers in v1 (%v)", label, res.Outcome, cs.Kind, valid[cs.Kind])
	}
	op, _ := c.desc.Operation(cs.Operation)
	for name := range res.Patch {
		if op.OutputSchema == nil {
			c.fail("%s: patch %v from an operation that declares no outputs", label, res.Patch)
			break
		}
		if _, ok := op.OutputSchema.Properties[name]; !ok {
			c.fail("%s: patch names %q, not in %s's outputSchema", label, name, cs.Operation)
		}
	}
	if res.Outcome == hookproto.OutcomeDeny {
		if res.Reason == nil || res.Reason.Code == "" {
			c.fail("%s: a deny without a reason code", label)
		} else if !slices.Contains(op.Errors, res.Reason.Code) {
			c.fail("%s: deny reason %q is not in %s's declared errors %v", label, res.Reason.Code, cs.Operation, op.Errors)
		}
	}
	if res.ConnectorVersion != c.desc.Version {
		c.fail("%s: answer connectorVersion %q, descriptor says %q", label, res.ConnectorVersion, c.desc.Version)
	}
	if cs.Want != nil && !sameAnswer(res, *cs.Want) {
		c.fail("%s: answer %s, want %s", label, show(res), show(*cs.Want))
	}
	return len(c.problems) == n
}

// sameAnswer compares what a hook acts on: outcome, patch, reason code.
func sameAnswer(a, b hookproto.Response) bool {
	code := func(r hookproto.Response) string {
		if r.Reason == nil {
			return ""
		}
		return r.Reason.Code
	}
	return a.Outcome == b.Outcome && code(a) == code(b) && jsonSame(a.Patch, b.Patch)
}

func jsonSame(a, b map[string]any) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	var u, v any
	_ = json.Unmarshal(x, &u)
	_ = json.Unmarshal(y, &v)
	return reflect.DeepEqual(u, v)
}

func show(r hookproto.Response) string {
	s := string(r.Outcome)
	if r.Reason != nil {
		s += " " + r.Reason.Code
	}
	if len(r.Patch) > 0 {
		p, _ := json.Marshal(r.Patch)
		s += " " + string(p)
	}
	return s
}

// pair finds two answered cases of one operation and kind with different
// inputs and different answers' inputs.
func pair(cases []Case, answered map[int]hookproto.Response) (Case, Case, bool) {
	for i := range cases {
		for j := range cases {
			if i == j {
				continue
			}
			_, ai := answered[i]
			_, aj := answered[j]
			if ai && aj && cases[i].Operation == cases[j].Operation && cases[i].Kind == cases[j].Kind &&
				!jsonSame(cases[i].Inputs, cases[j].Inputs) {
				return cases[i], cases[j], true
			}
		}
	}
	return Case{}, Case{}, false
}

// --- idempotency ---

func (c *checker) idempotency(a, b Case) {
	key := newID("idem")
	req := c.request(a, key)
	body := mustJSON(req)
	ts := time.Now()
	headers := c.headers(body, hookproto.WebhookID(key), ts, c.secret)

	status, first, _ := c.sendRaw(body, headers)
	if status != http.StatusOK {
		c.fail("idempotency: first request = %d, want 200", status)
		return
	}
	// A replay: the same bytes and headers. The same answer, no new effect.
	status, again, _ := c.sendRaw(body, headers)
	if status != http.StatusOK || !reflect.DeepEqual(first, again) {
		c.fail("idempotency: a replayed request = %d %s, want 200 and the identical answer %s", status, show(again), show(first))
	}
	// A resend: a new attempt, new request ID, same key and inputs.
	resend := req
	resend.RequestID, resend.Attempt = newID("req"), 2
	status, res, _ := c.send(resend, nil)
	if status != http.StatusOK || !sameAnswer(res, first) || res.RequestID != resend.RequestID {
		c.fail("idempotency: a resend under the same key = %d %s (requestId %q), want 200, %s, echoing %q",
			status, show(res), res.RequestID, show(first), resend.RequestID)
	}
	// The same key with another request: a conflict, never the stored answer.
	conflict := c.request(b, key)
	status, res, prob := c.send(conflict, nil)
	if status != http.StatusUnprocessableEntity || prob.Class != hookproto.ClassKeyConflict {
		c.fail("idempotency: the same key with other inputs = %d %s %+v, want 422 key_conflict", status, show(res), prob)
	}
}

// failuresNotStored: a key whose request failed is not taken. After an
// outage, the same key with other inputs is answered as if new.
func (c *checker) failuresNotStored(a, b Case) {
	if c.target.Outage == nil {
		c.fail("Target.Outage is nil: the check that failures are not stored needs a way to make the connector fail")
		return
	}
	key := newID("fail")
	c.target.Outage(true)
	status, _, prob := c.send(c.request(a, key), nil)
	c.target.Outage(false)
	if status < 500 || prob.Class != hookproto.ClassTransient {
		c.fail("failures: during an outage = %d %+v, want 5xx transient", status, prob)
	}
	status, res, prob := c.send(c.request(b, key), nil)
	if status != http.StatusOK || (b.Want != nil && !sameAnswer(res, *b.Want)) {
		c.fail("failures: the failed key with other inputs = %d %s %+v; want 200 %s: a failure must not be stored",
			status, show(res), prob, showWant(b))
	}
}

func showWant(cs Case) string {
	if cs.Want == nil {
		return "(any)"
	}
	return show(*cs.Want)
}

// --- signatures ---

func (c *checker) signatures(a Case) {
	other, _ := hookproto.ParseSecret(hookproto.GenerateSecret())
	type variant struct {
		name   string
		mutate func(body []byte, key string) ([]byte, http.Header)
	}
	sign := func(body []byte, key string, ts time.Time, s hookproto.Secret) http.Header {
		return c.headers(body, hookproto.WebhookID(key), ts, s)
	}
	refused := []variant{
		{"unsigned", func(body []byte, _ string) ([]byte, http.Header) { return body, http.Header{} }},
		{"a tampered body", func(body []byte, key string) ([]byte, http.Header) {
			h := sign(body, key, time.Now(), c.secret)
			return bytes.Replace(body, []byte(`"attempt":1`), []byte(`"attempt":7`), 1), h
		}},
		{"another secret", func(body []byte, key string) ([]byte, http.Header) {
			return body, sign(body, key, time.Now(), other)
		}},
		{"six minutes old", func(body []byte, key string) ([]byte, http.Header) {
			return body, sign(body, key, time.Now().Add(-6*time.Minute), c.secret)
		}},
		{"six minutes ahead", func(body []byte, key string) ([]byte, http.Header) {
			return body, sign(body, key, time.Now().Add(6*time.Minute), c.secret)
		}},
		{"only a scheme it does not know", func(body []byte, key string) ([]byte, http.Header) {
			h := sign(body, key, time.Now(), c.secret)
			h.Set(hookproto.HeaderSignature, strings.Replace(h.Get(hookproto.HeaderSignature), "v1,", "v9,", 1))
			return body, h
		}},
	}
	for _, v := range refused {
		key := newID("sig")
		body, h := v.mutate(mustJSON(c.request(a, key)), key)
		status, _, prob := c.sendRaw(body, h)
		if status != http.StatusUnauthorized || prob.Class != hookproto.ClassConfig {
			c.fail("signatures: %s = %d %+v, want 401 config", v.name, status, prob)
		}
		// A refused request stores nothing: the same key, signed, is answered.
		status, res, _ := c.send(c.request(a, key), nil)
		if status != http.StatusOK || (a.Want != nil && !sameAnswer(res, *a.Want)) {
			c.fail("signatures: after %s, the same key signed = %d %s, want 200 %s", v.name, status, show(res), showWant(a))
		}
	}
	// Entries it cannot read are ignored.
	key := newID("sig")
	body := mustJSON(c.request(a, key))
	h := sign(body, key, time.Now(), c.secret)
	h.Set(hookproto.HeaderSignature, ",,, v1,not,base64  v2a,AAAA "+h.Get(hookproto.HeaderSignature))
	if status, _, prob := c.sendRaw(body, h); status != http.StatusOK {
		c.fail("signatures: unreadable entries beside a good signature = %d %+v, want 200", status, prob)
	}
}

// --- the protocol ---

func (c *checker) protocol(a Case) {
	req := c.request(a, newID("proto"))
	req.Protocol = "pli.hooks/v9"
	if status, _, prob := c.send(req, nil); status != http.StatusBadRequest || prob.Class != hookproto.ClassInvalid {
		c.fail("protocol: protocol pli.hooks/v9 = %d %+v, want 400 invalid", status, prob)
	}

	key := newID("proto")
	body := []byte(`{"protocol":"pli.hooks/v1",`)
	if status, _, prob := c.sendRaw(body, c.headers(body, hookproto.WebhookID(key), time.Now(), c.secret)); status != http.StatusBadRequest ||
		prob.Class != hookproto.ClassInvalid {
		c.fail("protocol: a malformed body = %d %+v, want 400 invalid", status, prob)
	}

	req = c.request(a, newID("proto"))
	req.Hook.Operation = "no_such_operation"
	if status, _, prob := c.send(req, nil); status != http.StatusNotFound || prob.Class != hookproto.ClassInvalid {
		c.fail("protocol: an unknown operation = %d %+v, want 404 invalid", status, prob)
	}

	op, _ := c.desc.Operation(a.Operation)
	for _, k := range []hookproto.Kind{hookproto.KindCheck, hookproto.KindEnrich, hookproto.KindAction} {
		if slices.Contains(op.Kinds, k) {
			continue
		}
		req = c.request(a, newID("proto"))
		req.Kind, req.Transition = k, nil
		if status, _, prob := c.send(req, nil); status != http.StatusBadRequest || prob.Class != hookproto.ClassInvalid {
			c.fail("protocol: kind %s, which %s does not take = %d %+v, want 400 invalid", k, a.Operation, status, prob)
		}
		break
	}

	// Unknown envelope fields are ignored.
	req = c.request(a, newID("proto"))
	var m map[string]any
	_ = json.Unmarshal(mustJSON(req), &m)
	m["futureField"] = map[string]any{"x": 1}
	body = mustJSON(m)
	status, res, prob := c.sendRaw(body, c.headers(body, hookproto.WebhookID(req.IdempotencyKey), time.Now(), c.secret))
	if status != http.StatusOK || (a.Want != nil && !sameAnswer(res, *a.Want)) {
		c.fail("protocol: an unknown envelope field = %d %s %+v, want 200 %s", status, show(res), prob, showWant(a))
	}
}

// --- plumbing ---

// request builds the envelope for a case under key.
func (c *checker) request(cs Case, key string) hookproto.Request {
	major, _ := hookproto.Major(c.desc.Version)
	item := newID("item")
	req := hookproto.Request{
		Protocol: hookproto.Protocol, Kind: cs.Kind, RequestID: newID("req"), IdempotencyKey: key, Attempt: 1,
		Deadline: time.Now().Add(30 * time.Second).UTC().Truncate(time.Second),
		Hook: hookproto.HookRef{Binding: "conformance@1#" + cs.Operation,
			Connector: c.desc.Connector + "@" + strconv.Itoa(major), Operation: cs.Operation},
		Event:      hookproto.Event{ID: item + ":1", Type: hookproto.EventStateEntered, Subject: "item/" + item},
		Actor:      hookproto.Actor{Principal: "user:conformance"},
		Item:       hookproto.ItemRef{ID: item, Type: "conformance", TypeVersion: 1},
		Inputs:     cs.Inputs,
		Trace:      hookproto.Trace{CausationID: item + ":1"},
		Connection: hookproto.ConnectionRef{Slot: c.desc.Connector, Ref: "conformance"},
	}
	if cs.Kind == hookproto.KindCheck {
		req.Event.Type = hookproto.EventTransitionRequested
		req.Transition = &hookproto.Transition{From: "a", To: "b"}
	}
	return req
}

func (c *checker) headers(body []byte, id string, ts time.Time, s hookproto.Secret) http.Header {
	h := http.Header{}
	hookproto.SetSignatureHeaders(h, s, id, ts, body)
	return h
}

// send signs and posts req (extra headers added) and decodes the answer.
func (c *checker) send(req hookproto.Request, extra http.Header) (int, hookproto.Response, hookproto.Problem) {
	body := mustJSON(req)
	h := c.headers(body, hookproto.WebhookID(req.IdempotencyKey), time.Now(), c.secret)
	for k, v := range extra {
		h[k] = v
	}
	return c.sendRaw(body, h)
}

// sendRaw posts body with headers h. A 200 is decoded as a Response; any
// other status must carry a problem+json body whose status agrees.
func (c *checker) sendRaw(body []byte, h http.Header) (int, hookproto.Response, hookproto.Problem) {
	req, err := http.NewRequest(http.MethodPost, c.target.URL+hookproto.HooksPath, bytes.NewReader(body))
	if err != nil {
		c.fail("build request: %v", err)
		return 0, hookproto.Response{}, hookproto.Problem{}
	}
	req.Header = h.Clone()
	req.Header.Set("Content-Type", hookproto.ContentType)
	res, err := c.client.Do(req)
	if err != nil {
		c.fail("POST %s: %v", hookproto.HooksPath, err)
		return 0, hookproto.Response{}, hookproto.Problem{}
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(res.Body)
	var out hookproto.Response
	var prob hookproto.Problem
	if res.StatusCode == http.StatusOK {
		if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, hookproto.ContentType) {
			c.fail("answer Content-Type %q, want %s", ct, hookproto.ContentType)
		}
		if err := json.Unmarshal(data, &out); err != nil {
			c.fail("answer is not JSON: %v: %s", err, data)
		}
		return res.StatusCode, out, prob
	}
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, hookproto.ProblemContentType) {
		c.fail("a %d's Content-Type %q, want %s", res.StatusCode, ct, hookproto.ProblemContentType)
	}
	if err := json.Unmarshal(data, &prob); err != nil {
		c.fail("a %d's body is not a problem: %v: %s", res.StatusCode, err, data)
	} else if prob.Status != res.StatusCode || prob.Class == "" || prob.Title == "" {
		c.fail("a %d's problem %+v: want its status, a class and a title", res.StatusCode, prob)
	}
	return res.StatusCode, out, prob
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func newID(prefix string) string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return prefix + "-" + hex.EncodeToString(b)
}
