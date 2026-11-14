package itemservice

import (
	"context"
	"fmt"
	"strings"
	"time"

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
// see. If MinSeq is set it first waits, up to Wait, for the commit counter to
// reach it, and returns ErrNotCaughtUp if it does not.
//
// Today search reads the same Postgres the writes go to, so the wait ends at
// once. The contract is here so it still holds when search has its own index.
func (k *Service) Search(ctx context.Context, req SearchRequest) (SearchResult, error) {
	if req.Principal.Actor == "" || strings.TrimSpace(req.Query) == "" {
		return SearchResult{}, fmt.Errorf("%w: actor and query are required", ErrInvalid)
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

	// Read on every request, never cached: a narrowed Item or a removed
	// reader takes effect on the very next search.
	visible, err := policy.VisibleScopes(ctx, k.pool, defaultNamespace, req.Principal)
	if err != nil {
		return SearchResult{}, err
	}

	// The scope filter is part of the query, so Postgres applies it before
	// the limit and an Item the principal cannot see never leaves the database.
	// UUIDv7 IDs sort by creation time, so "id desc" is newest first.
	rows, err := k.pool.Query(ctx,
		`select id, type_id, type_version, container_id, scope_id, state, fields, item_seq
		 from items
		 where namespace_id = $1
		   and scope_id = any($2)
		   and search @@ plainto_tsquery('simple', $3)
		 order by id desc
		 limit $4`,
		defaultNamespace, visible, req.Query, limit)
	if err != nil {
		return SearchResult{}, fmt.Errorf("search: %w", err)
	}
	found, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (item.Item, error) {
		var it item.Item
		err := row.Scan(&it.ID, &it.TypeID, &it.TypeVersion, &it.ContainerID, &it.ScopeID,
			&it.State, &it.Fields, &it.ItemSeq)
		return it, err
	})
	if err != nil {
		return SearchResult{}, fmt.Errorf("search: %w", err)
	}

	// Every outbound path goes through Project. The query has already
	// filtered by scope, so this drops nothing today; it is where field-level
	// redaction will happen.
	res := SearchResult{Items: make([]item.Item, 0, len(found))}
	for _, it := range found {
		if projected, ok := policy.Project(req.Principal, visible, it); ok {
			res.Items = append(res.Items, projected)
		}
	}
	return res, nil
}

// waitForCommit returns once the commit counter has reached minSeq. It gives
// up with ErrNotCaughtUp after wait, or with the context's error if the
// caller goes away first.
func (k *Service) waitForCommit(ctx context.Context, minSeq int64, wait time.Duration) error {
	if minSeq == 0 {
		return nil
	}
	if wait == 0 {
		wait = defaultSearchWait
	}
	deadline := time.Now().Add(wait)
	for {
		// The counter row is updated inside each write's transaction, so
		// this read sees a number only once that write has committed.
		var last int64
		if err := k.pool.QueryRow(ctx, `select last_seq from commit_counter`).Scan(&last); err != nil {
			return fmt.Errorf("read commit counter: %w", err)
		}
		if last >= minSeq {
			return nil
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("%w: commit_seq is %d, wanted %d", ErrNotCaughtUp, last, minSeq)
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
