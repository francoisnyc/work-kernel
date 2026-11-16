// Tests for proof-of-concept Step 2, part 2, part three of three: the
// generic mechanisms on small Types of their own, where the vacation
// request cannot show them.
//
//   - A kernel-set link on a direct transition (no hold): the same lookup
//     runs on any transition entering the resolve state; none or several
//     refuse the transition as FAILED_PRECONDITION / LINK_UNRESOLVED, with
//     link_type and the code (no_<link>, ambiguous_<link>) in metadata. The
//     candidates are read again under the locks.
//   - 1e: while a held reaction's hold is open, the source fields its
//     expressions read are frozen (ErrHeld), when no state freezes them.
//   - Invariants on every door: create, update, a transition's patch, a
//     held request's patch, a hook's write, and a check's correction (which
//     then denies the hold, naming the invariant).
package itemservice_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
)

// pledge -> jar: filing a pledge links it to its creator's active jar (no
// hold); paying takes the amount (held); f_amount is frozen by no state.
const pledgeV1 = `
id: pledge
version: 1
initial_state: open
fields:
  - {id: f_amount, type: int}
  - {id: f_note,   type: string}
states:
  - {id: open,    category: todo}
  - {id: filed,   category: active}
  - {id: paid,    category: done}
  - {id: dropped, category: canceled}
transitions:
  - {from: open,  to: filed}
  - {from: filed, to: open}
  - {from: filed, to: paid}
  - {from: filed, to: dropped, escape: true}
  - {from: paid,  to: dropped}
links:
  - {name: jar, inverse: pledges, targets: [jar], max: one, writable_by: kernel,
     resolve: {on_enter: filed, field: f_holder, equals: creator, state: active}}
reactions:
  - {id: take, kind: held, on: enter, state: paid, from: [filed], link: jar, add: {f_level: f_amount}}
  - {id: give_back, kind: after, on: enter, state: dropped, from: [paid], undoes: take}
`

const jarV1 = `
id: jar
version: 1
initial_state: active
fields:
  - {id: f_holder, type: principal, writable_by: ['role:jar_admin']}
  - {id: f_cap,    type: int}
  - {id: f_level,  type: int, writable_by: kernel}
states:
  - {id: active,  category: active}
  - {id: retired, category: done}
transitions:
  - {from: active, to: retired}
invariants:
  - {id: over_cap, rule: 'f_level == null || f_level <= f_cap'}
accepts_reactions:
  pledge:take:      [f_level]
  pledge:give_back: [f_level]
`

type jarWorld struct {
	k          *itemservice.Service
	ana, admin item.Principal
}

func newJarWorld(t *testing.T) jarWorld {
	t.Helper()
	k := itemservice.New(pool)
	registerInline(t, k, jarV1)
	registerInline(t, k, pledgeV1)
	w := jarWorld{k: k, ana: person("ana"), admin: person("admin")}
	grantRole(t, w.admin, "jar_admin")
	return w
}

func (w jarWorld) jar(t *testing.T, holder item.Principal) uuid.UUID {
	t.Helper()
	res, err := w.k.Create(testCtx(t), itemservice.CreateRequest{IdempotencyKey: newKey(), Principal: w.admin,
		TypeID: "jar", TypeVersion: 1, Fields: map[string]any{"f_holder": holder.Actor, "f_cap": 100}})
	if err != nil {
		t.Fatalf("create jar: %v", err)
	}
	return res.ItemID
}

func (w jarWorld) pledge(t *testing.T, amount int) uuid.UUID {
	t.Helper()
	res, err := w.k.Create(testCtx(t), itemservice.CreateRequest{IdempotencyKey: newKey(), Principal: w.ana,
		TypeID: "pledge", TypeVersion: 1, Fields: map[string]any{"f_amount": amount}})
	if err != nil {
		t.Fatalf("create pledge: %v", err)
	}
	return res.ItemID
}

func (w jarWorld) file(t *testing.T, id uuid.UUID) (itemservice.TransitionResult, error) {
	t.Helper()
	return w.k.Transition(testCtx(t), itemservice.TransitionRequest{
		IdempotencyKey: newKey(), Principal: w.ana, ItemID: id, ToState: "filed",
	})
}

func jarLinks(t *testing.T, id uuid.UUID) []uuid.UUID {
	t.Helper()
	rows, err := pool.Query(testCtx(t), `select target_id from links where source_id = $1 and link_type = 'jar'`, id)
	if err != nil {
		t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// A direct transition into the resolve state looks the target up too:
// none or several refuse it, named; one sets the link in the same write.
func TestResolve_DirectTransition(t *testing.T) {
	w := newJarWorld(t)
	id := w.pledge(t, 10)
	_, err := w.file(t, id)
	assertRejectedCode(t, "filing without a jar", err, itemservice.CodeLinkUnresolved)
	var rej *itemservice.RejectedError
	if errors.As(err, &rej) && (rej.Metadata["link_type"] != "jar" || rej.Metadata["reason"] != "no_jar") {
		t.Errorf("metadata = %v, want link_type jar, reason no_jar", rej.Metadata)
	}
	assertEventCount(t, id, 1)

	first := w.jar(t, w.ana)
	second := w.jar(t, w.ana)
	_, err = w.file(t, id)
	assertRejectedCode(t, "filing with two jars", err, itemservice.CodeLinkUnresolved)
	if errors.As(err, &rej) && rej.Metadata["reason"] != "ambiguous_jar" {
		t.Errorf("metadata = %v, want reason ambiguous_jar", rej.Metadata)
	}
	assertEventCount(t, id, 1)

	if _, err := w.k.Transition(testCtx(t), itemservice.TransitionRequest{IdempotencyKey: newKey(), Principal: w.admin,
		ItemID: second, ToState: "retired"}); err != nil {
		t.Fatalf("retire the second jar: %v", err)
	}
	res, err := w.file(t, id)
	if err != nil || res.Outcome != itemservice.OutcomeApplied {
		t.Fatalf("filing with one active jar = %+v, %v; want applied", res, err)
	}
	if got := jarLinks(t, id); !slices.Equal(got, []uuid.UUID{first}) {
		t.Errorf("jar links = %v, want [%s]", got, first)
	}
	_, payload := event(t, id, res.ItemSeq)
	if payload["link_type"] != "jar" || payload["link_target"] != first.String() {
		t.Errorf("filing event payload = %v, want the jar link", payload)
	}
}

// On a direct transition too, the candidates are read again under the
// locks: a jar given away before the locks is not linked.
func TestResolve_DirectTransition_CandidatesChange(t *testing.T) {
	w := newJarWorld(t)
	jar := w.jar(t, w.ana)
	id := w.pledge(t, 10)
	other := person("other")

	var once sync.Once
	fired := false
	restore := itemservice.SetTestHook(func(ctx context.Context, tx pgx.Tx, point string) error {
		if point != "resolve_candidates_read" {
			return nil
		}
		var err error
		once.Do(func() {
			fired = true
			_, err = w.k.Update(context.Background(), itemservice.UpdateRequest{IdempotencyKey: newKey(), Principal: w.admin,
				ItemID: jar, Fields: map[string]any{"f_holder": other.Actor}})
		})
		return err
	})
	defer restore()

	_, err := w.file(t, id)
	if !fired {
		t.Fatal(`the test hook point "resolve_candidates_read" was never reached`)
	}
	assertRejectedCode(t, "filing after the jar was given away", err, itemservice.CodeLinkUnresolved)
	if len(jarLinks(t, id)) != 0 {
		t.Errorf("jar links = %v, want none", jarLinks(t, id))
	}
}

// 1e: while take's hold is open, f_amount (which its add reads) is frozen
// although no state freezes it; f_note is not.
func TestReactionHold_FreezesTheFieldsItReads(t *testing.T) {
	w := newJarWorld(t)
	w.jar(t, w.ana)
	id := w.pledge(t, 10)
	if _, err := w.file(t, id); err != nil {
		t.Fatalf("file: %v", err)
	}
	pay, err := w.k.Transition(testCtx(t), itemservice.TransitionRequest{IdempotencyKey: newKey(), Principal: w.ana,
		ItemID: id, ToState: "paid"})
	if err != nil || pay.Outcome != itemservice.OutcomePending {
		t.Fatalf("pay = %+v, %v; want pending (take)", pay, err)
	}
	err = updateFields(t, w.k, w.ana, id, map[string]any{"f_amount": 1})
	assertHeldErr(t, "changing the amount during take's hold", err, pay.HoldID)
	if err := updateFields(t, w.k, w.ana, id, map[string]any{"f_note": "hi"}); err != nil {
		t.Errorf("changing the note during the hold: %v", err)
	}
}

// --- invariants on every door ---

const gaugeV1 = `
id: gauge
version: 1
initial_state: low
fields:
  - {id: f_n,    type: int}
  - {id: f_note, type: string}
states:
  - {id: low,  category: todo}
  - {id: high, category: active}
  - {id: done, category: done}
transitions:
  - {from: low,  to: high}
  - {from: high, to: done}
hooks:
  - {id: check, on: transition, from: high, to: done, kind: check, action: erp.check_credit,
     reads: [f_note, f_n], writes: [f_note]}
invariants:
  - {id: too_big,  rule: 'f_n == null || f_n <= 10'}
  - {id: bad_note, rule: 'f_note == null || f_note != "bad"'}
`

func TestInvariants_EveryDoor(t *testing.T) {
	k := itemservice.New(pool)
	registerInline(t, k, gaugeV1)
	ana := person("ana")
	ctx := testCtx(t)
	create := func(fields map[string]any) (uuid.UUID, error) {
		res, err := k.Create(ctx, itemservice.CreateRequest{IdempotencyKey: newKey(), Principal: ana,
			TypeID: "gauge", TypeVersion: 1, Fields: fields})
		return res.ItemID, err
	}
	_, err := create(map[string]any{"f_n": 11})
	assertInvariantFailed(t, "create", err, "too_big")

	id, err := create(map[string]any{"f_n": 1})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	assertInvariantFailed(t, "update", updateFields(t, k, ana, id, map[string]any{"f_n": 11}), "too_big")

	move := func(to string, patch map[string]any) (itemservice.TransitionResult, error) {
		return k.Transition(ctx, itemservice.TransitionRequest{IdempotencyKey: newKey(), Principal: ana, ItemID: id,
			ToState: to, Fields: patch})
	}
	_, err = move("high", map[string]any{"f_n": 11})
	assertInvariantFailed(t, "a transition's patch", err, "too_big")
	if _, err := move("high", nil); err != nil {
		t.Fatalf("low -> high: %v", err)
	}
	_, err = move("done", map[string]any{"f_note": "bad"})
	assertInvariantFailed(t, "a held request's patch", err, "bad_note")
	if n := count(t, `select count(*) from item_holds where item_id = $1`, id); n != 0 {
		t.Errorf("holds = %d, want none: the refused request opened nothing", n)
	}

	check := item.Principal{Actor: "hook:gauge:check"}
	cause := func(seq int64) item.Attribution {
		return item.Attribution{OriginatingHook: "hook:gauge@v1:check", CausationID: fmt.Sprintf("%s:%d", id, seq), Hop: 1}
	}
	_, err = k.Internal().Update(ctx, itemservice.UpdateRequest{IdempotencyKey: newKey(), Principal: check, ItemID: id,
		Fields: map[string]any{"f_note": "bad"}, Attribution: cause(1)})
	assertInvariantFailed(t, "a hook's write", err, "bad_note")

	req, err := move("done", nil)
	if err != nil || req.Outcome != itemservice.OutcomePending {
		t.Fatalf("high -> done = %+v, %v; want pending", req, err)
	}
	if _, err := k.Internal().ResolveHold(ctx, itemservice.ResolveHoldRequest{IdempotencyKey: "hold:" + req.HoldID.String(),
		Principal: check, ItemID: id, HoldID: req.HoldID, Verdict: itemservice.VerdictCorrect,
		Fields: map[string]any{"f_note": "bad"}, Attribution: cause(req.ItemSeq)}); err != nil {
		t.Fatalf("a correction breaking an invariant: %v, want the hold denied, not an error", err)
	}
	it, err := k.Get(ctx, ana, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if it.State != "high" || it.Hold == nil || it.Hold.Status != item.HoldDenied || it.Hold.Reason != "bad_note" ||
		it.Fields["f_note"] != nil {
		t.Errorf("after the correction: %s, hold %+v, f_note %v; want high, denied bad_note, unset", it.State, it.Hold, it.Fields["f_note"])
	}
}
