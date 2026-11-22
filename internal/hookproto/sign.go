package hookproto

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
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

const secretPrefix = "whsec_"

// ParseSecret reads a whsec_<base64> secret.
func ParseSecret(s string) (Secret, error) {
	rest, ok := strings.CutPrefix(s, secretPrefix)
	if !ok {
		return Secret{}, fmt.Errorf("%w: no %s prefix", ErrBadSecret, secretPrefix)
	}
	key, err := base64.StdEncoding.DecodeString(rest)
	if err != nil || len(key) == 0 {
		return Secret{}, fmt.Errorf("%w: the part after %s is not base64 of a key", ErrBadSecret, secretPrefix)
	}
	return Secret{key: key}, nil
}

// GenerateSecret returns a new random secret in whsec_ form (32 bytes).
func GenerateSecret() string {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		panic(err) // crypto/rand does not fail on supported platforms
	}
	return secretPrefix + base64.StdEncoding.EncodeToString(key)
}

// WebhookID derives the webhook-id from the idempotency key: stable
// across retries, as Standard Webhooks requires, and without the key's
// text.
func WebhookID(idempotencyKey string) string {
	sum := sha256.Sum256([]byte(idempotencyKey))
	return "msg_" + hex.EncodeToString(sum[:16])
}

// Sign returns the v1 signature ("v1,<base64>") of body, sent with id at ts.
func Sign(s Secret, id string, ts time.Time, body []byte) string {
	return "v1," + base64.StdEncoding.EncodeToString(mac(s, id, ts.Unix(), body))
}

// mac is HMAC-SHA256 of "id.timestamp.body".
func mac(s Secret, id string, ts int64, body []byte) []byte {
	h := hmac.New(sha256.New, s.key)
	h.Write([]byte(id + "." + strconv.FormatInt(ts, 10) + "."))
	h.Write(body)
	return h.Sum(nil)
}

// SetSignatureHeaders sets the three webhook headers on h.
func SetSignatureHeaders(h http.Header, s Secret, id string, ts time.Time, body []byte) {
	h.Set(HeaderID, id)
	h.Set(HeaderTimestamp, strconv.FormatInt(ts.Unix(), 10))
	h.Set(HeaderSignature, Sign(s, id, ts, body))
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
	id, tsText, sigs := h.Get(HeaderID), h.Get(HeaderTimestamp), h.Get(HeaderSignature)
	if id == "" || tsText == "" || sigs == "" {
		return "", ErrNoSignature
	}
	ts, err := strconv.ParseInt(tsText, 10, 64)
	if err != nil {
		return "", fmt.Errorf("%w: %q is not a unix time", ErrStaleTimestamp, tsText)
	}
	now, tolerance := time.Now(), v.Tolerance
	if v.Now != nil {
		now = v.Now()
	}
	if tolerance == 0 {
		tolerance = Tolerance
	}
	if skew := now.Sub(time.Unix(ts, 0)); skew > tolerance || skew < -tolerance {
		return "", fmt.Errorf("%w: %v away", ErrStaleTimestamp, skew)
	}
	for _, entry := range strings.Fields(sigs) {
		scheme, encoded, ok := strings.Cut(entry, ",")
		if !ok || scheme != "v1" {
			continue // a scheme this side does not verify, or not an entry at all
		}
		got, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			continue
		}
		for _, s := range v.Secrets {
			// hmac.Equal compares in constant time.
			if hmac.Equal(got, mac(s, id, ts, body)) {
				return id, nil
			}
		}
	}
	return "", ErrBadSignature
}
