// Command ptapp is the plain-Temporal side of the second bake-off: one
// program that serves the team's HTTP API and runs its Temporal worker.
//
//	ptapp --tokens FILE [--listen 127.0.0.1:28080]
//
// Environment: DATABASE_URL, TEMPORAL_ADDRESS, HR_URL, ERP_URL, EMAIL_URL.
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"github.com/francoisnyc/work-kernel/spikes/temporal-bakeoff/access"
	"github.com/francoisnyc/work-kernel/spikes/temporal-bakeoff/acts"
	"github.com/francoisnyc/work-kernel/spikes/temporal-bakeoff/auth"
	"github.com/francoisnyc/work-kernel/spikes/temporal-bakeoff/httpapi"
	"github.com/francoisnyc/work-kernel/spikes/temporal-bakeoff/purchase"
	"github.com/francoisnyc/work-kernel/spikes/temporal-bakeoff/store"
)

func env(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

func main() {
	tokensPath := flag.String("tokens", "", "the dev-token file (the team's sign-in stand-in)")
	listen := flag.String("listen", "127.0.0.1:28080", "address of the HTTP API")
	flag.StringVar(&purchase.Deployed, "purchase-code", purchase.Deployed, "bake-off only: before, naive or patched (see purchase/change.go)")
	flag.Parse()
	if err := run(*tokensPath, *listen); err != nil {
		log.Fatal(err)
	}
}

// run returns an error instead of exiting, so deferred clean-up runs (Go
// has no finally; defer plays that part).
func run(tokensPath, listen string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	tokens, err := auth.Load(tokensPath)
	if err != nil {
		return err
	}
	st, err := store.Open(ctx, env("DATABASE_URL", "postgres://plain:plain@127.0.0.1:25432/plain?sslmode=disable"))
	if err != nil {
		return err
	}
	defer st.Close()
	tc, err := client.Dial(client.Options{HostPort: env("TEMPORAL_ADDRESS", "127.0.0.1:27233")})
	if err != nil {
		return err
	}
	defer tc.Close()

	w := worker.New(tc, purchase.TaskQueue, worker.Options{})
	w.RegisterWorkflowWithOptions(purchase.Workflow, workflow.RegisterOptions{Name: purchase.WorkflowName})
	w.RegisterWorkflowWithOptions(access.Workflow, workflow.RegisterOptions{Name: access.WorkflowName})
	w.RegisterActivity(acts.New(st, env("HR_URL", "http://127.0.0.1:18083"), env("ERP_URL", "http://127.0.0.1:18082"), env("EMAIL_URL", "http://127.0.0.1:18084")))
	if err := w.Start(); err != nil {
		return err
	}
	defer w.Stop()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(http.ResponseWriter, *http.Request) {})
	(&httpapi.Common{Store: st, Temporal: tc}).Routes(mux, tokens)
	(&purchase.API{Temporal: tc}).Routes(mux, tokens)
	(&access.API{Temporal: tc}).Routes(mux, tokens)
	srv := &http.Server{Addr: listen, Handler: mux, ReadHeaderTimeout: 10 * time.Second}

	// The server runs on its own goroutine; this one waits for it to fail or
	// for a signal. The channel carries the server's error, like a Future.
	failed := make(chan error, 1)
	go func() { failed <- srv.ListenAndServe() }()
	log.Printf("ptapp: API on %s, worker on task queue %q", listen, purchase.TaskQueue)
	select {
	case err := <-failed:
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutdown)
	}
}
