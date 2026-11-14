// Tests for Step A: commit_seq from a plain sequence, each event's
// transaction ID, and min_seq as a watermark over transaction IDs.
//
// Several tests play a second writer by hand: a transaction that inserts an
// event and then stays open. That is a side write no production code may
// make; here it stands in for a write the item service is still committing.
package itemservice_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/francoisnyc/work-kernel/internal/itemservice"
)

// --- helpers ---

// openWriter is a transaction playing a writer that has not committed yet.
type openWriter struct {
	t  *testing.T
	tx pgx.Tx
}

// beginWriter starts a transaction and gives it a transaction ID at once, so
// its place in the watermark is fixed from now on.
func beginWriter(t *testing.T) *openWriter {
	t.Helper()
	tx, err := pool.Begin(context.Background())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	if _, err := tx.Exec(context.Background(), `select pg_current_xact_id()`); err != nil {
		t.Fatalf("take a transaction ID: %v", err)
	}
	return &openWriter{t: t, tx: tx}
}

// event inserts an event for the Item, as a write would, and returns the
// commit_seq the sequence gave it. item_seq is far above anything the
// service writes, so it cannot collide.
func (w *openWriter) event(id uuid.UUID, itemSeq int64) int64 {
	w.t.Helper()
	var seq int64
	err := w.tx.QueryRow(context.Background(),
		`insert into item_events (item_id, item_seq, actor, op, payload)
		 values ($1, $2, 'user-test', 'update', '{}') returning commit_seq`, id, itemSeq).Scan(&seq)
	if err != nil {
		w.t.Fatalf("insert event: %v", err)
	}
	return seq
}

func (w *openWriter) commit() {
	w.t.Helper()
	if err := w.tx.Commit(context.Background()); err != nil {
		w.t.Fatalf("commit: %v", err)
	}
}

func (w *openWriter) rollback() {
	w.t.Helper()
	if err := w.tx.Rollback(context.Background()); err != nil {
		w.t.Fatalf("rollback: %v", err)
	}
}

// searchWait runs a Search with min_seq and returns how long it took.
func searchWait(t *testing.T, k *itemservice.Service, minSeq int64, wait time.Duration) (time.Duration, error) {
	t.Helper()
	start := time.Now()
	_, err := k.Search(testCtx(t), itemservice.SearchRequest{
		Principal: person("alice"), Query: word(), MinSeq: minSeq, Wait: wait,
	})
	return time.Since(start), err
}

// --- the schema ---

// commit_seq comes from a sequence; each event records the transaction that
// wrote it; the commit counter is gone.
func TestSchema_CommitSeqIsASequenceAndEventsCarryTheirTransaction(t *testing.T) {
	if n := count(t, `select count(*) from information_schema.tables where table_name = 'commit_counter'`); n != 0 {
		t.Error("table commit_counter still exists")
	}
	if n := count(t, `select count(*) from pg_class where relname = 'commit_seq' and relkind = 'S'`); n != 1 {
		t.Error("sequence commit_seq does not exist")
	}
	n := count(t, `select count(*) from information_schema.columns
	               where table_name = 'item_events' and column_name = 'xid'
	                 and data_type = 'xid8' and is_nullable = 'NO'`)
	if n != 1 {
		t.Error("item_events.xid (xid8, not null) does not exist")
	}

	k := itemservice.New(pool)
	res, err := k.Create(testCtx(t), newRequest())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// The transaction has committed, so its ID is below the current xmin.
	n = count(t, `select count(*) from item_events
	              where item_id = $1 and xid < pg_snapshot_xmin(pg_current_snapshot())`, res.ItemID)
	if n != 1 {
		t.Error("the committed event's xid is not below the snapshot xmin")
	}
}

// commit_seq values are unique and increase with each write, but they need
// not be dense: a rejected write may use up a number.
func TestCommitSeq_IncreasesAndIsUnique(t *testing.T) {
	k := itemservice.New(pool)
	var last int64
	for i := range 5 {
		res, err := k.Create(testCtx(t), newRequest())
		if err != nil {
			t.Fatalf("Create %d: %v", i, err)
		}
		if res.CommitSeq <= last {
			t.Errorf("Create %d: commit_seq %d, not above the previous %d", i, res.CommitSeq, last)
		}
		last = res.CommitSeq
	}
}

// --- no shared row lock ---

// A writer still in its transaction does not hold up writes to other Items.
// Under the commit counter it did: every write queued on one row.
func TestWrite_OpenWriterDoesNotBlockOtherItems(t *testing.T) {
	k := itemservice.New(pool)
	held := createTicket(t, k, map[string]any{"f_title": "held"})
	w := beginWriter(t)
	w.event(held, 1000)

	done := make(chan error, 1)
	go func() {
		_, err := k.Create(testCtx(t), newRequest())
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Create while another writer is open: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Create waited on another Item's open writer")
	}
}

// --- min_seq ---

// The named write is still committing: Search waits for it and returns as
// soon as it commits, not before.
func TestMinSeq_WaitsForAWriteStillCommitting(t *testing.T) {
	k := itemservice.New(pool)
	id := createTicket(t, k, map[string]any{"f_title": "x"})
	w := beginWriter(t)
	seq := w.event(id, 1000)

	go func() {
		time.Sleep(300 * time.Millisecond)
		w.commit()
	}()
	took, err := searchWait(t, k, seq, 10*time.Second)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if took < 250*time.Millisecond {
		t.Errorf("Search answered after %v, before the write committed at ~300ms", took)
	}
	if took > 5*time.Second {
		t.Errorf("Search took %v, want soon after the commit", took)
	}
}

// An event that cannot be found is never "reached": a write still open past
// the bound is ErrNotCaughtUp.
func TestMinSeq_WriteNotVisibleWithinTheBound_IsNotCaughtUp(t *testing.T) {
	k := itemservice.New(pool)
	id := createTicket(t, k, map[string]any{"f_title": "x"})
	w := beginWriter(t)
	seq := w.event(id, 1000)

	took, err := searchWait(t, k, seq, 300*time.Millisecond)
	if !errors.Is(err, itemservice.ErrNotCaughtUp) {
		t.Fatalf("Search error = %v, want ErrNotCaughtUp", err)
	}
	if took < 300*time.Millisecond {
		t.Errorf("Search gave up after %v, before its 300ms bound", took)
	}
	w.rollback()
}

// A rolled-back write never becomes visible, so its commit_seq is never
// reached: ErrNotCaughtUp after the bound, not success.
func TestMinSeq_RolledBackWrite_IsNotCaughtUp(t *testing.T) {
	k := itemservice.New(pool)
	id := createTicket(t, k, map[string]any{"f_title": "x"})
	w := beginWriter(t)
	seq := w.event(id, 1000)
	w.rollback()

	_, err := searchWait(t, k, seq, 300*time.Millisecond)
	if !errors.Is(err, itemservice.ErrNotCaughtUp) {
		t.Fatalf("Search error = %v, want ErrNotCaughtUp", err)
	}
}

// The guarantee covers older transactions: while an older writer is still
// open, a younger write that has committed is not yet "reached".
func TestMinSeq_WaitsForOlderTransactions(t *testing.T) {
	k := itemservice.New(pool)
	id := createTicket(t, k, map[string]any{"f_title": "x"})
	older := beginWriter(t)
	older.event(id, 1000)
	young, err := k.Create(testCtx(t), newRequest()) // committed, younger than `older`
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	_, err = searchWait(t, k, young.CommitSeq, 300*time.Millisecond)
	if !errors.Is(err, itemservice.ErrNotCaughtUp) {
		t.Fatalf("Search while an older writer is open: error = %v, want ErrNotCaughtUp", err)
	}
	older.commit()
	if _, err := searchWait(t, k, young.CommitSeq, 5*time.Second); err != nil {
		t.Errorf("Search after the older writer committed: %v", err)
	}
}

// The guarantee does not cover younger transactions: one that took a lower
// commit_seq and is still open does not hold up a min_seq above it. This is
// the weaker guarantee design.md accepts: commit_seq is not a cursor.
func TestMinSeq_DoesNotWaitForYoungerTransactions(t *testing.T) {
	k := itemservice.New(pool)
	id := createTicket(t, k, map[string]any{"f_title": "x"})
	older := beginWriter(t)        // older transaction ID
	younger := beginWriter(t)      // younger transaction ID
	low := younger.event(id, 1000) // takes the lower commit_seq
	high := older.event(id, 1001)  // takes the higher one
	if high <= low {
		t.Fatalf("setup: commit_seq %d is not above %d", high, low)
	}
	older.commit()

	took, err := searchWait(t, k, high, 3*time.Second)
	if err != nil {
		t.Fatalf("Search for the older write while a younger one is open: %v", err)
	}
	if took > time.Second {
		t.Errorf("Search took %v; it should not wait for a younger transaction", took)
	}
	younger.rollback()
}

// --- timeouts ---

// A write that waits too long (here, on another transaction's lock on the
// same Item) fails at the write path's statement timeout instead of hanging,
// rolls back, and does not use up its key.
func TestWrite_StatementTimeout(t *testing.T) {
	k := itemservice.NewWithOptions(pool, itemservice.Options{CacheTypes: true, WriteTimeout: 300 * time.Millisecond})
	id := createTicket(t, k, map[string]any{"f_title": "x"})

	tx, err := pool.Begin(testCtx(t))
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(testCtx(t), `select 1 from items where id = $1 for update`, id); err != nil {
		t.Fatalf("lock the Item: %v", err)
	}

	req := itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: alice, ItemID: id, Fields: map[string]any{"f_title": "y"},
	}
	start := time.Now()
	_, err = k.Update(testCtx(t), req)
	if err == nil {
		t.Fatal("Update succeeded while the Item was locked")
	}
	if took := time.Since(start); took > 3*time.Second {
		t.Errorf("Update gave up after %v, want about 300ms", took)
	}
	for _, sentinel := range []error{itemservice.ErrStale, itemservice.ErrRejected, itemservice.ErrNotFound} {
		if errors.Is(err, sentinel) {
			t.Errorf("a timeout reported as %v", sentinel)
		}
	}

	_ = tx.Rollback(context.Background())
	if _, err := k.Update(testCtx(t), req); err != nil {
		t.Errorf("retry with the same key after the lock was released: %v", err)
	}
}

// New uses a write timeout by default; a zero value is not "no timeout".
func TestWrite_DefaultStatementTimeout(t *testing.T) {
	if itemservice.DefaultWriteTimeout <= 0 || itemservice.DefaultWriteTimeout > 30*time.Second {
		t.Errorf("DefaultWriteTimeout = %v, want a bound between 0 and 30s", itemservice.DefaultWriteTimeout)
	}
}

// The database ends transactions left idle, so one forgotten session cannot
// hold the watermark back for everyone. ALTER DATABASE ... SET only applies
// to sessions opened afterwards, so the test opens a fresh connection rather
// than borrowing one from the pool.
func TestDatabase_IdleInTransactionTimeoutIsSet(t *testing.T) {
	conn, err := pgx.ConnectConfig(testCtx(t), pool.Config().ConnConfig.Copy())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = conn.Close(context.Background()) }()
	var setting string
	if err := conn.QueryRow(testCtx(t), `show idle_in_transaction_session_timeout`).Scan(&setting); err != nil {
		t.Fatalf("show: %v", err)
	}
	if setting == "0" {
		t.Error("idle_in_transaction_session_timeout is 0 (off)")
	}
}
