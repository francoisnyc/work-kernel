// Tests for Step 0, part 1: who is calling comes from the bearer token, on
// every operation. No token, an unknown token or another scheme is
// UNAUTHENTICATED / UNAUTHENTICATED, before anything else is looked at.
package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"connectrpc.com/connect"

	kernelv1 "github.com/francoisnyc/work-kernel/gen/kernel/v1"
	"github.com/francoisnyc/work-kernel/gen/kernel/v1/kernelv1connect"
)

// calls makes every operation once with a well-formed request.
func calls(c kernelv1connect.ItemServiceClient, item string) map[string]func(context.Context) error {
	return map[string]func(context.Context) error{
		"CreateItem": func(ctx context.Context) error {
			_, err := c.CreateItem(ctx, connect.NewRequest(&kernelv1.CreateItemRequest{
				Parent: "namespaces/acme", RequestId: newID(), Type: ticketV1,
			}))
			return err
		},
		"GetItem": func(ctx context.Context) error {
			_, err := c.GetItem(ctx, connect.NewRequest(&kernelv1.GetItemRequest{Name: item}))
			return err
		},
		"UpdateItem": func(ctx context.Context) error {
			_, err := c.UpdateItem(ctx, connect.NewRequest(&kernelv1.UpdateItemRequest{
				Name: item, RequestId: newID(), Move: &kernelv1.Move{},
			}))
			return err
		},
		"TransitionItem": func(ctx context.Context) error {
			_, err := c.TransitionItem(ctx, connect.NewRequest(&kernelv1.TransitionItemRequest{
				Name: item, RequestId: newID(), ToState: "canceled",
			}))
			return err
		},
		"SearchItems": func(ctx context.Context) error {
			_, err := c.SearchItems(ctx, connect.NewRequest(&kernelv1.SearchItemsRequest{
				Parent: "namespaces/acme", Query: "x",
			}))
			return err
		},
		"GetType": func(ctx context.Context) error {
			_, err := c.GetType(ctx, connect.NewRequest(&kernelv1.GetTypeRequest{Name: ticketV1}))
			return err
		},
		"ListTypes": func(ctx context.Context) error {
			_, err := c.ListTypes(ctx, connect.NewRequest(&kernelv1.ListTypesRequest{Parent: "namespaces/acme"}))
			return err
		},
		// Step 0, part 2.
		"ListItems": func(ctx context.Context) error {
			_, err := c.ListItems(ctx, connect.NewRequest(&kernelv1.ListItemsRequest{Parent: item}))
			return err
		},
		"ListItemEvents": func(ctx context.Context) error {
			_, err := c.ListItemEvents(ctx, connect.NewRequest(&kernelv1.ListItemEventsRequest{Parent: item}))
			return err
		},
		"StartAgentSession": func(ctx context.Context) error {
			_, err := c.StartAgentSession(ctx, connect.NewRequest(&kernelv1.StartAgentSessionRequest{
				Parent: "namespaces/acme", ActingFor: "user:ana", Scopes: []string{"namespaces/acme"}, Powers: []string{"read"},
			}))
			return err
		},
		"ApproveWiden": func(ctx context.Context) error {
			_, err := c.ApproveWiden(ctx, connect.NewRequest(&kernelv1.ApproveWidenRequest{
				Parent: "namespaces/acme", Session: "ses_x", Scope: "namespaces/acme",
			}))
			return err
		},
	}
}

func TestAuth_EveryOperationNeedsAToken(t *testing.T) {
	name := create(t, "ana", ticketV1, nil).Item.Name

	clients := map[string]kernelv1connect.ItemServiceClient{
		"no token":         withToken(""),
		"unknown token":    withToken("tok-nobody"),
		"a token's prefix": withToken("tok-an"),
		"basic scheme": kernelv1connect.NewItemServiceClient(srv.Client(), srv.URL, connect.WithProtoJSON(),
			connect.WithInterceptors(connect.UnaryInterceptorFunc(
				func(next connect.UnaryFunc) connect.UnaryFunc {
					return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
						req.Header().Set("Authorization", "Basic dG9rLWFuYQ==")
						return next(ctx, req)
					}
				}))),
	}
	for what, c := range clients {
		for op, call := range calls(c, name) {
			err := call(testCtx(t))
			apiError(t, what+": "+op, err, connect.CodeUnauthenticated, "UNAUTHENTICATED")
		}
	}
	if n := countEvents(t, name); n != 1 {
		t.Errorf("events = %d, want 1: an unauthenticated write wrote", n)
	}
}

// The scheme is case-insensitive (RFC 9110); the token is not.
func TestAuth_BearerScheme(t *testing.T) {
	name := create(t, "ana", ticketV1, nil).Item.Name
	get := func(header string) int {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/kernel.v1.ItemService/GetItem",
			strings.NewReader(`{"name":"`+name+`"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", header)
		res, err := srv.Client().Do(req)
		if err != nil {
			t.Fatalf("POST: %v", err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	if code := get("bearer tok-ana"); code != http.StatusOK {
		t.Errorf("lower-case scheme: HTTP %d, want 200", code)
	}
	if code := get("Bearer TOK-ANA"); code != http.StatusUnauthorized {
		t.Errorf("token in another case: HTTP %d, want 401", code)
	}
}
