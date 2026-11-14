// Tests for Step 6, part 2: holds, the bounded wait, and hook writes.
//
// A transition with a check hook (ticket v3: in_progress -> approved, checked
// by erp_check) does not move the Item when asked. In one transaction the
// item service checks the requester (guards, `allowed`), writes a
// transition_requested event and opens a hold. The check then settles it
// through Internal().ResolveHold:
//
//   - allow: the requester's transition is applied, with `allowed` and the
//     guards checked against the requester again. If they now fail, it
//     becomes transition_denied.
//   - correct: allow, with the hook's field patch applied with the move.
//   - deny: a transition_denied event; the Item stays.
//   - error: a transition_failed event; the Item stays, the hold shows why.
//
// While the hold is open, the fields the check reads are frozen, and only a
// declared escape may leave the state (it cancels the hold). A hook never
// moves the Item on its own authority: the resolving event records the hook
// as actor and the requester as on_behalf_of.
package itemservice_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
)

var (
	erpCheckV3   = item.Principal{Actor: "hook:ticket:erp_check"}
	getPlanV3    = item.Principal{Actor: "hook:ticket:get_plan"}
	getStatusV3  = item.Principal{Actor: "hook:ticket:get_credit_status"}
	approveCorr  = "corr-approve"
	approveState = "approved"
)

// --- helpers ---

// v3Ticket registers ticket v3 and walks a new ticket, created by a new
// approver, to in_progress: item_seq 1 create (triage), 2 open, 3 in_progress.
func v3Ticket(t *testing.T, k *itemservice.Service) (uuid.UUID, item.Principal) {
	t.Helper()
	registerFromFile(t, k, "ticket.v3.yaml")
	ana := person("ana")
	grantRole(t, ana, "approver")
	res, err := k.Create(testCtx(t), itemservice.CreateRequest{
		IdempotencyKey: newKey(), Principal: ana, TypeID: "ticket", TypeVersion: 3,
		Fields: map[string]any{
			"f_title": "Onboarding", "f_customer_id": "cust-1", "f_amount": 100, "f_assignee": "user-bob",
		},
	})
	if err != nil {
		t.Fatalf("create ticket v3: %v", err)
	}
	for _, to := range []string{"open", "in_progress"} {
		if _, err := k.Transition(testCtx(t), itemservice.TransitionRequest{
			IdempotencyKey: newKey(), Principal: ana, ItemID: res.ItemID, ToState: to,
		}); err != nil {
			t.Fatalf("transition to %s: %v", to, err)
		}
	}
	return res.ItemID, ana
}

// approve asks for in_progress -> approved as p.
func approve(t *testing.T, k *itemservice.Service, p item.Principal, id uuid.UUID, key string,
	wait time.Duration) (itemservice.TransitionResult, error) {
	t.Helper()
	return k.Transition(testCtx(t), itemservice.TransitionRequest{
		IdempotencyKey: key, Principal: p, ItemID: id, ToState: approveState, Wait: wait,
		Attribution: item.Attribution{CorrelationID: approveCorr},
	})
}

// requestHold asks for the approval and expects a pending hold at item_seq 4.
func requestHold(t *testing.T, k *itemservice.Service, p item.Principal, id uuid.UUID) itemservice.TransitionResult {
	t.Helper()
	res, err := approve(t, k, p, id, newKey(), 0)
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	if res.Outcome != itemservice.OutcomePending || res.HoldID == uuid.Nil || res.ItemSeq != 4 {
		t.Fatalf("approve = %+v, want pending, with a hold, at item_seq 4", res)
	}
	return res
}

// v3Cause is the attribution a hook of ticket v3 writes with: caused by
// event seq of the Item, one hop down, in the approval's correlation.
func v3Cause(p item.Principal, id uuid.UUID, seq int64) item.Attribution {
	_, hookID, _ := item.HookParts(p.Actor)
	return item.Attribution{
		OriginatingHook: "hook:ticket@v3:" + hookID,
		CausationID:     fmt.Sprintf("%s:%d", id, seq),
		CorrelationID:   approveCorr,
		Hop:             1,
	}
}

// resolve settles the hold as erp_check, caused by the transition_requested
// event (item_seq 4), with the key the hook runner uses.
func resolve(t *testing.T, k *itemservice.Service, id, holdID uuid.UUID, v itemservice.Verdict,
	fields map[string]any, reason string) (itemservice.WriteResult, error) {
	t.Helper()
	return k.Internal().ResolveHold(testCtx(t), itemservice.ResolveHoldRequest{
		IdempotencyKey: "hold:" + holdID.String(), Principal: erpCheckV3, ItemID: id, HoldID: holdID,
		Verdict: v, Fields: fields, Reason: reason, Attribution: v3Cause(erpCheckV3, id, 4),
	})
}

// event reads one event's op and payload.
func event(t *testing.T, id uuid.UUID, seq int64) (op string, payload map[string]any) {
	t.Helper()
	var raw []byte
	err := pool.QueryRow(testCtx(t),
		`select op, payload from item_events where item_id = $1 and item_seq = $2`, id, seq).Scan(&op, &raw)
	if err != nil {
		t.Fatalf("read event %d: %v", seq, err)
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("decode payload of event %d: %v", seq, err)
	}
	return op, payload
}

func commitSeqOf(t *testing.T, id uuid.UUID, seq int64) int64 {
	t.Helper()
	var cs int64
	if err := pool.QueryRow(testCtx(t),
		`select commit_seq from item_events where item_id = $1 and item_seq = $2`, id, seq).Scan(&cs); err != nil {
		t.Fatalf("read commit_seq of event %d: %v", seq, err)
	}
	return cs
}

func holdOf(t *testing.T, k *itemservice.Service, p item.Principal, id uuid.UUID) (string, *item.Hold) {
	t.Helper()
	it, err := k.Get(testCtx(t), p, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	return it.State, it.Hold
}

// assertHeldErr checks a refusal because of an open hold: ErrHeld, which is
// also ErrStale (re-read and retry later), naming the hold.
func assertHeldErr(t *testing.T, what string, err error, holdID uuid.UUID) {
	t.Helper()
	if !errors.Is(err, itemservice.ErrHeld) || !errors.Is(err, itemservice.ErrStale) {
		t.Fatalf("%s: error = %v, want ErrHeld (an ErrStale)", what, err)
	}
	if errors.Is(err, itemservice.ErrRejected) || errors.Is(err, itemservice.ErrForbidden) {
		t.Errorf("%s: error %v also matches ErrRejected or ErrForbidden", what, err)
	}
	if !strings.Contains(err.Error(), holdID.String()) {
		t.Errorf("%s: error %q does not name the hold %s", what, err, holdID)
	}
}

func assertOutcome(t *testing.T, what string, res itemservice.TransitionResult, err error,
	want itemservice.Outcome, wantSeq int64) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
	if res.Outcome != want || res.ItemSeq != wantSeq {
		t.Errorf("%s = %+v, want outcome %s at item_seq %d", what, res, want, wantSeq)
	}
}

// --- requesting a checked transition ---

// Asking for a checked transition opens a hold: one transition_requested
// event (no `to`: it enters no state), one outbox row, and the Item stays
// where it is. The hold is visible on the Item.
func TestHold_RequestOpensAHold(t *testing.T) {
	k := itemservice.New(pool)
	id, ana := v3Ticket(t, k)

	res := requestHold(t, k, ana, id)

	if res.CommitSeq != commitSeqOf(t, id, 4) || res.Reason != "" {
		t.Errorf("result = %+v, want the commit_seq of event 4 and no reason", res)
	}
	op, payload := event(t, id, 4)
	want := map[string]any{"from": "in_progress", "requested_to": "approved", "hold_id": res.HoldID.String()}
	if op != "transition_requested" || !reflect.DeepEqual(payload, want) {
		t.Errorf("event 4 = %s %v, want transition_requested %v", op, payload, want)
	}
	actor, attr := attribution(t, id, 4)
	if actor != ana.Actor || attr.CorrelationID != approveCorr {
		t.Errorf("event 4 by %s in %q, want %s in %q", actor, attr.CorrelationID, ana.Actor, approveCorr)
	}
	if n := count(t, `select count(*) from outbox where item_id = $1 and item_seq = 4`, id); n != 1 {
		t.Errorf("outbox rows for event 4 = %d, want 1", n)
	}
	state, hold := holdOf(t, k, ana, id)
	wantHold := &item.Hold{ID: res.HoldID, Hook: "erp_check", From: "in_progress", To: "approved",
		Status: item.HoldOpen}
	if state != "in_progress" || !reflect.DeepEqual(hold, wantHold) {
		t.Errorf("Item in %q with hold %+v; want in_progress with %+v", state, hold, wantHold)
	}
}

// A field patch sent with the request is written with it, in the same event,
// and the guards see the patched fields.
func TestHold_PatchIsWrittenWithTheRequest(t *testing.T) {
	k := itemservice.New(pool)
	id, ana := v3Ticket(t, k)
	res, err := k.Transition(testCtx(t), itemservice.TransitionRequest{
		IdempotencyKey: newKey(), Principal: ana, ItemID: id, ToState: approveState,
		Fields: map[string]any{"f_amount": 250},
	})
	assertOutcome(t, "approve with a patch", res, err, itemservice.OutcomePending, 4)
	if it := get(t, k, id); it.Fields["f_amount"] != float64(250) {
		t.Errorf("f_amount = %v, want 250", it.Fields["f_amount"])
	}
	if _, payload := event(t, id, 4); !reflect.DeepEqual(payload["field_ids"], []any{"f_amount"}) {
		t.Errorf("payload = %v, want field_ids [f_amount]", payload)
	}
}

// The requester is checked when the hold is requested: `allowed`, required
// fields and guards. A refusal opens no hold and writes nothing. With the
// prefetched on_hold fact, variant D answers at once, without the ERP.
func TestHold_RequestChecksTheRequesterFirst(t *testing.T) {
	k := itemservice.New(pool)

	t.Run("not allowed", func(t *testing.T) {
		id, _ := v3Ticket(t, k)
		_, err := approve(t, k, person("bob"), id, newKey(), 0)
		assertForbidden(t, err)
		assertNothingWritten(t, id, 3)
	})
	t.Run("the prefetched fact says on hold", func(t *testing.T) {
		id, ana := v3Ticket(t, k)
		up, err := k.Internal().Update(testCtx(t), itemservice.UpdateRequest{
			IdempotencyKey: newKey(), Principal: getStatusV3, ItemID: id,
			Fields: map[string]any{"f_on_hold": true}, Attribution: v3Cause(getStatusV3, id, 1),
		})
		if err != nil {
			t.Fatalf("prefetch write: %v", err)
		}
		start := time.Now()
		_, err = approve(t, k, ana, id, newKey(), 3*time.Second)
		if !errors.Is(err, itemservice.ErrRejected) || !strings.Contains(err.Error(), "f_on_hold") {
			t.Fatalf("approve: error = %v, want ErrRejected naming the f_on_hold guard", err)
		}
		if took := time.Since(start); took > time.Second {
			t.Errorf("the guard answered after %v; it must not wait", took)
		}
		assertNothingWritten(t, id, int(up.ItemSeq))
	})
	t.Run("a required field is missing", func(t *testing.T) {
		id, ana := v3Ticket(t, k)
		update(t, k, id, map[string]any{"f_amount": nil})
		_, err := approve(t, k, ana, id, newKey(), 0)
		if !errors.Is(err, itemservice.ErrRejected) || !strings.Contains(err.Error(), "f_amount") {
			t.Fatalf("approve: error = %v, want ErrRejected naming f_amount", err)
		}
		assertNothingWritten(t, id, 4)
	})
}

// --- resolving it ---

// allow applies the requester's transition. The event records the hook as
// actor and the requester as on_behalf_of, caused by the request, one hop
// down. The requester's retry of the same key now reports the outcome.
func TestHold_AllowAppliesTheRequestersTransition(t *testing.T) {
	k := itemservice.New(pool)
	id, ana := v3Ticket(t, k)
	key := newKey()
	req, err := approve(t, k, ana, id, key, 0)
	assertOutcome(t, "approve", req, err, itemservice.OutcomePending, 4)

	res, err := resolve(t, k, id, req.HoldID, itemservice.VerdictAllow, nil, "")
	if err != nil {
		t.Fatalf("ResolveHold allow: %v", err)
	}
	if res.ItemSeq != 5 {
		t.Errorf("ResolveHold = %+v, want item_seq 5", res)
	}
	op, payload := event(t, id, 5)
	want := map[string]any{"from": "in_progress", "to": "approved", "hold_id": req.HoldID.String()}
	if op != "transition" || !reflect.DeepEqual(payload, want) {
		t.Errorf("event 5 = %s %v, want transition %v", op, payload, want)
	}
	assertKernelEvent(t, id, 5, erpCheckV3.Actor, ana.Actor, v3Cause(erpCheckV3, id, 4))

	state, hold := holdOf(t, k, ana, id)
	if state != "approved" || hold == nil || hold.Status != item.HoldAllowed {
		t.Errorf("Item in %q with hold %+v; want approved, hold allowed", state, hold)
	}
	again, err := approve(t, k, ana, id, key, 0)
	assertOutcome(t, "retry of the request", again, err, itemservice.OutcomeApplied, 5)
	if again.CommitSeq != commitSeqOf(t, id, 5) || again.HoldID != req.HoldID {
		t.Errorf("retry = %+v, want the commit_seq of event 5 and the same hold", again)
	}
}

// correct is allow with the hook's field patch, applied with the move and
// recorded as supplied by the hook. It never chooses another target state.
func TestHold_CorrectAppliesThePatchWithTheMove(t *testing.T) {
	k := itemservice.New(pool)
	id, ana := v3Ticket(t, k)
	req := requestHold(t, k, ana, id)

	if _, err := resolve(t, k, id, req.HoldID, itemservice.VerdictCorrect,
		map[string]any{"f_cost_center": "CC-9"}, ""); err != nil {
		t.Fatalf("ResolveHold correct: %v", err)
	}
	it := get(t, k, id)
	if it.State != "approved" || it.Fields["f_cost_center"] != "CC-9" {
		t.Errorf("Item = %s %v, want approved with f_cost_center CC-9", it.State, it.Fields)
	}
	op, payload := event(t, id, 5)
	if op != "transition" || payload["to"] != "approved" || !reflect.DeepEqual(payload["field_ids"], []any{"f_cost_center"}) {
		t.Errorf("event 5 = %s %v, want a transition to approved with field_ids [f_cost_center]", op, payload)
	}
	assertKernelEvent(t, id, 5, erpCheckV3.Actor, ana.Actor, v3Cause(erpCheckV3, id, 4))
}

// deny records transition_denied with a reason code; the Item stays, and the
// requester's retry reports the denial.
func TestHold_DenyKeepsTheItemWhereItIs(t *testing.T) {
	k := itemservice.New(pool)
	id, ana := v3Ticket(t, k)
	key := newKey()
	req, err := approve(t, k, ana, id, key, 0)
	assertOutcome(t, "approve", req, err, itemservice.OutcomePending, 4)

	if _, err := resolve(t, k, id, req.HoldID, itemservice.VerdictDeny, nil, "customer_on_hold"); err != nil {
		t.Fatalf("ResolveHold deny: %v", err)
	}
	op, payload := event(t, id, 5)
	want := map[string]any{"from": "in_progress", "requested_to": "approved",
		"hold_id": req.HoldID.String(), "reason": "customer_on_hold"}
	if op != "transition_denied" || !reflect.DeepEqual(payload, want) {
		t.Errorf("event 5 = %s %v, want transition_denied %v", op, payload, want)
	}
	assertKernelEvent(t, id, 5, erpCheckV3.Actor, ana.Actor, v3Cause(erpCheckV3, id, 4))

	state, hold := holdOf(t, k, ana, id)
	if state != "in_progress" || hold == nil || hold.Status != item.HoldDenied || hold.Reason != "customer_on_hold" {
		t.Errorf("Item in %q with hold %+v; want in_progress, hold denied for customer_on_hold", state, hold)
	}
	again, err := approve(t, k, ana, id, key, 0)
	assertOutcome(t, "retry of the request", again, err, itemservice.OutcomeDenied, 5)
	if again.Reason != "customer_on_hold" {
		t.Errorf("retry reason = %q, want customer_on_hold", again.Reason)
	}
}

// When the check cannot answer (its budget ran out), the hold resolves as
// error: transition_failed, the Item stays, the hold shows the reason, and
// the frozen fields are free again. The requester may ask again.
func TestHold_ErrorIsVisibleAndReleasesTheHold(t *testing.T) {
	k := itemservice.New(pool)
	id, ana := v3Ticket(t, k)
	key := newKey()
	req, err := approve(t, k, ana, id, key, 0)
	assertOutcome(t, "approve", req, err, itemservice.OutcomePending, 4)

	if _, err := resolve(t, k, id, req.HoldID, itemservice.VerdictError, nil, "budget_exhausted"); err != nil {
		t.Fatalf("ResolveHold error: %v", err)
	}
	op, payload := event(t, id, 5)
	if op != "transition_failed" || payload["reason"] != "budget_exhausted" || payload["hold_id"] != req.HoldID.String() {
		t.Errorf("event 5 = %s %v, want transition_failed for budget_exhausted", op, payload)
	}
	state, hold := holdOf(t, k, ana, id)
	if state != "in_progress" || hold == nil || hold.Status != item.HoldError || hold.Reason != "budget_exhausted" {
		t.Errorf("Item in %q with hold %+v; want in_progress, hold error for budget_exhausted", state, hold)
	}
	again, err := approve(t, k, ana, id, key, 0)
	assertOutcome(t, "retry of the request", again, err, itemservice.OutcomeError, 5)
	if again.Reason != "budget_exhausted" {
		t.Errorf("retry reason = %q, want budget_exhausted", again.Reason)
	}

	update(t, k, id, map[string]any{"f_amount": 120}) // item_seq 6: no longer frozen
	next, err := approve(t, k, ana, id, newKey(), 0)
	assertOutcome(t, "a new request", next, err, itemservice.OutcomePending, 7)
	if next.HoldID == req.HoldID {
		t.Error("the new request reused the old hold")
	}
}

// allow checks the requester again: `allowed` (roles may have changed) and
// the guards (against the fields as they are now). A failure becomes
// transition_denied, never an applied move.
func TestHold_AllowRechecksTheRequester(t *testing.T) {
	k := itemservice.New(pool)

	t.Run("the requester lost the role", func(t *testing.T) {
		id, ana := v3Ticket(t, k)
		req := requestHold(t, k, ana, id)
		revokeRole(t, ana, "approver")
		if _, err := resolve(t, k, id, req.HoldID, itemservice.VerdictAllow, nil, ""); err != nil {
			t.Fatalf("ResolveHold allow: %v", err)
		}
		op, payload := event(t, id, 5)
		if op != "transition_denied" || payload["reason"] != "forbidden" {
			t.Errorf("event 5 = %s %v, want transition_denied for forbidden", op, payload)
		}
		if state, _ := holdOf(t, k, ana, id); state != "in_progress" {
			t.Errorf("state = %q, want in_progress", state)
		}
	})
	t.Run("the guard no longer holds", func(t *testing.T) {
		id, ana := v3Ticket(t, k)
		req := requestHold(t, k, ana, id)
		// A late prefetch lands while the hold is open: f_on_hold is not
		// one of the fields the check reads, so it is not frozen.
		if _, err := k.Internal().Update(testCtx(t), itemservice.UpdateRequest{
			IdempotencyKey: newKey(), Principal: getStatusV3, ItemID: id,
			Fields: map[string]any{"f_on_hold": true}, Attribution: v3Cause(getStatusV3, id, 1),
		}); err != nil {
			t.Fatalf("late prefetch: %v", err)
		}
		if _, err := k.Internal().ResolveHold(testCtx(t), itemservice.ResolveHoldRequest{
			IdempotencyKey: "hold:" + req.HoldID.String(), Principal: erpCheckV3, ItemID: id, HoldID: req.HoldID,
			Verdict: itemservice.VerdictAllow, Attribution: v3Cause(erpCheckV3, id, 4),
		}); err != nil {
			t.Fatalf("ResolveHold allow: %v", err)
		}
		op, payload := event(t, id, 6)
		if op != "transition_denied" || payload["reason"] != "guard_failed" {
			t.Errorf("event 6 = %s %v, want transition_denied for guard_failed", op, payload)
		}
	})
}

// A resolution is checked like any kernel write, and more: it must come from
// the hold's own check hook, be caused by the request, stay inside the hook's
// `writes`, and carry a reason code, never free text. Nothing is written.
func TestHold_ResolveIsChecked(t *testing.T) {
	k := itemservice.New(pool)
	id, ana := v3Ticket(t, k)
	req := requestHold(t, k, ana, id)
	good := func() itemservice.ResolveHoldRequest {
		return itemservice.ResolveHoldRequest{
			IdempotencyKey: newKey(), Principal: erpCheckV3, ItemID: id, HoldID: req.HoldID,
			Verdict: itemservice.VerdictDeny, Reason: "customer_on_hold", Attribution: v3Cause(erpCheckV3, id, 4),
		}
	}
	cases := []struct {
		name   string
		change func(r *itemservice.ResolveHoldRequest)
		want   error
	}{
		{"another hook", func(r *itemservice.ResolveHoldRequest) {
			r.Principal, r.Attribution = getPlanV3, v3Cause(getPlanV3, id, 4)
		}, itemservice.ErrInvalid},
		{"the timer", func(r *itemservice.ResolveHoldRequest) {
			r.Principal = item.Principal{Actor: "system:timer"}
			r.Attribution.OriginatingHook = "timer:ticket@v3:in_progress"
		}, itemservice.ErrInvalid},
		{"a person", func(r *itemservice.ResolveHoldRequest) { r.Principal = ana }, itemservice.ErrInvalid},
		{"caused by another event than the request", func(r *itemservice.ResolveHoldRequest) {
			r.Attribution = v3Cause(erpCheckV3, id, 3)
		}, itemservice.ErrInvalid},
		{"a correction outside the hook's writes", func(r *itemservice.ResolveHoldRequest) {
			r.Verdict, r.Reason, r.Fields = itemservice.VerdictCorrect, "", map[string]any{"f_amount": 1}
		}, itemservice.ErrInvalid},
		{"a correction without fields", func(r *itemservice.ResolveHoldRequest) {
			r.Verdict, r.Reason = itemservice.VerdictCorrect, ""
		}, itemservice.ErrInvalid},
		{"a denial with fields", func(r *itemservice.ResolveHoldRequest) {
			r.Fields = map[string]any{"f_cost_center": "CC-1"}
		}, itemservice.ErrInvalid},
		{"a denial without a reason", func(r *itemservice.ResolveHoldRequest) { r.Reason = "" }, itemservice.ErrInvalid},
		{"free text as the reason", func(r *itemservice.ResolveHoldRequest) {
			r.Reason = "Customer ACME is on hold, call Jane"
		}, itemservice.ErrInvalid},
		{"an unknown verdict", func(r *itemservice.ResolveHoldRequest) { r.Verdict = "maybe" }, itemservice.ErrInvalid},
		{"an unknown hold", func(r *itemservice.ResolveHoldRequest) { r.HoldID = uuid.New() }, itemservice.ErrNotFound},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := good()
			c.change(&r)
			_, err := k.Internal().ResolveHold(testCtx(t), r)
			if !errors.Is(err, c.want) {
				t.Fatalf("ResolveHold: error = %v, want %v", err, c.want)
			}
			assertNothingWritten(t, id, 4)
		})
	}
	if _, hold := holdOf(t, k, ana, id); hold == nil || hold.Status != item.HoldOpen {
		t.Errorf("hold = %+v, want still open", hold)
	}
}

// A hold is resolved once. The hook's retry with the same key gets the same
// result; another resolution is stale; the same key with another verdict is
// a key reuse.
func TestHold_ResolvedOnce(t *testing.T) {
	k := itemservice.New(pool)
	id, ana := v3Ticket(t, k)
	req := requestHold(t, k, ana, id)

	first, err := resolve(t, k, id, req.HoldID, itemservice.VerdictAllow, nil, "")
	if err != nil {
		t.Fatalf("ResolveHold: %v", err)
	}
	again, err := resolve(t, k, id, req.HoldID, itemservice.VerdictAllow, nil, "")
	if err != nil || again != first {
		t.Errorf("retry = %+v, %v; want %+v", again, err, first)
	}
	_, err = resolve(t, k, id, req.HoldID, itemservice.VerdictDeny, nil, "customer_on_hold")
	if !errors.Is(err, itemservice.ErrIdempotencyKeyReuse) {
		t.Errorf("same key, other verdict: error = %v, want ErrIdempotencyKeyReuse", err)
	}
	_, err = k.Internal().ResolveHold(testCtx(t), itemservice.ResolveHoldRequest{
		IdempotencyKey: newKey(), Principal: erpCheckV3, ItemID: id, HoldID: req.HoldID,
		Verdict: itemservice.VerdictDeny, Reason: "customer_on_hold", Attribution: v3Cause(erpCheckV3, id, 4),
	})
	assertStale(t, err)
	assertNothingWritten(t, id, 5)
}

// A hook never approves anything on its own authority. The check hook
// cannot make the checked transition itself, even if someone gave it the
// role, and neither can any other kernel actor: only ResolveHold applies it,
// for a requester.
func TestHold_AHookCannotApproveWithoutARequester(t *testing.T) {
	k := itemservice.New(pool)
	id, _ := v3Ticket(t, k)
	try := func(p item.Principal) error {
		_, err := k.Internal().Transition(testCtx(t), itemservice.TransitionRequest{
			IdempotencyKey: newKey(), Principal: p, ItemID: id, ToState: approveState,
			Attribution: v3Cause(p, id, 3),
		})
		return err
	}
	assertForbidden(t, try(erpCheckV3))
	assertForbidden(t, try(getPlanV3))

	grantRole(t, erpCheckV3, "approver")
	t.Cleanup(func() { revokeRole(t, erpCheckV3, "approver") })
	assertForbidden(t, try(erpCheckV3))
	assertNothingWritten(t, id, 3)
	if _, hold := holdOf(t, k, alice, id); hold != nil {
		t.Errorf("hold = %+v, want none", hold)
	}
}

// --- what the hold freezes ---

// While the hold is open, the fields the check reads (f_customer_id,
// f_amount) are frozen: an update touching any of them is refused as held.
// Other fields stay editable, by people and by hooks. Resolution frees them.
func TestHold_FreezesTheFieldsTheCheckReads(t *testing.T) {
	k := itemservice.New(pool)
	id, ana := v3Ticket(t, k)
	req := requestHold(t, k, ana, id)

	for name, fields := range map[string]map[string]any{
		"the amount":             {"f_amount": 1_000_000},
		"the customer and title": {"f_customer_id": "cust-2", "f_title": "x"},
		"clearing the amount":    {"f_amount": nil},
	} {
		_, err := k.Update(testCtx(t), itemservice.UpdateRequest{
			IdempotencyKey: newKey(), Principal: ana, ItemID: id, Fields: fields,
		})
		assertHeldErr(t, "update of "+name, err, req.HoldID)
	}
	assertNothingWritten(t, id, 4)

	update(t, k, id, map[string]any{"f_title": "still editable"}) // 5
	if _, err := k.Internal().Update(testCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: getPlanV3, ItemID: id,
		Fields: map[string]any{"f_plan": "pro"}, Attribution: v3Cause(getPlanV3, id, 1),
	}); err != nil { // 6
		t.Fatalf("hook update of an unfrozen field: %v", err)
	}

	if _, err := k.Internal().ResolveHold(testCtx(t), itemservice.ResolveHoldRequest{
		IdempotencyKey: newKey(), Principal: erpCheckV3, ItemID: id, HoldID: req.HoldID,
		Verdict: itemservice.VerdictDeny, Reason: "customer_on_hold", Attribution: v3Cause(erpCheckV3, id, 4),
	}); err != nil { // 7
		t.Fatalf("ResolveHold: %v", err)
	}
	update(t, k, id, map[string]any{"f_amount": 50}) // 8: free again
}

// While the hold is open, only a declared escape leaves the state. A person,
// a second request for the same transition and the timer are all held.
func TestHold_OnlyAnEscapeLeavesTheState(t *testing.T) {
	k := itemservice.New(pool)
	id, ana := v3Ticket(t, k)
	req := requestHold(t, k, ana, id)

	_, err := k.Transition(testCtx(t), itemservice.TransitionRequest{
		IdempotencyKey: newKey(), Principal: ana, ItemID: id, ToState: "open",
	})
	assertHeldErr(t, "a person's move", err, req.HoldID)
	_, err = approve(t, k, ana, id, newKey(), 0)
	assertHeldErr(t, "a second request", err, req.HoldID)
	_, err = k.Internal().Transition(testCtx(t), itemservice.TransitionRequest{
		IdempotencyKey: newKey(), Principal: item.Principal{Actor: "system:timer"}, ItemID: id, ToState: "open",
		Attribution: item.Attribution{OriginatingHook: "timer:ticket@v3:in_progress",
			CausationID: fmt.Sprintf("%s:3", id), Hop: 1},
	})
	assertHeldErr(t, "the timer's move", err, req.HoldID)
	assertNothingWritten(t, id, 4)
}

// The escape (cancel) leaves the state and cancels the hold, in one event.
// A resolution that arrives afterwards is stale and changes nothing.
func TestHold_EscapeCancelsTheHold(t *testing.T) {
	k := itemservice.New(pool)
	id, ana := v3Ticket(t, k)
	req := requestHold(t, k, ana, id)

	res, err := k.Transition(testCtx(t), itemservice.TransitionRequest{
		IdempotencyKey: newKey(), Principal: ana, ItemID: id, ToState: "canceled",
	})
	assertOutcome(t, "cancel", res, err, itemservice.OutcomeApplied, 5)
	op, payload := event(t, id, 5)
	want := map[string]any{"from": "in_progress", "to": "canceled", "canceled_hold_id": req.HoldID.String()}
	if op != "transition" || !reflect.DeepEqual(payload, want) {
		t.Errorf("event 5 = %s %v, want transition %v", op, payload, want)
	}
	if _, hold := holdOf(t, k, ana, id); hold == nil || hold.Status != item.HoldCanceled {
		t.Errorf("hold = %+v, want canceled", hold)
	}
	_, err = resolve(t, k, id, req.HoldID, itemservice.VerdictAllow, nil, "")
	assertStale(t, err)
	assertNothingWritten(t, id, 5)
}

// --- the bounded wait ---

func TestTransition_Wait(t *testing.T) {
	k := itemservice.New(pool)

	t.Run("capped at MaxWait", func(t *testing.T) {
		if itemservice.MaxWait != 5*time.Second {
			t.Errorf("MaxWait = %v, want 5s", itemservice.MaxWait)
		}
		id, ana := v3Ticket(t, k)
		_, err := approve(t, k, ana, id, newKey(), itemservice.MaxWait+time.Millisecond)
		assertInvalid(t, "wait above MaxWait", err)
		_, err = approve(t, k, ana, id, newKey(), -time.Second)
		assertInvalid(t, "negative wait", err)
		assertNothingWritten(t, id, 3)
	})

	t.Run("answers as soon as the hold resolves", func(t *testing.T) {
		id, ana := v3Ticket(t, k)
		go func() { // the hook, a little later
			time.Sleep(300 * time.Millisecond)
			holdID := waitForOpenHold(id)
			_, _ = k.Internal().ResolveHold(testCtx(t), itemservice.ResolveHoldRequest{
				IdempotencyKey: "hold:" + holdID.String(), Principal: erpCheckV3, ItemID: id, HoldID: holdID,
				Verdict: itemservice.VerdictAllow, Attribution: v3Cause(erpCheckV3, id, 4),
			})
		}()
		start := time.Now()
		res, err := approve(t, k, ana, id, newKey(), 3*time.Second)
		took := time.Since(start)
		assertOutcome(t, "approve with wait", res, err, itemservice.OutcomeApplied, 5)
		if res.CommitSeq != commitSeqOf(t, id, 5) {
			t.Errorf("commit_seq = %d, want event 5's: a read with min_seq must see the move", res.CommitSeq)
		}
		if took >= 2*time.Second {
			t.Errorf("answered after %v; it should answer soon after the hold resolved (~300 ms)", took)
		}
	})

	t.Run("pending when the wait runs out", func(t *testing.T) {
		id, ana := v3Ticket(t, k)
		start := time.Now()
		res, err := approve(t, k, ana, id, newKey(), 400*time.Millisecond)
		took := time.Since(start)
		assertOutcome(t, "approve with wait", res, err, itemservice.OutcomePending, 4)
		if took < 400*time.Millisecond || took > 1500*time.Millisecond {
			t.Errorf("answered after %v, want about 400 ms", took)
		}
	})

	t.Run("an unchecked transition is applied at once", func(t *testing.T) {
		id, ana := v3Ticket(t, k)
		start := time.Now()
		res, err := k.Transition(testCtx(t), itemservice.TransitionRequest{
			IdempotencyKey: newKey(), Principal: ana, ItemID: id, ToState: "waiting", Wait: 3 * time.Second,
		})
		assertOutcome(t, "move to waiting", res, err, itemservice.OutcomeApplied, 4)
		if res.HoldID != uuid.Nil {
			t.Errorf("hold %s on an unchecked transition", res.HoldID)
		}
		if took := time.Since(start); took > time.Second {
			t.Errorf("an unchecked transition took %v; it must not wait", took)
		}
	})

	// wait is not part of the request's identity: a retry may wait longer
	// or shorter under the same key, and gets the hold's current outcome.
	t.Run("a retry with another wait", func(t *testing.T) {
		id, ana := v3Ticket(t, k)
		key := newKey()
		first, err := approve(t, k, ana, id, key, 0)
		assertOutcome(t, "approve", first, err, itemservice.OutcomePending, 4)
		go func() {
			time.Sleep(300 * time.Millisecond)
			_, _ = resolve(t, k, id, first.HoldID, itemservice.VerdictAllow, nil, "")
		}()
		again, err := approve(t, k, ana, id, key, 3*time.Second)
		if errors.Is(err, itemservice.ErrIdempotencyKeyReuse) {
			t.Fatal("a different wait made the same key a reuse")
		}
		assertOutcome(t, "retry with a wait", again, err, itemservice.OutcomeApplied, 5)
		assertEventCount(t, id, 5)
	})
}

// waitForOpenHold polls for the Item's open hold, for a goroutine standing
// in for the hook. It may not call t.Fatal, so it returns uuid.Nil on failure.
func waitForOpenHold(id uuid.UUID) uuid.UUID {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var holdID uuid.UUID
		err := pool.QueryRow(context.Background(),
			`select id from item_holds where item_id = $1 and status = 'open'`, id).Scan(&holdID)
		if err == nil {
			return holdID
		}
		time.Sleep(20 * time.Millisecond)
	}
	return uuid.Nil
}

// assertEventCount checks that the Item has exactly wantSeq events.
func assertEventCount(t *testing.T, id uuid.UUID, wantSeq int) {
	t.Helper()
	if n := count(t, `select count(*) from item_events where item_id = $1`, id); n != wantSeq {
		t.Errorf("item_events rows = %d, want %d", n, wantSeq)
	}
}

// --- what hooks may read and write ---

// A hook reads only the fields it declares: the input the hook runner sends
// to the connector. Other fields, which may hold personal data, never leave.
func TestHookInput_OnlyTheDeclaredReads(t *testing.T) {
	k := itemservice.New(pool)
	id, ana := v3Ticket(t, k)
	requestHold(t, k, ana, id)

	got, err := k.Internal().HookInput(testCtx(t), itemservice.HookInputRequest{
		Principal: erpCheckV3, ItemID: id, Attribution: v3Cause(erpCheckV3, id, 4),
	})
	want := map[string]any{"f_customer_id": "cust-1", "f_amount": float64(100)}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("HookInput(erp_check) = %v, %v; want %v", got, err, want)
	}
	got, err = k.Internal().HookInput(testCtx(t), itemservice.HookInputRequest{
		Principal: getPlanV3, ItemID: id, Attribution: v3Cause(getPlanV3, id, 1),
	})
	if err != nil || !reflect.DeepEqual(got, map[string]any{"f_customer_id": "cust-1"}) {
		t.Errorf("HookInput(get_plan) = %v, %v; want only f_customer_id", got, err)
	}
	for name, p := range map[string]item.Principal{
		"the timer":          {Actor: "system:timer"},
		"an undeclared hook": {Actor: "hook:ticket:nope"},
		"a person":           ana,
	} {
		_, err := k.Internal().HookInput(testCtx(t), itemservice.HookInputRequest{
			Principal: p, ItemID: id, Attribution: v3Cause(p, id, 4),
		})
		assertInvalid(t, "HookInput as "+name, err)
	}
}

// A declared hook writes only the fields in its `writes`. On a Type that
// declares hooks, an undeclared hook writes nothing.
func TestHookWrites_OnlyTheDeclaredFields(t *testing.T) {
	k := itemservice.New(pool)
	id, _ := v3Ticket(t, k)
	write := func(p item.Principal, fields map[string]any) error {
		_, err := k.Internal().Update(testCtx(t), itemservice.UpdateRequest{
			IdempotencyKey: newKey(), Principal: p, ItemID: id, Fields: fields, Attribution: v3Cause(p, id, 1),
		})
		return err
	}
	assertInvalid(t, "get_plan writing f_on_hold", write(getPlanV3, map[string]any{"f_on_hold": true}))
	assertInvalid(t, "get_plan writing f_title too", write(getPlanV3, map[string]any{"f_plan": "pro", "f_title": "x"}))
	assertInvalid(t, "an undeclared hook", write(item.Principal{Actor: "hook:ticket:nope"},
		map[string]any{"f_plan": "pro"}))
	assertNothingWritten(t, id, 3)
	if err := write(getPlanV3, map[string]any{"f_plan": "enterprise"}); err != nil {
		t.Errorf("get_plan writing f_plan: %v", err)
	}
}
