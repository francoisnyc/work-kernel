// Command worker runs Item workflows and their activities. It is separate
// from the relay because the two scale differently: add workers for more
// workflow load, while the relay stays a single instance.
package main

import (
	"context"
	"errors"
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

	// The hooks' connectors: in the spike, base URLs of the CRM, ERP and HR
	// directory (stubs from internal/connstub in tests and the bench).
	opts := workflow.WorkerOptions{
		Connectors: hooks.NewConnectors(env("CRM_URL", "http://localhost:8081"), env("ERP_URL", "http://localhost:8082")).
			WithHR(env("HR_URL", "http://localhost:8083")),
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

// config is what the worker's flags say (Step 2, part 3).
type config struct {
	// DevDay is the length of a day for the durations Types declare (state
	// and acting timers), zero for real time. Dev only.
	DevDay time.Duration
	// DevTypes are the Types the scale applies to; required with it.
	DevTypes []string
}

// The dev clock's flags. --dev-time-scale takes the length a day lasts
// (2s: a day of a Type's timers passes in two seconds). It is refused
// unless the second flag is given too, and never defaulted.
const (
	flagTimeScale = "dev-time-scale"
	flagDevClock  = "dev-clock-i-know-this-is-not-production"
	// flagTimeScaleTypes lists the Types the scale applies to (comma
	// separated); every other Type keeps real time.
	flagTimeScaleTypes = "dev-time-scale-types"
)

// checkDevTypes refuses a scale naming a Type that is not loaded.
//
// Phase B stub: not implemented yet.
func checkDevTypes(c config, loaded []string) error {
	return nil
}

// parseConfig reads the worker's flags.
//
// Phase B stub: not implemented yet.
func parseConfig(args []string) (config, error) {
	if len(args) > 0 {
		return config{}, errors.New("worker: flags are not implemented")
	}
	return config{}, nil
}

// clockWarning is what the worker logs on every start that runs on a scaled
// clock ("" on real time).
//
// Phase B stub: not implemented yet.
func clockWarning(c config) string {
	return ""
}
