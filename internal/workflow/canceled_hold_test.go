// Tests for the consolidation after Step 2, item 5: an event that cancels
// a hold (an escape, canceled_hold_id; the relay sets Canceled) is never
// read as the hold allowed, even when it enters the very state the hold
// requested. Today the workflow infers "allowed" from To == RequestedTo.
// A loadable Type cannot produce this any more (a hold on an escape is
// refused at load), so the snapshot and event are built by hand.
package workflow_test

import (
	"reflect"
	"time"

	"testing"

	wf "github.com/francoisnyc/work-kernel/internal/workflow"
)

// The approval hold (deduct) is canceled by an event entering approved,
// the requested state. Its undo runs, caused by that event; the hold is
// not taken as the one that entered approved, so leaving approved by the
// undo's trigger runs no second undo.
func TestCanceledHold_NeverReadAsAllowed(t *testing.T) {
	h := newReactionHarness(t, leaveSnapshot("submitted", 5, 5))
	h.undoFails[hold1] = 2 // keeps the run alive while the withdrawal arrives
	h.deliver(0, approvalRequest(6, hold1))
	h.deliver(time.Hour, wf.Event{Seq: 7, Op: "transition", From: "submitted", To: "approved", HoldID: hold1,
		Canceled: true, CorrelationID: "corr-8"})
	h.deliver(time.Hour+time.Second, wf.Event{Seq: 8, Op: "transition", From: "approved", To: "withdrawn", CorrelationID: "corr-9"})
	h.runToCompletion()
	var causes []int64
	for _, u := range h.undos {
		if u.In.HoldID != hold1 || u.In.ReactionID != "refund" {
			t.Errorf("undo %+v, want refund of %s", u.In, hold1)
		}
		causes = append(causes, u.In.CauseSeq)
	}
	if want := []int64{7, 7, 7}; !reflect.DeepEqual(causes, want) {
		t.Errorf("undo attempts caused by %v, want %v: the cancel (7) runs it, the withdrawal (8) does not", causes, want)
	}
}
