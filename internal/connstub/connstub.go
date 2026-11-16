// Package connstub runs stub HTTP servers that stand in for a CRM and an ERP
// in tests, the bench and the compose stack (cmd/connstub). Each has a configurable latency, error rate and
// outage, and a table of customers the test fills in.
//
// Endpoints:
//
//	CRM  GET  /customers/{id}/plan    → {"plan": "enterprise"}
//	ERP  GET  /customers/{id}/credit  → {"on_hold": false}
//	ERP  POST /credit-checks          {"customer_id", "amount"}
//	                                  → {"verdict": "allow"|"deny"|"correct", "reason", "cost_center"}
//	ERP  POST /invoices               {"customer_id", "amount"}, header Idempotency-Key
//	                                  → {"invoice_id": "INV-…"}
//	both GET  /healthz                → 200, no body, never delayed
//
// An unknown customer is 404, an outage 503, an injected error 500, all
// before any effect.
//
// The ERP honours idempotency keys like a real one: the same key with the
// same request returns the invoice it already made, the same key with a
// different request is 409 {"error": "key_conflict"}, and without a key every
// request makes an invoice. To force timeouts that matter, the first invoice
// request for each customer can be committed and then answered late
// (SetFirstAnswerDelay) or not at all (SetDropFirstAnswer).
package connstub

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net"
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

	// Invoices (ERP only).
	invoices    map[string][]string   // customer -> invoice IDs, in order
	byKey       map[string]invoiceRec // Idempotency-Key -> what it made
	seen        map[string]bool       // customers that have sent an invoice request
	lastInvoice int
	firstDelay  time.Duration
	dropFirst   bool
	late        int
	dropped     int

	arrivals map[string][]Arrival // customer -> requests, as they arrived
}

// Arrival is when one request for a customer reached the stub, before any
// latency. The bench uses it to split an approval's time into its legs.
type Arrival struct {
	Path string
	At   time.Time
}

// invoiceRec is what one key made: the request, to spot a conflict, and the
// invoice, to return again.
type invoiceRec struct {
	customer string
	amount   int64
	id       string
}

// NewCRM starts a stub CRM on a free loopback port.
func NewCRM() *Server {
	s := newServer()
	s.start(s.crmHandler())
	return s
}

// NewERP starts a stub ERP on a free loopback port.
func NewERP() *Server {
	s := newServer()
	s.start(s.erpHandler())
	return s
}

// ServeCRM starts a stub CRM listening on addr (":8081"), for cmd/connstub.
func ServeCRM(addr string) (*Server, error) {
	s := newServer()
	return s, s.startOn(addr, s.crmHandler())
}

// ServeERP starts a stub ERP listening on addr (":8082"), for cmd/connstub.
func ServeERP(addr string) (*Server, error) {
	s := newServer()
	return s, s.startOn(addr, s.erpHandler())
}

// healthz answers 200 with no body, at once: no latency, outage or errors.
// NewHR starts a stub HR directory on a free local port. Contract stub
// (Step 2, part 1, Phase B): it answers only /healthz.
//
//	HR   GET  /employees/{id}/manager → {"manager": "user:bob"}; 404 for an unknown employee
func NewHR() *Server {
	s := newServer()
	mux := http.NewServeMux()
	healthz(mux)
	s.start(mux)
	return s
}

// ServeHR serves a stub HR directory on addr. Contract stub (Phase B).
func ServeHR(addr string) (*Server, error) {
	s := newServer()
	mux := http.NewServeMux()
	healthz(mux)
	return s, s.startOn(addr, mux)
}

// SetManager records who an employee's manager is (principal IDs). Contract
// stub (Phase B): it records nothing.
func (s *Server) SetManager(employee, manager string) {}

// LoadDirectory reads an HR directory file: a JSON object mapping each
// employee to their manager, both principal IDs. Contract stub (Phase B).
func LoadDirectory(path string) (map[string]string, error) {
	return nil, fmt.Errorf("connstub: LoadDirectory: not implemented")
}

func healthz(mux *http.ServeMux) {
	mux.HandleFunc("GET /healthz", func(http.ResponseWriter, *http.Request) {})
}

func (s *Server) crmHandler() http.Handler {
	mux := http.NewServeMux()
	healthz(mux)
	mux.HandleFunc("GET /customers/{id}/plan", s.wrap(func(w http.ResponseWriter, _ *http.Request, _ string, c Customer, _ int64) {
		writeJSON(w, map[string]any{"plan": c.Plan})
	}))
	return mux
}

func (s *Server) erpHandler() http.Handler {
	mux := http.NewServeMux()
	healthz(mux)
	mux.HandleFunc("GET /customers/{id}/credit", s.wrap(func(w http.ResponseWriter, _ *http.Request, _ string, c Customer, _ int64) {
		writeJSON(w, map[string]any{"on_hold": c.OnHold})
	}))
	mux.HandleFunc("POST /credit-checks", s.wrap(func(w http.ResponseWriter, _ *http.Request, _ string, c Customer, amount int64) {
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
	mux.HandleFunc("POST /invoices", s.wrap(s.invoice))
	return mux
}

func newServer() *Server {
	return &Server{
		customers: map[string]Customer{},
		invoices:  map[string][]string{}, byKey: map[string]invoiceRec{}, seen: map[string]bool{},
		arrivals: map[string][]Arrival{},
	}
}

// invoice makes an invoice, or returns the one its key already made. The
// effect is committed under the lock, before any delay, so two requests
// with one key can never both make one.
func (s *Server) invoice(w http.ResponseWriter, r *http.Request, customer string, _ Customer, amount int64) {
	key := r.Header.Get("Idempotency-Key")
	s.mu.Lock()
	first := !s.seen[customer]
	s.seen[customer] = true
	rec, done := s.byKey[key]
	if key != "" && done && (rec.customer != customer || rec.amount != amount) {
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "key_conflict"})
		return
	}
	if key == "" || !done {
		s.lastInvoice++
		rec = invoiceRec{customer: customer, amount: amount, id: fmt.Sprintf("INV-%d", s.lastInvoice)}
		s.invoices[customer] = append(s.invoices[customer], rec.id)
		if key != "" {
			s.byKey[key] = rec
		}
	}
	delay, drop := s.firstDelay, s.dropFirst
	s.mu.Unlock()

	if first && drop {
		// Close the connection without a word: the client cannot tell
		// whether the invoice was made.
		if hj, ok := w.(http.Hijacker); ok {
			if conn, _, err := hj.Hijack(); err == nil {
				_ = conn.Close()
				s.count(&s.dropped)
				return
			}
		}
	}
	if first && delay > 0 {
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			s.count(&s.late) // the client gave up; the invoice stands
			return
		}
	}
	writeJSON(w, map[string]any{"invoice_id": rec.id})
}

func (s *Server) count(n *int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	*n++
}

func (s *Server) start(h http.Handler) {
	s.srv = httptest.NewServer(h)
	s.URL = s.srv.URL
}

// startOn serves h on addr. httptest.Server is reused for its Close, which
// waits for requests in flight; only its listener changes.
func (s *Server) startOn(addr string, h http.Handler) error {
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	s.srv = httptest.NewUnstartedServer(h)
	s.srv.Listener.Close()
	s.srv.Listener = l
	s.srv.Start()
	s.URL = s.srv.URL
	return nil
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

// SetFirstAnswerDelay makes the ERP commit the first invoice request of each
// customer and answer it only after d. A client that gives up meanwhile is
// counted in LateAnswers; its invoice stands. Later requests answer at once.
func (s *Server) SetFirstAnswerDelay(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.firstDelay = d
}

// SetDropFirstAnswer makes the ERP commit the first invoice request of each
// customer and close the connection without answering (DroppedAnswers).
func (s *Server) SetDropFirstAnswer(on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dropFirst = on
}

// Invoices returns a customer's invoice IDs, in the order they were made.
func (s *Server) Invoices(customer string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.invoices[customer]...)
}

// InvoiceCount returns how many invoices the ERP has made in all.
func (s *Server) InvoiceCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastInvoice
}

// Arrivals returns when each request for a customer arrived, in order.
func (s *Server) Arrivals(customer string) []Arrival {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Arrival(nil), s.arrivals[customer]...)
}

// LateAnswers counts delayed first answers whose client had given up.
func (s *Server) LateAnswers() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.late
}

// DroppedAnswers counts first answers dropped by SetDropFirstAnswer.
func (s *Server) DroppedAnswers() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dropped
}

// wrap applies latency, outage and errors, finds the customer, and hands it
// to the endpoint. The customer comes from the path, or from a POSTed body
// (with the amount, for a credit check or an invoice).
func (s *Server) wrap(serve func(w http.ResponseWriter, r *http.Request, id string, c Customer, amount int64)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		arrived := time.Now()
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
		s.arrivals[id] = append(s.arrivals[id], Arrival{Path: r.URL.Path, At: arrived})
		s.mu.Unlock()
		if !ok {
			http.Error(w, "no such customer", http.StatusNotFound)
			return
		}
		serve(w, r, id, c, amount)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
