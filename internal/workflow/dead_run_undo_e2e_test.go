// End-to-end test for the consolidation after Step 2, item 7: a lost undo
// when the run is dead. The run that took the approval request is gone
// (terminated) when an escape cancels the hold after the deduction
// committed. The escape's event must start a workflow, whose LoadItem
// finds the undo pending: the refund lands, exactly once.
//
// To get a real run that took the request and never decided: a row lock
// on the balance keeps the run's React waiting while the request is
// delivered; then the worker stops, the run is terminated, the lock goes,
// and the deduction is committed by hand, as the dead run would have.
package workflow_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
)

func TestE2E_DeadRun_EscapeAfterTheDeduction_RefundsOnce(t *testing.T) {
	s := newLeaveStack(t)
	bal := s.balance(25)
	id := s.submittedRequest(5) // v2: the run completed after the HR check

	// The balance is locked: the run's React waits on it.
	lock, err := pool.Begin(serverCtx(t))
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback(context.Background())
	if _, err := lock.Exec(serverCtx(t), `select 1 from items where id = $1 for update`, bal); err != nil {
		t.Fatal(err)
	}
	ask := s.transition(s.bob, id, "approved", 0)
	if ask.Outcome != itemservice.OutcomePending {
		t.Fatalf("approve = %+v, want pending", ask)
	}
	eventually(t, 15*time.Second, "the request delivered to a run", func() bool {
		var delivered bool
		_ = pool.QueryRow(serverCtx(t), `select delivered_at is not null from outbox where item_id = $1 and item_seq = $2`,
			id, ask.ItemSeq).Scan(&delivered)
		return delivered
	})
	s.stopWorker()
	if err := tc.TerminateWorkflow(serverCtx(t), id.String(), "", "the run dies"); err != nil {
		t.Fatalf("terminate: %v", err)
	}
	if err := lock.Rollback(serverCtx(t)); err != nil {
		t.Fatal(err)
	}

	// The deduction the dead run would have committed; the hold stays open.
	if _, err := s.k.Internal().React(serverCtx(t), itemservice.ReactRequest{
		Principal: item.Principal{Actor: "reaction:vacation_request:deduct"}, SourceID: id, HoldID: ask.HoldID,
		Attribution: item.Attribution{OriginatingHook: "reaction:vacation_request@v2:deduct",
			CausationID: fmt.Sprintf("%s:%d", id, ask.ItemSeq), Hop: 1}}); err != nil {
		t.Fatalf("React: %v", err)
	}
	if got := s.used(bal); got != 5 {
		t.Fatalf("f_used after the deduction = %d, want 5", got)
	}
	escape := s.transition(s.ana, id, "withdrawn", 0) // the escape cancels the hold
	s.startHRWorker()

	deadline := time.Now().Add(15 * time.Second)
	for s.used(bal) != 0 {
		if time.Now().After(deadline) {
			var delivered bool
			_ = pool.QueryRow(serverCtx(t), `select delivered_at is not null from outbox where item_id = $1 and item_seq = $2`,
				id, escape.ItemSeq).Scan(&delivered)
			status, _, _, found := runStatus(t, id)
			t.Fatalf("no refund after 15 s (f_used %d, refunds %d); the escape delivered %v; the workflow: found %v, %v",
				s.used(bal), reactionEvents(t, bal, "refund"), delivered, found, status)
		}
		time.Sleep(100 * time.Millisecond)
	}
	staysFor(t, 3*time.Second, "one refund", func() bool { return reactionEvents(t, bal, "refund") == 1 && s.used(bal) == 0 })
}
