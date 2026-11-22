// End-to-end tests for proof-of-concept Step 2, part 1: the vacation
// request's HR check, run by the real worker against the stub HR directory,
// through the relay, on Postgres and a Temporal dev server.
//
// Submitting holds the request; the worker reads who it is for
// (created_on_behalf_of, else created_by) through HookInput, asks HR, and
// resolves: correct with f_approver = the manager (applied, the Item enters
// submitted with it), or deny unknown_employee / no_eligible_approver.
package workflow_test

import (
	"testing"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/connstub"
	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
)

// hrStack runs the worker with an HR directory. Each test names its own
// people, so tests never share a manager.
type hrStack struct {
	*stack
	crm, erp, hr *connstub.Server
}

func newHRStack(t *testing.T) *hrStack {
	t.Helper()
	return newHRStackWith(t, testBudget, (*hrStack).remoteHR)
}

func user(name string) item.Principal {
	return item.Principal{Actor: "user:" + name + "-" + uuid.NewString()[:8]}
}

func (h *hrStack) request(p item.Principal) uuid.UUID {
	h.t.Helper()
	res, err := h.k.Create(serverCtx(h.t), itemservice.CreateRequest{
		IdempotencyKey: "key-" + uuid.NewString(), Principal: p, TypeID: "vacation_request", TypeVersion: 1,
		Fields: map[string]any{"f_start": "2099-07-01", "f_end": "2099-07-05", "f_days": 5, "f_leave_type": "annual"},
	})
	if err != nil {
		h.t.Fatalf("create vacation request: %v", err)
	}
	return res.ItemID
}

func (h *hrStack) submit(p item.Principal, id uuid.UUID) itemservice.TransitionResult {
	h.t.Helper()
	res, err := h.k.Transition(serverCtx(h.t), itemservice.TransitionRequest{
		IdempotencyKey: "key-" + uuid.NewString(), Principal: p, ItemID: id, ToState: "submitted",
		Wait: itemservice.MaxWait,
	})
	if err != nil {
		h.t.Fatalf("submit: %v", err)
	}
	return res
}

func (h *hrStack) item(p item.Principal, id uuid.UUID) item.Item {
	h.t.Helper()
	it, err := h.k.Get(serverCtx(h.t), p, id)
	if err != nil {
		h.t.Fatalf("Get: %v", err)
	}
	return it
}

// The HR check names the requester's manager; the Item enters submitted
// with f_approver set, in the same write, by the hook, for the requester.
func TestE2E_HRCheck_SetsTheApprover(t *testing.T) {
	h := newHRStack(t)
	ana, bob := user("ana"), user("bob")
	h.hr.SetManager(ana.Actor, bob.Actor)
	id := h.request(ana)

	res := h.submit(ana, id)
	if res.Outcome != itemservice.OutcomeApplied {
		t.Fatalf("submit with wait = %+v, want applied", res)
	}
	it := h.item(ana, id)
	if it.State != "submitted" || it.Fields["f_approver"] != bob.Actor {
		t.Errorf("after HR: state %s, f_approver %v; want submitted, %s", it.State, it.Fields["f_approver"], bob.Actor)
	}
	ev := hookEvent(t, id, "hook:vacation_request:hr_check")
	if ev.OnBehalfOf != ana.Actor || ev.OriginatingHook != "hook:vacation_request@v1:hr_check" || ev.Hop != 1 {
		t.Errorf("HR's event = %+v; want on behalf of %s, hop 1", ev, ana.Actor)
	}
}

// An agent's request for ana: HR is asked about ana, not the agent.
func TestE2E_HRCheck_ReadsWhoTheRequestIsFor(t *testing.T) {
	h := newHRStack(t)
	ana, bob := user("ana"), user("bob")
	h.hr.SetManager(ana.Actor, bob.Actor)
	agent := item.Principal{Actor: "agent:" + uuid.NewString()[:8], OnBehalfOf: ana.Actor}
	all := []uuid.UUID{uuid.Nil}
	powers := []string{item.PowerRead, item.PowerCreate, item.PowerEditFields, item.PowerTransitionAll}
	session, err := h.k.StartAgentSession(serverCtx(t), itemservice.StartAgentSessionRequest{
		Principal: agent, Scopes: all, Powers: powers, Grant: &itemservice.Grant{Scopes: all, Powers: powers},
	})
	if err != nil {
		t.Fatalf("StartAgentSession: %v", err)
	}
	agent.AgentSession = session
	id := h.request(agent)

	if res := h.submit(agent, id); res.Outcome != itemservice.OutcomeApplied {
		t.Fatalf("submit = %+v, want applied", res)
	}
	if got := h.item(ana, id).Fields["f_approver"]; got != bob.Actor {
		t.Errorf("f_approver = %v, want ana's manager %s", got, bob.Actor)
	}
}

// HR's refusals are named outcomes: denied, with the reason code, and the
// request stays a draft without an approver.
func TestE2E_HRCheck_Denials(t *testing.T) {
	h := newHRStack(t)
	dana, erin := user("dana"), user("erin")
	h.hr.SetManager(dana.Actor, dana.Actor) // manages herself
	for _, c := range []struct {
		who    item.Principal
		reason string
	}{{dana, "no_eligible_approver"}, {erin, "unknown_employee"}} {
		id := h.request(c.who)
		res := h.submit(c.who, id)
		if res.Outcome != itemservice.OutcomeDenied || res.Reason != c.reason {
			t.Errorf("%s submits = %+v, want denied %s", c.who.Actor, res, c.reason)
		}
		if it := h.item(c.who, id); it.State != "draft" || it.Fields["f_approver"] != nil {
			t.Errorf("%s: state %s, f_approver %v; want draft, none", c.who.Actor, it.State, it.Fields["f_approver"])
		}
	}
}

// An HR outage longer than the hook's budget resolves as error; the
// request stays a draft and may be submitted again.
func TestE2E_HRCheck_OutageIsAnError(t *testing.T) {
	h := newHRStack(t)
	ana, bob := user("ana"), user("bob")
	h.hr.SetManager(ana.Actor, bob.Actor)
	h.hr.SetOutage(true)
	id := h.request(ana)
	res := h.submit(ana, id)
	eventually(t, 3*testBudget, "the HR check gives up", func() bool {
		_, hold := h.hold(ana, id)
		return hold != nil && hold.ID == res.HoldID && hold.Status == item.HoldError
	})
	h.hr.SetOutage(false)
	if res := h.submit(ana, id); res.Outcome != itemservice.OutcomeApplied {
		t.Errorf("submit after the outage = %+v, want applied", res)
	}
}

func (h *hrStack) hold(p item.Principal, id uuid.UUID) (string, *item.Hold) {
	it := h.item(p, id)
	return it.State, it.Hold
}
