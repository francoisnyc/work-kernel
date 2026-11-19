// Tests for Jira part 2, step 1: computed field effects (docs/design.md,
// "Decisions for Jira part 2", primitive 4). A transition's `sets` are
// evaluated inside its own write, at the cause's time.
//
// The order of checks, on every door:
//
//  1. malformed: a patch that sets a field the move sets or clears;
//  2. not writable, 3. frozen, 4. held: for the fields the CALLER names.
//     A field written by `sets` is written by the Type: it is exempt from
//     writable_by (kernel or roles);
//  5. required fields and the guard, on the fields as the patch leaves them;
//  6. the clears;
//  7. the sets, each evaluated on the view of step 5 (after the patch,
//     before the clears), with cause.at = the time of the event being
//     written; null unsets;
//  8. the invariants, on the fields as the write leaves them.
//
// A computed field shows in history like a patched one: in field_ids with
// its value, or in cleared_field_ids when it had a value and lost it.
package itemservice_test

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

const setsTypeYAML = `
id: TYPE
version: 1
initial_state: s_open
fields:
  - {id: f_title,       type: string}
  - {id: f_note,        type: string}
  - {id: f_resolution,  type: enum, options: [fixed, wontfix]}
  - {id: f_points,      type: int}
  - {id: f_double,      type: int}
  - {id: f_had_note,    type: bool}
  - {id: f_resolved_at, type: datetime, writable_by: kernel}
  - {id: f_grade,       type: int, writable_by: ['role:ROLE']}
states:
  - {id: s_open,   category: todo}
  - {id: s_done,   category: done}
  - {id: s_parked, category: waiting, timer: {after: 1h, to: s_done}}
  - {id: s_odd,    category: waiting}
transitions:
  - from: s_open
    to: s_done
    required: [f_resolution]
    clears: [f_note]
    sets:
      f_resolved_at: 'cause.at'
      f_double:      'f_points * 2'
      f_had_note:    'f_note != null'
      f_grade:       '1'
  # Reopen: the resolution and its time no longer stand.
  - from: s_done
    to: s_open
    clears: [f_resolution]
    sets: {f_resolved_at: 'null', f_double: 'null'}
  - {from: s_open,   to: s_parked}
  - {from: s_parked, to: s_done, sets: {f_resolved_at: 'cause.at'}}
  # An effect that is no option of its enum, and one that breaks an invariant.
  - {from: s_parked, to: s_open, sets: {f_resolution: 'f_note'}}
  - {from: s_open,   to: s_odd,  sets: {f_double: '99'}}
invariants:
  - {id: double_is_small, rule: 'f_double == null || f_double < 50'}
`

type setsWorld struct {
	k         *itemservice.Service
	typ, role string
	ana       item.Principal
}

func newSetsWorld(t *testing.T) setsWorld {
	t.Helper()
	k := itemservice.New(pool)
	sfx := uuid.NewString()[:8]
	w := setsWorld{k: k, typ: "sets-" + sfx, role: "lead-" + sfx, ana: in(person("ana"), newNamespace(t, k))}
	ty, err := typedef.Parse([]byte(strings.NewReplacer("TYPE", w.typ, "ROLE", w.role).Replace(setsTypeYAML)))
	if err != nil {
		t.Fatalf("parse sets test Type: %v", err)
	}
	if err := k.RegisterType(testCtx(t), ty); err != nil {
		t.Fatalf("register sets test Type: %v", err)
	}
	return w
}

func (w setsWorld) open(t *testing.T, fields map[string]any) uuid.UUID {
	t.Helper()
	all := map[string]any{"f_title": "chore " + word()}
	for id, v := range fields {
		all[id] = v
	}
	res, err := w.k.Create(testCtx(t), itemservice.CreateRequest{
		IdempotencyKey: newKey(), Principal: w.ana, TypeID: w.typ, TypeVersion: 1, Fields: all,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	return res.ItemID
}

func (w setsWorld) move(t *testing.T, id uuid.UUID, to string, patch map[string]any) error {
	t.Helper()
	_, err := w.k.Transition(testCtx(t), itemservice.TransitionRequest{
		IdempotencyKey: newKey(), Principal: w.ana, ItemID: id, ToState: to, Fields: patch,
	})
	return err
}

func (w setsWorld) mustMove(t *testing.T, id uuid.UUID, to string, patch map[string]any) {
	t.Helper()
	if err := w.move(t, id, to, patch); err != nil {
		t.Fatalf("move to %s: %v", to, err)
	}
}

func (w setsWorld) fields(t *testing.T, id uuid.UUID) map[string]any {
	t.Helper()
	return fieldsOf(t, w.k, w.ana, id)
}

// stamp is an event time as a datetime field holds it.
func stamp(at time.Time) string { return at.UTC().Format(typedef.DateTimeLayout) }

// Closing sets the resolution time to the time of its own event, exactly,
// in the same write: one event, which names the computed fields with the
// patched ones and holds their values.
func TestSets_ResolvedAtIsTheEventsTime(t *testing.T) {
	w := newSetsWorld(t)
	id := w.open(t, map[string]any{"f_points": 4, "f_note": "almost"})
	before := time.Now().Add(-time.Second)
	w.mustMove(t, id, "s_done", map[string]any{"f_resolution": "fixed"})

	events := history(t, w.k, w.ana, id)
	if len(events) != 2 {
		t.Fatalf("%d events, want 2 (create, and one for the move with its effects)", len(events))
	}
	done := events[1]
	fields := w.fields(t, id)
	if got := fields["f_resolved_at"]; got != stamp(done.CreatedAt) {
		t.Errorf("f_resolved_at = %v, want the event's time %s", got, stamp(done.CreatedAt))
	}
	if done.CreatedAt.Before(before) || done.CreatedAt.After(time.Now().Add(time.Second)) {
		t.Errorf("the event's time %v is not now", done.CreatedAt)
	}
	// The view is the guard's: f_points as stored, f_note before its clear.
	if n, _ := typedef.AsInt(fields["f_double"]); n != 8 {
		t.Errorf("f_double = %v, want 8", fields["f_double"])
	}
	if fields["f_had_note"] != true {
		t.Errorf("f_had_note = %v, want true: the effect reads the note before the move clears it", fields["f_had_note"])
	}
	if _, set := fields["f_note"]; set {
		t.Error("f_note is still set: the move clears it")
	}
	_, payload := event(t, id, 2)
	if got := anyStrings(payload["field_ids"]); !slices.Equal(got, []string{"f_double", "f_grade", "f_had_note", "f_resolution", "f_resolved_at"}) {
		t.Errorf("field_ids = %v, want the patched and the computed fields, sorted", got)
	}
	if got := anyStrings(payload["cleared_field_ids"]); !slices.Equal(got, []string{"f_note"}) {
		t.Errorf("cleared_field_ids = %v, want [f_note]", got)
	}
	if got := done.Values["f_resolved_at"]; got != stamp(done.CreatedAt) {
		t.Errorf("the event's value of f_resolved_at = %v, want %s", got, stamp(done.CreatedAt))
	}
	// The payload holds IDs, never the computed values.
	for k, v := range payload {
		if s := fmt.Sprint(v); strings.Contains(s, stamp(done.CreatedAt)) {
			t.Errorf("payload[%s] holds the computed value: %v", k, v)
		}
	}
}

// An effect reads the fields as the patch leaves them. (The patch cannot
// carry f_note: the move clears it, and a patch may not set what its move
// clears.)
func TestSets_ReadThePatchedFields(t *testing.T) {
	w := newSetsWorld(t)
	id := w.open(t, map[string]any{"f_points": 4})
	w.mustMove(t, id, "s_done", map[string]any{"f_resolution": "fixed", "f_points": 10})
	fields := w.fields(t, id)
	if n, _ := typedef.AsInt(fields["f_double"]); n != 20 {
		t.Errorf("f_double = %v, want 20 (the patched points, doubled)", fields["f_double"])
	}
	if fields["f_had_note"] != false {
		t.Errorf("f_had_note = %v, want false (no note)", fields["f_had_note"])
	}
}

// Reopening unsets what `sets` yields null for, in the move's write; the
// event lists it as cleared only if it had a value. Closing again stamps a
// new, later time.
func TestSets_NullUnsets(t *testing.T) {
	w := newSetsWorld(t)
	id := w.open(t, map[string]any{"f_points": 1})
	w.mustMove(t, id, "s_done", map[string]any{"f_resolution": "fixed"})
	first := w.fields(t, id)["f_resolved_at"]

	w.mustMove(t, id, "s_open", nil)
	fields := w.fields(t, id)
	for _, f := range []string{"f_resolved_at", "f_double", "f_resolution"} {
		if v, set := fields[f]; set {
			t.Errorf("%s = %v after the reopen, want it unset", f, v)
		}
	}
	_, payload := event(t, id, 3)
	if got := anyStrings(payload["cleared_field_ids"]); !slices.Equal(got, []string{"f_double", "f_resolution", "f_resolved_at"}) {
		t.Errorf("cleared_field_ids = %v, want [f_double f_resolution f_resolved_at]", got)
	}
	if got := anyStrings(payload["field_ids"]); len(got) != 0 {
		t.Errorf("field_ids = %v, want none: the reopen sets no value", got)
	}

	w.mustMove(t, id, "s_done", map[string]any{"f_resolution": "wontfix"})
	second, _ := w.fields(t, id)["f_resolved_at"].(string)
	if f, _ := first.(string); second <= f {
		t.Errorf("f_resolved_at = %q after closing again, want it later than %q", second, f)
	}

	// A reopen with nothing to unset lists nothing as cleared.
	other := w.open(t, nil)
	w.mustMove(t, other, "s_parked", nil)
	w.mustMove(t, other, "s_done", nil) // sets f_resolved_at only
	w.mustMove(t, other, "s_open", nil)
	_, payload = event(t, other, 4)
	if got := anyStrings(payload["cleared_field_ids"]); !slices.Equal(got, []string{"f_resolved_at"}) {
		t.Errorf("cleared_field_ids = %v, want [f_resolved_at]: f_double and f_resolution had no value", got)
	}
}

// A field written by `sets` is written by the Type, whoever makes the move:
// f_resolved_at is kernel-written and f_grade role-written, ana holds no
// role, and her move sets both. By hand she may write neither.
func TestSets_AreExemptFromWritableBy(t *testing.T) {
	w := newSetsWorld(t)
	id := w.open(t, map[string]any{"f_points": 1})
	_, err := w.k.Update(testCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: w.ana, ItemID: id,
		Fields: map[string]any{"f_resolved_at": "2026-12-04T17:00:00Z"},
	})
	assertNotWritable(t, "f_resolved_at by hand", err, "f_resolved_at")
	_, err = w.k.Update(testCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: w.ana, ItemID: id, Fields: map[string]any{"f_grade": 5},
	})
	assertNotWritable(t, "f_grade by hand", err, "f_grade")

	w.mustMove(t, id, "s_done", map[string]any{"f_resolution": "fixed"})
	fields := w.fields(t, id)
	if fields["f_resolved_at"] == nil {
		t.Error("f_resolved_at is unset after the move")
	}
	if n, _ := typedef.AsInt(fields["f_grade"]); n != 1 {
		t.Errorf("f_grade = %v, want 1", fields["f_grade"])
	}
	// And by hand afterwards: still not hers to change or to clear.
	_, err = w.k.Update(testCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: w.ana, ItemID: id, Fields: map[string]any{"f_resolved_at": nil},
	})
	assertNotWritable(t, "clearing f_resolved_at by hand", err, "f_resolved_at")
}

// A patch that sets, or nulls, a field the move computes contradicts the
// move: malformed, before anything else, and nothing is written.
func TestSets_PatchMayNotNameAComputedField(t *testing.T) {
	w := newSetsWorld(t)
	id := w.open(t, map[string]any{"f_points": 1})
	for name, v := range map[string]any{"a value": 7, "null": nil} {
		err := w.move(t, id, "s_done", map[string]any{"f_resolution": "fixed", "f_double": v})
		assertInvalidField(t, "a patch with "+name+" for a computed field", err, "f_double")
	}
	// Checked before writable_by: the answer is "malformed", not "forbidden".
	err := w.move(t, id, "s_done", map[string]any{"f_resolution": "fixed", "f_resolved_at": "2026-12-04T17:00:00Z"})
	assertInvalidField(t, "a patch for a computed, kernel-written field", err, "f_resolved_at")
	assertUnchanged(t, "after the refusals", id, "s_open", 1)
}

// Required fields and the guard are judged before any effect runs.
func TestSets_RequiredComesFirst(t *testing.T) {
	w := newSetsWorld(t)
	id := w.open(t, nil) // no points: the effect would fail, but required fails first
	assertRejectedCode(t, "close without a resolution", w.move(t, id, "s_done", nil), itemservice.CodeMissingFields)
}

// An effect that cannot be computed refuses the move: effect_failed, with
// the field as metadata and no value in the reason. Nothing is written, the
// key is not used up, and the same key works once the Item is fixed.
func TestSets_EffectFailed(t *testing.T) {
	w := newSetsWorld(t)
	id := w.open(t, nil) // f_points unset: f_points * 2 cannot be computed
	key := newKey()
	req := itemservice.TransitionRequest{
		IdempotencyKey: key, Principal: w.ana, ItemID: id, ToState: "s_done",
		Fields: map[string]any{"f_resolution": "fixed"},
	}
	_, err := w.k.Transition(testCtx(t), req)
	assertRejectedCode(t, "an effect on an unset field", err, itemservice.CodeEffectFailed)
	var rej *itemservice.RejectedError
	if errors.As(err, &rej) {
		if rej.Metadata["field"] != "f_double" {
			t.Errorf("metadata = %v, want field f_double", rej.Metadata)
		}
		if rej.Metadata["from_state"] != "s_open" || rej.Metadata["to_state"] != "s_done" {
			t.Errorf("metadata = %v, want the move's states", rej.Metadata)
		}
	}
	assertUnchanged(t, "after effect_failed", id, "s_open", 1)

	if _, err := w.k.Update(testCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: w.ana, ItemID: id, Fields: map[string]any{"f_points": 2},
	}); err != nil {
		t.Fatalf("set the points: %v", err)
	}
	if _, err := w.k.Transition(testCtx(t), req); err != nil {
		t.Fatalf("the same key after the fix: %v", err)
	}

	// A result that is no value of its field: a string that is no option.
	other := w.open(t, map[string]any{"f_note": "a secret sentence"})
	w.mustMove(t, other, "s_parked", nil)
	err = w.move(t, other, "s_open", nil)
	assertRejectedCode(t, "an enum set to no option", err, itemservice.CodeEffectFailed)
	if errors.As(err, &rej) && (rej.Metadata["field"] != "f_resolution" || strings.Contains(rej.Reason, "secret")) {
		t.Errorf("refusal = %q %v, want field f_resolution and no value", rej.Reason, rej.Metadata)
	}
}

// The invariants are judged last, on the fields as the effects leave them.
func TestSets_InvariantsSeeTheEffects(t *testing.T) {
	w := newSetsWorld(t)
	id := w.open(t, nil)
	assertInvariantFailed(t, "an effect that breaks an invariant", w.move(t, id, "s_odd", nil), "double_is_small")
	assertUnchanged(t, "after invariant_failed", id, "s_open", 1)
}

// A retry with the same key returns the first result and computes nothing
// again: one event, the first time.
func TestSets_RetryDoesNotRecompute(t *testing.T) {
	w := newSetsWorld(t)
	id := w.open(t, map[string]any{"f_points": 1})
	req := itemservice.TransitionRequest{
		IdempotencyKey: newKey(), Principal: w.ana, ItemID: id, ToState: "s_done",
		Fields: map[string]any{"f_resolution": "fixed"},
	}
	first, err := w.k.Transition(testCtx(t), req)
	if err != nil {
		t.Fatalf("close: %v", err)
	}
	at := w.fields(t, id)["f_resolved_at"]
	time.Sleep(20 * time.Millisecond)
	again, err := w.k.Transition(testCtx(t), req)
	if err != nil || again.CommitSeq != first.CommitSeq {
		t.Fatalf("retry = %+v, %v; want the first result %+v", again, err, first)
	}
	if got := w.fields(t, id)["f_resolved_at"]; got != at {
		t.Errorf("f_resolved_at = %v after the retry, want %v", got, at)
	}
	assertEventCount(t, id, 2)
}

// The internal door: a timer's move computes what the transition declares,
// at its own event's time.
func TestSets_TimerDoor(t *testing.T) {
	w := newSetsWorld(t)
	id := w.open(t, nil)
	w.mustMove(t, id, "s_parked", nil) // event 2 entered s_parked
	_, err := w.k.Internal().Transition(testCtx(t), itemservice.TransitionRequest{
		IdempotencyKey: newKey(), Principal: item.Principal{Actor: "system:timer", Namespace: w.ana.Namespace},
		ItemID: id, ToState: "s_done",
		Attribution: item.Attribution{
			OriginatingHook: fmt.Sprintf("timer:%s@v1:s_parked", w.typ),
			CausationID:     fmt.Sprintf("%s:2", id), Hop: 1,
		},
	})
	if err != nil {
		t.Fatalf("the timer closes: %v", err)
	}
	events := history(t, w.k, w.ana, id)
	if got := w.fields(t, id)["f_resolved_at"]; got != stamp(events[2].CreatedAt) {
		t.Errorf("f_resolved_at = %v, want the timer event's time %s", got, stamp(events[2].CreatedAt))
	}
}

// Effects are computed in the write: this Type's other states have a timer,
// so it has listeners, but the effect itself adds no write, no event and no
// outbox row of its own.
func TestSets_OneWriteOneEvent(t *testing.T) {
	w := newSetsWorld(t)
	id := w.open(t, map[string]any{"f_points": 1})
	w.mustMove(t, id, "s_done", map[string]any{"f_resolution": "fixed"})
	if n := count(t, `select count(*) from item_events where item_id = $1`, id); n != 2 {
		t.Errorf("item_events rows = %d, want 2", n)
	}
	if n := count(t, `select count(*) from outbox where item_id = $1`, id); n != 2 {
		t.Errorf("outbox rows = %d, want 2 (one per event)", n)
	}
	if seq := itemSeqOf(t, id); seq != 2 {
		t.Errorf("item_seq = %d, want 2", seq)
	}
}
