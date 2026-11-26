// Tests for the reads an inbox screen needs (docs/roadmap.md, track 1, item
// 5, step 4, PR 1). The owner's decisions:
//
//   - An Item carries when it entered its state (Item.StateEnteredAt, the
//     API's state_time), on every read, for staff, an agent and a customer.
//   - Filter.In keeps the Items below a container, at any depth; the
//     container itself is not among them. Search and Count only: List has
//     its own container. A container the caller cannot see, or that does
//     not exist, is ErrNotFound; below it, visibility as always.
//   - Count is Search's words and filter, answered with a number capped at
//     50: counted through the same query and after policy.Project, so it
//     never tells of an Item a page would hide.
//   - WhoAmI says who the caller is: principal, namespace, the roles they
//     hold there; an agent's user, that user's roles, and its session.
//     About the caller only.
//
// The moves the caller may make are in caller_moves_test.go.
package itemservice_test

import (
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
)

// --- Item.StateEnteredAt on every read ---

func TestStateTime_OnEveryRead(t *testing.T) {
	w := newClaimWorld(t)
	desk := w.create(t, itemservice.CreateRequest{
		Principal: w.ana, TypeID: "wq_desk", TypeVersion: 1, Fields: map[string]any{"f_name": "Purchasing"},
	})
	id := w.create(t, itemservice.CreateRequest{
		Principal: w.ana, TypeID: "cl_purchase", TypeVersion: 1, ContainerID: &desk,
		Fields: map[string]any{"f_amount": 2500, "f_manager": w.bob.Actor},
	})
	w.move(t, w.ana, id, "manager_review", nil)
	w.move(t, w.bob, id, "finance_queue", nil)
	want := stateEnteredAt(t, id) // the column (state_entered_at_test.go)

	agent := withSession(t, w.k, item.Principal{Actor: "agent:st-bot-" + uuid.NewString()[:8], OnBehalfOf: w.fay.Actor, Namespace: w.ns})
	for _, p := range []item.Principal{w.fay, w.ana, agent} {
		got, err := w.k.Get(testCtx(t), p, id)
		if err != nil {
			t.Fatalf("Get as %s: %v", p.Actor, err)
		}
		if !got.StateEnteredAt.Equal(want) {
			t.Errorf("Get as %s: StateEnteredAt = %s, want %s (when bob sent it to the queue)", p.Actor, got.StateEnteredAt, want)
		}
		if !got.StateEnteredAt.After(got.CreatedAt) {
			t.Errorf("Get as %s: StateEnteredAt %s is not after CreatedAt %s, though the Item moved twice", p.Actor, got.StateEnteredAt, got.CreatedAt)
		}
		res, err := w.k.Search(testCtx(t), itemservice.SearchRequest{
			Principal: p, Filter: itemservice.Filter{TypeIDs: []string{"cl_purchase"}, States: []string{"finance_queue"}},
		})
		if err != nil || len(res.Items) != 1 || !res.Items[0].StateEnteredAt.Equal(want) {
			t.Errorf("Search as %s: %d Items, error %v, StateEnteredAt %v; want the one purchase with %s", p.Actor, len(res.Items), err, times(res.Items), want)
		}
		listed, err := w.k.List(testCtx(t), itemservice.ListRequest{Principal: p, ContainerID: &desk})
		if err != nil || len(listed.Items) != 1 || !listed.Items[0].StateEnteredAt.Equal(want) {
			t.Errorf("List as %s: %d Items, error %v, StateEnteredAt %v; want the one purchase with %s", p.Actor, len(listed.Items), err, times(listed.Items), want)
		}
	}
	// A new Item: it entered its state when it was created.
	got, err := w.k.Get(testCtx(t), w.ana, desk)
	if err != nil || !got.StateEnteredAt.Equal(got.CreatedAt) || got.StateEnteredAt.IsZero() {
		t.Errorf("a new Item: StateEnteredAt %s, CreatedAt %s, error %v; want the same instant", got.StateEnteredAt, got.CreatedAt, err)
	}
}

// A customer sees it on the Items they see: it says no more than the state
// and the moves in their history already do.
func TestStateTime_ACustomerSeesIt(t *testing.T) {
	w := newCustWorld(t)
	inc := w.incident(t, w.kim, w.desk)
	want := stateEnteredAt(t, inc)
	got, err := w.k.Get(testCtx(t), w.kim, inc)
	if err != nil || !got.StateEnteredAt.Equal(want) {
		t.Errorf("kim's Get of her incident: StateEnteredAt %s, error %v; want %s", got.StateEnteredAt, err, want)
	}
	res, err := w.k.Search(testCtx(t), itemservice.SearchRequest{Principal: w.kim, Filter: itemservice.Filter{TypeIDs: []string{"incident"}}})
	if err != nil || len(res.Items) != 1 || !res.Items[0].StateEnteredAt.Equal(want) {
		t.Errorf("kim's Search: %d Items, error %v, StateEnteredAt %v; want her incident with %s", len(res.Items), err, times(res.Items), want)
	}
}

func times(items []item.Item) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.StateEnteredAt.String()
	}
	return out
}

// --- Filter.In: the Items below a container ---

// inWorld is a claim world with a tree:
//
//	desk                      (ana's, open to all)
//	├── top                   a purchase in the finance queue
//	├── box                   a sub-container
//	│   └── deep              a purchase in the finance queue
//	└── vault                 a sub-container narrowed to ana, bob and gus
//	    └── secret            a purchase in the finance queue
//	outside                   a purchase at the top level
type inWorld struct {
	claimWorld
	desk, top, box, deep, vault, secret, outside uuid.UUID
	names                                        map[uuid.UUID]string
}

func newInWorld(t *testing.T) inWorld {
	t.Helper()
	w := inWorld{claimWorld: newClaimWorld(t)}
	container := func(name string, in *uuid.UUID, readers []item.Principal) uuid.UUID {
		req := itemservice.CreateRequest{
			Principal: w.ana, TypeID: "wq_desk", TypeVersion: 1, ContainerID: in, Fields: map[string]any{"f_name": name},
		}
		if readers != nil {
			req.Narrow = &itemservice.Narrow{Readers: readers2(readers)}
		}
		return w.create(t, req)
	}
	purchase := func(in *uuid.UUID) uuid.UUID {
		id := w.create(t, itemservice.CreateRequest{
			Principal: w.ana, TypeID: "cl_purchase", TypeVersion: 1, ContainerID: in,
			Fields: map[string]any{"f_amount": 2500, "f_manager": w.bob.Actor},
		})
		w.move(t, w.ana, id, "manager_review", nil)
		w.move(t, w.bob, id, "finance_queue", nil)
		return id
	}
	w.desk = container("Purchasing", nil, nil)
	w.top = purchase(&w.desk)
	w.box = container("Hardware", &w.desk, nil)
	w.deep = purchase(&w.box)
	w.vault = container("Board only", &w.desk, []item.Principal{w.ana, w.bob, w.gus})
	w.secret = purchase(&w.vault)
	w.outside = purchase(nil)
	w.names = map[uuid.UUID]string{w.desk: "desk", w.top: "top", w.box: "box", w.deep: "deep", w.vault: "vault",
		w.secret: "secret", w.outside: "outside"}
	return w
}

func readers2(ps []item.Principal) []string { return readers(ps...) }

// found is every Item a search returns, all pages, as the test's names,
// sorted.
func (w inWorld) found(t *testing.T, p item.Principal, f itemservice.Filter) []string {
	t.Helper()
	got := named(w.viewInOrder(t, p, f, nil, 2), w.names)
	slices.Sort(got)
	return got
}

func TestFilterIn_EverythingBelowAContainer(t *testing.T) {
	w := newInWorld(t)
	in := func(c uuid.UUID) itemservice.Filter { return itemservice.Filter{In: &c} }

	for _, c := range []struct {
		what string
		who  item.Principal
		f    itemservice.Filter
		want []string
	}{
		// Every depth, and not the container itself. fay does not read the
		// vault: neither it nor what is in it.
		{"below the desk, as fay", w.fay, in(w.desk), []string{"box", "deep", "top"}},
		// gus reads the vault.
		{"below the desk, as gus", w.gus, in(w.desk), []string{"box", "deep", "secret", "top", "vault"}},
		{"below the box", w.fay, in(w.box), []string{"deep"}},
		{"below the vault, as gus", w.gus, in(w.vault), []string{"secret"}},
		// A leaf holds nothing.
		{"below a purchase", w.fay, in(w.top), []string{}},
	} {
		if got := w.found(t, c.who, c.f); !slices.Equal(got, c.want) {
			t.Errorf("%s = %v, want %v", c.what, got, c.want)
		}
	}

	// With the other filters, and with waiting_on: the desk's purchases
	// that wait on fay.
	f := in(w.desk)
	f.TypeIDs = []string{"cl_purchase"}
	if got := w.found(t, w.fay, f); !slices.Equal(got, []string{"deep", "top"}) {
		t.Errorf("the purchases below the desk, as fay = %v, want deep, top", got)
	}
	f = in(w.desk)
	f.WaitingOn = "me"
	if got := w.found(t, w.fay, f); !slices.Equal(got, []string{"deep", "top"}) {
		t.Errorf("what waits on fay below the desk = %v, want deep, top (not outside, not the vault's)", got)
	}
	if got := w.found(t, w.fay, itemservice.Filter{WaitingOn: "me"}); !slices.Equal(got, []string{"deep", "outside", "top"}) {
		t.Errorf("what waits on fay anywhere = %v, want deep, outside, top", got)
	}
	// And with words.
	res, err := w.k.Search(testCtx(t), itemservice.SearchRequest{Principal: w.fay, Query: "Hardware", Filter: in(w.desk)})
	if err != nil || len(res.Items) != 1 || res.Items[0].ID != w.box {
		t.Errorf("the word Hardware below the desk: %d Items, error %v; want the box", len(res.Items), err)
	}
}

// A container the caller cannot see and one that does not exist are the
// same ErrNotFound. A listing refuses the filter: it has its container.
func TestFilterIn_Refusals(t *testing.T) {
	w := newInWorld(t)
	search := func(p item.Principal, c uuid.UUID) error {
		_, err := w.k.Search(testCtx(t), itemservice.SearchRequest{Principal: p, Filter: itemservice.Filter{In: &c}})
		return err
	}
	hidden, missing := search(w.fay, w.vault), search(w.fay, uuid.New())
	if !errors.Is(hidden, itemservice.ErrNotFound) || !errors.Is(missing, itemservice.ErrNotFound) {
		t.Errorf("below a container fay cannot see: %v; below one that does not exist: %v; want ErrNotFound for both", hidden, missing)
	}
	if hidden != nil && missing != nil && hidden.Error() != missing.Error() {
		t.Errorf("the two refusals differ: %q and %q; a hidden container must read as a missing one", hidden, missing)
	}
	// Another namespace's container does not exist here.
	other := newClaimWorld(t)
	if err := search(other.fay, w.desk); !errors.Is(err, itemservice.ErrNotFound) {
		t.Errorf("below a container of another namespace: %v, want ErrNotFound", err)
	}

	for _, parent := range []*uuid.UUID{nil, &w.desk} {
		_, err := w.k.List(testCtx(t), itemservice.ListRequest{Principal: w.fay, ContainerID: parent, Filter: itemservice.Filter{In: &w.box}})
		var inv *itemservice.InvalidError
		if !errors.As(err, &inv) || inv.Path != "filter.in" {
			t.Errorf("List with Filter.In (container %v): error = %v, want an *InvalidError at filter.in", parent, err)
		}
	}

	// A page token is bound to the container, like to the rest.
	first, err := w.k.Search(testCtx(t), itemservice.SearchRequest{Principal: w.gus, Filter: itemservice.Filter{In: &w.desk}, Limit: 1})
	if err != nil || first.NextPageToken == "" {
		t.Fatalf("the first page below the desk: token %q, error %v", first.NextPageToken, err)
	}
	_, err = w.k.Search(testCtx(t), itemservice.SearchRequest{
		Principal: w.gus, Filter: itemservice.Filter{In: &w.box}, Limit: 1, PageToken: first.NextPageToken,
	})
	if !errors.Is(err, itemservice.ErrInvalid) {
		t.Errorf("a token of below-the-desk, used below the box: %v, want ErrInvalid", err)
	}
}

// An agent searches below a container only within its session's scopes;
// a customer only below a container they can see.
func TestFilterIn_AgentsAndCustomers(t *testing.T) {
	w := newInWorld(t)
	agent := item.Principal{Actor: "agent:in-bot-" + uuid.NewString()[:8], OnBehalfOf: w.fay.Actor, Namespace: w.ns}
	req := wholeNamespace(agent)
	req.Scopes = []uuid.UUID{w.box}
	session, err := w.k.StartAgentSession(testCtx(t), req)
	if err != nil {
		t.Fatalf("start a session over the box: %v", err)
	}
	agent.AgentSession = session
	if got := w.found(t, agent, itemservice.Filter{In: &w.box}); !slices.Equal(got, []string{"deep"}) {
		t.Errorf("the agent, below the box it is scoped to = %v, want deep", got)
	}
	_, err = w.k.Search(testCtx(t), itemservice.SearchRequest{Principal: agent, Filter: itemservice.Filter{In: &w.desk}})
	if !errors.Is(err, itemservice.ErrNotFound) {
		t.Errorf("the agent, below the desk outside its scopes: %v, want ErrNotFound", err)
	}

	c := newCustWorld(t)
	inc := c.incident(t, c.kim, c.desk)
	reply := c.reply(t, c.sam, inc, "we are on it")
	c.incident(t, c.lee, c.desk) // lee's: not kim's to see
	res, err := c.k.Search(testCtx(t), itemservice.SearchRequest{Principal: c.kim, Filter: itemservice.Filter{In: &c.desk}})
	if err != nil {
		t.Fatalf("kim searches below her portal: %v", err)
	}
	var got []uuid.UUID
	for _, it := range res.Items {
		got = append(got, it.ID)
	}
	if len(got) != 2 || !slices.Contains(got, inc) || !slices.Contains(got, reply) {
		t.Errorf("kim, below her portal = %v, want her incident %s and its reply %s, and not lee's", got, inc, reply)
	}
	for what, hidden := range map[string]uuid.UUID{"a desk that is no portal": c.oldDesk, "a space": c.space} {
		_, err := c.k.Search(testCtx(t), itemservice.SearchRequest{Principal: c.kim, Filter: itemservice.Filter{In: &hidden}})
		if !errors.Is(err, itemservice.ErrNotFound) {
			t.Errorf("kim, below %s: %v, want ErrNotFound", what, err)
		}
	}
}

// --- Count ---

// searched is how many Items the search returns, every page.
func searched(t *testing.T, k *itemservice.Service, p item.Principal, query string, f itemservice.Filter) int {
	t.Helper()
	n, token := 0, ""
	for page := 0; page < 100; page++ {
		res, err := k.Search(testCtx(t), itemservice.SearchRequest{Principal: p, Query: query, Filter: f, Limit: 20, PageToken: token})
		if err != nil {
			t.Fatalf("Search(%q, %+v) as %s: %v", query, f, p.Actor, err)
		}
		n += len(res.Items)
		if token = res.NextPageToken; token == "" {
			return n
		}
	}
	t.Fatal("more than 100 pages")
	return 0
}

// A count is the search's own answer, counted: for staff, an agent and a
// customer, over Items some of them cannot see, with waiting_on, a
// container and words.
func TestCount_IsWhatTheSearchReturns(t *testing.T) {
	w := newInWorld(t)
	agent := item.Principal{Actor: "agent:ct-bot-" + uuid.NewString()[:8], OnBehalfOf: w.fay.Actor, Namespace: w.ns}
	req := wholeNamespace(agent)
	req.Scopes = []uuid.UUID{w.box}
	session, err := w.k.StartAgentSession(testCtx(t), req)
	if err != nil {
		t.Fatalf("start a session over the box: %v", err)
	}
	agent.AgentSession = session
	w.grant(t, item.Principal{Actor: agent.Actor}, "cl_finance")

	purchases := itemservice.Filter{TypeIDs: []string{"cl_purchase"}}
	type ask struct {
		what  string
		who   item.Principal
		query string
		f     itemservice.Filter
		want  int // what the tree says, so that the comparison is not 0 = 0
	}
	asks := []ask{
		{"every purchase, as fay (not the vault's)", w.fay, "", purchases, 3},
		{"every purchase, as gus (the vault's too)", w.gus, "", purchases, 4},
		{"every purchase, as the agent scoped to the box", agent, "", purchases, 1},
		{"what waits on fay", w.fay, "", itemservice.Filter{WaitingOn: "me"}, 3},
		{"what waits on gus", w.gus, "", itemservice.Filter{WaitingOn: "me"}, 4},
		{"the finance queue, as olaf", w.olaf, "", itemservice.Filter{WaitingOn: "role:cl_finance"}, 3},
		{"what waits on olaf", w.olaf, "", itemservice.Filter{WaitingOn: "me"}, 0},
		{"what waits on the agent", agent, "", itemservice.Filter{WaitingOn: "me"}, 1},
		{"below the desk, as fay", w.fay, "", itemservice.Filter{In: &w.desk}, 3},
		{"below the desk, as gus", w.gus, "", itemservice.Filter{In: &w.desk}, 5},
		{"what waits on fay below the desk", w.fay, "", itemservice.Filter{In: &w.desk, WaitingOn: "me"}, 2},
		{"the word Hardware", w.fay, "Hardware", itemservice.Filter{}, 1},
		{"the words Board only, as fay", w.fay, "Board only", itemservice.Filter{}, 0},
		{"the words Board only, as gus", w.gus, "Board only", itemservice.Filter{}, 1},
	}
	for _, a := range asks {
		got, err := w.k.Count(testCtx(t), itemservice.CountRequest{Principal: a.who, Query: a.query, Filter: a.f})
		if err != nil {
			t.Errorf("Count %s: %v", a.what, err)
			continue
		}
		n := searched(t, w.k, a.who, a.query, a.f)
		if n != a.want {
			t.Fatalf("%s: the search returns %d Items, the test expects %d: the test's own tree is off", a.what, n, a.want)
		}
		if got.Count != n || got.Capped {
			t.Errorf("Count %s = %d (capped %v), want %d as the search returns, not capped", a.what, got.Count, got.Capped, n)
		}
	}

	// A customer: her own requests and what they share, never another's.
	c := newCustWorld(t)
	inc := c.incident(t, c.kim, c.desk)
	c.reply(t, c.sam, inc, "we are on it")
	c.note(t, c.sam, inc, "staff only")
	c.incident(t, c.lee, c.desk)
	for what, f := range map[string]itemservice.Filter{"every Type": anyType, "below her portal": {In: &c.desk}} {
		for _, who := range []item.Principal{c.kim, c.lee, c.sam} {
			got, err := c.k.Count(testCtx(t), itemservice.CountRequest{Principal: who, Filter: f})
			if err != nil {
				t.Errorf("Count %s as %s: %v", what, who.Actor, err)
				continue
			}
			if n := searched(t, c.k, who, "", f); got.Count != n || got.Capped || n == 0 {
				t.Errorf("Count %s as %s = %d (capped %v), want %d as the search returns (and more than none)", what, who.Actor, got.Count, got.Capped, n)
			}
		}
	}
}

// The cap: at most 50, and Capped says there are more. Never a number the
// caller could not reach by paging: Items they cannot see are not counted,
// however many.
func TestCount_CappedAtFifty(t *testing.T) {
	w := newClaimWorld(t)
	vault := w.create(t, itemservice.CreateRequest{
		Principal: w.ana, TypeID: "wq_desk", TypeVersion: 1, Fields: map[string]any{"f_name": "Board only"},
		Narrow: &itemservice.Narrow{Readers: readers(w.ana, w.gus)},
	})
	note := func(in *uuid.UUID, text string) {
		w.create(t, itemservice.CreateRequest{
			Principal: w.ana, TypeID: "wq_note", TypeVersion: 1, ContainerID: in, Fields: map[string]any{"f_text": text},
		})
	}
	// 51 notes say "all"; the first 50 also say "fifty"; 3 say "few". 55
	// more say "all" in the vault, which fay does not read.
	for i := range 51 {
		text := "all"
		if i < 50 {
			text += " fifty"
		}
		if i < 3 {
			text += " few"
		}
		note(nil, text)
	}
	for range 55 {
		note(&vault, "all hidden")
	}
	notes := itemservice.Filter{TypeIDs: []string{"wq_note"}}
	for _, c := range []struct {
		what, query string
		who         item.Principal
		count       int
		capped      bool
	}{
		{"3 match", "few", w.fay, 3, false},
		{"exactly 50 match", "fifty", w.fay, 50, false},
		{"51 match", "all", w.fay, 50, true},
		{"106 match, as gus who reads the vault", "all", w.gus, 50, true},
		{"55 match, none fay can see", "hidden", w.fay, 0, false},
		{"55 match, as gus", "hidden", w.gus, 50, true},
		{"nothing matches", "nowhere", w.fay, 0, false},
	} {
		got, err := w.k.Count(testCtx(t), itemservice.CountRequest{Principal: c.who, Query: c.query, Filter: notes})
		if err != nil {
			t.Errorf("Count %s: %v", c.what, err)
			continue
		}
		if got.Count != c.count || got.Capped != c.capped {
			t.Errorf("Count %s = %d (capped %v), want %d (capped %v)", c.what, got.Count, got.Capped, c.count, c.capped)
		}
	}
	if itemservice.CountCap != 50 {
		t.Errorf("CountCap = %d, want 50", itemservice.CountCap)
	}
}

// A count is refused exactly where its search is.
func TestCount_RefusedAsTheSearchIs(t *testing.T) {
	w := newInWorld(t)
	kim := item.Principal{Actor: "customer:kim", Namespace: w.ns}
	for _, c := range []struct {
		what  string
		who   item.Principal
		query string
		f     itemservice.Filter
	}{
		{"neither words nor a filter", w.fay, "", itemservice.Filter{}},
		{"an unknown Type", w.fay, "", itemservice.Filter{TypeIDs: []string{"no_such_type"}}},
		{"waiting on someone else", w.fay, "", itemservice.Filter{WaitingOn: "user:gus"}},
		{"a customer's role queue", kim, "", itemservice.Filter{WaitingOn: "role:in_team"}},
		{"below a container fay cannot see", w.fay, "", itemservice.Filter{In: &w.vault}},
		{"the kernel's own actor", item.Principal{Actor: "system:timer", Namespace: w.ns}, "all", itemservice.Filter{}},
		{"an agent with no session", item.Principal{Actor: "agent:nobody", Namespace: w.ns}, "all", itemservice.Filter{}},
	} {
		_, searchErr := w.k.Search(testCtx(t), itemservice.SearchRequest{Principal: c.who, Query: c.query, Filter: c.f})
		_, countErr := w.k.Count(testCtx(t), itemservice.CountRequest{Principal: c.who, Query: c.query, Filter: c.f})
		if searchErr == nil {
			t.Fatalf("%s: the search is not refused: the test's case is off", c.what)
		}
		if countErr == nil || countErr.Error() != searchErr.Error() {
			t.Errorf("%s: Count's error = %v, want the search's own: %v", c.what, countErr, searchErr)
		}
	}
}

// --- WhoAmI ---

func TestWhoAmI(t *testing.T) {
	w := newClaimWorld(t)
	w.grant(t, w.fay, "wq_board") // with cl_finance and wq_finance: three, to be sorted
	other := newClaimWorld(t)     // another namespace
	grantRoleIn(t, other.ns, item.Principal{Actor: w.fay.Actor, Namespace: other.ns}, "elsewhere_only")

	me, err := w.k.WhoAmI(testCtx(t), w.fay)
	if err != nil {
		t.Fatalf("WhoAmI as fay: %v", err)
	}
	want := itemservice.Me{Principal: w.fay.Actor, Namespace: w.ns, Roles: []string{"cl_finance", "wq_board", "wq_finance"}}
	if fmt.Sprintf("%+v", me) != fmt.Sprintf("%+v", want) {
		t.Errorf("WhoAmI as fay = %+v, want %+v: her roles in this namespace only, sorted, and nothing else", me, want)
	}

	// Someone with no role, and a customer: themselves, no roles.
	for _, p := range []item.Principal{w.olaf, {Actor: "customer:kim", Namespace: w.ns}} {
		me, err := w.k.WhoAmI(testCtx(t), p)
		if err != nil || me.Principal != p.Actor || me.Namespace != w.ns || len(me.Roles) != 0 ||
			me.OnBehalfOf != "" || len(me.OnBehalfOfRoles) != 0 || me.Session != nil {
			t.Errorf("WhoAmI as %s = %+v, error %v; want themselves in %s, no roles, nobody acted for, no session", p.Actor, me, err, w.ns)
		}
	}

	// An agent acting for fay: its own roles, hers, and its session.
	agent := item.Principal{Actor: "agent:who-bot-" + uuid.NewString()[:8], OnBehalfOf: w.fay.Actor, Namespace: w.ns}
	w.grant(t, item.Principal{Actor: agent.Actor}, "preparer")
	desk := w.create(t, itemservice.CreateRequest{
		Principal: w.ana, TypeID: "wq_desk", TypeVersion: 1, Fields: map[string]any{"f_name": "Purchasing"},
	})
	req := wholeNamespace(agent)
	req.Scopes, req.Powers = []uuid.UUID{desk}, []string{item.PowerRead}
	session, err := w.k.StartAgentSession(testCtx(t), req)
	if err != nil {
		t.Fatalf("start a session: %v", err)
	}
	agent.AgentSession = session
	me, err = w.k.WhoAmI(testCtx(t), agent)
	if err != nil {
		t.Fatalf("WhoAmI as the agent: %v", err)
	}
	if me.Principal != agent.Actor || me.Namespace != w.ns || !slices.Equal(me.Roles, []string{"preparer"}) ||
		me.OnBehalfOf != w.fay.Actor || !slices.Equal(me.OnBehalfOfRoles, []string{"cl_finance", "wq_board", "wq_finance"}) {
		t.Errorf("WhoAmI as the agent = %+v; want the agent with preparer, acting for fay with her three roles", me)
	}
	if s := me.Session; s == nil || s.ID != session || s.Agent != agent.Actor || s.OnBehalfOf != w.fay.Actor ||
		!slices.Equal(s.Scopes, []uuid.UUID{desk}) || !slices.Equal(s.Powers, []string{item.PowerRead}) {
		t.Errorf("WhoAmI as the agent: session = %+v; want %s over the desk with the read power", me.Session, session)
	}

	// An agent must carry its own session, for the user it was started for.
	for what, p := range map[string]item.Principal{
		"a session that is not":   {Actor: agent.Actor, OnBehalfOf: w.fay.Actor, Namespace: w.ns, AgentSession: "ses_" + uuid.NewString()},
		"its session, for gus":    {Actor: agent.Actor, OnBehalfOf: w.gus.Actor, Namespace: w.ns, AgentSession: session},
		"another agent's session": {Actor: "agent:other", OnBehalfOf: w.fay.Actor, Namespace: w.ns, AgentSession: session},
	} {
		if _, err := w.k.WhoAmI(testCtx(t), p); !errors.Is(err, itemservice.ErrSessionInvalid) {
			t.Errorf("WhoAmI as the agent with %s: %v, want ErrSessionInvalid", what, err)
		}
	}
	// An agent with no session at all, the kernel's own actors (no
	// callers), and a caller that names no namespace: refused as malformed,
	// as on every read.
	for what, p := range map[string]item.Principal{
		"an agent with no session": {Actor: agent.Actor, OnBehalfOf: w.fay.Actor, Namespace: w.ns},
		"the timer":                {Actor: "system:timer", Namespace: w.ns},
		"a hook":                   {Actor: "hook:wq_held:credit", Namespace: w.ns},
		"no namespace":             {Actor: w.fay.Actor},
	} {
		if _, err := w.k.WhoAmI(testCtx(t), p); !errors.Is(err, itemservice.ErrInvalid) {
			t.Errorf("WhoAmI as %s: %v, want ErrInvalid", what, err)
		}
	}
}
