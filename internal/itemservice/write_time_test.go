// Tests for Jira part 2, step 1: the time a write is stamped with
// (docs/design.md, "Write path").
//
// Each write reads the clock once, AFTER taking its row locks, and uses that
// one time for its event's created_at and for cause.at. The row lock runs
// an Item's writes one after the other, so along item_seq an Item's event
// times never go backwards. (Before, an event carried its transaction's
// start time: a write that waited for the lock was stamped earlier than the
// write it waited for, and a duration between two events could be negative.)
//
// commit_seq and the outbox are unaffected: commit_seq is still taken at
// the insert, and every event of a Type with listeners still gets its row.
package itemservice_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/francoisnyc/work-kernel/internal/itemservice"
)

// eventTimes returns the Item's event times and commit_seqs in item_seq
// order, as stored.
func eventTimes(t *testing.T, id uuid.UUID) (at []time.Time, commit []int64) {
	t.Helper()
	rows, err := pool.Query(testCtx(t),
		`select created_at, commit_seq from item_events where item_id = $1 order by item_seq`, id)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			a time.Time
			c int64
		)
		if err := rows.Scan(&a, &c); err != nil {
			t.Fatalf("scan event: %v", err)
		}
		at, commit = append(at, a), append(commit, c)
	}
	return at, commit
}

// The case that went wrong. "early" begins its transaction first, then is
// held before it takes the Item's lock. "late" begins afterwards, takes the
// lock, writes event 2 and commits. "early" then gets the lock and writes
// event 3. Event 3 must be stamped after event 2, although its transaction
// started before event 2's did.
func TestWriteTime_AWriteThatWaitedIsStampedAfterTheOneItWaitedFor(t *testing.T) {
	k := itemservice.New(pool)
	id := createTicket(t, k, map[string]any{"f_title": "clock " + word()}) // ticket@1 has listeners

	started, release := make(chan struct{}), make(chan struct{})
	defer itemservice.SetTestHook(func(ctx context.Context, tx pgx.Tx, point string) error {
		// The "write" point is inside the transaction, before the row lock.
		if markOf(ctx) == "early" && point == "write" {
			close(started)
			<-release
		}
		return nil
	})()

	type result struct {
		res itemservice.WriteResult
		err error
	}
	early := make(chan result, 1)
	go func() {
		res, err := k.Update(marked(testCtx(t), "early"), itemservice.UpdateRequest{
			IdempotencyKey: newKey(), Principal: alice, ItemID: id, Fields: map[string]any{"f_title": "early"},
		})
		early <- result{res, err}
	}()
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("the early write never began its transaction")
	}
	// Let the clock move on, so the two transactions' start times differ
	// by far more than its resolution.
	time.Sleep(50 * time.Millisecond)
	late, err := k.Update(testCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: alice, ItemID: id, Fields: map[string]any{"f_title": "late"},
	})
	if err != nil {
		t.Fatalf("the late write: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	first := <-early
	if first.err != nil {
		t.Fatalf("the early write: %v", first.err)
	}
	if late.ItemSeq != 2 || first.res.ItemSeq != 3 {
		t.Fatalf("item_seqs = late %d, early %d; want 2 and 3 (the test's own setup)", late.ItemSeq, first.res.ItemSeq)
	}

	at, commit := eventTimes(t, id)
	if !at[2].After(at[1]) {
		t.Errorf("event 3 is stamped %s, event 2 %s: the write that waited must be stamped after the one it waited for",
			at[2].Format(time.RFC3339Nano), at[1].Format(time.RFC3339Nano))
	}
	if !at[1].After(at[0]) {
		t.Errorf("event 2 (%v) is not after the create (%v)", at[1], at[0])
	}
	// commit_seq: taken at the insert, as before; each result names its own.
	if commit[1] != late.CommitSeq || commit[2] != first.res.CommitSeq {
		t.Errorf("commit_seqs = %v; the results say late %d, early %d", commit, late.CommitSeq, first.res.CommitSeq)
	}
	if !(commit[0] < commit[1] && commit[1] < commit[2]) {
		t.Errorf("commit_seqs = %v, want them rising with item_seq on one Item", commit)
	}
	// The outbox: one row per event, as before.
	if n := count(t, `select count(*) from outbox where item_id = $1`, id); n != 3 {
		t.Errorf("outbox rows = %d, want 3", n)
	}
	// And a read can still wait for either write.
	for _, seq := range []int64{late.CommitSeq, first.res.CommitSeq} {
		if _, err := k.Search(testCtx(t), itemservice.SearchRequest{
			Principal: alice, Filter: itemservice.Filter{TypeIDs: []string{"ticket"}}, MinSeq: seq, Limit: 1,
		}); err != nil {
			t.Errorf("a read with min_seq %d: %v", seq, err)
		}
	}
}

// Many writers on one Item, with no hook: along item_seq the event times
// never go backwards, and no two are equal (a duration between two events
// of an Item is never negative, and never zero by accident of a shared
// transaction start).
func TestWriteTime_NeverGoesBackwardsOnOneItem(t *testing.T) {
	k := itemservice.New(pool)
	id := createTicket(t, k, map[string]any{"f_title": "clock " + word()})
	const writers, each = 8, 10
	var wg sync.WaitGroup
	errs := make(chan error, writers*each)
	for range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range each {
				_, err := k.Update(context.Background(), itemservice.UpdateRequest{
					IdempotencyKey: newKey(), Principal: alice, ItemID: id, Fields: map[string]any{"f_title": word()},
				})
				if err != nil {
					errs <- err
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("update: %v", err)
	}
	at, _ := eventTimes(t, id)
	if len(at) != writers*each+1 {
		t.Fatalf("%d events, want %d", len(at), writers*each+1)
	}
	for i := 1; i < len(at); i++ {
		if !at[i].After(at[i-1]) {
			t.Fatalf("event %d is stamped %s, event %d %s: times must rise with item_seq",
				i+1, at[i].Format(time.RFC3339Nano), i, at[i-1].Format(time.RFC3339Nano))
		}
	}
}

// A create stamps the Item and its first event with the same time.
func TestWriteTime_CreateStampsItemAndEventAlike(t *testing.T) {
	k := itemservice.New(pool)
	id := createTicket(t, k, map[string]any{"f_title": "clock " + word()})
	var itemAt, eventAt time.Time
	if err := pool.QueryRow(testCtx(t),
		`select i.created_at, e.created_at from items i join item_events e on e.item_id = i.id and e.item_seq = 1
		 where i.id = $1`, id).Scan(&itemAt, &eventAt); err != nil {
		t.Fatalf("read times: %v", err)
	}
	if !itemAt.Equal(eventAt) {
		t.Errorf("the Item is stamped %v, its create event %v; want one time", itemAt, eventAt)
	}
}

// Options.ClockSkew (tests only) moves the time every write is stamped
// with: the tests of a timer that fires early stand on it.
func TestWriteTime_ClockSkewIsForTests(t *testing.T) {
	behind := itemservice.NewWithOptions(pool, itemservice.Options{CacheTypes: true, ClockSkew: -time.Hour})
	id := createTicket(t, behind, map[string]any{"f_title": "clock " + word()})
	at, _ := eventTimes(t, id)
	if d := time.Since(at[0]); d < 59*time.Minute || d > 61*time.Minute {
		t.Errorf("with a clock an hour behind, the event is stamped %v ago; want about an hour", d)
	}
}
