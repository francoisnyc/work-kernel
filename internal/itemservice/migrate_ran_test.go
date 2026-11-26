// Tests for track 1, item 6, steps 2 and 3a: an Item that has run an
// action. An action's write-back is stamped with the version the Item was
// on (hook:<type>@v<n>:<id>). Step 2 read every entry under the version
// the Item would go to, so an action finished under version 1 read as
// unfinished under version 2 and the migrate was refused. Since step 3a
// each entry is read under the version it was made under: the action is
// finished, the migrate goes through, and nothing runs again.
//
// A reaction is different: its outcome record names the reaction without
// a version (<type>:<id>), so one finished under version 1 reads as
// finished under version 2. The migrate goes through, and it does not run
// again.
package itemservice_test

import (
	"fmt"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
)

func TestMigrate_Quiet_AnActionFinishedUnderTheOldVersion_GoesThrough(t *testing.T) {
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

	// Version 2 declares the same hook on the same state: the dry run finds
	// nothing, the migrate goes through, and the loader owes nothing.
	w.reg(t, migHooked(2, false, true, false, migAllowed))
	w.migrateClean(t, id, w.id("hk"), 2)
	// The order was placed once, and is not pending again.
	if n := orders(); n != 1 {
		t.Errorf("events written by place_order = %d, want 1", n)
	}
	if se, err = w.k.StateEntry(testCtx(t), id); err != nil || len(se.PendingActions) != 0 {
		t.Errorf("after the migrate: pending actions = %+v, %v; want none", se.PendingActions, err)
	}
}

func TestMigrate_Quiet_AReactionFinishedUnderTheOldVersion_GoesThrough(t *testing.T) {
	bench := func(version int, rest string) string {
		return "\nid: bench_{s}\nversion: " + string(rune('0'+version)) + `
initial_state: open
states:
  - {id: open, category: todo}
  - {id: working, category: active}
transitions:
  - {from: open, to: working}
reactions:
  - {id: make, kind: create, on: enter, state: working, type: auto_{s}, version: 1, fields: {f_title: '"made"'}}
` + rest
	}
	const auto = "\nid: auto_{s}\nversion: 1\nfields:\n  - {id: f_title, type: string}\naccepts_reactions:\n  bench_{s}:make: {creates: [f_title]}\n"
	w := newMigWorld(t)
	w.reg(t, auto)
	w.reg(t, bench(1, ""))
	b := w.container(t, w.lea, w.id("bench"), nil)
	if err := w.move(t, w.lea, b, "working"); err != nil { // event 2
		t.Fatalf("lea starts the bench: %v", err)
	}
	// The reaction runs under version 1: it creates the Item inside.
	out, err := w.k.Internal().ReactCreate(testCtx(t), itemservice.ReactionRunRequest{
		SourceID: b, Reaction: "make", CauseItem: b, CauseSeq: 2, CorrelationID: "corr-" + newKey(), Hop: 1,
	})
	if err != nil || out.Outcome != itemservice.ReactionApplied {
		t.Fatalf("ReactCreate = %+v, %v; want applied", out, err)
	}
	made := func() (n int) {
		t.Helper()
		if err := pool.QueryRow(testCtx(t),
			`select count(*) from items where container_id = $1 and type_id = $2`, b, w.id("auto")).Scan(&n); err != nil {
			t.Fatalf("count the Items made: %v", err)
		}
		return n
	}
	pending := func() []itemservice.PendingReaction {
		t.Helper()
		se, err := w.k.StateEntry(testCtx(t), b)
		if err != nil {
			t.Fatalf("StateEntry: %v", err)
		}
		return se.PendingReactions
	}
	if got := pending(); len(got) != 0 {
		t.Fatalf("after the reaction, under version 1: pending = %+v, want none", got)
	}

	// Version 2 declares the same reaction.
	w.reg(t, bench(2, migAllowed))
	deliverAll(t, b)
	if got := w.dryRun(t, b, w.id("bench"), 2); len(got) != 0 {
		t.Errorf("the dry run: %v, want no problem", got)
	}
	if _, err := w.migrate(t, w.adm, b, w.id("bench"), 2); err != nil {
		t.Fatalf("the migrate: %v", err)
	}
	if v := versionOf(t, b); v != 2 {
		t.Errorf("the bench is on version %d, want 2", v)
	}
	// Under version 2 the reaction is not pending, and it ran once.
	if got := pending(); len(got) != 0 {
		t.Errorf("after the migrate: pending = %+v, want none", got)
	}
	if n := made(); n != 1 {
		t.Errorf("Items made by the reaction = %d, want 1", n)
	}
}
