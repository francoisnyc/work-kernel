// Tests for the schema follow-ups from the Step 1 review (first commit of
// Step 2): scoped idempotency keys, namespace_id, attribution columns,
// item_event_values, the commit counter and two indexes.
//
// This file is in the same package as kernel_test.go, so it shares that file's
// TestMain, pool and helpers. Go compiles all files of a package together.
package itemservice_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
)

var bob = item.Principal{Actor: "user-bob"}

// --- Idempotency keys scoped by (namespace_id, actor, key) ---

// Two clients that pick the same key must not collide.
func TestCreate_SameKeyDifferentActors_AreIndependentWrites(t *testing.T) {
	ctx := testCtx(t)
	k := itemservice.New(pool)
	aliceReq := newRequest()
	bobReq := aliceReq // a struct assignment copies the value, including the key
	bobReq.Principal = bob

	aliceRes, err := k.Create(ctx, aliceReq)
	if err != nil {
		t.Fatalf("alice Create: %v", err)
	}
	bobRes, err := k.Create(ctx, bobReq)
	if err != nil {
		t.Fatalf("bob Create with the same key: %v", err)
	}

	if aliceRes.ItemID == bobRes.ItemID {
		t.Error("two actors with the same key got the same Item")
	}

	// One record per actor, both in the default namespace.
	for _, actor := range []string{"user-alice", "user-bob"} {
		n := count(t,
			`select count(*) from idempotency where namespace_id = 'default' and actor = $1 and key = $2`,
			actor, aliceReq.IdempotencyKey)
		if n != 1 {
			t.Errorf("idempotency rows for %s = %d, want 1", actor, n)
		}
	}

	// Each actor's retry still returns that actor's own result.
	aliceAgain, err := k.Create(ctx, aliceReq)
	if err != nil {
		t.Fatalf("alice retry: %v", err)
	}
	if aliceAgain != aliceRes {
		t.Errorf("alice retry = %+v, want %+v", aliceAgain, aliceRes)
	}
	bobAgain, err := k.Create(ctx, bobReq)
	if err != nil {
		t.Fatalf("bob retry: %v", err)
	}
	if bobAgain != bobRes {
		t.Errorf("bob retry = %+v, want %+v", bobAgain, bobRes)
	}
}

// --- namespace_id ---

func TestCreate_ItemIsInDefaultNamespace(t *testing.T) {
	ctx := testCtx(t)
	k := itemservice.New(pool)

	res, err := k.Create(ctx, newRequest())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	var namespace string
	err = pool.QueryRow(ctx, `select namespace_id from items where id = $1`, res.ItemID).Scan(&namespace)
	if err != nil {
		t.Fatalf("read namespace_id: %v", err)
	}
	if namespace != "default" {
		t.Errorf("namespace_id = %q, want %q", namespace, "default")
	}
}

// --- Attribution columns ---

// The columns exist now; callers fill them in later steps. Until then they
// are null and hop is 0.
func TestCreate_EventHasEmptyAttributionColumns(t *testing.T) {
	ctx := testCtx(t)
	k := itemservice.New(pool)

	res, err := k.Create(ctx, newRequest())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// A *string scans a nullable column: nil means SQL NULL.
	var (
		agentSession, originatingHook, causationID, correlationID *string
		hop                                                       int
	)
	err = pool.QueryRow(ctx,
		`select agent_session, originating_hook, causation_id, correlation_id, hop
		 from item_events where item_id = $1`,
		res.ItemID).Scan(&agentSession, &originatingHook, &causationID, &correlationID, &hop)
	if err != nil {
		t.Fatalf("read attribution columns: %v", err)
	}
	if agentSession != nil || originatingHook != nil || causationID != nil || correlationID != nil {
		t.Errorf("attribution = (%v, %v, %v, %v), want all null",
			agentSession, originatingHook, causationID, correlationID)
	}
	if hop != 0 {
		t.Errorf("hop = %d, want 0", hop)
	}
}

// --- item_event_values ---

// The values an event set live beside the event, keyed by (item_id, item_seq).
// TestCreate_EventPayloadHasFieldIDsButNoValues still guards the payload.
func TestCreate_StoresFieldValuesBesideTheEvent(t *testing.T) {
	ctx := testCtx(t)
	k := itemservice.New(pool)
	req := newRequest()

	res, err := k.Create(ctx, req)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	var values map[string]any
	err = pool.QueryRow(ctx,
		`select field_values from item_event_values where item_id = $1 and item_seq = $2`,
		res.ItemID, res.ItemSeq).Scan(&values)
	if err != nil {
		t.Fatalf("read item_event_values: %v", err)
	}
	if !reflect.DeepEqual(values, req.Fields) {
		t.Errorf("field_values = %v, want %v", values, req.Fields)
	}
}

// An event that sets no field has no values row.
func TestCreate_WithoutFields_StoresNoValuesRow(t *testing.T) {
	ctx := testCtx(t)
	k := itemservice.New(pool)
	req := newRequest()
	req.Fields = nil

	res, err := k.Create(ctx, req)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if n := count(t, `select count(*) from item_event_values where item_id = $1`, res.ItemID); n != 0 {
		t.Errorf("item_event_values rows = %d, want 0", n)
	}
	assertOneWrite(t, res.ItemID, req.IdempotencyKey)
}

// Erasing personal data deletes values rows; the event log stays intact.
func TestErasingFieldValues_LeavesTheEventLogIntact(t *testing.T) {
	ctx := testCtx(t)
	k := itemservice.New(pool)

	res, err := k.Create(ctx, newRequest())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	tag, err := pool.Exec(ctx, `delete from item_event_values where item_id = $1`, res.ItemID)
	if err != nil {
		t.Fatalf("erase values: %v", err)
	}
	if tag.RowsAffected() != 1 {
		t.Errorf("erased rows = %d, want 1", tag.RowsAffected())
	}

	if n := count(t, `select count(*) from item_events where item_id = $1`, res.ItemID); n != 1 {
		t.Errorf("item_events rows after erasure = %d, want 1", n)
	}
}

// --- commit counter ---

// commit_seq is the counter's value, not a sequence's. Tests in this package
// run one after another, so nothing else moves the counter in between.
func TestCreate_CommitSeqComesFromTheCounter(t *testing.T) {
	ctx := testCtx(t)
	k := itemservice.New(pool)

	var before int64
	if err := pool.QueryRow(ctx, `select last_seq from commit_counter`).Scan(&before); err != nil {
		t.Fatalf("read commit_counter: %v", err)
	}

	res, err := k.Create(ctx, newRequest())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	var after int64
	if err := pool.QueryRow(ctx, `select last_seq from commit_counter`).Scan(&after); err != nil {
		t.Fatalf("read commit_counter: %v", err)
	}
	if res.CommitSeq != before+1 || after != res.CommitSeq {
		t.Errorf("counter %d -> %d, CommitSeq = %d; want CommitSeq = counter = %d",
			before, after, res.CommitSeq, before+1)
	}

	// No sequence is left behind the column.
	var columnDefault *string
	err = pool.QueryRow(ctx,
		`select column_default from information_schema.columns
		 where table_name = 'item_events' and column_name = 'commit_seq'`).Scan(&columnDefault)
	if err != nil {
		t.Fatalf("read column default: %v", err)
	}
	if columnDefault != nil {
		t.Errorf("item_events.commit_seq default = %q, want none", *columnDefault)
	}
}

// A write holds the counter row until it commits, so commit_seq values become
// visible in order. We hold the row ourselves and check that Create waits.
func TestCreate_WaitsForTheCommitCounterLock(t *testing.T) {
	ctx := testCtx(t)
	k := itemservice.New(pool)

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	// Rollback after Commit is a harmless no-op, so this is safe on every path.
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `select last_seq from commit_counter for update`); err != nil {
		t.Fatalf("lock commit_counter: %v", err)
	}

	// A channel carries the goroutine's result back, like a Future.
	done := make(chan error, 1)
	go func() {
		_, err := k.Create(ctx, newRequest())
		done <- err
	}()

	// select waits for whichever channel is ready first.
	select {
	case err := <-done:
		t.Fatalf("Create finished while the counter was locked (err = %v)", err)
	case <-time.After(300 * time.Millisecond):
		// Still waiting, as expected.
	}

	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("release lock: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Create after release: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Create did not finish after the counter was released")
	}
}

// --- indexes ---

func TestSchema_HasFollowUpIndexes(t *testing.T) {
	for _, name := range []string{"outbox_pending", "items_container"} {
		if n := count(t, `select count(*) from pg_indexes where indexname = $1`, name); n != 1 {
			t.Errorf("index %s: found %d, want 1", name, n)
		}
	}
}
