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
	// A customer never holds a role, whatever the table says.
	if item.IsKind(principal, item.KindCustomer) {
		return nil, nil
	}
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
// as if the Item did not exist. ty is the Item's own Type version; every
// path of the item service passes it (it is optional only so that the
// Step 4 callers that project for staff stay as they were).
//
// Staff see an Item in a visible scope whole. A customer (or an agent
// acting for one) sees what the Type version lists in customer_fields and
// nothing else (Jira part 2, step 4a): see projectForCustomer. Without the
// Type, a customer sees nothing: fail closed.
func Project(p item.Principal, visible []uuid.UUID, it item.Item, ty ...*typedef.Type) (item.Item, bool) {
	// An Item without a scope is never "visible to everyone".
	if it.ScopeID == nil || !slices.Contains(visible, *it.ScopeID) {
		return item.Item{}, false
	}
	if CustomerOf(p) != "" {
		if len(ty) != 1 {
			return item.Item{}, false
		}
		return projectForCustomer(it, ty[0])
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
	return matchesEntry(tr.Allowed, principal, roles, nil)
}

// AuthorizeTransitionOn is AuthorizeTransition with the Item's fields, so a
// field:<id> entry matches the principal that field holds when the move is
// made; an unset or empty field matches no one. It judges one principal; a
// creator entry is not judged here (see MayTransition).
func AuthorizeTransitionOn(principal string, roles []string, tr typedef.Transition, fields map[string]any) bool {
	if len(tr.Allowed) == 0 {
		return true
	}
	return matchesEntry(tr.Allowed, principal, roles, fields)
}

// matchesEntry reports whether one principal matches a role:, principal:
// or field:<id> entry of an allowed list, the field read from fields (the
// Item's for a transition, the container's for a start block). It is the
// one place these entries are matched: MayTransition and MayStart share it.
// A field matches only the principal it holds (any member of a set).
func matchesEntry(entries []string, principal string, roles []string, fields map[string]any) bool {
	if slices.Contains(entries, typedef.AllowedPrincipalPrefix+principal) {
		return true
	}
	for _, role := range roles {
		if slices.Contains(entries, typedef.AllowedRolePrefix+role) {
			return true
		}
	}
	for _, entry := range entries {
		id, ok := strings.CutPrefix(entry, typedef.AllowedFieldPrefix)
		if !ok {
			continue
		}
		// A principal field holds one principal; a multi-value one, a set:
		// any member matches.
		switch held := fields[id].(type) {
		case string:
			if held != "" && held == principal {
				return true
			}
		case []any:
			if slices.Contains(held, any(principal)) {
				return true
			}
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
// (for field:<id>), who created it (for creator), and who created its
// container (for except: [container_creator], Jira 2.4b; empty at the top
// level).
type ItemFacts struct {
	Fields                       map[string]any
	CreatedBy, CreatedOnBehalfOf string

	ContainerCreatedBy, ContainerCreatedOnBehalfOf string
}

// creator means two things, on purpose; these two functions are the only
// places either is computed.
//
//   - In `allowed` it asks "is this the requester?": IsRequester. Narrow, so
//     that only the person the Item is for (or an agent acting for them)
//     may act as them.
//   - In `except` it asks "did this principal take part in creating the
//     Item?": TookPartInCreation. Wide, because `except` is separation of
//     duties, and those rules must be conservative: an agent that created a
//     request for ana must not complete it, whoever it acts for now.
//
// The kernel's own actors (system:, hook:, reaction:) are neither: they
// create nothing, and a reaction's on_behalf_of is attribution only.

// IsRequester reports whether the principal is the requester: its effective
// user (on_behalf_of if set, else the actor) is the Item's effective
// creator (created_on_behalf_of if set, else created_by).
func IsRequester(actor, onBehalfOf, createdBy, createdOnBehalfOf string) bool {
	if item.IsKernelActor(actor) {
		return false
	}
	user, creator := cmp.Or(onBehalfOf, actor), cmp.Or(createdOnBehalfOf, createdBy)
	return user != "" && user == creator
}

// IsNotRequester reports whether the principal is a person (or an agent
// acting for one) other than the requester: what `by: not_creator` asks.
// The kernel's own actors are neither the requester nor someone else: an
// Item a reaction created answers for nobody.
func IsNotRequester(actor, onBehalfOf, createdBy, createdOnBehalfOf string) bool {
	return !item.IsKernelActor(actor) && !IsRequester(actor, onBehalfOf, createdBy, createdOnBehalfOf)
}

// TookPartInCreation reports whether the principal took part in creating
// the Item: its actor or its effective user is created_by or
// created_on_behalf_of.
func TookPartInCreation(actor, onBehalfOf, createdBy, createdOnBehalfOf string) bool {
	if item.IsKernelActor(actor) {
		return false
	}
	for _, who := range []string{actor, cmp.Or(onBehalfOf, actor)} {
		if who != "" && (who == createdBy || who == createdOnBehalfOf) {
			return true
		}
	}
	return false
}

// MayTransition answers "may this principal make this move?" from the
// transition's `allowed` and `except`. Whether the move is legal at all is
// lifecycle's question, asked before.
//
//   - except: [creator] refuses anyone who took part in creating the Item
//     (TookPartInCreation), whatever `allowed` says; except:
//     [container_creator] anyone who took part in creating its container.
//   - allowed: [nobody] lets no one in: no person, agent, customer, nor the
//     kernel's own actors (Jira 2.4b). Only a reaction, which stands in for
//     `allowed` by consent, makes such a move.
//   - An empty `allowed` lets anyone in.
//   - creator in `allowed` is the requester (IsRequester), judged once for
//     the principal as a whole: an agent acting for the requester matches.
//   - Every other entry (role:, principal:, field:) is judged for the actor
//     and for the user it acts for, and both must pass: an agent has its
//     user's rights only where it also has its own.
func MayTransition(tr typedef.Transition, who Who, it ItemFacts) bool {
	if slices.Contains(tr.Except, typedef.AllowedCreator) &&
		TookPartInCreation(who.Actor, who.OnBehalfOf, it.CreatedBy, it.CreatedOnBehalfOf) {
		return false
	}
	if slices.Contains(tr.Except, typedef.AllowedContainerCreator) &&
		TookPartInCreation(who.Actor, who.OnBehalfOf, it.ContainerCreatedBy, it.ContainerCreatedOnBehalfOf) {
		return false
	}
	if slices.Contains(tr.Allowed, typedef.AllowedNobody) {
		return false
	}
	// A customer takes only the moves whose allowed names creator: an empty
	// allowed, a role or a principal never lets one in.
	if CustomerOf(item.Principal{Actor: who.Actor, OnBehalfOf: who.OnBehalfOf}) != "" {
		return mayCustomerTransition(tr, who, it)
	}
	if len(tr.Allowed) == 0 {
		return true
	}
	if slices.Contains(tr.Allowed, typedef.AllowedCreator) &&
		IsRequester(who.Actor, who.OnBehalfOf, it.CreatedBy, it.CreatedOnBehalfOf) {
		return true
	}
	if !AuthorizeTransitionOn(who.Actor, who.ActorRoles, tr, it.Fields) {
		return false
	}
	return who.OnBehalfOf == "" || AuthorizeTransitionOn(who.OnBehalfOf, who.OnBehalfOfRoles, tr, it.Fields)
}

// MayWriteField answers whether who may write the field on the public door
// (fields closed to agents by default). A field's writable_by lists its
// writers: people, agents and role:<name>; none means people only, so an
// agent writes a field only where its Type says so. kernel is no one's
// here: only the internal door writes it.
//
// An agent has its user's rights only where it also has its own: the agent
// must pass (as agents, or holding a role) and so must the user it acts
// for (as people, or holding a role). An agent acting for a customer is
// judged as that customer; the customer rules decide which fields that is.
func MayWriteField(f typedef.Field, who Who) bool {
	if f.WritableBy == typedef.WritableByKernel {
		return false
	}
	if CustomerOf(item.Principal{Actor: who.Actor, OnBehalfOf: who.OnBehalfOf}) != "" && who.OnBehalfOf != "" {
		return writes(f, who.OnBehalfOf, who.OnBehalfOfRoles)
	}
	if !writes(f, who.Actor, who.ActorRoles) {
		return false
	}
	return who.OnBehalfOf == "" || writes(f, who.OnBehalfOf, who.OnBehalfOfRoles)
}

// MayWriteInState answers the requester's part of who writes a field
// ("only the requester" for drafts): while an Item is in st, a field the
// state lists under requester_only is written on the public door only by
// the Item's requester (IsRequester: the effective creator, or an agent
// acting for them). A field the state does not list is not limited by it;
// writable_by is MayWriteField's question, asked as well, so an agent
// acting for the requester also needs the field open to agents. The
// kernel's own actors are no one's requester; the internal door does not
// ask.
func MayWriteInState(st typedef.State, id string, who Who, it ItemFacts) bool {
	if !slices.Contains(st.RequesterOnly, id) {
		return true
	}
	return IsRequester(who.Actor, who.OnBehalfOf, it.CreatedBy, it.CreatedOnBehalfOf)
}

// writes answers for one principal: an agent as agents, anyone else (a
// user, a customer) as people, and either by holding one of the roles.
func writes(f typedef.Field, principal string, roles []string) bool {
	if item.IsKind(principal, item.KindAgent) {
		if f.WritableByAgents {
			return true
		}
	} else if f.PeopleWrite() {
		return true
	}
	return slices.ContainsFunc(f.WritableByRoles, func(r string) bool { return slices.Contains(roles, r) })
}

// MayWriteRoleField answers whether who may write a field limited to roles
// (writable_by: [role:<name>, ...]) on the public door: the actor holds one
// of the roles and, when it acts for a user, so does the user (an agent has
// its user's rights only where it also has its own). A field without roles
// is not limited by it (MayWriteField asks who writes it, people or
// agents); a kernel-written field is not this function's question.
func MayWriteRoleField(f typedef.Field, who Who) bool {
	if len(f.WritableByRoles) == 0 {
		return true
	}
	holds := func(roles []string) bool {
		return slices.ContainsFunc(f.WritableByRoles, func(r string) bool { return slices.Contains(roles, r) })
	}
	if !holds(who.ActorRoles) {
		return false
	}
	return who.OnBehalfOf == "" || holds(who.OnBehalfOfRoles)
}
