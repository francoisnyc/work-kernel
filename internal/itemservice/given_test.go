// Tests for "required means given by the mover" (docs/roadmap.md, track 1,
// item 3; the owner's decisions of Phase A). A transition may list, in
// `given`, fields of its `required` that must be given in the move itself:
// named in the move's own patch, with a value. A value written before, by
// anyone, does not count; the same value given again does. A missing given
// field is refused like a missing required field: MISSING_FIELDS, naming
// it, in the same place in the order of checks (after not writable, frozen
// and held; with the guard). Opt-in: a transition without `given` keeps
// today's `required`.
//
// First the bake-offs' attack (docs/research/camunda-bakeoff.md, test 2b;
// docs/research/temporal-bakeoff.md): a reason for a decision was planted
// before the decider acted, so the decider rejected without giving one,
// though the Type requires it. Since track 1 item 2 no agent plants it; a
// colleague still could. gv_purchase is the bake-off's purchase@1, cut to
// what the tests touch, with `given: [f_reason]` on both rejections.
package itemservice_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
	"github.com/francoisnyc/work-kernel/internal/lifecycle"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// gvPurchaseSrc: TYPE is replaced by the Type's ID; GIVEN by the rejections'
// given list (or nothing); REASON by f_reason's writable_by (or nothing).
const gvPurchaseSrc = `
id: TYPE
version: 1
initial_state: draft
fields:
  - {id: f_amount, type: int}
  - {id: f_note,   type: string}
  - {id: f_reason, type: string REASON}
states:
  - {id: draft,          category: todo}
  - {id: manager_review, category: waiting, frozen: [f_amount]}
  - {id: finance_review, category: active,  frozen: [f_amount]}
  - {id: approved,       category: done,     frozen: [f_amount, f_reason]}
  - {id: rejected,       category: canceled, frozen: [f_amount, f_reason]}
transitions:
  - {from: draft,          to: manager_review, allowed: [creator], required: [f_amount]}
  - {from: manager_review, to: finance_review, allowed: ['role:gv_manager'], except: [creator]}
  - {from: manager_review, to: rejected,       allowed: ['role:gv_manager'], except: [creator], required: [f_reason] GIVEN}
  - {from: finance_review, to: approved,       allowed: ['role:gv_finance'], except: [creator]}
  - {from: finance_review, to: rejected,       allowed: ['role:gv_finance'], except: [creator], required: [f_reason] GIVEN}
`

// The three Types the tests use: the rule; the same Type without it
// (today's required); and the rule with f_reason open to agents.
const (
	gvPurchase = "gv_purchase"
	gvPlain    = "gv_plain"
	gvOpen     = "gv_open"
)

func gvSource(typeID string) string {
	given, reason := ", given: [f_reason]", ""
	switch typeID {
	case gvPlain:
		given = ""
	case gvOpen:
		reason = ", writable_by: [people, agents]"
	}
	return strings.NewReplacer("TYPE", typeID, " GIVEN", given, " REASON", reason).Replace(gvPurchaseSrc)
}

// gvWorld: ana asks; bob is her manager; fay is in finance; gus is a
// colleague with no role in the purchase. bot is an agent acting for fay.
type gvWorld struct {
	k                  *itemservice.Service
	ana, bob, fay, gus item.Principal
	forFay             item.Principal
}

func newGVWorld(t *testing.T) gvWorld {
	t.Helper()
	k := itemservice.New(pool)
	for _, id := range []string{gvPurchase, gvPlain, gvOpen} {
		ty, err := typedef.Parse([]byte(gvSource(id)))
		if err != nil {
			t.Fatalf("parse %s: %v", id, err)
		}
		if err := k.RegisterType(testCtx(t), ty); err != nil {
			t.Fatalf("register %s: %v", id, err)
		}
	}
	w := gvWorld{k: k, ana: person("ana"), bob: person("bob"), fay: person("fay"), gus: person("gus")}
	grantRole(t, w.bob, "gv_manager")
	grantRole(t, w.fay, "gv_finance")
	agent := "agent:gv-bot-" + uuid.NewString()[:8]
	w.forFay = withSession(t, k, item.Principal{Actor: agent, OnBehalfOf: w.fay.Actor})
	return w
}

// inFinance: ana's purchase of 2500, submitted, sent on by bob: fay decides.
func (w gvWorld) inFinance(t *testing.T, typeID string) uuid.UUID {
	t.Helper()
	id := w.inManagerReview(t, typeID)
	w.mustMove(t, w.bob, id, "finance_review", nil)
	return id
}

// inManagerReview: ana's purchase of 2500, submitted: bob decides.
func (w gvWorld) inManagerReview(t *testing.T, typeID string) uuid.UUID {
	t.Helper()
	res, err := w.k.Create(testCtx(t), itemservice.CreateRequest{
		IdempotencyKey: newKey(), Principal: w.ana, TypeID: typeID, TypeVersion: 1,
		Fields: map[string]any{"f_amount": 2500},
	})
	if err != nil {
		t.Fatalf("ana creates a %s: %v", typeID, err)
	}
	w.mustMove(t, w.ana, res.ItemID, "manager_review", nil)
	return res.ItemID
}

// plant: gus writes a reason on the purchase before anyone decides.
func (w gvWorld) plant(t *testing.T, id uuid.UUID, reason string) {
	t.Helper()
	_, err := w.k.Update(testCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: w.gus, ItemID: id, Fields: map[string]any{"f_reason": reason},
	})
	if err != nil {
		t.Fatalf("gus writes f_reason (a person may, before the decision): %v", err)
	}
}

func (w gvWorld) move(t *testing.T, p item.Principal, id uuid.UUID, to string, patch map[string]any) error {
	t.Helper()
	_, err := w.k.Transition(testCtx(t), itemservice.TransitionRequest{
		IdempotencyKey: newKey(), Principal: p, ItemID: id, ToState: to, Fields: patch,
	})
	return err
}

func (w gvWorld) mustMove(t *testing.T, p item.Principal, id uuid.UUID, to string, patch map[string]any) {
	t.Helper()
	if err := w.move(t, p, id, to, patch); err != nil {
		t.Fatalf("%s moves it to %s: %v", p.Actor, to, err)
	}
}

func (w gvWorld) reason(t *testing.T, id uuid.UUID) any {
	t.Helper()
	it, err := w.k.Get(testCtx(t), w.ana, id)
	if err != nil {
		t.Fatalf("ana reads the purchase: %v", err)
	}
	return it.Fields["f_reason"]
}

// assertNotGiven: the move is refused as MISSING_FIELDS naming exactly
// f_reason, the item service says it must be given in the move, and
// nothing was written: the Item is still in state, at the same item_seq.
func assertNotGiven(t *testing.T, what string, err error, id uuid.UUID, state string, seq int64) {
	t.Helper()
	var rej *itemservice.RejectedError
	if !errors.Is(err, itemservice.ErrRejected) || !errors.As(err, &rej) ||
		rej.Code != string(lifecycle.MissingFields) || rej.Metadata["fields"] != "f_reason" {
		t.Errorf("%s: error = %v, want MISSING_FIELDS naming f_reason", what, err)
		return
	}
	want := `needs these fields given in the move: f_reason`
	if !strings.Contains(err.Error(), want) {
		t.Errorf("%s: error = %q, want it to say %q", what, err, want)
	}
	assertUnchanged(t, what, id, state, seq)
}

// The bake-offs' attack: gus, a colleague, writes a reason while finance
// reviews; fay rejects without giving one. Refused, and nothing changes.
// fay then gives her own reason in the move: the purchase is rejected, and
// the reason kept is hers.
func TestGiven_Bakeoff_APlantedReasonDoesNotCount(t *testing.T) {
	w := newGVWorld(t)
	id := w.inFinance(t, gvPurchase)
	w.plant(t, id, "planted by gus")
	seq := itemSeqOf(t, id)

	err := w.move(t, w.fay, id, "rejected", nil)
	assertNotGiven(t, "fay rejects without giving a reason", err, id, "finance_review", seq)

	w.mustMove(t, w.fay, id, "rejected", map[string]any{"f_reason": "Over the cost centre's budget"})
	if got := w.reason(t, id); got != "Over the cost centre's budget" {
		t.Errorf("the rejected purchase's f_reason = %v, want fay's", got)
	}
}

// The manager's rejection carries the rule too: a reason planted while bob
// decides does not let him reject without one.
func TestGiven_TheManagersRejection(t *testing.T) {
	w := newGVWorld(t)
	id := w.inManagerReview(t, gvPurchase)
	w.plant(t, id, "planted by gus")
	seq := itemSeqOf(t, id)
	assertNotGiven(t, "bob rejects without giving a reason", w.move(t, w.bob, id, "rejected", nil), id, "manager_review", seq)
	w.mustMove(t, w.bob, id, "rejected", map[string]any{"f_reason": "Not this quarter"})
}

// Given means named in the move with a value, whoever wrote what before:
// the same text as the planted one, given again by fay, counts; so does a
// reason with no earlier value, and an empty string (it counts as set for
// required, so it counts as given; a Type wanting a non-empty reason says
// so in a guard).
func TestGiven_AValueInTheMoveCounts(t *testing.T) {
	w := newGVWorld(t)

	again := w.inFinance(t, gvPurchase)
	w.plant(t, again, "Over budget")
	if err := w.move(t, w.fay, again, "rejected", map[string]any{"f_reason": "Over budget"}); err != nil {
		t.Errorf("fay gives the same reason as was planted, in her move: %v", err)
	}

	fresh := w.inFinance(t, gvPurchase)
	if err := w.move(t, w.fay, fresh, "rejected", map[string]any{"f_reason": "Over budget"}); err != nil {
		t.Errorf("fay gives a reason where none was written: %v", err)
	}

	empty := w.inFinance(t, gvPurchase)
	if err := w.move(t, w.fay, empty, "rejected", map[string]any{"f_reason": ""}); err != nil {
		t.Errorf("fay gives an empty reason (it counts as set, so as given): %v", err)
	}
}

// What does not give it: a patch that unsets it (null), and a patch of
// other fields only. Both are refused whole: the other field is not
// written either.
func TestGiven_NullOrOtherFieldsDoNotGive(t *testing.T) {
	w := newGVWorld(t)

	id := w.inFinance(t, gvPurchase)
	w.plant(t, id, "planted by gus")
	seq := itemSeqOf(t, id)
	err := w.move(t, w.fay, id, "rejected", map[string]any{"f_note": "see the budget sheet"})
	assertNotGiven(t, "fay rejects with a note and no reason", err, id, "finance_review", seq)

	unset := w.inFinance(t, gvPurchase)
	seq = itemSeqOf(t, unset)
	var rej *itemservice.RejectedError
	err = w.move(t, w.fay, unset, "rejected", map[string]any{"f_reason": nil})
	if !errors.As(err, &rej) || rej.Code != string(lifecycle.MissingFields) || rej.Metadata["fields"] != "f_reason" {
		t.Errorf("fay rejects with f_reason: null: %v, want MISSING_FIELDS naming f_reason", err)
	}
	assertUnchanged(t, "fay rejects with f_reason: null", unset, "finance_review", seq)
}

// Opt-in: the same Type without `given` keeps today's required, which a
// value written before meets, whoever wrote it.
func TestGiven_WithoutItRequiredIsUnchanged(t *testing.T) {
	w := newGVWorld(t)
	id := w.inFinance(t, gvPlain)
	w.plant(t, id, "planted by gus")
	if err := w.move(t, w.fay, id, "rejected", nil); err != nil {
		t.Fatalf("on a Type without given, fay rejects on the planted reason: %v (required alone is unchanged)", err)
	}
	if got := w.reason(t, id); got != "planted by gus" {
		t.Errorf("f_reason = %v, want the planted one kept", got)
	}
}

// An agent acting for fay. Where the Type keeps f_reason closed to agents
// (the default), its patch naming f_reason is FIELD_NOT_WRITABLE, which
// comes before the given rule; without a patch it is MISSING_FIELDS like
// fay's. Where the Type opens f_reason to agents, the agent gives it in
// fay's move: writable_by says who may give a field, given says when.
func TestGiven_AnAgentForTheDecider(t *testing.T) {
	w := newGVWorld(t)

	closed := w.inFinance(t, gvPurchase)
	w.plant(t, closed, "planted by gus")
	seq := itemSeqOf(t, closed)
	err := w.move(t, w.forFay, closed, "rejected", map[string]any{"f_reason": "Over budget"})
	var fe *itemservice.FieldNotWritableError
	if !errors.Is(err, itemservice.ErrForbidden) || !errors.As(err, &fe) || strings.Join(fe.Fields, ",") != "f_reason" {
		t.Errorf("the agent for fay gives a reason closed to agents: %v, want FIELD_NOT_WRITABLE naming f_reason", err)
	}
	assertUnchanged(t, "the agent for fay gives a closed reason", closed, "finance_review", seq)
	assertNotGiven(t, "the agent for fay rejects without a reason", w.move(t, w.forFay, closed, "rejected", nil),
		closed, "finance_review", seq)

	open := w.inFinance(t, gvOpen)
	w.plant(t, open, "planted by gus")
	seq = itemSeqOf(t, open)
	assertNotGiven(t, "the agent for fay rejects without a reason (open to agents)", w.move(t, w.forFay, open, "rejected", nil),
		open, "finance_review", seq)
	if err := w.move(t, w.forFay, open, "rejected", map[string]any{"f_reason": "Over budget"}); err != nil {
		t.Errorf("the agent for fay gives a reason the Type opens to agents: %v", err)
	}
	if got := w.reason(t, open); got != "Over budget" {
		t.Errorf("f_reason = %v, want the one given in the agent's move", got)
	}
}

// Where it sits in the order: after "not writable" (above, the agent) and
// before allowed: gus, who may not reject, is told the reason is missing
// when he gives none, and is refused as not allowed when he gives one.
// That is today's order for required, kept.
func TestGiven_TheOrderOfChecks(t *testing.T) {
	w := newGVWorld(t)
	id := w.inFinance(t, gvPurchase)
	w.plant(t, id, "planted by gus")
	seq := itemSeqOf(t, id)
	assertNotGiven(t, "gus rejects without a reason", w.move(t, w.gus, id, "rejected", nil), id, "finance_review", seq)
	if err := w.move(t, w.gus, id, "rejected", map[string]any{"f_reason": "mine"}); !errors.Is(err, itemservice.ErrForbidden) {
		t.Errorf("gus rejects, giving a reason: %v, want ErrForbidden (not finance)", err)
	}
	assertUnchanged(t, "gus's refused moves", id, "finance_review", seq)
}
