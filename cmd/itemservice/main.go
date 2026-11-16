// Command itemservice serves the public API v1 (internal/api) over HTTP:
// the Connect protocol, gRPC and gRPC-Web, on one port.
//
//	itemservice --dev-tokens dev-tokens.yaml [--types types] [--listen :8080]
//
// The database is DATABASE_URL. Migrations are not run here: apply them first
// (make migrate). At start the server loads every Type in --types and makes
// every namespace the token file names.
//
// Dev tokens are the only auth in the proof of concept, so the server refuses
// to start without --dev-tokens, and warns on every start that uses it.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/francoisnyc/work-kernel/internal/api"
	"github.com/francoisnyc/work-kernel/internal/devtoken"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

func main() {
	// The context is canceled on SIGINT or SIGTERM; run then shuts the
	// server down and returns.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Getenv, os.Stderr); err != nil {
		slog.Error("itemservice stopped", "error", err)
		os.Exit(1)
	}
}

// run is main with its inputs passed in, so tests can call it. It returns
// when ctx is canceled or the server fails.
func run(ctx context.Context, args []string, getenv func(string) string, stderr io.Writer) error {
	fs := flag.NewFlagSet("itemservice", flag.ContinueOnError)
	fs.SetOutput(stderr)
	tokensPath := fs.String("dev-tokens", "", "dev-token file (proof of concept only; required)")
	typesDir := fs.String("types", "types", "directory of Type files to load at start")
	listen := fs.String("listen", ":8080", "address to listen on")
	probeURL := fs.String("probe", "", "check a running server's /healthz URL and exit (for container health checks)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *probeURL != "" {
		return probe(ctx, *probeURL)
	}
	if *tokensPath == "" {
		return errors.New("refusing to start without auth: pass --dev-tokens FILE (dev tokens are the only auth in the proof of concept)")
	}
	tokens, err := devtoken.Load(*tokensPath)
	if err != nil {
		return err
	}
	// Grants live in the same file: what each user let each agent do.
	grants, err := devtoken.LoadGrants(*tokensPath)
	if err != nil {
		return err
	}
	fmt.Fprintf(stderr, "WARNING: dev tokens are on (%d from %s). Anyone holding that file can act as its users. "+
		"Proof of concept only; never expose this server.\n", len(tokens), *tokensPath)

	types, err := typedef.ParseDir(*typesDir)
	if err != nil {
		return err
	}

	dsn := getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://kernel:kernel@localhost:5432/kernel?sslmode=disable"
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	defer pool.Close()
	// pgxpool connects lazily; ping so a bad DATABASE_URL fails at start.
	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("database: %w", err)
	}

	k := itemservice.New(pool)
	for _, t := range types {
		if err := k.RegisterType(ctx, t); err != nil {
			return fmt.Errorf("register %s v%d: %w", t.ID, t.Version, err)
		}
	}
	for _, t := range tokens {
		if err := k.EnsureNamespace(ctx, t.Namespace); err != nil {
			return err
		}
	}

	path, handler := api.NewHandler(k, api.Options{Tokens: tokens, Grants: grants})
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	// gRPC needs HTTP/2. Without TLS that is "h2c", which net/http serves
	// when asked through Protocols (Go 1.24+), so no extra library is needed.
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)
	srv := &http.Server{Addr: *listen, Handler: mux, Protocols: protocols, ReadHeaderTimeout: 10 * time.Second}

	// The server runs in a goroutine; this one waits for either the server
	// to fail or ctx to be canceled. A channel carries the server's result,
	// like a Future.
	failed := make(chan error, 1)
	go func() { failed <- srv.ListenAndServe() }()
	slog.Info("itemservice listening", "addr", *listen, "types", len(types), "tokens", len(tokens))
	select {
	case err := <-failed:
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdown)
	}
}

// errNotBuilt marks what Step 0, part 3 has not built yet.
var errNotBuilt = errors.New("itemservice: not built yet (Step 0, part 3)")

// healthz answers 200 when ping succeeds and 503 when it fails, with no
// body either way: no error text, nothing about the database.
func healthz(ping func(context.Context) error) http.Handler {
	return http.NotFoundHandler()
}

// probe gets url and returns nil on 200, an error otherwise. The images
// have no shell or curl, so the binary checks itself.
func probe(ctx context.Context, url string) error {
	return errNotBuilt
}
