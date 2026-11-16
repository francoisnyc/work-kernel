// Tests for Step 0, part 3, review fix 6b: ApproveWiden carries the audience
// its approver was shown (expected_audience, required unless validate_only)
// and commits only for that audience, in one kernel call (one transaction).
// A different audience at commit is FAILED_PRECONDITION / AUDIENCE_CHANGED
// with the fixed message and both audiences in metadata; nothing is
// approved.
package api_test

import (
	"testing"

	"connectrpc.com/connect"

	kernelv1 "github.com/francoisnyc/work-kernel/gen/kernel/v1"
	"github.com/francoisnyc/work-kernel/internal/api"
)

// approveAs calls ApproveWiden as ana for a session and scope.
func approveAs(t *testing.T, session, scope, requestID, expected string, validateOnly bool) (*kernelv1.ApproveWidenResponse, error) {
	t.Helper()
	res, err := as("ana").ApproveWiden(testCtx(t), connect.NewRequest(&kernelv1.ApproveWidenRequest{
		Parent: "namespaces/acme", Session: session, Scope: scope, RequestId: requestID,
		ValidateOnly: validateOnly, ExpectedAudience: expected,
	}))
	if err != nil {
		return nil, err
	}
	return res.Msg, nil
}

// widen drops a narrowed Item's own readers: it reads like its container.
func widen(t *testing.T, who, name string) {
	t.Helper()
	if _, err := as(who).UpdateItem(testCtx(t), connect.NewRequest(&kernelv1.UpdateItemRequest{
		Name: name, RequestId: newID(), Policy: &kernelv1.PolicyChange{Change: &kernelv1.PolicyChange_Inherit{Inherit: true}},
	})); err != nil {
		t.Fatalf("widen %s: %v", name, err)
	}
}

// expected_audience is required on a real approval, and must be one of the
// two audiences; validate_only ignores it.
func TestApproveWiden_ExpectedAudienceIsRequired(t *testing.T) {
	open := create(t, "ana", ticketV1, nil).Item.Name
	session := startSession(t, "bot", "user:ana", []string{"namespaces/acme"}, "read")

	_, err := approveAs(t, session, open, newID(), "", false)
	invalid(t, "no expected_audience", err, "expected_audience")
	_, err = approveAs(t, session, open, newID(), "everyone", false)
	invalid(t, "an unknown expected_audience", err, "expected_audience")
	if res, err := approveAs(t, session, open, "", "everyone", true); err != nil || res.Audience != "namespace" {
		t.Errorf("validate_only with a garbage expected_audience: %v, %v; want it ignored, audience namespace", res, err)
	}
	res, err := approveAs(t, session, open, newID(), "namespace", false)
	if err != nil || res.Audience != "namespace" {
		t.Errorf("approve expecting namespace: %v, %v; want audience namespace", res, err)
	}
}

// Shown one audience, committed under another: refused, with both
// audiences, and the agent still needs approval.
func TestApproveWiden_AudienceChanged(t *testing.T) {
	ctx := testCtx(t)
	private := createNarrowed(t, "ana", "user:ana", "agent:triage-bot")
	room := createNarrowed(t, "ana", "user:ana", "agent:triage-bot")
	session := startSession(t, "bot", "user:ana", []string{"namespaces/acme"}, "read", "create")
	bot := asAgent("bot", session)
	if _, err := bot.GetItem(ctx, connect.NewRequest(&kernelv1.GetItemRequest{Name: private})); err != nil {
		t.Fatalf("the bot reads the private Item: %v", err)
	}

	shown, err := approveAs(t, session, room, "", "", true)
	if err != nil || shown.Audience != "restricted" {
		t.Fatalf("validate_only = %v, %v; want restricted", shown, err)
	}
	widen(t, "ana", room)
	_, err = approveAs(t, session, room, newID(), shown.Audience, false)
	ce := apiError(t, "audience changed", err, connect.CodeFailedPrecondition, "AUDIENCE_CHANGED")
	if want, ok := api.ReasonMessage("AUDIENCE_CHANGED"); !ok || ce.Message() != want {
		t.Errorf("message %q, want the fixed %q (known %v)", ce.Message(), want, ok)
	}
	if idLike.MatchString(ce.Message()) {
		t.Errorf("message %q contains an ID", ce.Message())
	}
	md := errorInfo(t, ce).GetMetadata()
	if md["audience"] != "namespace" || md["expected_audience"] != "restricted" || len(md) != 2 {
		t.Errorf("metadata %v, want audience namespace, expected_audience restricted, nothing else", md)
	}

	_, err = bot.CreateItem(ctx, connect.NewRequest(&kernelv1.CreateItemRequest{
		Parent: "namespaces/acme", RequestId: newID(), Type: ticketV1, Container: room,
	}))
	apiError(t, "create after the refused approval", err, connect.CodePermissionDenied, "NEEDS_APPROVAL")
}

// A real approval is one kernel call, ApproveWiden, which checks the
// audience in its own transaction; validate_only is one ApprovalAudience
// call. A replay answers the audience the approval was made for.
func TestApproveWiden_OneKernelCall(t *testing.T) {
	room := createNarrowed(t, "ana", "user:ana", "agent:triage-bot")
	session := startSession(t, "bot", "user:ana", []string{"namespaces/acme"}, "read")

	ops := func(start int) []string {
		var out []string
		for _, c := range watched.since(start) {
			out = append(out, c.op)
		}
		return out
	}
	start := watched.count()
	if _, err := approveAs(t, session, room, "", "", true); err != nil {
		t.Fatalf("validate_only: %v", err)
	}
	if got := ops(start); len(got) != 1 || got[0] != "ApprovalAudience" {
		t.Errorf("validate_only kernel calls = %v, want [ApprovalAudience]", got)
	}

	id := newID()
	start = watched.count()
	res, err := approveAs(t, session, room, id, "restricted", false)
	if err != nil || res.Audience != "restricted" {
		t.Fatalf("approve: %v, %v; want audience restricted", res, err)
	}
	if got := ops(start); len(got) != 1 || got[0] != "ApproveWiden" {
		t.Errorf("approve kernel calls = %v, want [ApproveWiden] alone", got)
	}

	widen(t, "ana", room)
	if res, err := approveAs(t, session, room, id, "restricted", false); err != nil || res.Audience != "restricted" {
		t.Errorf("replay after the audience changed: %v, %v; want the stored answer, restricted", res, err)
	}
}
