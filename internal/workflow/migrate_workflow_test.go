// Workflow tests for track 1, item 6, step 3b: a run that hears of a
// migrate (a migrated event) hands over to a fresh run that loads
// everything under the new version: it continues as new with nothing
// carried. Behind the GetVersion change ID "migrate-reloads": a run whose
// history was recorded before the change keeps the old behaviour (the
// event is taken and nothing else happens).
package workflow_test

import (
	"errors"
	"testing"
	"time"

	"go.temporal.io/sdk/converter"
	sdk "go.temporal.io/sdk/workflow"

	wf "github.com/francoisnyc/work-kernel/internal/workflow"
)

func migratedEvent(seq int64) wf.Event { return wf.Event{Seq: seq, Op: "migrated"} }

// The run in review, its 4h timer armed, hears of a migrate after 1h: it
// takes the event, fires nothing, and continues as new without carrying
// its snapshot, so the next run loads the new version from Postgres.
func TestItemWorkflow_Migrated_ContinuesAsNewWithNothingCarried(t *testing.T) {
	h := newHarness(t, snapshot(t, "review", 5, 5, t0))
	out := h.deliver(time.Hour, migratedEvent(6))

	err := h.run(wf.Input{ItemID: itemID})
	var next *sdk.ContinueAsNewError
	if !errors.As(err, &next) {
		t.Fatalf("workflow ended with %v, want continue-as-new", err)
	}
	h.wantApplied("the migrated event", out, true)
	var in wf.Input
	if err := converter.GetDefaultDataConverter().FromPayloads(next.Input, &in); err != nil {
		t.Fatalf("decode the next run's input: %v", err)
	}
	if in.ItemID != itemID || in.Carried != nil {
		t.Errorf("next run's input = %+v, want the item ID and nothing carried", in)
	}
	if len(h.fires) != 0 {
		t.Errorf("FireTimer calls = %+v, want none: the old version's timer is dropped", h.fires)
	}
	if now := h.env.Now(); !now.Equal(t0.Add(time.Hour)) {
		t.Errorf("the run ended at %v, want at the migrate (%v)", now, t0.Add(time.Hour))
	}
}

// A run recorded before the change (migrate-reloads at DefaultVersion)
// keeps the old behaviour: the migrated event is taken, the run goes on,
// and its timer fires as before.
func TestItemWorkflow_Migrated_OldVersionKeepsTheRun(t *testing.T) {
	h := newHarness(t, snapshot(t, "review", 5, 5, t0))
	h.env.OnGetVersion("migrate-reloads", sdk.DefaultVersion, 1).Return(sdk.DefaultVersion)
	out := h.deliver(time.Hour, migratedEvent(6))
	back := h.deliver(4*time.Hour+time.Minute, transitionEvent(7, "review", "draft"))
	h.runToCompletion()

	h.wantApplied("the migrated event", out, true)
	h.wantApplied("the timer's transition", back, true)
	if len(h.fires) != 1 || !h.fires[0].At.Equal(t0.Add(4*time.Hour)) || h.fires[0].In.ExpectedItemSeq != 6 {
		t.Errorf("FireTimer calls = %+v, want one at %v expecting item_seq 6", h.fires, t0.Add(4*time.Hour))
	}
}
