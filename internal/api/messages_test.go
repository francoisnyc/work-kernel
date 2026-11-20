// Tests for Step 0, part 2 (PR #18 review): error messages are fixed. Every
// error the API returns carries its reason's one message; dynamic values
// (the hold, the scope, the fields, the states) travel in ErrorInfo metadata.
// A field violation's description may name the field, never a value or an
// ID. This test provokes every refusal it can over the wire and checks each.
package api_test

import (
	"context"
	"regexp"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/protobuf/types/known/durationpb"

	kernelv1 "github.com/francoisnyc/work-kernel/gen/kernel/v1"
	"github.com/francoisnyc/work-kernel/internal/api"
)

// idLike matches a UUID, a typed principal, a resource name, a session ID or
// a hold: none may be in a message or a field violation's description.
var idLike = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}|\b(user|agent|group|system|hook):|namespaces/|ses_|holds/`)

func TestMessages_FixedPerReasonAndFreeOfIDs(t *testing.T) {
	ctx := testCtx(t)
	open := create(t, "ana", ticketV1, map[string]any{"f_reporter": "Jane Doe"}).Item.Name
	private := createNarrowed(t, "ana", "user:ana", "agent:triage-bot")
	held := create(t, "ana", ticketV3, map[string]any{"f_customer_id": "cust-1", "f_amount": 100, "f_assignee": "user-bob"}).Item.Name
	transition(t, "ana", held, "open")
	transition(t, "ana", held, "in_progress")
	if _, err := as("ana").TransitionItem(ctx, connect.NewRequest(&kernelv1.TransitionItemRequest{
		Name: held, RequestId: newID(), ToState: "approved", Wait: durationpb.New(0),
	})); err != nil {
		t.Fatalf("approve: %v", err)
	}
	// approve needs role:approver, which bob does not have.
	approvable := create(t, "ana", ticketV3, map[string]any{"f_customer_id": "cust-2", "f_amount": 1, "f_assignee": "user-bob"}).Item.Name
	transition(t, "ana", approvable, "open")
	transition(t, "ana", approvable, "in_progress")
	session := startSession(t, "bot", "user:ana", []string{"namespaces/acme"}, "read", "create")
	bot := asAgent("bot", session)
	if _, err := bot.GetItem(ctx, connect.NewRequest(&kernelv1.GetItemRequest{Name: private})); err != nil {
		t.Fatalf("bot reads: %v", err)
	}
	reused := newID()
	if _, err := as("ana").CreateItem(ctx, connect.NewRequest(&kernelv1.CreateItemRequest{
		Parent: "namespaces/acme", RequestId: reused, Type: ticketV1,
	})); err != nil {
		t.Fatalf("create: %v", err)
	}

	refusals := map[string]func(context.Context) error{
		"UNAUTHENTICATED": func(ctx context.Context) error {
			_, err := withToken("tok-nobody").GetItem(ctx, connect.NewRequest(&kernelv1.GetItemRequest{Name: open}))
			return err
		},
		"SESSION_INVALID": func(ctx context.Context) error {
			_, err := asAgent("bot", "ses_made-up").GetItem(ctx, connect.NewRequest(&kernelv1.GetItemRequest{Name: open}))
			return err
		},
		"INVALID_REQUEST (header)": func(ctx context.Context) error {
			_, err := asAgent("bot", "").GetItem(ctx, connect.NewRequest(&kernelv1.GetItemRequest{Name: open}))
			return err
		},
		"INVALID_REQUEST (field value)": func(ctx context.Context) error {
			_, err := as("ana").UpdateItem(ctx, connect.NewRequest(&kernelv1.UpdateItemRequest{
				Name: open, RequestId: newID(), Fields: fields(t, map[string]any{"f_priority": "Jane Doe"}),
			}))
			return err
		},
		"INVALID_REQUEST (readers)": func(ctx context.Context) error {
			_, err := as("ana").UpdateItem(ctx, connect.NewRequest(&kernelv1.UpdateItemRequest{
				Name: open, RequestId: newID(),
				Policy: &kernelv1.PolicyChange{Change: &kernelv1.PolicyChange_Narrow{Narrow: &kernelv1.Readers{Principals: []string{"user:bob"}}}},
			}))
			return err
		},
		"INVALID_REQUEST (page token)": func(ctx context.Context) error {
			_, err := as("ana").ListItems(ctx, connect.NewRequest(&kernelv1.ListItemsRequest{Parent: open, PageToken: "garbage"}))
			return err
		},
		"NOT_FOUND": func(ctx context.Context) error {
			_, err := as("bob").GetItem(ctx, connect.NewRequest(&kernelv1.GetItemRequest{Name: private}))
			return err
		},
		"NOT_ALLOWED": func(ctx context.Context) error {
			_, err := as("bob").TransitionItem(ctx, connect.NewRequest(&kernelv1.TransitionItemRequest{
				Name: approvable, RequestId: newID(), ToState: "approved",
			}))
			return err
		},
		"NEEDS_APPROVAL": func(ctx context.Context) error {
			_, err := bot.CreateItem(ctx, connect.NewRequest(&kernelv1.CreateItemRequest{
				Parent: "namespaces/acme", RequestId: newID(), Type: ticketV1, Container: open,
			}))
			return err
		},
		"GUARD_FAILED": func(ctx context.Context) error {
			_, err := as("ana").TransitionItem(ctx, connect.NewRequest(&kernelv1.TransitionItemRequest{
				Name: open, RequestId: newID(), ToState: "in_progress",
			}))
			return err
		},
		"NO_TRANSITION": func(ctx context.Context) error {
			_, err := as("ana").TransitionItem(ctx, connect.NewRequest(&kernelv1.TransitionItemRequest{
				Name: open, RequestId: newID(), ToState: "resolved",
			}))
			return err
		},
		"STALE": func(ctx context.Context) error {
			_, err := as("ana").UpdateItem(ctx, connect.NewRequest(&kernelv1.UpdateItemRequest{
				Name: open, RequestId: newID(), Etag: "c99", Fields: fields(t, map[string]any{"f_priority": 1}),
			}))
			return err
		},
		"HELD": func(ctx context.Context) error {
			_, err := as("ana").UpdateItem(ctx, connect.NewRequest(&kernelv1.UpdateItemRequest{
				Name: held, RequestId: newID(), Fields: fields(t, map[string]any{"f_amount": 5}),
			}))
			return err
		},
		"IDEMPOTENCY_KEY_REUSED": func(ctx context.Context) error {
			_, err := as("ana").CreateItem(ctx, connect.NewRequest(&kernelv1.CreateItemRequest{
				Parent: "namespaces/acme", RequestId: reused, Type: ticketV3,
			}))
			return err
		},
		"NOT_CAUGHT_UP": func(ctx context.Context) error {
			_, err := as("ana").SearchItems(ctx, connect.NewRequest(&kernelv1.SearchItemsRequest{
				Parent: "namespaces/acme", Query: "x", MinSeq: 1 << 50,
			}))
			return err
		},
	}
	for name, call := range refusals {
		t.Run(name, func(t *testing.T) {
			err := call(testCtx(t))
			ce, ok := err.(*connect.Error)
			if !ok {
				t.Fatalf("error = %v, want a refusal", err)
			}
			info := errorInfo(t, ce)
			if info == nil {
				t.Fatalf("no ErrorInfo in %v", ce)
			}
			want, known := api.ReasonMessage(info.Reason)
			if !known || ce.Message() != want {
				t.Errorf("%s: message %q, want the fixed %q (known %v)", info.Reason, ce.Message(), want, known)
			}
			if idLike.MatchString(ce.Message()) {
				t.Errorf("%s: message %q contains an ID", info.Reason, ce.Message())
			}
			for _, d := range ce.Details() {
				v, _ := d.Value()
				if br, ok := v.(*errdetails.BadRequest); ok {
					for _, fv := range br.FieldViolations {
						if idLike.MatchString(fv.Description) || regexp.MustCompile(`Jane Doe`).MatchString(fv.Description) {
							t.Errorf("%s: field violation %q: %q names an ID or a value", info.Reason, fv.Field, fv.Description)
						}
					}
				}
			}
		})
	}
}

// The metadata carries what the message no longer does.
func TestMessages_DetailsInMetadata(t *testing.T) {
	ctx := testCtx(t)
	open := create(t, "ana", ticketV1, nil).Item.Name
	_, err := as("ana").TransitionItem(ctx, connect.NewRequest(&kernelv1.TransitionItemRequest{
		Name: open, RequestId: newID(), ToState: "in_progress",
	}))
	ce := apiError(t, "guard", err, connect.CodeFailedPrecondition, "GUARD_FAILED")
	if md := errorInfo(t, ce).GetMetadata(); md["from_state"] != "open" || md["to_state"] != "in_progress" {
		t.Errorf("GUARD_FAILED metadata = %v, want from_state open, to_state in_progress", md)
	}

	working := create(t, "ana", ticketV1, map[string]any{"f_assignee": "user-bob"}).Item.Name
	transition(t, "ana", working, "in_progress")
	_, err = as("ana").TransitionItem(ctx, connect.NewRequest(&kernelv1.TransitionItemRequest{
		Name: working, RequestId: newID(), ToState: "resolved",
	}))
	ce = apiError(t, "missing fields", err, connect.CodeFailedPrecondition, "MISSING_FIELDS")
	if md := errorInfo(t, ce).GetMetadata(); md["fields"] != "f_resolution" {
		t.Errorf("MISSING_FIELDS metadata = %v, want fields = f_resolution", md)
	}
}
