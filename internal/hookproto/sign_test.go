// Tests for the hook protocol: Standard Webhooks v1.0.0 signing, symmetric
// (v1), built on the standard library. The kernel signs every request to a
// connector with the connection's secret; a connector verifies the
// signature, the timestamp (within five minutes either way), and ignores
// signature entries it cannot read. Several signatures may be sent at once,
// and several secrets trusted at once, for rotation.
package hookproto_test

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/francoisnyc/work-kernel/internal/hookproto"
)

// The reference library's own known answer (standard-webhooks,
// libraries/go/webhook_test.go, TestWebhookSign), checked independently
// with Python's hmac before it was written here.
func TestSign_TheSpecsVector(t *testing.T) {
	s, err := hookproto.ParseSecret("whsec_MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw")
	if err != nil {
		t.Fatalf("ParseSecret: %v", err)
	}
	got := hookproto.Sign(s, "msg_p5jXN8AQM9LWM0D4loKWxJek", time.Unix(1614265330, 0), []byte(`{"test": 2432232314}`))
	if want := "v1,g0hM9SsE+OTPJTGt/tmIKtSyZlE3uFJELVlNIOLJ1OE="; got != want {
		t.Errorf("Sign = %q, want %q", got, want)
	}
}

func TestParseSecret_RefusesWhatIsNotAWhsecSecret(t *testing.T) {
	for _, s := range []string{"", "whsec_", "MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw", "whsk_MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw",
		"whsec_not base64!"} {
		if _, err := hookproto.ParseSecret(s); !errors.Is(err, hookproto.ErrBadSecret) {
			t.Errorf("ParseSecret(%q) = %v, want ErrBadSecret", s, err)
		}
	}
}

// A generated secret is a whsec_ secret of 32 random bytes; two differ.
func TestGenerateSecret(t *testing.T) {
	a, b := hookproto.GenerateSecret(), hookproto.GenerateSecret()
	if a == b {
		t.Errorf("two generated secrets are equal: %q", a)
	}
	for _, s := range []string{a, b} {
		if !strings.HasPrefix(s, "whsec_") || len(s) != len("whsec_")+44 { // 32 bytes, base64 with padding
			t.Errorf("generated secret %q: want whsec_ and 32 bytes in base64", s)
		}
		if _, err := hookproto.ParseSecret(s); err != nil {
			t.Errorf("ParseSecret(generated %q) = %v", s, err)
		}
	}
}

// signed builds the headers of a request signed with secret at ts.
func signed(t *testing.T, secret, id string, ts time.Time, body []byte) http.Header {
	t.Helper()
	s, err := hookproto.ParseSecret(secret)
	if err != nil {
		t.Fatalf("ParseSecret: %v", err)
	}
	h := http.Header{}
	hookproto.SetSignatureHeaders(h, s, id, ts, body)
	return h
}

func verifier(t *testing.T, now time.Time, secrets ...string) hookproto.Verifier {
	t.Helper()
	v := hookproto.Verifier{Now: func() time.Time { return now }}
	for _, s := range secrets {
		p, err := hookproto.ParseSecret(s)
		if err != nil {
			t.Fatalf("ParseSecret: %v", err)
		}
		v.Secrets = append(v.Secrets, p)
	}
	return v
}

func TestVerify_AcceptsASignedRequest(t *testing.T) {
	secret, now := hookproto.GenerateSecret(), time.Unix(1_800_000_000, 0)
	body := []byte(`{"protocol":"pli.hooks/v1"}`)
	h := signed(t, secret, "msg_1", now, body)
	if h.Get(hookproto.HeaderID) != "msg_1" || h.Get(hookproto.HeaderTimestamp) != "1800000000" ||
		!strings.HasPrefix(h.Get(hookproto.HeaderSignature), "v1,") {
		t.Errorf("headers = %v; want webhook-id msg_1, webhook-timestamp 1800000000, a v1 signature", h)
	}
	id, err := verifier(t, now, secret).Verify(h, body)
	if err != nil || id != "msg_1" {
		t.Errorf("Verify = %q, %v; want msg_1", id, err)
	}
	// Up to the tolerance, either way.
	for _, skew := range []time.Duration{hookproto.Tolerance, -hookproto.Tolerance} {
		if _, err := verifier(t, now.Add(skew), secret).Verify(h, body); err != nil {
			t.Errorf("Verify with the clock %v away = %v, want accepted", skew, err)
		}
	}
}

func TestVerify_Refuses(t *testing.T) {
	secret, now := hookproto.GenerateSecret(), time.Unix(1_800_000_000, 0)
	body := []byte(`{"protocol":"pli.hooks/v1","inputs":{"createdBy":"user:ana"}}`)
	good := func() http.Header { return signed(t, secret, "msg_1", now, body) }
	cases := []struct {
		name   string
		header http.Header
		body   []byte
		want   error
	}{
		{"no headers", http.Header{}, body, hookproto.ErrNoSignature},
		{"no signature header", func() http.Header { h := good(); h.Del(hookproto.HeaderSignature); return h }(), body,
			hookproto.ErrNoSignature},
		{"no id header", func() http.Header { h := good(); h.Del(hookproto.HeaderID); return h }(), body,
			hookproto.ErrNoSignature},
		{"a tampered body", good(), []byte(`{"protocol":"pli.hooks/v1","inputs":{"createdBy":"user:bob"}}`),
			hookproto.ErrBadSignature},
		{"another id", func() http.Header { h := good(); h.Set(hookproto.HeaderID, "msg_2"); return h }(), body,
			hookproto.ErrBadSignature},
		{"another secret", signed(t, hookproto.GenerateSecret(), "msg_1", now, body), body, hookproto.ErrBadSignature},
		{"six minutes old", signed(t, secret, "msg_1", now.Add(-6*time.Minute), body), body, hookproto.ErrStaleTimestamp},
		{"six minutes ahead", signed(t, secret, "msg_1", now.Add(6*time.Minute), body), body, hookproto.ErrStaleTimestamp},
		{"a timestamp that is not a number", func() http.Header {
			h := good()
			h.Set(hookproto.HeaderTimestamp, "yesterday")
			return h
		}(), body, hookproto.ErrStaleTimestamp},
		{"only a scheme it does not know", func() http.Header {
			h := good()
			h.Set(hookproto.HeaderSignature, strings.Replace(h.Get(hookproto.HeaderSignature), "v1,", "v2,", 1))
			return h
		}(), body, hookproto.ErrBadSignature},
		{"an empty v1 signature", func() http.Header { h := good(); h.Set(hookproto.HeaderSignature, "v1,"); return h }(),
			body, hookproto.ErrBadSignature},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := verifier(t, now, secret).Verify(c.header, c.body); !errors.Is(err, c.want) {
				t.Errorf("Verify = %v, want %v", err, c.want)
			}
		})
	}
}

// Rotation: the header may carry several signatures, and the receiver may
// trust several secrets; any match is enough. Entries it cannot read are
// ignored, as the reference library does.
func TestVerify_RotationAndEntriesItCannotRead(t *testing.T) {
	oldSecret, newSecret, now := hookproto.GenerateSecret(), hookproto.GenerateSecret(), time.Unix(1_800_000_000, 0)
	body := []byte(`{}`)
	oldSig := signed(t, oldSecret, "msg_1", now, body).Get(hookproto.HeaderSignature)
	newSig := signed(t, newSecret, "msg_1", now, body).Get(hookproto.HeaderSignature)
	h := http.Header{}
	h.Set(hookproto.HeaderID, "msg_1")
	h.Set(hookproto.HeaderTimestamp, strconv.FormatInt(now.Unix(), 10))

	h.Set(hookproto.HeaderSignature, oldSig+" "+newSig)
	if _, err := verifier(t, now, newSecret).Verify(h, body); err != nil {
		t.Errorf("two signatures, the new secret trusted: %v", err)
	}
	h.Set(hookproto.HeaderSignature, oldSig)
	if _, err := verifier(t, now, newSecret, oldSecret).Verify(h, body); err != nil {
		t.Errorf("the old signature, both secrets trusted: %v", err)
	}
	h.Set(hookproto.HeaderSignature, ",,, v1,not,base64  v2a,AAAA "+newSig)
	if _, err := verifier(t, now, newSecret).Verify(h, body); err != nil {
		t.Errorf("unreadable entries before a good one: %v", err)
	}
}

// The webhook-id is derived from the idempotency key: the same key gives
// the same id (it is stable across retries, as Standard Webhooks wants),
// another key another id, and the id never carries the key's text. It
// never contains a '.', which the signed content uses as a separator.
func TestWebhookID(t *testing.T) {
	a, b := hookproto.WebhookID("hold:3f2c9a1e"), hookproto.WebhookID("hold:3f2c9a1e")
	c := hookproto.WebhookID("hook:9d1e:4:get_plan")
	if a == "" || a != b || a == c {
		t.Errorf("WebhookID: %q, %q for one key, %q for another; want equal, then different", a, b, c)
	}
	for _, id := range []string{a, c} {
		if !strings.HasPrefix(id, "msg_") || strings.Contains(id, ".") || strings.Contains(id, "hold") ||
			strings.Contains(id, "3f2c9a1e") || strings.Contains(id, "get_plan") {
			t.Errorf("WebhookID = %q: want msg_<opaque>, no '.', nothing of the key", id)
		}
	}
}
