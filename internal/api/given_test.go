// Track 1 item 3, "required means given by the mover": a required field
// the move must give and does not is refused with the existing reason,
// MISSING_FIELDS (FAILED_PRECONDITION, metadata fields). Its one fixed
// message now says both cases (the owner's decision, Phase A): the field is
// not set, or it is set but was not given in the move.
package api_test

import (
	"testing"

	"connectrpc.com/connect"

	kernelv1 "github.com/francoisnyc/work-kernel/gen/kernel/v1"
	"github.com/francoisnyc/work-kernel/internal/api"
)

const missingFieldsMessage = "the transition needs fields that are not set or not given in the move"

func TestGiven_MissingFieldsMessage(t *testing.T) {
	msg, ok := api.ReasonMessage("MISSING_FIELDS")
	if !ok || msg != missingFieldsMessage {
		t.Errorf("MISSING_FIELDS's message = %q (known %v), want %q", msg, ok, missingFieldsMessage)
	}

	// And it is what goes over the wire, with the fields in the metadata.
	working := create(t, "ana", ticketV1, map[string]any{"f_assignee": "user-bob"}).Item.Name
	transition(t, "ana", working, "in_progress")
	_, err := as("ana").TransitionItem(testCtx(t), connect.NewRequest(&kernelv1.TransitionItemRequest{
		Name: working, RequestId: newID(), ToState: "resolved",
	}))
	ce := apiError(t, "resolved without a resolution", err, connect.CodeFailedPrecondition, "MISSING_FIELDS")
	if ce.Message() != missingFieldsMessage {
		t.Errorf("message over the wire = %q, want %q", ce.Message(), missingFieldsMessage)
	}
	if md := errorInfo(t, ce).GetMetadata(); md["fields"] != "f_resolution" {
		t.Errorf("metadata = %v, want fields = f_resolution", md)
	}
}
