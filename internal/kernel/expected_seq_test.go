// Tests for Step 3, commit 1: an optional expected_item_seq on Update and
// Transition (an if-match against lost updates), and attribution fields on
// write requests. The timer's write needs both.
package kernel_test

import (
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/kernel"
	"github.com/francoisnyc/work-kernel/internal/lifecycle"
)

// assertStale checks that err is a rejection with code "stale".
func assertStale(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, kernel.ErrRejected) {
		t.Fatalf("error = %v, want ErrRejected", err)
	}
	// errors.As finds an error of a given type in the chain and copies it out,
	// like catching a wrapped exception by class and reading its fields.
	var rej *lifecycle.Rejection
	if !errors.As(err, &rej) || rej.Code != lifecycle.Stale {
		t.Fatalf("error = %v, want a rejection with code %q", err, lifecycle.Stale)
	}
}

// --- expected_item_seq ---

// A write that names the Item's current item_seq goes through.
func TestExpectedItemSeq_Match_IsAccepted(t *testing.T) {
	ctx := testCtx(t)
	k := kernel.New(pool)
	id := createTicket(t, k, map[string]any{"f_title": "Printer on fire"}) // item_seq 1

	up, err := k.Update(ctx, kernel.UpdateRequest{
		IdempotencyKey: newKey(), Principal: alice, ItemID: id,
		Fields: map[string]any{"f_assignee": "user-bob"}, ExpectedItemSeq: 1,
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	tr, err := k.Transition(ctx, kernel.TransitionRequest{
		IdempotencyKey: newKey(), Principal: alice, ItemID: id,
		ToState: "in_progress", ExpectedItemSeq: 2,
	})
	if err != nil {
		t.Fatalf("Transition: %v", err)
	}
	if up.ItemSeq != 2 || tr.ItemSeq != 3 {
		t.Errorf("item_seq = %d then %d, want 2 then 3", up.ItemSeq, tr.ItemSeq)
	}
}

// A Transition based on an old item_seq is rejected as stale and writes
// nothing, even though the move itself is legal.
func TestTransition_ExpectedItemSeq_Mismatch_IsStale(t *testing.T) {
	k := kernel.New(pool)
	id := createTicket(t, k, map[string]any{"f_assignee": "user-bob"}) // item_seq 1
	update(t, k, id, map[string]any{"f_title": "moved on"})            // item_seq 2

	_, err := k.Transition(testCtx(t), kernel.TransitionRequest{
		IdempotencyKey: newKey(), Principal: alice, ItemID: id,
		ToState: "in_progress", ExpectedItemSeq: 1,
	})
	assertStale(t, err)
	// The reason gives both numbers, so the caller knows how far behind it is.
	if !strings.Contains(err.Error(), "1") || !strings.Contains(err.Error(), "2") {
		t.Errorf("error %q does not give the expected and the current item_seq", err)
	}
	if got := get(t, k, id).State; got != "open" {
		t.Errorf("state = %q, want it unchanged (open)", got)
	}
	assertNothingWritten(t, id, 2)
}

// The same if-match protects Update.
func TestUpdate_ExpectedItemSeq_Mismatch_IsStale(t *testing.T) {
	k := kernel.New(pool)
	id := createTicket(t, k, map[string]any{"f_title": "first"}) // item_seq 1
	update(t, k, id, map[string]any{"f_title": "second"})        // item_seq 2

	_, err := k.Update(testCtx(t), kernel.UpdateRequest{
		IdempotencyKey: newKey(), Principal: alice, ItemID: id,
		Fields: map[string]any{"f_title": "third"}, ExpectedItemSeq: 1,
	})
	assertStale(t, err)
	if got := get(t, k, id).Fields["f_title"]; got != "second" {
		t.Errorf("f_title = %v, want it unchanged (second)", got)
	}
	assertNothingWritten(t, id, 2)
}

// An expected item_seq ahead of the Item is just as stale as one behind it.
func TestExpectedItemSeq_AheadOfTheItem_IsStale(t *testing.T) {
	k := kernel.New(pool)
	id := createTicket(t, k, nil)

	_, err := k.Update(testCtx(t), kernel.UpdateRequest{
		IdempotencyKey: newKey(), Principal: alice, ItemID: id,
		Fields: map[string]any{"f_title": "x"}, ExpectedItemSeq: 7,
	})
	assertStale(t, err)
	assertNothingWritten(t, id, 1)
}

// Zero means "no check" (the field is optional); a negative number is malformed.
func TestExpectedItemSeq_Negative_IsInvalid(t *testing.T) {
	k := kernel.New(pool)
	id := createTicket(t, k, map[string]any{"f_assignee": "user-bob"})

	_, err := k.Update(testCtx(t), kernel.UpdateRequest{
		IdempotencyKey: newKey(), Principal: alice, ItemID: id,
		Fields: map[string]any{"f_title": "x"}, ExpectedItemSeq: -1,
	})
	if !errors.Is(err, kernel.ErrInvalid) {
		t.Errorf("Update error = %v, want ErrInvalid", err)
	}
	_, err = k.Transition(testCtx(t), kernel.TransitionRequest{
		IdempotencyKey: newKey(), Principal: alice, ItemID: id, ToState: "in_progress", ExpectedItemSeq: -1,
	})
	if !errors.Is(err, kernel.ErrInvalid) {
		t.Errorf("Transition error = %v, want ErrInvalid", err)
	}
	assertNothingWritten(t, id, 1)
}

// The lost-update case the check exists for: eight writers all read item_seq 1
// and write on that basis. Exactly one wins; the rest are told they are stale.
func TestUpdate_ExpectedItemSeq_Concurrent_OneWins(t *testing.T) {
	k := kernel.New(pool)
	id := createTicket(t, k, nil)

	const writers = 8
	errs := make([]error, writers)
	var wg sync.WaitGroup
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = k.Update(testCtx(t), kernel.UpdateRequest{
				IdempotencyKey: newKey(), Principal: alice, ItemID: id,
				Fields: map[string]any{"f_title": "from-" + uuid.NewString()}, ExpectedItemSeq: 1,
			})
		}()
	}
	wg.Wait()

	wins := 0
	for _, err := range errs {
		if err == nil {
			wins++
			continue
		}
		assertStale(t, err)
	}
	if wins != 1 {
		t.Errorf("successful writers = %d, want exactly 1", wins)
	}
	assertNothingWritten(t, id, 2)
}

// A retry with the same key gets the stored result, although by then its
// expected item_seq is behind: the key is looked up before anything is checked.
func TestExpectedItemSeq_RetryAfterSuccess_ReturnsTheStoredResult(t *testing.T) {
	ctx := testCtx(t)
	k := kernel.New(pool)
	id := createTicket(t, k, map[string]any{"f_assignee": "user-bob"})
	req := kernel.TransitionRequest{
		IdempotencyKey: newKey(), Principal: alice, ItemID: id, ToState: "in_progress", ExpectedItemSeq: 1,
	}

	first, err := k.Transition(ctx, req)
	if err != nil {
		t.Fatalf("first Transition: %v", err)
	}
	second, err := k.Transition(ctx, req)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if second != first {
		t.Errorf("retry result = %+v, want %+v", second, first)
	}
	assertNothingWritten(t, id, 2)
}

// A stale rejection does not use up the key: the caller re-reads and retries.
func TestExpectedItemSeq_StaleDoesNotConsumeTheKey(t *testing.T) {
	ctx := testCtx(t)
	k := kernel.New(pool)
	id := createTicket(t, k, nil)
	update(t, k, id, map[string]any{"f_title": "moved on"}) // item_seq 2
	key := newKey()

	_, err := k.Update(ctx, kernel.UpdateRequest{
		IdempotencyKey: key, Principal: alice, ItemID: id,
		Fields: map[string]any{"f_title": "mine"}, ExpectedItemSeq: 1,
	})
	assertStale(t, err)
	if n := count(t, `select count(*) from idempotency where key = $1`, key); n != 0 {
		t.Errorf("idempotency rows after a stale rejection = %d, want 0", n)
	}
}

// --- attribution ---

// attribution reads the attribution columns of one event. Null reads as "".
func attribution(t *testing.T, id uuid.UUID, itemSeq int64) (actor string, a item.Attribution) {
	t.Helper()
	err := pool.QueryRow(testCtx(t),
		`select actor, coalesce(originating_hook, ''), coalesce(causation_id, ''),
		        coalesce(correlation_id, ''), hop
		 from item_events where item_id = $1 and item_seq = $2`, id, itemSeq,
	).Scan(&actor, &a.OriginatingHook, &a.CausationID, &a.CorrelationID, &a.Hop)
	if err != nil {
		t.Fatalf("read attribution of event %d: %v", itemSeq, err)
	}
	return actor, a
}

// What a write request says about its cause is stored on the event, for all
// three operations. This is how a timer's transition will name its timer.
func TestWrite_AttributionIsStoredOnTheEvent(t *testing.T) {
	ctx := testCtx(t)
	k := kernel.New(pool)
	timer := item.Principal{Actor: "system:timer"}

	createReq := newRequest()
	createReq.Attribution = item.Attribution{CorrelationID: "corr-1"}
	created, err := k.Create(ctx, createReq)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	id := created.ItemID
	if _, got := attribution(t, id, 1); got != createReq.Attribution {
		t.Errorf("create event attribution = %+v, want %+v", got, createReq.Attribution)
	}

	want := item.Attribution{
		OriginatingHook: "timer:ticket@v1:open",
		CausationID:     id.String() + ":1",
		CorrelationID:   "corr-1",
		Hop:             1,
	}
	up, err := k.Update(ctx, kernel.UpdateRequest{
		IdempotencyKey: newKey(), Principal: timer, ItemID: id,
		Fields: map[string]any{"f_assignee": "user-bob"}, Attribution: want,
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if actor, got := attribution(t, id, up.ItemSeq); actor != "system:timer" || got != want {
		t.Errorf("update event: actor %q, attribution %+v; want system:timer, %+v", actor, got, want)
	}

	tr, err := k.Transition(ctx, kernel.TransitionRequest{
		IdempotencyKey: newKey(), Principal: timer, ItemID: id, ToState: "in_progress", Attribution: want,
	})
	if err != nil {
		t.Fatalf("Transition: %v", err)
	}
	if actor, got := attribution(t, id, tr.ItemSeq); actor != "system:timer" || got != want {
		t.Errorf("transition event: actor %q, attribution %+v; want system:timer, %+v", actor, got, want)
	}
}

// hop counts depth in a chain, so it cannot be negative.
func TestWrite_NegativeHop_IsInvalid(t *testing.T) {
	k := kernel.New(pool)
	id := createTicket(t, k, nil)

	_, err := k.Update(testCtx(t), kernel.UpdateRequest{
		IdempotencyKey: newKey(), Principal: alice, ItemID: id,
		Fields: map[string]any{"f_title": "x"}, Attribution: item.Attribution{Hop: -1},
	})
	if !errors.Is(err, kernel.ErrInvalid) {
		t.Fatalf("Update error = %v, want ErrInvalid", err)
	}
	assertNothingWritten(t, id, 1)
}
