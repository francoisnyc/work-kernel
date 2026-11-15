// Tests for Step 6, part 2: the stub HTTP servers that stand in for a CRM
// and an ERP. Tests and the bench set their latency, error rate and outage,
// and the customers they know.
package connstub_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/francoisnyc/work-kernel/internal/connstub"
)

func get(t *testing.T, url string) (int, map[string]any) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	return resp.StatusCode, body
}

func TestCRM_ServesThePlan(t *testing.T) {
	crm := connstub.NewCRM()
	defer crm.Close()
	crm.SetCustomer("cust-1", connstub.Customer{Plan: "enterprise"})

	code, body := get(t, crm.URL+"/customers/cust-1/plan")
	if code != http.StatusOK || body["plan"] != "enterprise" {
		t.Errorf("GET plan = %d %v, want 200 with plan enterprise", code, body)
	}
	if code, _ := get(t, crm.URL+"/customers/nobody/plan"); code != http.StatusNotFound {
		t.Errorf("GET plan of an unknown customer = %d, want 404", code)
	}
	if n := crm.Calls(); n != 2 {
		t.Errorf("Calls = %d, want 2", n)
	}
}

func TestERP_ServesTheCreditStatus(t *testing.T) {
	erp := connstub.NewERP()
	defer erp.Close()
	erp.SetCustomer("cust-1", connstub.Customer{OnHold: false})
	if code, body := get(t, erp.URL+"/customers/cust-1/credit"); code != http.StatusOK || body["on_hold"] != false {
		t.Errorf("GET credit = %d %v, want 200 with on_hold false", code, body)
	}
	// A test flips the fact after a prefetch read it.
	erp.SetCustomer("cust-1", connstub.Customer{OnHold: true})
	if _, body := get(t, erp.URL+"/customers/cust-1/credit"); body["on_hold"] != true {
		t.Errorf("GET credit after the flip = %v, want on_hold true", body)
	}
}

func TestStub_Latency(t *testing.T) {
	crm := connstub.NewCRM()
	defer crm.Close()
	crm.SetCustomer("cust-1", connstub.Customer{Plan: "pro"})
	crm.SetLatency(200 * time.Millisecond)
	start := time.Now()
	get(t, crm.URL+"/customers/cust-1/plan")
	if took := time.Since(start); took < 200*time.Millisecond {
		t.Errorf("answered after %v, want at least the 200 ms latency", took)
	}
}

func TestStub_OutageAndErrors(t *testing.T) {
	crm := connstub.NewCRM()
	defer crm.Close()
	crm.SetCustomer("cust-1", connstub.Customer{Plan: "pro"})

	crm.SetOutage(true)
	if code, _ := get(t, crm.URL+"/customers/cust-1/plan"); code != http.StatusServiceUnavailable {
		t.Errorf("during an outage: %d, want 503", code)
	}
	crm.SetOutage(false)
	crm.SetErrorRate(1)
	if code, _ := get(t, crm.URL+"/customers/cust-1/plan"); code != http.StatusInternalServerError {
		t.Errorf("with error rate 1: %d, want 500", code)
	}
	crm.SetErrorRate(0)
	if code, _ := get(t, crm.URL+"/customers/cust-1/plan"); code != http.StatusOK {
		t.Errorf("back to normal: %d, want 200", code)
	}
}
