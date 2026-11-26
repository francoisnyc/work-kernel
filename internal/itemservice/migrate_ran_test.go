// Tests for track 1, item 6, step 2: the limit on an Item that has run an
// action. An action's write-back is stamped with the version the Item was
// on (hook:<type>@v<n>:<id>), and a version reads only its own stamps. So
// an action finished under version 1 reads as unfinished under version 2
// when version 2 declares the same hook on a state the Item entered: the
// migrate is refused, and nothing runs again. Reading history written
// before a migrate is a later step.
package itemservice_test

import (
	"fmt"
	"slices"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
	"github.com/francoisnyc/work-kernel/internal/lifecycle"
)

func TestMigrate_Quiet_AnActionFinishedUnderTheOldVersion_IsRefused(t *testing.T) {
	w := newMigWorld(t)
	w.reg(t, migHooked(1, false, true, false, ""))
	id := w.hooked(t, 1, "review", "ordered") // events 1, 2, 3

	// The order is placed: the hook's write-back, for the entry into ordered.
	se, err := w.k.StateEntry(testCtx(t), id)
	if err != nil {
		t.Fatalf("StateEntry: %v", err)
	}
	if len(se.PendingActions) != 1 || se.PendingActions[0].Hook != "place_order" {
		t.Fatalf("pending actions = %+v, want place_order", se.PendingActions)
	}
	p := se.PendingActions[0]
	if _, err := w.k.Internal().Update(testCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: fmt.Sprintf("hook:%s:%d:place_order", id, p.CauseSeq),
		Principal:      item.Principal{Actor: "hook:" + w.id("hk") + ":place_order"},
		ItemID:         id, Fields: map[string]any{"f_order_id": "INV-1"},
		Attribution: item.Attribution{
			OriginatingHook: "hook:" + w.id("hk") + "@v1:place_order",
			CausationID:     fmt.Sprintf("%s:%d", id, p.CauseSeq),
			CorrelationID:   p.CorrelationID, Hop: p.Hop + 1,
		},
	}); err != nil {
		t.Fatalf("the order's write-back: %v", err)
	}
	orders := func() (n int) {
		t.Helper()
		if err := pool.QueryRow(testCtx(t),
			`select count(*) from item_events where item_id = $1 and originating_hook like 'hook:%:place_order'`,
			id).Scan(&n); err != nil {
			t.Fatalf("count the order's events: %v", err)
		}
		return n
	}
	if se, err = w.k.StateEntry(testCtx(t), id); err != nil || len(se.PendingActions) != 0 {
		t.Fatalf("after the write-back, under version 1: pending actions = %+v, %v; want none", se.PendingActions, err)
	}

	// Version 2 declares the same hook on the same state.
	w.reg(t, migHooked(2, false, true, false, migAllowed))
	deliverAll(t, id)
	if got := w.dryRun(t, id, w.id("hk"), 2); !slices.Equal(got, []lifecycle.MigrationCheck{lifecycle.ActionUnfinished}) {
		t.Errorf("the dry run: %v, want action_unfinished alone", got)
	}
	_, err = w.migrate(t, w.adm, id, w.id("hk"), 2)
	assertNotMigratable(t, "the migrate", err, lifecycle.ActionUnfinished)
	if v := versionOf(t, id); v != 1 {
		t.Errorf("the request is on version %d, want 1", v)
	}
	// The order was placed once, and is not pending again.
	if n := orders(); n != 1 {
		t.Errorf("events written by place_order = %d, want 1", n)
	}
	if se, err = w.k.StateEntry(testCtx(t), id); err != nil || len(se.PendingActions) != 0 {
		t.Errorf("after the refusal: pending actions = %+v, %v; want none", se.PendingActions, err)
	}
}
