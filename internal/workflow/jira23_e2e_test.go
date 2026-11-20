// End-to-end tests for Jira part 2, step 3a: Postgres, the relay, a real
// Temporal server and worker.
//
// Test Types again (nothing here is named after a goal): a job, in a yard
// that has hours, contains meters.
//
//   - Created, the job starts a main and a reply meter; the main meter
//     shows its due instant on the job. All from creation events.
//   - At the due instant, never before, the meter marks itself late with
//     that instant, and goes on running; stopped later, it is missed.
//   - Waiting pauses the main meter and working resumes it; done stops
//     both. Another person's public remark stops the reply meter.
//   - A worker stopped for seconds delays all of it and changes none of
//     the numbers: elapsed time, due instant and late mark come from the
//     causes' times.
//   - An acting timer at an instant runs its hook after its write.
//
// The hours are around the clock in UTC and budgets are a few seconds.
package workflow_test

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/connstub"
	"github.com/francoisnyc/work-kernel/internal/hooks"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
	"github.com/francoisnyc/work-kernel/internal/typedef"
	wf "github.com/francoisnyc/work-kernel/internal/workflow"
)

const wf3Types = `
id: wf3hours
version: 1
fields:
  - {id: f_week,     type: string, multiple: true, max: 21}
  - {id: f_days_off, type: date,   multiple: true, max: 366}
  - {id: f_zone,     type: string}
readable_by_reactions: [f_week, f_days_off, f_zone]
invariants:
  - {id: hours_invalid, rule: 'calendar(f_week, f_days_off, f_zone) != null'}
---
id: wf3yard
version: 1
fields:
  - {id: f_name, type: string}
links:
  - {name: hours, inverse: yards, targets: [wf3hours], max: one}
---
id: wf3remark
version: 1
fields:
  - {id: f_public, type: bool}
---
id: wf3job
version: 1
initial_state: open
fields:
  - {id: f_title,    type: string}
  - {id: f_budget_s, type: int}
  - {id: f_due,      type: datetime, writable_by: kernel}
states:
  - {id: open,    category: todo}
  - {id: working, category: active}
  - {id: waiting, category: waiting}
  - {id: done,    category: done}
transitions:
  - {from: open,    to: working}
  - {from: working, to: waiting}
  - {from: waiting, to: working}
  - {from: working, to: done}
  - {from: done,    to: working}
reactions:
  - id: start
    kind: create
    on: enter
    state: open
    type: wf3meter
    version: 1
    fields: &main
      f_kind: '"main"'
      f_target_s: 'f_budget_s'
      f_started_at: 'cause.at'
      f_week: 'container.linked.hours.f_week'
      f_days_off: 'container.linked.hours.f_days_off'
      f_zone: 'container.linked.hours.f_zone'
      f_due_at: 'working_after(cause.at, f_budget_s, JOBCAL)'
  - id: start_reply
    kind: create
    on: enter
    state: open
    type: wf3meter
    version: 1
    fields:
      f_kind: '"reply"'
      f_target_s: '3600'
      f_started_at: 'cause.at'
      f_week: 'container.linked.hours.f_week'
      f_days_off: 'container.linked.hours.f_days_off'
      f_zone: 'container.linked.hours.f_zone'
      f_due_at: 'working_after(cause.at, 3600, JOBCAL)'
  - {id: restart, kind: create, on: enter, state: working, from: [done], type: wf3meter, version: 1, fields: *main}
  - {id: pause,  kind: move, on: enter, state: waiting, children: {of: [wf3meter], when: 'child.f_kind == "main"'}, to: paused, target_from: [running]}
  - {id: resume, kind: move, on: enter, state: working, from: [waiting], children: {of: [wf3meter], when: 'child.f_kind == "main"'}, to: running, target_from: [paused]}
  - {id: stop,   kind: move, on: enter, state: done, children: {of: [wf3meter]}, to: stopped, target_from: [running, paused]}
  - {id: answered, kind: move, on: created_inside, of: [wf3remark], when: 'child.f_public == true', by: not_creator,
     children: {of: [wf3meter], when: 'child.f_kind == "reply"'}, to: stopped, target_from: [running]}
accepts_reactions:
  wf3meter:show:  {sets: [f_due]}
  wf3meter:hide:  {sets: [f_due]}
  wf3meter:clear: {sets: [f_due]}
---
id: wf3meter
version: 1
initial_state: running
fields:
  - {id: f_kind,       type: enum, options: [main, reply]}
  - {id: f_target_s,   type: int,      writable_by: kernel}
  - {id: f_elapsed_s,  type: int,      writable_by: kernel}
  - {id: f_started_at, type: datetime, writable_by: kernel}
  - {id: f_due_at,     type: datetime, writable_by: kernel}
  - {id: f_late_at,    type: datetime, writable_by: kernel}
  - {id: f_outcome,    type: enum, options: [met, missed], writable_by: kernel}
  - {id: f_week,       type: string, multiple: true, max: 21,  writable_by: kernel}
  - {id: f_days_off,   type: date,   multiple: true, max: 366, writable_by: kernel}
  - {id: f_zone,       type: string, writable_by: kernel}
states:
  - id: running
    category: active
    acting:
      - {id: late, at: f_due_at, when: 'f_late_at == null', sets: {f_late_at: 'cause.at'}}
  - {id: paused,  category: waiting}
  - {id: stopped, category: done}
transitions:
  - from: running
    to: paused
    sets:
      f_elapsed_s: '(f_elapsed_s == null ? 0 : f_elapsed_s) + working_between(f_started_at, cause.at, CAL)'
      f_late_at: 'f_due_at == null || cause.at >= f_due_at ? f_late_at : null'
      f_due_at: 'null'
  - from: paused
    to: running
    sets:
      f_started_at: 'cause.at'
      f_due_at: 'f_late_at != null ? f_late_at : working_after(cause.at, f_target_s - f_elapsed_s, CAL)'
  - from: running
    to: stopped
    sets:
      f_elapsed_s: '(f_elapsed_s == null ? 0 : f_elapsed_s) + working_between(f_started_at, cause.at, CAL)'
      f_outcome: '(f_elapsed_s == null ? 0 : f_elapsed_s) + working_between(f_started_at, cause.at, CAL) <= f_target_s ? "met" : "missed"'
      f_late_at: 'f_due_at == null || cause.at >= f_due_at ? f_late_at : null'
      f_due_at: 'null'
  - from: paused
    to: stopped
    sets:
      f_outcome: 'f_elapsed_s <= f_target_s ? "met" : "missed"'
reactions:
  - {id: show,  kind: set, on: enter, state: running, when: 'f_kind == "main"', target: container, set: {f_due: 'f_due_at'}}
  - {id: hide,  kind: set, on: enter, state: paused,  when: 'f_kind == "main"', target: container, set: {f_due: 'null'}}
  - {id: clear, kind: set, on: enter, state: stopped, when: 'f_kind == "main"', target: container, set: {f_due: 'null'}}
accepts_reactions:
  '*:start':       {creates: [f_kind, f_target_s, f_started_at, f_due_at, f_week, f_days_off, f_zone]}
  '*:start_reply': {creates: [f_kind, f_target_s, f_started_at, f_due_at, f_week, f_days_off, f_zone]}
  '*:restart':     {creates: [f_kind, f_target_s, f_started_at, f_due_at, f_week, f_days_off, f_zone]}
  '*:pause':    {moves: [paused]}
  '*:resume':   {moves: [running]}
  '*:stop':     {moves: [stopped]}
  '*:answered': {moves: [stopped]}
---
id: wf3alarm
version: 1
initial_state: set
fields:
  - {id: f_at,      type: datetime}
  - {id: f_rang_at, type: datetime, writable_by: kernel}
states:
  - id: set
    category: active
    acting:
      - {id: ring, at: f_at, sets: {f_rang_at: 'cause.at'}, hook: tell}
  - {id: "off", category: done}
transitions:
  - {from: set, to: "off"}
hooks:
  - {id: tell, on: timer, kind: action, action: email.notify, reads: [creator]}
`

var registerWF3Once sync.Once

type jobStack struct {
	*stack
	yard uuid.UUID
}

// newJobStack is newStack with the step 3 Types registered, and a yard
// linked to hours that are open around the clock.
func newJobStack(t *testing.T) *jobStack {
	t.Helper()
	registerWF3Once.Do(func() {
		src := strings.ReplaceAll(wf3Types, "JOBCAL", "calendar(container.linked.hours.f_week, container.linked.hours.f_days_off, container.linked.hours.f_zone)")
		src = strings.ReplaceAll(src, "CAL", "calendar(f_week, f_days_off, f_zone)")
		for _, doc := range splitDocs(src) {
			ty, err := typedef.Parse([]byte(doc))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if err := itemservice.New(pool).RegisterType(serverCtx(t), ty); err != nil {
				t.Fatalf("register %s: %v", ty.ID, err)
			}
		}
	})
	if _, err := itemservice.New(pool).Type(serverCtx(t), "wf3job", 1); err != nil {
		t.Fatalf("the job Type is not registered: %v", err)
	}
	s := &jobStack{stack: newStack(t)}
	hours := s.makeIn(alice, "wf3hours", nil, map[string]any{"f_zone": "UTC", "f_week": []any{
		"mon 00:00-24:00", "tue 00:00-24:00", "wed 00:00-24:00", "thu 00:00-24:00", "fri 00:00-24:00", "sat 00:00-24:00", "sun 00:00-24:00"}})
	s.yard = s.makeIn(alice, "wf3yard", nil, map[string]any{"f_name": "yard"})
	if _, err := s.k.Link(serverCtx(t), itemservice.LinkRequest{
		IdempotencyKey: "key-" + uuid.NewString(), Principal: alice, SourceID: s.yard, LinkType: "hours", TargetID: hours,
	}); err != nil {
		t.Fatalf("link the yard to its hours: %v", err)
	}
	return s
}

func (s *jobStack) job(budget int) uuid.UUID {
	s.t.Helper()
	return s.makeIn(alice, "wf3job", &s.yard, map[string]any{"f_title": "gate", "f_budget_s": budget})
}

// meter finds the job's newest meter of a kind; uuid.Nil if none yet.
func meter(t *testing.T, job uuid.UUID, kind string) uuid.UUID {
	t.Helper()
	rows, err := pool.Query(serverCtx(t),
		`select id from items where container_id = $1 and type_id = 'wf3meter' and fields->>'f_kind' = $2 order by created_at desc, id desc limit 1`, job, kind)
	if err != nil {
		t.Fatalf("find the meter: %v", err)
	}
	defer rows.Close()
	var id uuid.UUID
	if rows.Next() {
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan: %v", err)
		}
	}
	return id
}

// started is a job whose two meters exist and whose due instant shows.
func (s *jobStack) started(budget int) (job, main, reply uuid.UUID) {
	s.t.Helper()
	job = s.job(budget)
	eventually(s.t, 20*time.Second, "both meters created and the due instant shown on the job", func() bool {
		main, reply = meter(s.t, job, "main"), meter(s.t, job, "reply")
		return main != uuid.Nil && reply != uuid.Nil && s.field(job, "f_due") != nil
	})
	return job, main, reply
}

func dt(at time.Time) string { return at.UTC().Format(typedef.DateTimeLayout) }

func (s *jobStack) seconds(id uuid.UUID, field string) int64 {
	s.t.Helper()
	n, ok := typedef.AsInt(s.field(id, field))
	if !ok {
		s.t.Fatalf("%s of %s = %v, not an int", field, id, s.field(id, field))
	}
	return n
}

func (s *jobStack) stateIs(id uuid.UUID, want string) func() bool {
	return func() bool { st, _ := s.state(id); return st == want }
}

// --- done when ---

// From nothing but the job's creation: two meters, on the yard's hours,
// timed from the create event; and the main meter's due instant on the job.
func TestE2E_Step3_AJobStartsItsMetersWhenCreated(t *testing.T) {
	s := newJobStack(t)
	job, main, reply := s.started(600)
	created := eventAt(t, job, 1)

	if got := s.field(main, "f_due_at"); got != dt(created.Add(600*time.Second)) || s.field(main, "f_started_at") != dt(created) {
		t.Errorf("the main meter: f_started_at %v, f_due_at %v; want the job's create time %s and ten minutes later",
			s.field(main, "f_started_at"), got, dt(created))
	}
	if got := s.field(reply, "f_due_at"); got != dt(created.Add(time.Hour)) {
		t.Errorf("the reply meter's f_due_at = %v, want an hour after the job's creation", got)
	}
	if s.field(main, "f_zone") != "UTC" {
		t.Errorf("the meter's copy of the hours: zone %v", s.field(main, "f_zone"))
	}
	// The copy on the job: the main meter's due instant, written by the
	// meter's reaction, two hops from the job's create.
	if got := s.field(job, "f_due"); got != dt(created.Add(600*time.Second)) {
		t.Errorf("the job's f_due = %v, want the main meter's due instant", got)
	}
	eventually(t, 15*time.Second, "the job's three events after its create", func() bool { _, seq := s.state(job); return seq == 4 })
	var sets int
	for seq := int64(2); seq <= 4; seq++ {
		if ev := readEvent(t, job, seq); ev.Op == "update" {
			sets++
			if ev.Actor != "reaction:wf3meter:show" || ev.Causation != fmt.Sprintf("%s:1", main) || ev.Hop != 2 {
				t.Errorf("the write of f_due = %+v, want the meter's show, caused by its create, at hop 2", ev)
			}
		}
	}
	want := []string{"reaction_applied wf3job:start", "reaction_applied wf3job:start_reply"}
	if got := records(t, job); sets != 1 || fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("the job: %d writes of f_due and records %v; want 1 and %v", sets, got, want)
	}
	// The reply meter shows nothing (its condition), and says so once.
	if got := records(t, reply); fmt.Sprint(got) != "[reaction_skipped wf3meter:show condition_false]" {
		t.Errorf("the reply meter's records = %v", got)
	}
}

// Late at the due instant, never before; the meter goes on; stopped after
// it, it is missed.
func TestE2E_Step3_LateAtTheDueInstant_ThenMissed(t *testing.T) {
	s := newJobStack(t)
	job, main, _ := s.started(3)
	due := eventAt(t, job, 1).Add(3 * time.Second)
	if time.Now().Before(due.Add(-time.Second)) {
		if got := s.field(main, "f_late_at"); got != nil {
			t.Fatalf("f_late_at = %v more than a second before the due instant", got)
		}
	}
	eventually(t, 20*time.Second, "the meter marked late", func() bool { return s.field(main, "f_late_at") != nil })
	if got := s.field(main, "f_late_at"); got != dt(due) {
		t.Errorf("f_late_at = %v, want the due instant %s exactly", got, dt(due))
	}
	if st, _ := s.state(main); st != "running" {
		t.Errorf("the meter is %s, want running: late is not an end", st)
	}
	// The write: a field update by system:timer, stamped at or after the
	// instant, never before.
	_, seq := s.state(main)
	var late timerEvent
	for i := int64(2); i <= seq; i++ {
		if ev := readEvent(t, main, i); ev.Actor == "system:timer" {
			late = ev
			if at := eventAt(t, main, i); at.Before(due) {
				t.Errorf("the late mark was written at %v, before the due instant %v", at, due)
			}
		}
	}
	if late.Op != "update" || late.Causation != fmt.Sprintf("%s:1", main) {
		t.Errorf("the timer's event = %+v, want an update caused by the meter's entry", late)
	}

	s.move(job, "working")
	s.move(job, "done")
	eventually(t, 20*time.Second, "the main meter stopped", s.stateIs(main, "stopped"))
	if got := s.field(main, "f_outcome"); got != "missed" || s.field(main, "f_late_at") != dt(due) {
		t.Errorf("stopped after the due instant: f_outcome %v, f_late_at %v; want missed, and the mark kept", got, s.field(main, "f_late_at"))
	}
	eventually(t, 15*time.Second, "the job's f_due cleared", func() bool { return s.field(job, "f_due") == nil })
}

// Waiting pauses the main meter, working resumes it, done stops it: met.
// The job's copy of the due instant follows, a step behind.
func TestE2E_Step3_PauseResumeAndMet(t *testing.T) {
	s := newJobStack(t)
	job, main, reply := s.started(600)
	s.move(job, "working")
	waiting := s.move(job, "waiting")
	eventually(t, 20*time.Second, "the main meter paused", s.stateIs(main, "paused"))
	eventually(t, 15*time.Second, "the job's f_due cleared", func() bool { return s.field(job, "f_due") == nil })
	if st, _ := s.state(reply); st != "running" {
		t.Errorf("the reply meter is %s, want running: only the main one pauses", st)
	}
	elapsed := s.seconds(main, "f_elapsed_s")
	if want := int64(eventAt(t, job, waiting.ItemSeq).Sub(eventAt(t, job, 1)) / time.Second); elapsed != want {
		t.Errorf("f_elapsed_s = %d, want %d", elapsed, want)
	}

	back := s.move(job, "working")
	eventually(t, 20*time.Second, "the main meter running again", s.stateIs(main, "running"))
	due := dt(eventAt(t, job, back.ItemSeq).Add(time.Duration(600-elapsed) * time.Second))
	if got := s.field(main, "f_due_at"); got != due {
		t.Errorf("f_due_at after the resume = %v, want %s", got, due)
	}
	eventually(t, 15*time.Second, "the job's f_due shown again", func() bool { return s.field(job, "f_due") == due })

	s.move(job, "done")
	eventually(t, 20*time.Second, "both meters stopped", func() bool { return s.stateIs(main, "stopped")() && s.stateIs(reply, "stopped")() })
	if s.field(main, "f_outcome") != "met" || s.field(main, "f_late_at") != nil || s.field(reply, "f_outcome") != "met" {
		t.Errorf("main: %v, late %v; reply: %v; want met, never late, met",
			s.field(main, "f_outcome"), s.field(main, "f_late_at"), s.field(reply, "f_outcome"))
	}
	eventually(t, 15*time.Second, "the job's f_due cleared at the end", func() bool { return s.field(job, "f_due") == nil })
}

// The worker is down while the job waits and while it works again: the
// meter hears of both seconds late, and computes what it would have.
func TestE2E_Step3_AStoppedWorker_ComputesTheSameElapsedAndDue(t *testing.T) {
	s := newJobStack(t)
	job, main, _ := s.started(600)
	s.move(job, "working")
	eventually(t, 15*time.Second, "the job's run settled", func() bool { _, seq := s.state(job); return seq == 5 })

	s.stopWorker()
	waiting := s.move(job, "waiting")
	staysFor(t, 2*time.Second, "running while the worker is down", s.stateIs(main, "running"))
	s.startWorker()
	eventually(t, 30*time.Second, "the main meter paused", s.stateIs(main, "paused"))
	elapsed := s.seconds(main, "f_elapsed_s")
	want := int64(eventAt(t, job, waiting.ItemSeq).Sub(eventAt(t, job, 1)) / time.Second)
	if elapsed != want {
		t.Errorf("f_elapsed_s = %d, want %d: up to the event that entered waiting, not to when the worker came back", elapsed, want)
	}
	_, pausedSeq := s.state(main)
	if d := eventAt(t, main, pausedSeq).Sub(eventAt(t, job, waiting.ItemSeq)); d < 2*time.Second {
		t.Errorf("the meter's pause is stamped %v after the job's event; want at least the 2 s the worker was down", d)
	}

	s.stopWorker()
	back := s.move(job, "working")
	staysFor(t, 2*time.Second, "paused while the worker is down", s.stateIs(main, "paused"))
	s.startWorker()
	eventually(t, 30*time.Second, "the main meter running again", s.stateIs(main, "running"))
	due := dt(eventAt(t, job, back.ItemSeq).Add(time.Duration(600-elapsed) * time.Second))
	if got := s.field(main, "f_due_at"); got != due {
		t.Errorf("f_due_at = %v, want %s: counted from the event that resumed, not from when the worker came back", got, due)
	}
	eventually(t, 15*time.Second, "the same instant on the job", func() bool { return s.field(job, "f_due") == due })
}

// The worker is down across the due instant: the late mark, written
// seconds after it, is the instant itself.
func TestE2E_Step3_LateIsTheInstant_EvenWrittenLate(t *testing.T) {
	s := newJobStack(t)
	job, main, _ := s.started(2)
	due := eventAt(t, job, 1).Add(2 * time.Second)
	s.stopWorker()
	time.Sleep(time.Until(due.Add(2 * time.Second)))
	if got := s.field(main, "f_late_at"); got != nil && got != dt(due) {
		t.Fatalf("f_late_at = %v, want unset or the due instant", got)
	}
	s.startWorker()
	eventually(t, 30*time.Second, "the meter marked late", func() bool { return s.field(main, "f_late_at") != nil })
	if got := s.field(main, "f_late_at"); got != dt(due) {
		t.Errorf("f_late_at = %v, want the due instant %s, not the time the worker came back", got, dt(due))
	}
}

// The job waits before the due instant, but the worker is down until after
// it. Whichever comes first when it is back, the timer or the pause, the
// meter ends paused, not late, with the time that had really elapsed.
func TestE2E_Step3_APauseHeardAfterTheDueInstant_IsNotLate(t *testing.T) {
	s := newJobStack(t)
	job, main, _ := s.started(3)
	due := eventAt(t, job, 1).Add(3 * time.Second)
	s.move(job, "working")
	s.stopWorker()
	waiting := s.move(job, "waiting")
	if at := eventAt(t, job, waiting.ItemSeq); !at.Before(due) {
		t.Skipf("the job waited at %v, after the due instant %v: the machine is too slow for this test", at, due)
	}
	time.Sleep(time.Until(due.Add(2 * time.Second)))
	s.startWorker()
	eventually(t, 30*time.Second, "the main meter paused", s.stateIs(main, "paused"))
	// Give a timer that lost the race the time to show itself.
	staysFor(t, 2*time.Second, "paused and not late", func() bool {
		return s.stateIs(main, "paused")() && s.field(main, "f_late_at") == nil
	})
	if got := s.seconds(main, "f_elapsed_s"); got > 2 {
		t.Errorf("f_elapsed_s = %d, want what had elapsed when the job waited (under 3 s)", got)
	}
}

// Another person's public remark stops the reply meter, from cold; the
// requester's own does not.
func TestE2E_Step3_AnotherPersonsRemarkStopsTheReplyMeter(t *testing.T) {
	s := newJobStack(t)
	job, main, reply := s.started(600)
	eventually(t, 15*time.Second, "the job's run settled", func() bool { _, seq := s.state(job); return seq == 4 })
	s.waitCompleted(job)

	mine := s.makeIn(alice, "wf3remark", &job, map[string]any{"f_public": true})
	eventually(t, 20*time.Second, "the requester's own remark recorded as skipped", func() bool { _, seq := s.state(job); return seq == 5 })
	if st, _ := s.state(reply); st != "running" {
		t.Fatalf("the reply meter is %s after the requester's own remark, want running", st)
	}
	theirs := s.makeIn(bea, "wf3remark", &job, map[string]any{"f_public": true})
	eventually(t, 20*time.Second, "the reply meter stopped", s.stateIs(reply, "stopped"))
	if st, _ := s.state(main); st != "running" {
		t.Errorf("the main meter is %s, want running", st)
	}
	if st, _ := s.state(job); st != "open" {
		t.Errorf("the job is %s, want open: the remark moves the meter, not the job", st)
	}
	// Stopped at the remark's time: the meter's elapsed time is up to it.
	if got, want := s.seconds(reply, "f_elapsed_s"), int64(eventAt(t, theirs, 1).Sub(eventAt(t, job, 1))/time.Second); got != want {
		t.Errorf("the reply meter's f_elapsed_s = %d, want %d (up to the remark's create)", got, want)
	}
	eventually(t, 15*time.Second, "both remarks recorded", func() bool { return len(records(t, job)) == 4 })
	got := records(t, job)[2:]
	want := []string{"reaction_skipped wf3job:answered not_by", "reaction_applied wf3job:answered"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("the job's records for the remarks = %v, want %v (%s then %s)", got, want, mine, theirs)
	}
}

// Work taken up again after done starts a new main meter; the old one
// stays as it ended, and the job shows the new due instant.
func TestE2E_Step3_ANewCycleStartsANewMeter(t *testing.T) {
	s := newJobStack(t)
	job, first, _ := s.started(600)
	s.move(job, "working")
	s.move(job, "done")
	eventually(t, 20*time.Second, "the first meter stopped", s.stateIs(first, "stopped"))
	again := s.move(job, "working")
	var second uuid.UUID
	eventually(t, 20*time.Second, "a second main meter", func() bool { second = meter(t, job, "main"); return second != first })
	if st, _ := s.state(second); st != "running" {
		t.Errorf("the new meter is %s, want running", st)
	}
	due := dt(eventAt(t, job, again.ItemSeq).Add(600 * time.Second))
	if got := s.field(second, "f_due_at"); got != due {
		t.Errorf("the new meter's f_due_at = %v, want %s (from the event that took the work up again)", got, due)
	}
	eventually(t, 15*time.Second, "the job shows the new due instant", func() bool { return s.field(job, "f_due") == due })
	if st, _ := s.state(first); st != "stopped" || s.field(first, "f_outcome") != "met" {
		t.Errorf("the first meter is %s (%v), want stopped and met, as it ended", st, s.field(first, "f_outcome"))
	}
	if n := children(t, job, "wf3meter"); n != 3 {
		t.Errorf("%d meters in the job, want 3: two main, one reply", n)
	}
}

// An acting timer at an instant with a hook: the write, then the email,
// once per instant.
func TestE2E_Step3_ActingAt_RunsItsHookAfterItsWrite(t *testing.T) {
	s := newJobStack(t)
	mail := connstub.NewEmail()
	t.Cleanup(mail.Close)
	s.stopWorker()
	s.worker = wf.NewWorker(tc, s.queue, s.k, wf.WorkerOptions{
		Connectors: hooks.NewConnectors("http://127.0.0.1:1", "http://127.0.0.1:1").WithEmail(mail.URL), HookBudget: testBudget,
	})
	if err := s.worker.Start(); err != nil {
		t.Fatalf("start worker: %v", err)
	}
	sent := func(id uuid.UUID) []connstub.Message { return mail.Messages(id.String()) }

	at := time.Now().Add(1500 * time.Millisecond).UTC().Truncate(time.Microsecond)
	alarm := s.makeIn(alice, "wf3alarm", nil, map[string]any{"f_at": at.Format(time.RFC3339Nano)})
	staysFor(t, time.Second, "nothing before the instant", func() bool { return s.field(alarm, "f_rang_at") == nil && len(sent(alarm)) == 0 })
	eventually(t, 20*time.Second, "the alarm rang and told", func() bool { return s.field(alarm, "f_rang_at") != nil && len(sent(alarm)) == 1 })
	if got := s.field(alarm, "f_rang_at"); got != dt(at) {
		t.Errorf("f_rang_at = %v, want the instant %s", got, dt(at))
	}
	if msg := sent(alarm)[0]; msg.Template != "tell" || msg.To != alice.Actor {
		t.Errorf("the email = %+v, want template tell to %s", msg, alice.Actor)
	}
	if st, _ := s.state(alarm); st != "set" {
		t.Errorf("the alarm is %s, want set: the timer makes no move", st)
	}
	// The hook ran: recorded on the Item, naming the timer.
	eventually(t, 15*time.Second, "hook_ran recorded", func() bool {
		_, seq := s.state(alarm)
		return readEvent(t, alarm, seq).Op == "hook_ran"
	})

	// Moved to a new instant: it rings again, and tells again. Once each.
	next := time.Now().Add(1500 * time.Millisecond).UTC().Truncate(time.Microsecond)
	if _, err := s.k.Update(serverCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: "key-" + uuid.NewString(), Principal: alice, ItemID: alarm, Fields: map[string]any{"f_at": next.Format(time.RFC3339Nano)},
	}); err != nil {
		t.Fatalf("move the alarm: %v", err)
	}
	eventually(t, 20*time.Second, "the alarm rang again", func() bool { return s.field(alarm, "f_rang_at") == dt(next) && len(sent(alarm)) == 2 })
	staysFor(t, 2*time.Second, "two emails, no more", func() bool { return len(sent(alarm)) == 2 })
}
