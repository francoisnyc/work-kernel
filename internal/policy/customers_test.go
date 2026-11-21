// Tests for Jira part 2, step 4a: Project and the Type it is given.
//
// Every path of the item service passes the Item's own Type version. The
// Type is optional only so that callers projecting for staff are as they
// were; so a call site that forgets it must not open a gap:
//
//   - a customer (or an agent acting for one) with no Type gets nothing;
//   - staff get the same result with or without a Type, and as before this
//     step: the whole Item.
package policy_test

import (
	"reflect"
	"testing"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/policy"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

func customerTestItem() (item.Item, *typedef.Type, uuid.UUID) {
	scope := uuid.New()
	ty := &typedef.Type{ID: "incident", Version: 2, CustomerFields: []string{"f_title"},
		Fields: []typedef.Field{{ID: "f_title", Type: typedef.String}, {ID: "f_assignee", Type: typedef.Principal}}}
	it := item.Item{ID: uuid.New(), TypeID: "incident", TypeVersion: 2, ScopeID: &scope, State: "new",
		Fields: map[string]any{"f_title": "VPN down", "f_assignee": "user:sam"}, ItemSeq: 4, ContentSeq: 2,
		CreatedBy: "customer:kim"}
	return it, ty, scope
}

func TestProject_ACustomerWithNoType_GetsNothing(t *testing.T) {
	it, ty, scope := customerTestItem()
	bot := item.Principal{Actor: "agent:portal-bot", OnBehalfOf: "customer:kim", AgentSession: "s"}
	for _, p := range []item.Principal{{Actor: "customer:kim"}, bot} {
		if got, ok := policy.Project(p, []uuid.UUID{scope}, it); ok || !reflect.DeepEqual(got, item.Item{}) {
			t.Errorf("Project(%s) with no Type = %+v, %v; want nothing", p.Actor, got, ok)
		}
		// With the wrong Type version, nothing either.
		other := *ty
		other.Version = 1
		if _, ok := policy.Project(p, []uuid.UUID{scope}, it, &other); ok {
			t.Errorf("Project(%s) with another version's Type let the Item through", p.Actor)
		}
		// With its Type: the customer fields, no etag.
		got, ok := policy.Project(p, []uuid.UUID{scope}, it, ty)
		if !ok || !reflect.DeepEqual(got.Fields, map[string]any{"f_title": "VPN down"}) || got.ContentSeq != 0 {
			t.Errorf("Project(%s) with its Type = %+v, %v; want f_title only and content_seq 0", p.Actor, got, ok)
		}
	}
}

func TestProject_Staff_WithOrWithoutAType_TheWholeItem(t *testing.T) {
	it, ty, scope := customerTestItem()
	bot := item.Principal{Actor: "agent:triage-bot", OnBehalfOf: "user:ana", AgentSession: "s"}
	// The kernel's own actors carry an on_behalf_of for attribution only:
	// a customer there does not make them a customer.
	reaction := item.Principal{Actor: "reaction:incident:customer_replied", OnBehalfOf: "customer:kim"}
	for _, p := range []item.Principal{{Actor: "user:sam"}, bot, reaction} {
		without, ok1 := policy.Project(p, []uuid.UUID{scope}, it)
		with, ok2 := policy.Project(p, []uuid.UUID{scope}, it, ty)
		if !ok1 || !ok2 || !reflect.DeepEqual(without, it) || !reflect.DeepEqual(with, it) {
			t.Errorf("Project(%s) = %+v, %v without a Type, %+v, %v with one; want the whole Item both times",
				p.Actor, without, ok1, with, ok2)
		}
		if _, ok := policy.Project(p, nil, it, ty); ok {
			t.Errorf("Project(%s) let through an Item in a scope they cannot see", p.Actor)
		}
	}
}
