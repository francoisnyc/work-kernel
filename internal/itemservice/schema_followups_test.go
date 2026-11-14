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

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
)

var bob = item.Principal{Actor: "user:bob"}

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
	for _, actor := range []string{"user:alice", "user:bob"} {
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

// --- commit_seq ---

// Step A replaced the commit counter with a sequence and a watermark; its
// tests are in watermark_test.go.

// --- indexes ---

func TestSchema_HasFollowUpIndexes(t *testing.T) {
	for _, name := range []string{"outbox_pending", "items_container"} {
		if n := count(t, `select count(*) from pg_indexes where indexname = $1`, name); n != 1 {
			t.Errorf("index %s: found %d, want 1", name, n)
		}
	}
}
