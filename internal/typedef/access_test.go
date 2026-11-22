// Jira 2.4b, PR 2: the access request's Type files load together with the
// repository's, and declare what the design says.
package typedef_test

import (
	"slices"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/typedef"
)

func TestAccessTypes_Files(t *testing.T) {
	types, err := typedef.ParseDir("../../types")
	if err != nil {
		t.Fatalf("ParseDir: %v", err)
	}
	find := func(id string) *typedef.Type {
		for _, ty := range types {
			if ty.ID == id && ty.Version == 1 {
				return ty
			}
		}
		t.Fatalf("types/ has no %s@1", id)
		return nil
	}
	invariant := func(ty *typedef.Type, id string) bool {
		return slices.ContainsFunc(ty.Invariants, func(i typedef.Invariant) bool { return i.ID == id })
	}
	reaction := func(ty *typedef.Type, id string) typedef.Reaction {
		r, ok := ty.Reaction(id)
		if !ok {
			t.Errorf("%s has no reaction %s", ty.ID, id)
		}
		return r
	}

	// resource@1: approvers, quorum and contact are access_admin's.
	res := find("resource")
	if !slices.Equal(res.PlacementWritableBy, []string{"role:access_admin"}) || !invariant(res, "quorum_invalid") {
		t.Errorf("resource: placement %v, invariants %v", res.PlacementWritableBy, res.Invariants)
	}
	for _, id := range []string{"f_approvers", "f_required", "f_contact"} {
		if f, ok := res.Field(id); !ok || !slices.Equal(f.WritableByRoles, []string{"access_admin"}) {
			t.Errorf("resource %s = %+v; want written by access_admin", id, f)
		}
	}
	for _, l := range []string{"calendar", "sla_policy"} {
		if lt, ok := res.Link(l); !ok || !slices.Equal(lt.WritableByRoles, []string{"sla_admin"}) {
			t.Errorf("resource link %s = %+v; want written by sla_admin", l, lt)
		}
	}

	// access_request@1.
	req := find("access_request")
	if !slices.Equal(req.PlacementWritableBy, []string{"kernel"}) || !invariant(req, "submit_without_quorum") {
		t.Errorf("access_request: placement %v, invariants %v", req.PlacementWritableBy, req.Invariants)
	}
	submit, ok := req.Transition("draft", "pending")
	if !ok || submit.Sets["f_required"] != "container.f_required" || submit.Sets["f_approvers"] != "container.f_approvers" ||
		submit.Sets["f_desk_contact"] != "container.f_contact" || submit.Sets["f_submitted_at"] != "cause.at" {
		t.Errorf("submit = %+v", submit)
	}
	for _, id := range []string{"f_required", "f_approvers", "f_desk_contact", "f_submitted_at", "f_due_approval"} {
		if f, _ := req.Field(id); f.WritableBy != typedef.WritableByKernel {
			t.Errorf("access_request %s is not the kernel's", id)
		}
	}
	if r := reaction(req, "request_approvals"); r.ForEach != "f_approvers" || r.Type != "approval" || r.Fields["f_approver"] != "each" {
		t.Errorf("request_approvals = %+v", r)
	}
	for id, state := range map[string]string{"sla_stop": "approved", "sla_answered": "rejected", "sla_cancel": "withdrawn",
		"close_on_approved": "approved", "close_on_rejected": "rejected", "close_on_withdrawn": "withdrawn"} {
		if r := reaction(req, id); r.State != state {
			t.Errorf("%s runs on entering %q; want %s", id, r.State, state)
		}
	}
	if r := reaction(req, "fulfil"); r.State != "approved" || r.Type != "fulfilment_task" || r.Fields["f_resource"] != "container.f_name" {
		t.Errorf("fulfil = %+v", r)
	}
	if req.Offers["breach_contact"] != "f_desk_contact" {
		t.Errorf("access_request offers %v", req.Offers)
	}

	// approval@1.
	ap := find("approval")
	f, _ := ap.Field("f_approver")
	if !slices.Equal(f.UniqueAmongSiblings, []string{"pending", "approved"}) || !slices.Equal(ap.PlacementWritableBy, []string{"kernel"}) {
		t.Errorf("approval: f_approver unique %v, placement %v", f.UniqueAmongSiblings, ap.PlacementWritableBy)
	}
	for _, to := range []string{"approved", "rejected"} {
		tr, _ := ap.Transition("pending", to)
		if !slices.Equal(tr.Except, []string{typedef.AllowedContainerCreator}) || !slices.Contains(tr.Allowed, "field:f_approver") {
			t.Errorf("pending -> %s = %+v", to, tr)
		}
	}
	if tr, _ := ap.Transition("pending", "pending"); tr.Sets["f_approver"] != "f_delegate" || !slices.Equal(tr.Except, []string{typedef.AllowedContainerCreator}) {
		t.Errorf("the hand-over = %+v", tr)
	}
	if tr, _ := ap.Transition("pending", "canceled"); !slices.Equal(tr.Allowed, []string{typedef.AllowedNobody}) {
		t.Errorf("pending -> canceled allowed %v; want [nobody]", tr.Allowed)
	}
	for _, id := range []string{"move_approved", "move_rejected"} {
		if r := reaction(ap, id); r.Target != typedef.TargetContainer || r.OnGuard != typedef.OnGuardSkip {
			t.Errorf("%s = %+v", id, r)
		}
	}
	if !invariant(ap, "approver_missing") || !invariant(ap, "delegate_not_approver") {
		t.Errorf("approval invariants %v", ap.Invariants)
	}

	// fulfilment_task@1.
	task := find("fulfilment_task")
	if !invariant(task, "resource_missing") {
		t.Errorf("fulfilment_task invariants %v", task.Invariants)
	}
	if tr, _ := task.Transition("open", "in_progress"); !slices.Equal(tr.Allowed, []string{"role:desk_agent"}) {
		t.Errorf("open -> in_progress allowed %v", tr.Allowed)
	}
}
