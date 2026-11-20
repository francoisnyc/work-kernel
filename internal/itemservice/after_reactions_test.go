// Tests for Jira part 2, step 2: reactions that move or create, in the item
// service (docs/design.md, "Decisions for Jira part 2", primitive 3).
//
// Internal().ReactMove and ReactCreate run one reaction for one trigger:
// they judge the conditions, write the target under a key of its own, then
// record ONE outcome on the Item that declares the reaction, as a
// field-less event caused by the trigger: reaction_applied,
// reaction_failed (the target refused; not retried) or reaction_skipped
// (it rightly did nothing: condition_false, not_by, not_in_from,
// no_target). Until that record exists the reaction is pending
// (StateEntry.PendingReactions), so a restart finishes it; with it, calling
// again writes nothing.
//
//   - created_inside: an Item of one of `of`, created inside an Item whose
//     Type declares the reaction, moves that Item. The rule is the
//     container's, so it needs no consent and stands in for the
//     transition's `allowed`; guards, required fields and invariants apply.
//   - on: enter, kind: move: the Item a max-one link points to is moved; its
//     Type consents (moves), which stands in for `allowed` the same way.
//   - kind: create: an Item is created inside the source. It has no human
//     creator; its readers are its container's.
//   - Time comes from the cause: in a reaction's write cause.at is the
//     TRIGGERING event's time, not the moment the reaction runs. The
//     write's own event still carries its own, later, time.
package itemservice_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
	"github.com/francoisnyc/work-kernel/internal/lifecycle"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

const r2Desk = `
id: r2desk
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
  - {id: done,    category: done}
transitions:
  - {from: open,    to: working}
  - {from: working, to: waiting}
  # Only support may take a desk out of waiting by hand, and only with an
  # assignee. The reopen reaction stands in for the first, not the second.
  - from: waiting
    to: working
    allowed: ['role:r2support']
    guard: 'f_assignee != null'
    sets: {f_reopened_at: 'cause.at'}
  - {from: working, to: done}
reactions:
  - {id: reopen, kind: move, on: created_inside, of: [r2remark], when: 'child.f_public == true', by: creator, to: working, from: [waiting]}
`

// A bench: starting work on it (from open only) creates a todo inside it.
const r2Bench = `
id: r2bench
version: 1
initial_state: open
fields:
  - {id: f_title, type: string}
states:
  - {id: open,    category: todo}
  - {id: working, category: active}
  - {id: waiting, category: waiting}
transitions:
  - {from: open,    to: working}
  - {from: working, to: waiting}
  - {from: waiting, to: working}
reactions:
  - {id: checklist, kind: create, on: enter, state: working, from: [open], type: r2todo, version: 1,
     fields: {f_title: '"Check: " + f_title', f_opened_at: 'cause.at'}}
`

const r2Remark = `
id: r2remark
version: 1
fields:
  - {id: f_body,   type: string}
  - {id: f_public, type: bool}
`

const r2Todo = `
id: r2todo
version: 1
fields:
  - {id: f_title,     type: string}
  - {id: f_opened_at, type: datetime, writable_by: kernel}
accepts_reactions:
  r2bench:checklist: {creates: [f_title, f_opened_at]}
`

const r2Part = `
id: r2part
version: 1
initial_state: open
fields:
  - {id: f_name, type: string}
  - {id: f_ok,   type: bool}
states:
  - {id: open, category: todo}
  - {id: done, category: done}
transitions:
  - {from: open, to: done}
links:
  - {name: whole, inverse: parts, targets: [r2whole], max: one}
reactions:
  - {id: finish, kind: move, on: enter, state: done, when: 'f_ok == true', link: whole, to: done, target_from: [building]}
`

const r2Whole = `
id: r2whole
version: 1
initial_state: building
fields:
  - {id: f_name,    type: string}
  - {id: f_done_at, type: datetime, writable_by: kernel}
states:
  - {id: building, category: active}
  - {id: paused,   category: waiting}
  - {id: done,     category: done}
transitions:
  - from: building
    to: done
    allowed: ['role:r2lead']
    guard: 'f_name != null'
    sets: {f_done_at: 'cause.at'}
  - {from: building, to: paused}
  - {from: paused,   to: building}
accepts_reactions:
  r2part:finish: {moves: [done]}
`

type r2World struct {
	k              *itemservice.Service
	ana, bob, carl item.Principal
}

func newR2World(t *testing.T) r2World {
	t.Helper()
	k := itemservice.New(pool)
	for _, src := range []string{r2Remark, r2Todo, r2Desk, r2Bench, r2Whole, r2Part} {
		registerInline(t, k, src)
	}
	ns := newNamespace(t, k)
	return r2World{k: k, ana: in(person("ana"), ns), bob: in(person("bob"), ns), carl: in(person("carl"), ns)}
}

func (w r2World) create(t *testing.T, p item.Principal, typ string, in *uuid.UUID, fields map[string]any, readers ...item.Principal) uuid.UUID {
	t.Helper()
	req := itemservice.CreateRequest{
		IdempotencyKey: newKey(), Principal: p, TypeID: typ, TypeVersion: 1, ContainerID: in, Fields: fields,
		Attribution: item.Attribution{CorrelationID: "corr-" + uuid.NewString()[:8]},
	}
	if len(readers) > 0 {
		req.Narrow = &itemservice.Narrow{}
		for _, r := range readers {
			req.Narrow.Readers = append(req.Narrow.Readers, r.Actor)
		}
	}
	res, err := w.k.Create(testCtx(t), req)
	if err != nil {
		t.Fatalf("create %s: %v", typ, err)
	}
	return res.ItemID
}

func (w r2World) move(t *testing.T, p item.Principal, id uuid.UUID, to string) itemservice.TransitionResult {
	t.Helper()
	res, err := w.k.Transition(testCtx(t), itemservice.TransitionRequest{
		IdempotencyKey: newKey(), Principal: p, ItemID: id, ToState: to,
	})
	if err != nil {
		t.Fatalf("move to %s: %v", to, err)
	}
	return res
}

func (w r2World) update(t *testing.T, p item.Principal, id uuid.UUID, fields map[string]any) {
	t.Helper()
	if _, err := w.k.Update(testCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: p, ItemID: id, Fields: fields,
	}); err != nil {
		t.Fatalf("update %v: %v", fields, err)
	}
}

// waitingDesk: ana's desk, assigned to bob, taken to waiting (event 3).
func (w r2World) waitingDesk(t *testing.T) uuid.UUID {
	t.Helper()
	desk := w.create(t, w.ana, "r2desk", nil, map[string]any{"f_title": "VPN " + word(), "f_assignee": w.bob.Actor})
	w.move(t, w.ana, desk, "working") // 2
	w.move(t, w.ana, desk, "waiting") // 3
	return desk
}

func (w r2World) remark(t *testing.T, p item.Principal, in uuid.UUID, public any) uuid.UUID {
	t.Helper()
	fields := map[string]any{"f_body": "any news? " + word()}
	if public != nil {
		fields["f_public"] = public
	}
	return w.create(t, p, "r2remark", &in, fields)
}

// inside is the trigger "child was created inside source".
func inside(source, child uuid.UUID, reaction string) itemservice.ReactionRunRequest {
	return itemservice.ReactionRunRequest{
		SourceID: source, Reaction: reaction, CauseItem: child, CauseSeq: 1,
		CorrelationID: "corr-reaction", Hop: 1,
	}
}

// entered is the trigger "source entered a state with its event seq".
func entered(source uuid.UUID, seq int64, reaction string) itemservice.ReactionRunRequest {
	return itemservice.ReactionRunRequest{
		SourceID: source, Reaction: reaction, CauseItem: source, CauseSeq: seq,
		CorrelationID: "corr-reaction", Hop: 1,
	}
}

func (w r2World) pending(t *testing.T, id uuid.UUID) []itemservice.PendingReaction {
	t.Helper()
	se, err := w.k.StateEntry(testCtx(t), id)
	if err != nil {
		t.Fatalf("StateEntry: %v", err)
	}
	return se.PendingReactions
}

// assertRecord checks the last event of id: the reaction's one outcome
// record, field-less, written by the reaction and caused by the trigger.
func assertRecord(t *testing.T, id uuid.UUID, op, reaction, reason string, causeItem uuid.UUID, causeSeq int64) {
	t.Helper()
	seq := itemSeqOf(t, id)
	gotOp, payload := event(t, id, seq)
	if gotOp != op || payload["reaction"] != reaction {
		t.Fatalf("the last event of %s = %s %v, want %s of %s", id, gotOp, payload, op, reaction)
	}
	if reason != "" && payload["reason"] != reason {
		t.Errorf("reason = %v, want %s", payload["reason"], reason)
	}
	for _, k := range []string{"field_ids", "cleared_field_ids", "to", "from"} {
		if v, has := payload[k]; has {
			t.Errorf("the record's payload has %s = %v; it changes no field and no state", k, v)
		}
	}
	actor, attr := attribution(t, id, seq)
	_, rid, _ := cutLast(reaction)
	if actor != "reaction:"+reaction || attr.CausationID != fmt.Sprintf("%s:%d", causeItem, causeSeq) || attr.Hop != 1 {
		t.Errorf("the record: actor %s, cause %s, hop %d; want reaction:%s, %s:%d, hop 1 (reaction %s)",
			actor, attr.CausationID, attr.Hop, reaction, causeItem, causeSeq, rid)
	}
}

func cutLast(key string) (typ, id string, ok bool) {
	for i := len(key) - 1; i >= 0; i-- {
		if key[i] == ':' {
			return key[:i], key[i+1:], true
		}
	}
	return "", key, false
}

// --- created_inside ---

// The classic: the requester's public remark, made inside a desk that
// waits for them, sets it working again. By hand she could not.
func TestCreatedInside_ARemarkByTheCreatorMovesItsContainer(t *testing.T) {
	w := newR2World(t)
	desk := w.waitingDesk(t)
	_, err := w.k.Transition(testCtx(t), itemservice.TransitionRequest{
		IdempotencyKey: newKey(), Principal: w.ana, ItemID: desk, ToState: "working",
	})
	assertForbidden(t, err) // support only

	remark := w.remark(t, w.ana, desk, true)
	if got := w.pending(t, desk); len(got) != 1 || got[0].Reaction != "reopen" || got[0].CauseItem != remark || got[0].CauseSeq != 1 {
		t.Fatalf("pending on the desk = %+v, want reopen for the remark's create", got)
	}
	// The reaction runs a moment later: time must still come from the cause.
	time.Sleep(60 * time.Millisecond)

	out, err := w.k.Internal().ReactMove(testCtx(t), inside(desk, remark, "reopen"))
	if err != nil {
		t.Fatalf("ReactMove: %v", err)
	}
	if out.Outcome != itemservice.ReactionApplied || out.Target != desk || out.Reason != "" {
		t.Fatalf("outcome = %+v, want applied on the desk", out)
	}
	if s := stateOf(t, desk); s != "working" {
		t.Fatalf("the desk is %s, want working", s)
	}
	// Event 4 is the move, made by the reaction for ana, caused by the
	// remark's create; event 5 is the record.
	op, payload := event(t, desk, 4)
	actor, attr := attribution(t, desk, 4)
	if op != "transition" || payload["from"] != "waiting" || payload["to"] != "working" || actor != "reaction:r2desk:reopen" ||
		attr.CausationID != fmt.Sprintf("%s:1", remark) || attr.Hop != 1 || attr.CorrelationID != "corr-reaction" ||
		attr.OriginatingHook != "reaction:r2desk@v1:reopen" {
		t.Errorf("the move = %s %v by %s, %+v", op, payload, actor, attr)
	}
	if who := onBehalfOf(t, desk, 4); who != w.ana.Actor {
		t.Errorf("the move is on behalf of %q, want the remark's author %s", who, w.ana.Actor)
	}
	assertRecord(t, desk, lifecycle.OpReactionApplied, "r2desk:reopen", "", remark, 1)
	if seq := itemSeqOf(t, desk); seq != 5 {
		t.Errorf("the desk is at item_seq %d, want 5: the move and its record", seq)
	}
	if got := w.pending(t, desk); len(got) != 0 {
		t.Errorf("pending after the record = %+v, want none", got)
	}

	// Two times: cause.at is the remark's create event's time; the move's
	// own event is stamped later, when it was written.
	remarkAt, _ := eventTimes(t, remark)
	deskAt, _ := eventTimes(t, desk)
	if got := fieldsOf(t, w.k, w.ana, desk)["f_reopened_at"]; got != stamp(remarkAt[0]) {
		t.Errorf("f_reopened_at = %v, want the remark's create time %s (cause.at is the cause's)", got, stamp(remarkAt[0]))
	}
	if d := deskAt[3].Sub(remarkAt[0]); d < 50*time.Millisecond {
		t.Errorf("the move's event is stamped %v after the remark; want its own, later time", d)
	}
	if !deskAt[3].After(deskAt[2]) || !deskAt[4].After(deskAt[3]) {
		t.Errorf("the desk's event times go backwards: %v", deskAt)
	}

	// Again: the same outcome, and nothing written.
	again, err := w.k.Internal().ReactMove(testCtx(t), inside(desk, remark, "reopen"))
	if err != nil || again.Outcome != itemservice.ReactionApplied || again.Record.CommitSeq != out.Record.CommitSeq {
		t.Errorf("again = %+v, %v; want the first outcome %+v", again, err, out)
	}
	if seq := itemSeqOf(t, desk); seq != 5 {
		t.Errorf("the desk is at item_seq %d after a second call, want 5", seq)
	}
	// The remark itself is untouched: one event, its create.
	if seq := itemSeqOf(t, remark); seq != 1 {
		t.Errorf("the remark is at item_seq %d, want 1", seq)
	}
}

// It rightly does nothing, and says why, once: a private remark, a remark
// by someone else, a desk that is not waiting.
func TestCreatedInside_Skipped(t *testing.T) {
	w := newR2World(t)
	cases := []struct {
		name, reason string
		setup        func(desk uuid.UUID) uuid.UUID // returns the remark
		state        string
	}{
		{"a private remark", itemservice.SkipConditionFalse, func(d uuid.UUID) uuid.UUID { return w.remark(t, w.ana, d, false) }, "waiting"},
		{"a remark that does not say", itemservice.SkipConditionFalse, func(d uuid.UUID) uuid.UUID { return w.remark(t, w.ana, d, nil) }, "waiting"},
		{"a public remark by someone else", itemservice.SkipNotBy, func(d uuid.UUID) uuid.UUID { return w.remark(t, w.bob, d, true) }, "waiting"},
		{"a desk that is not waiting", itemservice.SkipNotInFrom, func(d uuid.UUID) uuid.UUID {
			grantRoleIn(t, w.ana.Namespace, w.carl, "r2support")
			w.move(t, w.carl, d, "working") // support takes it back first
			return w.remark(t, w.ana, d, true)
		}, "working"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			desk := w.waitingDesk(t)
			remark := c.setup(desk)
			before := itemSeqOf(t, desk)
			out, err := w.k.Internal().ReactMove(testCtx(t), inside(desk, remark, "reopen"))
			if err != nil {
				t.Fatalf("ReactMove: %v", err)
			}
			if out.Outcome != itemservice.ReactionSkipped || out.Reason != c.reason || out.Target != uuid.Nil {
				t.Fatalf("outcome = %+v, want skipped %s", out, c.reason)
			}
			assertRecord(t, desk, lifecycle.OpReactionSkipped, "r2desk:reopen", c.reason, remark, 1)
			if s, seq := stateOf(t, desk), itemSeqOf(t, desk); s != c.state || seq != before+1 {
				t.Errorf("the desk is %s at %d, want %s at %d: one record, no move", s, seq, c.state, before+1)
			}
			if got := w.pending(t, desk); len(got) != 0 {
				t.Errorf("pending = %+v, want none: a skip is finished", got)
			}
			// A skip is final: the desk waiting again later does not revive it.
			if _, err := w.k.Internal().ReactMove(testCtx(t), inside(desk, remark, "reopen")); err != nil {
				t.Fatalf("again: %v", err)
			}
			if seq := itemSeqOf(t, desk); seq != before+1 {
				t.Errorf("a second call wrote: item_seq %d, want %d", seq, before+1)
			}
		})
	}
}

// The reaction stands in for `allowed`, not for the guard: without an
// assignee the desk refuses, and that is recorded, not retried.
func TestCreatedInside_ARefusedMoveIsRecordedAsFailed(t *testing.T) {
	w := newR2World(t)
	desk := w.waitingDesk(t)
	w.update(t, w.ana, desk, map[string]any{"f_assignee": nil}) // 4
	remark := w.remark(t, w.ana, desk, true)

	out, err := w.k.Internal().ReactMove(testCtx(t), inside(desk, remark, "reopen"))
	if err != nil {
		t.Fatalf("ReactMove: %v (a refusal is an outcome, not an error)", err)
	}
	if out.Outcome != itemservice.ReactionFailed || out.Reason != itemservice.CodeGuardFailed {
		t.Fatalf("outcome = %+v, want failed guard_failed", out)
	}
	assertRecord(t, desk, lifecycle.OpReactionFailed, "r2desk:reopen", itemservice.CodeGuardFailed, remark, 1)
	if s, seq := stateOf(t, desk), itemSeqOf(t, desk); s != "waiting" || seq != 5 {
		t.Errorf("the desk is %s at %d, want waiting at 5", s, seq)
	}
	// Fixing the desk later does not replay the remark.
	w.update(t, w.ana, desk, map[string]any{"f_assignee": w.bob.Actor}) // 6
	if out, err := w.k.Internal().ReactMove(testCtx(t), inside(desk, remark, "reopen")); err != nil || out.Outcome != itemservice.ReactionFailed {
		t.Errorf("again = %+v, %v; want the recorded failure", out, err)
	}
	if s := stateOf(t, desk); s != "waiting" {
		t.Errorf("the desk is %s, want still waiting", s)
	}
}

// What is no trigger of the reaction is malformed, and writes nothing.
func TestCreatedInside_OnlyARealTrigger(t *testing.T) {
	w := newR2World(t)
	desk := w.waitingDesk(t)
	other := w.waitingDesk(t)
	elsewhere := w.remark(t, w.ana, other, true)
	todo := w.create(t, w.ana, "r2remark", nil, map[string]any{"f_public": true}) // at the top level
	inDesk := w.remark(t, w.ana, desk, true)
	sub := w.create(t, w.ana, "r2desk", &desk, map[string]any{"f_title": "a desk in a desk"}) // not one of `of`

	for name, req := range map[string]itemservice.ReactionRunRequest{
		"a remark made in another desk":   inside(desk, elsewhere, "reopen"),
		"a remark at the top level":       inside(desk, todo, "reopen"),
		"an Item of a Type not in `of`":   inside(desk, sub, "reopen"),
		"an event that is not the create": {SourceID: desk, Reaction: "reopen", CauseItem: inDesk, CauseSeq: 2, Hop: 1},
		"an unknown reaction":             inside(desk, inDesk, "nope"),
		"no hop":                          {SourceID: desk, Reaction: "reopen", CauseItem: inDesk, CauseSeq: 1},
	} {
		_, err := w.k.Internal().ReactMove(testCtx(t), req)
		assertInvalid(t, name, err)
	}
	if _, err := w.k.Internal().ReactMove(testCtx(t), inside(uuid.New(), inDesk, "reopen")); !errors.Is(err, itemservice.ErrNotFound) {
		t.Errorf("no such Item: %v, want ErrNotFound", err)
	}
	if seq := itemSeqOf(t, desk); seq != 3 {
		t.Errorf("the desk is at item_seq %d, want 3: nothing written", seq)
	}
	// The one real trigger is still pending.
	if got := w.pending(t, desk); len(got) != 1 || got[0].CauseItem != inDesk {
		t.Errorf("pending = %+v, want the remark made in the desk", got)
	}
}

// A crash after the move and before the record: the reaction is still
// pending, the next attempt finds the move under its key, and records.
func TestCreatedInside_ACrashBetweenTheMoveAndTheRecord(t *testing.T) {
	w := newR2World(t)
	desk := w.waitingDesk(t)
	remark := w.remark(t, w.ana, desk, true)

	// The reaction makes two writes, each a transaction: the move, then
	// the record. The second fails once.
	writes := 0
	restore := itemservice.SetTestHook(func(ctx context.Context, tx pgx.Tx, point string) error {
		if markOf(ctx) == "crash" && point == "write" {
			if writes++; writes == 2 {
				return errors.New("the worker died here")
			}
		}
		return nil
	})
	_, err := w.k.Internal().ReactMove(marked(testCtx(t), "crash"), inside(desk, remark, "reopen"))
	restore()
	if err == nil || errors.Is(err, itemservice.ErrRejected) || errors.Is(err, itemservice.ErrInvalid) {
		t.Fatalf("ReactMove = %v, want the crash as a plain error (retried)", err)
	}
	if s, seq := stateOf(t, desk), itemSeqOf(t, desk); s != "working" || seq != 4 {
		t.Fatalf("the desk is %s at %d, want working at 4: moved, not yet recorded", s, seq)
	}
	if got := w.pending(t, desk); len(got) != 1 || got[0].CauseItem != remark {
		t.Fatalf("pending = %+v, want the reaction still pending", got)
	}

	out, err := w.k.Internal().ReactMove(testCtx(t), inside(desk, remark, "reopen"))
	if err != nil || out.Outcome != itemservice.ReactionApplied {
		t.Fatalf("the retry = %+v, %v; want applied", out, err)
	}
	assertRecord(t, desk, lifecycle.OpReactionApplied, "r2desk:reopen", "", remark, 1)
	if seq := itemSeqOf(t, desk); seq != 5 {
		t.Errorf("the desk is at item_seq %d, want 5: one move, one record", seq)
	}
	if n := count(t, `select count(*) from item_events where item_id = $1 and op = 'transition' and actor like 'reaction:%'`, desk); n != 1 {
		t.Errorf("%d moves by the reaction, want 1", n)
	}
	if got := w.pending(t, desk); len(got) != 0 {
		t.Errorf("pending = %+v, want none", got)
	}
}

// Only a container that declares the reaction pays for it. A remark in a
// desk gets an outbox row (the relay tells the desk); a remark in an Item
// whose Type declares nothing costs its row and its event, nothing more,
// and its container is not written at all.
func TestCreatedInside_OnlyContainersThatDeclareItPay(t *testing.T) {
	w := newR2World(t)
	desk := w.waitingDesk(t)
	inDesk := w.remark(t, w.ana, desk, true)
	if n := count(t, `select count(*) from outbox where item_id = $1`, inDesk); n != 1 {
		t.Errorf("a remark in a desk: %d outbox rows, want 1 (its create, for the desk's workflow)", n)
	}
	w.update(t, w.ana, inDesk, map[string]any{"f_body": "edited"})
	if n := count(t, `select count(*) from outbox where item_id = $1`, inDesk); n != 1 {
		t.Errorf("after an edit: %d outbox rows, want still 1: only the create is told", n)
	}

	box := w.create(t, w.ana, "r2todo", nil, map[string]any{"f_title": "a plain box"})
	inBox := w.remark(t, w.ana, box, true)
	top := w.remark2(t, w.ana)
	for name, id := range map[string]uuid.UUID{"in a plain container": inBox, "at the top level": top} {
		if n := count(t, `select count(*) from outbox where item_id = $1`, id); n != 0 {
			t.Errorf("a remark %s: %d outbox rows, want 0", name, n)
		}
		if n := count(t, `select count(*) from item_events where item_id = $1`, id); n != 1 {
			t.Errorf("a remark %s: %d events, want 1", name, n)
		}
	}
	if seq := itemSeqOf(t, box); seq != 1 {
		t.Errorf("the plain container is at item_seq %d, want 1: nothing recorded on it", seq)
	}
	if got := w.pending(t, box); len(got) != 0 {
		t.Errorf("pending on the plain container = %+v, want none", got)
	}
}

func (w r2World) remark2(t *testing.T, p item.Principal) uuid.UUID {
	t.Helper()
	return w.create(t, p, "r2remark", nil, map[string]any{"f_public": true})
}

// --- on: enter, kind: move ---

// partOfWhole returns a part linked to a new whole (named, building); the part is
// at item_seq 2 (create, link).
func (w r2World) partOfWhole(t *testing.T, ok any, wholeName any) (part, whole uuid.UUID) {
	t.Helper()
	fields := map[string]any{}
	if wholeName != nil {
		fields["f_name"] = wholeName
	}
	whole = w.create(t, w.ana, "r2whole", nil, fields)
	fields = map[string]any{"f_name": "a part"}
	if ok != nil {
		fields["f_ok"] = ok
	}
	part = w.create(t, w.ana, "r2part", nil, fields)
	if _, err := w.k.Link(testCtx(t), itemservice.LinkRequest{
		IdempotencyKey: newKey(), Principal: w.ana, SourceID: part, LinkType: "whole", TargetID: whole,
	}); err != nil {
		t.Fatalf("link: %v", err)
	}
	return part, whole
}

// A part that is done finishes its whole: the whole's Type consents, which
// stands in for its `allowed` (leads only), and the whole's effect reads
// the PART's event time.
func TestMoveOnEnter_MovesTheLinkedItem(t *testing.T) {
	w := newR2World(t)
	part, whole := w.partOfWhole(t, true, "a whole")
	_, err := w.k.Transition(testCtx(t), itemservice.TransitionRequest{
		IdempotencyKey: newKey(), Principal: w.ana, ItemID: whole, ToState: "done",
	})
	assertForbidden(t, err)

	w.move(t, w.ana, part, "done") // 3
	if got := w.pending(t, part); len(got) != 1 || got[0].Reaction != "finish" || got[0].CauseItem != part || got[0].CauseSeq != 3 {
		t.Fatalf("pending on the part = %+v, want finish for event 3", got)
	}
	time.Sleep(60 * time.Millisecond)
	out, err := w.k.Internal().ReactMove(testCtx(t), entered(part, 3, "finish"))
	if err != nil || out.Outcome != itemservice.ReactionApplied || out.Target != whole {
		t.Fatalf("ReactMove = %+v, %v; want applied on the whole", out, err)
	}
	if s := stateOf(t, whole); s != "done" {
		t.Fatalf("the whole is %s, want done", s)
	}
	actor, attr := attribution(t, whole, 2)
	if actor != "reaction:r2part:finish" || attr.CausationID != fmt.Sprintf("%s:3", part) || attr.Hop != 1 {
		t.Errorf("the whole's move: by %s, %+v", actor, attr)
	}
	assertRecord(t, part, lifecycle.OpReactionApplied, "r2part:finish", "", part, 3)
	_, payload := event(t, part, 4)
	if payload["target"] != whole.String() {
		t.Errorf("the record names target %v, want the whole", payload["target"])
	}
	partAt, _ := eventTimes(t, part)
	wholeAt, _ := eventTimes(t, whole)
	if got := fieldsOf(t, w.k, w.ana, whole)["f_done_at"]; got != stamp(partAt[2]) {
		t.Errorf("f_done_at = %v, want the part's done event time %s", got, stamp(partAt[2]))
	}
	if d := wholeAt[1].Sub(partAt[2]); d < 50*time.Millisecond {
		t.Errorf("the whole's event is stamped %v after the part's; want its own time", d)
	}
	if got := w.pending(t, part); len(got) != 0 {
		t.Errorf("pending = %+v, want none", got)
	}
}

func TestMoveOnEnter_SkippedAndFailed(t *testing.T) {
	w := newR2World(t)
	run := func(part uuid.UUID) itemservice.ReactionOutcome {
		t.Helper()
		w.move(t, w.ana, part, "done")
		out, err := w.k.Internal().ReactMove(testCtx(t), entered(part, itemSeqOf(t, part), "finish"))
		if err != nil {
			t.Fatalf("ReactMove: %v", err)
		}
		return out
	}
	t.Run("the condition is false", func(t *testing.T) {
		part, whole := w.partOfWhole(t, false, "a whole")
		if out := run(part); out.Outcome != itemservice.ReactionSkipped || out.Reason != itemservice.SkipConditionFalse {
			t.Errorf("outcome = %+v, want skipped condition_false", out)
		}
		assertRecord(t, part, lifecycle.OpReactionSkipped, "r2part:finish", itemservice.SkipConditionFalse, part, 3)
		if s := stateOf(t, whole); s != "building" {
			t.Errorf("the whole is %s", s)
		}
	})
	t.Run("no linked Item", func(t *testing.T) {
		part := w.create(t, w.ana, "r2part", nil, map[string]any{"f_ok": true})
		if out := run(part); out.Outcome != itemservice.ReactionSkipped || out.Reason != itemservice.SkipNoTarget {
			t.Errorf("outcome = %+v, want skipped no_target", out)
		}
	})
	t.Run("the target is not in target_from", func(t *testing.T) {
		part, whole := w.partOfWhole(t, true, "a whole")
		w.move(t, w.ana, whole, "paused")
		if out := run(part); out.Outcome != itemservice.ReactionSkipped || out.Reason != itemservice.SkipNotInFrom {
			t.Errorf("outcome = %+v, want skipped not_in_from", out)
		}
		if s, seq := stateOf(t, whole), itemSeqOf(t, whole); s != "paused" || seq != 2 {
			t.Errorf("the whole is %s at %d, want paused at 2: untouched", s, seq)
		}
	})
	t.Run("the target's guard refuses", func(t *testing.T) {
		part, whole := w.partOfWhole(t, true, nil) // a whole without a name
		out := run(part)
		if out.Outcome != itemservice.ReactionFailed || out.Reason != itemservice.CodeGuardFailed || out.Target != whole {
			t.Errorf("outcome = %+v, want failed guard_failed on the whole", out)
		}
		assertRecord(t, part, lifecycle.OpReactionFailed, "r2part:finish", itemservice.CodeGuardFailed, part, 3)
		if s, seq := stateOf(t, whole), itemSeqOf(t, whole); s != "building" || seq != 1 {
			t.Errorf("the whole is %s at %d, want building at 1", s, seq)
		}
	})
	t.Run("an event that did not enter the state", func(t *testing.T) {
		part, _ := w.partOfWhole(t, true, "a whole")
		_, err := w.k.Internal().ReactMove(testCtx(t), entered(part, 1, "finish")) // the create entered open
		assertInvalid(t, "the create as the trigger", err)
		_, err = w.k.Internal().ReactMove(testCtx(t), entered(part, 2, "finish")) // the link event
		assertInvalid(t, "the link event as the trigger", err)
	})
}

// --- kind: create ---

// Starting work on a desk creates a todo inside it: its fields computed
// from the desk's, its time from the desk's event; no human creator; the
// desk's readers.
func TestCreate_EnteringAStateCreatesAContainedItem(t *testing.T) {
	w := newR2World(t)
	title := "VPN " + word()
	desk := w.create(t, w.ana, "r2bench", nil, map[string]any{"f_title": title}, w.ana, w.bob) // narrowed: ana and bob
	w.move(t, w.ana, desk, "working")                                                          // 2
	if got := w.pending(t, desk); len(got) != 1 || got[0].Reaction != "checklist" || got[0].CauseItem != desk || got[0].CauseSeq != 2 {
		t.Fatalf("pending = %+v, want checklist for event 2", got)
	}
	time.Sleep(60 * time.Millisecond)

	out, err := w.k.Internal().ReactCreate(testCtx(t), entered(desk, 2, "checklist"))
	if err != nil || out.Outcome != itemservice.ReactionApplied || out.Target == uuid.Nil {
		t.Fatalf("ReactCreate = %+v, %v; want applied with the new Item", out, err)
	}
	todo, err := w.k.Get(testCtx(t), w.bob, out.Target)
	if err != nil {
		t.Fatalf("bob, a reader of the desk, reads the todo: %v", err)
	}
	deskAt, _ := eventTimes(t, desk)
	if todo.TypeID != "r2todo" || todo.TypeVersion != 1 || todo.ContainerID == nil || *todo.ContainerID != desk ||
		todo.Fields["f_title"] != "Check: "+title || todo.Fields["f_opened_at"] != stamp(deskAt[1]) {
		t.Errorf("the todo = %+v; want an r2todo@1 in the desk, titled from it, opened at the desk's event time %s", todo, stamp(deskAt[1]))
	}
	// It has no human creator: the reaction made it. The event says for whom.
	if todo.CreatedBy != "reaction:r2bench:checklist" || todo.CreatedOnBehalfOf != "" {
		t.Errorf("created by %q on behalf of %q; want the reaction, and nobody", todo.CreatedBy, todo.CreatedOnBehalfOf)
	}
	actor, attr := attribution(t, out.Target, 1)
	if actor != "reaction:r2bench:checklist" || onBehalfOf(t, out.Target, 1) != w.ana.Actor ||
		attr.CausationID != fmt.Sprintf("%s:2", desk) || attr.Hop != 1 || attr.OriginatingHook != "reaction:r2bench@v1:checklist" {
		t.Errorf("the create event: by %s for %s, %+v", actor, onBehalfOf(t, out.Target, 1), attr)
	}
	todoAt, _ := eventTimes(t, out.Target)
	if d := todoAt[0].Sub(deskAt[1]); d < 50*time.Millisecond {
		t.Errorf("the todo's create is stamped %v after the desk's event; want its own time", d)
	}
	// Its readers are its container's: carl sees neither.
	if _, err := w.k.Get(testCtx(t), w.carl, out.Target); !errors.Is(err, itemservice.ErrNotFound) {
		t.Errorf("carl reads the todo: %v, want ErrNotFound", err)
	}
	assertRecord(t, desk, lifecycle.OpReactionApplied, "r2bench:checklist", "", desk, 2)
	_, payload := event(t, desk, 3)
	if payload["target"] != out.Target.String() {
		t.Errorf("the record names target %v, want the todo", payload["target"])
	}

	// Once per trigger: again gives the same Item.
	again, err := w.k.Internal().ReactCreate(testCtx(t), entered(desk, 2, "checklist"))
	if err != nil || again.Target != out.Target {
		t.Errorf("again = %+v, %v; want the same todo", again, err)
	}
	if n := count(t, `select count(*) from items where container_id = $1`, desk); n != 1 {
		t.Errorf("%d Items in the desk, want 1", n)
	}
	// By hand nobody creates one with the kernel-written field, and the
	// todo's own kernel field is the reaction's to set.
	_, err = w.k.Create(testCtx(t), itemservice.CreateRequest{
		IdempotencyKey: newKey(), Principal: w.ana, TypeID: "r2todo", TypeVersion: 1,
		Fields: map[string]any{"f_opened_at": "2026-12-04T17:00:00Z"},
	})
	assertNotWritable(t, "f_opened_at by hand", err, "f_opened_at")
}

// `from` on the trigger: coming back to working from waiting is not the
// checklist's trigger, so nothing is pending and running it is malformed.
func TestCreate_OnlyFromItsFromStates(t *testing.T) {
	w := newR2World(t)
	desk := w.create(t, w.ana, "r2bench", nil, map[string]any{"f_title": "t"})
	w.move(t, w.ana, desk, "working") // 2
	w.move(t, w.ana, desk, "waiting") // 3
	if out, err := w.k.Internal().ReactCreate(testCtx(t), entered(desk, 2, "checklist")); err != nil || out.Outcome != itemservice.ReactionApplied {
		t.Fatalf("the first entry: %+v, %v", out, err)
	}
	// 4 is the checklist's record; 5 comes back from waiting.
	w.move(t, w.ana, desk, "working")
	if got := w.pending(t, desk); len(got) != 0 {
		t.Errorf("pending = %+v, want none: checklist runs from open only", got)
	}
	_, err := w.k.Internal().ReactCreate(testCtx(t), entered(desk, 5, "checklist"))
	assertInvalid(t, "the entry from waiting as the trigger", err)
	if n := count(t, `select count(*) from items where container_id = $1`, desk); n != 1 {
		t.Errorf("%d Items in the desk, want 1", n)
	}
}

// A crash after the Item was created and before the record: one Item.
func TestCreate_ACrashBetweenTheCreateAndTheRecord(t *testing.T) {
	w := newR2World(t)
	desk := w.create(t, w.ana, "r2bench", nil, map[string]any{"f_title": "t"})
	w.move(t, w.ana, desk, "working")
	writes := 0
	restore := itemservice.SetTestHook(func(ctx context.Context, tx pgx.Tx, point string) error {
		if markOf(ctx) == "crash" && point == "write" {
			if writes++; writes == 2 {
				return errors.New("the worker died here")
			}
		}
		return nil
	})
	_, err := w.k.Internal().ReactCreate(marked(testCtx(t), "crash"), entered(desk, 2, "checklist"))
	restore()
	if err == nil {
		t.Fatal("ReactCreate succeeded; the hook should have failed its record")
	}
	if n := count(t, `select count(*) from items where container_id = $1`, desk); n != 1 {
		t.Fatalf("%d Items in the desk after the crash, want 1: created, not yet recorded", n)
	}
	if got := w.pending(t, desk); len(got) != 1 {
		t.Fatalf("pending = %+v, want the reaction still pending", got)
	}
	out, err := w.k.Internal().ReactCreate(testCtx(t), entered(desk, 2, "checklist"))
	if err != nil || out.Outcome != itemservice.ReactionApplied {
		t.Fatalf("the retry = %+v, %v", out, err)
	}
	if n := count(t, `select count(*) from items where container_id = $1`, desk); n != 1 {
		t.Errorf("%d Items in the desk after the retry, want 1", n)
	}
	if got := w.pending(t, desk); len(got) != 0 {
		t.Errorf("pending = %+v, want none", got)
	}
}

// The hop limit stops a chain at run time: a reaction one hop too deep is
// recorded as failed, hop_limit.
func TestReactions_HopLimit(t *testing.T) {
	w := newR2World(t)
	desk := w.waitingDesk(t)
	remark := w.remark(t, w.ana, desk, true)
	req := inside(desk, remark, "reopen")
	req.Hop = itemservice.MaxHop + 1
	out, err := w.k.Internal().ReactMove(testCtx(t), req)
	if err != nil || out.Outcome != itemservice.ReactionFailed || out.Reason != itemservice.CodeHopLimit {
		t.Fatalf("ReactMove one hop too deep = %+v, %v; want failed hop_limit", out, err)
	}
	if s := stateOf(t, desk); s != "waiting" {
		t.Errorf("the desk is %s, want waiting", s)
	}
}

var _ = typedef.DateTimeLayout // stamp (sets_test.go) formats with it
