package itemservice_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// agentTicketYAML is ticket@1 (types/ticket.v1.yaml) with every field open
// to agents as well as people. Fields are closed to agents by default, so a
// test in which an agent writes a ticket's fields, about something other
// than who may write them (the agent write rule, sessions, attribution),
// uses this Type. It is registered in TestMain with the ticket.
const agentTicketYAML = `
id: agent_ticket
version: 1
initial_state: open

fields:
  - {id: f_title,      type: string, writable_by: [people, agents]}
  - {id: f_priority,   type: int,    writable_by: [people, agents]}
  - {id: f_reporter,   type: string, writable_by: [people, agents]}
  - {id: f_assignee,   type: string, writable_by: [people, agents]}
  - {id: f_urgent,     type: bool,   writable_by: [people, agents]}
  - {id: f_resolution, type: enum, options: [fixed, wontfix, duplicate], writable_by: [people, agents]}

states:
  - {id: open,        category: todo}
  - {id: in_progress, category: active, timer: {after: 48h, to: open}}
  - {id: resolved,    category: done}
  - {id: canceled,    category: canceled}

transitions:
  - {from: open,        to: in_progress, guard: 'f_assignee != null'}
  - {from: in_progress, to: open}
  - {from: in_progress, to: resolved, required: [f_resolution]}
  - {from: resolved,    to: open, guard: f_resolution != "wontfix"}
  - {from: open,        to: canceled}
`

// registerAgentTicketType is called from TestMain, with the ticket.
func registerAgentTicketType(ctx context.Context) error {
	ty, err := typedef.Parse([]byte(agentTicketYAML))
	if err != nil {
		return err
	}
	return itemservice.New(pool).RegisterType(ctx, ty)
}

// createOpenAs is createAs on an agent_ticket: an Item whose fields an
// agent may write.
func createOpenAs(t *testing.T, k *itemservice.Service, p item.Principal, title string,
	narrow *itemservice.Narrow, container *uuid.UUID) itemservice.WriteResult {
	t.Helper()
	res, err := k.Create(testCtx(t), itemservice.CreateRequest{
		IdempotencyKey: newKey(), Principal: p, TypeID: "agent_ticket", TypeVersion: 1,
		Fields:      map[string]any{"f_title": title},
		Narrow:      narrow,
		ContainerID: container,
	})
	if err != nil {
		t.Fatalf("Create agent_ticket as %s: %v", p.Actor, err)
	}
	return res
}
