// Tests for the consolidation after Step 2, item 3: the held deduction
// writes on the balance resolution would pick now, or not at all. Between
// submitting (which resolved the request's balance link) and approving, the
// balance can stop matching the resolve rule: closed, given to someone
// else, or joined by a second open balance for the requester. React runs
// the resolve lookup again under the target lock; any answer other than
// the linked balance denies the approval with link_target_changed, and no
// balance is touched. The requester resubmits, which resolves again.
package itemservice_test

import (
	"testing"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
)

func assertDeniedLinkTargetChanged(t *testing.T, w leaveWorld, id uuid.UUID) {
	t.Helper()
	_, h := w.approve(t, w.bob, id)
	if h.Status != item.HoldDenied || h.Reason != "link_target_changed" {
		t.Errorf("approval hold = %+v, want denied link_target_changed", h)
	}
	if s := w.state(t, id); s != "submitted" {
		t.Errorf("request state = %s, want still submitted", s)
	}
}

func TestReact_BalanceClosedSinceSubmit_Denied(t *testing.T) {
	w := newLeaveWorld(t)
	b1 := w.balance(t, w.ana, 25)
	id := w.submitted(t, w.ana, w.bob, 5)
	w.closeBalance(t, b1)
	assertDeniedLinkTargetChanged(t, w, id)
	assertUsed(t, w, b1, 0)
}

func TestReact_BalanceGivenToAnotherSinceSubmit_Denied(t *testing.T) {
	w := newLeaveWorld(t)
	b1 := w.balance(t, w.ana, 25)
	id := w.submitted(t, w.ana, w.bob, 5)
	erin := person("erin")
	if _, err := w.k.Update(testCtx(t), itemservice.UpdateRequest{IdempotencyKey: newKey(), Principal: w.hana,
		ItemID: b1, Fields: map[string]any{"f_owner": erin.Actor}}); err != nil {
		t.Fatalf("hana gives the balance to erin: %v", err)
	}
	assertDeniedLinkTargetChanged(t, w, id)
	assertUsed(t, w, b1, 0) // now erin's: untouched
}

func TestReact_SecondOpenBalanceSinceSubmit_Denied(t *testing.T) {
	w := newLeaveWorld(t)
	b1 := w.balance(t, w.ana, 25)
	id := w.submitted(t, w.ana, w.bob, 5)
	b2 := w.balance(t, w.ana, 25) // ambiguous now
	assertDeniedLinkTargetChanged(t, w, id)
	assertUsed(t, w, b1, 0)
	assertUsed(t, w, b2, 0)
}
