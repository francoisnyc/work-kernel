// Command worker runs Item workflows and their activities. It is separate
// from the relay because the two scale differently: add workers for more
// workflow load, while the relay stays a single instance.
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"

	"github.com/francoisnyc/work-kernel/internal/kernel"
	"github.com/francoisnyc/work-kernel/internal/workflow"
)

func main() {
	if err := run(); err != nil {
		slog.Error("worker stopped", "error", err)
		os.Exit(1)
	}
}

// run holds the real body so that deferred cleanup runs before the process
// exits; os.Exit in main would skip it.
func run() error {
	ctx := context.Background()
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

	w := workflow.NewWorker(c, env("TASK_QUEUE", "items"), kernel.New(pool), workflow.WorkerOptions{})
	// Run blocks until SIGINT or SIGTERM, then stops the worker cleanly.
	return w.Run(worker.InterruptCh())
}

func env(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
