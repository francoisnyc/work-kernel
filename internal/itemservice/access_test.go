// Tests for Jira 2.4b, PR 2: the access request, on the repository's Type
// files (resource@1, access_request@1, approval@1, fulfilment_task@1, with
// sla_goal@2 and sla_policy@2). The design: docs/proposals/
// 2026-11-21-jira-2-4b-phase-a.md, Part 1 sections 1 to 9 with Part 2's
// answers, and the PR 2 Phase A answers (an invariant refuses a submit
// without a quorum; sla_answered stops the goal on a rejection).
//
// Reactions are run by hand through the internal door, in the order the
// workflow would run them.
package itemservice_test

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
)

type accessWorld struct {
	k                                   *itemservice.Service
	ns                                  string
	lea, ana, bob, dana, erin, sam, kai item.Principal
	resource, cal, policy               uuid.UUID
	resourceName                        string
}

// newAccessWorld: lea (access_admin, sla_admin) sets up a resource whose
// approvers are bob, dana and erin, two of whom must approve, with sam as
// its contact, on an always-open calendar and an approval target of an
// hour. kai is a desk agent.
func newAccessWorld(t *testing.T) accessWorld {
	t.Helper()
	k := itemservice.New(pool)
	for _, f := range []string{"sla/calendar.v1.yaml", "sla/sla_policy.v2.yaml", "sla/sla_goal.v2.yaml",
		"fulfilment_task.v1.yaml", "approval.v1.yaml", "access_request.v1.yaml", "resource.v1.yaml", "desk.v3.yaml"} {
		registerFromFile(t, k, f)
	}
	ns := newNamespace(t, k)
	w := accessWorld{k: k, ns: ns, lea: in(person("lea"), ns), ana: in(person("ana"), ns), bob: in(person("bob"), ns),
		dana: in(person("dana"), ns), erin: in(person("erin"), ns), sam: in(person("sam"), ns), kai: in(person("kai"), ns)}
	grantRoleIn(t, ns, w.lea, "access_admin")
	grantRoleIn(t, ns, w.lea, "sla_admin")
	grantRoleIn(t, ns, w.kai, "desk_agent")
	w.cal = w.create(t, w.lea, "calendar", nil, map[string]any{"f_name": "always", "f_week": alwaysOpen, "f_zone": "UTC"})
	pf := policyFields()
	pf["f_approval_s"] = 3600
	w.policy = w.create2(t, w.lea, "sla_policy", 2, nil, pf)
	w.resourceName = "VPN " + word()
	w.resource = w.create(t, w.lea, "resource", nil, map[string]any{"f_name": w.resourceName,
		"f_approvers": []any{w.bob.Actor, w.dana.Actor, w.erin.Actor}, "f_required": 2, "f_contact": w.sam.Actor})
	for link, target := range map[string]uuid.UUID{"calendar": w.cal, "sla_policy": w.policy} {
		if _, err := k.Link(testCtx(t), itemservice.LinkRequest{IdempotencyKey: newKey(), Principal: w.lea, SourceID: w.resource,
			LinkType: link, TargetID: target}); err != nil {
			t.Fatalf("link the resource's %s: %v", link, err)
		}
	}
	return w
}

func (w accessWorld) create(t *testing.T, p item.Principal, typ string, in *uuid.UUID, fields map[string]any) uuid.UUID {
	t.Helper()
	return w.create2(t, p, typ, 1, in, fields)
}

func (w accessWorld) create2(t *testing.T, p item.Principal, typ string, version int, in *uuid.UUID, fields map[string]any) uuid.UUID {
	t.Helper()
	res, err := w.k.Create(testCtx(t), itemservice.CreateRequest{IdempotencyKey: newKey(), Principal: p, TypeID: typ,
		TypeVersion: version, ContainerID: in, Fields: fields})
	if err != nil {
		t.Fatalf("%s creates %s@%d: %v", p.Actor, typ, version, err)
	}
	return res.ItemID
}

func (w accessWorld) move(t *testing.T, p item.Principal, id uuid.UUID, to string, fields map[string]any) (itemservice.TransitionResult, error) {
	t.Helper()
	return w.k.Transition(testCtx(t), itemservice.TransitionRequest{IdempotencyKey: newKey(), Principal: p, ItemID: id,
		ToState: to, Fields: fields})
}

func (w accessWorld) mustMove(t *testing.T, p item.Principal, id uuid.UUID, to string, fields map[string]any) int64 {
	t.Helper()
	res, err := w.move(t, p, id, to, fields)
	if err != nil {
		t.Fatalf("%s moves to %s: %v", p.Actor, to, err)
	}
	return res.ItemSeq
}

func (w accessWorld) get(t *testing.T, id uuid.UUID) item.Item {
	t.Helper()
	it, err := w.k.Get(testCtx(t), w.lea, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	return it
}

func (w accessWorld) update(t *testing.T, p item.Principal, id uuid.UUID, req itemservice.UpdateRequest) error {
	t.Helper()
	req.IdempotencyKey, req.Principal, req.ItemID = newKey(), p, id
	_, err := w.k.Update(testCtx(t), req)
	return err
}

// react runs one reaction for a trigger and wants it to finish.
func (w accessWorld) react(t *testing.T, kind string, req itemservice.ReactionRunRequest) itemservice.ReactionOutcome {
	t.Helper()
	in := w.k.Internal()
	run := map[string]func() (itemservice.ReactionOutcome, error){
		"move":   func() (itemservice.ReactionOutcome, error) { return in.ReactMove(testCtx(t), req) },
		"create": func() (itemservice.ReactionOutcome, error) { return in.ReactCreate(testCtx(t), req) },
		"set":    func() (itemservice.ReactionOutcome, error) { return in.ReactSet(testCtx(t), req) },
	}[kind]
	out, err := run()
	if err != nil {
		t.Fatalf("%s %s: %v", kind, req.Reaction, err)
	}
	return out
}

// submitted is ana's request in the resource, submitted, its approvals and
// its goal made: it returns the request, its approvals by approver, and
// the goal.
func (w accessWorld) submitted(t *testing.T) (uuid.UUID, map[string]uuid.UUID, uuid.UUID) {
	t.Helper()
	req := w.create(t, w.ana, "access_request", &w.resource, map[string]any{"f_title": "VPN for the audit " + word()})
	seq := w.mustMove(t, w.ana, req, "pending", nil)
	if out := w.react(t, "create", entered(req, seq, "request_approvals")); out.Outcome != itemservice.ReactionApplied {
		t.Fatalf("request_approvals: %+v", out)
	}
	goal := w.react(t, "create", entered(req, seq, "sla_start_approval"))
	if goal.Outcome != itemservice.ReactionApplied {
		t.Fatalf("sla_start_approval: %+v", goal)
	}
	return req, w.approvals(t, req), goal.Target
}

func (w accessWorld) approvals(t *testing.T, req uuid.UUID) map[string]uuid.UUID {
	t.Helper()
	rows, err := pool.Query(testCtx(t), `select id, fields->>'f_approver' from items where container_id = $1 and type_id = 'approval'`, req)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]uuid.UUID{}
	for rows.Next() {
		var id uuid.UUID
		var who string
		if err := rows.Scan(&id, &who); err != nil {
			t.Fatal(err)
		}
		out[who] = id
	}
	return out
}

func (w accessWorld) childrenOf(t *testing.T, req uuid.UUID, typ string) []uuid.UUID {
	t.Helper()
	rows, err := pool.Query(testCtx(t), `select id from items where container_id = $1 and type_id = $2 order by id`, req, typ)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		out = append(out, id)
	}
	return out
}

// decide makes one decision and runs the approval's move at its request,
// as the workflow would; it returns that reaction's outcome.
func (w accessWorld) decide(t *testing.T, p item.Principal, approval uuid.UUID, to string) itemservice.ReactionOutcome {
	t.Helper()
	seq := w.mustMove(t, p, approval, to, nil)
	return w.react(t, "move", entered(approval, seq, map[string]string{"approved": "move_approved", "rejected": "move_rejected"}[to]))
}

// lastMove is the event seq of the Item's last transition.
func lastMove(t *testing.T, id uuid.UUID) int64 {
	t.Helper()
	var seq int64
	if err := pool.QueryRow(testCtx(t), `select max(item_seq) from item_events where item_id = $1 and op = 'transition'`, id).Scan(&seq); err != nil {
		t.Fatal(err)
	}
	return seq
}

func invariantOf(err error) string {
	var rej *itemservice.RejectedError
	if errors.As(err, &rej) && rej.Code == "invariant_failed" {
		return rej.Metadata["invariant"]
	}
	return ""
}

func placementRefused(err error) bool {
	var pe *itemservice.PlacementNotWritableError
	return errors.As(err, &pe) && errors.Is(err, itemservice.ErrForbidden)
}

func notWritable(err error) bool {
	var fe *itemservice.FieldNotWritableError
	return errors.As(err, &fe)
}

// ============================================================ submit

func TestAccess_SubmitCopiesTheResource_OneApprovalPerApprover_AndAGoal(t *testing.T) {
	w := newAccessWorld(t)
	req, approvals, goal := w.submitted(t)
	r := w.get(t, req)
	if r.State != "pending" || r3Int(t, r.Fields["f_required"]) != 2 || r.Fields["f_desk_contact"] != w.sam.Actor || r.Fields["f_submitted_at"] == nil {
		t.Errorf("the request after submit: %s %v", r.State, r.Fields)
	}
	if got := r3Strings(t, r.Fields["f_approvers"]); !slices.Equal(got, sortedStrings(w.bob.Actor, w.dana.Actor, w.erin.Actor)) {
		t.Errorf("f_approvers = %v; want the resource's three", got)
	}
	if len(approvals) != 3 {
		t.Fatalf("%d approvals, want one per approver", len(approvals))
	}
	for _, who := range []item.Principal{w.bob, w.dana, w.erin} {
		a := w.get(t, approvals[who.Actor])
		if a.State != "pending" || a.Fields["f_title"] != r.Fields["f_title"] || a.CreatedBy != "reaction:access_request:request_approvals" {
			t.Errorf("%s's approval: %s %v by %s", who.Actor, a.State, a.Fields, a.CreatedBy)
		}
	}
	// The approval goal: an hour of working time from the submit.
	g := w.get(t, goal)
	submitted := eventAt(t, req, lastMove(t, req))
	if g.Fields["f_metric"] != "approval" || r3Int(t, g.Fields["f_target_s"]) != 3600 || g.Fields["f_due_at"] != dtOf(submitted.Add(time.Hour)) {
		t.Errorf("the goal = %v; want approval, 3600 s, due an hour after the submit", g.Fields)
	}
	// The request keeps its copy when the resource changes.
	if err := w.update(t, w.lea, w.resource, itemservice.UpdateRequest{Fields: map[string]any{"f_required": 3}}); err != nil {
		t.Fatal(err)
	}
	if r3Int(t, w.get(t, req).Fields["f_required"]) != 2 {
		t.Error("the request's f_required followed the resource")
	}
}

// A submit is refused when nothing gives it a quorum: outside a resource
// the copies are unset, and the invariant (judged after them) refuses it.
func TestAccess_SubmitWithoutAQuorum_IsRefused(t *testing.T) {
	w := newAccessWorld(t)
	desk := w.create2(t, w.lea, "desk", 3, nil, map[string]any{"f_key": "AR", "f_name": "access " + word()})
	for name, in := range map[string]*uuid.UUID{"at the top level": nil, "in a desk": &desk} {
		req := w.create(t, w.ana, "access_request", in, map[string]any{"f_title": "x"})
		if _, err := w.move(t, w.ana, req, "pending", nil); invariantOf(err) != "submit_without_quorum" {
			t.Errorf("%s: %v; want INVARIANT_FAILED submit_without_quorum", name, err)
		}
	}
	// A resource cannot lack one: its own invariant.
	for name, f := range map[string]map[string]any{
		"no approvers":         {"f_name": "x", "f_required": 1},
		"more than it has":     {"f_name": "x", "f_approvers": []any{w.bob.Actor}, "f_required": 2},
		"no approval required": {"f_name": "x", "f_approvers": []any{w.bob.Actor}, "f_required": 0},
	} {
		_, err := w.k.Create(testCtx(t), itemservice.CreateRequest{IdempotencyKey: newKey(), Principal: w.lea, TypeID: "resource",
			TypeVersion: 1, Fields: f})
		if invariantOf(err) != "quorum_invalid" {
			t.Errorf("a resource with %s: %v; want quorum_invalid", name, err)
		}
	}
}

// ============================================================ N of M

func TestAccess_TwoOfThree_TheRequestMovesOnce(t *testing.T) {
	w := newAccessWorld(t)
	req, approvals, goal := w.submitted(t)
	if o := w.decide(t, w.bob, approvals[w.bob.Actor], "approved"); o.Outcome != itemservice.ReactionSkipped || o.Reason != itemservice.SkipGuardFalse {
		t.Errorf("the first approval's move = %+v; want skipped guard_false", o)
	}
	if st := w.get(t, req).State; st != "pending" {
		t.Fatalf("after one of two: %s", st)
	}
	if o := w.decide(t, w.dana, approvals[w.dana.Actor], "approved"); o.Outcome != itemservice.ReactionApplied || o.Target != req {
		t.Errorf("the second approval's move = %+v; want the request moved", o)
	}
	if st := w.get(t, req).State; st != "approved" {
		t.Fatalf("after two of two: %s", st)
	}
	// The window before the leftover approvals are closed: GUARD_FAILED.
	if _, err := w.move(t, w.erin, approvals[w.erin.Actor], "rejected", nil); rejectedCode(err) != "guard_failed" {
		t.Errorf("erin decides before the close: %v; want guard_failed", err)
	}
	seq := lastMove(t, req)
	w.react(t, "move", entered(req, seq, "close_on_approved"))
	if st := w.get(t, approvals[w.erin.Actor]).State; st != "canceled" {
		t.Errorf("erin's approval after the close: %s; want canceled", st)
	}
	// After it: NO_TRANSITION.
	if _, err := w.move(t, w.erin, approvals[w.erin.Actor], "approved", nil); rejectedCode(err) != "no_transition" {
		t.Errorf("erin decides after the close: %v; want no_transition", err)
	}
	// Moved once, by the second approval's reaction.
	var moves []itemservice.Event
	for _, e := range allEvents(t, w.k, w.lea, req) {
		if e.Op == "transition" && e.To == "approved" {
			moves = append(moves, e)
		}
	}
	if len(moves) != 1 || moves[0].Actor != "reaction:approval:move_approved" {
		t.Errorf("the request's moves to approved: %+v", moves)
	}
	// The goal: a decision in time is met.
	if o := w.react(t, "move", entered(req, seq, "sla_stop")); o.Outcome != itemservice.ReactionApplied {
		t.Errorf("sla_stop = %+v", o)
	}
	if g := w.get(t, goal); g.State != "completed" || g.Fields["f_outcome"] != "met" {
		t.Errorf("the goal: %s %v; want completed, met", g.State, g.Fields["f_outcome"])
	}
	// The fulfilment task, for the resource.
	if o := w.react(t, "create", entered(req, seq, "fulfil")); o.Outcome != itemservice.ReactionApplied {
		t.Fatalf("fulfil = %+v", o)
	}
	tasks := w.childrenOf(t, req, "fulfilment_task")
	if len(tasks) != 1 {
		t.Fatalf("%d fulfilment tasks, want one", len(tasks))
	}
	if task := w.get(t, tasks[0]); task.State != "open" || task.Fields["f_resource"] != w.resourceName {
		t.Errorf("the task: %s %v", task.State, task.Fields)
	}
}

func TestAccess_ARejection_RejectsTheRequest_AndClosesTheRest(t *testing.T) {
	w := newAccessWorld(t)
	req, approvals, goal := w.submitted(t)
	w.decide(t, w.bob, approvals[w.bob.Actor], "approved")
	if o := w.decide(t, w.dana, approvals[w.dana.Actor], "rejected"); o.Outcome != itemservice.ReactionApplied || o.Target != req {
		t.Errorf("the rejection's move = %+v", o)
	}
	if st := w.get(t, req).State; st != "rejected" {
		t.Fatalf("the request: %s; want rejected", st)
	}
	seq := lastMove(t, req)
	w.react(t, "move", entered(req, seq, "close_on_rejected"))
	if st := w.get(t, approvals[w.erin.Actor]).State; st != "canceled" {
		t.Errorf("erin's approval: %s; want canceled", st)
	}
	if st := w.get(t, approvals[w.bob.Actor]).State; st != "approved" {
		t.Errorf("bob's decision stays: %s", st)
	}
	// A rejection is a decision: the goal completes, as for an approval.
	if o := w.react(t, "move", entered(req, seq, "sla_answered")); o.Outcome != itemservice.ReactionApplied {
		t.Errorf("sla_answered = %+v", o)
	}
	if g := w.get(t, goal); g.State != "completed" || g.Fields["f_outcome"] != "met" {
		t.Errorf("the goal: %s %v; want completed, met", g.State, g.Fields["f_outcome"])
	}
	if n := len(w.childrenOf(t, req, "fulfilment_task")); n != 0 {
		t.Errorf("%d fulfilment tasks for a rejected request", n)
	}
}

func TestAccess_Withdraw(t *testing.T) {
	w := newAccessWorld(t)
	req, approvals, goal := w.submitted(t)
	if _, err := w.move(t, w.bob, req, "withdrawn", nil); !errors.Is(err, itemservice.ErrForbidden) {
		t.Errorf("bob withdraws ana's request: %v; want ErrForbidden", err)
	}
	seq := w.mustMove(t, w.ana, req, "withdrawn", nil)
	w.react(t, "move", entered(req, seq, "close_on_withdrawn"))
	for who, a := range approvals {
		if st := w.get(t, a).State; st != "canceled" {
			t.Errorf("%s's approval: %s; want canceled", who, st)
		}
	}
	w.react(t, "move", entered(req, seq, "sla_cancel"))
	if st := w.get(t, goal).State; st != "canceled" {
		t.Errorf("the goal: %s; want canceled", st)
	}
	// A draft is withdrawn too.
	draft := w.create(t, w.ana, "access_request", &w.resource, map[string]any{"f_title": "y"})
	w.mustMove(t, w.ana, draft, "withdrawn", nil)
}

// ============================================================ hand-over

func TestAccess_HandOver(t *testing.T) {
	w := newAccessWorld(t)
	_, approvals, _ := w.submitted(t)
	hand := func(p item.Principal, approval uuid.UUID, to string) error {
		_, err := w.move(t, p, approval, "pending", map[string]any{"f_delegate": to})
		return err
	}
	bobs := approvals[w.bob.Actor]
	if err := hand(w.bob, bobs, w.bob.Actor); rejectedCode(err) != "guard_failed" {
		t.Errorf("to himself: %v; want guard_failed", err)
	}
	if err := hand(w.bob, bobs, w.dana.Actor); rejectedCode(err) != itemservice.CodeNotUnique {
		t.Errorf("to dana, who already approves: %v; want not_unique", err)
	}
	if err := hand(w.dana, bobs, w.sam.Actor); !errors.Is(err, itemservice.ErrForbidden) {
		t.Errorf("dana hands over bob's: %v; want ErrForbidden", err)
	}
	if err := hand(w.bob, bobs, w.sam.Actor); err != nil {
		t.Fatalf("bob hands over to sam: %v", err)
	}
	if a := w.get(t, bobs); a.Fields["f_approver"] != w.sam.Actor || a.State != "pending" {
		t.Errorf("after the hand-over: %s %v", a.State, a.Fields)
	}
	if _, err := w.move(t, w.bob, bobs, "approved", nil); !errors.Is(err, itemservice.ErrForbidden) {
		t.Errorf("bob decides after handing over: %v; want ErrForbidden", err)
	}
	// Who handed over to whom: one move on the approval, by bob.
	var ho *itemservice.Event
	for _, e := range allEvents(t, w.k, w.lea, bobs) {
		if e.Op == "transition" && e.From == "pending" && e.To == "pending" {
			ho = &e
		}
	}
	if ho == nil || ho.Actor != w.bob.Actor || ho.Values["f_approver"] != w.sam.Actor {
		t.Errorf("the hand-over event: %+v", ho)
	}
	// access_admin hands over someone else's.
	if err := hand(w.lea, approvals[w.erin.Actor], w.kai.Actor); err != nil {
		t.Errorf("lea (access_admin) hands over erin's: %v", err)
	}
	// Someone else filling in a delegate by update is refused.
	if err := w.update(t, w.dana, approvals[w.dana.Actor], itemservice.UpdateRequest{Fields: map[string]any{"f_delegate": w.erin.Actor}}); invariantOf(err) != "delegate_not_approver" {
		t.Errorf("a delegate set by update: %v; want delegate_not_approver", err)
	}
	// The documented weakness: a hand-over to the requester is accepted,
	// and she cannot decide it (except: [container_creator]).
	if err := hand(w.dana, approvals[w.dana.Actor], w.ana.Actor); err != nil {
		t.Fatalf("dana hands over to ana: %v", err)
	}
	if _, err := w.move(t, w.ana, approvals[w.dana.Actor], "approved", nil); !errors.Is(err, itemservice.ErrForbidden) {
		t.Errorf("ana decides an approval on her own request: %v; want ErrForbidden", err)
	}
}

// ============================================================ the requester

// ana is the requester, and one of the approvers: every way she could
// approve her own request, or change who approves it, is refused.
func TestAccess_TheRequesterCannotChangeWhoApproves(t *testing.T) {
	w := newAccessWorld(t)
	if err := w.update(t, w.lea, w.resource, itemservice.UpdateRequest{Fields: map[string]any{
		"f_approvers": []any{w.ana.Actor, w.bob.Actor, w.dana.Actor}}}); err != nil {
		t.Fatal(err)
	}
	req, approvals, _ := w.submitted(t)
	anas := approvals[w.ana.Actor]
	if _, err := w.move(t, w.ana, anas, "approved", nil); !errors.Is(err, itemservice.ErrForbidden) {
		t.Errorf("ana approves her own request: %v; want ErrForbidden", err)
	}
	// The copies are the kernel's.
	for _, f := range []map[string]any{{"f_required": 1}, {"f_approvers": []any{w.ana.Actor}}} {
		if err := w.update(t, w.ana, req, itemservice.UpdateRequest{Fields: f}); !notWritable(err) {
			t.Errorf("ana writes %v on her request: %v; want FIELD_NOT_WRITABLE", f, err)
		}
		_, err := w.k.Create(testCtx(t), itemservice.CreateRequest{IdempotencyKey: newKey(), Principal: w.ana, TypeID: "access_request",
			TypeVersion: 1, ContainerID: &w.resource, Fields: f})
		if !notWritable(err) {
			t.Errorf("ana creates a request with %v: %v; want FIELD_NOT_WRITABLE", f, err)
		}
	}
	// The resource is access_admin's.
	if err := w.update(t, w.ana, w.resource, itemservice.UpdateRequest{Fields: map[string]any{"f_required": 1}}); !notWritable(err) {
		t.Errorf("ana writes the resource: %v; want FIELD_NOT_WRITABLE", err)
	}
	// An approval made by hand: no approver can be set, and none is refused.
	if _, err := w.k.Create(testCtx(t), itemservice.CreateRequest{IdempotencyKey: newKey(), Principal: w.ana, TypeID: "approval",
		TypeVersion: 1, ContainerID: &req, Fields: map[string]any{"f_approver": w.ana.Actor}}); !notWritable(err) {
		t.Errorf("ana creates an approval naming herself: %v; want FIELD_NOT_WRITABLE", err)
	}
	if _, err := w.k.Create(testCtx(t), itemservice.CreateRequest{IdempotencyKey: newKey(), Principal: w.ana, TypeID: "approval",
		TypeVersion: 1, ContainerID: &req}); invariantOf(err) != "approver_missing" {
		t.Errorf("ana creates a bare approval: %v; want approver_missing", err)
	}
	// Nobody moves or narrows a request or an approval; only access_admin a resource.
	other := w.create(t, w.ana, "access_request", &w.resource, map[string]any{"f_title": "other"})
	for name, c := range map[string]struct {
		id  uuid.UUID
		req itemservice.UpdateRequest
	}{
		"moves an approval into another request": {approvals[w.bob.Actor], itemservice.UpdateRequest{Move: &itemservice.Move{To: &other}}},
		"narrows an approval":                    {approvals[w.bob.Actor], itemservice.UpdateRequest{Policy: &itemservice.PolicyPatch{Readers: []string{w.ana.Actor}}}},
		"narrows her request":                    {req, itemservice.UpdateRequest{Policy: &itemservice.PolicyPatch{Readers: []string{w.ana.Actor}}}},
		"moves her request out":                  {req, itemservice.UpdateRequest{Move: &itemservice.Move{To: nil, ConfirmWiden: true}}},
		"narrows the resource":                   {w.resource, itemservice.UpdateRequest{Policy: &itemservice.PolicyPatch{Readers: []string{w.ana.Actor}}}},
	} {
		if err := w.update(t, w.ana, c.id, c.req); !placementRefused(err) {
			t.Errorf("ana %s: %v; want PlacementNotWritableError", name, err)
		}
	}
	// Even lea, access_admin, places no request or approval.
	if err := w.update(t, w.lea, req, itemservice.UpdateRequest{Policy: &itemservice.PolicyPatch{Readers: []string{w.lea.Actor}}}); !placementRefused(err) {
		t.Errorf("lea narrows a request: %v; want PlacementNotWritableError", err)
	}
}

// ============================================================ fulfilment

func TestAccess_FulfilmentTask(t *testing.T) {
	w := newAccessWorld(t)
	req, approvals, _ := w.submitted(t)
	w.decide(t, w.bob, approvals[w.bob.Actor], "approved")
	w.decide(t, w.dana, approvals[w.dana.Actor], "approved")
	w.react(t, "create", entered(req, lastMove(t, req), "fulfil"))
	task := w.childrenOf(t, req, "fulfilment_task")[0]
	if _, err := w.move(t, w.ana, task, "in_progress", nil); !errors.Is(err, itemservice.ErrForbidden) {
		t.Errorf("ana starts the task: %v; want ErrForbidden", err)
	}
	w.mustMove(t, w.kai, task, "in_progress", nil)
	w.mustMove(t, w.kai, task, "done", nil)
	// Made by hand, a task names no resource: refused.
	if _, err := w.k.Create(testCtx(t), itemservice.CreateRequest{IdempotencyKey: newKey(), Principal: w.kai, TypeID: "fulfilment_task",
		TypeVersion: 1, ContainerID: &req}); invariantOf(err) != "resource_missing" {
		t.Errorf("a task made by hand: %v; want resource_missing", err)
	}
}
