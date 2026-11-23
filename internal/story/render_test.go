// Tests for Renderer: every kind of event and side effect, written as the
// package documentation's sentences. The events are shaped as the item
// service's history gives them (itemservice.Event), as recorded from the
// real kernel's vacation and incident flows.
package story_test

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/contracts"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
	"github.com/francoisnyc/work-kernel/internal/story"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

var (
	requestID = uuid.MustParse("01a2087f-0000-7000-8000-000000000001")
	balanceID = uuid.MustParse("01a2087f-0000-7000-8000-000000000002")
	incID     = uuid.MustParse("01a2087f-0000-7000-8000-000000000003")
	portalID  = uuid.MustParse("01a2087f-0000-7000-8000-000000000004")
	firstID   = uuid.MustParse("01a2087f-0000-7000-8000-000000000005")
	replyID   = uuid.MustParse("01a2087f-0000-7000-8000-000000000006")
	calID     = uuid.MustParse("01a2087f-0000-7000-8000-000000000007")
	unknownID = uuid.MustParse("01a2087f-0000-7000-8000-0000000000ff")
)

func renderer() story.Renderer {
	return story.Renderer{
		Day1: day1,
		People: map[string]string{"user:alice": "alice", "user:bob": "bob", "user:sam": "sam", "user:lea": "lea",
			"customer:kim": "kim"},
		Items: map[uuid.UUID]string{requestID: "request", balanceID: "balance", incID: "inc", portalID: "portal",
			firstID: "first", replyID: "reply1", calID: "cal"},
		Hidden:  map[string][]string{"sla_goal": {"f_week", "f_days_off", "f_zone"}},
		Catalog: contracts.Default(),
	}
}

// typeOf finds a Type version in types/.
func typeOf(t *testing.T, id string, version int) *typedef.Type {
	t.Helper()
	for _, ty := range repoTypes(t) {
		if ty.ID == id && ty.Version == version {
			return ty
		}
	}
	t.Fatalf("no %s@%d in types/", id, version)
	return nil
}

func ref(id uuid.UUID) *itemservice.ItemRef { return &itemservice.ItemRef{ID: id} }

func TestRenderer_Events(t *testing.T) {
	vacation := typeOf(t, "vacation_request", 3)
	balance := typeOf(t, "leave_balance", 1)
	incident := typeOf(t, "incident", 3)
	goal := typeOf(t, "sla_goal", 3)
	reply := typeOf(t, "reply", 2)
	desk := typeOf(t, "desk", 2)
	hold := uuid.New()

	cases := []struct {
		name string
		ty   *typedef.Type
		ev   itemservice.Event
		own  []string
		item string
		want string
	}{
		{"a step's create: what it sent is left out", vacation,
			itemservice.Event{ItemID: requestID, Op: "create", Actor: "user:alice", To: "draft",
				Values: map[string]any{"f_start": "2026-12-21", "f_end": "2026-12-25", "f_days": float64(5), "f_leave_type": "annual"}},
			[]string{"f_days", "f_end", "f_leave_type", "f_start"}, "request", "alice created it in draft"},
		{"a create's values: a date is day N, an enum bare, a number plain", vacation,
			itemservice.Event{ItemID: requestID, Op: "create", Actor: "user:alice", To: "draft",
				Values: map[string]any{"f_start": "2026-12-21", "f_days": float64(5), "f_leave_type": "annual"}},
			nil, "request", "alice created it in draft: f_days = 5, f_leave_type = annual, f_start = day 15"},
		{"a held request", vacation,
			itemservice.Event{ItemID: requestID, Op: "transition_requested", Actor: "user:alice", From: "draft", RequestedTo: "submitted", HoldID: &hold},
			nil, "request", "alice asked draft -> submitted"},
		{"a check's correction, with the link it resolved", vacation,
			itemservice.Event{ItemID: requestID, Op: "transition", Actor: "hook:vacation_request:hr_check", OnBehalfOf: "user:alice",
				From: "draft", To: "submitted", HoldID: &hold, LinkType: "balance", LinkTarget: ref(balanceID),
				Values: map[string]any{"f_approver": "user:bob"}},
			nil, "request", "hr_check for alice moved it draft -> submitted: f_approver = bob; linked balance to balance"},
		{"a reaction's addition", balance,
			itemservice.Event{ItemID: balanceID, Op: "update", Actor: "reaction:vacation_request:deduct", OnBehalfOf: "user:bob",
				Values: map[string]any{"f_used": float64(5)}, Added: map[string]any{"f_used": float64(5)}},
			nil, "balance", "deduct for bob updated it: f_used +5 = 5"},
		{"a refund: a negative addition", balance,
			itemservice.Event{ItemID: balanceID, Op: "update", Actor: "reaction:vacation_request:refund", OnBehalfOf: "user:alice",
				Values: map[string]any{"f_used": float64(0)}, Added: map[string]any{"f_used": float64(-5)}},
			nil, "balance", "refund for alice updated it: f_used -5 = 0"},
		{"a denial", vacation,
			itemservice.Event{ItemID: requestID, Op: "transition_denied", Actor: "reaction:vacation_request:deduct", OnBehalfOf: "user:bob",
				From: "submitted", RequestedTo: "approved", HoldID: &hold, Reason: "insufficient_balance"},
			nil, "request", "deduct for bob denied submitted -> approved: insufficient_balance"},
		{"a failure", vacation,
			itemservice.Event{ItemID: requestID, Op: "transition_failed", Actor: "hook:vacation_request:hr_check", OnBehalfOf: "user:alice",
				From: "draft", RequestedTo: "submitted", HoldID: &hold, Reason: "budget_exhausted"},
			nil, "request", "hr_check for alice failed draft -> submitted: budget_exhausted"},
		{"an acting timer's run", vacation,
			itemservice.Event{ItemID: requestID, Op: "hook_ran", Actor: "hook:vacation_request:remind_approver", OnBehalfOf: "user:alice",
				Hook: "remind_approver", Timer: "remind", Run: 1},
			nil, "request", "remind_approver for alice ran (remind run 1)"},
		{"a hook that ran", vacation,
			itemservice.Event{ItemID: requestID, Op: "hook_ran", Actor: "hook:vacation_request:notify_expired", OnBehalfOf: "user:alice",
				Hook: "notify_expired"},
			nil, "request", "notify_expired for alice ran"},
		{"a hook that failed", vacation,
			itemservice.Event{ItemID: requestID, Op: "hook_failed", Actor: "hook:vacation_request:remind_approver", OnBehalfOf: "user:alice",
				Hook: "remind_approver", Timer: "remind", Run: 2, Reason: "budget_exhausted"},
			nil, "request", "remind_approver for alice failed (remind run 2): budget_exhausted"},
		{"a timer's move", vacation,
			itemservice.Event{ItemID: requestID, Op: "transition", Actor: "system:timer", OnBehalfOf: "user:alice", From: "submitted", To: "expired"},
			nil, "request", "timer for alice moved it submitted -> expired"},
		{"a person's move", vacation,
			itemservice.Event{ItemID: requestID, Op: "transition", Actor: "user:alice", From: "approved", To: "withdrawn"},
			nil, "request", "alice moved it approved -> withdrawn"},
		{"a reaction's record", vacation,
			itemservice.Event{ItemID: requestID, Op: "reaction_applied", Actor: "reaction:vacation_request:refund", OnBehalfOf: "user:alice",
				Reaction: "vacation_request:refund", ReactionTarget: ref(balanceID)},
			nil, "request", "refund for alice applied to balance"},
		{"a reaction refused", vacation,
			itemservice.Event{ItemID: requestID, Op: "reaction_failed", Actor: "reaction:vacation_request:refund", OnBehalfOf: "user:alice",
				Reaction: "vacation_request:refund", ReactionTarget: ref(balanceID), Reason: "reaction_failed"},
			nil, "request", "refund for alice failed on balance: reaction_failed"},
		{"a reaction skipped", incident,
			itemservice.Event{ItemID: incID, Op: "reaction_skipped", Actor: "reaction:incident:customer_replied", OnBehalfOf: "user:sam",
				Reaction: "incident:customer_replied", Reason: "not_by"},
			nil, "inc", "customer_replied for sam skipped: not_by"},
		{"a goal created by reaction: instants, hidden values, a container", goal,
			itemservice.Event{ItemID: firstID, Op: "create", Actor: "reaction:incident:sla_start_first_response", OnBehalfOf: "customer:kim",
				To: "running", Container: ref(incID), Values: map[string]any{
					"f_due_at": "2026-12-14T11:00:00Z", "f_metric": "first_response", "f_started_at": "2026-12-11T15:00:00Z",
					"f_target_s": float64(14400), "f_week": []any{"mon 09:00-17:00"}, "f_zone": "UTC"}},
			nil, "first", "sla_start_first_response for kim created it in running inside inc: f_due_at = day 8 11:00, " +
				"f_metric = first_response, f_started_at = day 5 15:00, f_target_s = 14400, f_week = ..., f_zone = ..."},
		{"a set reaction", incident,
			itemservice.Event{ItemID: incID, Op: "update", Actor: "reaction:sla_goal:show_first_response", OnBehalfOf: "customer:kim",
				Values: map[string]any{"f_due_first_response": "2026-12-14T11:00:00Z"}},
			nil, "inc", "show_first_response for kim updated it: f_due_first_response = day 8 11:00"},
		{"a cleared field", incident,
			itemservice.Event{ItemID: incID, Op: "update", Actor: "reaction:sla_goal:clear_first_response", OnBehalfOf: "user:sam",
				Values: map[string]any{"f_due_first_response": nil}},
			nil, "inc", "clear_first_response for sam updated it: f_due_first_response = null"},
		{"an acting timer at an instant", goal,
			itemservice.Event{ItemID: firstID, Op: "update", Actor: "system:timer", OnBehalfOf: "customer:kim", Timer: "breached",
				Values: map[string]any{"f_breached_at": "2026-12-07T11:00:00Z"}},
			nil, "first", "timer for kim updated it (breached): f_breached_at = day 1 11:00"},
		{"its hook", goal,
			itemservice.Event{ItemID: firstID, Op: "hook_ran", Actor: "hook:sla_goal:notify_breach", OnBehalfOf: "customer:kim",
				Hook: "notify_breach", Timer: "breached"},
			nil, "first", "notify_breach for kim ran (breached)"},
		{"a reaction's move, in seconds", goal,
			itemservice.Event{ItemID: firstID, Op: "transition", Actor: "reaction:incident:sla_answered", OnBehalfOf: "user:sam",
				From: "running", To: "completed", Values: map[string]any{"f_due_at": nil, "f_elapsed_s": float64(3600), "f_outcome": "met"}},
			nil, "first", "sla_answered for sam moved it running -> completed: f_due_at = null, f_elapsed_s = 3600, f_outcome = met"},
		{"a step's own update, with nothing else", incident,
			itemservice.Event{ItemID: incID, Op: "update", Actor: "user:sam", Values: map[string]any{"f_assignee": "user:sam"}},
			[]string{"f_assignee"}, "inc", "sam updated it"},
		{"a move that cleared a field the step did not send", incident,
			itemservice.Event{ItemID: incID, Op: "transition", Actor: "customer:kim", From: "resolved", To: "in_progress",
				Values: map[string]any{"f_resolution": nil}},
			nil, "inc", "kim moved it resolved -> in_progress: f_resolution = null"},
		{"text is quoted, in a container", reply,
			itemservice.Event{ItemID: replyID, Op: "create", Actor: "user:sam", To: "default", Container: ref(incID),
				Values: map[string]any{"f_body": `Which "client" version?`}},
			nil, "reply1", `sam created it in default inside inc: f_body = "Which \"client\" version?"`},
		{"a link", desk,
			itemservice.Event{ItemID: portalID, Op: "link_added", Actor: "user:lea", LinkType: "calendar", LinkTarget: ref(calID)},
			nil, "portal", "lea linked calendar to cal"},
		{"an unlink", desk,
			itemservice.Event{ItemID: portalID, Op: "link_removed", Actor: "user:lea", LinkType: "calendar", LinkTarget: ref(calID)},
			nil, "portal", "lea unlinked calendar from cal"},
		{"an Item the observer cannot see", goal,
			itemservice.Event{ItemID: firstID, Op: "reaction_applied", Actor: "reaction:sla_goal:show_first_response", OnBehalfOf: "customer:kim",
				Reaction: "sla_goal:show_first_response", ReactionTarget: &itemservice.ItemRef{Restricted: true}},
			nil, "first", "show_first_response for kim applied to (hidden)"},
		{"a principal who is not one of the people", balance,
			itemservice.Event{ItemID: balanceID, Op: "create", Actor: "user:zed", To: "open",
				Values: map[string]any{"f_owner": "user:yan", "f_allowance": float64(20)}},
			nil, "balance", "user:zed created it in open: f_allowance = 20, f_owner = user:yan"},
		{"an Item the story does not name", vacation,
			itemservice.Event{ItemID: unknownID, Op: "transition", Actor: "user:alice", From: "draft", To: "withdrawn"},
			nil, "vacation_request#?", "alice moved it draft -> withdrawn"},
	}
	r := renderer()
	when := at(2, 10, 0, 0)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ev := c.ev
			ev.CreatedAt = when
			got := r.Event(c.ty, ev, c.own)
			if got.Text != c.want || got.Item != c.item || !got.At.Equal(when) {
				t.Errorf("line = %q %q at %v\nwant   %q %q at %v", got.Item, got.Text, got.At, c.item, c.want, when)
			}
		})
	}
}

// The line's instant is the event's, to the second: the item service
// stamps writes to the microsecond.
func TestRenderer_EventTime(t *testing.T) {
	r := renderer()
	ev := itemservice.Event{ItemID: requestID, Op: "transition_failed", Actor: "hook:vacation_request:hr_check",
		OnBehalfOf: "user:alice", From: "draft", RequestedTo: "submitted", Reason: "budget_exhausted",
		CreatedAt: at(1, 9, 19, 3).Add(250 * time.Microsecond)}
	if got := r.Event(typeOf(t, "vacation_request", 3), ev, nil).Format(day1); got !=
		"day 1 09:19:03 request: hr_check for alice failed draft -> submitted: budget_exhausted" {
		t.Errorf("line = %q", got)
	}
}

func TestRenderer_Calls(t *testing.T) {
	r := renderer()
	for _, c := range []struct {
		name string
		call story.Call
		want string
	}{
		{"an email", story.Call{At: at(2, 10, 0, 0), Action: "email.notify@1", ItemID: requestID, Hook: "notify_approved",
			Inputs: map[string]any{"to": "user:alice"}, Answer: story.OK, SideEffect: true},
			"day 2 10:00 email: notify for request (notify_approved): to = alice"},
		{"an action with outputs: inputs sorted, text quoted", story.Call{At: at(1, 9, 0, 0), Action: "erp.create_invoice@1",
			ItemID: requestID, Hook: "invoice", Inputs: map[string]any{"customerId": "C-1", "amount": float64(100)},
			Outputs: map[string]any{"invoiceId": "INV-1"}, Answer: story.OK, SideEffect: true},
			`day 1 09:00 erp: create_invoice for request (invoice): amount = 100, customerId = "C-1" -> invoiceId = "INV-1"`},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := r.Call(c.call).Format(day1); got != c.want {
				t.Errorf("line = %q\nwant   %q", got, c.want)
			}
		})
	}
}
