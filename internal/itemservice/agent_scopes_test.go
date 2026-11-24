// Tests for Step 0, part 2: agent sessions in capability mode
// (docs/design.md, "Who is calling"). A session names the scopes it may read
// and write (container subtrees, or the whole namespace) and the powers it
// holds. Outside its scopes an Item does not exist for the session
// (ErrNotFound, on reads and writes alike); without a write power every write
// is ErrForbidden. A session fits inside what the agent's user granted it,
// and belongs to one namespace. The Step A2 write rule (NEEDS_APPROVAL) still
// applies on top.
package itemservice_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
)

// allPowers is every power of the vocabulary, transitions included.
var allPowers = []string{
	item.PowerRead, item.PowerCreate, item.PowerEditFields, item.PowerTransitionAll,
	item.PowerMove, item.PowerComment, item.PowerOutbound,
}

// wholeNamespace asks for a session over the whole namespace with every
// power, granted exactly that.
func wholeNamespace(p item.Principal) itemservice.StartAgentSessionRequest {
	all := []uuid.UUID{uuid.Nil}
	return itemservice.StartAgentSessionRequest{
		Principal: p, Scopes: all, Powers: allPowers,
		Grant: &itemservice.Grant{Scopes: all, Powers: allPowers},
	}
}

// scoped starts a session for a fresh agent acting for user, over scopes with
// powers, granted the whole namespace and every power.
func scoped(t *testing.T, k *itemservice.Service, user item.Principal, scopes []uuid.UUID, powers ...string) item.Principal {
	t.Helper()
	p := item.Principal{Actor: "agent:" + uuid.NewString()[:8], OnBehalfOf: user.Actor, Namespace: user.Namespace}
	req := wholeNamespace(p)
	req.Scopes, req.Powers = scopes, powers
	session, err := k.StartAgentSession(testCtx(t), req)
	if err != nil {
		t.Fatalf("StartAgentSession(scopes %v, powers %v): %v", scopes, powers, err)
	}
	p.AgentSession = session
	return p
}

// capWorld: a user with two top-level containers, A and B, each holding a
// ticket; A also holds a sub-container with a ticket in it, and there is a
// loose ticket at the top level. Nothing is narrowed: the user sees it all.
type capWorld struct {
	k                      *itemservice.Service
	user                   item.Principal
	a, b, sub              uuid.UUID // containers
	inA, inB, inSub, loose uuid.UUID // tickets
}

func newCapWorld(t *testing.T) capWorld {
	t.Helper()
	k := itemservice.New(pool)
	// In the default namespace, named: List and ListEvents refuse none.
	user := in(person("scoped-user"), "default")
	w := capWorld{k: k, user: user}
	w.a = createAs(t, k, user, "container A", nil, nil).ItemID
	w.b = createAs(t, k, user, "container B", nil, nil).ItemID
	w.sub = createAs(t, k, user, "sub-container of A", nil, &w.a).ItemID
	w.inA = createAs(t, k, user, "in A", nil, &w.a).ItemID
	w.inB = createAs(t, k, user, "in B", nil, &w.b).ItemID
	w.inSub = createAs(t, k, user, "in A's sub-container", nil, &w.sub).ItemID
	w.loose = createAs(t, k, user, "at the top level", nil, nil).ItemID
	return w
}

func agentUpdate(t *testing.T, k *itemservice.Service, p item.Principal, id uuid.UUID) error {
	t.Helper()
	_, err := k.Update(testCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: p, ItemID: id, Fields: map[string]any{"f_title": "agent was here"},
	})
	return err
}

// --- starting a session ---

// The session stores its namespace, scopes and powers exactly as asked.
func TestAgentScopes_SessionIsStored(t *testing.T) {
	w := newCapWorld(t)
	ns := newNamespace(t, w.k)
	user := in(person("ns-user"), ns)
	container := createIn(t, w.k, user, nil, nil)
	agent := scoped(t, w.k, user, []uuid.UUID{container}, item.PowerRead, item.PowerComment, "transition:approved")

	s, err := w.k.AgentSession(testCtx(t), item.Principal{Actor: agent.Actor, AgentSession: agent.AgentSession, Namespace: ns})
	if err != nil {
		t.Fatalf("AgentSession: %v", err)
	}
	if s.ID != agent.AgentSession || s.Agent != agent.Actor || s.OnBehalfOf != user.Actor || s.Namespace != ns {
		t.Errorf("session = %+v, want %s for %s acting for %s in %s", s, agent.AgentSession, agent.Actor, user.Actor, ns)
	}
	if !slices.Equal(s.Scopes, []uuid.UUID{container}) {
		t.Errorf("scopes = %v, want [%s]", s.Scopes, container)
	}
	// Every power is stored, even those v1 does not enforce yet.
	want := []string{item.PowerRead, item.PowerComment, "transition:approved"}
	if got := slices.Sorted(slices.Values(s.Powers)); !slices.Equal(got, slices.Sorted(slices.Values(want))) {
		t.Errorf("powers = %v, want %v", s.Powers, want)
	}
	n := count(t, `select count(*) from agent_sessions where session = $1 and namespace_id = $2`, agent.AgentSession, ns)
	if n != 1 {
		t.Errorf("agent_sessions rows for %s in %s = %d, want 1", agent.AgentSession, ns, n)
	}
}

// Scopes and powers are required; an unknown power, a malformed transition
// power or a scope that is not an Item is malformed.
func TestAgentScopes_StartRefusesMalformedRequests(t *testing.T) {
	w := newCapWorld(t)
	base := func() itemservice.StartAgentSessionRequest {
		r := wholeNamespace(item.Principal{Actor: "agent:" + uuid.NewString()[:8], OnBehalfOf: w.user.Actor})
		r.Scopes, r.Powers = []uuid.UUID{w.a}, []string{item.PowerRead}
		return r
	}
	cases := []struct {
		name string
		edit func(*itemservice.StartAgentSessionRequest)
		path string
	}{
		{"no scopes", func(r *itemservice.StartAgentSessionRequest) { r.Scopes = nil }, "scopes"},
		{"no powers", func(r *itemservice.StartAgentSessionRequest) { r.Powers = nil }, "powers"},
		{"unknown power", func(r *itemservice.StartAgentSessionRequest) { r.Powers = []string{"read", "launch_missiles"} }, "powers"},
		{"transition without an id", func(r *itemservice.StartAgentSessionRequest) { r.Powers = []string{"transition:"} }, "powers"},
		{"power in capitals", func(r *itemservice.StartAgentSessionRequest) { r.Powers = []string{"READ"} }, "powers"},
		// transition:<state> names the target state; its format is checked,
		// not the state (a session spans Types).
		{"transition to a malformed state", func(r *itemservice.StartAgentSessionRequest) { r.Powers = []string{"transition:In Progress"} }, "powers"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := base()
			c.edit(&r)
			_, err := w.k.StartAgentSession(testCtx(t), r)
			var ie *itemservice.InvalidError
			if !errors.As(err, &ie) || ie.Path != c.path {
				t.Errorf("error = %v, want an InvalidError on %s", err, c.path)
			}
		})
	}
	// Every known power is accepted.
	r := base()
	r.Powers = append(slices.Clone(allPowers), "transition:resolved")
	if _, err := w.k.StartAgentSession(testCtx(t), r); err != nil {
		t.Errorf("every known power: %v", err)
	}
}

// A scope the user cannot see, one that does not exist, and one in another
// namespace are all ErrNotFound, alike.
func TestAgentScopes_StartRefusesScopesTheUserCannotSee(t *testing.T) {
	w := newCapWorld(t)
	stranger := person("stranger")
	hidden := createAs(t, w.k, stranger, "hidden", &itemservice.Narrow{Readers: readers(stranger)}, nil).ItemID
	ns := newNamespace(t, w.k)
	elsewhere := createIn(t, w.k, in(person("other-ns"), ns), nil, nil)

	for name, scope := range map[string]uuid.UUID{
		"hidden": hidden, "missing": uuid.New(), "another namespace": elsewhere,
	} {
		t.Run(name, func(t *testing.T) {
			r := wholeNamespace(item.Principal{Actor: "agent:" + uuid.NewString()[:8], OnBehalfOf: w.user.Actor})
			r.Scopes = []uuid.UUID{w.a, scope}
			_, err := w.k.StartAgentSession(testCtx(t), r)
			assertNotFound(t, "StartAgentSession", err)
		})
	}
}

// The session must fit inside the grant: its scopes at or below the grant's,
// its powers among the grant's. No grant grants nothing.
func TestAgentScopes_SessionFitsInsideTheGrant(t *testing.T) {
	w := newCapWorld(t)
	grantA := &itemservice.Grant{Scopes: []uuid.UUID{w.a}, Powers: []string{item.PowerRead, item.PowerEditFields}}
	cases := []struct {
		name   string
		scopes []uuid.UUID
		powers []string
		grant  *itemservice.Grant
		ok     bool
	}{
		{"exactly the grant", []uuid.UUID{w.a}, []string{item.PowerRead, item.PowerEditFields}, grantA, true},
		{"below the grant's scope", []uuid.UUID{w.sub}, []string{item.PowerRead}, grantA, true},
		{"read is implied by any grant", []uuid.UUID{w.a}, []string{item.PowerRead},
			&itemservice.Grant{Scopes: []uuid.UUID{w.a}, Powers: []string{item.PowerEditFields}}, true},
		{"a sibling scope", []uuid.UUID{w.b}, []string{item.PowerRead}, grantA, false},
		{"the whole namespace", []uuid.UUID{uuid.Nil}, []string{item.PowerRead}, grantA, false},
		{"a power not granted", []uuid.UUID{w.a}, []string{item.PowerMove}, grantA, false},
		{"a specific transition, only * granted", []uuid.UUID{w.a}, []string{"transition:resolved"},
			&itemservice.Grant{Scopes: []uuid.UUID{w.a}, Powers: []string{item.PowerTransitionAll}}, true},
		{"all transitions, one granted", []uuid.UUID{w.a}, []string{item.PowerTransitionAll},
			&itemservice.Grant{Scopes: []uuid.UUID{w.a}, Powers: []string{"transition:resolved"}}, false},
		{"no grant", []uuid.UUID{w.a}, []string{item.PowerRead}, nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := w.k.StartAgentSession(testCtx(t), itemservice.StartAgentSessionRequest{
				Principal: item.Principal{Actor: "agent:" + uuid.NewString()[:8], OnBehalfOf: w.user.Actor},
				Scopes:    c.scopes, Powers: c.powers, Grant: c.grant,
			})
			if c.ok && err != nil {
				t.Errorf("error = %v, want a session", err)
			}
			if !c.ok && !errors.Is(err, itemservice.ErrForbidden) {
				t.Errorf("error = %v, want ErrForbidden", err)
			}
		})
	}
}

// --- using a session ---

// AgentSession answers ErrSessionInvalid, an ErrInvalid, for an unknown
// session, another agent's, or one from another namespace; so do reads and
// writes made with them.
func TestAgentScopes_SessionBelongsToOneAgentAndNamespace(t *testing.T) {
	w := newCapWorld(t)
	agent := scoped(t, w.k, w.user, []uuid.UUID{w.a}, allPowers...)
	other := scoped(t, w.k, w.user, []uuid.UUID{w.a}, allPowers...)
	ns := newNamespace(t, w.k)

	cases := map[string]item.Principal{
		"unknown session":         {Actor: agent.Actor, OnBehalfOf: w.user.Actor, AgentSession: "ses_made-up"},
		"another agent's session": {Actor: agent.Actor, OnBehalfOf: w.user.Actor, AgentSession: other.AgentSession},
		"another namespace":       {Actor: agent.Actor, OnBehalfOf: w.user.Actor, AgentSession: agent.AgentSession, Namespace: ns},
	}
	for name, p := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := w.k.AgentSession(testCtx(t), item.Principal{Actor: p.Actor, AgentSession: p.AgentSession, Namespace: p.Namespace})
			if !errors.Is(err, itemservice.ErrSessionInvalid) || !errors.Is(err, itemservice.ErrInvalid) {
				t.Errorf("AgentSession: error = %v, want ErrSessionInvalid (an ErrInvalid)", err)
			}
			if _, err := w.k.Get(testCtx(t), p, w.inA); !errors.Is(err, itemservice.ErrSessionInvalid) {
				t.Errorf("Get: error = %v, want ErrSessionInvalid", err)
			}
			if err := agentUpdate(t, w.k, p, w.inA); !errors.Is(err, itemservice.ErrSessionInvalid) {
				t.Errorf("Update: error = %v, want ErrSessionInvalid", err)
			}
		})
	}
	assertNothingWritten(t, w.inA, 1)
}

// Reads: the scope container and everything below it exist for the session;
// nothing else does, not even what the user can see.
func TestAgentScopes_ReadsOutsideTheScopesAreNotFound(t *testing.T) {
	w := newCapWorld(t)
	agent := scoped(t, w.k, w.user, []uuid.UUID{w.a}, item.PowerRead)

	for _, id := range []uuid.UUID{w.a, w.inA, w.sub, w.inSub} {
		assertVisible(t, w.k, agent, id)
	}
	for _, id := range []uuid.UUID{w.b, w.inB, w.loose} {
		assertHidden(t, w.k, agent, id)
		_, err := w.k.ListEvents(testCtx(t), itemservice.ListEventsRequest{Principal: agent, ItemID: id})
		assertNotFound(t, "ListEvents outside the scopes", err)
	}

	// Search and List answer only from inside the scopes.
	all := []uuid.UUID{w.a, w.b, w.sub, w.inA, w.inB, w.inSub, w.loose}
	res, err := w.k.Search(testCtx(t), itemservice.SearchRequest{
		Principal: agent, Filter: itemservice.Filter{TypeIDs: []string{"ticket"}}, Limit: 500,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	got := idsOf(res.Items)
	for _, id := range all {
		inside := slices.Contains([]uuid.UUID{w.a, w.sub, w.inA, w.inSub}, id)
		if slices.Contains(got, id) != inside {
			t.Errorf("Search: found %s = %v, want %v", id, !inside, inside)
		}
	}
	list, err := w.k.List(testCtx(t), itemservice.ListRequest{Principal: agent, ContainerID: &w.a})
	if err != nil {
		t.Fatalf("List A: %v", err)
	}
	if got := idsOf(list.Items); !sameIDs(got, []uuid.UUID{w.sub, w.inA}) {
		t.Errorf("List A = %v, want its two children", got)
	}
	_, err = w.k.List(testCtx(t), itemservice.ListRequest{Principal: agent, ContainerID: &w.b})
	assertNotFound(t, "List B", err)
	_, err = w.k.List(testCtx(t), itemservice.ListRequest{Principal: agent})
	assertNotFound(t, "List of the top level, without the namespace in scope", err)
}

// Writes outside the scopes are ErrNotFound too, before any other check:
// updates, transitions, creates (into a container outside, or at the top
// level) and moves (of an Item outside, or to a destination outside).
func TestAgentScopes_WritesOutsideTheScopesAreNotFound(t *testing.T) {
	w := newCapWorld(t)
	agent := scoped(t, w.k, w.user, []uuid.UUID{w.a}, allPowers...)
	ctx := testCtx(t)

	assertNotFound(t, "Update outside", agentUpdate(t, w.k, agent, w.inB))
	_, err := w.k.Transition(ctx, itemservice.TransitionRequest{
		IdempotencyKey: newKey(), Principal: agent, ItemID: w.inB, ToState: "canceled",
	})
	assertNotFound(t, "Transition outside", err)
	_, err = w.k.Create(ctx, itemservice.CreateRequest{
		IdempotencyKey: newKey(), Principal: agent, TypeID: "ticket", TypeVersion: 1, ContainerID: &w.b,
	})
	assertNotFound(t, "Create into a container outside", err)
	_, err = w.k.Create(ctx, itemservice.CreateRequest{
		IdempotencyKey: newKey(), Principal: agent, TypeID: "ticket", TypeVersion: 1,
	})
	assertNotFound(t, "Create at the top level", err)
	_, err = w.k.Update(ctx, itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: agent, ItemID: w.inA, Move: &itemservice.Move{To: &w.b},
	})
	assertNotFound(t, "Move to a destination outside", err)
	_, err = w.k.Update(ctx, itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: agent, ItemID: w.inA, Move: &itemservice.Move{},
	})
	assertNotFound(t, "Move to the top level", err)
	_, err = w.k.Update(ctx, itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: agent, ItemID: w.inB, Move: &itemservice.Move{To: &w.a},
	})
	assertNotFound(t, "Move of an Item outside into the scope", err)

	for _, id := range []uuid.UUID{w.inA, w.inB, w.b} {
		assertNothingWritten(t, id, 1)
	}
	if n := count(t, `select count(*) from items where container_id = $1`, w.b); n != 1 {
		t.Errorf("B has %d children, want its one ticket", n)
	}

	// Inside, the same writes work (the update on an Item whose fields an
	// agent may write: fields are closed to agents by default).
	openInSub := createOpenAs(t, w.k, w.user, "open in A's sub-container", nil, &w.sub).ItemID
	if err := agentUpdate(t, w.k, agent, openInSub); err != nil {
		t.Errorf("Update inside: %v", err)
	}
	if _, err := w.k.Create(ctx, itemservice.CreateRequest{
		IdempotencyKey: newKey(), Principal: agent, TypeID: "ticket", TypeVersion: 1, ContainerID: &w.sub,
	}); err != nil {
		t.Errorf("Create inside: %v", err)
	}
	if _, err := w.k.Update(ctx, itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: agent, ItemID: w.inSub, Move: &itemservice.Move{To: &w.a},
	}); err != nil {
		t.Errorf("Move inside: %v", err)
	}
}

// A session over the whole namespace reaches the top level.
func TestAgentScopes_WholeNamespace(t *testing.T) {
	w := newCapWorld(t)
	agent := scoped(t, w.k, w.user, []uuid.UUID{uuid.Nil}, item.PowerRead, item.PowerCreate)
	assertVisible(t, w.k, agent, w.loose)
	assertVisible(t, w.k, agent, w.inB)
	if _, err := w.k.Create(testCtx(t), itemservice.CreateRequest{
		IdempotencyKey: newKey(), Principal: agent, TypeID: "ticket", TypeVersion: 1,
	}); err != nil {
		t.Errorf("Create at the top level: %v", err)
	}
}

// Membership is checked at each request: once the user moves an Item out of
// the scope, it is gone for the session; moved in, it appears.
func TestAgentScopes_MembershipIsLive(t *testing.T) {
	w := newCapWorld(t)
	agent := scoped(t, w.k, w.user, []uuid.UUID{w.a}, item.PowerRead)
	assertVisible(t, w.k, agent, w.inSub)
	assertHidden(t, w.k, agent, w.inB)

	move := func(id uuid.UUID, to *uuid.UUID) {
		t.Helper()
		if _, err := w.k.Update(testCtx(t), itemservice.UpdateRequest{
			IdempotencyKey: newKey(), Principal: w.user, ItemID: id, Move: &itemservice.Move{To: to},
		}); err != nil {
			t.Fatalf("move: %v", err)
		}
	}
	move(w.sub, &w.b) // A's sub-container, with its ticket, goes to B
	move(w.inB, &w.a)
	assertHidden(t, w.k, agent, w.sub)
	assertHidden(t, w.k, agent, w.inSub)
	assertVisible(t, w.k, agent, w.inB)
}

// A read-only session reads, and every write is ErrForbidden: nothing written.
func TestAgentScopes_ReadOnlySessionCannotWrite(t *testing.T) {
	w := newCapWorld(t)
	agent := scoped(t, w.k, w.user, []uuid.UUID{w.a}, item.PowerRead)
	ctx := testCtx(t)
	assertVisible(t, w.k, agent, w.inA)

	forbidden := func(what string, err error) {
		t.Helper()
		if !errors.Is(err, itemservice.ErrForbidden) {
			t.Errorf("%s: error = %v, want ErrForbidden", what, err)
		}
	}
	forbidden("Update", agentUpdate(t, w.k, agent, w.inA))
	_, err := w.k.Transition(ctx, itemservice.TransitionRequest{
		IdempotencyKey: newKey(), Principal: agent, ItemID: w.inA, ToState: "canceled",
	})
	forbidden("Transition", err)
	_, err = w.k.Create(ctx, itemservice.CreateRequest{
		IdempotencyKey: newKey(), Principal: agent, TypeID: "ticket", TypeVersion: 1, ContainerID: &w.a,
	})
	forbidden("Create", err)
	_, err = w.k.Update(ctx, itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: agent, ItemID: w.inSub, Move: &itemservice.Move{To: &w.a},
	})
	forbidden("Move", err)
	_, err = w.k.Update(ctx, itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: agent, ItemID: w.inA,
		Policy: &itemservice.PolicyPatch{Readers: []string{w.user.Actor, agent.Actor}},
	})
	forbidden("Narrow", err)
	assertNothingWritten(t, w.inA, 1)
	assertNothingWritten(t, w.inSub, 1)

	// Outside the scopes the answer is still ErrNotFound, never ErrForbidden:
	// the session cannot learn the Item exists.
	assertNotFound(t, "Update outside, read-only", agentUpdate(t, w.k, agent, w.inB))
}

// Any write power allows writes in v1 (read versus write only), except a
// create, which needs the create power.
func TestAgentScopes_AnyWritePowerWrites(t *testing.T) {
	w := newCapWorld(t)
	agent := scoped(t, w.k, w.user, []uuid.UUID{w.a}, item.PowerEditFields)
	if _, err := w.k.Transition(testCtx(t), itemservice.TransitionRequest{
		IdempotencyKey: newKey(), Principal: agent, ItemID: w.inA, ToState: "canceled",
	}); err != nil {
		t.Errorf("Transition with edit_fields only: %v", err)
	}
}

// The Step A2 write rule still applies inside the scopes: a narrowing read
// then a write to a wider scope needs approval, and the refusal names the
// target's Item (nil for the root).
func TestAgentScopes_NeedsApprovalNamesTheScopeItem(t *testing.T) {
	w := newCapWorld(t)
	private := createAs(t, w.k, w.user, "private", &itemservice.Narrow{Readers: readers(w.user)}, &w.a).ItemID
	agent := scoped(t, w.k, w.user, []uuid.UUID{uuid.Nil}, allPowers...)
	// The user lets the agent read the private Item, and it does.
	setPolicy(t, w.k, w.user, private, itemservice.PolicyPatch{Readers: []string{w.user.Actor, agent.Actor}})
	assertVisible(t, w.k, agent, private)

	_, err := w.k.Create(testCtx(t), itemservice.CreateRequest{
		IdempotencyKey: newKey(), Principal: agent, TypeID: "ticket", TypeVersion: 1, ContainerID: &w.b,
	})
	var na *itemservice.NeedsApprovalError
	if !errors.As(err, &na) || na.ScopeItem == nil || *na.ScopeItem != w.b {
		t.Errorf("create into B: error = %v, want NEEDS_APPROVAL naming B (%s)", err, w.b)
	}
	_, err = w.k.Create(testCtx(t), itemservice.CreateRequest{
		IdempotencyKey: newKey(), Principal: agent, TypeID: "ticket", TypeVersion: 1,
	})
	if !errors.As(err, &na) || na.ScopeItem != nil {
		t.Errorf("create at the top: error = %v, want NEEDS_APPROVAL naming the root (nil)", err)
	}

	// The user approves B by its Item, as the API will; the retry works.
	if err := w.k.ApproveWiden(testCtx(t), itemservice.ApproveWidenRequest{IdempotencyKey: newKey(),
		Principal: w.user, Session: agent.AgentSession, ScopeItem: &w.b, ExpectedAudience: &openAudience,
	}); err != nil {
		t.Fatalf("ApproveWiden by the scope's Item: %v", err)
	}
	if _, err := w.k.Create(testCtx(t), itemservice.CreateRequest{
		IdempotencyKey: newKey(), Principal: agent, TypeID: "ticket", TypeVersion: 1, ContainerID: &w.b,
	}); err != nil {
		t.Errorf("create into B after approval: %v", err)
	}
}

// ApproveWiden is scoped to the session's namespace: an approver from
// another namespace, or a session that does not exist, is ErrNotFound alike.
func TestAgentScopes_ApprovalStaysInTheSessionsNamespace(t *testing.T) {
	w := newCapWorld(t)
	agent := scoped(t, w.k, w.user, []uuid.UUID{uuid.Nil}, allPowers...)
	ns := newNamespace(t, w.k)
	sameNameElsewhere := in(w.user, ns)

	err := w.k.ApproveWiden(testCtx(t), itemservice.ApproveWidenRequest{IdempotencyKey: newKey(),
		Principal: sameNameElsewhere, Session: agent.AgentSession, ScopeItem: &w.b, ExpectedAudience: &openAudience,
	})
	assertNotFound(t, "approver in another namespace", err)
	err = w.k.ApproveWiden(testCtx(t), itemservice.ApproveWidenRequest{IdempotencyKey: newKey(),
		Principal: w.user, Session: "ses_" + uuid.NewString(), ScopeItem: &w.b, ExpectedAudience: &openAudience,
	})
	assertNotFound(t, "unknown session", err)
	if n := count(t, `select count(*) from agent_session_approvals where session = $1`, agent.AgentSession); n != 0 {
		t.Errorf("approvals recorded = %d, want 0", n)
	}
}

// ApproveWiden is a write like any other: it takes a required idempotency
// key; the same key and request again records nothing more; the same key
// with another request is ErrIdempotencyKeyReuse.
func TestAgentScopes_ApprovalTakesAnIdempotencyKey(t *testing.T) {
	w := newCapWorld(t)
	agent := scoped(t, w.k, w.user, []uuid.UUID{uuid.Nil}, allPowers...)
	approve := func(key string, scope *uuid.UUID) error {
		return w.k.ApproveWiden(testCtx(t), itemservice.ApproveWidenRequest{
			IdempotencyKey: key, Principal: w.user, Session: agent.AgentSession, ScopeItem: scope,
			ExpectedAudience: &openAudience, // A and B are open
		})
	}
	if err := approve("", &w.b); !errors.Is(err, itemservice.ErrInvalid) {
		t.Errorf("no key: error = %v, want ErrInvalid", err)
	}
	key := newKey()
	if err := approve(key, &w.b); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if err := approve(key, &w.b); err != nil {
		t.Errorf("replay: %v", err)
	}
	if err := approve(key, &w.a); !errors.Is(err, itemservice.ErrIdempotencyKeyReuse) {
		t.Errorf("same key, other scope: error = %v, want ErrIdempotencyKeyReuse", err)
	}
	if n := count(t, `select count(*) from agent_session_approvals where session = $1`, agent.AgentSession); n != 1 {
		t.Errorf("approvals recorded = %d, want 1", n)
	}
	if n := count(t, `select count(*) from idempotency where actor = $1 and key = $2`, w.user.Actor, key); n != 1 {
		t.Errorf("idempotency rows for the key = %d, want 1", n)
	}
}

// Approving an Item approves its audience: everyone who reads the scope its
// content lands in. NEEDS_APPROVAL says whether that scope is open (the
// whole namespace reads it) or narrowed.
func TestAgentScopes_NeedsApprovalNamesTheAudience(t *testing.T) {
	w := newCapWorld(t)
	agent := scoped(t, w.k, w.user, []uuid.UUID{uuid.Nil}, allPowers...)
	both := &itemservice.Narrow{Readers: []string{w.user.Actor, agent.Actor}}
	private := createAs(t, w.k, w.user, "private", both, &w.a).ItemID
	restricted := createAs(t, w.k, w.user, "restricted room", both, nil).ItemID
	assertVisible(t, w.k, agent, private) // a narrowing read

	create := func(container *uuid.UUID) *itemservice.NeedsApprovalError {
		t.Helper()
		_, err := w.k.Create(testCtx(t), itemservice.CreateRequest{
			IdempotencyKey: newKey(), Principal: agent, TypeID: "ticket", TypeVersion: 1, ContainerID: container,
		})
		var na *itemservice.NeedsApprovalError
		if !errors.As(err, &na) {
			t.Fatalf("create: error = %v, want NEEDS_APPROVAL", err)
		}
		return na
	}
	if na := create(&w.b); !na.Open {
		t.Errorf("into an open container: %+v, want Open (the whole namespace)", na)
	}
	if na := create(nil); !na.Open {
		t.Errorf("at the top level: %+v, want Open", na)
	}
	if na := create(&restricted); na.Open {
		t.Errorf("into a narrowed container: %+v, want not Open", na)
	}
}

// ApprovalAudience checks an approval like ApproveWiden and says who would
// see the content, recording nothing.
func TestAgentScopes_ApprovalAudience(t *testing.T) {
	w := newCapWorld(t)
	agent := scoped(t, w.k, w.user, []uuid.UUID{uuid.Nil}, allPowers...)
	restricted := createAs(t, w.k, w.user, "restricted room",
		&itemservice.Narrow{Readers: []string{w.user.Actor, agent.Actor}}, nil).ItemID
	audience := func(scope *uuid.UUID, session string) (itemservice.Audience, error) {
		return w.k.ApprovalAudience(testCtx(t), itemservice.ApproveWidenRequest{
			Principal: w.user, Session: session, ScopeItem: scope,
		})
	}
	for name, c := range map[string]struct {
		scope *uuid.UUID
		open  bool
	}{"open container": {&w.b, true}, "the namespace": {nil, true}, "narrowed container": {&restricted, false}} {
		a, err := audience(c.scope, agent.AgentSession)
		if err != nil || a.Open != c.open {
			t.Errorf("%s: %+v, %v; want Open %v", name, a, err, c.open)
		}
	}
	_, err := audience(&w.b, "ses_"+uuid.NewString())
	assertNotFound(t, "unknown session", err)
	if n := count(t, `select count(*) from agent_session_approvals where session = $1`, agent.AgentSession); n != 0 {
		t.Errorf("approvals recorded = %d, want 0", n)
	}
}
