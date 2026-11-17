// Tests for the consolidation after Step 2: what the relay sends for an
// escape that cancels a held reaction's open hold (withdrawing a request
// while its approval is held).
//
//   - item 5: the event names the hold (HoldID) and says it was canceled
//     (Canceled), so the workflow never reads it as allowed.
package relay_test

import (
	"fmt"
	"testing"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
	"github.com/francoisnyc/work-kernel/internal/workflow"
)

// escapedApproval: a v2 request whose approval (held by deduct, the
// deduction committed) is canceled by the withdrawal. It returns the
// request, the hold and the escape's seq.
func escapedApproval(t *testing.T) (id, hold uuid.UUID, escape int64) {
	t.Helper()
	k := itemservice.New(pool)
	registerFile(t, k, "leave_balance.v1.yaml")
	registerFile(t, k, "vacation_request.v2.yaml")
	suffix := uuid.NewString()[:8]
	ana, bob, hana := item.Principal{Actor: "user:ana-" + suffix}, item.Principal{Actor: "user:bob-" + suffix},
		item.Principal{Actor: "user:hana-" + suffix}
	if _, err := pool.Exec(testCtx(t),
		`insert into principal_roles (namespace_id, principal, role) values ('default', $1, 'hr_admin')`, hana.Actor); err != nil {
		t.Fatal(err)
	}
	ctx := testCtx(t)
	if _, err := k.Create(ctx, itemservice.CreateRequest{IdempotencyKey: newKey(), Principal: hana, TypeID: "leave_balance",
		TypeVersion: 1, Fields: map[string]any{"f_owner": ana.Actor, "f_allowance": 25}}); err != nil {
		t.Fatalf("create balance: %v", err)
	}
	res, err := k.Create(ctx, itemservice.CreateRequest{IdempotencyKey: newKey(), Principal: ana, TypeID: "vacation_request",
		TypeVersion: 2, Fields: map[string]any{"f_start": "2099-07-01", "f_end": "2099-07-05", "f_days": 5, "f_leave_type": "annual"}})
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	id = res.ItemID
	cause := func(origin string, seq int64) item.Attribution {
		return item.Attribution{OriginatingHook: origin, CausationID: fmt.Sprintf("%s:%d", id, seq), Hop: 1}
	}
	move := func(p item.Principal, to string) itemservice.TransitionResult {
		t.Helper()
		out, err := k.Transition(ctx, itemservice.TransitionRequest{IdempotencyKey: newKey(), Principal: p, ItemID: id, ToState: to})
		if err != nil {
			t.Fatalf("transition to %s: %v", to, err)
		}
		return out
	}
	submit := move(ana, "submitted")
	if _, err := k.Internal().ResolveHold(ctx, itemservice.ResolveHoldRequest{IdempotencyKey: "hold:" + submit.HoldID.String(),
		Principal: item.Principal{Actor: "hook:vacation_request:hr_check"}, ItemID: id, HoldID: submit.HoldID,
		Verdict: itemservice.VerdictCorrect, Fields: map[string]any{"f_approver": bob.Actor},
		Attribution: cause("hook:vacation_request@v2:hr_check", submit.ItemSeq)}); err != nil {
		t.Fatalf("HR: %v", err)
	}
	approve := move(bob, "approved")
	if _, err := k.Internal().React(ctx, itemservice.ReactRequest{Principal: item.Principal{Actor: "reaction:vacation_request:deduct"},
		SourceID: id, HoldID: approve.HoldID, Attribution: cause("reaction:vacation_request@v2:deduct", approve.ItemSeq)}); err != nil {
		t.Fatalf("React: %v", err)
	}
	w := move(ana, "withdrawn") // the escape: cancels the open hold
	return id, approve.HoldID, w.ItemSeq
}

func deliveryOf(t *testing.T, fake *fakeWorkflows, id uuid.UUID, seq int64) (workflow.Event, bool) {
	t.Helper()
	for _, c := range fake.callsFor(id) {
		if c.Event.Seq == seq {
			return c.Event, c.Start
		}
	}
	t.Fatalf("event %d was not delivered", seq)
	return workflow.Event{}, false
}

func TestRelay_EscapeCancelingAHold_IsMarkedCanceled(t *testing.T) {
	r, fake, _ := newRelay(t)
	id, hold, escape := escapedApproval(t)
	runOnce(t, r)
	ev, _ := deliveryOf(t, fake, id, escape)
	if ev.HoldID != hold.String() || !ev.Canceled || ev.To != "withdrawn" {
		t.Errorf("the escape = %+v, want hold %s, Canceled, to withdrawn", ev, hold)
	}
}
