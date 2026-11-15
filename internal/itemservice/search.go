package itemservice

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/policy"
)

const (
	// defaultSearchWait bounds how long Search waits for min_seq.
	defaultSearchWait = 2 * time.Second
	// catchUpPoll is how often Search looks at the commit counter while it waits.
	catchUpPoll = 20 * time.Millisecond
	// defaultSearchLimit applies when a request names no limit.
	defaultSearchLimit = 50
)

// SearchRequest asks for the Items whose string field values match Query.
type SearchRequest struct {
	Principal item.Principal
	Query     string // words; an Item must contain all of them
	// MinSeq is optional: "do not answer from a state older than this
	// commit_seq". Pass the commit_seq a write returned to read that write.
	MinSeq int64
	// Wait bounds how long to wait for MinSeq; zero means the default (2 s).
	// time.Duration is a count of nanoseconds with units in its type, so
	// callers write 500*time.Millisecond.
	Wait  time.Duration
	Limit int // zero means the default (50)
}

// SearchResult holds the matching Items, newest first, as the principal may
// see them.
type SearchResult struct {
	Items []item.Item
}

// Search returns the Items that match the query and that the principal can
// see. If MinSeq is set it first waits, up to Wait, until that write and
// every older transaction are visible (see waitForCommit), and returns
// ErrNotCaughtUp if they are not.
//
// Today search reads the same Postgres the writes go to, so the wait ends at
// once. The contract is here so it still holds when search has its own index.
func (k *Service) Search(ctx context.Context, req SearchRequest) (SearchResult, error) {
	if req.Principal.Actor == "" || strings.TrimSpace(req.Query) == "" {
		return SearchResult{}, fmt.Errorf("%w: actor and query are required", ErrInvalid)
	}
	if err := checkPublicPrincipal(req.Principal); err != nil {
		return SearchResult{}, err
	}
	if err := checkSession(ctx, k.pool, req.Principal); err != nil {
		return SearchResult{}, err
	}
	if req.MinSeq < 0 || req.Wait < 0 || req.Limit < 0 {
		return SearchResult{}, fmt.Errorf("%w: min_seq, wait and limit cannot be negative", ErrInvalid)
	}
	limit := req.Limit
	if limit == 0 {
		limit = defaultSearchLimit
	}
	if err := k.waitForCommit(ctx, req.MinSeq, req.Wait); err != nil {
		return SearchResult{}, err
	}

	// The text match runs first, in a MATERIALIZED step the planner cannot
	// fold into the rest. Without it, Postgres walks Items newest-first and
	// throws away non-matches, which grows with the size of the scope
	// (Step A, EXPLAIN). The visibility filter is part of the query, before
	// the limit, so an Item the principal cannot see never leaves the database.
	// UUIDv7 IDs sort by creation time, so "id desc" is newest first.
	args := policy.VisibleArgs(req.Principal)
	args["ns"], args["q"], args["limit"] = nsOf(req.Principal), req.Query, limit
	rows, err := k.pool.Query(ctx,
		`with matches as materialized (
		     select id, type_id, type_version, container_id, scope_id, state, fields, item_seq
		     from items
		     where namespace_id = @ns and search @@ plainto_tsquery('simple', @q)
		 )
		 select m.id, m.type_id, m.type_version, m.container_id, m.scope_id, m.state, m.fields, m.item_seq,
		        s.policy_scope_id
		 from matches m join scopes s on s.id = m.scope_id
		 where `+policy.VisibleCondition+`
		 order by m.id desc
		 limit @limit`, args)
	if err != nil {
		return SearchResult{}, fmt.Errorf("search: %w", err)
	}
	// Each row carries the Item and the narrowing scope it is read through.
	type hit struct {
		it          item.Item
		policyScope *uuid.UUID
	}
	found, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (hit, error) {
		var h hit
		err := row.Scan(&h.it.ID, &h.it.TypeID, &h.it.TypeVersion, &h.it.ContainerID, &h.it.ScopeID,
			&h.it.State, &h.it.Fields, &h.it.ItemSeq, &h.policyScope)
		return h, err
	})
	if err != nil {
		return SearchResult{}, fmt.Errorf("search: %w", err)
	}

	// Every outbound path goes through Project. The query has already
	// checked each Item's scope, so this drops nothing today; it is where
	// field-level redaction will happen. Any count or total is taken from
	// what Project returns, never before it.
	res := SearchResult{Items: make([]item.Item, 0, len(found))}
	var read []*uuid.UUID
	for _, h := range found {
		if projected, ok := policy.Project(req.Principal, []uuid.UUID{*h.it.ScopeID}, h.it); ok {
			res.Items = append(res.Items, projected)
			read = append(read, h.policyScope)
		}
	}
	// An agent's read of every result is recorded for the write rule.
	if err := recordReads(ctx, k.pool, req.Principal, read); err != nil {
		return SearchResult{}, err
	}
	return res, nil
}

// waitForCommit returns once the write named by minSeq is visible and every
// transaction older than it has finished: the write's transaction ID is below
// the snapshot xmin, the oldest transaction still running. A commit_seq whose
// event cannot be found yet (still committing, or rolled back) is not reached.
// It gives up with ErrNotCaughtUp after wait, or with the context's error if
// the caller goes away first.
//
// It does not promise every lower commit_seq: a younger transaction may hold
// a lower number and still be running. commit_seq names a write; it is not a
// cursor (docs/design.md, Write path).
func (k *Service) waitForCommit(ctx context.Context, minSeq int64, wait time.Duration) error {
	if minSeq == 0 {
		return nil
	}
	if wait == 0 {
		wait = defaultSearchWait
	}
	deadline := time.Now().Add(wait)
	for {
		var reached bool
		err := k.pool.QueryRow(ctx,
			`select xid < pg_snapshot_xmin(pg_current_snapshot()) from item_events where commit_seq = $1`,
			minSeq).Scan(&reached)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("read the watermark: %w", err)
		}
		if reached {
			return nil
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("%w: write %d and the transactions before it are not all visible yet", ErrNotCaughtUp, minSeq)
		}
		// select waits on several channels at once and takes whichever is
		// ready first: here, the caller giving up or the next poll.
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(catchUpPoll):
		}
	}
}
