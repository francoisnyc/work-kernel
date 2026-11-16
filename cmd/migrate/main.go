// Command migrate applies the goose migrations compiled into it (the files
// in migrations/, embedded) to DATABASE_URL, then exits. The compose stack
// runs it once; the other services wait for it to finish successfully.
package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Getenv, os.Stderr); err != nil {
		slog.Error("migrate failed", "error", err)
		os.Exit(1)
	}
}

// errNotBuilt marks what Step 0, part 3 has not built yet.
var errNotBuilt = errors.New("migrate: not built yet (Step 0, part 3)")

// run applies every pending migration and says what it applied on stderr.
func run(ctx context.Context, getenv func(string) string, stderr io.Writer) error {
	return errNotBuilt
}
