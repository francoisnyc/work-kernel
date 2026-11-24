// Tests for starting work, step 2 (docs/plans/starting-work.md, 1.3 and
// 1.6), over the wire, on the repository's Types: incident@4's start
// block, in: [desk], judges a create of every incident version. A staff
// member opens an incident in a desk as before, never at the top level
// or in another container. The refusal is PERMISSION_DENIED / NOT_ALLOWED
// with the reason's fixed message: START_NOT_IN stays in the item
// service's message (no contract change), so over the API it reads
// exactly as any other NOT_ALLOWED.
package api_test

import (
	"bytes"
	"testing"

	"connectrpc.com/connect"

	kernelv1 "github.com/francoisnyc/work-kernel/gen/kernel/v1"
)

func TestStart_AnIncidentOnlyInADesk(t *testing.T) {
	ctx := testCtx(t)
	desk := mkIn(t, "ana", deskV1, "", map[string]any{"f_key": "IT", "f_name": "IT " + word()})
	ticket := create(t, "ana", ticketV1, nil).Item.Name
	open := func(typ, container string) error {
		_, err := as("ana").CreateItem(ctx, connect.NewRequest(&kernelv1.CreateItemRequest{
			Parent: "namespaces/acme", RequestId: newID(), Type: typ, Container: container,
			Fields: fields(t, map[string]any{"f_title": "VPN " + word(), "f_priority": "P3"}),
		}))
		return err
	}
	for _, typ := range []string{incidentV1, incidentV2, "namespaces/acme/types/incident/versions/3", "namespaces/acme/types/incident/versions/4"} {
		if err := open(typ, desk); err != nil {
			t.Errorf("ana opens %s in a desk: %v", typ, err)
		}
		apiError(t, "ana opens "+typ+" at the top level", open(typ, ""), connect.CodePermissionDenied, "NOT_ALLOWED")
		apiError(t, "ana opens "+typ+" in a ticket", open(typ, ticket), connect.CodePermissionDenied, "NOT_ALLOWED")
	}

	// The reason stays inside: the bytes are those of a read-only agent
	// session's refusal.
	top := open(incidentV1, "")
	reader := startSession(t, "bot", "user:ana", []string{"namespaces/acme"}, "read")
	_, readOnly := asAgent("bot", reader).CreateItem(ctx, connect.NewRequest(&kernelv1.CreateItemRequest{
		Parent: "namespaces/acme", RequestId: newID(), Type: ticketV1,
	}))
	apiError(t, "a read-only session creates", readOnly, connect.CodePermissionDenied, "NOT_ALLOWED")
	if top != nil && !bytes.Equal(errBytes(t, top), errBytes(t, readOnly)) {
		t.Errorf("an incident at the top level and a read-only create differ over the API:\n%q\n%q",
			errBytes(t, top), errBytes(t, readOnly))
	}
}
