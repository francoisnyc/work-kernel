// The hardening step after Jira 2.4, on the repository's Type files: who
// places an incident (incident@3: desk agents), a reply and a note (reply@2,
// note@2: nobody, they stay in their incident), a goal (sla_goal@3: nobody)
// and a comment (comment@3: nobody); incident@3's goals are sla_goal@3, which
// no person moves. Items of the older versions keep their version's rules.
package itemservice_test

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
)

type hardWorld struct {
	k                    *itemservice.Service
	ns                   string
	lea, sam, ana, kim   item.Principal
	desk, desk2, oldDesk uuid.UUID
}

func newHardWorld(t *testing.T) hardWorld {
	t.Helper()
	k := itemservice.New(pool)
	for _, f := range []string{"sla/calendar.v1.yaml", "sla/sla_policy.v1.yaml", "sla/sla_goal.v1.yaml", "sla/sla_goal.v3.yaml",
		"reply.v1.yaml", "note.v1.yaml", "reply.v2.yaml", "note.v2.yaml", "comment.v2.yaml", "comment.v3.yaml",
		"incident.v2.yaml", "incident.v3.yaml", "desk.v2.yaml"} {
		registerFromFile(t, k, f)
	}
	ns := newNamespace(t, k)
	w := hardWorld{k: k, ns: ns, lea: in(person("lea"), ns), sam: in(person("sam"), ns), ana: in(person("ana"), ns), kim: customer("kim", ns)}
	grantRoleIn(t, ns, w.lea, "sla_admin")
	grantRoleIn(t, ns, w.lea, "desk_agent")
	grantRoleIn(t, ns, w.sam, "desk_agent")
	cal := w.mk(t, w.lea, "calendar", 1, nil, map[string]any{"f_name": "always", "f_week": alwaysOpen, "f_zone": "UTC"})
	policy := w.mk(t, w.lea, "sla_policy", 1, nil, policyFields())
	for _, d := range []*uuid.UUID{&w.desk, &w.desk2} {
		*d = w.mk(t, w.lea, "desk", 2, nil, map[string]any{"f_key": "IT", "f_name": "IT " + word()})
		for link, target := range map[string]uuid.UUID{"calendar": cal, "sla_policy": policy} {
			if _, err := k.Link(testCtx(t), itemservice.LinkRequest{IdempotencyKey: newKey(), Principal: w.lea, SourceID: *d,
				LinkType: link, TargetID: target}); err != nil {
				t.Fatalf("link the desk's %s: %v", link, err)
			}
		}
	}
	return w
}

func (w hardWorld) mk(t *testing.T, p item.Principal, typ string, version int, in *uuid.UUID, fields map[string]any) uuid.UUID {
	t.Helper()
	res, err := w.k.Create(testCtx(t), itemservice.CreateRequest{IdempotencyKey: newKey(), Principal: p, TypeID: typ,
		TypeVersion: version, ContainerID: in, Fields: fields})
	if err != nil {
		t.Fatalf("%s creates %s@%d: %v", p.Actor, typ, version, err)
	}
	return res.ItemID
}

func (w hardWorld) update(t *testing.T, p item.Principal, id uuid.UUID, req itemservice.UpdateRequest) error {
	t.Helper()
	req.IdempotencyKey, req.Principal, req.ItemID = newKey(), p, id
	_, err := w.k.Update(testCtx(t), req)
	return err
}

func narrowTo(p item.Principal) itemservice.UpdateRequest {
	return itemservice.UpdateRequest{Policy: &itemservice.PolicyPatch{Readers: []string{p.Actor}}}
}

func hardMove(to *uuid.UUID) itemservice.UpdateRequest {
	return itemservice.UpdateRequest{Move: &itemservice.Move{To: to, ConfirmWiden: true}}
}

// incident is an incident of a version in the desk, by who, with both its
// goals made; it returns the incident and the goals.
func (w hardWorld) incident(t *testing.T, who item.Principal, version int) (uuid.UUID, []uuid.UUID) {
	t.Helper()
	inc := w.mk(t, who, "incident", version, &w.desk, map[string]any{"f_title": "VPN down " + word(), "f_priority": "P3"})
	var goals []uuid.UUID
	for _, r := range []string{"sla_start_first_response", "sla_start_resolution"} {
		out, err := w.k.Internal().ReactCreate(testCtx(t), entered(inc, 1, r))
		if err != nil || out.Outcome != itemservice.ReactionApplied {
			t.Fatalf("%s: %+v, %v", r, out, err)
		}
		goals = append(goals, out.Target)
	}
	return inc, goals
}

func TestHardening_Incident3_ItsGoalsAreV3_NoPersonMovesOrPlacesThem(t *testing.T) {
	w := newHardWorld(t)
	inc, goals := w.incident(t, w.kim, 3)
	for _, g := range goals {
		it, err := w.k.Get(testCtx(t), w.lea, g)
		if err != nil {
			t.Fatal(err)
		}
		if it.TypeID != "sla_goal" || it.TypeVersion != 3 {
			t.Errorf("incident@3's goal is %s@%d; want sla_goal@3", it.TypeID, it.TypeVersion)
		}
		// Nobody completes a goal by hand, a desk agent included.
		for _, p := range []item.Principal{w.sam, w.lea} {
			if _, err := w.k.Transition(testCtx(t), itemservice.TransitionRequest{IdempotencyKey: newKey(), Principal: p,
				ItemID: g, ToState: "completed"}); !errors.Is(err, itemservice.ErrForbidden) {
				t.Errorf("%s completes a goal by hand: %v; want ErrForbidden", p.Actor, err)
			}
		}
		// Nobody narrows one away from the desk, or moves it out.
		if err := w.update(t, w.sam, g, narrowTo(w.sam)); !placementRefused(err) {
			t.Errorf("sam narrows a goal: %v; want PlacementNotWritableError", err)
		}
		if err := w.update(t, w.lea, g, hardMove(nil)); !placementRefused(err) {
			t.Errorf("lea moves a goal out of its incident: %v; want PlacementNotWritableError", err)
		}
	}
	// The incident's own moves still move its goals (a reaction, by consent).
	w.mustMove3(t, w.sam, inc, "in_progress")
	seq := w.mustMove3(t, w.sam, inc, "waiting_for_customer")
	if out, err := w.k.Internal().ReactMove(testCtx(t), entered(inc, seq, "sla_pause")); err != nil || out.Outcome != itemservice.ReactionApplied {
		t.Errorf("sla_pause: %+v, %v", out, err)
	}
}

func (w hardWorld) mustMove3(t *testing.T, p item.Principal, id uuid.UUID, to string) int64 {
	t.Helper()
	res, err := w.k.Transition(testCtx(t), itemservice.TransitionRequest{IdempotencyKey: newKey(), Principal: p, ItemID: id, ToState: to})
	if err != nil {
		t.Fatalf("%s moves to %s: %v", p.Actor, to, err)
	}
	return res.ItemSeq
}

func TestHardening_Incident3_PlacedByDeskAgentsOnly(t *testing.T) {
	w := newHardWorld(t)
	inc, _ := w.incident(t, w.ana, 3)
	if err := w.update(t, w.ana, inc, narrowTo(w.ana)); !placementRefused(err) {
		t.Errorf("ana narrows her incident: %v; want PlacementNotWritableError", err)
	}
	if err := w.update(t, w.ana, inc, hardMove(&w.desk2)); !placementRefused(err) {
		t.Errorf("ana moves her incident to another desk: %v; want PlacementNotWritableError", err)
	}
	if err := w.update(t, w.sam, inc, hardMove(&w.desk2)); err != nil {
		t.Errorf("sam (desk_agent) moves it to another desk: %v", err)
	}
	if err := w.update(t, w.sam, inc, itemservice.UpdateRequest{Policy: &itemservice.PolicyPatch{
		Readers: []string{w.sam.Actor, w.lea.Actor, w.ana.Actor}}}); err != nil {
		t.Errorf("sam (desk_agent) narrows it: %v", err)
	}
	// A customer narrows nothing either, as before (a customer never updates).
	kims, _ := w.incident(t, w.kim, 3)
	if err := w.update(t, w.kim, kims, narrowTo(w.kim)); err == nil {
		t.Error("kim narrows her incident: accepted")
	}
}

func TestHardening_Reply2Note2Comment3_StayWhereTheyWereWritten(t *testing.T) {
	w := newHardWorld(t)
	inc, _ := w.incident(t, w.ana, 3)
	other, _ := w.incident(t, w.ana, 3)
	reply := w.mk(t, w.sam, "reply", 2, &inc, map[string]any{"f_body": "on it"})
	note := w.mk(t, w.sam, "note", 2, &inc, map[string]any{"f_body": "internal"})
	comment := w.mk(t, w.sam, "comment", 3, &inc, map[string]any{"f_body": "a word", "f_public": false})
	for name, id := range map[string]uuid.UUID{"a reply": reply, "a note": note, "a comment": comment} {
		for _, p := range []item.Principal{w.sam, w.lea} {
			if err := w.update(t, p, id, hardMove(&other)); !placementRefused(err) {
				t.Errorf("%s moves %s to another incident: %v; want PlacementNotWritableError", p.Actor, name, err)
			}
			if err := w.update(t, p, id, narrowTo(p)); !placementRefused(err) {
				t.Errorf("%s narrows %s: %v; want PlacementNotWritableError", p.Actor, name, err)
			}
		}
	}
	// A customer's reply too.
	kims, _ := w.incident(t, w.kim, 3)
	w.mk(t, w.kim, "reply", 2, &kims, map[string]any{"f_body": "thanks"})
}

// Items of the older versions keep their version's rules: an incident@2,
// its goals (sla_goal@1) and a reply@1 are placed, and the goals moved, as
// before. design.md says so; moving Items to a new version is Later.
func TestHardening_OlderVersionsKeepTheirRules(t *testing.T) {
	w := newHardWorld(t)
	inc, goals := w.incident(t, w.ana, 2)
	if err := w.update(t, w.ana, inc, itemservice.UpdateRequest{Policy: &itemservice.PolicyPatch{
		Readers: []string{w.ana.Actor, w.sam.Actor}}}); err != nil {
		t.Errorf("ana narrows an incident@2: %v; want it accepted, as before", err)
	}
	g, err := w.k.Get(testCtx(t), w.sam, goals[0])
	if err != nil {
		t.Fatal(err)
	}
	if g.TypeVersion != 1 {
		t.Errorf("incident@2's goal is sla_goal@%d; want @1, as before", g.TypeVersion)
	}
	reply := w.mk(t, w.sam, "reply", 1, &inc, map[string]any{"f_body": "on it"})
	if err := w.update(t, w.sam, reply, narrowTo(w.sam)); err != nil {
		t.Errorf("sam narrows a reply@1: %v; want it accepted, as before", err)
	}
}
