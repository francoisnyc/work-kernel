// Tests for a claim that binds, over the wire (docs/roadmap.md, track 1,
// item 5, step 2). The contract does not change: the claim answers with
// the refusals the API already has.
//
//   - a colleague's decision on a claimed Item: PERMISSION_DENIED / NOT_ALLOWED;
//   - a claim for someone else: PERMISSION_DENIED / FIELD_NOT_WRITABLE, naming the field;
//   - a write of the claim field while claimed: FAILED_PRECONDITION / FIELD_FROZEN, naming the field and the state;
//   - a release by a colleague: applied.
//
// The rule itself is tested in internal/policy and internal/itemservice.
package api_test

import (
	"sync"
	"testing"

	"connectrpc.com/connect"

	kernelv1 "github.com/francoisnyc/work-kernel/gen/kernel/v1"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// apicl: a queue the team claims from. No other test makes one, and only
// this file grants apicl_team (to ana and bob). Like apiwq in
// waiting_test.go it is registered when the test runs, after
// TestListTypes_EveryVersionInOrder (types_test.go) has listed the Types:
// Go runs test files in name order, which is why this file's name starts
// with "waiting".
const apiClaimSrc = `
id: apicl
version: 1
initial_state: queue
fields:
  - {id: f_title,      type: string}
  - {id: f_claimed_by, type: principal}
states:
  - {id: queue,  category: waiting}
  - {id: review, category: active, claim: f_claimed_by}
  - {id: done,   category: done}
transitions:
  - {from: queue,  to: review, allowed: ['role:apicl_team']}
  - {from: review, to: queue,  allowed: ['role:apicl_team'], release: true, clears: [f_claimed_by]}
  - {from: review, to: done,   allowed: ['role:apicl_team']}
`

const apiClaimV1 = "namespaces/acme/types/apicl/versions/1"

var apiClaimOnce sync.Once

func apiClaimSetup(t *testing.T) {
	t.Helper()
	apiClaimOnce.Do(func() {
		ty, err := typedef.Parse([]byte(apiClaimSrc))
		if err != nil {
			t.Fatalf("parse apicl: %v", err)
		}
		if err := k.RegisterType(testCtx(t), ty); err != nil {
			t.Fatalf("register apicl: %v", err)
		}
		if _, err := pool.Exec(testCtx(t), `insert into principal_roles (namespace_id, principal, role)
			values ('acme', 'user:ana', 'apicl_team'), ('acme', 'user:bob', 'apicl_team') on conflict do nothing`); err != nil {
			t.Fatalf("grant apicl_team: %v", err)
		}
	})
}

// move asks for a transition, with a patch, and returns the error.
func claimMove(t *testing.T, who, name, to string, patch map[string]any) (*kernelv1.Item, error) {
	t.Helper()
	req := &kernelv1.TransitionItemRequest{Name: name, RequestId: newID(), ToState: to}
	if patch != nil {
		req.Fields = fields(t, patch)
	}
	res, err := as(who).TransitionItem(testCtx(t), connect.NewRequest(req))
	if err != nil {
		return nil, err
	}
	return res.Msg.Item, nil
}

func TestClaim_OverTheWire(t *testing.T) {
	apiClaimSetup(t)
	name := create(t, "sam-acme", apiClaimV1, map[string]any{"f_title": "a claim " + word()}).Item.Name

	// A claim for someone else is refused, and names the field.
	_, err := claimMove(t, "bob", name, "review", map[string]any{"f_claimed_by": "user:ana"})
	ce := apiError(t, "bob claims it for ana", err, connect.CodePermissionDenied, "FIELD_NOT_WRITABLE")
	if got := errorInfo(t, ce).Metadata["fields"]; got != "f_claimed_by" {
		t.Errorf("bob claims it for ana: fields = %q, want f_claimed_by", got)
	}

	// bob claims it, with no patch: the answer shows him as the claimant.
	it, err := claimMove(t, "bob", name, "review", nil)
	if err != nil {
		t.Fatalf("bob claims it: %v", err)
	}
	if it.State != "review" || it.Fields.AsMap()["f_claimed_by"] != "user:bob" {
		t.Errorf("after bob's claim: in %s, claimed by %v; want in review, claimed by user:bob", it.State, it.Fields.AsMap()["f_claimed_by"])
	}

	// ana, in the team like bob, may not decide it.
	_, err = claimMove(t, "ana", name, "done", nil)
	apiError(t, "ana decides what bob claimed", err, connect.CodePermissionDenied, "NOT_ALLOWED")

	// Nobody rewrites the claim.
	_, err = as("ana").UpdateItem(testCtx(t), connect.NewRequest(&kernelv1.UpdateItemRequest{
		Name: name, RequestId: newID(), Fields: fields(t, map[string]any{"f_claimed_by": "user:ana"}),
	}))
	ce = apiError(t, "ana takes the claim with an update", err, connect.CodeFailedPrecondition, "FIELD_FROZEN")
	if md := errorInfo(t, ce).Metadata; md["fields"] != "f_claimed_by" || md["state"] != "review" {
		t.Errorf("ana takes the claim with an update: metadata = %v, want fields f_claimed_by, state review", md)
	}

	// ana releases it, claims it, and decides.
	it, err = claimMove(t, "ana", name, "queue", nil)
	if err != nil {
		t.Fatalf("ana releases bob's claim: %v", err)
	}
	if _, set := it.Fields.AsMap()["f_claimed_by"]; set || it.State != "queue" {
		t.Errorf("after the release: in %s with fields %v; want in queue, the claim cleared", it.State, it.Fields.AsMap())
	}
	if _, err := claimMove(t, "ana", name, "review", map[string]any{"f_claimed_by": "user:ana"}); err != nil {
		t.Fatalf("ana claims it, naming herself: %v", err)
	}
	_, err = claimMove(t, "bob", name, "done", nil)
	apiError(t, "bob decides what ana claimed", err, connect.CodePermissionDenied, "NOT_ALLOWED")
	if it, err = claimMove(t, "ana", name, "done", nil); err != nil || it.State != "done" {
		t.Errorf("ana decides her claim: %v, error %v", it.GetState(), err)
	}
}
