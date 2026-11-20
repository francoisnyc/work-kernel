// Tests for Jira part 2, step 3a, in the item service (docs/design.md,
// "Decisions for Jira part 2"; the Phase A answers of step 3).
//
// The Types are test Types: a job, in a yard that has hours, contains
// meters. The job starts them when it is created, pauses, resumes and
// stops them as it moves; each meter shows its due instant on its job and
// marks itself late when that instant passes. Nothing is named after a goal.
//
//   - Creation enters the initial state: an enter reaction without `from`
//     is pending from the create event.
//   - A create reaction's fields read the hours linked from the job's
//     container, with that Type's consent (readable_by_reactions).
//   - A reaction with `children` moves contained Items: at most
//     MaxReactionTargets, one write and one outcome record per child, in
//     the order of their IDs. Every record but the last says `more`.
//   - by: not_creator.
//   - A consent *:<id> is honoured through containment only.
//   - A set reaction writes fields on the container (ReactSet).
//   - An acting timer at an instant (ActingInstants, ActAt): never before
//     the instant; cause.at is the instant.
//   - not_empty in filters.
//
// Time: the hours used here are around the clock in UTC, so working time
// is time and every expected instant is an event's own time plus a number
// of seconds. Business hours, weekends, holidays and DST are tested in
// packages worktime and lifecycle.
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
	"github.com/francoisnyc/work-kernel/internal/lifecycle"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

const r3Hours = `
id: r3hours
version: 1
fields:
  - {id: f_name,     type: string}
  - {id: f_week,     type: string, multiple: true, max: 21}
  - {id: f_days_off, type: date,   multiple: true, max: 366}
  - {id: f_zone,     type: string}
readable_by_reactions: [f_week, f_days_off, f_zone]
invariants:
  - {id: hours_invalid, rule: 'calendar(f_week, f_days_off, f_zone) != null'}
`

// Version 2 lets reactions read the week only.
const r3HoursV2 = `
id: r3hours
version: 2
fields:
  - {id: f_name,     type: string}
  - {id: f_week,     type: string, multiple: true, max: 21}
  - {id: f_days_off, type: date,   multiple: true, max: 366}
  - {id: f_zone,     type: string}
readable_by_reactions: [f_week]
`

const r3Yard = `
id: r3yard
version: 1
fields:
  - {id: f_name, type: string}
links:
  - {name: hours, inverse: yards, targets: [r3hours], max: one}
`

const r3Cal = "calendar(container.linked.hours.f_week, container.linked.hours.f_days_off, container.linked.hours.f_zone)"

const r3Job = `
id: r3job
version: 1
initial_state: open
fields:
  - {id: f_title,    type: string}
  - {id: f_budget_s, type: int}
  - {id: f_due,      type: datetime, writable_by: kernel}
states:
  - {id: open,    category: todo}
  - {id: working, category: active}
  - {id: waiting, category: waiting}
  - {id: done,    category: done}
transitions:
  - {from: open,    to: working}
  - {from: working, to: waiting}
  - {from: waiting, to: working}
  - {from: working, to: done}
  - {from: waiting, to: done}
  - {from: done,    to: working}
reactions:
  - id: start
    kind: create
    on: enter
    state: open
    type: r3meter
    version: 1
    fields: &main
      f_kind: '"main"'
      f_target_s: 'f_budget_s'
      f_started_at: 'cause.at'
      f_week: 'container.linked.hours.f_week'
      f_days_off: 'container.linked.hours.f_days_off'
      f_zone: 'container.linked.hours.f_zone'
      f_due_at: 'working_after(cause.at, f_budget_s, CAL)'
  - id: start_reply
    kind: create
    on: enter
    state: open
    type: r3meter
    version: 1
    fields:
      f_kind: '"reply"'
      f_target_s: '60'
      f_started_at: 'cause.at'
      f_week: 'container.linked.hours.f_week'
      f_days_off: 'container.linked.hours.f_days_off'
      f_zone: 'container.linked.hours.f_zone'
      f_due_at: 'working_after(cause.at, 60, CAL)'
  # A new cycle: work taken up again after done starts a new main meter.
  - {id: restart, kind: create, on: enter, state: working, from: [done], type: r3meter, version: 1, fields: *main}
  - {id: pause,  kind: move, on: enter, state: waiting, children: {of: [r3meter], when: 'child.f_kind == "main"'}, to: paused, target_from: [running]}
  - {id: resume, kind: move, on: enter, state: working, from: [waiting], children: {of: [r3meter], when: 'child.f_kind == "main"'}, to: running, target_from: [paused]}
  - {id: stop,   kind: move, on: enter, state: done, children: {of: [r3meter]}, to: stopped, target_from: [running, paused]}
  - {id: sweep,  kind: move, on: enter, state: done, children: {of: [r3tag]}, to: "off"}
  - {id: note,   kind: create, on: enter, state: done, type: r3remark, version: 1, fields: {f_public: 'true'}}
  - {id: answered, kind: move, on: created_inside, of: [r3remark], when: 'child.f_public == true', by: not_creator,
     children: {of: [r3meter], when: 'child.f_kind == "reply"'}, to: stopped, target_from: [running]}
accepts_reactions:
  r3meter:show: {sets: [f_due]}
  r3meter:hide: {sets: [f_due]}
  r3meter:clear: {sets: [f_due]}
`

const r3MeterCal = "calendar(f_week, f_days_off, f_zone)"

const r3Meter = `
id: r3meter
version: 1
initial_state: running
fields:
  - {id: f_kind,       type: enum, options: [main, reply]}
  - {id: f_target_s,   type: int,      writable_by: kernel}
  - {id: f_elapsed_s,  type: int,      writable_by: kernel}
  - {id: f_started_at, type: datetime, writable_by: kernel}
  - {id: f_due_at,     type: datetime, writable_by: kernel}
  - {id: f_late_at,    type: datetime, writable_by: kernel}
  - {id: f_outcome,    type: enum, options: [met, missed], writable_by: kernel}
  - {id: f_week,       type: string, multiple: true, max: 21,  writable_by: kernel}
  - {id: f_days_off,   type: date,   multiple: true, max: 366, writable_by: kernel}
  - {id: f_zone,       type: string, writable_by: kernel}
  - {id: f_note,       type: string}
states:
  - id: running
    category: active
    acting:
      - {id: late, at: f_due_at, when: 'f_late_at == null', sets: {f_late_at: 'cause.at'}}
  - {id: paused,  category: waiting}
  - {id: stopped, category: done}
transitions:
  - from: running
    to: paused
    sets:
      f_elapsed_s: '(f_elapsed_s == null ? 0 : f_elapsed_s) + working_between(f_started_at, cause.at, CAL)'
      f_late_at: 'f_due_at == null || cause.at >= f_due_at ? f_late_at : null'
      f_due_at: 'null'
  - from: paused
    to: running
    sets:
      f_started_at: 'cause.at'
      # Late already: the due instant stays the one it missed.
      f_due_at: 'f_late_at != null ? f_late_at : working_after(cause.at, f_target_s - f_elapsed_s, CAL)'
  - from: running
    to: stopped
    guard: 'f_note != "stuck"'
    sets:
      f_elapsed_s: '(f_elapsed_s == null ? 0 : f_elapsed_s) + working_between(f_started_at, cause.at, CAL)'
      f_outcome: '(f_elapsed_s == null ? 0 : f_elapsed_s) + working_between(f_started_at, cause.at, CAL) <= f_target_s ? "met" : "missed"'
      f_late_at: 'f_due_at == null || cause.at >= f_due_at ? f_late_at : null'
      f_due_at: 'null'
  - from: paused
    to: stopped
    sets:
      f_outcome: 'f_elapsed_s <= f_target_s ? "met" : "missed"'
reactions:
  - {id: show,  kind: set, on: enter, state: running, when: 'f_kind == "main"', target: container, set: {f_due: 'f_due_at'}}
  - {id: hide,  kind: set, on: enter, state: paused,  when: 'f_kind == "main"', target: container, set: {f_due: 'null'}}
  - {id: clear, kind: set, on: enter, state: stopped, when: 'f_kind == "main"', target: container, set: {f_due: 'null'}}
accepts_reactions:
  '*:start':       {creates: [f_kind, f_target_s, f_started_at, f_due_at, f_week, f_days_off, f_zone]}
  '*:start_reply': {creates: [f_kind, f_target_s, f_started_at, f_due_at, f_week, f_days_off, f_zone]}
  '*:restart':     {creates: [f_kind, f_target_s, f_started_at, f_due_at, f_week, f_days_off, f_zone]}
  '*:pause':    {moves: [paused]}
  '*:resume':   {moves: [running]}
  '*:stop':     {moves: [stopped]}
  '*:answered': {moves: [stopped]}
`

// A tag: something small a job sweeps away when it is done.
const r3Tag = `
id: r3tag
version: 1
initial_state: "on"
fields:
  - {id: f_note, type: string}
states:
  - {id: "on",  category: active}
  - {id: "off", category: done}
transitions:
  - {from: "on", to: "off", allowed: ['role:r3nobody'], guard: 'f_note != "stuck"'}
accepts_reactions:
  '*:sweep': {moves: ["off"]}
`

const r3Remark = `
id: r3remark
version: 1
fields:
  - {id: f_body,   type: string}
  - {id: f_public, type: bool}
accepts_reactions:
  '*:note': {creates: [f_public]}
`

// A stranger: its reaction has an ID meters accept from their container,
// but it reaches them over a link of its own. Loaded with the others it is
// refused (typedef's tests); registered alone, the write must refuse it.
const r3Rig = `
id: r3rig
version: 1
initial_state: up
states:
  - {id: up,   category: active}
  - {id: down, category: done}
transitions:
  - {from: up, to: down}
links:
  - {name: gauge, inverse: rigs, targets: [r3meter], max: one}
reactions:
  - {id: pause, kind: move, on: enter, state: down, link: gauge, to: paused}
`

var alwaysOpen = []any{"fri 00:00-24:00", "mon 00:00-24:00", "sat 00:00-24:00", "sun 00:00-24:00",
	"thu 00:00-24:00", "tue 00:00-24:00", "wed 00:00-24:00"}

type r3World struct {
	k *itemservice.Service
	// late is the same service with its clock an hour ahead: what an
	// instant in the near future looks like once it has passed.
	late                *itemservice.Service
	admin, ana, bob     item.Principal
	hours, yard         uuid.UUID
	hoursSeq, hoursComm int64
}

func newR3World(t *testing.T) r3World {
	t.Helper()
	k := itemservice.New(pool)
	for _, src := range []string{r3Hours, r3HoursV2, r3Yard, r3Remark, r3Tag,
		strings.ReplaceAll(r3Meter, "CAL", r3MeterCal), strings.ReplaceAll(r3Job, "CAL", r3Cal), r3Rig} {
		registerInline(t, k, src)
	}
	ns := newNamespace(t, k)
	w := r3World{k: k, late: itemservice.NewWithOptions(pool, itemservice.Options{ClockSkew: time.Hour}),
		admin: in(person("admin"), ns), ana: in(person("ana"), ns), bob: in(person("bob"), ns)}
	w.hours = w.create(t, w.admin, "r3hours", 1, nil, map[string]any{"f_name": "always", "f_week": alwaysOpen, "f_zone": "UTC"})
	w.yard = w.create(t, w.admin, "r3yard", 1, nil, map[string]any{"f_name": "yard " + word()})
	w.link(t, w.admin, w.yard, "hours", w.hours)
	return w
}

func (w r3World) create(t *testing.T, p item.Principal, typ string, version int, in *uuid.UUID, fields map[string]any) uuid.UUID {
	t.Helper()
	res, err := w.k.Create(testCtx(t), itemservice.CreateRequest{
		IdempotencyKey: newKey(), Principal: p, TypeID: typ, TypeVersion: version, ContainerID: in, Fields: fields,
		Attribution: item.Attribution{CorrelationID: "corr-" + uuid.NewString()[:8]},
	})
	if err != nil {
		t.Fatalf("create %s: %v", typ, err)
	}
	return res.ItemID
}

func (w r3World) link(t *testing.T, p item.Principal, src uuid.UUID, name string, dst uuid.UUID) {
	t.Helper()
	if _, err := w.k.Link(testCtx(t), itemservice.LinkRequest{
		IdempotencyKey: newKey(), Principal: p, SourceID: src, LinkType: name, TargetID: dst,
	}); err != nil {
		t.Fatalf("link %s: %v", name, err)
	}
}

func (w r3World) move(t *testing.T, k *itemservice.Service, p item.Principal, id uuid.UUID, to string) int64 {
	t.Helper()
	res, err := k.Transition(testCtx(t), itemservice.TransitionRequest{
		IdempotencyKey: newKey(), Principal: p, ItemID: id, ToState: to,
	})
	if err != nil {
		t.Fatalf("move to %s: %v", to, err)
	}
	return res.ItemSeq
}

// job is ana's job in the yard, with a budget in seconds. Nothing has run
// yet: its two start reactions are pending.
func (w r3World) job(t *testing.T, budget int) uuid.UUID {
	t.Helper()
	return w.create(t, w.ana, "r3job", 1, &w.yard, map[string]any{"f_title": "gate " + word(), "f_budget_s": budget})
}

func (w r3World) reactCreate(t *testing.T, k *itemservice.Service, req itemservice.ReactionRunRequest) itemservice.ReactionOutcome {
	t.Helper()
	out, err := k.Internal().ReactCreate(testCtx(t), req)
	if err != nil {
		t.Fatalf("ReactCreate %s: %v", req.Reaction, err)
	}
	return out
}

func (w r3World) reactMove(t *testing.T, k *itemservice.Service, req itemservice.ReactionRunRequest) itemservice.ReactionOutcome {
	t.Helper()
	out, err := k.Internal().ReactMove(testCtx(t), req)
	if err != nil {
		t.Fatalf("ReactMove %s: %v", req.Reaction, err)
	}
	return out
}

// started is a job whose two meters exist: main and reply.
func (w r3World) started(t *testing.T, budget int) (job, main, reply uuid.UUID) {
	t.Helper()
	job = w.job(t, budget)
	m := w.reactCreate(t, w.k, entered(job, 1, "start"))
	r := w.reactCreate(t, w.k, entered(job, 1, "start_reply"))
	if m.Outcome != itemservice.ReactionApplied || r.Outcome != itemservice.ReactionApplied {
		t.Fatalf("start = %+v, start_reply = %+v; want both applied", m, r)
	}
	return job, m.Target, r.Target
}

func (w r3World) fields(t *testing.T, id uuid.UUID) map[string]any {
	t.Helper()
	return fieldsOf(t, w.k, w.admin, id)
}

func eventAt(t *testing.T, id uuid.UUID, seq int64) time.Time {
	t.Helper()
	at, _ := eventTimes(t, id)
	if int(seq) > len(at) {
		t.Fatalf("%s has %d events, want at least %d", id, len(at), seq)
	}
	return at[seq-1]
}

func r3Int(t *testing.T, v any) int64 {
	t.Helper()
	n, ok := typedef.AsInt(v)
	if !ok {
		t.Fatalf("%v (%T) is not an int", v, v)
	}
	return n
}

func r3Strings(t *testing.T, v any) []string {
	t.Helper()
	var out []string
	switch l := v.(type) {
	case []string:
		return l
	case []any:
		for _, e := range l {
			out = append(out, fmt.Sprint(e))
		}
	case nil:
	default:
		t.Fatalf("%v (%T) is not a list", v, v)
	}
	return out
}

// records returns the reaction outcome records of id for one reaction, in
// order: op, target and whether it says more follow.
type r3Record struct {
	op, reason, target string
	more               bool
}

func recordsOf(t *testing.T, id uuid.UUID, reaction string) []r3Record {
	t.Helper()
	var out []r3Record
	for seq := int64(1); seq <= itemSeqOf(t, id); seq++ {
		op, payload := event(t, id, seq)
		if payload["reaction"] != reaction {
			continue
		}
		r := r3Record{op: op}
		r.reason, _ = payload["reason"].(string)
		r.target, _ = payload["target"].(string)
		r.more, _ = payload["more"].(bool)
		out = append(out, r)
	}
	return out
}

// --- creation enters the initial state ---

// A job's start reactions have no `from`: they are pending from its create
// event, and the create wrote an outbox row so a workflow hears of it.
func TestCreate_EntersTheInitialState_ReactionsArePending(t *testing.T) {
	w := newR3World(t)
	job := w.job(t, 600)

	var got []string
	for _, p := range stateEntry(t, w.k, job).PendingReactions {
		if p.CauseItem != job || p.CauseSeq != 1 {
			t.Errorf("pending %+v: want it caused by the job's create event", p)
		}
		got = append(got, p.Reaction)
	}
	if !slices.Equal(got, []string{"start", "start_reply"}) {
		t.Fatalf("pending at creation = %v, want [start start_reply] (restart has a `from`)", got)
	}
	if n := count(t, `select count(*) from outbox where item_id = $1`, job); n != 1 {
		t.Errorf("outbox rows for the job's create = %d, want 1", n)
	}
	// A reaction with a `from` is not triggered by a creation.
	_, err := w.k.Internal().ReactCreate(testCtx(t), entered(job, 1, "restart"))
	assertInvalid(t, "restart for the create event", err)
}

// The start reaction creates the meter from the job's fields, the create
// event's time and the hours linked from the job's container.
func TestReactCreate_AtCreation_ReadsTheContainersLinkedItem(t *testing.T) {
	w := newR3World(t)
	job := w.job(t, 600)
	time.Sleep(60 * time.Millisecond) // the reaction runs a moment later

	out := w.reactCreate(t, w.k, entered(job, 1, "start"))
	if out.Outcome != itemservice.ReactionApplied || out.Target == uuid.Nil {
		t.Fatalf("outcome = %+v, want applied", out)
	}
	meter := out.Target
	createdAt := eventAt(t, job, 1)
	f := w.fields(t, meter)
	if f["f_kind"] != "main" || r3Int(t, f["f_target_s"]) != 600 {
		t.Errorf("the meter = %v", f)
	}
	// Time comes from the cause: the job's create event, not the moment
	// the reaction ran.
	if f["f_started_at"] != stamp(createdAt) || f["f_due_at"] != stamp(createdAt.Add(600*time.Second)) {
		t.Errorf("f_started_at %v, f_due_at %v; want %s and ten minutes later", f["f_started_at"], f["f_due_at"], stamp(createdAt))
	}
	// The hours are copied onto the meter, whole.
	if !slices.Equal(r3Strings(t, f["f_week"]), r3Strings(t, alwaysOpen)) || f["f_zone"] != "UTC" || f["f_days_off"] != nil {
		t.Errorf("the hours copied = %v, %v, %v", f["f_week"], f["f_zone"], f["f_days_off"])
	}
	if s := stateOf(t, meter); s != "running" {
		t.Errorf("the meter is %s, want running", s)
	}
	assertRecord(t, job, lifecycle.OpReactionApplied, "r3job:start", "", job, 1)

	// The meter is contained in the job, made by the reaction, and its own
	// reaction (show, without `from`) is pending from its create event.
	if c := containerOf(t, meter); c == nil || *c != job {
		t.Errorf("the meter's container = %v, want the job", c)
	}
	got := stateEntry(t, w.k, meter).PendingReactions
	if len(got) != 1 || got[0].Reaction != "show" || got[0].CauseItem != meter || got[0].CauseSeq != 1 {
		t.Errorf("pending on the new meter = %+v, want show for its create", got)
	}
	if n := count(t, `select count(*) from outbox where item_id = $1`, meter); n != 1 {
		t.Errorf("outbox rows for the meter's create = %d, want 1", n)
	}
}

// The requester cannot see the hours: the yard's admin keeps them. The
// reaction reads them all the same. That is what readable_by_reactions
// consents to, and why it lists fields.
func TestReactCreate_ReadsLinkedFieldsTheRequesterCannotSee(t *testing.T) {
	w := newR3World(t)
	res, err := w.k.Create(testCtx(t), itemservice.CreateRequest{
		IdempotencyKey: newKey(), Principal: w.admin, TypeID: "r3hours", TypeVersion: 1,
		Fields: map[string]any{"f_name": "private", "f_week": []any{"sun 00:00-24:00"}, "f_zone": "Asia/Tokyo"},
		Narrow: &itemservice.Narrow{Readers: []string{w.admin.Actor}},
	})
	if err != nil {
		t.Fatalf("create narrowed hours: %v", err)
	}
	yard := w.create(t, w.admin, "r3yard", 1, nil, map[string]any{"f_name": "yard " + word()})
	w.link(t, w.admin, yard, "hours", res.ItemID)
	if _, err := w.k.Get(testCtx(t), w.ana, res.ItemID); !errors.Is(err, itemservice.ErrNotFound) {
		t.Fatalf("ana reads the narrowed hours: %v, want NOT_FOUND", err)
	}
	job := w.create(t, w.ana, "r3job", 1, &yard, map[string]any{"f_title": "x", "f_budget_s": 60})
	out := w.reactCreate(t, w.k, entered(job, 1, "start"))
	if out.Outcome != itemservice.ReactionApplied {
		t.Fatalf("outcome = %+v, want applied", out)
	}
	if f := fieldsOf(t, w.k, w.ana, out.Target); f["f_zone"] != "Asia/Tokyo" || !slices.Equal(r3Strings(t, f["f_week"]), []string{"sun 00:00-24:00"}) {
		t.Errorf("the meter's copy = %v, %v", f["f_week"], f["f_zone"])
	}
}

// No container, no link, or no consent: the reaction fails, once, and
// nothing is created.
func TestReactCreate_LinkedRead_Failures(t *testing.T) {
	w := newR3World(t)
	bare := w.create(t, w.admin, "r3yard", 1, nil, map[string]any{"f_name": "no hours"})
	v2 := w.create(t, w.admin, "r3hours", 2, nil, map[string]any{"f_name": "v2", "f_week": alwaysOpen, "f_zone": "UTC"})
	closed := w.create(t, w.admin, "r3yard", 1, nil, map[string]any{"f_name": "v2 hours"})
	w.link(t, w.admin, closed, "hours", v2)
	parent := w.job(t, 60) // a job is a container without a link named hours

	for _, c := range []struct {
		name, reason string
		in           *uuid.UUID
	}{
		{"the job is in no container", itemservice.CodeNoContainer, nil},
		{"the container has no hours", itemservice.CodeNotLinked, &bare},
		{"the container's Type has no such link", itemservice.CodeNotLinked, &parent},
		// The hours' own version decides what may be read: version 2 does
		// not list f_zone.
		{"the linked Item's version does not let the field be read", itemservice.CodeReadNotAccepted, &closed},
	} {
		t.Run(c.name, func(t *testing.T) {
			job := w.create(t, w.ana, "r3job", 1, c.in, map[string]any{"f_title": "x", "f_budget_s": 60})
			out, err := w.k.Internal().ReactCreate(testCtx(t), entered(job, 1, "start"))
			if err != nil {
				t.Fatalf("ReactCreate: %v (a failure is an outcome, not an error)", err)
			}
			if out.Outcome != itemservice.ReactionFailed || out.Reason != c.reason || out.Target != uuid.Nil {
				t.Fatalf("outcome = %+v, want failed %s", out, c.reason)
			}
			assertRecord(t, job, lifecycle.OpReactionFailed, "r3job:start", c.reason, job, 1)
			if n := count(t, `select count(*) from items where container_id = $1`, job); n != 0 {
				t.Errorf("%d Items were created inside the job, want none", n)
			}
			// Final: again changes nothing.
			seq := itemSeqOf(t, job)
			if _, err := w.k.Internal().ReactCreate(testCtx(t), entered(job, 1, "start")); err != nil || itemSeqOf(t, job) != seq {
				t.Errorf("again: %v, item_seq %d, want nothing written", err, itemSeqOf(t, job))
			}
		})
	}
}

// The meter keeps the hours it started with: editing the yard's hours, or
// linking the yard to others, changes no running meter.
func TestMeter_KeepsTheHoursItStartedWith(t *testing.T) {
	w := newR3World(t)
	job, main, _ := w.started(t, 600)
	if _, err := w.k.Update(testCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: w.admin, ItemID: w.hours,
		Fields: map[string]any{"f_week": []any{"mon 09:00-17:00"}, "f_zone": "Europe/Paris"},
	}); err != nil {
		t.Fatalf("edit the hours: %v", err)
	}
	w.move(t, w.k, w.ana, job, "working")
	waiting := w.move(t, w.k, w.ana, job, "waiting")
	w.reactMove(t, w.k, entered(job, waiting, "pause"))
	back := w.move(t, w.k, w.ana, job, "working")
	w.reactMove(t, w.k, entered(job, back, "resume"))

	f := w.fields(t, main)
	if f["f_zone"] != "UTC" || len(r3Strings(t, f["f_week"])) != 7 {
		t.Errorf("the meter's hours changed with the yard's: %v, %v", f["f_week"], f["f_zone"])
	}
	// Around the clock: due ten minutes of working time after the resume,
	// not next Monday morning in Paris.
	want := eventAt(t, job, back).Add(time.Duration(600-r3Int(t, f["f_elapsed_s"])) * time.Second)
	if f["f_due_at"] != stamp(want) {
		t.Errorf("f_due_at = %v, want %s (on the hours it started with)", f["f_due_at"], stamp(want))
	}
}

// --- children ---

// Waiting pauses the main meter, and only it; working again resumes it.
// Elapsed time and the due instant come from the job's events, however
// late the reaction runs.
func TestReactMove_Children_PauseAndResume(t *testing.T) {
	w := newR3World(t)
	job, main, reply := w.started(t, 600)
	w.move(t, w.k, w.ana, job, "working")
	time.Sleep(1100 * time.Millisecond) // a second of working time passes
	waiting := w.move(t, w.k, w.ana, job, "waiting")
	before := itemSeqOf(t, job)
	time.Sleep(60 * time.Millisecond)

	out := w.reactMove(t, w.k, entered(job, waiting, "pause"))
	if out.Outcome != itemservice.ReactionApplied || out.Target != main || len(out.Each) != 1 || out.Each[0].Target != main {
		t.Fatalf("pause = %+v, want applied on the main meter alone", out)
	}
	if stateOf(t, main) != "paused" || stateOf(t, reply) != "running" {
		t.Fatalf("main is %s, reply is %s; want paused and running", stateOf(t, main), stateOf(t, reply))
	}
	// The meter's move: made by the job's reaction, for ana, caused by the
	// job's event, with the wildcard consent standing in for `allowed`.
	seq := itemSeqOf(t, main)
	op, payload := event(t, main, seq)
	actor, attr := attribution(t, main, seq)
	if op != "transition" || payload["from"] != "running" || payload["to"] != "paused" || actor != "reaction:r3job:pause" ||
		attr.CausationID != fmt.Sprintf("%s:%d", job, waiting) || attr.Hop != 1 || attr.OriginatingHook != "reaction:r3job@v1:pause" {
		t.Errorf("the meter's move = %s %v by %s, %+v", op, payload, actor, attr)
	}
	if who := onBehalfOf(t, main, seq); who != w.ana.Actor {
		t.Errorf("the move is on behalf of %q, want %s", who, w.ana.Actor)
	}
	// One record on the job, the last (and only) one: it says no more follow.
	recs := recordsOf(t, job, "r3job:pause")
	if len(recs) != 1 || recs[0].op != lifecycle.OpReactionApplied || recs[0].target != main.String() || recs[0].more {
		t.Errorf("records = %+v, want one applied on the meter, without `more`", recs)
	}
	if itemSeqOf(t, job) != before+1 {
		t.Errorf("the job is at %d, want %d: one record", itemSeqOf(t, job), before+1)
	}

	// Elapsed: whole seconds of working time between the meter's start
	// (the job's create) and the event that entered waiting.
	f := w.fields(t, main)
	wantElapsed := int64(eventAt(t, job, waiting).Sub(eventAt(t, job, 1)) / time.Second)
	if got := r3Int(t, f["f_elapsed_s"]); got != wantElapsed || got < 1 {
		t.Errorf("f_elapsed_s = %d, want %d (at least the second slept)", got, wantElapsed)
	}
	if f["f_due_at"] != nil {
		t.Errorf("f_due_at = %v while paused, want unset", f["f_due_at"])
	}

	// Resume, run late: due = the resuming event's time + what is left.
	back := w.move(t, w.k, w.ana, job, "working")
	time.Sleep(60 * time.Millisecond)
	out = w.reactMove(t, w.k, entered(job, back, "resume"))
	if out.Outcome != itemservice.ReactionApplied || out.Target != main {
		t.Fatalf("resume = %+v, want applied on the main meter", out)
	}
	f = w.fields(t, main)
	backAt := eventAt(t, job, back)
	if f["f_started_at"] != stamp(backAt) || f["f_due_at"] != stamp(backAt.Add(time.Duration(600-wantElapsed)*time.Second)) {
		t.Errorf("after resume: f_started_at %v, f_due_at %v; want %s and %d s later", f["f_started_at"], f["f_due_at"], stamp(backAt), 600-wantElapsed)
	}
	if got := w.pendingOn(t, job); len(got) != 0 {
		t.Errorf("pending on the job = %+v, want none", got)
	}
}

func (w r3World) pendingOn(t *testing.T, id uuid.UUID) []string {
	t.Helper()
	var out []string
	for _, p := range stateEntry(t, w.k, id).PendingReactions {
		out = append(out, p.Reaction)
	}
	return out
}

// Done stops every meter, each in a write of its own, in the order of
// their IDs, with one record each on the job. Every record but the last
// says more follow: the reaction is finished by the one that does not.
func TestReactMove_Children_OneWriteAndOneRecordEach(t *testing.T) {
	w := newR3World(t)
	job, main, reply := w.started(t, 600)
	w.move(t, w.k, w.ana, job, "working")
	done := w.move(t, w.k, w.ana, job, "done")
	before := itemSeqOf(t, job)

	if got := w.pendingOn(t, job); !slices.Contains(got, "stop") {
		t.Fatalf("pending on the job = %v, want stop among them", got)
	}
	out := w.reactMove(t, w.k, entered(job, done, "stop"))
	first, second := main, reply
	if strings.Compare(first.String(), second.String()) > 0 {
		first, second = second, first
	}
	if len(out.Each) != 2 || out.Each[0].Target != first || out.Each[1].Target != second ||
		out.Each[0].Outcome != itemservice.ReactionApplied || out.Each[1].Outcome != itemservice.ReactionApplied {
		t.Fatalf("stop = %+v, want two applied outcomes in the order of the meters' IDs", out.Each)
	}
	if out.Outcome != itemservice.ReactionApplied || out.Target != second {
		t.Errorf("the reaction's own outcome = %+v, want the last child's", out)
	}
	if stateOf(t, main) != "stopped" || stateOf(t, reply) != "stopped" {
		t.Errorf("main is %s, reply is %s; want both stopped", stateOf(t, main), stateOf(t, reply))
	}
	recs := recordsOf(t, job, "r3job:stop")
	want := []r3Record{{op: lifecycle.OpReactionApplied, target: first.String(), more: true}, {op: lifecycle.OpReactionApplied, target: second.String()}}
	if !slices.Equal(recs, want) {
		t.Errorf("records = %+v, want %+v", recs, want)
	}
	// Both records are caused by the same event of the job, by the reaction.
	for seq := before + 1; seq <= before+2; seq++ {
		actor, attr := attribution(t, job, seq)
		if actor != "reaction:r3job:stop" || attr.CausationID != fmt.Sprintf("%s:%d", job, done) {
			t.Errorf("record %d: by %s, caused by %s", seq, actor, attr.CausationID)
		}
	}
	// The outcome is in the meter: met, with the elapsed time.
	if f := w.fields(t, main); f["f_outcome"] != "met" || f["f_due_at"] != nil {
		t.Errorf("the main meter after stop = %v", f)
	}
	if got := w.pendingOn(t, job); slices.Contains(got, "stop") {
		t.Errorf("stop is still pending: %v", got)
	}
	// Again: the same outcomes, nothing written, on the job or the meters.
	seqs := []int64{itemSeqOf(t, job), itemSeqOf(t, main), itemSeqOf(t, reply)}
	again := w.reactMove(t, w.k, entered(job, done, "stop"))
	if len(again.Each) != 2 || again.Each[0].Target != first || again.Each[1].Target != second {
		t.Errorf("again = %+v, want the same two outcomes", again.Each)
	}
	if got := []int64{itemSeqOf(t, job), itemSeqOf(t, main), itemSeqOf(t, reply)}; !slices.Equal(got, seqs) {
		t.Errorf("a second call wrote: item_seqs %v, want %v", got, seqs)
	}
}

// Nothing to move is one skipped record: no child of the Types, none for
// which the condition holds, or none in a state the reaction moves from.
// Children of other Items are never touched.
func TestReactMove_Children_NoTarget(t *testing.T) {
	w := newR3World(t)
	other, otherMain, _ := w.started(t, 600)
	_ = other

	// A job whose meters were never started: waiting finds no child.
	job := w.job(t, 600)
	w.move(t, w.k, w.ana, job, "working")
	waiting := w.move(t, w.k, w.ana, job, "waiting")
	out := w.reactMove(t, w.k, entered(job, waiting, "pause"))
	if out.Outcome != itemservice.ReactionSkipped || out.Reason != itemservice.SkipNoTarget || len(out.Each) != 0 {
		t.Fatalf("pause with no meter = %+v, want skipped no_target", out)
	}
	assertRecord(t, job, lifecycle.OpReactionSkipped, "r3job:pause", itemservice.SkipNoTarget, job, waiting)
	if stateOf(t, otherMain) != "running" {
		t.Errorf("another job's meter is %s, want running: it is not this job's child", stateOf(t, otherMain))
	}

	// Only a reply meter: the condition (main) holds for none.
	job2 := w.job(t, 600)
	w.reactCreate(t, w.k, entered(job2, 1, "start_reply"))
	w.move(t, w.k, w.ana, job2, "working")
	waiting = w.move(t, w.k, w.ana, job2, "waiting")
	if out := w.reactMove(t, w.k, entered(job2, waiting, "pause")); out.Outcome != itemservice.ReactionSkipped || out.Reason != itemservice.SkipNoTarget {
		t.Errorf("pause with only a reply meter = %+v, want skipped no_target", out)
	}

	// A main meter that is not paused: resume moves from paused only. A
	// child outside target_from is passed over without a record of its own.
	job3, main3, _ := w.started(t, 600)
	w.move(t, w.k, w.ana, job3, "working")
	w.move(t, w.k, w.ana, job3, "waiting") // pause is not run
	back := w.move(t, w.k, w.ana, job3, "working")
	before := itemSeqOf(t, job3)
	if out := w.reactMove(t, w.k, entered(job3, back, "resume")); out.Outcome != itemservice.ReactionSkipped || out.Reason != itemservice.SkipNoTarget {
		t.Errorf("resume of a meter that was never paused = %+v, want skipped no_target", out)
	}
	if stateOf(t, main3) != "running" || itemSeqOf(t, job3) != before+1 {
		t.Errorf("main is %s, the job at %d; want running and one record (%d)", stateOf(t, main3), itemSeqOf(t, job3), before+1)
	}
}

// A child that refuses is a failure recorded for that child; the others
// are still moved. The consent stands in for `allowed`, never for a guard.
func TestReactMove_Children_ARefusalIsRecordedPerChild(t *testing.T) {
	w := newR3World(t)
	job := w.job(t, 600)
	var tags []uuid.UUID
	for i := 0; i < 3; i++ {
		tags = append(tags, w.create(t, w.ana, "r3tag", 1, &job, nil))
	}
	slices.SortFunc(tags, func(a, b uuid.UUID) int { return strings.Compare(a.String(), b.String()) })
	stuck := tags[1]
	if _, err := w.k.Update(testCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: w.ana, ItemID: stuck, Fields: map[string]any{"f_note": "stuck"},
	}); err != nil {
		t.Fatalf("update: %v", err)
	}
	// By hand nobody may switch a tag off: the role exists for no one.
	_, err := w.k.Transition(testCtx(t), itemservice.TransitionRequest{IdempotencyKey: newKey(), Principal: w.ana, ItemID: tags[0], ToState: "off"})
	assertForbidden(t, err)

	w.move(t, w.k, w.ana, job, "working")
	done := w.move(t, w.k, w.ana, job, "done")
	out := w.reactMove(t, w.k, entered(job, done, "sweep"))
	if len(out.Each) != 3 {
		t.Fatalf("sweep = %+v, want three outcomes", out.Each)
	}
	for i, want := range []string{itemservice.ReactionApplied, itemservice.ReactionFailed, itemservice.ReactionApplied} {
		if out.Each[i].Outcome != want || out.Each[i].Target != tags[i] {
			t.Errorf("outcome %d = %+v, want %s on %s", i, out.Each[i], want, tags[i])
		}
	}
	if out.Each[1].Reason != itemservice.CodeGuardFailed {
		t.Errorf("the stuck tag's reason = %q, want guard_failed", out.Each[1].Reason)
	}
	if stateOf(t, tags[0]) != "off" || stateOf(t, stuck) != "on" || stateOf(t, tags[2]) != "off" {
		t.Errorf("tags are %s, %s, %s; want off, on, off", stateOf(t, tags[0]), stateOf(t, stuck), stateOf(t, tags[2]))
	}
	want := []r3Record{
		{op: lifecycle.OpReactionApplied, target: tags[0].String(), more: true},
		{op: lifecycle.OpReactionFailed, reason: itemservice.CodeGuardFailed, target: stuck.String(), more: true},
		{op: lifecycle.OpReactionApplied, target: tags[2].String()},
	}
	if recs := recordsOf(t, job, "r3job:sweep"); !slices.Equal(recs, want) {
		t.Errorf("records = %+v, want %+v", recs, want)
	}
	// A refusal is not retried: unsticking the tag later moves nothing.
	if _, err := w.k.Update(testCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: w.ana, ItemID: stuck, Fields: map[string]any{"f_note": "free"},
	}); err != nil {
		t.Fatalf("update: %v", err)
	}
	w.reactMove(t, w.k, entered(job, done, "sweep"))
	if stateOf(t, stuck) != "on" {
		t.Errorf("the tag moved on a second call; a recorded failure is final")
	}
}

// At most MaxReactionTargets children per trigger. One more, and nothing
// moves: one failure, too_many_targets.
func TestReactMove_Children_AtMostEight(t *testing.T) {
	w := newR3World(t)
	sweep := func(n int) (uuid.UUID, []uuid.UUID, itemservice.ReactionOutcome) {
		job := w.job(t, 600)
		var tags []uuid.UUID
		for i := 0; i < n; i++ {
			tags = append(tags, w.create(t, w.ana, "r3tag", 1, &job, nil))
		}
		w.move(t, w.k, w.ana, job, "working")
		done := w.move(t, w.k, w.ana, job, "done")
		return job, tags, w.reactMove(t, w.k, entered(job, done, "sweep"))
	}

	job, tags, out := sweep(itemservice.MaxReactionTargets)
	if len(out.Each) != itemservice.MaxReactionTargets || out.Outcome != itemservice.ReactionApplied {
		t.Fatalf("eight tags: %+v, want eight outcomes", out)
	}
	for _, tag := range tags {
		if stateOf(t, tag) != "off" {
			t.Errorf("tag %s is %s, want off", tag, stateOf(t, tag))
		}
	}
	if n := len(recordsOf(t, job, "r3job:sweep")); n != itemservice.MaxReactionTargets {
		t.Errorf("%d records, want %d", n, itemservice.MaxReactionTargets)
	}

	job, tags, out = sweep(itemservice.MaxReactionTargets + 1)
	if out.Outcome != itemservice.ReactionFailed || out.Reason != itemservice.CodeTooManyTargets || len(out.Each) != 0 || out.Target != uuid.Nil {
		t.Fatalf("nine tags: %+v, want failed too_many_targets", out)
	}
	for _, tag := range tags {
		if stateOf(t, tag) != "on" {
			t.Errorf("tag %s is %s, want on: nothing moves", tag, stateOf(t, tag))
		}
	}
	want := []r3Record{{op: lifecycle.OpReactionFailed, reason: itemservice.CodeTooManyTargets}}
	if recs := recordsOf(t, job, "r3job:sweep"); !slices.Equal(recs, want) {
		t.Errorf("records = %+v, want %+v", recs, want)
	}
}

// --- consent by reaction ID ---

// A stranger whose reaction has the accepted ID, but reaches a meter over
// its own link: *:pause does not cover it.
func TestReactMove_AStrangerOverALinkIsNotAccepted(t *testing.T) {
	w := newR3World(t)
	_, main, _ := w.started(t, 600)
	rig := w.create(t, w.ana, "r3rig", 1, nil, nil)
	w.link(t, w.ana, rig, "gauge", main)
	down := w.move(t, w.k, w.ana, rig, "down")

	out := w.reactMove(t, w.k, entered(rig, down, "pause"))
	if out.Outcome != itemservice.ReactionFailed || out.Reason != itemservice.CodeReactionNotAccepted {
		t.Fatalf("outcome = %+v, want failed reaction_not_accepted", out)
	}
	if s := stateOf(t, main); s != "running" {
		t.Errorf("the meter is %s, want running", s)
	}
	assertRecord(t, rig, lifecycle.OpReactionFailed, "r3rig:pause", itemservice.CodeReactionNotAccepted, rig, down)
}

// --- by: not_creator ---

// A public remark by someone else than the job's requester stops the reply
// meter. The requester's own, an agent's acting for them, a private one
// and one made by the kernel do not.
func TestCreatedInside_ByNotCreator_MovesASibling(t *testing.T) {
	w := newR3World(t)
	remark := func(p item.Principal, job uuid.UUID, public bool) uuid.UUID {
		return w.create(t, p, "r3remark", 1, &job, map[string]any{"f_body": "news " + word(), "f_public": public})
	}

	job, main, reply := w.started(t, 600)
	r := remark(w.bob, job, true)
	if got := w.pendingOn(t, job); !slices.Equal(got, []string{"answered"}) {
		t.Fatalf("pending on the job = %v, want [answered]", got)
	}
	out := w.reactMove(t, w.k, inside(job, r, "answered"))
	if out.Outcome != itemservice.ReactionApplied || out.Target != reply || len(out.Each) != 1 {
		t.Fatalf("bob's public remark: %+v, want applied on the reply meter", out)
	}
	if stateOf(t, reply) != "stopped" || stateOf(t, main) != "running" || stateOf(t, job) != "open" {
		t.Errorf("reply %s, main %s, job %s; want stopped, running, open", stateOf(t, reply), stateOf(t, main), stateOf(t, job))
	}
	// The reply meter's time is the remark's: stopped at its create event.
	if f := w.fields(t, reply); f["f_outcome"] != "met" {
		t.Errorf("the reply meter = %v, want met", f)
	}
	assertRecord(t, job, lifecycle.OpReactionApplied, "r3job:answered", "", r, 1)
	// A second public remark finds nothing running: no_target.
	r2 := remark(w.bob, job, true)
	if out := w.reactMove(t, w.k, inside(job, r2, "answered")); out.Outcome != itemservice.ReactionSkipped || out.Reason != itemservice.SkipNoTarget {
		t.Errorf("a second remark: %+v, want skipped no_target", out)
	}

	agent := withSession(t, w.k, in(item.Principal{Actor: "agent:" + uuid.NewString()[:8], OnBehalfOf: w.ana.Actor}, w.ana.Namespace))
	for _, c := range []struct {
		name, reason string
		by           item.Principal
		public       bool
	}{
		{"the requester's own remark", itemservice.SkipNotBy, w.ana, true},
		{"an agent acting for the requester", itemservice.SkipNotBy, agent, true},
		{"a private remark by someone else", itemservice.SkipConditionFalse, w.bob, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			job, _, reply := w.started(t, 600)
			r := remark(c.by, job, c.public)
			out := w.reactMove(t, w.k, inside(job, r, "answered"))
			if out.Outcome != itemservice.ReactionSkipped || out.Reason != c.reason {
				t.Fatalf("outcome = %+v, want skipped %s", out, c.reason)
			}
			if s := stateOf(t, reply); s != "running" {
				t.Errorf("the reply meter is %s, want running", s)
			}
		})
	}

	// A remark the kernel made (the job's own `note` reaction, public) has
	// no human creator: it is neither the requester's nor someone else's.
	t.Run("a remark created by a reaction", func(t *testing.T) {
		job, _, reply := w.started(t, 600)
		w.move(t, w.k, w.ana, job, "working")
		done := w.move(t, w.k, w.ana, job, "done")
		note := w.reactCreate(t, w.k, entered(job, done, "note")) // run before `stop`
		if note.Outcome != itemservice.ReactionApplied {
			t.Fatalf("note = %+v, want applied", note)
		}
		out := w.reactMove(t, w.k, inside(job, note.Target, "answered"))
		if out.Outcome != itemservice.ReactionSkipped || out.Reason != itemservice.SkipNotBy {
			t.Fatalf("outcome = %+v, want skipped not_by", out)
		}
		if s := stateOf(t, reply); s != "running" {
			t.Errorf("the reply meter is %s, want running", s)
		}
	})
}

// --- set ---

// A running main meter shows its due instant on its job; paused, it takes
// it away. The job's field is the kernel's: nobody writes it by hand.
func TestReactSet_WritesTheContainersField(t *testing.T) {
	w := newR3World(t)
	job, main, reply := w.started(t, 600)
	before := itemSeqOf(t, job)

	out, err := w.k.Internal().ReactSet(testCtx(t), itemservice.ReactionRunRequest{
		SourceID: main, Reaction: "show", CauseItem: main, CauseSeq: 1, CorrelationID: "corr-reaction", Hop: 2,
	})
	if err != nil {
		t.Fatalf("ReactSet: %v", err)
	}
	if out.Outcome != itemservice.ReactionApplied || out.Target != job {
		t.Fatalf("outcome = %+v, want applied on the job", out)
	}
	due := w.fields(t, main)["f_due_at"]
	if got := w.fields(t, job)["f_due"]; got == nil || got != due {
		t.Errorf("the job's f_due = %v, want the meter's f_due_at %v", got, due)
	}
	// One event on the job: an update of that field, by the meter's
	// reaction, caused by the meter's event, two hops from the job's own.
	if itemSeqOf(t, job) != before+1 {
		t.Fatalf("the job is at %d, want %d: one write", itemSeqOf(t, job), before+1)
	}
	op, payload := event(t, job, before+1)
	actor, attr := attribution(t, job, before+1)
	if op != "update" || fmt.Sprint(payload["field_ids"]) != "[f_due]" || actor != "reaction:r3meter:show" ||
		attr.CausationID != fmt.Sprintf("%s:1", main) || attr.Hop != 2 || attr.OriginatingHook != "reaction:r3meter@v1:show" {
		t.Errorf("the job's event = %s %v by %s, %+v", op, payload, actor, attr)
	}
	if s := stateOf(t, job); s != "open" {
		t.Errorf("the job is %s, want open: a set moves nothing", s)
	}
	// The record is on the meter, which declares the reaction.
	recs := recordsOf(t, main, "r3meter:show")
	if len(recs) != 1 || recs[0].op != lifecycle.OpReactionApplied || recs[0].target != job.String() {
		t.Errorf("records on the meter = %+v, want one applied on the job", recs)
	}
	// Again: nothing written.
	seqs := []int64{itemSeqOf(t, job), itemSeqOf(t, main)}
	if _, err := w.k.Internal().ReactSet(testCtx(t), itemservice.ReactionRunRequest{
		SourceID: main, Reaction: "show", CauseItem: main, CauseSeq: 1, CorrelationID: "corr-reaction", Hop: 2,
	}); err != nil || itemSeqOf(t, job) != seqs[0] || itemSeqOf(t, main) != seqs[1] {
		t.Errorf("again: %v; item_seqs %d, %d, want %v", err, itemSeqOf(t, job), itemSeqOf(t, main), seqs)
	}

	// The reply meter's show has a condition (main only): skipped.
	skipped, err := w.k.Internal().ReactSet(testCtx(t), entered(reply, 1, "show"))
	if err != nil || skipped.Outcome != itemservice.ReactionSkipped || skipped.Reason != itemservice.SkipConditionFalse {
		t.Errorf("the reply meter's show = %+v, %v; want skipped condition_false", skipped, err)
	}

	// By hand the field is refused, as any kernel-written field.
	_, err = w.k.Update(testCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: w.ana, ItemID: job, Fields: map[string]any{"f_due": "2030-01-01T00:00:00Z"},
	})
	assertNotWritable(t, "f_due by hand", err, "f_due")

	// Paused: the meter's hide unsets it. The event names it as cleared.
	w.move(t, w.k, w.ana, job, "working")
	waiting := w.move(t, w.k, w.ana, job, "waiting")
	w.reactMove(t, w.k, entered(job, waiting, "pause"))
	pausedSeq := itemSeqOf(t, main)
	before = itemSeqOf(t, job)
	hid, err := w.k.Internal().ReactSet(testCtx(t), entered(main, pausedSeq, "hide"))
	if err != nil || hid.Outcome != itemservice.ReactionApplied || hid.Target != job {
		t.Fatalf("hide = %+v, %v; want applied on the job", hid, err)
	}
	if got := w.fields(t, job)["f_due"]; got != nil {
		t.Errorf("the job's f_due = %v after the pause, want unset", got)
	}
	if _, payload := event(t, job, before+1); fmt.Sprint(payload["cleared_field_ids"]) != "[f_due]" {
		t.Errorf("the job's event = %v, want f_due among cleared_field_ids", payload)
	}
}

// A container that did not consent refuses; an Item in no container has
// no target. A reaction that is not a set is not ReactSet's to run.
func TestReactSet_Refusals(t *testing.T) {
	w := newR3World(t)
	// A meter someone put straight into the yard: the yard accepts nothing.
	inYard := w.create(t, w.ana, "r3meter", 1, &w.yard, map[string]any{"f_kind": "main"})
	out, err := w.k.Internal().ReactSet(testCtx(t), entered(inYard, 1, "show"))
	if err != nil || out.Outcome != itemservice.ReactionFailed || out.Reason != itemservice.CodeReactionNotAccepted || out.Target != w.yard {
		t.Errorf("a meter in a yard: %+v, %v; want failed reaction_not_accepted on the yard", out, err)
	}
	if seq := itemSeqOf(t, w.yard); seq != 2 { // its create, and the link
		t.Errorf("the yard is at %d, want 2: nothing written", seq)
	}
	alone := w.create(t, w.ana, "r3meter", 1, nil, map[string]any{"f_kind": "main"})
	out, err = w.k.Internal().ReactSet(testCtx(t), entered(alone, 1, "show"))
	if err != nil || out.Outcome != itemservice.ReactionSkipped || out.Reason != itemservice.SkipNoTarget {
		t.Errorf("a meter in nothing: %+v, %v; want skipped no_target", out, err)
	}

	job, main, _ := w.started(t, 600)
	_, err = w.k.Internal().ReactSet(testCtx(t), entered(job, 1, "start"))
	assertInvalid(t, "ReactSet for a create reaction", err)
	_, err = w.k.Internal().ReactMove(testCtx(t), entered(main, 1, "show"))
	assertInvalid(t, "ReactMove for a set reaction", err)
	_, err = w.k.Internal().ReactSet(testCtx(t), entered(main, 1, "hide")) // hide runs on paused, not at creation
	assertInvalid(t, "hide for the create event", err)
}

// --- an acting timer at an instant ---

func (w r3World) actAt(t *testing.T, k *itemservice.Service, meter uuid.UUID, enteredSeq int64, instant time.Time) (itemservice.ActAtResult, error) {
	t.Helper()
	return k.Internal().ActAt(testCtx(t), itemservice.ActAtRequest{
		ItemID: meter, Timer: "late", EnteredSeq: enteredSeq, Instant: instant,
		Attribution: item.Attribution{CorrelationID: "corr-timer", Hop: 2},
	})
}

func r3Instant(t *testing.T, v any) time.Time {
	t.Helper()
	at, ok := typedef.ParseDateTime(v)
	if !ok {
		t.Fatalf("%v is not an instant", v)
	}
	return at
}

func TestActingInstants_ReadsTheStatesTimers(t *testing.T) {
	w := newR3World(t)
	job, main, _ := w.started(t, 600)
	got, err := w.k.ActingInstants(testCtx(t), main)
	if err != nil {
		t.Fatalf("ActingInstants: %v", err)
	}
	due := r3Instant(t, w.fields(t, main)["f_due_at"])
	if len(got) != 1 || got[0].Timer != "late" || !got[0].Set || !got[0].At.Equal(due) {
		t.Errorf("ActingInstants = %+v, want late at %v", got, due)
	}
	// Paused: the state has no such timer.
	w.move(t, w.k, w.ana, job, "working")
	waiting := w.move(t, w.k, w.ana, job, "waiting")
	w.reactMove(t, w.k, entered(job, waiting, "pause"))
	if got, err := w.k.ActingInstants(testCtx(t), main); err != nil || len(got) != 0 {
		t.Errorf("ActingInstants while paused = %+v, %v; want none", got, err)
	}
	// A meter someone made by hand has no due instant: listed, unset.
	bare := w.create(t, w.ana, "r3meter", 1, nil, map[string]any{"f_kind": "main"})
	if got, err := w.k.ActingInstants(testCtx(t), bare); err != nil || len(got) != 1 || got[0].Set {
		t.Errorf("ActingInstants with the field unset = %+v, %v; want late, unset", got, err)
	}
	if _, err := w.k.ActingInstants(testCtx(t), uuid.New()); !errors.Is(err, itemservice.ErrNotFound) {
		t.Errorf("ActingInstants of no Item: %v, want ErrNotFound", err)
	}
}

// Never before the instant: by the item service's own clock the due
// instant is ten minutes away, and nothing is written. Once it has passed,
// the timer sets f_late_at to the instant itself, in one field write by
// system:timer, and the meter stays running.
func TestActAt_NeverBeforeTheInstant_ThenRecordsTheInstant(t *testing.T) {
	w := newR3World(t)
	_, main, _ := w.started(t, 600)
	due := r3Instant(t, w.fields(t, main)["f_due_at"])
	before := itemSeqOf(t, main)

	_, err := w.actAt(t, w.k, main, 1, due)
	var notYet *itemservice.NotYetError
	if !errors.As(err, &notYet) || notYet.Remaining <= 9*time.Minute || notYet.Remaining > 10*time.Minute {
		t.Fatalf("before the instant: %v, want ErrNotYet with about ten minutes left", err)
	}
	if itemSeqOf(t, main) != before || w.fields(t, main)["f_late_at"] != nil {
		t.Fatalf("something was written before the instant")
	}

	// An hour later, by a service whose clock is an hour ahead.
	res, err := w.actAt(t, w.late, main, 1, due)
	if err != nil {
		t.Fatalf("ActAt after the instant: %v", err)
	}
	if !res.Applied || res.RunHook || res.ItemSeq != before+1 {
		t.Fatalf("result = %+v, want applied at item_seq %d, and no hook to run (the timer declares none)", res, before+1)
	}
	f := w.fields(t, main)
	if f["f_late_at"] != stamp(due) {
		t.Errorf("f_late_at = %v, want the due instant %s (not the time it was written)", f["f_late_at"], stamp(due))
	}
	if s := stateOf(t, main); s != "running" {
		t.Errorf("the meter is %s, want running: it goes on", s)
	}
	if f["f_due_at"] != stamp(due) {
		t.Errorf("f_due_at = %v, want it untouched", f["f_due_at"])
	}
	op, payload := event(t, main, before+1)
	actor, attr := attribution(t, main, before+1)
	if op != "update" || fmt.Sprint(payload["field_ids"]) != "[f_late_at]" || actor != "system:timer" ||
		attr.OriginatingHook != "timer:r3meter@v1:late" || attr.CausationID != fmt.Sprintf("%s:1", main) ||
		attr.Hop != 2 || attr.CorrelationID != "corr-timer" {
		t.Errorf("the event = %s %v by %s, %+v", op, payload, actor, attr)
	}
	if _, has := payload["to"]; has {
		t.Errorf("the event has a `to`: %v; the timer makes no move", payload)
	}
	// The write is heard: the meter has listeners.
	if n := count(t, `select count(*) from outbox where item_id = $1 and item_seq = $2`, main, before+1); n != 1 {
		t.Errorf("outbox rows for the timer's write = %d, want 1", n)
	}

	// The same instant again, on either clock: the first answer, nothing
	// written. An instant acts once.
	for _, k := range []*itemservice.Service{w.late, w.k} {
		again, err := w.actAt(t, k, main, 1, due)
		if err != nil || again.CommitSeq != res.CommitSeq || itemSeqOf(t, main) != before+1 {
			t.Errorf("again: %+v, %v; want the first result and nothing written", again, err)
		}
	}
}

// What the timer was armed for is gone: the Item left the state, entered
// it again, or the field holds another instant, or none. Nothing is
// written; the workflow looks again.
func TestActAt_Stale(t *testing.T) {
	w := newR3World(t)
	job, main, _ := w.started(t, 600)
	due := r3Instant(t, w.fields(t, main)["f_due_at"])

	_, err := w.actAt(t, w.late, main, 1, due.Add(time.Second))
	assertStale(t, err) // not the instant the field holds
	_, err = w.actAt(t, w.late, main, 7, due)
	assertStale(t, err) // not the event that entered the state

	w.move(t, w.k, w.ana, job, "working")
	waiting := w.move(t, w.k, w.ana, job, "waiting")
	w.reactMove(t, w.k, entered(job, waiting, "pause"))
	before := itemSeqOf(t, main)
	_, err = w.actAt(t, w.late, main, 1, due)
	assertStale(t, err) // paused: the state has no such timer, and the field is unset

	back := w.move(t, w.k, w.ana, job, "working")
	w.reactMove(t, w.k, entered(job, back, "resume"))
	_, err = w.actAt(t, w.late, main, 1, due)
	assertStale(t, err) // running again, but by a later entry, with a later instant
	if itemSeqOf(t, main) != before+1 || w.fields(t, main)["f_late_at"] != nil {
		t.Errorf("a stale timer wrote something")
	}
	// For the new entry and the new instant it acts.
	newDue := r3Instant(t, w.fields(t, main)["f_due_at"])
	if !newDue.After(due) {
		t.Fatalf("the due instant did not move with the pause: %v then %v", due, newDue)
	}
	res, err := w.actAt(t, w.late, main, before+1, newDue)
	if err != nil || !res.Applied || w.fields(t, main)["f_late_at"] != stamp(newDue) {
		t.Errorf("for the new entry: %+v, %v, f_late_at %v; want applied at the new instant", res, err, w.fields(t, main)["f_late_at"])
	}

	// A timer the state does not declare, or a missing Item.
	_, err = w.late.Internal().ActAt(testCtx(t), itemservice.ActAtRequest{ItemID: main, Timer: "nope", EnteredSeq: before + 1, Instant: newDue,
		Attribution: item.Attribution{Hop: 1}})
	assertInvalid(t, "an unknown timer", err)
	_, err = w.actAt(t, w.late, uuid.New(), 1, due)
	if !errors.Is(err, itemservice.ErrNotFound) {
		t.Errorf("no such Item: %v, want ErrNotFound", err)
	}
}

// Its condition does not hold: late already, a meter resumed keeps the
// instant it missed as its due instant, and the timer does nothing for it.
func TestActAt_WhenItsConditionDoesNotHold_WritesNothing(t *testing.T) {
	w := newR3World(t)
	job, main, _ := w.started(t, 600)
	due := r3Instant(t, w.fields(t, main)["f_due_at"])
	if _, err := w.actAt(t, w.late, main, 1, due); err != nil {
		t.Fatalf("ActAt: %v", err)
	}
	// An hour later (the skewed clock) the job waits, then works again.
	w.move(t, w.late, w.ana, job, "working")
	waiting := w.move(t, w.late, w.ana, job, "waiting")
	w.reactMove(t, w.late, entered(job, waiting, "pause"))
	if got := w.fields(t, main)["f_late_at"]; got != stamp(due) {
		t.Fatalf("f_late_at = %v after a pause caused after the due instant, want it kept", got)
	}
	back := w.move(t, w.late, w.ana, job, "working")
	w.reactMove(t, w.late, entered(job, back, "resume"))
	f := w.fields(t, main)
	if f["f_due_at"] != stamp(due) {
		t.Fatalf("f_due_at = %v after resuming late, want the instant it missed %s", f["f_due_at"], stamp(due))
	}
	entry := itemSeqOf(t, main)
	res, err := w.actAt(t, w.late, main, entry, due)
	if err != nil || res.Applied || res.RunHook {
		t.Errorf("ActAt on a meter already late: %+v, %v; want nothing applied and no hook", res, err)
	}
	if itemSeqOf(t, main) != entry {
		t.Errorf("the meter is at %d, want %d: nothing written", itemSeqOf(t, main), entry)
	}
}

// The timer fired before the pause was heard of: the job waited before the
// due instant, but its reaction ran after the timer. The pause's time is
// its cause's, so the late mark is taken back, and no time was lost.
func TestActAt_ThenAPauseCausedBeforeTheInstant_TakesTheMarkBack(t *testing.T) {
	w := newR3World(t)
	job, main, _ := w.started(t, 600)
	due := r3Instant(t, w.fields(t, main)["f_due_at"])
	w.move(t, w.k, w.ana, job, "working")
	waiting := w.move(t, w.k, w.ana, job, "waiting") // now: ten minutes before the due instant

	if _, err := w.actAt(t, w.late, main, 1, due); err != nil { // the worker comes back an hour later; the timer first
		t.Fatalf("ActAt: %v", err)
	}
	if w.fields(t, main)["f_late_at"] == nil {
		t.Fatal("the timer did not mark the meter late")
	}
	w.reactMove(t, w.late, entered(job, waiting, "pause")) // then the pause, an hour late
	f := w.fields(t, main)
	if f["f_late_at"] != nil {
		t.Errorf("f_late_at = %v, want it taken back: the pause was caused before the due instant", f["f_late_at"])
	}
	if got := r3Int(t, f["f_elapsed_s"]); got > 5 {
		t.Errorf("f_elapsed_s = %d, want the seconds before the job waited, not the hour the worker was away", got)
	}
}

// --- hours must be hours ---

func TestInvariant_HoursMustBeACalendar(t *testing.T) {
	w := newR3World(t)
	mk := func(fields map[string]any) error {
		_, err := w.k.Create(testCtx(t), itemservice.CreateRequest{
			IdempotencyKey: newKey(), Principal: w.admin, TypeID: "r3hours", TypeVersion: 1, Fields: fields,
		})
		return err
	}
	for name, fields := range map[string]map[string]any{
		"no working time":       {"f_name": "x", "f_zone": "UTC"},
		"no zone":               {"f_name": "x", "f_week": alwaysOpen},
		"an unknown zone":       {"f_name": "x", "f_week": alwaysOpen, "f_zone": "Europe/Atlantis"},
		"a line that is no day": {"f_name": "x", "f_week": []any{"someday 09:00-17:00"}, "f_zone": "UTC"},
		"overlapping intervals": {"f_name": "x", "f_week": []any{"mon 09:00-13:00", "mon 12:00-17:00"}, "f_zone": "UTC"},
	} {
		assertInvariantFailed(t, name, mk(fields), "hours_invalid")
	}
	if err := mk(map[string]any{"f_week": []any{"mon 09:00-17:00"}, "f_days_off": []any{"2026-12-25"}, "f_zone": "Europe/Paris"}); err != nil {
		t.Errorf("an office in Paris: %v", err)
	}
	// An update that breaks the calendar is refused too.
	_, err := w.k.Update(testCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: w.admin, ItemID: w.hours, Fields: map[string]any{"f_zone": nil},
	})
	assertInvariantFailed(t, "unsetting the zone", err, "hours_invalid")
}

// --- not_empty ---

// "Has a value": the meters marked late, among those that are not.
func TestFilter_NotEmpty(t *testing.T) {
	w := newR3World(t)
	_, late, reply := w.started(t, 600)
	_, onTime, _ := w.started(t, 600)
	if _, err := w.actAt(t, w.late, late, 1, r3Instant(t, w.fields(t, late)["f_due_at"])); err != nil {
		t.Fatalf("ActAt: %v", err)
	}
	find := func(ffs ...itemservice.FieldFilter) ([]uuid.UUID, error) {
		res, err := w.k.Search(testCtx(t), itemservice.SearchRequest{
			Principal: w.admin, MinSeq: lastCommitSeq(t),
			Filter: itemservice.Filter{TypeIDs: []string{"r3meter"}, Fields: ffs},
		})
		var ids []uuid.UUID
		for _, it := range res.Items {
			ids = append(ids, it.ID)
		}
		return ids, err
	}
	got, err := find(itemservice.FieldFilter{Field: "f_late_at", NotEmpty: true})
	if err != nil || !sameIDs(got, []uuid.UUID{late}) {
		t.Errorf("late meters = %v, %v; want %s alone", got, err, late)
	}
	// With another condition; and a multi-value field with a member.
	got, err = find(itemservice.FieldFilter{Field: "f_late_at", Empty: true}, itemservice.FieldFilter{Field: "f_kind", Eq: "main"})
	if err != nil || !sameIDs(got, []uuid.UUID{onTime}) {
		t.Errorf("main meters not late = %v, %v; want %s", got, err, onTime)
	}
	got, err = find(itemservice.FieldFilter{Field: "f_week", NotEmpty: true}, itemservice.FieldFilter{Field: "f_kind", Eq: "reply"})
	if err != nil || len(got) != 2 || !slices.Contains(got, reply) {
		t.Errorf("reply meters with a week = %v, %v; want the two reply meters", got, err)
	}
	// The same through a container's listing.
	listed, err := w.k.List(testCtx(t), itemservice.ListRequest{
		Principal: w.admin, ContainerID: containerOf(t, late),
		Filter: itemservice.Filter{Fields: []itemservice.FieldFilter{{Field: "f_late_at", NotEmpty: true}}},
	})
	if err != nil || len(listed.Items) != 1 || listed.Items[0].ID != late {
		t.Errorf("listing the job's late meters = %+v, %v; want the late one", listed.Items, err)
	}

	for name, ff := range map[string]itemservice.FieldFilter{
		"not_empty with empty":          {Field: "f_late_at", Empty: true, NotEmpty: true},
		"not_empty with eq":             {Field: "f_kind", Eq: "main", NotEmpty: true},
		"not_empty with a bound":        {Field: "f_late_at", Gt: "2026-01-01T00:00:00Z", NotEmpty: true},
		"a field no Type here declares": {Field: "f_nope", NotEmpty: true},
	} {
		_, err := find(ff)
		assertInvalid(t, name, err)
	}
}
