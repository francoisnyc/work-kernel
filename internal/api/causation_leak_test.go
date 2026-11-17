// Tests for the consolidation after Step 2, item 4, through the API: the
// balance's history names the request that deducted from it (causation_id)
// only to a reader who can see that request.
package api_test

import (
	"fmt"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"

	kernelv1 "github.com/francoisnyc/work-kernel/gen/kernel/v1"
	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
)

func TestHistory_CausationNamingAHiddenItem_Omitted(t *testing.T) {
	bal := openBalanceFor(t, "user:ana", 25) // not narrowed: hana and everyone in acme see it
	res, err := as("ana").CreateItem(testCtx(t), connect.NewRequest(&kernelv1.CreateItemRequest{
		Parent: "namespaces/acme", RequestId: newID(), Type: vacationV2, Fields: fields(t, map[string]any{
			"f_start": "2099-07-01", "f_end": "2099-07-05", "f_days": 5, "f_leave_type": "annual"}),
		Narrow: &kernelv1.Readers{Principals: []string{"user:ana", "user:bob"}},
	}))
	if err != nil {
		t.Fatalf("create narrowed request: %v", err)
	}
	name := res.Msg.Item.Name
	id := itemID(t, name)
	sub := transition(t, "ana", name, "submitted")
	cause := func(origin string, seq int64) item.Attribution {
		return item.Attribution{OriginatingHook: origin, CausationID: fmt.Sprintf("%s:%d", id, seq), Hop: 1}
	}
	subHold := itemID(t, sub.Hold)
	if _, err := k.Internal().ResolveHold(testCtx(t), itemservice.ResolveHoldRequest{IdempotencyKey: "hold:" + subHold.String(),
		Principal: hrCheck, ItemID: id, HoldID: subHold, Verdict: itemservice.VerdictCorrect,
		Fields: map[string]any{"f_approver": "user:bob"}, Attribution: cause("hook:vacation_request@v2:hr_check", sub.ItemSeq)}); err != nil {
		t.Fatalf("HR: %v", err)
	}
	ask := transition(t, "bob", name, "approved")
	hold := itemID(t, ask.Hold)
	dc := cause("reaction:vacation_request@v2:deduct", ask.ItemSeq)
	if _, err := k.Internal().React(testCtx(t), itemservice.ReactRequest{Principal: deductP, SourceID: id, HoldID: hold, Attribution: dc}); err != nil {
		t.Fatalf("React: %v", err)
	}
	if _, err := k.Internal().ResolveHold(testCtx(t), itemservice.ResolveHoldRequest{IdempotencyKey: "hold:" + hold.String(),
		Principal: deductP, ItemID: id, HoldID: hold, Verdict: itemservice.VerdictAllow, Attribution: dc}); err != nil {
		t.Fatalf("resolve: %v", err)
	}

	page := func(who string) string {
		res, err := as(who).ListItemEvents(testCtx(t), connect.NewRequest(&kernelv1.ListItemEventsRequest{Parent: bal}))
		if err != nil {
			t.Fatalf("ListItemEvents as %s: %v", who, err)
		}
		return protojson.Format(res.Msg)
	}
	if _, err := as("hana").GetItem(testCtx(t), connect.NewRequest(&kernelv1.GetItemRequest{Name: name})); err == nil {
		t.Fatal("test bug: hana can see the narrowed request")
	}
	if got := page("hana"); strings.Contains(got, id.String()) {
		t.Errorf("hana (who cannot see the request) finds its ID in the balance's history:\n%s", got)
	}
	if got := page("ana"); !strings.Contains(got, id.String()) {
		t.Errorf("ana (who sees the request) does not find its ID in the balance's history:\n%s", got)
	}
}
