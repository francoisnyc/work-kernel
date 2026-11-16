// Tests for proof-of-concept Step 2, part 2: the new refusals in the error
// table. Each reason has one fixed message; what it names (the invariant,
// the link type, the resolution's code) goes in ErrorInfo metadata.
package api

import (
	"maps"
	"testing"

	"connectrpc.com/connect"

	"github.com/francoisnyc/work-kernel/internal/itemservice"
)

func TestErrorFor_ReactionRefusals(t *testing.T) {
	const itemName = "namespaces/acme/items/0190c0de-0000-7000-8000-000000000001"
	cases := []struct {
		name   string
		err    error
		code   connect.Code
		reason string
		md     map[string]string
	}{
		{"an invariant", &itemservice.RejectedError{Code: itemservice.CodeInvariantFailed,
			Reason: "the write breaks insufficient_balance", Metadata: map[string]string{"invariant": "insufficient_balance"}},
			connect.CodeFailedPrecondition, "INVARIANT_FAILED", map[string]string{"invariant": "insufficient_balance"}},
		{"a kernel-set link", &itemservice.LinkNotWritableError{LinkType: "balance"},
			connect.CodePermissionDenied, "LINK_NOT_WRITABLE", map[string]string{"link_type": "balance"}},
		{"a link the kernel could not resolve", &itemservice.RejectedError{Code: itemservice.CodeLinkUnresolved,
			Reason: "no jar", Metadata: map[string]string{"link_type": "jar", "reason": "no_jar"}},
			connect.CodeFailedPrecondition, "LINK_UNRESOLVED", map[string]string{"link_type": "jar", "reason": "no_jar"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ce := errorFor(c.err, itemName)
			if ce.Code() != c.code {
				t.Errorf("code = %v, want %v", ce.Code(), c.code)
			}
			ei, _ := info(t, ce)
			if ei.Reason != c.reason || ei.Domain != ErrorDomain {
				t.Errorf("ErrorInfo = %s/%s, want %s/%s", ei.Domain, ei.Reason, ErrorDomain, c.reason)
			}
			if !maps.Equal(ei.Metadata, c.md) {
				t.Errorf("metadata = %v, want %v", ei.Metadata, c.md)
			}
			msg, ok := ReasonMessage(c.reason)
			if !ok || ce.Message() != msg {
				t.Errorf("message = %q, want the reason's fixed message %q (known: %v)", ce.Message(), msg, ok)
			}
		})
	}
}
