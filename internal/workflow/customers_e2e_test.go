// End-to-end tests for Jira part 2, step 4a: a customer's incident in a
// portal (desk@2, incident@2, reply@1, note@1), with Postgres, the relay, a
// real Temporal server and worker, and the email stub. Nothing here calls
// the internal door.
//
//   - The customer's reply still reopens a waiting incident and resumes
//     its resolution goal (Jira 2.2 and 2.3); staff's reply answers it;
//     a note does neither.
//   - The breach mail goes to the assignee, never to the customer, and the
//     customer sees none of it: no goal, no due instant, no record.
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

var registerPortalOnce sync.Once

// portalStack is an incident stack with a portal (desk@2) linked to the
// same calendar and policy as its desk, and a customer.
type portalStack struct {
	*incStack
	portal uuid.UUID
	kim    item.Principal
}

func newPortalStack(t *testing.T) *portalStack {
	t.Helper()
	s := newIncStack(t, 0)
	registerPortalOnce.Do(func() {
		for _, f := range []string{"reply.v1.yaml", "note.v1.yaml", "incident.v2.yaml", "desk.v2.yaml"} {
			registerTypeFile(t, f)
		}
	})
	p := &portalStack{incStack: s, kim: item.Principal{Actor: "customer:kim-" + uuid.NewString()[:8], Namespace: "default"}}
	if _, err := pool.Exec(serverCtx(t),
		`insert into principal_roles (namespace_id, principal, role) values ('default', $1, 'desk_agent')`, s.sam.Actor); err != nil {
		t.Fatalf("grant desk_agent: %v", err)
	}
	res, err := s.k.Create(serverCtx(t), itemservice.CreateRequest{IdempotencyKey: "key-" + uuid.NewString(), Principal: s.admin,
		TypeID: "desk", TypeVersion: 2, Fields: map[string]any{"f_key": "IT", "f_name": "portal"}})
	if err != nil {
		t.Fatalf("create the portal: %v", err)
	}
	p.portal = res.ItemID
	// The same calendar and policy as the desk's.
	desk, err := s.k.Get(serverCtx(t), s.admin, s.desk)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range desk.Links {
		if _, err := s.k.Link(serverCtx(t), itemservice.LinkRequest{IdempotencyKey: "key-" + uuid.NewString(), Principal: s.admin,
			SourceID: p.portal, LinkType: l.LinkType, TargetID: l.Other}); err != nil {
			t.Fatalf("link the portal's %s: %v", l.LinkType, err)
		}
	}
	return p
}

// opened is kim's incident of a priority, assigned to sam by sam, with
// both goals created.
func (p *portalStack) opened(prio string) (inc, first, res uuid.UUID) {
	p.t.Helper()
	r, err := p.k.Create(serverCtx(p.t), itemservice.CreateRequest{IdempotencyKey: "key-" + uuid.NewString(), Principal: p.kim,
		TypeID: "incident", TypeVersion: 2, ContainerID: &p.portal, Fields: map[string]any{"f_title": "VPN down", "f_priority": prio}})
	if err != nil {
		p.t.Fatalf("kim opens an incident: %v", err)
	}
	inc = r.ItemID
	if _, err := p.k.Update(serverCtx(p.t), itemservice.UpdateRequest{IdempotencyKey: "key-" + uuid.NewString(), Principal: p.sam,
		ItemID: inc, Fields: map[string]any{"f_assignee": p.sam.Actor}}); err != nil {
		p.t.Fatalf("sam takes it: %v", err)
	}
	eventually(p.t, 20*time.Second, "both goals created", func() bool {
		first, res = goalOf(p.t, inc, "first_response"), goalOf(p.t, inc, "resolution")
		return first != uuid.Nil && res != uuid.Nil
	})
	return inc, first, res
}

func (p *portalStack) write(who item.Principal, typ string, inc uuid.UUID) uuid.UUID {
	p.t.Helper()
	r, err := p.k.Create(serverCtx(p.t), itemservice.CreateRequest{IdempotencyKey: "key-" + uuid.NewString(), Principal: who,
		TypeID: typ, TypeVersion: 1, ContainerID: &inc, Fields: map[string]any{"f_body": "a word"}})
	if err != nil {
		p.t.Fatalf("%s writes a %s: %v", who.Actor, typ, err)
	}
	return r.ItemID
}

func TestE2E_Customer_ReplyAnswersReopensAndResumes(t *testing.T) {
	p := newPortalStack(t)
	inc, first, res := p.opened("P4")
	p.act(p.sam, inc, "in_progress", nil)
	// A note answers nothing; kim's own reply neither.
	p.write(p.sam, "note", inc)
	p.write(p.kim, "reply", inc)
	staysFor(t, 2*time.Second, "first response still running", p.is(first, "running"))
	p.write(p.sam, "reply", inc)
	eventually(t, 20*time.Second, "first response answered by sam's reply", p.is(first, "completed"))

	p.act(p.sam, inc, "waiting_for_customer", nil)
	eventually(t, 20*time.Second, "resolution paused", p.is(res, "paused"))
	p.write(p.sam, "note", inc)
	p.write(p.sam, "reply", inc)
	staysFor(t, 2*time.Second, "still waiting after staff's note and reply", p.is(inc, "waiting_for_customer"))
	p.write(p.kim, "reply", inc)
	eventually(t, 20*time.Second, "kim's reply brings it back", p.is(inc, "in_progress"))
	eventually(t, 20*time.Second, "resolution resumed", p.is(res, "running"))

	// What kim sees of all this: her incident and the replies, nothing else.
	seen, err := p.k.List(serverCtx(t), itemservice.ListRequest{Principal: p.kim, ContainerID: &inc, Limit: 50})
	if err != nil {
		t.Fatalf("kim lists her incident: %v", err)
	}
	for _, it := range seen.Items {
		if it.TypeID != "reply" {
			t.Errorf("kim sees a %s in her incident", it.TypeID)
		}
	}
	if len(seen.Items) != 4 {
		t.Errorf("kim sees %d Items in her incident, want the 4 replies", len(seen.Items))
	}
}

func TestE2E_Customer_TheBreachMailGoesToTheAssignee_NeverTheCustomer(t *testing.T) {
	p := newPortalStack(t)
	inc, first, res := p.opened("P1")
	eventually(t, 25*time.Second, "both goals breached and told", func() bool {
		return len(p.told(first)) == 1 && len(p.told(res)) == 1
	})
	staysFor(t, 2*time.Second, "sam told once per goal, kim never", func() bool {
		return slices.Equal(p.told(first), []string{p.sam.Actor}) && slices.Equal(p.told(res), []string{p.sam.Actor})
	})
	for _, g := range []uuid.UUID{first, res} {
		for _, m := range p.mail.Messages(g.String()) {
			if m.To == p.kim.Actor {
				t.Errorf("kim was emailed about goal %s", g)
			}
		}
		if _, err := p.k.Get(serverCtx(t), p.kim, g); err == nil {
			t.Errorf("kim reads goal %s", g)
		}
	}
	it, err := p.k.Get(serverCtx(t), p.kim, inc)
	if err != nil {
		t.Fatalf("kim reads her incident: %v", err)
	}
	for _, f := range []string{"f_due_first_response", "f_due_resolution", "f_assignee"} {
		if _, ok := it.Fields[f]; ok {
			t.Errorf("kim sees %s", f)
		}
	}
}
