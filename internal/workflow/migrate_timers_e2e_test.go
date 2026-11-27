// End-to-end tests for track 1, item 6, step 3b: an Item whose state has a
// timer can be migrated. The migrated event reaches the Item's workflow
// (starting one if none runs); the run hands over to a fresh one that
// loads the new version, and the timer counts from the state entry with
// the new version's duration. The old version's timer never moves the
// Item: exactly one timer event in all, written under the new version.
//
// Each test has its own Type: its versions differ from every other test's.
package workflow_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"github.com/francoisnyc/work-kernel/internal/itemservice"
	"github.com/francoisnyc/work-kernel/internal/typedef"
	wf "github.com/francoisnyc/work-kernel/internal/workflow"
)

const mtSlack = 6 * time.Second // how late a timer's write may land on a slow runner

// mtSrc is a shift: open -> in_progress -> open | done. timer, if set, is
// the in_progress state's timer block (e.g. "timer: {after: 3s, to: open}").
// A version above 1 keeps the older ones open and lets alice migrate.
func mtSrc(id string, version int, timer, rest string) string {
	ip := "  - {id: in_progress, category: active}\n"
	if timer != "" {
		ip = "  - {id: in_progress, category: active, " + timer + "}\n"
	}
	src := "\nid: " + id + "\nversion: " + string(rune('0'+version)) + `
initial_state: open
fields:
  - {id: f_ok,   type: bool}
  - {id: f_due1, type: datetime}
  - {id: f_due2, type: datetime}
states:
  - {id: open, category: todo}
` + ip + `  - {id: done, category: done}
transitions:
  - {from: open, to: in_progress}
  - {from: in_progress, to: open}
  - {from: in_progress, to: done}
` + rest
	if version > 1 {
		src += "start:\n  older_versions: all\nmigrate:\n  allowed: ['principal:" + alice.Actor + "']\n"
	}
	return src
}

func (s *stack) mtRegister(defs ...string) {
	s.t.Helper()
	for _, def := range defs {
		ty, err := typedef.Parse([]byte(def))
		if err != nil {
			s.t.Fatalf("parse: %v", err)
		}
		if err := s.k.RegisterType(serverCtx(s.t), ty); err != nil {
			s.t.Fatalf("register %s v%d: %v", ty.ID, ty.Version, err)
		}
	}
}

// mtInProgress creates a version 1 Item, moves it to in_progress and waits
// until the relay has delivered everything. It returns the Item and the
// item_seq of the entry into in_progress.
func (s *stack) mtInProgress(typ string, fields map[string]any) (uuid.UUID, int64) {
	s.t.Helper()
	res, err := s.k.Create(serverCtx(s.t), itemservice.CreateRequest{
		IdempotencyKey: "key-" + uuid.NewString(), Principal: alice, TypeID: typ, TypeVersion: 1, Fields: fields,
	})
	if err != nil {
		s.t.Fatalf("create %s v1: %v", typ, err)
	}
	entered := s.move(res.ItemID, "in_progress").ItemSeq
	s.waitDelivered(res.ItemID)
	return res.ItemID, entered
}

func (s *stack) mtMigrate(id uuid.UUID, typ string, version int) int64 {
	s.t.Helper()
	res, err := s.k.Migrate(serverCtx(s.t), itemservice.MigrateRequest{
		IdempotencyKey: "key-" + uuid.NewString(), Principal: alice, ItemID: id, TypeID: typ, TypeVersion: version,
	})
	if err != nil {
		s.t.Fatalf("migrate %s to v%d: %v", typ, version, err)
	}
	return res.ItemSeq
}

// mtTimerEvents returns the item_seq of every event a timer wrote on the
// Item, oldest first.
func mtTimerEvents(t *testing.T, id uuid.UUID) []int64 {
	t.Helper()
	rows, err := pool.Query(serverCtx(t),
		`select item_seq from item_events where item_id = $1 and originating_hook like 'timer:%' order by item_seq`, id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var seq int64
		if err := rows.Scan(&seq); err != nil {
			t.Fatal(err)
		}
		out = append(out, seq)
	}
	return out
}

// mtOneTimer checks that exactly one timer event was written, under
// version 2, from in_progress, between min and min+mtSlack after the
// event since (an entry or the migrate), and returns its item_seq.
func mtOneTimer(t *testing.T, id uuid.UUID, typ string, since int64, min time.Duration) int64 {
	t.Helper()
	got := mtTimerEvents(t, id)
	if len(got) != 1 {
		t.Fatalf("timer events = %v, want exactly one", got)
	}
	gap, hook := timerGap(t, id, since, got[0])
	if want := "timer:" + typ + "@v2:in_progress"; hook != want {
		t.Errorf("originating_hook = %q, want %q", hook, want)
	}
	if gap < min || gap > min+mtSlack {
		t.Errorf("the timer wrote %v after event %d, want %v (up to %v late)", gap, since, min, mtSlack)
	}
	return got[0]
}

// The histories of a run that hears of a migrate and hands over, and of
// the run that reloads under the new version and fires its timer: recorded
// by TestMigrateTimers_E2E_ShorterUnderTheNewVersion (RECORD_HISTORIES=1)
// from the code that first had the reload. Do not re-record them after
// changing the workflow: then they would prove nothing.
var migrateHistories = []string{
	"mt_migrated_continues_as_new.json", // run 1: v1's timer armed, the migrated event, continue-as-new
	"mt_reloaded_timer_fires.json",      // run 2: LoadItem under v2, v2's timer from the entry, FireTimer
}

// v2's timer is shorter: it fires at v2's duration from the state entry.
func TestMigrateTimers_E2E_ShorterUnderTheNewVersion(t *testing.T) {
	s := newStack(t)
	s.mtRegister(mtSrc("mt_short", 1, "timer: {after: 20s, to: open}", ""), mtSrc("mt_short", 2, "timer: {after: 4s, to: open}", ""))
	id, entered := s.mtInProgress("mt_short", nil)
	first := s.waitRunning(id)
	s.mtMigrate(id, "mt_short", 2)

	s.waitState(id, "open", 30*time.Second)
	mtOneTimer(t, id, "mt_short", entered, 4*time.Second)
	s.waitCompleted(id)
	_, second, _, _ := runStatus(t, id)
	if second == first {
		t.Errorf("the timer fired in the run that heard of the migrate (%s); want a fresh run", first)
	}
	if os.Getenv("RECORD_HISTORIES") == "1" {
		saveRunHistory(t, id, first, migrateHistories[0])
		saveRunHistory(t, id, second, migrateHistories[1])
	}
}

// The recorded migrate histories replay on today's workflow code without
// a non-determinism error.
func TestReplay_MigrateHistories(t *testing.T) {
	for _, name := range migrateHistories {
		t.Run(name, func(t *testing.T) {
			f, err := os.Open(filepath.Join(historiesDir, name))
			if err != nil {
				t.Fatalf("open recorded history: %v", err)
			}
			defer f.Close()
			history, err := client.HistoryFromJSON(f, client.HistoryJSONOptions{})
			if err != nil {
				t.Fatalf("read history: %v", err)
			}
			replayer := worker.NewWorkflowReplayer()
			replayer.RegisterWorkflowWithOptions(wf.ItemWorkflow, workflow.RegisterOptions{Name: wf.Name})
			if err := replayer.ReplayWorkflowHistory(nil, history); err != nil {
				t.Fatalf("replay %s: %v", name, err)
			}
		})
	}
}

// v2's timer is longer: nothing at v1's time; it fires at v2's.
func TestMigrateTimers_E2E_LongerUnderTheNewVersion(t *testing.T) {
	s := newStack(t)
	s.mtRegister(mtSrc("mt_long", 1, "timer: {after: 3s, to: open}", ""), mtSrc("mt_long", 2, "timer: {after: 10s, to: open}", ""))
	id, entered := s.mtInProgress("mt_long", nil)
	s.mtMigrate(id, "mt_long", 2)

	staysFor(t, 6*time.Second, "still in_progress after v1's 3s", func() bool {
		st, _ := s.state(id)
		return st == "in_progress"
	})
	s.waitState(id, "open", 30*time.Second)
	mtOneTimer(t, id, "mt_long", entered, 10*time.Second)
	s.waitCompleted(id)
}

// v2 has no timer on in_progress (and nothing else that listens): nothing
// fires, and the run completes.
func TestMigrateTimers_E2E_NoTimerUnderTheNewVersion(t *testing.T) {
	s := newStack(t)
	s.mtRegister(mtSrc("mt_none", 1, "timer: {after: 3s, to: open}", ""), mtSrc("mt_none", 2, "", ""))
	id, _ := s.mtInProgress("mt_none", nil)
	s.mtMigrate(id, "mt_none", 2)

	staysFor(t, 3*time.Second+mtSlack, "still in_progress, no timer", func() bool {
		st, _ := s.state(id)
		return st == "in_progress" && len(mtTimerEvents(t, id)) == 0
	})
	s.waitCompleted(id)
}

// v1 has no timer, so no run is alive; v2 adds one. The migrated event
// starts a run, which arms v2's timer from the state entry.
func TestMigrateTimers_E2E_ATimerTheNewVersionAdds(t *testing.T) {
	s := newStack(t)
	s.mtRegister(mtSrc("mt_add", 1, "", ""), mtSrc("mt_add", 2, "timer: {after: 4s, to: open}", ""))
	id, entered := s.mtInProgress("mt_add", nil)
	s.mtMigrate(id, "mt_add", 2)

	s.waitState(id, "open", 30*time.Second)
	mtOneTimer(t, id, "mt_add", entered, 4*time.Second)
	s.waitCompleted(id)
}

// v2's timer is already overdue when the Item is migrated: it fires at once.
func TestMigrateTimers_E2E_AnOverdueTimerFiresAtOnce(t *testing.T) {
	s := newStack(t)
	s.mtRegister(mtSrc("mt_due", 1, "timer: {after: 1h, to: open}", ""), mtSrc("mt_due", 2, "timer: {after: 2s, to: open}", ""))
	id, _ := s.mtInProgress("mt_due", nil)
	staysFor(t, 3*time.Second, "in_progress, past v2's 2s", func() bool {
		st, _ := s.state(id)
		return st == "in_progress"
	})
	migrated := s.mtMigrate(id, "mt_due", 2)

	s.waitState(id, "open", 30*time.Second)
	mtOneTimer(t, id, "mt_due", migrated, 0)
	s.waitCompleted(id)
}

// A timer at an instant: v1 waits for f_due1 (an hour away), v2 for f_due2
// (a few seconds away). After the migrate the run reads f_due2 and fires
// at it.
func TestMigrateTimers_E2E_ATimerAtAnInstant(t *testing.T) {
	s := newStack(t)
	s.mtRegister(mtSrc("mt_at", 1, "at: {field: f_due1, to: open}", ""), mtSrc("mt_at", 2, "at: {field: f_due2, to: open}", ""))
	start := time.Now()
	due2 := start.Add(6 * time.Second).UTC().Truncate(time.Millisecond)
	id, _ := s.mtInProgress("mt_at", map[string]any{
		"f_due1": start.Add(time.Hour).UTC().Format(typedef.DateTimeLayout),
		"f_due2": due2.Format(typedef.DateTimeLayout),
	})
	migrated := s.mtMigrate(id, "mt_at", 2)

	s.waitState(id, "open", 30*time.Second)
	seq := mtOneTimer(t, id, "mt_at", migrated, 0)
	var at time.Time
	if err := pool.QueryRow(serverCtx(t), `select created_at from item_events where item_id = $1 and item_seq = $2`, id, seq).Scan(&at); err != nil {
		t.Fatal(err)
	}
	if at.Before(due2) {
		t.Errorf("the timer wrote at %v, before f_due2 (%v)", at, due2)
	}
	s.waitCompleted(id)
}

// The old timer races the migrate: the relay is stopped, so the run does
// not hear of the migrate when v1's 2s timer comes due. Its move is
// refused (the migrate moved item_seq). Once the relay is back, the run
// reloads under v2 and v2's timer fires: exactly one timer event in all.
func TestMigrateTimers_E2E_TheOldTimerRacesTheMigrate(t *testing.T) {
	s := newStack(t)
	s.mtRegister(mtSrc("mt_race", 1, "timer: {after: 2s, to: open}", ""), mtSrc("mt_race", 2, "timer: {after: 8s, to: open}", ""))
	id, entered := s.mtInProgress("mt_race", nil)
	s.stopRelay()
	s.mtMigrate(id, "mt_race", 2)

	staysFor(t, 5*time.Second, "v1's timer came due and moved nothing", func() bool {
		st, _ := s.state(id)
		return st == "in_progress" && len(mtTimerEvents(t, id)) == 0
	})
	s.startRelay()
	s.waitState(id, "open", 30*time.Second)
	mtOneTimer(t, id, "mt_race", entered, 8*time.Second)
	s.waitCompleted(id)
}

// v1's timer move is refused (its guard fails); v2 drops the guard. After
// the migrate the new run arms v2's timer again, and it moves the Item.
func TestMigrateTimers_E2E_ATimerTheOldVersionRefusedIsArmedAgain(t *testing.T) {
	s := newStack(t)
	guarded := "  - {from: in_progress, to: done, guard: 'f_ok == true'}\n"
	v1 := mtSrc("mt_again", 1, "timer: {after: 1s, to: done}", "")
	v1 = v1[:len(v1)-len("  - {from: in_progress, to: done}\n")] + guarded
	s.mtRegister(v1, mtSrc("mt_again", 2, "timer: {after: 3s, to: done}", ""))
	id, entered := s.mtInProgress("mt_again", map[string]any{"f_ok": false})
	eventually(t, 20*time.Second, "v1's timer fired and its move was refused", func() bool {
		types, _ := activityFailures(t, id)
		return len(types) > 0 && types[0] == wf.ErrTypeRejected
	})
	s.mtMigrate(id, "mt_again", 2)

	s.waitState(id, "done", 30*time.Second)
	got := mtTimerEvents(t, id)
	if len(got) != 1 {
		t.Fatalf("timer events = %v, want exactly one (v2's)", got)
	}
	if _, hook := timerGap(t, id, entered, got[0]); hook != "timer:mt_again@v2:in_progress" {
		t.Errorf("originating_hook = %q, want timer:mt_again@v2:in_progress", hook)
	}
	s.waitCompleted(id)
}
