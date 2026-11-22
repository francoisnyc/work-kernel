// Tests for the conformance test itself: it passes a conformant connector
// (connstub's HR directory) and catches each way a connector can break the
// protocol. Each broken connector is a proxy in front of the HR directory
// that breaks one rule.
package conformance_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/francoisnyc/work-kernel/internal/connstub"
	"github.com/francoisnyc/work-kernel/internal/hookproto"
	"github.com/francoisnyc/work-kernel/internal/hookproto/conformance"
)

func correct(manager string) *hookproto.Response {
	return &hookproto.Response{Outcome: hookproto.OutcomeCorrect, Patch: map[string]any{"manager": manager}}
}

func deny(code string) *hookproto.Response {
	return &hookproto.Response{Outcome: hookproto.OutcomeDeny, Reason: &hookproto.Reason{Code: code}}
}

func check(name string, onBehalfOf, createdBy any, want *hookproto.Response) conformance.Case {
	return conformance.Case{Name: name, Kind: hookproto.KindCheck, Operation: "manager_of",
		Inputs: map[string]any{"onBehalfOf": onBehalfOf, "createdBy": createdBy}, Want: want}
}

func hrCases() []conformance.Case {
	return []conformance.Case{
		check("ana's own request", nil, "user:ana", correct("user:bob")),
		check("bob's request", nil, "user:bob", correct("user:dana")),
		check("dana manages herself", nil, "user:dana", deny("no_eligible_approver")),
		check("erin is unknown", nil, "user:erin", deny("unknown_employee")),
	}
}

func newHR(t *testing.T) *connstub.Server {
	t.Helper()
	hr := connstub.NewHR()
	t.Cleanup(hr.Close)
	hr.SetManager("user:ana", "user:bob")
	hr.SetManager("user:bob", "user:dana")
	hr.SetManager("user:dana", "user:dana")
	return hr
}

func TestCheck_AConformantConnectorPasses(t *testing.T) {
	hr := newHR(t)
	problems := conformance.Check(conformance.Target{URL: hr.URL, Secret: hr.Secret(), Cases: hrCases(), Outage: hr.SetOutage})
	if len(problems) != 0 {
		t.Errorf("the HR directory: %d problems:\n%s", len(problems), strings.Join(problems, "\n"))
	}
}

// proxy stands in front of the HR directory. rewrite may change a POST's
// body and headers before it is forwarded (re-signing it if it likes), and
// answer may change the answer or answer itself (ok false: not forwarded).
type proxy struct {
	hr      *connstub.Server
	secret  hookproto.Secret
	rewrite func(body []byte, h http.Header) ([]byte, http.Header)
	answer  func(req []byte, status int, body []byte) (int, []byte)
	before  func(w http.ResponseWriter, req []byte) bool // answers itself when true
}

func (p *proxy) serve(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.Method == http.MethodPost && p.before != nil && p.before(w, body) {
			return
		}
		h := r.Header.Clone()
		if r.Method == http.MethodPost && p.rewrite != nil {
			body, h = p.rewrite(body, h)
		}
		out, err := http.NewRequest(r.Method, p.hr.URL+r.URL.Path, bytes.NewReader(body))
		if err != nil {
			t.Errorf("proxy: %v", err)
			return
		}
		out.Header = h
		res, err := http.DefaultClient.Do(out)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer res.Body.Close()
		data, _ := io.ReadAll(res.Body)
		status := res.StatusCode
		if r.Method == http.MethodPost && p.answer != nil {
			status, data = p.answer(body, status, data)
		}
		w.Header().Set("Content-Type", res.Header.Get("Content-Type"))
		w.WriteHeader(status)
		_, _ = w.Write(data)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// resign signs body afresh with the HR directory's secret.
func (p *proxy) resign(body []byte, h http.Header) http.Header {
	out := h.Clone()
	hookproto.SetSignatureHeaders(out, p.secret, h.Get(hookproto.HeaderID), time.Now(), body)
	return out
}

func editJSON(body []byte, edit func(m map[string]any)) []byte {
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return body
	}
	edit(m)
	out, _ := json.Marshal(m)
	return out
}

func TestCheck_CatchesBrokenConnectors(t *testing.T) {
	cases := []struct {
		name string
		// breakIt breaks the proxy, and returns its own outage switch when
		// it fails in front of the HR directory (nil: the directory's).
		// The problems must mention want.
		breakIt func(p *proxy) func(bool)
		want    string
	}{
		{"it accepts unsigned requests", func(p *proxy) func(bool) {
			p.rewrite = func(body []byte, h http.Header) ([]byte, http.Header) {
				if h.Get(hookproto.HeaderID) == "" {
					h.Set(hookproto.HeaderID, "msg_proxy")
				}
				return body, p.resign(body, h)
			}
			return nil
		}, "unsigned"},
		{"it echoes another request ID", func(p *proxy) func(bool) {
			p.answer = func(_ []byte, status int, body []byte) (int, []byte) {
				if status != http.StatusOK {
					return status, body
				}
				return status, editJSON(body, func(m map[string]any) { m["requestId"] = "someone-else" })
			}
			return nil
		}, "requestId"},
		{"it answers a deny reason it does not declare", func(p *proxy) func(bool) {
			p.answer = func(_ []byte, status int, body []byte) (int, []byte) {
				return status, bytes.ReplaceAll(body, []byte(`"unknown_employee"`), []byte(`"made_up"`))
			}
			return nil
		}, "declared errors"},
		{"it ignores idempotency keys", func(p *proxy) func(bool) {
			var n atomic.Int64
			p.rewrite = func(body []byte, h http.Header) ([]byte, http.Header) {
				key := fmt.Sprintf("fresh-%d", n.Add(1))
				body = editJSON(body, func(m map[string]any) { m["idempotencyKey"] = key })
				return body, p.resign(body, h)
			}
			return nil
		}, "422"},
		{"it stores its failures", func(p *proxy) func(bool) {
			failed := map[string]bool{}
			var mu sync.Mutex
			down := false
			p.before = func(w http.ResponseWriter, req []byte) bool {
				var r hookproto.Request
				_ = json.Unmarshal(req, &r)
				mu.Lock()
				defer mu.Unlock()
				if down {
					failed[r.IdempotencyKey] = true
					w.Header().Set("Content-Type", hookproto.ProblemContentType)
					w.WriteHeader(http.StatusServiceUnavailable)
					_ = json.NewEncoder(w).Encode(hookproto.Problem{Title: "down", Status: 503, Class: hookproto.ClassTransient})
					return true
				}
				if failed[r.IdempotencyKey] {
					w.Header().Set("Content-Type", hookproto.ProblemContentType)
					w.WriteHeader(http.StatusUnprocessableEntity)
					_ = json.NewEncoder(w).Encode(hookproto.Problem{Title: "conflict", Status: 422, Class: hookproto.ClassKeyConflict})
					return true
				}
				return false
			}
			return func(on bool) { mu.Lock(); down = on; mu.Unlock() }
		}, "must not be stored"},
		{"its refusals are not problem+json", func(p *proxy) func(bool) {
			p.answer = func(_ []byte, status int, body []byte) (int, []byte) {
				if status == http.StatusOK {
					return status, body
				}
				return status, []byte("nope")
			}
			return nil
		}, "problem"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			hr := newHR(t)
			s, err := hookproto.ParseSecret(hr.Secret())
			if err != nil {
				t.Fatalf("the HR directory's secret: %v", err)
			}
			p := &proxy{hr: hr, secret: s}
			outage := c.breakIt(p)
			if outage == nil {
				outage = hr.SetOutage
			}
			srv := p.serve(t)
			problems := conformance.Check(conformance.Target{URL: srv.URL, Secret: hr.Secret(), Cases: hrCases(), Outage: outage})
			all := strings.Join(problems, "\n")
			if !strings.Contains(all, c.want) {
				t.Errorf("problems do not mention %q:\n%s", c.want, all)
			}
		})
	}
}

// A target without the cases the idempotency checks need, or without an
// outage switch, is itself a problem: the suite never passes by checking
// less.
func TestCheck_RefusesATargetItCannotFullyCheck(t *testing.T) {
	hr := newHR(t)
	one := hrCases()[:1]
	if problems := conformance.Check(conformance.Target{URL: hr.URL, Secret: hr.Secret(), Cases: one, Outage: hr.SetOutage}); !strings.Contains(strings.Join(problems, "\n"), "two answered") {
		t.Errorf("one case: problems %v, want one about two answered cases", problems)
	}
	if problems := conformance.Check(conformance.Target{URL: hr.URL, Secret: hr.Secret(), Cases: hrCases()}); !strings.Contains(strings.Join(problems, "\n"), "Outage") {
		t.Errorf("no outage switch: problems %v, want one about Outage", problems)
	}
}
