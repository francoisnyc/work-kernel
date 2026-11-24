// Tests for how cmd/connstub's stubs start (ServeCRM, ServeERP, ServeHR):
// a stub answers nothing, not even /healthz, until it holds what it was
// started with. A caller takes /healthz as "ready", so a stub that answers
// before its customers are loaded answers 404 for a customer it was given.
package connstub_test

import (
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/francoisnyc/work-kernel/internal/connstub"
)

// freeAddr returns a loopback address nothing listens on.
func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

func TestServe_AnswersNothingUntilItsSetupHasRun(t *testing.T) {
	// A map of functions: Go's functions are values, so the three
	// constructors can be tried by one loop.
	for name, serve := range map[string]func(string, func(*connstub.Server)) (*connstub.Server, error){
		"CRM": connstub.ServeCRM, "ERP": connstub.ServeERP, "HR": connstub.ServeHR,
	} {
		t.Run(name, func(t *testing.T) {
			addr := freeAddr(t)
			answeredEarly := false
			s, err := serve(addr, func(*connstub.Server) {
				// While setup runs, the stub must not answer. A refused
				// connection and a request left waiting both count.
				c := http.Client{Timeout: 300 * time.Millisecond}
				if res, err := c.Get("http://" + addr + "/healthz"); err == nil {
					res.Body.Close()
					answeredEarly = true
				}
			})
			if err != nil {
				t.Fatalf("serve: %v", err)
			}
			defer s.Close()
			if answeredEarly {
				t.Error("the stub answered /healthz while its setup was still running")
			}
			res, err := http.Get("http://" + addr + "/healthz")
			if err != nil {
				t.Fatalf("/healthz after the start: %v", err)
			}
			res.Body.Close()
			if res.StatusCode != http.StatusOK {
				t.Errorf("/healthz after the start = %d, want 200", res.StatusCode)
			}
		})
	}
}

// What setup gives a stub is there for its first answer.
func TestServe_TheFirstAnswerHoldsWhatSetupGave(t *testing.T) {
	addr := freeAddr(t)
	crm, err := connstub.ServeCRM(addr, func(s *connstub.Server) {
		s.SetCustomer("cust-1", connstub.Customer{Plan: "pro"})
	})
	if err != nil {
		t.Fatalf("ServeCRM: %v", err)
	}
	defer crm.Close()
	res, err := http.Get("http://" + addr + "/customers/cust-1/plan")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Errorf("the first request for a customer given at the start = %d, want 200", res.StatusCode)
	}
}
