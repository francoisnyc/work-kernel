// Tests for the fixes of the 2026-11-21 review of Jira part 2, step 4a,
// through the public API (docs/reviews/2026-11-21-customers.md):
//
//   - a customer's writes and history carry no commit_seq (0, so absent in
//     JSON), and a customer may not send min_seq;
//   - a link condition on a name a customer may not see is refused in the
//     same bytes as one on an unknown name;
//   - an agent acting for a customer whose write would need an approval is
//     PERMISSION_DENIED / APPROVAL_UNAVAILABLE, with no metadata.
package api_test

import (
	"testing"

	"connectrpc.com/connect"

	kernelv1 "github.com/francoisnyc/work-kernel/gen/kernel/v1"
)

func TestFix_API_ACustomerSeesNoCommitSeq_AndMayNotSendMinSeq(t *testing.T) {
	desk := portal(t)
	kim := asCustomer("kim", "")
	res, err := customerCreates(t, kim, incidentV2, desk, map[string]any{"f_title": "x", "f_priority": "P3"})
	if err != nil {
		t.Fatal(err)
	}
	if res.CommitSeq != 0 || res.ItemSeq == 0 {
		t.Errorf("kim's create: commit_seq %d, item_seq %d; want 0 and the item_seq", res.CommitSeq, res.ItemSeq)
	}
	inc := res.Item.Name
	tr, err := kim.TransitionItem(testCtx(t), connect.NewRequest(&kernelv1.TransitionItemRequest{Name: inc, RequestId: newID(), ToState: "canceled"}))
	if err != nil || tr.Msg.CommitSeq != 0 {
		t.Errorf("kim's cancel: %v, commit_seq %d; want 0", err, tr.Msg.GetCommitSeq())
	}
	ev, err := kim.ListItemEvents(testCtx(t), connect.NewRequest(&kernelv1.ListItemEventsRequest{Parent: inc}))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ev.Msg.Events {
		if e.CommitSeq != 0 {
			t.Errorf("kim's history: %s has commit_seq %d", e.Name, e.CommitSeq)
		}
	}
	_, err = kim.SearchItems(testCtx(t), connect.NewRequest(&kernelv1.SearchItemsRequest{Parent: "namespaces/acme", Query: "x", MinSeq: 1}))
	invalid(t, "kim sends min_seq", err, "min_seq")
	// Staff keep both.
	staff := mkIn(t, "sam-acme", replyV1, inc, map[string]any{"f_body": "x"})
	if e, _ := as("sam-acme").ListItemEvents(testCtx(t), connect.NewRequest(&kernelv1.ListItemEventsRequest{Parent: staff})); e.Msg.Events[0].CommitSeq == 0 {
		t.Error("sam's history lost commit_seq")
	}
}

func TestFix_API_AnUnlistedLinkName_TheSameBytesAsAnUnknownName(t *testing.T) {
	desk := portal(t)
	inc := kimsIncident(t, desk)
	ask := func(name string) error {
		_, err := asCustomer("kim", "").SearchItems(testCtx(t), connect.NewRequest(&kernelv1.SearchItemsRequest{Parent: "namespaces/acme",
			Filter: &kernelv1.ItemFilter{Links: []*kernelv1.LinkFilter{{LinkType: name, Direction: kernelv1.LinkDirection_LINK_DIRECTION_OUTGOING, Item: inc}}}}))
		return err
	}
	e1, e2 := ask("caused_by"), ask("no_such_link")
	if e1 == nil || e2 == nil || string(errBytes(t, e1)) != string(errBytes(t, e2)) {
		t.Errorf("caused_by (not listed for customers): %v; an unknown name: %v; want the same bytes", e1, e2)
	}
}

func TestFix_API_APortalBot_ApprovalUnavailable(t *testing.T) {
	desk := portal(t)
	narrowed, open := kimsIncident(t, desk), kimsIncident(t, desk)
	if _, err := as("hana").UpdateItem(testCtx(t), connect.NewRequest(&kernelv1.UpdateItemRequest{Name: narrowed, RequestId: newID(),
		Policy: &kernelv1.PolicyChange{Change: &kernelv1.PolicyChange_Narrow{Narrow: &kernelv1.Readers{
			Principals: []string{"user:hana", "customer:kim", "agent:portal-bot"}}}}})); err != nil {
		t.Fatalf("narrow: %v", err)
	}
	s, err := asCustomer("portal-bot", "").StartAgentSession(testCtx(t), connect.NewRequest(&kernelv1.StartAgentSessionRequest{
		Parent: "namespaces/acme", ActingFor: "customer:kim", Scopes: []string{"namespaces/acme"}, Powers: []string{"read", "create"}}))
	if err != nil {
		t.Fatal(err)
	}
	bot := asCustomer("portal-bot", s.Msg.Session.GetId())
	if _, err := bot.GetItem(testCtx(t), connect.NewRequest(&kernelv1.GetItemRequest{Name: narrowed})); err != nil {
		t.Fatal(err)
	}
	_, err = customerCreates(t, bot, replyV1, open, map[string]any{"f_body": "x"})
	ce := apiError(t, "the bot writes wider than it read", err, connect.CodePermissionDenied, "APPROVAL_UNAVAILABLE")
	if info := errorInfo(t, ce); info != nil && len(info.Metadata) != 0 {
		t.Errorf("the refusal carries %v", info.Metadata)
	}
}
