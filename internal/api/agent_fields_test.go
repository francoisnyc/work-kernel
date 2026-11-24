// Tests for fields closed to agents by default, over the wire: the refusal
// is the existing PERMISSION_DENIED / FIELD_NOT_WRITABLE with the fields in
// its metadata (no contract change). ticket@7 opens f_priority to agents
// and nothing else.
package api_test

import (
	"testing"

	"connectrpc.com/connect"

	kernelv1 "github.com/francoisnyc/work-kernel/gen/kernel/v1"
)

func TestAgentFields_API_ClosedFieldIsNotWritable(t *testing.T) {
	ctx := testCtx(t)
	it := create(t, "ana", ticketV7, map[string]any{"f_title": "printer " + word()}).Item.Name
	bot := asAgent("bot", startSession(t, "bot", "user:ana", []string{"namespaces/acme"}, "read", "create", "edit_fields"))

	_, err := bot.UpdateItem(ctx, connect.NewRequest(&kernelv1.UpdateItemRequest{
		Name: it, RequestId: newID(), Fields: fields(t, map[string]any{"f_title": "renamed by the bot", "f_priority": 1}),
	}))
	ce := apiError(t, "the bot retitles a ticket@7", err, connect.CodePermissionDenied, "FIELD_NOT_WRITABLE")
	if md := metadata(t, ce); md["fields"] != "f_title" {
		t.Errorf("metadata = %v, want fields f_title", md)
	}
	_, err = bot.CreateItem(ctx, connect.NewRequest(&kernelv1.CreateItemRequest{
		Parent: "namespaces/acme", RequestId: newID(), Type: ticketV7, Fields: fields(t, map[string]any{"f_title": "by the bot"}),
	}))
	ce = apiError(t, "the bot creates a titled ticket@7", err, connect.CodePermissionDenied, "FIELD_NOT_WRITABLE")
	if md := metadata(t, ce); md["fields"] != "f_title" {
		t.Errorf("metadata = %v, want fields f_title", md)
	}

	if _, err := bot.UpdateItem(ctx, connect.NewRequest(&kernelv1.UpdateItemRequest{
		Name: it, RequestId: newID(), Fields: fields(t, map[string]any{"f_priority": 1}),
	})); err != nil {
		t.Errorf("the bot sets the priority: %v", err)
	}
	if got := get(t, "ana", it).Fields.AsMap()["f_title"]; got == "renamed by the bot" {
		t.Errorf("f_title = %v, want ana's", got)
	}
}
