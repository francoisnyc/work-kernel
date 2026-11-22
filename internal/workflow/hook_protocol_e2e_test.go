// End-to-end tests for the hook protocol spike: the vacation request's HR
// check against the HR directory as a remote pli.hooks/v1 connector, run by
// the real worker through the relay, on Postgres and a Temporal dev server.
// The Type is unchanged: its hr.manager_of is hr@1, which configuration
// (not code) points at a URL.
//
//   - Retries within the budget: an HR outage shorter than the budget ends
//     with the approver set, as if nothing happened.
//   - A misconfigured connector fails the check at once, its hold naming
//     the cause: connector_unconfigured, connector_unauthorized or
//     connector_mismatch.
//   - Switching implementations: while a request waits in its hold, between
//     the check's retries, the connectors file points hr@1 at HR 1.5
//     instead of 1.4; the next attempt asks 1.5. Same worker, same Type.
package workflow_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/connstub"
	"github.com/francoisnyc/work-kernel/internal/hookproto"
	"github.com/francoisnyc/work-kernel/internal/hooks"
	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
	"github.com/francoisnyc/work-kernel/internal/typedef"
	wf "github.com/francoisnyc/work-kernel/internal/workflow"
)

// remoteHR is the stack's connectors with hr@1 at its HR directory, signed
// with the directory's secret: how the HR check is reached since the hook
// protocol.
func (h *hrStack) remoteHR() *hooks.Connectors {
	return hooks.NewConnectors(h.crm.URL, h.erp.URL).
		WithConnection("hr@1", hooks.Connection{URL: h.hr.URL, Secret: h.hr.Secret(), Ref: "hr-directory"})
}

// newHRStackWith is newHRStack with the hook budget and the worker's
// connectors chosen by the test. connectors runs once the stubs exist.
func newHRStackWith(t *testing.T, budget time.Duration, connectors func(h *hrStack) *hooks.Connectors) *hrStack {
	t.Helper()
	if _, err := pool.Exec(serverCtx(t), `update outbox set delivered_at = now() where delivered_at is null`); err != nil {
		t.Fatalf("clear outbox: %v", err)
	}
	data, err := os.ReadFile("../../types/vacation_request.v1.yaml")
	if err != nil {
		t.Fatalf("read vacation_request v1: %v", err)
	}
	ty, err := typedef.Parse(data)
	if err != nil {
		t.Fatalf("parse vacation_request v1: %v", err)
	}
	if err := itemservice.New(pool).RegisterType(serverCtx(t), ty); err != nil {
		t.Fatalf("register vacation_request v1: %v", err)
	}
	h := &hrStack{
		stack: &stack{t: t, k: itemservice.New(pool), queue: "items-" + uuid.NewString()},
		crm:   connstub.NewCRM(), erp: connstub.NewERP(), hr: connstub.NewHR(),
	}
	h.worker = wf.NewWorker(tc, h.queue, h.k, wf.WorkerOptions{Connectors: connectors(h), HookBudget: budget})
	if err := h.worker.Start(); err != nil {
		t.Fatalf("start worker: %v", err)
	}
	h.startRelay()
	t.Cleanup(func() {
		h.stopRelay()
		h.stopWorker()
		h.crm.Close()
		h.erp.Close()
		h.hr.Close()
	})
	return h
}

// ask asks for the submit without waiting: the hold opens.
func (h *hrStack) ask(p item.Principal, id uuid.UUID) itemservice.TransitionResult {
	h.t.Helper()
	res, err := h.k.Transition(serverCtx(h.t), itemservice.TransitionRequest{
		IdempotencyKey: "key-" + uuid.NewString(), Principal: p, ItemID: id, ToState: "submitted",
	})
	if err != nil || res.Outcome != itemservice.OutcomePending {
		h.t.Fatalf("submit = %+v, %v; want pending", res, err)
	}
	return res
}

// An HR outage shorter than the budget: the check retries and the request
// enters submitted with ana's manager.
func TestE2E_HRCheck_RetriesWithinTheBudget(t *testing.T) {
	h := newHRStackWith(t, 10*time.Second, (*hrStack).remoteHR)
	ana, bob := user("ana"), user("bob")
	h.hr.SetManager(ana.Actor, bob.Actor)
	h.hr.SetOutage(true)
	id := h.request(ana)
	h.ask(ana, id)
	eventually(t, 5*time.Second, "the first attempt reaches HR", func() bool { return h.hr.Calls() >= 1 })
	h.hr.SetOutage(false)
	eventually(t, 10*time.Second, "the check succeeds on a retry", func() bool {
		it := h.item(ana, id)
		return it.State == "submitted"
	})
	it := h.item(ana, id)
	if it.Fields["f_approver"] != bob.Actor || it.Hold == nil || it.Hold.Status != item.HoldAllowed {
		t.Errorf("after the outage: f_approver %v, hold %+v; want %s, allowed", it.Fields["f_approver"], it.Hold, bob.Actor)
	}
	if n := h.hr.Calls(); n < 2 {
		t.Errorf("HR calls = %d, want at least 2 (one failed, one answered)", n)
	}
}

// A misconfigured connector: the hold closes as error at once, naming the
// cause, well before the budget (10 s) would run out. The request stays a
// draft.
func TestE2E_HRCheck_ConnectorFaults(t *testing.T) {
	cases := []struct {
		reason     string
		connectors func(h *hrStack) *hooks.Connectors
	}{
		{wf.ReasonConnectorUnconfigured, func(h *hrStack) *hooks.Connectors {
			return hooks.NewConnectors(h.crm.URL, h.erp.URL) // no hr@1
		}},
		{wf.ReasonConnectorUnauthorized, func(h *hrStack) *hooks.Connectors {
			return hooks.NewConnectors(h.crm.URL, h.erp.URL).
				WithConnection("hr@1", hooks.Connection{URL: h.hr.URL, Secret: hookproto.GenerateSecret()})
		}},
		{wf.ReasonConnectorMismatch, func(h *hrStack) *hooks.Connectors {
			h.hr.SetVersion("2.0.0") // HR 2 behind the hr@1 connection
			return h.remoteHR()
		}},
	}
	const budget = 10 * time.Second
	for _, c := range cases {
		t.Run(c.reason, func(t *testing.T) {
			h := newHRStackWith(t, budget, c.connectors)
			ana, bob := user("ana"), user("bob")
			h.hr.SetManager(ana.Actor, bob.Actor)
			id := h.request(ana)
			asked := time.Now()
			res := h.ask(ana, id)
			eventually(t, budget, "the hold closes", func() bool {
				_, hold := h.hold(ana, id)
				return hold != nil && hold.ID == res.HoldID && hold.Status != item.HoldOpen
			})
			took := time.Since(asked)
			state, hold := h.hold(ana, id)
			if state != "draft" || hold.Status != item.HoldError || hold.Reason != c.reason {
				t.Errorf("state %s, hold %+v; want draft, error %s", state, hold, c.reason)
			}
			if took > budget/2 {
				t.Errorf("the hold closed after %v; want at once, not after the budget (%v)", took, budget)
			}
		})
	}
}

// HR 1.4 and HR 1.5, behind one connection (one secret). 1.4 is down; ana
// submits; while her request waits in its hold, the connectors file is
// pointed at 1.5. The check's next attempt asks 1.5, whose answer (carl)
// is the one written. No worker restart, no Type change.
func TestE2E_HRCheck_SwitchesImplementationWhileHeld(t *testing.T) {
	path := filepath.Join(t.TempDir(), "connectors.yaml")
	pointAt := func(url string, at time.Time) {
		t.Helper()
		body := "connectors:\n  hr@1: {url: '" + url + "', secret_env: TEST_HR_HOOK_SECRET, ref: hr-directory}\n"
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatal(err)
		}
	}
	hr15 := connstub.NewHR()
	t.Cleanup(hr15.Close)
	hr15.SetVersion("1.5.0")
	start := time.Now().Add(-time.Hour)

	h := newHRStackWith(t, 20*time.Second, func(h *hrStack) *hooks.Connectors {
		h.hr.SetVersion("1.4.0")
		hr15.SetSecret(h.hr.Secret())
		t.Setenv("TEST_HR_HOOK_SECRET", h.hr.Secret())
		pointAt(h.hr.URL, start)
		c, err := hooks.NewConnectors(h.crm.URL, h.erp.URL).WithConfigFile(path)
		if err != nil {
			t.Fatalf("WithConfigFile: %v", err)
		}
		return c
	})
	worker := h.worker
	ana, bob, carl := user("ana"), user("bob"), user("carl")
	h.hr.SetManager(ana.Actor, bob.Actor)
	hr15.SetManager(ana.Actor, carl.Actor)
	h.hr.SetOutage(true)

	id := h.request(ana)
	res := h.ask(ana, id)
	eventually(t, 5*time.Second, "1.4 is asked and fails", func() bool { return h.hr.Calls() >= 1 })
	pointAt(hr15.URL, start.Add(time.Minute))
	calls14 := h.hr.Calls()

	eventually(t, 15*time.Second, "the request enters submitted", func() bool { return h.item(ana, id).State == "submitted" })
	it := h.item(ana, id)
	if it.Fields["f_approver"] != carl.Actor || it.Hold == nil || it.Hold.ID != res.HoldID || it.Hold.Status != item.HoldAllowed {
		t.Errorf("f_approver %v, hold %+v; want %s (HR 1.5's answer), the same hold allowed", it.Fields["f_approver"], it.Hold, carl.Actor)
	}
	if hr15.Calls() < 1 {
		t.Error("HR 1.5 was never asked")
	}
	if h.hr.Calls() > calls14+1 { // at most one attempt already on its way to 1.4
		t.Errorf("HR 1.4 asked %d times after the switch", h.hr.Calls()-calls14)
	}
	if h.worker != worker {
		t.Error("the worker was replaced")
	}
}
