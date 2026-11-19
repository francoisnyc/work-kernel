// End-to-end tests for Jira part 2, step 1: a timer at a field's instant on
// the real stack (item service, relay, Temporal dev server, worker).
//
//   - A lap closes itself when its end instant passes, as system:timer, and
//     its closing event is never stamped before the instant.
//   - Moving the end re-arms in the same run: nothing happens at the old
//     instant.
//   - An end already past on entry closes at once; an end set later arms.
//   - The worker's dev clock scales declared durations only: an instant is
//     not one.
//   - The item service's clock runs behind the worker's: the firing is
//     answered NotYet, and the move is made once the item service's clock
//     reaches the instant.
//   - A hold defers it, as it defers a duration timer.
package workflow_test

import (
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/temporal"

	"github.com/francoisnyc/work-kernel/internal/connstub"
	"github.com/francoisnyc/work-kernel/internal/hooks"
	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
	"github.com/francoisnyc/work-kernel/internal/typedef"
	wf "github.com/francoisnyc/work-kernel/internal/workflow"
)

// A lap is planned, then active until f_end_at, then closed. Approving an
// active lap is checked by the ERP (a hold), so a test can hold it open
// past its end.
const lapType = `
id: lap
version: 1
initial_state: planned
fields:
  - {id: f_name,        type: string}
  - {id: f_end_at,      type: datetime}
  - {id: f_closed_at,   type: datetime, writable_by: kernel}
  - {id: f_customer_id, type: string}
  - {id: f_amount,      type: int}
  - {id: f_cost_center, type: string}
states:
  - {id: planned,  category: backlog}
  - {id: active,   category: active, at: {field: f_end_at, to: closed}}
  - {id: approved, category: active}
  - {id: closed,   category: done}
transitions:
  - {from: planned, to: active}
  - {from: active,  to: planned}
  - {from: active,  to: closed, sets: {f_closed_at: 'cause.at'}}
  - {from: active,  to: approved, required: [f_customer_id, f_amount]}
hooks:
  - id: erp_check
    on: transition
    from: active
    to: approved
    kind: check
    action: erp.check_credit
    reads: [f_customer_id, f_amount]
    writes: [f_cost_center]
`

var registerLapOnce sync.Once

func registerLap(t *testing.T) {
	t.Helper()
	registerLapOnce.Do(func() {
		ty, err := typedef.Parse([]byte(lapType))
		if err != nil {
			t.Fatalf("parse lap: %v", err)
		}
		if err := itemservice.New(pool).RegisterType(serverCtx(t), ty); err != nil {
			t.Fatalf("register lap: %v", err)
		}
	})
	if _, err := itemservice.New(pool).Type(serverCtx(t), "lap", 1); err != nil {
		t.Fatalf("the lap Type is not registered: %v", err)
	}
}

// newLapStack is newStack with the lap Type, a service whose clock is
// skewed by skew, and worker options (zero: production's).
func newLapStack(t *testing.T, skew time.Duration, opts wf.WorkerOptions) *stack {
	t.Helper()
	registerLap(t)
	if _, err := pool.Exec(serverCtx(t), `update outbox set delivered_at = now() where delivered_at is null`); err != nil {
		t.Fatalf("clear outbox: %v", err)
	}
	s := &stack{t: t, queue: "items-" + uuid.NewString(),
		k: itemservice.NewWithOptions(pool, itemservice.Options{CacheTypes: true, ClockSkew: skew})}
	s.worker = wf.NewWorker(tc, s.queue, s.k, opts)
	if err := s.worker.Start(); err != nil {
		t.Fatalf("start worker: %v", err)
	}
	s.startRelay()
	t.Cleanup(func() {
		s.stopRelay()
		s.stopWorker()
	})
	return s
}

// newLap creates a planned lap ending at end (nil: no end).
func (s *stack) newLap(end *time.Time, more map[string]any) uuid.UUID {
	s.t.Helper()
	fields := map[string]any{"f_name": "lap " + uuid.NewString()[:8]}
	if end != nil {
		fields["f_end_at"] = end.UTC().Format(time.RFC3339Nano)
	}
	for k, v := range more {
		fields[k] = v
	}
	res, err := s.k.Create(serverCtx(s.t), itemservice.CreateRequest{
		IdempotencyKey: "key-" + uuid.NewString(), Principal: alice, TypeID: "lap", TypeVersion: 1, Fields: fields,
		Attribution: item.Attribution{CorrelationID: "corr-" + uuid.NewString()[:8]},
	})
	if err != nil {
		s.t.Fatalf("create lap: %v", err)
	}
	return res.ItemID
}

func (s *stack) setEnd(id uuid.UUID, end *time.Time) {
	s.t.Helper()
	var v any
	if end != nil {
		v = end.UTC().Format(time.RFC3339Nano)
	}
	if _, err := s.k.Update(serverCtx(s.t), itemservice.UpdateRequest{
		IdempotencyKey: "key-" + uuid.NewString(), Principal: alice, ItemID: id, Fields: map[string]any{"f_end_at": v},
	}); err != nil {
		s.t.Fatalf("set the end: %v", err)
	}
}

func in(d time.Duration) *time.Time {
	v := time.Now().Add(d).UTC().Truncate(time.Microsecond)
	return &v
}

// closings returns the Item's transitions into closed: who made each, and
// when its event is stamped.
type closingEvent struct {
	Seq   int64
	Actor string
	At    time.Time
}

func closings(t *testing.T, id uuid.UUID) []closingEvent {
	t.Helper()
	rows, err := pool.Query(serverCtx(t),
		`select item_seq, actor, created_at from item_events
		 where item_id = $1 and op = 'transition' and payload->>'to' = 'closed' order by item_seq`, id)
	if err != nil {
		t.Fatalf("read closings: %v", err)
	}
	defer rows.Close()
	var out []closingEvent
	for rows.Next() {
		var c closingEvent
		if err := rows.Scan(&c.Seq, &c.Actor, &c.At); err != nil {
			t.Fatalf("scan closing: %v", err)
		}
		out = append(out, c)
	}
	return out
}

// assertClosedByTheTimer: exactly one closing, by system:timer, attributed
// like a duration timer's move, stamped at or after the instant; and
// f_closed_at (cause.at) is that event's time.
func (s *stack) assertClosedByTheTimer(id uuid.UUID, end time.Time, enteredSeq int64) {
	s.t.Helper()
	cs := closings(s.t, id)
	if len(cs) != 1 || cs[0].Actor != "system:timer" {
		s.t.Fatalf("closings = %+v, want exactly one, by system:timer", cs)
	}
	if cs[0].At.Before(end) {
		s.t.Errorf("the closing event is stamped %s, before the instant %s",
			cs[0].At.Format(time.RFC3339Nano), end.Format(time.RFC3339Nano))
	}
	got := readEvent(s.t, id, cs[0].Seq)
	entered := readEvent(s.t, id, enteredSeq)
	want := timerEvent{
		Actor: "system:timer", Op: "transition", Hook: "timer:lap@v1:active",
		Causation: fmt.Sprintf("%s:%d", id, enteredSeq), Correlation: entered.Correlation, Hop: entered.Hop + 1,
	}
	if got != want {
		s.t.Errorf("the closing event = %+v\nwant %+v", got, want)
	}
	it, err := s.k.Get(serverCtx(s.t), alice, id)
	if err != nil {
		s.t.Fatalf("Get: %v", err)
	}
	if it.Fields["f_closed_at"] != cs[0].At.UTC().Format(typedef.DateTimeLayout) {
		s.t.Errorf("f_closed_at = %v, want the closing event's time %s", it.Fields["f_closed_at"],
			cs[0].At.UTC().Format(typedef.DateTimeLayout))
	}
}

// --- done when: a sprint closes itself, and moving the end re-arms ---

func TestE2E_At_ClosesItselfAtTheInstant(t *testing.T) {
	s := newLapStack(t, 0, wf.WorkerOptions{})
	end := in(2 * time.Second)
	id := s.newLap(end, nil)
	s.move(id, "active") // event 2
	staysFor(t, time.Until(*end)-300*time.Millisecond, "active until the end", s.isAt(id, "active", 2))
	s.waitState(id, "closed", 10*time.Second)
	s.assertClosedByTheTimer(id, *end, 2)
	s.waitCompleted(id)
}

func TestE2E_At_MovingTheEndRearms(t *testing.T) {
	s := newLapStack(t, 0, wf.WorkerOptions{})
	first := in(1500 * time.Millisecond)
	id := s.newLap(first, nil)
	s.move(id, "active") // event 2
	run := s.waitRunning(id)

	s.setEnd(id, in(time.Hour)) // event 3
	// Well past the first instant: nothing happens at it.
	staysFor(t, time.Until(*first)+1500*time.Millisecond, "active past the old end", s.isAt(id, "active", 3))

	second := in(1200 * time.Millisecond)
	s.setEnd(id, second) // event 4
	s.waitState(id, "closed", 10*time.Second)
	s.assertClosedByTheTimer(id, *second, 2)

	// One run did it all: re-armed, not restarted.
	if _, now, _, _ := runStatus(t, id); now != run {
		t.Errorf("the run changed from %s to %s; the same run must re-arm", run, now)
	}
	s.waitCompleted(id)
}

func TestE2E_At_PastOnEntry_ClosesAtOnce(t *testing.T) {
	s := newLapStack(t, 0, wf.WorkerOptions{})
	end := in(-time.Hour)
	id := s.newLap(end, nil)
	s.move(id, "active")
	s.waitState(id, "closed", 10*time.Second)
	s.assertClosedByTheTimer(id, *end, 2)
}

// No end on entry: a run is there all the same, waiting to hear of one.
// Setting it arms; clearing it disarms; setting it again arms.
func TestE2E_At_UnsetThenSet(t *testing.T) {
	s := newLapStack(t, 0, wf.WorkerOptions{})
	id := s.newLap(nil, nil)
	s.move(id, "active") // event 2
	s.waitRunning(id)
	staysFor(t, time.Second, "active with no end", s.isAt(id, "active", 2))

	s.setEnd(id, in(1500*time.Millisecond)) // event 3
	s.setEnd(id, nil)                       // event 4: cleared before it is due
	staysFor(t, 2500*time.Millisecond, "active once the end is cleared", s.isAt(id, "active", 4))
	if status, _, _, _ := runStatus(t, id); status != enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING {
		t.Errorf("the run is %v, want it still running", status)
	}

	end := in(time.Second)
	s.setEnd(id, end) // event 5
	s.waitState(id, "closed", 10*time.Second)
	s.assertClosedByTheTimer(id, *end, 2)
}

// Left by hand before the end: the timer is gone and the run completes.
func TestE2E_At_LeftByHand_NothingFires(t *testing.T) {
	s := newLapStack(t, 0, wf.WorkerOptions{})
	end := in(1500 * time.Millisecond)
	id := s.newLap(end, nil)
	s.move(id, "active")
	s.waitRunning(id)
	s.move(id, "planned") // event 3
	s.waitCompleted(id)
	staysFor(t, time.Until(*end)+time.Second, "planned past the end", s.isAt(id, "planned", 3))
}

// The worker is down when the instant passes: it fires when the worker is
// back, as a duration timer does.
func TestE2E_At_WorkerDownPastTheInstant(t *testing.T) {
	s := newLapStack(t, 0, wf.WorkerOptions{})
	end := in(1500 * time.Millisecond)
	id := s.newLap(end, nil)
	s.move(id, "active")
	s.waitRunning(id)
	s.waitDelivered(id)
	s.stopWorker()
	staysFor(t, time.Until(*end)+time.Second, "active while the worker is down", s.isAt(id, "active", 2))
	s.startWorker()
	s.waitState(id, "closed", 15*time.Second)
	s.assertClosedByTheTimer(id, *end, 2)
}

// --- the dev clock ---

// A worker on a scaled clock for lap (a day lasts one second) scales
// nothing here: an instant two seconds away is two seconds away.
func TestE2E_At_TheDevClockDoesNotScaleInstants(t *testing.T) {
	s := newLapStack(t, 0, wf.WorkerOptions{DevDay: time.Second, DevTypes: []string{"lap"}})
	end := in(2500 * time.Millisecond)
	id := s.newLap(end, nil)
	s.move(id, "active")
	staysFor(t, time.Until(*end)-400*time.Millisecond, "active until the real instant", s.isAt(id, "active", 2))
	s.waitState(id, "closed", 10*time.Second)
	s.assertClosedByTheTimer(id, *end, 2)
}

// --- not yet ---

// The item service's clock runs 800 ms behind the worker's. Temporal fires
// at the instant by its own clock; the item service answers NotYet, the
// workflow arms again for the remainder, and the move is made when the
// item service's clock reaches the instant. The closing event, stamped by
// that clock, is not before the instant. One closing, no retry storm.
func TestE2E_At_ServiceClockBehind_NeverClosesEarly(t *testing.T) {
	const skew = -800 * time.Millisecond
	s := newLapStack(t, skew, wf.WorkerOptions{})
	end := in(time.Second) // by the worker's clock; for the service it is 1.8 s away
	id := s.newLap(end, nil)
	s.move(id, "active")
	// At the instant by real time, plus a little: still active.
	staysFor(t, time.Until(*end)+300*time.Millisecond, "active at the worker's instant", s.isAt(id, "active", 2))
	s.waitState(id, "closed", 10*time.Second)
	s.assertClosedByTheTimer(id, *end, 2)

	types, allFinal := activityFailures(t, id)
	notYet := 0
	for _, ty := range types {
		if ty == wf.ErrTypeNotYet {
			notYet++
		}
	}
	if notYet == 0 || notYet > 3 || !allFinal {
		t.Errorf("activity failures = %v (all non-retryable: %v); want one to three NotYet, each final", types, allFinal)
	}
}

// --- the activities ---

// LoadItem gives the workflow the Type's at-timers as a table (state ->
// field ID and target state), unscaled; LoadInstant gives it the instant.
// FireTimer turns ErrNotYet into a non-retryable NotYet carrying the
// remainder.
func TestActivities_AtTimer(t *testing.T) {
	registerLap(t)
	k := itemservice.New(pool)
	a := &wf.Activities{Items: k, DevDay: time.Second, DevTypes: []string{"lap"}}
	s := &stack{t: t, k: k}
	end := in(time.Hour)
	id := s.newLap(end, nil)
	s.move(id, "active")

	snap, err := a.LoadItem(serverCtx(t), id.String())
	if err != nil {
		t.Fatalf("LoadItem: %v", err)
	}
	if len(snap.At) != 1 || snap.At["active"] != (wf.AtSpec{Field: "f_end_at", To: "closed"}) {
		t.Errorf("Snapshot.At = %+v, want active -> {f_end_at closed}", snap.At)
	}
	if len(snap.Timers) != 0 {
		t.Errorf("Snapshot.Timers = %+v, want none: an at-timer is not a duration timer", snap.Timers)
	}

	got, err := a.LoadInstant(serverCtx(t), id.String())
	if err != nil || !got.Set || !got.At.Equal(*end) {
		t.Errorf("LoadInstant = %+v, %v; want set, %v", got, err, *end)
	}
	_, err = a.LoadInstant(serverCtx(t), uuid.NewString())
	var app *temporal.ApplicationError
	if !errors.As(err, &app) || app.Type() != wf.ErrTypeNotFound || !app.NonRetryable() {
		t.Errorf("LoadInstant of no Item = %v, want a non-retryable NotFound", err)
	}

	err = a.FireTimer(serverCtx(t), wf.FireTimerInput{
		ItemID: id.String(), TypeID: "lap", TypeVersion: 1, FromState: "active", ToState: "closed",
		ExpectedItemSeq: 2, EnteredSeq: 2, Hop: 1, IdempotencyKey: "timer:" + id.String() + ":2:2",
	})
	if !errors.As(err, &app) || app.Type() != wf.ErrTypeNotYet || !app.NonRetryable() {
		t.Fatalf("FireTimer an hour early = %v, want a non-retryable NotYet", err)
	}
	var remaining time.Duration
	if err := app.Details(&remaining); err != nil || remaining <= 59*time.Minute || remaining > time.Hour {
		t.Errorf("NotYet details = %v, %v; want the remainder, just under an hour", remaining, err)
	}
	if state, seq := s.state(id); state != "active" || seq != 2 {
		t.Errorf("after NotYet the lap is %s at %d, want active at 2", state, seq)
	}
}

// --- a hold defers it ---

// The lap's approval is held open by a slow ERP check, past its end. The
// check denies (over the credit limit): the lap is still active, so the
// timer fires once the hold closes, after the denial.
func TestE2E_At_DueDuringAHold_FiresAfterTheDenial(t *testing.T) {
	registerLap(t)
	h := newHookStack(t)
	h.erp.SetCustomer(h.customer, connstub.Customer{CreditLimit: 50, Latency: 2500 * time.Millisecond})
	h.stopWorker()
	h.worker = wf.NewWorker(tc, h.queue, h.k, wf.WorkerOptions{
		Connectors: hookConnectors(h), HookBudget: 10 * time.Second,
	})
	if err := h.worker.Start(); err != nil {
		t.Fatalf("start worker: %v", err)
	}

	end := in(time.Second)
	id := h.newLap(end, map[string]any{"f_customer_id": h.customer, "f_amount": 100})
	h.move(id, "active") // event 2
	res, err := h.k.Transition(serverCtx(t), itemservice.TransitionRequest{
		IdempotencyKey: "key-" + uuid.NewString(), Principal: alice, ItemID: id, ToState: "approved",
	})
	if err != nil || res.Outcome != itemservice.OutcomePending {
		t.Fatalf("approve = %+v, %v; want held by the ERP check", res, err)
	}
	h.waitState(id, "closed", 15*time.Second)

	moves, denied := timerMoves(t, id)
	if denied == 0 || len(moves) != 1 || moves[0] < denied {
		t.Errorf("denial at %d, timer moves at %v; want one move, after the denial", denied, moves)
	}
	var seqs []int64
	for _, c := range closings(t, id) {
		seqs = append(seqs, c.Seq)
	}
	if !slices.Equal(seqs, moves) {
		t.Errorf("closings at %v, timer moves at %v; want the same one event", seqs, moves)
	}
}

// hookConnectors are the stack's stub connectors, as its worker gets them.
func hookConnectors(h *hookStack) *hooks.Connectors { return hooks.NewConnectors(h.crm.URL, h.erp.URL) }
