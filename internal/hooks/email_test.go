// Tests for proof-of-concept Step 2, part 3: email.notify, the field-less
// action that sends one message through the email stub. It reads one
// principal (the recipient) and writes nothing; the hook's ID is the
// template; the hook's key goes to the stub, so a retry is stored once.
package hooks_test

import (
	"errors"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/connstub"
	"github.com/francoisnyc/work-kernel/internal/hooks"
)

func TestEmailNotify_Arity(t *testing.T) {
	reads, writes, ok := hooks.Arity("email.notify")
	if !ok || reads != 1 || writes != 0 {
		t.Errorf("Arity(email.notify) = %d, %d, %v; want 1 read, 0 writes", reads, writes, ok)
	}
}

func TestEmailNotify_SendsOnceByKey(t *testing.T) {
	mail := connstub.NewEmail()
	t.Cleanup(mail.Close)
	c := hooks.NewConnectors("", "").WithEmail(mail.URL)
	call := hooks.Call{HookID: "notify_approved", ItemID: "0193a1b2-0000-7000-8000-000000000001"}

	for i := 0; i < 2; i++ { // the second is a retry: the same key
		res, err := c.RunCall(ctx(t), "email.notify", "hook:item-1:5:notify_approved", []any{"user:ana"}, call)
		if err != nil || len(res.Values) != 0 {
			t.Fatalf("email.notify #%d = %+v, %v; want no values, no error", i+1, res, err)
		}
	}
	got := mail.Messages(call.ItemID)
	want := connstub.Message{To: "user:ana", Template: "notify_approved", Item: call.ItemID, Key: "hook:item-1:5:notify_approved"}
	if len(got) != 1 || got[0].ID == "" {
		t.Fatalf("messages = %+v, want one", got)
	}
	got[0].ID = ""
	if got[0] != want {
		t.Errorf("message = %+v, want %+v", got[0], want)
	}
}

// What makes no sense to send is permanent: no key (a retry could not be
// told from a new message), no recipient, a recipient that is not a
// principal, no email service configured.
func TestEmailNotify_Refusals(t *testing.T) {
	mail := connstub.NewEmail()
	t.Cleanup(mail.Close)
	c := hooks.NewConnectors("", "").WithEmail(mail.URL)
	call := hooks.Call{HookID: "notify_approved", ItemID: "item-1"}
	cases := []struct {
		name string
		c    *hooks.Connectors
		key  string
		to   any
	}{
		{"no key", c, "", "user:ana"},
		{"no recipient", c, "k-1", nil},
		{"not a principal", c, "k-2", "ana@example.com"},
		{"no email service", hooks.NewConnectors("", ""), "k-3", "user:ana"},
	}
	for _, tc := range cases {
		if _, err := tc.c.RunCall(ctx(t), "email.notify", tc.key, []any{tc.to}, call); !errors.Is(err, hooks.ErrPermanent) {
			t.Errorf("%s: %v, want ErrPermanent", tc.name, err)
		}
	}
	if n := len(mail.Messages("")); n != 0 {
		t.Errorf("messages = %d, want none sent", n)
	}
}

// The same key with another message is a key conflict, never a second send.
func TestEmailNotify_KeyConflict(t *testing.T) {
	mail := connstub.NewEmail()
	t.Cleanup(mail.Close)
	c := hooks.NewConnectors("", "").WithEmail(mail.URL)
	if _, err := c.RunCall(ctx(t), "email.notify", "k-1", []any{"user:ana"}, hooks.Call{HookID: "a", ItemID: "i"}); err != nil {
		t.Fatalf("first: %v", err)
	}
	_, err := c.RunCall(ctx(t), "email.notify", "k-1", []any{"user:bob"}, hooks.Call{HookID: "a", ItemID: "i"})
	if !errors.Is(err, hooks.ErrKeyConflict) {
		t.Errorf("same key, another recipient: %v, want ErrKeyConflict", err)
	}
	if n := len(mail.Messages("")); n != 1 {
		t.Errorf("messages = %d, want 1", n)
	}
}
