// Tests for Jira part 2, step 1: what the relay tells the workflow about a
// timer at a field's instant. The relay still sends shapes, never values:
// for a Type with such a timer it adds the IDs of the at-fields an event
// set or cleared (Event.FieldIDs), so the workflow knows when to read the
// instant again. Every other field, and every other Type, adds nothing.
package relay_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/itemservice"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

const atTypeYAML = `
id: TYPE
version: 1
initial_state: planned
fields:
  - {id: f_name,     type: string}
  - {id: f_end_at,   type: datetime}
  - {id: f_other_at, type: datetime}
states:
  - {id: planned, category: backlog}
  - {id: active,  category: active, at: {field: f_end_at, to: closed}}
  - {id: closed,  category: done}
transitions:
  - {from: planned, to: active}
  - {from: active,  to: closed}
  # Postponing computes a new end: an effect on the at-field.
  - {from: active,  to: planned, sets: {f_end_at: 'f_end_at + duration("24h")'}}
`

func TestRelay_NamesTheAtFieldsAnEventTouched(t *testing.T) {
	r, fake, _ := newRelay(t)
	k := itemservice.New(pool)
	typ := "at-" + uuid.NewString()[:8]
	ty, err := typedef.Parse([]byte(strings.Replace(atTypeYAML, "TYPE", typ, 1)))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := k.RegisterType(testCtx(t), ty); err != nil {
		t.Fatalf("register: %v", err)
	}
	res, err := k.Create(testCtx(t), itemservice.CreateRequest{ // 1: sets the at-field and another
		IdempotencyKey: newKey(), Principal: alice, TypeID: typ, TypeVersion: 1,
		Fields: map[string]any{"f_name": "lap", "f_end_at": "2026-12-04T17:00:00Z"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	id := res.ItemID
	move := func(to string) {
		t.Helper()
		if _, err := k.Transition(testCtx(t), itemservice.TransitionRequest{
			IdempotencyKey: newKey(), Principal: alice, ItemID: id, ToState: to,
		}); err != nil {
			t.Fatalf("to %s: %v", to, err)
		}
	}
	update := func(fields map[string]any) {
		t.Helper()
		if _, err := k.Update(testCtx(t), itemservice.UpdateRequest{
			IdempotencyKey: newKey(), Principal: alice, ItemID: id, Fields: fields,
		}); err != nil {
			t.Fatalf("update %v: %v", fields, err)
		}
	}
	move("active")                                                                  // 2: enters the at-state
	update(map[string]any{"f_name": "lap 2", "f_other_at": "2026-12-05T00:00:00Z"}) // 3: other fields
	update(map[string]any{"f_end_at": "2026-12-11T17:00:00Z", "f_name": "lap 3"})   // 4: moves the end
	update(map[string]any{"f_end_at": nil})                                         // 5: clears it
	update(map[string]any{"f_end_at": "2026-12-12T17:00:00+01:00"})                 // 6: sets it again
	move("planned")                                                                 // 7: an effect computes it
	runOnce(t, r)

	calls := fake.callsFor(id)
	if len(calls) != 7 {
		t.Fatalf("%d deliveries, want 7", len(calls))
	}
	end := []string{"f_end_at"}
	want := []struct {
		fields []string
		start  bool
	}{
		{end, false}, // planned has no behavior
		{nil, true},  // entering active starts the workflow, with the field unset or not
		{nil, false},
		{end, false},
		{end, false},
		{end, false},
		{end, false},
	}
	for i, w := range want {
		c := calls[i]
		if !slices.Equal(c.Event.FieldIDs, w.fields) || c.Start != w.start {
			t.Errorf("delivery %d: FieldIDs %v, start %v; want %v, %v", i+1, c.Event.FieldIDs, c.Start, w.fields, w.start)
		}
	}
	if left := pending(t, id); len(left) != 0 {
		t.Errorf("undelivered rows = %v, want none", left)
	}
}

// A Type without such a timer names no fields, whatever its events set: the
// workflows of every existing Type receive exactly what they did.
func TestRelay_NoAtTimer_NamesNoFields(t *testing.T) {
	r, fake, _ := newRelay(t)
	id := ticketInProgress(t, itemservice.New(pool)) // create, update f_assignee, transition
	runOnce(t, r)
	for i, c := range fake.callsFor(id) {
		if c.Event.FieldIDs != nil {
			t.Errorf("delivery %d of a ticket: FieldIDs %v, want nil", i+1, c.Event.FieldIDs)
		}
	}
}
