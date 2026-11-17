// Tests for proof-of-concept Step 2, part 3: the email stub. It records
// each message once per idempotency key, refuses a key reused with another
// message (409 key_conflict), and serves what it recorded; its reader
// serves the GET routes only, for the dev compose file to publish.
package connstub_test

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/connstub"
)

func post(t *testing.T, url string, body map[string]string) (*http.Response, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	res, err := http.Post(url+"/messages", "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer res.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res, out
}

func TestEmail_RecordsOncePerKey(t *testing.T) {
	s := connstub.NewEmail()
	t.Cleanup(s.Close)
	msg := map[string]string{"to": "user:ana", "template": "notify_approved", "item": "item-1", "key": "k-1"}
	res, first := post(t, s.URL, msg)
	if res.StatusCode != http.StatusOK || first["id"] == "" || first["id"] == nil {
		t.Fatalf("POST = %d %v, want 200 and an id", res.StatusCode, first)
	}
	res, again := post(t, s.URL, msg)
	if res.StatusCode != http.StatusOK || again["id"] != first["id"] {
		t.Errorf("the same key again = %d %v, want 200 and the first id %v", res.StatusCode, again, first["id"])
	}
	if got := s.Messages("item-1"); len(got) != 1 {
		t.Errorf("messages = %+v, want one", got)
	}

	other := map[string]string{"to": "user:bob", "template": "notify_approved", "item": "item-1", "key": "k-1"}
	res, body := post(t, s.URL, other)
	if res.StatusCode != http.StatusConflict || body["error"] != "key_conflict" {
		t.Errorf("the key with another message = %d %v, want 409 key_conflict", res.StatusCode, body)
	}
	post(t, s.URL, map[string]string{"to": "user:bob", "template": "remind_approver", "item": "item-2", "key": "k-2"})
	if got := s.Messages("item-1"); len(got) != 1 {
		t.Errorf("messages for item-1 = %+v, want one", got)
	}
	if got := s.Messages(""); len(got) != 2 {
		t.Errorf("all messages = %+v, want two", got)
	}

	// GET /messages?item= serves them, in arrival order.
	r, err := http.Get(s.URL + "/messages?item=item-2")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	var listed struct {
		Messages []connstub.Message `json:"messages"`
	}
	if err := json.NewDecoder(r.Body).Decode(&listed); err != nil || len(listed.Messages) != 1 ||
		listed.Messages[0].Template != "remind_approver" {
		t.Errorf("GET /messages?item=item-2 = %+v, %v", listed, err)
	}
}

// A message needs every part.
func TestEmail_IncompleteMessagesRefused(t *testing.T) {
	s := connstub.NewEmail()
	t.Cleanup(s.Close)
	for _, missing := range []string{"to", "template", "item", "key"} {
		msg := map[string]string{"to": "user:ana", "template": "t", "item": "i", "key": "k-" + missing}
		delete(msg, missing)
		if res, _ := post(t, s.URL, msg); res.StatusCode != http.StatusBadRequest {
			t.Errorf("without %s: %d, want 400", missing, res.StatusCode)
		}
	}
	if n := len(s.Messages("")); n != 0 {
		t.Errorf("messages = %d, want none", n)
	}
}

// The reader serves GET only: nothing outside the stack can send.
func TestEmail_ReaderIsReadOnly(t *testing.T) {
	s := connstub.NewEmail()
	t.Cleanup(s.Close)
	post(t, s.URL, map[string]string{"to": "user:ana", "template": "t", "item": "i", "key": "k-1"})
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	if err := s.ServeEmailReader(addr); err != nil {
		t.Fatalf("ServeEmailReader: %v", err)
	}
	reader := "http://" + addr
	r, err := http.Get(reader + "/messages?item=i")
	if err != nil || r.StatusCode != http.StatusOK {
		t.Fatalf("GET on the reader = %v, %v", r, err)
	}
	r.Body.Close()
	res, _ := post(t, reader, map[string]string{"to": "user:bob", "template": "t", "item": "i", "key": "k-2"})
	if res.StatusCode != http.StatusMethodNotAllowed && res.StatusCode != http.StatusNotFound {
		t.Errorf("POST on the reader = %d, want it refused (405 or 404)", res.StatusCode)
	}
	if n := len(s.Messages("i")); n != 1 {
		t.Errorf("messages = %d, want 1", n)
	}
}
