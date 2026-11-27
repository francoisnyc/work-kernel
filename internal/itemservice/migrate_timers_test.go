// Tests for track 1, item 6, step 3b: Items with timers.
//
//   - A state with a timer, or a timer at a field's instant, no longer
//     keeps an Item from being migrated: the workflow reloads under the new
//     version (internal/workflow, migrate_timers_e2e_test.go).
//   - An acting timer still does (HAS_TIMER), under either version: a run
//     started just before a migrate could not record itself after it.
//   - The old version's timer cannot move a migrated Item: its move is
//     stale (the migrate moved item_seq) or names the wrong version.
package itemservice_test

import (
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
	"github.com/francoisnyc/work-kernel/internal/lifecycle"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// rmSrc is a request waiting on its approver. With acting, waiting reminds
// the approver (an acting timer and its hook); with at, waiting ends at
// f_due (a timer at a field's instant).
func rmSrc(version int, acting, at bool, rest string) string {
	src := "\nid: rm_{s}\nversion: " + fmt.Sprint(version) + `
initial_state: open
fields:
  - {id: f_approver, type: principal}
  - {id: f_due, type: datetime}
states:
  - {id: open, category: todo}
`
	switch {
	case acting:
		src += "  - {id: waiting, category: waiting, acting: [{id: remind, after: 48h, every: 24h, max: 2, hook: ping}]}\n"
	case at:
		src += "  - {id: waiting, category: waiting, at: {field: f_due, to: open}}\n"
	default:
		src += "  - {id: waiting, category: waiting}\n"
	}
	src += `  - {id: done, category: done}
transitions:
  - {from: open, to: waiting}
  - {from: waiting, to: open}
  - {from: waiting, to: done}
`
	if acting {
		src += "hooks:\n  - {id: ping, on: timer, kind: action, action: email.notify, reads: [f_approver]}\n"
	}
	return src + rest
}

func (w migWorld) waitingRequest(t *testing.T) itemservice.WriteResult {
	t.Helper()
	res, err := w.create(t, w.eve, w.id("rm"), 1, nil, map[string]any{
		"f_approver": w.lea.Actor, "f_due": time.Now().Add(time.Hour).UTC().Format(typedef.DateTimeLayout)})
	id := mustCreate(t, "a request", res, err)
	if err := w.move(t, w.eve, id, "waiting"); err != nil {
		t.Fatalf("eve sends the request to waiting: %v", err)
	}
	return res
}

// An acting timer on the Item's state, under the version it is on, the one
// it would go to, or both: still refused, HAS_TIMER alone.
func TestMigrate_Timers_AnActingTimerStillRefuses(t *testing.T) {
	for name, c := range map[string][2]bool{
		"under both versions":        {true, true},
		"under the old version only": {true, false},
		"under the new version only": {false, true},
	} {
		t.Run(name, func(t *testing.T) {
			w := newMigWorld(t)
			w.reg(t, rmSrc(1, c[0], false, ""))
			id := w.waitingRequest(t).ItemID
			w.reg(t, rmSrc(2, c[1], false, migAllowed))
			deliverAll(t, id)
			if got := w.dryRun(t, id, w.id("rm"), 2); !slices.Equal(got, []lifecycle.MigrationCheck{lifecycle.HasTimer}) {
				t.Errorf("the dry run: %v, want has_timer alone", got)
			}
			_, err := w.migrate(t, w.adm, id, w.id("rm"), 2)
			assertNotMigratable(t, "the migrate", err, lifecycle.HasTimer)
			if v := versionOf(t, id); v != 1 {
				t.Errorf("the request is on version %d, want 1", v)
			}
		})
	}
}

// A timer at a field's instant on the Item's state: the migrate goes
// through, and the loader owes nothing.
func TestMigrate_Timers_ATimerAtAnInstantNoLongerRefuses(t *testing.T) {
	w := newMigWorld(t)
	w.reg(t, rmSrc(1, false, true, ""))
	id := w.waitingRequest(t).ItemID
	w.reg(t, rmSrc(2, false, true, migAllowed))
	w.migrateClean(t, id, w.id("rm"), 2)
}

// The old version's timer cannot move a migrated Item. Sent as the old run
// would send it, with the item_seq it last saw, or with the current one,
// it names the old version: invalid. (The internal door checks the version
// a kernel write names before its expected item_seq, so the version answers
// first.) Either way nothing is written; the run hands over once it hears
// of the migrate.
func TestMigrate_Timers_TheOldTimersMoveIsRefused(t *testing.T) {
	w := newMigWorld(t)
	typ := w.id("hk")
	w.reg(t, migHooked(1, false, false, true, ""))
	id := w.hooked(t, 1, "review", "waiting") // waiting has a 48h timer to review
	se := w.entry(t, id)
	w.reg(t, migHooked(2, false, false, true, migAllowed))
	w.migrateClean(t, id, typ, 2)

	fire := func(expected int64) error {
		t.Helper()
		_, err := w.k.Internal().Transition(testCtx(t), itemservice.TransitionRequest{
			IdempotencyKey: fmt.Sprintf("timer:%s:%d:%d", id, se.EnteredSeq, expected),
			Principal:      item.Principal{Actor: typedef.TimerPrincipal},
			ItemID:         id, ToState: "review", ExpectedItemSeq: expected,
			Attribution: item.Attribution{
				OriginatingHook: "timer:" + typ + "@v1:waiting",
				CausationID:     fmt.Sprintf("%s:%d", id, se.EnteredSeq),
				CorrelationID:   se.CorrelationID, Hop: se.Hop + 1,
			},
		})
		return err
	}
	if err := fire(se.ItemSeq); !errors.Is(err, itemservice.ErrInvalid) {
		t.Errorf("the old timer with the item_seq it saw before the migrate: %v, want ErrInvalid (it names version 1)", err)
	}
	if err := fire(w.entry(t, id).ItemSeq); !errors.Is(err, itemservice.ErrInvalid) {
		t.Errorf("the old timer with the current item_seq: %v, want ErrInvalid (it names version 1)", err)
	}
	if got := w.entry(t, id); got.State != "waiting" || versionOf(t, id) != 2 {
		t.Errorf("the request is in %s on version %d, want waiting on version 2", got.State, versionOf(t, id))
	}
}
