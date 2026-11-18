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
	Limit int // the page size: zero means 50; more than 500 is read as 500
	// Filter narrows the search; a filter alone (no words) is a search.
	Filter Filter
	// PageToken is the previous page's NextPageToken, sent with the same
	// request; with any other request, caller or namespace it is ErrInvalid.
	PageToken string
	// OrderBy sorts by a declared field instead of newest first; nil keeps
	// newest first.
	OrderBy *OrderBy
}

// OrderBy sorts Search and List by one declared field: ascending unless
// Desc, Items without a value last in both directions, ties by Item ID
// ascending. The field must be a string, int, date or enum, with one type
// across the selected Types (or across every registered Type that declares
// it when none is selected); else an InvalidError on order_by. Strings and
// enum names compare byte by byte, never by locale. Items whose Type does
// not declare the field sort as unset.
type OrderBy struct {
	Field string
	Desc  bool
}

// SearchResult holds the matching Items, newest first, as the principal may
// see them.
type SearchResult struct {
	Items         []item.Item
	NextPageToken string // empty on the last page
}

// Search returns the Items that match the query and that the principal can
// see. If MinSeq is set it first waits, up to Wait, until that write and
// every older transaction are visible (see waitForCommit), and returns
// ErrNotCaughtUp if they are not.
//
// Today search reads the same Postgres the writes go to, so the wait ends at
// once. The contract is here so it still holds when search has its own index.
func (k *Service) Search(ctx context.Context, req SearchRequest) (SearchResult, error) {
	if req.Principal.Actor == "" || (strings.TrimSpace(req.Query) == "" && req.Filter.empty()) {
		return SearchResult{}, fmt.Errorf("%w: actor and words or a filter are required", ErrInvalid)
	}
	if err := checkPublicPrincipal(req.Principal); err != nil {
		return SearchResult{}, err
	}
	if err := checkSession(ctx, k.pool, req.Principal); err != nil {
		return SearchResult{}, err
	}
	if req.MinSeq < 0 || req.Wait < 0 {
		return SearchResult{}, fmt.Errorf("%w: min_seq and wait cannot be negative", ErrInvalid)
	}
	limit, err := pageSize(req.Limit)
	if err != nil {
		return SearchResult{}, err
	}
	filter, err := k.resolveFilter(ctx, req.Principal, req.Filter)
	if err != nil {
		return SearchResult{}, err
	}
	key := requestKey("search", req.Principal, struct {
		Query  string
		Filter Filter
	}{strings.TrimSpace(req.Query), req.Filter})
	after, err := decodeToken(req.PageToken, key)
	if err != nil {
		return SearchResult{}, err
	}
	if err := k.waitForCommit(ctx, req.MinSeq, req.Wait); err != nil {
		return SearchResult{}, err
	}
	found, next, err := k.page(ctx, itemQuery{
		p: req.Principal, words: strings.TrimSpace(req.Query), filter: filter, limit: limit, after: after, key: key,
	})
	if err != nil {
		return SearchResult{}, err
	}
	// Every outbound path goes through Project. The query has already
	// checked each Item's scope, so this drops nothing today; it is where
	// field-level redaction will happen. Any count or total is taken from
	// what Project returns, never before it. An agent's read of every
	// result is recorded for the write rule.
	items := make([]item.Item, 0, len(found))
	var read []*uuid.UUID
	for _, h := range found {
		if projected, ok := policy.Project(req.Principal, []uuid.UUID{*h.it.ScopeID}, h.it); ok {
			items = append(items, projected)
			read = append(read, h.policyScope)
		}
	}
	if err := recordReads(ctx, k.pool, req.Principal, read); err != nil {
		return SearchResult{}, err
	}
	return SearchResult{Items: items, NextPageToken: next}, nil
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
