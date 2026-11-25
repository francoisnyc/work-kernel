// Tests for track 1, item 6, step 1: a create names the latest version.
//
// The first bake-off's hole (docs/research/camunda-bakeoff.md, test 1):
// after purchase@2 added the CFO's approval, a requester created a
// purchase@1 by naming it, and it was approved without the CFO.
//
//   - A create on the public door must name the latest registered version
//     of its Type, unless that version's start block keeps the one named
//     open: start: {older_versions: [1, 2]}, or older_versions: all.
//     Absent: the latest only (fail closed).
//   - It holds for a person, an agent, a customer, an agent acting for a
//     customer and a person creating for a customer. A create reaction is
//     the kernel's and is exempt: an Item in flight keeps making the
//     children its own version names.
//   - The refusal is an ErrInvalid, an *InvalidError on the path "type"
//     (over the API: INVALID_ARGUMENT / INVALID_REQUEST with a field
//     violation on type). Its message names no Type, version, principal or
//     Item. A refused create writes nothing and uses up no idempotency key.
//   - Which version is latest is read in the create's own transaction: a
//     version another process registers counts at once.
//   - A retry of an earlier create, with its key, still answers with what
//     that create made, even if a newer version came since.
//   - The order: what is NOT_FOUND first; then this; then start.in and
//     start.allowed.
//   - Items already on an older version are untouched: they keep their
//     version and its lifecycle.
//
// Types are global and every test of this package shares one Postgres:
// each test registers Types of its own (startWorld's suffix), so the
// versions it registers judge no create elsewhere, and what is "latest"
// here never depends on another test.
package itemservice_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
)

// assertVersionClosed checks the refusal of a create that names a version
// closed to new Items.
func assertVersionClosed(t *testing.T, what string, err error) {
	t.Helper()
	if !errors.Is(err, itemservice.ErrInvalid) {
		t.Errorf("%s: error = %v, want ErrInvalid (the version is closed to new Items)", what, err)
		return
	}
	// errors.As looks down the chain of wrapped errors for one of this
	// type, as a Java catch of a subclass would.
	var inv *itemservice.InvalidError
	if !errors.As(err, &inv) {
		t.Errorf("%s: error %v is not an *InvalidError", what, err)
		return
	}
	if inv.Path != "type" {
		t.Errorf("%s: the refusal is on the path %q, want type", what, inv.Path)
	}
	if !strings.Contains(inv.Reason, "latest version") {
		t.Errorf("%s: the reason %q does not say to create the latest version", what, inv.Reason)
	}
	for _, leak := range []string{"user:", "customer:", "agent:", "namespaces/", "_"} {
		if strings.Contains(inv.Reason, leak) {
			t.Errorf("%s: the reason %q names a principal, an Item or a Type (%s)", what, inv.Reason, leak)
		}
	}
}

// latestPurchaseSrc is a purchase: finance approves it; from version 2 on,
// over 5,000 the CFO does (the first bake-off's change, reduced).
func latestPurchaseSrc(version int) string {
	if version == 1 {
		return `
id: pur_{s}
version: 1
initial_state: review
fields:
  - {id: f_amount, type: int}
states:
  - {id: review, category: active}
  - {id: approved, category: done}
transitions:
  - {from: review, to: approved, allowed: ['role:finance'], except: [creator]}
`
	}
	return `
id: pur_{s}
version: 2
initial_state: review
fields:
  - {id: f_amount, type: int}
states:
  - {id: review, category: active}
  - {id: cfo_review, category: waiting}
  - {id: approved, category: done}
transitions:
  - {from: review, to: approved, allowed: ['role:finance'], except: [creator], guard: 'f_amount <= 5000'}
  - {from: review, to: cfo_review, allowed: ['role:finance'], except: [creator], guard: 'f_amount > 5000'}
  - {from: cfo_review, to: approved, allowed: ['role:cfo'], except: [creator]}
`
}

// The bake-off's row: once purchase@2 is live, ana cannot create a
// purchase@1 and so skip the CFO. The purchase she had in flight keeps its
// version and its rules.
func TestCreateLatest_TheBakeOffRow_AnOlderVersionIsRefused(t *testing.T) {
	w := newStartWorld(t)
	ana, fay := w.eve, w.bob
	grantRoleIn(t, w.ns, fay, "finance")
	six := map[string]any{"f_amount": 6000}
	w.reg(t, latestPurchaseSrc(1))

	res, err := w.create(t, ana, w.id("pur"), 1, nil, six)
	inFlight := mustCreate(t, "a purchase@1 before the change", res, err)

	w.reg(t, latestPurchaseSrc(2)) // the change: over 5,000, the CFO too

	key := newKey()
	_, err = w.k.Create(testCtx(t), itemservice.CreateRequest{IdempotencyKey: key, Principal: ana,
		TypeID: w.id("pur"), TypeVersion: 1, Fields: six})
	assertVersionClosed(t, "ana asks for purchase@1 by name, after the change", err)
	if n := count(t, `select count(*) from items where type_id = $1`, w.id("pur")); n != 1 {
		t.Errorf("purchases = %d, want 1 (the one in flight): a refused create writes nothing", n)
	}
	if n := count(t, `select count(*) from idempotency where actor = $1 and key = $2`, ana.Actor, key); n != 0 {
		t.Errorf("idempotency rows for the refused key = %d, want 0", n)
	}

	// The latest version is created, and over 5,000 finance cannot approve it.
	res, err = w.create(t, ana, w.id("pur"), 2, nil, six)
	fresh := mustCreate(t, "a purchase@2", res, err)
	move := func(p item.Principal, id uuid.UUID, to string) error {
		_, err := w.k.Transition(testCtx(t), itemservice.TransitionRequest{IdempotencyKey: newKey(), Principal: p, ItemID: id, ToState: to})
		return err
	}
	if err := move(fay, fresh, "approved"); !errors.Is(err, itemservice.ErrRejected) {
		t.Errorf("fay approves a 6,000 purchase@2: %v, want ErrRejected (the CFO's step)", err)
	}
	if err := move(fay, fresh, "cfo_review"); err != nil {
		t.Errorf("fay sends the purchase@2 to the CFO: %v", err)
	}

	// The purchase in flight is untouched: still purchase@1, with its rules.
	got, err := w.k.Get(testCtx(t), ana, inFlight)
	if err != nil || got.TypeVersion != 1 || got.State != "review" {
		t.Fatalf("the purchase in flight: version %d, state %q, %v; want purchase@1 in review", got.TypeVersion, got.State, err)
	}
	if err := move(fay, inFlight, "approved"); err != nil {
		t.Errorf("fay approves the purchase@1 in flight: %v (moving it to @2 is a later step)", err)
	}
}

// Everyone on the public door is refused an older version: a person, an
// agent, a customer, a customer's agent, and a person creating for a
// customer. Each creates the latest.
func TestCreateLatest_EveryPublicCallerIsRefused(t *testing.T) {
	w := newStartWorld(t)
	w.reg(t, stRoomSrc)
	w.reg(t, stAskSrc("1", ""))
	w.reg(t, stAskSrc("2", ""))
	room := w.container(t, w.lea, w.id("room"), nil)

	forLea := withSession(t, w.k, item.Principal{Actor: "agent:a-" + word(), OnBehalfOf: w.lea.Actor, Namespace: w.ns})
	bot := item.Principal{Actor: "agent:portal-" + word(), OnBehalfOf: w.kim.Actor, Namespace: w.ns}
	all := []uuid.UUID{uuid.Nil}
	session, err := w.k.StartAgentSession(testCtx(t), itemservice.StartAgentSessionRequest{
		Principal: bot, Scopes: all, Powers: customerPowers, Grant: &itemservice.Grant{Scopes: all, Powers: customerPowers}})
	if err != nil {
		t.Fatalf("a portal bot for kim: %v", err)
	}
	bot.AgentSession = session

	callers := []struct {
		name   string
		p      item.Principal
		fields map[string]any // an agent sets none: ask opens no field to agents
	}{
		{"a person", w.eve, nil},
		{"an agent acting for a person", forLea, map[string]any{}},
		{"a customer", w.kim, nil},
		{"a customer's agent", bot, nil},
	}
	for _, c := range callers {
		_, err := w.create(t, c.p, w.id("ask"), 1, &room, c.fields)
		assertVersionClosed(t, c.name+" creates ask@1", err)
		if _, err := w.create(t, c.p, w.id("ask"), 2, &room, c.fields); err != nil {
			t.Errorf("%s creates ask@2: %v", c.name, err)
		}
	}
	open := func(version int) error {
		_, err := w.k.Create(testCtx(t), itemservice.CreateRequest{IdempotencyKey: newKey(), Principal: w.lea, TypeID: w.id("ask"),
			TypeVersion: version, ContainerID: &room, Requester: w.kim.Actor, Fields: map[string]any{"f_title": "phoned in"}})
		return err
	}
	assertVersionClosed(t, "lea opens an ask@1 for kim", open(1))
	if err := open(2); err != nil {
		t.Errorf("lea opens an ask@2 for kim: %v", err)
	}
	if n := count(t, `select count(*) from items where type_id = $1 and type_version = 1`, w.id("ask")); n != 0 {
		t.Errorf("asks of version 1 = %d, want 0", n)
	}
}

// The latest version says which older versions stay open: a list, or all.
// The Item is pinned to the version named. It is the latest version's
// word only: a newer version without the key closes them again.
func TestCreateLatest_TheLatestVersionKeepsOlderOnesOpen(t *testing.T) {
	w := newStartWorld(t)
	w.reg(t, stAskSrc("1", ""))
	w.reg(t, stAskSrc("2", ""))
	try := func(version int) (uuid.UUID, error) {
		res, err := w.create(t, w.eve, w.id("ask"), version, nil, nil)
		return res.ItemID, err
	}
	wantOpen := func(when string, versions ...int) {
		t.Helper()
		for _, v := range versions {
			id, err := try(v)
			if err != nil {
				t.Errorf("%s, an ask@%d: %v", when, v, err)
				continue
			}
			if got, err := w.k.Get(testCtx(t), w.eve, id); err != nil || got.TypeVersion != v {
				t.Errorf("%s, the ask created as @%d: version %d, %v; want pinned to %d", when, v, got.TypeVersion, err, v)
			}
		}
	}
	wantClosed := func(when string, versions ...int) {
		t.Helper()
		for _, v := range versions {
			_, err := try(v)
			assertVersionClosed(t, when+", an ask@"+string(rune('0'+v)), err)
		}
	}

	w.reg(t, stAskSrc("3", "start:\n  older_versions: [2]\n"))
	wantOpen("@3 lists [2]", 2, 3)
	wantClosed("@3 lists [2]", 1)

	w.reg(t, stAskSrc("4", "start:\n  older_versions: all\n"))
	wantOpen("@4 says all", 1, 2, 3, 4)

	// A block with in, allowed and the key: the older version is open, and
	// the block's own rules judge it as they judge the latest.
	w.reg(t, stAskSrc("5", "start:\n  in: [top]\n  allowed: ['role:lead']\n  older_versions: [1]\n"))
	if _, err := w.create(t, w.lea, w.id("ask"), 1, nil, nil); err != nil {
		t.Errorf("@5 lists [1], lea (lead) creates an ask@1: %v", err)
	}
	_, err := try(1)
	assertStartRefused(t, "@5 lists [1], eve (no role) creates an ask@1", err, itemservice.StartNotAllowed, w.id("ask"))
	wantClosed("@5 lists [1]", 2, 3, 4)

	w.reg(t, stAskSrc("6", ""))
	wantOpen("@6 has no block", 6)
	wantClosed("@6 has no block", 1, 2, 3, 4, 5)
}

// A Type with one version, and the latest of several: as before.
func TestCreateLatest_TheLatestVersion_AsBefore(t *testing.T) {
	w := newStartWorld(t)
	w.reg(t, stAskSrc("1", ""))
	if _, err := w.create(t, w.eve, w.id("ask"), 1, nil, nil); err != nil {
		t.Errorf("an ask@1, the only version: %v", err)
	}
	// A version that is not registered is refused as before: not this rule's.
	_, err := w.create(t, w.eve, w.id("ask"), 2, nil, nil)
	if !errors.Is(err, itemservice.ErrInvalid) || !strings.Contains(err.Error(), "not registered") {
		t.Errorf("an ask@2, not registered: %v; want ErrInvalid, not registered", err)
	}
}

// A create reaction is the kernel's own create and is exempt: a bench
// whose reaction names auto@1 still makes one after auto@2 is registered,
// though no person creates an auto@1 any more.
func TestCreateLatest_AReactionStillCreatesTheVersionItNames(t *testing.T) {
	w := newStartWorld(t)
	w.reg(t, stBenchSrc)
	auto := func(version string) string {
		return "\nid: auto_{s}\nversion: " + version + "\nfields:\n  - {id: f_title, type: string}\n" +
			"accepts_reactions:\n  bench_{s}:make: {creates: [f_title]}\n"
	}
	w.reg(t, auto("1"))
	w.reg(t, auto("2"))
	bench := w.container(t, w.lea, w.id("bench"), nil)

	_, err := w.create(t, w.lea, w.id("auto"), 1, &bench, nil)
	assertVersionClosed(t, "lea creates an auto@1", err)

	if _, err := w.k.Transition(testCtx(t), itemservice.TransitionRequest{IdempotencyKey: newKey(), Principal: w.lea,
		ItemID: bench, ToState: "working"}); err != nil {
		t.Fatalf("lea starts the bench: %v", err)
	}
	out, err := w.k.Internal().ReactCreate(testCtx(t), entered(bench, 2, "make"))
	if err != nil || out.Outcome != itemservice.ReactionApplied || out.Target == uuid.Nil {
		t.Fatalf("the bench's create reaction = %+v, %v; want applied", out, err)
	}
	if n := count(t, `select count(*) from items where id = $1 and type_id = $2 and type_version = 1`, out.Target, w.id("auto")); n != 1 {
		t.Errorf("the Item the reaction made is not an auto@1")
	}
}

// Which version is latest is read in the create's own transaction, never
// cached: a version another process registers closes the older one at once.
func TestCreateLatest_AVersionRegisteredElsewhereCountsAtOnce(t *testing.T) {
	w := newStartWorld(t)
	w.reg(t, stAskSrc("1", ""))
	// Warm whatever this service caches about ask@1.
	if _, err := w.create(t, w.eve, w.id("ask"), 1, nil, nil); err != nil {
		t.Fatalf("an ask@1 before @2: %v", err)
	}
	// Another process: a second Service on the same database, with its
	// own Type cache.
	other := itemservice.New(pool)
	registerInline(t, other, strings.ReplaceAll(stAskSrc("2", ""), "{s}", w.s))

	_, err := w.create(t, w.eve, w.id("ask"), 1, nil, nil)
	assertVersionClosed(t, "an ask@1, once another process registered @2", err)
	if _, err := w.create(t, w.eve, w.id("ask"), 2, nil, nil); err != nil {
		t.Errorf("an ask@2, registered by another process: %v", err)
	}
}

// A retry of a create that succeeded, with its idempotency key, answers
// with what that create made, even though a newer version has closed the
// version since: the write happened, and a retry must not turn it into a
// refusal.
func TestCreateLatest_ARetryOfAnEarlierCreateStillAnswers(t *testing.T) {
	w := newStartWorld(t)
	w.reg(t, stAskSrc("1", ""))
	req := itemservice.CreateRequest{IdempotencyKey: newKey(), Principal: w.eve, TypeID: w.id("ask"), TypeVersion: 1,
		Fields: map[string]any{"f_title": "sent twice"}}
	first, err := w.k.Create(testCtx(t), req)
	if err != nil {
		t.Fatalf("the create: %v", err)
	}
	w.reg(t, stAskSrc("2", ""))

	again, err := w.k.Create(testCtx(t), req)
	if err != nil {
		t.Fatalf("the retry, after @2 was registered: %v; want the first answer", err)
	}
	if again.ItemID != first.ItemID || again.CommitSeq != first.CommitSeq {
		t.Errorf("the retry answered Item %s at %d, want the first answer: %s at %d", again.ItemID, again.CommitSeq, first.ItemID, first.CommitSeq)
	}
	// The same request under a new key is a new create, and is refused.
	req.IdempotencyKey = newKey()
	_, err = w.k.Create(testCtx(t), req)
	assertVersionClosed(t, "the same create under a new key", err)
}

// The order: what is NOT_FOUND stays NOT_FOUND; then the version; then
// start.in and start.allowed.
func TestCreateLatest_Order(t *testing.T) {
	w := newStartWorld(t)
	w.reg(t, stRoomSrc)
	w.reg(t, stHallSrc)
	w.reg(t, stAskSrc("1", ""))
	w.reg(t, stAskSrc("2", "start:\n  in: [room_{s}]\n  allowed: ['role:lead']\n"))

	// A container the caller cannot see: not found, whatever the version.
	private := createRoom(t, w, w.lea, &itemservice.Narrow{Readers: readers(w.lea)})
	_, err := w.create(t, w.eve, w.id("ask"), 1, &private, nil)
	assertNotFound(t, "eve creates an ask@1 in lea's private room", err)

	// A closed version in a place the block refuses, by someone it
	// refuses: the version is said first.
	hall := w.container(t, w.lea, w.id("hall"), nil)
	room := w.container(t, w.lea, w.id("room"), nil)
	_, err = w.create(t, w.eve, w.id("ask"), 1, &hall, nil)
	assertVersionClosed(t, "eve creates an ask@1 in a hall", err)
	_, err = w.create(t, w.eve, w.id("ask"), 1, &room, nil)
	assertVersionClosed(t, "eve (no role) creates an ask@1 in a room", err)

	// The latest version meets the block as before.
	_, err = w.create(t, w.eve, w.id("ask"), 2, &hall, nil)
	assertStartRefused(t, "eve creates an ask@2 in a hall", err, itemservice.StartNotIn, w.id("ask"))
	_, err = w.create(t, w.eve, w.id("ask"), 2, &room, nil)
	assertStartRefused(t, "eve (no role) creates an ask@2 in a room", err, itemservice.StartNotAllowed, w.id("ask"))
	if _, err := w.create(t, w.lea, w.id("ask"), 2, &room, nil); err != nil {
		t.Errorf("lea (lead) creates an ask@2 in a room: %v", err)
	}
}

// Items already on an older version are untouched by a newer one: they
// keep their version, are read and written as before.
func TestCreateLatest_ItemsOnAnOlderVersionCarryOn(t *testing.T) {
	w := newStartWorld(t)
	w.reg(t, stAskSrc("1", ""))
	res, err := w.create(t, w.eve, w.id("ask"), 1, nil, nil)
	id := mustCreate(t, "an ask@1", res, err)
	w.reg(t, stAskSrc("2", ""))

	if _, err := w.k.Update(testCtx(t), itemservice.UpdateRequest{IdempotencyKey: newKey(), Principal: w.eve, ItemID: id,
		Fields: map[string]any{"f_title": "still mine"}}); err != nil {
		t.Errorf("an update of the ask@1, after @2: %v", err)
	}
	got, err := w.k.Get(testCtx(t), w.eve, id)
	if err != nil || got.TypeVersion != 1 || got.Fields["f_title"] != "still mine" {
		t.Errorf("the ask@1 after @2: version %d, fields %v, %v; want version 1 with the update", got.TypeVersion, got.Fields, err)
	}
}
