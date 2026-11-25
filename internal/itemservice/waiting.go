package itemservice

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/policy"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// "Waiting on me" (docs/roadmap.md, track 1, item 5, step 1):
// Filter.WaitingOn keeps the open Items that wait on the caller ("me") or
// on a role ("role:<name>").
//
// The view is derived when it is asked: nothing is stored, so nothing can
// fall behind a write, a role granted or revoked, or a new Type version.
// The rule is policy's (WaitingMoves, NamedEntries); this file only turns
// it into SQL on the Items alias m, so that the condition joins the
// visibility test and the other filters before the limit. A test holds
// what the SQL returns to policy.WaitsOn and policy.WaitsOnRole.
//
// How: for every registered Type version and state, each waiting move
// becomes a condition under which the caller may make it for who they are
// ("true" when a role or their own ID lets them in; a test of the Item's
// field for a field:<id> entry; `except` as a test of who created the
// Item). States that share a condition are grouped, so the SQL is a few
// lists of (type, version, state) rather than one clause per state.

// noOpenHold leaves out an Item whose move is being checked: it waits on
// the check, not on a person. One lookup in item_holds_one_open.
const noOpenHold = `not exists (select 1 from item_holds h where h.item_id = m.id and h.status = 'open')`

// waitingConds checks a waiting_on value and returns its SQL conditions,
// adding their arguments to args (all named w_…). all is every registered
// Type version.
func (k *Service) waitingConds(ctx context.Context, p item.Principal, all []*typedef.Type, on string,
	args pgx.NamedArgs) ([]string, error) {
	invalid := func(reason string) error { return &InvalidError{Path: "filter.waiting_on", Reason: reason} }
	// A customer takes only the moves that name the creator, and those do
	// not count until a Type can say that a state waits on its requester.
	if policy.CustomerOf(p) != "" {
		return nil, invalid("a customer has no waiting_on view")
	}
	role, isRole := strings.CutPrefix(on, typedef.AllowedRolePrefix)
	if on != "me" && (!isRole || !roleName.MatchString(role)) {
		return nil, invalid("want me, or role:<name> for a role's queue")
	}

	who := policy.Who{Actor: p.Actor, OnBehalfOf: p.OnBehalfOf}
	if !isRole {
		// Roles are read now, never kept: a new member of a role sees its
		// queue at once.
		var err error
		if who.ActorRoles, err = policy.Roles(ctx, k.pool, nsOf(p), p.Actor); err != nil {
			return nil, err
		}
		if p.OnBehalfOf != "" {
			if who.OnBehalfOfRoles, err = policy.Roles(ctx, k.pool, nsOf(p), p.OnBehalfOf); err != nil {
				return nil, err
			}
		}
		args["w_actor"], args["w_behalf"] = p.Actor, p.OnBehalfOf
	}
	fields := waitFieldArgs{args: args, names: map[string]string{}}

	// groups: the states that share one condition, in the order found.
	var groups []*waitGroup
	byCond := map[string]*waitGroup{}
	for _, t := range all {
		for _, st := range t.States {
			for _, tr := range policy.WaitingMoves(t, st.ID) {
				cond, ok := "true", slices.Contains(tr.Allowed, typedef.AllowedRolePrefix+role)
				if !isRole {
					cond, ok = moveCond(tr, who, fields)
				}
				if !ok {
					continue
				}
				g := byCond[cond]
				if g == nil {
					g = &waitGroup{cond: cond, seen: map[string]bool{}}
					byCond[cond] = g
					groups = append(groups, g)
				}
				g.add(t, st.ID)
			}
		}
	}
	if len(groups) == 0 {
		return []string{"false"}, nil // no Type names them: an empty view
	}
	var ors []string
	for i, g := range groups {
		ta, va, sa := fmt.Sprintf("w_types_%d", i), fmt.Sprintf("w_versions_%d", i), fmt.Sprintf("w_states_%d", i)
		tda, sda := fmt.Sprintf("w_type_ids_%d", i), fmt.Sprintf("w_state_ids_%d", i)
		args[ta], args[va], args[sa] = g.types, g.versions, g.states
		args[tda], args[sda] = distinct(g.types), distinct(g.states)
		// The two "= any" tests say nothing the triples do not: they are
		// there for the planner, which can walk items_namespace_type_state
		// on them and cannot on the triples (cmd/bench -waiting).
		ors = append(ors, fmt.Sprintf(
			`(m.type_id = any(@%s::text[]) and m.state = any(@%s::text[])`+
				` and (m.type_id, m.type_version, m.state) in (select * from unnest(@%s::text[], @%s::int[], @%s::text[])) and %s)`,
			tda, sda, ta, va, sa, g.cond))
	}
	return []string{"(" + strings.Join(ors, " or ") + ")", noOpenHold}, nil
}

// waitGroup is the (type, version, state) triples that wait under one
// condition.
type waitGroup struct {
	cond     string
	types    []string
	versions []int32
	states   []string
	seen     map[string]bool
}

func (g *waitGroup) add(t *typedef.Type, state string) {
	key := fmt.Sprintf("%s\x00%d\x00%s", t.ID, t.Version, state)
	if g.seen[key] {
		return // two moves out of one state with the same condition
	}
	g.seen[key] = true
	g.types, g.versions, g.states = append(g.types, t.ID), append(g.versions, int32(t.Version)), append(g.states, state)
}

// waitFieldArgs gives each field ID a moves' `allowed` names one named
// argument: a field's ID is passed to SQL, never spliced into it.
type waitFieldArgs struct {
	args  pgx.NamedArgs
	names map[string]string
}

func (f waitFieldArgs) arg(field string) string {
	if a, ok := f.names[field]; ok {
		return a
	}
	a := fmt.Sprintf("w_field_%d", len(f.names))
	f.names[field], f.args[a] = a, field
	return a
}

// moveCond is the SQL under which who may make the waiting move tr for who
// they are (policy.WaitsOn, for one move), and false when they never can.
// It mirrors policy.MayTransition on the move's named entries: the actor
// passes, and so does the user it acts for; `except` refuses whoever took
// part in creating the Item, or its container.
func moveCond(tr typedef.Transition, who policy.Who, fields waitFieldArgs) (string, bool) {
	named := policy.NamedEntries(tr)
	cond, ok := principalCond(named, who.Actor, who.ActorRoles, "w_actor", fields)
	if !ok {
		return "", false
	}
	if who.OnBehalfOf != "" {
		behalf, ok := principalCond(named, who.OnBehalfOf, who.OnBehalfOfRoles, "w_behalf", fields)
		if !ok {
			return "", false
		}
		cond = "(" + cond + " and " + behalf + ")"
	}
	// policy.TookPartInCreation: the actor, or the user it acts for.
	const people = `(@w_actor, coalesce(nullif(@w_behalf, ''), @w_actor))`
	if slices.Contains(tr.Except, typedef.AllowedCreator) {
		cond += ` and m.created_by not in ` + people + ` and coalesce(m.created_on_behalf_of, '') not in ` + people
	}
	if slices.Contains(tr.Except, typedef.AllowedContainerCreator) {
		cond += ` and not exists (select 1 from items c where c.id = m.container_id and (c.created_by in ` + people +
			` or coalesce(c.created_on_behalf_of, '') in ` + people + `))`
	}
	return cond, true
}

// principalCond is the SQL under which one principal matches one of the
// named entries (policy's matchesEntry): "true" when a role it holds or
// its own ID is named; else a test of each named field, which holds one
// principal or a set of them; false when no entry can match.
func principalCond(named []string, principal string, roles []string, arg string, fields waitFieldArgs) (string, bool) {
	var tests []string
	for _, e := range named {
		if r, ok := strings.CutPrefix(e, typedef.AllowedRolePrefix); ok && slices.Contains(roles, r) {
			return "true", true
		}
		if e == typedef.AllowedPrincipalPrefix+principal {
			return "true", true
		}
		if f, ok := strings.CutPrefix(e, typedef.AllowedFieldPrefix); ok {
			fa := fields.arg(f)
			tests = append(tests, fmt.Sprintf(
				`(m.fields ->> @%[1]s = @%[2]s or (jsonb_typeof(m.fields -> @%[1]s) = 'array' and jsonb_exists(m.fields -> @%[1]s, @%[2]s)))`,
				fa, arg))
		}
	}
	if len(tests) == 0 {
		return "", false
	}
	return "(" + strings.Join(tests, " or ") + ")", true
}

// distinct returns the values of s, each once, sorted.
func distinct(s []string) []string {
	out := slices.Clone(s)
	slices.Sort(out)
	return slices.Compact(out)
}
