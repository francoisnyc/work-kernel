// Tests for Step 5: the outbox skip. "Pay for what you use": an Item whose
// Type has no listeners costs its row and its event, and no outbox row.
// lifecycle.HasListeners decides, per Type version, not per event.
package itemservice_test

import (
	"testing"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/itemservice"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// assertNothingWrittenNoOutbox is assertNothingWritten for a Type without
// listeners: the Item is still at wantSeq with that many events, and has no
// outbox rows at all.
func assertNothingWrittenNoOutbox(t *testing.T, id uuid.UUID, wantSeq int) {
	t.Helper()
	if n := count(t, `select item_seq from items where id = $1`, id); n != wantSeq {
		t.Errorf("item_seq = %d, want %d", n, wantSeq)
	}
	if n := count(t, `select count(*) from item_events where item_id = $1`, id); n != wantSeq {
		t.Errorf("item_events rows = %d, want %d", n, wantSeq)
	}
	if n := count(t, `select count(*) from outbox where item_id = $1`, id); n != 0 {
		t.Errorf("outbox rows = %d, want 0", n)
	}
}

// A message (no states, so no listeners) gets its events and values, and no
// outbox row, for every kind of write: create, field update, policy change.
func TestOutbox_TypeWithoutListeners_WritesNoOutboxRow(t *testing.T) {
	k := itemservice.New(pool)
	id := createMessage(t, k)

	update(t, k, id, map[string]any{"f_body": "edited"})
	setPolicy(t, k, alice, id, itemservice.PolicyPatch{Readers: readers(alice)})

	if n := count(t, `select count(*) from item_events where item_id = $1`, id); n != 3 {
		t.Errorf("item_events rows = %d, want 3", n)
	}
	if n := count(t, `select count(*) from item_event_values where item_id = $1`, id); n != 2 {
		t.Errorf("item_event_values rows = %d, want 2 (create and the field update)", n)
	}
	if n := count(t, `select count(*) from outbox where item_id = $1`, id); n != 0 {
		t.Errorf("outbox rows = %d, want 0", n)
	}
}

// A Type with states and transitions but no timer has no listeners either.
func TestOutbox_TypeWithStatesButNoTimer_WritesNoOutboxRow(t *testing.T) {
	k := itemservice.New(pool)
	ty, err := typedef.Parse([]byte(`
id: memo
version: 1
initial_state: draft
fields:
  - {id: f_text, type: string}
states:
  - {id: draft, category: backlog}
  - {id: sent, category: done}
transitions:
  - {from: draft, to: sent}
`))
	if err != nil {
		t.Fatalf("parse memo Type: %v", err)
	}
	if err := k.RegisterType(testCtx(t), ty); err != nil {
		t.Fatalf("register memo Type: %v", err)
	}
	res, err := k.Create(testCtx(t), itemservice.CreateRequest{
		IdempotencyKey: newKey(), Principal: alice, TypeID: "memo", TypeVersion: 1,
		Fields: map[string]any{"f_text": "hi"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	transition(t, k, res.ItemID, "sent")

	if n := count(t, `select count(*) from item_events where item_id = $1`, res.ItemID); n != 2 {
		t.Errorf("item_events rows = %d, want 2", n)
	}
	if n := count(t, `select count(*) from outbox where item_id = $1`, res.ItemID); n != 0 {
		t.Errorf("outbox rows = %d, want 0", n)
	}
}

// A Type with a timer anywhere has listeners, so every event gets an outbox
// row, including events that enter a state without a timer: a workflow
// already running for the Item must hear about them.
func TestOutbox_TypeWithListeners_WritesARowForEveryEvent(t *testing.T) {
	k := itemservice.New(pool)
	id := createTicket(t, k, map[string]any{"f_title": "printer"}) // open: no timer
	update(t, k, id, map[string]any{"f_assignee": "user-bob"})
	transition(t, k, id, "in_progress") // the state with the timer
	transition(t, k, id, "open")        // no timer again

	rows := count(t, `select count(*) from outbox where item_id = $1`, id)
	if rows != 4 {
		t.Errorf("outbox rows = %d, want 4, one per event", rows)
	}
	n := count(t, `select count(*) from outbox o join item_events e using (item_id, item_seq)
	               where o.item_id = $1`, id)
	if n != 4 {
		t.Errorf("outbox rows that match an event = %d, want 4", n)
	}
}

// The decision follows the Item's pinned Type version, not the newest one:
// a version without listeners writes none even when a later version of the
// same Type has a timer.
func TestOutbox_FollowsTheItemsTypeVersion(t *testing.T) {
	k := itemservice.New(pool)
	quiet, err := typedef.Parse([]byte(`
id: alarm
version: 1
initial_state: armed
states:
  - {id: armed, category: waiting}
  - {id: rung, category: done}
transitions:
  - {from: armed, to: rung}
`))
	if err != nil {
		t.Fatalf("parse alarm v1: %v", err)
	}
	loud, err := typedef.Parse([]byte(`
id: alarm
version: 2
initial_state: armed
states:
  - {id: armed, category: waiting, timer: {after: 1h, to: rung}}
  - {id: rung, category: done}
transitions:
  - {from: armed, to: rung}
`))
	if err != nil {
		t.Fatalf("parse alarm v2: %v", err)
	}
	for _, ty := range []*typedef.Type{quiet, loud} {
		if err := k.RegisterType(testCtx(t), ty); err != nil {
			t.Fatalf("register alarm v%d: %v", ty.Version, err)
		}
	}

	ids := map[int]uuid.UUID{}
	for _, v := range []int{1, 2} {
		res, err := k.Create(testCtx(t), itemservice.CreateRequest{
			IdempotencyKey: newKey(), Principal: alice, TypeID: "alarm", TypeVersion: v,
		})
		if err != nil {
			t.Fatalf("Create alarm v%d: %v", v, err)
		}
		ids[v] = res.ItemID
	}

	if n := count(t, `select count(*) from outbox where item_id = $1`, ids[1]); n != 0 {
		t.Errorf("v1 outbox rows = %d, want 0", n)
	}
	if n := count(t, `select count(*) from outbox where item_id = $1`, ids[2]); n != 1 {
		t.Errorf("v2 outbox rows = %d, want 1", n)
	}
}

// The skip changes nothing else about a write: item_seq and commit_seq move
// as before, and the same idempotency key returns the stored result.
func TestOutbox_SkipKeepsTheWriteContract(t *testing.T) {
	k := itemservice.New(pool)
	registerMessageType(t, k)
	req := itemservice.CreateRequest{
		IdempotencyKey: newKey(), Principal: alice, TypeID: "message", TypeVersion: 1,
		Fields: map[string]any{"f_body": "once"},
	}
	first, err := k.Create(testCtx(t), req)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	again, err := k.Create(testCtx(t), req)
	if err != nil {
		t.Fatalf("Create again: %v", err)
	}
	if again != first {
		t.Errorf("same key: result = %+v, want %+v", again, first)
	}
	if first.ItemSeq != 1 || first.CommitSeq < 1 {
		t.Errorf("result = %+v, want item_seq 1 and a commit_seq", first)
	}
	assertNothingWrittenNoOutbox(t, first.ItemID, 1)
}
