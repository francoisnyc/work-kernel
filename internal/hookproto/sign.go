package hookproto

import (
	"errors"
	"net/http"
	"time"
)

// Standard Webhooks v1.0.0 signing (symmetric, v1: HMAC-SHA256 over
// "id.timestamp.body"), from the standard library only. The kernel signs
// every request to a connector; the connector verifies it. Each connection
// has its own secret, in whsec_<base64> form.

// Headers.
const (
	HeaderID        = "webhook-id"
	HeaderTimestamp = "webhook-timestamp"
	HeaderSignature = "webhook-signature"
)

// Tolerance is how far a request's timestamp may be from the receiver's
// clock, either way.
const Tolerance = 5 * time.Minute

var (
	ErrBadSecret      = errors.New("hookproto: not a whsec_ secret")
	ErrNoSignature    = errors.New("hookproto: no webhook signature headers")
	ErrBadSignature   = errors.New("hookproto: no signature matches")
	ErrStaleTimestamp = errors.New("hookproto: timestamp outside the tolerance")
)

// Secret is a parsed signing secret.
type Secret struct {
	key []byte
}

// ParseSecret reads a whsec_<base64> secret.
func ParseSecret(s string) (Secret, error) {
	return Secret{}, ErrNotImplemented
}

// GenerateSecret returns a new random secret in whsec_ form (32 bytes).
func GenerateSecret() string {
	return ""
}

// WebhookID derives the webhook-id from the idempotency key: stable
// across retries, as Standard Webhooks requires, and without the key's
// text.
func WebhookID(idempotencyKey string) string {
	return ""
}

// Sign returns the v1 signature ("v1,<base64>") of body, sent with id at ts.
func Sign(s Secret, id string, ts time.Time, body []byte) string {
	return ""
}

// SetSignatureHeaders sets the three webhook headers on h.
func SetSignatureHeaders(h http.Header, s Secret, id string, ts time.Time, body []byte) {
}

// Verifier checks a request's signature and timestamp. It accepts the
// request if any v1 signature in the header matches any of its secrets
// (rotation); it ignores schemes and entries it cannot read.
type Verifier struct {
	Secrets   []Secret
	Tolerance time.Duration    // zero: Tolerance
	Now       func() time.Time // nil: time.Now
}

// Verify returns the webhook-id, or ErrNoSignature, ErrStaleTimestamp or
// ErrBadSignature.
func (v Verifier) Verify(h http.Header, body []byte) (string, error) {
	return "", ErrNotImplemented
}
