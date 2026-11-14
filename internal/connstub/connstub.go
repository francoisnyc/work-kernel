// Package connstub runs stub HTTP servers that stand in for a CRM and an ERP
// in tests and the bench. Each has a configurable latency, error rate and
// outage, and a table of customers the test fills in.
//
// Endpoints:
//
//	CRM  GET  /customers/{id}/plan    → {"plan": "enterprise"}
//	ERP  GET  /customers/{id}/credit  → {"on_hold": false}
//	ERP  POST /credit-checks          {"customer_id", "amount"}
//	                                  → {"verdict": "allow"|"deny"|"correct", "reason", "cost_center"}
//
// An unknown customer is 404, an outage 503, an injected error 500.
package connstub

import (
	"encoding/json"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"sync"
	"time"
)

// Customer is what the stubs know about one customer.
type Customer struct {
	Plan        string // CRM
	OnHold      bool   // ERP: credit on hold
	CreditLimit int    // ERP: the largest amount a check allows
	CostCenter  string // ERP: if set, a check corrects the ticket's cost center to it
}

// Server is one stub. Its settings may be changed while it serves: a test
// flips a customer or starts an outage between two calls.
type Server struct {
	URL string

	srv *httptest.Server

	// A sync.Mutex guards the fields below; handlers run on many goroutines.
	mu        sync.Mutex
	customers map[string]Customer
	latency   time.Duration
	errorRate float64
	outage    bool
	calls     int
}

// NewCRM starts a stub CRM.
func NewCRM() *Server {
	s := newServer()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /customers/{id}/plan", s.wrap(func(w http.ResponseWriter, c Customer, _ int64) {
		writeJSON(w, map[string]any{"plan": c.Plan})
	}))
	s.start(mux)
	return s
}

// NewERP starts a stub ERP.
func NewERP() *Server {
	s := newServer()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /customers/{id}/credit", s.wrap(func(w http.ResponseWriter, c Customer, _ int64) {
		writeJSON(w, map[string]any{"on_hold": c.OnHold})
	}))
	mux.HandleFunc("POST /credit-checks", s.wrap(func(w http.ResponseWriter, c Customer, amount int64) {
		switch {
		case c.OnHold:
			writeJSON(w, map[string]any{"verdict": "deny", "reason": "customer_on_hold"})
		case amount > int64(c.CreditLimit):
			writeJSON(w, map[string]any{"verdict": "deny", "reason": "over_credit_limit"})
		case c.CostCenter != "":
			writeJSON(w, map[string]any{"verdict": "correct", "cost_center": c.CostCenter})
		default:
			writeJSON(w, map[string]any{"verdict": "allow"})
		}
	}))
	s.start(mux)
	return s
}

func newServer() *Server { return &Server{customers: map[string]Customer{}} }

func (s *Server) start(h http.Handler) {
	s.srv = httptest.NewServer(h)
	s.URL = s.srv.URL
}

// Close stops the server.
func (s *Server) Close() { s.srv.Close() }

// SetCustomer adds or replaces a customer.
func (s *Server) SetCustomer(id string, c Customer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.customers[id] = c
}

// SetLatency makes every answer wait d first.
func (s *Server) SetLatency(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.latency = d
}

// SetErrorRate makes that share of calls (0 to 1) answer 500.
func (s *Server) SetErrorRate(r float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.errorRate = r
}

// SetOutage makes every call answer 503 while on.
func (s *Server) SetOutage(on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.outage = on
}

// Calls returns how many requests the server has received.
func (s *Server) Calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// wrap applies latency, outage and errors, finds the customer, and hands it
// to the endpoint. The customer comes from the path, or from a POSTed body
// (with the amount, for a credit check).
func (s *Server) wrap(serve func(w http.ResponseWriter, c Customer, amount int64)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.calls++
		latency, outage, errorRate := s.latency, s.outage, s.errorRate
		s.mu.Unlock()

		if latency > 0 {
			// select waits for whichever happens first: the latency, or the
			// client giving up. A caller's deadline must not hang the stub.
			select {
			case <-time.After(latency):
			case <-r.Context().Done():
				return
			}
		}
		if outage {
			http.Error(w, "outage", http.StatusServiceUnavailable)
			return
		}
		if errorRate > 0 && rand.Float64() < errorRate {
			http.Error(w, "injected error", http.StatusInternalServerError)
			return
		}

		id, amount := r.PathValue("id"), int64(0)
		if r.Method == http.MethodPost {
			var body struct {
				CustomerID string `json:"customer_id"`
				Amount     int64  `json:"amount"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, "bad body", http.StatusBadRequest)
				return
			}
			id, amount = body.CustomerID, body.Amount
		}
		s.mu.Lock()
		c, ok := s.customers[id]
		s.mu.Unlock()
		if !ok {
			http.Error(w, "no such customer", http.StatusNotFound)
			return
		}
		serve(w, c, amount)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
