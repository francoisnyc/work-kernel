// Tests for the PR #5 follow-up: Transition takes an optional Fields patch.
// The patch is applied and the guards are checked under one row lock, and the
// whole thing is recorded as one "transition" event.
package kernel_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/kernel"
)

// transitionEvent is the part of a "transition" event these tests look at.
type transitionEvent struct {
	Op      string
	Raw     string // the payload as stored
	Payload struct {
		From            string   `json:"from"`
		To              string   `json:"to"`
		FieldIDs        []string `json:"field_ids"`
		ClearedFieldIDs []string `json:"cleared_field_ids"`
	}
	Values map[string]any // the item_event_values row; nil if there is none
}

func readTransitionEvent(t *testing.T, id uuid.UUID, itemSeq int) transitionEvent {
	t.Helper()
	var (
		ev     transitionEvent
		raw    []byte
		values []byte
	)
	err := pool.QueryRow(testCtx(t),
		`select e.op, e.payload, v.field_values
		 from item_events e
		 left join item_event_values v on v.item_id = e.item_id and v.item_seq = e.item_seq
		 where e.item_id = $1 and e.item_seq = $2`, id, itemSeq,
	).Scan(&ev.Op, &raw, &values)
	if err != nil {
		t.Fatalf("read event %d: %v", itemSeq, err)
	}
	ev.Raw = string(raw)
	if err := json.Unmarshal(raw, &ev.Payload); err != nil {
		t.Fatalf("decode payload %s: %v", raw, err)
	}
	if values != nil {
		if err := json.Unmarshal(values, &ev.Values); err != nil {
			t.Fatalf("decode values %s: %v", values, err)
		}
	}
	return ev
}

// The patch can supply what the guard needs: one call, one event, one outbox row.
func TestTransition_WithFields_PatchSatisfiesTheGuard(t *testing.T) {
	k := kernel.New(pool)
	id := createTicket(t, k, map[string]any{"f_title": "Printer on fire"})

	// Without the patch this move is rejected: the guard is "f_assignee is set".
	res, err := k.Transition(testCtx(t), kernel.TransitionRequest{
		IdempotencyKey: newKey(), Principal: alice, ItemID: id, ToState: "in_progress",
		Fields: map[string]any{"f_assignee": "user-bob"},
	})
	if err != nil {
		t.Fatalf("Transition: %v", err)
	}
	if res.ItemSeq != 2 {
		t.Errorf("ItemSeq = %d, want 2: patch and move are one write", res.ItemSeq)
	}

	got := get(t, k, id)
	if got.State != "in_progress" || got.ItemSeq != 2 {
		t.Errorf("item = state %q at item_seq %d, want in_progress at 2", got.State, got.ItemSeq)
	}
	want := map[string]any{"f_title": "Printer on fire", "f_assignee": "user-bob"}
	if !reflect.DeepEqual(got.Fields, want) {
		t.Errorf("fields = %v, want %v", got.Fields, want)
	}

	ev := readTransitionEvent(t, id, 2)
	if ev.Op != "transition" || ev.Payload.From != "open" || ev.Payload.To != "in_progress" {
		t.Errorf("event = op %q, payload %s; want a transition from open to in_progress", ev.Op, ev.Raw)
	}
	if !reflect.DeepEqual(ev.Payload.FieldIDs, []string{"f_assignee"}) {
		t.Errorf("payload field_ids = %v, want [f_assignee]", ev.Payload.FieldIDs)
	}
	// Values never go in the permanent log.
	if strings.Contains(ev.Raw, "user-bob") {
		t.Errorf("payload holds a field value: %s", ev.Raw)
	}
	if !reflect.DeepEqual(ev.Values, map[string]any{"f_assignee": "user-bob"}) {
		t.Errorf("item_event_values = %v, want only the patched field", ev.Values)
	}
	assertNothingWritten(t, id, 2) // create + this one event; nothing more
}

// "required" fields may also arrive in the patch.
func TestTransition_WithFields_PatchSuppliesARequiredField(t *testing.T) {
	k := kernel.New(pool)
	id := createTicket(t, k, map[string]any{"f_assignee": "user-bob"})
	transition(t, k, id, "in_progress")

	res, err := k.Transition(testCtx(t), kernel.TransitionRequest{
		IdempotencyKey: newKey(), Principal: alice, ItemID: id, ToState: "resolved",
		Fields: map[string]any{"f_resolution": "fixed"},
	})
	if err != nil {
		t.Fatalf("Transition: %v", err)
	}
	got := get(t, k, id)
	if got.State != "resolved" || res.ItemSeq != 3 || got.Fields["f_resolution"] != "fixed" {
		t.Errorf("item = state %q, fields %v, item_seq %d; want resolved with f_resolution at 3",
			got.State, got.Fields, res.ItemSeq)
	}
}

// A patch can set and clear in one go; the event names both, like an update.
func TestTransition_WithFields_SetsAndClears(t *testing.T) {
	k := kernel.New(pool)
	id := createTicket(t, k, map[string]any{"f_assignee": "user-bob", "f_priority": float64(2)})

	_, err := k.Transition(testCtx(t), kernel.TransitionRequest{
		IdempotencyKey: newKey(), Principal: alice, ItemID: id, ToState: "in_progress",
		Fields: map[string]any{"f_urgent": true, "f_priority": nil},
	})
	if err != nil {
		t.Fatalf("Transition: %v", err)
	}

	want := map[string]any{"f_assignee": "user-bob", "f_urgent": true}
	if got := get(t, k, id).Fields; !reflect.DeepEqual(got, want) {
		t.Errorf("fields = %v, want %v", got, want)
	}
	ev := readTransitionEvent(t, id, 2)
	if !reflect.DeepEqual(ev.Payload.FieldIDs, []string{"f_urgent"}) ||
		!reflect.DeepEqual(ev.Payload.ClearedFieldIDs, []string{"f_priority"}) {
		t.Errorf("payload = %s, want field_ids [f_urgent] and cleared_field_ids [f_priority]", ev.Raw)
	}
	if !reflect.DeepEqual(ev.Values, map[string]any{"f_urgent": true}) {
		t.Errorf("item_event_values = %v, want only the field that was set", ev.Values)
	}
}

// Guards see the fields as they are after the patch. A rejection applies
// nothing: not the move, and not the patch either.
func TestTransition_WithFields_Rejected_WritesNothing(t *testing.T) {
	cases := []struct {
		name         string
		fields       map[string]any // the ticket's fields; it is in state "open"
		to           string
		patch        map[string]any
		wantInReason string
	}{
		{"guard still fails after the patch",
			map[string]any{"f_title": "old"}, "in_progress",
			map[string]any{"f_title": "new"}, "f_assignee"},
		{"patch clears the field the guard needs",
			map[string]any{"f_assignee": "user-bob"}, "in_progress",
			map[string]any{"f_assignee": nil}, "f_assignee"},
		{"no such transition, valid patch",
			map[string]any{"f_title": "old"}, "resolved",
			map[string]any{"f_resolution": "fixed"}, "resolved"},
		{"undeclared field, legal move",
			map[string]any{"f_assignee": "user-bob"}, "in_progress",
			map[string]any{"f_ghost": "x"}, "f_ghost"},
		{"wrong value type, legal move",
			map[string]any{"f_assignee": "user-bob"}, "in_progress",
			map[string]any{"f_priority": "high"}, "f_priority"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			k := kernel.New(pool)
			id := createTicket(t, k, c.fields)

			_, err := k.Transition(testCtx(t), kernel.TransitionRequest{
				IdempotencyKey: newKey(), Principal: alice, ItemID: id, ToState: c.to, Fields: c.patch,
			})
			if !errors.Is(err, kernel.ErrRejected) {
				t.Fatalf("Transition error = %v, want ErrRejected", err)
			}
			if !strings.Contains(err.Error(), c.wantInReason) {
				t.Errorf("error %q does not mention %q", err, c.wantInReason)
			}
			got := get(t, k, id)
			if got.State != "open" || !reflect.DeepEqual(got.Fields, c.fields) {
				t.Errorf("item = state %q, fields %v; want it unchanged (open, %v)", got.State, got.Fields, c.fields)
			}
			assertNothingWritten(t, id, 1)
		})
	}
}

// The patch is part of the request: the same key with the same patch replays,
// the same key with another patch is a different request.
func TestTransition_WithFields_Idempotency(t *testing.T) {
	ctx := testCtx(t)
	k := kernel.New(pool)
	id := createTicket(t, k, nil)
	req := kernel.TransitionRequest{
		IdempotencyKey: newKey(), Principal: alice, ItemID: id, ToState: "in_progress",
		Fields: map[string]any{"f_assignee": "user-bob"},
	}

	first, err := k.Transition(ctx, req)
	if err != nil {
		t.Fatalf("first Transition: %v", err)
	}
	second, err := k.Transition(ctx, req)
	if err != nil {
		t.Fatalf("second Transition: %v", err)
	}
	if second != first {
		t.Errorf("second result = %+v, want %+v", second, first)
	}

	other := req
	other.Fields = map[string]any{"f_assignee": "user-carol"}
	if _, err := k.Transition(ctx, other); !errors.Is(err, kernel.ErrIdempotencyKeyReuse) {
		t.Fatalf("same key, other patch: error = %v, want ErrIdempotencyKeyReuse", err)
	}
	if got := get(t, k, id).Fields["f_assignee"]; got != "user-bob" {
		t.Errorf("f_assignee = %v, want user-bob", got)
	}
	assertNothingWritten(t, id, 2)
}

// Patch and guard check share one lock: of eight racing callers exactly one
// wins, its patch is the one stored, and the losers leave no trace.
func TestTransition_WithFields_Concurrent_OneWins(t *testing.T) {
	k := kernel.New(pool)
	id := createTicket(t, k, nil)

	const callers = 8
	assignees := make([]string, callers)
	errs := make([]error, callers)
	// A WaitGroup waits for a set of goroutines, like a CountDownLatch.
	var wg sync.WaitGroup
	for i := range callers {
		assignees[i] = "user-" + uuid.NewString()
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = k.Transition(testCtx(t), kernel.TransitionRequest{
				IdempotencyKey: newKey(), Principal: alice, ItemID: id, ToState: "in_progress",
				Fields: map[string]any{"f_assignee": assignees[i]},
			})
		}()
	}
	wg.Wait()

	winner := -1
	for i, err := range errs {
		switch {
		case err == nil && winner == -1:
			winner = i
		case err == nil:
			t.Errorf("callers %d and %d both succeeded", winner, i)
		case !errors.Is(err, kernel.ErrRejected):
			t.Errorf("caller %d: error = %v, want ErrRejected", i, err)
		}
	}
	if winner == -1 {
		t.Fatal("no caller succeeded")
	}
	got := get(t, k, id)
	if got.State != "in_progress" || got.Fields["f_assignee"] != assignees[winner] {
		t.Errorf("item = state %q, f_assignee %v; want in_progress with the winner's patch (%s)",
			got.State, got.Fields["f_assignee"], assignees[winner])
	}
	assertNothingWritten(t, id, 2)
}
