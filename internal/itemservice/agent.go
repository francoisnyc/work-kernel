package itemservice

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/policy"
)

// ErrNeedsApproval means an agent's write would put content into a scope
// wider than what it read in its session. It is not a refusal on the rules
// (ErrRejected) or on who may act (ErrForbidden): the session's user can
// approve the target scope with ApproveWiden, and the same request then
// succeeds. The error is a *NeedsApprovalError naming that scope.
var ErrNeedsApproval = errors.New("itemservice: needs approval")

// NeedsApprovalError carries the scope the agent's write would land in.
type NeedsApprovalError struct {
	Scope uuid.UUID
	// ScopeItem is the Item the write lands at (the Item written, the
	// container created in, a move's destination); nil for the top level.
	// The API names the scope by it; approving it approves Scope.
	ScopeItem *uuid.UUID
}

// ErrSessionInvalid means an agent's session does not exist, belongs to
// another agent or user, or to another namespace. It is an ErrInvalid in the
// kernel; the API answers it as a bad credential (UNAUTHENTICATED).
var ErrSessionInvalid = fmt.Errorf("%w: unknown agent session, or not this agent's", ErrInvalid)

// StartAgentSessionRequest asks for a session in capability mode.
type StartAgentSessionRequest struct {
	// The agent, with OnBehalfOf the user it acts for (or empty), in a
	// namespace. The session ID is minted here, never chosen.
	Principal item.Principal
	// Scopes the session may read and write: container Items (each with
	// everything below it, checked at each request) or uuid.Nil for the
	// whole namespace. Required; each must be visible to the agent and its
	// user, else ErrNotFound.
	Scopes []uuid.UUID
	// Powers, from the item.Power* vocabulary; read is implied. Required;
	// an unknown power is ErrInvalid.
	Powers []string
	// Grant is what the agent's user granted it (dev config for now); the
	// session's scopes and powers must fit inside it, else ErrForbidden. A
	// nil Grant grants nothing.
	Grant *Grant
}

// Grant is what a user let an agent do: scopes (as in a session) and powers.
type Grant struct {
	Scopes []uuid.UUID
	Powers []string
}

// AgentSession is a stored session.
type AgentSession struct {
	ID         string
	Agent      string
	OnBehalfOf string
	Namespace  string
	Scopes     []uuid.UUID
	Powers     []string
}

func (e *NeedsApprovalError) Error() string {
	return fmt.Sprintf("%v: the write lands in scope %s, wider than what this agent session read", ErrNeedsApproval, e.Scope)
}

// Unwrap lets errors.Is(err, ErrNeedsApproval) match, the way an exception
// subclass matches a catch of its parent.
func (e *NeedsApprovalError) Unwrap() error { return ErrNeedsApproval }

// checkSession refuses an agent principal whose session does not exist, or
// belongs to another agent, another user or another namespace
// (ErrSessionInvalid). Non-agents have no session.
func checkSession(ctx context.Context, q policy.Querier, p item.Principal) error {
	_, err := loadCaps(ctx, q, p)
	return err
}

// recordReads notes the narrowing scopes an agent read. Open scopes (a nil
// pointer) restrict nothing and are not recorded.
func recordReads(ctx context.Context, q execer, p item.Principal, scopes []*uuid.UUID) error {
	if !item.IsKind(p.Actor, item.KindAgent) {
		return nil
	}
	ids := []uuid.UUID{}
	for _, s := range scopes {
		if s != nil && !slices.Contains(ids, *s) {
			ids = append(ids, *s)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	_, err := q.Exec(ctx,
		`insert into agent_session_reads (session, scope_id) select $1, unnest($2::uuid[])
		 on conflict do nothing`, p.AgentSession, ids)
	if err != nil {
		return fmt.Errorf("record agent read: %w", err)
	}
	return nil
}

// execer is satisfied by a pool and a transaction.
type execer interface {
	policy.Querier
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// checkAgentWrite applies the agent write rule to a write that lands in the
// target scope: it must be at or below every narrowing scope the session
// read, unless the session's user approved that target.
//
// at is the Item the write lands at, as the caller named it: the Item
// written, the container created in, the destination of a move; nil for the
// top level. A refusal names it, and approving it approves target.
func checkAgentWrite(ctx context.Context, tx pgx.Tx, p item.Principal, target uuid.UUID, at *uuid.UUID) error {
	if !item.IsKind(p.Actor, item.KindAgent) {
		return nil
	}
	// The read scopes that are not the target or one of its ancestors.
	var outside int
	err := tx.QueryRow(ctx,
		`with recursive up (id, parent_id) as (
		     select id, parent_id from scopes where id = $2
		     union all
		     select s.id, s.parent_id from scopes s join up on s.id = up.parent_id
		 )
		 select count(*) from agent_session_reads r
		 where r.session = $1 and r.scope_id not in (select id from up)`,
		p.AgentSession, target).Scan(&outside)
	if err != nil {
		return fmt.Errorf("check agent write: %w", err)
	}
	if outside == 0 {
		return nil
	}
	return needsApproval(ctx, tx, p, target, at)
}

// checkAgentWiden applies the rule to a policy change that widens readers:
// what it exposes may be what the session read, so any recorded read makes
// it need approval.
func checkAgentWiden(ctx context.Context, tx pgx.Tx, p item.Principal, target uuid.UUID, at *uuid.UUID) error {
	if !item.IsKind(p.Actor, item.KindAgent) {
		return nil
	}
	var read bool
	err := tx.QueryRow(ctx, `select exists (select 1 from agent_session_reads where session = $1)`,
		p.AgentSession).Scan(&read)
	if err != nil {
		return fmt.Errorf("check agent widening: %w", err)
	}
	if !read {
		return nil
	}
	return needsApproval(ctx, tx, p, target, at)
}

// needsApproval returns nil if the session's user approved the target scope,
// and a *NeedsApprovalError otherwise. An approval by anyone else is on
// record but does not count.
func needsApproval(ctx context.Context, tx pgx.Tx, p item.Principal, target uuid.UUID, at *uuid.UUID) error {
	var approved bool
	err := tx.QueryRow(ctx,
		`select exists (select 1 from agent_session_approvals
		                where session = $1 and scope_id = $2 and approved_by = $3)`,
		p.AgentSession, target, p.OnBehalfOf).Scan(&approved)
	if err != nil {
		return fmt.Errorf("read approvals: %w", err)
	}
	if approved {
		return nil
	}
	return &NeedsApprovalError{Scope: target, ScopeItem: at}
}

// ApproveWidenRequest is a user approving one wider scope for one agent
// session.
type ApproveWidenRequest struct {
	// IdempotencyKey is required, as on every write.
	IdempotencyKey string
	Principal      item.Principal // the approving user
	Session        string
	Scope          uuid.UUID
	// ScopeItem names the scope by its Item when Scope is zero (the API
	// passes what NEEDS_APPROVAL named); nil with a zero Scope is the
	// namespace's root scope.
	ScopeItem *uuid.UUID
}

// ApproveWiden records a user's approval for an agent session to write into
// one scope. It is a separate act by the human, not a flag the agent could
// set on its own request. It counts only when the approver is the user the
// agent acts for; the approver must be able to see the scope.
//
// It is a write like any other: the idempotency key is required, the same
// key and request again records nothing more, and the key with another
// request is ErrIdempotencyKeyReuse. A session that does not exist, or is in
// another namespace than the approver's, is ErrNotFound.
func (k *Service) ApproveWiden(ctx context.Context, req ApproveWidenRequest) error {
	if req.IdempotencyKey == "" || !item.IsKind(req.Principal.Actor, item.KindUser) ||
		req.Principal.AgentSession != "" || req.Principal.OnBehalfOf != "" || req.Session == "" {
		return fmt.Errorf("%w: an approval is given by a user, for an agent session, with an idempotency key", ErrInvalid)
	}
	hash, err := requestHash("approve_widen", req)
	if err != nil {
		return err
	}
	ns := nsOf(req.Principal)
	_, err = k.write(ctx, req.Principal, req.IdempotencyKey, hash, func(tx pgx.Tx) (WriteResult, error) {
		var exists bool
		if err := tx.QueryRow(ctx,
			`select exists (select 1 from agent_sessions where session = $1 and namespace_id = $2)`,
			req.Session, ns).Scan(&exists); err != nil {
			return WriteResult{}, fmt.Errorf("read agent session: %w", err)
		}
		if !exists {
			return WriteResult{}, ErrNotFound
		}
		scope, err := approvedScope(ctx, tx, ns, req)
		if err != nil {
			return WriteResult{}, err
		}
		if err := checkVisible(ctx, tx, req.Principal, scope); err != nil {
			return WriteResult{}, err
		}
		_, err = tx.Exec(ctx,
			`insert into agent_session_approvals (session, scope_id, approved_by) values ($1, $2, $3)
			 on conflict do nothing`, req.Session, scope, req.Principal.Actor)
		if err != nil {
			return WriteResult{}, fmt.Errorf("record approval: %w", err)
		}
		return WriteResult{}, nil
	})
	return err
}

// approvedScope is the scope an approval names: Scope itself; else the scope
// content written at ScopeItem lands in (its own node if it has one, else
// the scope it inherits; ErrNotFound if it is not an Item of the
// namespace), which is the target a NEEDS_APPROVAL naming that Item was
// refused for; else the namespace's root scope.
func approvedScope(ctx context.Context, tx pgx.Tx, ns string, req ApproveWidenRequest) (uuid.UUID, error) {
	if req.Scope != uuid.Nil {
		return req.Scope, nil
	}
	if req.ScopeItem == nil {
		return rootScope(ctx, tx, ns)
	}
	return landingScope(ctx, tx, ns, req.ScopeItem)
}

// sessionAttribution puts the principal's agent session on the event's
// attribution. A different session already on the attribution is ErrInvalid.
func sessionAttribution(p item.Principal, a item.Attribution) (item.Attribution, error) {
	if p.AgentSession == "" {
		return a, nil
	}
	if a.AgentSession != "" && a.AgentSession != p.AgentSession {
		return a, fmt.Errorf("%w: the principal's agent session and the attribution's differ", ErrInvalid)
	}
	a.AgentSession = p.AgentSession
	return a, nil
}

// effective is who can read a scope: everyone (open), or a set of principals.
type effective struct {
	open    bool
	readers map[string]bool
}

func effectiveReaders(ctx context.Context, tx pgx.Tx, scope uuid.UUID) (effective, error) {
	var (
		ptr     *uuid.UUID
		readers []string
	)
	err := tx.QueryRow(ctx,
		`select s.policy_scope_id,
		        coalesce(array(select principal from scope_access where scope_id = s.policy_scope_id), '{}')
		 from scopes s where s.id = $1`, scope).Scan(&ptr, &readers)
	if err != nil {
		return effective{}, fmt.Errorf("read effective readers: %w", err)
	}
	e := effective{open: ptr == nil, readers: map[string]bool{}}
	for _, r := range readers {
		e.readers[r] = true
	}
	return e, nil
}

// widens reports whether after lets someone read who could not before.
func widens(before, after effective) bool {
	if before.open {
		return false
	}
	if after.open {
		return true
	}
	for r := range after.readers {
		if !before.readers[r] {
			return true
		}
	}
	return false
}
