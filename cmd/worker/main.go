// Command worker runs Item workflows and their activities. It is separate
// from the relay because the two scale differently: add workers for more
// workflow load, while the relay stays a single instance.
//
// Configuration is by environment variables. Its only flags are the dev
// clock, for the scenario (never in production):
//
//	worker --dev-time-scale 2s --dev-clock-i-know-this-is-not-production \
//	       --dev-time-scale-types vacation_request
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"slices"
	"strings"
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
	cfg, err := parseConfig(os.Args[1:])
	if err != nil {
		return err
	}
	pool, err := pgxpool.New(ctx, env("DATABASE_URL", "postgres://kernel:kernel@localhost:5432/kernel?sslmode=disable"))
	if err != nil {
		return err
	}
	defer pool.Close()
	k := itemservice.New(pool)
	if cfg.DevDay > 0 {
		// The Types must be loaded (by the item service) before a scale may
		// name them.
		types, err := k.ListTypes(ctx)
		if err != nil {
			return err
		}
		var loaded []string
		for _, t := range types {
			if !slices.Contains(loaded, t.ID) {
				loaded = append(loaded, t.ID)
			}
		}
		if err := checkDevTypes(cfg, loaded); err != nil {
			return err
		}
		slog.Warn(clockWarning(cfg), "day", cfg.DevDay.String(), "types", cfg.DevTypes)
	}

	c, err := client.Dial(client.Options{HostPort: env("TEMPORAL_ADDRESS", "localhost:7233")})
	if err != nil {
		return err
	}
	defer c.Close()

	// The hooks' connectors: in the spike, base URLs of the CRM, ERP, HR
	// directory and email service (stubs from internal/connstub).
	opts := workflow.WorkerOptions{
		Connectors: hooks.NewConnectors(env("CRM_URL", "http://localhost:8081"), env("ERP_URL", "http://localhost:8082")).
			WithHR(env("HR_URL", "http://localhost:8083")).WithEmail(env("EMAIL_URL", "http://localhost:8084")),
		DevDay: cfg.DevDay, DevTypes: cfg.DevTypes,
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
	w := workflow.NewWorker(c, env("TASK_QUEUE", "items"), k, opts)
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

// checkDevTypes refuses a scale naming a Type that is not loaded: a typo
// would otherwise leave that Type on real time without a word.
func checkDevTypes(c config, loaded []string) error {
	if c.DevDay == 0 {
		return nil
	}
	var unknown []string
	for _, name := range c.DevTypes {
		if !slices.Contains(loaded, name) {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) > 0 {
		return fmt.Errorf("--%s names Types that are not loaded: %s (loaded: %s)",
			flagTimeScaleTypes, strings.Join(unknown, ", "), strings.Join(loaded, ", "))
	}
	return nil
}

// parseConfig reads the worker's flags. The scale needs the dev flag and an
// explicit list of Types, each named once: never "all Types" by default.
func parseConfig(args []string) (config, error) {
	fs := flag.NewFlagSet("worker", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // the error is returned, and logged by run
	scale := fs.Duration(flagTimeScale, 0,
		"DEV ONLY: how long a day of the Types' declared timers lasts (needs --"+flagDevClock+" and --"+flagTimeScaleTypes+")")
	ack := fs.Bool(flagDevClock, false, "acknowledge that a scaled clock is not for production")
	types := fs.String(flagTimeScaleTypes, "", "DEV ONLY: the Types the scale applies to, comma separated")
	if err := fs.Parse(args); err != nil {
		return config{}, err
	}
	if fs.NArg() > 0 {
		return config{}, fmt.Errorf("unexpected arguments: %v", fs.Args())
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })

	if !set[flagTimeScale] {
		if set[flagTimeScaleTypes] {
			return config{}, fmt.Errorf("--%s without --%s", flagTimeScaleTypes, flagTimeScale)
		}
		return config{}, nil // real time; the acknowledgment alone changes nothing
	}
	if !*ack {
		return config{}, fmt.Errorf("--%s is for development only: it also needs --%s", flagTimeScale, flagDevClock)
	}
	if *scale <= 0 || *scale >= 24*time.Hour {
		return config{}, fmt.Errorf("--%s %v: want a positive duration shorter than a day", flagTimeScale, *scale)
	}
	if !set[flagTimeScaleTypes] {
		return config{}, fmt.Errorf("--%s needs --%s, the Types it applies to: it never applies to all", flagTimeScale, flagTimeScaleTypes)
	}
	var names []string
	for _, name := range strings.Split(*types, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			return config{}, fmt.Errorf("--%s %q: an empty Type name", flagTimeScaleTypes, *types)
		}
		if !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	return config{DevDay: *scale, DevTypes: names}, nil
}

// clockWarning is what the worker logs on every start that runs on a scaled
// clock ("" on real time).
func clockWarning(c config) string {
	if c.DevDay == 0 {
		return ""
	}
	return fmt.Sprintf("DEV CLOCK: this worker is NOT for production. A day of the timers declared by %s "+
		"lasts %v; their workflows record it. Every other Type, hook budgets and every other timeout keep real time.",
		strings.Join(c.DevTypes, ", "), c.DevDay)
}
