// Tests for the hook protocol spike: the stub HR directory as a
// pli.hooks/v1 connector. It serves connector hr, operation manager_of (a
// check), on POST /hooks/v1, and its descriptor at
// /.well-known/pli-connector. What hr.manager_of decided in the worker
// before, it decides now: the requester is onBehalfOf if set, else
// createdBy; it answers correct with the requester's manager, or deny
// unknown_employee or no_eligible_approver; bad inputs are 400.
//
// Every request is signed (Standard Webhooks, the server's own secret);
// successes are stored by idempotency key, failures never. The knobs the
// other stubs have (latency, error rate, outage, a dropped first answer)
// apply to it, and its version can be set, so two instances can stand for
// HR 1.4 and 1.5.
package connstub_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/francoisnyc/work-kernel/internal/connstub"
	"github.com/francoisnyc/work-kernel/internal/hookproto"
	"github.com/francoisnyc/work-kernel/internal/hookproto/conformance"
)

func hrDirectory(t *testing.T) *connstub.Server {
	t.Helper()
	hr := connstub.NewHR()
	t.Cleanup(hr.Close)
	hr.SetManager("user:ana", "user:bob")
	hr.SetManager("user:bob", "user:dana")
	hr.SetManager("user:dana", "user:dana")
	return hr
}

func managerOf(name string, onBehalfOf, createdBy any, want *hookproto.Response) conformance.Case {
	return conformance.Case{Name: name, Kind: hookproto.KindCheck, Operation: "manager_of",
		Inputs: map[string]any{"onBehalfOf": onBehalfOf, "createdBy": createdBy}, Want: want}
}

func correctWith(manager string) *hookproto.Response {
	return &hookproto.Response{Outcome: hookproto.OutcomeCorrect, Patch: map[string]any{"manager": manager}}
}

func denyWith(code string) *hookproto.Response {
	return &hookproto.Response{Outcome: hookproto.OutcomeDeny, Reason: &hookproto.Reason{Code: code}}
}

// The cases are the old hr.manager_of tests' (internal/hooks/hr_test.go),
// now answered by the connector.
func TestHR_Conformance(t *testing.T) {
	hr := hrDirectory(t)
	refused := func(name string, onBehalfOf, createdBy any) conformance.Case {
		c := managerOf(name, onBehalfOf, createdBy, nil)
		c.WantStatus = http.StatusBadRequest
		return c
	}
	conformance.Run(t, conformance.Target{URL: hr.URL, Secret: hr.Secret(), Outage: hr.SetOutage, Cases: []conformance.Case{
		managerOf("ana's own request: her manager", nil, "user:ana", correctWith("user:bob")),
		managerOf("an agent's request for ana: ana's manager", "user:ana", "agent:triage-bot", correctWith("user:bob")),
		managerOf("bob's request: his manager", nil, "user:bob", correctWith("user:dana")),
		managerOf("dana manages herself", nil, "user:dana", denyWith("no_eligible_approver")),
		managerOf("erin is not in the directory", nil, "user:erin", denyWith("unknown_employee")),
		managerOf("an agent acting for itself is not an employee", nil, "agent:triage-bot", denyWith("unknown_employee")),
		managerOf("an empty onBehalfOf is unset", "", "user:ana", correctWith("user:bob")),
		refused("no requester", nil, nil),
		refused("an empty requester", nil, ""),
		refused("a requester that is not a string", nil, float64(3)),
		refused("an onBehalfOf that is not a string", true, "user:ana"),
	}})
}

func TestHR_Descriptor(t *testing.T) {
	hr := hrDirectory(t)
	no := false
	want := hookproto.Descriptor{
		Connector: "hr", Version: "1.0.0", SupportedProtocolVersions: []string{hookproto.Protocol},
		Operations: []hookproto.Operation{{
			Name: "manager_of", Kinds: []hookproto.Kind{hookproto.KindCheck},
			InputSchema: hookproto.Schema{Type: "object", AdditionalProperties: &no, Properties: map[string]hookproto.Property{
				"onBehalfOf": {Type: hookproto.TypeList{"string", "null"}},
				"createdBy":  {Type: hookproto.TypeList{"string", "null"}},
			}},
			OutputSchema: &hookproto.Schema{Type: "object", Properties: map[string]hookproto.Property{
				"manager": {Type: hookproto.TypeList{"string"}},
			}},
			SideEffects: "none", Async: "forbidden", Errors: []string{"unknown_employee", "no_eligible_approver"},
		}},
	}
	if got := descriptor(t, hr.URL); !reflect.DeepEqual(got, want) {
		t.Errorf("descriptor = %+v\nwant %+v", got, want)
	}
	// The descriptor is served whatever the knobs say: it is how the
	// worker checks it has the right connector, even during an outage.
	hr.SetOutage(true)
	hr.SetVersion("1.5.0")
	if got := descriptor(t, hr.URL); got.Version != "1.5.0" {
		t.Errorf("descriptor version after SetVersion(1.5.0) = %q", got.Version)
	}
}

func descriptor(t *testing.T, base string) hookproto.Descriptor {
	t.Helper()
	res, err := http.Get(base + hookproto.DescriptorPath)
	if err != nil {
		t.Fatalf("GET descriptor: %v", err)
	}
	defer res.Body.Close()
	var d hookproto.Descriptor
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET descriptor = %d", res.StatusCode)
	}
	if err := json.NewDecoder(res.Body).Decode(&d); err != nil {
		t.Fatalf("decode descriptor: %v", err)
	}
	return d
}

// hook posts one signed manager_of check under key and decodes the
// answer (or the problem).
func hook(t *testing.T, client *http.Client, hr *connstub.Server, key, createdBy string) (int, hookproto.Response, hookproto.Problem, error) {
	t.Helper()
	secret, err := hookproto.ParseSecret(hr.Secret())
	if err != nil {
		t.Fatalf("the HR secret: %v", err)
	}
	req := hookproto.Request{
		Protocol: hookproto.Protocol, Kind: hookproto.KindCheck, RequestID: "req-" + key + "-" + time.Now().Format("150405.000000000"),
		IdempotencyKey: key, Attempt: 1,
		Hook:       hookproto.HookRef{Binding: "vacation_request@3#hr_check", Connector: "hr@1", Operation: "manager_of"},
		Event:      hookproto.Event{ID: "item-1:2", Type: hookproto.EventTransitionRequested, Subject: "item/item-1"},
		Actor:      hookproto.Actor{Principal: createdBy},
		Item:       hookproto.ItemRef{ID: "item-1", Type: "vacation_request", TypeVersion: 3},
		Transition: &hookproto.Transition{From: "draft", To: "submitted"},
		Inputs:     map[string]any{"onBehalfOf": nil, "createdBy": createdBy},
		Trace:      hookproto.Trace{CausationID: "item-1:2"},
		Connection: hookproto.ConnectionRef{Slot: "hr", Ref: "hr-directory"},
	}
	body, _ := json.Marshal(req)
	r, _ := http.NewRequest(http.MethodPost, hr.URL+hookproto.HooksPath, bytes.NewReader(body))
	r.Header.Set("Content-Type", hookproto.ContentType)
	hookproto.SetSignatureHeaders(r.Header, secret, hookproto.WebhookID(key), time.Now(), body)
	res, err := client.Do(r)
	if err != nil {
		return 0, hookproto.Response{}, hookproto.Problem{}, err
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(res.Body)
	var out hookproto.Response
	var prob hookproto.Problem
	if res.StatusCode == http.StatusOK {
		_ = json.Unmarshal(data, &out)
	} else {
		_ = json.Unmarshal(data, &prob)
	}
	return res.StatusCode, out, prob, nil
}

// A success is stored by key: the same key gets the first answer even
// after the directory changed; a new key sees the change.
func TestHR_StoresItsAnswersByKey(t *testing.T) {
	hr := hrDirectory(t)
	c := http.DefaultClient
	if code, res, _, err := hook(t, c, hr, "k-1", "user:ana"); err != nil || code != 200 || res.Patch["manager"] != "user:bob" {
		t.Fatalf("first = %d %+v %v, want bob", code, res, err)
	}
	hr.SetManager("user:ana", "user:carl")
	if code, res, _, err := hook(t, c, hr, "k-1", "user:ana"); err != nil || code != 200 || res.Patch["manager"] != "user:bob" {
		t.Errorf("same key after a change = %d %+v %v, want the stored bob", code, res, err)
	}
	if code, res, _, err := hook(t, c, hr, "k-2", "user:ana"); err != nil || code != 200 || res.Patch["manager"] != "user:carl" {
		t.Errorf("new key after a change = %d %+v %v, want carl", code, res, err)
	}
	if code, _, prob, err := hook(t, c, hr, "k-1", "user:bob"); err != nil || code != http.StatusUnprocessableEntity ||
		prob.Class != hookproto.ClassKeyConflict {
		t.Errorf("same key, other inputs = %d %+v %v, want 422 key_conflict", code, prob, err)
	}
}

// SetDropFirstAnswer: the first answer for each key is stored, then the
// connection closes without a word. The caller's resend, under the same
// key, gets the stored answer, even if the directory changed in between.
func TestHR_DroppedAnswerIsStored(t *testing.T) {
	hr := hrDirectory(t)
	hr.SetDropFirstAnswer(true)
	fresh := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}} // no net/http resend
	if _, _, _, err := hook(t, fresh, hr, "k-1", "user:ana"); err == nil {
		t.Fatal("the first answer was not dropped")
	}
	hr.SetManager("user:ana", "user:carl")
	if code, res, _, err := hook(t, fresh, hr, "k-1", "user:ana"); err != nil || code != 200 || res.Patch["manager"] != "user:bob" {
		t.Errorf("resend = %d %+v %v, want the stored bob", code, res, err)
	}
	if n := hr.DroppedAnswers(); n != 1 {
		t.Errorf("DroppedAnswers = %d, want 1", n)
	}
}

// The knobs: an outage is 503 and an injected error 500, each a transient
// problem, and neither is stored; the version set is the one answers name.
func TestHR_KnobsOnTheProtocol(t *testing.T) {
	hr := hrDirectory(t)
	c := http.DefaultClient
	hr.SetOutage(true)
	if code, _, prob, err := hook(t, c, hr, "k-1", "user:ana"); err != nil || code != http.StatusServiceUnavailable ||
		prob.Class != hookproto.ClassTransient {
		t.Errorf("outage = %d %+v %v, want 503 transient", code, prob, err)
	}
	hr.SetOutage(false)
	hr.SetErrorRate(1)
	if code, _, prob, err := hook(t, c, hr, "k-1", "user:ana"); err != nil || code != http.StatusInternalServerError ||
		prob.Class != hookproto.ClassTransient {
		t.Errorf("error rate 1 = %d %+v %v, want 500 transient", code, prob, err)
	}
	hr.SetErrorRate(0)
	hr.SetVersion("1.5.0")
	if code, res, _, err := hook(t, c, hr, "k-1", "user:ana"); err != nil || code != 200 || res.ConnectorVersion != "1.5.0" ||
		res.Patch["manager"] != "user:bob" {
		t.Errorf("after the failures = %d %+v %v, want 200 bob from 1.5.0", code, res, err)
	}
	if n := hr.Calls(); n != 3 {
		t.Errorf("Calls = %d, want 3", n)
	}
}

// Each server has its own secret; SetSecret makes two share one (a 1.4 and
// a 1.5 behind one connection). A request signed with another secret is
// 401.
func TestHR_Secret(t *testing.T) {
	a, b := hrDirectory(t), hrDirectory(t)
	if a.Secret() == "" || a.Secret() == b.Secret() {
		t.Fatalf("secrets %q and %q: want two different ones", a.Secret(), b.Secret())
	}
	if _, err := hookproto.ParseSecret(a.Secret()); err != nil {
		t.Errorf("Secret() = %q: %v", a.Secret(), err)
	}
	signedForA := a.Secret()
	b.SetSecret(signedForA)
	if code, _, _, err := hook(t, http.DefaultClient, b, "k-1", "user:ana"); err != nil || code != 200 {
		t.Errorf("b with a's secret = %d %v, want 200", code, err)
	}
	// hook signs with the server's current secret; give b another one
	// after signing to see the refusal.
	b.SetSecret(hookproto.GenerateSecret())
	secret, _ := hookproto.ParseSecret(signedForA)
	body := []byte(`{"protocol":"pli.hooks/v1"}`)
	r, _ := http.NewRequest(http.MethodPost, b.URL+hookproto.HooksPath, bytes.NewReader(body))
	hookproto.SetSignatureHeaders(r.Header, secret, "msg_1", time.Now(), body)
	res, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("signed with the old secret = %d, want 401", res.StatusCode)
	}
}
