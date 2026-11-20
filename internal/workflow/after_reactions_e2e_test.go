// End-to-end tests for Jira part 2, step 2 on the real stack (item
// service, relay, Temporal dev server, worker): reactions that move or
// create.
//
//   - The classic: the requester's public remark, made inside a desk that
//     waits, sets it working again. The desk had no workflow: the remark's
//     create starts one, cold.
//   - Time comes from the cause: with the worker stopped for a while, the
//     desk's computed time is still the remark's, and the move's own event
//     is stamped when it was written.
//   - A remark that triggers nothing is recorded as skipped; a move the
//     desk refuses, as failed. The run completes either way.
//   - Starting work on a desk creates a todo inside it, once.
//   - A remark in a container that declares nothing starts no workflow.
package workflow_test

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
	"github.com/francoisnyc/work-kernel/internal/typedef"
	wf "github.com/francoisnyc/work-kernel/internal/workflow"
)

const wfDeskTypes = `
id: wfremark
version: 1
fields:
  - {id: f_body,   type: string}
  - {id: f_public, type: bool}
---
id: wftodo
version: 1
fields:
  - {id: f_title,     type: string}
  - {id: f_opened_at, type: datetime, writable_by: kernel}
accepts_reactions:
  wfdesk:checklist: {creates: [f_title, f_opened_at]}
---
id: wfdesk
version: 1
initial_state: open
fields:
  - {id: f_title,       type: string}
  - {id: f_assignee,    type: principal}
  - {id: f_reopened_at, type: datetime, writable_by: kernel}
states:
  - {id: open,    category: todo}
  - {id: working, category: active}
  - {id: waiting, category: waiting}
transitions:
  - {from: open,    to: working}
  - {from: working, to: waiting}
  - from: waiting
    to: working
    allowed: ['role:wfsupport']
    guard: 'f_assignee != null'
    sets: {f_reopened_at: 'cause.at'}
reactions:
  - {id: reopen, kind: move, on: created_inside, of: [wfremark], when: 'child.f_public == true', by: creator, to: working, from: [waiting]}
  - {id: checklist, kind: create, on: enter, state: working, from: [open], type: wftodo, version: 1,
     fields: {f_title: '"Check: " + f_title', f_opened_at: 'cause.at'}}
`

var registerDeskOnce sync.Once

// newDeskStack is newStack with the desk Types registered.
func newDeskStack(t *testing.T) *stack {
	t.Helper()
	registerDeskOnce.Do(func() {
		for _, src := range splitDocs(wfDeskTypes) {
			ty, err := typedef.Parse([]byte(src))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if err := itemservice.New(pool).RegisterType(serverCtx(t), ty); err != nil {
				t.Fatalf("register %s: %v", ty.ID, err)
			}
		}
	})
	if _, err := itemservice.New(pool).Type(serverCtx(t), "wfdesk", 1); err != nil {
		t.Fatalf("the desk Type is not registered: %v", err)
	}
	return newStack(t)
}

func splitDocs(src string) []string {
	var out []string
	doc := ""
	for _, line := range splitLines(src) {
		if line == "---" {
			out, doc = append(out, doc), ""
			continue
		}
		doc += line + "\n"
	}
	return append(out, doc)
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	return append(lines, s[start:])
}

var bea = item.Principal{Actor: "user:bea"}

func (s *stack) makeIn(p item.Principal, typ string, in *uuid.UUID, fields map[string]any) uuid.UUID {
	s.t.Helper()
	res, err := s.k.Create(serverCtx(s.t), itemservice.CreateRequest{
		IdempotencyKey: "key-" + uuid.NewString(), Principal: p, TypeID: typ, TypeVersion: 1, ContainerID: in, Fields: fields,
		Attribution: item.Attribution{CorrelationID: "corr-" + uuid.NewString()[:8]},
	})
	if err != nil {
		s.t.Fatalf("create %s: %v", typ, err)
	}
	return res.ItemID
}

// waitingDesk is alice's desk, assigned, taken to waiting. Its checklist
// (created on starting work) is done and its run has completed: the desk
// has no running workflow, and is at item_seq 4 (create, working, the
// checklist's record, waiting).
func (s *stack) waitingDesk() uuid.UUID {
	s.t.Helper()
	desk := s.makeIn(alice, "wfdesk", nil, map[string]any{"f_title": "VPN", "f_assignee": "user:bob"})
	s.move(desk, "working")
	eventually(s.t, 15*time.Second, "the checklist recorded", s.isAt(desk, "working", 3))
	s.move(desk, "waiting")
	s.waitDelivered(desk)
	s.waitCompleted(desk)
	return desk
}

// records returns the ops and reasons of the Item's reaction outcome
// records, in order: "reaction_applied", "reaction_skipped:not_by", ...
func records(t *testing.T, id uuid.UUID) []string {
	t.Helper()
	rows, err := pool.Query(serverCtx(t),
		`select op, coalesce(payload->>'reason', ''), payload->>'reaction' from item_events
		 where item_id = $1 and op like 'reaction_%' order by item_seq`, id)
	if err != nil {
		t.Fatalf("read records: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var op, reason, reaction string
		if err := rows.Scan(&op, &reason, &reaction); err != nil {
			t.Fatalf("scan record: %v", err)
		}
		entry := op + " " + reaction
		if reason != "" {
			entry += " " + reason
		}
		out = append(out, entry)
	}
	return out
}

func eventAt(t *testing.T, id uuid.UUID, seq int64) time.Time {
	t.Helper()
	var at time.Time
	if err := pool.QueryRow(serverCtx(t),
		`select created_at from item_events where item_id = $1 and item_seq = $2`, id, seq).Scan(&at); err != nil {
		t.Fatalf("read event %d: %v", seq, err)
	}
	return at
}

func (s *stack) field(id uuid.UUID, name string) any {
	s.t.Helper()
	it, err := s.k.Get(serverCtx(s.t), alice, id)
	if err != nil {
		s.t.Fatalf("Get: %v", err)
	}
	return it.Fields[name]
}

// --- done when: an Item created inside another can move it ---

func TestE2E_ARemarkByTheRequesterReopensTheDesk_FromCold(t *testing.T) {
	s := newDeskStack(t)
	desk := s.waitingDesk()
	_, firstRun, _, _ := runStatus(t, desk)

	remark := s.makeIn(alice, "wfremark", &desk, map[string]any{"f_body": "any news?", "f_public": true})
	s.waitState(desk, "working", 15*time.Second)
	eventually(t, 15*time.Second, "the outcome recorded on the desk", s.isAt(desk, "working", 6))
	s.waitCompleted(desk)

	// A new run did it: the desk had none when the remark was made.
	if _, run, _, _ := runStatus(t, desk); run == firstRun {
		t.Errorf("the run that reopened the desk is %s, the one that had completed before", run)
	}
	move := readEvent(t, desk, 5)
	if move.Actor != "reaction:wfdesk:reopen" || move.Op != "transition" || move.Causation != fmt.Sprintf("%s:1", remark) || move.Hop != 1 {
		t.Errorf("the move = %+v, want the reaction's, caused by the remark's create, one hop below it", move)
	}
	want := []string{"reaction_applied wfdesk:checklist", "reaction_applied wfdesk:reopen"}
	if got := records(t, desk); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("the desk's records = %v, want %v", got, want)
	}
	if got := s.field(desk, "f_reopened_at"); got != eventAt(t, remark, 1).UTC().Format(typedef.DateTimeLayout) {
		t.Errorf("f_reopened_at = %v, want the remark's create time", got)
	}
	// The remark itself: one event, delivered, nothing else.
	if _, seq := s.state(remark); seq != 1 {
		t.Errorf("the remark is at item_seq %d, want 1", seq)
	}
	// Coming back to working from waiting is not the checklist's trigger.
	if n := children(t, desk, "wftodo"); n != 1 {
		t.Errorf("%d todos in the desk, want 1", n)
	}
}

func children(t *testing.T, container uuid.UUID, typ string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(serverCtx(t),
		`select count(*) from items where container_id = $1 and type_id = $2`, container, typ).Scan(&n); err != nil {
		t.Fatalf("count children: %v", err)
	}
	return n
}

// Time comes from the cause. The worker is down when the remark is made
// and comes back two seconds later: the desk's computed time is the
// remark's; the move's own event carries the time it was written.
func TestE2E_AReactionDelayedByAStoppedWorker_KeepsTheCausesTime(t *testing.T) {
	s := newDeskStack(t)
	desk := s.waitingDesk()
	s.stopWorker()
	remark := s.makeIn(alice, "wfremark", &desk, map[string]any{"f_public": true})
	staysFor(t, 2*time.Second, "waiting while the worker is down", s.isAt(desk, "waiting", 4))
	s.startWorker()
	s.waitState(desk, "working", 20*time.Second)

	made, moved := eventAt(t, remark, 1), eventAt(t, desk, 5)
	if got := s.field(desk, "f_reopened_at"); got != made.UTC().Format(typedef.DateTimeLayout) {
		t.Errorf("f_reopened_at = %v, want the remark's create time %s", got, made.UTC().Format(typedef.DateTimeLayout))
	}
	if d := moved.Sub(made); d < 2*time.Second {
		t.Errorf("the move's event is stamped %v after the remark, want at least the 2 s the worker was down", d)
	}
}

// Rightly nothing, and refused: each leaves one record on the desk, and
// the run completes.
func TestE2E_ReactionsSkippedAndFailed(t *testing.T) {
	s := newDeskStack(t)
	t.Run("a private remark", func(t *testing.T) {
		desk := s.waitingDesk()
		s.makeIn(alice, "wfremark", &desk, map[string]any{"f_public": false})
		eventually(t, 15*time.Second, "a record on the desk", s.isAt(desk, "waiting", 5))
		s.waitCompleted(desk)
		if got := records(t, desk); got[len(got)-1] != "reaction_skipped wfdesk:reopen condition_false" {
			t.Errorf("records = %v, want the last one skipped, condition_false", got)
		}
	})
	t.Run("a public remark by someone else", func(t *testing.T) {
		desk := s.waitingDesk()
		s.makeIn(bea, "wfremark", &desk, map[string]any{"f_public": true})
		eventually(t, 15*time.Second, "a record on the desk", s.isAt(desk, "waiting", 5))
		if got := records(t, desk); got[len(got)-1] != "reaction_skipped wfdesk:reopen not_by" {
			t.Errorf("records = %v, want the last one skipped, not_by", got)
		}
	})
	t.Run("the desk refuses: no assignee", func(t *testing.T) {
		desk := s.waitingDesk()
		if _, err := s.k.Update(serverCtx(t), itemservice.UpdateRequest{
			IdempotencyKey: "key-" + uuid.NewString(), Principal: alice, ItemID: desk, Fields: map[string]any{"f_assignee": nil},
		}); err != nil {
			t.Fatalf("clear the assignee: %v", err)
		}
		s.makeIn(alice, "wfremark", &desk, map[string]any{"f_public": true})
		eventually(t, 15*time.Second, "a record on the desk", s.isAt(desk, "waiting", 6))
		s.waitCompleted(desk)
		if got := records(t, desk); got[len(got)-1] != "reaction_failed wfdesk:reopen guard_failed" {
			t.Errorf("records = %v, want the last one failed, guard_failed", got)
		}
		// Not retried: it stays waiting.
		staysFor(t, time.Second, "still waiting, one record", s.isAt(desk, "waiting", 6))
	})
}

// Two remarks at once: each gets its own outcome, in whatever order the
// relay brings them. One reopens the desk; the other finds it working.
func TestE2E_TwoRemarksAtOnce_OneOutcomeEach(t *testing.T) {
	s := newDeskStack(t)
	desk := s.waitingDesk()
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.makeIn(alice, "wfremark", &desk, map[string]any{"f_public": true})
		}()
	}
	wg.Wait()
	eventually(t, 20*time.Second, "the move and two records", s.isAt(desk, "working", 7))
	s.waitCompleted(desk)
	got := records(t, desk)[1:] // after the checklist's
	slicesSort(got)
	want := []string{"reaction_applied wfdesk:reopen", "reaction_skipped wfdesk:reopen not_in_from"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("the two remarks' records = %v, want %v", got, want)
	}
}

func slicesSort(s []string) {
	for i := range s {
		for j := i + 1; j < len(s); j++ {
			if s[j] < s[i] {
				s[i], s[j] = s[j], s[i]
			}
		}
	}
}

// --- done when: entering a state can create a contained Item ---

func TestE2E_StartingWorkCreatesATodoInside(t *testing.T) {
	s := newDeskStack(t)
	desk := s.makeIn(alice, "wfdesk", nil, map[string]any{"f_title": "VPN"})
	s.move(desk, "working") // 2
	eventually(t, 15*time.Second, "the todo created and recorded", s.isAt(desk, "working", 3))
	s.waitCompleted(desk)
	if n := children(t, desk, "wftodo"); n != 1 {
		t.Fatalf("%d todos in the desk, want 1", n)
	}
	var todo uuid.UUID
	var createdBy string
	var onBehalfOf *string
	if err := pool.QueryRow(serverCtx(t),
		`select id, created_by, created_on_behalf_of from items where container_id = $1`, desk).Scan(&todo, &createdBy, &onBehalfOf); err != nil {
		t.Fatalf("read the todo: %v", err)
	}
	if createdBy != "reaction:wfdesk:checklist" || onBehalfOf != nil {
		t.Errorf("the todo was created by %s on behalf of %v; want the reaction, and nobody", createdBy, onBehalfOf)
	}
	if got, want := s.field(todo, "f_opened_at"), eventAt(t, desk, 2).UTC().Format(typedef.DateTimeLayout); got != want {
		t.Errorf("the todo's f_opened_at = %v, want the desk's event time %s", got, want)
	}
	if got := s.field(todo, "f_title"); got != "Check: VPN" {
		t.Errorf("the todo's f_title = %v", got)
	}
	if got := records(t, desk); fmt.Sprint(got) != fmt.Sprint([]string{"reaction_applied wfdesk:checklist"}) {
		t.Errorf("records = %v", got)
	}
}

// The worker dies before it runs the reaction; a new one finishes it, once.
func TestE2E_AReactionSurvivesAWorkerRestart(t *testing.T) {
	s := newDeskStack(t)
	desk := s.makeIn(alice, "wfdesk", nil, map[string]any{"f_title": "VPN"})
	s.stopWorker()
	s.move(desk, "working")
	time.Sleep(time.Second)
	s.startWorker()
	eventually(t, 20*time.Second, "the todo created and recorded", s.isAt(desk, "working", 3))
	staysFor(t, time.Second, "one todo, one record", func() bool {
		return children(t, desk, "wftodo") == 1 && len(records(t, desk)) == 1
	})
}

// --- only containers that declare it pay ---

func TestE2E_ARemarkElsewhereStartsNoWorkflow(t *testing.T) {
	s := newDeskStack(t)
	box := s.makeIn(alice, "wfremark", nil, nil) // a plain Item, used as a container
	remark := s.makeIn(alice, "wfremark", &box, map[string]any{"f_public": true})
	time.Sleep(time.Second)
	for name, id := range map[string]uuid.UUID{"the container": box, "the remark": remark} {
		if _, _, _, found := runStatus(t, id); found {
			t.Errorf("%s has a workflow; nothing here reacts", name)
		}
		var n int
		if err := pool.QueryRow(serverCtx(t), `select count(*) from outbox where item_id = $1`, id).Scan(&n); err != nil || n != 0 {
			t.Errorf("%s has %d outbox rows (%v), want 0", name, n, err)
		}
	}
	if _, seq := s.state(box); seq != 1 {
		t.Errorf("the container is at item_seq %d, want 1", seq)
	}
}

var _ = wf.Name
