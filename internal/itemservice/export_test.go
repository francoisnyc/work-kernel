package itemservice

import (
	"context"

	"github.com/jackc/pgx/v5"
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
