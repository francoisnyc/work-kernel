// End-to-end tests for Jira 2.4b, PR 2: the access request, with Postgres,
// the relay, a real Temporal server and worker. Nothing here calls the
// internal door: decisions are made on the public door, at the same moment,
// and the reactions run where they run in production.
//
//   - Two approvals of a 2-of-3 request at once: the request is approved
//     once, one approval's move lands and the other's is skipped, the
//     leftover approval is closed, the goal is met, one fulfilment task.
//   - An approval and a rejection of a 1-of-2 request at once: the request
//     ends in exactly one of approved or rejected, and the other move is
//     skipped.
//
// Which skip the second move records depends on timing: guard_false when
// it ran before the other decision committed, not_in_from when the request
// had already left pending. Both mean "not this one"; either is accepted.
package workflow_test

import (
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
)

var registerAccessOnce sync.Once

type accessStack struct {
	*incStack
	bob, dana, erin item.Principal
	// twoOfThree: bob, dana and erin, two must approve; oneOfTwo: bob and
	// dana, one must.
	twoOfThree, oneOfTwo uuid.UUID
}

func newAccessStack(t *testing.T) *accessStack {
	t.Helper()
	s := newIncStack(t, 0)
	registerAccessOnce.Do(func() {
		for _, f := range []string{"sla/sla_policy.v2.yaml", "sla/sla_goal.v2.yaml", "fulfilment_task.v1.yaml", "approval.v1.yaml",
			"access_request.v1.yaml", "resource.v1.yaml"} {
			registerTypeFile(t, f)
		}
	})
	a := &accessStack{incStack: s, bob: user("bob"), dana: user("dana"), erin: user("erin")}
	if _, err := pool.Exec(serverCtx(t),
		`insert into principal_roles (namespace_id, principal, role) values ('default', $1, 'access_admin')`, s.admin.Actor); err != nil {
		t.Fatalf("grant access_admin: %v", err)
	}
	cal := s.makeIn(s.admin, "calendar", nil, map[string]any{"f_name": "always", "f_zone": "UTC", "f_week": []any{
		"mon 00:00-24:00", "tue 00:00-24:00", "wed 00:00-24:00", "thu 00:00-24:00", "fri 00:00-24:00", "sat 00:00-24:00", "sun 00:00-24:00"}})
	res, err := s.k.Create(serverCtx(t), itemservice.CreateRequest{IdempotencyKey: "key-" + uuid.NewString(), Principal: s.admin,
		TypeID: "sla_policy", TypeVersion: 2, Fields: map[string]any{"f_name": "a day", "f_approval_s": 86400}})
	if err != nil {
		t.Fatalf("create the policy: %v", err)
	}
	policy := res.ItemID
	resource := func(required int, approvers ...item.Principal) uuid.UUID {
		var who []any
		for _, p := range approvers {
			who = append(who, p.Actor)
		}
		r := s.makeIn(s.admin, "resource", nil, map[string]any{"f_name": "VPN", "f_approvers": who, "f_required": required,
			"f_contact": s.sam.Actor})
		for link, target := range map[string]uuid.UUID{"calendar": cal, "sla_policy": policy} {
			if _, err := s.k.Link(serverCtx(t), itemservice.LinkRequest{IdempotencyKey: "key-" + uuid.NewString(), Principal: s.admin,
				SourceID: r, LinkType: link, TargetID: target}); err != nil {
				t.Fatalf("link the resource's %s: %v", link, err)
			}
		}
		return r
	}
	a.twoOfThree = resource(2, a.bob, a.dana, a.erin)
	a.oneOfTwo = resource(1, a.bob, a.dana)
	return a
}

// submitted is ana's request in a resource, submitted, with its approvals
// and its goal made by the worker.
func (a *accessStack) submitted(resource uuid.UUID, approvers int) uuid.UUID {
	a.t.Helper()
	req := a.makeIn(a.ana, "access_request", &resource, map[string]any{"f_title": "VPN for the audit"})
	a.act(a.ana, req, "pending", nil)
	eventually(a.t, 20*time.Second, "one approval per approver", func() bool { return len(a.children(req, "approval")) == approvers })
	eventually(a.t, 20*time.Second, "the approval goal", func() bool { return goalOf(a.t, req, "approval") != uuid.Nil })
	return req
}

func (a *accessStack) children(req uuid.UUID, typ string) []uuid.UUID {
	a.t.Helper()
	rows, err := pool.Query(serverCtx(a.t), `select id from items where container_id = $1 and type_id = $2 order by id`, req, typ)
	if err != nil {
		a.t.Fatalf("read children: %v", err)
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			a.t.Fatal(err)
		}
		out = append(out, id)
	}
	return out
}

func (a *accessStack) approvalOf(req uuid.UUID, p item.Principal) uuid.UUID {
	a.t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(serverCtx(a.t), `select id from items where container_id = $1 and type_id = 'approval'
		and fields->>'f_approver' = $2`, req, p.Actor).Scan(&id); err != nil {
		a.t.Fatalf("%s's approval: %v", p.Actor, err)
	}
	return id
}

// atOnce makes the decisions together: each starts when all are ready.
func (a *accessStack) atOnce(decisions map[item.Principal]string, req uuid.UUID) {
	a.t.Helper()
	var ready, done sync.WaitGroup
	start := make(chan struct{})
	errs := make(chan error, len(decisions))
	for p, to := range decisions {
		id := a.approvalOf(req, p)
		ready.Add(1)
		done.Add(1)
		go func() {
			defer done.Done()
			ready.Done()
			<-start
			_, err := a.k.Transition(serverCtx(a.t), itemservice.TransitionRequest{IdempotencyKey: "key-" + uuid.NewString(),
				Principal: p, ItemID: id, ToState: to})
			errs <- err
		}()
	}
	ready.Wait()
	close(start)
	done.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			a.t.Fatalf("a decision: %v", err)
		}
	}
}

// outcomes are the records of one reaction on the request's approvals,
// as "applied" or "skipped <reason>", sorted.
func (a *accessStack) outcomes(req uuid.UUID, reaction string) []string {
	a.t.Helper()
	rows, err := pool.Query(serverCtx(a.t), `select e.op, coalesce(e.payload->>'reason', '') from item_events e
		join items i on i.id = e.item_id
		where i.container_id = $1 and i.type_id = 'approval' and e.payload->>'reaction' = $2
		  and e.op in ('reaction_applied', 'reaction_skipped', 'reaction_failed')`, req, reaction)
	if err != nil {
		a.t.Fatalf("read the records: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var op, reason string
		if err := rows.Scan(&op, &reason); err != nil {
			a.t.Fatal(err)
		}
		switch op {
		case "reaction_applied":
			out = append(out, "applied")
		case "reaction_skipped":
			out = append(out, "skipped "+reason)
		default:
			out = append(out, "failed "+reason)
		}
	}
	slices.Sort(out)
	return out
}

// leftPending counts the request's moves out of pending.
func (a *accessStack) leftPending(req uuid.UUID) int {
	a.t.Helper()
	var n int
	if err := pool.QueryRow(serverCtx(a.t), `select count(*) from item_events where item_id = $1 and op = 'transition'
		and payload->>'from' = 'pending'`, req).Scan(&n); err != nil {
		a.t.Fatal(err)
	}
	return n
}

func skipped(o string) bool { return o == "skipped guard_false" || o == "skipped not_in_from" }

func TestE2E_Access_TwoApprovalsAtOnce_TheRequestMovesOnce(t *testing.T) {
	a := newAccessStack(t)
	for round := range 3 {
		req := a.submitted(a.twoOfThree, 3)
		a.atOnce(map[item.Principal]string{a.bob: "approved", a.dana: "approved"}, req)
		eventually(t, 20*time.Second, "the request approved", a.is(req, "approved"))
		eventually(t, 20*time.Second, "both approvals' moves recorded", func() bool { return len(a.outcomes(req, "approval:move_approved")) == 2 })
		if o := a.outcomes(req, "approval:move_approved"); o[0] != "applied" || !skipped(o[1]) {
			t.Errorf("round %d: move_approved = %v; want one applied, one skipped", round, o)
		}
		eventually(t, 20*time.Second, "erin's approval closed", a.is(a.approvalOf(req, a.erin), "canceled"))
		goal := goalOf(t, req, "approval")
		eventually(t, 20*time.Second, "the goal completed", a.is(goal, "completed"))
		if out := a.f(goal, "f_outcome"); out != "met" {
			t.Errorf("round %d: the goal's outcome = %v; want met", round, out)
		}
		eventually(t, 20*time.Second, "the fulfilment task", func() bool { return len(a.children(req, "fulfilment_task")) == 1 })
		staysFor(t, time.Second, "one fulfilment task, one move out of pending", func() bool {
			return len(a.children(req, "fulfilment_task")) == 1 && a.leftPending(req) == 1
		})
	}
}

func TestE2E_Access_AnApprovalAndARejectionAtOnce_OneOutcome(t *testing.T) {
	a := newAccessStack(t)
	for round := range 3 {
		req := a.submitted(a.oneOfTwo, 2)
		a.atOnce(map[item.Principal]string{a.bob: "approved", a.dana: "rejected"}, req)
		eventually(t, 20*time.Second, "the request decided", func() bool {
			st, _ := a.state(req)
			return st == "approved" || st == "rejected"
		})
		eventually(t, 20*time.Second, "both moves recorded", func() bool {
			return len(a.outcomes(req, "approval:move_approved"))+len(a.outcomes(req, "approval:move_rejected")) == 2
		})
		st, _ := a.state(req)
		won, lost := "approval:move_approved", "approval:move_rejected"
		if st == "rejected" {
			won, lost = lost, won
		}
		if w, l := a.outcomes(req, won), a.outcomes(req, lost); !slices.Equal(w, []string{"applied"}) || len(l) != 1 || !skipped(l[0]) {
			t.Errorf("round %d (%s): %s %v, %s %v; want one applied, the other skipped", round, st, won, w, lost, l)
		}
		goal := goalOf(t, req, "approval")
		eventually(t, 20*time.Second, "the goal completed", a.is(goal, "completed"))
		tasks := 0
		if st == "approved" {
			tasks = 1
		}
		eventually(t, 20*time.Second, "the fulfilment tasks", func() bool { return len(a.children(req, "fulfilment_task")) == tasks })
		staysFor(t, time.Second, "one move out of pending", func() bool { return a.leftPending(req) == 1 })
	}
}
