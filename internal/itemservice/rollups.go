package itemservice

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/policy"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// Rollups (Jira proof of concept, part 1). A Type declares values computed
// when one of its Items is read: counts of the Items linked under a name it
// can read, by state category or by state (an epic's progress). They are
// never stored, so they cannot fall behind, and never searched.
//
// A rollup is a read, unlike a guard: it counts only the Items the reader
// can see, because counting a hidden one would reveal that it exists. For
// an agent session that is the Items inside the session's scopes that the
// agent and its user can both see; and counting Items of a narrowed scope
// is a read of that scope, recorded for the write rule.

// rollups computes every rollup the Item's Type version declares, as the
// principal sees the linked Items. It returns nil for a Type that declares
// none, and the narrowing scopes of the Items it counted (to record an
// agent's read).
func (k *Service) rollups(ctx context.Context, p item.Principal, ty *typedef.Type, id uuid.UUID) (map[string]item.Rollup,
	[]*uuid.UUID, error) {
	if len(ty.Rollups) == 0 {
		return nil, nil, nil
	}
	c, err := loadCaps(ctx, k.pool, p)
	if err != nil {
		return nil, nil, err
	}
	// One count per link name, shared by the rollups over it.
	type group struct {
		state, category string
		n               int64
	}
	counted := map[string][]group{}
	var read []*uuid.UUID
	for _, r := range ty.Rollups {
		if _, done := counted[r.Over]; done {
			continue
		}
		src, ok := ty.LinkSource(r.Over)
		if !ok {
			return nil, nil, fmt.Errorf("type %s v%d: rollup %s counts over %q, which it cannot read", ty.ID, ty.Version, r.ID, r.Over)
		}
		args := policy.VisibleArgs(p)
		args["id"], args["ns"], args["link"] = id, nsOf(p), src.Link
		cte, inSession := sessionFilter(c, "o", args)
		with := ""
		if cte != "" {
			with = "with recursive " + cte + "\n"
		}
		// The linked Items: the targets of this Item's links, or the
		// sources of the links that point to it from the named Types.
		join, where := "o.id = l.target_id", "l.source_id = @id and l.link_type = @link"
		if !src.Outgoing {
			args["from"] = src.From
			join, where = "o.id = l.source_id", "l.target_id = @id and l.link_type = @link and l.source_type_id = any(@from::text[])"
		}
		// Visibility is part of the count, in SQL: nothing the principal
		// cannot see is counted, and nothing is fetched to be dropped.
		rows, err := k.pool.Query(ctx, with+
			`select o.type_id, o.type_version, o.state, s.policy_scope_id, count(*)
			 from links l join items o on `+join+` join scopes s on s.id = o.scope_id
			 where `+where+` and o.namespace_id = @ns and `+policy.VisibleCondition+inSession+`
			 group by o.type_id, o.type_version, o.state, s.policy_scope_id`, args)
		if err != nil {
			return nil, nil, fmt.Errorf("count linked items: %w", err)
		}
		type row struct {
			typeID  string
			version int
			state   string
			scope   *uuid.UUID
			n       int64
		}
		got, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (row, error) {
			var x row
			err := r.Scan(&x.typeID, &x.version, &x.state, &x.scope, &x.n)
			return x, err
		})
		if err != nil {
			return nil, nil, fmt.Errorf("count linked items: %w", err)
		}
		groups := []group{}
		for _, x := range got {
			// A category is read in each linked Item's own Type version.
			linked, err := k.loadType(ctx, k.pool, x.typeID, x.version)
			if err != nil {
				return nil, nil, err
			}
			st, _ := linked.State(x.state)
			groups = append(groups, group{state: x.state, category: st.Category, n: x.n})
			read = append(read, x.scope)
		}
		counted[r.Over] = groups
	}

	out := make(map[string]item.Rollup, len(ty.Rollups))
	for _, r := range ty.Rollups {
		counts := map[string]int64{}
		for _, g := range counted[r.Over] {
			key := g.category
			if r.CountBy == typedef.CountByState {
				key = g.state
			}
			counts[key] += g.n
		}
		out[r.ID] = item.Rollup{Counts: counts}
	}
	return out, read, nil
}
