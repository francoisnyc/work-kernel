// Tests for the hook protocol spike: Internal().HookActor, the envelope's
// actor. It is who caused the event a hook runs for: that event's actor,
// on_behalf_of and agent_session, principal IDs only. Like HookInput, only
// the hook itself asks, for an event of its own Item, and the Item goes
// through policy.Project first. (A kernel change, counted in the journal.)
package itemservice_test

import (
	"testing"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
)

func TestHookActor_WhoCausedTheEvent(t *testing.T) {
	w := newVacWorld(t)
	actor := func(id uuid.UUID, seq int64) itemservice.HookActor {
		t.Helper()
		got, err := w.k.Internal().HookActor(testCtx(t), itemservice.HookInputRequest{
			Principal: hrCheck, ItemID: id, Attribution: hrCause(id, seq),
		})
		if err != nil {
			t.Fatalf("HookActor: %v", err)
		}
		return got
	}

	// ana submits her own request.
	id := w.create(t, w.ana, vacTerms())
	req := w.submit(t, w.ana, id)
	if got := actor(id, req.ItemSeq); got != (itemservice.HookActor{Principal: w.ana.Actor}) {
		t.Errorf("ana's submit: HookActor = %+v, want ana alone", got)
	}

	// An agent creates ana's request and submits it for her.
	agent := withSession(t, w.k, item.Principal{Actor: "agent:" + uuid.NewString()[:8], OnBehalfOf: w.ana.Actor})
	id = w.create(t, agent, vacTerms())
	req = w.submit(t, agent, id)
	want := itemservice.HookActor{Principal: agent.Actor, OnBehalfOf: w.ana.Actor, AgentSession: agent.AgentSession}
	if got := actor(id, req.ItemSeq); got != want {
		t.Errorf("the agent's submit: HookActor = %+v, want %+v", got, want)
	}

	// The actor is the cause event's, not the Item's creator's: the agent
	// creates ana's request, ana submits it herself.
	id = w.create(t, agent, vacTerms())
	req = w.submit(t, w.ana, id)
	if got := actor(id, req.ItemSeq); got != (itemservice.HookActor{Principal: w.ana.Actor}) {
		t.Errorf("ana's submit of the agent's draft: HookActor = %+v, want ana alone", got)
	}
	if got := actor(id, 1); got != want {
		t.Errorf("the same Item's creation: HookActor = %+v, want %+v", got, want)
	}
}

// The same refusals as HookInput: only the Item's own declared hook asks,
// for an event that exists.
func TestHookActor_Refusals(t *testing.T) {
	w := newVacWorld(t)
	id := w.create(t, w.ana, vacTerms())
	req := w.submit(t, w.ana, id)
	for name, p := range map[string]item.Principal{
		"the timer":          {Actor: "system:timer"},
		"an undeclared hook": {Actor: "hook:vacation_request:nope"},
		"a person":           w.ana,
	} {
		_, err := w.k.Internal().HookActor(testCtx(t), itemservice.HookInputRequest{
			Principal: p, ItemID: id, Attribution: hrCause(id, req.ItemSeq),
		})
		assertInvalid(t, "HookActor as "+name, err)
	}
	_, err := w.k.Internal().HookActor(testCtx(t), itemservice.HookInputRequest{
		Principal: hrCheck, ItemID: id, Attribution: hrCause(id, 99),
	})
	assertInvalid(t, "HookActor caused by an event that does not exist", err)
}
