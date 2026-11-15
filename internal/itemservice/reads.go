package itemservice

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/policy"
)

// Paging (docs/design.md, "Reads"): a page size, an opaque token, no
// offsets and no totals. Items page newest first on (created_at, id), a
// key that new Items never come between, so a page never repeats or skips
// one. Events page by item_seq.

const (
	defaultPageSize = 50
	maxPageSize     = 500
)

// pageSize applies the default and the cap; a negative size is malformed.
func pageSize(n int) (int, error) {
	switch {
	case n < 0:
		return 0, &InvalidError{Path: "page_size", Reason: "a page size cannot be negative"}
	case n == 0:
		return defaultPageSize, nil
	case n > maxPageSize:
		return maxPageSize, nil
	}
	return n, nil
}

// pageToken is what a token carries: where the last page ended, and a
// fingerprint of the request, caller and namespace it belongs to. It is
// base64 JSON, unsigned: every page is filtered by visibility anyway, so a
// forged token reveals nothing.
type pageToken struct {
	Request   string    `json:"r"`
	CreatedAt time.Time `json:"t,omitzero"`
	ID        uuid.UUID `json:"i,omitzero"`
	Seq       int64     `json:"s,omitzero"`
}

// requestKey fingerprints what a token is bound to: the kind of read, its
// parameters (page size and token aside), the caller and the namespace.
func requestKey(read string, p item.Principal, params any) string {
	b, _ := json.Marshal(struct {
		Read      string
		Actor     string
		OnBehalf  string
		Session   string
		Namespace string
		Params    any
	}{read, p.Actor, p.OnBehalfOf, p.AgentSession, nsOf(p), params})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:16])
}

func encodeToken(t pageToken) string {
	b, _ := json.Marshal(t)
	return base64.RawURLEncoding.EncodeToString(b)
}

// decodeToken reads a token and checks it belongs to this request; an empty
// token is the first page (nil).
func decodeToken(token, key string) (*pageToken, error) {
	if token == "" {
		return nil, nil
	}
	bad := &InvalidError{Path: "page_token", Reason: "not a page token of this request"}
	b, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return nil, bad
	}
	var t pageToken
	if err := json.Unmarshal(b, &t); err != nil || t.Request != key {
		return nil, bad
	}
	return &t, nil
}

// itemQuery is one page of Items read for a principal: Search and List.
type itemQuery struct {
	p      item.Principal
	words  string     // optional full-text words
	parent *uuid.UUID // List: the container; with top, the top level
	list   bool       // List: only the container's (or the top level's) direct children
	filter sqlFilter
	limit  int
	after  *pageToken
	key    string // the token fingerprint
}

// hit is one Item a page found, with the narrowing scope it is read
// through (nil when open), which an agent's read records.
type hit struct {
	it          item.Item
	policyScope *uuid.UUID
}

// page runs the query and returns the Items found and the next token.
// Visibility and an agent session's scopes are part of the SQL, before the
// limit, so nothing the principal cannot see leaves the database. The
// caller projects each Item (policy.Project) and records an agent's reads
// (projectHits).
func (k *Service) page(ctx context.Context, q itemQuery) ([]hit, string, error) {
	c, err := loadCaps(ctx, k.pool, q.p)
	if err != nil {
		return nil, "", err
	}
	args := policy.VisibleArgs(q.p)
	args["ns"], args["limit"] = nsOf(q.p), q.limit+1
	for name, v := range q.filter.args {
		args[name] = v
	}

	var ctes []string
	if cte, _ := sessionFilter(c, "m", args); cte != "" {
		ctes = append(ctes, cte)
	}
	// The text match runs first, in a MATERIALIZED step the planner cannot
	// fold into the rest. Without it, Postgres walks Items newest-first and
	// throws away non-matches, which grows with the size of the scope
	// (Step A, EXPLAIN).
	from := "items m"
	if q.words != "" {
		args["q"] = q.words
		ctes = append(ctes, `matches as materialized (
		     select * from items where namespace_id = @ns and search @@ plainto_tsquery('simple', @q)
		 )`)
		from = "matches m"
	}
	conds := []string{"m.namespace_id = @ns"}
	if q.list {
		if q.parent != nil {
			args["parent"] = *q.parent
			conds = append(conds, "m.container_id = @parent")
		} else {
			conds = append(conds, "m.container_id is null")
		}
	}
	conds = append(conds, q.filter.conds...)
	if _, cond := sessionFilter(c, "m", args); cond != "" {
		conds = append(conds, strings.TrimPrefix(cond, " and "))
	}
	if q.after != nil {
		args["after_at"], args["after_id"] = q.after.CreatedAt, q.after.ID
		conds = append(conds, "(m.created_at, m.id) < (@after_at, @after_id)")
	}
	conds = append(conds, policy.VisibleCondition)

	with := ""
	if len(ctes) > 0 {
		with = "with recursive " + strings.Join(ctes, ",\n") + "\n"
	}
	rows, err := k.pool.Query(ctx, with+
		`select m.id, m.type_id, m.type_version, m.container_id, m.scope_id, m.state, m.fields, m.item_seq,
		        m.created_at, m.created_by, coalesce(m.created_on_behalf_of, ''), s.policy_scope_id
		 from `+from+` join scopes s on s.id = m.scope_id
		 where `+strings.Join(conds, "\n and ")+`
		 order by m.created_at desc, m.id desc
		 limit @limit`, args)
	if err != nil {
		return nil, "", fmt.Errorf("read items: %w", err)
	}
	found, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (hit, error) {
		var h hit
		err := row.Scan(&h.it.ID, &h.it.TypeID, &h.it.TypeVersion, &h.it.ContainerID, &h.it.ScopeID,
			&h.it.State, &h.it.Fields, &h.it.ItemSeq, &h.it.CreatedAt, &h.it.CreatedBy, &h.it.CreatedOnBehalfOf,
			&h.policyScope)
		return h, err
	})
	if err != nil {
		return nil, "", fmt.Errorf("read items: %w", err)
	}
	next := ""
	if len(found) > q.limit {
		found = found[:q.limit]
		last := found[len(found)-1].it
		next = encodeToken(pageToken{Request: q.key, CreatedAt: last.CreatedAt, ID: last.ID})
	}

	return found, next, nil
}

// requireNamespace refuses a principal with no namespace on the reads that
// came with Step 0, part 2: in the kernel, empty means "default", which is
// how a forgotten path would leak across namespaces.
func requireNamespace(p item.Principal) error {
	if p.Namespace == "" {
		return fmt.Errorf("%w: the principal names no namespace", ErrInvalid)
	}
	return nil
}
