// Tests for Step 6, part 1: protect internal principals.
//
// The item service has two doors. The public operations (Create, Update,
// Transition, Get, Search, StartAgentSession) serve people and agents: they
// refuse a system: or hook: actor, and any of originating_hook, causation_id
// or hop. correlation_id stays allowed, as a claim. The internal door,
// Service.Internal(), serves the kernel's own actors (the timer, hooks): it
// refuses people and agents, requires a cause, skips visibility, and still
// applies `allowed`. Which code may call Internal() is checked by
// internal_door_test.go.
package itemservice_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// intakeType has one transition only a hook may make, one only people with
// a role may make, and one anyone may make.
const intakeType = `
id: intake
version: 1
initial_state: triage
fields:
  # Open to agents: an agent creates an intake in these tests (fields are
  # closed to agents by default).
  - {id: f_title, type: string, writable_by: [people, agents]}
  - {id: f_plan,  type: string}
states:
  - {id: triage,   category: triage}
  - {id: approved, category: todo}
  - {id: denied,   category: canceled}
  - {id: parked,   category: backlog}
transitions:
  - {from: triage, to: approved, allowed: ['principal:hook:intake:erp_check']}
  - {from: triage, to: denied,   allowed: ['role:support']}
  - {from: triage, to: parked}
`

var (
	erpCheck = item.Principal{Actor: "hook:intake:erp_check"}
	crmPlan  = item.Principal{Actor: "hook:intake:crm_plan"}
	timer    = item.Principal{Actor: "system:timer"}
)

// createIntake registers the intake Type (a no-op after the first time) and
// creates one Item of it as p.
func createIntake(t *testing.T, k *itemservice.Service, p item.Principal, narrow *itemservice.Narrow) uuid.UUID {
	t.Helper()
	ty, err := typedef.Parse([]byte(intakeType))
	if err != nil {
		t.Fatalf("parse intake Type: %v", err)
	}
	if err := k.RegisterType(testCtx(t), ty); err != nil {
		t.Fatalf("register intake Type: %v", err)
	}
	res, err := k.Create(testCtx(t), itemservice.CreateRequest{
		IdempotencyKey: newKey(), Principal: p, TypeID: "intake", TypeVersion: 1,
		Fields: map[string]any{"f_title": "new customer"}, Narrow: narrow,
	})
	if err != nil {
		t.Fatalf("create intake: %v", err)
	}
	return res.ItemID
}

// hookCause is the attribution a hook of the intake Type writes with: caused
// by event seq of the Item it acts on.
func hookCause(p item.Principal, id uuid.UUID, seq int64) item.Attribution {
	_, hookID, _ := item.HookParts(p.Actor)
	return item.Attribution{
		OriginatingHook: "hook:intake@v1:" + hookID,
		CausationID:     fmt.Sprintf("%s:%d", id, seq),
		CorrelationID:   "corr-intake",
		Hop:             1,
	}
}

// --- the public door ---

// A caller of the public operations cannot act as the kernel's own actors,
// whatever the operation: not as system:timer, not as another system:
// principal, not as a hook. Nothing is written.
func TestPublic_KernelActorsAreRefused(t *testing.T) {
	k := itemservice.New(pool)
	id := createTicket(t, k, map[string]any{"f_title": "x", "f_assignee": "user-bob"})
	for _, actor := range []string{"system:timer", "system:anything", "hook:ticket:crm_plan"} {
		p := item.Principal{Actor: actor}
		t.Run(actor, func(t *testing.T) {
			_, err := k.Create(testCtx(t), itemservice.CreateRequest{
				IdempotencyKey: newKey(), Principal: p, TypeID: "ticket", TypeVersion: 1,
			})
			assertInvalid(t, "Create", err)
			_, err = k.Update(testCtx(t), itemservice.UpdateRequest{
				IdempotencyKey: newKey(), Principal: p, ItemID: id, Fields: map[string]any{"f_title": "y"},
			})
			assertInvalid(t, "Update", err)
			_, err = k.Transition(testCtx(t), itemservice.TransitionRequest{
				IdempotencyKey: newKey(), Principal: p, ItemID: id, ToState: "in_progress",
			})
			assertInvalid(t, "Transition", err)
			_, err = k.Get(testCtx(t), p, id)
			assertInvalid(t, "Get", err)
			_, err = k.Search(testCtx(t), itemservice.SearchRequest{Principal: p, Query: "x"})
			assertInvalid(t, "Search", err)
			_, err = k.StartAgentSession(testCtx(t), wholeNamespace(p))
			assertInvalid(t, "StartAgentSession", err)
		})
	}
	assertNothingWritten(t, id, 1)
}

// A hook is not a reader, and not the human at the root of a chain.
func TestPublic_HookIsNeitherReaderNorOnBehalfOf(t *testing.T) {
	k := itemservice.New(pool)
	alice := person("alice")
	id := createAs(t, k, alice, "x", nil, nil).ItemID

	_, err := k.Create(testCtx(t), itemservice.CreateRequest{
		IdempotencyKey: newKey(), Principal: alice, TypeID: "ticket", TypeVersion: 1,
		Narrow: &itemservice.Narrow{Readers: []string{alice.Actor, "hook:ticket:crm_plan"}},
	})
	assertInvalid(t, "Create with a hook reader", err)
	_, err = k.Update(testCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: alice, ItemID: id,
		Policy: &itemservice.PolicyPatch{Readers: []string{alice.Actor, "hook:ticket:crm_plan"}},
	})
	assertInvalid(t, "Update with a hook reader", err)
	_, err = k.Update(testCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: item.Principal{Actor: alice.Actor, OnBehalfOf: "hook:ticket:crm_plan"},
		ItemID: id, Fields: map[string]any{"f_title": "y"},
	})
	assertInvalid(t, "Update on behalf of a hook", err)
	assertNothingWritten(t, id, 1)
}

// Only kernel code may say what caused a write. A public caller that sets
// originating_hook, causation_id or a hop is refused on every write, and the
// refusal does not use up its idempotency key.
func TestPublic_InternalAttributionIsRefused(t *testing.T) {
	k := itemservice.New(pool)
	id := createTicket(t, k, map[string]any{"f_title": "x", "f_assignee": "user-bob"})
	cases := []struct {
		name string
		a    item.Attribution
	}{
		{"originating_hook", item.Attribution{OriginatingHook: "timer:ticket@v1:in_progress"}},
		{"causation_id", item.Attribution{CausationID: id.String() + ":1"}},
		{"hop", item.Attribution{Hop: 1}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			key := newKey()
			_, err := k.Create(testCtx(t), itemservice.CreateRequest{
				IdempotencyKey: key, Principal: alice, TypeID: "ticket", TypeVersion: 1, Attribution: c.a,
			})
			assertInvalid(t, "Create", err)
			_, err = k.Update(testCtx(t), itemservice.UpdateRequest{
				IdempotencyKey: key, Principal: alice, ItemID: id,
				Fields: map[string]any{"f_title": "y"}, Attribution: c.a,
			})
			assertInvalid(t, "Update", err)
			_, err = k.Transition(testCtx(t), itemservice.TransitionRequest{
				IdempotencyKey: key, Principal: alice, ItemID: id, ToState: "in_progress", Attribution: c.a,
			})
			assertInvalid(t, "Transition", err)
			if n := count(t, `select count(*) from idempotency where key = $1`, key); n != 0 {
				t.Errorf("idempotency rows after a refusal = %d, want 0", n)
			}
		})
	}
	assertNothingWritten(t, id, 1)
}

// correlation_id is a trace the caller may pass, as a claim. It is stored on
// the event for every write.
func TestPublic_CorrelationIDIsAllowed(t *testing.T) {
	k := itemservice.New(pool)
	a := item.Attribution{CorrelationID: "trace-" + uuid.NewString()[:8]}
	created, err := k.Create(testCtx(t), itemservice.CreateRequest{
		IdempotencyKey: newKey(), Principal: alice, TypeID: "ticket", TypeVersion: 1, Attribution: a,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	id := created.ItemID
	up, err := k.Update(testCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: alice, ItemID: id,
		Fields: map[string]any{"f_assignee": "user-bob"}, Attribution: a,
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	tr, err := k.Transition(testCtx(t), itemservice.TransitionRequest{
		IdempotencyKey: newKey(), Principal: alice, ItemID: id, ToState: "in_progress", Attribution: a,
	})
	if err != nil {
		t.Fatalf("Transition: %v", err)
	}
	for _, seq := range []int64{created.ItemSeq, up.ItemSeq, tr.ItemSeq} {
		if _, got := attribution(t, id, seq); got != a {
			t.Errorf("event %d attribution = %+v, want %+v", seq, got, a)
		}
	}
}

// --- the internal door ---

// The internal door is for the kernel's own actors: system:timer and
// hook:<type>:<hook-id>, nothing else. People and agents use the public
// one. A caller cannot set on_behalf_of here (the kernel fills it from the
// cause), and a kernel actor has no agent session.
func TestInternal_RefusesPeopleAgentsAndOnBehalfOf(t *testing.T) {
	k := itemservice.New(pool)
	alice := person("alice")
	id := createIntake(t, k, alice, nil)
	agent := withSession(t, k, item.Principal{Actor: "agent:" + uuid.NewString()[:8], OnBehalfOf: alice.Actor})
	cause := hookCause(crmPlan, id, 1)
	for _, p := range []item.Principal{
		alice,
		agent,
		{Actor: crmPlan.Actor, OnBehalfOf: alice.Actor},
		{Actor: timer.Actor, OnBehalfOf: alice.Actor},
		// Only system:timer is defined; any other system: actor waits
		// until we define it.
		{Actor: "system:anything"},
		{Actor: crmPlan.Actor, AgentSession: agent.AgentSession},
	} {
		t.Run(p.Actor+" for "+p.OnBehalfOf+" session "+p.AgentSession, func(t *testing.T) {
			_, err := k.Internal().Update(testCtx(t), itemservice.UpdateRequest{
				IdempotencyKey: newKey(), Principal: p, ItemID: id,
				Fields: map[string]any{"f_plan": "gold"}, Attribution: cause,
			})
			assertInvalid(t, "Internal().Update", err)
			_, err = k.Internal().Transition(testCtx(t), itemservice.TransitionRequest{
				IdempotencyKey: newKey(), Principal: p, ItemID: id, ToState: "parked", Attribution: cause,
			})
			assertInvalid(t, "Internal().Transition", err)
		})
	}
	// An agent session on the attribution is refused too.
	sessioned := cause
	sessioned.AgentSession = agent.AgentSession
	_, err := k.Internal().Update(testCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: crmPlan, ItemID: id,
		Fields: map[string]any{"f_plan": "gold"}, Attribution: sessioned,
	})
	assertInvalid(t, "Internal().Update with an agent session on the attribution", err)
	assertNothingWrittenNoOutbox(t, id, 1)
}

// A kernel write must say what caused it, and the cause must be an event of
// the Item it writes to: a hook or timer acts only on the Item that
// triggered it.
func TestInternal_RequiresACauseOnTheSameItem(t *testing.T) {
	k := itemservice.New(pool)
	id := createIntake(t, k, alice, nil)
	other := createIntake(t, k, alice, nil)
	good := hookCause(crmPlan, id, 1)
	cases := []struct {
		name string
		edit func(a *item.Attribution)
	}{
		{"no originating_hook", func(a *item.Attribution) { a.OriginatingHook = "" }},
		{"no causation_id", func(a *item.Attribution) { a.CausationID = "" }},
		{"causation_id is not item:seq", func(a *item.Attribution) { a.CausationID = "yesterday" }},
		{"caused by another Item", func(a *item.Attribution) { a.CausationID = other.String() + ":1" }},
		{"no hop", func(a *item.Attribution) { a.Hop = 0 }},
		// A hook's originating_hook is hook:<type>@v<version>:<hook-id>, and
		// it names the hook that is acting, on this Item's Type and version.
		{"originating_hook names another hook", func(a *item.Attribution) { a.OriginatingHook = "hook:intake@v1:erp_check" }},
		{"originating_hook names another Type", func(a *item.Attribution) { a.OriginatingHook = "hook:ticket@v1:crm_plan" }},
		{"originating_hook names another version", func(a *item.Attribution) { a.OriginatingHook = "hook:intake@v2:crm_plan" }},
		{"originating_hook without a version", func(a *item.Attribution) { a.OriginatingHook = "hook:intake:crm_plan" }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := good
			c.edit(&a)
			_, err := k.Internal().Update(testCtx(t), itemservice.UpdateRequest{
				IdempotencyKey: newKey(), Principal: crmPlan, ItemID: id,
				Fields: map[string]any{"f_plan": "gold"}, Attribution: a,
			})
			assertInvalid(t, "Internal().Update", err)
		})
	}
	// A hook acts only on Items of its own Type.
	ticket := createTicket(t, k, map[string]any{"f_title": "x"})
	_, err := k.Internal().Update(testCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: crmPlan, ItemID: ticket,
		Fields: map[string]any{"f_title": "y"}, Attribution: hookCause(crmPlan, ticket, 1),
	})
	assertInvalid(t, "Internal().Update on another Type's Item", err)

	assertNothingWrittenNoOutbox(t, id, 1)
	assertNothingWrittenNoOutbox(t, other, 1)
	assertNothingWritten(t, ticket, 1)
}

// The timer's originating_hook is parsed like a hook's:
// timer:<type>@v<version>:<state>, on the Item's own Type and version.
func TestInternal_TimerOriginMustMatchTheItem(t *testing.T) {
	k := itemservice.New(pool)
	id := createIntake(t, k, alice, nil)
	cause := func(origin string) item.Attribution {
		return item.Attribution{OriginatingHook: origin, CausationID: fmt.Sprintf("%s:1", id), Hop: 1}
	}
	for _, origin := range []string{
		"timer:",                 // nothing after the prefix
		"timer:intake:triage",    // no version
		"timer:intake@v1:",       // no state
		"timer:intake@vx:triage", // not a version
		"timer:ticket@v1:triage", // another Type
		"timer:intake@v2:triage", // another version
	} {
		_, err := k.Internal().Transition(testCtx(t), itemservice.TransitionRequest{
			IdempotencyKey: newKey(), Principal: timer, ItemID: id, ToState: "parked", Attribution: cause(origin),
		})
		assertInvalid(t, "timer with originating_hook "+origin, err)
	}
	assertNothingWrittenNoOutbox(t, id, 1)

	res, err := k.Internal().Transition(testCtx(t), itemservice.TransitionRequest{
		IdempotencyKey: newKey(), Principal: timer, ItemID: id, ToState: "parked",
		Attribution: cause("timer:intake@v1:triage"),
	})
	if err != nil {
		t.Fatalf("timer with a matching originating_hook: %v", err)
	}
	assertKernelEvent(t, id, res.ItemSeq, timer.Actor, alice.Actor, cause("timer:intake@v1:triage"))
}

// The internal door writes fields and moves state. It does not change who
// may read an Item or where it lives: those stay with people.
func TestInternal_UpdateIsFieldsOnly(t *testing.T) {
	k := itemservice.New(pool)
	alice := person("alice")
	id := createIntake(t, k, alice, nil)
	cause := hookCause(crmPlan, id, 1)

	_, err := k.Internal().Update(testCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: crmPlan, ItemID: id, Attribution: cause,
		Policy: &itemservice.PolicyPatch{Readers: []string{alice.Actor}},
	})
	assertInvalid(t, "Internal().Update with a policy patch", err)
	_, err = k.Internal().Update(testCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: crmPlan, ItemID: id, Attribution: cause,
		Move: &itemservice.Move{To: nil},
	})
	assertInvalid(t, "Internal().Update with a move", err)
	assertNothingWrittenNoOutbox(t, id, 1)
}

// Kernel actors are internal infrastructure acting on the Item that
// triggered them, so their writes skip visibility: a hook updates and moves
// an Item narrowed to one person. They are still subject to `allowed`.
// Every write is recorded with the hook as actor, the cause it gave, and as
// on_behalf_of the human at the root of the cause (here sam, who created
// the Item).
func TestInternal_HookWritesSkipVisibilityButNotAllowed(t *testing.T) {
	k := itemservice.New(pool)
	sam := person("sam")
	id := createIntake(t, k, sam, &itemservice.Narrow{Readers: readers(sam)})

	cause := hookCause(crmPlan, id, 1)
	up, err := k.Internal().Update(testCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: crmPlan, ItemID: id,
		Fields: map[string]any{"f_plan": "gold"}, Attribution: cause,
	})
	if err != nil {
		t.Fatalf("hook Update of a narrowed Item: %v", err)
	}
	assertKernelEvent(t, id, up.ItemSeq, crmPlan.Actor, sam.Actor, cause)

	// Not named in `allowed`: forbidden, even through the internal door.
	_, err = k.Internal().Transition(testCtx(t), itemservice.TransitionRequest{
		IdempotencyKey: newKey(), Principal: crmPlan, ItemID: id, ToState: "approved",
		Attribution: hookCause(crmPlan, id, up.ItemSeq),
	})
	assertForbidden(t, err)

	cause = hookCause(erpCheck, id, up.ItemSeq)
	tr, err := k.Internal().Transition(testCtx(t), itemservice.TransitionRequest{
		IdempotencyKey: newKey(), Principal: erpCheck, ItemID: id, ToState: "approved", Attribution: cause,
	})
	if err != nil {
		t.Fatalf("hook Transition named in allowed: %v", err)
	}
	assertKernelEvent(t, id, tr.ItemSeq, erpCheck.Actor, sam.Actor, cause)
	if it := getAs(t, k, sam, id); it.State != "approved" || it.Fields["f_plan"] != "gold" {
		t.Errorf("Item = %s %v, want approved with f_plan gold", it.State, it.Fields)
	}
}

// assertKernelEvent checks that event seq was written by actor, on behalf
// of onBehalfOf ("" for none), with the attribution the request gave.
func assertKernelEvent(t *testing.T, id uuid.UUID, seq int64, actor, onBehalfOf string, want item.Attribution) {
	t.Helper()
	gotActor, got := attribution(t, id, seq)
	if gotActor != actor || got != want {
		t.Errorf("event %d: actor %q, attribution %+v; want %q, %+v", seq, gotActor, got, actor, want)
	}
	var gotOBO string
	if err := pool.QueryRow(testCtx(t),
		`select coalesce(on_behalf_of, '') from item_events where item_id = $1 and item_seq = $2`, id, seq,
	).Scan(&gotOBO); err != nil {
		t.Fatalf("read on_behalf_of of event %d: %v", seq, err)
	}
	if gotOBO != onBehalfOf {
		t.Errorf("event %d: on_behalf_of = %q, want %q", seq, gotOBO, onBehalfOf)
	}
}

// The kernel fills on_behalf_of on every kernel write with the human at the
// root of its cause: the cause's on_behalf_of if set, else the cause's actor
// if it is a user, else nothing. It is attribution only: the hook writes even
// after that human has lost sight of the Item.
func TestInternal_OnBehalfOfIsTheHumanAtTheRoot(t *testing.T) {
	k := itemservice.New(pool)
	ana := person("ana")

	t.Run("an agent working for ana", func(t *testing.T) {
		agent := withSession(t, k, item.Principal{Actor: "agent:" + uuid.NewString()[:8], OnBehalfOf: ana.Actor})
		id := createIntake(t, k, agent, nil)
		first := hookCause(crmPlan, id, 1)
		up, err := k.Internal().Update(testCtx(t), itemservice.UpdateRequest{
			IdempotencyKey: newKey(), Principal: crmPlan, ItemID: id,
			Fields: map[string]any{"f_plan": "gold"}, Attribution: first,
		})
		if err != nil {
			t.Fatalf("hook Update: %v", err)
		}
		assertKernelEvent(t, id, up.ItemSeq, crmPlan.Actor, ana.Actor, first)

		// One hop further: the cause is the hook's own event, which already
		// names ana.
		second := hookCause(erpCheck, id, up.ItemSeq)
		second.Hop = 2
		tr, err := k.Internal().Transition(testCtx(t), itemservice.TransitionRequest{
			IdempotencyKey: newKey(), Principal: erpCheck, ItemID: id, ToState: "approved", Attribution: second,
		})
		if err != nil {
			t.Fatalf("hook Transition: %v", err)
		}
		assertKernelEvent(t, id, tr.ItemSeq, erpCheck.Actor, ana.Actor, second)
	})

	t.Run("ana, who can no longer see the Item", func(t *testing.T) {
		id := createIntake(t, k, ana, &itemservice.Narrow{Readers: readers(ana)})
		bob := person("bob")
		setPolicy(t, k, ana, id, itemservice.PolicyPatch{Readers: readers(ana, bob)})
		cause := hookCause(crmPlan, id, 1)
		// bob narrows ana out after the event that triggers the hook.
		setPolicy(t, k, bob, id, itemservice.PolicyPatch{Readers: readers(bob)})
		assertHidden(t, k, ana, id)
		up, err := k.Internal().Update(testCtx(t), itemservice.UpdateRequest{
			IdempotencyKey: newKey(), Principal: crmPlan, ItemID: id,
			Fields: map[string]any{"f_plan": "gold"}, Attribution: cause,
		})
		if err != nil {
			t.Fatalf("hook Update after ana lost access: %v", err)
		}
		assertKernelEvent(t, id, up.ItemSeq, crmPlan.Actor, ana.Actor, cause)
	})

	t.Run("an agent acting for itself: no human", func(t *testing.T) {
		agent := withSession(t, k, item.Principal{Actor: "agent:" + uuid.NewString()[:8]})
		id := createIntake(t, k, agent, nil)
		cause := hookCause(crmPlan, id, 1)
		up, err := k.Internal().Update(testCtx(t), itemservice.UpdateRequest{
			IdempotencyKey: newKey(), Principal: crmPlan, ItemID: id,
			Fields: map[string]any{"f_plan": "gold"}, Attribution: cause,
		})
		if err != nil {
			t.Fatalf("hook Update: %v", err)
		}
		assertKernelEvent(t, id, up.ItemSeq, crmPlan.Actor, "", cause)
	})
}

// A chain of kernel writes stops at itemservice.MaxHop (8). A write one hop
// deeper is rejected with reason "hop limit", and nothing is written.
func TestInternal_HopLimit(t *testing.T) {
	if itemservice.MaxHop != 8 {
		t.Fatalf("MaxHop = %d, want 8", itemservice.MaxHop)
	}
	k := itemservice.New(pool)
	id := createIntake(t, k, alice, nil)

	at := hookCause(crmPlan, id, 1)
	at.Hop = itemservice.MaxHop
	res, err := k.Internal().Update(testCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: crmPlan, ItemID: id,
		Fields: map[string]any{"f_plan": "gold"}, Attribution: at,
	})
	if err != nil {
		t.Fatalf("Update at hop %d: %v", itemservice.MaxHop, err)
	}

	over := hookCause(crmPlan, id, res.ItemSeq)
	over.Hop = itemservice.MaxHop + 1
	_, err = k.Internal().Update(testCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: crmPlan, ItemID: id,
		Fields: map[string]any{"f_plan": "silver"}, Attribution: over,
	})
	if !errors.Is(err, itemservice.ErrRejected) || !strings.Contains(err.Error(), "hop limit") {
		t.Fatalf("Update at hop %d: error = %v, want ErrRejected with \"hop limit\"", over.Hop, err)
	}
	assertNothingWrittenNoOutbox(t, id, 2)
}
