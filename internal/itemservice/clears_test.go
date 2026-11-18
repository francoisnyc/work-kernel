// Tests for the Jira proof of concept, part 1: field effects on transitions
// (docs/design.md, "Jira proof of concept"). A transition may clear fields
// in the same write: closing requires a resolution, reopening clears it.
//
// The order of checks, on every door:
//
//  1. malformed: a patch that sets a field the move clears;
//  2. not writable: a cleared field counts as named by the write, so a field
//     only a role writes is cleared only by a holder of the role;
//  3. frozen, 4. held: as for any patch;
//  5. required fields and the guard, on the fields as the patch leaves
//     them, BEFORE the clear: a reopening guard reads the resolution;
//  6. the clear;
//  7. the invariants, on the fields as the write leaves them.
//
// The clear shows in history like a patch's null: cleared_field_ids on the
// transition's event, listed only for fields that had a value.
package itemservice_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

const clearsTypeYAML = `
id: TYPE
version: 1
initial_state: s_open
fields:
  - {id: f_title,       type: string}
  - {id: f_resolution,  type: enum, options: [fixed, wontfix]}
  - {id: f_fix_version, type: string}
  - {id: f_grade,       type: int, writable_by: ['role:ROLE']}
states:
  - {id: s_open,   category: todo}
  - {id: s_done,   category: done}
  - {id: s_graded, category: done}
  - {id: s_parked, category: waiting, timer: {after: 1h, to: s_open}}
transitions:
  - {from: s_open, to: s_done, required: [f_resolution]}
  # Reopen: not a wontfix; the resolution goes.
  - from: s_done
    to: s_open
    guard: 'f_resolution != "wontfix"'
    clears: [f_resolution]
  - {from: s_done,   to: s_graded}
  - {from: s_graded, to: s_open, clears: [f_grade, f_resolution]}
  - {from: s_done,   to: s_parked}
  - {from: s_parked, to: s_open, clears: [f_resolution]}
invariants:
  - {id: version_needs_resolution, rule: 'f_fix_version == null || f_resolution != null'}
`

type clearsWorld struct {
	k         *itemservice.Service
	typ, role string
	ana, lead item.Principal
}

func newClearsWorld(t *testing.T) clearsWorld {
	t.Helper()
	k := itemservice.New(pool)
	sfx := uuid.NewString()[:8]
	ns := newNamespace(t, k)
	w := clearsWorld{
		k: k, typ: "clr-" + sfx, role: "lead-" + sfx,
		ana: in(person("ana"), ns), lead: in(person("lead"), ns),
	}
	ty, err := typedef.Parse([]byte(strings.NewReplacer("TYPE", w.typ, "ROLE", w.role).Replace(clearsTypeYAML)))
	if err != nil {
		t.Fatalf("parse clears test Type: %v", err)
	}
	if err := k.RegisterType(testCtx(t), ty); err != nil {
		t.Fatalf("register clears test Type: %v", err)
	}
	grantRoleIn(t, ns, w.lead, w.role)
	return w
}

func (w clearsWorld) move(t *testing.T, p item.Principal, id uuid.UUID, to string, patch map[string]any) error {
	t.Helper()
	_, err := w.k.Transition(testCtx(t), itemservice.TransitionRequest{
		IdempotencyKey: newKey(), Principal: p, ItemID: id, ToState: to, Fields: patch,
	})
	return err
}

func (w clearsWorld) mustMove(t *testing.T, p item.Principal, id uuid.UUID, to string, patch map[string]any) {
	t.Helper()
	if err := w.move(t, p, id, to, patch); err != nil {
		t.Fatalf("move to %s: %v", to, err)
	}
}

// done creates an Item and closes it with a resolution: two events.
func (w clearsWorld) done(t *testing.T, resolution string) uuid.UUID {
	t.Helper()
	res, err := w.k.Create(testCtx(t), itemservice.CreateRequest{
		IdempotencyKey: newKey(), Principal: w.ana, TypeID: w.typ, TypeVersion: 1,
		Fields: map[string]any{"f_title": "chore " + word()},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	w.mustMove(t, w.ana, res.ItemID, "s_done", map[string]any{"f_resolution": resolution})
	return res.ItemID
}

func (w clearsWorld) fields(t *testing.T, id uuid.UUID) map[string]any {
	t.Helper()
	return fieldsOf(t, w.k, w.ana, id)
}

// assertUnchanged checks a refused write left the Item as it was.
func assertUnchanged(t *testing.T, what string, id uuid.UUID, state string, seq int64) {
	t.Helper()
	if s := stateOf(t, id); s != state {
		t.Errorf("%s: the Item is in %s, want it still in %s", what, s, state)
	}
	if got := itemSeqOf(t, id); got != seq {
		t.Errorf("%s: item_seq = %d, want %d (nothing written)", what, got, seq)
	}
}

// Reopening clears the resolution in the transition's own write: one event,
// which says so, and history shows the field cleared. Closing again needs a
// new resolution.
func TestClears_ReopenClearsInTheSameWrite(t *testing.T) {
	w := newClearsWorld(t)
	id := w.done(t, "fixed")
	if got := w.fields(t, id)["f_resolution"]; got != "fixed" {
		t.Fatalf("f_resolution = %v before the reopen, want fixed", got)
	}

	w.mustMove(t, w.ana, id, "s_open", nil)
	if s := stateOf(t, id); s != "s_open" {
		t.Errorf("state = %s, want s_open", s)
	}
	if v, set := w.fields(t, id)["f_resolution"]; set {
		t.Errorf("f_resolution = %v after the reopen, want it cleared", v)
	}
	if seq := itemSeqOf(t, id); seq != 3 {
		t.Fatalf("item_seq = %d, want 3: create, close, reopen (the clear is not a write of its own)", seq)
	}
	op, payload := event(t, id, 3)
	if op != "transition" || payload["from"] != "s_done" || payload["to"] != "s_open" {
		t.Errorf("event 3 = %s %v, want the transition s_done -> s_open", op, payload)
	}
	if got := anyStrings(payload["cleared_field_ids"]); !slices.Equal(got, []string{"f_resolution"}) {
		t.Errorf("event 3 cleared_field_ids = %v, want [f_resolution]", payload["cleared_field_ids"])
	}
	// No personal data, no values: the payload names IDs only.
	if raw := fmt.Sprint(payload); strings.Contains(raw, "fixed") {
		t.Errorf("the payload %v holds the cleared value", payload)
	}

	events := history(t, w.k, w.ana, id)
	last := events[len(events)-1]
	if v, named := last.Values["f_resolution"]; !named || v != nil {
		t.Errorf("history: the reopen's values = %v, want f_resolution present and nil (cleared)", last.Values)
	}

	// The requirement is back: done needs a resolution again.
	err := w.move(t, w.ana, id, "s_done", nil)
	assertRejectedCode(t, "close again without a resolution", err, itemservice.CodeMissingFields)
	w.mustMove(t, w.ana, id, "s_done", map[string]any{"f_resolution": "wontfix"})
}

// The guard is judged before the clear: it reads the resolution the move
// would clear. A refused move clears nothing.
func TestClears_TheGuardSeesTheValueBeforeTheClear(t *testing.T) {
	w := newClearsWorld(t)
	id := w.done(t, "wontfix")
	err := w.move(t, w.ana, id, "s_open", nil)
	assertRejectedCode(t, "reopen a wontfix", err, itemservice.CodeGuardFailed)
	assertUnchanged(t, "reopen a wontfix", id, "s_done", 2)
	if got := w.fields(t, id)["f_resolution"]; got != "wontfix" {
		t.Errorf("f_resolution = %v after the refusal, want wontfix", got)
	}
	// The way out is an update first: the move's own patch may not set the
	// field the move clears (TestClears_WithAPatch).
	if _, err := w.k.Update(testCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: w.ana, ItemID: id, Fields: map[string]any{"f_resolution": "fixed"},
	}); err != nil {
		t.Fatalf("update the resolution: %v", err)
	}
	w.mustMove(t, w.ana, id, "s_open", nil)
}

// A patch travels with the move as before: its fields are set, the move's
// are cleared, in one event that lists both. A patch may not set a field
// the move clears (malformed, before anything else); clearing it by hand in
// the patch is the same as the move's own clear.
func TestClears_WithAPatch(t *testing.T) {
	w := newClearsWorld(t)
	id := w.done(t, "fixed")
	w.mustMove(t, w.ana, id, "s_open", map[string]any{"f_title": "again"})
	_, payload := event(t, id, 3)
	if got := anyStrings(payload["field_ids"]); !slices.Equal(got, []string{"f_title"}) {
		t.Errorf("field_ids = %v, want [f_title]", payload["field_ids"])
	}
	if got := anyStrings(payload["cleared_field_ids"]); !slices.Equal(got, []string{"f_resolution"}) {
		t.Errorf("cleared_field_ids = %v, want [f_resolution]", payload["cleared_field_ids"])
	}
	if f := w.fields(t, id); f["f_title"] != "again" || f["f_resolution"] != nil {
		t.Errorf("fields after the reopen = %v, want the new title and no resolution", f)
	}

	id = w.done(t, "fixed")
	err := w.move(t, w.ana, id, "s_open", map[string]any{"f_resolution": "wontfix"})
	assertInvalidField(t, "a patch that sets a field the move clears", err, "f_resolution")
	assertUnchanged(t, "a patch that sets a field the move clears", id, "s_done", 2)

	w.mustMove(t, w.ana, id, "s_open", map[string]any{"f_resolution": nil})
	_, payload = event(t, id, 3)
	if got := anyStrings(payload["cleared_field_ids"]); !slices.Equal(got, []string{"f_resolution"}) {
		t.Errorf("a patch's null on a cleared field: cleared_field_ids = %v, want [f_resolution] once", payload["cleared_field_ids"])
	}
}

// Only what had a value is recorded as cleared; a move that found nothing
// to clear writes the plain two-key payload of any transition.
func TestClears_NothingToClearRecordsNothing(t *testing.T) {
	w := newClearsWorld(t)
	id := w.done(t, "fixed")
	w.mustMove(t, w.lead, id, "s_graded", nil) // event 3; f_grade was never set
	w.mustMove(t, w.lead, id, "s_open", nil)   // event 4: clears f_grade and f_resolution
	_, payload := event(t, id, 4)
	if got := anyStrings(payload["cleared_field_ids"]); !slices.Equal(got, []string{"f_resolution"}) {
		t.Errorf("cleared_field_ids = %v, want [f_resolution] only: f_grade had no value", payload["cleared_field_ids"])
	}

	// Neither field set: close, drop the resolution by hand, then reopen
	// through s_graded.
	id = w.done(t, "fixed")
	if _, err := w.k.Update(testCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: w.ana, ItemID: id, Fields: map[string]any{"f_resolution": nil},
	}); err != nil {
		t.Fatalf("clear the resolution by hand: %v", err)
	}
	w.mustMove(t, w.lead, id, "s_graded", nil)
	w.mustMove(t, w.lead, id, "s_open", nil)
	seq := itemSeqOf(t, id)
	_, payload = event(t, id, seq)
	if len(payload) != 2 || payload["from"] != "s_graded" || payload["to"] != "s_open" {
		t.Errorf("the payload of a move that cleared nothing = %v, want only from and to", payload)
	}
}

// A cleared field counts as named by the write: a field only a role writes
// is cleared only by a holder of the role, whether or not it has a value
// (naming is enough, as for a patch and for frozen fields).
func TestClears_RespectWritableBy(t *testing.T) {
	w := newClearsWorld(t)
	id := w.done(t, "fixed")
	w.mustMove(t, w.ana, id, "s_graded", nil)
	if _, err := w.k.Update(testCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: w.lead, ItemID: id, Fields: map[string]any{"f_grade": 5},
	}); err != nil {
		t.Fatalf("the lead grades: %v", err)
	}
	seq := itemSeqOf(t, id)

	err := w.move(t, w.ana, id, "s_open", nil)
	assertNotWritable(t, "ana reopens, which would clear the lead's grade", err, "f_grade")
	assertUnchanged(t, "ana reopens", id, "s_graded", seq)
	if got := w.fields(t, id)["f_grade"]; got == nil {
		t.Error("the grade is gone after a refused reopen")
	}

	w.mustMove(t, w.lead, id, "s_open", nil)
	if f := w.fields(t, id); f["f_grade"] != nil || f["f_resolution"] != nil {
		t.Errorf("fields after the lead reopens = %v, want neither grade nor resolution", f)
	}

	// With no grade to clear, ana is still refused: the move names the field.
	id = w.done(t, "fixed")
	w.mustMove(t, w.ana, id, "s_graded", nil)
	err = w.move(t, w.ana, id, "s_open", nil)
	assertNotWritable(t, "ana reopens an ungraded Item", err, "f_grade")
}

// The invariants are judged last, on the fields as the write leaves them:
// a clear that would break one refuses the move.
func TestClears_InvariantsAreJudgedAfterTheClear(t *testing.T) {
	w := newClearsWorld(t)
	id := w.done(t, "fixed")
	if _, err := w.k.Update(testCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: w.ana, ItemID: id, Fields: map[string]any{"f_fix_version": "1.2"},
	}); err != nil {
		t.Fatalf("set the fix version: %v", err)
	}
	err := w.move(t, w.ana, id, "s_open", nil)
	assertInvariantFailed(t, "reopen while a fix version stands", err, "version_needs_resolution")
	assertUnchanged(t, "reopen while a fix version stands", id, "s_done", 3)

	// Dropping the fix version in the same move satisfies it.
	w.mustMove(t, w.ana, id, "s_open", map[string]any{"f_fix_version": nil})
	_, payload := event(t, id, 4)
	if got := anyStrings(payload["cleared_field_ids"]); !slices.Equal(got, []string{"f_fix_version", "f_resolution"}) {
		t.Errorf("cleared_field_ids = %v, want [f_fix_version f_resolution]", payload["cleared_field_ids"])
	}
}

// The internal door: a timer's move clears what the transition declares,
// like anyone's.
func TestClears_TimerDoor(t *testing.T) {
	w := newClearsWorld(t)
	id := w.done(t, "fixed")
	w.mustMove(t, w.ana, id, "s_parked", nil) // event 3 entered s_parked
	_, err := w.k.Internal().Transition(testCtx(t), itemservice.TransitionRequest{
		IdempotencyKey: newKey(), Principal: item.Principal{Actor: "system:timer", Namespace: w.ana.Namespace},
		ItemID: id, ToState: "s_open",
		Attribution: item.Attribution{
			OriginatingHook: fmt.Sprintf("timer:%s@v1:s_parked", w.typ),
			CausationID:     fmt.Sprintf("%s:3", id), Hop: 1,
		},
	})
	if err != nil {
		t.Fatalf("the timer reopens: %v", err)
	}
	if v, set := w.fields(t, id)["f_resolution"]; set {
		t.Errorf("f_resolution = %v after the timer's move, want it cleared", v)
	}
	_, payload := event(t, id, 4)
	if got := anyStrings(payload["cleared_field_ids"]); !slices.Equal(got, []string{"f_resolution"}) {
		t.Errorf("the timer's event: cleared_field_ids = %v, want [f_resolution]", payload["cleared_field_ids"])
	}
}

// The same request sent twice clears once: the stored result answers.
func TestClears_SameKeyTwice(t *testing.T) {
	w := newClearsWorld(t)
	id := w.done(t, "fixed")
	req := itemservice.TransitionRequest{IdempotencyKey: newKey(), Principal: w.ana, ItemID: id, ToState: "s_open"}
	first, err := w.k.Transition(testCtx(t), req)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	second, err := w.k.Transition(testCtx(t), req)
	if err != nil || second.CommitSeq != first.CommitSeq {
		t.Errorf("the same key again: %+v, %v; want the first result (commit_seq %d)", second, err, first.CommitSeq)
	}
	if seq := itemSeqOf(t, id); seq != 3 {
		t.Errorf("item_seq = %d, want 3: one reopen", seq)
	}
}
