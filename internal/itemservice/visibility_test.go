// Tests for Step 4: scopes and visibility.
//
// Every Item belongs to one scope. An Item is narrowed by giving it a scope
// node of its own with a list of readers. A principal who cannot see an Item
// gets ErrNotFound for it, on reads and on writes alike: its existence must
// not leak.
package itemservice_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
)

// --- helpers ---

// person returns a principal no other test uses. Roles and readers are rows
// in a database every test shares, so each test brings its own people.
func person(name string) item.Principal {
	return item.Principal{Actor: "user:" + name + "-" + uuid.NewString()[:8]}
}

// word returns a search term no other test uses.
func word() string {
	return "w" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
}

func readers(ps ...item.Principal) []string {
	ids := make([]string, len(ps))
	for i, p := range ps {
		ids[i] = p.Actor
	}
	return ids
}

// createAs creates a ticket as p. A nil narrow leaves the Item in its
// container's scope (the namespace's root scope when it has no container).
func createAs(t *testing.T, k *itemservice.Service, p item.Principal, title string,
	narrow *itemservice.Narrow, container *uuid.UUID) itemservice.WriteResult {
	t.Helper()
	res, err := k.Create(testCtx(t), itemservice.CreateRequest{
		IdempotencyKey: newKey(), Principal: p, TypeID: "ticket", TypeVersion: 1,
		Fields:      map[string]any{"f_title": title},
		Narrow:      narrow,
		ContainerID: container,
	})
	if err != nil {
		t.Fatalf("Create as %s: %v", p.Actor, err)
	}
	return res
}

// setPolicy changes an Item's policy as p, with no field change.
func setPolicy(t *testing.T, k *itemservice.Service, p item.Principal, id uuid.UUID,
	patch itemservice.PolicyPatch) itemservice.WriteResult {
	t.Helper()
	res, err := k.Update(testCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: p, ItemID: id, Policy: &patch,
	})
	if err != nil {
		t.Fatalf("Update policy as %s: %v", p.Actor, err)
	}
	return res
}

// assertHidden checks that p cannot tell the Item exists.
func assertHidden(t *testing.T, k *itemservice.Service, p item.Principal, id uuid.UUID) {
	t.Helper()
	_, err := k.Get(testCtx(t), p, id)
	if !errors.Is(err, itemservice.ErrNotFound) {
		t.Errorf("Get as %s: error = %v, want ErrNotFound", p.Actor, err)
	}
	if errors.Is(err, itemservice.ErrForbidden) {
		t.Errorf("Get as %s: error is ErrForbidden, which would reveal that the Item exists", p.Actor)
	}
}

func assertVisible(t *testing.T, k *itemservice.Service, p item.Principal, id uuid.UUID) {
	t.Helper()
	it, err := k.Get(testCtx(t), p, id)
	if err != nil {
		t.Fatalf("Get as %s: %v", p.Actor, err)
	}
	if it.ID != id {
		t.Errorf("Get as %s returned Item %s, want %s", p.Actor, it.ID, id)
	}
}

// searchIDs returns the IDs a search for query gives p.
func searchIDs(t *testing.T, k *itemservice.Service, p item.Principal, query string, minSeq int64) []uuid.UUID {
	t.Helper()
	res, err := k.Search(testCtx(t), itemservice.SearchRequest{Principal: p, Query: query, MinSeq: minSeq})
	if err != nil {
		t.Fatalf("Search as %s: %v", p.Actor, err)
	}
	ids := make([]uuid.UUID, len(res.Items))
	for i, it := range res.Items {
		ids[i] = it.ID
	}
	return ids
}

// eventPayload reads one event's op and payload.
func eventPayload(t *testing.T, id uuid.UUID, itemSeq int64) (op string, payload map[string]any, raw string) {
	t.Helper()
	var b []byte
	err := pool.QueryRow(testCtx(t),
		`select op, payload from item_events where item_id = $1 and item_seq = $2`, id, itemSeq).Scan(&op, &b)
	if err != nil {
		t.Fatalf("read event %d: %v", itemSeq, err)
	}
	if err := json.Unmarshal(b, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	return op, payload, string(b)
}

// --- the root scope ---

// An Item that is not narrowed sits in the namespace's root scope, which
// everyone in the namespace can read. scope_id is never null.
func TestCreate_PlainItem_IsInTheRootScope(t *testing.T) {
	k := itemservice.New(pool)
	alice, bob := person("alice"), person("bob")
	a := createAs(t, k, alice, "plain one", nil, nil)
	b := createAs(t, k, bob, "plain two", nil, nil)

	var (
		scopeA, scopeB uuid.UUID
		isRoot, narrow bool
	)
	err := pool.QueryRow(testCtx(t),
		`select i.scope_id, s.parent_id is null, s.narrows
		 from items i join scopes s on s.id = i.scope_id where i.id = $1`, a.ItemID).Scan(&scopeA, &isRoot, &narrow)
	if err != nil {
		t.Fatalf("read scope: %v", err)
	}
	if !isRoot || narrow {
		t.Errorf("scope: root = %v, narrows = %v; want a root scope that does not narrow", isRoot, narrow)
	}
	if err := pool.QueryRow(testCtx(t), `select scope_id from items where id = $1`, b.ItemID).Scan(&scopeB); err != nil {
		t.Fatalf("read scope: %v", err)
	}
	if scopeA != scopeB {
		t.Errorf("two plain Items are in scopes %s and %s, want the one root scope", scopeA, scopeB)
	}

	assertVisible(t, k, bob, a.ItemID)
	assertVisible(t, k, alice, b.ItemID)
	if it, _ := k.Get(testCtx(t), alice, a.ItemID); it.ScopeID == nil || *it.ScopeID != scopeA {
		t.Errorf("Get: ScopeID = %v, want %s", it.ScopeID, scopeA)
	}
}

// The schema refuses an Item without a scope: there is no "null means everyone".
func TestSchema_ScopeIsRequiredAndIndexed(t *testing.T) {
	n := count(t, `select count(*) from information_schema.columns
	               where table_name = 'items' and column_name = 'scope_id' and is_nullable = 'NO'`)
	if n != 1 {
		t.Error("items.scope_id is nullable, want NOT NULL")
	}
	if n := count(t, `select count(*) from items where scope_id is null`); n != 0 {
		t.Errorf("%d Items have no scope, want 0", n)
	}
	n = count(t, `select count(*) from pg_indexes where tablename = 'items'
	              and indexdef like '%(namespace_id, scope_id)%'`)
	if n != 1 {
		t.Errorf("indexes on items (namespace_id, scope_id): found %d, want 1", n)
	}
	n = count(t, `select count(*) from pg_indexes where tablename = 'items' and indexdef ilike '%using gin%'`)
	if n != 1 {
		t.Errorf("GIN indexes on items: found %d, want 1", n)
	}
}

// --- one narrowed Item ---

// The step's "done when": a second user never sees the narrowed Item, through
// Get or through Search.
func TestNarrowedItem_IsHiddenFromASecondUser(t *testing.T) {
	k := itemservice.New(pool)
	alice, bob := person("alice"), person("bob")
	term := word()
	res := createAs(t, k, alice, "secret "+term, &itemservice.Narrow{Readers: readers(alice)}, nil)

	assertVisible(t, k, alice, res.ItemID)
	if got := searchIDs(t, k, alice, term, res.CommitSeq); !slices.Contains(got, res.ItemID) {
		t.Errorf("alice's search = %v, want it to hold %s", got, res.ItemID)
	}

	assertHidden(t, k, bob, res.ItemID)
	if got := searchIDs(t, k, bob, term, res.CommitSeq); len(got) != 0 {
		t.Errorf("bob's search = %v, want nothing", got)
	}
}

// Narrowing creates a scope node under the root with exactly the readers
// named, and records them on the create event as opaque IDs.
func TestCreate_Narrow_CreatesAScopeAndRecordsTheReaders(t *testing.T) {
	k := itemservice.New(pool)
	alice, bob := person("alice"), person("bob")
	res := createAs(t, k, alice, "for two", &itemservice.Narrow{Readers: readers(bob, alice)}, nil)

	n := count(t, `select count(*) from items i join scopes s on s.id = i.scope_id
	               join scopes parent on parent.id = s.parent_id
	               where i.id = $1 and s.narrows and parent.parent_id is null`, res.ItemID)
	if n != 1 {
		t.Error("the narrowed Item's scope is not a narrowing node under the root scope")
	}
	n = count(t, `select count(*) from scope_readers r join items i on i.scope_id = r.scope_id
	              where i.id = $1 and r.principal = any($2)`, res.ItemID, readers(alice, bob))
	if n != 2 {
		t.Errorf("scope_readers rows for alice and bob = %d, want 2", n)
	}
	n = count(t, `select count(*) from scope_readers r join items i on i.scope_id = r.scope_id
	              where i.id = $1`, res.ItemID)
	if n != 2 {
		t.Errorf("scope_readers rows = %d, want 2", n)
	}

	op, payload, _ := eventPayload(t, res.ItemID, 1)
	want := []any{alice.Actor, bob.Actor}
	slices.SortFunc(want, func(a, b any) int { return strings.Compare(a.(string), b.(string)) })
	if op != "create" || !reflect.DeepEqual(payload["readers"], want) {
		t.Errorf("event: op = %q, readers = %v; want create with readers %v (sorted)", op, payload["readers"], want)
	}
	assertNothingWritten(t, res.ItemID, 1) // still one event and one outbox row
}

// --- "even right after a change" ---

// An Item everyone could see is narrowed. The second user's very next Get
// and Search must not return it: no cache, no grace window.
func TestItemNarrowedLater_IsHiddenAtOnce(t *testing.T) {
	k := itemservice.New(pool)
	alice, bob := person("alice"), person("bob")
	term := word()
	created := createAs(t, k, alice, "open "+term, nil, nil)

	// Bob can see it first, so the test proves a change and not a constant.
	assertVisible(t, k, bob, created.ItemID)
	if got := searchIDs(t, k, bob, term, created.CommitSeq); !slices.Contains(got, created.ItemID) {
		t.Fatalf("before narrowing, bob's search = %v, want it to hold the Item", got)
	}

	narrowed := setPolicy(t, k, alice, created.ItemID, itemservice.PolicyPatch{Readers: readers(alice)})

	assertHidden(t, k, bob, created.ItemID)
	if got := searchIDs(t, k, bob, term, narrowed.CommitSeq); len(got) != 0 {
		t.Errorf("after narrowing, bob's search = %v, want nothing", got)
	}
	// Without min_seq too: the filter is live, not a matter of waiting.
	if got := searchIDs(t, k, bob, term, 0); len(got) != 0 {
		t.Errorf("after narrowing, bob's search without min_seq = %v, want nothing", got)
	}
	assertVisible(t, k, alice, created.ItemID)
}

// A reader is removed from a narrowed Item. Their very next Get and Search
// must not return it.
func TestReaderRemoved_IsHiddenAtOnce(t *testing.T) {
	k := itemservice.New(pool)
	alice, bob := person("alice"), person("bob")
	term := word()
	created := createAs(t, k, alice, "shared "+term, &itemservice.Narrow{Readers: readers(alice, bob)}, nil)

	assertVisible(t, k, bob, created.ItemID)
	if got := searchIDs(t, k, bob, term, created.CommitSeq); !slices.Contains(got, created.ItemID) {
		t.Fatalf("before removal, bob's search = %v, want it to hold the Item", got)
	}

	removed := setPolicy(t, k, alice, created.ItemID, itemservice.PolicyPatch{Readers: readers(alice)})

	assertHidden(t, k, bob, created.ItemID)
	if got := searchIDs(t, k, bob, term, removed.CommitSeq); len(got) != 0 {
		t.Errorf("after removal, bob's search = %v, want nothing", got)
	}
	if got := searchIDs(t, k, bob, term, 0); len(got) != 0 {
		t.Errorf("after removal, bob's search without min_seq = %v, want nothing", got)
	}
	assertVisible(t, k, alice, created.ItemID)
}

// --- the policy patch is an ordinary write ---

// A policy change bumps item_seq and writes an "update" event and an outbox
// row. The payload names the readers by ID; it sets no field, so there is no
// values row.
func TestUpdate_PolicyPatch_IsRecordedAsAnEvent(t *testing.T) {
	k := itemservice.New(pool)
	alice, bob := person("alice"), person("bob")
	created := createAs(t, k, alice, "to narrow", nil, nil)

	res := setPolicy(t, k, alice, created.ItemID, itemservice.PolicyPatch{Readers: readers(bob, alice)})
	if res.ItemSeq != 2 || res.CommitSeq <= created.CommitSeq {
		t.Errorf("result = %+v, want item_seq 2 and a later commit_seq than %d", res, created.CommitSeq)
	}

	op, payload, _ := eventPayload(t, created.ItemID, 2)
	want := []any{alice.Actor, bob.Actor}
	slices.SortFunc(want, func(a, b any) int { return strings.Compare(a.(string), b.(string)) })
	if op != "update" || !reflect.DeepEqual(payload["readers"], want) {
		t.Errorf("event: op = %q, readers = %v; want update with readers %v (sorted)", op, payload["readers"], want)
	}
	if n := count(t, `select count(*) from outbox where item_id = $1 and item_seq = 2`, created.ItemID); n != 1 {
		t.Errorf("outbox rows for the policy change = %d, want 1", n)
	}
	if n := count(t, `select count(*) from item_event_values where item_id = $1 and item_seq = 2`, created.ItemID); n != 0 {
		t.Errorf("values rows for the policy change = %d, want 0", n)
	}
	// The fields are untouched.
	if it := getAs(t, k, alice, created.ItemID); it.Fields["f_title"] != "to narrow" || it.ItemSeq != 2 {
		t.Errorf("after the policy change: fields = %v, item_seq = %d", it.Fields, it.ItemSeq)
	}
}

// One Update may change fields and policy together: one event, one item_seq.
func TestUpdate_FieldsAndPolicyTogether_IsOneWrite(t *testing.T) {
	k := itemservice.New(pool)
	alice, bob := person("alice"), person("bob")
	created := createAs(t, k, alice, "before", nil, nil)

	res, err := k.Update(testCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: alice, ItemID: created.ItemID,
		Fields: map[string]any{"f_title": "after"},
		Policy: &itemservice.PolicyPatch{Readers: readers(alice)},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if res.ItemSeq != 2 {
		t.Errorf("item_seq = %d, want 2", res.ItemSeq)
	}
	_, payload, _ := eventPayload(t, created.ItemID, 2)
	if !reflect.DeepEqual(payload["field_ids"], []any{"f_title"}) || !reflect.DeepEqual(payload["readers"], []any{alice.Actor}) {
		t.Errorf("payload = %v, want field_ids [f_title] and readers [%s]", payload, alice.Actor)
	}
	assertHidden(t, k, bob, created.ItemID)
}

// Inherit removes the Item's own narrowing: it goes back to its container's
// scope (here the root), and the second user sees it again.
func TestUpdate_PolicyInherit_WidensBackToTheParent(t *testing.T) {
	k := itemservice.New(pool)
	alice, bob := person("alice"), person("bob")
	created := createAs(t, k, alice, "narrow then not", &itemservice.Narrow{Readers: readers(alice)}, nil)
	assertHidden(t, k, bob, created.ItemID)

	res := setPolicy(t, k, alice, created.ItemID, itemservice.PolicyPatch{Inherit: true})

	assertVisible(t, k, bob, created.ItemID)
	op, payload, _ := eventPayload(t, created.ItemID, res.ItemSeq)
	if op != "update" || payload["inherit"] != true {
		t.Errorf("event: op = %q, payload = %v; want update with inherit true", op, payload)
	}
	n := count(t, `select count(*) from items i join scopes s on s.id = i.scope_id
	               where i.id = $1 and s.parent_id is null`, created.ItemID)
	if n != 1 {
		t.Error("after inherit, the Item is not back in the root scope")
	}
}

// A malformed policy is refused before anything is written.
func TestPolicy_MalformedRequests_AreInvalid(t *testing.T) {
	k := itemservice.New(pool)
	alice := person("alice")
	created := createAs(t, k, alice, "plain", nil, nil)

	cases := []struct {
		name  string
		patch itemservice.PolicyPatch
	}{
		{"neither readers nor inherit", itemservice.PolicyPatch{}},
		{"readers and inherit together", itemservice.PolicyPatch{Readers: readers(alice), Inherit: true}},
		{"an empty reader ID", itemservice.PolicyPatch{Readers: []string{alice.Actor, ""}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := k.Update(testCtx(t), itemservice.UpdateRequest{
				IdempotencyKey: newKey(), Principal: alice, ItemID: created.ItemID, Policy: &c.patch,
			})
			if !errors.Is(err, itemservice.ErrInvalid) {
				t.Errorf("Update error = %v, want ErrInvalid", err)
			}
		})
	}
	t.Run("create narrowed to nobody", func(t *testing.T) {
		_, err := k.Create(testCtx(t), itemservice.CreateRequest{
			IdempotencyKey: newKey(), Principal: alice, TypeID: "ticket", TypeVersion: 1,
			Narrow: &itemservice.Narrow{},
		})
		if !errors.Is(err, itemservice.ErrInvalid) {
			t.Errorf("Create error = %v, want ErrInvalid", err)
		}
	})
	assertNothingWritten(t, created.ItemID, 1)
}

// A policy change that would hide the Item from the one making it is refused:
// nobody locks themselves out. With on_behalf_of set, both must stay readers.
// (Recovering an Item nobody can see needs grants, which come later.)
func TestPolicy_HidingTheItemFromYourself_IsInvalid(t *testing.T) {
	k := itemservice.New(pool)
	alice, bob := person("alice"), person("bob")
	agent := item.Principal{Actor: "agent:" + uuid.NewString()[:8], OnBehalfOf: alice.Actor, AgentSession: "session-" + uuid.NewString()[:8]}
	created := createAs(t, k, alice, "mine", nil, nil)

	check := func(what string, err error) {
		t.Helper()
		if !errors.Is(err, itemservice.ErrInvalid) {
			t.Errorf("%s: error = %v, want ErrInvalid", what, err)
			return
		}
		if !strings.Contains(err.Error(), "hide the Item from you") {
			t.Errorf("%s: reason %q does not say the change would hide the Item from you", what, err)
		}
	}

	_, err := k.Create(testCtx(t), itemservice.CreateRequest{
		IdempotencyKey: newKey(), Principal: alice, TypeID: "ticket", TypeVersion: 1,
		Narrow: &itemservice.Narrow{Readers: readers(bob)},
	})
	check("Create narrowed to someone else", err)

	_, err = k.Update(testCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: alice, ItemID: created.ItemID,
		Policy: &itemservice.PolicyPatch{Readers: readers(bob)},
	})
	check("Update narrowing to someone else", err)

	// An agent acting for alice: naming only one of the two is not enough.
	_, err = k.Update(testCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: agent, ItemID: created.ItemID,
		Policy: &itemservice.PolicyPatch{Readers: []string{agent.Actor}},
	})
	check("Update that leaves out on_behalf_of", err)
	_, err = k.Update(testCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: agent, ItemID: created.ItemID,
		Policy: &itemservice.PolicyPatch{Readers: readers(alice)},
	})
	check("Update that leaves out the acting agent", err)

	assertNothingWritten(t, created.ItemID, 1)
	assertVisible(t, k, bob, created.ItemID) // still in the root scope

	// Naming both is fine.
	if _, err := k.Update(testCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: agent, ItemID: created.ItemID,
		Policy: &itemservice.PolicyPatch{Readers: []string{agent.Actor, alice.Actor}},
	}); err != nil {
		t.Errorf("Update that keeps both the agent and the user: %v", err)
	}
}

// --- hidden Items and writes ---

// A principal who cannot see an Item cannot Update or Transition it, and is
// told ErrNotFound, exactly as for an Item that does not exist. Nothing is
// written, and an expected_item_seq does not turn the answer into ErrStale.
func TestHiddenItem_WritesReturnNotFound(t *testing.T) {
	k := itemservice.New(pool)
	alice, bob := person("alice"), person("bob")
	created := createAs(t, k, alice, "secret", &itemservice.Narrow{Readers: readers(alice)}, nil)

	check := func(what string, err error) {
		t.Helper()
		if !errors.Is(err, itemservice.ErrNotFound) {
			t.Errorf("%s: error = %v, want ErrNotFound", what, err)
		}
		for _, other := range []error{itemservice.ErrForbidden, itemservice.ErrStale, itemservice.ErrRejected} {
			if errors.Is(err, other) {
				t.Errorf("%s: error matches %v, which would reveal that the Item exists", what, other)
			}
		}
	}

	_, err := k.Update(testCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: bob, ItemID: created.ItemID,
		Fields: map[string]any{"f_title": "mine now"},
	})
	check("Update", err)

	_, err = k.Update(testCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: bob, ItemID: created.ItemID,
		Fields: map[string]any{"f_title": "mine now"}, ExpectedItemSeq: 7,
	})
	check("Update with a wrong expected_item_seq", err)

	_, err = k.Update(testCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: bob, ItemID: created.ItemID,
		Policy: &itemservice.PolicyPatch{Readers: readers(bob)},
	})
	check("Update of the policy", err)

	_, err = k.Transition(testCtx(t), itemservice.TransitionRequest{
		IdempotencyKey: newKey(), Principal: bob, ItemID: created.ItemID, ToState: "canceled",
	})
	check("a legal Transition", err)

	_, err = k.Transition(testCtx(t), itemservice.TransitionRequest{
		IdempotencyKey: newKey(), Principal: bob, ItemID: created.ItemID, ToState: "resolved",
	})
	check("an illegal Transition", err)

	assertNothingWritten(t, created.ItemID, 1)
	if it := getAs(t, k, alice, created.ItemID); it.Fields["f_title"] != "secret" || it.State != "open" {
		t.Errorf("the hidden Item changed: fields = %v, state = %q", it.Fields, it.State)
	}
}

// --- containers ---

// An Item's access comes from its nearest narrowing ancestor, and a child can
// narrow its container's access but never widen it.
func TestContainer_ChildInheritsAndCannotWiden(t *testing.T) {
	k := itemservice.New(pool)
	alice, bob, carol := person("alice"), person("bob"), person("carol")
	term := word()
	space := createAs(t, k, alice, "space", &itemservice.Narrow{Readers: readers(alice, bob)}, nil)

	// A child that keeps the default is in its container's scope.
	inherits := createAs(t, k, alice, "child "+term, nil, &space.ItemID)
	assertVisible(t, k, bob, inherits.ItemID)
	assertHidden(t, k, carol, inherits.ItemID)
	n := count(t, `select count(*) from items c join items p on p.id = c.container_id
	               where c.id = $1 and c.scope_id = p.scope_id`, inherits.ItemID)
	if n != 1 {
		t.Error("a child that is not narrowed should share its container's scope")
	}

	// A child narrowed to alice and carol: carol is not a reader of the
	// container, so naming her on the child gives her nothing.
	narrower := createAs(t, k, alice, "child "+term, &itemservice.Narrow{Readers: readers(alice, carol)}, &space.ItemID)
	assertVisible(t, k, alice, narrower.ItemID)
	assertHidden(t, k, bob, narrower.ItemID)   // narrowed away from bob
	assertHidden(t, k, carol, narrower.ItemID) // never widened to carol

	if got := searchIDs(t, k, carol, term, narrower.CommitSeq); len(got) != 0 {
		t.Errorf("carol's search = %v, want nothing", got)
	}
	if got := searchIDs(t, k, bob, term, narrower.CommitSeq); !reflect.DeepEqual(got, []uuid.UUID{inherits.ItemID}) {
		t.Errorf("bob's search = %v, want only %s", got, inherits.ItemID)
	}

	// Removing bob from the container removes him from the child that inherits.
	setPolicy(t, k, alice, space.ItemID, itemservice.PolicyPatch{Readers: readers(alice)})
	assertHidden(t, k, bob, inherits.ItemID)
}

// Narrowing a container that already has children hides the children that
// inherit from it too, and lifting the narrowing shows them again.
func TestContainer_NarrowedLater_HidesItsChildren(t *testing.T) {
	k := itemservice.New(pool)
	alice, bob := person("alice"), person("bob")
	space := createAs(t, k, alice, "space", nil, nil)
	child := createAs(t, k, alice, "child", nil, &space.ItemID)
	grandchild := createAs(t, k, alice, "grandchild", nil, &child.ItemID)
	own := createAs(t, k, alice, "own readers", &itemservice.Narrow{Readers: readers(alice, bob)}, &space.ItemID)
	outside := createAs(t, k, alice, "not in the space", nil, nil)
	assertVisible(t, k, bob, grandchild.ItemID)

	setPolicy(t, k, alice, space.ItemID, itemservice.PolicyPatch{Readers: readers(alice)})

	assertHidden(t, k, bob, space.ItemID)
	assertHidden(t, k, bob, child.ItemID)
	assertHidden(t, k, bob, grandchild.ItemID)
	assertHidden(t, k, bob, own.ItemID) // its own list names bob, but the space no longer does
	assertVisible(t, k, bob, outside.ItemID)
	assertVisible(t, k, alice, grandchild.ItemID)
	// Re-pointing a child's scope is not a write to the child.
	assertNothingWritten(t, child.ItemID, 1)

	setPolicy(t, k, alice, space.ItemID, itemservice.PolicyPatch{Inherit: true})

	assertVisible(t, k, bob, child.ItemID)
	assertVisible(t, k, bob, grandchild.ItemID)
	assertVisible(t, k, bob, own.ItemID)
}

// Creating an Item inside a container you cannot see is refused as if the
// container did not exist.
func TestCreate_InAHiddenContainer_ReturnsNotFound(t *testing.T) {
	k := itemservice.New(pool)
	alice, bob := person("alice"), person("bob")
	space := createAs(t, k, alice, "space", &itemservice.Narrow{Readers: readers(alice)}, nil)

	_, err := k.Create(testCtx(t), itemservice.CreateRequest{
		IdempotencyKey: newKey(), Principal: bob, TypeID: "ticket", TypeVersion: 1,
		ContainerID: &space.ItemID,
	})
	if !errors.Is(err, itemservice.ErrNotFound) {
		t.Errorf("Create error = %v, want ErrNotFound", err)
	}
	if n := count(t, `select count(*) from items where container_id = $1`, space.ItemID); n != 0 {
		t.Errorf("Items in the hidden container = %d, want 0", n)
	}
}

// getAs is get for any principal; get, from Step 2, always reads as alice.
func getAs(t *testing.T, k *itemservice.Service, p item.Principal, id uuid.UUID) item.Item {
	t.Helper()
	it, err := k.Get(testCtx(t), p, id)
	if err != nil {
		t.Fatalf("Get as %s: %v", p.Actor, err)
	}
	return it
}
