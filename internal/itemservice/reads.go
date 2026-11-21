package itemservice

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/policy"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// Paging (docs/design.md, "Reads"): a page size, an opaque token, no
// offsets and no totals. Items page newest first on (created_at, id), a
// key that new Items never come between, so a page never repeats or skips
// one; or, with order_by, on (the field's value, id), Items without a
// value last. Events page by item_seq.

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
	// Value is the last Item's sort value under order_by, as text; nil
	// when it had none (the page ended among the Items without a value).
	Value *string `json:"v,omitempty"`
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
	order  *sqlOrder // nil: newest first
	limit  int
	after  *pageToken
	key    string // the token fingerprint
}

// sqlOrder is an OrderBy checked against the registered Types: the SQL
// that reads the sort value from the Items alias m (NULL when an Item has
// none, or its Type does not declare the field), and what a token's value
// is cast to for comparison with it.
type sqlOrder struct {
	field   string // the field ID, passed as the argument @o_field
	expr    string
	cast    string // "::numeric", or "::text" (compared under COLLATE "C")
	numeric bool
	desc    bool
	vis     pgx.NamedArgs // a customer's per-row rule's arguments (rowListing); nil for staff
}

// resolveOrder checks an OrderBy against the Types in play: the selected
// Types, or every registered Type when none is selected. The field must be
// declared by at least one of them, with one type across all that declare
// it, and that type must have an order: string, int, date, datetime or enum. A
// malformed order is an *InvalidError on order_by.
func (k *Service) resolveOrder(ctx context.Context, p item.Principal, typeIDs []string, o *OrderBy) (*sqlOrder, error) {
	if o == nil {
		return nil, nil
	}
	bad := func(reason string) error { return &InvalidError{Path: "order_by", Reason: reason} }
	if o.Field == "" {
		return nil, bad("order_by needs a field")
	}
	all, err := k.typesFor(ctx, p)
	if err != nil {
		return nil, err
	}
	types, _, err := declarations(inPlay(all, typeIDs), o.Field)
	if err != nil {
		return nil, bad(err.Error())
	}
	if len(types) > 1 {
		return nil, bad("the field has different types across the Types that declare it")
	}
	if multiple, mixed := multipleAcross(inPlay(all, typeIDs), o.Field); multiple || mixed {
		return nil, bad("a multi-value field is a set: it has no order")
	}
	out := &sqlOrder{field: o.Field, desc: o.Desc, expr: fieldRead(types[0], "o_field"), cast: "::text"}
	// A customer: a row whose own version does not show them the field
	// sorts as if it had no value, after the rest.
	if policy.CustomerOf(p) != "" {
		out.vis = pgx.NamedArgs{}
		out.expr = "(case when " + rowListing(all, o.Field, "o_vis", out.vis) + " then " + out.expr + " end)"
	}
	switch types[0] {
	case typedef.Int:
		out.cast, out.numeric = "::numeric", true
	case typedef.String, typedef.Date, typedef.DateTime, typedef.Enum:
		// A datetime's stored form is fixed-width UTC: as text, in byte
		// order, it sorts by the instant.
	default:
		return nil, bad("only string, int, date, datetime and enum fields have an order")
	}
	return out, nil
}

// after returns the keyset condition for the page that follows the token:
// the Items that sort after (value, id) in this order. Items without a
// value come last in both directions, and ties are broken by ID ascending
// in both, so only the comparison of values turns around.
func (o *sqlOrder) after(t *pageToken, args pgx.NamedArgs) (string, error) {
	args["after_id"] = t.ID
	if t.Value == nil {
		// The last page ended among the Items without a value.
		return "(" + o.expr + " is null and m.id > @after_id)", nil
	}
	if o.numeric {
		// The token is unsigned: a value that is no number must be refused
		// here, not fail in the database.
		if _, ok := new(big.Rat).SetString(*t.Value); !ok {
			return "", &InvalidError{Path: "page_token", Reason: "not a page token of this request"}
		}
	}
	args["after_value"] = *t.Value
	beyond := ">"
	if o.desc {
		beyond = "<"
	}
	value := "@after_value" + o.cast
	return fmt.Sprintf("(%[1]s %[2]s %[3]s or (%[1]s = %[3]s and m.id > @after_id) or %[1]s is null)",
		o.expr, beyond, value), nil
}

// hit is one Item a page found, with the narrowing scope it is read
// through (nil when open), which an agent's read records.
type hit struct {
	it          item.Item
	policyScope *uuid.UUID
	sortValue   *string // under order_by: the Item's sort value as text, nil when it has none
}

// page runs the query and returns the Items found and the next token.
// Visibility and an agent session's scopes are part of the SQL, before the
// limit, so nothing the principal cannot see leaves the database. The
// caller projects each Item (policy.Project) and records an agent's reads
// (projectHits).
func (k *Service) page(ctx context.Context, q itemQuery) ([]hit, string, error) {
	sql, args, err := k.pageSQL(ctx, q)
	if err != nil {
		return nil, "", err
	}
	rows, err := k.pool.Query(ctx, sql, args)
	if err != nil {
		return nil, "", fmt.Errorf("read items: %w", err)
	}
	found, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (hit, error) {
		var h hit
		err := row.Scan(&h.it.ID, &h.it.TypeID, &h.it.TypeVersion, &h.it.ContainerID, &h.it.ScopeID,
			&h.it.State, &h.it.Fields, &h.it.ItemSeq, &h.it.ContentSeq, &h.it.CreatedAt, &h.it.CreatedBy, &h.it.CreatedOnBehalfOf,
			&h.policyScope, &h.sortValue)
		return h, err
	})
	if err != nil {
		return nil, "", fmt.Errorf("read items: %w", err)
	}
	next := ""
	if len(found) > q.limit {
		found = found[:q.limit]
		last := found[len(found)-1]
		token := pageToken{Request: q.key, ID: last.it.ID}
		if q.order != nil {
			token.Value = last.sortValue
		} else {
			token.CreatedAt = last.it.CreatedAt
		}
		next = encodeToken(token)
	}

	return found, next, nil
}

// pageSQL builds the one query a page of Search or List runs: its text and
// its named arguments. page runs it; the plan test explains this very text.
//
// A customer's page (Jira part 2, step 4a) reads from the relation
// policy.CustomerRelation (mine) instead of items: their own requests from
// the index on the effective creator, what those share, and the portals.
// Their words match the fields they see only (policy.CustomerWords).
func (k *Service) pageSQL(ctx context.Context, q itemQuery) (string, pgx.NamedArgs, error) {
	c, err := loadCaps(ctx, k.pool, q.p)
	if err != nil {
		return "", nil, err
	}
	args, err := k.seeArgs(ctx, q.p)
	if err != nil {
		return "", nil, err
	}
	customer := policy.CustomerOf(q.p) != ""
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
	from, base := "items m", "items"
	if customer {
		ctes = append(ctes, policy.CustomerRelation)
		from, base = "mine m", "mine"
	}
	if q.words != "" {
		args["q"] = q.words
		match := "search"
		if customer {
			match = policy.CustomerWords(base)
		}
		ctes = append(ctes, `matches as materialized (
		     select * from `+base+` where namespace_id = @ns and `+match+` @@ plainto_tsquery('simple', @q)
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
	// The order: newest first, or by the field's value with the Items that
	// have none last, ties by ID. sortValue is the value as text, which the
	// next page's token carries.
	orderBy, sortValue := "m.created_at desc, m.id desc", "null::text"
	if q.order != nil {
		args["o_field"] = q.order.field
		for name, v := range q.order.vis {
			args[name] = v
		}
		direction := "asc"
		if q.order.desc {
			direction = "desc"
		}
		orderBy = q.order.expr + " " + direction + " nulls last, m.id asc"
		sortValue = "(" + q.order.expr + ")::text"
	}
	switch {
	case q.after == nil:
	case q.order != nil:
		cond, err := q.order.after(q.after, args)
		if err != nil {
			return "", nil, err
		}
		conds = append(conds, cond)
	default:
		args["after_at"], args["after_id"] = q.after.CreatedAt, q.after.ID
		conds = append(conds, "(m.created_at, m.id) < (@after_at, @after_id)")
	}
	conds = append(conds, policy.VisibleCondition)

	with := ""
	if len(ctes) > 0 {
		with = "with recursive " + strings.Join(ctes, ",\n") + "\n"
	}
	return with +
		`select m.id, m.type_id, m.type_version, m.container_id, m.scope_id, m.state, m.fields, m.item_seq, m.content_seq,
		        m.created_at, m.created_by, coalesce(m.created_on_behalf_of, ''), s.policy_scope_id,
		        ` + sortValue + `
		 from ` + from + ` join scopes s on s.id = m.scope_id
		 where ` + strings.Join(conds, "\n and ") + `
		 order by ` + orderBy + `
		 limit @limit`, args, nil
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
