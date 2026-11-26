// Tests for track 1, item 6, step 3a: history written before a migrate is
// read as what it was. Each state entry is judged by the version the Item
// was on when the entry was made (lifecycle.VersionAt, read from the
// migrated events):
//
//   - An action, an enrich hook, a reaction or an undo is owed for an entry
//     only if the entry's own version declares it, and it is finished by
//     that version's records. So one finished before a migrate stays
//     finished, and one the new version adds on a state already entered is
//     not owed for the old entry. The next entry, under the new version,
//     owes it.
//   - One still unfinished under its own version still refuses the
//     migrate, whatever the new version declares.
//   - The quiet check and the workflow's loader (StateEntry) read the same
//     way: after every migrate that goes through, StateEntry owes nothing
//     for an entry made before it.
package itemservice_test

import (
	"fmt"
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
	"github.com/francoisnyc/work-kernel/internal/lifecycle"
)

// lpSrc is a loop: open -> working -> open, and working -> done. With
// act, entering working places an order (an action); with enrich, it
// looks the plan up (an enrich hook). rest is appended.
func lpSrc(version int, act, enrich bool, rest string) string {
	src := "\nid: lp_{s}\nversion: " + fmt.Sprint(version) + `
initial_state: open
fields:
  - {id: f_vendor, type: string}
  - {id: f_amount, type: int}
  - {id: f_customer_id, type: string}
  - {id: f_plan, type: enum, options: [free, pro, enterprise, unknown]}
`
	if act {
		src += "  - {id: f_order_id, type: string, writable_by: kernel}\n"
	}
	src += `states:
  - {id: open, category: todo}
  - {id: working, category: active}
  - {id: done, category: done}
transitions:
  - {from: open, to: working}
  - {from: working, to: open}
  - {from: working, to: done}
`
	if act || enrich {
		src += "hooks:\n"
	}
	if act {
		src += "  - {id: place_order, on: enter, state: working, kind: action, action: erp.create_invoice, " +
			"reads: [f_vendor, f_amount], writes: [f_order_id]}\n"
	}
	if enrich {
		src += "  - {id: get_plan, on: enter, state: working, kind: enrich, action: crm.get_plan, " +
			"reads: [f_customer_id], writes: [f_plan], fallback: {f_plan: unknown}}\n"
	}
	return src + rest
}

func (w migWorld) loop(t *testing.T) uuid.UUID {
	t.Helper()
	res, err := w.create(t, w.eve, w.id("lp"), 1, nil, map[string]any{"f_vendor": "cloudhost", "f_amount": 10})
	return mustCreate(t, "a loop", res, err)
}

func (w migWorld) mustMove(t *testing.T, id uuid.UUID, to ...string) {
	t.Helper()
	for _, s := range to {
		if err := w.move(t, w.eve, id, s); err != nil {
			t.Fatalf("eve moves the loop to %s: %v", s, err)
		}
	}
}

func (w migWorld) entry(t *testing.T, id uuid.UUID) itemservice.StateEntry {
	t.Helper()
	se, err := w.k.StateEntry(testCtx(t), id)
	if err != nil {
		t.Fatalf("StateEntry: %v", err)
	}
	return se
}

// finish writes the action's write-back for one pending action, as the
// worker does, under the version the Item is on now.
func (w migWorld) finish(t *testing.T, id uuid.UUID, typ string, version int, p itemservice.PendingAction) {
	t.Helper()
	if _, err := w.k.Internal().Update(testCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: fmt.Sprintf("hook:%s:%d:%s", id, p.CauseSeq, p.Hook),
		Principal:      item.Principal{Actor: "hook:" + typ + ":" + p.Hook},
		ItemID:         id, Fields: map[string]any{"f_order_id": "INV-" + word()},
		Attribution: item.Attribution{
			OriginatingHook: fmt.Sprintf("hook:%s@v%d:%s", typ, version, p.Hook),
			CausationID:     fmt.Sprintf("%s:%d", id, p.CauseSeq),
			CorrelationID:   p.CorrelationID, Hop: p.Hop + 1,
		},
	}); err != nil {
		t.Fatalf("the write-back of %s for event %d: %v", p.Hook, p.CauseSeq, err)
	}
}

// migrateClean dry-runs (no problem), migrates, and checks the loader
// agrees: nothing is owed for an entry made before the migrate.
func (w migWorld) migrateClean(t *testing.T, id uuid.UUID, typ string, version int) {
	t.Helper()
	deliverAll(t, id)
	if got := w.dryRun(t, id, typ, version); len(got) != 0 {
		t.Fatalf("the dry run to version %d: %v, want no problem", version, got)
	}
	if _, err := w.migrate(t, w.adm, id, typ, version); err != nil {
		t.Fatalf("the migrate to version %d: %v", version, err)
	}
	if v := versionOf(t, id); v != version {
		t.Fatalf("the Item is on version %d, want %d", v, version)
	}
	se := w.entry(t, id)
	if len(se.PendingActions) != 0 || len(se.PendingEnrich) != 0 || len(se.PendingReactions) != 0 || len(se.PendingUndos) != 0 {
		t.Errorf("after the migrate the loader owes: actions %+v, enrich %v, reactions %+v, undos %+v; want nothing",
			se.PendingActions, se.PendingEnrich, se.PendingReactions, se.PendingUndos)
	}
}

// owesOne checks the loader owes exactly one action, place_order, for the
// current entry.
func (w migWorld) owesOne(t *testing.T, id uuid.UUID) itemservice.PendingAction {
	t.Helper()
	se := w.entry(t, id)
	if len(se.PendingActions) != 1 || se.PendingActions[0].Hook != "place_order" || se.PendingActions[0].CauseSeq != se.EnteredSeq {
		t.Fatalf("pending actions = %+v, want place_order for the current entry (event %d) alone", se.PendingActions, se.EnteredSeq)
	}
	return se.PendingActions[0]
}

// --- actions ---

// An action the new version adds on the state the Item is in, or on one
// it entered and left: not owed for those entries, so the migrate goes
// through. The next entry, under the new version, owes it.
func TestMigrate_Entries_AnActionTheNewVersionAdds(t *testing.T) {
	for _, c := range []struct {
		name string
		path []string
	}{
		{"on the state the Item is in", []string{"working"}},
		{"on a state the Item entered and left", []string{"working", "open"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newMigWorld(t)
			w.reg(t, lpSrc(1, false, false, ""))
			id := w.loop(t)
			w.mustMove(t, id, c.path...)
			w.reg(t, lpSrc(2, true, false, migAllowed))
			w.migrateClean(t, id, w.id("lp"), 2)

			if c.path[len(c.path)-1] == "working" {
				w.mustMove(t, id, "open")
			}
			w.mustMove(t, id, "working")
			w.owesOne(t, id)
		})
	}
}

// An action unfinished under the version it is owed by still refuses the
// migrate, whether the new version keeps the hook or drops it.
func TestMigrate_Entries_AnActionUnfinishedUnderItsVersion_StillRefuses(t *testing.T) {
	for name, keeps := range map[string]bool{"the new version keeps the hook": true, "the new version drops it": false} {
		t.Run(name, func(t *testing.T) {
			w := newMigWorld(t)
			w.reg(t, lpSrc(1, true, false, ""))
			id := w.loop(t)
			w.mustMove(t, id, "working", "open")
			w.reg(t, lpSrc(2, keeps, false, migAllowed))
			deliverAll(t, id)
			if got := w.dryRun(t, id, w.id("lp"), 2); !slices.Equal(got, []lifecycle.MigrationCheck{lifecycle.ActionUnfinished}) {
				t.Errorf("the dry run: %v, want action_unfinished alone", got)
			}
			_, err := w.migrate(t, w.adm, id, w.id("lp"), 2)
			assertNotMigratable(t, "the migrate", err, lifecycle.ActionUnfinished)
			if v := versionOf(t, id); v != 1 {
				t.Errorf("the loop is on version %d, want 1", v)
			}
		})
	}
}

// Migrated twice, v1 -> v2 -> v3, with the action finished under each:
// every entry is read under its own version, so neither migrate finds
// anything owed, and the entry under v3 owes the action once.
func TestMigrate_Entries_MigratedTwice(t *testing.T) {
	w := newMigWorld(t)
	typ := w.id("lp")
	w.reg(t, lpSrc(1, true, false, ""))
	id := w.loop(t)

	w.mustMove(t, id, "working") // under v1
	w.finish(t, id, typ, 1, w.owesOne(t, id))
	w.mustMove(t, id, "open")
	w.reg(t, lpSrc(2, true, false, migAllowed))
	w.migrateClean(t, id, typ, 2)

	w.mustMove(t, id, "working") // under v2
	w.finish(t, id, typ, 2, w.owesOne(t, id))
	w.mustMove(t, id, "open")
	w.reg(t, lpSrc(3, true, false, migAllowed))
	w.migrateClean(t, id, typ, 3)

	w.mustMove(t, id, "working") // under v3
	w.owesOne(t, id)
	var n int
	if err := pool.QueryRow(testCtx(t),
		`select count(*) from item_events where item_id = $1 and originating_hook like 'hook:%:place_order'`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("events written by place_order = %d, want 2 (one per entry it finished)", n)
	}
}

// --- enrich hooks ---

// The loader reports the enrich hooks owed for the current entry, by the
// entry's version: one the entry's version declares and that has not
// written; not one the new version adds on the state the Item is in. The
// next entry, under the new version, owes it.
func TestMigrate_Entries_AnEnrichHookTheNewVersionAdds(t *testing.T) {
	t.Run("owed under its own version", func(t *testing.T) {
		w := newMigWorld(t)
		w.reg(t, lpSrc(1, false, true, ""))
		id := w.loop(t)
		w.mustMove(t, id, "working")
		if se := w.entry(t, id); !slices.Equal(se.PendingEnrich, []string{"get_plan"}) {
			t.Errorf("PendingEnrich = %v, want [get_plan]", se.PendingEnrich)
		}
	})
	t.Run("added by the new version on the state the Item is in", func(t *testing.T) {
		w := newMigWorld(t)
		w.reg(t, lpSrc(1, false, false, ""))
		id := w.loop(t)
		w.mustMove(t, id, "working")
		w.reg(t, lpSrc(2, false, true, migAllowed))
		w.migrateClean(t, id, w.id("lp"), 2)
		w.mustMove(t, id, "open", "working")
		if se := w.entry(t, id); !slices.Equal(se.PendingEnrich, []string{"get_plan"}) {
			t.Errorf("the next entry, under v2: PendingEnrich = %v, want [get_plan]", se.PendingEnrich)
		}
	})
}

// --- reactions ---

func reBenchSrc(version int, reacts bool, rest string) string {
	src := "\nid: rb_{s}\nversion: " + fmt.Sprint(version) + `
initial_state: open
states:
  - {id: open, category: todo}
  - {id: working, category: active}
transitions:
  - {from: open, to: working}
  - {from: working, to: open}
`
	if reacts {
		src += "reactions:\n  - {id: make, kind: create, on: enter, state: working, type: rmade_{s}, version: 1, fields: {f_title: '\"made\"'}}\n"
	}
	return src + rest
}

const reMadeSrc = "\nid: rmade_{s}\nversion: 1\nfields:\n  - {id: f_title, type: string}\naccepts_reactions:\n  rb_{s}:make: {creates: [f_title]}\n"

// A create reaction the new version adds on the state the Item is in, or
// on one it entered and left: not owed for those entries; the migrate goes
// through and nothing is made. The next entry owes it once.
func TestMigrate_Entries_AReactionTheNewVersionAdds(t *testing.T) {
	for _, c := range []struct {
		name string
		path []string
	}{
		{"on the state the Item is in", []string{"working"}},
		{"on a state the Item entered and left", []string{"working", "open"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newMigWorld(t)
			w.reg(t, reMadeSrc)
			w.reg(t, reBenchSrc(1, false, ""))
			b := w.container(t, w.lea, w.id("rb"), nil)
			for _, s := range c.path {
				if err := w.move(t, w.lea, b, s); err != nil {
					t.Fatalf("lea moves the bench to %s: %v", s, err)
				}
			}
			w.reg(t, reBenchSrc(2, true, migAllowed))
			w.migrateClean(t, b, w.id("rb"), 2)

			if c.path[len(c.path)-1] == "working" {
				if err := w.move(t, w.lea, b, "open"); err != nil {
					t.Fatal(err)
				}
			}
			if err := w.move(t, w.lea, b, "working"); err != nil {
				t.Fatal(err)
			}
			se := w.entry(t, b)
			if len(se.PendingReactions) != 1 || se.PendingReactions[0].Reaction != "make" || se.PendingReactions[0].CauseSeq != se.EnteredSeq {
				t.Errorf("pending reactions = %+v, want make for the current entry (event %d) alone", se.PendingReactions, se.EnteredSeq)
			}
		})
	}
}

// --- undos ---

// pjPledge: filing links the pledge to its creator's active jar; paying
// takes the amount (held), and a refusal of the payment gives it back.
// The undo is give_back in version 1, refund from version 2 on.
func pjPledge(version int, rest string) string {
	undo := "give_back"
	if version > 1 {
		undo = "refund"
	}
	return "\nid: pj_{s}\nversion: " + fmt.Sprint(version) + `
initial_state: open
fields:
  - {id: f_amount, type: int}
states:
  - {id: open,    category: todo}
  - {id: filed,   category: active}
  - {id: paid,    category: done}
  - {id: dropped, category: canceled}
transitions:
  - {from: open,  to: filed}
  - {from: filed, to: paid}
  - {from: paid,  to: dropped}
links:
  - {name: jar, inverse: pledges, targets: [jj_{s}], max: one, writable_by: kernel,
     resolve: {on_enter: filed, field: f_holder, equals: creator, state: active}}
reactions:
  - {id: take, kind: held, on: enter, state: paid, from: [filed], link: jar, add: {f_level: f_amount}}
  - {id: ` + undo + `, kind: after, on: enter, state: dropped, from: [paid], undoes: take}
` + rest
}

const pjJar = `
id: jj_{s}
version: 1
initial_state: active
fields:
  - {id: f_holder, type: principal}
  - {id: f_cap,    type: int}
  - {id: f_level,  type: int, writable_by: kernel}
states:
  - {id: active, category: active}
accepts_reactions:
  pj_{s}:take:      [f_level]
  pj_{s}:give_back: [f_level]
  pj_{s}:refund:    [f_level]
`

// A payment taken and then refused under v1: v1's undo, give_back, gives
// the amount back. v2 renames the undo refund. The hold was decided under
// v1 and v1's undo is done, so nothing is owed: the migrate goes through,
// nothing is pending, and nothing is given back twice.
func TestMigrate_Entries_AnUndoDoneUnderItsVersion(t *testing.T) {
	w := newMigWorld(t)
	typ := w.id("pj")
	w.reg(t, pjJar)
	w.reg(t, pjPledge(1, ""))
	jar := w.container(t, w.eve, w.id("jj"), map[string]any{"f_holder": w.eve.Actor, "f_cap": 100})
	res, err := w.create(t, w.eve, typ, 1, nil, map[string]any{"f_amount": 30})
	id := mustCreate(t, "a pledge", res, err)
	w.mustMove(t, id, "filed")

	ask, err := w.k.Transition(testCtx(t), itemservice.TransitionRequest{IdempotencyKey: newKey(), Principal: w.eve, ItemID: id, ToState: "paid"})
	if err != nil || ask.HoldID == uuid.Nil {
		t.Fatalf("ask to pay = %+v, %v; want a hold", ask, err)
	}
	take := item.Principal{Actor: "reaction:" + typ + ":take"}
	cause := item.Attribution{OriginatingHook: "reaction:" + typ + "@v1:take",
		CausationID: fmt.Sprintf("%s:%d", id, ask.ItemSeq), Hop: hopOf(t, id, ask.ItemSeq) + 1}
	if _, err := w.k.Internal().React(testCtx(t), itemservice.ReactRequest{Principal: take, SourceID: id, HoldID: ask.HoldID, Attribution: cause}); err != nil {
		t.Fatalf("take: %v", err)
	}
	closed, err := w.k.Internal().ResolveHold(testCtx(t), itemservice.ResolveHoldRequest{
		IdempotencyKey: "hold:" + ask.HoldID.String(), Principal: take, ItemID: id, HoldID: ask.HoldID,
		Verdict: itemservice.VerdictDeny, Reason: "refused_here", Attribution: cause,
	})
	if err != nil {
		t.Fatalf("refuse the payment: %v", err)
	}
	se := w.entry(t, id)
	if len(se.PendingUndos) != 1 || se.PendingUndos[0].Reaction != "give_back" {
		t.Fatalf("pending undos under v1 = %+v, want give_back", se.PendingUndos)
	}
	if _, err := w.k.Internal().Undo(testCtx(t), itemservice.UndoRequest{
		Principal: item.Principal{Actor: "reaction:" + typ + ":give_back"}, SourceID: id, HoldID: ask.HoldID,
		Attribution: item.Attribution{OriginatingHook: "reaction:" + typ + "@v1:give_back",
			CausationID: fmt.Sprintf("%s:%d", id, closed.ItemSeq), Hop: hopOf(t, id, closed.ItemSeq) + 1},
	}); err != nil {
		t.Fatalf("give back: %v", err)
	}
	level := func() string {
		t.Helper()
		return fmt.Sprint(fieldsOf(t, w.k, w.eve, jar)["f_level"])
	}
	before := level()

	w.reg(t, pjPledge(2, migAllowed))
	deliverAll(t, jar)
	w.migrateClean(t, id, typ, 2)
	if got := level(); got != before {
		t.Errorf("the jar's level = %s, want %s: nothing given back twice", got, before)
	}
}
