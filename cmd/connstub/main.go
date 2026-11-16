// Command connstub runs the stub CRM and ERP (internal/connstub) as a
// service, for the compose stack: the worker's hooks call them as they
// would call real ones.
//
//	connstub [--crm-listen :8081] [--erp-listen :8082] [--erp-latency 1s] [--customers FILE]
//	connstub --probe URL
//
// --customers is a JSON object of customers by ID:
//
//	{"cust-good": {"plan": "pro", "credit_limit": 1000},
//	 "cust-hold": {"plan": "free", "on_hold": true}}
//
// Each server also answers GET /healthz with 200 and no body. --probe gets a
// URL and exits 0 on 200: the image has no shell or curl.
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
	if err := run(ctx, os.Args[1:], os.Stderr); err != nil {
		slog.Error("connstub stopped", "error", err)
		os.Exit(1)
	}
}

// errNotBuilt marks what Step 0, part 3 has not built yet.
var errNotBuilt = errors.New("connstub: not built yet (Step 0, part 3)")

// run serves both stubs until ctx is canceled, or probes and returns.
func run(ctx context.Context, args []string, stderr io.Writer) error {
	return errNotBuilt
}
