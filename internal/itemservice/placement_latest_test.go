// Tests for starting work, step 2b (docs/plans/starting-work.md, 1.3 and
// review answer 2): who may place an Item (move it, narrow it, give it its
// container's readers again) is judged by placement_writable_by of the
// latest registered version of its Type, whatever version the Item is
// pinned to and whatever version a create names.
//
//   - A later version that adds a rule governs Items of earlier versions,
//     on Update and on a narrowed Create.
//   - A later version that drops the rule loosens every version at once.
//   - The Item stays pinned to its version: its lifecycle, and who may
//     make its moves (a transition's allowed), are its own version's.
//   - The order is unchanged: an Item the caller cannot see is not found.
//
// Types are global and every test of this package shares one Postgres, so
// each test registers Types of its own (startWorld, start_test.go).
package itemservice_test

import (
	"testing"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
)

// plBoxSrc is a box of a version: two states, one move, and the rest
// (a placement rule, or nothing).
func plBoxSrc(version, moveAllowed, rest string) string {
	return `
id: box_{s}
version: ` + version + `
fields:
  - {id: f_title, type: string}
initial_state: open
states:
  - {id: open, category: todo}
  - {id: done, category: done}
transitions:
  - {from: open, to: done` + moveAllowed + `}
start: {older_versions: all}
` + rest
}

const plLeadPlaces = "placement_writable_by: ['role:lead']\n"

// box creates a box of a version as p, in a container, narrowed or not.
func (w startWorld) box(t *testing.T, p item.Principal, version int, in *uuid.UUID, narrow *itemservice.Narrow) (itemservice.WriteResult, error) {
	t.Helper()
	return w.k.Create(testCtx(t), itemservice.CreateRequest{
		IdempotencyKey: newKey(), Principal: p, TypeID: w.id("box"), TypeVersion: version, ContainerID: in,
		Fields: map[string]any{"f_title": "t " + word()}, Narrow: narrow,
	})
}

func (w startWorld) place(t *testing.T, p item.Principal, id uuid.UUID, req itemservice.UpdateRequest) error {
	t.Helper()
	req.IdempotencyKey, req.Principal, req.ItemID = newKey(), p, id
	_, err := w.k.Update(testCtx(t), req)
	return err
}

// assertPlacementRefused: a *PlacementNotWritableError naming the Type, an
// ErrForbidden.
func assertPlacementRefused(t *testing.T, what string, err error, typeID string) {
	t.Helper()
	if !placementRefused(err) {
		t.Errorf("%s: error = %v; want PlacementNotWritableError", what, err)
		return
	}
	if got := err.Error(); got != (&itemservice.PlacementNotWritableError{TypeID: typeID}).Error() {
		t.Errorf("%s: message %q; want it to name %s only", what, got, typeID)
	}
}

// The three ways to place an Item, as p would ask them.
func placings(p item.Principal, hall *uuid.UUID) map[string]itemservice.UpdateRequest {
	return map[string]itemservice.UpdateRequest{
		"narrows":         {Policy: &itemservice.PolicyPatch{Readers: []string{p.Actor}}},
		"inherits again":  {Policy: &itemservice.PolicyPatch{Inherit: true}},
		"moves elsewhere": {Move: &itemservice.Move{To: hall, ConfirmWiden: true}},
	}
}

// A later version adds a rule: an Item of the earlier version is placed
// by that rule from then on, and stays pinned to its version.
func TestPlacement_TheLatestVersionGoverns_AnUpdate(t *testing.T) {
	w := newStartWorld(t)
	w.reg(t, stHallSrc)
	w.reg(t, plBoxSrc("1", "", ""))
	hall, other := w.container(t, w.lea, w.id("hall"), nil), w.container(t, w.lea, w.id("hall"), nil)
	res, err := w.box(t, w.eve, 1, &hall, nil)
	box := mustCreate(t, "a box@1", res, err)
	// Before @2: anyone who sees it places it, as box@1 says.
	if err := w.place(t, w.eve, box, itemservice.UpdateRequest{Policy: &itemservice.PolicyPatch{Readers: []string{w.eve.Actor, w.lea.Actor}}}); err != nil {
		t.Fatalf("eve narrows a box@1, before @2: %v", err)
	}
	if err := w.place(t, w.eve, box, itemservice.UpdateRequest{Policy: &itemservice.PolicyPatch{Inherit: true}}); err != nil {
		t.Fatalf("eve gives a box@1 its container's readers again, before @2: %v", err)
	}

	w.reg(t, plBoxSrc("2", "", plLeadPlaces))
	before := itemSeqOf(t, box)
	for what, req := range placings(w.eve, &other) {
		assertPlacementRefused(t, "eve (no role) "+what+" a box@1, judged by @2", w.place(t, w.eve, box, req), w.id("box"))
	}
	if n := itemSeqOf(t, box); n != before {
		t.Errorf("the refused placings wrote: item_seq %d, was %d", n, before)
	}
	// lea holds the role @2 names.
	for _, what := range []string{"narrows", "inherits again", "moves elsewhere"} {
		if err := w.place(t, w.lea, box, placings(w.lea, &other)[what]); err != nil {
			t.Errorf("lea (lead) %s a box@1, judged by @2: %v", what, err)
		}
	}
	got, err := w.k.Get(testCtx(t), w.lea, box)
	if err != nil || got.TypeVersion != 1 {
		t.Errorf("the box: version %d, %v; want still pinned to 1", got.TypeVersion, err)
	}
}

// The same on a create: naming the earlier version does not dodge the
// rule, and a refused create writes nothing.
func TestPlacement_TheLatestVersionGoverns_ANarrowedCreate(t *testing.T) {
	w := newStartWorld(t)
	w.reg(t, plBoxSrc("1", "", ""))
	narrow := func(p item.Principal) *itemservice.Narrow { return &itemservice.Narrow{Readers: []string{p.Actor}} }
	if _, err := w.box(t, w.eve, 1, nil, narrow(w.eve)); err != nil {
		t.Fatalf("eve creates a narrowed box@1, before @2: %v", err)
	}

	w.reg(t, plBoxSrc("2", "", plLeadPlaces))
	for _, version := range []int{1, 2} {
		_, err := w.box(t, w.eve, version, nil, narrow(w.eve))
		assertPlacementRefused(t, "eve creates a narrowed box of either version", err, w.id("box"))
	}
	if _, err := w.box(t, w.eve, 1, nil, nil); err != nil {
		t.Errorf("eve creates a box@1 without narrowing it: %v", err)
	}
	res, err := w.box(t, w.lea, 1, nil, narrow(w.lea))
	id := mustCreate(t, "lea (lead) creates a narrowed box@1", res, err)
	if got, err := w.k.Get(testCtx(t), w.lea, id); err != nil || got.TypeVersion != 1 {
		t.Errorf("lea's box: version %d, %v; want pinned to 1", got.TypeVersion, err)
	}
	if _, err := w.k.Get(testCtx(t), w.eve, id); err == nil {
		t.Error("eve sees the box lea narrowed to herself")
	}
}

// kernel in the latest version: no person places an Item of any version,
// whatever role they hold.
func TestPlacement_TheLatestVersionGoverns_Kernel(t *testing.T) {
	w := newStartWorld(t)
	w.reg(t, stHallSrc)
	w.reg(t, plBoxSrc("1", "", plLeadPlaces))
	hall, other := w.container(t, w.lea, w.id("hall"), nil), w.container(t, w.lea, w.id("hall"), nil)
	res, err := w.box(t, w.lea, 1, &hall, nil)
	box := mustCreate(t, "a box@1", res, err)

	w.reg(t, plBoxSrc("2", "", "placement_writable_by: [kernel]\n"))
	for _, p := range []item.Principal{w.lea, w.eve} {
		for what, req := range placings(p, &other) {
			assertPlacementRefused(t, p.Actor+" "+what+" a box@1, once @2 says kernel", w.place(t, p, box, req), w.id("box"))
		}
	}
	_, err = w.box(t, w.lea, 1, nil, &itemservice.Narrow{Readers: []string{w.lea.Actor}})
	assertPlacementRefused(t, "lea creates a narrowed box@1, once @2 says kernel", err, w.id("box"))
}

// A later version that drops the rule loosens every version at once; a
// third version that brings one back counts at once too.
func TestPlacement_ALaterVersionWithoutTheRuleLoosens(t *testing.T) {
	w := newStartWorld(t)
	w.reg(t, stHallSrc)
	w.reg(t, plBoxSrc("1", "", "placement_writable_by: [kernel]\n"))
	hall, other := w.container(t, w.lea, w.id("hall"), nil), w.container(t, w.lea, w.id("hall"), nil)
	res, err := w.box(t, w.eve, 1, &hall, nil)
	box := mustCreate(t, "a box@1", res, err)
	assertPlacementRefused(t, "eve narrows a box@1, while @1 is the latest", w.place(t, w.eve, box, placings(w.eve, &other)["narrows"]), w.id("box"))

	w.reg(t, plBoxSrc("2", "", ""))
	for _, what := range []string{"narrows", "inherits again", "moves elsewhere"} {
		if err := w.place(t, w.eve, box, placings(w.eve, &other)[what]); err != nil {
			t.Errorf("eve %s a box@1, once @2 dropped the rule: %v", what, err)
		}
	}
	if _, err := w.box(t, w.eve, 1, nil, &itemservice.Narrow{Readers: []string{w.eve.Actor}}); err != nil {
		t.Errorf("eve creates a narrowed box@1, once @2 dropped the rule: %v", err)
	}

	w.reg(t, plBoxSrc("3", "", plLeadPlaces))
	for _, version := range []int{1, 2} {
		res, err := w.box(t, w.lea, version, &hall, nil)
		id := mustCreate(t, "a box", res, err)
		assertPlacementRefused(t, "eve narrows a box of an earlier version, once @3 brought a rule back",
			w.place(t, w.eve, id, placings(w.eve, &other)["narrows"]), w.id("box"))
	}
}

// Only placement follows the latest version. The Item's lifecycle, and
// who may make its moves, stay its own version's: box@2 keeps the move
// for the role lead, and an Item of box@1 is still moved by anyone.
func TestPlacement_TheLifecycleStaysPinned(t *testing.T) {
	w := newStartWorld(t)
	w.reg(t, plBoxSrc("1", "", ""))
	res, err := w.box(t, w.eve, 1, nil, nil)
	box := mustCreate(t, "a box@1", res, err)
	w.reg(t, plBoxSrc("2", ", allowed: ['role:lead']", plLeadPlaces))

	assertPlacementRefused(t, "eve narrows a box@1, judged by @2", w.place(t, w.eve, box, placings(w.eve, nil)["narrows"]), w.id("box"))
	if _, err := w.k.Transition(testCtx(t), itemservice.TransitionRequest{IdempotencyKey: newKey(), Principal: w.eve, ItemID: box, ToState: "done"}); err != nil {
		t.Errorf("eve moves a box@1 to done, as box@1 allows: %v", err)
	}
	res, err = w.box(t, w.eve, 2, nil, nil)
	box2 := mustCreate(t, "a box@2", res, err)
	if _, err := w.k.Transition(testCtx(t), itemservice.TransitionRequest{IdempotencyKey: newKey(), Principal: w.eve, ItemID: box2, ToState: "done"}); err == nil {
		t.Error("eve moves a box@2 to done; box@2 keeps that move for the role lead")
	}
}

// The order is unchanged: an Item the caller cannot see is not found,
// before anything is said of who places it.
func TestPlacement_Order_NotFoundFirst(t *testing.T) {
	w := newStartWorld(t)
	w.reg(t, plBoxSrc("1", "", ""))
	res, err := w.box(t, w.lea, 1, nil, &itemservice.Narrow{Readers: []string{w.lea.Actor}})
	box := mustCreate(t, "a box@1 narrowed to lea", res, err)
	w.reg(t, plBoxSrc("2", "", plLeadPlaces))
	assertNotFound(t, "eve narrows a box she cannot see", w.place(t, w.eve, box, placings(w.eve, nil)["narrows"]))
}
