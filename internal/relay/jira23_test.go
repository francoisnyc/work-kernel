// Tests for Jira part 2, step 3a, in the relay.
//
//   - An acting timer at an instant needs what a timer at an instant does:
//     the events that set or clear its field name it (Event.FieldIDs), so
//     the workflow reads the instant again; and a state with one starts a
//     workflow, from the Item's creation, with the field set or not.
//   - Creation enters the initial state: a create whose initial state has
//     an enter reaction without `from` starts a workflow.
package relay_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/itemservice"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

const actingAtTypeYAML = `
id: TYPE
version: 1
initial_state: running
fields:
  - {id: f_name,     type: string}
  - {id: f_due_at,   type: datetime}
  - {id: f_begin_at, type: datetime}
  - {id: f_other_at, type: datetime}
  - {id: f_late_at,  type: datetime, writable_by: kernel}
  - {id: f_begun,    type: bool,     writable_by: kernel}
states:
  - id: running
    category: active
    acting:
      - {id: late,  at: f_due_at,   sets: {f_late_at: 'cause.at'}}
      - {id: begun, at: f_begin_at, sets: {f_begun: 'true'}}
  - {id: paused, category: waiting}
transitions:
  - {from: running, to: paused, sets: {f_due_at: 'null'}}
  - {from: paused,  to: running}
`

func registerYAML(t *testing.T, k *itemservice.Service, src string) string {
	t.Helper()
	typ := "r3-" + uuid.NewString()[:8]
	ty, err := typedef.Parse([]byte(strings.Replace(src, "TYPE", typ, 1)))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := k.RegisterType(testCtx(t), ty); err != nil {
		t.Fatalf("register: %v", err)
	}
	return typ
}

func TestRelay_NamesTheFieldsOfActingTimersAtAnInstant(t *testing.T) {
	r, fake, _ := newRelay(t)
	k := itemservice.New(pool)
	typ := registerYAML(t, k, actingAtTypeYAML)
	res, err := k.Create(testCtx(t), itemservice.CreateRequest{ // 1: no instant yet
		IdempotencyKey: newKey(), Principal: alice, TypeID: typ, TypeVersion: 1, Fields: map[string]any{"f_name": "lap"},
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
	update(map[string]any{"f_due_at": "2026-12-04T17:00:00Z"})                                       // 2: one timer's field
	update(map[string]any{"f_name": "lap 2", "f_other_at": "2026-12-05T00:00:00Z"})                  // 3: neither
	update(map[string]any{"f_begin_at": "2026-12-01T09:00:00Z", "f_due_at": "2026-12-11T17:00:00Z"}) // 4: both
	move("paused")                                                                                   // 5: an effect clears one
	move("running")                                                                                  // 6: back, nothing set
	runOnce(t, r)

	calls := fake.callsFor(id)
	if len(calls) != 6 {
		t.Fatalf("%d deliveries, want 6", len(calls))
	}
	want := []struct {
		fields []string
		start  bool
	}{
		{nil, true}, // created in a state with such a timer: a run must be there to hear the field being set
		{[]string{"f_due_at"}, false},
		{nil, false},
		{[]string{"f_begin_at", "f_due_at"}, false},
		{[]string{"f_due_at"}, false}, // paused has no behavior
		{nil, true},
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

// begins sets a field on its container when it is created: an enter
// reaction on the initial state, without `from`. later only reacts when
// it is entered again.
const beginsTypeYAML = `
id: TYPE
version: 1
initial_state: open
fields:
  - {id: f_name, type: string}
states:
  - {id: open,   category: todo}
  - {id: closed, category: done}
transitions:
  - {from: open,   to: closed}
  - {from: closed, to: open}
reactions:
  - REACTION
`

func TestRelay_ACreateIntoAStateWithAnEnterReaction_StartsAWorkflow(t *testing.T) {
	r, fake, _ := newRelay(t)
	k := itemservice.New(pool)
	created := func(reaction string) uuid.UUID {
		typ := registerYAML(t, k, strings.Replace(beginsTypeYAML, "REACTION", reaction, 1))
		res, err := k.Create(testCtx(t), itemservice.CreateRequest{
			IdempotencyKey: newKey(), Principal: alice, TypeID: typ, TypeVersion: 1, Fields: map[string]any{"f_name": "x"},
		})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		return res.ItemID
	}
	begins := created("{id: tell, kind: set, on: enter, state: open, target: container, set: {f_seen_at: 'cause.at'}}")
	// With a `from`, the reaction does not run at creation; the state
	// still has behavior, so a run is started as before and finds nothing
	// to do. (Per state, not per reaction: lifecycle.NeedsWorkflow.)
	later := created("{id: tell, kind: set, on: enter, state: open, from: [closed], target: container, set: {f_seen_at: 'cause.at'}}")
	runOnce(t, r)

	for name, id := range map[string]uuid.UUID{"without from": begins, "with from": later} {
		calls := fake.callsFor(id)
		if len(calls) != 1 || !calls[0].Start || calls[0].Event.Op != "create" {
			t.Errorf("%s: deliveries = %+v, want the create, starting a workflow", name, calls)
		}
	}
}
