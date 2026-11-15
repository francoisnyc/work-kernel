// Tests for Step A2: the agent write rule. When an agent acts, every
// narrowing scope it reads in a session is recorded. A write that would put
// content into a scope not at or below every recorded scope is refused with
// ErrNeedsApproval, naming the target scope, until the session's user
// approves that one scope with ApproveWiden.
//
// Writes: field updates, transitions with fields, creates (target = the
// container's scope), moves (target = the destination's scope) and policy
// changes that widen readers, Inherit included.
package itemservice_test

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
)

// agentFor returns a new agent acting for the user, in a session the item
// service minted for it.
func agentFor(t *testing.T, k *itemservice.Service, user item.Principal) item.Principal {
	t.Helper()
	return withSession(t, k, item.Principal{Actor: "agent:" + uuid.NewString()[:8], OnBehalfOf: user.Actor})
}

// withSession starts a session for the agent (and the user it acts for) and
// returns the principal carrying it. Sessions are minted by the server, never
// chosen by the caller.
//
// Step 0, part 2: a session now names its scopes and powers. These Step A2
// tests are about the write rule, not capability mode, so their sessions
// cover the whole namespace with every write power (wholeNamespace).
func withSession(t *testing.T, k *itemservice.Service, p item.Principal) item.Principal {
	t.Helper()
	session, err := k.StartAgentSession(testCtx(t), wholeNamespace(p))
	if err != nil {
		t.Fatalf("StartAgentSession(%+v): %v", p, err)
	}
	p.AgentSession = session
	return p
}

func assertNeedsApproval(t *testing.T, err error, target uuid.UUID) {
	t.Helper()
	if !errors.Is(err, itemservice.ErrNeedsApproval) {
		t.Fatalf("error = %v, want ErrNeedsApproval", err)
	}
	for _, other := range []error{itemservice.ErrForbidden, itemservice.ErrInvalid, itemservice.ErrNotFound, itemservice.ErrRejected} {
		if errors.Is(err, other) {
			t.Errorf("error also matches %v; ErrNeedsApproval must be distinct", other)
		}
	}
	var na *itemservice.NeedsApprovalError
	if !errors.As(err, &na) {
		t.Fatalf("error %v is not a *NeedsApprovalError", err)
	}
	if na.Scope != target {
		t.Errorf("refusal names scope %s, want the target scope %s", na.Scope, target)
	}
}

// recordedReads lists the scopes recorded for an agent session.
func recordedReads(t *testing.T, session string) []uuid.UUID {
	t.Helper()
	rows, err := pool.Query(testCtx(t), `select scope_id from agent_session_reads where session = $1`, session)
	if err != nil {
		t.Fatalf("read agent_session_reads: %v", err)
	}
	defer rows.Close()
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan: %v", err)
		}
		ids = append(ids, id)
	}
	return ids
}

// world: a user, their agent, an open Item, and a narrowed space (user and
// agent) with an Item in it.
type agentWorld struct {
	k                    *itemservice.Service
	user, agent          item.Principal
	open, space, inSpace uuid.UUID
}

func newAgentWorld(t *testing.T) agentWorld {
	t.Helper()
	k := itemservice.New(pool)
	user := person("user")
	agent := agentFor(t, k, user)
	w := agentWorld{k: k, user: user, agent: agent}
	w.open = createAs(t, k, user, "open", nil, nil).ItemID
	w.space = createAs(t, k, user, "space", &itemservice.Narrow{Readers: []string{user.Actor, agent.Actor}}, nil).ItemID
	w.inSpace = createAs(t, k, user, "in the space", nil, &w.space).ItemID
	return w
}

// --- sessions ---

// An agent must say which session it acts in, on reads and writes alike.
func TestAgent_WithoutASession_IsInvalid(t *testing.T) {
	w := newAgentWorld(t)
	noSession := item.Principal{Actor: w.agent.Actor, OnBehalfOf: w.user.Actor}
	if _, err := w.k.Get(testCtx(t), noSession, w.open); !errors.Is(err, itemservice.ErrInvalid) {
		t.Errorf("Get: error = %v, want ErrInvalid", err)
	}
	if _, err := w.k.Search(testCtx(t), itemservice.SearchRequest{Principal: noSession, Query: "open"}); !errors.Is(err, itemservice.ErrInvalid) {
		t.Errorf("Search: error = %v, want ErrInvalid", err)
	}
	_, err := w.k.Update(testCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: noSession, ItemID: w.open, Fields: map[string]any{"f_title": "x"},
	})
	if !errors.Is(err, itemservice.ErrInvalid) {
		t.Errorf("Update: error = %v, want ErrInvalid", err)
	}
}

// Sessions are minted by the server and bound to one agent and one user. An
// unknown session, another agent's session, or the same agent's session used
// for a different user is ErrInvalid: nothing is recorded, nothing written.
// Otherwise an agent could escape the write rule with a fresh session that
// has no recorded reads.
func TestAgent_SessionMustBeMintedForThisAgentAndUser(t *testing.T) {
	w := newAgentWorld(t)
	otherAgent := agentFor(t, w.k, w.user)
	otherUser := person("other-user")
	cases := []struct {
		name string
		p    item.Principal
	}{
		{"unknown session", item.Principal{Actor: w.agent.Actor, OnBehalfOf: w.user.Actor, AgentSession: "session-made-up"}},
		{"another agent's session", item.Principal{Actor: w.agent.Actor, OnBehalfOf: w.user.Actor, AgentSession: otherAgent.AgentSession}},
		{"same agent, another user", item.Principal{Actor: w.agent.Actor, OnBehalfOf: otherUser.Actor, AgentSession: w.agent.AgentSession}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := w.k.Get(testCtx(t), c.p, w.inSpace); !errors.Is(err, itemservice.ErrInvalid) {
				t.Errorf("Get: error = %v, want ErrInvalid", err)
			}
			if _, err := w.k.Search(testCtx(t), itemservice.SearchRequest{Principal: c.p, Query: "space"}); !errors.Is(err, itemservice.ErrInvalid) {
				t.Errorf("Search: error = %v, want ErrInvalid", err)
			}
			_, err := w.k.Update(testCtx(t), itemservice.UpdateRequest{
				IdempotencyKey: newKey(), Principal: c.p, ItemID: w.open, Fields: map[string]any{"f_title": "x"},
			})
			if !errors.Is(err, itemservice.ErrInvalid) {
				t.Errorf("Update: error = %v, want ErrInvalid", err)
			}
			_, err = w.k.Create(testCtx(t), itemservice.CreateRequest{
				IdempotencyKey: newKey(), Principal: c.p, TypeID: "ticket", TypeVersion: 1,
			})
			if !errors.Is(err, itemservice.ErrInvalid) {
				t.Errorf("Create: error = %v, want ErrInvalid", err)
			}
			if got := recordedReads(t, c.p.AgentSession); len(got) != 0 {
				t.Errorf("recorded reads for session %q: %v, want none", c.p.AgentSession, got)
			}
		})
	}
	assertNothingWritten(t, w.open, 1)
	assertNothingWritten(t, w.inSpace, 1)
}

// StartAgentSession is for agents, acting for a user or for themselves.
func TestAgent_StartAgentSession_Rules(t *testing.T) {
	k := itemservice.New(pool)
	for _, p := range []item.Principal{
		{Actor: "user:" + uuid.NewString()},
		{Actor: "system:timer"},
		{Actor: "agent:" + uuid.NewString(), OnBehalfOf: "agent:" + uuid.NewString()},
		{Actor: "agent:" + uuid.NewString(), OnBehalfOf: "user:" + uuid.NewString(), AgentSession: "chosen-by-caller"},
	} {
		if _, err := k.StartAgentSession(testCtx(t), wholeNamespace(p)); !errors.Is(err, itemservice.ErrInvalid) {
			t.Errorf("StartAgentSession(%+v): error = %v, want ErrInvalid", p, err)
		}
	}
	a := withSession(t, k, item.Principal{Actor: "agent:" + uuid.NewString(), OnBehalfOf: "user:" + uuid.NewString()})
	b := withSession(t, k, item.Principal{Actor: a.Actor, OnBehalfOf: a.OnBehalfOf})
	if a.AgentSession == "" || a.AgentSession == b.AgentSession {
		t.Errorf("sessions %q and %q: want two distinct, non-empty IDs", a.AgentSession, b.AgentSession)
	}
	n := count(t, `select count(*) from agent_sessions where session = $1 and agent = $2 and on_behalf_of = $3
	               and created_at is not null`, a.AgentSession, a.Actor, a.OnBehalfOf)
	if n != 1 {
		t.Errorf("agent_sessions rows for %s = %d, want 1", a.AgentSession, n)
	}
}

// The event records the session the agent acted in.
func TestAgent_SessionIsStoredOnTheEvent(t *testing.T) {
	w := newAgentWorld(t)
	res, err := w.k.Update(testCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: w.agent, ItemID: w.open, Fields: map[string]any{"f_title": "x"},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	var session *string
	err = pool.QueryRow(testCtx(t), `select agent_session from item_events where item_id = $1 and item_seq = $2`,
		w.open, res.ItemSeq).Scan(&session)
	if err != nil || session == nil || *session != w.agent.AgentSession {
		t.Errorf("event agent_session = %v (err %v), want %s", session, err, w.agent.AgentSession)
	}
}

// --- what is recorded ---

// Reading a narrowed Item records its narrowing scope; reading an open Item
// records nothing, since everyone may read it anyway. Search records the
// narrowing scope of every result. Another session's reads are its own.
func TestAgent_ReadsRecordNarrowingScopesOnly(t *testing.T) {
	w := newAgentWorld(t)
	assertVisible(t, w.k, w.agent, w.open)
	if got := recordedReads(t, w.agent.AgentSession); len(got) != 0 {
		t.Errorf("after reading an open Item: recorded %v, want nothing", got)
	}

	assertVisible(t, w.k, w.agent, w.inSpace)
	space := scopeOf(t, w.space)
	if got := recordedReads(t, w.agent.AgentSession); len(got) != 1 || got[0] != space {
		t.Errorf("after reading inside the space: recorded %v, want [%s]", got, space)
	}

	other := withSession(t, w.k, item.Principal{Actor: w.agent.Actor, OnBehalfOf: w.user.Actor})
	if got := recordedReads(t, other.AgentSession); len(got) != 0 {
		t.Errorf("another session: recorded %v, want nothing", got)
	}
	term := word()
	inSpace := createAs(t, w.k, w.user, "searchable "+term, nil, &w.space)
	if _, err := w.k.Search(testCtx(t), itemservice.SearchRequest{Principal: other, Query: term, MinSeq: inSpace.CommitSeq}); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if got := recordedReads(t, other.AgentSession); len(got) != 1 || got[0] != space {
		t.Errorf("after a Search: recorded %v, want [%s]", got, space)
	}
}

// --- the rule ---

// After reading inside a narrowed space, writing into an open Item would
// carry what was read to more people: ErrNeedsApproval, naming the target
// scope, and nothing is written.
func TestAgent_NarrowReadThenWiderWrite_NeedsApproval(t *testing.T) {
	w := newAgentWorld(t)
	assertVisible(t, w.k, w.agent, w.inSpace)

	_, err := w.k.Update(testCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: w.agent, ItemID: w.open, Fields: map[string]any{"f_title": "leak"},
	})
	assertNeedsApproval(t, err, scopeOf(t, w.open))
	assertNothingWritten(t, w.open, 1)
}

// Writing at or below everything read is allowed without approval.
func TestAgent_WriteAtOrBelowTheReadScope_IsAllowed(t *testing.T) {
	w := newAgentWorld(t)
	assertVisible(t, w.k, w.agent, w.inSpace)

	// A user's writes are not limited by the agent rule.
	if _, err := w.k.Update(testCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: w.user, ItemID: w.inSpace, Fields: map[string]any{"f_title": "by the user"},
	}); err != nil {
		t.Errorf("write by the user: %v", err)
	}
	if _, err := w.k.Update(testCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: w.agent, ItemID: w.inSpace, Fields: map[string]any{"f_title": "same scope"},
	}); err != nil {
		t.Errorf("write into the scope read: %v", err)
	}
	deeper := createAs(t, w.k, w.agent, "deeper", &itemservice.Narrow{Readers: []string{w.user.Actor, w.agent.Actor}}, &w.inSpace)
	if _, err := w.k.Update(testCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: w.agent, ItemID: deeper.ItemID, Fields: map[string]any{"f_title": "below"},
	}); err != nil {
		t.Errorf("write below the scope read: %v", err)
	}
}

// A plain transition puts no content anywhere and is not limited; a
// transition with fields is a write.
func TestAgent_TransitionWithFieldsIsAWrite(t *testing.T) {
	w := newAgentWorld(t)
	assertVisible(t, w.k, w.agent, w.inSpace)

	if _, err := w.k.Transition(testCtx(t), itemservice.TransitionRequest{
		IdempotencyKey: newKey(), Principal: w.agent, ItemID: w.open, ToState: "canceled",
	}); err != nil {
		t.Errorf("plain transition of an open Item: %v", err)
	}
	other := createAs(t, w.k, w.user, "open 2", nil, nil)
	_, err := w.k.Transition(testCtx(t), itemservice.TransitionRequest{
		IdempotencyKey: newKey(), Principal: w.agent, ItemID: other.ItemID, ToState: "canceled",
		Fields: map[string]any{"f_title": "leak"},
	})
	assertNeedsApproval(t, err, scopeOf(t, other.ItemID))
}

// Creating into a wider container needs approval (the target is the
// container's scope; at the top level, the root scope).
func TestAgent_CreateIntoAWiderContainer_NeedsApproval(t *testing.T) {
	w := newAgentWorld(t)
	assertVisible(t, w.k, w.agent, w.inSpace)

	_, err := w.k.Create(testCtx(t), itemservice.CreateRequest{
		IdempotencyKey: newKey(), Principal: w.agent, TypeID: "ticket", TypeVersion: 1,
		ContainerID: &w.open, Fields: map[string]any{"f_title": "leak"},
	})
	assertNeedsApproval(t, err, scopeOf(t, w.open))

	var root uuid.UUID
	if err := pool.QueryRow(testCtx(t), `select id from scopes where namespace_id = 'default' and parent_id is null`).Scan(&root); err != nil {
		t.Fatalf("read root: %v", err)
	}
	_, err = w.k.Create(testCtx(t), itemservice.CreateRequest{
		IdempotencyKey: newKey(), Principal: w.agent, TypeID: "ticket", TypeVersion: 1,
		Fields: map[string]any{"f_title": "leak"},
	})
	assertNeedsApproval(t, err, root)
}

// A move to a wider destination needs approval (target = the destination's
// scope), on top of ConfirmWiden.
func TestAgent_MoveToAWiderDestination_NeedsApproval(t *testing.T) {
	w := newAgentWorld(t)
	assertVisible(t, w.k, w.agent, w.inSpace)

	_, err := moveTo(t, w.k, w.agent, w.inSpace, &w.open, true)
	assertNeedsApproval(t, err, scopeOf(t, w.open))
	assertNothingWritten(t, w.inSpace, 1)
}

// A policy change that widens readers, Inherit included, needs approval; one
// that narrows does not.
func TestAgent_WideningPolicyChange_NeedsApproval(t *testing.T) {
	w := newAgentWorld(t)
	assertVisible(t, w.k, w.agent, w.inSpace)
	space := scopeOf(t, w.space)

	if _, err := w.k.Update(testCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: w.agent, ItemID: w.space,
		Policy: &itemservice.PolicyPatch{Readers: []string{w.user.Actor, w.agent.Actor}},
	}); err != nil {
		t.Errorf("a policy change that does not widen: %v", err)
	}
	bob := person("bob")
	_, err := w.k.Update(testCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: w.agent, ItemID: w.space,
		Policy: &itemservice.PolicyPatch{Readers: []string{w.user.Actor, w.agent.Actor, bob.Actor}},
	})
	assertNeedsApproval(t, err, space)
	_, err = w.k.Update(testCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: w.agent, ItemID: w.space,
		Policy: &itemservice.PolicyPatch{Inherit: true},
	})
	assertNeedsApproval(t, err, space)
}

// Reads in two separate narrowed branches: no scope is below both, so a
// write into either needs approval. Conservative, and correct.
func TestAgent_ReadsInTwoBranches_EveryWriteNeedsApproval(t *testing.T) {
	w := newAgentWorld(t)
	other := createAs(t, w.k, w.user, "other space", &itemservice.Narrow{Readers: []string{w.user.Actor, w.agent.Actor}}, nil)
	inOther := createAs(t, w.k, w.user, "in the other space", nil, &other.ItemID)
	assertVisible(t, w.k, w.agent, w.inSpace)
	assertVisible(t, w.k, w.agent, inOther.ItemID)

	for _, id := range []uuid.UUID{w.inSpace, inOther.ItemID} {
		_, err := w.k.Update(testCtx(t), itemservice.UpdateRequest{
			IdempotencyKey: newKey(), Principal: w.agent, ItemID: id, Fields: map[string]any{"f_title": "x"},
		})
		assertNeedsApproval(t, err, scopeOf(t, id))
	}
}

// --- approval ---

// The session's user approves the one target scope; the agent's retry, with
// the same idempotency key, then succeeds. The approval is recorded with who
// gave it and when, and is bound to that scope: another wider scope still
// needs its own approval.
func TestAgent_ApprovalByTheSessionUser_UnlocksThatScopeOnly(t *testing.T) {
	w := newAgentWorld(t)
	assertVisible(t, w.k, w.agent, w.inSpace)
	target := scopeOf(t, w.open)
	req := itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: w.agent, ItemID: w.open, Fields: map[string]any{"f_title": "approved"},
	}
	_, err := w.k.Update(testCtx(t), req)
	assertNeedsApproval(t, err, target)

	if err := w.k.ApproveWiden(testCtx(t), itemservice.ApproveWidenRequest{
		Principal: w.user, Session: w.agent.AgentSession, Scope: target,
	}); err != nil {
		t.Fatalf("ApproveWiden: %v", err)
	}
	n := count(t, `select count(*) from agent_session_approvals
	               where session = $1 and scope_id = $2 and approved_by = $3 and created_at is not null`,
		w.agent.AgentSession, target, w.user.Actor)
	if n != 1 {
		t.Errorf("approval rows = %d, want 1 naming who approved", n)
	}
	if _, err := w.k.Update(testCtx(t), req); err != nil {
		t.Fatalf("retry after approval: %v", err)
	}

	// Bound to one scope: a different wider scope is still refused.
	another := createAs(t, w.k, w.user, "another open space", nil, nil)
	inAnother := createAs(t, w.k, w.user, "in it", nil, &another.ItemID)
	_, err = w.k.Update(testCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: w.agent, ItemID: inAnother.ItemID, Fields: map[string]any{"f_title": "x"},
	})
	assertNeedsApproval(t, err, scopeOf(t, inAnother.ItemID))
}

// Only the session's user can approve. The agent cannot approve itself, and
// another user's approval does not count. The approver must see the scope.
func TestAgent_ApprovalRules(t *testing.T) {
	w := newAgentWorld(t)
	assertVisible(t, w.k, w.agent, w.inSpace)
	target := scopeOf(t, w.open)

	err := w.k.ApproveWiden(testCtx(t), itemservice.ApproveWidenRequest{
		Principal: w.agent, Session: w.agent.AgentSession, Scope: target,
	})
	if !errors.Is(err, itemservice.ErrInvalid) {
		t.Errorf("an agent approving: error = %v, want ErrInvalid", err)
	}

	stranger := person("stranger")
	if err := w.k.ApproveWiden(testCtx(t), itemservice.ApproveWidenRequest{
		Principal: stranger, Session: w.agent.AgentSession, Scope: target,
	}); err != nil {
		t.Fatalf("ApproveWiden by another user: %v", err)
	}
	_, err = w.k.Update(testCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: w.agent, ItemID: w.open, Fields: map[string]any{"f_title": "x"},
	})
	assertNeedsApproval(t, err, target) // the stranger is not the session's user

	hidden := createAs(t, w.k, stranger, "stranger only", &itemservice.Narrow{Readers: readers(stranger)}, nil)
	err = w.k.ApproveWiden(testCtx(t), itemservice.ApproveWidenRequest{
		Principal: w.user, Session: w.agent.AgentSession, Scope: scopeOf(t, hidden.ItemID),
	})
	if !errors.Is(err, itemservice.ErrNotFound) {
		t.Errorf("approving a scope the user cannot see: error = %v, want ErrNotFound", err)
	}
}
