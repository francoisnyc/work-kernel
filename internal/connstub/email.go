package connstub

import (
	"errors"
	"net/http"
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
//
// Phase B stub: not implemented yet.
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
//
// Phase B stub: not implemented yet.
func (s *Server) ServeEmailReader(addr string) error {
	return errors.New("connstub: the email reader is not implemented")
}

// Messages returns the messages recorded for an Item ("" for all), in
// arrival order.
//
// Phase B stub: not implemented yet.
func (s *Server) Messages(item string) []Message {
	return nil
}

// emailHandler serves the email routes.
//
// Phase B stub: not implemented yet.
func (s *Server) emailHandler() http.Handler {
	mux := http.NewServeMux()
	healthz(mux)
	return mux
}
