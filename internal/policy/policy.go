// Package policy decides who may see and do what. Every outbound path goes
// through Project.
//
// The data (scopes, readers, roles) lives in Postgres; the decisions are made
// in Go. VisibleScopes and Roles load the data; Project and
// AuthorizeTransition are pure functions over what was loaded.
package policy

import (
	"context"
	"fmt"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// Querier is satisfied by both a connection pool and a transaction, so the
// caller chooses whether the read is part of its transaction.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// VisibleScopes returns the scopes the principal can read in a namespace.
// Load it once per request and hand it to Project.
//
// The walk starts at the root scope, which everyone reads, and goes down. A
// node is visible if its parent is and, when the node narrows, the principal
// is one of its readers. So a child can narrow its parent's access and never
// widen it.
//
// When the principal acts on behalf of someone, the result is the
// intersection: a narrowing node must list both.
//
// Grants (explicit widening) are not built yet.
func VisibleScopes(ctx context.Context, q Querier, namespaceID string, p item.Principal) ([]uuid.UUID, error) {
	who := []string{p.Actor}
	if p.OnBehalfOf != "" && p.OnBehalfOf != p.Actor {
		who = append(who, p.OnBehalfOf)
	}
	// The inner count asks "how many of these principals read this node?";
	// all of them must.
	rows, err := q.Query(ctx,
		`with recursive visible as (
		     select id from scopes where namespace_id = $1 and parent_id is null
		     union all
		     select s.id
		     from scopes s
		     join visible v on s.parent_id = v.id
		     where not s.narrows
		        or (select count(*) from scope_readers r
		            where r.scope_id = s.id and r.principal = any($2)) = $3
		 )
		 select id from visible`,
		namespaceID, who, len(who))
	if err != nil {
		return nil, fmt.Errorf("read visible scopes: %w", err)
	}
	// CollectRows reads every row and closes the result set.
	ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return nil, fmt.Errorf("read visible scopes: %w", err)
	}
	return ids, nil
}

// Roles returns the roles one principal holds in a namespace. They are the
// server's own records; a request never carries its roles.
func Roles(ctx context.Context, q Querier, namespaceID, principal string) ([]string, error) {
	rows, err := q.Query(ctx,
		`select role from principal_roles where namespace_id = $1 and principal = $2`,
		namespaceID, principal)
	if err != nil {
		return nil, fmt.Errorf("read roles: %w", err)
	}
	roles, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("read roles: %w", err)
	}
	return roles, nil
}

// Project returns the Item as the principal may see it. The second value is
// false when the principal may not see it at all; the caller must then behave
// as if the Item did not exist.
//
// Step 4 is all-or-nothing per Item: an Item in a visible scope comes back
// unchanged. Hiding single fields comes later, which is why the principal is
// already a parameter.
func Project(p item.Principal, visible []uuid.UUID, it item.Item) (item.Item, bool) {
	// An Item without a scope is never "visible to everyone".
	if it.ScopeID == nil || !slices.Contains(visible, *it.ScopeID) {
		return item.Item{}, false
	}
	return it, true
}

// AuthorizeTransition answers "may this principal make this move?" from the
// transition's `allowed` list. An empty list allows anyone who can see the
// Item; seeing it is the caller's check, made before this one.
//
// Whether the move is legal at all is not decided here: that is
// lifecycle.Transition.
func AuthorizeTransition(principal string, roles []string, tr typedef.Transition) bool {
	if len(tr.Allowed) == 0 {
		return true
	}
	if slices.Contains(tr.Allowed, typedef.AllowedPrincipalPrefix+principal) {
		return true
	}
	for _, role := range roles {
		if slices.Contains(tr.Allowed, typedef.AllowedRolePrefix+role) {
			return true
		}
	}
	return false
}
