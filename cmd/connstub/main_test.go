// Tests for Step 0, part 3: cmd/connstub, the stub CRM and ERP as a service
// for the compose stack. It serves both stubs on the addresses given, with
// the customers from a JSON file and the ERP's latency; each answers
// /healthz; --probe checks a URL and exits.
package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const customers = `{
  "cust-good": {"plan": "pro", "credit_limit": 1000},
  "cust-hold": {"plan": "free", "on_hold": true},
  "cust-cc":   {"plan": "enterprise", "credit_limit": 50, "cost_center": "CC-9"}
}`

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

func writeFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "customers.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// start runs connstub until the test ends and waits until both stubs
// answer /healthz.
func start(t *testing.T, latency time.Duration) (crm, erp string) {
	t.Helper()
	crmAddr, erpAddr := freeAddr(t), freeAddr(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, []string{"--crm-listen", crmAddr, "--erp-listen", erpAddr,
			"--erp-latency", latency.String(), "--customers", writeFile(t, customers)}, io.Discard)
	}()
	stopped := false // run returned and its answer was read
	t.Cleanup(func() {
		cancel()
		if stopped {
			return
		}
		if err := <-done; err != nil {
			t.Errorf("run after cancel = %v, want nil", err)
		}
	})
	crm, erp = "http://"+crmAddr, "http://"+erpAddr
	deadline := time.Now().Add(5 * time.Second)
	for _, base := range []string{crm, erp} {
		for {
			select {
			case err := <-done:
				stopped = true
				t.Fatalf("connstub stopped at start: %v", err)
			default:
			}
			res, err := http.Get(base + "/healthz")
			if err == nil {
				body, _ := io.ReadAll(res.Body)
				res.Body.Close()
				if res.StatusCode == http.StatusOK && len(body) == 0 {
					break
				}
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s/healthz not 200 with no body after 5 s", base)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	return crm, erp
}

func getJSON(t *testing.T, method, url, body string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer res.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

func TestRun_ServesTheCustomersFile(t *testing.T) {
	crm, erp := start(t, 0)
	if code, out := getJSON(t, "GET", crm+"/customers/cust-good/plan", ""); code != 200 || out["plan"] != "pro" {
		t.Errorf("CRM plan of cust-good = %d %v, want 200 pro", code, out)
	}
	if code, out := getJSON(t, "GET", erp+"/customers/cust-hold/credit", ""); code != 200 || out["on_hold"] != true {
		t.Errorf("ERP credit of cust-hold = %d %v, want 200 on_hold", code, out)
	}
	if code, out := getJSON(t, "POST", erp+"/credit-checks", `{"customer_id": "cust-good", "amount": 100}`); code != 200 || out["verdict"] != "allow" {
		t.Errorf("credit check, cust-good 100 = %d %v, want allow", code, out)
	}
	if code, out := getJSON(t, "POST", erp+"/credit-checks", `{"customer_id": "cust-good", "amount": 5000}`); code != 200 || out["verdict"] != "deny" {
		t.Errorf("credit check, cust-good 5000 = %d %v, want deny", code, out)
	}
	if code, out := getJSON(t, "POST", erp+"/credit-checks", `{"customer_id": "cust-cc", "amount": 10}`); code != 200 || out["cost_center"] != "CC-9" {
		t.Errorf("credit check, cust-cc = %d %v, want cost center CC-9", code, out)
	}
	if code, _ := getJSON(t, "GET", crm+"/customers/nobody/plan", ""); code != 404 {
		t.Errorf("unknown customer = %d, want 404", code)
	}
}

// The ERP answers after its latency; the CRM, given none, at once.
func TestRun_ERPLatency(t *testing.T) {
	crm, erp := start(t, 300*time.Millisecond)
	began := time.Now()
	getJSON(t, "POST", erp+"/credit-checks", `{"customer_id": "cust-good", "amount": 1}`)
	if d := time.Since(began); d < 300*time.Millisecond {
		t.Errorf("ERP answered in %v, want at least its 300 ms latency", d)
	}
	began = time.Now()
	getJSON(t, "GET", crm+"/customers/cust-good/plan", "")
	if d := time.Since(began); d > 250*time.Millisecond {
		t.Errorf("CRM answered in %v, want no latency", d)
	}
}

func TestRun_BadCustomersFileIsRefused(t *testing.T) {
	path := writeFile(t, `{"cust-good": {"plan": "pro", "credit_limt": 1000}}`) // a typo
	err := run(context.Background(), []string{"--crm-listen", freeAddr(t), "--erp-listen", freeAddr(t),
		"--customers", path}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), path) {
		t.Errorf("run with a bad customers file = %v, want an error naming %s", err, path)
	}
}

func TestRun_Probe(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer up.Close()
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer down.Close()
	if err := run(context.Background(), []string{"--probe", up.URL + "/healthz"}, io.Discard); err != nil {
		t.Errorf("probe of a healthy server = %v, want nil", err)
	}
	if err := run(context.Background(), []string{"--probe", down.URL + "/healthz"}, io.Discard); err == nil {
		t.Error("probe of a 503 = nil, want an error")
	}
}
