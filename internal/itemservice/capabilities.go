package itemservice

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/policy"
)

// Agent sessions in capability mode (docs/design.md, "Who is calling"). A
// session names the scopes it may read and write, container Items with
// everything below them or the whole namespace, and its powers. Outside its
// scopes an Item does not exist for the session (ErrNotFound, on reads and
// writes); without a write power every write is ErrForbidden. Membership is
// checked at each request, so a move changes what the session sees at once.

// StartAgentSession mints a session for an agent acting for a user (or for
// itself, with no on_behalf_of), in the agent's namespace. Every read and
// write by that agent must carry the session, so an agent cannot escape the
// write rule with a fresh session it made up.
func (k *Service) StartAgentSession(ctx context.Context, req StartAgentSessionRequest) (string, error) {
	p := req.Principal
	if !item.IsKind(p.Actor, item.KindAgent) || p.AgentSession != "" ||
		(p.OnBehalfOf != "" && !item.IsKind(p.OnBehalfOf, item.KindUser, item.KindCustomer)) {
		return "", fmt.Errorf("%w: a session is started by an agent:, acting for a user:, a customer: or itself, "+
			"and the session ID is minted here", ErrInvalid)
	}
	if len(req.Scopes) == 0 {
		return "", &InvalidError{Path: "scopes", Reason: "a session needs at least one scope"}
	}
	if len(req.Powers) == 0 {
		return "", &InvalidError{Path: "powers", Reason: "a session needs at least one power"}
	}
	for _, pw := range req.Powers {
		if !item.KnownPower(pw) {
			return "", &InvalidError{Path: "powers", Reason: "unknown power"}
		}
		// Acting for a customer, an agent has at most a customer's fixed
		// powers, whatever it was granted.
		if policy.CustomerOf(p) != "" && !customerPower(pw) {
			return "", fmt.Errorf("%w: power %s is not a customer's", ErrForbidden, pw)
		}
	}
	ns := nsOf(p)
	session := "ses_" + uuid.NewString()
	err := pgx.BeginFunc(ctx, k.pool, func(tx pgx.Tx) error {
		// Every scope must exist in the namespace and be visible to the agent
		// and its user, or it does not exist for them. This comes before the
		// grant, so a refusal never tells that a hidden container exists.
		for _, s := range req.Scopes {
			if s == uuid.Nil {
				continue
			}
			scope, err := landingScope(ctx, tx, ns, &s)
			if err != nil {
				return err
			}
			if err := checkVisible(ctx, tx, p, scope); err != nil {
				return err
			}
			if err := k.checkCustomerSees(ctx, tx, p, s); err != nil {
				return err
			}
		}
		if err := fitsGrant(ctx, tx, req); err != nil {
			return err
		}
		_, err := tx.Exec(ctx,
			`insert into agent_sessions (session, agent, on_behalf_of, namespace_id, scopes, powers)
			 values ($1, $2, nullif($3, ''), $4, $5, $6)`,
			session, p.Actor, p.OnBehalfOf, ns, req.Scopes, req.Powers)
		if err != nil {
			return fmt.Errorf("start agent session: %w", err)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return session, nil
}

// fitsGrant refuses a session that asks for more than its grant: every
// scope at or below a granted scope, every power covered by a granted one
// (read always is). A nil grant grants nothing.
func fitsGrant(ctx context.Context, q policy.Querier, req StartAgentSessionRequest) error {
	g := req.Grant
	if g == nil {
		return fmt.Errorf("%w: no grant lets this agent act for this user", ErrForbidden)
	}
	for _, pw := range req.Powers {
		if pw == item.PowerRead {
			continue
		}
		if !slices.ContainsFunc(g.Powers, func(granted string) bool { return item.PowerCovers(granted, pw) }) {
			return fmt.Errorf("%w: power %s is not granted", ErrForbidden, pw)
		}
	}
	for _, s := range req.Scopes {
		var id *uuid.UUID
		if s != uuid.Nil {
			id = &s
		}
		in, err := inScopes(ctx, q, g.Scopes, id)
		if err != nil {
			return err
		}
		if !in {
			return fmt.Errorf("%w: a scope is outside the grant", ErrForbidden)
		}
	}
	return nil
}

// AgentSession returns the session p names (p.AgentSession) if it exists in
// p's namespace and belongs to the agent p.Actor; otherwise
// ErrSessionInvalid. The API calls it to learn the user the agent acts for.
func (k *Service) AgentSession(ctx context.Context, p item.Principal) (AgentSession, error) {
	if !item.IsKind(p.Actor, item.KindAgent) || p.AgentSession == "" {
		return AgentSession{}, ErrSessionInvalid
	}
	s := AgentSession{ID: p.AgentSession}
	err := k.pool.QueryRow(ctx,
		`select agent, coalesce(on_behalf_of, ''), namespace_id, scopes, powers from agent_sessions
		 where session = $1 and agent = $2 and namespace_id = $3`,
		p.AgentSession, p.Actor, nsOf(p),
	).Scan(&s.Agent, &s.OnBehalfOf, &s.Namespace, &s.Scopes, &s.Powers)
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentSession{}, ErrSessionInvalid
	}
	if err != nil {
		return AgentSession{}, fmt.Errorf("read agent session: %w", err)
	}
	return s, nil
}

// caps is what an agent's session lets it do; nil for a principal that is
// not an agent, who is limited by visibility alone.
type caps struct {
	scopes []uuid.UUID
	powers []string
}

// loadCaps checks the principal's session (ErrSessionInvalid unless it is
// this agent's, for this user, in this namespace) and returns its
// capabilities. Non-agents get nil.
func loadCaps(ctx context.Context, q policy.Querier, p item.Principal) (*caps, error) {
	if !item.IsKind(p.Actor, item.KindAgent) {
		return nil, nil
	}
	c := &caps{}
	var onBehalfOf string
	err := q.QueryRow(ctx,
		`select coalesce(on_behalf_of, ''), scopes, powers from agent_sessions
		 where session = $1 and agent = $2 and namespace_id = $3`,
		p.AgentSession, p.Actor, nsOf(p),
	).Scan(&onBehalfOf, &c.scopes, &c.powers)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && onBehalfOf != p.OnBehalfOf) {
		return nil, ErrSessionInvalid
	}
	if err != nil {
		return nil, fmt.Errorf("read agent session: %w", err)
	}
	return c, nil
}

// whole reports whether the session covers the whole namespace.
func (c *caps) whole() bool { return c == nil || slices.Contains(c.scopes, uuid.Nil) }

// containers returns the session's container scopes (the whole namespace
// left out).
func (c *caps) containers() []uuid.UUID {
	return slices.DeleteFunc(slices.Clone(c.scopes), func(id uuid.UUID) bool { return id == uuid.Nil })
}

// inScopes reports whether the Item (nil: the top level) is one of the
// scopes or below one of them, walking up its containers as they are now.
// uuid.Nil among the scopes is the whole namespace.
func inScopes(ctx context.Context, q policy.Querier, scopes []uuid.UUID, id *uuid.UUID) (bool, error) {
	if slices.Contains(scopes, uuid.Nil) {
		return true, nil
	}
	if id == nil || len(scopes) == 0 {
		return false, nil
	}
	var in bool
	err := q.QueryRow(ctx,
		`with recursive up (id, container_id) as (
		     select id, container_id from items where id = $1
		     union all
		     select i.id, i.container_id from items i join up on i.id = up.container_id
		 )
		 select exists (select 1 from up where id = any($2))`, *id, scopes).Scan(&in)
	if err != nil {
		return false, fmt.Errorf("check session scopes: %w", err)
	}
	return in, nil
}

// checkInSession is ErrNotFound unless the principal's session covers the
// Item (nil: the top level, which only a whole-namespace session covers).
// Non-agents pass.
func checkInSession(ctx context.Context, q policy.Querier, p item.Principal, id *uuid.UUID) error {
	c, err := loadCaps(ctx, q, p)
	if err != nil || c == nil {
		return err
	}
	in, err := inScopes(ctx, q, c.scopes, id)
	if err != nil {
		return err
	}
	if !in {
		return ErrNotFound
	}
	return nil
}

// checkWritePower is ErrForbidden for a session with no write power.
func checkWritePower(ctx context.Context, q policy.Querier, p item.Principal) error {
	c, err := loadCaps(ctx, q, p)
	if err != nil || c == nil {
		return err
	}
	if !item.Writes(c.powers) {
		return fmt.Errorf("%w: this agent session may only read", ErrForbidden)
	}
	return nil
}

// sessionFilter returns, for a session limited to containers, a recursive
// CTE named in_session holding every Item at or below them, and the SQL
// condition that keeps alias's Items to it. Otherwise both are empty. The
// session's containers go in the named argument session_scopes.
func sessionFilter(c *caps, alias string, args pgx.NamedArgs) (cte, cond string) {
	if c.whole() {
		return "", ""
	}
	args["session_scopes"] = c.containers()
	cte = `in_session (id) as (
	           select id from items where id = any(@session_scopes::uuid[])
	           union all
	           select i.id from items i join in_session s on i.container_id = s.id
	       )`
	return cte, " and " + alias + ".id in (select id from in_session)"
}
