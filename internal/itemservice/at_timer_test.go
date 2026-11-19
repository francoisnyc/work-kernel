// Tests for Jira part 2, step 1: the item service's side of a timer at a
// field's instant (docs/design.md, "Decisions for Jira part 2", primitive 2).
//
//   - AtInstant is the one read that gives the workflow a field's value:
//     the instant of the field the Item's CURRENT state names in `at`, and
//     nothing else. The caller names no field.
//   - An `at` timer never applies before its instant. When system:timer
//     makes a state's `at` move and the write's time (the time its event
//     would carry) is still before the instant, the answer is ErrNotYet
//     with the remainder: nothing is written, the key is not used up. So a
//     worker whose clock runs ahead of the item service's can never record
//     the move before the instant.
//   - With the field unset there is no instant to have passed: the timer's
//     picture is old (ErrStale), and the workflow looks again.
//   - A person making the same move by hand is not a timer: no such check.
package itemservice_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

const atTypeYAML = `
id: TYPE
version: 1
initial_state: s_planned
fields:
  - {id: f_name,      type: string}
  - {id: f_end_at,    type: datetime}
  - {id: f_other_at,  type: datetime}
  - {id: f_closed_at, type: datetime, writable_by: kernel}
states:
  - {id: s_planned, category: backlog}
  - {id: s_active,  category: active, at: {field: f_end_at, to: s_closed}}
  - {id: s_closed,  category: done}
transitions:
  - {from: s_planned, to: s_active}
  - {from: s_active,  to: s_closed, sets: {f_closed_at: 'cause.at'}}
  - {from: s_active,  to: s_planned}
`

type atWorld struct {
	k   *itemservice.Service
	typ string
	lea item.Principal
}

// newAtWorld registers the Type and returns a world whose service's clock
// is skewed by skew (zero: none).
func newAtWorld(t *testing.T, skew time.Duration) atWorld {
	t.Helper()
	k := itemservice.NewWithOptions(pool, itemservice.Options{CacheTypes: true, ClockSkew: skew})
	w := atWorld{k: k, typ: "at-" + uuid.NewString()[:8], lea: in(person("lea"), newNamespace(t, k))}
	ty, err := typedef.Parse([]byte(strings.Replace(atTypeYAML, "TYPE", w.typ, 1)))
	if err != nil {
		t.Fatalf("parse at-timer test Type: %v", err)
	}
	if err := k.RegisterType(testCtx(t), ty); err != nil {
		t.Fatalf("register at-timer test Type: %v", err)
	}
	return w
}

// active creates an iteration ending at `end` (nil: no end) and starts it:
// event 2 enters s_active.
func (w atWorld) active(t *testing.T, end *time.Time) uuid.UUID {
	t.Helper()
	fields := map[string]any{"f_name": "iteration " + word()}
	if end != nil {
		fields["f_end_at"] = end.UTC().Format(time.RFC3339Nano)
	}
	res, err := w.k.Create(testCtx(t), itemservice.CreateRequest{
		IdempotencyKey: newKey(), Principal: w.lea, TypeID: w.typ, TypeVersion: 1, Fields: fields,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := w.k.Transition(testCtx(t), itemservice.TransitionRequest{
		IdempotencyKey: newKey(), Principal: w.lea, ItemID: res.ItemID, ToState: "s_active",
	}); err != nil {
		t.Fatalf("start: %v", err)
	}
	return res.ItemID
}

// fire is the timer's move, as the FireTimer activity makes it.
func (w atWorld) fire(t *testing.T, id uuid.UUID, key string) error {
	t.Helper()
	_, err := w.k.Internal().Transition(testCtx(t), itemservice.TransitionRequest{
		IdempotencyKey: key, Principal: item.Principal{Actor: "system:timer", Namespace: w.lea.Namespace},
		ItemID: id, ToState: "s_closed",
		Attribution: item.Attribution{
			OriginatingHook: fmt.Sprintf("timer:%s@v1:s_active", w.typ),
			CausationID:     fmt.Sprintf("%s:2", id), Hop: 1,
		},
	})
	return err
}

// micros truncates to what a datetime field holds.
func micros(t time.Time) time.Time { return t.UTC().Truncate(time.Microsecond) }

// --- AtInstant ---

func TestAtInstant_ReadsTheCurrentStatesField(t *testing.T) {
	w := newAtWorld(t, 0)
	end := micros(time.Now().Add(time.Hour))
	id := w.active(t, &end)

	got, err := w.k.AtInstant(testCtx(t), id)
	if err != nil {
		t.Fatalf("AtInstant: %v", err)
	}
	if !got.Set || !got.At.Equal(end) || got.State != "s_active" || got.ItemSeq != 2 {
		t.Errorf("AtInstant = %+v, want set, %v, s_active, item_seq 2", got, end)
	}

	// Moved: the new instant, at the new item_seq. Another datetime field
	// changes nothing: only the field the state names is read.
	later := end.Add(30 * time.Minute)
	if _, err := w.k.Update(testCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: w.lea, ItemID: id,
		Fields: map[string]any{"f_end_at": later.Format(time.RFC3339Nano), "f_other_at": "2020-01-01T00:00:00Z"},
	}); err != nil {
		t.Fatalf("move the end: %v", err)
	}
	if got, err = w.k.AtInstant(testCtx(t), id); err != nil || !got.Set || !got.At.Equal(later) || got.ItemSeq != 3 {
		t.Errorf("after the move: AtInstant = %+v, %v; want %v at item_seq 3", got, err, later)
	}

	// Cleared: nothing is armed.
	if _, err := w.k.Update(testCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: w.lea, ItemID: id, Fields: map[string]any{"f_end_at": nil},
	}); err != nil {
		t.Fatalf("clear the end: %v", err)
	}
	if got, err = w.k.AtInstant(testCtx(t), id); err != nil || got.Set || !got.At.IsZero() || got.ItemSeq != 4 {
		t.Errorf("after the clear: AtInstant = %+v, %v; want unset at item_seq 4", got, err)
	}
}

// In a state without `at` there is nothing to read, even when the field
// has a value: the answer holds no instant.
func TestAtInstant_OnlyInAStateThatNamesAField(t *testing.T) {
	w := newAtWorld(t, 0)
	end := micros(time.Now().Add(time.Hour))
	id := w.active(t, &end)
	if _, err := w.k.Transition(testCtx(t), itemservice.TransitionRequest{
		IdempotencyKey: newKey(), Principal: w.lea, ItemID: id, ToState: "s_planned",
	}); err != nil {
		t.Fatalf("back to planned: %v", err)
	}
	got, err := w.k.AtInstant(testCtx(t), id)
	if err != nil || got.Set || !got.At.IsZero() || got.State != "s_planned" {
		t.Errorf("AtInstant in s_planned = %+v, %v; want no instant", got, err)
	}
	if _, err := w.k.AtInstant(testCtx(t), uuid.New()); !errors.Is(err, itemservice.ErrNotFound) {
		t.Errorf("AtInstant of no Item: %v, want ErrNotFound", err)
	}
}

// --- not yet ---

// The timer fires 400 ms early (its worker's clock runs ahead): not yet,
// with the remainder; nothing written. The same key, once the instant has
// passed, makes the move, and the event's time is not before the instant.
func TestAtTimer_NeverAppliesBeforeItsInstant(t *testing.T) {
	w := newAtWorld(t, 0)
	end := micros(time.Now().Add(400 * time.Millisecond))
	id := w.active(t, &end)

	key := newKey()
	err := w.fire(t, id, key)
	var notYet *itemservice.NotYetError
	if !errors.As(err, &notYet) || !errors.Is(err, itemservice.ErrNotYet) {
		t.Fatalf("an early firing: %v, want ErrNotYet", err)
	}
	if errors.Is(err, itemservice.ErrRejected) || errors.Is(err, itemservice.ErrStale) || errors.Is(err, itemservice.ErrInvalid) {
		t.Errorf("ErrNotYet must be none of rejected, stale, invalid: %v", err)
	}
	if notYet.Remaining <= 0 || notYet.Remaining > 400*time.Millisecond {
		t.Errorf("Remaining = %v, want more than zero and at most 400ms", notYet.Remaining)
	}
	assertUnchanged(t, "after not yet", id, "s_active", 2)

	time.Sleep(time.Until(end) + 20*time.Millisecond)
	if err := w.fire(t, id, key); err != nil {
		t.Fatalf("the same key after the instant: %v", err)
	}
	events := history(t, w.k, w.lea, id)
	closing := events[len(events)-1]
	if closing.To != "s_closed" || closing.Actor != "system:timer" {
		t.Fatalf("the last event = %+v, want the timer's closing move", closing)
	}
	if closing.CreatedAt.Before(end) {
		t.Errorf("the closing event is stamped %v, before the instant %v", closing.CreatedAt, end)
	}
	// cause.at is that same time.
	if got := fieldsOf(t, w.k, w.lea, id)["f_closed_at"]; got != stamp(closing.CreatedAt) {
		t.Errorf("f_closed_at = %v, want the event's time %s", got, stamp(closing.CreatedAt))
	}
}

// The clocks disagree: the item service's runs 300 ms behind. The instant
// has passed by the worker's clock (real time here) and the timer fires;
// by the item service's it has not. The move waits for the item service's
// clock, and is stamped at or after the instant by the clock that stamps.
func TestAtTimer_NotYet_WithTheServicesClockBehind(t *testing.T) {
	const skew = -300 * time.Millisecond
	w := newAtWorld(t, skew)
	// The instant, by the item service's own (skewed) clock, is 100 ms
	// before "now" in real time plus 200 ms: real now - 100 ms would be
	// past for the worker, and is still 200 ms away for the service.
	end := micros(time.Now().Add(-100 * time.Millisecond))
	id := w.active(t, &end)

	key := newKey()
	err := w.fire(t, id, key)
	var notYet *itemservice.NotYetError
	if !errors.As(err, &notYet) {
		t.Fatalf("a firing the worker thinks is due: %v, want ErrNotYet (the service's clock is 300 ms behind)", err)
	}
	if notYet.Remaining <= 0 || notYet.Remaining > 200*time.Millisecond {
		t.Errorf("Remaining = %v, want more than zero and at most 200ms", notYet.Remaining)
	}
	assertUnchanged(t, "after not yet", id, "s_active", 2)

	// Arm again for the remainder, as the workflow does.
	time.Sleep(notYet.Remaining + 10*time.Millisecond)
	if err := w.fire(t, id, key); err != nil {
		t.Fatalf("after the remainder: %v", err)
	}
	at, _ := eventTimes(t, id)
	if closing := at[len(at)-1]; closing.Before(end) {
		t.Errorf("the closing event is stamped %v, before the instant %v", closing, end)
	}
}

// An instant already past: the timer's move is made at once.
func TestAtTimer_APastInstantFires(t *testing.T) {
	w := newAtWorld(t, 0)
	end := micros(time.Now().Add(-time.Hour))
	id := w.active(t, &end)
	if err := w.fire(t, id, newKey()); err != nil {
		t.Fatalf("fire: %v", err)
	}
	if s := stateOf(t, id); s != "s_closed" {
		t.Errorf("state = %s, want s_closed", s)
	}
}

// The field is unset: there is no instant, so the timer that fires has an
// old picture. Stale, like a timer that lost a race; nothing is written.
func TestAtTimer_UnsetFieldIsStale(t *testing.T) {
	w := newAtWorld(t, 0)
	id := w.active(t, nil)
	err := w.fire(t, id, newKey())
	if !errors.Is(err, itemservice.ErrStale) || errors.Is(err, itemservice.ErrHeld) {
		t.Fatalf("a firing with the field unset: %v, want ErrStale", err)
	}
	assertUnchanged(t, "after the stale firing", id, "s_active", 2)
}

// A person closes by hand before the instant: that is not the timer, and
// the transition is open to them.
func TestAtTimer_ByHandIsNotChecked(t *testing.T) {
	w := newAtWorld(t, 0)
	end := micros(time.Now().Add(time.Hour))
	id := w.active(t, &end)
	if _, err := w.k.Transition(testCtx(t), itemservice.TransitionRequest{
		IdempotencyKey: newKey(), Principal: w.lea, ItemID: id, ToState: "s_closed",
	}); err != nil {
		t.Fatalf("close by hand before the end: %v", err)
	}
}

// A duration timer's move is not an `at` move: system:timer leaving a state
// with `timer` is never answered "not yet" (ticket@1's in_progress -> open).
func TestAtTimer_DurationTimersAreNotChecked(t *testing.T) {
	k := itemservice.New(pool)
	id := createTicket(t, k, map[string]any{"f_title": "t " + word(), "f_assignee": "user-bob"})
	if _, err := k.Transition(testCtx(t), itemservice.TransitionRequest{
		IdempotencyKey: newKey(), Principal: alice, ItemID: id, ToState: "in_progress",
	}); err != nil {
		t.Fatalf("start: %v", err)
	}
	_, err := k.Internal().Transition(testCtx(t), itemservice.TransitionRequest{
		IdempotencyKey: newKey(), Principal: item.Principal{Actor: "system:timer"}, ItemID: id, ToState: "open",
		Attribution: item.Attribution{
			OriginatingHook: "timer:ticket@v1:in_progress", CausationID: fmt.Sprintf("%s:2", id), Hop: 1,
		},
	})
	if err != nil {
		t.Fatalf("a duration timer's move: %v", err)
	}
}
