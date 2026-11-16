// Tests for proof-of-concept Step 2, part 1: the two new refusals in the
// error table. Each reason has one fixed message; the fields and the state
// go in ErrorInfo metadata.
package api

import (
	"maps"
	"testing"

	"connectrpc.com/connect"

	"github.com/francoisnyc/work-kernel/internal/itemservice"
)

func TestErrorFor_FieldRefusals(t *testing.T) {
	const itemName = "namespaces/acme/items/0190c0de-0000-7000-8000-000000000001"
	cases := []struct {
		name   string
		err    error
		code   connect.Code
		reason string
		md     map[string]string
	}{
		{"kernel-written field", &itemservice.FieldNotWritableError{Fields: []string{"f_approver", "f_owner"},
			Reason: "f_approver, f_owner are written by the kernel"},
			connect.CodePermissionDenied, "FIELD_NOT_WRITABLE", map[string]string{"fields": "f_approver,f_owner"}},
		{"frozen field", &itemservice.RejectedError{Code: itemservice.CodeFieldFrozen, Reason: "f_days is frozen in submitted",
			Metadata: map[string]string{"fields": "f_days", "state": "submitted"}},
			connect.CodeFailedPrecondition, "FIELD_FROZEN", map[string]string{"fields": "f_days", "state": "submitted"}},
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
