// Command connstub runs the stub CRM, ERP and (with --hr-listen) HR
// directory (internal/connstub) as a service, for the compose stack: the
// worker's hooks call them as they would call real ones.
//
// --hr-directory is a JSON object mapping each employee to their manager,
// both user principal IDs (deploy/hr-directory.json).
//
//	connstub [--crm-listen :8081] [--erp-listen :8082] [--erp-latency 1s] [--customers FILE]
//	         [--hr-listen :8083 [--hr-directory FILE]]
//	connstub --probe URL
//
// --customers is a JSON object of customers by ID; an unknown key is an
// error, so a typo never passes silently:
//
//	{"cust-good": {"plan": "pro", "credit_limit": 1000},
//	 "cust-hold": {"plan": "free", "on_hold": true}}
//
// Each server also answers GET /healthz with 200 and no body. --probe gets a
// URL and exits 0 on 200: the image has no shell or curl.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/francoisnyc/work-kernel/internal/connstub"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stderr); err != nil {
		slog.Error("connstub stopped", "error", err)
		os.Exit(1)
	}
}

// customer is one entry of the customers file.
type customer struct {
	Plan        string `json:"plan"`
	OnHold      bool   `json:"on_hold"`
	CreditLimit int    `json:"credit_limit"`
	CostCenter  string `json:"cost_center"`
}

// run serves both stubs until ctx is canceled, or probes and returns.
func run(ctx context.Context, args []string, stderr io.Writer) error {
	fs := flag.NewFlagSet("connstub", flag.ContinueOnError)
	fs.SetOutput(stderr)
	crmAddr := fs.String("crm-listen", ":8081", "the CRM's address")
	erpAddr := fs.String("erp-listen", ":8082", "the ERP's address")
	erpLatency := fs.Duration("erp-latency", 0, "how long the ERP waits before every answer")
	customersPath := fs.String("customers", "", "JSON file of customers by ID")
	hrAddr := fs.String("hr-listen", "", "the HR directory's address (none: no HR directory)")
	hrPath := fs.String("hr-directory", "", "JSON file mapping each employee to their manager (principal IDs)")
	probeURL := fs.String("probe", "", "check a running stub's /healthz URL and exit (for container health checks)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *probeURL != "" {
		return probe(ctx, *probeURL)
	}
	customers := map[string]customer{}
	if *customersPath != "" {
		data, err := os.ReadFile(*customersPath)
		if err != nil {
			return err
		}
		// A json.Decoder can refuse unknown keys; json.Unmarshal cannot.
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&customers); err != nil {
			return fmt.Errorf("%s: %w", *customersPath, err)
		}
	}

	crm, err := connstub.ServeCRM(*crmAddr)
	if err != nil {
		return fmt.Errorf("crm: %w", err)
	}
	defer crm.Close()
	erp, err := connstub.ServeERP(*erpAddr)
	if err != nil {
		return fmt.Errorf("erp: %w", err)
	}
	defer erp.Close()
	erp.SetLatency(*erpLatency)
	for id, c := range customers {
		sc := connstub.Customer{Plan: c.Plan, OnHold: c.OnHold, CreditLimit: c.CreditLimit, CostCenter: c.CostCenter}
		crm.SetCustomer(id, sc)
		erp.SetCustomer(id, sc)
	}
	if *hrAddr != "" {
		directory := map[string]string{}
		if *hrPath != "" {
			if directory, err = connstub.LoadDirectory(*hrPath); err != nil {
				return err
			}
		}
		hr, err := connstub.ServeHR(*hrAddr)
		if err != nil {
			return fmt.Errorf("hr: %w", err)
		}
		defer hr.Close()
		for employee, manager := range directory {
			hr.SetManager(employee, manager)
		}
		slog.Info("connstub serving HR", "hr", *hrAddr, "employees", len(directory))
	}
	slog.Info("connstub serving", "crm", *crmAddr, "erp", *erpAddr, "erp_latency", *erpLatency, "customers", len(customers))
	<-ctx.Done()
	return nil
}

// probe gets url and returns nil on 200, an error otherwise.
func probe(ctx context.Context, url string) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("probe: %w", err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("probe: %w", err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("probe: %s answered %d", url, res.StatusCode)
	}
	return nil
}
