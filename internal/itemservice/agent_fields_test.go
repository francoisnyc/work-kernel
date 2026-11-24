// Tests for fields closed to agents by default (the owner's decisions of
// Phase A): a field's writable_by lists who writes it, people, agents and
// role:<name>; absent means [people]. An agent and the user it acts for
// must both pass: the agent as agents or a role it holds, its user as
// people or a role they hold. The kernel's own writes (hooks, a move's
// sets) are not the public door's business; a move's clears is a write of
// the field it clears; agents acting for a customer stay under the
// customer rules.
package itemservice_test

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// af_note: one field of each kind of writer.
const afNoteSrc = `
id: af_note
version: 1
initial_state: open
fields:
  - {id: f_shut,  type: string}
  - {id: f_open,  type: string, writable_by: [people, agents]}
  - {id: f_bots,  type: string, writable_by: [agents]}
  - {id: f_role,  type: string, writable_by: ['role:af_writer']}
  - {id: f_mixed, type: string, writable_by: [people, 'role:af_writer']}
states:
  - {id: open,         category: todo}
  - {id: done,         category: done}
  - {id: shut_cleared, category: done}
  - {id: open_cleared, category: done}
  - {id: stamped,      category: done}
transitions:
  - {from: open, to: done}
  - {from: open, to: shut_cleared, clears: [f_shut]}
  - {from: open, to: open_cleared, clears: [f_open]}
  - {from: open, to: stamped, sets: {f_shut: '"stamped"'}}
`

// noteWorld: ana, a person; wren, a person holding af_writer; and agents:
// bot (no role), alone and for ana; scribe (holding af_writer), alone,
// for ana and for wren. Every session covers the namespace with every power.
type noteWorld struct {
	k                                    *itemservice.Service
	ana, wren                            item.Principal
	bot, botForAna                       item.Principal
	scribe, scribeForAna, scribeForWren item.Principal
}

func newNoteWorld(t *testing.T) noteWorld {
	t.Helper()
	k := itemservice.New(pool)
	ty, err := typedef.Parse([]byte(afNoteSrc))
	if err != nil {
		t.Fatalf("parse af_note: %v", err)
	}
	if err := k.RegisterType(testCtx(t), ty); err != nil {
		t.Fatalf("register af_note: %v", err)
	}
	w := noteWorld{k: k, ana: person("ana"), wren: person("wren")}
	grantRole(t, w.wren, "af_writer")
	bot := "agent:bot-" + uuid.NewString()[:8]
	scribe := "agent:scribe-" + uuid.NewString()[:8]
	grantRole(t, item.Principal{Actor: scribe}, "af_writer")
	w.bot = withSession(t, k, item.Principal{Actor: bot})
	w.botForAna = withSession(t, k, item.Principal{Actor: bot, OnBehalfOf: w.ana.Actor})
	w.scribe = withSession(t, k, item.Principal{Actor: scribe})
	w.scribeForAna = withSession(t, k, item.Principal{Actor: scribe, OnBehalfOf: w.ana.Actor})
	w.scribeForWren = withSession(t, k, item.Principal{Actor: scribe, OnBehalfOf: w.wren.Actor})
	return w
}

// note: an af_note ana creates, with f_shut and f_open set.
func (w noteWorld) note(t *testing.T) uuid.UUID {
	t.Helper()
	res, err := w.k.Create(testCtx(t), itemservice.CreateRequest{
		IdempotencyKey: newKey(), Principal: w.ana, TypeID: "af_note", TypeVersion: 1,
		Fields: map[string]any{"f_shut": "ana's", "f_open": "ana's"},
	})
	if err != nil {
		t.Fatalf("ana creates an af_note: %v", err)
	}
	return res.ItemID
}

func (w noteWorld) create(t *testing.T, p item.Principal, fields map[string]any) (itemservice.WriteResult, error) {
	t.Helper()
	return w.k.Create(testCtx(t), itemservice.CreateRequest{
		IdempotencyKey: newKey(), Principal: p, TypeID: "af_note", TypeVersion: 1, Fields: fields,
	})
}

func (w noteWorld) move(t *testing.T, p item.Principal, id uuid.UUID, to string, patch map[string]any) error {
	t.Helper()
	_, err := w.k.Transition(testCtx(t), itemservice.TransitionRequest{
		IdempotencyKey: newKey(), Principal: p, ItemID: id, ToState: to, Fields: patch,
	})
	return err
}

// assertFieldRefused: FIELD_NOT_WRITABLE naming exactly the one field.
func assertFieldRefused(t *testing.T, what string, err error, field string) {
	t.Helper()
	var fe *itemservice.FieldNotWritableError
	if !errors.Is(err, itemservice.ErrForbidden) || !errors.As(err, &fe) {
		t.Errorf("%s: error = %v, want FIELD_NOT_WRITABLE", what, err)
		return
	}
	if len(fe.Fields) != 1 || fe.Fields[0] != field {
		t.Errorf("%s: refused fields = %v, want [%s]", what, fe.Fields, field)
	}
}

// Who writes each kind of field, by Update. A person writes a field with
// no writable_by; an agent does not, alone or acting for a person who may.
// [people, agents] is anyone's; [agents] is an agent's acting alone (its
// user, a person, has no right there); a role is its holders', agents
// included, an agent and its user both holding it; [people, role:x] is any
// person's, and an agent's that holds x.
func TestAgentFields_WhoWritesWhat(t *testing.T) {
	w := newNoteWorld(t)
	cases := []struct {
		field, who string
		p          item.Principal
		ok         bool
	}{
		{"f_shut", "ana", w.ana, true},
		{"f_shut", "an agent", w.bot, false},
		{"f_shut", "an agent for ana", w.botForAna, false},
		{"f_shut", "an agent holding a role", w.scribe, false},

		{"f_open", "ana", w.ana, true},
		{"f_open", "an agent", w.bot, true},
		{"f_open", "an agent for ana", w.botForAna, true},

		{"f_bots", "ana", w.ana, false},
		{"f_bots", "an agent", w.bot, true},
		{"f_bots", "an agent for ana", w.botForAna, false},

		{"f_role", "ana, without the role", w.ana, false},
		{"f_role", "wren, holding it", w.wren, true},
		{"f_role", "an agent without the role", w.bot, false},
		{"f_role", "an agent holding it", w.scribe, true},
		{"f_role", "an agent holding it, for ana", w.scribeForAna, false},
		{"f_role", "an agent holding it, for wren", w.scribeForWren, true},

		{"f_mixed", "ana, without the role", w.ana, true},
		{"f_mixed", "an agent without the role", w.bot, false},
		{"f_mixed", "an agent holding it", w.scribe, true},
		{"f_mixed", "an agent holding it, for ana", w.scribeForAna, true},
	}
	for _, c := range cases {
		t.Run(c.field+" by "+c.who, func(t *testing.T) {
			id := w.note(t)
			_, err := w.k.Update(testCtx(t), itemservice.UpdateRequest{
				IdempotencyKey: newKey(), Principal: c.p, ItemID: id, Fields: map[string]any{c.field: "written"},
			})
			if c.ok {
				if err != nil {
					t.Errorf("%s writes %s: %v", c.who, c.field, err)
				}
				return
			}
			assertFieldRefused(t, c.who+" writes "+c.field, err, c.field)
			assertNothingWrittenNoOutbox(t, id, 1)
		})
	}
}

// An agent creating an Item may not give a closed field a value, alone or
// for a person; nothing is written. It may create one with no fields, or
// with fields open to it. A person's create is unaffected.
func TestAgentFields_Create(t *testing.T) {
	w := newNoteWorld(t)
	before := count(t, `select count(*) from item_events where actor = $1`, w.bot.Actor)
	_, err := w.create(t, w.bot, map[string]any{"f_shut": "by the agent"})
	assertClosedToAgents(t, "the agent creates with a closed field", err, "f_shut")
	_, err = w.create(t, w.botForAna, map[string]any{"f_shut": "by the agent", "f_open": "fine"})
	assertClosedToAgents(t, "the agent, for ana, creates with a closed field", err, "f_shut")
	if n := count(t, `select count(*) from item_events where actor = $1`, w.bot.Actor); n != before {
		t.Errorf("events by the agent = %d, want %d: nothing written", n, before)
	}

	if _, err := w.create(t, w.bot, nil); err != nil {
		t.Errorf("the agent creates with no fields: %v", err)
	}
	if _, err := w.create(t, w.botForAna, map[string]any{"f_open": "by the agent"}); err != nil {
		t.Errorf("the agent, for ana, creates with an open field: %v", err)
	}
	if _, err := w.create(t, w.ana, map[string]any{"f_shut": "by ana"}); err != nil {
		t.Errorf("ana creates with f_shut: %v", err)
	}
}

// A transition's patch is a write: a closed field in it is refused to an
// agent, an open one is not.
func TestAgentFields_TransitionPatch(t *testing.T) {
	w := newNoteWorld(t)
	id := w.note(t)
	assertClosedToAgents(t, "the agent moves it with f_shut", w.move(t, w.bot, id, "done", map[string]any{"f_shut": "x"}), "f_shut")
	assertNothingWrittenNoOutbox(t, id, 1)
	if err := w.move(t, w.bot, id, "done", map[string]any{"f_open": "x"}); err != nil {
		t.Errorf("the agent moves it with f_open: %v", err)
	}
}

// A move's clears is a write of the field it clears (the owner's decision):
// an agent may not make a move that clears a closed field, alone or for
// ana; it may make one that clears an open field; ana may make either.
func TestAgentFields_ClearsCountAsAWrite(t *testing.T) {
	w := newNoteWorld(t)
	id := w.note(t)
	assertClosedToAgents(t, "the agent's move clears f_shut", w.move(t, w.bot, id, "shut_cleared", nil), "f_shut")
	assertClosedToAgents(t, "the agent's move, for ana, clears f_shut", w.move(t, w.botForAna, id, "shut_cleared", nil), "f_shut")
	assertNothingWrittenNoOutbox(t, id, 1)
	if err := w.move(t, w.bot, id, "open_cleared", nil); err != nil {
		t.Errorf("the agent's move clears f_open: %v", err)
	}
	if err := w.move(t, w.ana, w.note(t), "shut_cleared", nil); err != nil {
		t.Errorf("ana's move clears f_shut: %v", err)
	}
}

// A move's sets is the Type's write, not the agent's: an agent's move that
// sets a closed field applies.
func TestAgentFields_SetsAreTheTypes(t *testing.T) {
	w := newNoteWorld(t)
	id := w.note(t)
	if err := w.move(t, w.bot, id, "stamped", nil); err != nil {
		t.Fatalf("the agent's move sets f_shut: %v", err)
	}
	if got := get(t, w.k, id).Fields["f_shut"]; got != "stamped" {
		t.Errorf("f_shut = %v, want the move's value", got)
	}
}

// The kernel's own writes are untouched: a hook writes f_plan, a field no
// agent may write, of an intake an agent created for ana.
func TestAgentFields_KernelWritesUnaffected(t *testing.T) {
	k := itemservice.New(pool)
	ana := person("ana")
	agent := withSession(t, k, item.Principal{Actor: "agent:" + uuid.NewString()[:8], OnBehalfOf: ana.Actor})
	id := createIntake(t, k, agent, nil)
	_, err := k.Update(testCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: agent, ItemID: id, Fields: map[string]any{"f_plan": "gold"},
	})
	assertClosedToAgents(t, "the agent writes f_plan", err, "f_plan")
	if _, err := k.Internal().Update(testCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: crmPlan, ItemID: id,
		Fields: map[string]any{"f_plan": "gold"}, Attribution: hookCause(crmPlan, id, 1),
	}); err != nil {
		t.Errorf("the hook writes f_plan: %v", err)
	}
}

// An agent acting for a customer stays under the customer rules (the
// owner's decision): a portal bot creates kim's reply with its body, a
// customer field, though reply opens no field to agents.
func TestAgentFields_ACustomersAgent_UnderTheCustomerRules(t *testing.T) {
	w := newCustWorld(t)
	inc := w.incident(t, w.kim, w.desk)
	bot := w.portalBot(t, "agent:portal-"+word(), w.kim)
	if _, err := w.tryCreate(t, bot, "reply", 1, &inc, map[string]any{"f_body": "from the portal"}); err != nil {
		t.Errorf("the portal bot creates kim's reply: %v", err)
	}
}

// NOT_FOUND comes first: an agent creating, with a closed field, in a
// container outside its session's scopes is told the container does not
// exist, not that the field is closed.
func TestAgentFields_Create_NotFoundFirst(t *testing.T) {
	w := newCapWorld(t)
	newNoteWorld(t) // registers af_note
	agent := scoped(t, w.k, w.user, []uuid.UUID{w.a}, item.PowerRead, item.PowerCreate)
	_, err := w.k.Create(testCtx(t), itemservice.CreateRequest{
		IdempotencyKey: newKey(), Principal: agent, TypeID: "af_note", TypeVersion: 1, ContainerID: &w.b,
		Fields: map[string]any{"f_shut": "x"},
	})
	assertNotFound(t, "create with a closed field in a container outside the session", err)
}
