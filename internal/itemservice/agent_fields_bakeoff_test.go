// Tests for fields closed to agents by default (docs/roadmap.md, track 1,
// item 2), first the two holes the bake-offs found
// (docs/research/temporal-bakeoff.md, measure 4;
// docs/research/camunda-bakeoff.md, test 2b). A compromised agent with
// every power, acting alone or for a person, changed what a person is
// asked to approve (a draft's amount and vendor) and planted the reason
// for a decision. af_purchase is the bake-off's purchase@1 cut to what the
// attacks touch, with no writable_by on what is asked or on the reason, as
// the bake-off's authors wrote it: the kernel's default must close both.
package itemservice_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

const afPurchaseSrc = `
id: af_purchase
version: 1
initial_state: draft
fields:
  - {id: f_amount,        type: int}
  - {id: f_vendor,        type: string}
  - {id: f_justification, type: string}
  - {id: f_summary,       type: string, writable_by: ['role:af_preparer']}
  - {id: f_reason,        type: string}
states:
  - {id: draft, category: todo}
  - {id: manager_review, category: waiting, frozen: [f_amount, f_vendor, f_justification]}
  - {id: finance_review, category: active,  frozen: [f_amount, f_vendor, f_justification, f_summary]}
  - {id: approved, category: done,     frozen: [f_amount, f_vendor, f_justification, f_summary, f_reason]}
  - {id: rejected, category: canceled, frozen: [f_amount, f_vendor, f_justification, f_summary, f_reason]}
transitions:
  - {from: draft, to: manager_review, allowed: [creator], required: [f_amount, f_vendor, f_justification]}
  - {from: manager_review, to: finance_review, allowed: ['role:af_manager'], except: [creator]}
  - {from: finance_review, to: approved, allowed: ['role:af_finance'], except: [creator]}
  - {from: finance_review, to: rejected, allowed: ['role:af_finance'], except: [creator], required: [f_reason]}
`

// purchaseWorld: ana asks, bob is a manager, fay is in finance, and
// prep-bot is the agent, holding the preparer role, with a session for
// itself and sessions acting for ana and for fay, every power in each.
type purchaseWorld struct {
	k                  *itemservice.Service
	ana, bob, fay      item.Principal
	bot, forAna, forFay item.Principal
}

func newPurchaseWorld(t *testing.T) purchaseWorld {
	t.Helper()
	k := itemservice.New(pool)
	ty, err := typedef.Parse([]byte(afPurchaseSrc))
	if err != nil {
		t.Fatalf("parse af_purchase: %v", err)
	}
	if err := k.RegisterType(testCtx(t), ty); err != nil {
		t.Fatalf("register af_purchase: %v", err)
	}
	w := purchaseWorld{k: k, ana: person("ana"), bob: person("bob"), fay: person("fay")}
	grantRole(t, w.bob, "af_manager")
	grantRole(t, w.fay, "af_finance")
	agent := "agent:prep-bot-" + uuid.NewString()[:8]
	grantRole(t, item.Principal{Actor: agent}, "af_preparer")
	w.bot = withSession(t, k, item.Principal{Actor: agent})
	w.forAna = withSession(t, k, item.Principal{Actor: agent, OnBehalfOf: w.ana.Actor})
	w.forFay = withSession(t, k, item.Principal{Actor: agent, OnBehalfOf: w.fay.Actor})
	return w
}

// draft: ana's purchase of 2500 from officeco, not yet submitted.
func (w purchaseWorld) draft(t *testing.T) uuid.UUID {
	t.Helper()
	res, err := w.k.Create(testCtx(t), itemservice.CreateRequest{
		IdempotencyKey: newKey(), Principal: w.ana, TypeID: "af_purchase", TypeVersion: 1,
		Fields: map[string]any{"f_amount": 2500, "f_vendor": "officeco", "f_justification": "Monitors for the team"},
	})
	if err != nil {
		t.Fatalf("ana creates a purchase: %v", err)
	}
	return res.ItemID
}

func (w purchaseWorld) moveAs(t *testing.T, p item.Principal, id uuid.UUID, to string, patch map[string]any) error {
	t.Helper()
	_, err := w.k.Transition(testCtx(t), itemservice.TransitionRequest{
		IdempotencyKey: newKey(), Principal: p, ItemID: id, ToState: to, Fields: patch,
	})
	return err
}

func (w purchaseWorld) mustMove(t *testing.T, p item.Principal, id uuid.UUID, to string, patch map[string]any) {
	t.Helper()
	if err := w.moveAs(t, p, id, to, patch); err != nil {
		t.Fatalf("%s moves it to %s: %v", p.Actor, to, err)
	}
}

func (w purchaseWorld) updateAs(t *testing.T, p item.Principal, id uuid.UUID, fields map[string]any) error {
	t.Helper()
	_, err := w.k.Update(testCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: p, ItemID: id, Fields: fields,
	})
	return err
}

// assertClosedToAgents: the write is refused as FIELD_NOT_WRITABLE naming
// exactly fields, and the item service says why: the field is closed to
// agents.
func assertClosedToAgents(t *testing.T, what string, err error, fields ...string) {
	t.Helper()
	if !errors.Is(err, itemservice.ErrForbidden) {
		t.Fatalf("%s: error = %v, want ErrForbidden (FIELD_NOT_WRITABLE)", what, err)
	}
	var fe *itemservice.FieldNotWritableError
	if !errors.As(err, &fe) {
		t.Fatalf("%s: error %v is not a *FieldNotWritableError", what, err)
	}
	if !slices.Equal(fe.Fields, fields) {
		t.Errorf("%s: refused fields = %v, want %v", what, fe.Fields, fields)
	}
	if !strings.Contains(err.Error(), "opens to agents") {
		t.Errorf("%s: error = %q, want it to say the field is not one its Type opens to agents", what, err)
	}
}

// Hole 1: with only the right to edit fields, the agent changed ana's draft
// before she submitted it, and what was frozen for approval was the
// agent's version. Refused by default, acting alone and acting for ana
// (who may edit her draft herself); ana then submits her own values.
func TestAgentFields_Bakeoff_TheAgentMayNotEditADraft(t *testing.T) {
	w := newPurchaseWorld(t)
	id := w.draft(t)

	err := w.updateAs(t, w.bot, id, map[string]any{"f_amount": 950, "f_vendor": "shadyparts"})
	assertClosedToAgents(t, "the agent changes the amount and vendor", err, "f_amount", "f_vendor")
	err = w.updateAs(t, w.forAna, id, map[string]any{"f_amount": 950})
	assertClosedToAgents(t, "the agent, acting for ana, changes the amount", err, "f_amount")
	assertNothingWrittenNoOutbox(t, id, 1)

	w.mustMove(t, w.ana, id, "manager_review", nil)
	got := get(t, w.k, id)
	if got.Fields["f_amount"] != float64(2500) || got.Fields["f_vendor"] != "officeco" {
		t.Errorf("frozen for approval: %v, want ana's 2500 from officeco", got.Fields)
	}
}

// Hole 2: the agent planted a rejection reason while finance reviewed, so
// fay could reject without giving one, though the Type requires it.
// Refused by default, acting alone and acting for fay; fay's reject
// without a reason stays refused, and with hers it applies.
func TestAgentFields_Bakeoff_TheAgentMayNotPlantTheReason(t *testing.T) {
	w := newPurchaseWorld(t)
	id := w.draft(t)
	w.mustMove(t, w.ana, id, "manager_review", nil)
	w.mustMove(t, w.bob, id, "finance_review", nil)

	err := w.updateAs(t, w.bot, id, map[string]any{"f_reason": "planted by the agent"})
	assertClosedToAgents(t, "the agent plants a reason", err, "f_reason")
	err = w.updateAs(t, w.forFay, id, map[string]any{"f_reason": "planted by the agent"})
	assertClosedToAgents(t, "the agent, acting for fay, plants a reason", err, "f_reason")
	err = w.moveAs(t, w.forFay, id, "rejected", map[string]any{"f_reason": "planted by the agent"})
	if !errors.Is(err, itemservice.ErrForbidden) {
		t.Errorf("the agent, acting for fay, rejects with a reason: %v, want refused", err)
	}

	err = w.moveAs(t, w.fay, id, "rejected", nil)
	assertRejectedCode(t, "fay rejects without a reason", err, itemservice.CodeMissingFields)
	w.mustMove(t, w.fay, id, "rejected", map[string]any{"f_reason": "over budget"})
	if got := get(t, w.k, id); got.Fields["f_reason"] != "over budget" {
		t.Errorf("f_reason = %v, want fay's", got.Fields["f_reason"])
	}
}

// What the bake-offs' agent is for still works: it holds the preparer
// role, and a field limited to a role it holds is open to it. The same
// agent is refused on what is asked, in the same request.
func TestAgentFields_Bakeoff_ThePreparationStillWorks(t *testing.T) {
	w := newPurchaseWorld(t)
	id := w.draft(t)
	w.mustMove(t, w.ana, id, "manager_review", nil)

	if err := w.updateAs(t, w.bot, id, map[string]any{"f_summary": "Checked: within policy"}); err != nil {
		t.Errorf("the agent writes its preparation: %v", err)
	}
	err := w.updateAs(t, w.bot, id, map[string]any{"f_summary": "x", "f_justification": "x"})
	assertClosedToAgents(t, "the agent writes its preparation and the justification", err, "f_justification")
}
