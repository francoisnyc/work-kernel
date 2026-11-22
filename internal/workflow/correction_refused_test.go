// Tests for the consolidation after Step 2, item 2: a check's correction
// that ResolveHold refuses (Invalid, Rejected or Forbidden: a value of the
// wrong shape, a frozen field) must not leave the hold open. RunCheck then
// resolves the hold as error, reason correction_refused, under its own key
// hold:<id>:refused, recorded in history. Only on the correction path: any
// other refusal is returned as before, and the workflow does not count it
// as giving up.
//
// The HR stub answers "bob", not a principal ("user:bob"), for ana.
package workflow_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.temporal.io/sdk/temporal"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
	wf "github.com/francoisnyc/work-kernel/internal/workflow"
)

// refusedEvents counts the request's transition_failed events with reason
// correction_refused.
func refusedEvents(t *testing.T, id uuid.UUID) (n int) {
	t.Helper()
	for _, e := range events(t, id) {
		if e.Op == "transition_failed" && e.Payload["reason"] == "correction_refused" {
			n++
		}
	}
	return n
}

func TestE2E_CorrectionRefused_HoldClosesAsError(t *testing.T) {
	m := newMailStack(t)
	m.balance(25)
	m.hr.SetManager(m.ana.Actor, "bob") // not a principal
	res, err := m.k.Create(serverCtx(t), itemservice.CreateRequest{IdempotencyKey: "key-" + uuid.NewString(),
		Principal: m.ana, TypeID: "vacation_request", TypeVersion: 3,
		Fields: map[string]any{"f_start": "2099-07-01", "f_end": "2099-07-05", "f_days": 5, "f_leave_type": "annual"}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	id := res.ItemID
	if ask := m.transition(m.ana, id, "submitted", 0); ask.Outcome != itemservice.OutcomePending {
		t.Fatalf("submit = %+v, want pending", ask)
	}
	deadline := time.Now().Add(testBudget + 3*time.Second)
	for {
		it := m.item(m.ana, id)
		if it.Hold != nil && it.Hold.Status != item.HoldOpen {
			if it.Hold.Status != item.HoldError || it.Hold.Reason != "correction_refused" || it.State != "draft" {
				t.Fatalf("hold %+v, state %s; want error correction_refused, still draft", it.Hold, it.State)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("hold %+v after the hook budget (%v) and 3 s; want closed as error, correction_refused", it.Hold, testBudget)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if n := refusedEvents(t, id); n != 1 {
		t.Errorf("transition_failed correction_refused events = %d, want 1", n)
	}
}

// The activity retried after its error resolution committed (its attempt
// failed after the write, say) asks HR again and sends the correction
// again, under hold:<id>: that must not collide with the error
// resolution's key. It ends cleanly: no key reuse, the hold stays closed,
// no second event.
func TestActivities_RunCheck_CorrectionRefused_RetryEndsCleanly(t *testing.T) {
	m := newMailStack(t)
	m.stopRelay()
	m.stopWorker()
	m.balance(25)
	m.hr.SetManager(m.ana.Actor, "bob")
	res, err := m.k.Create(serverCtx(t), itemservice.CreateRequest{IdempotencyKey: "key-" + uuid.NewString(),
		Principal: m.ana, TypeID: "vacation_request", TypeVersion: 3,
		Fields: map[string]any{"f_start": "2099-07-01", "f_end": "2099-07-05", "f_days": 5, "f_leave_type": "annual"}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	id := res.ItemID
	ask := m.transition(m.ana, id, "submitted", 0)
	a := &wf.Activities{Items: m.k, Connectors: m.remoteHR()}
	in := wf.HookInput{ItemID: id.String(), TypeID: "vacation_request", TypeVersion: 3, HookID: "hr_check",
		CauseSeq: ask.ItemSeq, HoldID: ask.HoldID.String(), Hop: 1, IdempotencyKey: "hold:" + ask.HoldID.String()}
	for attempt := 1; attempt <= 2; attempt++ {
		err := a.RunCheck(serverCtx(t), in)
		var appErr *temporal.ApplicationError
		stale := errors.As(err, &appErr) && appErr.Type() == wf.ErrTypeStale
		if err != nil && !(attempt == 2 && stale) {
			t.Fatalf("RunCheck attempt %d = %v; want nil (the hold closed as error, correction_refused)", attempt, err)
		}
		if errors.Is(err, itemservice.ErrIdempotencyKeyReuse) {
			t.Fatalf("RunCheck attempt %d: key reuse: %v", attempt, err)
		}
	}
	it := m.item(m.ana, id)
	if it.Hold == nil || it.Hold.Status != item.HoldError || it.Hold.Reason != "correction_refused" || it.State != "draft" {
		t.Errorf("hold %+v, state %s; want error correction_refused, still draft", it.Hold, it.State)
	}
	if n := refusedEvents(t, id); n != 1 {
		t.Errorf("transition_failed correction_refused events = %d, want 1", n)
	}
}

// Any other refusal (RunCheck answering Rejected: the kernel refused an
// allow, say) is not giving up: no error resolution, the hold stays open
// until something closes it.
func TestHooks_NonCorrectionRefusal_IsNotGivingUp(t *testing.T) {
	h := newHookHarness(t, orderSnapshot("open", 5, 5))
	h.checkErr["check"] = temporal.NewNonRetryableApplicationError("refused", wf.ErrTypeRejected, nil)
	h.deliver(0, requestEvent(6))
	h.deliver(time.Hour, closing(7, "transition", "open"))
	h.runToCompletion()
	if len(h.resolves) != 0 {
		t.Errorf("ResolveHold calls = %+v, want none: a refusal is not giving up", h.resolves)
	}
	if len(h.check) != 1 {
		t.Errorf("RunCheck calls = %d, want 1", len(h.check))
	}
}
