// Tests for proof-of-concept Step 2, part 3: the relay sends a
// reaction_applied event with its reaction, and starts the workflow for it
// (the request is withdrawn, a final state, and the refund email must
// still be sent).
package relay_test

import (
	"fmt"
	"testing"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
)

func TestRelay_ReactionAppliedStartsTheWorkflow(t *testing.T) {
	r, fake, _ := newRelay(t)
	k := itemservice.New(pool)
	registerFile(t, k, "leave_balance.v1.yaml")
	registerFile(t, k, "vacation_request.v3.yaml")
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
		TypeVersion: 3, Fields: map[string]any{"f_start": "2099-07-01", "f_end": "2099-07-05", "f_days": 5, "f_leave_type": "annual"}})
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
	submit := move(ana, "submitted")
	if _, err := k.Internal().ResolveHold(ctx, itemservice.ResolveHoldRequest{IdempotencyKey: "hold:" + submit.HoldID.String(),
		Principal: item.Principal{Actor: "hook:vacation_request:hr_check"}, ItemID: id, HoldID: submit.HoldID,
		Verdict: itemservice.VerdictCorrect, Fields: map[string]any{"f_approver": bob.Actor},
		Attribution: cause("hook:vacation_request@v3:hr_check", submit.ItemSeq)}); err != nil {
		t.Fatalf("HR: %v", err)
	}
	approve := move(bob, "approved")
	deduct := item.Principal{Actor: "reaction:vacation_request:deduct"}
	if _, err := k.Internal().React(ctx, itemservice.ReactRequest{Principal: deduct, SourceID: id, HoldID: approve.HoldID,
		Attribution: cause("reaction:vacation_request@v3:deduct", approve.ItemSeq)}); err != nil {
		t.Fatalf("React: %v", err)
	}
	if _, err := k.Internal().ResolveHold(ctx, itemservice.ResolveHoldRequest{IdempotencyKey: "hold:" + approve.HoldID.String(),
		Principal: deduct, ItemID: id, HoldID: approve.HoldID, Verdict: itemservice.VerdictAllow,
		Attribution: cause("reaction:vacation_request@v3:deduct", approve.ItemSeq)}); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	withdraw := move(ana, "withdrawn")
	refund := item.Principal{Actor: "reaction:vacation_request:refund"}
	if _, err := k.Internal().Undo(ctx, itemservice.UndoRequest{Principal: refund, SourceID: id, HoldID: approve.HoldID,
		Attribution: cause("reaction:vacation_request@v3:refund", withdraw.ItemSeq)}); err != nil {
		t.Fatalf("Undo: %v", err)
	}
	applied, err := k.Internal().ReactionApplied(ctx, itemservice.ReactionAppliedRequest{Principal: refund, SourceID: id,
		HoldID: approve.HoldID, Attribution: cause("reaction:vacation_request@v3:refund", withdraw.ItemSeq)})
	if err != nil {
		t.Fatalf("ReactionApplied: %v", err)
	}

	runOnce(t, r)
	var found bool
	for _, c := range fake.callsFor(id) {
		if c.Event.Seq == applied.ItemSeq {
			found = true
			if c.Event.Op != "reaction_applied" || c.Event.Reaction != "refund" || !c.Start {
				t.Errorf("delivery = %+v, want reaction_applied of refund, starting the workflow", c)
			}
		}
	}
	if !found {
		t.Errorf("reaction_applied (seq %d) was not delivered", applied.ItemSeq)
	}
}
