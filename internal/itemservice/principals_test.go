// Tests for Step A: principals are typed IDs given by the caller (user:,
// agent:, group:, system:). An untyped ID is ErrInvalid wherever a principal
// appears; group: is reserved until groups exist.
package itemservice_test

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
)

func assertInvalid(t *testing.T, what string, err error) {
	t.Helper()
	if !errors.Is(err, itemservice.ErrInvalid) {
		t.Errorf("%s: error = %v, want ErrInvalid", what, err)
	}
}

// The actor and on_behalf_of must be typed. on_behalf_of names the human at
// the root of a chain, so it must be a user.
func TestPrincipals_ActorAndOnBehalfOfMustBeTyped(t *testing.T) {
	k := itemservice.New(pool)
	id := createTicket(t, k, map[string]any{"f_title": "x"})
	cases := []struct {
		name string
		p    item.Principal
	}{
		{"untyped actor", item.Principal{Actor: "alice"}},
		{"actor with an unknown type", item.Principal{Actor: "robot:7"}},
		{"actor with an empty ID", item.Principal{Actor: "user:"}},
		{"a group cannot act", item.Principal{Actor: "group:support"}},
		{"untyped on_behalf_of", item.Principal{Actor: "agent:7", OnBehalfOf: "alice", AgentSession: "session-7"}},
		{"on_behalf_of is not a user", item.Principal{Actor: "agent:7", OnBehalfOf: "agent:8", AgentSession: "session-7"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := k.Create(testCtx(t), itemservice.CreateRequest{
				IdempotencyKey: newKey(), Principal: c.p, TypeID: "ticket", TypeVersion: 1,
			})
			assertInvalid(t, "Create", err)
			_, err = k.Update(testCtx(t), itemservice.UpdateRequest{
				IdempotencyKey: newKey(), Principal: c.p, ItemID: id, Fields: map[string]any{"f_title": "y"},
			})
			assertInvalid(t, "Update", err)
			_, err = k.Transition(testCtx(t), itemservice.TransitionRequest{
				IdempotencyKey: newKey(), Principal: c.p, ItemID: id, ToState: "canceled",
			})
			assertInvalid(t, "Transition", err)
			_, err = k.Get(testCtx(t), c.p, id)
			assertInvalid(t, "Get", err)
			_, err = k.Search(testCtx(t), itemservice.SearchRequest{Principal: c.p, Query: "x"})
			assertInvalid(t, "Search", err)
		})
	}
	assertNothingWritten(t, id, 1)
}

// The kinds that may act today all work: a user, an agent for a user, and
// the kernel's own system:timer.
func TestPrincipals_TypedActorsAreAccepted(t *testing.T) {
	k := itemservice.New(pool)
	for _, p := range []item.Principal{
		{Actor: "user:" + uuid.NewString()},
		withSession(t, k, item.Principal{Actor: "agent:" + uuid.NewString(), OnBehalfOf: "user:" + uuid.NewString()}),
		{Actor: "system:timer"},
	} {
		if _, err := k.Create(testCtx(t), itemservice.CreateRequest{
			IdempotencyKey: newKey(), Principal: p, TypeID: "ticket", TypeVersion: 1,
		}); err != nil {
			t.Errorf("Create as %+v: %v", p, err)
		}
	}
}

// Readers must be typed users or agents. group: is reserved and refused until
// groups exist; system: principals read nothing through scopes.
func TestPrincipals_ReadersMustBeTypedUsersOrAgents(t *testing.T) {
	k := itemservice.New(pool)
	alice := person("alice")
	id := createAs(t, k, alice, "x", nil, nil).ItemID
	for _, bad := range []string{"bob", "group:support", "system:timer", "user:", "robot:1"} {
		t.Run(bad, func(t *testing.T) {
			_, err := k.Create(testCtx(t), itemservice.CreateRequest{
				IdempotencyKey: newKey(), Principal: alice, TypeID: "ticket", TypeVersion: 1,
				Narrow: &itemservice.Narrow{Readers: []string{alice.Actor, bad}},
			})
			assertInvalid(t, "Create with reader "+bad, err)
			_, err = k.Update(testCtx(t), itemservice.UpdateRequest{
				IdempotencyKey: newKey(), Principal: alice, ItemID: id,
				Policy: &itemservice.PolicyPatch{Readers: []string{alice.Actor, bad}},
			})
			assertInvalid(t, "Update with reader "+bad, err)
		})
	}
	assertNothingWritten(t, id, 1)
	setPolicy(t, k, alice, id, itemservice.PolicyPatch{Readers: []string{alice.Actor, "agent:" + uuid.NewString()}})
}

// scope_access stores the typed IDs as given.
func TestPrincipals_ScopeAccessHoldsTypedIDs(t *testing.T) {
	k := itemservice.New(pool)
	alice := person("alice")
	agent := "agent:" + uuid.NewString()
	res := createAs(t, k, alice, "x", &itemservice.Narrow{Readers: []string{alice.Actor, agent}}, nil)
	n := count(t, `select count(*) from scope_access a join scopes s on a.scope_id = s.policy_scope_id
	               join items i on i.scope_id = s.id
	               where i.id = $1 and a.principal = any($2)`, res.ItemID, []string{alice.Actor, agent})
	if n != 2 {
		t.Errorf("typed principals in scope_access = %d, want 2", n)
	}
}
