// End-to-end tests for proof-of-concept Step 2, part 2: the leave balance,
// run by the real worker through the relay, on Postgres and a Temporal dev
// server, plus the reaction activities against the real kernel.
//
//   - Submitting links the request to its owner's open balance (in the HR
//     check's resolution); approving deducts f_days through the held
//     reaction, or is DENIED insufficient_balance.
//   - Withdrawing an approved request refunds it; withdrawing during the
//     approval hold leaves nothing taken; two approvals racing on one
//     balance never overdraw it.
//   - LoadItem reports reactions, the entering hold and the pending undos;
//     RunReaction resolves its hold (allow, or deny with the refusal);
//     Undo refunds once and reports a refusal as non-retryable Rejected,
//     the code as its detail.
package workflow_test

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.temporal.io/sdk/temporal"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
	"github.com/francoisnyc/work-kernel/internal/typedef"
	wf "github.com/francoisnyc/work-kernel/internal/workflow"
)

type leaveStack struct {
	*hrStack
	ana, bob, hana item.Principal
}

func registerTypeFile(t *testing.T, name string) {
	t.Helper()
	data, err := os.ReadFile("../../types/" + name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	ty, err := typedef.Parse(data)
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	if err := itemservice.New(pool).RegisterType(serverCtx(t), ty); err != nil {
		t.Fatalf("register %s: %v", name, err)
	}
}

func newLeaveStack(t *testing.T) *leaveStack {
	t.Helper()
	registerTypeFile(t, "leave_balance.v1.yaml")
	registerTypeFile(t, "vacation_request.v2.yaml")
	s := &leaveStack{hrStack: newHRStack(t), ana: user("ana"), bob: user("bob"), hana: user("hana")}
	s.hr.SetManager(s.ana.Actor, s.bob.Actor)
	if _, err := pool.Exec(serverCtx(t),
		`insert into principal_roles (namespace_id, principal, role) values ('default', $1, 'hr_admin')`, s.hana.Actor); err != nil {
		t.Fatalf("grant hr_admin: %v", err)
	}
	return s
}

func (s *leaveStack) balance(allowance int) uuid.UUID {
	s.t.Helper()
	res, err := s.k.Create(serverCtx(s.t), itemservice.CreateRequest{IdempotencyKey: "key-" + uuid.NewString(),
		Principal: s.hana, TypeID: "leave_balance", TypeVersion: 1,
		Fields: map[string]any{"f_owner": s.ana.Actor, "f_allowance": allowance}})
	if err != nil {
		s.t.Fatalf("create balance: %v", err)
	}
	return res.ItemID
}

// submittedRequest: ana's v2 request of `days`, submitted with --wait (the
// HR check names bob).
func (s *leaveStack) submittedRequest(days int) uuid.UUID {
	s.t.Helper()
	res, err := s.k.Create(serverCtx(s.t), itemservice.CreateRequest{IdempotencyKey: "key-" + uuid.NewString(),
		Principal: s.ana, TypeID: "vacation_request", TypeVersion: 2,
		Fields: map[string]any{"f_start": "2099-07-01", "f_end": "2099-07-05", "f_days": days, "f_leave_type": "annual"}})
	if err != nil {
		s.t.Fatalf("create request: %v", err)
	}
	if out := s.submit(s.ana, res.ItemID); out.Outcome != itemservice.OutcomeApplied {
		s.t.Fatalf("submit = %+v, want applied", out)
	}
	return res.ItemID
}

func (s *leaveStack) transition(p item.Principal, id uuid.UUID, to string, wait time.Duration) itemservice.TransitionResult {
	s.t.Helper()
	res, err := s.k.Transition(serverCtx(s.t), itemservice.TransitionRequest{IdempotencyKey: "key-" + uuid.NewString(),
		Principal: p, ItemID: id, ToState: to, Wait: wait})
	if err != nil {
		s.t.Fatalf("transition to %s: %v", to, err)
	}
	return res
}

func (s *leaveStack) used(bal uuid.UUID) int64 {
	s.t.Helper()
	it, err := s.k.Get(serverCtx(s.t), s.hana, bal)
	if err != nil {
		s.t.Fatalf("Get balance: %v", err)
	}
	n, _ := typedef.AsInt(it.Fields["f_used"]) // unset is 0
	return n
}

func reactionEvents(t *testing.T, bal uuid.UUID, reaction string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(serverCtx(t), `select count(*) from item_events where item_id = $1 and actor = $2`,
		bal, "reaction:vacation_request:"+reaction).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestE2E_Leave_ApprovalDeducts(t *testing.T) {
	s := newLeaveStack(t)
	bal := s.balance(25)
	id := s.submittedRequest(5)
	res := s.transition(s.bob, id, "approved", itemservice.MaxWait)
	if res.Outcome != itemservice.OutcomeApplied {
		t.Fatalf("approve = %+v, want applied within the wait", res)
	}
	if got := s.used(bal); got != 5 {
		t.Errorf("f_used = %d, want 5", got)
	}
}

func TestE2E_Leave_BeyondTheBalanceIsDenied(t *testing.T) {
	s := newLeaveStack(t)
	bal := s.balance(4)
	id := s.submittedRequest(5)
	res := s.transition(s.bob, id, "approved", itemservice.MaxWait)
	if res.Outcome != itemservice.OutcomeDenied || res.Reason != "insufficient_balance" {
		t.Fatalf("approve = %+v, want denied insufficient_balance", res)
	}
	if got := s.used(bal); got != 0 {
		t.Errorf("f_used = %d, want 0", got)
	}
}

func TestE2E_Leave_WithdrawingAnApprovedRequestRefunds(t *testing.T) {
	s := newLeaveStack(t)
	bal := s.balance(25)
	id := s.submittedRequest(5)
	if res := s.transition(s.bob, id, "approved", itemservice.MaxWait); res.Outcome != itemservice.OutcomeApplied {
		t.Fatalf("approve = %+v", res)
	}
	s.transition(s.ana, id, "withdrawn", 0)
	eventually(t, 15*time.Second, "the refund", func() bool { return s.used(bal) == 0 })
	staysFor(t, time.Second, "one refund", func() bool { return reactionEvents(t, bal, "refund") == 1 })
}

// Withdrawn while the approval waits (the worker is down, so the deduction
// cannot have run): the hold is canceled, and once the worker is back
// nothing is taken.
func TestE2E_Leave_WithdrawnDuringTheApproval(t *testing.T) {
	s := newLeaveStack(t)
	bal := s.balance(25)
	id := s.submittedRequest(5)
	s.stopWorker()
	if res := s.transition(s.bob, id, "approved", 0); res.Outcome != itemservice.OutcomePending {
		t.Fatalf("approve = %+v, want pending", res)
	}
	s.transition(s.ana, id, "withdrawn", 0)
	s.startHRWorker()
	s.waitCompleted(id)
	if got := s.used(bal); got != 0 {
		t.Errorf("f_used = %d, want 0", got)
	}
	if d, r := reactionEvents(t, bal, "deduct"), reactionEvents(t, bal, "refund"); d != r {
		t.Errorf("deductions %d, refunds %d; want equal", d, r)
	}
}

// Two approvals at once, 15 days each, 20 left: one applies, the other is
// denied insufficient_balance; the balance is never overdrawn.
func TestE2E_Leave_TwoApprovalsRace(t *testing.T) {
	s := newLeaveStack(t)
	bal := s.balance(20)
	ids := []uuid.UUID{s.submittedRequest(15), s.submittedRequest(15)}
	results := make([]itemservice.TransitionResult, 2)
	var wg sync.WaitGroup
	for i, id := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := s.k.Transition(serverCtx(t), itemservice.TransitionRequest{IdempotencyKey: "key-" + uuid.NewString(),
				Principal: s.bob, ItemID: id, ToState: "approved", Wait: itemservice.MaxWait})
			if err != nil {
				t.Errorf("approve: %v", err)
			}
			results[i] = res
		}()
	}
	wg.Wait()
	applied, denied := 0, 0
	for _, r := range results {
		switch {
		case r.Outcome == itemservice.OutcomeApplied:
			applied++
		case r.Outcome == itemservice.OutcomeDenied && r.Reason == "insufficient_balance":
			denied++
		}
	}
	if applied != 1 || denied != 1 {
		t.Errorf("outcomes = %+v, want one applied, one denied insufficient_balance", results)
	}
	if got := s.used(bal); got != 15 {
		t.Errorf("f_used = %d, want 15", got)
	}
}

// startHRWorker starts the stack's worker again, with the HR directory.
func (s *leaveStack) startHRWorker() {
	s.t.Helper()
	s.worker = wf.NewWorker(tc, s.queue, s.k, wf.WorkerOptions{
		Connectors: s.remoteHR(), HookBudget: testBudget,
	})
	if err := s.worker.Start(); err != nil {
		s.t.Fatalf("start worker: %v", err)
	}
}

// --- the activities against the real kernel ---

// LoadItem reports the reactions and, after an escape that closed a hold
// whose deduction had committed, the undo to run. The Undo activity then
// refunds, once; RunReaction resolves its hold.
func TestActivities_Reactions(t *testing.T) {
	s := newLeaveStack(t)
	s.stopRelay() // the activities are called by hand here
	s.stopWorker()
	a := &wf.Activities{Items: s.k}
	bal := s.balance(25)
	id := s.submittedByHand(t, 5)

	ask := s.transition(s.bob, id, "approved", 0)
	in := wf.ReactionInput{ItemID: id.String(), TypeID: "vacation_request", TypeVersion: 2, ReactionID: "deduct",
		HoldID: ask.HoldID.String(), CauseSeq: ask.ItemSeq, Hop: 1}
	if _, err := s.k.Internal().React(serverCtx(t), itemservice.ReactRequest{
		Principal: item.Principal{Actor: "reaction:vacation_request:deduct"}, SourceID: id, HoldID: ask.HoldID,
		Attribution: item.Attribution{OriginatingHook: "reaction:vacation_request@v2:deduct",
			CausationID: fmt.Sprintf("%s:%d", id, ask.ItemSeq), Hop: 1}}); err != nil {
		t.Fatalf("React: %v", err)
	}
	withdraw := s.transition(s.ana, id, "withdrawn", 0) // the escape cancels the hold after the deduction

	snap, err := a.LoadItem(serverCtx(t), id.String())
	if err != nil {
		t.Fatalf("LoadItem: %v", err)
	}
	if snap.HeldReactions["submitted->approved"] != "deduct" || snap.Undos["deduct"] != "refund" ||
		snap.UndoOnEntry["approved->withdrawn"] != "refund" {
		t.Errorf("snapshot reactions = %v, %v, %v", snap.HeldReactions, snap.Undos, snap.UndoOnEntry)
	}
	wantPending := []wf.PendingUndo{{ReactionID: "refund", HoldID: ask.HoldID.String(), CauseSeq: withdraw.ItemSeq}}
	if len(snap.PendingUndos) != 1 || snap.PendingUndos[0] != wantPending[0] {
		t.Errorf("PendingUndos = %+v, want %+v", snap.PendingUndos, wantPending)
	}

	undo := wf.ReactionInput{ItemID: id.String(), TypeID: "vacation_request", TypeVersion: 2, ReactionID: "refund",
		HoldID: ask.HoldID.String(), CauseSeq: withdraw.ItemSeq, Hop: 1}
	for i := 0; i < 2; i++ { // the second time is a retry after a crash
		if err := a.Undo(serverCtx(t), undo); err != nil {
			t.Fatalf("Undo #%d: %v", i+1, err)
		}
	}
	if got := s.used(bal); got != 0 || reactionEvents(t, bal, "refund") != 1 {
		t.Errorf("f_used %d, refunds %d; want 0 and 1", got, reactionEvents(t, bal, "refund"))
	}
	if snap, _ := a.LoadItem(serverCtx(t), id.String()); len(snap.PendingUndos) != 0 {
		t.Errorf("PendingUndos after the undo = %+v", snap.PendingUndos)
	}
	// RunReaction for a closed hold: nothing left to decide, not an error.
	if err := a.RunReaction(serverCtx(t), in); err != nil {
		t.Errorf("RunReaction after the escape: %v, want nil", err)
	}

	// RunReaction allows, and the approval applies; the entering hold is known.
	id2 := s.submittedByHand(t, 5)
	ask2 := s.transition(s.bob, id2, "approved", 0)
	in2 := wf.ReactionInput{ItemID: id2.String(), TypeID: "vacation_request", TypeVersion: 2, ReactionID: "deduct",
		HoldID: ask2.HoldID.String(), CauseSeq: ask2.ItemSeq, Hop: 1}
	if err := a.RunReaction(serverCtx(t), in2); err != nil {
		t.Fatalf("RunReaction: %v", err)
	}
	if it := s.item(s.ana, id2); it.State != "approved" || it.Hold == nil || it.Hold.Status != item.HoldAllowed {
		t.Errorf("after RunReaction: %s, hold %+v; want approved, allowed", it.State, it.Hold)
	}
	if snap, _ := a.LoadItem(serverCtx(t), id2.String()); snap.EnteredHold != ask2.HoldID.String() {
		t.Errorf("EnteredHold = %q, want %s", snap.EnteredHold, ask2.HoldID)
	}

	// RunReaction denies with the refusal: beyond the balance.
	id3 := s.submittedByHand(t, 21)
	ask3 := s.transition(s.bob, id3, "approved", 0)
	if err := a.RunReaction(serverCtx(t), wf.ReactionInput{ItemID: id3.String(), TypeID: "vacation_request", TypeVersion: 2,
		ReactionID: "deduct", HoldID: ask3.HoldID.String(), CauseSeq: ask3.ItemSeq, Hop: 1}); err != nil {
		t.Fatalf("RunReaction: %v", err)
	}
	if it := s.item(s.ana, id3); it.Hold == nil || it.Hold.Status != item.HoldDenied || it.Hold.Reason != "insufficient_balance" {
		t.Errorf("hold = %+v, want denied insufficient_balance", it.Hold)
	}
}

// An undo the target refuses is a non-retryable Rejected, with the code as
// its detail, so the workflow records it with ReactionFailed.
func TestActivities_UndoRefused_IsANonRetryableRejectedWithTheCode(t *testing.T) {
	s := newLeaveStack(t)
	s.stopRelay()
	s.stopWorker()
	a := &wf.Activities{Items: s.k}
	refuseRefunds := `
id: leave_balance
version: 92
initial_state: open
fields:
  - {id: f_owner,     type: principal, writable_by: ['role:hr_admin']}
  - {id: f_allowance, type: int,       writable_by: ['role:hr_admin']}
  - {id: f_used,      type: int,       writable_by: kernel}
states:
  - {id: open,   category: active}
  - {id: closed, category: done}
transitions:
  - {from: open, to: closed, allowed: ['role:hr_admin']}
accepts_reactions:
  vacation_request:deduct: [f_used]
`
	ty, err := typedef.Parse([]byte(refuseRefunds))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.k.RegisterType(serverCtx(t), ty); err != nil {
		t.Fatal(err)
	}
	if _, err := s.k.Create(serverCtx(t), itemservice.CreateRequest{IdempotencyKey: "key-" + uuid.NewString(), Principal: s.hana,
		TypeID: "leave_balance", TypeVersion: 92, Fields: map[string]any{"f_owner": s.ana.Actor, "f_allowance": 25}}); err != nil {
		t.Fatal(err)
	}
	id := s.submittedByHand(t, 5)
	ask := s.transition(s.bob, id, "approved", 0)
	if err := a.RunReaction(serverCtx(t), wf.ReactionInput{ItemID: id.String(), TypeID: "vacation_request", TypeVersion: 2,
		ReactionID: "deduct", HoldID: ask.HoldID.String(), CauseSeq: ask.ItemSeq, Hop: 1}); err != nil {
		t.Fatalf("RunReaction: %v", err)
	}
	withdraw := s.transition(s.ana, id, "withdrawn", 0)
	err = a.Undo(serverCtx(t), wf.ReactionInput{ItemID: id.String(), TypeID: "vacation_request", TypeVersion: 2,
		ReactionID: "refund", HoldID: ask.HoldID.String(), CauseSeq: withdraw.ItemSeq, Hop: 1})
	var app *temporal.ApplicationError
	if !errors.As(err, &app) || app.Type() != wf.ErrTypeRejected || !app.NonRetryable() {
		t.Fatalf("Undo = %v, want a non-retryable %s", err, wf.ErrTypeRejected)
	}
	var code string
	if err := app.Details(&code); err != nil || code != "reaction_not_accepted" {
		t.Errorf("detail = %q (%v), want reaction_not_accepted", code, err)
	}
}

// submittedByHand walks ana's v2 request into submitted without the
// worker: the HR check is played through ResolveHold.
func (s *leaveStack) submittedByHand(t *testing.T, days int) uuid.UUID {
	t.Helper()
	res, err := s.k.Create(serverCtx(t), itemservice.CreateRequest{IdempotencyKey: "key-" + uuid.NewString(),
		Principal: s.ana, TypeID: "vacation_request", TypeVersion: 2,
		Fields: map[string]any{"f_start": "2099-07-01", "f_end": "2099-07-05", "f_days": days, "f_leave_type": "annual"}})
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	ask := s.transition(s.ana, res.ItemID, "submitted", 0)
	if _, err := s.k.Internal().ResolveHold(serverCtx(t), itemservice.ResolveHoldRequest{
		IdempotencyKey: "hold:" + ask.HoldID.String(), Principal: item.Principal{Actor: "hook:vacation_request:hr_check"},
		ItemID: res.ItemID, HoldID: ask.HoldID, Verdict: itemservice.VerdictCorrect,
		Fields: map[string]any{"f_approver": s.bob.Actor},
		Attribution: item.Attribution{OriginatingHook: "hook:vacation_request@v2:hr_check",
			CausationID: fmt.Sprintf("%s:%d", res.ItemID, ask.ItemSeq), Hop: 1}}); err != nil {
		t.Fatalf("HR: %v", err)
	}
	return res.ItemID
}
