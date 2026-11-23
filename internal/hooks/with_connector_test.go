// Tests for WithConnector (Ventoux item 5): the Go binding takes any
// Connector in this process, so a story's fakes stand in for every
// connector, compiled in or remote. It wins over both; its answers are held
// to the action's contract like every connector's (Ventoux V1); and it
// leaves the Connectors it was called on as they were.
package hooks_test

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/hookproto"
	"github.com/francoisnyc/work-kernel/internal/hooks"
)

// goFake answers every request with answer, and keeps the requests.
type goFake struct {
	mu     sync.Mutex
	got    []hookproto.Request
	answer hookproto.Response
	err    error
}

func (f *goFake) Handle(ctx context.Context, req hookproto.Request) (hookproto.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.got = append(f.got, req)
	if f.err != nil {
		return hookproto.Response{}, f.err
	}
	res := f.answer
	res.Protocol, res.RequestID = hookproto.Protocol, req.RequestID
	return res, nil
}

func (f *goFake) requests() []hookproto.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]hookproto.Request(nil), f.got...)
}

// A remote action (no code in the worker) runs through the fake: it gets
// the envelope, inputs by the contract's names and the hook's key, and its
// correction comes back as the hook's writes.
func TestWithConnector_RunsARemoteActionInProcess(t *testing.T) {
	fake := &goFake{answer: hookproto.Response{Outcome: hookproto.OutcomeCorrect,
		Patch: map[string]any{"manager": "user:bob"}}}
	c := hooks.NewConnectors("http://crm.invalid", "http://erp.invalid").WithConnector("hr@1", fake)

	res, err := c.RunCall(ctx(t), "hr.manager_of", "hook:item-1:2:hr_check",
		[]any{nil, "user:alice"}, hooks.Call{HookID: "hr_check", ItemID: "item-1", Kind: hookproto.KindCheck})
	if err != nil {
		t.Fatalf("RunCall: %v", err)
	}
	if res.Verdict != hooks.Correct || !reflect.DeepEqual(res.Values, []any{"user:bob"}) {
		t.Errorf("result = %+v, want correct [user:bob]", res)
	}
	got := fake.requests()
	if len(got) != 1 {
		t.Fatalf("the fake got %d requests, want 1", len(got))
	}
	req := got[0]
	if req.Kind != hookproto.KindCheck || req.Hook.Operation != "manager_of" || req.Hook.Connector != "hr@1" {
		t.Errorf("request = kind %s, %s %s; want a check of hr@1 manager_of", req.Kind, req.Hook.Connector, req.Hook.Operation)
	}
	if want := map[string]any{"onBehalfOf": nil, "createdBy": "user:alice"}; !reflect.DeepEqual(req.Inputs, want) {
		t.Errorf("inputs = %v, want %v", req.Inputs, want)
	}
	if req.IdempotencyKey != "hook:item-1:2:hr_check" || req.Item.ID != "item-1" {
		t.Errorf("key %q, item %q; want the hook's key and the Item", req.IdempotencyKey, req.Item.ID)
	}
}

// A connector compiled in (email@1) is replaced too: nothing is posted to
// the email service, and a field-less action answers ok.
func TestWithConnector_WinsOverCompiledInCode(t *testing.T) {
	fake := &goFake{answer: hookproto.Response{Outcome: hookproto.OutcomeOK}}
	c := hooks.NewConnectors("http://crm.invalid", "http://erp.invalid").
		WithEmail("http://email.invalid").WithConnector("email@1", fake)
	if _, err := c.RunCall(ctx(t), "email.notify", "hook:item-1:5:notify_approved", []any{"user:alice"},
		hooks.Call{HookID: "notify_approved", ItemID: "item-1"}); err != nil {
		t.Fatalf("RunCall: %v (the compiled-in code would have posted to email.invalid)", err)
	}
	if got := fake.requests(); len(got) != 1 || got[0].Inputs["to"] != "user:alice" {
		t.Errorf("the fake got %+v, want one request to user:alice", got)
	}
}

// It also wins over a remote connection configured for the same
// connector@major.
func TestWithConnector_WinsOverARemoteConnection(t *testing.T) {
	fake := &goFake{answer: hookproto.Response{Outcome: hookproto.OutcomeAllow}}
	c := hooks.NewConnectors("http://crm.invalid", "http://erp.invalid").
		WithConnection("hr@1", hooks.Connection{URL: "http://hr.invalid", Secret: "whsec_dGVzdA=="}).
		WithConnector("hr@1", fake)
	if _, err := c.RunCall(ctx(t), "hr.manager_of", "k", []any{nil, "user:alice"}, hooks.Call{}); err != nil {
		t.Fatalf("RunCall: %v (the remote connection would have been tried)", err)
	}
	if len(fake.requests()) != 1 {
		t.Error("the fake was not called")
	}
}

// Its answers are held to the contract: an output of the wrong type and a
// reason the contract does not declare are mismatches, permanent.
func TestWithConnector_AnswersAreHeldToTheContract(t *testing.T) {
	for name, answer := range map[string]hookproto.Response{
		"a manager that is not a principal": {Outcome: hookproto.OutcomeCorrect, Patch: map[string]any{"manager": 5}},
		"an undeclared reason":              {Outcome: hookproto.OutcomeDeny, Reason: &hookproto.Reason{Code: "on_vacation"}},
	} {
		t.Run(name, func(t *testing.T) {
			c := hooks.NewConnectors("http://crm.invalid", "http://erp.invalid").
				WithConnector("hr@1", &goFake{answer: answer})
			_, err := c.RunCall(ctx(t), "hr.manager_of", "k", []any{nil, "user:alice"}, hooks.Call{})
			if !errors.Is(err, hooks.ErrConnectorMismatch) || !errors.Is(err, hooks.ErrPermanent) {
				t.Errorf("error = %v, want ErrConnectorMismatch (permanent)", err)
			}
		})
	}
}

// A failure the fake returns is the call's: retryable stays retryable,
// permanent stays permanent.
func TestWithConnector_FailuresPassThrough(t *testing.T) {
	transient := errors.New("hr is down")
	c := hooks.NewConnectors("http://crm.invalid", "http://erp.invalid").WithConnector("hr@1", &goFake{err: transient})
	_, err := c.RunCall(ctx(t), "hr.manager_of", "k", []any{nil, "user:alice"}, hooks.Call{})
	if !errors.Is(err, transient) || errors.Is(err, hooks.ErrPermanent) {
		t.Errorf("transient: error = %v, want the fake's error, not permanent", err)
	}
	c = hooks.NewConnectors("http://crm.invalid", "http://erp.invalid").
		WithConnector("hr@1", &goFake{err: hooks.ErrPermanent})
	if _, err := c.RunCall(ctx(t), "hr.manager_of", "k", []any{nil, "user:alice"}, hooks.Call{}); !errors.Is(err, hooks.ErrPermanent) {
		t.Errorf("permanent: error = %v, want ErrPermanent", err)
	}
}

// WithConnector returns new Connectors: the ones it was called on still
// reach the connector as before (here: hr@1 unconfigured).
func TestWithConnector_LeavesTheOriginalAlone(t *testing.T) {
	base := hooks.NewConnectors("http://crm.invalid", "http://erp.invalid")
	_ = base.WithConnector("hr@1", &goFake{answer: hookproto.Response{Outcome: hookproto.OutcomeAllow}})
	if _, err := base.RunCall(ctx(t), "hr.manager_of", "k", []any{nil, "user:alice"}, hooks.Call{}); !errors.Is(err, hooks.ErrConnectorUnconfigured) {
		t.Errorf("the original: error = %v, want ErrConnectorUnconfigured", err)
	}
}
