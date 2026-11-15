// Tests for Step 0, part 1: namespaces are real. A principal belongs to one
// namespace (item.Principal.Namespace; empty means "default", so the earlier
// tests keep working), and nothing crosses from one namespace to another:
// Items, containers, search, idempotency keys and roles are all per
// namespace. Types stay global for now (docs/journal.md, Later).
package itemservice_test

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
)

// newNamespace makes a namespace no other test uses and returns its name.
func newNamespace(t *testing.T, k *itemservice.Service) string {
	t.Helper()
	ns := "ns-" + uuid.NewString()[:8]
	if err := k.EnsureNamespace(testCtx(t), ns); err != nil {
		t.Fatalf("EnsureNamespace(%s): %v", ns, err)
	}
	return ns
}

// in returns p as a member of namespace ns.
func in(p item.Principal, ns string) item.Principal {
	p.Namespace = ns
	return p
}

func grantRoleIn(t *testing.T, ns string, p item.Principal, role string) {
	t.Helper()
	_, err := pool.Exec(testCtx(t),
		`insert into principal_roles (namespace_id, principal, role) values ($1, $2, $3)`, ns, p.Actor, role)
	if err != nil {
		t.Fatalf("grant role %s to %s in %s: %v", role, p.Actor, ns, err)
	}
}

func createIn(t *testing.T, k *itemservice.Service, p item.Principal, fields map[string]any, container *uuid.UUID) uuid.UUID {
	t.Helper()
	res, err := k.Create(testCtx(t), itemservice.CreateRequest{
		IdempotencyKey: newKey(), Principal: p, TypeID: "ticket", TypeVersion: 1,
		Fields: fields, ContainerID: container,
	})
	if err != nil {
		t.Fatalf("create in %q as %s: %v", p.Namespace, p.Actor, err)
	}
	return res.ItemID
}

func assertNotFound(t *testing.T, what string, err error) {
	t.Helper()
	if !errors.Is(err, itemservice.ErrNotFound) {
		t.Errorf("%s: error = %v, want ErrNotFound", what, err)
	}
}

// EnsureNamespace gives a namespace its one root scope, once.
func TestNamespaces_EnsureIsIdempotent(t *testing.T) {
	k := itemservice.New(pool)
	ns := newNamespace(t, k)
	if err := k.EnsureNamespace(testCtx(t), ns); err != nil {
		t.Fatalf("second EnsureNamespace: %v", err)
	}
	if n := count(t, `select count(*) from scopes where namespace_id = $1 and parent_id is null`, ns); n != 1 {
		t.Errorf("root scopes in %s = %d, want 1", ns, n)
	}
}

// A namespace nobody ensured does not exist: writing into it is malformed.
func TestNamespaces_UnknownNamespace_IsInvalid(t *testing.T) {
	k := itemservice.New(pool)
	_, err := k.Create(testCtx(t), itemservice.CreateRequest{
		IdempotencyKey: newKey(), Principal: in(alice, "ns-never-made"), TypeID: "ticket", TypeVersion: 1,
	})
	if !errors.Is(err, itemservice.ErrInvalid) {
		t.Fatalf("Create in an unknown namespace: error = %v, want ErrInvalid", err)
	}
}

// The core of the test the API repeats over the wire: the same principal ID
// in two namespaces is two principals. An Item of namespace B is NOT_FOUND
// for every operation from namespace A, and does not show up in A's search.
func TestNamespaces_ItemsDoNotLeak(t *testing.T) {
	ctx := testCtx(t)
	k := itemservice.New(pool)
	a, b := newNamespace(t, k), newNamespace(t, k)
	sam := person("sam") // one principal ID, used in both namespaces
	samA, samB := in(sam, a), in(sam, b)

	w := word()
	inB := createIn(t, k, samB, map[string]any{"f_title": w, "f_assignee": "user-bob"}, nil)
	inA := createIn(t, k, samA, map[string]any{"f_title": "mine"}, nil)

	_, err := k.Get(ctx, samA, inB)
	assertNotFound(t, "Get", err)

	_, err = k.Update(ctx, itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: samA, ItemID: inB, Fields: map[string]any{"f_title": "x"},
	})
	assertNotFound(t, "Update", err)

	_, err = k.Update(ctx, itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: samA, ItemID: inB,
		Policy: &itemservice.PolicyPatch{Readers: []string{sam.Actor}},
	})
	assertNotFound(t, "Update (policy)", err)

	_, err = k.Transition(ctx, itemservice.TransitionRequest{
		IdempotencyKey: newKey(), Principal: samA, ItemID: inB, ToState: "in_progress",
	})
	assertNotFound(t, "Transition", err)

	// B's Item as a container, and as a move destination, from A.
	_, err = k.Create(ctx, itemservice.CreateRequest{
		IdempotencyKey: newKey(), Principal: samA, TypeID: "ticket", TypeVersion: 1, ContainerID: &inB,
	})
	assertNotFound(t, "Create into B's Item", err)

	_, err = k.Update(ctx, itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: samA, ItemID: inA, Move: &itemservice.Move{To: &inB},
	})
	assertNotFound(t, "Move into B's Item", err)

	res, err := k.Search(ctx, itemservice.SearchRequest{Principal: samA, Query: w})
	if err != nil {
		t.Fatalf("Search from A: %v", err)
	}
	if len(res.Items) != 0 {
		t.Errorf("Search from A found %d Items of B, want 0", len(res.Items))
	}

	// Nothing above was written to B's Item; B still sees it, unchanged.
	got, err := k.Get(ctx, samB, inB)
	if err != nil {
		t.Fatalf("Get from B: %v", err)
	}
	if got.ItemSeq != 1 || got.State != "open" || got.Fields["f_title"] != w {
		t.Errorf("B's Item = seq %d, state %q, fields %v; want it untouched", got.ItemSeq, got.State, got.Fields)
	}
	res, err = k.Search(ctx, itemservice.SearchRequest{Principal: samB, Query: w})
	if err != nil || len(res.Items) != 1 {
		t.Errorf("Search from B: %d Items, err %v; want 1", len(res.Items), err)
	}
}

// An Item created in a namespace is stored in it.
func TestNamespaces_CreateStoresTheNamespace(t *testing.T) {
	k := itemservice.New(pool)
	ns := newNamespace(t, k)
	id := createIn(t, k, in(alice, ns), nil, nil)
	if n := count(t, `select count(*) from items where id = $1 and namespace_id = $2`, id, ns); n != 1 {
		t.Errorf("Item %s not stored in namespace %s", id, ns)
	}
}

// Idempotency keys are scoped by (namespace, actor, key): the same actor ID
// and key in two namespaces are two requests.
func TestNamespaces_IdempotencyKeysAreScoped(t *testing.T) {
	ctx := testCtx(t)
	k := itemservice.New(pool)
	a, b := newNamespace(t, k), newNamespace(t, k)
	key := newKey()
	req := itemservice.CreateRequest{IdempotencyKey: key, TypeID: "ticket", TypeVersion: 1}

	req.Principal = in(alice, a)
	inA, err := k.Create(ctx, req)
	if err != nil {
		t.Fatalf("Create in A: %v", err)
	}
	req.Principal = in(alice, b)
	inB, err := k.Create(ctx, req)
	if err != nil {
		t.Fatalf("Create in B with the same key: %v", err)
	}
	if inA.ItemID == inB.ItemID {
		t.Errorf("the same key in two namespaces returned one Item %s; want two", inA.ItemID)
	}
}

// Roles are per namespace: an approver in A is nobody special in B.
func TestNamespaces_RolesAreScoped(t *testing.T) {
	ctx := testCtx(t)
	k := itemservice.New(pool)
	registerFromFile(t, k, "ticket.v3.yaml")
	a, b := newNamespace(t, k), newNamespace(t, k)
	ana := person("ana")
	grantRoleIn(t, a, ana, "approver")

	inProgress := func(p item.Principal) uuid.UUID {
		t.Helper()
		res, err := k.Create(ctx, itemservice.CreateRequest{
			IdempotencyKey: newKey(), Principal: p, TypeID: "ticket", TypeVersion: 3,
			Fields: map[string]any{"f_customer_id": "cust-1", "f_amount": 100, "f_assignee": "user-bob"},
		})
		if err != nil {
			t.Fatalf("create v3 ticket in %s: %v", p.Namespace, err)
		}
		for _, to := range []string{"open", "in_progress"} {
			if _, err := k.Transition(ctx, itemservice.TransitionRequest{
				IdempotencyKey: newKey(), Principal: p, ItemID: res.ItemID, ToState: to,
			}); err != nil {
				t.Fatalf("to %s in %s: %v", to, p.Namespace, err)
			}
		}
		return res.ItemID
	}

	inB := inProgress(in(ana, b))
	_, err := k.Transition(ctx, itemservice.TransitionRequest{
		IdempotencyKey: newKey(), Principal: in(ana, b), ItemID: inB, ToState: "approved",
	})
	if !errors.Is(err, itemservice.ErrForbidden) {
		t.Errorf("approve in B with a role held in A: error = %v, want ErrForbidden", err)
	}

	inA := inProgress(in(ana, a))
	res, err := k.Transition(ctx, itemservice.TransitionRequest{
		IdempotencyKey: newKey(), Principal: in(ana, a), ItemID: inA, ToState: "approved",
	})
	if err != nil || res.Outcome != itemservice.OutcomePending {
		t.Errorf("approve in A: %+v, %v; want a pending hold", res, err)
	}
}

// The internal door is the kernel's own: it finds an Item by ID in any
// namespace, so timers and hooks work outside "default" too.
func TestNamespaces_InternalDoorReachesEveryNamespace(t *testing.T) {
	ctx := testCtx(t)
	k := itemservice.New(pool)
	registerFromFile(t, k, "ticket.v3.yaml")
	ns := newNamespace(t, k)
	ana := in(person("ana"), ns)
	grantRoleIn(t, ns, ana, "approver")

	res, err := k.Create(ctx, itemservice.CreateRequest{
		IdempotencyKey: newKey(), Principal: ana, TypeID: "ticket", TypeVersion: 3,
		Fields: map[string]any{"f_customer_id": "cust-1", "f_amount": 100, "f_assignee": "user-bob"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	id := res.ItemID
	for _, to := range []string{"open", "in_progress"} {
		if _, err := k.Transition(ctx, itemservice.TransitionRequest{
			IdempotencyKey: newKey(), Principal: ana, ItemID: id, ToState: to,
		}); err != nil {
			t.Fatalf("to %s: %v", to, err)
		}
	}
	held, err := approve(t, k, ana, id, newKey(), 0)
	if err != nil || held.Outcome != itemservice.OutcomePending {
		t.Fatalf("approve: %+v, %v; want pending", held, err)
	}

	if _, err := resolve(t, k, id, held.HoldID, itemservice.VerdictAllow, nil, ""); err != nil {
		t.Fatalf("ResolveHold on an Item in %s: %v", ns, err)
	}
	got, err := k.Get(ctx, ana, id)
	if err != nil || got.State != "approved" {
		t.Errorf("after allow: state %q, err %v; want approved", got.State, err)
	}
}
