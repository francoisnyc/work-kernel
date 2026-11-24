// Tests for starting work, step 1 (docs/plans/starting-work.md, 1.0), over
// the wire: an agent session without the create power may not CreateItem.
// The answer is PERMISSION_DENIED / NOT_ALLOWED, exactly what a read-only
// session gets: the reason is in the item service's message only, and the
// API sends the reason's fixed message (no contract change).
package api_test

import (
	"bytes"
	"testing"

	"connectrpc.com/connect"

	kernelv1 "github.com/francoisnyc/work-kernel/gen/kernel/v1"
)

func TestAgentSession_CreateNeedsTheCreatePower(t *testing.T) {
	ctx := testCtx(t)
	createAsBot := func(session string) error {
		_, err := asAgent("bot", session).CreateItem(ctx, connect.NewRequest(&kernelv1.CreateItemRequest{
			Parent: "namespaces/acme", RequestId: newID(), Type: ticketV1,
		}))
		return err
	}
	editor := startSession(t, "bot", "user:ana", []string{"namespaces/acme"}, "read", "edit_fields")
	errEditor := createAsBot(editor)
	apiError(t, "CreateItem with read,edit_fields", errEditor, connect.CodePermissionDenied, "NOT_ALLOWED")

	reader := startSession(t, "bot", "user:ana", []string{"namespaces/acme"}, "read")
	errReader := createAsBot(reader)
	apiError(t, "CreateItem read-only", errReader, connect.CodePermissionDenied, "NOT_ALLOWED")
	if !bytes.Equal(errBytes(t, errEditor), errBytes(t, errReader)) {
		t.Errorf("without create and read-only differ over the API:\n%q\n%q", errBytes(t, errEditor), errBytes(t, errReader))
	}

	// The same session still updates: any write power, as before (a
	// ticket@7's priority, which it opens to agents).
	it := create(t, "ana", ticketV7, nil).Item.Name
	if _, err := asAgent("bot", editor).UpdateItem(ctx, connect.NewRequest(&kernelv1.UpdateItemRequest{
		Name: it, RequestId: newID(), Fields: fields(t, map[string]any{"f_priority": 1}),
	})); err != nil {
		t.Errorf("UpdateItem with read,edit_fields: %v", err)
	}
}
