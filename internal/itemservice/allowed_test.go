// Tests for Step 4: who may make a transition.
//
// A transition's `allowed` list names roles and principals. "Is the move
// legal" stays with lifecycle.Transition; "may this principal make it" is
// policy, asked afterwards. A refusal is ErrForbidden, and only for an Item
// the principal can see: a hidden Item is always ErrNotFound.
//
// Roles are rows in principal_roles: the server's own truth, never something
// a request claims. Tests write those rows directly.
package itemservice_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// requestType is a small Type that uses `allowed`. The ticket Type does not.
const requestType = `
id: request
version: 1
initial_state: open
fields:
  - {id: f_title, type: string}
states:
  - {id: open,        category: todo}
  - {id: in_progress, category: active, timer: {after: 48h, to: open}}
  - {id: done,        category: done}
transitions:
  - {from: open,        to: in_progress, allowed: ['role:support']}
  - {from: in_progress, to: open,        allowed: ['role:support', 'principal:system:timer']}
  - {from: in_progress, to: done,        allowed: ['principal:user-owner']}
`

var (
	owner       = item.Principal{Actor: "user-owner"} // named by the Type itself
	systemTimer = item.Principal{Actor: "system:timer"}
)

// createRequest registers the request Type (a no-op after the first time)
// and creates one Item of it as p.
func createRequest(t *testing.T, k *itemservice.Service, p item.Principal, narrow *itemservice.Narrow) uuid.UUID {
	t.Helper()
	ty, err := typedef.Parse([]byte(requestType))
	if err != nil {
		t.Fatalf("parse request Type: %v", err)
	}
	if err := k.RegisterType(testCtx(t), ty); err != nil {
		t.Fatalf("register request Type: %v", err)
	}
	res, err := k.Create(testCtx(t), itemservice.CreateRequest{
		IdempotencyKey: newKey(), Principal: p, TypeID: "request", TypeVersion: 1,
		Fields: map[string]any{"f_title": "please"}, Narrow: narrow,
	})
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	return res.ItemID
}

func grantRole(t *testing.T, p item.Principal, role string) {
	t.Helper()
	_, err := pool.Exec(testCtx(t),
		`insert into principal_roles (namespace_id, principal, role) values ('default', $1, $2)`, p.Actor, role)
	if err != nil {
		t.Fatalf("grant role %s to %s: %v", role, p.Actor, err)
	}
}

func revokeRole(t *testing.T, p item.Principal, role string) {
	t.Helper()
	_, err := pool.Exec(testCtx(t),
		`delete from principal_roles where namespace_id = 'default' and principal = $1 and role = $2`, p.Actor, role)
	if err != nil {
		t.Fatalf("revoke role %s from %s: %v", role, p.Actor, err)
	}
}

// move asks for a transition as p and returns the error.
func move(t *testing.T, k *itemservice.Service, p item.Principal, id uuid.UUID, to, key string) error {
	t.Helper()
	_, err := k.Transition(testCtx(t), itemservice.TransitionRequest{
		IdempotencyKey: key, Principal: p, ItemID: id, ToState: to,
	})
	return err
}

func assertForbidden(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, itemservice.ErrForbidden) {
		t.Fatalf("error = %v, want ErrForbidden", err)
	}
	for _, other := range []error{itemservice.ErrRejected, itemservice.ErrNotFound, itemservice.ErrStale} {
		if errors.Is(err, other) {
			t.Errorf("error also matches %v; ErrForbidden must be distinct", other)
		}
	}
}

// A principal who can see the Item but is not in `allowed` gets ErrForbidden,
// with a reason, and nothing is written.
func TestTransition_NotAllowed_IsForbidden(t *testing.T) {
	k := itemservice.New(pool)
	sam := person("sam")
	id := createRequest(t, k, sam, nil)

	err := move(t, k, sam, id, "in_progress", newKey())
	assertForbidden(t, err)
	if !strings.Contains(err.Error(), "in_progress") {
		t.Errorf("reason %q does not name the transition", err)
	}
	assertNothingWritten(t, id, 1)
	if it := getAs(t, k, sam, id); it.State != "open" {
		t.Errorf("state = %q, want open", it.State)
	}
}

// A principal holding a role the transition lists may make the move.
func TestTransition_AllowedByRole_IsAccepted(t *testing.T) {
	k := itemservice.New(pool)
	sam := person("sam")
	grantRole(t, sam, "support")
	id := createRequest(t, k, sam, nil)

	if err := move(t, k, sam, id, "in_progress", newKey()); err != nil {
		t.Fatalf("Transition: %v", err)
	}
	if it := getAs(t, k, sam, id); it.State != "in_progress" || it.ItemSeq != 2 {
		t.Errorf("state = %q, item_seq = %d; want in_progress at 2", it.State, it.ItemSeq)
	}
}

// A `principal:<id>` entry allows that one principal and nobody else: a role
// the transition does not list is no help.
func TestTransition_AllowedByPrincipalID(t *testing.T) {
	k := itemservice.New(pool)
	sam := person("sam")
	grantRole(t, sam, "support")
	id := createRequest(t, k, sam, nil)
	if err := move(t, k, sam, id, "in_progress", newKey()); err != nil {
		t.Fatalf("Transition to in_progress: %v", err)
	}

	assertForbidden(t, move(t, k, sam, id, "done", newKey()))
	if err := move(t, k, owner, id, "done", newKey()); err != nil {
		t.Fatalf("Transition to done as the named principal: %v", err)
	}
}

// A transition without `allowed` is open to anyone who can see the Item
// (the ticket Type declares none).
func TestTransition_WithoutAllowed_AnyoneWhoSeesTheItemMayAct(t *testing.T) {
	k := itemservice.New(pool)
	alice, bob := person("alice"), person("bob")
	res := createAs(t, k, alice, "open to all", nil, nil)

	if err := move(t, k, bob, res.ItemID, "canceled", newKey()); err != nil {
		t.Fatalf("Transition as bob, who holds no role: %v", err)
	}
}

// The lifecycle is asked first. A move the Type does not declare is
// ErrRejected whoever asks, so ErrForbidden always means "legal, but not for you".
func TestTransition_IllegalMove_IsRejectedBeforePolicyIsAsked(t *testing.T) {
	k := itemservice.New(pool)
	sam := person("sam") // no role
	id := createRequest(t, k, sam, nil)

	err := move(t, k, sam, id, "done", newKey()) // open -> done is not declared
	if !errors.Is(err, itemservice.ErrRejected) || errors.Is(err, itemservice.ErrForbidden) {
		t.Errorf("error = %v, want ErrRejected and not ErrForbidden", err)
	}
}

// Hidden wins over forbidden: a principal who can neither see the Item nor
// make the move learns only that there is no such Item.
func TestTransition_HiddenItem_IsNotFoundEvenWhenNotAllowed(t *testing.T) {
	k := itemservice.New(pool)
	sam, eve := person("sam"), person("eve")
	id := createRequest(t, k, sam, &itemservice.Narrow{Readers: readers(sam)})

	err := move(t, k, eve, id, "in_progress", newKey())
	if !errors.Is(err, itemservice.ErrNotFound) || errors.Is(err, itemservice.ErrForbidden) {
		t.Errorf("error = %v, want ErrNotFound and not ErrForbidden", err)
	}
	assertNothingWritten(t, id, 1)
}

// A forbidden write rolls back like a rejected one, so it does not use up its
// idempotency key: once the role is granted, the same key goes through.
func TestTransition_ForbiddenDoesNotConsumeTheKey(t *testing.T) {
	k := itemservice.New(pool)
	sam := person("sam")
	id := createRequest(t, k, sam, nil)
	key := newKey()

	assertForbidden(t, move(t, k, sam, id, "in_progress", key))
	grantRole(t, sam, "support")
	if err := move(t, k, sam, id, "in_progress", key); err != nil {
		t.Fatalf("retry with the same key after the role was granted: %v", err)
	}
}

// Roles are read on every write. Take the role away and the very next move
// is forbidden: no cache.
func TestTransition_RoleRevoked_IsForbiddenAtOnce(t *testing.T) {
	k := itemservice.New(pool)
	sam := person("sam")
	grantRole(t, sam, "support")
	id := createRequest(t, k, sam, nil)
	if err := move(t, k, sam, id, "in_progress", newKey()); err != nil {
		t.Fatalf("Transition with the role: %v", err)
	}

	revokeRole(t, sam, "support")
	assertForbidden(t, move(t, k, sam, id, "open", newKey()))
	assertNothingWritten(t, id, 2)
}

// The timer is not a principal: its write is not subject to visibility, so it
// can move an Item narrowed to one person. It is still subject to `allowed`,
// where the Type had to list it (see typedef's load check).
func TestTransition_TimerMovesANarrowedItem(t *testing.T) {
	k := itemservice.New(pool)
	sam := person("sam")
	grantRole(t, sam, "support")
	id := createRequest(t, k, sam, &itemservice.Narrow{Readers: readers(sam)})
	if err := move(t, k, sam, id, "in_progress", newKey()); err != nil {
		t.Fatalf("Transition to in_progress: %v", err)
	}

	if err := move(t, k, systemTimer, id, "open", newKey()); err != nil {
		t.Fatalf("the timer's move back to open: %v", err)
	}
	if it := getAs(t, k, sam, id); it.State != "open" {
		t.Errorf("state = %q, want open", it.State)
	}
	// The exemption is for writes the timer makes, not for reading as it.
	assertHidden(t, k, systemTimer, id)
}

// An agent acting on behalf of a user has the rights of both, intersected:
// both must see the Item, and both must pass `allowed`.
func TestOnBehalfOf_BothMustSeeAndBothMustBeAllowed(t *testing.T) {
	k := itemservice.New(pool)
	uma := person("uma")
	agent := item.Principal{Actor: "agent-" + uuid.NewString()[:8]}
	both := item.Principal{Actor: agent.Actor, OnBehalfOf: uma.Actor}

	t.Run("visibility", func(t *testing.T) {
		onlyUser := createRequest(t, k, uma, &itemservice.Narrow{Readers: readers(uma)})
		onlyAgent := createRequest(t, k, agent, &itemservice.Narrow{Readers: readers(agent)})
		shared := createRequest(t, k, uma, &itemservice.Narrow{Readers: readers(uma, agent)})

		assertHidden(t, k, both, onlyUser)  // the agent cannot see it
		assertHidden(t, k, both, onlyAgent) // the user cannot see it
		assertVisible(t, k, both, shared)

		// The same rule guards writes.
		_, err := k.Update(testCtx(t), itemservice.UpdateRequest{
			IdempotencyKey: newKey(), Principal: both, ItemID: onlyUser,
			Fields: map[string]any{"f_title": "changed"},
		})
		if !errors.Is(err, itemservice.ErrNotFound) {
			t.Errorf("Update of an Item the agent cannot see: error = %v, want ErrNotFound", err)
		}
	})

	t.Run("allowed", func(t *testing.T) {
		id := createRequest(t, k, uma, nil) // in the root scope: both can see it

		assertForbidden(t, move(t, k, both, id, "in_progress", newKey())) // neither has the role

		grantRole(t, uma, "support")
		assertForbidden(t, move(t, k, both, id, "in_progress", newKey())) // only the user

		revokeRole(t, uma, "support")
		grantRole(t, agent, "support")
		assertForbidden(t, move(t, k, both, id, "in_progress", newKey())) // only the agent

		grantRole(t, uma, "support")
		if err := move(t, k, both, id, "in_progress", newKey()); err != nil {
			t.Fatalf("Transition when both hold the role: %v", err)
		}
	})
}

// principal_roles is keyed by (namespace_id, principal, role).
func TestSchema_PrincipalRolesTable(t *testing.T) {
	sam := person("sam")
	grantRole(t, sam, "support")
	_, err := pool.Exec(testCtx(t),
		`insert into principal_roles (namespace_id, principal, role) values ('default', $1, 'support')`, sam.Actor)
	if err == nil {
		t.Error("a duplicate (namespace_id, principal, role) row was accepted")
	}
}
