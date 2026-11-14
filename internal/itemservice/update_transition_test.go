// Tests for Step 2: Types, Update and Transition.
//
// The ticket Type in types/ticket.v1.yaml is registered once in TestMain
// (see registerTicketType), so every test can create tickets.
package itemservice_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// --- helpers ---

func loadTicketType() (*typedef.Type, error) {
	data, err := os.ReadFile("../../types/ticket.v1.yaml")
	if err != nil {
		return nil, err
	}
	return typedef.Parse(data)
}

// registerTicketType is called from TestMain, after the pool is ready.
func registerTicketType(ctx context.Context) error {
	ty, err := loadTicketType()
	if err != nil {
		return err
	}
	return itemservice.New(pool).RegisterType(ctx, ty)
}

func newKey() string { return "key-" + uuid.NewString() }

// createTicket creates a ticket with the given fields and returns its ID.
func createTicket(t *testing.T, k *itemservice.Service, fields map[string]any) uuid.UUID {
	t.Helper()
	req := newRequest()
	req.Fields = fields
	res, err := k.Create(testCtx(t), req)
	if err != nil {
		t.Fatalf("create ticket: %v", err)
	}
	return res.ItemID
}

func update(t *testing.T, k *itemservice.Service, id uuid.UUID, fields map[string]any) itemservice.WriteResult {
	t.Helper()
	res, err := k.Update(testCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: alice, ItemID: id, Fields: fields,
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	return res
}

func transition(t *testing.T, k *itemservice.Service, id uuid.UUID, to string) itemservice.WriteResult {
	t.Helper()
	res, err := k.Transition(testCtx(t), itemservice.TransitionRequest{
		IdempotencyKey: newKey(), Principal: alice, ItemID: id, ToState: to,
	})
	if err != nil {
		t.Fatalf("Transition to %s: %v", to, err)
	}
	return res
}

func get(t *testing.T, k *itemservice.Service, id uuid.UUID) item.Item {
	t.Helper()
	it, err := k.Get(testCtx(t), alice, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	return it
}

// assertNothingWritten checks that the Item is still at wantSeq with that many
// events and outbox rows: a rejected write leaves no trace.
func assertNothingWritten(t *testing.T, id uuid.UUID, wantSeq int) {
	t.Helper()
	if n := count(t, `select item_seq from items where id = $1`, id); n != wantSeq {
		t.Errorf("item_seq = %d, want %d", n, wantSeq)
	}
	if n := count(t, `select count(*) from item_events where item_id = $1`, id); n != wantSeq {
		t.Errorf("item_events rows = %d, want %d", n, wantSeq)
	}
	if n := count(t, `select count(*) from outbox where item_id = $1`, id); n != wantSeq {
		t.Errorf("outbox rows = %d, want %d", n, wantSeq)
	}
}

// --- Types ---

// A Type version is immutable: registering it again is a no-op, changing it is refused.
func TestRegisterType_VersionIsImmutable(t *testing.T) {
	ctx := testCtx(t)
	k := itemservice.New(pool)
	ty, err := loadTicketType()
	if err != nil {
		t.Fatalf("load ticket Type: %v", err)
	}

	if err := k.RegisterType(ctx, ty); err != nil {
		t.Fatalf("register the same definition again: %v", err)
	}
	if n := count(t, `select count(*) from types where id = 'ticket' and version = 1`); n != 1 {
		t.Errorf("types rows = %d, want 1", n)
	}

	changed, err := typedef.Parse([]byte(`
id: ticket
version: 1
initial_state: open
states:
  - {id: open, category: todo}
`))
	if err != nil {
		t.Fatalf("parse changed Type: %v", err)
	}
	if err := k.RegisterType(ctx, changed); !errors.Is(err, itemservice.ErrInvalid) {
		t.Fatalf("register a changed definition: error = %v, want ErrInvalid", err)
	}
}

// --- Create against a Type ---

func TestCreate_UnknownType_IsRejected(t *testing.T) {
	ctx := testCtx(t)
	k := itemservice.New(pool)
	req := newRequest()
	req.TypeID = "type-" + uuid.NewString()

	_, err := k.Create(ctx, req)
	if !errors.Is(err, itemservice.ErrInvalid) {
		t.Fatalf("Create error = %v, want ErrInvalid", err)
	}
	if n := count(t, `select count(*) from items where type_id = $1`, req.TypeID); n != 0 {
		t.Errorf("items rows = %d, want 0", n)
	}
	if n := count(t, `select count(*) from idempotency where key = $1`, req.IdempotencyKey); n != 0 {
		t.Errorf("idempotency rows = %d, want 0", n)
	}
}

// The Type chooses the initial state when the request names none.
func TestCreate_WithoutState_StartsInTheInitialState(t *testing.T) {
	ctx := testCtx(t)
	k := itemservice.New(pool)
	req := newRequest()
	req.State = ""

	res, err := k.Create(ctx, req)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got := get(t, k, res.ItemID).State; got != "open" {
		t.Errorf("state = %q, want %q", got, "open")
	}
}

func TestCreate_InANonInitialState_IsRejected(t *testing.T) {
	ctx := testCtx(t)
	k := itemservice.New(pool)
	req := newRequest()
	req.State = "resolved"

	_, err := k.Create(ctx, req)
	if !errors.Is(err, itemservice.ErrRejected) {
		t.Fatalf("Create error = %v, want ErrRejected", err)
	}
	if n := count(t, `select count(*) from idempotency where key = $1`, req.IdempotencyKey); n != 0 {
		t.Errorf("idempotency rows = %d, want 0", n)
	}
}

func TestCreate_InvalidFields_AreRejected(t *testing.T) {
	ctx := testCtx(t)
	k := itemservice.New(pool)
	req := newRequest()
	req.Fields = map[string]any{"f_title": "ok", "f_ghost": "not in the Type"}

	_, err := k.Create(ctx, req)
	if !errors.Is(err, itemservice.ErrRejected) {
		t.Fatalf("Create error = %v, want ErrRejected", err)
	}
	if !strings.Contains(err.Error(), "f_ghost") {
		t.Errorf("error %q does not name the field", err)
	}
}

// --- Update ---

func TestUpdate_MergesFieldsAndRecordsTheEvent(t *testing.T) {
	ctx := testCtx(t)
	k := itemservice.New(pool)
	id := createTicket(t, k, map[string]any{"f_title": "Call Jane Doe", "f_priority": 2})

	var createdAt time.Time
	if err := pool.QueryRow(ctx, `select updated_at from items where id = $1`, id).Scan(&createdAt); err != nil {
		t.Fatalf("read updated_at: %v", err)
	}

	// Set one field, change one, clear one (nil), leave nothing else touched.
	res, err := k.Update(ctx, itemservice.UpdateRequest{
		IdempotencyKey: newKey(),
		Principal:      item.Principal{Actor: "agent:7", OnBehalfOf: "user:alice"},
		ItemID:         id,
		Fields:         map[string]any{"f_assignee": "user-bob", "f_priority": 3, "f_title": nil},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if res.ItemID != id || res.ItemSeq != 2 || res.CommitSeq <= 0 {
		t.Errorf("result = %+v, want item %s at item_seq 2", res, id)
	}

	got := get(t, k, id)
	wantFields := map[string]any{"f_assignee": "user-bob", "f_priority": float64(3)}
	if !reflect.DeepEqual(got.Fields, wantFields) || got.ItemSeq != 2 || got.State != "open" {
		t.Errorf("item = %+v, want fields %v at item_seq 2 in state open", got, wantFields)
	}

	// The event: who acted, and the shape of the change without any value.
	var (
		commitSeq  int64
		actor, op  string
		onBehalfOf *string
		hop        int
		raw        []byte
	)
	err = pool.QueryRow(ctx,
		`select commit_seq, actor, on_behalf_of, hop, op, payload from item_events
		 where item_id = $1 and item_seq = 2`, id,
	).Scan(&commitSeq, &actor, &onBehalfOf, &hop, &op, &raw)
	if err != nil {
		t.Fatalf("read event: %v", err)
	}
	if commitSeq != res.CommitSeq || op != "update" || hop != 0 {
		t.Errorf("event = (commit_seq %d, op %q, hop %d), want (%d, update, 0)", commitSeq, op, hop, res.CommitSeq)
	}
	if actor != "agent:7" || onBehalfOf == nil || *onBehalfOf != "user:alice" {
		t.Errorf("event actor = %q, on_behalf_of = %v", actor, onBehalfOf)
	}
	var payload struct {
		FieldIDs        []string `json:"field_ids"`
		ClearedFieldIDs []string `json:"cleared_field_ids"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("decode payload %s: %v", raw, err)
	}
	if !reflect.DeepEqual(payload.FieldIDs, []string{"f_assignee", "f_priority"}) ||
		!reflect.DeepEqual(payload.ClearedFieldIDs, []string{"f_title"}) {
		t.Errorf("payload = %s, want field_ids [f_assignee f_priority] and cleared_field_ids [f_title]", raw)
	}
	if strings.Contains(string(raw), "user-bob") {
		t.Errorf("payload contains a field value: %s", raw)
	}

	// The values this event set live beside it.
	var values map[string]any
	err = pool.QueryRow(ctx,
		`select field_values from item_event_values where item_id = $1 and item_seq = 2`, id).Scan(&values)
	if err != nil {
		t.Fatalf("read item_event_values: %v", err)
	}
	if !reflect.DeepEqual(values, wantFields) {
		t.Errorf("field_values = %v, want %v", values, wantFields)
	}

	if n := count(t,
		`select count(*) from outbox where item_id = $1 and item_seq = 2 and delivered_at is null`, id); n != 1 {
		t.Errorf("undelivered outbox rows for item_seq 2 = %d, want 1", n)
	}

	var updatedAt time.Time
	if err := pool.QueryRow(ctx, `select updated_at from items where id = $1`, id).Scan(&updatedAt); err != nil {
		t.Fatalf("read updated_at: %v", err)
	}
	if !updatedAt.After(createdAt) {
		t.Errorf("updated_at = %v, want later than %v", updatedAt, createdAt)
	}
}

func TestUpdate_InvalidFields_AreRejected(t *testing.T) {
	cases := []struct {
		name   string
		fields map[string]any
		want   string // the field the reason must name
	}{
		{"unknown field", map[string]any{"f_ghost": "x"}, "f_ghost"},
		{"number for a string", map[string]any{"f_title": 42}, "f_title"},
		{"string for an int", map[string]any{"f_priority": "high"}, "f_priority"},
		{"fraction for an int", map[string]any{"f_priority": 1.5}, "f_priority"},
		// Above 2^53 a JSON number no longer round-trips exactly; refuse it
		// rather than store a different number.
		{"int beyond the exact range", map[string]any{"f_priority": float64(1 << 53)}, "f_priority"},
		{"string for a bool", map[string]any{"f_urgent": "yes"}, "f_urgent"},
		{"value outside the enum", map[string]any{"f_resolution": "magic"}, "f_resolution"},
		{"one bad field among good ones", map[string]any{"f_title": "ok", "f_priority": "high"}, "f_priority"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			k := itemservice.New(pool)
			id := createTicket(t, k, map[string]any{"f_title": "Printer on fire"})

			_, err := k.Update(testCtx(t), itemservice.UpdateRequest{
				IdempotencyKey: newKey(), Principal: alice, ItemID: id, Fields: c.fields,
			})
			if !errors.Is(err, itemservice.ErrRejected) {
				t.Fatalf("Update error = %v, want ErrRejected", err)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %q does not name %s", err, c.want)
			}
			assertNothingWritten(t, id, 1)
		})
	}
}

func TestUpdate_MalformedRequest_IsInvalid(t *testing.T) {
	k := itemservice.New(pool)
	id := createTicket(t, k, nil)
	good := itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: alice, ItemID: id, Fields: map[string]any{"f_title": "x"},
	}

	noKey := good
	noKey.IdempotencyKey = ""
	noActor := good
	noActor.Principal = item.Principal{}
	noFields := good
	noFields.Fields = nil

	for name, req := range map[string]itemservice.UpdateRequest{"no key": noKey, "no actor": noActor, "no fields": noFields} {
		t.Run(name, func(t *testing.T) {
			if _, err := k.Update(testCtx(t), req); !errors.Is(err, itemservice.ErrInvalid) {
				t.Fatalf("Update error = %v, want ErrInvalid", err)
			}
			assertNothingWritten(t, id, 1)
		})
	}
}

func TestUpdate_SameKeyTwice_ReturnsSameResultAndWritesOnce(t *testing.T) {
	ctx := testCtx(t)
	k := itemservice.New(pool)
	id := createTicket(t, k, nil)
	req := itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: alice, ItemID: id, Fields: map[string]any{"f_title": "x"},
	}

	first, err := k.Update(ctx, req)
	if err != nil {
		t.Fatalf("first Update: %v", err)
	}
	second, err := k.Update(ctx, req)
	if err != nil {
		t.Fatalf("second Update: %v", err)
	}
	if second != first {
		t.Errorf("second result = %+v, want %+v", second, first)
	}
	assertNothingWritten(t, id, 2) // the create and one update
}

func TestUpdate_UnknownItem_ReturnsNotFound(t *testing.T) {
	k := itemservice.New(pool)
	req := itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: alice, ItemID: uuid.New(), Fields: map[string]any{"f_title": "x"},
	}
	if _, err := k.Update(testCtx(t), req); !errors.Is(err, itemservice.ErrNotFound) {
		t.Fatalf("Update error = %v, want ErrNotFound", err)
	}
	if n := count(t, `select count(*) from idempotency where key = $1`, req.IdempotencyKey); n != 0 {
		t.Errorf("idempotency rows = %d, want 0", n)
	}
}

// Kernel rule: the Item row is locked during a write, so item_seq has no gaps.
func TestUpdate_Concurrent_ItemSeqHasNoGaps(t *testing.T) {
	ctx := testCtx(t)
	k := itemservice.New(pool)
	id := createTicket(t, k, nil)

	const writers = 8
	seqs := make([]int64, writers)
	errs := make([]error, writers)
	var wg sync.WaitGroup
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := k.Update(ctx, itemservice.UpdateRequest{
				IdempotencyKey: newKey(), Principal: alice, ItemID: id,
				Fields: map[string]any{"f_priority": i},
			})
			seqs[i], errs[i] = res.ItemSeq, err
		}()
	}
	wg.Wait()

	seen := map[int64]bool{}
	for i := range writers {
		if errs[i] != nil {
			t.Fatalf("writer %d: %v", i, errs[i])
		}
		seen[seqs[i]] = true
	}
	// The create took item_seq 1, so the updates must hold exactly 2..9.
	for seq := int64(2); seq <= writers+1; seq++ {
		if !seen[seq] {
			t.Errorf("no update got item_seq %d (got %v)", seq, seqs)
		}
	}
	assertNothingWritten(t, id, writers+1)

	// commit_seq grows with item_seq: the log of one Item is in commit order.
	if n := count(t,
		`select count(*) from item_events a join item_events b
		   on a.item_id = b.item_id and a.item_seq < b.item_seq and a.commit_seq >= b.commit_seq
		 where a.item_id = $1`, id); n != 0 {
		t.Errorf("%d event pairs have commit_seq out of item_seq order", n)
	}
}

// --- Transition ---

// Step 2 "done when": legal transitions are recorded, with attribution.
func TestTransition_Legal_IsRecorded(t *testing.T) {
	ctx := testCtx(t)
	k := itemservice.New(pool)
	id := createTicket(t, k, map[string]any{"f_title": "Printer on fire", "f_assignee": "user-bob"})

	res, err := k.Transition(ctx, itemservice.TransitionRequest{
		IdempotencyKey: newKey(),
		Principal:      item.Principal{Actor: "agent:7", OnBehalfOf: "user:alice"},
		ItemID:         id,
		ToState:        "in_progress",
	})
	if err != nil {
		t.Fatalf("Transition: %v", err)
	}
	if res.ItemID != id || res.ItemSeq != 2 || res.CommitSeq <= 0 {
		t.Errorf("result = %+v, want item %s at item_seq 2", res, id)
	}

	got := get(t, k, id)
	if got.State != "in_progress" || got.ItemSeq != 2 {
		t.Errorf("item state = %q at item_seq %d, want in_progress at 2", got.State, got.ItemSeq)
	}
	if !reflect.DeepEqual(got.Fields, map[string]any{"f_title": "Printer on fire", "f_assignee": "user-bob"}) {
		t.Errorf("a transition changed the fields: %v", got.Fields)
	}

	var (
		commitSeq                                                 int64
		actor, op                                                 string
		onBehalfOf                                                *string
		agentSession, originatingHook, causationID, correlationID *string
		hop                                                       int
		raw                                                       []byte
	)
	err = pool.QueryRow(ctx,
		`select commit_seq, actor, on_behalf_of, agent_session, originating_hook,
		        causation_id, correlation_id, hop, op, payload
		 from item_events where item_id = $1 and item_seq = 2`, id,
	).Scan(&commitSeq, &actor, &onBehalfOf, &agentSession, &originatingHook,
		&causationID, &correlationID, &hop, &op, &raw)
	if err != nil {
		t.Fatalf("read event: %v", err)
	}
	if commitSeq != res.CommitSeq || op != "transition" {
		t.Errorf("event = (commit_seq %d, op %q), want (%d, transition)", commitSeq, op, res.CommitSeq)
	}
	if actor != "agent:7" || onBehalfOf == nil || *onBehalfOf != "user:alice" {
		t.Errorf("event actor = %q, on_behalf_of = %v", actor, onBehalfOf)
	}
	if agentSession != nil || originatingHook != nil || causationID != nil || correlationID != nil || hop != 0 {
		t.Errorf("unfilled attribution columns should be null with hop 0")
	}
	var payload struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("decode payload %s: %v", raw, err)
	}
	if payload.From != "open" || payload.To != "in_progress" {
		t.Errorf("payload = %s, want from open to in_progress", raw)
	}

	// A transition sets no field, so it has no values row.
	if n := count(t,
		`select count(*) from item_event_values where item_id = $1 and item_seq = 2`, id); n != 0 {
		t.Errorf("item_event_values rows for the transition = %d, want 0", n)
	}
	if n := count(t,
		`select count(*) from outbox where item_id = $1 and item_seq = 2 and delivered_at is null`, id); n != 1 {
		t.Errorf("undelivered outbox rows for item_seq 2 = %d, want 1", n)
	}
}

// Step 2 "done when": illegal transitions are rejected with a reason.
func TestTransition_Illegal_IsRejectedWithAReason(t *testing.T) {
	cases := []struct {
		name         string
		fields       map[string]any // the ticket's fields; it is in state "open"
		to           string
		wantInReason []string
	}{
		{"no such transition", map[string]any{"f_assignee": "user-bob"}, "resolved", []string{"open", "resolved"}},
		{"unknown state", nil, "limbo", []string{"limbo"}},
		{"same state", nil, "open", []string{"open"}},
		{"guard fails", map[string]any{"f_title": "No assignee yet"}, "in_progress", []string{"f_assignee"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			k := itemservice.New(pool)
			id := createTicket(t, k, c.fields)

			_, err := k.Transition(testCtx(t), itemservice.TransitionRequest{
				IdempotencyKey: newKey(), Principal: alice, ItemID: id, ToState: c.to,
			})
			if !errors.Is(err, itemservice.ErrRejected) {
				t.Fatalf("Transition error = %v, want ErrRejected", err)
			}
			for _, want := range c.wantInReason {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not mention %q", err, want)
				}
			}
			if got := get(t, k, id).State; got != "open" {
				t.Errorf("state = %q, want it unchanged (open)", got)
			}
			assertNothingWritten(t, id, 1)
		})
	}
}

func TestTransition_MissingRequiredField_IsRejected(t *testing.T) {
	k := itemservice.New(pool)
	id := createTicket(t, k, map[string]any{"f_assignee": "user-bob"})
	transition(t, k, id, "in_progress")

	_, err := k.Transition(testCtx(t), itemservice.TransitionRequest{
		IdempotencyKey: newKey(), Principal: alice, ItemID: id, ToState: "resolved",
	})
	if !errors.Is(err, itemservice.ErrRejected) {
		t.Fatalf("Transition error = %v, want ErrRejected", err)
	}
	if !strings.Contains(err.Error(), "f_resolution") {
		t.Errorf("error %q does not name the missing field", err)
	}
	if got := get(t, k, id).State; got != "in_progress" {
		t.Errorf("state = %q, want it unchanged (in_progress)", got)
	}
	assertNothingWritten(t, id, 2)
}

// A rejected write does not use up its key: the client fixes the Item and
// retries with the same key.
func TestTransition_RejectionDoesNotConsumeTheKey(t *testing.T) {
	ctx := testCtx(t)
	k := itemservice.New(pool)
	id := createTicket(t, k, nil)
	req := itemservice.TransitionRequest{
		IdempotencyKey: newKey(), Principal: alice, ItemID: id, ToState: "in_progress",
	}

	if _, err := k.Transition(ctx, req); !errors.Is(err, itemservice.ErrRejected) {
		t.Fatalf("first Transition error = %v, want ErrRejected", err)
	}
	if n := count(t, `select count(*) from idempotency where key = $1`, req.IdempotencyKey); n != 0 {
		t.Errorf("idempotency rows after a rejection = %d, want 0", n)
	}

	update(t, k, id, map[string]any{"f_assignee": "user-bob"})

	res, err := k.Transition(ctx, req)
	if err != nil {
		t.Fatalf("retry with the same key: %v", err)
	}
	if res.ItemSeq != 3 {
		t.Errorf("ItemSeq = %d, want 3 (create, update, transition)", res.ItemSeq)
	}
}

// A retry returns the stored result even after the Item has moved on.
func TestTransition_SameKeyTwice_ReturnsSameResultAndWritesOnce(t *testing.T) {
	ctx := testCtx(t)
	k := itemservice.New(pool)
	id := createTicket(t, k, map[string]any{"f_assignee": "user-bob"})
	req := itemservice.TransitionRequest{
		IdempotencyKey: newKey(), Principal: alice, ItemID: id, ToState: "in_progress",
	}

	first, err := k.Transition(ctx, req)
	if err != nil {
		t.Fatalf("first Transition: %v", err)
	}
	transition(t, k, id, "open") // the Item moves on

	second, err := k.Transition(ctx, req)
	if err != nil {
		t.Fatalf("second Transition: %v", err)
	}
	if second != first {
		t.Errorf("second result = %+v, want %+v", second, first)
	}
	if got := get(t, k, id).State; got != "open" {
		t.Errorf("state = %q, want open: the retry must not transition again", got)
	}
	assertNothingWritten(t, id, 3)
}

func TestTransition_UnknownItem_ReturnsNotFound(t *testing.T) {
	k := itemservice.New(pool)
	_, err := k.Transition(testCtx(t), itemservice.TransitionRequest{
		IdempotencyKey: newKey(), Principal: alice, ItemID: uuid.New(), ToState: "in_progress",
	})
	if !errors.Is(err, itemservice.ErrNotFound) {
		t.Fatalf("Transition error = %v, want ErrNotFound", err)
	}
}

func TestTransition_MalformedRequest_IsInvalid(t *testing.T) {
	k := itemservice.New(pool)
	id := createTicket(t, k, map[string]any{"f_assignee": "user-bob"})
	good := itemservice.TransitionRequest{
		IdempotencyKey: newKey(), Principal: alice, ItemID: id, ToState: "in_progress",
	}

	noKey := good
	noKey.IdempotencyKey = ""
	noActor := good
	noActor.Principal = item.Principal{}
	noState := good
	noState.ToState = ""

	for name, req := range map[string]itemservice.TransitionRequest{"no key": noKey, "no actor": noActor, "no state": noState} {
		t.Run(name, func(t *testing.T) {
			if _, err := k.Transition(testCtx(t), req); !errors.Is(err, itemservice.ErrInvalid) {
				t.Fatalf("Transition error = %v, want ErrInvalid", err)
			}
			assertNothingWritten(t, id, 1)
		})
	}
}

// One key names one request, whatever the operation.
func TestWrite_KeyReusedForAnotherOperation_IsRejected(t *testing.T) {
	ctx := testCtx(t)
	k := itemservice.New(pool)
	id := createTicket(t, k, map[string]any{"f_assignee": "user-bob"})
	key := newKey()

	_, err := k.Update(ctx, itemservice.UpdateRequest{
		IdempotencyKey: key, Principal: alice, ItemID: id, Fields: map[string]any{"f_title": "x"},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	_, err = k.Transition(ctx, itemservice.TransitionRequest{
		IdempotencyKey: key, Principal: alice, ItemID: id, ToState: "in_progress",
	})
	if !errors.Is(err, itemservice.ErrIdempotencyKeyReuse) {
		t.Fatalf("Transition error = %v, want ErrIdempotencyKeyReuse", err)
	}
	assertNothingWritten(t, id, 2)
}

// --- End to end ---

// One ticket walks its whole lifecycle; history holds every accepted
// operation, in order, and none of the rejected ones.
func TestTicket_WalksItsLifecycle(t *testing.T) {
	ctx := testCtx(t)
	k := itemservice.New(pool)
	id := createTicket(t, k, map[string]any{"f_title": "Printer on fire"})

	// Rejected: nobody is assigned yet.
	_, err := k.Transition(ctx, itemservice.TransitionRequest{
		IdempotencyKey: newKey(), Principal: alice, ItemID: id, ToState: "in_progress",
	})
	if !errors.Is(err, itemservice.ErrRejected) {
		t.Fatalf("early Transition error = %v, want ErrRejected", err)
	}

	update(t, k, id, map[string]any{"f_assignee": "user-bob"})
	transition(t, k, id, "in_progress")
	update(t, k, id, map[string]any{"f_resolution": "fixed"})
	last := transition(t, k, id, "resolved")

	got := get(t, k, id)
	if got.State != "resolved" || got.ItemSeq != 5 || last.ItemSeq != 5 {
		t.Errorf("item = state %q at item_seq %d, want resolved at 5", got.State, got.ItemSeq)
	}

	rows, err := pool.Query(ctx,
		`select item_seq, commit_seq, op from item_events where item_id = $1 order by item_seq`, id)
	if err != nil {
		t.Fatalf("read history: %v", err)
	}
	defer rows.Close()
	var (
		ops        []string
		lastCommit int64
	)
	for rows.Next() {
		var (
			itemSeq, commitSeq int64
			op                 string
		)
		if err := rows.Scan(&itemSeq, &commitSeq, &op); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if itemSeq != int64(len(ops)+1) {
			t.Errorf("item_seq = %d, want %d (no gaps)", itemSeq, len(ops)+1)
		}
		if commitSeq <= lastCommit {
			t.Errorf("commit_seq %d does not increase after %d", commitSeq, lastCommit)
		}
		lastCommit = commitSeq
		ops = append(ops, op)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read history: %v", err)
	}
	want := []string{"create", "update", "transition", "update", "transition"}
	if !reflect.DeepEqual(ops, want) {
		t.Errorf("history ops = %v, want %v", ops, want)
	}
}
