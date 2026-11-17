// End-to-end test for proof-of-concept Step 2, part 3: a state timer that
// comes due while a hold is open waits for the hold to close, then looks
// again. On the real stack: ticket v3, its in_progress timer (24 h) on a
// worker scaled for ticket only (a day lasts deferDay), and the ERP check
// held open by a slow customer (deferLatency, longer than the timer).
//
//   - The check allows: the ticket is approved and the timer, now spent,
//     does nothing.
//   - The check denies: the ticket is still in_progress, so the timer fires
//     once the hold closes and moves it to open, as system:timer, after the
//     denial.
package workflow_test

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/connstub"
	"github.com/francoisnyc/work-kernel/internal/hooks"
	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
	wf "github.com/francoisnyc/work-kernel/internal/workflow"
)

const (
	deferDay     = time.Second             // in_progress's 24 h timer: 1 s
	deferLatency = 2500 * time.Millisecond // the ERP check answers after the timer is due
)

// newDeferStack: a hook stack whose customer the ERP answers slowly, with
// the given credit limit, on a worker scaled for ticket.
func newDeferStack(t *testing.T, creditLimit int) *hookStack {
	t.Helper()
	h := newHookStack(t)
	h.erp.SetCustomer(h.customer, connstub.Customer{CreditLimit: creditLimit, Latency: deferLatency})
	h.stopWorker()
	h.worker = wf.NewWorker(tc, h.queue, h.k, wf.WorkerOptions{
		Connectors: hooks.NewConnectors(h.crm.URL, h.erp.URL),
		HookBudget: 10 * time.Second, // the slow check gets its answer in
		DevDay:     deferDay, DevTypes: []string{"ticket"},
	})
	if err := h.worker.Start(); err != nil {
		t.Fatalf("start worker: %v", err)
	}
	return h
}

// heldPastTheTimer: the ticket entered in_progress and its approval is held
// by the slow check; the timer comes due during the hold.
func (h *hookStack) heldPastTheTimer(p item.Principal) uuid.UUID {
	h.t.Helper()
	id := h.newTicket(p)
	h.prefetched(p, id)
	h.toInProgress(p, id)
	res, _ := h.approve(p, id, 0)
	if res.Outcome != itemservice.OutcomePending {
		h.t.Fatalf("approve = %+v, want held by the ERP check", res)
	}
	return id
}

// timerMoves returns the seqs of the timer's transitions, and the seq of the
// approval's denial (0 if none).
func timerMoves(t *testing.T, id uuid.UUID) (moves []int64, denied int64) {
	t.Helper()
	for seq, e := range events(t, id) {
		switch {
		case e.Op == "transition" && e.Actor == "system:timer":
			moves = append(moves, seq)
		case e.Op == "transition_denied":
			denied = seq
		}
	}
	return moves, denied
}

func TestE2E_DeferredTimer_HoldAllows_TimerDoesNothing(t *testing.T) {
	h := newDeferStack(t, 1000) // the ticket's 100 is within the limit
	ana := approver(t)
	id := h.heldPastTheTimer(ana)
	eventually(t, 10*time.Second, "approved", func() bool { s, _ := h.hold(ana, id); return s == "approved" })
	staysFor(t, 3*deferDay, "approved, the timer spent", func() bool {
		s, _ := h.hold(ana, id)
		moves, _ := timerMoves(t, id)
		return s == "approved" && len(moves) == 0
	})
}

func TestE2E_DeferredTimer_HoldDenies_TimerFiresAfter(t *testing.T) {
	h := newDeferStack(t, 50) // the ticket's 100 is over the limit
	ana := approver(t)
	id := h.heldPastTheTimer(ana)
	eventually(t, 10*time.Second, "the timer moved the ticket to open", func() bool {
		s, _ := h.hold(ana, id)
		return s == "open"
	})
	moves, denied := timerMoves(t, id)
	if denied == 0 || len(moves) != 1 || moves[0] < denied {
		t.Errorf("denial at %d, timer moves at %v; want one move, after the denial", denied, moves)
	}
	if e := events(t, id)[moves[0]]; e.Payload["to"] != "open" {
		t.Errorf("the timer's move = %+v, want to open", e)
	}
	staysFor(t, 2*deferDay, "open, one move", func() bool {
		s, _ := h.hold(ana, id)
		moves, _ := timerMoves(t, id)
		return s == "open" && len(moves) == 1
	})
}
