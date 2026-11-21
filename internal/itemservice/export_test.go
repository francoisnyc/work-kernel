package itemservice

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// SetTestHook sets the write path's test hook (see testHook) and returns a
// function that removes it. Only tests compile this file, so production
// code has no way to set the hook: it stays nil.
func SetTestHook(f func(ctx context.Context, tx pgx.Tx, point string) error) (restore func()) {
	testHook = f
	return func() { testHook = nil }
}

// Retryable reports whether write runs a transaction again after err.
func Retryable(err error) bool { return retryable(err) }

// ChangesContent is the etag rule (content.go).
func ChangesContent(ty *typedef.Type, op string, payload map[string]any) (bool, error) {
	return changesContent(ty, op, payload)
}

// RequestHash is the idempotency hash of a request.
func RequestHash(op string, req any) (string, error) { return requestHash(op, req) }

// ExplainSearch and ExplainList return the plan (EXPLAIN, without ANALYZE)
// of the very page query Search or List runs for the request: the query is
// built by searchQuery or listQuery and pageSQL, which the reads call too
// (Jira part 2, step 4a: a customer's reads never scan every Item).
func ExplainSearch(k *Service, ctx context.Context, req SearchRequest) (string, error) {
	q, err := k.searchQuery(ctx, req)
	if err != nil {
		return "", err
	}
	return k.explain(ctx, q)
}

func ExplainList(k *Service, ctx context.Context, req ListRequest) (string, error) {
	q, err := k.listQuery(ctx, req)
	if err != nil {
		return "", err
	}
	return k.explain(ctx, q)
}

func (k *Service) explain(ctx context.Context, q itemQuery) (string, error) {
	sql, args, err := k.pageSQL(ctx, q)
	if err != nil {
		return "", err
	}
	rows, err := k.pool.Query(ctx, "explain "+sql, args)
	if err != nil {
		return "", err
	}
	lines, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return "", err
	}
	return strings.Join(lines, "\n"), nil
}

// DeadlockRetries is how many write transactions Postgres has aborted as a
// deadlock and write has run again, since the process started.
func DeadlockRetries() int64 { return deadlockRetries.Load() }
