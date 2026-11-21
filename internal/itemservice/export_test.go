package itemservice

import (
	"context"

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
// of the page query that Search or List would run for the request, so a
// test can hold a customer's reads to an index (Jira part 2, step 4a).
func ExplainSearch(k *Service, ctx context.Context, req SearchRequest) (string, error) {
	return k.explainSearch(ctx, req)
}

func ExplainList(k *Service, ctx context.Context, req ListRequest) (string, error) {
	return k.explainList(ctx, req)
}
