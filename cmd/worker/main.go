// Command worker runs Item workflows and their activities. It is separate
// from the relay because the two scale differently: add workers for more
// workflow load, while the relay stays a single instance.
package main

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"

	"github.com/francoisnyc/work-kernel/internal/hooks"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
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

	// The hooks' connectors: in the spike, base URLs of the CRM and ERP
	// (stubs from internal/connstub in tests and the bench).
	opts := workflow.WorkerOptions{
		Connectors: hooks.NewConnectors(env("CRM_URL", "http://localhost:8081"), env("ERP_URL", "http://localhost:8082")),
	}
	if b := os.Getenv("HOOK_BUDGET"); b != "" {
		// A Go duration such as 15m or 30s.
		if opts.HookBudget, err = time.ParseDuration(b); err != nil {
			return err
		}
	}
	if a := os.Getenv("HOOK_ATTEMPT_TIMEOUT"); a != "" {
		// Bench only: one hook attempt's StartToClose, to force timeouts.
		if opts.HookAttemptTimeout, err = time.ParseDuration(a); err != nil {
			return err
		}
	}
	w := workflow.NewWorker(c, env("TASK_QUEUE", "items"), itemservice.New(pool), opts)
	// Run blocks until SIGINT or SIGTERM, then stops the worker cleanly.
	return w.Run(worker.InterruptCh())
}

func env(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
