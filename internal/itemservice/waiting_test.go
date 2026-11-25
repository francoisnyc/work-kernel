// Tests for "waiting on me" (docs/roadmap.md, track 1, item 5, step 1): a
// filter on Search and List, Filter.WaitingOn, that keeps the open Items
// the caller may act on now ("me"), or a role's queue ("role:<name>"), for
// every Type with no per-Type code.
//
// The view is derived when it is asked, from the moves out of each Item's
// state in the Item's own Type version (policy.WaitsOn is the rule; the
// last test holds the view to it):
//
//   - a move counts when its `allowed` names a role, a person or a field;
//     an empty `allowed`, a creator-only move, nobody's and the kernel's do
//     not; states in category done or canceled never wait;
//   - roles are read when the view is asked: granted, the queue shows at
//     once; revoked, it is gone;
//   - `except` is applied to the person asking;
//   - an Item with an open hold is left out: it waits on a check, not on a
//     person;
//   - guards, required and given fields are not evaluated;
//   - the view never shows an Item the caller cannot see: visibility is
//     applied before the limit, and every Item goes through policy.Project;
//   - an agent's view is what its session may act on: the agent and its
//     user both pass, within the session's scopes;
//   - a customer's waiting_on is refused (until a Type can say a state
//     waits on its requester).
//
// The first test is the bake-offs' test 3 ("one inbox":
// docs/research/camunda-bakeoff.md): six requests of two Types, four of
// them in fay's inbox.
package itemservice_test

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
	"github.com/francoisnyc/work-kernel/internal/policy"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// wq_purchase@1 is the bake-off's purchase cut to who decides: the manager
// a field names, then finance from a shared queue. Its last move is a
// reopen out of a done state. Version 2 gives the queue to the board.
const wqPurchaseSrc = `
id: wq_purchase
version: %d
initial_state: draft
fields:
  - {id: f_amount,     type: int}
  - {id: f_vendor,     type: string}
  - {id: f_manager,    type: principal}
  - {id: f_claimed_by, type: principal, writable_by: ['role:wq_finance', 'role:wq_board']}
states:
  - {id: draft,          category: todo}
  - {id: manager_review, category: waiting, frozen: [f_amount, f_vendor]}
  - {id: finance_queue,  category: waiting}
  - {id: finance_review, category: active}
  - {id: approved,       category: done}
  - {id: rejected,       category: canceled}
transitions:
  - {from: draft,          to: manager_review, allowed: [creator], required: [f_amount, f_manager]}
  - {from: manager_review, to: draft,          allowed: [creator]}
  - {from: manager_review, to: approved,       allowed: ['field:f_manager'], except: [creator], guard: 'f_amount < 1000'}
  - {from: manager_review, to: finance_queue,  allowed: ['field:f_manager'], except: [creator], guard: 'f_amount >= 1000'}
  - {from: manager_review, to: rejected,       allowed: ['field:f_manager'], except: [creator]}
  - {from: finance_queue,  to: finance_review, allowed: ['role:%[2]s'], except: [creator], required: [f_claimed_by]}
  - {from: finance_review, to: finance_queue,  allowed: ['role:%[2]s'], clears: [f_claimed_by]}
  - {from: finance_review, to: approved,       allowed: ['role:%[2]s'], except: [creator]}
  - {from: finance_review, to: rejected,       allowed: ['role:%[2]s'], except: [creator]}
  - {from: approved,       to: finance_review, allowed: ['role:%[2]s']}
  - {from: rejected,       to: draft,          allowed: ['field:f_manager']}
`

// wq_access is the bake-off's second kind: the owner a field names decides.
const wqAccessSrc = `
id: wq_access
version: 1
initial_state: draft
fields:
  - {id: f_system, type: string}
  - {id: f_owner,  type: principal}
states:
  - {id: draft,        category: todo}
  - {id: owner_review, category: waiting, frozen: [f_owner]}
  - {id: approved,     category: done}
  - {id: rejected,     category: canceled}
transitions:
  - {from: draft,        to: owner_review, allowed: [creator], required: [f_owner]}
  - {from: owner_review, to: approved,     allowed: ['field:f_owner'], except: [creator]}
  - {from: owner_review, to: rejected,     allowed: ['field:f_owner'], except: [creator]}
`

// wq_note has the moves that do not count: open to anyone, and the
// creator's own. From parked, one move names the creator beside a role.
const wqNoteSrc = `
id: wq_note
version: 1
initial_state: open
fields:
  - {id: f_text, type: string}
states:
  - {id: open,   category: active}
  - {id: parked, category: waiting}
  - {id: shared, category: active}
  - {id: closed, category: done}
transitions:
  - {from: open,   to: parked}
  - {from: open,   to: closed, allowed: [creator]}
  - {from: parked, to: shared, allowed: [creator, 'role:wq_finance']}
  - {from: shared, to: closed}
`

// wq_held: finance's move out of asked is checked by a hook, so asking for
// it opens a hold.
const wqHeldSrc = `
id: wq_held
version: 1
initial_state: asked
fields:
  - {id: f_customer_id, type: string}
  - {id: f_amount,      type: int}
  - {id: f_cost_center, type: string, writable_by: kernel}
states:
  - {id: asked,   category: waiting}
  - {id: granted, category: done}
transitions:
  - {from: asked, to: granted, allowed: ['role:wq_finance']}
hooks:
  - {id: credit, on: transition, from: asked, to: granted, kind: check, action: erp.check_credit,
     reads: [f_customer_id, f_amount], writes: [f_cost_center]}
`

// wq_desk is a container with no lifecycle.
const wqDeskSrc = `
id: wq_desk
version: 1
fields:
  - {id: f_name, type: string}
`

// waitWorld: a namespace of its own, so that nothing another test made is
// in anyone's view. ana asks; bob is her manager; fay and gus are in
// finance; olaf is a member of staff with no role.
type waitWorld struct {
	k                        *itemservice.Service
	ns                       string
	ana, bob, fay, gus, olaf item.Principal
	types                    map[string]*typedef.Type // by id@version
	roles                    map[string][]string      // by principal ID
	made                     []uuid.UUID              // every Item the world created
}

func newWaitWorld(t *testing.T) *waitWorld {
	t.Helper()
	k := itemservice.New(pool)
	w := &waitWorld{k: k, types: map[string]*typedef.Type{}, roles: map[string][]string{}}
	for _, src := range []string{
		fmt.Sprintf(wqPurchaseSrc, 1, "wq_finance"), fmt.Sprintf(wqPurchaseSrc, 2, "wq_board"),
		wqAccessSrc, wqNoteSrc, wqHeldSrc, wqDeskSrc,
	} {
		ty, err := typedef.Parse([]byte(src))
		if err != nil {
			t.Fatalf("parse: %v\n%s", err, src)
		}
		if err := k.RegisterType(testCtx(t), ty); err != nil {
			t.Fatalf("register %s@%d: %v", ty.ID, ty.Version, err)
		}
		w.types[fmt.Sprintf("%s@%d", ty.ID, ty.Version)] = ty
	}
	w.ns = newNamespace(t, k)
	w.ana, w.bob, w.fay = in(person("ana"), w.ns), in(person("bob"), w.ns), in(person("fay"), w.ns)
	w.gus, w.olaf = in(person("gus"), w.ns), in(person("olaf"), w.ns)
	w.grant(t, w.fay, "wq_finance")
	w.grant(t, w.gus, "wq_finance")
	return w
}

func (w *waitWorld) grant(t *testing.T, p item.Principal, role string) {
	t.Helper()
	grantRoleIn(t, w.ns, p, role)
	w.roles[p.Actor] = append(w.roles[p.Actor], role)
}

func (w *waitWorld) revoke(t *testing.T, p item.Principal, role string) {
	t.Helper()
	_, err := pool.Exec(testCtx(t),
		`delete from principal_roles where namespace_id = $1 and principal = $2 and role = $3`, w.ns, p.Actor, role)
	if err != nil {
		t.Fatalf("revoke role %s from %s: %v", role, p.Actor, err)
	}
	w.roles[p.Actor] = slices.DeleteFunc(w.roles[p.Actor], func(r string) bool { return r == role })
}

// create makes an Item as p and remembers it.
func (w *waitWorld) create(t *testing.T, req itemservice.CreateRequest) uuid.UUID {
	t.Helper()
	req.IdempotencyKey = newKey()
	res, err := w.k.Create(testCtx(t), req)
	if err != nil {
		t.Fatalf("%s creates a %s@%d: %v", req.Principal.Actor, req.TypeID, req.TypeVersion, err)
	}
	w.made = append(w.made, res.ItemID)
	return res.ItemID
}

func (w *waitWorld) move(t *testing.T, p item.Principal, id uuid.UUID, to string, patch map[string]any) {
	t.Helper()
	if _, err := w.k.Transition(testCtx(t), itemservice.TransitionRequest{
		IdempotencyKey: newKey(), Principal: p, ItemID: id, ToState: to, Fields: patch,
	}); err != nil {
		t.Fatalf("%s moves it to %s: %v", p.Actor, to, err)
	}
}

// purchaseOpts is where a purchase is made, on which version, and by whom.
type purchaseOpts struct {
	version   int             // default 1
	by        *item.Principal // default ana
	container *uuid.UUID
	narrow    []item.Principal // its readers, when narrowed
}

// purchase makes a purchase of amount (its manager: bob) and walks it to
// state: draft, manager_review, finance_queue, or finance_review claimed by
// claimer. On version 2 the queue is the board's: claimer must hold
// wq_board.
func (w *waitWorld) purchase(t *testing.T, amount int, state string, claimer *item.Principal, o purchaseOpts) uuid.UUID {
	t.Helper()
	by := w.ana
	if o.by != nil {
		by = *o.by
	}
	req := itemservice.CreateRequest{
		Principal: by, TypeID: "wq_purchase", TypeVersion: max(o.version, 1), ContainerID: o.container,
		Fields: map[string]any{"f_amount": amount, "f_vendor": "officeco", "f_manager": w.bob.Actor},
	}
	if o.narrow != nil {
		req.Narrow = &itemservice.Narrow{Readers: readers(o.narrow...)}
	}
	id := w.create(t, req)
	if state == "draft" {
		return id
	}
	w.move(t, by, id, "manager_review", nil)
	if state == "manager_review" {
		return id
	}
	w.move(t, w.bob, id, "finance_queue", nil)
	if state == "finance_queue" {
		return id
	}
	w.move(t, *claimer, id, "finance_review", map[string]any{"f_claimed_by": claimer.Actor})
	return id
}

// access makes ana's access request for a system owned by owner, waiting
// on that owner.
func (w *waitWorld) access(t *testing.T, system string, owner item.Principal) uuid.UUID {
	t.Helper()
	id := w.create(t, itemservice.CreateRequest{
		Principal: w.ana, TypeID: "wq_access", TypeVersion: 1,
		Fields: map[string]any{"f_system": system, "f_owner": owner.Actor},
	})
	w.move(t, w.ana, id, "owner_review", nil)
	return id
}

// waiting is the whole view of p (every page), as Search answers it.
func (w *waitWorld) waiting(t *testing.T, p item.Principal, on string, f itemservice.Filter) []uuid.UUID {
	t.Helper()
	f.WaitingOn = on
	var ids []uuid.UUID
	token := ""
	for page := 0; ; page++ {
		res, err := w.k.Search(testCtx(t), itemservice.SearchRequest{Principal: p, Filter: f, PageToken: token})
		if err != nil {
			t.Fatalf("Search waiting on %q as %s: %v", on, p.Actor, err)
		}
		for _, it := range res.Items {
			ids = append(ids, it.ID)
		}
		if token = res.NextPageToken; token == "" {
			return ids
		}
		if page > 100 {
			t.Fatalf("Search waiting on %q as %s: more than 100 pages", on, p.Actor)
		}
	}
}

// assertWaiting: got holds exactly the named Items, in any order.
func assertWaiting(t *testing.T, what string, got []uuid.UUID, want map[string]uuid.UUID) {
	t.Helper()
	names := map[uuid.UUID]string{}
	for name, id := range want {
		names[id] = name
	}
	var extra, seen []string
	for _, id := range got {
		if name, ok := names[id]; ok {
			seen = append(seen, name)
		} else {
			extra = append(extra, id.String())
		}
	}
	var missing []string
	for name := range want {
		if !slices.Contains(seen, name) {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	sort.Strings(seen)
	if len(missing) > 0 || len(extra) > 0 || len(seen) != len(want) {
		t.Errorf("%s: got %v, missing %v, and %d Items that should not be there %v", what, seen, missing, len(extra), extra)
	}
}

// these builds assertWaiting's map from name, ID pairs.
func these(pairs ...any) map[string]uuid.UUID {
	out := map[string]uuid.UUID{}
	for i := 0; i+1 < len(pairs); i += 2 {
		out[pairs[i].(string)] = pairs[i+1].(uuid.UUID)
	}
	return out
}

// assertWaitingOnInvalid: the request is malformed at filter.waiting_on.
func assertWaitingOnInvalid(t *testing.T, what string, err error) {
	t.Helper()
	var inv *itemservice.InvalidError
	if !errors.Is(err, itemservice.ErrInvalid) || !errors.As(err, &inv) || inv.Path != "filter.waiting_on" {
		t.Errorf("%s: error = %v, want an *InvalidError (ErrInvalid) at filter.waiting_on", what, err)
	}
}

// The bake-offs' test 3. Six requests: a purchase in the finance queue, one
// fay claimed, one gus claimed, a small one waiting on its manager, an
// access request for the system fay owns and one for olaf's. Four are in
// fay's inbox: the three with finance and her system's. (A claim does not
// bind yet: gus's is still hers to decide. That is step 2.)
func TestWaiting_FaysInbox_TheBakeoffsFourOfSix(t *testing.T) {
	w := newWaitWorld(t)
	queued := w.purchase(t, 2500, "finance_queue", nil, purchaseOpts{})
	faysClaim := w.purchase(t, 4000, "finance_review", &w.fay, purchaseOpts{})
	gusClaim := w.purchase(t, 1800, "finance_review", &w.gus, purchaseOpts{})
	small := w.purchase(t, 300, "manager_review", nil, purchaseOpts{})
	ledger := w.access(t, "ledger", w.fay)
	payroll := w.access(t, "payroll", w.olaf)
	w.purchase(t, 700, "draft", nil, purchaseOpts{}) // ana's draft: hers to send, and nobody's work

	none := itemservice.Filter{}
	assertWaiting(t, "fay (finance, owner of the ledger)", w.waiting(t, w.fay, "me", none),
		these("queued", queued, "fay's claim", faysClaim, "gus's claim", gusClaim, "ledger", ledger))
	assertWaiting(t, "gus (finance)", w.waiting(t, w.gus, "me", none),
		these("queued", queued, "fay's claim", faysClaim, "gus's claim", gusClaim))
	assertWaiting(t, "bob (ana's manager)", w.waiting(t, w.bob, "me", none), these("small", small))
	assertWaiting(t, "olaf (owner of payroll)", w.waiting(t, w.olaf, "me", none), these("payroll", payroll))
	// ana asked for all seven: none waits on her. Withdrawing a request
	// and sending a draft are her moves, not work waiting on her.
	assertWaiting(t, "ana (the requester)", w.waiting(t, w.ana, "me", none), these())

	// What comes back is the Item as its reader sees it (policy.Project):
	// whole, for staff.
	res, err := w.k.Search(testCtx(t), itemservice.SearchRequest{
		Principal: w.olaf, Filter: itemservice.Filter{WaitingOn: "me"},
	})
	if err != nil || len(res.Items) != 1 {
		t.Fatalf("olaf's view: %d Items, error %v; want one", len(res.Items), err)
	}
	if got := res.Items[0]; got.State != "owner_review" || got.Fields["f_system"] != "payroll" || got.TypeID != "wq_access" {
		t.Errorf("olaf's Item = %s in %s with %v, want the payroll wq_access in owner_review", got.TypeID, got.State, got.Fields)
	}
}

// Roles are read when the view is asked, not when the Item entered its
// state: a new member of finance sees the queue at once, and one who left
// sees it no more. The same for a person a field names: change the field,
// and the Item waits on the new one.
func TestWaiting_FollowsRolesAndFieldsAtOnce(t *testing.T) {
	w := newWaitWorld(t)
	queued := w.purchase(t, 2500, "finance_queue", nil, purchaseOpts{})
	small := w.purchase(t, 300, "manager_review", nil, purchaseOpts{})
	none := itemservice.Filter{}

	assertWaiting(t, "olaf, before he joins finance", w.waiting(t, w.olaf, "me", none), these())
	w.grant(t, w.olaf, "wq_finance")
	assertWaiting(t, "olaf, in finance", w.waiting(t, w.olaf, "me", none), these("queued", queued))
	w.revoke(t, w.olaf, "wq_finance")
	assertWaiting(t, "olaf, after he left finance", w.waiting(t, w.olaf, "me", none), these())

	// ana names gus as her manager instead of bob.
	assertWaiting(t, "bob, named as manager", w.waiting(t, w.bob, "me", none), these("small", small))
	if _, err := w.k.Update(testCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: w.ana, ItemID: small, Fields: map[string]any{"f_manager": w.gus.Actor},
	}); err != nil {
		t.Fatalf("ana names gus as manager: %v", err)
	}
	assertWaiting(t, "bob, no longer the manager", w.waiting(t, w.bob, "me", none), these())
	assertWaiting(t, "gus, now the manager (and in finance)", w.waiting(t, w.gus, "me", none),
		these("queued", queued, "small", small))
}

// The view never shows an Item its reader cannot see. A purchase narrowed
// to ana, bob and gus sits in the finance queue: gus sees it there, fay
// does not, in her own view or in the role's. And visibility comes before
// the limit: with three hidden purchases newer than her two, a page of two
// holds her two.
func TestWaiting_NeverAnItemTheCallerCannotSee(t *testing.T) {
	w := newWaitWorld(t)
	mine1 := w.purchase(t, 2500, "finance_queue", nil, purchaseOpts{})
	mine2 := w.purchase(t, 2600, "finance_queue", nil, purchaseOpts{})
	var hidden []uuid.UUID
	for i := range 3 {
		hidden = append(hidden, w.purchase(t, 3000+i, "finance_queue", nil,
			purchaseOpts{narrow: []item.Principal{w.ana, w.bob, w.gus}}))
	}

	none := itemservice.Filter{}
	assertWaiting(t, "fay (not a reader of the narrowed three)", w.waiting(t, w.fay, "me", none),
		these("mine1", mine1, "mine2", mine2))
	assertWaiting(t, "the finance queue, asked by fay", w.waiting(t, w.fay, "role:wq_finance", none),
		these("mine1", mine1, "mine2", mine2))
	assertWaiting(t, "gus (a reader)", w.waiting(t, w.gus, "me", none),
		these("mine1", mine1, "mine2", mine2, "hidden0", hidden[0], "hidden1", hidden[1], "hidden2", hidden[2]))

	res, err := w.k.Search(testCtx(t), itemservice.SearchRequest{
		Principal: w.fay, Filter: itemservice.Filter{WaitingOn: "me"}, Limit: 2,
	})
	if err != nil {
		t.Fatalf("fay's first page: %v", err)
	}
	var got []uuid.UUID
	for _, it := range res.Items {
		got = append(got, it.ID)
	}
	// Newest first: mine2, then mine1.
	if !slices.Equal(got, []uuid.UUID{mine2, mine1}) {
		t.Errorf("fay's first page of two = %v, want her two purchases, newest first (%v, %v): "+
			"the three she cannot see must not take room in the page", got, mine2, mine1)
	}
	if res.NextPageToken != "" {
		next, err := w.k.Search(testCtx(t), itemservice.SearchRequest{
			Principal: w.fay, Filter: itemservice.Filter{WaitingOn: "me"}, Limit: 2, PageToken: res.NextPageToken,
		})
		if err != nil || len(next.Items) != 0 {
			t.Errorf("fay's second page: %d Items, error %v; want none", len(next.Items), err)
		}
	}
}

// except: [creator] is applied to the person asking. gus, in finance, asks
// for a purchase himself: in the finance queue it waits on fay, not on
// him. It is still in the role's queue, whoever asks: a queue is a role's.
func TestWaiting_ExceptCreator(t *testing.T) {
	w := newWaitWorld(t)
	gusOwn := w.purchase(t, 2500, "finance_queue", nil, purchaseOpts{by: &w.gus})
	anas := w.purchase(t, 2600, "finance_queue", nil, purchaseOpts{})

	none := itemservice.Filter{}
	assertWaiting(t, "gus (who asked for one of them)", w.waiting(t, w.gus, "me", none), these("ana's", anas))
	assertWaiting(t, "fay", w.waiting(t, w.fay, "me", none), these("ana's", anas, "gus's own", gusOwn))
	assertWaiting(t, "the finance queue, asked by gus", w.waiting(t, w.gus, "role:wq_finance", none),
		these("ana's", anas, "gus's own", gusOwn))

	// Once fay has claimed it, one move out names finance without the
	// exception (back to the queue): it waits on gus too.
	w.move(t, w.fay, gusOwn, "finance_review", map[string]any{"f_claimed_by": w.fay.Actor})
	assertWaiting(t, "gus, once his own is in review", w.waiting(t, w.gus, "me", none),
		these("ana's", anas, "gus's own", gusOwn))
}

// What does not count. A note whose moves are open to anyone, or its
// creator's, waits on nobody. Parked, one move names the creator beside
// finance: it waits on finance, not on its requester.
func TestWaiting_EmptyAndCreatorOnlyMovesDoNotCount(t *testing.T) {
	w := newWaitWorld(t)
	note := func() uuid.UUID {
		return w.create(t, itemservice.CreateRequest{
			Principal: w.ana, TypeID: "wq_note", TypeVersion: 1, Fields: map[string]any{"f_text": "a note"},
		})
	}
	open := note()
	parked := note()
	w.move(t, w.olaf, parked, "parked", nil)
	_ = open

	none := itemservice.Filter{}
	for _, p := range []item.Principal{w.ana, w.bob, w.olaf} {
		assertWaiting(t, p.Actor+" (no role)", w.waiting(t, p, "me", none), these())
	}
	assertWaiting(t, "fay (finance)", w.waiting(t, w.fay, "me", none), these("parked", parked))
	assertWaiting(t, "the finance queue", w.waiting(t, w.olaf, "role:wq_finance", none), these("parked", parked))
}

// A reopen is not work. An approved purchase (done) has a move finance
// may make, and a rejected one (canceled) a move its manager may make:
// neither waits on anyone.
func TestWaiting_DoneAndCanceledNeverWait(t *testing.T) {
	w := newWaitWorld(t)
	approved := w.purchase(t, 2500, "finance_review", &w.fay, purchaseOpts{})
	w.move(t, w.fay, approved, "approved", nil)
	rejected := w.purchase(t, 300, "manager_review", nil, purchaseOpts{})
	w.move(t, w.bob, rejected, "rejected", nil)

	none := itemservice.Filter{}
	assertWaiting(t, "fay", w.waiting(t, w.fay, "me", none), these())
	assertWaiting(t, "bob", w.waiting(t, w.bob, "me", none), these())
	assertWaiting(t, "the finance queue", w.waiting(t, w.fay, "role:wq_finance", none), these())

	// The moves are there: fay reopens it, and it waits on finance again.
	w.move(t, w.fay, approved, "finance_review", nil)
	assertWaiting(t, "fay, after the reopen", w.waiting(t, w.fay, "me", none), these("reopened", approved))
}

// The view follows each Item's own Type version, as its moves do. Version
// 2 gives the queue to the board: a purchase made on version 1 stays
// finance's, one made on version 2 is the board's, side by side.
func TestWaiting_FollowsEachItemsOwnVersion(t *testing.T) {
	w := newWaitWorld(t)
	w.grant(t, w.olaf, "wq_board")
	onV1 := w.purchase(t, 2500, "finance_queue", nil, purchaseOpts{})
	onV2 := w.purchase(t, 2600, "finance_queue", nil, purchaseOpts{version: 2})

	none := itemservice.Filter{}
	assertWaiting(t, "fay (finance)", w.waiting(t, w.fay, "me", none), these("on version 1", onV1))
	assertWaiting(t, "olaf (the board)", w.waiting(t, w.olaf, "me", none), these("on version 2", onV2))
	assertWaiting(t, "the finance queue", w.waiting(t, w.bob, "role:wq_finance", none), these("on version 1", onV1))
	assertWaiting(t, "the board's queue", w.waiting(t, w.bob, "role:wq_board", none), these("on version 2", onV2))
}

// An agent's view is what its session may act on. Acting for fay without
// the role, nothing: a role must be the agent's too. Holding it, finance's
// purchases, but not the access request that names fay: a field that names
// a person never names an agent. And a session over one container sees
// only what is inside it.
func TestWaiting_AnAgentsViewIsWhatItsSessionMayActOn(t *testing.T) {
	w := newWaitWorld(t)
	desk := w.create(t, itemservice.CreateRequest{
		Principal: w.ana, TypeID: "wq_desk", TypeVersion: 1, Fields: map[string]any{"f_name": "Purchasing"},
	})
	inDesk := w.purchase(t, 2500, "finance_queue", nil, purchaseOpts{container: &desk})
	outside := w.purchase(t, 2600, "finance_queue", nil, purchaseOpts{})
	w.access(t, "ledger", w.fay)

	agent := item.Principal{Actor: "agent:wq-bot-" + uuid.NewString()[:8], OnBehalfOf: w.fay.Actor, Namespace: w.ns}
	whole := withSession(t, w.k, agent)
	none := itemservice.Filter{}
	assertWaiting(t, "the agent for fay, holding no role", w.waiting(t, whole, "me", none), these())

	w.grant(t, item.Principal{Actor: agent.Actor}, "wq_finance")
	assertWaiting(t, "the agent for fay, in finance too", w.waiting(t, whole, "me", none),
		these("in the desk", inDesk, "outside", outside))

	req := wholeNamespace(agent)
	req.Scopes = []uuid.UUID{desk}
	session, err := w.k.StartAgentSession(testCtx(t), req)
	if err != nil {
		t.Fatalf("start a session over the desk: %v", err)
	}
	deskOnly := agent
	deskOnly.AgentSession = session
	assertWaiting(t, "the agent's session over the desk", w.waiting(t, deskOnly, "me", none), these("in the desk", inDesk))
	assertWaiting(t, "the finance queue, asked in that session", w.waiting(t, deskOnly, "role:wq_finance", none),
		these("in the desk", inDesk))

	// The agent acting for itself: its own role is enough.
	alone := withSession(t, w.k, item.Principal{Actor: agent.Actor, Namespace: w.ns})
	assertWaiting(t, "the agent on its own", w.waiting(t, alone, "me", none),
		these("in the desk", inDesk, "outside", outside))
}

// An Item with an open hold waits on its check, not on a person: it is
// left out, of a person's view and of the role's queue, until the hold
// closes.
func TestWaiting_AnOpenHoldHides(t *testing.T) {
	w := newWaitWorld(t)
	asked := w.create(t, itemservice.CreateRequest{
		Principal: w.ana, TypeID: "wq_held", TypeVersion: 1,
		Fields: map[string]any{"f_customer_id": "cust-1", "f_amount": 100},
	})
	none := itemservice.Filter{}
	assertWaiting(t, "fay, before anyone asks", w.waiting(t, w.fay, "me", none), these("asked", asked))

	res, err := w.k.Transition(testCtx(t), itemservice.TransitionRequest{
		IdempotencyKey: newKey(), Principal: w.fay, ItemID: asked, ToState: "granted",
	})
	if err != nil || res.HoldID == uuid.Nil {
		t.Fatalf("fay asks for the checked move: hold %v, error %v; want an open hold", res.HoldID, err)
	}
	assertWaiting(t, "fay, while the check runs", w.waiting(t, w.fay, "me", none), these())
	assertWaiting(t, "gus, while the check runs", w.waiting(t, w.gus, "me", none), these())
	assertWaiting(t, "the finance queue, while the check runs", w.waiting(t, w.gus, "role:wq_finance", none), these())

	// The check denies: the Item stays where it was, and waits again.
	hook := item.Principal{Actor: "hook:wq_held:credit", Namespace: w.ns}
	if _, err := w.k.Internal().ResolveHold(testCtx(t), itemservice.ResolveHoldRequest{
		IdempotencyKey: "hold:" + res.HoldID.String(), Principal: hook, ItemID: asked, HoldID: res.HoldID,
		Verdict: itemservice.VerdictDeny, Reason: "credit_hold",
		Attribution: item.Attribution{
			OriginatingHook: "hook:wq_held@v1:credit", CausationID: fmt.Sprintf("%s:%d", asked, res.ItemSeq), Hop: 1,
		},
	}); err != nil {
		t.Fatalf("the check denies: %v", err)
	}
	assertWaiting(t, "gus, once the hold has closed", w.waiting(t, w.gus, "me", none), these("asked", asked))
}

// A customer's waiting_on is refused: a customer takes only the moves
// that name the creator, and those do not count until a Type can say that
// a state waits on its requester. So is an agent's acting for one.
func TestWaiting_ACustomerIsRefused(t *testing.T) {
	w := newWaitWorld(t)
	w.purchase(t, 2500, "finance_queue", nil, purchaseOpts{})
	kim := customer("kim-"+uuid.NewString()[:8], w.ns)
	for _, on := range []string{"me", "role:wq_finance"} {
		_, err := w.k.Search(testCtx(t), itemservice.SearchRequest{Principal: kim, Filter: itemservice.Filter{WaitingOn: on}})
		assertWaitingOnInvalid(t, "a customer's Search waiting on "+on, err)
		_, err = w.k.List(testCtx(t), itemservice.ListRequest{Principal: kim, Filter: itemservice.Filter{WaitingOn: on}})
		assertWaitingOnInvalid(t, "a customer's List waiting on "+on, err)
	}
}

// waiting_on is "me" or "role:<name>", nothing else: not another person's
// inbox, not a group. A role nobody holds, or that no Type names, is an
// empty queue, not an error. Any member of staff may ask for a role's
// queue, and sees of it what they can see.
func TestWaiting_TheValueAndARolesQueue(t *testing.T) {
	w := newWaitWorld(t)
	queued := w.purchase(t, 2500, "finance_queue", nil, purchaseOpts{})
	w.purchase(t, 2600, "finance_queue", nil, purchaseOpts{narrow: []item.Principal{w.ana, w.bob, w.fay}})
	w.access(t, "ledger", w.fay)

	for _, on := range []string{"everyone", "role:", "role:Not A Name", "ROLE:wq_finance", w.fay.Actor,
		"principal:" + w.fay.Actor, "group:finance", "me ", "field:f_owner", "creator"} {
		_, err := w.k.Search(testCtx(t), itemservice.SearchRequest{Principal: w.fay, Filter: itemservice.Filter{WaitingOn: on}})
		assertWaitingOnInvalid(t, fmt.Sprintf("waiting on %q", on), err)
	}

	none := itemservice.Filter{}
	// olaf holds no role and is no reader of the narrowed purchase.
	assertWaiting(t, "the finance queue, asked by olaf", w.waiting(t, w.olaf, "role:wq_finance", none), these("queued", queued))
	assertWaiting(t, "olaf's own view", w.waiting(t, w.olaf, "me", none), these())
	assertWaiting(t, "a role no Type names", w.waiting(t, w.fay, "role:wq_nobody_holds_this", none), these())

	// waiting_on alone is a search: no words, no other filter.
	if _, err := w.k.Search(testCtx(t), itemservice.SearchRequest{
		Principal: w.fay, Filter: itemservice.Filter{WaitingOn: "me"},
	}); err != nil {
		t.Errorf("Search with waiting_on alone: %v, want it to be a search", err)
	}
}

// waiting_on is one more condition: it composes with the other filters,
// with words, with order_by and with paging, on Search and on List. A page
// token is bound to it like to the rest of the request.
func TestWaiting_ComposesWithFiltersOrderAndPaging(t *testing.T) {
	w := newWaitWorld(t)
	desk := w.create(t, itemservice.CreateRequest{
		Principal: w.ana, TypeID: "wq_desk", TypeVersion: 1, Fields: map[string]any{"f_name": "Purchasing"},
	})
	p2500 := w.purchase(t, 2500, "finance_queue", nil, purchaseOpts{container: &desk})
	p4000 := w.purchase(t, 4000, "finance_review", &w.fay, purchaseOpts{})
	p1800 := w.purchase(t, 1800, "finance_review", &w.gus, purchaseOpts{container: &desk})
	w.purchase(t, 300, "manager_review", nil, purchaseOpts{container: &desk}) // bob's
	ledger := w.access(t, "ledger", w.fay)

	purchases := itemservice.Filter{TypeIDs: []string{"wq_purchase"}}
	assertWaiting(t, "fay's purchases", w.waiting(t, w.fay, "me", purchases),
		these("2500", p2500, "4000", p4000, "1800", p1800))
	assertWaiting(t, "fay's, in the queue", w.waiting(t, w.fay, "me", itemservice.Filter{States: []string{"finance_queue"}}),
		these("2500", p2500))
	assertWaiting(t, "fay's, in a waiting state",
		w.waiting(t, w.fay, "me", itemservice.Filter{Categories: []string{"waiting"}}), these("2500", p2500, "ledger", ledger))
	assertWaiting(t, "fay's, claimed by gus", w.waiting(t, w.fay, "me",
		itemservice.Filter{Fields: []itemservice.FieldFilter{{Field: "f_claimed_by", Eq: w.gus.Actor}}}), these("1800", p1800))

	// With words: the ledger request holds the word "ledger".
	res, err := w.k.Search(testCtx(t), itemservice.SearchRequest{
		Principal: w.fay, Query: "ledger", Filter: itemservice.Filter{WaitingOn: "me"},
	})
	if err != nil || len(res.Items) != 1 || res.Items[0].ID != ledger {
		t.Errorf("fay's view with the word ledger: %d Items, error %v; want the ledger request", len(res.Items), err)
	}

	// Ordered by amount, one Item a page: 1800, 2500, 4000, then nothing.
	req := itemservice.SearchRequest{
		Principal: w.fay, Limit: 1, OrderBy: &itemservice.OrderBy{Field: "f_amount"},
		Filter: itemservice.Filter{TypeIDs: []string{"wq_purchase"}, WaitingOn: "me"},
	}
	var order []uuid.UUID
	var firstToken string
	for page := 0; page < 5; page++ {
		res, err := w.k.Search(testCtx(t), req)
		if err != nil {
			t.Fatalf("page %d by amount: %v", page, err)
		}
		if len(res.Items) > 1 {
			t.Fatalf("page %d holds %d Items, want at most one", page, len(res.Items))
		}
		for _, it := range res.Items {
			order = append(order, it.ID)
		}
		if page == 0 {
			firstToken = res.NextPageToken
		}
		if req.PageToken = res.NextPageToken; req.PageToken == "" {
			break
		}
	}
	if !slices.Equal(order, []uuid.UUID{p1800, p2500, p4000}) {
		t.Errorf("fay's purchases by amount, a page each = %v, want %v, %v, %v", order, p1800, p2500, p4000)
	}

	// A token made for "me" names a place in "me": with the role's queue,
	// or with no waiting_on, it is refused.
	if firstToken == "" {
		t.Fatalf("the first page of three gave no token")
	}
	for name, on := range map[string]string{"the role's queue": "role:wq_finance", "no waiting_on": ""} {
		other := req
		other.PageToken, other.Filter.WaitingOn = firstToken, on
		if _, err := w.k.Search(testCtx(t), other); !errors.Is(err, itemservice.ErrInvalid) {
			t.Errorf("a token of waiting on me, used with %s: error = %v, want ErrInvalid", name, err)
		}
	}

	// List: the desk's direct children that wait on fay.
	listed, err := w.k.List(testCtx(t), itemservice.ListRequest{
		Principal: w.fay, ContainerID: &desk, Filter: itemservice.Filter{WaitingOn: "me"},
	})
	if err != nil {
		t.Fatalf("List the desk waiting on fay: %v", err)
	}
	var got []uuid.UUID
	for _, it := range listed.Items {
		got = append(got, it.ID)
	}
	assertWaiting(t, "the desk's children waiting on fay", got, these("2500", p2500, "1800", p1800))
	// And the top level, waiting on the finance queue.
	top, err := w.k.List(testCtx(t), itemservice.ListRequest{Principal: w.olaf, Filter: itemservice.Filter{WaitingOn: "role:wq_finance"}})
	if err != nil {
		t.Fatalf("List the top level waiting on finance: %v", err)
	}
	got = nil
	for _, it := range top.Items {
		got = append(got, it.ID)
	}
	assertWaiting(t, "the top level's Items in the finance queue", got, these("4000", p4000))
}

// The view is held to the one rule (policy.WaitsOn, policy.WaitsOnRole):
// over a world with every case above, for each person, the view is
// exactly the Items they can see, with no open hold, that the rule says
// wait on them; and each of those is an Item with a move MayTransition
// lets them make.
func TestWaiting_TheViewIsTheRule(t *testing.T) {
	w := newWaitWorld(t)
	w.grant(t, w.olaf, "wq_board")
	desk := w.create(t, itemservice.CreateRequest{
		Principal: w.ana, TypeID: "wq_desk", TypeVersion: 1, Fields: map[string]any{"f_name": "Purchasing"},
	})
	for i, state := range []string{"draft", "manager_review", "finance_queue", "finance_review"} {
		w.purchase(t, 1000+i, state, &w.fay, purchaseOpts{})
		w.purchase(t, 2000+i, state, &w.fay, purchaseOpts{by: &w.gus, container: &desk})
		w.purchase(t, 3000+i, state, &w.olaf, purchaseOpts{version: 2})
		w.purchase(t, 4000+i, state, &w.gus, purchaseOpts{narrow: []item.Principal{w.ana, w.bob, w.gus}})
	}
	done := w.purchase(t, 5000, "finance_review", &w.fay, purchaseOpts{})
	w.move(t, w.fay, done, "approved", nil)
	w.access(t, "ledger", w.fay)
	w.access(t, "payroll", w.olaf)
	parked := w.create(t, itemservice.CreateRequest{Principal: w.ana, TypeID: "wq_note", TypeVersion: 1})
	w.move(t, w.ana, parked, "parked", nil)
	held := w.create(t, itemservice.CreateRequest{
		Principal: w.ana, TypeID: "wq_held", TypeVersion: 1, Fields: map[string]any{"f_customer_id": "c", "f_amount": 1},
	})
	if _, err := w.k.Transition(testCtx(t), itemservice.TransitionRequest{
		IdempotencyKey: newKey(), Principal: w.fay, ItemID: held, ToState: "granted",
	}); err != nil {
		t.Fatalf("open a hold: %v", err)
	}
	w.create(t, itemservice.CreateRequest{
		Principal: w.ana, TypeID: "wq_held", TypeVersion: 1, Fields: map[string]any{"f_customer_id": "c", "f_amount": 2},
	})

	total := 0
	for _, p := range []item.Principal{w.ana, w.bob, w.fay, w.gus, w.olaf} {
		who := policy.Who{Actor: p.Actor, ActorRoles: w.roles[p.Actor]}
		wantMe, wantQueue := map[uuid.UUID]bool{}, map[uuid.UUID]bool{}
		for _, id := range w.made {
			it, err := w.k.Get(testCtx(t), p, id)
			if errors.Is(err, itemservice.ErrNotFound) {
				continue // hidden from p: never in their view
			}
			if err != nil {
				t.Fatalf("Get %s as %s: %v", id, p.Actor, err)
			}
			if it.Hold != nil && it.Hold.Status == item.HoldOpen {
				continue
			}
			ty := w.types[fmt.Sprintf("%s@%d", it.TypeID, it.TypeVersion)]
			facts := policy.ItemFacts{Fields: it.Fields, CreatedBy: it.CreatedBy, CreatedOnBehalfOf: it.CreatedOnBehalfOf}
			if policy.WaitsOn(ty, it.State, who, facts) {
				wantMe[id] = true
				// What waits on someone is a move they may make.
				may := false
				for _, tr := range ty.Transitions {
					may = may || (tr.From == it.State && policy.MayTransition(tr, who, facts))
				}
				if !may {
					t.Errorf("%s: the rule says %s (%s in %s) waits on them, yet MayTransition lets them make no move",
						p.Actor, id, it.TypeID, it.State)
				}
			}
			if policy.WaitsOnRole(ty, it.State, "wq_finance") {
				wantQueue[id] = true
			}
		}
		total += len(wantMe)
		for name, c := range map[string]struct {
			on   string
			want map[uuid.UUID]bool
		}{"waiting on me": {"me", wantMe}, "the finance queue": {"role:wq_finance", wantQueue}} {
			got := w.waiting(t, p, c.on, itemservice.Filter{})
			var extra, missing []string
			for _, id := range got {
				if !c.want[id] {
					extra = append(extra, id.String())
				}
			}
			for id := range c.want {
				if !slices.Contains(got, id) {
					missing = append(missing, id.String())
				}
			}
			if len(extra) > 0 || len(missing) > 0 || len(got) != len(c.want) {
				t.Errorf("%s, %s: the view has %d Items, the rule %d; in the view only: %s; in the rule only: %s",
					p.Actor, name, len(got), len(c.want), strings.Join(extra, ", "), strings.Join(missing, ", "))
			}
		}
	}
	// The world must exercise the rule: with nothing waiting on anyone,
	// an empty view would pass.
	if total < 10 {
		t.Errorf("the rule says only %d Items wait on the five people: the world is too thin to hold the view to it", total)
	}
}
