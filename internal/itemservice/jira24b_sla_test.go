// Tests for Jira 2.4b, PR 1: desk@3 (only sla_admin places it),
// sla_policy@2 (an approval target) and sla_goal@2 (the approval metric;
// no person moves a goal; no goal without a metric), on the repository's
// Type files, with a test host (an errand in a desk@3) that starts and
// stops an approval goal as the access request will in PR 2.
package itemservice_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
)

const errand4b = `
id: errand
version: 1
initial_state: open
fields:
  - {id: f_title,        type: string}
  - {id: f_contact,      type: principal}
  - {id: f_due_approval, type: datetime, writable_by: kernel}
offers:
  breach_contact: f_contact
states:
  - {id: open, category: active}
  - {id: done, category: done}
transitions:
  - {from: open, to: done}
reactions:
  - id: sla_start_approval
    kind: create
    on: enter
    state: open
    type: sla_goal
    version: 2
    fields:
      f_metric: '"approval"'
      f_target_s: 'container.linked.sla_policy.f_approval_s'
      f_started_at: 'cause.at'
      f_week: 'container.linked.calendar.f_week'
      f_days_off: 'container.linked.calendar.f_days_off'
      f_zone: 'container.linked.calendar.f_zone'
      f_due_at: >-
        working_after(cause.at, container.linked.sla_policy.f_approval_s,
        calendar(container.linked.calendar.f_week, container.linked.calendar.f_days_off, container.linked.calendar.f_zone))
  - {id: sla_stop, kind: move, on: enter, state: done, children: {of: [sla_goal]}, to: completed, target_from: [running, paused]}
accepts_reactions:
  sla_goal:show_approval:   {sets: [f_due_approval]}
  sla_goal:hide_approval:   {sets: [f_due_approval]}
  sla_goal:clear_approval:  {sets: [f_due_approval]}
  sla_goal:cancel_approval: {sets: [f_due_approval]}
`

type sla4b struct {
	k        *itemservice.Service
	ns       string
	lea, sam item.Principal
	desk     uuid.UUID
}

func newSLA4b(t *testing.T) sla4b {
	t.Helper()
	k := itemservice.New(pool)
	for _, f := range []string{"sla/calendar.v1.yaml", "sla/sla_policy.v2.yaml", "sla/sla_goal.v2.yaml", "desk.v3.yaml"} {
		registerFromFile(t, k, f)
	}
	registerSrc(t, k, errand4b)
	ns := newNamespace(t, k)
	w := sla4b{k: k, ns: ns, lea: in(person("lea"), ns), sam: in(person("sam"), ns)}
	grantRoleIn(t, ns, w.lea, "sla_admin")
	mk := func(typ string, version int, fields map[string]any) uuid.UUID {
		res, err := k.Create(testCtx(t), itemservice.CreateRequest{IdempotencyKey: newKey(), Principal: w.lea, TypeID: typ, TypeVersion: version, Fields: fields})
		if err != nil {
			t.Fatalf("create %s@%d: %v", typ, version, err)
		}
		return res.ItemID
	}
	cal := mk("calendar", 1, map[string]any{"f_name": "always", "f_week": alwaysOpen, "f_zone": "UTC"})
	pf := policyFields()
	pf["f_approval_s"] = 3600
	policy := mk("sla_policy", 2, pf)
	w.desk = mk("desk", 3, map[string]any{"f_key": "AR", "f_name": "access " + word()})
	for link, target := range map[string]uuid.UUID{"calendar": cal, "sla_policy": policy} {
		if _, err := k.Link(testCtx(t), itemservice.LinkRequest{IdempotencyKey: newKey(), Principal: w.lea, SourceID: w.desk,
			LinkType: link, TargetID: target}); err != nil {
			t.Fatalf("link %s: %v", link, err)
		}
	}
	return w
}

func (w sla4b) fields(t *testing.T, id uuid.UUID) map[string]any { t.Helper(); return fieldsOf(t, w.k, w.lea, id) }

func TestJira24b_Desk3_OnlySLAAdminPlacesIt(t *testing.T) {
	w := newSLA4b(t)
	var pe *itemservice.PlacementNotWritableError
	_, err := w.k.Update(testCtx(t), itemservice.UpdateRequest{IdempotencyKey: newKey(), Principal: w.sam, ItemID: w.desk,
		Policy: &itemservice.PolicyPatch{Readers: []string{w.sam.Actor}}})
	if !errors.As(err, &pe) {
		t.Errorf("sam narrows the desk (hiding every request under it): %v; want PlacementNotWritableError", err)
	}
	if _, err := w.k.Update(testCtx(t), itemservice.UpdateRequest{IdempotencyKey: newKey(), Principal: w.lea, ItemID: w.desk,
		Policy: &itemservice.PolicyPatch{Readers: []string{w.lea.Actor, w.sam.Actor}}}); err != nil {
		t.Errorf("lea (sla_admin) narrows the desk: %v", err)
	}
}

func TestJira24b_SLAGoal2_TheApprovalMetric_NoHandMoves_NoBareGoal(t *testing.T) {
	w := newSLA4b(t)
	errand, err := w.k.Create(testCtx(t), itemservice.CreateRequest{IdempotencyKey: newKey(), Principal: w.sam, TypeID: "errand", TypeVersion: 1,
		ContainerID: &w.desk, Fields: map[string]any{"f_title": "access to the VPN", "f_contact": w.lea.Actor}})
	if err != nil {
		t.Fatal(err)
	}
	start, err := w.k.Internal().ReactCreate(testCtx(t), entered(errand.ItemID, 1, "sla_start_approval"))
	if err != nil || start.Outcome != itemservice.ReactionApplied {
		t.Fatalf("sla_start_approval: %+v, %v", start, err)
	}
	goal := start.Target
	g := w.fields(t, goal)
	created := eventAt(t, errand.ItemID, 1)
	if g["f_metric"] != "approval" || r3Int(t, g["f_target_s"]) != 3600 || g["f_due_at"] != dtOf(created.Add(time.Hour)) {
		t.Errorf("the goal = %v; want an approval goal of 3600 s, due an hour after the errand", g)
	}
	if out, err := w.k.Internal().ReactSet(testCtx(t), entered(goal, 1, "show_approval")); err != nil || out.Outcome != itemservice.ReactionApplied {
		t.Fatalf("show_approval: %+v, %v", out, err)
	}
	if w.fields(t, errand.ItemID)["f_due_approval"] != g["f_due_at"] {
		t.Errorf("the errand shows %v, want the goal's due instant", w.fields(t, errand.ItemID)["f_due_approval"])
	}
	// Nobody completes a goal by hand, not even an admin.
	for _, p := range []item.Principal{w.sam, w.lea} {
		if _, err := w.k.Transition(testCtx(t), itemservice.TransitionRequest{IdempotencyKey: newKey(), Principal: p, ItemID: goal,
			ToState: "completed"}); !errors.Is(err, itemservice.ErrForbidden) {
			t.Errorf("%s completes the goal by hand: %v; want ErrForbidden", p.Actor, err)
		}
	}
	// The reaction still does.
	done, err := w.k.Transition(testCtx(t), itemservice.TransitionRequest{IdempotencyKey: newKey(), Principal: w.sam, ItemID: errand.ItemID, ToState: "done"})
	if err != nil {
		t.Fatal(err)
	}
	if out, err := w.k.Internal().ReactMove(testCtx(t), entered(errand.ItemID, done.ItemSeq, "sla_stop")); err != nil || out.Outcome != itemservice.ReactionApplied {
		t.Fatalf("sla_stop: %+v, %v", out, err)
	}
	if it, _ := w.k.Get(testCtx(t), w.lea, goal); it.State != "completed" || it.Fields["f_outcome"] != "met" {
		t.Errorf("the goal after the errand is done: %s, %v; want completed, met", it.State, it.Fields["f_outcome"])
	}
	// A goal with no metric is refused, whoever makes it.
	_, err = w.k.Create(testCtx(t), itemservice.CreateRequest{IdempotencyKey: newKey(), Principal: w.lea, TypeID: "sla_goal", TypeVersion: 2,
		ContainerID: &errand.ItemID})
	if rejectedCode(err) != "invariant_failed" {
		t.Errorf("a bare sla_goal@2: %v; want invariant_failed", err)
	}
}
