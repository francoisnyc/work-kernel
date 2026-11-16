// Tests for proof-of-concept Step 2, part 1: approval as a business state.
//
// The vacation request (types/vacation_request.v1.yaml) goes draft ->
// submitted -> approved or rejected. The manager's decision is an ordinary
// transition; the kernel gains only generic primitives, tested here through
// the item service on every door:
//
//   - writable_by: kernel: the public door refuses the field on create,
//     update and a transition's patch (FieldNotWritableError, an
//     ErrForbidden); the internal door (a hook) may write it.
//   - frozen: [fields] on a state: no write names them while the Item is in
//     that state, from any door (field_frozen); the transition entering the
//     state may set them, the one leaving it may not.
//   - field:<id> in `allowed`: whoever the field holds when the move runs;
//     a transition's patch may not write a field its own `allowed` names.
//   - guards see caller, caller_on_behalf_of, created_by,
//     created_on_behalf_of, and today from the service's clock.
//
// The HR check is the hook hr_check; here the tests play it, through
// Internal().ResolveHold, so they can also set data HR would refuse (a
// manager who is the requester) and test the rule on its own.
package itemservice_test

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// --- the world ---

// testClock is a clock a test can set; the service reads `today` from it.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}

var hrCheck = item.Principal{Actor: "hook:vacation_request:hr_check"}

type vacWorld struct {
	k              *itemservice.Service
	clock          *testClock
	ana, bob, erin item.Principal
}

// newVacWorld: a service whose clock says 2099-06-01, 09:00 UTC, with the
// vacation request registered, and three fresh users.
func newVacWorld(t *testing.T) vacWorld {
	t.Helper()
	clock := &testClock{now: time.Date(2099, 6, 1, 9, 0, 0, 0, time.UTC)}
	k := itemservice.NewWithOptions(pool, itemservice.Options{CacheTypes: true, Now: clock.Now})
	registerFromFile(t, k, "vacation_request.v1.yaml")
	return vacWorld{k: k, clock: clock, ana: person("ana"), bob: person("bob"), erin: person("erin")}
}

// vacTerms: five days of annual leave from 2099-07-01.
func vacTerms() map[string]any {
	return map[string]any{
		"f_start": "2099-07-01", "f_end": "2099-07-05", "f_days": 5, "f_leave_type": "annual",
		"f_comment": "beach",
	}
}

func (w vacWorld) create(t *testing.T, p item.Principal, fields map[string]any) uuid.UUID {
	t.Helper()
	res, err := w.k.Create(testCtx(t), itemservice.CreateRequest{
		IdempotencyKey: newKey(), Principal: p, TypeID: "vacation_request", TypeVersion: 1, Fields: fields,
	})
	if err != nil {
		t.Fatalf("create vacation request: %v", err)
	}
	return res.ItemID
}

func (w vacWorld) move(t *testing.T, p item.Principal, id uuid.UUID, to string, patch map[string]any,
	key string) (itemservice.TransitionResult, error) {
	t.Helper()
	return w.k.Transition(testCtx(t), itemservice.TransitionRequest{
		IdempotencyKey: key, Principal: p, ItemID: id, ToState: to, Fields: patch,
	})
}

// submit asks for draft -> submitted as p and expects a pending hold.
func (w vacWorld) submit(t *testing.T, p item.Principal, id uuid.UUID) itemservice.TransitionResult {
	t.Helper()
	res, err := w.move(t, p, id, "submitted", nil, newKey())
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if res.Outcome != itemservice.OutcomePending || res.HoldID == uuid.Nil {
		t.Fatalf("submit = %+v, want pending with a hold (the HR check)", res)
	}
	return res
}

// hr settles the hold as hr_check, caused by the request event.
func (w vacWorld) hr(t *testing.T, id uuid.UUID, req itemservice.TransitionResult, v itemservice.Verdict,
	fields map[string]any, reason string) (itemservice.WriteResult, error) {
	t.Helper()
	return w.k.Internal().ResolveHold(testCtx(t), itemservice.ResolveHoldRequest{
		IdempotencyKey: "hold:" + req.HoldID.String(), Principal: hrCheck, ItemID: id, HoldID: req.HoldID,
		Verdict: v, Fields: fields, Reason: reason, Attribution: hrCause(id, req.ItemSeq),
	})
}

func hrCause(id uuid.UUID, seq int64) item.Attribution {
	return item.Attribution{
		OriginatingHook: "hook:vacation_request@v1:hr_check",
		CausationID:     fmt.Sprintf("%s:%d", id, seq),
		Hop:             1,
	}
}

// submitted creates a request as requester and walks it into submitted,
// with HR naming approver.
func (w vacWorld) submitted(t *testing.T, requester, approver item.Principal) uuid.UUID {
	t.Helper()
	id := w.create(t, requester, vacTerms())
	req := w.submit(t, requester, id)
	if _, err := w.hr(t, id, req, itemservice.VerdictCorrect, map[string]any{"f_approver": approver.Actor}, ""); err != nil {
		t.Fatalf("HR correct: %v", err)
	}
	if st := w.get(t, requester, id).State; st != "submitted" {
		t.Fatalf("state after HR = %s, want submitted", st)
	}
	return id
}

func (w vacWorld) get(t *testing.T, p item.Principal, id uuid.UUID) item.Item {
	t.Helper()
	it, err := w.k.Get(testCtx(t), p, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	return it
}

func (w vacWorld) update(t *testing.T, p item.Principal, id uuid.UUID, fields map[string]any) error {
	t.Helper()
	_, err := w.k.Update(testCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: p, ItemID: id, Fields: fields,
	})
	return err
}

// --- assertions ---

func assertNotWritable(t *testing.T, what string, err error, fields ...string) {
	t.Helper()
	var nw *itemservice.FieldNotWritableError
	if !errors.As(err, &nw) {
		t.Fatalf("%s: error = %v, want a *FieldNotWritableError", what, err)
	}
	if !errors.Is(err, itemservice.ErrForbidden) || errors.Is(err, itemservice.ErrRejected) {
		t.Errorf("%s: error %v must match ErrForbidden and not ErrRejected", what, err)
	}
	if !slices.Equal(nw.Fields, fields) {
		t.Errorf("%s: fields = %v, want %v", what, nw.Fields, fields)
	}
}

func assertFrozen(t *testing.T, what string, err error, state string, fields ...string) {
	t.Helper()
	assertRejectedCode(t, what, err, itemservice.CodeFieldFrozen)
	var rej *itemservice.RejectedError
	if !errors.As(err, &rej) {
		return
	}
	if got := rej.Metadata["fields"]; got != strings.Join(fields, ",") {
		t.Errorf("%s: metadata fields = %q, want %q", what, got, strings.Join(fields, ","))
	}
	if got := rej.Metadata["state"]; got != state {
		t.Errorf("%s: metadata state = %q, want %q", what, got, state)
	}
}

// assertNotAllowed: plain ErrForbidden (NOT_ALLOWED), not a field refusal.
func assertNotAllowed(t *testing.T, what string, err error) {
	t.Helper()
	var nw *itemservice.FieldNotWritableError
	if errors.As(err, &nw) {
		t.Fatalf("%s: error = %v, want NOT_ALLOWED, not a field refusal", what, err)
	}
	if !errors.Is(err, itemservice.ErrForbidden) {
		t.Fatalf("%s: error = %v, want ErrForbidden", what, err)
	}
}

func assertState(t *testing.T, w vacWorld, p item.Principal, id uuid.UUID, want string) {
	t.Helper()
	if got := w.get(t, p, id).State; got != want {
		t.Errorf("state = %s, want %s", got, want)
	}
}

// --- the flow ---

// Item 8: ana submits; the HR check sets f_approver on the way into
// submitted, in the same write; bob, whom it names, approves.
func TestVacation_SubmitThenApprove(t *testing.T) {
	w := newVacWorld(t)
	id := w.create(t, w.ana, vacTerms())
	req := w.submit(t, w.ana, id)
	if req.ItemSeq != 2 {
		t.Errorf("request at item_seq %d, want 2", req.ItemSeq)
	}
	if it := w.get(t, w.ana, id); it.State != "draft" || it.Fields["f_approver"] != nil {
		t.Errorf("while HR checks: state %s, f_approver %v; want draft and none", it.State, it.Fields["f_approver"])
	}

	res, err := w.hr(t, id, req, itemservice.VerdictCorrect, map[string]any{"f_approver": w.bob.Actor}, "")
	if err != nil {
		t.Fatalf("HR correct: %v", err)
	}
	it := w.get(t, w.ana, id)
	if it.State != "submitted" || it.Fields["f_approver"] != w.bob.Actor || it.ItemSeq != res.ItemSeq {
		t.Errorf("after HR: state %s, f_approver %v, item_seq %d; want submitted, %s, %d",
			it.State, it.Fields["f_approver"], it.ItemSeq, w.bob.Actor, res.ItemSeq)
	}
	op, payload := event(t, id, res.ItemSeq)
	if op != "transition" || payload["to"] != "submitted" ||
		!slices.Equal(anyStrings(payload["field_ids"]), []string{"f_approver"}) {
		t.Errorf("HR's event = %s %v, want a transition into submitted setting f_approver", op, payload)
	}
	if obo := onBehalfOf(t, id, res.ItemSeq); obo != w.ana.Actor {
		t.Errorf("HR's event on_behalf_of = %q, want the requester %s", obo, w.ana.Actor)
	}

	dec, err := w.move(t, w.bob, id, "approved", nil, newKey())
	assertOutcome(t, "bob approves", dec, err, itemservice.OutcomeApplied, res.ItemSeq+1)
	assertState(t, w, w.bob, id, "approved")
	if actor, _ := attribution(t, id, dec.ItemSeq); actor != w.bob.Actor {
		t.Errorf("approval recorded as %s, want %s", actor, w.bob.Actor)
	}
}

func anyStrings(v any) []string {
	list, _ := v.([]any)
	var out []string
	for _, x := range list {
		s, _ := x.(string)
		out = append(out, s)
	}
	return out
}

func TestVacation_ApproverRejects(t *testing.T) {
	w := newVacWorld(t)
	id := w.submitted(t, w.ana, w.bob)
	res, err := w.move(t, w.bob, id, "rejected", nil, newKey())
	if err != nil || res.Outcome != itemservice.OutcomeApplied {
		t.Fatalf("bob rejects = %+v, %v; want applied", res, err)
	}
	assertState(t, w, w.ana, id, "rejected")
}

// --- 2: f_approver is written by the kernel only ---

// The public door refuses f_approver on create, update and a transition's
// patch, held requests included, naming the field. Nothing is written, and
// no hold opens. An agent's write is the public door too.
func TestVacation_RequesterCannotSetTheApprover(t *testing.T) {
	w := newVacWorld(t)
	withApprover := vacTerms()
	withApprover["f_approver"] = w.ana.Actor
	_, err := w.k.Create(testCtx(t), itemservice.CreateRequest{
		IdempotencyKey: newKey(), Principal: w.ana, TypeID: "vacation_request", TypeVersion: 1, Fields: withApprover,
	})
	assertNotWritable(t, "create with f_approver", err, "f_approver")

	id := w.create(t, w.ana, vacTerms())
	assertNotWritable(t, "update f_approver", w.update(t, w.ana, id, map[string]any{"f_approver": w.bob.Actor}), "f_approver")
	assertNotWritable(t, "clear f_approver", w.update(t, w.ana, id, map[string]any{"f_approver": nil}), "f_approver")
	assertNotWritable(t, "update f_approver with another field",
		w.update(t, w.ana, id, map[string]any{"f_days": 4, "f_approver": w.ana.Actor}), "f_approver")

	_, err = w.move(t, w.ana, id, "submitted", map[string]any{"f_approver": w.ana.Actor}, newKey())
	assertNotWritable(t, "submit (held) with f_approver in the patch", err, "f_approver")
	if n := count(t, `select count(*) from item_holds where item_id = $1`, id); n != 0 {
		t.Errorf("holds = %d, want none: the refusal comes before the hold", n)
	}

	agent := withSession(t, w.k, item.Principal{Actor: "agent:" + uuid.NewString()[:8], OnBehalfOf: w.ana.Actor})
	assertNotWritable(t, "an agent for ana updates f_approver",
		w.update(t, agent, id, map[string]any{"f_approver": w.bob.Actor}), "f_approver")
	assertEventCount(t, id, 1)

	// created_by and created_on_behalf_of are not fields: a patch naming one
	// is a field the Type does not declare.
	assertInvalidField(t, "update created_by", w.update(t, w.ana, id, map[string]any{"created_by": w.bob.Actor}), "created_by")
}

// The internal door may write a kernel-written field: a hook declaring it.
func TestVacation_HookMayWriteTheApproverInDraft(t *testing.T) {
	w := newVacWorld(t)
	id := w.create(t, w.ana, vacTerms())
	_, err := w.k.Internal().Update(testCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: hrCheck, ItemID: id,
		Fields: map[string]any{"f_approver": w.bob.Actor}, Attribution: hrCause(id, 1),
	})
	if err != nil {
		t.Fatalf("hr_check writes f_approver in draft: %v", err)
	}
}

// --- 3: frozen terms ---

// In submitted, the terms and the approver are frozen: any write naming one
// is field_frozen, naming the frozen fields (sorted) and the state. Fields
// not frozen stay editable.
func TestVacation_TermsFrozenWhileSubmitted(t *testing.T) {
	w := newVacWorld(t)
	id := w.submitted(t, w.ana, w.bob)
	seq := w.get(t, w.ana, id).ItemSeq

	assertFrozen(t, "edit f_days", w.update(t, w.ana, id, map[string]any{"f_days": 20}), "submitted", "f_days")
	assertFrozen(t, "edit several", w.update(t, w.ana, id,
		map[string]any{"f_start": "2099-07-02", "f_days": 4, "f_comment": "x"}), "submitted", "f_days", "f_start")
	assertFrozen(t, "clear f_end", w.update(t, w.ana, id, map[string]any{"f_end": nil}), "submitted", "f_end")
	assertFrozen(t, "the approver edits too", w.update(t, w.bob, id, map[string]any{"f_leave_type": "unpaid"}),
		"submitted", "f_leave_type")
	// Naming is enough: the same value is still a write of a frozen field.
	assertFrozen(t, "same value", w.update(t, w.ana, id, map[string]any{"f_days": 5}), "submitted", "f_days")

	// Leaving the state with a patch on a frozen field is refused too.
	_, err := w.move(t, w.ana, id, "draft", map[string]any{"f_days": 3}, newKey())
	assertFrozen(t, "recall with an f_days patch", err, "submitted", "f_days")
	assertEventCount(t, id, int(seq))
	assertState(t, w, w.ana, id, "submitted")

	if err := w.update(t, w.ana, id, map[string]any{"f_comment": "beach, then mountains"}); err != nil {
		t.Errorf("f_comment is not frozen: %v", err)
	}
}

// Frozen holds on every door: a hook writing a frozen field is refused too.
func TestVacation_FrozenOnTheInternalDoor(t *testing.T) {
	w := newVacWorld(t)
	id := w.submitted(t, w.ana, w.bob)
	seq := w.get(t, w.ana, id).ItemSeq
	_, err := w.k.Internal().Update(testCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: hrCheck, ItemID: id,
		Fields: map[string]any{"f_approver": w.erin.Actor}, Attribution: hrCause(id, seq),
	})
	assertFrozen(t, "hr_check rewrites f_approver in submitted", err, "submitted", "f_approver")
}

// Once decided, the terms stay frozen: an approved request cannot become
// twenty days, nor a rejected one be edited.
func TestVacation_TermsFrozenOnceDecided(t *testing.T) {
	w := newVacWorld(t)
	for _, to := range []string{"approved", "rejected"} {
		id := w.submitted(t, w.ana, w.bob)
		if _, err := w.move(t, w.bob, id, to, nil, newKey()); err != nil {
			t.Fatalf("bob -> %s: %v", to, err)
		}
		assertFrozen(t, "edit f_days when "+to, w.update(t, w.ana, id, map[string]any{"f_days": 20}), to, "f_days")
		assertFrozen(t, "edit f_end when "+to, w.update(t, w.bob, id, map[string]any{"f_end": "2099-07-25"}), to, "f_end")
		if err := w.update(t, w.ana, id, map[string]any{"f_comment": "noted"}); err != nil {
			t.Errorf("%s: f_comment is not frozen: %v", to, err)
		}
	}
}

// Order: not writable, then frozen. In submitted, f_approver is both.
func TestVacation_NotWritableBeforeFrozen(t *testing.T) {
	w := newVacWorld(t)
	id := w.submitted(t, w.ana, w.bob)
	assertNotWritable(t, "update f_approver and f_days in submitted",
		w.update(t, w.ana, id, map[string]any{"f_approver": w.ana.Actor, "f_days": 2}), "f_approver")
	_, err := w.move(t, w.ana, id, "draft", map[string]any{"f_approver": nil}, newKey())
	assertNotWritable(t, "recall clearing f_approver", err, "f_approver")
}

// 10b: to change a pending request, recall it (requester only), edit,
// submit again: the HR check runs again.
func TestVacation_RecallEditResubmit(t *testing.T) {
	w := newVacWorld(t)
	id := w.submitted(t, w.ana, w.bob)

	_, err := w.move(t, w.bob, id, "draft", nil, newKey())
	assertRejectedCode(t, "the approver recalls", err, itemservice.CodeGuardFailed)

	if res, err := w.move(t, w.ana, id, "draft", nil, newKey()); err != nil || res.Outcome != itemservice.OutcomeApplied {
		t.Fatalf("ana recalls = %+v, %v; want applied", res, err)
	}
	if err := w.update(t, w.ana, id, map[string]any{"f_days": 3, "f_end": "2099-07-03"}); err != nil {
		t.Fatalf("edit in draft: %v", err)
	}
	req := w.submit(t, w.ana, id)
	if _, err := w.hr(t, id, req, itemservice.VerdictCorrect, map[string]any{"f_approver": w.bob.Actor}, ""); err != nil {
		t.Fatalf("HR correct again: %v", err)
	}
	it := w.get(t, w.ana, id)
	if it.State != "submitted" || it.Fields["f_days"] != float64(3) || it.Fields["f_approver"] != w.bob.Actor {
		t.Errorf("resubmitted: state %s, f_days %v, f_approver %v", it.State, it.Fields["f_days"], it.Fields["f_approver"])
	}
}

// --- 4-7: who decides ---

// 5c: the requester asking to approve her own request fails the guard
// (guards are asked before `allowed`): GUARD_FAILED, not NOT_ALLOWED.
func TestVacation_RequesterApprovingIsGuardFailed(t *testing.T) {
	w := newVacWorld(t)
	id := w.submitted(t, w.ana, w.bob)
	for _, to := range []string{"approved", "rejected"} {
		_, err := w.move(t, w.ana, id, to, nil, newKey())
		assertRejectedCode(t, "ana -> "+to, err, itemservice.CodeGuardFailed)
	}
	assertState(t, w, w.ana, id, "submitted")
}

// 6: no self-approval, even when the approver field names the requester
// (HR refuses that case; the rule stands on its own).
func TestVacation_NoSelfApprovalEvenIfNamed(t *testing.T) {
	w := newVacWorld(t)
	id := w.submitted(t, w.ana, w.ana)
	for _, to := range []string{"approved", "rejected"} {
		_, err := w.move(t, w.ana, id, to, nil, newKey())
		assertRejectedCode(t, "ana named as her own approver -> "+to, err, itemservice.CodeGuardFailed)
	}
	assertState(t, w, w.ana, id, "submitted")
}

// An agent that created the request for ana does not make ana eligible.
func TestVacation_NoSelfApprovalForAnAgentsUser(t *testing.T) {
	w := newVacWorld(t)
	agent := withSession(t, w.k, item.Principal{Actor: "agent:" + uuid.NewString()[:8], OnBehalfOf: w.ana.Actor})
	id := w.create(t, agent, vacTerms())
	req := w.submit(t, agent, id)
	if _, err := w.hr(t, id, req, itemservice.VerdictCorrect, map[string]any{"f_approver": w.ana.Actor}, ""); err != nil {
		t.Fatalf("HR correct: %v", err)
	}
	_, err := w.move(t, w.ana, id, "approved", nil, newKey())
	assertRejectedCode(t, "ana approves what her agent asked for", err, itemservice.CodeGuardFailed)
}

// 4: someone who is neither the requester nor the approver: NOT_ALLOWED.
func TestVacation_ThirdUserIsNotAllowed(t *testing.T) {
	w := newVacWorld(t)
	id := w.submitted(t, w.ana, w.bob)
	for _, to := range []string{"approved", "rejected"} {
		_, err := w.move(t, w.erin, id, to, nil, newKey())
		assertNotAllowed(t, "erin -> "+to, err)
	}
	assertState(t, w, w.ana, id, "submitted")
}

// 7: an agent and the user it acts for must both pass `allowed`. A field
// holding user:bob never matches agent:x, so an agent acting for the
// approver, with a namespace-wide session and every transition power, is
// NOT_ALLOWED. Agents cannot approve in Step 2.
func TestVacation_AgentActingForTheApproverIsNotAllowed(t *testing.T) {
	w := newVacWorld(t)
	id := w.submitted(t, w.ana, w.bob)
	bot := withSession(t, w.k, item.Principal{Actor: "agent:" + uuid.NewString()[:8], OnBehalfOf: w.bob.Actor})
	for _, to := range []string{"approved", "rejected"} {
		_, err := w.move(t, bot, id, to, nil, newKey())
		assertNotAllowed(t, "an agent for bob -> "+to, err)
	}
	assertState(t, w, w.bob, id, "submitted")
}

// --- 10b: withdrawing ends an open HR check ---

// draft -> withdrawn is an escape: while the HR check holds the Item, the
// requester may still withdraw. The move cancels the hold in the same
// event; the hold shows canceled; asking about the submission again (same
// key) answers denied, canceled; the check's late answer is stale.
// Someone else may not withdraw (the guard), hold or not.
func TestVacation_WithdrawEndsTheHRCheck(t *testing.T) {
	w := newVacWorld(t)
	id := w.create(t, w.ana, vacTerms())
	submitKey := newKey()
	req, err := w.move(t, w.ana, id, "submitted", nil, submitKey)
	if err != nil || req.Outcome != itemservice.OutcomePending {
		t.Fatalf("submit = %+v, %v; want pending", req, err)
	}

	_, err = w.move(t, w.bob, id, "withdrawn", nil, newKey())
	assertRejectedCode(t, "bob withdraws ana's request", err, itemservice.CodeGuardFailed)

	res, err := w.move(t, w.ana, id, "withdrawn", nil, newKey())
	assertOutcome(t, "ana withdraws while HR checks", res, err, itemservice.OutcomeApplied, req.ItemSeq+1)
	state, hold := holdOf(t, w.k, w.ana, id)
	if state != "withdrawn" || hold == nil || hold.ID != req.HoldID || hold.Status != item.HoldCanceled {
		t.Errorf("after withdrawing: state %s, hold %+v; want withdrawn and the hold canceled", state, hold)
	}
	if _, payload := event(t, id, res.ItemSeq); payload["canceled_hold_id"] != req.HoldID.String() {
		t.Errorf("withdraw payload = %v, want canceled_hold_id %s", payload, req.HoldID)
	}

	again, err := w.move(t, w.ana, id, "submitted", nil, submitKey)
	if err != nil || again.Outcome != itemservice.OutcomeDenied || again.Reason != "canceled" {
		t.Errorf("the submission asked again = %+v, %v; want denied, canceled", again, err)
	}
	_, err = w.hr(t, id, req, itemservice.VerdictCorrect, map[string]any{"f_approver": w.bob.Actor}, "")
	assertStale(t, err)
	assertEventCount(t, id, int(res.ItemSeq))
}

// --- G2: today comes from the service's clock ---

// The service's clock says what today is (UTC): a request starting before
// it is refused, one starting today is not. With the real clock (2026), a
// 2099 start would pass: the refusal shows the clock is the service's.
func TestVacation_SubmitReadsTodayFromTheClock(t *testing.T) {
	w := newVacWorld(t)
	w.clock.Set(time.Date(2099, 7, 2, 0, 30, 0, 0, time.UTC))
	id := w.create(t, w.ana, vacTerms()) // starts 2099-07-01
	_, err := w.move(t, w.ana, id, "submitted", nil, newKey())
	assertRejectedCode(t, "submit starting yesterday", err, itemservice.CodeGuardFailed)
	assertEventCount(t, id, 1)

	if err := w.update(t, w.ana, id, map[string]any{"f_start": "2099-07-02"}); err != nil {
		t.Fatalf("edit start: %v", err)
	}
	w.submit(t, w.ana, id)
}

// 9: the re-check at resolution reads today then. A request that was valid
// when asked, resolved after its start date, is denied guard_failed; the
// HR correction is not written.
func TestVacation_RecheckAtResolutionReadsTodayThen(t *testing.T) {
	w := newVacWorld(t)
	id := w.create(t, w.ana, vacTerms())
	submitKey := newKey()
	req, err := w.move(t, w.ana, id, "submitted", nil, submitKey)
	if err != nil || req.Outcome != itemservice.OutcomePending {
		t.Fatalf("submit = %+v, %v", req, err)
	}
	w.clock.Set(time.Date(2099, 7, 2, 9, 0, 0, 0, time.UTC))
	if _, err := w.hr(t, id, req, itemservice.VerdictCorrect, map[string]any{"f_approver": w.bob.Actor}, ""); err != nil {
		t.Fatalf("HR correct: %v", err)
	}
	it := w.get(t, w.ana, id)
	if it.State != "draft" || it.Fields["f_approver"] != nil {
		t.Errorf("after a late resolution: state %s, f_approver %v; want draft, none", it.State, it.Fields["f_approver"])
	}
	again, err := w.move(t, w.ana, id, "submitted", nil, submitKey)
	if err != nil || again.Outcome != itemservice.OutcomeDenied || again.Reason != itemservice.CodeGuardFailed {
		t.Errorf("the submission asked again = %+v, %v; want denied, guard_failed", again, err)
	}
}

// --- G3: the HR check reads who the request is for ---

// A hook may read created_by and created_on_behalf_of. For a request a
// user made herself, created_on_behalf_of is null; for one an agent made
// for her, it names her.
func TestVacation_HookInputReadsTheCreator(t *testing.T) {
	w := newVacWorld(t)
	read := func(id uuid.UUID, seq int64) map[string]any {
		t.Helper()
		got, err := w.k.Internal().HookInput(testCtx(t), itemservice.HookInputRequest{
			Principal: hrCheck, ItemID: id, Attribution: hrCause(id, seq),
		})
		if err != nil {
			t.Fatalf("HookInput: %v", err)
		}
		return got
	}

	id := w.create(t, w.ana, vacTerms())
	req := w.submit(t, w.ana, id)
	got := read(id, req.ItemSeq)
	if got["created_by"] != w.ana.Actor || got["created_on_behalf_of"] != nil || len(got) > 2 {
		t.Errorf("HookInput, ana's own request = %v; want created_by %s and nothing else", got, w.ana.Actor)
	}

	agent := withSession(t, w.k, item.Principal{Actor: "agent:" + uuid.NewString()[:8], OnBehalfOf: w.ana.Actor})
	id = w.create(t, agent, vacTerms())
	req = w.submit(t, agent, id)
	got = read(id, req.ItemSeq)
	if got["created_by"] != agent.Actor || got["created_on_behalf_of"] != w.ana.Actor || len(got) != 2 {
		t.Errorf("HookInput, an agent's request for ana = %v; want created_by %s, created_on_behalf_of %s",
			got, agent.Actor, w.ana.Actor)
	}
}

// --- 4a: field:<id> on an editable field ---

// dutyType: f_owner is an ordinary, editable principal field. done and
// checked are allowed to whoever f_owner names; checked has a check.
const dutyType = `
id: duty
version: 1
initial_state: open
fields:
  - {id: f_owner, type: principal}
  - {id: f_note,  type: string}
states:
  - {id: open,    category: todo}
  - {id: done,    category: done}
  - {id: checked, category: done}
transitions:
  - {from: open, to: done,    allowed: ['field:f_owner']}
  - {from: open, to: checked, allowed: ['field:f_owner']}
hooks:
  - {id: stamp, on: enter, state: open, kind: enrich, action: crm.get_plan, reads: [f_note], writes: [f_owner]}
  - {id: gate,  on: transition, from: open, to: checked, kind: check, action: crm.get_plan, reads: [f_note], writes: [f_note]}
`

func newDuty(t *testing.T, k *itemservice.Service, p item.Principal, fields map[string]any) uuid.UUID {
	t.Helper()
	ty, err := typedef.Parse([]byte(dutyType))
	if err != nil {
		t.Fatalf("parse duty: %v", err)
	}
	if err := k.RegisterType(testCtx(t), ty); err != nil {
		t.Fatalf("register duty: %v", err)
	}
	res, err := k.Create(testCtx(t), itemservice.CreateRequest{
		IdempotencyKey: newKey(), Principal: p, TypeID: "duty", TypeVersion: 1, Fields: fields,
	})
	if err != nil {
		t.Fatalf("create duty: %v", err)
	}
	return res.ItemID
}

// `allowed` reads the stored value: the principal f_owner holds when the
// move runs. Someone else is NOT_ALLOWED; an empty field matches no one.
func TestAllowedField_MatchesTheStoredPrincipal(t *testing.T) {
	k := itemservice.New(pool)
	ana, bob, carol := person("ana"), person("bob"), person("carol")

	id := newDuty(t, k, ana, map[string]any{"f_owner": bob.Actor})
	assertNotAllowed(t, "carol, not the owner", move(t, k, carol, id, "done", newKey()))
	if err := move(t, k, bob, id, "done", newKey()); err != nil {
		t.Errorf("bob, the owner: %v", err)
	}

	empty := newDuty(t, k, ana, map[string]any{"f_note": "nobody owns it"})
	for _, p := range []item.Principal{ana, bob} {
		assertNotAllowed(t, p.Actor+" with f_owner empty", move(t, k, p, empty, "done", newKey()))
	}
}

// A transition's patch may not write a field its own `allowed` names:
// otherwise the caller would name themselves and pass. Refused as
// FIELD_NOT_WRITABLE (before `allowed`), on the direct and the held
// transition alike, and for the owner too: naming the field is enough.
// Nothing is written; no hold opens.
func TestAllowedField_PatchMayNotWriteIt(t *testing.T) {
	k := itemservice.New(pool)
	ana, bob, carol := person("ana"), person("bob"), person("carol")
	id := newDuty(t, k, ana, map[string]any{"f_owner": bob.Actor})

	for _, to := range []string{"done", "checked"} {
		for _, p := range []item.Principal{carol, bob} {
			_, err := k.Transition(testCtx(t), itemservice.TransitionRequest{
				IdempotencyKey: newKey(), Principal: p, ItemID: id, ToState: to,
				Fields: map[string]any{"f_owner": p.Actor},
			})
			assertNotWritable(t, p.Actor+" -> "+to+" naming f_owner", err, "f_owner")
		}
	}
	assertEventCount(t, id, 1)
	if n := count(t, `select count(*) from item_holds where item_id = $1`, id); n != 0 {
		t.Errorf("holds = %d, want none", n)
	}

	// Other fields may still travel with the move.
	res, err := k.Transition(testCtx(t), itemservice.TransitionRequest{
		IdempotencyKey: newKey(), Principal: bob, ItemID: id, ToState: "done",
		Fields: map[string]any{"f_note": "done by bob"},
	})
	if err != nil || res.Outcome != itemservice.OutcomeApplied {
		t.Errorf("bob -> done with f_note = %+v, %v; want applied", res, err)
	}
}

// The same holds on the internal door: a hook's transition may not write
// the field its `allowed` names.
func TestAllowedField_PatchRefusedOnTheInternalDoor(t *testing.T) {
	k := itemservice.New(pool)
	ana, bob := person("ana"), person("bob")
	id := newDuty(t, k, ana, map[string]any{"f_owner": bob.Actor})
	stamp := item.Principal{Actor: "hook:duty:stamp"}
	_, err := k.Internal().Transition(testCtx(t), itemservice.TransitionRequest{
		IdempotencyKey: newKey(), Principal: stamp, ItemID: id, ToState: "done",
		Fields: map[string]any{"f_owner": bob.Actor},
		Attribution: item.Attribution{
			OriginatingHook: "hook:duty@v1:stamp", CausationID: fmt.Sprintf("%s:1", id), Hop: 1,
		},
	})
	assertNotWritable(t, "hook stamp -> done naming f_owner", err, "f_owner")
}

// 4b, documented: field:<id> on an editable field means whoever the field
// names when the transition runs. Anyone who may edit the field in an
// earlier write can change that.
func TestAllowedField_AnEditableFieldCanBeRepointed(t *testing.T) {
	k := itemservice.New(pool)
	ana, bob, carol := person("ana"), person("bob"), person("carol")
	id := newDuty(t, k, ana, map[string]any{"f_owner": bob.Actor})
	if _, err := k.Update(testCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: carol, ItemID: id, Fields: map[string]any{"f_owner": carol.Actor},
	}); err != nil {
		t.Fatalf("carol repoints f_owner: %v", err)
	}
	if err := move(t, k, carol, id, "done", newKey()); err != nil {
		t.Errorf("carol, now the owner: %v", err)
	}
}

// At resolution the requester is checked against the field as it is then:
// an owner who asked, and was replaced while the check ran, is denied
// forbidden.
func TestAllowedField_RecheckReadsTheFieldAtResolution(t *testing.T) {
	k := itemservice.New(pool)
	ana, bob, carol := person("ana"), person("bob"), person("carol")
	id := newDuty(t, k, ana, map[string]any{"f_owner": bob.Actor, "f_note": "n"})
	req, err := k.Transition(testCtx(t), itemservice.TransitionRequest{
		IdempotencyKey: newKey(), Principal: bob, ItemID: id, ToState: "checked",
	})
	if err != nil || req.Outcome != itemservice.OutcomePending {
		t.Fatalf("bob asks for checked = %+v, %v; want pending", req, err)
	}
	if _, err := k.Update(testCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: ana, ItemID: id, Fields: map[string]any{"f_owner": carol.Actor},
	}); err != nil {
		t.Fatalf("ana repoints f_owner during the check: %v", err)
	}
	gate := item.Principal{Actor: "hook:duty:gate"}
	if _, err := k.Internal().ResolveHold(testCtx(t), itemservice.ResolveHoldRequest{
		IdempotencyKey: "hold:" + req.HoldID.String(), Principal: gate, ItemID: id, HoldID: req.HoldID,
		Verdict: itemservice.VerdictAllow,
		Attribution: item.Attribution{
			OriginatingHook: "hook:duty@v1:gate", CausationID: fmt.Sprintf("%s:%d", id, req.ItemSeq), Hop: 1,
		},
	}); err != nil {
		t.Fatalf("gate allows: %v", err)
	}
	state, hold := holdOf(t, k, ana, id)
	if state != "open" || hold == nil || hold.Status != item.HoldDenied || hold.Reason != "forbidden" {
		t.Errorf("after resolution: state %s, hold %+v; want open, denied forbidden", state, hold)
	}
}

// --- 3c: the order of refusals: frozen before held ---

// heldFrozenType: in pending, f_x is frozen by the state and read by the
// open check. A write of f_x is FIELD_FROZEN, not HELD.
const heldFrozenType = `
id: heldfrozen
version: 1
initial_state: pending
fields:
  - {id: f_x, type: string}
  - {id: f_y, type: string}
states:
  - {id: pending, category: waiting, frozen: [f_x]}
  - {id: done,    category: done}
transitions:
  - {from: pending, to: done}
hooks:
  - {id: gate, on: transition, from: pending, to: done, kind: check, action: crm.get_plan, reads: [f_x], writes: [f_y]}
`

func TestFrozen_BeforeHeld(t *testing.T) {
	k := itemservice.New(pool)
	ty, err := typedef.Parse([]byte(heldFrozenType))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := k.RegisterType(testCtx(t), ty); err != nil {
		t.Fatalf("register: %v", err)
	}
	ana := person("ana")
	res, err := k.Create(testCtx(t), itemservice.CreateRequest{
		IdempotencyKey: newKey(), Principal: ana, TypeID: "heldfrozen", TypeVersion: 1,
		Fields: map[string]any{"f_x": "a"},
	})
	if err != nil {
		t.Fatalf("create (the initial state may set its frozen fields): %v", err)
	}
	req, err := k.Transition(testCtx(t), itemservice.TransitionRequest{
		IdempotencyKey: newKey(), Principal: ana, ItemID: res.ItemID, ToState: "done",
	})
	if err != nil || req.Outcome != itemservice.OutcomePending {
		t.Fatalf("request = %+v, %v; want pending", req, err)
	}
	_, err = k.Update(testCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: ana, ItemID: res.ItemID, Fields: map[string]any{"f_x": "b"},
	})
	assertFrozen(t, "f_x, frozen and held", err, "pending", "f_x")
}

// --- 5: who the guards see, on every entry point ---

// probeType: each move out of start has a guard that holds for exactly one
// caller, so a move that succeeds proves what the guard saw.
const probeType = `
id: probe
version: 1
initial_state: start
fields:
  - {id: f_note, type: string}
states:
  - {id: start,      category: todo, timer: {after: 1h, to: by_timer}}
  - {id: by_user,    category: done}
  - {id: by_agent,   category: done}
  - {id: by_timer,   category: done}
  - {id: by_hook,    category: done}
  - {id: by_request, category: done}
transitions:
  - from: start
    to: by_user
    guard: 'caller == "user:probe-ana" && caller_on_behalf_of == null && created_by == "user:probe-ana" && created_on_behalf_of == null'
  - from: start
    to: by_agent
    guard: 'caller == "agent:probe-bot" && caller_on_behalf_of == "user:probe-ana" && created_by == "agent:probe-bot" && created_on_behalf_of == "user:probe-ana"'
  - from: start
    to: by_timer
    guard: 'caller == "system:timer" && caller_on_behalf_of == null && created_by == "user:probe-ana"'
  - from: start
    to: by_hook
    guard: 'caller == "hook:probe:stamp" && caller_on_behalf_of == null && created_by == "user:probe-ana"'
  - from: start
    to: by_request
    guard: 'caller == "agent:probe-bot" && caller_on_behalf_of == "user:probe-ana"'
hooks:
  - {id: stamp, on: enter, state: start, kind: enrich, action: crm.get_plan, reads: [f_note], writes: [f_note]}
  - {id: gate, on: transition, from: start, to: by_request, kind: check, action: crm.get_plan, reads: [f_note], writes: [f_note]}
`

var (
	probeAna = item.Principal{Actor: "user:probe-ana"}
	probeBot = item.Principal{Actor: "agent:probe-bot", OnBehalfOf: "user:probe-ana"}
)

func newProbe(t *testing.T, k *itemservice.Service, p item.Principal) uuid.UUID {
	t.Helper()
	ty, err := typedef.Parse([]byte(probeType))
	if err != nil {
		t.Fatalf("parse probe: %v", err)
	}
	if err := k.RegisterType(testCtx(t), ty); err != nil {
		t.Fatalf("register probe: %v", err)
	}
	res, err := k.Create(testCtx(t), itemservice.CreateRequest{
		IdempotencyKey: newKey(), Principal: p, TypeID: "probe", TypeVersion: 1,
		Fields: map[string]any{"f_note": "n"},
	})
	if err != nil {
		t.Fatalf("create probe: %v", err)
	}
	return res.ItemID
}

// Public door, a user: caller is the token's actor; nobody on behalf.
func TestGuardCaller_PublicDoorUser(t *testing.T) {
	k := itemservice.New(pool)
	id := newProbe(t, k, probeAna)
	assertRejectedCode(t, "ana takes the agent's move", move(t, k, probeAna, id, "by_agent", newKey()), itemservice.CodeGuardFailed)
	if err := move(t, k, probeAna, id, "by_user", newKey()); err != nil {
		t.Errorf("ana -> by_user: %v", err)
	}
}

// Public door, an agent: caller is the agent, caller_on_behalf_of its user;
// created_by and created_on_behalf_of are what the kernel set at create.
func TestGuardCaller_PublicDoorAgent(t *testing.T) {
	k := itemservice.New(pool)
	bot := withSession(t, k, probeBot)
	id := newProbe(t, k, bot)
	assertRejectedCode(t, "the agent takes the user's move", move(t, k, bot, id, "by_user", newKey()), itemservice.CodeGuardFailed)
	if err := move(t, k, bot, id, "by_agent", newKey()); err != nil {
		t.Errorf("the agent -> by_agent: %v", err)
	}
}

// A timer: caller is system:timer, nobody on behalf (the attribution's
// on_behalf_of is never checked).
func TestGuardCaller_Timer(t *testing.T) {
	k := itemservice.New(pool)
	id := newProbe(t, k, probeAna)
	_, err := k.Internal().Transition(testCtx(t), itemservice.TransitionRequest{
		IdempotencyKey: newKey(), Principal: systemTimer, ItemID: id, ToState: "by_timer",
		Attribution: item.Attribution{OriginatingHook: "timer:probe@v1:start", CausationID: fmt.Sprintf("%s:1", id), Hop: 1},
	})
	if err != nil {
		t.Errorf("the timer -> by_timer: %v", err)
	}
}

// A hook: caller is the hook's principal, nobody on behalf.
func TestGuardCaller_Hook(t *testing.T) {
	k := itemservice.New(pool)
	id := newProbe(t, k, probeAna)
	_, err := k.Internal().Transition(testCtx(t), itemservice.TransitionRequest{
		IdempotencyKey: newKey(), Principal: item.Principal{Actor: "hook:probe:stamp"}, ItemID: id, ToState: "by_hook",
		Attribution: item.Attribution{OriginatingHook: "hook:probe@v1:stamp", CausationID: fmt.Sprintf("%s:1", id), Hop: 1},
	})
	if err != nil {
		t.Errorf("the hook -> by_hook: %v", err)
	}
}

// ResolveHold's re-check: the guard sees the original requester (actor and
// on_behalf_of), not the hook that resolves.
func TestGuardCaller_ResolveHoldRecheckIsTheRequester(t *testing.T) {
	k := itemservice.New(pool)
	id := newProbe(t, k, probeAna)
	bot := withSession(t, k, probeBot)
	req, err := k.Transition(testCtx(t), itemservice.TransitionRequest{
		IdempotencyKey: newKey(), Principal: bot, ItemID: id, ToState: "by_request",
	})
	if err != nil || req.Outcome != itemservice.OutcomePending {
		t.Fatalf("the agent asks for by_request = %+v, %v; want pending", req, err)
	}
	gate := item.Principal{Actor: "hook:probe:gate"}
	if _, err := k.Internal().ResolveHold(testCtx(t), itemservice.ResolveHoldRequest{
		IdempotencyKey: "hold:" + req.HoldID.String(), Principal: gate, ItemID: id, HoldID: req.HoldID,
		Verdict: itemservice.VerdictAllow,
		Attribution: item.Attribution{
			OriginatingHook: "hook:probe@v1:gate", CausationID: fmt.Sprintf("%s:%d", id, req.ItemSeq), Hop: 1,
		},
	}); err != nil {
		t.Fatalf("gate allows: %v", err)
	}
	state, hold := holdOf(t, k, probeAna, id)
	if state != "by_request" || hold == nil || hold.Status != item.HoldAllowed {
		t.Errorf("after resolution: state %s, hold %+v; want by_request, allowed (the re-check saw the agent and ana)",
			state, hold)
	}
}
