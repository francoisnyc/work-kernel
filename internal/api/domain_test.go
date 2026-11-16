// Tests for Step 0, part 3: the ErrorInfo domain is frozen. Clients tell
// errors apart by (reason, domain), so the domain never changes from here
// on: this test makes a change deliberate. It is a label, never resolved.
package api_test

import (
	"testing"

	"connectrpc.com/connect"

	kernelv1 "github.com/francoisnyc/work-kernel/gen/kernel/v1"
	"github.com/francoisnyc/work-kernel/internal/api"
)

const frozenDomain = "pli.francoisnyc.github.io"

func TestErrorDomain_IsFrozen(t *testing.T) {
	if api.ErrorDomain != frozenDomain {
		t.Fatalf("api.ErrorDomain = %q, want the frozen %q", api.ErrorDomain, frozenDomain)
	}
	// And it is what goes over the wire.
	_, err := withToken("tok-nobody").ListTypes(testCtx(t), connect.NewRequest(&kernelv1.ListTypesRequest{Parent: "namespaces/acme"}))
	ce := apiError(t, "bad token", err, connect.CodeUnauthenticated, "UNAUTHENTICATED")
	if d := errorInfo(t, ce).GetDomain(); d != frozenDomain {
		t.Errorf("ErrorInfo domain over the wire = %q, want %q", d, frozenDomain)
	}
}

// AUDIENCE_CHANGED is a known reason with a fixed message.
func TestReasonMessage_AudienceChanged(t *testing.T) {
	msg, ok := api.ReasonMessage("AUDIENCE_CHANGED")
	if !ok || msg == "" {
		t.Fatal("AUDIENCE_CHANGED has no fixed message")
	}
	if idLike.MatchString(msg) {
		t.Errorf("message %q contains something like an ID", msg)
	}
}
