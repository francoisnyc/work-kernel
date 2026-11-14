// Tests for the outbox relay, against a real Postgres and a fake in place of
// Temporal. They cover what the relay decides: the order of delivery, start
// versus plain update, and what to do with each answer. How those calls
// behave on a real Temporal server is tested in internal/workflow.
package relay_test

import (
	"context"
	"errors"
	"log"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
	"github.com/francoisnyc/work-kernel/internal/relay"
	"github.com/francoisnyc/work-kernel/internal/testdb"
	"github.com/francoisnyc/work-kernel/internal/typedef"
	wf "github.com/francoisnyc/work-kernel/internal/workflow"
)

var pool *pgxpool.Pool

func TestMain(m *testing.M) {
	os.Exit(runTests(m))
}

func runTests(m *testing.M) int {
	ctx := context.Background()
	var (
		stop func()
		err  error
	)
	pool, stop, err = testdb.Start(ctx)
	if err != nil {
		log.Print(err)
		return 1
	}
	defer stop()

	ticket, err := os.ReadFile("../../types/ticket.v1.yaml")
	if err != nil {
		log.Print(err)
		return 1
	}
	// ticket: a timer on in_progress. watch: a timer on its initial state.
	// message: no lifecycle at all.
	for _, def := range []string{string(ticket), watchType, "id: message\nversion: 1\n"} {
		ty, err := typedef.Parse([]byte(def))
		if err == nil {
			err = itemservice.New(pool).RegisterType(ctx, ty)
		}
		if err != nil {
			log.Printf("register type: %v", err)
			return 1
		}
	}
	return m.Run()
}

const watchType = `
id: watch
version: 1
initial_state: watching
states:
  - {id: watching, category: active, timer: {after: 1h, to: lapsed}}
  - {id: lapsed, category: done}
transitions:
  - {from: watching, to: lapsed}
`

// --- the fake Temporal ---

// call is one delivery the relay attempted. It is the relay's own Delivery:
// the Item, the update ID, the event's shape, and whether to use
// Update-With-Start (Start true) or a plain Update.
type call = relay.Delivery

// fakeWorkflows records every delivery and answers with whatever `answer`
// says (nil if it is not set). It satisfies relay.Workflows; Go interfaces
// are implicit, so there is no "implements" clause.
type fakeWorkflows struct {
	mu     sync.Mutex
	calls  []call
	answer func(c call) error
}

func (f *fakeWorkflows) Deliver(ctx context.Context, c relay.Delivery) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, c)
	if f.answer == nil {
		return nil
	}
	return f.answer(c)
}

// seqs returns, in order, the item_seq of every delivery attempted for one Item.
func (f *fakeWorkflows) seqs(id uuid.UUID) []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []int64{}
	for _, c := range f.calls {
		if c.ItemID == id.String() {
			out = append(out, c.Event.Seq)
		}
	}
	return out
}

func (f *fakeWorkflows) callsFor(id uuid.UUID) []call {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []call
	for _, c := range f.calls {
		if c.ItemID == id.String() {
			out = append(out, c)
		}
	}
	return out
}

// --- helpers ---

var alice = item.Principal{Actor: "user:alice"}

func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func newKey() string { return "key-" + uuid.NewString() }

// fakeClock is a clock the test moves by hand, so backoff needs no sleeping.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// newRelay returns a relay over the shared database with a fake Temporal and
// a hand-moved clock. The tests share one outbox, so each starts by marking
// whatever earlier tests left behind as delivered.
func newRelay(t *testing.T) (*relay.Relay, *fakeWorkflows, *fakeClock) {
	t.Helper()
	if _, err := pool.Exec(testCtx(t), `update outbox set delivered_at = now() where delivered_at is null`); err != nil {
		t.Fatalf("clear outbox: %v", err)
	}
	fake := &fakeWorkflows{}
	clock := &fakeClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	r := relay.New(pool, fake, relay.Options{Backoff: time.Second, Now: clock.Now})
	return r, fake, clock
}

// ticketInProgress creates a ticket and walks it to in_progress:
// item_seq 1 create, 2 update, 3 transition.
func ticketInProgress(t *testing.T, k *itemservice.Service) uuid.UUID {
	t.Helper()
	ctx := testCtx(t)
	res, err := k.Create(ctx, itemservice.CreateRequest{
		IdempotencyKey: newKey(), Principal: alice, TypeID: "ticket", TypeVersion: 1,
		Attribution: item.Attribution{CorrelationID: "corr-1"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	_, err = k.Update(ctx, itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: alice, ItemID: res.ItemID,
		Fields: map[string]any{"f_assignee": "user-bob"},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	_, err = k.Transition(ctx, itemservice.TransitionRequest{
		IdempotencyKey: newKey(), Principal: alice, ItemID: res.ItemID, ToState: "in_progress",
		Attribution: item.Attribution{CorrelationID: "corr-2", Hop: 1},
	})
	if err != nil {
		t.Fatalf("Transition: %v", err)
	}
	return res.ItemID
}

func runOnce(t *testing.T, r *relay.Relay) {
	t.Helper()
	if err := r.RunOnce(testCtx(t)); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
}

// pending returns the Item's undelivered item_seqs, in order.
func pending(t *testing.T, id uuid.UUID) []int64 {
	t.Helper()
	rows, err := pool.Query(testCtx(t),
		`select item_seq from outbox where item_id = $1 and delivered_at is null order by item_seq`, id)
	if err != nil {
		t.Fatalf("read outbox: %v", err)
	}
	defer rows.Close()
	out := []int64{}
	for rows.Next() {
		var seq int64
		if err := rows.Scan(&seq); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, seq)
	}
	return out
}

func eventTime(t *testing.T, id uuid.UUID, seq int64) time.Time {
	t.Helper()
	var at time.Time
	err := pool.QueryRow(testCtx(t),
		`select created_at from item_events where item_id = $1 and item_seq = $2`, id, seq).Scan(&at)
	if err != nil {
		t.Fatalf("read event time: %v", err)
	}
	return at
}

// --- the tests ---

// One pass delivers an Item's events in item_seq order and marks them
// delivered. Each delivery carries the event's shape: what happened, which
// state it entered, when, and its attribution. The Type decides which one
// starts a workflow: here only event 3, which enters a state with a timer.
func TestRelay_DeliversInOrder_AndTheTypeDecidesWhatStartsAWorkflow(t *testing.T) {
	r, fake, _ := newRelay(t)
	id := ticketInProgress(t, itemservice.New(pool))

	runOnce(t, r)

	got := fake.callsFor(id)
	if len(got) != 3 {
		t.Fatalf("deliveries = %+v, want three", got)
	}
	want := []call{
		{ItemID: id.String(), UpdateID: id.String() + ":1", Start: false,
			Event: wf.Event{Seq: 1, Op: "create", To: "open", CorrelationID: "corr-1"}},
		{ItemID: id.String(), UpdateID: id.String() + ":2", Start: false,
			Event: wf.Event{Seq: 2, Op: "update"}},
		{ItemID: id.String(), UpdateID: id.String() + ":3", Start: true,
			Event: wf.Event{Seq: 3, Op: "transition", From: "open", To: "in_progress", CorrelationID: "corr-2", Hop: 1}},
	}
	for i := range want {
		// Times are compared with Equal, which ignores how the zone is stored.
		if at := eventTime(t, id, want[i].Event.Seq); !got[i].Event.At.Equal(at) {
			t.Errorf("delivery %d: At = %v, want the event's created_at %v", i+1, got[i].Event.At, at)
		}
		got[i].Event.At = time.Time{}
		if !reflect.DeepEqual(got[i], want[i]) {
			t.Errorf("delivery %d = %+v\nwant %+v", i+1, got[i], want[i])
		}
	}
	if left := pending(t, id); len(left) != 0 {
		t.Errorf("undelivered rows = %v, want none", left)
	}
}

// A create can start a workflow too, when the initial state has a timer.
func TestRelay_CreateIntoATimerState_StartsAWorkflow(t *testing.T) {
	r, fake, _ := newRelay(t)
	res, err := itemservice.New(pool).Create(testCtx(t), itemservice.CreateRequest{
		IdempotencyKey: newKey(), Principal: alice, TypeID: "watch", TypeVersion: 1,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	runOnce(t, r)

	got := fake.callsFor(res.ItemID)
	if len(got) != 1 || !got[0].Start || got[0].Event.Op != "create" || got[0].Event.To != "watching" {
		t.Errorf("deliveries = %+v, want one create into watching, with start", got)
	}
}

// "Nothing listening": a plain Update finds no running workflow. That is the
// normal case for most Items, so the row is marked delivered and the relay
// moves on to the Item's next event in the same pass.
func TestRelay_NothingListening_MarksDelivered(t *testing.T) {
	r, fake, _ := newRelay(t)
	fake.answer = func(c call) error {
		if !c.Start {
			return relay.ErrNothingListening
		}
		return nil
	}
	k := itemservice.New(pool)
	ticket := ticketInProgress(t, k)
	message, err := k.Create(testCtx(t), itemservice.CreateRequest{
		IdempotencyKey: newKey(), Principal: alice, TypeID: "message", TypeVersion: 1,
	})
	if err != nil {
		t.Fatalf("Create message: %v", err)
	}

	runOnce(t, r)

	if got := fake.seqs(ticket); !reflect.DeepEqual(got, []int64{1, 2, 3}) {
		t.Errorf("ticket deliveries = %v, want 1, 2, 3", got)
	}
	// A Type without listeners writes no outbox row (Step 5), so the relay
	// never sees the message at all.
	if got := fake.callsFor(message.ItemID); len(got) != 0 {
		t.Errorf("message deliveries = %+v, want none", got)
	}
	for _, id := range []uuid.UUID{ticket, message.ItemID} {
		if left := pending(t, id); len(left) != 0 {
			t.Errorf("item %s: undelivered rows = %v, want none", id, left)
		}
	}
}

// A failed delivery stops that Item where it is, so nothing is delivered out
// of order, and does not hold up other Items. The Item then backs off: it is
// not tried again until the backoff has passed, and then from the lowest
// undelivered event.
func TestRelay_FailingItem_KeepsOrder_BacksOff_AndDoesNotBlockOthers(t *testing.T) {
	r, fake, clock := newRelay(t)
	k := itemservice.New(pool)
	stuck := ticketInProgress(t, k)
	fine := ticketInProgress(t, k)
	down := true
	fake.answer = func(c call) error {
		if down && c.ItemID == stuck.String() && c.Event.Seq == 2 {
			return errors.New("temporal is unreachable")
		}
		return nil
	}

	runOnce(t, r)
	if got := fake.seqs(stuck); !reflect.DeepEqual(got, []int64{1, 2}) {
		t.Errorf("stuck item: deliveries = %v, want 1, 2 and then a stop", got)
	}
	if left := pending(t, stuck); !reflect.DeepEqual(left, []int64{2, 3}) {
		t.Errorf("stuck item: undelivered = %v, want 2, 3", left)
	}
	if left := pending(t, fine); len(left) != 0 {
		t.Errorf("other item: undelivered = %v, want none", left)
	}

	// Within the backoff, the stuck Item is left alone.
	down = false
	runOnce(t, r)
	if got := fake.seqs(stuck); !reflect.DeepEqual(got, []int64{1, 2}) {
		t.Errorf("stuck item, during backoff: deliveries = %v, want no new attempt", got)
	}

	// After it, the relay resumes at event 2.
	clock.advance(2 * time.Second)
	runOnce(t, r)
	if got := fake.seqs(stuck); !reflect.DeepEqual(got, []int64{1, 2, 2, 3}) {
		t.Errorf("stuck item, after backoff: deliveries = %v, want 1, 2, 2, 3", got)
	}
	if left := pending(t, stuck); len(left) != 0 {
		t.Errorf("stuck item: undelivered = %v, want none", left)
	}
}

// Deliver succeeds only when the update has completed, not merely been
// accepted. The workflow can accept an update and then fail it in the handler
// (a gap seen before its load finished). If the relay took "accepted" as
// done, that event would never be retried. So a failed update is a failed
// delivery like any other: the row stays pending and is sent again.
func TestRelay_UpdateAcceptedThenFailed_StaysPendingAndIsRetried(t *testing.T) {
	r, fake, clock := newRelay(t)
	id := ticketInProgress(t, itemservice.New(pool))
	failed := false
	fake.answer = func(c call) error {
		if c.ItemID == id.String() && c.Event.Seq == 3 && !failed {
			failed = true
			return errors.New("update accepted, then failed: gap")
		}
		return nil
	}

	runOnce(t, r)
	if left := pending(t, id); !reflect.DeepEqual(left, []int64{3}) {
		t.Fatalf("undelivered after the failed update = %v, want 3", left)
	}

	clock.advance(2 * time.Second)
	runOnce(t, r)
	if got := fake.seqs(id); !reflect.DeepEqual(got, []int64{1, 2, 3, 3}) {
		t.Errorf("deliveries = %v, want 1, 2, 3 and 3 again", got)
	}
	if left := pending(t, id); len(left) != 0 {
		t.Errorf("undelivered after the retry = %v, want none", left)
	}
}

// The relay can die between a delivery and marking it. The row is then still
// pending, so the next pass sends it again, under the same update ID
// "item:seq". That is what lets Temporal, and failing that the workflow's
// lastAppliedSeq, recognize it as a duplicate.
func TestRelay_CrashBeforeMarkingDelivered_RedeliversWithTheSameUpdateID(t *testing.T) {
	r, fake, clock := newRelay(t)
	id := ticketInProgress(t, itemservice.New(pool))

	// Simulate the crash: the first delivery goes through, and at that very
	// moment the pass's context is canceled, so its "mark delivered" fails.
	ctx, crash := context.WithCancel(testCtx(t))
	fake.answer = func(c call) error {
		crash()
		return nil
	}
	_ = r.RunOnce(ctx) // how the interrupted pass reports is not the point here
	if left := pending(t, id); !reflect.DeepEqual(left, []int64{1, 2, 3}) {
		t.Fatalf("undelivered after the crash = %v, want 1, 2, 3", left)
	}

	fake.answer = nil
	clock.advance(time.Minute) // well past any backoff
	runOnce(t, r)

	var ids []string
	for _, c := range fake.callsFor(id) {
		ids = append(ids, c.UpdateID)
	}
	want := []string{id.String() + ":1", id.String() + ":1", id.String() + ":2", id.String() + ":3"}
	if !reflect.DeepEqual(ids, want) {
		t.Errorf("update IDs = %v\nwant %v", ids, want)
	}
	if left := pending(t, id); len(left) != 0 {
		t.Errorf("undelivered after the second pass = %v, want none", left)
	}
}

// An Item that keeps failing waits longer each time, up to a limit.
func TestRelay_RepeatedFailures_BackOffLonger(t *testing.T) {
	r, fake, clock := newRelay(t)
	id := ticketInProgress(t, itemservice.New(pool))
	fake.answer = func(c call) error { return errors.New("temporal is unreachable") }

	runOnce(t, r) // attempt 1 fails; backoff 1s
	clock.advance(1100 * time.Millisecond)
	runOnce(t, r) // attempt 2 fails; backoff 2s
	clock.advance(1100 * time.Millisecond)
	runOnce(t, r) // still backing off
	if got := len(fake.seqs(id)); got != 2 {
		t.Errorf("attempts after 2.2s = %d, want 2: the second backoff is longer", got)
	}
	clock.advance(time.Second)
	runOnce(t, r)
	if got := len(fake.seqs(id)); got != 3 {
		t.Errorf("attempts after 3.2s = %d, want 3", got)
	}
}

// With nothing pending, a pass does nothing.
func TestRelay_NothingPending_DeliversNothing(t *testing.T) {
	r, fake, _ := newRelay(t)
	id := ticketInProgress(t, itemservice.New(pool))
	runOnce(t, r)
	runOnce(t, r)

	if got := fake.seqs(id); !reflect.DeepEqual(got, []int64{1, 2, 3}) {
		t.Errorf("deliveries after two passes = %v, want each event once", got)
	}
}

// Run polls until its context is canceled: an event written while it runs is
// picked up without anyone calling RunOnce.
func TestRelay_Run_PollsUntilCanceled(t *testing.T) {
	_, fake, _ := newRelay(t)
	r := relay.New(pool, fake, relay.Options{PollInterval: 10 * time.Millisecond})

	// A context carries cancellation; canceling it is how Go asks a
	// long-running call to stop, like interrupting a thread, but explicit.
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1) // a channel: a typed pipe between goroutines
	go func() { done <- r.Run(ctx) }()

	id := ticketInProgress(t, itemservice.New(pool))
	deadline := time.Now().Add(10 * time.Second)
	for len(pending(t, id)) > 0 {
		if time.Now().After(deadline) {
			t.Fatalf("still undelivered after 10s: %v", pending(t, id))
		}
		time.Sleep(20 * time.Millisecond)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("Run returned %v, want nil or context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop after its context was canceled")
	}
	if got := fake.seqs(id); !reflect.DeepEqual(got, []int64{1, 2, 3}) {
		t.Errorf("deliveries = %v, want 1, 2, 3", got)
	}
}
