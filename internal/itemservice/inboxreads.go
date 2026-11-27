package itemservice

import (
	"context"
	"fmt"
	"slices"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/policy"
)

// The reads an inbox screen needs beside Search (docs/roadmap.md, track 1,
// item 5, step 4): a capped count, who the caller is, and the moves the
// caller may make on each Item of a waiting_on answer.

// CountCap is the most Count ever answers: past it, "50+".
const CountCap = 50

// CountRequest is a search's words and filter, with no page and no order.
type CountRequest struct {
	Principal item.Principal
	Query     string
	Filter    Filter
}

// CountResult: how many Items match and the principal can see, at most
// CountCap; Capped when more do.
type CountResult struct {
	Count  int
	Capped bool
}

// Count answers how many Items Search would return for the same words and
// filter. It is that search, read once with a page of CountCap and one
// more: the same checks, the same query, each Item through policy.Project,
// and the count taken from what Project returns. An agent's reads are
// recorded as for the search. So a count can never tell of an Item a page
// would hide.
func (k *Service) Count(ctx context.Context, req CountRequest) (CountResult, error) {
	q, err := k.searchQuery(ctx, SearchRequest{Principal: req.Principal, Query: req.Query, Filter: req.Filter, Limit: CountCap + 1})
	if err != nil {
		return CountResult{}, err
	}
	found, _, err := k.page(ctx, q)
	if err != nil {
		return CountResult{}, err
	}
	n := 0
	var read []*uuid.UUID
	for _, h := range found {
		ty, err := k.loadType(ctx, k.pool, h.it.TypeID, h.it.TypeVersion)
		if err != nil {
			return CountResult{}, err
		}
		if _, ok := policy.Project(req.Principal, []uuid.UUID{*h.it.ScopeID}, h.it, ty); ok {
			n++
			read = append(read, h.policyScope)
		}
	}
	if err := recordReads(ctx, k.pool, req.Principal, read); err != nil {
		return CountResult{}, err
	}
	return CountResult{Count: min(n, CountCap), Capped: n > CountCap}, nil
}

// Me is who a caller is: their principal, their namespace and the roles
// they hold in it; for an agent, the user it acts for with their roles,
// and its session. It is about the caller only and holds no Item data.
type Me struct {
	Principal       string
	Namespace       string
	Roles           []string // sorted
	OnBehalfOf      string
	OnBehalfOfRoles []string      // sorted
	Session         *AgentSession // an agent's; nil for anyone else
}

// WhoAmI returns who the principal is. Roles are read now, from the
// namespace's principal_roles. An agent must carry its session
// (ErrSessionInvalid otherwise), as on every read.
func (k *Service) WhoAmI(ctx context.Context, p item.Principal) (Me, error) {
	if err := requireNamespace(p); err != nil {
		return Me{}, err
	}
	if err := checkPublicPrincipal(p); err != nil {
		return Me{}, err
	}
	me := Me{Principal: p.Actor, Namespace: nsOf(p), OnBehalfOf: p.OnBehalfOf}
	if item.IsKind(p.Actor, item.KindAgent) {
		s, err := k.AgentSession(ctx, p)
		if err != nil {
			return Me{}, err
		}
		if s.OnBehalfOf != p.OnBehalfOf {
			return Me{}, ErrSessionInvalid
		}
		me.Session = &s
	}
	var err error
	if me.Roles, err = policy.Roles(ctx, k.pool, nsOf(p), p.Actor); err != nil {
		return Me{}, err
	}
	slices.Sort(me.Roles)
	if p.OnBehalfOf != "" {
		if me.OnBehalfOfRoles, err = policy.Roles(ctx, k.pool, nsOf(p), p.OnBehalfOf); err != nil {
			return Me{}, err
		}
		slices.Sort(me.OnBehalfOfRoles)
	}
	return me, nil
}

// fillMoves sets Moves on each Item of a waiting_on answer: the moves out
// of its state that the principal may make (policy.Moves), in the Type's
// order; and Withheld, the moves their `allowed` lets in that except or
// the claim refuses them (policy.Withheld). Roles are read once; the creators of the Items' containers in one
// query (for except: [container_creator]).
func (k *Service) fillMoves(ctx context.Context, p item.Principal, items []item.Item) error {
	if len(items) == 0 {
		return nil
	}
	who := policy.Who{Actor: p.Actor, OnBehalfOf: p.OnBehalfOf}
	var err error
	if who.ActorRoles, err = policy.Roles(ctx, k.pool, nsOf(p), p.Actor); err != nil {
		return err
	}
	if p.OnBehalfOf != "" {
		if who.OnBehalfOfRoles, err = policy.Roles(ctx, k.pool, nsOf(p), p.OnBehalfOf); err != nil {
			return err
		}
	}
	var containers []uuid.UUID
	for _, it := range items {
		if it.ContainerID != nil {
			containers = append(containers, *it.ContainerID)
		}
	}
	type creator struct{ by, behalf string }
	created := map[uuid.UUID]creator{}
	if len(containers) > 0 {
		rows, err := k.pool.Query(ctx,
			`select id, created_by, coalesce(created_on_behalf_of, '') from items where id = any($1) and namespace_id = $2`,
			containers, nsOf(p))
		if err != nil {
			return fmt.Errorf("read the containers' creators: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var id uuid.UUID
			var c creator
			if err := rows.Scan(&id, &c.by, &c.behalf); err != nil {
				return err
			}
			created[id] = c
		}
		if err := rows.Err(); err != nil {
			return err
		}
	}
	for i := range items {
		it := &items[i]
		ty, err := k.loadType(ctx, k.pool, it.TypeID, it.TypeVersion)
		if err != nil {
			return err
		}
		facts := policy.ItemFacts{Fields: it.Fields, CreatedBy: it.CreatedBy, CreatedOnBehalfOf: it.CreatedOnBehalfOf}
		if it.ContainerID != nil {
			c := created[*it.ContainerID]
			facts.ContainerCreatedBy, facts.ContainerCreatedOnBehalfOf = c.by, c.behalf
		}
		it.Moves = policy.Moves(ty, it.State, who, facts)
		it.Withheld = policy.Withheld(ty, it.State, who, facts)
	}
	return nil
}
