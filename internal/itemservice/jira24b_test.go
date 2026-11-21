// Tests for Jira 2.4b, PR 1 (building blocks), in the item service, by
// kernel change number (docs/proposals/2026-11-21-jira-2-4b-phase-a.md,
// Part 2 wins). On test Types, as 4a did with kiosk and kticket: a rack
// contains jars, a jar contains beans (a request, its approvals, in PR 2).
// The jar's Type ID is urn: another test of this package registers a jar.
// A crate contains peas (for the narrowed child). Reactions are run by
// hand through the internal door, as the workflow would.
package itemservice_test

import (
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
)

const rack4b = `
id: rack
version: 1
fields:
  - {id: f_name,    type: string}
  - {id: f_quota,   type: int}
  - {id: f_boss,    type: principal}
  - {id: f_keepers, type: principal, multiple: true, max: 8}
  - {id: f_secret,  type: string}
readable_by_reactions: [f_name, f_quota, f_boss, f_keepers]
placement_writable_by: ['role:rack_admin']
`

// shed: a container that lets reactions read nothing.
const shed4b = `
id: shed
version: 1
fields:
  - {id: f_quota, type: int}
`

const jar4b = `
id: urn
version: 1
initial_state: draft
fields:
  - {id: f_title,   type: string}
  - {id: f_needed,  type: int,       writable_by: kernel}
  - {id: f_owner,   type: principal, writable_by: kernel}
  - {id: f_keepers, type: principal, multiple: true, max: 8, writable_by: kernel}
states:
  - {id: draft,    category: triage}
  - {id: open,     category: active}
  - {id: full,     category: done}
  - {id: spilled,  category: canceled}
  - {id: archived, category: done}
transitions:
  - from: draft
    to: open
    sets: {f_needed: 'container.f_quota', f_owner: 'container.f_boss', f_keepers: 'container.f_keepers'}
  - {from: open, to: full,    guard: 'children.bean.filter(c, c.state == "yes").size() >= f_needed'}
  - {from: open, to: spilled, guard: 'children.bean.exists(c, c.state == "no")'}
  - {from: full, to: archived, allowed: [nobody]}
reactions:
  - {id: make_beans, kind: create, on: enter, state: open, type: bean, version: 1, for_each: f_keepers,
     fields: {f_holder: 'each', f_label: 'container.f_name'}}
accepts_reactions:
  'bean:tip':      {moves: [full]}
  'bean:tip_loud': {moves: [full]}
`

const bean4b = `
id: bean
version: 1
initial_state: waiting
fields:
  - {id: f_holder, type: principal, writable_by: kernel, unique_among_siblings: [waiting, yes]}
  - {id: f_next,   type: principal}
  - {id: f_label,  type: string, writable_by: kernel}
states:
  - {id: waiting, category: active}
  - {id: yes,     category: done}
  - {id: no,      category: done}
transitions:
  - {from: waiting, to: yes, allowed: ['field:f_holder'], except: [container_creator], guard: 'container.state == "open"'}
  - {from: waiting, to: no,  allowed: ['field:f_holder'], except: [container_creator], guard: 'container.state == "open"'}
  - {from: waiting, to: waiting, allowed: ['field:f_holder'], required: [f_next], sets: {f_holder: 'f_next'}, guard: 'f_next != f_holder'}
reactions:
  - {id: tip,      kind: move, on: enter, state: yes, target: container, to: full, target_from: [open], on_guard: skip}
  - {id: tip_loud, kind: move, on: enter, state: yes, target: container, to: full, target_from: [open]}
accepts_reactions:
  'urn:make_beans': {creates: [f_holder, f_label]}
placement_writable_by: [kernel]
`

// crate and pea: no placement rule, so a pea can be narrowed and moved in;
// a pea's set reaction writes its container (as a goal writes its host).
const crate4b = `
id: crate
version: 1
initial_state: open
fields:
  - {id: f_name, type: string}
  - {id: f_last, type: datetime, writable_by: kernel}
states:
  - {id: open,   category: active}
  - {id: closed, category: done}
transitions:
  - {from: open, to: closed, guard: 'children.pea.filter(c, c.category == "done").size() >= 2'}
accepts_reactions:
  'pea:mark': {sets: [f_last]}
`

const pea4b = `
id: pea
version: 1
initial_state: open
fields:
  - {id: f_name, type: string}
states:
  - {id: open, category: active}
  - {id: done, category: done}
transitions:
  - {from: open, to: done, guard: 'container == null || container.state == "open"'}
reactions:
  - {id: mark, kind: set, on: enter, state: done, target: container, set: {f_last: 'cause.at'}}
`

type world4b struct {
	k                        *itemservice.Service
	ns                       string
	lea, sam, ana, bob, dana item.Principal
	erin                     item.Principal
	rack                     uuid.UUID
}

func newWorld4b(t *testing.T) world4b {
	t.Helper()
	k := itemservice.New(pool)
	for _, src := range []string{rack4b, shed4b, bean4b, jar4b, pea4b, crate4b} {
		registerSrc(t, k, src)
	}
	ns := newNamespace(t, k)
	w := world4b{k: k, ns: ns, lea: in(person("lea"), ns), sam: in(person("sam"), ns), ana: in(person("ana"), ns),
		bob: in(person("bob"), ns), dana: in(person("dana"), ns), erin: in(person("erin"), ns)}
	grantRoleIn(t, ns, w.lea, "rack_admin")
	w.rack = w.mk(t, w.lea, "rack", nil, map[string]any{"f_name": "rack " + word(), "f_quota": 2, "f_boss": w.bob.Actor,
		"f_keepers": []any{w.bob.Actor, w.dana.Actor, w.erin.Actor}, "f_secret": "s"})
	return w
}

func (w world4b) mk(t *testing.T, p item.Principal, typ string, in *uuid.UUID, fields map[string]any) uuid.UUID {
	t.Helper()
	res, err := w.k.Create(testCtx(t), itemservice.CreateRequest{IdempotencyKey: newKey(), Principal: p, TypeID: typ, TypeVersion: 1,
		ContainerID: in, Fields: fields})
	if err != nil {
		t.Fatalf("%s creates %s: %v", p.Actor, typ, err)
	}
	return res.ItemID
}

func (w world4b) move(t *testing.T, p item.Principal, id uuid.UUID, to string, fields map[string]any) (itemservice.TransitionResult, error) {
	t.Helper()
	return w.k.Transition(testCtx(t), itemservice.TransitionRequest{IdempotencyKey: newKey(), Principal: p, ItemID: id, ToState: to, Fields: fields})
}

func (w world4b) mustMove(t *testing.T, p item.Principal, id uuid.UUID, to string, fields map[string]any) int64 {
	t.Helper()
	res, err := w.move(t, p, id, to, fields)
	if err != nil {
		t.Fatalf("%s moves to %s: %v", p.Actor, to, err)
	}
	return res.ItemSeq
}

func (w world4b) item(t *testing.T, id uuid.UUID) item.Item {
	t.Helper()
	it, err := w.k.Get(testCtx(t), w.lea, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	return it
}

// openJar is ana's jar in the rack, submitted (draft -> open) and its beans
// made: one per keeper. It returns the jar and its beans by holder.
func (w world4b) openJar(t *testing.T) (uuid.UUID, map[string]uuid.UUID) {
	t.Helper()
	jar := w.mk(t, w.ana, "urn", &w.rack, map[string]any{"f_title": "jar " + word()})
	seq := w.mustMove(t, w.ana, jar, "open", nil)
	out, err := w.k.Internal().ReactCreate(testCtx(t), entered(jar, seq, "make_beans"))
	if err != nil || out.Outcome != itemservice.ReactionApplied {
		t.Fatalf("make_beans: %+v, %v", out, err)
	}
	return jar, w.beans(t, jar)
}

func (w world4b) beans(t *testing.T, jar uuid.UUID) map[string]uuid.UUID {
	t.Helper()
	rows, err := pool.Query(testCtx(t), `select id, fields->>'f_holder' from items where container_id = $1 and type_id = 'bean'`, jar)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]uuid.UUID{}
	for rows.Next() {
		var id uuid.UUID
		var holder string
		if err := rows.Scan(&id, &holder); err != nil {
			t.Fatal(err)
		}
		out[holder] = id
	}
	return out
}

func rejectedCode(err error) string {
	var rej *itemservice.RejectedError
	if errors.As(err, &rej) {
		return rej.Code
	}
	return ""
}

// ============================================================ 1 and 2

func TestJira24b_1_2_SetsReadTheContainer_PrincipalsCopiedByReference(t *testing.T) {
	w := newWorld4b(t)
	jar := w.mk(t, w.ana, "urn", &w.rack, map[string]any{"f_title": "x"})
	w.mustMove(t, w.ana, jar, "open", nil)
	f := w.item(t, jar).Fields
	if r3Int(t, f["f_needed"]) != 2 || f["f_owner"] != w.bob.Actor {
		t.Errorf("after submit: f_needed %v, f_owner %v; want 2 and bob, from the rack", f["f_needed"], f["f_owner"])
	}
	if got := r3Strings(t, f["f_keepers"]); !slices.Equal(got, sortedStrings(w.bob.Actor, w.dana.Actor, w.erin.Actor)) {
		t.Errorf("f_keepers = %v; want the rack's three, copied as a set", got)
	}
	// The copy stays: the rack changing later changes nothing on the jar.
	if _, err := w.k.Update(testCtx(t), itemservice.UpdateRequest{IdempotencyKey: newKey(), Principal: w.lea, ItemID: w.rack,
		Fields: map[string]any{"f_quota": 3}}); err != nil {
		t.Fatal(err)
	}
	if r3Int(t, w.item(t, jar).Fields["f_needed"]) != 2 {
		t.Error("the jar's copy followed the rack")
	}
	// A container that lets reactions read nothing, or none: the fields are unset.
	shed := w.mk(t, w.lea, "shed", nil, map[string]any{"f_quota": 5})
	for name, in := range map[string]*uuid.UUID{"in a shed": &shed, "at the top level": nil} {
		j := w.mk(t, w.ana, "urn", in, map[string]any{"f_title": "y"})
		w.mustMove(t, w.ana, j, "open", nil)
		if f := w.item(t, j).Fields; f["f_needed"] != nil || f["f_owner"] != nil || f["f_keepers"] != nil {
			t.Errorf("%s: %v; want the copies unset", name, f)
		}
	}
}

func sortedStrings(s ...string) []string {
	out := slices.Clone(s)
	slices.Sort(out)
	return out
}

// ============================================================ 3

func TestJira24b_3_ForEach_OneItemPerMember(t *testing.T) {
	w := newWorld4b(t)
	jar := w.mk(t, w.ana, "urn", &w.rack, map[string]any{"f_title": "x"})
	seq := w.mustMove(t, w.ana, jar, "open", nil)
	out, err := w.k.Internal().ReactCreate(testCtx(t), entered(jar, seq, "make_beans"))
	if err != nil || out.Outcome != itemservice.ReactionApplied || len(out.Each) != 3 {
		t.Fatalf("make_beans = %+v, %v; want applied, one outcome per member", out, err)
	}
	beans := w.beans(t, jar)
	rackName := w.item(t, w.rack).Fields["f_name"]
	for _, who := range []item.Principal{w.bob, w.dana, w.erin} {
		b, ok := beans[who.Actor]
		if !ok {
			t.Errorf("no bean for %s", who.Actor)
			continue
		}
		it := w.item(t, b)
		if it.Fields["f_label"] != rackName || it.CreatedBy != "reaction:urn:make_beans" || it.State != "waiting" {
			t.Errorf("%s's bean = %+v", who.Actor, it)
		}
	}
	if len(beans) != 3 {
		t.Errorf("%d beans, want 3", len(beans))
	}
	// One record per member, each naming its bean.
	var targets []uuid.UUID
	for _, e := range allEvents(t, w.k, w.lea, jar) {
		if e.Op == "reaction_applied" && e.Reaction == "urn:make_beans" && e.ReactionTarget != nil {
			targets = append(targets, e.ReactionTarget.ID)
		}
	}
	if !sameIDs(targets, []uuid.UUID{beans[w.bob.Actor], beans[w.dana.Actor], beans[w.erin.Actor]}) {
		t.Errorf("the records name %v", targets)
	}
	// A replay makes nothing more.
	if _, err := w.k.Internal().ReactCreate(testCtx(t), entered(jar, seq, "make_beans")); err != nil {
		t.Fatal(err)
	}
	if n := len(w.beans(t, jar)); n != 3 {
		t.Errorf("after a replay: %d beans", n)
	}
}

// ============================================================ 4

// The jar's guards count its beans; a bean's guard reads its jar's state.
func TestJira24b_4_GuardsReadChildrenAndContainer(t *testing.T) {
	w := newWorld4b(t)
	jar, beans := w.openJar(t)
	if _, err := w.move(t, w.lea, jar, "full", nil); rejectedCode(err) != "guard_failed" {
		t.Errorf("full with no yes: %v; want guard_failed", err)
	}
	w.mustMove(t, w.bob, beans[w.bob.Actor], "yes", nil)
	if _, err := w.move(t, w.lea, jar, "full", nil); rejectedCode(err) != "guard_failed" {
		t.Errorf("full with one yes of two needed: %v; want guard_failed", err)
	}
	w.mustMove(t, w.dana, beans[w.dana.Actor], "yes", nil)
	w.mustMove(t, w.lea, jar, "full", nil)
	// The jar is no longer open: the last bean's decision is refused.
	if _, err := w.move(t, w.erin, beans[w.erin.Actor], "no", nil); rejectedCode(err) != "guard_failed" {
		t.Errorf("a decision once the jar is full: %v; want guard_failed", err)
	}
}

// A guard judges the data, not the reader: a narrowed child counts for
// someone who cannot see it.
func TestJira24b_4_ANarrowedChildStillCounts(t *testing.T) {
	w := newWorld4b(t)
	crate := w.mk(t, w.lea, "crate", nil, map[string]any{"f_name": "c"})
	p1 := w.mk(t, w.lea, "pea", &crate, map[string]any{"f_name": "1"})
	p2 := w.mk(t, w.lea, "pea", &crate, map[string]any{"f_name": "2"})
	w.mustMove(t, w.lea, p1, "done", nil)
	w.mustMove(t, w.lea, p2, "done", nil)
	if _, err := w.k.Update(testCtx(t), itemservice.UpdateRequest{IdempotencyKey: newKey(), Principal: w.lea, ItemID: p2,
		Policy: &itemservice.PolicyPatch{Readers: []string{w.lea.Actor}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.k.Get(testCtx(t), w.sam, p2); !errors.Is(err, itemservice.ErrNotFound) {
		t.Fatalf("precondition: sam sees the narrowed pea (%v)", err)
	}
	if _, err := w.move(t, w.sam, crate, "closed", nil); err != nil {
		t.Errorf("sam closes the crate, two peas done, one narrowed away from him: %v", err)
	}
}

// ============================================================ 5 and 6

// The first yes: tip (on_guard: skip) is skipped guard_false, tip_loud
// fails guard_failed. The second: tip moves the jar; tip_loud finds it
// already full.
func TestJira24b_5_6_AMoveAtTheContainer_AndOnGuardSkip(t *testing.T) {
	w := newWorld4b(t)
	jar, beans := w.openJar(t)
	run := func(bean uuid.UUID, seq int64, r string) itemservice.ReactionOutcome {
		t.Helper()
		out, err := w.k.Internal().ReactMove(testCtx(t), entered(bean, seq, r))
		if err != nil {
			t.Fatalf("%s: %v", r, err)
		}
		return out
	}
	s1 := w.mustMove(t, w.bob, beans[w.bob.Actor], "yes", nil)
	if o := run(beans[w.bob.Actor], s1, "tip"); o.Outcome != itemservice.ReactionSkipped || o.Reason != itemservice.SkipGuardFalse {
		t.Errorf("tip after one yes = %+v; want skipped guard_false", o)
	}
	if o := run(beans[w.bob.Actor], s1, "tip_loud"); o.Outcome != itemservice.ReactionFailed || o.Reason != "guard_failed" {
		t.Errorf("tip_loud after one yes = %+v; want failed guard_failed (the default)", o)
	}
	s2 := w.mustMove(t, w.dana, beans[w.dana.Actor], "yes", nil)
	if o := run(beans[w.dana.Actor], s2, "tip"); o.Outcome != itemservice.ReactionApplied || o.Target != jar {
		t.Errorf("tip after two yes = %+v; want the jar moved", o)
	}
	if o := run(beans[w.dana.Actor], s2, "tip_loud"); o.Outcome != itemservice.ReactionSkipped || o.Reason != itemservice.SkipNotInFrom {
		t.Errorf("tip_loud then = %+v; want skipped not_in_from", o)
	}
	it := w.item(t, jar)
	if it.State != "full" {
		t.Fatalf("the jar is %s", it.State)
	}
	var moves []itemservice.Event
	for _, e := range allEvents(t, w.k, w.lea, jar) {
		if e.Op == "transition" && e.To == "full" {
			moves = append(moves, e)
		}
	}
	if len(moves) != 1 || moves[0].Actor != "reaction:bean:tip" {
		t.Errorf("the jar's moves to full: %+v; want one, by reaction:bean:tip", moves)
	}
}

// ============================================================ 7

// ana created the jar: a bean she holds is not hers to decide.
func TestJira24b_7_ContainerCreatorInExcept(t *testing.T) {
	w := newWorld4b(t)
	if _, err := w.k.Update(testCtx(t), itemservice.UpdateRequest{IdempotencyKey: newKey(), Principal: w.lea, ItemID: w.rack,
		Fields: map[string]any{"f_keepers": []any{w.ana.Actor, w.bob.Actor}}}); err != nil {
		t.Fatal(err)
	}
	_, beans := w.openJar(t)
	if _, err := w.move(t, w.ana, beans[w.ana.Actor], "yes", nil); !errors.Is(err, itemservice.ErrForbidden) {
		t.Errorf("ana decides a bean of her own jar: %v; want ErrForbidden", err)
	}
	if _, err := w.move(t, w.bob, beans[w.bob.Actor], "yes", nil); err != nil {
		t.Errorf("bob decides his: %v", err)
	}
}

// ============================================================ 9

func TestJira24b_9_UniqueAmongSiblings(t *testing.T) {
	w := newWorld4b(t)
	_, beans := w.openJar(t)
	hand := func(from item.Principal, to string) error {
		_, err := w.move(t, from, beans[from.Actor], "waiting", map[string]any{"f_next": to})
		return err
	}
	// dana already holds a waiting bean in this jar.
	if err := hand(w.bob, w.dana.Actor); rejectedCode(err) != itemservice.CodeNotUnique {
		t.Errorf("bob hands over to dana: %v; want not_unique", err)
	}
	// A bean that is no longer waiting or yes does not count.
	w.mustMove(t, w.erin, beans[w.erin.Actor], "no", nil)
	if err := hand(w.bob, w.erin.Actor); err != nil {
		t.Errorf("bob hands over to erin, whose bean is no: %v", err)
	}
}

// Two hand-overs to the same person at the same moment: one lands.
func TestJira24b_9_TwoHandOversToOnePerson_OneLands(t *testing.T) {
	w := newWorld4b(t)
	for range 5 {
		_, beans := w.openJar(t)
		var wg sync.WaitGroup
		errs := make(chan error, 2)
		for _, from := range []item.Principal{w.bob, w.dana} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := w.k.Transition(testCtx(t), itemservice.TransitionRequest{IdempotencyKey: newKey(), Principal: from,
					ItemID: beans[from.Actor], ToState: "waiting", Fields: map[string]any{"f_next": w.sam.Actor}})
				errs <- err
			}()
		}
		wg.Wait()
		close(errs)
		ok, unique := 0, 0
		for err := range errs {
			switch {
			case err == nil:
				ok++
			case rejectedCode(err) == itemservice.CodeNotUnique:
				unique++
			default:
				t.Fatalf("a hand-over: %v", err)
			}
		}
		if ok != 1 || unique != 1 {
			t.Errorf("two hand-overs to sam: %d landed, %d not_unique; want 1 and 1", ok, unique)
		}
	}
}

// ============================================================ 10

func TestJira24b_10_PlacementWritableBy(t *testing.T) {
	w := newWorld4b(t)
	_, beans := w.openJar(t)
	bean := beans[w.bob.Actor]
	other := w.mk(t, w.lea, "rack", nil, map[string]any{"f_name": "other"})
	placement := func(err error) bool {
		var pe *itemservice.PlacementNotWritableError
		return errors.As(err, &pe) && errors.Is(err, itemservice.ErrForbidden)
	}
	update := func(p item.Principal, id uuid.UUID, req itemservice.UpdateRequest) error {
		req.IdempotencyKey, req.Principal, req.ItemID = newKey(), p, id
		_, err := w.k.Update(testCtx(t), req)
		return err
	}
	// A bean is placed by the kernel only: nobody moves or narrows it.
	for _, p := range []item.Principal{w.sam, w.lea, w.bob} {
		if err := update(p, bean, itemservice.UpdateRequest{Policy: &itemservice.PolicyPatch{Readers: []string{p.Actor}}}); !placement(err) {
			t.Errorf("%s narrows a bean: %v; want PlacementNotWritableError", p.Actor, err)
		}
		if err := update(p, bean, itemservice.UpdateRequest{Move: &itemservice.Move{To: &other}}); !placement(err) {
			t.Errorf("%s moves a bean: %v; want PlacementNotWritableError", p.Actor, err)
		}
	}
	// A rack is placed by rack_admin: sam no, lea yes.
	if err := update(w.sam, w.rack, itemservice.UpdateRequest{Policy: &itemservice.PolicyPatch{Readers: []string{w.sam.Actor}}}); !placement(err) {
		t.Errorf("sam narrows the rack: %v", err)
	}
	if _, err := w.k.Create(testCtx(t), itemservice.CreateRequest{IdempotencyKey: newKey(), Principal: w.sam, TypeID: "rack", TypeVersion: 1,
		Fields: map[string]any{"f_name": "mine"}, Narrow: &itemservice.Narrow{Readers: []string{w.sam.Actor}}}); !placement(err) {
		t.Errorf("sam creates a narrowed rack: %v", err)
	}
	// An agent for lea without the role of its own: both must hold it.
	agent := withSession(t, w.k, item.Principal{Actor: "agent:" + uuid.NewString()[:8], OnBehalfOf: w.lea.Actor, Namespace: w.ns})
	if err := update(agent, w.rack, itemservice.UpdateRequest{Policy: &itemservice.PolicyPatch{Inherit: true}}); !placement(err) {
		t.Errorf("an agent without the role, for lea: %v", err)
	}
	if err := update(w.lea, w.rack, itemservice.UpdateRequest{Policy: &itemservice.PolicyPatch{Readers: []string{w.lea.Actor, w.ana.Actor}}}); err != nil {
		t.Errorf("lea (rack_admin) narrows the rack: %v", err)
	}
	// Narrowed to lea and ana, the rack is hidden from the agent:
	// visibility comes first, so it learns nothing of who places it.
	if err := update(agent, w.rack, itemservice.UpdateRequest{Policy: &itemservice.PolicyPatch{Inherit: true}}); !errors.Is(err, itemservice.ErrNotFound) {
		t.Errorf("the agent, once the rack is narrowed away from it: %v; want ErrNotFound", err)
	}
	// Types that say nothing are placed by anyone who sees the Item, as before.
	crate := w.mk(t, w.sam, "crate", nil, map[string]any{"f_name": "c"})
	if err := update(w.sam, crate, itemservice.UpdateRequest{Policy: &itemservice.PolicyPatch{Readers: []string{w.sam.Actor}}}); err != nil {
		t.Errorf("sam narrows his crate: %v", err)
	}
}

// ============================================================ 11

func TestJira24b_11_HandOver(t *testing.T) {
	w := newWorld4b(t)
	_, beans := w.openJar(t)
	bean := beans[w.bob.Actor]
	if _, err := w.move(t, w.bob, bean, "waiting", map[string]any{"f_next": w.bob.Actor}); rejectedCode(err) != "guard_failed" {
		t.Errorf("bob hands over to himself: %v; want guard_failed", err)
	}
	if _, err := w.move(t, w.dana, bean, "waiting", map[string]any{"f_next": w.dana.Actor}); !errors.Is(err, itemservice.ErrForbidden) {
		t.Errorf("dana hands over bob's bean: %v; want ErrForbidden", err)
	}
	w.mustMove(t, w.bob, bean, "waiting", map[string]any{"f_next": w.sam.Actor})
	if got := w.item(t, bean).Fields["f_holder"]; got != w.sam.Actor {
		t.Fatalf("after the hand-over the holder is %v", got)
	}
	if _, err := w.move(t, w.bob, bean, "yes", nil); !errors.Is(err, itemservice.ErrForbidden) {
		t.Errorf("bob decides after handing over: %v; want ErrForbidden", err)
	}
	w.mustMove(t, w.sam, bean, "yes", nil)
	// The history says who handed over to whom.
	var hand *itemservice.Event
	for _, e := range allEvents(t, w.k, w.lea, bean) {
		if e.Op == "transition" && e.From == "waiting" && e.To == "waiting" {
			hand = &e
		}
	}
	if hand == nil || hand.Actor != w.bob.Actor || hand.Values["f_holder"] != w.sam.Actor {
		t.Errorf("the hand-over event = %+v", hand)
	}
}

// ============================================================ 12

func TestJira24b_12_Nobody(t *testing.T) {
	w := newWorld4b(t)
	jar, beans := w.openJar(t)
	w.mustMove(t, w.bob, beans[w.bob.Actor], "yes", nil)
	w.mustMove(t, w.dana, beans[w.dana.Actor], "yes", nil)
	w.mustMove(t, w.lea, jar, "full", nil)
	for _, p := range []item.Principal{w.lea, w.ana, w.sam} {
		if _, err := w.move(t, p, jar, "archived", nil); !errors.Is(err, itemservice.ErrForbidden) {
			t.Errorf("%s archives the jar (allowed: [nobody]): %v; want ErrForbidden", p.Actor, err)
		}
	}
	agent := withSession(t, w.k, item.Principal{Actor: "agent:" + uuid.NewString()[:8], OnBehalfOf: w.lea.Actor, Namespace: w.ns})
	if _, err := w.move(t, agent, jar, "archived", nil); !errors.Is(err, itemservice.ErrForbidden) {
		t.Errorf("an agent archives the jar: %v; want ErrForbidden", err)
	}
	if st := w.item(t, jar).State; st != "full" {
		t.Errorf("the jar is %s", st)
	}
}

var _ = fmt.Sprintf
