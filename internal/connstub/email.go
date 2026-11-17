package connstub

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
)

// The email stub (proof of concept, Step 2, part 3). POST /messages
// {to, template, item, key} records one message and answers {"id": ...};
// the same key with the same message returns the first ID and stores
// nothing more; the same key with a different message is 409
// {"error": "key_conflict"}. GET /messages?item=<id> lists the messages for
// an Item, in arrival order, and GET /messages lists them all. Messages
// hold opaque IDs only (a principal, a template ID, an Item ID): no text.
//
// The reader (ServeEmailReader) serves the GET routes only, for the dev
// compose file to publish: the scenario reads what was sent with curl,
// and nothing outside the stack can send.

// Message is one recorded email.
type Message struct {
	ID       string `json:"id"`
	To       string `json:"to"`       // a principal, user:ana
	Template string `json:"template"` // the hook's ID, notify_approved
	Item     string `json:"item"`     // the Item's ID
	Key      string `json:"key"`      // the idempotency key
}

// NewEmail starts a stub email service on a free loopback port.
func NewEmail() *Server {
	s := newServer()
	s.start(s.emailHandler())
	return s
}

// ServeEmail starts a stub email service listening on addr (":8084"), for
// cmd/connstub.
func ServeEmail(addr string) (*Server, error) {
	s := newServer()
	return s, s.startOn(addr, s.emailHandler())
}

// ServeEmailReader serves s's messages, read-only (GET only), on addr.
func (s *Server) ServeEmailReader(addr string) error {
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	healthz(mux)
	mux.HandleFunc("GET /messages", s.listMessages)
	r := httptest.NewUnstartedServer(mux)
	r.Listener.Close()
	r.Listener = l
	r.Start()
	s.mu.Lock()
	s.reader = r
	s.mu.Unlock()
	return nil
}

// Messages returns the messages recorded for an Item ("" for all), in
// arrival order.
func (s *Server) Messages(item string) []Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Message
	for _, m := range s.messages {
		if item == "" || m.Item == item {
			out = append(out, m)
		}
	}
	return out
}

// emailHandler serves the email routes.
func (s *Server) emailHandler() http.Handler {
	mux := http.NewServeMux()
	healthz(mux)
	mux.HandleFunc("POST /messages", s.send)
	mux.HandleFunc("GET /messages", s.listMessages)
	return mux
}

// send records a message once per key. Like the invoice, the effect is
// committed under the lock, so one key never records two messages.
func (s *Server) send(w http.ResponseWriter, r *http.Request) {
	if !s.knobs(w, r) {
		return
	}
	var m Message
	if err := json.NewDecoder(r.Body).Decode(&m); err != nil ||
		m.To == "" || m.Template == "" || m.Item == "" || m.Key == "" {
		http.Error(w, "a message needs to, template, item and key", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	prev, done := s.byMsgKey[m.Key]
	if done {
		s.mu.Unlock()
		if prev.To != m.To || prev.Template != m.Template || prev.Item != m.Item {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "key_conflict"})
			return
		}
		writeJSON(w, map[string]any{"id": prev.ID})
		return
	}
	s.lastMsg++
	m.ID = fmt.Sprintf("MSG-%d", s.lastMsg)
	s.messages = append(s.messages, m)
	s.byMsgKey[m.Key] = m
	s.mu.Unlock()
	writeJSON(w, map[string]any{"id": m.ID})
}

func (s *Server) listMessages(w http.ResponseWriter, r *http.Request) {
	msgs := s.Messages(r.URL.Query().Get("item"))
	if msgs == nil {
		msgs = []Message{} // [] rather than null, for jq
	}
	writeJSON(w, map[string]any{"messages": msgs})
}
