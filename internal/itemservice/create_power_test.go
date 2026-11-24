// Tests for starting work, step 1 (docs/plans/starting-work.md, 1.0): an
// agent session needs the create power for every Create. Holding another
// write power is not enough; every other write keeps the v1 rule (any write
// power). The refusal is ErrForbidden, its message naming the missing power,
// and nothing is written. The session's scopes are checked first (NOT_FOUND),
// then the power, then the agent write rule (NEEDS_APPROVAL). A create
// reaction is governed by its target's `creates:` consent, never by the
// session of the agent whose write triggered it.
package itemservice_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
)

// assertMayNotCreate checks the create power's refusal: ErrForbidden, and
// the message names the missing power.
func assertMayNotCreate(t *testing.T, what string, err error) {
	t.Helper()
	if !errors.Is(err, itemservice.ErrForbidden) {
		t.Errorf("%s: error = %v, want ErrForbidden", what, err)
		return
	}
	if !strings.Contains(err.Error(), "this agent session may not create") {
		t.Errorf("%s: error = %q, want it to say the session may not create", what, err)
	}
}

// A session holding a write power other than create may not create, and
// nothing is written; the same session still updates and transitions.
func TestCreatePower_OtherWritePowersMayNotCreate(t *testing.T) {
	for _, power := range []string{item.PowerEditFields, item.PowerComment, item.PowerTransitionAll} {
		t.Run(power, func(t *testing.T) {
			w := newCapWorld(t)
			agent := scoped(t, w.k, w.user, []uuid.UUID{w.a}, item.PowerRead, power)
			// An Item in A whose fields an agent may write (fields are
			// closed to agents by default).
			openInA := createOpenAs(t, w.k, w.user, "open in A", nil, &w.a).ItemID
			ctx := testCtx(t)
			key := newKey()
			before := count(t, `select count(*) from items where container_id = $1`, w.a)

			_, err := w.k.Create(ctx, itemservice.CreateRequest{
				IdempotencyKey: key, Principal: agent, TypeID: "ticket", TypeVersion: 1, ContainerID: &w.a,
				Fields: map[string]any{"f_title": "by the agent"},
			})
			assertMayNotCreate(t, "Create with "+power, err)

			// Nothing written: no Item, no event, no idempotency row.
			if n := count(t, `select count(*) from items where container_id = $1`, w.a); n != before {
				t.Errorf("Items in A = %d, want %d", n, before)
			}
			if n := count(t, `select count(*) from item_events where actor = $1`, agent.Actor); n != 0 {
				t.Errorf("events by the agent = %d, want 0", n)
			}
			if n := count(t, `select count(*) from idempotency where actor = $1 and key = $2`, agent.Actor, key); n != 0 {
				t.Errorf("idempotency rows for the refused key = %d, want 0", n)
			}

			// Every other write keeps the v1 rule: any write power.
			if err := agentUpdate(t, w.k, agent, openInA); err != nil {
				t.Errorf("Update with %s: %v", power, err)
			}
			if _, err := w.k.Transition(ctx, itemservice.TransitionRequest{
				IdempotencyKey: newKey(), Principal: agent, ItemID: w.inA, ToState: "canceled",
			}); err != nil {
				t.Errorf("Transition with %s: %v", power, err)
			}
		})
	}
}

// A session with create only creates, and also updates: create is a write
// power like any other for every write but Create.
func TestCreatePower_CreateOnly(t *testing.T) {
	w := newCapWorld(t)
	agent := scoped(t, w.k, w.user, []uuid.UUID{w.a}, item.PowerCreate)
	openInA := createOpenAs(t, w.k, w.user, "open in A", nil, &w.a).ItemID // fields an agent may write
	if _, err := w.k.Create(testCtx(t), itemservice.CreateRequest{
		IdempotencyKey: newKey(), Principal: agent, TypeID: "ticket", TypeVersion: 1, ContainerID: &w.a,
	}); err != nil {
		t.Errorf("Create with create only: %v", err)
	}
	if err := agentUpdate(t, w.k, agent, openInA); err != nil {
		t.Errorf("Update with create only: %v", err)
	}
}

// The session's scopes come first: outside them the container does not
// exist for the session, whatever its powers (ErrNotFound, not ErrForbidden).
func TestCreatePower_OutsideItsScopesIsNotFound(t *testing.T) {
	w := newCapWorld(t)
	agent := scoped(t, w.k, w.user, []uuid.UUID{w.a}, item.PowerRead, item.PowerEditFields)
	_, err := w.k.Create(testCtx(t), itemservice.CreateRequest{
		IdempotencyKey: newKey(), Principal: agent, TypeID: "ticket", TypeVersion: 1, ContainerID: &w.b,
	})
	assertNotFound(t, "Create in B, outside the session", err)
	_, err = w.k.Create(testCtx(t), itemservice.CreateRequest{
		IdempotencyKey: newKey(), Principal: agent, TypeID: "ticket", TypeVersion: 1,
	})
	assertNotFound(t, "Create at the top level, outside the session", err)
}

// The power comes before the agent write rule: where a session holding
// create would need approval, a session without it is refused outright
// (an approval would not help it).
func TestCreatePower_BeforeTheApprovalRule(t *testing.T) {
	w := newCapWorld(t)
	private := createAs(t, w.k, w.user, "private", &itemservice.Narrow{Readers: readers(w.user)}, &w.a).ItemID
	all := []uuid.UUID{uuid.Nil}
	withCreate := scoped(t, w.k, w.user, all, item.PowerRead, item.PowerCreate)
	without := scoped(t, w.k, w.user, all, item.PowerRead, item.PowerEditFields)
	// The user lets both agents read the private Item, and both do: each
	// session has now read narrower than B.
	setPolicy(t, w.k, w.user, private, itemservice.PolicyPatch{Readers: []string{w.user.Actor, withCreate.Actor, without.Actor}})
	assertVisible(t, w.k, withCreate, private)
	assertVisible(t, w.k, without, private)

	_, err := w.k.Create(testCtx(t), itemservice.CreateRequest{
		IdempotencyKey: newKey(), Principal: withCreate, TypeID: "ticket", TypeVersion: 1, ContainerID: &w.b,
	})
	var na *itemservice.NeedsApprovalError
	if !errors.As(err, &na) {
		t.Fatalf("create into B with create: %v, want NEEDS_APPROVAL (the world must need it)", err)
	}
	_, err = w.k.Create(testCtx(t), itemservice.CreateRequest{
		IdempotencyKey: newKey(), Principal: without, TypeID: "ticket", TypeVersion: 1, ContainerID: &w.b,
	})
	if errors.As(err, &na) {
		t.Errorf("create into B without create: NEEDS_APPROVAL; want the create power's refusal first")
	}
	assertMayNotCreate(t, "create into B without create", err)
}

// A customer's portal bot started without create (read, comment) may not
// create what the customer could: a reply on her own incident.
func TestCreatePower_PortalBotWithoutCreate(t *testing.T) {
	w := newCustWorld(t)
	powers := []string{item.PowerRead, item.PowerComment}
	all := []uuid.UUID{uuid.Nil}
	bot := item.Principal{Actor: "agent:portal-" + word(), OnBehalfOf: w.kim.Actor, Namespace: w.ns}
	session, err := w.k.StartAgentSession(testCtx(t), itemservice.StartAgentSessionRequest{
		Principal: bot, Scopes: all, Powers: powers, Grant: &itemservice.Grant{Scopes: all, Powers: powers},
	})
	if err != nil {
		t.Fatalf("a portal bot for kim with read,comment: %v", err)
	}
	bot.AgentSession = session
	inc := w.incident(t, w.kim, w.desk)
	if _, err := w.tryCreate(t, w.kim, "reply", 1, &inc, map[string]any{"f_body": "kim herself"}); err != nil {
		t.Fatalf("kim replies on her incident: %v (the reply must be hers to create)", err)
	}
	_, err = w.tryCreate(t, bot, "reply", 1, &inc, map[string]any{"f_body": "from the portal"})
	assertMayNotCreate(t, "the bot replies for kim without create", err)
}

// A create reaction is not the agent's create: an agent session with
// transition:* and no create may not create a todo itself, but its
// transition's reaction creates one (the todo's `creates:` consent governs).
func TestCreatePower_AReactionCreatesForAnAgentWithoutCreate(t *testing.T) {
	w := newR2World(t)
	bench := w.create(t, w.ana, "r2bench", nil, map[string]any{"f_title": "VPN " + word()})
	agent := scoped(t, w.k, w.ana, []uuid.UUID{uuid.Nil}, item.PowerRead, item.PowerTransitionAll)

	_, err := w.k.Create(testCtx(t), itemservice.CreateRequest{
		IdempotencyKey: newKey(), Principal: agent, TypeID: "r2todo", TypeVersion: 1, ContainerID: &bench,
		Fields: map[string]any{"f_title": "by the agent"},
	})
	assertMayNotCreate(t, "the agent creates a todo itself", err)

	w.move(t, agent, bench, "working") // event 2, by the agent
	out, err := w.k.Internal().ReactCreate(testCtx(t), entered(bench, 2, "checklist"))
	if err != nil || out.Outcome != itemservice.ReactionApplied || out.Target == uuid.Nil {
		t.Fatalf("ReactCreate after the agent's transition = %+v, %v; want applied with the new todo", out, err)
	}
	todo, err := w.k.Get(testCtx(t), w.ana, out.Target)
	if err != nil {
		t.Fatalf("ana reads the todo: %v", err)
	}
	if todo.TypeID != "r2todo" || todo.CreatedBy != "reaction:r2bench:checklist" {
		t.Errorf("the todo = %s created by %s; want an r2todo created by the reaction", todo.TypeID, todo.CreatedBy)
	}
}
