// Tests for the hook protocol spike, worker side: a hook whose
// connector@major is configured as a remote service. RunCall builds the
// pli.hooks/v1 envelope (the hook's reads by the operation's input names,
// mapped by position), signs it with the connection's secret, posts it,
// and reads the answer back into a Result (the patch by the operation's
// output names, mapped by position to the hook's writes).
//
// A fake connector stands in here, so each test can say exactly what it
// answers; the real HR directory's protocol tests are in internal/connstub.
//
// How answers fail:
//
//   - permanent (ErrPermanent): 400, 404, 410, a redirect (never followed),
//     an answer that breaks the protocol (another request ID, an outcome the
//     kind does not answer, a patch naming an undeclared output);
//   - 401, 403: ErrConnectorUnauthorized; 422: ErrKeyConflict;
//   - ErrConnectorMismatch: the descriptor is not the declared connector
//     (fetched once per URL, before the first call), or a deny reason it
//     does not declare;
//   - ErrConnectorUnconfigured: no connection for a remote-only connector;
//   - retryable: 409, 429, 5xx, a network failure, an unreachable
//     descriptor; a problem's retryable flag overrides the status.
package hooks_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/francoisnyc/work-kernel/internal/hookproto"
	"github.com/francoisnyc/work-kernel/internal/hooks"
)

// hrDescriptor is the HR connector's descriptor, as the catalog declares
// hr.manager_of@1.
func hrDescriptor() hookproto.Descriptor {
	str := hookproto.Property{Type: hookproto.TypeList{"string", "null"}}
	return hookproto.Descriptor{
		Connector: "hr", Version: "1.4.0", SupportedProtocolVersions: []string{hookproto.Protocol},
		Operations: []hookproto.Operation{{
			Name: "manager_of", Kinds: []hookproto.Kind{hookproto.KindCheck},
			InputSchema: hookproto.Schema{Type: "object", Properties: map[string]hookproto.Property{"onBehalfOf": str, "createdBy": str}},
			OutputSchema: &hookproto.Schema{Type: "object",
				Properties: map[string]hookproto.Property{"manager": {Type: hookproto.TypeList{"string"}}}},
			SideEffects: "none", Async: "forbidden", Errors: []string{"unknown_employee", "no_eligible_approver"},
		}},
	}
}

// fake is a connector whose descriptor and answers a test sets.
type fake struct {
	t      *testing.T
	srv    *httptest.Server
	secret string

	mu          sync.Mutex
	desc        hookproto.Descriptor
	descStatus  int // 0: 200
	descGets    int
	posts       []posted
	answer      func(req hookproto.Request) (status int, body any)
	redirectsTo string
}

type posted struct {
	req      hookproto.Request
	raw      []byte
	header   http.Header
	verified error
}

func newFake(t *testing.T, desc hookproto.Descriptor) *fake {
	t.Helper()
	f := &fake{t: t, secret: hookproto.GenerateSecret(), desc: desc}
	f.answer = func(req hookproto.Request) (int, any) {
		return 200, hookproto.Response{Protocol: hookproto.Protocol, RequestID: req.RequestID,
			Outcome: hookproto.OutcomeCorrect, Patch: map[string]any{"manager": "user:bob"}, ConnectorVersion: "1.4.0"}
	}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fake) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.Method == http.MethodGet && r.URL.Path == hookproto.DescriptorPath:
		f.descGets++
		if f.descStatus != 0 {
			w.WriteHeader(f.descStatus)
			return
		}
		w.Header().Set("Content-Type", hookproto.ContentType)
		_ = json.NewEncoder(w).Encode(f.desc)
	case r.Method == http.MethodPost && r.URL.Path == hookproto.HooksPath:
		raw, _ := io.ReadAll(r.Body)
		var req hookproto.Request
		_ = json.Unmarshal(raw, &req)
		s, _ := hookproto.ParseSecret(f.secret)
		_, verr := hookproto.Verifier{Secrets: []hookproto.Secret{s}}.Verify(r.Header, raw)
		f.posts = append(f.posts, posted{req: req, raw: raw, header: r.Header.Clone(), verified: verr})
		if f.redirectsTo != "" {
			http.Redirect(w, r, f.redirectsTo, http.StatusTemporaryRedirect)
			return
		}
		status, body := f.answer(req)
		if status == 200 {
			w.Header().Set("Content-Type", hookproto.ContentType)
		} else {
			w.Header().Set("Content-Type", hookproto.ProblemContentType)
		}
		w.WriteHeader(status)
		switch b := body.(type) {
		case []byte:
			_, _ = w.Write(b)
		default:
			_ = json.NewEncoder(w).Encode(b)
		}
	default:
		http.NotFound(w, r)
	}
}

func (f *fake) connection() hooks.Connection {
	return hooks.Connection{URL: f.srv.URL, Secret: f.secret, Ref: "hr-directory"}
}

func (f *fake) counts() (descGets, posts int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.descGets, len(f.posts)
}

func (f *fake) lastPost() posted {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.posts) == 0 {
		f.t.Fatal("nothing was posted")
	}
	return f.posts[len(f.posts)-1]
}

// hrCall is the HR check's call for ana's request, made by an agent.
func hrCall() hooks.Call {
	return hooks.Call{HookID: "hr_check", ItemID: "item-1", Kind: hookproto.KindCheck,
		TypeID: "vacation_request", TypeVersion: 3, From: "draft", To: "submitted",
		EventID: "item-1:4", EventType: hookproto.EventTransitionRequested,
		CorrelationID: "corr-1", Attempt: 2}
}

func remoteHR(t *testing.T, f *fake) *hooks.Connectors {
	t.Helper()
	return newWorld(t).c.WithConnection("hr@1", f.connection())
}

func TestRemote_SendsTheEnvelope(t *testing.T) {
	f := newFake(t, hrDescriptor())
	c := remoteHR(t, f)
	cctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	deadline, _ := cctx.Deadline()

	res, err := c.RunCall(cctx, "hr.manager_of", "hold:h-1", []any{"user:ana", "agent:triage-bot"}, hrCall())
	if err != nil || !reflect.DeepEqual(res, hooks.Result{Verdict: hooks.Correct, Values: []any{"user:bob"}}) {
		t.Fatalf("RunCall = %+v, %v; want correct [user:bob]", res, err)
	}
	p := f.lastPost()
	want := hookproto.Request{
		Protocol: hookproto.Protocol, Kind: hookproto.KindCheck, RequestID: p.req.RequestID, IdempotencyKey: "hold:h-1",
		Attempt: 2, Deadline: p.req.Deadline,
		Hook:       hookproto.HookRef{Binding: "vacation_request@3#hr_check", Connector: "hr@1", Operation: "manager_of"},
		Event:      hookproto.Event{ID: "item-1:4", Type: hookproto.EventTransitionRequested, Subject: "item/item-1"},
		Item:       hookproto.ItemRef{ID: "item-1", Type: "vacation_request", TypeVersion: 3},
		Transition: &hookproto.Transition{From: "draft", To: "submitted"},
		Inputs:     map[string]any{"onBehalfOf": "user:ana", "createdBy": "agent:triage-bot"},
		Trace:      hookproto.Trace{CorrelationID: "corr-1", CausationID: "item-1:4"},
		Connection: hookproto.ConnectionRef{Slot: "hr", Ref: "hr-directory"},
	}
	if !reflect.DeepEqual(p.req, want) {
		t.Errorf("envelope =\n%+v\nwant\n%+v", p.req, want)
	}
	if p.req.RequestID == "" {
		t.Error("no requestId")
	}
	if !p.req.Deadline.Equal(deadline) {
		t.Errorf("deadline %v, want the context's %v", p.req.Deadline, deadline)
	}
	// Signed with the connection's secret; the webhook-id comes from the
	// key; the key travels in the envelope and the Idempotency-Key header.
	if p.verified != nil {
		t.Errorf("signature: %v", p.verified)
	}
	if got := p.header.Get(hookproto.HeaderID); got != hookproto.WebhookID("hold:h-1") {
		t.Errorf("webhook-id %q, want WebhookID(hold:h-1)", got)
	}
	if got := p.header.Get("Idempotency-Key"); got != "hold:h-1" {
		t.Errorf("Idempotency-Key %q, want hold:h-1", got)
	}
	if ct := p.header.Get("Content-Type"); !strings.HasPrefix(ct, hookproto.ContentType) {
		t.Errorf("Content-Type %q", ct)
	}
	// The secret itself never travels.
	if strings.Contains(string(p.raw), f.secret) || strings.Contains(string(p.raw), "whsec_") {
		t.Errorf("the body carries the secret: %s", p.raw)
	}
	for name, vs := range p.header {
		if strings.Contains(strings.Join(vs, " "), f.secret) {
			t.Errorf("header %s carries the secret", name)
		}
	}

	// Every attempt has its own request ID; nil reads are sent as null.
	if _, err := c.RunCall(cctx, "hr.manager_of", "hold:h-1", []any{nil, "user:ana"}, hrCall()); err != nil {
		t.Fatal(err)
	}
	q := f.lastPost()
	if q.req.RequestID == p.req.RequestID {
		t.Errorf("two attempts share requestId %q", q.req.RequestID)
	}
	if v, ok := q.req.Inputs["onBehalfOf"]; !ok || v != nil {
		t.Errorf("an unset read = %v (present %v), want null", v, ok)
	}
}

func TestRemote_AnswersBecomeResults(t *testing.T) {
	f := newFake(t, hrDescriptor())
	c := remoteHR(t, f)
	cases := []struct {
		answer hookproto.Response
		want   hooks.Result
	}{
		{hookproto.Response{Outcome: hookproto.OutcomeAllow}, hooks.Result{Verdict: hooks.Allow}},
		{hookproto.Response{Outcome: hookproto.OutcomeDeny, Reason: &hookproto.Reason{Code: "unknown_employee", Message: "no such person"}},
			hooks.Result{Verdict: hooks.Deny, Reason: "unknown_employee"}},
		{hookproto.Response{Outcome: hookproto.OutcomeCorrect, Patch: map[string]any{"manager": "user:dana"}},
			hooks.Result{Verdict: hooks.Correct, Values: []any{"user:dana"}}},
	}
	for _, tc := range cases {
		f.mu.Lock()
		f.answer = func(req hookproto.Request) (int, any) {
			a := tc.answer
			a.Protocol, a.RequestID, a.ConnectorVersion = hookproto.Protocol, req.RequestID, "1.4.0"
			return 200, a
		}
		f.mu.Unlock()
		got, err := c.RunCall(ctx(t), "hr.manager_of", "hold:h-1", []any{nil, "user:ana"}, hrCall())
		if err != nil || !reflect.DeepEqual(got, tc.want) {
			t.Errorf("answer %+v: RunCall = %+v, %v; want %+v", tc.answer, got, err, tc.want)
		}
	}
}

func problem(status int, class hookproto.Class, retryable *bool) hookproto.Problem {
	return hookproto.Problem{Title: "refused", Status: status, Class: class, Retryable: retryable}
}

func TestRemote_HowFailuresAreClassed(t *testing.T) {
	yes, no := true, false
	type want int
	const (
		retryable want = iota
		permanent
		unauthorized
		keyConflict
	)
	cases := []struct {
		name   string
		status int
		body   any
		want   want
	}{
		{"400", 400, problem(400, hookproto.ClassInvalid, nil), permanent},
		{"401", 401, problem(401, hookproto.ClassConfig, nil), unauthorized},
		{"403", 403, problem(403, hookproto.ClassConfig, nil), unauthorized},
		{"404", 404, problem(404, hookproto.ClassInvalid, nil), permanent},
		{"409 in flight", 409, problem(409, hookproto.ClassInFlight, nil), retryable},
		{"410", 410, problem(410, hookproto.ClassGone, nil), permanent},
		{"422", 422, problem(422, hookproto.ClassKeyConflict, nil), keyConflict},
		{"429", 429, problem(429, hookproto.ClassRateLimited, nil), retryable},
		{"500", 500, problem(500, hookproto.ClassTransient, nil), retryable},
		{"502", 502, problem(502, hookproto.ClassTransient, nil), retryable},
		{"503", 503, problem(503, hookproto.ClassTransient, nil), retryable},
		{"504", 504, problem(504, hookproto.ClassTransient, nil), retryable},
		{"503 that says it is not retryable", 503, problem(503, hookproto.ClassTransient, &no), permanent},
		{"400 that says it is retryable", 400, problem(400, hookproto.ClassInvalid, &yes), retryable},
		{"500 with no problem body", 500, []byte("oops"), retryable},
		{"200 that is not JSON", 200, []byte("{"), retryable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake(t, hrDescriptor())
			f.answer = func(hookproto.Request) (int, any) { return tc.status, tc.body }
			_, err := remoteHR(t, f).RunCall(ctx(t), "hr.manager_of", "hold:h-1", []any{nil, "user:ana"}, hrCall())
			switch tc.want {
			case retryable:
				if err == nil || errors.Is(err, hooks.ErrPermanent) {
					t.Errorf("error = %v, want a retryable error", err)
				}
			case permanent:
				if !errors.Is(err, hooks.ErrPermanent) || errors.Is(err, hooks.ErrConnectorUnauthorized) ||
					errors.Is(err, hooks.ErrKeyConflict) || errors.Is(err, hooks.ErrConnectorMismatch) {
					t.Errorf("error = %v, want plain ErrPermanent", err)
				}
			case unauthorized:
				if !errors.Is(err, hooks.ErrConnectorUnauthorized) || !errors.Is(err, hooks.ErrPermanent) {
					t.Errorf("error = %v, want ErrConnectorUnauthorized (permanent)", err)
				}
			case keyConflict:
				if !errors.Is(err, hooks.ErrKeyConflict) {
					t.Errorf("error = %v, want ErrKeyConflict", err)
				}
			}
		})
	}
}

// A redirect is never followed: it is a permanent failure, and the target
// is never called.
func TestRemote_RedirectIsNeverFollowed(t *testing.T) {
	target := newFake(t, hrDescriptor())
	f := newFake(t, hrDescriptor())
	f.redirectsTo = target.srv.URL + hookproto.HooksPath
	_, err := remoteHR(t, f).RunCall(ctx(t), "hr.manager_of", "hold:h-1", []any{nil, "user:ana"}, hrCall())
	if !errors.Is(err, hooks.ErrPermanent) {
		t.Errorf("a redirect: error = %v, want ErrPermanent", err)
	}
	if _, posts := target.counts(); posts != 0 {
		t.Errorf("the redirect's target got %d requests, want none", posts)
	}
}

// An answer that breaks the protocol is permanent; a deny reason the
// descriptor does not declare is a mismatch.
func TestRemote_RefusesAnswersThatBreakTheProtocol(t *testing.T) {
	cases := []struct {
		name   string
		answer func(req hookproto.Request) hookproto.Response
		want   error
	}{
		{"another request ID", func(req hookproto.Request) hookproto.Response {
			return hookproto.Response{Protocol: hookproto.Protocol, RequestID: "someone-else", Outcome: hookproto.OutcomeAllow}
		}, hooks.ErrPermanent},
		{"another protocol", func(req hookproto.Request) hookproto.Response {
			return hookproto.Response{Protocol: "pli.hooks/v2", RequestID: req.RequestID, Outcome: hookproto.OutcomeAllow}
		}, hooks.ErrPermanent},
		{"an outcome nobody answers", func(req hookproto.Request) hookproto.Response {
			return hookproto.Response{Protocol: hookproto.Protocol, RequestID: req.RequestID, Outcome: "maybe"}
		}, hooks.ErrPermanent},
		{"an enrich outcome to a check", func(req hookproto.Request) hookproto.Response {
			return hookproto.Response{Protocol: hookproto.Protocol, RequestID: req.RequestID, Outcome: hookproto.OutcomeOK,
				Patch: map[string]any{"manager": "user:bob"}}
		}, hooks.ErrPermanent},
		{"pending, which v1 does not build", func(req hookproto.Request) hookproto.Response {
			return hookproto.Response{Protocol: hookproto.Protocol, RequestID: req.RequestID, Outcome: hookproto.OutcomePending}
		}, hooks.ErrPermanent},
		{"a patch naming an undeclared output", func(req hookproto.Request) hookproto.Response {
			return hookproto.Response{Protocol: hookproto.Protocol, RequestID: req.RequestID, Outcome: hookproto.OutcomeCorrect,
				Patch: map[string]any{"manager": "user:bob", "salary": 1}}
		}, hooks.ErrPermanent},
		{"a correct without the output", func(req hookproto.Request) hookproto.Response {
			return hookproto.Response{Protocol: hookproto.Protocol, RequestID: req.RequestID, Outcome: hookproto.OutcomeCorrect}
		}, hooks.ErrPermanent},
		{"a deny reason it does not declare", func(req hookproto.Request) hookproto.Response {
			return hookproto.Response{Protocol: hookproto.Protocol, RequestID: req.RequestID, Outcome: hookproto.OutcomeDeny,
				Reason: &hookproto.Reason{Code: "made_up"}}
		}, hooks.ErrConnectorMismatch},
		{"a deny without a reason", func(req hookproto.Request) hookproto.Response {
			return hookproto.Response{Protocol: hookproto.Protocol, RequestID: req.RequestID, Outcome: hookproto.OutcomeDeny}
		}, hooks.ErrConnectorMismatch},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake(t, hrDescriptor())
			f.answer = func(req hookproto.Request) (int, any) { return 200, tc.answer(req) }
			_, err := remoteHR(t, f).RunCall(ctx(t), "hr.manager_of", "hold:h-1", []any{nil, "user:ana"}, hrCall())
			if !errors.Is(err, tc.want) {
				t.Errorf("error = %v, want %v", err, tc.want)
			}
		})
	}
}

// The descriptor at the connection's URL must be the connector the catalog
// declares: otherwise ErrConnectorMismatch, at once, and the hook is never
// posted.
func TestRemote_DescriptorMismatch(t *testing.T) {
	cases := map[string]func(d *hookproto.Descriptor){
		"another connector":            func(d *hookproto.Descriptor) { d.Connector = "crm" },
		"another major":                func(d *hookproto.Descriptor) { d.Version = "2.0.0" },
		"a version that is not semver": func(d *hookproto.Descriptor) { d.Version = "banana" },
		"no pli.hooks/v1":              func(d *hookproto.Descriptor) { d.SupportedProtocolVersions = []string{"pli.hooks/v2"} },
		"no such operation":            func(d *hookproto.Descriptor) { d.Operations[0].Name = "boss_of" },
		"not a check":                  func(d *hookproto.Descriptor) { d.Operations[0].Kinds = []hookproto.Kind{hookproto.KindEnrich} },
		"other input names": func(d *hookproto.Descriptor) {
			d.Operations[0].InputSchema.Properties = map[string]hookproto.Property{"requester": {Type: hookproto.TypeList{"string"}}}
		},
		"an extra input": func(d *hookproto.Descriptor) {
			d.Operations[0].InputSchema.Properties["department"] = hookproto.Property{Type: hookproto.TypeList{"string"}}
		},
		"other output names": func(d *hookproto.Descriptor) {
			d.Operations[0].OutputSchema.Properties = map[string]hookproto.Property{"boss": {Type: hookproto.TypeList{"string"}}}
		},
		"no outputs": func(d *hookproto.Descriptor) { d.Operations[0].OutputSchema = nil },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			d := hrDescriptor()
			mutate(&d)
			f := newFake(t, d)
			_, err := remoteHR(t, f).RunCall(ctx(t), "hr.manager_of", "hold:h-1", []any{nil, "user:ana"}, hrCall())
			if !errors.Is(err, hooks.ErrConnectorMismatch) {
				t.Errorf("error = %v, want ErrConnectorMismatch", err)
			}
			if _, posts := f.counts(); posts != 0 {
				t.Errorf("the hook was posted %d times, want never", posts)
			}
		})
	}
	// A later minor version of the same major is the same connector.
	d := hrDescriptor()
	d.Version = "1.9.3"
	f := newFake(t, d)
	if _, err := remoteHR(t, f).RunCall(ctx(t), "hr.manager_of", "hold:h-1", []any{nil, "user:ana"}, hrCall()); err != nil {
		t.Errorf("version 1.9.3 for hr@1: %v", err)
	}
}

// The descriptor is read once per URL; one that cannot be read is a
// retryable failure, not remembered: the next call reads it again.
func TestRemote_DescriptorIsReadOncePerURL(t *testing.T) {
	f := newFake(t, hrDescriptor())
	f.descStatus = http.StatusServiceUnavailable
	c := remoteHR(t, f)
	_, err := c.RunCall(ctx(t), "hr.manager_of", "hold:h-1", []any{nil, "user:ana"}, hrCall())
	if err == nil || errors.Is(err, hooks.ErrPermanent) {
		t.Fatalf("descriptor unavailable: error = %v, want a retryable error", err)
	}
	f.mu.Lock()
	f.descStatus = 0
	f.mu.Unlock()
	for i := 0; i < 3; i++ {
		if _, err := c.RunCall(ctx(t), "hr.manager_of", "hold:h-1", []any{nil, "user:ana"}, hrCall()); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if gets, posts := f.counts(); gets != 2 || posts != 3 {
		t.Errorf("descriptor GETs %d, hook POSTs %d; want 2 (one failed, one read) and 3", gets, posts)
	}
}

// A remote-only connector with no connection is unconfigured: permanent.
func TestRemote_Unconfigured(t *testing.T) {
	_, err := newWorld(t).c.RunCall(ctx(t), "hr.manager_of", "hold:h-1", []any{nil, "user:ana"}, hrCall())
	if !errors.Is(err, hooks.ErrConnectorUnconfigured) || !errors.Is(err, hooks.ErrPermanent) {
		t.Errorf("no hr@1 connection: error = %v, want ErrConnectorUnconfigured (permanent)", err)
	}
	if _, err := newWorld(t).c.Connector("hr@1"); !errors.Is(err, hooks.ErrConnectorUnconfigured) {
		t.Errorf("Connector(hr@1) = %v, want ErrConnectorUnconfigured", err)
	}
}

// A remote enrich operation: outcome ok, its patch back as values in the
// order of the declared outputs. (No real remote enrich connector exists
// yet; the test declares one.)
func TestRemote_Enrich(t *testing.T) {
	hooks.DeclareRemoteForTest("text.upper@1", []hookproto.Kind{hookproto.KindEnrich}, []string{"text"}, []string{"upper", "length"})
	str := hookproto.Property{Type: hookproto.TypeList{"string"}}
	f := newFake(t, hookproto.Descriptor{Connector: "text", Version: "1.0.0", SupportedProtocolVersions: []string{hookproto.Protocol},
		Operations: []hookproto.Operation{{Name: "upper", Kinds: []hookproto.Kind{hookproto.KindEnrich},
			InputSchema: hookproto.Schema{Type: "object", Properties: map[string]hookproto.Property{"text": str}},
			OutputSchema: &hookproto.Schema{Type: "object", Properties: map[string]hookproto.Property{
				"upper": str, "length": {Type: hookproto.TypeList{"integer"}}}},
			SideEffects: "none", Async: "forbidden"}}})
	f.answer = func(req hookproto.Request) (int, any) {
		s, _ := req.Inputs["text"].(string)
		return 200, hookproto.Response{Protocol: hookproto.Protocol, RequestID: req.RequestID, Outcome: hookproto.OutcomeOK,
			Patch: map[string]any{"length": len(s), "upper": strings.ToUpper(s)}, ConnectorVersion: "1.0.0"}
	}
	c := newWorld(t).c.WithConnection("text@1", hooks.Connection{URL: f.srv.URL, Secret: f.secret})
	call := hooks.Call{HookID: "shout", ItemID: "item-2", Kind: hookproto.KindEnrich, TypeID: "note", TypeVersion: 1,
		EventID: "item-2:1", EventType: hookproto.EventStateEntered}
	res, err := c.RunCall(ctx(t), "text.upper", "hook:item-2:1:shout", []any{"ana"}, call)
	if err != nil || !reflect.DeepEqual(res, hooks.Result{Values: []any{"ANA", float64(3)}}) {
		t.Errorf("RunCall = %+v, %v; want values [ANA 3]", res, err)
	}
	p := f.lastPost()
	if p.req.Kind != hookproto.KindEnrich || p.req.Transition != nil || p.req.Connection != (hookproto.ConnectionRef{Slot: "text", Ref: "text@1"}) {
		t.Errorf("envelope kind %s, transition %+v, connection %+v; want enrich, none, {text text@1}",
			p.req.Kind, p.req.Transition, p.req.Connection)
	}
}

// Arity reads the major: none means 1; a major the catalog does not
// declare is not an action (a Type naming it is refused at load).
func TestArity_Majors(t *testing.T) {
	for _, name := range []string{"hr.manager_of", "hr.manager_of@1", "crm.get_plan@1", "email.notify@1"} {
		if _, _, ok := hooks.Arity(name); !ok {
			t.Errorf("Arity(%s): not found", name)
		}
	}
	if r, w, ok := hooks.Arity("hr.manager_of@1"); r != 2 || w != 1 || !ok {
		t.Errorf("Arity(hr.manager_of@1) = %d, %d, %v; want 2, 1", r, w, ok)
	}
	for _, name := range []string{"hr.manager_of@2", "crm.get_plan@0", "crm.get_plan@x", "crm.get_plan@", "crm.get_plan@1@1"} {
		if _, _, ok := hooks.Arity(name); ok {
			t.Errorf("Arity(%s) found, want not an action", name)
		}
	}
}
