// Tests for Step 6, part 2: what the relay sends for hooks and holds.
//
// Entering a state with enrich hooks starts a workflow, and so does a
// transition request, which opens a hold but enters no state. Belt and
// braces: a request's payload names its target `requested_to`, and the relay
// reads `to` as "the state entered" for transition events only.
package relay_test

import (
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
	"github.com/francoisnyc/work-kernel/internal/typedef"
	wf "github.com/francoisnyc/work-kernel/internal/workflow"
)

func registerTicketV3(t *testing.T, k *itemservice.Service) {
	t.Helper()
	data, err := os.ReadFile("../../types/ticket.v3.yaml")
	if err != nil {
		t.Fatalf("read ticket v3: %v", err)
	}
	ty, err := typedef.Parse(data)
	if err != nil {
		t.Fatalf("parse ticket v3: %v", err)
	}
	if err := k.RegisterType(testCtx(t), ty); err != nil {
		t.Fatalf("register ticket v3: %v", err)
	}
}

// heldTicket creates a v3 ticket in triage (1), moves it to open (2) and
// in_progress (3), and asks for approval (4), which opens a hold.
func heldTicket(t *testing.T, k *itemservice.Service) (uuid.UUID, uuid.UUID) {
	t.Helper()
	registerTicketV3(t, k)
	ana := item.Principal{Actor: "user:ana-" + uuid.NewString()[:8]}
	if _, err := pool.Exec(testCtx(t),
		`insert into principal_roles (namespace_id, principal, role) values ('default', $1, 'approver')`,
		ana.Actor); err != nil {
		t.Fatalf("grant approver: %v", err)
	}
	res, err := k.Create(testCtx(t), itemservice.CreateRequest{
		IdempotencyKey: newKey(), Principal: ana, TypeID: "ticket", TypeVersion: 3,
		Fields: map[string]any{"f_customer_id": "cust-1", "f_amount": 100, "f_assignee": "user-bob"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	var held itemservice.TransitionResult
	for _, to := range []string{"open", "in_progress", "approved"} {
		held, err = k.Transition(testCtx(t), itemservice.TransitionRequest{
			IdempotencyKey: newKey(), Principal: ana, ItemID: res.ItemID, ToState: to,
		})
		if err != nil {
			t.Fatalf("Transition to %s: %v", to, err)
		}
	}
	if held.Outcome != itemservice.OutcomePending {
		t.Fatalf("approve = %+v, want pending", held)
	}
	return res.ItemID, held.HoldID
}

func TestRelay_HooksAndHolds(t *testing.T) {
	r, fake, _ := newRelay(t)
	k := itemservice.New(pool)
	id, holdID := heldTicket(t, k)

	// The check denies: transition_denied (5) enters no state.
	_, err := k.Internal().ResolveHold(testCtx(t), itemservice.ResolveHoldRequest{
		IdempotencyKey: "hold:" + holdID.String(), Principal: item.Principal{Actor: "hook:ticket:erp_check"},
		ItemID: id, HoldID: holdID, Verdict: itemservice.VerdictDeny, Reason: "customer_on_hold",
		Attribution: item.Attribution{OriginatingHook: "hook:ticket@v3:erp_check",
			CausationID: fmt.Sprintf("%s:4", id), Hop: 1},
	})
	if err != nil {
		t.Fatalf("ResolveHold: %v", err)
	}

	runOnce(t, r)

	got := fake.callsFor(id)
	if len(got) != 5 {
		t.Fatalf("deliveries = %+v, want five", got)
	}
	want := []call{
		// Entering triage runs two enrich hooks: a workflow starts.
		{Start: true, Event: wf.Event{Seq: 1, Op: "create", To: "triage"}},
		{Start: false, Event: wf.Event{Seq: 2, Op: "transition", From: "triage", To: "open"}},
		{Start: true, Event: wf.Event{Seq: 3, Op: "transition", From: "open", To: "in_progress"}}, // a timer
		// The request enters no state, and still starts a workflow: the hold
		// needs its check to run.
		{Start: true, Event: wf.Event{Seq: 4, Op: "transition_requested", From: "in_progress",
			RequestedTo: "approved", HoldID: holdID.String()}},
		{Start: false, Event: wf.Event{Seq: 5, Op: "transition_denied", From: "in_progress",
			RequestedTo: "approved", HoldID: holdID.String(), Hop: 1}},
	}
	for i := range want {
		want[i].ItemID, want[i].UpdateID = id.String(), fmt.Sprintf("%s:%d", id, want[i].Event.Seq)
		got[i].Event.At = time.Time{}
		if !reflect.DeepEqual(got[i], want[i]) {
			t.Errorf("delivery %d = %+v\nwant %+v", i+1, got[i], want[i])
		}
	}
}

// Belt and braces: even if a payload that is not a transition's carries a
// `to` key, the relay does not read it as a state the Item entered.
func TestRelay_OnlyATransitionEntersAState(t *testing.T) {
	r, fake, _ := newRelay(t)
	k := itemservice.New(pool)
	id, holdID := heldTicket(t, k)

	// Event 5, written by hand: an op that is not a transition, with a `to`.
	ctx := testCtx(t)
	if _, err := pool.Exec(ctx,
		`insert into item_events (item_id, item_seq, actor, op, payload)
		 values ($1, 5, 'hook:ticket:erp_check', 'transition_failed',
		         jsonb_build_object('from', 'in_progress', 'to', 'approved', 'hold_id', $2::text))`,
		id, holdID); err != nil {
		t.Fatalf("insert event: %v", err)
	}
	if _, err := pool.Exec(ctx, `insert into outbox (item_id, item_seq) values ($1, 5)`, id); err != nil {
		t.Fatalf("insert outbox row: %v", err)
	}

	runOnce(t, r)

	got := fake.callsFor(id)
	if len(got) != 5 {
		t.Fatalf("deliveries = %+v, want five", got)
	}
	if ev := got[4].Event; ev.Op != "transition_failed" || ev.To != "" || got[4].Start {
		t.Errorf("delivery 5 = %+v, want transition_failed entering no state, without start", got[4])
	}
}
