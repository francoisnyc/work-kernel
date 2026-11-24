// Tests for starting work, step 2 (docs/plans/starting-work.md, 1.1 to
// 1.6): Create judges the start block of the latest registered version of
// the Type, whatever version the caller names, inside the transaction
// that locks the container.
//
//   - in: the container's Type (any version), or top for no container.
//     Otherwise StartRefusedError START_NOT_IN, an ErrForbidden.
//   - allowed, by policy.MayStart, for staff and agents: role:,
//     principal:user:, field: (the container's field), container_creator,
//     nobody. Otherwise START_NOT_ALLOWED. A customer (or an agent acting
//     for one) is judged by own and shared, as before, and by in; nobody
//     refuses them too. A person opening an Item for a customer is judged
//     as the person.
//   - The order: the container visible, the customer's check and the
//     agent session's scope (all NOT_FOUND); then in; then allowed; then
//     the rest unchanged (placement for a narrow, write power, create
//     power, agent write rule, uniqueness, invariants).
//   - A refused create writes nothing.
//
// Types are global, not per namespace, and every test of this package
// shares one Postgres: each test registers Types of its own, with IDs no
// other test uses, so that a start block here never judges a create
// elsewhere. incident@4 is never registered in this package.
package itemservice_test

import (
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
)

// startWorld: Types of this test only (their IDs end in s), a namespace,
// lea (role lead), bob, carl and eve (no role), and the customer kim.
type startWorld struct {
	k                   *itemservice.Service
	ns, s               string
	lea, bob, carl, eve item.Principal
	kim                 item.Principal
}

func newStartWorld(t *testing.T) startWorld {
	t.Helper()
	k := itemservice.New(pool)
	ns := newNamespace(t, k)
	w := startWorld{k: k, ns: ns, s: word()[1:9],
		lea: in(person("lea"), ns), bob: in(person("bob"), ns), carl: in(person("carl"), ns), eve: in(person("eve"), ns),
		kim: customer("kim-"+word(), ns)}
	grantRoleIn(t, ns, w.lea, "lead")
	return w
}

// id is a Type ID of this test: name_<suffix>.
func (w startWorld) id(name string) string { return name + "_" + w.s }

// reg registers a Type from YAML in which every {s} becomes the suffix.
func (w startWorld) reg(t *testing.T, src string) {
	t.Helper()
	registerInline(t, w.k, strings.ReplaceAll(src, "{s}", w.s))
}

// The containers: a room (a principal field and a set of them; it owns
// asks, so customers see rooms) and a hall.
const (
	stRoomSrc = `
id: room_{s}
version: 1
fields:
  - {id: f_lead,  type: principal}
  - {id: f_leads, type: principal, multiple: true}
own: [ask_{s}]
`
	stHallSrc = `
id: hall_{s}
version: 1
`
)

// stAskSrc is an ask of a version with a start block (or none).
func stAskSrc(version, start string) string {
	return `
id: ask_{s}
version: ` + version + `
fields:
  - {id: f_title, type: string}
customer_fields: [f_title]
` + start
}

func (w startWorld) create(t *testing.T, p item.Principal, typ string, version int, container *uuid.UUID, fields map[string]any) (itemservice.WriteResult, error) {
	if fields == nil {
		fields = map[string]any{"f_title": "t " + word()}
	}
	return w.k.Create(testCtx(t), itemservice.CreateRequest{
		IdempotencyKey: newKey(), Principal: p, TypeID: typ, TypeVersion: version, ContainerID: container, Fields: fields,
	})
}

// container creates a room or a hall as p, with fields.
func (w startWorld) container(t *testing.T, p item.Principal, typ string, fields map[string]any) uuid.UUID {
	t.Helper()
	res, err := w.k.Create(testCtx(t), itemservice.CreateRequest{
		IdempotencyKey: newKey(), Principal: p, TypeID: typ, TypeVersion: 1, Fields: fields,
	})
	if err != nil {
		t.Fatalf("create %s as %s: %v", typ, p.Actor, err)
	}
	return res.ItemID
}

func mustCreate(t *testing.T, what string, res itemservice.WriteResult, err error) uuid.UUID {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
	return res.ItemID
}

// assertStartRefused checks a start refusal: an ErrForbidden, a
// *StartRefusedError with the reason and the Type, a message that names
// the reason and no principal or Item.
func assertStartRefused(t *testing.T, what string, err error, reason, typeID string) {
	t.Helper()
	if !errors.Is(err, itemservice.ErrForbidden) {
		t.Errorf("%s: error = %v, want ErrForbidden (%s)", what, err, reason)
		return
	}
	var sr *itemservice.StartRefusedError
	if !errors.As(err, &sr) {
		t.Errorf("%s: error %v is not a *StartRefusedError", what, err)
		return
	}
	if sr.Reason != reason || sr.TypeID != typeID {
		t.Errorf("%s: refused %s for %s, want %s for %s", what, sr.Reason, sr.TypeID, reason, typeID)
	}
	msg := err.Error()
	if !strings.Contains(msg, reason) {
		t.Errorf("%s: message %q does not name %s", what, msg, reason)
	}
	for _, leak := range []string{"user:", "customer:", "agent:", "namespaces/"} {
		if strings.Contains(msg, leak) {
			t.Errorf("%s: message %q names a principal or an Item (%s)", what, msg, leak)
		}
	}
}

// --- in ---

// in names the container Types an Item may be created in (any version of
// each), and top for the top level; a refused create writes nothing.
func TestStart_In(t *testing.T) {
	w := newStartWorld(t)
	w.reg(t, stRoomSrc)
	w.reg(t, stHallSrc)
	w.reg(t, stAskSrc("1", "start:\n  in: [room_{s}]\n"))
	w.reg(t, `
id: any_{s}
version: 1
start:
  in: [room_{s}, top]
`)
	room := w.container(t, w.lea, w.id("room"), nil)
	hall := w.container(t, w.lea, w.id("hall"), nil)

	if _, err := w.create(t, w.eve, w.id("ask"), 1, &room, nil); err != nil {
		t.Errorf("an ask in a room: %v", err)
	}
	key := newKey()
	_, err := w.k.Create(testCtx(t), itemservice.CreateRequest{IdempotencyKey: key, Principal: w.eve, TypeID: w.id("ask"),
		TypeVersion: 1, ContainerID: &hall, Fields: map[string]any{"f_title": "in the hall"}})
	assertStartRefused(t, "an ask in a hall", err, itemservice.StartNotIn, w.id("ask"))
	if n := count(t, `select count(*) from items where container_id = $1`, hall); n != 0 {
		t.Errorf("Items in the hall = %d, want 0", n)
	}
	if n := count(t, `select count(*) from idempotency where actor = $1 and key = $2`, w.eve.Actor, key); n != 0 {
		t.Errorf("idempotency rows for the refused key = %d, want 0", n)
	}
	_, err = w.create(t, w.eve, w.id("ask"), 1, nil, nil)
	assertStartRefused(t, "an ask at the top level", err, itemservice.StartNotIn, w.id("ask"))
	if n := count(t, `select count(*) from items where type_id = $1 and container_id is null`, w.id("ask")); n != 0 {
		t.Errorf("asks at the top level = %d, want 0", n)
	}

	// top: the top level, beside a container Type.
	if _, err := w.create(t, w.eve, w.id("any"), 1, nil, map[string]any{}); err != nil {
		t.Errorf("an any at the top level: %v", err)
	}
	if _, err := w.create(t, w.eve, w.id("any"), 1, &room, map[string]any{}); err != nil {
		t.Errorf("an any in a room: %v", err)
	}
	_, err = w.create(t, w.eve, w.id("any"), 1, &hall, map[string]any{})
	assertStartRefused(t, "an any in a hall", err, itemservice.StartNotIn, w.id("any"))

	// Any version of the container's Type.
	w.reg(t, strings.Replace(stRoomSrc, "version: 1", "version: 2", 1))
	res2, err := w.k.Create(testCtx(t), itemservice.CreateRequest{IdempotencyKey: newKey(), Principal: w.lea,
		TypeID: w.id("room"), TypeVersion: 2})
	room2 := mustCreate(t, "a room@2", res2, err)
	if _, err := w.create(t, w.eve, w.id("ask"), 1, &room2, nil); err != nil {
		t.Errorf("an ask in a room@2: %v", err)
	}
}

// A Type without a start block: anyone who sees the container creates it
// anywhere, as before this step.
func TestStart_NoBlock_AsBefore(t *testing.T) {
	w := newStartWorld(t)
	w.reg(t, stRoomSrc)
	w.reg(t, stHallSrc)
	w.reg(t, stAskSrc("1", ""))
	room := w.container(t, w.lea, w.id("room"), nil)
	hall := w.container(t, w.lea, w.id("hall"), nil)
	for name, where := range map[string]*uuid.UUID{"the top level": nil, "a room": &room, "a hall": &hall} {
		if _, err := w.create(t, w.eve, w.id("ask"), 1, where, nil); err != nil {
			t.Errorf("eve creates an ask in %s: %v", name, err)
		}
	}
}

// The latest registered version's block judges a create of any version;
// the Item stays pinned to the version named. A later version that drops
// the block loosens every version at once.
func TestStart_TheLatestVersionGoverns(t *testing.T) {
	w := newStartWorld(t)
	w.reg(t, stRoomSrc)
	w.reg(t, stAskSrc("1", ""))
	room := w.container(t, w.lea, w.id("room"), nil)
	if _, err := w.create(t, w.eve, w.id("ask"), 1, nil, nil); err != nil {
		t.Fatalf("an ask@1 at the top level, before @2: %v", err)
	}

	w.reg(t, stAskSrc("2", "start:\n  in: [room_{s}]\n"))
	_, err := w.create(t, w.eve, w.id("ask"), 1, nil, nil)
	assertStartRefused(t, "an ask@1 at the top level, judged by @2", err, itemservice.StartNotIn, w.id("ask"))
	res1, err := w.create(t, w.eve, w.id("ask"), 1, &room, nil)
	id := mustCreate(t, "an ask@1 in a room", res1, err)
	got, err := w.k.Get(testCtx(t), w.eve, id)
	if err != nil || got.TypeVersion != 1 {
		t.Errorf("the ask created as @1: version %d, %v; want pinned to 1", got.TypeVersion, err)
	}

	w.reg(t, stAskSrc("3", ""))
	if _, err := w.create(t, w.eve, w.id("ask"), 1, nil, nil); err != nil {
		t.Errorf("an ask@1 at the top level, once @3 dropped the block: %v", err)
	}
	if _, err := w.create(t, w.eve, w.id("ask"), 2, nil, nil); err != nil {
		t.Errorf("an ask@2 at the top level, once @3 dropped the block: %v", err)
	}
}

// --- allowed ---

// role:, and in before allowed: someone without the role, in the wrong
// place, is told START_NOT_IN.
func TestStart_AllowedRole(t *testing.T) {
	w := newStartWorld(t)
	w.reg(t, stRoomSrc)
	w.reg(t, stHallSrc)
	w.reg(t, stAskSrc("1", "start:\n  in: [room_{s}]\n  allowed: ['role:lead']\n"))
	room := w.container(t, w.lea, w.id("room"), nil)
	hall := w.container(t, w.lea, w.id("hall"), nil)
	if _, err := w.create(t, w.lea, w.id("ask"), 1, &room, nil); err != nil {
		t.Errorf("lea (lead) in a room: %v", err)
	}
	_, err := w.create(t, w.eve, w.id("ask"), 1, &room, nil)
	assertStartRefused(t, "eve (no role) in a room", err, itemservice.StartNotAllowed, w.id("ask"))
	_, err = w.create(t, w.lea, w.id("ask"), 1, &hall, nil)
	assertStartRefused(t, "lea in a hall", err, itemservice.StartNotIn, w.id("ask"))
	_, err = w.create(t, w.eve, w.id("ask"), 1, &hall, nil)
	assertStartRefused(t, "eve in a hall: in comes first", err, itemservice.StartNotIn, w.id("ask"))
}

// principal:user:<id>.
func TestStart_AllowedPrincipal(t *testing.T) {
	w := newStartWorld(t)
	w.reg(t, stRoomSrc)
	w.reg(t, stAskSrc("1", "start:\n  in: [room_{s}]\n  allowed: ['principal:"+w.bob.Actor+"']\n"))
	room := w.container(t, w.lea, w.id("room"), nil)
	if _, err := w.create(t, w.bob, w.id("ask"), 1, &room, nil); err != nil {
		t.Errorf("bob, named: %v", err)
	}
	_, err := w.create(t, w.lea, w.id("ask"), 1, &room, nil)
	assertStartRefused(t, "lea, not named", err, itemservice.StartNotAllowed, w.id("ask"))
}

// field:<id> reads the container's field as it is now, under the lock: a
// change to it counts at once.
func TestStart_AllowedField(t *testing.T) {
	w := newStartWorld(t)
	w.reg(t, stRoomSrc)
	w.reg(t, stAskSrc("1", "start:\n  in: [room_{s}]\n  allowed: ['field:f_lead', 'field:f_leads']\n"))
	room := w.container(t, w.lea, w.id("room"), map[string]any{"f_lead": w.bob.Actor, "f_leads": []any{w.carl.Actor}})
	if _, err := w.create(t, w.bob, w.id("ask"), 1, &room, nil); err != nil {
		t.Errorf("bob, the room's f_lead: %v", err)
	}
	if _, err := w.create(t, w.carl, w.id("ask"), 1, &room, nil); err != nil {
		t.Errorf("carl, in the room's f_leads: %v", err)
	}
	_, err := w.create(t, w.eve, w.id("ask"), 1, &room, nil)
	assertStartRefused(t, "eve, in neither", err, itemservice.StartNotAllowed, w.id("ask"))
	// The new Item's own fields never count.
	_, err = w.create(t, w.eve, w.id("ask"), 1, &room, map[string]any{"f_title": w.eve.Actor})
	assertStartRefused(t, "eve, naming herself in the new Item", err, itemservice.StartNotAllowed, w.id("ask"))

	if _, err := w.k.Update(testCtx(t), itemservice.UpdateRequest{IdempotencyKey: newKey(), Principal: w.lea, ItemID: room,
		Fields: map[string]any{"f_lead": w.eve.Actor}}); err != nil {
		t.Fatalf("lea makes eve the room's lead: %v", err)
	}
	if _, err := w.create(t, w.eve, w.id("ask"), 1, &room, nil); err != nil {
		t.Errorf("eve, now the room's f_lead: %v", err)
	}
	_, err = w.create(t, w.bob, w.id("ask"), 1, &room, nil)
	assertStartRefused(t, "bob, no longer the room's f_lead", err, itemservice.StartNotAllowed, w.id("ask"))
}

// container_creator: anyone who took part in creating the container,
// judged once for the principal as a whole.
func TestStart_AllowedContainerCreator(t *testing.T) {
	w := newStartWorld(t)
	w.reg(t, stRoomSrc)
	w.reg(t, stAskSrc("1", "start:\n  in: [room_{s}]\n  allowed: [container_creator]\n"))
	bobs := w.container(t, w.bob, w.id("room"), nil)
	if _, err := w.create(t, w.bob, w.id("ask"), 1, &bobs, nil); err != nil {
		t.Errorf("bob, who created the room: %v", err)
	}
	_, err := w.create(t, w.eve, w.id("ask"), 1, &bobs, nil)
	assertStartRefused(t, "eve", err, itemservice.StartNotAllowed, w.id("ask"))

	// An agent creates a room for carl.
	maker := "agent:maker-" + word()
	forCarl := withSession(t, w.k, item.Principal{Actor: maker, OnBehalfOf: w.carl.Actor, Namespace: w.ns})
	carls := w.container(t, forCarl, w.id("room"), nil)
	if _, err := w.create(t, w.carl, w.id("ask"), 1, &carls, nil); err != nil {
		t.Errorf("carl, the room was created for him: %v", err)
	}
	other := withSession(t, w.k, item.Principal{Actor: "agent:other-" + word(), OnBehalfOf: w.carl.Actor, Namespace: w.ns})
	// The agents' asks that must be created carry no fields: ask opens none
	// to agents (fields are closed to agents by default).
	if _, err := w.create(t, other, w.id("ask"), 1, &carls, map[string]any{}); err != nil {
		t.Errorf("another agent acting for carl: %v", err)
	}
	forEve := withSession(t, w.k, item.Principal{Actor: maker, OnBehalfOf: w.eve.Actor, Namespace: w.ns})
	if _, err := w.create(t, forEve, w.id("ask"), 1, &carls, map[string]any{}); err != nil {
		t.Errorf("the agent that created the room, now acting for eve: %v", err)
	}
	forEve2 := withSession(t, w.k, item.Principal{Actor: "agent:third-" + word(), OnBehalfOf: w.eve.Actor, Namespace: w.ns})
	_, err = w.create(t, forEve2, w.id("ask"), 1, &carls, nil)
	assertStartRefused(t, "another agent acting for eve", err, itemservice.StartNotAllowed, w.id("ask"))
}

// stBenchSrc makes an auto inside itself when it starts working; autos are
// started by nobody else.
const stBenchSrc = `
id: bench_{s}
version: 1
initial_state: open
states:
  - {id: open, category: todo}
  - {id: working, category: active}
transitions:
  - {from: open, to: working}
reactions:
  - {id: make, kind: create, on: enter, state: working, type: auto_{s}, version: 1, fields: {f_title: '"made"'}}
`

// nobody: no person, no agent, no customer; a create reaction, by the
// target's consent, still creates.
func TestStart_AllowedNobody(t *testing.T) {
	w := newStartWorld(t)
	w.reg(t, stBenchSrc)
	w.reg(t, `
id: auto_{s}
version: 1
fields:
  - {id: f_title, type: string}
customer_fields: [f_title]
accepts_reactions:
  bench_{s}:make: {creates: [f_title]}
start:
  in: [bench_{s}, portal_{s}]
  allowed: [nobody]
`)
	// A portal that owns autos: the load check refuses it (CONFLICT), but
	// RegisterType does not run the checks of the Types together, so the
	// kernel must refuse at run time too.
	w.reg(t, "\nid: portal_{s}\nversion: 1\nown: [auto_{s}]\n")
	bench := w.container(t, w.lea, w.id("bench"), nil)
	portal := w.container(t, w.lea, w.id("portal"), nil)

	_, err := w.create(t, w.lea, w.id("auto"), 1, &bench, nil)
	assertStartRefused(t, "lea (lead)", err, itemservice.StartNotAllowed, w.id("auto"))
	forLea := withSession(t, w.k, item.Principal{Actor: "agent:a-" + word(), OnBehalfOf: w.lea.Actor, Namespace: w.ns})
	_, err = w.create(t, forLea, w.id("auto"), 1, &bench, nil)
	assertStartRefused(t, "an agent for lea", err, itemservice.StartNotAllowed, w.id("auto"))
	_, err = w.create(t, w.kim, w.id("auto"), 1, &portal, nil)
	assertStartRefused(t, "kim, in a portal that owns autos", err, itemservice.StartNotAllowed, w.id("auto"))

	if _, err := w.k.Transition(testCtx(t), itemservice.TransitionRequest{IdempotencyKey: newKey(), Principal: w.lea,
		ItemID: bench, ToState: "working"}); err != nil {
		t.Fatalf("lea starts the bench: %v", err)
	}
	out, err := w.k.Internal().ReactCreate(testCtx(t), entered(bench, 2, "make"))
	if err != nil || out.Outcome != itemservice.ReactionApplied || out.Target == uuid.Nil {
		t.Fatalf("the bench's create reaction = %+v, %v; want applied", out, err)
	}
}

// --- agents, customers, a requester ---

// An agent and the user it acts for must both pass role:.
func TestStart_Agents(t *testing.T) {
	w := newStartWorld(t)
	w.reg(t, stRoomSrc)
	w.reg(t, stAskSrc("1", "start:\n  in: [room_{s}]\n  allowed: ['role:lead']\n"))
	room := w.container(t, w.lea, w.id("room"), nil)
	agent := func(holds bool, user item.Principal) item.Principal {
		p := item.Principal{Actor: "agent:a-" + word(), OnBehalfOf: user.Actor, Namespace: w.ns}
		if holds {
			grantRoleIn(t, w.ns, p, "lead")
		}
		return withSession(t, w.k, p)
	}
	// With no fields: ask opens none to agents (closed by default).
	if _, err := w.create(t, agent(true, w.lea), w.id("ask"), 1, &room, map[string]any{}); err != nil {
		t.Errorf("an agent with lead, for lea (lead): %v", err)
	}
	_, err := w.create(t, agent(false, w.lea), w.id("ask"), 1, &room, nil)
	assertStartRefused(t, "an agent without lead, for lea", err, itemservice.StartNotAllowed, w.id("ask"))
	_, err = w.create(t, agent(true, w.eve), w.id("ask"), 1, &room, nil)
	assertStartRefused(t, "an agent with lead, for eve", err, itemservice.StartNotAllowed, w.id("ask"))
}

// Customers: allowed does not judge them (their gate is own and shared),
// in does.
func TestStart_Customers(t *testing.T) {
	w := newStartWorld(t)
	w.reg(t, stRoomSrc)
	w.reg(t, stAskSrc("1", "start:\n  in: [room_{s}]\n  allowed: ['role:lead']\n"))
	// A hall that owns asks, though the ask's in excludes it: refused at
	// load (START_NOT_IN on own), not by RegisterType; the kernel refuses
	// at run time too.
	w.reg(t, "\nid: hall_{s}\nversion: 1\nown: [ask_{s}]\n")
	room := w.container(t, w.lea, w.id("room"), nil)
	hall := w.container(t, w.lea, w.id("hall"), nil)

	if _, err := w.create(t, w.kim, w.id("ask"), 1, &room, nil); err != nil {
		t.Errorf("kim in a room, which owns asks: %v", err)
	}
	bot := item.Principal{Actor: "agent:portal-" + word(), OnBehalfOf: w.kim.Actor, Namespace: w.ns}
	all := []uuid.UUID{uuid.Nil}
	session, err := w.k.StartAgentSession(testCtx(t), itemservice.StartAgentSessionRequest{
		Principal: bot, Scopes: all, Powers: customerPowers, Grant: &itemservice.Grant{Scopes: all, Powers: customerPowers}})
	if err != nil {
		t.Fatalf("a portal bot for kim: %v", err)
	}
	bot.AgentSession = session
	if _, err := w.create(t, bot, w.id("ask"), 1, &room, nil); err != nil {
		t.Errorf("kim's portal bot in a room: %v", err)
	}
	_, err = w.create(t, w.kim, w.id("ask"), 1, &hall, nil)
	assertStartRefused(t, "kim in a hall that owns asks the ask's in excludes", err, itemservice.StartNotIn, w.id("ask"))
}

// A person opening an Item for a customer (requester) is judged as the
// person.
func TestStart_ARequesterIsJudgedAsThePerson(t *testing.T) {
	w := newStartWorld(t)
	w.reg(t, stRoomSrc)
	w.reg(t, stAskSrc("1", "start:\n  in: [room_{s}]\n  allowed: ['role:lead']\n"))
	room := w.container(t, w.lea, w.id("room"), nil)
	open := func(p item.Principal) error {
		_, err := w.k.Create(testCtx(t), itemservice.CreateRequest{IdempotencyKey: newKey(), Principal: p, TypeID: w.id("ask"),
			TypeVersion: 1, ContainerID: &room, Requester: w.kim.Actor, Fields: map[string]any{"f_title": "phoned in"}})
		return err
	}
	if err := open(w.lea); err != nil {
		t.Errorf("lea (lead) opens one for kim: %v", err)
	}
	assertStartRefused(t, "eve opens one for kim", open(w.eve), itemservice.StartNotAllowed, w.id("ask"))
}

// --- the order of the checks ---

// What is NOT_FOUND stays NOT_FOUND: a container the caller cannot see,
// and one outside an agent session's scopes.
func TestStart_Order_NotFoundFirst(t *testing.T) {
	w := newStartWorld(t)
	w.reg(t, stRoomSrc)
	w.reg(t, stHallSrc)
	w.reg(t, stAskSrc("1", "start:\n  in: [hall_{s}]\n  allowed: ['role:lead']\n"))
	private := createRoom(t, w, w.lea, &itemservice.Narrow{Readers: readers(w.lea)})
	_, err := w.create(t, w.eve, w.id("ask"), 1, &private, nil)
	assertNotFound(t, "eve in lea's private room", err)

	hall := w.container(t, w.lea, w.id("hall"), nil)
	room := w.container(t, w.lea, w.id("room"), nil)
	agent := scoped(t, w.k, w.lea, []uuid.UUID{hall}, item.PowerRead, item.PowerCreate)
	_, err = w.create(t, agent, w.id("ask"), 1, &room, nil)
	assertNotFound(t, "an agent scoped to the hall, in a room", err)
}

func createRoom(t *testing.T, w startWorld, p item.Principal, narrow *itemservice.Narrow) uuid.UUID {
	t.Helper()
	res, err := w.k.Create(testCtx(t), itemservice.CreateRequest{IdempotencyKey: newKey(), Principal: p,
		TypeID: w.id("room"), TypeVersion: 1, Narrow: narrow})
	if err != nil {
		t.Fatalf("a room: %v", err)
	}
	return res.ItemID
}

// The start block comes before the rest: the write and create powers, the
// agent write rule, placement for a narrow, and the invariants.
func TestStart_Order_BeforeTheRest(t *testing.T) {
	w := newStartWorld(t)
	w.reg(t, stRoomSrc)
	w.reg(t, stHallSrc)
	w.reg(t, `
id: strict_{s}
version: 1
fields:
  - {id: f_title, type: string}
invariants:
  - {id: title_missing, rule: 'f_title != null'}
placement_writable_by: ['role:boss']
start:
  in: [room_{s}]
`)
	w.reg(t, "\nid: plain_{s}\nversion: 1\n")
	hall := w.container(t, w.lea, w.id("hall"), nil)

	// A read-only session, and a session without create.
	all := []uuid.UUID{uuid.Nil}
	for _, powers := range [][]string{{item.PowerRead}, {item.PowerRead, item.PowerEditFields}} {
		agent := scoped(t, w.k, w.lea, all, powers...)
		_, err := w.create(t, agent, w.id("strict"), 1, &hall, nil)
		assertStartRefused(t, "an agent with "+strings.Join(powers, ","), err, itemservice.StartNotIn, w.id("strict"))
	}

	// The agent write rule: an agent that read lea's private room would
	// need an approval to create in the hall.
	withCreate := scoped(t, w.k, w.lea, all, item.PowerRead, item.PowerCreate)
	private := createRoom(t, w, w.lea, &itemservice.Narrow{Readers: []string{w.lea.Actor, withCreate.Actor}})
	if _, err := w.k.Get(testCtx(t), withCreate, private); err != nil {
		t.Fatalf("the agent reads the private room: %v", err)
	}
	_, err := w.create(t, withCreate, w.id("plain"), 1, &hall, map[string]any{})
	var na *itemservice.NeedsApprovalError
	if !errors.As(err, &na) {
		t.Fatalf("a plain Item in the hall: %v, want NEEDS_APPROVAL (the world must need it)", err)
	}
	_, err = w.create(t, withCreate, w.id("strict"), 1, &hall, nil)
	assertStartRefused(t, "a strict Item in the hall, which would need an approval", err, itemservice.StartNotIn, w.id("strict"))

	// Placement for a narrow (eve does not hold boss), and the invariant
	// (no title).
	_, err = w.k.Create(testCtx(t), itemservice.CreateRequest{IdempotencyKey: newKey(), Principal: w.eve, TypeID: w.id("strict"),
		TypeVersion: 1, ContainerID: &hall, Fields: map[string]any{"f_title": "x"}, Narrow: &itemservice.Narrow{Readers: readers(w.eve)}})
	assertStartRefused(t, "eve narrows a strict Item in the hall", err, itemservice.StartNotIn, w.id("strict"))
	_, err = w.create(t, w.eve, w.id("strict"), 1, nil, map[string]any{})
	assertStartRefused(t, "a strict Item with no title at the top level", err, itemservice.StartNotIn, w.id("strict"))
}

// --- under the container's lock ---

// A create judged by field: races an update that takes the principal off
// the container's field. The container is locked for the check, so a
// create that succeeds committed before the update: its commit_seq is
// lower.
func TestStart_ARaceWithTheContainersField(t *testing.T) {
	w := newStartWorld(t)
	w.reg(t, stRoomSrc)
	w.reg(t, stAskSrc("1", "start:\n  in: [room_{s}]\n  allowed: ['field:f_lead']\n"))
	created, refused := 0, 0
	for round := range 20 {
		room := w.container(t, w.lea, w.id("room"), map[string]any{"f_lead": w.bob.Actor})
		var (
			wg         sync.WaitGroup
			cres, ures itemservice.WriteResult
			cerr, uerr error
		)
		wg.Add(2)
		go func() {
			defer wg.Done()
			cres, cerr = w.create(t, w.bob, w.id("ask"), 1, &room, nil)
		}()
		go func() {
			defer wg.Done()
			ures, uerr = w.k.Update(testCtx(t), itemservice.UpdateRequest{IdempotencyKey: newKey(), Principal: w.lea, ItemID: room,
				Fields: map[string]any{"f_lead": w.carl.Actor}})
		}()
		wg.Wait()
		if uerr != nil {
			t.Fatalf("round %d: the update: %v", round, uerr)
		}
		switch {
		case cerr == nil:
			created++
			if cres.CommitSeq >= ures.CommitSeq {
				t.Errorf("round %d: bob's create committed at %d, after the update that took him off (%d)", round, cres.CommitSeq, ures.CommitSeq)
			}
		default:
			refused++
			assertStartRefused(t, "bob, racing the update", cerr, itemservice.StartNotAllowed, w.id("ask"))
		}
	}
	t.Logf("%d created, %d refused", created, refused)
}
