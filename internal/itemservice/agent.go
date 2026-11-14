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
}

func (e *NeedsApprovalError) Error() string {
	return fmt.Sprintf("%v: the write lands in scope %s, wider than what this agent session read", ErrNeedsApproval, e.Scope)
}

// Unwrap lets errors.Is(err, ErrNeedsApproval) match, the way an exception
// subclass matches a catch of its parent.
func (e *NeedsApprovalError) Unwrap() error { return ErrNeedsApproval }

// StartAgentSession mints a session for an agent acting for a user (or for
// itself, with no on_behalf_of). Every read and write by that agent must
// carry the session, so an agent cannot escape the write rule with a fresh
// session it made up.
func (k *Service) StartAgentSession(ctx context.Context, p item.Principal) (string, error) {
	if !item.IsKind(p.Actor, item.KindAgent) || p.AgentSession != "" ||
		(p.OnBehalfOf != "" && !item.IsKind(p.OnBehalfOf, item.KindUser)) {
		return "", fmt.Errorf("%w: a session is started by an agent:, acting for a user: or for itself, "+
			"and the session ID is minted here", ErrInvalid)
	}
	session := "ses_" + uuid.NewString()
	_, err := k.pool.Exec(ctx,
		`insert into agent_sessions (session, agent, on_behalf_of) values ($1, $2, nullif($3, ''))`,
		session, p.Actor, p.OnBehalfOf)
	if err != nil {
		return "", fmt.Errorf("start agent session: %w", err)
	}
	return session, nil
}

// checkSession refuses an agent principal whose session does not exist or
// belongs to another agent or another user. Non-agents have no session.
func checkSession(ctx context.Context, q policy.Querier, p item.Principal) error {
	if !item.IsKind(p.Actor, item.KindAgent) {
		return nil
	}
	var agent, onBehalfOf string
	err := q.QueryRow(ctx,
		`select agent, coalesce(on_behalf_of, '') from agent_sessions where session = $1`, p.AgentSession,
	).Scan(&agent, &onBehalfOf)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (agent != p.Actor || onBehalfOf != p.OnBehalfOf)) {
		return fmt.Errorf("%w: unknown agent session, or not this agent's for this user", ErrInvalid)
	}
	if err != nil {
		return fmt.Errorf("read agent session: %w", err)
	}
	return nil
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
func checkAgentWrite(ctx context.Context, tx pgx.Tx, p item.Principal, target uuid.UUID) error {
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
	return needsApproval(ctx, tx, p, target)
}

// checkAgentWiden applies the rule to a policy change that widens readers:
// what it exposes may be what the session read, so any recorded read makes
// it need approval.
func checkAgentWiden(ctx context.Context, tx pgx.Tx, p item.Principal, target uuid.UUID) error {
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
	return needsApproval(ctx, tx, p, target)
}

// needsApproval returns nil if the session's user approved the target scope,
// and a *NeedsApprovalError otherwise. An approval by anyone else is on
// record but does not count.
func needsApproval(ctx context.Context, tx pgx.Tx, p item.Principal, target uuid.UUID) error {
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
	return &NeedsApprovalError{Scope: target}
}

// ApproveWidenRequest is a user approving one wider scope for one agent
// session.
type ApproveWidenRequest struct {
	Principal item.Principal // the approving user
	Session   string
	Scope     uuid.UUID
}

// ApproveWiden records a user's approval for an agent session to write into
// one scope. It is a separate act by the human, not a flag the agent could
// set on its own request. It counts only when the approver is the user the
// agent acts for; the approver must be able to see the scope.
func (k *Service) ApproveWiden(ctx context.Context, req ApproveWidenRequest) error {
	if !item.IsKind(req.Principal.Actor, item.KindUser) || req.Principal.AgentSession != "" ||
		req.Principal.OnBehalfOf != "" || req.Session == "" {
		return fmt.Errorf("%w: an approval is given by a user, for an agent session", ErrInvalid)
	}
	return pgx.BeginFunc(ctx, k.pool, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, `select exists (select 1 from agent_sessions where session = $1)`,
			req.Session).Scan(&exists); err != nil {
			return fmt.Errorf("read agent session: %w", err)
		}
		if !exists {
			return fmt.Errorf("%w: unknown agent session", ErrInvalid)
		}
		if err := checkVisible(ctx, tx, req.Principal, req.Scope); err != nil {
			return err
		}
		_, err := tx.Exec(ctx,
			`insert into agent_session_approvals (session, scope_id, approved_by) values ($1, $2, $3)
			 on conflict do nothing`, req.Session, req.Scope, req.Principal.Actor)
		if err != nil {
			return fmt.Errorf("record approval: %w", err)
		}
		return nil
	})
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
