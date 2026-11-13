// Command relay delivers outbox rows to Temporal. Run exactly one.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.temporal.io/sdk/client"

	"github.com/francoisnyc/work-kernel/internal/relay"
)

func main() {
	if err := run(); err != nil {
		slog.Error("relay stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	// The context is canceled on SIGINT or SIGTERM; the relay's loop sees
	// that and returns, which is Go's usual shape for a graceful shutdown.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := pgxpool.New(ctx, env("DATABASE_URL", "postgres://kernel:kernel@localhost:5432/kernel?sslmode=disable"))
	if err != nil {
		return err
	}
	defer pool.Close()

	c, err := client.Dial(client.Options{HostPort: env("TEMPORAL_ADDRESS", "localhost:7233")})
	if err != nil {
		return err
	}
	defer c.Close()

	workflows := relay.NewTemporalWorkflows(c, env("TASK_QUEUE", "items"))
	return relay.New(pool, workflows, relay.Options{}).Run(ctx)
}

func env(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
