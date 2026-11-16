// Package policy decides who may see and do what. Every outbound path goes
// through Project.
//
// The data (scopes, readers, roles) lives in Postgres. CanSee and
// VisibleCondition answer "may this principal read this scope?" from the kept
// scopes.open and scope_access; Roles loads roles; Project and
// AuthorizeTransition are pure functions over what was loaded.
package policy

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// Querier is satisfied by both a connection pool and a transaction, so the
// caller chooses whether the read is part of its transaction.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// VisibleCondition is the one rule for "may the principal read scope s?",
// as SQL over a row `s` of scopes and the named arguments @actor and @behalf
// (empty when nobody is acted for). A scope is readable when nothing at or
// above it narrows (policy_scope_id is null), or when the nearest narrowing
// scope's scope_access lists the actor and, if set, the person acted for: an
// agent sees only what both may see.
//
// Pointers and rows are kept up to date in the same transaction as every
// policy change, so the answer is right on the very next read. It is one
// indexed lookup per scope: other people's narrowed scopes cost nothing.
const VisibleCondition = `(s.policy_scope_id is null or (
	exists (select 1 from scope_access a where a.scope_id = s.policy_scope_id and a.principal = @actor)
	and (@behalf = '' or exists (select 1 from scope_access b where b.scope_id = s.policy_scope_id and b.principal = @behalf))))`

// VisibleArgs returns the named arguments VisibleCondition needs.
// pgx.NamedArgs fills @name placeholders, like named parameters in JDBC templates.
func VisibleArgs(p item.Principal) pgx.NamedArgs {
	behalf := p.OnBehalfOf
	if behalf == p.Actor {
		behalf = ""
	}
	return pgx.NamedArgs{"actor": p.Actor, "behalf": behalf}
}

// CanSee reports whether the principal may read the scope. Grants (explicit
// widening) are not built yet.
func CanSee(ctx context.Context, q Querier, p item.Principal, scopeID uuid.UUID) (bool, error) {
	args := VisibleArgs(p)
	args["scope"] = scopeID
	var ok bool
	err := q.QueryRow(ctx, `select `+VisibleCondition+` from scopes s where s.id = @scope`, args).Scan(&ok)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check visibility: %w", err)
	}
	return ok, nil
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

// AuthorizeTransitionOn is AuthorizeTransition with the Item's fields, so a
// field:<id> entry matches the principal that field holds when the move is
// made; an unset or empty field matches no one. It judges one principal; a
// creator entry is not judged here (see MayTransition).
func AuthorizeTransitionOn(principal string, roles []string, tr typedef.Transition, fields map[string]any) bool {
	if AuthorizeTransition(principal, roles, tr) {
		return true
	}
	for _, entry := range tr.Allowed {
		id, ok := strings.CutPrefix(entry, typedef.AllowedFieldPrefix)
		if !ok {
			continue
		}
		if held, _ := fields[id].(string); held != "" && held == principal {
			return true
		}
	}
	return false
}

// Who is the principal asking for a transition, with the roles each part
// holds in the Item's namespace. OnBehalfOf is the user an agent acts for;
// empty for a user acting as themselves and for the kernel's own actors.
type Who struct {
	Actor           string
	ActorRoles      []string
	OnBehalfOf      string
	OnBehalfOfRoles []string
}

// ItemFacts is what `allowed` and `except` read of the Item: its fields
// (for field:<id>) and who created it (for creator).
type ItemFacts struct {
	Fields                       map[string]any
	CreatedBy, CreatedOnBehalfOf string
}

// IsCreator reports whether the principal is the Item's creator: its
// effective user (on_behalf_of if set, else the actor) is the Item's
// effective creator (created_on_behalf_of if set, else created_by). The
// kernel's own actors (system:, hook:) never are. It is the one place this
// is computed.
func IsCreator(actor, onBehalfOf, createdBy, createdOnBehalfOf string) bool {
	if item.IsKind(actor, item.KindSystem, item.KindHook) {
		return false
	}
	user, creator := cmp.Or(onBehalfOf, actor), cmp.Or(createdOnBehalfOf, createdBy)
	return user != "" && user == creator
}

// MayTransition answers "may this principal make this move?" from the
// transition's `allowed` and `except`. Whether the move is legal at all is
// lifecycle's question, asked before.
//
//   - except: [creator] refuses the creator, whatever `allowed` says.
//   - An empty `allowed` lets anyone in.
//   - creator in `allowed` is judged once, for the principal as a whole:
//     an agent acting for the creator matches.
//   - Every other entry (role:, principal:, field:) is judged for the actor
//     and for the user it acts for, and both must pass: an agent has its
//     user's rights only where it also has its own.
func MayTransition(tr typedef.Transition, who Who, it ItemFacts) bool {
	creator := IsCreator(who.Actor, who.OnBehalfOf, it.CreatedBy, it.CreatedOnBehalfOf)
	if creator && slices.Contains(tr.Except, typedef.AllowedCreator) {
		return false
	}
	if len(tr.Allowed) == 0 {
		return true
	}
	if creator && slices.Contains(tr.Allowed, typedef.AllowedCreator) {
		return true
	}
	if !AuthorizeTransitionOn(who.Actor, who.ActorRoles, tr, it.Fields) {
		return false
	}
	return who.OnBehalfOf == "" || AuthorizeTransitionOn(who.OnBehalfOf, who.OnBehalfOfRoles, tr, it.Fields)
}
