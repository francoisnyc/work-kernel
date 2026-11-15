// Tests for Step 0, part 1: two namespaces that both exist leak nothing to
// each other. A caller in acme gets NOT_FOUND on every operation that touches
// globex, whether the name says globex or claims acme, and the answer cannot
// be told apart from an Item that does not exist. (List and history join this
// test in part 2, when they exist.)
package api_test

import (
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	kernelv1 "github.com/francoisnyc/work-kernel/gen/kernel/v1"
)

func TestNamespaces_NothingCrossesOver(t *testing.T) {
	ctx := testCtx(t)
	w := word()

	// bea, in globex, makes a ticket and a channel there.
	res, err := as("bea").CreateItem(ctx, connect.NewRequest(&kernelv1.CreateItemRequest{
		Parent: "namespaces/globex", RequestId: newID(), Type: "namespaces/globex/types/ticket/versions/1",
		Fields: fields(t, map[string]any{"f_title": w, "f_assignee": "user-bob"}),
	}))
	if err != nil {
		t.Fatalf("bea creates in globex: %v", err)
	}
	theirs := res.Msg.Item.Name
	if !strings.HasPrefix(theirs, "namespaces/globex/items/") {
		t.Fatalf("bea's Item is %s, want it under namespaces/globex", theirs)
	}
	// The same Item ID, named as if it were in acme.
	disguised := "namespaces/acme/items/" + itemID(t, theirs).String()
	missing := "namespaces/acme/items/" + uuid.NewString()
	mine := create(t, "ana", ticketV1, nil).Item.Name

	// Both callers in acme: ana, and sam, who also has a token in globex.
	for _, who := range []string{"ana", "sam-acme"} {
		c := as(who)
		for _, name := range []string{theirs, disguised} {
			_, err := c.GetItem(ctx, connect.NewRequest(&kernelv1.GetItemRequest{Name: name}))
			_, ghost := c.GetItem(ctx, connect.NewRequest(&kernelv1.GetItemRequest{Name: missing}))
			assertSameNotFound(t, err, ghost)

			_, err = c.UpdateItem(ctx, connect.NewRequest(&kernelv1.UpdateItemRequest{
				Name: name, RequestId: newID(), Fields: fields(t, map[string]any{"f_title": "mine now"}),
			}))
			notFound(t, who+" UpdateItem "+name, err)

			_, err = c.UpdateItem(ctx, connect.NewRequest(&kernelv1.UpdateItemRequest{
				Name: name, RequestId: newID(),
				Policy: &kernelv1.PolicyChange{Change: &kernelv1.PolicyChange_Inherit{Inherit: true}},
			}))
			notFound(t, who+" UpdateItem policy "+name, err)

			_, err = c.TransitionItem(ctx, connect.NewRequest(&kernelv1.TransitionItemRequest{
				Name: name, RequestId: newID(), ToState: "in_progress",
			}))
			notFound(t, who+" TransitionItem "+name, err)

			_, err = c.CreateItem(ctx, connect.NewRequest(&kernelv1.CreateItemRequest{
				Parent: "namespaces/acme", RequestId: newID(), Type: ticketV1, Container: name,
			}))
			notFound(t, who+" CreateItem into "+name, err)

			_, err = c.UpdateItem(ctx, connect.NewRequest(&kernelv1.UpdateItemRequest{
				Name: mine, RequestId: newID(), Move: &kernelv1.Move{Container: name},
			}))
			notFound(t, who+" move into "+name, err)
		}

		_, err := c.CreateItem(ctx, connect.NewRequest(&kernelv1.CreateItemRequest{
			Parent: "namespaces/globex", RequestId: newID(), Type: "namespaces/globex/types/ticket/versions/1",
		}))
		notFound(t, who+" CreateItem in globex", err)

		_, err = c.CreateItem(ctx, connect.NewRequest(&kernelv1.CreateItemRequest{
			Parent: "namespaces/acme", RequestId: newID(), Type: "namespaces/globex/types/ticket/versions/1",
		}))
		notFound(t, who+" CreateItem with a globex Type name", err)

		_, err = c.SearchItems(ctx, connect.NewRequest(&kernelv1.SearchItemsRequest{
			Parent: "namespaces/globex", Query: w,
		}))
		notFound(t, who+" SearchItems in globex", err)

		found, err := c.SearchItems(ctx, connect.NewRequest(&kernelv1.SearchItemsRequest{
			Parent: "namespaces/acme", Query: w,
		}))
		if err != nil || len(found.Msg.Items) != 0 {
			t.Errorf("%s SearchItems in acme for globex's word: %v, %v; want nothing", who, found, err)
		}

		_, err = c.GetType(ctx, connect.NewRequest(&kernelv1.GetTypeRequest{
			Name: "namespaces/globex/types/ticket/versions/1",
		}))
		notFound(t, who+" GetType in globex", err)

		_, err = c.ListTypes(ctx, connect.NewRequest(&kernelv1.ListTypesRequest{Parent: "namespaces/globex"}))
		notFound(t, who+" ListTypes in globex", err)

		_, err = c.GetType(ctx, connect.NewRequest(&kernelv1.GetTypeRequest{
			Name: "namespaces/nowhere/types/ticket/versions/1",
		}))
		notFound(t, who+" GetType in a namespace that does not exist", err)
	}

	// Nothing above touched bea's Item.
	it, err := as("bea").GetItem(ctx, connect.NewRequest(&kernelv1.GetItemRequest{Name: theirs}))
	if err != nil {
		t.Fatalf("bea reads her Item: %v", err)
	}
	if it.Msg.Item.State != "open" || it.Msg.Item.Fields.AsMap()["f_title"] != w || countEvents(t, theirs) != 1 {
		t.Errorf("bea's Item changed: %v", it.Msg.Item)
	}
	// And sam in globex sees it: one principal ID, two principals.
	if _, err := as("sam-globex").GetItem(ctx, connect.NewRequest(&kernelv1.GetItemRequest{Name: theirs})); err != nil {
		t.Errorf("sam in globex: %v", err)
	}
}

// A request_id is scoped by namespace and caller: sam's key in acme does not
// replay sam's write in globex.
func TestNamespaces_RequestIDsDoNotCross(t *testing.T) {
	ctx := testCtx(t)
	id := newID()
	a, err := as("sam-acme").CreateItem(ctx, connect.NewRequest(&kernelv1.CreateItemRequest{
		Parent: "namespaces/acme", RequestId: id, Type: ticketV1,
	}))
	if err != nil {
		t.Fatalf("sam in acme: %v", err)
	}
	g, err := as("sam-globex").CreateItem(ctx, connect.NewRequest(&kernelv1.CreateItemRequest{
		Parent: "namespaces/globex", RequestId: id, Type: "namespaces/globex/types/ticket/versions/1",
	}))
	if err != nil {
		t.Fatalf("sam in globex, same request_id: %v", err)
	}
	if itemID(t, a.Msg.Item.Name) == itemID(t, g.Msg.Item.Name) {
		t.Errorf("one request_id in two namespaces made one Item")
	}
}

// Step 0, part 2: the new operations leak nothing across namespaces either.
// A caller in acme gets NOT_FOUND for globex's Items, sessions and scopes.
func TestNamespaces_NewOperations(t *testing.T) {
	ctx := testCtx(t)
	res, err := as("bea").CreateItem(ctx, connect.NewRequest(&kernelv1.CreateItemRequest{
		Parent: "namespaces/globex", RequestId: newID(), Type: "namespaces/globex/types/ticket/versions/1",
	}))
	if err != nil {
		t.Fatalf("bea creates in globex: %v", err)
	}
	theirs := res.Msg.Item.Name
	disguised := "namespaces/acme/items/" + itemID(t, theirs).String()
	theirSession := startSession(t, "bot-globex", "user:bea", []string{"namespaces/globex"}, "read")

	for _, who := range []string{"ana", "sam-acme"} {
		c := as(who)
		for _, name := range []string{theirs, disguised} {
			_, err := c.ListItems(ctx, connect.NewRequest(&kernelv1.ListItemsRequest{Parent: name}))
			notFound(t, who+" ListItems "+name, err)
			_, err = c.ListItemEvents(ctx, connect.NewRequest(&kernelv1.ListItemEventsRequest{Parent: name}))
			notFound(t, who+" ListItemEvents "+name, err)
		}
		_, err := c.ListItems(ctx, connect.NewRequest(&kernelv1.ListItemsRequest{Parent: "namespaces/globex"}))
		notFound(t, who+" ListItems of globex's top level", err)
		_, err = c.SearchItems(ctx, connect.NewRequest(&kernelv1.SearchItemsRequest{
			Parent: "namespaces/globex", Filter: &kernelv1.ItemFilter{States: []string{"open"}},
		}))
		notFound(t, who+" SearchItems in globex with a filter", err)
		_, err = c.ApproveWiden(ctx, connect.NewRequest(&kernelv1.ApproveWidenRequest{
			Parent: "namespaces/acme", Session: theirSession, Scope: "namespaces/acme",
		}))
		notFound(t, who+" ApproveWiden of a globex session", err)
		_, err = c.ApproveWiden(ctx, connect.NewRequest(&kernelv1.ApproveWidenRequest{
			Parent: "namespaces/globex", Session: theirSession, Scope: "namespaces/globex",
		}))
		notFound(t, who+" ApproveWiden in globex", err)
	}
	_, err = asAgent("bot", "").StartAgentSession(ctx, connect.NewRequest(&kernelv1.StartAgentSessionRequest{
		Parent: "namespaces/acme", ActingFor: "user:ana", Scopes: []string{disguised}, Powers: []string{"read"},
	}))
	notFound(t, "a session scoped to a globex container", err)
}
