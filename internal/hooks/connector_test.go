// Tests for the hook protocol spike: compiled-in connectors and remote ones
// look the same to a Type. Each is a hooks.Connector, whose request and
// response are the protocol's envelopes; a compiled-in action reads its
// inputs by its declared names (crm.get_plan's customerId), as a remote
// one does.
//
// And the connectors file: which connector@major is remote, at which URL,
// with which secret (from the environment). The worker re-reads it when it
// changes, so pointing hr@1 at another implementation (HR 1.4 to 1.5) takes
// effect on the next call, with no restart and no Type change.
package hooks_test

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/francoisnyc/work-kernel/internal/connstub"
	"github.com/francoisnyc/work-kernel/internal/hookproto"
	"github.com/francoisnyc/work-kernel/internal/hooks"
)

func envelope(kind hookproto.Kind, connector, operation string, inputs map[string]any) hookproto.Request {
	return hookproto.Request{
		Protocol: hookproto.Protocol, Kind: kind, RequestID: "req-1", IdempotencyKey: "hook:item-1:2:h", Attempt: 1,
		Hook:   hookproto.HookRef{Binding: "ticket@3#h", Connector: connector, Operation: operation},
		Event:  hookproto.Event{ID: "item-1:2", Type: hookproto.EventStateEntered, Subject: "item/item-1"},
		Item:   hookproto.ItemRef{ID: "item-1", Type: "ticket", TypeVersion: 3},
		Inputs: inputs, Trace: hookproto.Trace{CausationID: "item-1:2"},
		Connection: hookproto.ConnectionRef{Slot: strings.Split(connector, "@")[0], Ref: connector},
	}
}

func TestCompiledIn_IsAConnector(t *testing.T) {
	w := newWorld(t)
	crm, err := w.c.Connector("crm@1")
	if err != nil {
		t.Fatalf("Connector(crm@1): %v", err)
	}
	res, err := crm.Handle(ctx(t), envelope(hookproto.KindEnrich, "crm@1", "get_plan", map[string]any{"customerId": "cust-1"}))
	want := hookproto.Response{Protocol: hookproto.Protocol, RequestID: "req-1", Outcome: hookproto.OutcomeOK,
		Patch: map[string]any{"plan": "enterprise"}}
	if err != nil || !reflect.DeepEqual(res, want) {
		t.Errorf("crm@1 get_plan = %+v, %v; want %+v", res, err, want)
	}

	erp, err := w.c.Connector("erp@1")
	if err != nil {
		t.Fatalf("Connector(erp@1): %v", err)
	}
	req := envelope(hookproto.KindCheck, "erp@1", "check_credit", map[string]any{"customerId": "cust-1", "amount": float64(50)})
	req.Transition = &hookproto.Transition{From: "open", To: "approved"}
	if res, err := erp.Handle(ctx(t), req); err != nil || res.Outcome != hookproto.OutcomeAllow || res.RequestID != "req-1" {
		t.Errorf("erp@1 check_credit = %+v, %v; want allow", res, err)
	}
	// An input it does not declare, or one missing, is a bad request.
	if _, err := crm.Handle(ctx(t), envelope(hookproto.KindEnrich, "crm@1", "get_plan", map[string]any{"customer": "cust-1"})); !errors.Is(err, hooks.ErrPermanent) {
		t.Errorf("get_plan with an undeclared input: %v, want ErrPermanent", err)
	}
	if _, err := crm.Handle(ctx(t), envelope(hookproto.KindEnrich, "crm@1", "nope", map[string]any{"customerId": "cust-1"})); !errors.Is(err, hooks.ErrPermanent) {
		t.Errorf("an unknown operation: %v, want ErrPermanent", err)
	}
	// No other major is compiled in.
	if _, err := w.c.Connector("crm@2"); !errors.Is(err, hooks.ErrConnectorUnconfigured) {
		t.Errorf("Connector(crm@2) = %v, want ErrConnectorUnconfigured", err)
	}
}

// A configured remote connector is a Connector too, and Handle takes the
// same envelope.
func TestRemote_IsAConnector(t *testing.T) {
	f := newFake(t, hrDescriptor())
	hr, err := remoteHR(t, f).Connector("hr@1")
	if err != nil {
		t.Fatalf("Connector(hr@1): %v", err)
	}
	req := envelope(hookproto.KindCheck, "hr@1", "manager_of", map[string]any{"onBehalfOf": nil, "createdBy": "user:ana"})
	req.Transition = &hookproto.Transition{From: "draft", To: "submitted"}
	res, err := hr.Handle(ctx(t), req)
	if err != nil || res.Outcome != hookproto.OutcomeCorrect || res.Patch["manager"] != "user:bob" || res.RequestID != "req-1" {
		t.Errorf("hr@1 manager_of = %+v, %v; want correct user:bob", res, err)
	}
}

func writeConfig(t *testing.T, path, body string, mtime time.Time) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

func env(vars map[string]string) func(string) string {
	return func(name string) string { return vars[name] }
}

func TestLoadConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "connectors.yaml")
	secret := hookproto.GenerateSecret()
	writeConfig(t, path, `connectors:
  hr@1: {url: "http://hr.internal:8083", secret_env: HR_SECRET, ref: hr-directory}
  crm@2: {url: "https://crm.example", secret_env: CRM_SECRET}
`, time.Now())
	got, err := hooks.LoadConfig(path, env(map[string]string{"HR_SECRET": secret, "CRM_SECRET": secret}))
	want := hooks.Config{Connectors: map[string]hooks.Connection{
		"hr@1":  {URL: "http://hr.internal:8083", Secret: secret, Ref: "hr-directory"},
		"crm@2": {URL: "https://crm.example", Secret: secret, Ref: "crm@2"}, // no ref: the key
	}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("LoadConfig = %+v, %v; want %+v", got, err, want)
	}
}

func TestLoadConfig_Refuses(t *testing.T) {
	secret := hookproto.GenerateSecret()
	vars := map[string]string{"S": secret, "BAD": "not-a-secret"}
	cases := map[string]string{
		"not YAML":                     "connectors: [",
		"an unknown key":               "connectors: {}\nextra: 1\n",
		"an unknown entry key":         "connectors:\n  hr@1: {url: 'http://h', secret_env: S, timeout: 3s}\n",
		"no major":                     "connectors:\n  hr: {url: 'http://h', secret_env: S}\n",
		"a major that is not a number": "connectors:\n  hr@x: {url: 'http://h', secret_env: S}\n",
		"major 0":                      "connectors:\n  hr@0: {url: 'http://h', secret_env: S}\n",
		"an action, not a connector":   "connectors:\n  hr.manager_of@1: {url: 'http://h', secret_env: S}\n",
		"no url":                       "connectors:\n  hr@1: {secret_env: S}\n",
		"a url that is not http":       "connectors:\n  hr@1: {url: 'ftp://h', secret_env: S}\n",
		"no secret_env":                "connectors:\n  hr@1: {url: 'http://h'}\n",
		"an unset secret_env":          "connectors:\n  hr@1: {url: 'http://h', secret_env: UNSET}\n",
		"a malformed secret":           "connectors:\n  hr@1: {url: 'http://h', secret_env: BAD}\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "connectors.yaml")
			writeConfig(t, path, body, time.Now())
			if _, err := hooks.LoadConfig(path, env(vars)); err == nil || !strings.Contains(err.Error(), path) {
				t.Errorf("LoadConfig(%q) = %v, want an error naming the file", body, err)
			}
		})
	}
	if _, err := hooks.LoadConfig(filepath.Join(t.TempDir(), "missing.yaml"), env(vars)); err == nil {
		t.Error("a missing file was accepted")
	}
}

// The stack's file: hr@1 is the HR directory in connstub, signed with
// HR_HOOK_SECRET.
func TestLoadConfig_TheStacksFile(t *testing.T) {
	secret := hookproto.GenerateSecret()
	got, err := hooks.LoadConfig("../../deploy/connectors.yaml", env(map[string]string{"HR_HOOK_SECRET": secret}))
	want := hooks.Config{Connectors: map[string]hooks.Connection{
		"hr@1": {URL: "http://connstub:8083", Secret: secret, Ref: "hr-directory"},
	}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("deploy/connectors.yaml = %+v, %v; want %+v", got, err, want)
	}
}

// HR 1.4 and 1.5 behind one connection: the file points hr@1 at 1.4, then
// at 1.5. The next call goes to 1.5, with the same Connectors (no
// restart). A file that no longer parses keeps the last good one.
func TestWithConfigFile_FollowsTheFile(t *testing.T) {
	hr14, hr15 := connstub.NewHR(), connstub.NewHR()
	t.Cleanup(hr14.Close)
	t.Cleanup(hr15.Close)
	hr14.SetVersion("1.4.0")
	hr15.SetVersion("1.5.0")
	hr15.SetSecret(hr14.Secret())
	hr14.SetManager("user:ana", "user:bob")
	hr15.SetManager("user:ana", "user:carl") // a different answer, to see who answered
	t.Setenv("TEST_HR_HOOK_SECRET", hr14.Secret())

	path := filepath.Join(t.TempDir(), "connectors.yaml")
	pointAt := func(url string, at time.Time) {
		writeConfig(t, path, "connectors:\n  hr@1: {url: '"+url+"', secret_env: TEST_HR_HOOK_SECRET, ref: hr-directory}\n", at)
	}
	start := time.Now().Add(-time.Hour)
	pointAt(hr14.URL, start)
	c, err := newWorld(t).c.WithConfigFile(path)
	if err != nil {
		t.Fatalf("WithConfigFile: %v", err)
	}
	ask := func() any {
		t.Helper()
		res, err := c.RunCall(ctx(t), "hr.manager_of", "hold:"+time.Now().Format("150405.000000000"),
			[]any{nil, "user:ana"}, hrCall())
		if err != nil {
			t.Fatalf("RunCall: %v", err)
		}
		return res.Values[0]
	}
	if got := ask(); got != "user:bob" {
		t.Fatalf("with hr@1 at 1.4: %v, want user:bob", got)
	}

	pointAt(hr15.URL, start.Add(time.Minute))
	if got := ask(); got != "user:carl" {
		t.Errorf("after pointing hr@1 at 1.5: %v, want user:carl", got)
	}
	calls14 := hr14.Calls()

	writeConfig(t, path, "connectors: [", start.Add(2*time.Minute))
	if got := ask(); got != "user:carl" {
		t.Errorf("after the file broke: %v, want user:carl (the last good file)", got)
	}
	if hr14.Calls() != calls14 {
		t.Errorf("1.4 was called after the switch")
	}

	if _, err := newWorld(t).c.WithConfigFile(filepath.Join(t.TempDir(), "missing.yaml")); err == nil {
		t.Error("WithConfigFile accepted a missing file")
	}
}
