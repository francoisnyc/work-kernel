// Tests for proof-of-concept Step 2, part 3, through the public API: the
// vacation request v3's acting timer in GetType, and the new history kinds:
// HOOK_RAN (hook, and an acting timer's timer and run) and
// REACTION_APPLIED (the reaction). The kernel's own writes are played
// through the internal door, as the worker plays them.
package api_test

import (
	"fmt"
	"testing"

	"connectrpc.com/connect"

	kernelv1 "github.com/francoisnyc/work-kernel/gen/kernel/v1"
	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
)

const vacationV3 = "namespaces/acme/types/vacation_request/versions/3"

func TestGetType_VacationRequestV3(t *testing.T) {
	res, err := as("ana").GetType(testCtx(t), connect.NewRequest(&kernelv1.GetTypeRequest{Name: vacationV3}))
	if err != nil {
		t.Fatalf("GetType: %v", err)
	}
	var submitted *kernelv1.State
	for _, s := range res.Msg.Type.States {
		if s.Id == "submitted" {
			submitted = s
		}
	}
	if submitted == nil || len(submitted.Acting) != 1 {
		t.Fatalf("submitted = %v, want one acting timer", submitted)
	}
	a := submitted.Acting[0]
	if a.Id != "remind" || a.After != "48h" || a.Every != "24h" || a.Max != 2 {
		t.Errorf("acting timer = %v, want remind after 48h, every 24h, max 2", a)
	}
}

func TestHistory_HookRan(t *testing.T) {
	openBalanceFor(t, "user:ana", 25)
	name := create(t, "ana", vacationV3, map[string]any{
		"f_start": "2099-07-01", "f_end": "2099-07-05", "f_days": 5, "f_leave_type": "annual",
	}).Item.Name
	res := transition(t, "ana", name, "submitted")
	id, holdID := itemID(t, name), itemID(t, res.Hold)
	hr := item.Attribution{OriginatingHook: "hook:vacation_request@v3:hr_check", CausationID: fmt.Sprintf("%s:%d", id, res.ItemSeq), Hop: 1}
	entered, err := k.Internal().ResolveHold(testCtx(t), itemservice.ResolveHoldRequest{IdempotencyKey: "hold:" + holdID.String(),
		Principal: hrCheck, ItemID: id, HoldID: holdID, Verdict: itemservice.VerdictCorrect,
		Fields: map[string]any{"f_approver": "user:bob"}, Attribution: hr})
	if err != nil {
		t.Fatalf("HR: %v", err)
	}
	if _, err := k.Internal().HookRan(testCtx(t), itemservice.HookRanRequest{
		IdempotencyKey: fmt.Sprintf("timer:%s:remind:%d:1", id, entered.ItemSeq),
		Principal:      item.Principal{Actor: "hook:vacation_request:remind_approver"}, ItemID: id, Timer: "remind", Run: 1,
		Attribution: item.Attribution{OriginatingHook: "hook:vacation_request@v3:remind_approver",
			CausationID: fmt.Sprintf("%s:%d", id, entered.ItemSeq), Hop: 2},
	}); err != nil {
		t.Fatalf("HookRan: %v", err)
	}
	events := history(t, "ana", name)
	last := events[len(events)-1]
	if last.Kind != kernelv1.ItemEventKind_ITEM_EVENT_KIND_HOOK_RAN || last.Hook != "remind_approver" ||
		last.Timer != "remind" || last.Run != 1 || last.Fields != nil {
		t.Errorf("event = %v, want HOOK_RAN of remind_approver, timer remind, run 1, no fields", last)
	}
}

func TestHistory_ReactionApplied(t *testing.T) {
	openBalanceFor(t, "user:ana", 25)
	name := create(t, "ana", vacationV3, map[string]any{
		"f_start": "2099-07-01", "f_end": "2099-07-05", "f_days": 5, "f_leave_type": "annual",
	}).Item.Name
	res := transition(t, "ana", name, "submitted")
	id, holdID := itemID(t, name), itemID(t, res.Hold)
	cause := func(origin string, seq int64) item.Attribution {
		return item.Attribution{OriginatingHook: origin, CausationID: fmt.Sprintf("%s:%d", id, seq), Hop: 1}
	}
	if _, err := k.Internal().ResolveHold(testCtx(t), itemservice.ResolveHoldRequest{IdempotencyKey: "hold:" + holdID.String(),
		Principal: hrCheck, ItemID: id, HoldID: holdID, Verdict: itemservice.VerdictCorrect,
		Fields: map[string]any{"f_approver": "user:bob"}, Attribution: cause("hook:vacation_request@v3:hr_check", res.ItemSeq)}); err != nil {
		t.Fatalf("HR: %v", err)
	}
	approve := transition(t, "bob", name, "approved")
	approveHold := itemID(t, approve.Hold)
	dc := cause("reaction:vacation_request@v3:deduct", approve.ItemSeq)
	if _, err := k.Internal().React(testCtx(t), itemservice.ReactRequest{Principal: deductP, SourceID: id, HoldID: approveHold, Attribution: dc}); err != nil {
		t.Fatalf("React: %v", err)
	}
	if _, err := k.Internal().ResolveHold(testCtx(t), itemservice.ResolveHoldRequest{IdempotencyKey: "hold:" + approveHold.String(),
		Principal: deductP, ItemID: id, HoldID: approveHold, Verdict: itemservice.VerdictAllow, Attribution: dc}); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	withdraw := transition(t, "ana", name, "withdrawn")
	refund := item.Principal{Actor: "reaction:vacation_request:refund"}
	rc := cause("reaction:vacation_request@v3:refund", withdraw.ItemSeq)
	if _, err := k.Internal().Undo(testCtx(t), itemservice.UndoRequest{Principal: refund, SourceID: id, HoldID: approveHold, Attribution: rc}); err != nil {
		t.Fatalf("Undo: %v", err)
	}
	if _, err := k.Internal().ReactionApplied(testCtx(t), itemservice.ReactionAppliedRequest{Principal: refund, SourceID: id,
		HoldID: approveHold, Attribution: rc}); err != nil {
		t.Fatalf("ReactionApplied: %v", err)
	}
	events := history(t, "ana", name)
	last := events[len(events)-1]
	if last.Kind != kernelv1.ItemEventKind_ITEM_EVENT_KIND_REACTION_APPLIED || last.Reaction != "vacation_request:refund" {
		t.Errorf("event = %v, want REACTION_APPLIED of vacation_request:refund", last)
	}
}
