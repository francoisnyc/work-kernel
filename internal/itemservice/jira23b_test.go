// Tests for Jira part 2, step 3b, in the item service: the `sla` package
// (types/sla/) with its first host, incident@1 in a desk@1, on the
// repository's own Type files. The reactions are run by hand through the
// internal door, as the workflow would; the end-to-end tests run them for
// real.
//
// Three kernel changes are held here (the Phase A answers, items 8, 11
// and 12):
//
//   - A hook reads container:<offer>: HookInput gives the value of the
//     field the container's Type offers under that name, read when the
//     hook runs. Nobody to tell (no container, no such offer, the field
//     unset, or a principal who cannot see the container) is
//     ErrNoRecipient; the hook then fails once with no_recipient.
//   - A link whose writable_by lists roles is added and removed only by
//     their holders.
//   - An outcome record of a reaction names its target through the
//     visibility filter (Event.ReactionTarget).
//
// And what the Types do with them: both goals start at creation with the
// target the desk's policy gives the incident's priority; only the
// resolution goal pauses; reopening starts a new resolution goal only;
// canceling ends the goals without an outcome.
//
// Time: the calendar here is open around the clock in UTC, so working time
// is time. Business hours are tested in packages worktime and lifecycle.
package itemservice_test

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
	"github.com/francoisnyc/work-kernel/internal/lifecycle"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

type slaWorld struct {
	k *itemservice.Service
	// late is the same service with its clock an hour ahead.
	late *itemservice.Service
	ns   string
	// admin holds sla_admin; ana opens incidents; sam works on them; eve
	// is in the namespace and has nothing to do with any of it.
	admin, ana, sam, eve item.Principal
	cal, policy, desk    uuid.UUID
}

// Seconds per priority: first response, resolution.
var slaTargets = map[string][2]int{"P1": {60, 600}, "P2": {120, 1200}, "P3": {180, 1800}, "P4": {240, 2400}}

func policyFields() map[string]any {
	out := map[string]any{"f_name": "standard " + word()}
	for prio, s := range slaTargets {
		p := map[string]string{"P1": "p1", "P2": "p2", "P3": "p3", "P4": "p4"}[prio]
		out["f_first_response_s_"+p] = s[0]
		out["f_resolution_s_"+p] = s[1]
	}
	return out
}

func newSLAWorld(t *testing.T) slaWorld {
	t.Helper()
	k := itemservice.New(pool)
	for _, f := range []string{"bug.v3.yaml", "comment.v2.yaml", "sla/calendar.v1.yaml", "sla/sla_policy.v1.yaml",
		"sla/sla_goal.v1.yaml", "desk.v1.yaml", "incident.v1.yaml"} {
		registerFromFile(t, k, f)
	}
	ns := newNamespace(t, k)
	w := slaWorld{k: k, late: itemservice.NewWithOptions(pool, itemservice.Options{ClockSkew: time.Hour}), ns: ns,
		admin: in(person("admin"), ns), ana: in(person("ana"), ns), sam: in(person("sam"), ns), eve: in(person("eve"), ns)}
	grantRoleIn(t, ns, w.admin, "sla_admin")
	w.cal = w.create(t, w.admin, "calendar", nil, map[string]any{"f_name": "always", "f_week": alwaysOpen, "f_zone": "UTC"})
	w.policy = w.create(t, w.admin, "sla_policy", nil, policyFields())
	w.desk = w.create(t, w.admin, "desk", nil, map[string]any{"f_key": "IT", "f_name": "IT " + word()})
	w.link(t, w.admin, w.desk, "calendar", w.cal)
	w.link(t, w.admin, w.desk, "sla_policy", w.policy)
	return w
}

func (w slaWorld) create(t *testing.T, p item.Principal, typ string, in *uuid.UUID, fields map[string]any) uuid.UUID {
	t.Helper()
	res, err := w.k.Create(testCtx(t), itemservice.CreateRequest{
		IdempotencyKey: newKey(), Principal: p, TypeID: typ, TypeVersion: 1, ContainerID: in, Fields: fields,
		Attribution: item.Attribution{CorrelationID: "corr-" + uuid.NewString()[:8]},
	})
	if err != nil {
		t.Fatalf("create %s: %v", typ, err)
	}
	return res.ItemID
}

func (w slaWorld) link(t *testing.T, p item.Principal, src uuid.UUID, name string, dst uuid.UUID) {
	t.Helper()
	if _, err := w.k.Link(testCtx(t), itemservice.LinkRequest{
		IdempotencyKey: newKey(), Principal: p, SourceID: src, LinkType: name, TargetID: dst,
	}); err != nil {
		t.Fatalf("link %s: %v", name, err)
	}
}

func (w slaWorld) move(t *testing.T, p item.Principal, id uuid.UUID, to string, fields map[string]any) int64 {
	t.Helper()
	res, err := w.k.Transition(testCtx(t), itemservice.TransitionRequest{
		IdempotencyKey: newKey(), Principal: p, ItemID: id, ToState: to, Fields: fields,
	})
	if err != nil {
		t.Fatalf("move to %s: %v", to, err)
	}
	return res.ItemSeq
}

func (w slaWorld) state(t *testing.T, id uuid.UUID) string {
	t.Helper()
	it, err := w.k.Get(testCtx(t), w.admin, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	return it.State
}

func (w slaWorld) fields(t *testing.T, id uuid.UUID) map[string]any {
	t.Helper()
	return fieldsOf(t, w.k, w.admin, id)
}

// incident is ana's incident in the desk, with nothing run yet.
func (w slaWorld) incident(t *testing.T, prio string, more map[string]any) uuid.UUID {
	t.Helper()
	fields := map[string]any{"f_title": "VPN down " + word(), "f_priority": prio}
	for k, v := range more {
		fields[k] = v
	}
	return w.create(t, w.ana, "incident", &w.desk, fields)
}

// goalSets runs the goal's own set reactions for the event that entered
// its current state: the one of its metric and state applies, each other
// declared for that state says condition_false.
func (w slaWorld) goalSets(t *testing.T, goal uuid.UUID, seq int64) {
	t.Helper()
	ty, err := w.k.Type(testCtx(t), "sla_goal", 1)
	if err != nil {
		t.Fatalf("the sla_goal Type: %v", err)
	}
	state, applied := w.state(t, goal), 0
	for _, r := range ty.Reactions {
		if r.State != state {
			continue
		}
		out, err := w.k.Internal().ReactSet(testCtx(t), entered(goal, seq, r.ID))
		if err != nil {
			t.Fatalf("ReactSet %s: %v", r.ID, err)
		}
		switch {
		case out.Outcome == itemservice.ReactionApplied:
			applied++
		case out.Outcome != itemservice.ReactionSkipped || out.Reason != itemservice.SkipConditionFalse:
			t.Fatalf("ReactSet %s = %+v; want applied, or skipped condition_false", r.ID, out)
		}
	}
	if applied != 1 {
		t.Fatalf("%d set reactions applied for a goal entering %s, want exactly one (its metric's)", applied, state)
	}
}

// started is an incident whose two goals exist and show on it.
func (w slaWorld) started(t *testing.T, prio string, more map[string]any) (inc, first, res uuid.UUID) {
	t.Helper()
	inc = w.incident(t, prio, more)
	f := w.reactCreate(t, entered(inc, 1, "sla_start_first_response"))
	r := w.reactCreate(t, entered(inc, 1, "sla_start_resolution"))
	if f.Outcome != itemservice.ReactionApplied || r.Outcome != itemservice.ReactionApplied {
		t.Fatalf("the start reactions = %+v, %+v; want both applied", f, r)
	}
	w.goalSets(t, f.Target, 1)
	w.goalSets(t, r.Target, 1)
	return inc, f.Target, r.Target
}

func (w slaWorld) reactCreate(t *testing.T, req itemservice.ReactionRunRequest) itemservice.ReactionOutcome {
	t.Helper()
	out, err := w.k.Internal().ReactCreate(testCtx(t), req)
	if err != nil {
		t.Fatalf("ReactCreate %s: %v", req.Reaction, err)
	}
	return out
}

func (w slaWorld) reactMove(t *testing.T, req itemservice.ReactionRunRequest) itemservice.ReactionOutcome {
	t.Helper()
	out, err := w.k.Internal().ReactMove(testCtx(t), req)
	if err != nil {
		t.Fatalf("ReactMove %s: %v", req.Reaction, err)
	}
	return out
}

func dtOf(at time.Time) string { return at.UTC().Format(typedef.DateTimeLayout) }

// --- only sla_admin chooses a desk's calendar and policy ---

func TestDesk_OnlySLAAdminLinksItsCalendarAndPolicy(t *testing.T) {
	w := newSLAWorld(t)
	desk := w.create(t, w.ana, "desk", nil, map[string]any{"f_key": "HR", "f_name": "HR " + word()})
	mine := w.create(t, w.admin, "calendar", nil, map[string]any{"f_name": "mine", "f_week": alwaysOpen, "f_zone": "UTC"})

	for _, c := range []struct {
		link   string
		target uuid.UUID
	}{{"calendar", mine}, {"sla_policy", w.policy}} {
		_, err := w.k.Link(testCtx(t), itemservice.LinkRequest{IdempotencyKey: newKey(), Principal: w.ana, SourceID: desk, LinkType: c.link, TargetID: c.target})
		var nw *itemservice.LinkNotWritableError
		if !errors.As(err, &nw) || nw.LinkType != c.link || !errors.Is(err, itemservice.ErrForbidden) {
			t.Errorf("ana links %s on her own desk: %v; want LinkNotWritableError", c.link, err)
		}
		w.link(t, w.admin, desk, c.link, c.target)
		// Nor may she remove what the admin set.
		_, err = w.k.Unlink(testCtx(t), itemservice.LinkRequest{IdempotencyKey: newKey(), Principal: w.ana, SourceID: desk, LinkType: c.link, TargetID: c.target})
		if !errors.As(err, &nw) {
			t.Errorf("ana unlinks %s: %v; want LinkNotWritableError", c.link, err)
		}
	}
	// A refused link wrote nothing: two links, two events after the create.
	if n := itemSeqOf(t, desk); n != 3 {
		t.Errorf("the desk is at item_seq %d, want 3 (create and the admin's two links)", n)
	}
	// The admin replaces the calendar: unlink, link.
	if _, err := w.k.Unlink(testCtx(t), itemservice.LinkRequest{IdempotencyKey: newKey(), Principal: w.admin, SourceID: desk, LinkType: "calendar", TargetID: mine}); err != nil {
		t.Errorf("the admin unlinks the calendar: %v", err)
	}
	// An agent acting for the admin holds no role of its own: both must.
	agent := withSession(t, w.k, item.Principal{Actor: "agent:" + uuid.NewString()[:8], OnBehalfOf: w.admin.Actor, Namespace: w.ns})
	_, err := w.k.Link(testCtx(t), itemservice.LinkRequest{IdempotencyKey: newKey(), Principal: agent, SourceID: desk, LinkType: "calendar", TargetID: mine})
	var nw *itemservice.LinkNotWritableError
	if !errors.As(err, &nw) {
		t.Errorf("an agent without the role, for the admin: %v; want LinkNotWritableError (as for a role-written field)", err)
	}
}

func TestSLAPolicy_OnlySLAAdminWritesTargets(t *testing.T) {
	w := newSLAWorld(t)
	_, err := w.k.Create(testCtx(t), itemservice.CreateRequest{IdempotencyKey: newKey(), Principal: w.ana, TypeID: "sla_policy", TypeVersion: 1, Fields: policyFields()})
	var nw *itemservice.FieldNotWritableError
	if !errors.As(err, &nw) {
		t.Errorf("ana creates a policy with targets: %v; want FieldNotWritableError", err)
	}
	_, err = w.k.Update(testCtx(t), itemservice.UpdateRequest{IdempotencyKey: newKey(), Principal: w.ana, ItemID: w.policy,
		Fields: map[string]any{"f_resolution_s_p1": 999999}})
	if !errors.As(err, &nw) || !slices.Equal(nw.Fields, []string{"f_resolution_s_p1"}) {
		t.Errorf("ana gives herself more time: %v; want FieldNotWritableError naming the field", err)
	}
	// A target is a positive number of seconds.
	for _, bad := range []any{0, -60} {
		_, err = w.k.Update(testCtx(t), itemservice.UpdateRequest{IdempotencyKey: newKey(), Principal: w.admin, ItemID: w.policy,
			Fields: map[string]any{"f_resolution_s_p1": bad}})
		var rej *itemservice.RejectedError
		if !errors.As(err, &rej) {
			t.Errorf("a target of %v seconds: %v; want refused by the policy's invariant", bad, err)
		}
	}
}

// --- the goals start at creation ---

func TestIncident_StartsBothGoals_TargetsByPriority(t *testing.T) {
	w := newSLAWorld(t)
	for _, prio := range []string{"P1", "P3"} {
		inc, first, res := w.started(t, prio, nil)
		created := eventAt(t, inc, 1)
		for metric, goal := range map[string]uuid.UUID{"first_response": first, "resolution": res} {
			target := slaTargets[prio][0]
			if metric == "resolution" {
				target = slaTargets[prio][1]
			}
			g := w.fields(t, goal)
			if g["f_metric"] != metric || r3Int(t, g["f_target_s"]) != int64(target) ||
				g["f_started_at"] != dtOf(created) || g["f_due_at"] != dtOf(created.Add(time.Duration(target)*time.Second)) {
				t.Errorf("%s %s goal = %v; want target %d s, started at the incident's create time %s, due %d s later",
					prio, metric, g, target, dtOf(created), target)
			}
			// The calendar is copied: the goal never reads the desk's again.
			if g["f_zone"] != "UTC" || !slices.Equal(r3Strings(t, g["f_week"]), r3Strings(t, alwaysOpen)) {
				t.Errorf("%s %s goal's calendar: zone %v, week %v", prio, metric, g["f_zone"], g["f_week"])
			}
			for _, f := range []string{"f_elapsed_s", "f_breached_at", "f_outcome"} {
				if g[f] != nil {
					t.Errorf("%s %s goal starts with %s = %v", prio, metric, f, g[f])
				}
			}
			it, err := w.k.Get(testCtx(t), w.ana, goal)
			if err != nil || it.State != "running" || it.ContainerID == nil || *it.ContainerID != inc ||
				it.CreatedBy != "reaction:incident:sla_start_"+metric || it.CreatedOnBehalfOf != "" {
				t.Errorf("%s %s goal = %+v, %v; want running, in the incident, readable by its requester, made by the reaction for nobody",
					prio, metric, it, err)
			}
		}
		// Shown on the incident, one field per metric.
		f := w.fields(t, inc)
		if f["f_due_first_response"] != w.fields(t, first)["f_due_at"] || f["f_due_resolution"] != w.fields(t, res)["f_due_at"] {
			t.Errorf("%s incident: f_due_first_response %v, f_due_resolution %v; want its goals' due instants",
				prio, f["f_due_first_response"], f["f_due_resolution"])
		}
	}
}

func TestIncident_AGoalKeepsWhatItStartedWith(t *testing.T) {
	w := newSLAWorld(t)
	inc, _, res := w.started(t, "P2", nil)
	before := w.fields(t, res)
	// The priority changes, the policy changes, the desk gets another
	// calendar: the running goal is as it was (recomputing is Later).
	if _, err := w.k.Update(testCtx(t), itemservice.UpdateRequest{IdempotencyKey: newKey(), Principal: w.sam, ItemID: inc,
		Fields: map[string]any{"f_priority": "P1"}}); err != nil {
		t.Fatalf("change the priority: %v", err)
	}
	if _, err := w.k.Update(testCtx(t), itemservice.UpdateRequest{IdempotencyKey: newKey(), Principal: w.admin, ItemID: w.policy,
		Fields: map[string]any{"f_resolution_s_p2": 5, "f_resolution_s_p1": 5}}); err != nil {
		t.Fatalf("change the policy: %v", err)
	}
	if _, err := w.k.Update(testCtx(t), itemservice.UpdateRequest{IdempotencyKey: newKey(), Principal: w.admin, ItemID: w.cal,
		Fields: map[string]any{"f_week": []any{"mon 09:00-17:00"}, "f_zone": "Europe/Paris"}}); err != nil {
		t.Fatalf("change the calendar: %v", err)
	}
	if after := w.fields(t, res); fmt.Sprint(after) != fmt.Sprint(before) {
		t.Errorf("the running goal changed:\n got %v\nwant %v", after, before)
	}
	if itemSeqOf(t, res) != 1 {
		t.Errorf("the goal has %d events, want its create only", itemSeqOf(t, res))
	}
}

func TestIncident_StartRefusals(t *testing.T) {
	w := newSLAWorld(t)
	// Priority is required at creation: the target is picked by it.
	_, err := w.k.Create(testCtx(t), itemservice.CreateRequest{IdempotencyKey: newKey(), Principal: w.ana, TypeID: "incident", TypeVersion: 1,
		ContainerID: &w.desk, Fields: map[string]any{"f_title": "no priority " + word()}})
	var rej *itemservice.RejectedError
	if !errors.As(err, &rej) {
		t.Errorf("an incident without a priority: %v; want refused", err)
	}
	// The requester cannot write the due instants.
	_, err = w.k.Create(testCtx(t), itemservice.CreateRequest{IdempotencyKey: newKey(), Principal: w.ana, TypeID: "incident", TypeVersion: 1,
		ContainerID: &w.desk, Fields: map[string]any{"f_title": "mine " + word(), "f_priority": "P4", "f_due_resolution": "2099-01-01T00:00:00Z"}})
	var nw *itemservice.FieldNotWritableError
	if !errors.As(err, &nw) {
		t.Errorf("an incident created with its own due instant: %v; want FieldNotWritableError", err)
	}

	// A desk with no policy: the goal cannot start, and the incident says
	// so once (reaction_failed not_linked), not retried.
	bare := w.create(t, w.admin, "desk", nil, map[string]any{"f_key": "X", "f_name": "bare " + word()})
	w.link(t, w.admin, bare, "calendar", w.cal)
	inc := w.create(t, w.ana, "incident", &bare, map[string]any{"f_title": "no policy " + word(), "f_priority": "P1"})
	out := w.reactCreate(t, entered(inc, 1, "sla_start_resolution"))
	if out.Outcome != itemservice.ReactionFailed || out.Reason != "not_linked" {
		t.Errorf("start in a desk without a policy = %+v; want failed, not_linked", out)
	}
	// An incident outside any desk: no_container.
	alone := w.create(t, w.ana, "incident", nil, map[string]any{"f_title": "no desk " + word(), "f_priority": "P1"})
	if out := w.reactCreate(t, entered(alone, 1, "sla_start_first_response")); out.Outcome != itemservice.ReactionFailed || out.Reason != "no_container" {
		t.Errorf("start outside a desk = %+v; want failed, no_container", out)
	}
}

// --- pause, resume, stop, reopen, cancel ---

func TestIncident_OnlyResolutionPauses_AndOnlyWhileWaitingForTheCustomer(t *testing.T) {
	w := newSLAWorld(t)
	inc, first, res := w.started(t, "P1", nil)
	w.move(t, w.sam, inc, "in_progress", nil)
	waiting := w.move(t, w.sam, inc, "waiting_for_customer", nil)

	out := w.reactMove(t, entered(inc, waiting, "sla_pause"))
	if out.Outcome != itemservice.ReactionApplied || len(out.Each) != 1 || out.Each[0].Target != res {
		t.Fatalf("sla_pause = %+v; want the resolution goal moved, and only it", out)
	}
	if w.state(t, res) != "paused" || w.state(t, first) != "running" {
		t.Errorf("resolution %s, first response %s; want paused and running", w.state(t, res), w.state(t, first))
	}
	g := w.fields(t, res)
	elapsed := int64(eventAt(t, inc, waiting).Sub(eventAt(t, inc, 1)) / time.Second)
	if r3Int(t, g["f_elapsed_s"]) != elapsed || g["f_due_at"] != nil {
		t.Errorf("paused: f_elapsed_s %v, f_due_at %v; want %d (up to the incident's event) and unset", g["f_elapsed_s"], g["f_due_at"], elapsed)
	}
	w.goalSets(t, res, itemSeqOf(t, res))
	f := w.fields(t, inc)
	if f["f_due_resolution"] != nil || f["f_due_first_response"] == nil {
		t.Errorf("the waiting incident: f_due_resolution %v, f_due_first_response %v; want unset, and still shown",
			f["f_due_resolution"], f["f_due_first_response"])
	}

	back := w.move(t, w.sam, inc, "in_progress", nil)
	if out := w.reactMove(t, entered(inc, back, "sla_resume")); out.Outcome != itemservice.ReactionApplied || len(out.Each) != 1 {
		t.Fatalf("sla_resume = %+v", out)
	}
	due := dtOf(eventAt(t, inc, back).Add(time.Duration(600-elapsed) * time.Second))
	if got := w.fields(t, res)["f_due_at"]; w.state(t, res) != "running" || got != due {
		t.Errorf("resumed: %s, f_due_at %v; want running, due %s (what was left, from the incident's event)", w.state(t, res), got, due)
	}
	w.goalSets(t, res, itemSeqOf(t, res))
	if got := w.fields(t, inc)["f_due_resolution"]; got != due {
		t.Errorf("the incident's f_due_resolution after the resume = %v, want %s", got, due)
	}
}

func TestIncident_Resolved_StopsBothGoals_ReopenStartsANewResolutionGoalOnly(t *testing.T) {
	w := newSLAWorld(t)
	inc, first, res := w.started(t, "P1", nil)
	w.move(t, w.sam, inc, "in_progress", nil)
	// Resolving says how.
	_, err := w.k.Transition(testCtx(t), itemservice.TransitionRequest{IdempotencyKey: newKey(), Principal: w.sam, ItemID: inc, ToState: "resolved"})
	var rej *itemservice.RejectedError
	if !errors.As(err, &rej) || rej.Code != string(lifecycle.MissingFields) || rej.Metadata["fields"] != "f_resolution" {
		t.Errorf("resolved without a resolution: %v; want MISSING_FIELDS naming f_resolution", err)
	}
	resolved := w.move(t, w.sam, inc, "resolved", map[string]any{"f_resolution": "fixed"})
	// Closing is the timer's only: not the assignee's, the requester's or the admin's.
	for _, p := range []item.Principal{w.sam, w.ana, w.admin} {
		if _, err := w.k.Transition(testCtx(t), itemservice.TransitionRequest{IdempotencyKey: newKey(), Principal: p, ItemID: inc, ToState: "closed"}); !errors.Is(err, itemservice.ErrForbidden) {
			t.Errorf("%s closes the incident by hand: %v; want ErrForbidden", p.Actor, err)
		}
	}
	out := w.reactMove(t, entered(inc, resolved, "sla_stop"))
	if out.Outcome != itemservice.ReactionApplied || len(out.Each) != 2 {
		t.Fatalf("sla_stop = %+v; want both goals stopped", out)
	}
	for _, g := range []uuid.UUID{first, res} {
		if w.state(t, g) != "completed" || w.fields(t, g)["f_outcome"] != "met" {
			t.Errorf("goal %s: %s, outcome %v; want stopped, met", g, w.state(t, g), w.fields(t, g)["f_outcome"])
		}
		w.goalSets(t, g, itemSeqOf(t, g))
	}
	if f := w.fields(t, inc); f["f_due_resolution"] != nil || f["f_due_first_response"] != nil {
		t.Errorf("the resolved incident still shows %v, %v", f["f_due_first_response"], f["f_due_resolution"])
	}

	reopened := w.move(t, w.ana, inc, "in_progress", nil)
	again := w.reactCreate(t, entered(inc, reopened, "sla_restart_resolution"))
	if again.Outcome != itemservice.ReactionApplied || again.Target == res {
		t.Fatalf("sla_restart_resolution = %+v; want a new goal", again)
	}
	g := w.fields(t, again.Target)
	if g["f_metric"] != "resolution" || g["f_started_at"] != dtOf(eventAt(t, inc, reopened)) ||
		g["f_due_at"] != dtOf(eventAt(t, inc, reopened).Add(600*time.Second)) || g["f_elapsed_s"] != nil {
		t.Errorf("the second cycle's goal = %v; want a whole new target from the reopening", g)
	}
	// The first cycle stays as history, as it ended.
	if w.state(t, res) != "completed" || w.fields(t, res)["f_outcome"] != "met" {
		t.Errorf("the first cycle's goal changed: %s, %v", w.state(t, res), w.fields(t, res)["f_outcome"])
	}
	// The incident now contains three goals: one first response, two
	// resolutions; the newest by f_started_at is current.
	kinds := map[string]int{}
	for _, id := range containedGoals(t, inc) {
		kinds[fmt.Sprint(w.fields(t, id)["f_metric"])]++
	}
	if kinds["first_response"] != 1 || kinds["resolution"] != 2 {
		t.Errorf("the reopened incident's goals = %v; want 1 first_response and 2 resolution", kinds)
	}
	// The resume reaction does not concern a reopening.
	if got := w.pendingOn(t, inc); slices.Contains(got, "sla_resume") {
		t.Errorf("pending on the reopened incident = %v; sla_resume is for coming back from waiting only", got)
	}
}

func containedGoals(t *testing.T, inc uuid.UUID) []uuid.UUID {
	t.Helper()
	rows, err := pool.Query(testCtx(t), `select id from items where container_id = $1 and type_id = 'sla_goal' order by id`, inc)
	if err != nil {
		t.Fatalf("read the goals: %v", err)
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, id)
	}
	return out
}

func (w slaWorld) pendingOn(t *testing.T, id uuid.UUID) []string {
	t.Helper()
	se, err := w.k.StateEntry(testCtx(t), id)
	if err != nil {
		t.Fatalf("StateEntry: %v", err)
	}
	var out []string
	for _, p := range se.PendingReactions {
		out = append(out, p.Reaction)
	}
	return out
}

func TestIncident_Canceled_GoalsEndWithoutAnOutcome(t *testing.T) {
	w := newSLAWorld(t)
	inc, first, res := w.started(t, "P1", nil)
	w.move(t, w.sam, inc, "in_progress", nil)
	waiting := w.move(t, w.sam, inc, "waiting_for_customer", nil)
	w.reactMove(t, entered(inc, waiting, "sla_pause"))
	back := w.move(t, w.sam, inc, "in_progress", nil)
	_ = back // the resume is not run: the cancel finds one goal paused, one running
	canceled := w.move(t, w.sam, inc, "canceled", nil)
	out := w.reactMove(t, entered(inc, canceled, "sla_cancel"))
	if out.Outcome != itemservice.ReactionApplied || len(out.Each) != 2 {
		t.Fatalf("sla_cancel = %+v; want both goals, the running one and the paused one", out)
	}
	for _, g := range []uuid.UUID{first, res} {
		f := w.fields(t, g)
		if w.state(t, g) != "canceled" || f["f_outcome"] != nil || f["f_due_at"] != nil {
			t.Errorf("goal %s: %s, outcome %v, due %v; want canceled with neither", g, w.state(t, g), f["f_outcome"], f["f_due_at"])
		}
		w.goalSets(t, g, itemSeqOf(t, g))
	}
	if f := w.fields(t, inc); f["f_due_resolution"] != nil || f["f_due_first_response"] != nil {
		t.Errorf("the canceled incident still shows %v, %v", f["f_due_first_response"], f["f_due_resolution"])
	}
}

// A public comment by someone other than the requester stops the first
// response goal. The requester's own, or a private one, does not.
func TestIncident_FirstResponse_StoppedBySomeoneElsesPublicComment(t *testing.T) {
	w := newSLAWorld(t)
	inc, first, res := w.started(t, "P1", nil)
	comment := func(p item.Principal, public bool) uuid.UUID {
		t.Helper()
		res, err := w.k.Create(testCtx(t), itemservice.CreateRequest{IdempotencyKey: newKey(), Principal: p, TypeID: "comment", TypeVersion: 2,
			ContainerID: &inc, Fields: map[string]any{"f_body": "looking into it", "f_public": public}})
		if err != nil {
			t.Fatalf("comment: %v", err)
		}
		return res.ItemID
	}
	inside := func(c uuid.UUID) itemservice.ReactionRunRequest {
		return itemservice.ReactionRunRequest{SourceID: inc, Reaction: "sla_answered", CauseItem: c, CauseSeq: 1, Hop: 1}
	}
	if out := w.reactMove(t, inside(comment(w.ana, true))); out.Outcome != itemservice.ReactionSkipped || out.Reason != itemservice.SkipNotBy {
		t.Errorf("the requester's own comment = %+v; want skipped, not_by", out)
	}
	if out := w.reactMove(t, inside(comment(w.sam, false))); out.Outcome != itemservice.ReactionSkipped || out.Reason != itemservice.SkipConditionFalse {
		t.Errorf("sam's private note = %+v; want skipped, condition_false", out)
	}
	answer := comment(w.sam, true)
	out := w.reactMove(t, inside(answer))
	if out.Outcome != itemservice.ReactionApplied || len(out.Each) != 1 || out.Each[0].Target != first {
		t.Fatalf("sam's public answer = %+v; want the first response goal stopped, and only it", out)
	}
	f := w.fields(t, first)
	if w.state(t, first) != "completed" || f["f_outcome"] != "met" || w.state(t, res) != "running" {
		t.Errorf("first response %s (%v), resolution %s; want stopped (met) and running", w.state(t, first), f["f_outcome"], w.state(t, res))
	}
	// Its time is the comment's: the answer was given when it was written.
	want := int64(eventAt(t, answer, 1).Sub(eventAt(t, inc, 1)) / time.Second)
	if r3Int(t, f["f_elapsed_s"]) != want {
		t.Errorf("f_elapsed_s = %v, want %d (up to the comment's create)", f["f_elapsed_s"], want)
	}
	// A second answer finds nothing running.
	if out := w.reactMove(t, inside(comment(w.sam, true))); out.Outcome != itemservice.ReactionSkipped || out.Reason != itemservice.SkipNoTarget {
		t.Errorf("a second answer = %+v; want skipped, no_target", out)
	}
}

// --- breach: the mark, and who is told ---

func (w slaWorld) breach(t *testing.T, goal uuid.UUID) itemservice.ActAtResult {
	t.Helper()
	due, ok := typedef.ParseDateTime(w.fields(t, goal)["f_due_at"])
	if !ok {
		t.Fatalf("the goal has no due instant: %v", w.fields(t, goal))
	}
	res, err := w.late.Internal().ActAt(testCtx(t), itemservice.ActAtRequest{
		ItemID: goal, Timer: breachTimer(t, w.k), EnteredSeq: 1, Instant: due, Attribution: item.Attribution{Hop: 2},
	})
	if err != nil {
		t.Fatalf("ActAt: %v", err)
	}
	return res
}

// breachTimer and breachHook read the goal's one acting timer and its hook
// from the Type: their IDs are the package's own business.
func breachTimer(t *testing.T, k *itemservice.Service) string {
	t.Helper()
	ty, err := k.Type(testCtx(t), "sla_goal", 1)
	if err != nil {
		t.Fatalf("the sla_goal Type: %v", err)
	}
	st, _ := ty.State("running")
	if len(st.Acting) != 1 {
		t.Fatalf("running has %d acting timers, want 1", len(st.Acting))
	}
	return st.Acting[0].ID
}

func breachHook(t *testing.T, k *itemservice.Service) string {
	t.Helper()
	ty, _ := k.Type(testCtx(t), "sla_goal", 1)
	st, _ := ty.State("running")
	return st.Acting[0].Hook
}

func (w slaWorld) breachInput(t *testing.T, goal uuid.UUID) (map[string]any, error) {
	t.Helper()
	hook := breachHook(t, w.k)
	return w.k.Internal().HookInput(testCtx(t), itemservice.HookInputRequest{
		Principal: item.Principal{Actor: "hook:sla_goal:" + hook}, ItemID: goal,
		Attribution: item.Attribution{OriginatingHook: "hook:sla_goal@v1:" + hook, CausationID: fmt.Sprintf("%s:1", goal), Hop: 3},
	})
}

func TestGoal_Breach_MarksTheInstant_AndGoesOn(t *testing.T) {
	w := newSLAWorld(t)
	inc, _, res := w.started(t, "P1", map[string]any{"f_assignee": w.sam.Actor})
	due := w.fields(t, res)["f_due_at"]
	// Never before the instant.
	at, _ := typedef.ParseDateTime(due)
	if _, err := w.k.Internal().ActAt(testCtx(t), itemservice.ActAtRequest{ItemID: res, Timer: breachTimer(t, w.k), EnteredSeq: 1, Instant: at,
		Attribution: item.Attribution{Hop: 2}}); !errors.Is(err, itemservice.ErrNotYet) {
		t.Fatalf("ActAt ten minutes early: %v; want ErrNotYet", err)
	}
	got := w.breach(t, res)
	if !got.Applied || !got.RunHook {
		t.Fatalf("the breach = %+v; want a write, and the hook to run", got)
	}
	f := w.fields(t, res)
	if f["f_breached_at"] != due || f["f_due_at"] != due || w.state(t, res) != "running" || f["f_outcome"] != nil {
		t.Errorf("after the breach: %v in %s; want f_breached_at = the due instant %v, still running, no outcome", f, w.state(t, res), due)
	}
	// The incident goes on showing the missed instant: its queue sorts it first.
	if w.fields(t, inc)["f_due_resolution"] != due {
		t.Errorf("the incident's f_due_resolution = %v, want %v still", w.fields(t, inc)["f_due_resolution"], due)
	}
	// Resolved after it: missed, the mark kept.
	w.move(t, w.sam, inc, "in_progress", nil)
	resolved := w.late.Transition
	out, err := resolved(testCtx(t), itemservice.TransitionRequest{IdempotencyKey: newKey(), Principal: w.sam, ItemID: inc, ToState: "resolved",
		Fields: map[string]any{"f_resolution": "fixed"}})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if _, err := w.late.Internal().ReactMove(testCtx(t), entered(inc, out.ItemSeq, "sla_stop")); err != nil {
		t.Fatalf("sla_stop: %v", err)
	}
	f = w.fields(t, res)
	if w.state(t, res) != "completed" || f["f_outcome"] != "missed" || f["f_breached_at"] != due {
		t.Errorf("resolved an hour late: %s, %v; want stopped, missed, the mark kept", w.state(t, res), f)
	}
}

// The breach hook reads container:breach_contact: the incident's assignee
// at the moment the hook runs, not at the goal's creation.
func TestHookInput_ContainerOffer_IsReadWhenTheHookRuns(t *testing.T) {
	w := newSLAWorld(t)
	inc, _, res := w.started(t, "P1", map[string]any{"f_assignee": w.sam.Actor})
	w.breach(t, res)
	in, err := w.breachInput(t, res)
	if err != nil || len(in) != 1 || in["container:breach_contact"] != w.sam.Actor {
		t.Fatalf("the breach hook's input = %v, %v; want only container:breach_contact = %s", in, err, w.sam.Actor)
	}
	// Reassigned since: the new assignee is told.
	if _, err := w.k.Update(testCtx(t), itemservice.UpdateRequest{IdempotencyKey: newKey(), Principal: w.sam, ItemID: inc,
		Fields: map[string]any{"f_assignee": w.eve.Actor}}); err != nil {
		t.Fatalf("reassign: %v", err)
	}
	if in, err := w.breachInput(t, res); err != nil || in["container:breach_contact"] != w.eve.Actor {
		t.Errorf("after the reassignment = %v, %v; want %s", in, err, w.eve.Actor)
	}
	// Reading the offer wrote nothing, on the goal or on the incident.
	if n := itemSeqOf(t, res); n != 2 {
		t.Errorf("the goal is at item_seq %d, want 2 (create, breach mark)", n)
	}
}

func TestHookInput_ContainerOffer_NoRecipient(t *testing.T) {
	w := newSLAWorld(t)

	t.Run("nobody is assigned", func(t *testing.T) {
		_, _, res := w.started(t, "P1", nil)
		if in, err := w.breachInput(t, res); !errors.Is(err, itemservice.ErrNoRecipient) {
			t.Errorf("input = %v, %v; want ErrNoRecipient", in, err)
		}
	})

	// The incident is narrowed to its requester and the admin: the
	// assignee cannot see it, so is told nothing about it.
	t.Run("the assignee cannot see the incident", func(t *testing.T) {
		res, err := w.k.Create(testCtx(t), itemservice.CreateRequest{IdempotencyKey: newKey(), Principal: w.ana, TypeID: "incident", TypeVersion: 1,
			ContainerID: &w.desk, Narrow: &itemservice.Narrow{Readers: []string{w.ana.Actor, w.admin.Actor}},
			Fields: map[string]any{"f_title": "private " + word(), "f_priority": "P1", "f_assignee": w.sam.Actor}})
		if err != nil {
			t.Fatalf("create a narrowed incident: %v", err)
		}
		goal := w.reactCreate(t, entered(res.ItemID, 1, "sla_start_resolution"))
		if goal.Outcome != itemservice.ReactionApplied {
			t.Fatalf("start = %+v", goal)
		}
		if in, err := w.breachInput(t, goal.Target); !errors.Is(err, itemservice.ErrNoRecipient) {
			t.Errorf("input = %v, %v; want ErrNoRecipient: sam cannot see the incident", in, err)
		}
		// The error says nothing of who was not told.
		if _, err := w.breachInput(t, goal.Target); err != nil && (strings.Contains(err.Error(), w.sam.Actor) || strings.Contains(err.Error(), "sam")) {
			t.Errorf("the error names the assignee: %v", err)
		}
	})

	// A goal that is in something whose Type offers no breach_contact (a
	// bare desk here), or in nothing: checked when the hook runs.
	t.Run("the container offers nothing, or there is none", func(t *testing.T) {
		for name, in := range map[string]*uuid.UUID{"in a desk": &w.desk, "in nothing": nil} {
			goal := w.create(t, w.admin, "sla_goal", in, nil)
			if got, err := w.breachInput(t, goal); !errors.Is(err, itemservice.ErrNoRecipient) {
				t.Errorf("%s: input = %v, %v; want ErrNoRecipient", name, got, err)
			}
		}
	})
}

// With nobody to tell, the hook fails once: hook_failed no_recipient,
// naming the timer, under the hook's key; nothing is retried, and the
// breach mark stands.
func TestHookFailed_NoRecipient_IsRecordedOnce(t *testing.T) {
	w := newSLAWorld(t)
	_, _, res := w.started(t, "P1", nil)
	w.breach(t, res)
	hook, timer := breachHook(t, w.k), breachTimer(t, w.k)
	due, _ := typedef.ParseDateTime(w.fields(t, res)["f_due_at"])
	req := itemservice.HookFailedRequest{
		IdempotencyKey: fmt.Sprintf("timer:%s:%s:1:%s:hook", res, timer, dtOf(due)),
		Principal:      item.Principal{Actor: "hook:sla_goal:" + hook}, ItemID: res, Reason: itemservice.ReasonNoRecipient, Timer: timer,
		Attribution: item.Attribution{OriginatingHook: "hook:sla_goal@v1:" + hook, CausationID: fmt.Sprintf("%s:1", res), Hop: 3},
	}
	first, err := w.k.Internal().HookFailed(testCtx(t), req)
	if err != nil {
		t.Fatalf("HookFailed no_recipient: %v", err)
	}
	again, err := w.k.Internal().HookFailed(testCtx(t), req)
	if err != nil || again.ItemSeq != first.ItemSeq {
		t.Errorf("the same failure again = %+v, %v; want the first answer %+v", again, err, first)
	}
	op, payload := event(t, res, first.ItemSeq)
	if op != "hook_failed" || payload["reason"] != "no_recipient" || payload["hook"] != hook || payload["timer"] != timer {
		t.Errorf("the record = %s %v; want hook_failed, no_recipient, naming the hook and its timer", op, payload)
	}
	// No personal data in the payload: no principal anywhere in it.
	for k, v := range payload {
		if s, ok := v.(string); ok && (strings.Contains(s, "user:") || strings.Contains(s, "agent:")) {
			t.Errorf("the record's %s holds a principal: %v", k, v)
		}
	}
	if f := w.fields(t, res); f["f_breached_at"] == nil || w.state(t, res) != "running" {
		t.Errorf("after the failed notification: %v in %s; want the mark kept, still running", f, w.state(t, res))
	}
}

// --- a reaction record names its target, through visibility ---

func TestListEvents_ReactionRecordsNameTheirTarget(t *testing.T) {
	w := newSLAWorld(t)
	inc, first, res := w.started(t, "P1", nil)
	w.move(t, w.sam, inc, "in_progress", nil)
	resolved := w.move(t, w.sam, inc, "resolved", map[string]any{"f_resolution": "fixed"})
	w.reactMove(t, entered(inc, resolved, "sla_stop"))
	// An answer after the end finds no goal running: a record without a
	// target.
	late, err := w.k.Create(testCtx(t), itemservice.CreateRequest{IdempotencyKey: newKey(), Principal: w.sam, TypeID: "comment", TypeVersion: 2,
		ContainerID: &inc, Fields: map[string]any{"f_body": "done", "f_public": true}})
	if err != nil {
		t.Fatalf("comment: %v", err)
	}
	if out := w.reactMove(t, itemservice.ReactionRunRequest{SourceID: inc, Reaction: "sla_answered", CauseItem: late.ItemID, CauseSeq: 1, Hop: 1}); out.Reason != itemservice.SkipNoTarget {
		t.Fatalf("an answer after the end = %+v; want skipped, no_target", out)
	}

	records := func(p item.Principal, id uuid.UUID) map[string][]*itemservice.ItemRef {
		t.Helper()
		got, err := w.k.ListEvents(testCtx(t), itemservice.ListEventsRequest{Principal: p, ItemID: id, Limit: 100})
		if err != nil {
			t.Fatalf("ListEvents as %s: %v", p.Actor, err)
		}
		out := map[string][]*itemservice.ItemRef{}
		for _, ev := range got.Events {
			if ev.Reaction == "" {
				if ev.ReactionTarget != nil {
					t.Errorf("event %d (%s) is no reaction record and names a reaction target", ev.ItemSeq, ev.Op)
				}
				continue
			}
			out[ev.Reaction] = append(out[ev.Reaction], ev.ReactionTarget)
		}
		return out
	}
	is := func(ref *itemservice.ItemRef, id uuid.UUID) bool {
		return ref != nil && !ref.Restricted && ref.ID == id
	}

	// The requester sees her goals: each record names the goal it wrote.
	got := records(w.ana, inc)
	if r := got["incident:sla_start_first_response"]; len(r) != 1 || !is(r[0], first) {
		t.Errorf("the first start's record names %+v, want the goal it created", r)
	}
	if r := got["incident:sla_start_resolution"]; len(r) != 1 || !is(r[0], res) {
		t.Errorf("the second start's record names %+v, want the goal it created", r)
	}
	// One record per child, in the order of their IDs: now told apart.
	stops := got["incident:sla_stop"]
	want := []uuid.UUID{first, res}
	slices.SortFunc(want, func(a, b uuid.UUID) int { return slices.Compare(a[:], b[:]) })
	if len(stops) != 2 || !is(stops[0], want[0]) || !is(stops[1], want[1]) {
		t.Errorf("sla_stop's records name %+v, want the two goals in the order of their IDs", stops)
	}
	// A set reaction's record, on the goal, names the incident it wrote.
	var named bool
	for reaction, refs := range records(w.ana, res) {
		for _, ref := range refs {
			if ref != nil && !is(ref, inc) {
				t.Errorf("the goal's record of %s names %+v, want the incident", reaction, ref)
			}
			named = named || is(ref, inc)
		}
	}
	if !named {
		t.Error("no record on the goal names the incident its set reaction wrote")
	}

	// Someone who sees the incident and not one goal: that target is
	// restricted, with no ID; the other is named as before.
	if _, err := w.k.Update(testCtx(t), itemservice.UpdateRequest{IdempotencyKey: newKey(), Principal: w.admin, ItemID: res,
		Policy: &itemservice.PolicyPatch{Readers: []string{w.admin.Actor}}}); err != nil {
		t.Fatalf("narrow the resolution goal: %v", err)
	}
	got = records(w.ana, inc)
	if r := got["incident:sla_start_resolution"]; len(r) != 1 || r[0] == nil || !r[0].Restricted || r[0].ID != uuid.Nil {
		t.Errorf("the hidden goal's record names %+v; want restricted, without its ID", r)
	}
	if r := got["incident:sla_start_first_response"]; len(r) != 1 || !is(r[0], first) {
		t.Errorf("the visible goal's record names %+v", r)
	}
	// A record that wrote nothing names nothing.
	if r := got["incident:sla_answered"]; len(r) != 1 || r[0] != nil {
		t.Errorf("the record of nothing moved names %+v; want one record, naming nothing", r)
	}
}
