// End-to-end test for Step 5: two versions of one Type running side by side.
// Items stay on the version they were created with, so one worker and one
// relay must run v1's timers and v2's timers at the same time, each from its
// own version's timer table.
//
// The Types mirror ticket v1 and v2 (types/ticket.v*.yaml) with timers of
// seconds instead of days.
package workflow_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/itemservice"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

const (
	shiftV1Timer      = 8 * time.Second // in_progress -> open
	shiftV2Timer      = 2 * time.Second // in_progress -> open, shorter in v2
	shiftV2WaitTimer  = 3 * time.Second // waiting -> in_progress, new in v2
	shiftTimerSlack   = 6 * time.Second // how late a timer's write may land on a slow runner
	shiftWaitForTimer = 30 * time.Second
)

const shiftV1 = `
id: shift
version: 1
initial_state: open
states:
  - {id: open, category: todo}
  - {id: in_progress, category: active, timer: {after: 8s, to: open}}
  - {id: done, category: done}
transitions:
  - {from: open, to: in_progress}
  - {from: in_progress, to: open}
  - {from: in_progress, to: done}
`

const shiftV2 = `
id: shift
version: 2
initial_state: open
states:
  - {id: open, category: todo}
  - {id: in_progress, category: active, timer: {after: 2s, to: open}}
  - {id: waiting, category: waiting, timer: {after: 3s, to: in_progress}}
  - {id: done, category: done}
transitions:
  - {from: open, to: in_progress}
  - {from: in_progress, to: open}
  - {from: in_progress, to: waiting}
  - {from: waiting, to: in_progress}
  - {from: in_progress, to: done}
start: {older_versions: all}
`

func (s *stack) registerShiftTypes() {
	s.t.Helper()
	for _, def := range []string{shiftV1, shiftV2} {
		ty, err := typedef.Parse([]byte(def))
		if err != nil {
			s.t.Fatalf("parse shift Type: %v", err)
		}
		if err := s.k.RegisterType(serverCtx(s.t), ty); err != nil {
			s.t.Fatalf("register shift v%d: %v", ty.Version, err)
		}
	}
}

func (s *stack) createShift(version int) uuid.UUID {
	s.t.Helper()
	res, err := s.k.Create(serverCtx(s.t), itemservice.CreateRequest{
		IdempotencyKey: "key-" + uuid.NewString(), Principal: alice, TypeID: "shift", TypeVersion: version,
	})
	if err != nil {
		s.t.Fatalf("create shift v%d: %v", version, err)
	}
	return res.ItemID
}

// timerGap returns how long after event entered the timer's event seq was
// written, from the events' own timestamps, and which hook wrote it.
func timerGap(t *testing.T, id uuid.UUID, entered, seq int64) (gap time.Duration, hook string) {
	t.Helper()
	var from, to time.Time
	err := pool.QueryRow(serverCtx(t),
		`select a.created_at, b.created_at, coalesce(b.originating_hook, '')
		 from item_events a, item_events b
		 where a.item_id = $1 and a.item_seq = $2 and b.item_id = $1 and b.item_seq = $3`,
		id, entered, seq).Scan(&from, &to, &hook)
	if err != nil {
		t.Fatalf("read events %d and %d: %v", entered, seq, err)
	}
	return to.Sub(from), hook
}

// A v1 Item and a v2 Item enter in_progress at the same time. Each fires at
// its own version's duration: v2 first, while v1 is still waiting, then v1.
// Each timer's write names its own version, and both workflows complete.
func TestVersions_V1AndV2SideBySide_EachFiresAtItsOwnDuration(t *testing.T) {
	s := newStack(t)
	s.registerShiftTypes()
	v1, v2 := s.createShift(1), s.createShift(2)
	s.move(v1, "in_progress") // item_seq 2
	s.move(v2, "in_progress") // item_seq 2

	// v2 fires first. v1 must still be in_progress when it does.
	s.waitState(v2, "open", shiftWaitForTimer)
	if state, _ := s.state(v1); state != "in_progress" {
		t.Fatalf("when v2's timer fired, v1 was %q, want still in_progress", state)
	}
	s.waitState(v1, "open", shiftWaitForTimer)

	for _, c := range []struct {
		id       uuid.UUID
		version  string
		duration time.Duration
	}{
		{v1, "v1", shiftV1Timer},
		{v2, "v2", shiftV2Timer},
	} {
		gap, hook := timerGap(t, c.id, 2, 3)
		if gap < c.duration || gap > c.duration+shiftTimerSlack {
			t.Errorf("%s: the timer wrote %v after entering in_progress, want %v (up to %v late)",
				c.version, gap, c.duration, shiftTimerSlack)
		}
		if want := "timer:shift@" + c.version + ":in_progress"; hook != want {
			t.Errorf("%s: originating_hook = %q, want %q", c.version, hook, want)
		}
		// open has no timer, so each Item's workflow has nothing left to do.
		s.waitCompleted(c.id)
	}
}

// v2's new state works end to end: waiting fires back to in_progress after
// its own timer, which in turn fires back to open after v2's in_progress
// timer, all in one workflow that then completes.
func TestVersions_V2WaitingState_FiresBackToInProgress(t *testing.T) {
	s := newStack(t)
	s.registerShiftTypes()
	id := s.createShift(2)
	s.move(id, "in_progress") // 2
	s.move(id, "waiting")     // 3

	s.waitState(id, "in_progress", shiftWaitForTimer) // 4, by the waiting timer
	s.waitState(id, "open", shiftWaitForTimer)        // 5, by the in_progress timer

	gap, hook := timerGap(t, id, 3, 4)
	if gap < shiftV2WaitTimer || gap > shiftV2WaitTimer+shiftTimerSlack {
		t.Errorf("waiting timer wrote %v after entering waiting, want %v", gap, shiftV2WaitTimer)
	}
	if hook != "timer:shift@v2:waiting" {
		t.Errorf("event 4 originating_hook = %q, want timer:shift@v2:waiting", hook)
	}
	gap, hook = timerGap(t, id, 4, 5)
	if gap < shiftV2Timer || gap > shiftV2Timer+shiftTimerSlack {
		t.Errorf("in_progress timer wrote %v after the waiting timer's move, want %v", gap, shiftV2Timer)
	}
	if hook != "timer:shift@v2:in_progress" {
		t.Errorf("event 5 originating_hook = %q, want timer:shift@v2:in_progress", hook)
	}
	s.waitCompleted(id)
}

// A v1 Item is pinned to v1: v2's new state does not exist for it.
func TestVersions_V1ItemIsPinned_WaitingIsRejected(t *testing.T) {
	s := newStack(t)
	s.registerShiftTypes()
	id := s.createShift(1)
	s.move(id, "in_progress")

	_, err := s.k.Transition(serverCtx(t), itemservice.TransitionRequest{
		IdempotencyKey: "key-" + uuid.NewString(), Principal: alice, ItemID: id, ToState: "waiting",
	})
	if !errors.Is(err, itemservice.ErrRejected) {
		t.Errorf("v1 Item to waiting: error = %v, want ErrRejected", err)
	}
	s.move(id, "done") // let the workflow finish rather than wait 8 s
	s.waitCompleted(id)
}
