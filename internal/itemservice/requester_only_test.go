// Tests for "only the requester" for drafts (docs/roadmap.md, track 1,
// item 4; the owner's decisions of Phase A). While an Item is in a state
// that lists a field under requester_only, only its requester writes that
// field on the public door: the effective creator (IsRequester), or an
// agent acting for them, where writable_by also opens the field to agents.
// It is part of "not writable" (FIELD_NOT_WRITABLE), before frozen and
// held. A transition's patch is judged against the state it leaves; a
// move's clears is a write; a move's sets and the kernel's own writes are
// untouched.
//
// First the bake-offs' attack (docs/research/temporal-bakeoff.md, the
// staff-role run, spikes/temporal-bakeoff/results/kernel-staff-role.md): a
// colleague, olaf, holding role staff like every person, changed ana's
// draft before she submitted it, and the manager then approved what olaf
// wrote. ro_purchase is that run's purchase@1, cut to what the tests touch,
// with its draft's fields limited to the requester.
package itemservice_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

const roPurchaseSrc = `
id: ro_purchase
version: 1
initial_state: draft
fields:
  - {id: f_amount,        type: int,    writable_by: [people, agents]}
  - {id: f_vendor,        type: string, writable_by: ['role:ro_staff']}
  - {id: f_justification, type: string, writable_by: ['role:ro_staff']}
  - {id: f_note,          type: string}
  - {id: f_reason,        type: string, writable_by: ['role:ro_staff']}
states:
  - {id: draft,    category: todo, requester_only: [f_amount, f_vendor, f_justification]}
  - {id: review,   category: waiting, frozen: [f_amount, f_vendor, f_justification]}
  - {id: returned, category: todo}
  - {id: approved, category: done,     frozen: [f_amount, f_vendor, f_justification, f_reason]}
  - {id: rejected, category: canceled, frozen: [f_amount, f_vendor, f_justification, f_reason]}
  - {id: parked,   category: backlog}
  - {id: voided,   category: canceled}
transitions:
  - {from: draft,    to: review,   allowed: [creator], required: [f_amount, f_vendor, f_justification]}
  - {from: review,   to: approved, allowed: ['role:ro_manager'], except: [creator]}
  - {from: review,   to: rejected, allowed: ['role:ro_manager'], except: [creator], required: [f_reason]}
  - {from: review,   to: draft,    allowed: [creator]}
  - {from: review,   to: returned, allowed: ['role:ro_manager'], except: [creator]}
  - {from: returned, to: draft,    allowed: ['role:ro_manager']}
  - {from: draft,    to: parked,   allowed: ['role:ro_staff'], sets: {f_amount: '0'}}
  - {from: draft,    to: voided,   allowed: ['role:ro_staff'], clears: [f_vendor]}
`

// roWorld: ana asks; olaf is a colleague; bob is a manager. Every person
// holds ro_staff, as every person held staff in the bake-off. bot is an
// agent with every power, with sessions for itself, for ana and for olaf.
type roWorld struct {
	k                    *itemservice.Service
	ana, olaf, bob       item.Principal
	bot, forAna, forOlaf item.Principal
}

func newROWorld(t *testing.T) roWorld {
	t.Helper()
	k := itemservice.New(pool)
	ty, err := typedef.Parse([]byte(roPurchaseSrc))
	if err != nil {
		t.Fatalf("parse ro_purchase: %v", err)
	}
	if err := k.RegisterType(testCtx(t), ty); err != nil {
		t.Fatalf("register ro_purchase: %v", err)
	}
	w := roWorld{k: k, ana: person("ana"), olaf: person("olaf"), bob: person("bob")}
	for _, p := range []item.Principal{w.ana, w.olaf, w.bob} {
		grantRole(t, p, "ro_staff")
	}
	grantRole(t, w.bob, "ro_manager")
	agent := "agent:ro-bot-" + uuid.NewString()[:8]
	w.bot = withSession(t, k, item.Principal{Actor: agent})
	w.forAna = withSession(t, k, item.Principal{Actor: agent, OnBehalfOf: w.ana.Actor})
	w.forOlaf = withSession(t, k, item.Principal{Actor: agent, OnBehalfOf: w.olaf.Actor})
	return w
}

// draft: ana's purchase of 2500 from officeco, not yet submitted.
func (w roWorld) draft(t *testing.T) uuid.UUID {
	t.Helper()
	res, err := w.k.Create(testCtx(t), itemservice.CreateRequest{
		IdempotencyKey: newKey(), Principal: w.ana, TypeID: "ro_purchase", TypeVersion: 1,
		Fields: map[string]any{"f_amount": 2500, "f_vendor": "officeco", "f_justification": "Monitors"},
	})
	if err != nil {
		t.Fatalf("ana creates a purchase: %v", err)
	}
	return res.ItemID
}

func (w roWorld) update(t *testing.T, p item.Principal, id uuid.UUID, fields map[string]any) error {
	t.Helper()
	_, err := w.k.Update(testCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: p, ItemID: id, Fields: fields,
	})
	return err
}

func (w roWorld) move(t *testing.T, p item.Principal, id uuid.UUID, to string, patch map[string]any) error {
	t.Helper()
	_, err := w.k.Transition(testCtx(t), itemservice.TransitionRequest{
		IdempotencyKey: newKey(), Principal: p, ItemID: id, ToState: to, Fields: patch,
	})
	return err
}

func (w roWorld) mustMove(t *testing.T, p item.Principal, id uuid.UUID, to string, patch map[string]any) {
	t.Helper()
	if err := w.move(t, p, id, to, patch); err != nil {
		t.Fatalf("%s moves it to %s: %v", p.Actor, to, err)
	}
}

func (w roWorld) fields(t *testing.T, id uuid.UUID) map[string]any {
	t.Helper()
	it, err := w.k.Get(testCtx(t), w.ana, id)
	if err != nil {
		t.Fatalf("ana reads the purchase: %v", err)
	}
	return it.Fields
}

// assertRequesterOnly: the write is refused as FIELD_NOT_WRITABLE naming
// exactly fields, and the item service says why: only the requester writes
// them while the Item is in state. It does not blame agents.
func assertRequesterOnly(t *testing.T, what string, err error, state string, fields ...string) {
	t.Helper()
	var fe *itemservice.FieldNotWritableError
	if !errors.Is(err, itemservice.ErrForbidden) || !errors.As(err, &fe) {
		t.Errorf("%s: error = %v, want FIELD_NOT_WRITABLE", what, err)
		return
	}
	if strings.Join(fe.Fields, ",") != strings.Join(fields, ",") {
		t.Errorf("%s: refused fields = %v, want %v", what, fe.Fields, fields)
	}
	want := "only the requester writes these fields while the Item is in " + state
	if !strings.Contains(err.Error(), want) {
		t.Errorf("%s: error = %q, want it to say %q", what, err, want)
	}
	if strings.Contains(err.Error(), "opens to agents") {
		t.Errorf("%s: error = %q blames agents; the field is limited to the requester", what, err)
	}
}

// The bake-offs' attack: olaf, a colleague holding staff like ana, changes
// the amount and vendor of ana's draft before she submits it. Refused, and
// nothing changes; ana then submits her own values, and what the manager
// approves is what ana wrote.
func TestRequesterOnly_Bakeoff_AColleagueMayNotEditADraft(t *testing.T) {
	w := newROWorld(t)
	id := w.draft(t)
	err := w.update(t, w.olaf, id, map[string]any{"f_amount": 950, "f_vendor": "shadyparts"})
	assertRequesterOnly(t, "olaf changes ana's draft", err, "draft", "f_amount", "f_vendor")

	if err := w.update(t, w.ana, id, map[string]any{"f_justification": "Monitors for the design team"}); err != nil {
		t.Fatalf("ana edits her own draft: %v", err)
	}
	w.mustMove(t, w.ana, id, "review", nil)
	w.mustMove(t, w.bob, id, "approved", nil)
	f := w.fields(t, id)
	if f["f_amount"] != float64(2500) || f["f_vendor"] != "officeco" || f["f_justification"] != "Monitors for the design team" {
		t.Errorf("approved fields = %v, want ana's: 2500, officeco, Monitors for the design team", f)
	}
}

// Only the listed fields: olaf still writes a field the draft does not
// limit, and the rule comes on top of writable_by (a person without
// ro_staff is refused f_vendor whoever created the draft).
func TestRequesterOnly_OnlyTheListedFields(t *testing.T) {
	w := newROWorld(t)
	id := w.draft(t)
	if err := w.update(t, w.olaf, id, map[string]any{"f_note": "see the quote"}); err != nil {
		t.Errorf("olaf writes f_note, which the draft does not limit: %v", err)
	}
	if err := w.update(t, w.ana, id, map[string]any{"f_amount": 2400, "f_vendor": "officeco ltd"}); err != nil {
		t.Errorf("ana edits her own draft: %v", err)
	}
	// One refusal, for the field olaf may not write; f_note was his to write.
	err := w.update(t, w.olaf, id, map[string]any{"f_amount": 1, "f_note": "fine"})
	assertRequesterOnly(t, "olaf writes f_amount and f_note", err, "draft", "f_amount")
}

// Agents: the agent acting alone is not the requester, though f_amount is
// open to agents; acting for ana it writes f_amount, but not f_vendor,
// which writable_by closes to it (an agent needs both); acting for olaf it
// is refused as olaf is.
func TestRequesterOnly_Agents(t *testing.T) {
	w := newROWorld(t)
	id := w.draft(t)
	err := w.update(t, w.bot, id, map[string]any{"f_amount": 950})
	assertRequesterOnly(t, "the agent, alone, changes ana's amount", err, "draft", "f_amount")
	err = w.update(t, w.forOlaf, id, map[string]any{"f_amount": 950})
	assertRequesterOnly(t, "the agent, for olaf, changes ana's amount", err, "draft", "f_amount")

	if err := w.update(t, w.forAna, id, map[string]any{"f_amount": 2600}); err != nil {
		t.Errorf("the agent, for ana, changes her amount (open to agents): %v", err)
	}
	err = w.update(t, w.forAna, id, map[string]any{"f_vendor": "shadyparts"})
	var fe *itemservice.FieldNotWritableError
	if !errors.As(err, &fe) || strings.Join(fe.Fields, ",") != "f_vendor" || !strings.Contains(err.Error(), "opens to agents") {
		t.Errorf("the agent, for ana, changes her vendor: error = %v, want FIELD_NOT_WRITABLE f_vendor, closed to agents", err)
	}
	// Both causes in one patch: one refusal names every field, and says
	// each cause.
	err = w.update(t, w.bot, id, map[string]any{"f_amount": 950, "f_note": "by the bot"})
	if !errors.As(err, &fe) || strings.Join(fe.Fields, ",") != "f_amount,f_note" ||
		!strings.Contains(err.Error(), "only the requester writes these fields while the Item is in draft") ||
		!strings.Contains(err.Error(), "opens to agents") {
		t.Errorf("the agent, alone, writes f_amount and f_note: error = %v, want FIELD_NOT_WRITABLE f_amount,f_note, saying both causes", err)
	}
	if f := w.fields(t, id); f["f_amount"] != float64(2600) || f["f_vendor"] != "officeco" {
		t.Errorf("fields = %v, want f_amount 2600 (ana's agent) and f_vendor officeco", f)
	}
}

// The requester is the effective creator: on a draft an agent filed for
// ana, ana writes, and the agent acting alone does not, though it created
// the Item; on a draft the agent filed for itself, it writes and ana does
// not.
func TestRequesterOnly_TheEffectiveCreator(t *testing.T) {
	w := newROWorld(t)
	forAna, err := w.k.Create(testCtx(t), itemservice.CreateRequest{
		IdempotencyKey: newKey(), Principal: w.forAna, TypeID: "ro_purchase", TypeVersion: 1,
		Fields: map[string]any{"f_amount": 100},
	})
	if err != nil {
		t.Fatalf("the agent files a draft for ana: %v", err)
	}
	if err := w.update(t, w.ana, forAna.ItemID, map[string]any{"f_amount": 120, "f_vendor": "officeco"}); err != nil {
		t.Errorf("ana edits the draft filed for her: %v", err)
	}
	err = w.update(t, w.bot, forAna.ItemID, map[string]any{"f_amount": 950})
	assertRequesterOnly(t, "the agent, now alone, changes the draft it filed for ana", err, "draft", "f_amount")

	own, err := w.k.Create(testCtx(t), itemservice.CreateRequest{
		IdempotencyKey: newKey(), Principal: w.bot, TypeID: "ro_purchase", TypeVersion: 1,
		Fields: map[string]any{"f_amount": 5},
	})
	if err != nil {
		t.Fatalf("the agent files a draft for itself: %v", err)
	}
	if err := w.update(t, w.bot, own.ItemID, map[string]any{"f_amount": 6}); err != nil {
		t.Errorf("the agent edits its own draft: %v", err)
	}
	err = w.update(t, w.ana, own.ItemID, map[string]any{"f_amount": 7})
	assertRequesterOnly(t, "ana changes the agent's draft", err, "draft", "f_amount")
}

// A transition's patch is judged against the state it leaves: ana submits
// with a patch; a patch on a move out of the draft by someone else is
// refused; a patch on the move into the draft (from returned, which limits
// nothing) is the mover's, as with frozen.
func TestRequesterOnly_TransitionPatch(t *testing.T) {
	w := newROWorld(t)
	id := w.draft(t)
	err := w.move(t, w.olaf, id, "parked", map[string]any{"f_justification": "parked by olaf"})
	assertRequesterOnly(t, "olaf parks ana's draft with a patch", err, "draft", "f_justification")
	if err := w.move(t, w.ana, id, "review", map[string]any{"f_amount": 2450}); err != nil {
		t.Fatalf("ana submits with a patch: %v", err)
	}
	w.mustMove(t, w.bob, id, "returned", nil)
	if err := w.move(t, w.bob, id, "draft", map[string]any{"f_amount": 2000}); err != nil {
		t.Errorf("bob returns it to the draft with a patch (judged against returned): %v", err)
	}
	if f := w.fields(t, id); f["f_amount"] != float64(2000) {
		t.Errorf("f_amount = %v, want 2000 (bob's patch on the way in)", f["f_amount"])
	}
	// Back in the draft, the rule applies again.
	err = w.update(t, w.bob, id, map[string]any{"f_amount": 1})
	assertRequesterOnly(t, "bob changes the draft once it is back", err, "draft", "f_amount")
}

// Sent back by ana herself (review to draft), the draft is hers again: olaf
// is refused, ana writes.
func TestRequesterOnly_AppliesAgainOnReturn(t *testing.T) {
	w := newROWorld(t)
	id := w.draft(t)
	w.mustMove(t, w.ana, id, "review", nil)
	w.mustMove(t, w.ana, id, "draft", nil)
	err := w.update(t, w.olaf, id, map[string]any{"f_vendor": "shadyparts"})
	assertRequesterOnly(t, "olaf changes ana's draft after she took it back", err, "draft", "f_vendor")
	if err := w.update(t, w.ana, id, map[string]any{"f_vendor": "officeco ltd"}); err != nil {
		t.Errorf("ana edits her draft after taking it back: %v", err)
	}
}

// A move's clears is a write of the field it clears: olaf may void ana's
// draft (allowed: ro_staff) only if it clears nothing of hers; it clears
// f_vendor, so it is refused. A move's sets is the Type's: olaf parks it,
// and the move sets f_amount.
func TestRequesterOnly_ClearsAndSets(t *testing.T) {
	w := newROWorld(t)
	id := w.draft(t)
	err := w.move(t, w.olaf, id, "voided", nil)
	assertRequesterOnly(t, "olaf voids ana's draft, clearing f_vendor", err, "draft", "f_vendor")
	if err := w.move(t, w.olaf, id, "parked", nil); err != nil {
		t.Errorf("olaf parks ana's draft (the move sets f_amount): %v", err)
	}
	if f := w.fields(t, id); f["f_amount"] != float64(0) {
		t.Errorf("f_amount = %v, want 0 (set by the move)", f["f_amount"])
	}
}

// After submit the rule no longer applies, but frozen does, to everyone:
// ana is refused too (FIELD_FROZEN), and so is olaf. f_reason is not
// frozen in review: bob writes it while he decides.
func TestRequesterOnly_AfterSubmit_FrozenApplies(t *testing.T) {
	w := newROWorld(t)
	id := w.draft(t)
	w.mustMove(t, w.ana, id, "review", nil)
	for _, p := range []item.Principal{w.ana, w.olaf} {
		err := w.update(t, p, id, map[string]any{"f_amount": 1})
		var re *itemservice.RejectedError
		if !errors.As(err, &re) || re.Code != itemservice.CodeFieldFrozen {
			t.Errorf("%s changes the amount under review: error = %v, want FIELD_FROZEN", p.Actor, err)
		}
	}
	if err := w.update(t, w.bob, id, map[string]any{"f_reason": "over budget"}); err != nil {
		t.Errorf("bob writes the reason under review: %v", err)
	}
}

// The kernel's own writes are untouched: a hook writes f_amount of ana's
// draft through the internal door.
func TestRequesterOnly_KernelWritesUnaffected(t *testing.T) {
	w := newROWorld(t)
	id := w.draft(t)
	hook := item.Principal{Actor: "hook:ro_purchase:fill"}
	if _, err := w.k.Internal().Update(testCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: hook, ItemID: id, Fields: map[string]any{"f_amount": 2499},
		Attribution: item.Attribution{OriginatingHook: "hook:ro_purchase@v1:fill", CausationID: id.String() + ":1",
			CorrelationID: "corr-ro", Hop: 1},
	}); err != nil {
		t.Errorf("a hook writes f_amount of ana's draft: %v", err)
	}
}

// A create is always the requester's: whoever creates the Item is its
// requester, so the draft's rule never refuses a create.
func TestRequesterOnly_CreateIsTheRequesters(t *testing.T) {
	w := newROWorld(t)
	if _, err := w.k.Create(testCtx(t), itemservice.CreateRequest{
		IdempotencyKey: newKey(), Principal: w.olaf, TypeID: "ro_purchase", TypeVersion: 1,
		Fields: map[string]any{"f_amount": 10, "f_vendor": "olafco", "f_justification": "Chairs"},
	}); err != nil {
		t.Errorf("olaf creates his own draft with its fields: %v", err)
	}
}
