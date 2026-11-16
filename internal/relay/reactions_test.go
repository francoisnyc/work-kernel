// Tests for proof-of-concept Step 2, part 2: what the relay sends for link
// reactions. Approving a vacation request v2 opens a hold (deduct), which
// starts a workflow; withdrawing an approved request runs the refund, an
// after reaction, so that event starts a workflow too: the run that saw the
// approval may have completed (approved has no other behavior).
package relay_test

import (
	"fmt"
	"os"
	"testing"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

func registerFile(t *testing.T, k *itemservice.Service, name string) {
	t.Helper()
	data, err := os.ReadFile("../../types/" + name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	ty, err := typedef.Parse(data)
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	if err := k.RegisterType(testCtx(t), ty); err != nil {
		t.Fatalf("register %s: %v", name, err)
	}
}

func TestRelay_WithdrawingAnApprovedRequestStartsTheWorkflow(t *testing.T) {
	r, fake, _ := newRelay(t)
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
	id := res.ItemID
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
	submit := move(ana, "submitted") // 2
	if _, err := k.Internal().ResolveHold(ctx, itemservice.ResolveHoldRequest{IdempotencyKey: "hold:" + submit.HoldID.String(),
		Principal: item.Principal{Actor: "hook:vacation_request:hr_check"}, ItemID: id, HoldID: submit.HoldID,
		Verdict: itemservice.VerdictCorrect, Fields: map[string]any{"f_approver": bob.Actor},
		Attribution: cause("hook:vacation_request@v2:hr_check", submit.ItemSeq)}); err != nil { // 3
		t.Fatalf("HR: %v", err)
	}
	approve := move(bob, "approved") // 4
	deduct := item.Principal{Actor: "reaction:vacation_request:deduct"}
	if _, err := k.Internal().React(ctx, itemservice.ReactRequest{Principal: deduct, SourceID: id, HoldID: approve.HoldID,
		Attribution: cause("reaction:vacation_request@v2:deduct", approve.ItemSeq)}); err != nil {
		t.Fatalf("React: %v", err)
	}
	if _, err := k.Internal().ResolveHold(ctx, itemservice.ResolveHoldRequest{IdempotencyKey: "hold:" + approve.HoldID.String(),
		Principal: deduct, ItemID: id, HoldID: approve.HoldID, Verdict: itemservice.VerdictAllow,
		Attribution: cause("reaction:vacation_request@v2:deduct", approve.ItemSeq)}); err != nil { // 5
		t.Fatalf("resolve the approval: %v", err)
	}
	move(ana, "withdrawn") // 6

	runOnce(t, r)
	got := fake.callsFor(id)
	if len(got) != 6 {
		t.Fatalf("deliveries = %+v, want six", got)
	}
	starts := map[int64]bool{}
	for _, c := range got {
		starts[c.Event.Seq] = c.Start
	}
	if !starts[4] {
		t.Error("the approval request did not start the workflow (deduct holds it)")
	}
	if !starts[6] || got[5].Event.From != "approved" || got[5].Event.To != "withdrawn" {
		t.Errorf("the withdrawal = %+v, want approved -> withdrawn starting the workflow (refund runs there)", got[5])
	}
}
