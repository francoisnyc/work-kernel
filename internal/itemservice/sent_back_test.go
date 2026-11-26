// Tests for "sent back to the requester" in the item service (docs/
// roadmap.md, track 1, item 5, step 5; the owner's decisions of its Phase
// A): an Item in a state with `waits_on: requester` is in its requester's
// "waiting on me" view, staff or customer, besides whoever its moves name.
//
//   - staff: their own request, sent back, is in their view; a colleague's
//     is not; a role's queue does not hold it unless a move names the role;
//   - a customer, and an agent acting for one, may ask waiting_on: me:
//     they get their own requests that wait on them, inside the customer
//     rule (their customer fields only, through Project), and nothing
//     else; role:<name> stays refused for them;
//   - it composes with order_by: waiting_since, filter.in, paging and
//     Count, as the view does for staff;
//   - what the SQL returns is what policy.WaitsOn says, Item by Item.
package itemservice_test

import (
	"slices"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
	"github.com/francoisnyc/work-kernel/internal/policy"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// sb_desk owns sb_req, which customers open. The team takes a request
// (open -> taken), asks its requester a question (taken -> asked, where it
// waits on the requester only), or answers (done). The requester sends it
// back (asked -> taken). In `hold` it waits on its requester and on the
// team at once.
const sbDeskSrc = `
id: sb_desk
version: 1
own: [sb_req]
`

const sbReqSrc = `
id: sb_req
version: 1
initial_state: open
fields:
  - {id: f_title,  type: string}
  - {id: f_secret, type: string}
states:
  - {id: open,  category: waiting}
  - {id: taken, category: active}
  - {id: asked, category: waiting, waits_on: requester}
  - {id: hold,  category: waiting, waits_on: requester}
  - {id: done,  category: done}
transitions:
  - {from: open,  to: taken, allowed: ['role:sb_team']}
  - {from: taken, to: asked, allowed: ['role:sb_team']}
  - {from: taken, to: hold,  allowed: ['role:sb_team']}
  - {from: taken, to: done,  allowed: ['role:sb_team']}
  - {from: asked, to: taken, allowed: [creator]}
  - {from: hold,  to: taken, allowed: [creator]}
  - {from: hold,  to: done,  allowed: ['role:sb_team']}
customer_fields: [f_title]
`

var sbOnce sync.Once

type sbWorld struct {
	k                *itemservice.Service
	ns               string
	ana, bob, cyd    item.Principal // ana asks; bob and cyd are in the team
	kim, lee         item.Principal // customers
	kimsBot, anasBot item.Principal // agents acting for kim and for ana
	desk             uuid.UUID
	byName           map[uuid.UUID]string
	sbType           *typedef.Type
}

func newSBWorld(t *testing.T) *sbWorld {
	t.Helper()
	k := itemservice.New(pool)
	sbOnce.Do(func() {
		registerSrc(t, k, sbDeskSrc)
		registerSrc(t, k, sbReqSrc)
	})
	ty, err := typedef.Parse([]byte(sbReqSrc))
	if err != nil {
		t.Fatal(err)
	}
	ns := newNamespace(t, k)
	w := &sbWorld{k: k, ns: ns,
		ana: in(person("ana"), ns), bob: in(person("bob"), ns), cyd: in(person("cyd"), ns),
		kim: customer("kim", ns), lee: customer("lee", ns), byName: map[uuid.UUID]string{}, sbType: ty}
	// The portal's agent acting for kim, with a customer's fixed powers.
	w.kimsBot = item.Principal{Actor: "agent:portal-" + word(), OnBehalfOf: w.kim.Actor, Namespace: ns}
	all := []uuid.UUID{uuid.Nil}
	session, err := k.StartAgentSession(testCtx(t), itemservice.StartAgentSessionRequest{
		Principal: w.kimsBot, Scopes: all, Powers: customerPowers, Grant: &itemservice.Grant{Scopes: all, Powers: customerPowers}})
	if err != nil {
		t.Fatalf("a portal agent for kim: %v", err)
	}
	w.kimsBot.AgentSession = session
	w.anasBot = withSession(t, k, item.Principal{Actor: "agent:prep-" + word(), OnBehalfOf: w.ana.Actor, Namespace: ns})
	grantRoleIn(t, ns, w.bob, "sb_team")
	grantRoleIn(t, ns, w.cyd, "sb_team")
	w.desk = w.create(t, w.bob, "sb_desk", nil, nil, "desk")
	return w
}

func (w *sbWorld) create(t *testing.T, p item.Principal, typ string, in *uuid.UUID, fields map[string]any, name string) uuid.UUID {
	t.Helper()
	res, err := w.k.Create(testCtx(t), itemservice.CreateRequest{
		IdempotencyKey: newKey(), Principal: p, TypeID: typ, TypeVersion: 1, ContainerID: in, Fields: fields,
	})
	if err != nil {
		t.Fatalf("%s creates %s %s: %v", p.Actor, typ, name, err)
	}
	w.byName[res.ItemID] = name
	return res.ItemID
}

// request: p opens a request in the desk, bob writes a staff field on it,
// and the team walks it to state.
func (w *sbWorld) request(t *testing.T, p item.Principal, name, state string) uuid.UUID {
	t.Helper()
	id := w.create(t, p, "sb_req", &w.desk, map[string]any{"f_title": name + " " + word()}, name)
	if _, err := w.k.Update(testCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: w.bob, ItemID: id, Fields: map[string]any{"f_secret": "staff only"},
	}); err != nil {
		t.Fatalf("bob writes f_secret on %s: %v", name, err)
	}
	path := map[string][]string{"open": nil, "taken": {"taken"}, "asked": {"taken", "asked"}, "hold": {"taken", "hold"}, "done": {"taken", "done"}}[state]
	for _, to := range path {
		w.move(t, w.bob, id, to)
	}
	return id
}

func (w *sbWorld) move(t *testing.T, p item.Principal, id uuid.UUID, to string) {
	t.Helper()
	if _, err := w.k.Transition(testCtx(t), itemservice.TransitionRequest{
		IdempotencyKey: newKey(), Principal: p, ItemID: id, ToState: to,
	}); err != nil {
		t.Fatalf("%s moves %s to %s: %v", p.Actor, w.byName[id], to, err)
	}
}

// view: every page of p's waiting_on view, in order; the Items' names.
func (w *sbWorld) view(t *testing.T, p item.Principal, on string, f itemservice.Filter, order *itemservice.OrderBy, limit int) []string {
	t.Helper()
	f.WaitingOn = on
	var out []string
	token := ""
	for range 100 {
		res, err := w.k.Search(testCtx(t), itemservice.SearchRequest{Principal: p, Filter: f, OrderBy: order, Limit: limit, PageToken: token})
		if err != nil {
			t.Fatalf("%s's view waiting on %s: %v", p.Actor, on, err)
		}
		for _, it := range res.Items {
			if name := w.byName[it.ID]; name != "" {
				out = append(out, name)
			}
		}
		if token = res.NextPageToken; token == "" {
			return out
		}
	}
	t.Fatalf("%s's view does not end", p.Actor)
	return nil
}

func sameSet(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

func TestSentBack_TheView(t *testing.T) {
	w := newSBWorld(t)
	kimAsked := w.request(t, w.kim, "kim asked", "asked")
	kimHold := w.request(t, w.kim, "kim hold", "hold")
	w.request(t, w.kim, "kim taken", "taken")
	w.request(t, w.kim, "kim done", "done")
	w.request(t, w.lee, "lee asked", "asked")
	w.request(t, w.ana, "ana asked", "asked")
	w.request(t, w.ana, "ana open", "open")
	w.create(t, w.kimsBot, "sb_req", &w.desk, map[string]any{"f_title": "for kim " + word()}, "kim via bot")
	_ = kimAsked
	_ = kimHold
	me := itemservice.Filter{}

	for _, c := range []struct {
		what string
		who  item.Principal
		on   string
		want []string
	}{
		{"kim, a customer: her requests that wait on her", w.kim, "me", []string{"kim asked", "kim hold"}},
		{"the portal's agent for kim: the same", w.kimsBot, "me", []string{"kim asked", "kim hold"}},
		{"lee, another customer: hers only", w.lee, "me", []string{"lee asked"}},
		{"ana, staff, who asked: her own, sent back", w.ana, "me", []string{"ana asked"}},
		{"an agent acting for ana: the same", w.anasBot, "me", []string{"ana asked"}},
		{"bob, in the team: what the team's moves name, and hold, which waits on both", w.bob, "me",
			[]string{"ana open", "kim taken", "kim hold", "kim via bot"}},
		{"the team's queue: hold is in it, asked is not", w.bob, "role:sb_team", []string{"ana open", "kim taken", "kim hold", "kim via bot"}},
	} {
		if got := w.view(t, c.who, c.on, me, nil, 0); !sameSet(got, c.want) {
			t.Errorf("%s: %v, want %v", c.what, got, c.want)
		}
	}
}

// What a customer gets is what Project gives them: their customer fields.
func TestSentBack_ACustomerSeesTheirFieldsOnly(t *testing.T) {
	w := newSBWorld(t)
	id := w.request(t, w.kim, "kim asked", "asked")
	res, err := w.k.Search(testCtx(t), itemservice.SearchRequest{Principal: w.kim, Filter: itemservice.Filter{WaitingOn: "me"}})
	if err != nil || len(res.Items) != 1 || res.Items[0].ID != id {
		t.Fatalf("kim's view: %d Items, error %v; want her one request", len(res.Items), err)
	}
	if _, has := res.Items[0].Fields["f_secret"]; has || res.Items[0].Fields["f_title"] == nil {
		t.Errorf("kim's row fields = %v, want f_title only", res.Items[0].Fields)
	}
	if !slices.Equal(res.Items[0].Moves, []string{"taken"}) {
		t.Errorf("kim's moves = %v, want [taken]: the Type's move for the creator", res.Items[0].Moves)
	}
}

// A customer holds no role: role:<name> stays refused, for her and for an
// agent acting for her, in Search, List and Count.
func TestSentBack_ACustomerHasNoRolesQueue(t *testing.T) {
	w := newSBWorld(t)
	for _, p := range []item.Principal{w.kim, w.kimsBot} {
		f := itemservice.Filter{WaitingOn: "role:sb_team"}
		_, err := w.k.Search(testCtx(t), itemservice.SearchRequest{Principal: p, Filter: f})
		assertWaitingOnInvalid(t, p.Actor+"'s Search of a role's queue", err)
		_, err = w.k.List(testCtx(t), itemservice.ListRequest{Principal: p, Filter: f})
		assertWaitingOnInvalid(t, p.Actor+"'s List of a role's queue", err)
		_, err = w.k.Count(testCtx(t), itemservice.CountRequest{Principal: p, Filter: f})
		assertWaitingOnInvalid(t, p.Actor+"'s Count of a role's queue", err)
	}
}

// It composes as the view does: oldest waiting first, below a container,
// paged, counted.
func TestSentBack_OrderPlacePagesAndCounts(t *testing.T) {
	w := newSBWorld(t)
	first := w.request(t, w.kim, "first", "asked")
	second := w.request(t, w.kim, "second", "asked")
	third := w.request(t, w.kim, "third", "hold")
	// first is sent back again after the others: it waits since then.
	w.move(t, w.kim, first, "taken")
	w.move(t, w.bob, first, "asked")
	_, _ = second, third
	oldest := &itemservice.OrderBy{Field: itemservice.OrderWaitingSince}
	newest := &itemservice.OrderBy{Field: itemservice.OrderWaitingSince, Desc: true}

	if got := w.view(t, w.kim, "me", itemservice.Filter{}, oldest, 0); !slices.Equal(got, []string{"second", "third", "first"}) {
		t.Errorf("oldest waiting first = %v, want [second third first]", got)
	}
	if got := w.view(t, w.kim, "me", itemservice.Filter{}, newest, 1); !slices.Equal(got, []string{"first", "third", "second"}) {
		t.Errorf("newest first, a page of one = %v, want [first third second]", got)
	}
	other := w.create(t, w.bob, "sb_desk", nil, nil, "other desk")
	if got := w.view(t, w.kim, "me", itemservice.Filter{In: &w.desk}, oldest, 0); len(got) != 3 {
		t.Errorf("below her desk = %v, want her three", got)
	}
	if got := w.view(t, w.kim, "me", itemservice.Filter{In: &other}, oldest, 0); len(got) != 0 {
		t.Errorf("below another desk = %v, want nothing", got)
	}
	for who, want := range map[item.Principal]int{w.kim: 3, w.kimsBot: 3, w.lee: 0} {
		res, err := w.k.Count(testCtx(t), itemservice.CountRequest{Principal: who, Filter: itemservice.Filter{WaitingOn: "me"}})
		if err != nil || res.Count != want || res.Capped {
			t.Errorf("%s's count: %+v, error %v; want %d", who.Actor, res, err, want)
		}
	}
}

// What the SQL view returns is what policy.WaitsOn says, for each person
// and each request.
func TestSentBack_TheViewIsWaitsOn(t *testing.T) {
	w := newSBWorld(t)
	type req struct {
		id    uuid.UUID
		by    item.Principal
		state string
	}
	var reqs []req
	for _, by := range []item.Principal{w.kim, w.lee, w.ana} {
		for _, st := range []string{"open", "taken", "asked", "hold", "done"} {
			reqs = append(reqs, req{w.request(t, by, by.Actor+" "+st, st), by, st})
		}
	}
	people := map[string]struct {
		p   item.Principal
		who policy.Who
	}{
		"kim": {w.kim, policy.Who{Actor: w.kim.Actor}},
		"lee": {w.lee, policy.Who{Actor: w.lee.Actor}},
		"ana": {w.ana, policy.Who{Actor: w.ana.Actor}},
		"bob": {w.bob, policy.Who{Actor: w.bob.Actor, ActorRoles: []string{"sb_team"}}},
		"cyd": {w.cyd, policy.Who{Actor: w.cyd.Actor, ActorRoles: []string{"sb_team"}}},
	}
	for name, pp := range people {
		var want []string
		for _, r := range reqs {
			if policy.WaitsOn(w.sbType, r.state, pp.who, policy.ItemFacts{CreatedBy: r.by.Actor}) {
				want = append(want, w.byName[r.id])
			}
		}
		got := w.view(t, pp.p, "me", itemservice.Filter{}, nil, 0)
		// The view also holds what the person may see only: staff see all.
		if !sameSet(got, want) {
			t.Errorf("%s: the view = %v, policy.WaitsOn = %v", name, got, want)
		}
	}
}
