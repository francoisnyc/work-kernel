// Tests for the etag step: the rule that says whether an event changes
// something a caller could overwrite (itemservice.ChangesContent). An
// Item's content_seq, from which the public etag is derived, moves only
// then; item_seq still counts every event.
//
// The rule reads the event's shape (its kind and its payload) and the
// Type, never a value and never the actor: a state change, a field a
// caller may write (by anyone, role-written fields included), a link, the
// readers or the container. Field-less records and writes that touch only
// writable_by: kernel fields leave the content alone. When in doubt it
// says yes: a refusal costs a refresh, a missed one loses an update.
package itemservice_test

import (
	"os"
	"slices"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/itemservice"
	"github.com/francoisnyc/work-kernel/internal/lifecycle"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

func typeFromFile(t *testing.T, name string) *typedef.Type {
	t.Helper()
	data, err := os.ReadFile("../../types/" + name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	ty, err := typedef.Parse(data)
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	return ty
}

// contentRows is the decision, one row per shape of event. The fields are
// the repository's own Types':
//
//	incident@1      f_title, f_impact: anyone; f_due_*: writable_by kernel
//	story@3         f_labels: a set anyone writes
//	sla_goal@1      f_week: a set only the kernel writes; f_due_at: kernel
//	sla_policy@1    f_resolution_s_p1: writable_by role:sla_admin
//	leave_balance@1 f_used: kernel (a reaction adds to it)
type contentRow struct {
	name    string
	typ     string // a file under types/
	op      string
	payload map[string]any
	want    bool
}

var contentRows = []contentRow{
	// --- create: the first content ---
	{"create", "incident.v1.yaml", "create", map[string]any{"field_ids": []string{"f_priority", "f_title"}}, true},
	{"create with no field", "incident.v1.yaml", "create", map[string]any{"field_ids": []string{}}, true},
	{"create by a reaction, kernel fields only", "sla/sla_goal.v1.yaml", "create",
		map[string]any{"field_ids": []string{"f_due_at", "f_metric"}}, true},

	// --- transition: the state changed, whoever moved it ---
	{"transition", "incident.v1.yaml", "transition", map[string]any{"from": "new", "to": "in_progress"}, true},
	{"transition that re-enters its state", "incident.v1.yaml", "transition", map[string]any{"from": "in_progress", "to": "in_progress"}, true},
	{"transition applied by a hold (allow)", "incident.v1.yaml", "transition",
		map[string]any{"from": "new", "to": "in_progress", "hold_id": "h"}, true},
	{"transition that sets only a kernel field", "incident.v1.yaml", "transition",
		map[string]any{"from": "new", "to": "in_progress", "field_ids": []string{"f_due_resolution"}}, true},

	// --- update: the fields decide ---
	{"update of a field anyone writes", "incident.v1.yaml", "update", map[string]any{"field_ids": []string{"f_title"}}, true},
	{"update that clears a field anyone writes", "incident.v1.yaml", "update",
		map[string]any{"field_ids": []string{}, "cleared_field_ids": []string{"f_impact"}}, true},
	{"update of a role-written field", "sla/sla_policy.v1.yaml", "update",
		map[string]any{"field_ids": []string{"f_resolution_s_p1"}}, true},
	{"update of a kernel-written field (a reaction's set)", "incident.v1.yaml", "update",
		map[string]any{"field_ids": []string{"f_due_resolution"}}, false},
	{"update that clears a kernel-written field", "incident.v1.yaml", "update",
		map[string]any{"field_ids": []string{}, "cleared_field_ids": []string{"f_due_first_response"}}, false},
	{"update of both kernel-written fields", "incident.v1.yaml", "update",
		map[string]any{"field_ids": []string{"f_due_first_response", "f_due_resolution"}}, false},
	{"update of a kernel-written and a caller-written field", "incident.v1.yaml", "update",
		map[string]any{"field_ids": []string{"f_due_resolution", "f_title"}}, true},
	{"update that sets a kernel field and clears a caller's", "incident.v1.yaml", "update",
		map[string]any{"field_ids": []string{"f_due_resolution"}, "cleared_field_ids": []string{"f_impact"}}, true},
	{"an acting timer's write of a kernel field", "sla/sla_goal.v1.yaml", "update",
		map[string]any{"field_ids": []string{"f_breached_at"}, "timer": "breach"}, false},
	{"a reaction's add to a kernel field", "leave_balance.v1.yaml", "update",
		map[string]any{"field_ids": []string{"f_used"}, "added_field_ids": []string{"f_used"}}, false},
	{"a set operation on a set anyone writes", "story.v3.yaml", "update",
		map[string]any{"field_ids": []string{"f_labels"}, "set_ops": map[string]any{"f_labels": map[string]any{"add": []any{"api"}}}}, true},
	{"a set operation that empties a set anyone writes", "story.v3.yaml", "update",
		map[string]any{"field_ids": []string{}, "cleared_field_ids": []string{"f_labels"}}, true},
	{"a kernel-written set", "sla/sla_goal.v1.yaml", "update", map[string]any{"field_ids": []string{"f_week"}}, false},
	{"update of a field the Type version does not declare: in doubt, yes", "incident.v1.yaml", "update",
		map[string]any{"field_ids": []string{"f_not_declared"}}, true},
	{"update that names nothing: in doubt, yes", "incident.v1.yaml", "update", map[string]any{"field_ids": []string{}}, true},
	{"update read back from storage (lists as []any)", "incident.v1.yaml", "update",
		map[string]any{"field_ids": []any{"f_due_resolution"}}, false},
	{"update read back from storage, a caller's field", "incident.v1.yaml", "update",
		map[string]any{"field_ids": []any{"f_due_resolution", "f_title"}}, true},

	// --- update: who reads the Item, and where it is ---
	{"readers narrowed", "incident.v1.yaml", "update", map[string]any{"field_ids": []string{}, "readers": []string{"user:ana"}}, true},
	{"readers inherited again", "incident.v1.yaml", "update", map[string]any{"field_ids": []string{}, "inherit": true}, true},
	{"a move to another container", "incident.v1.yaml", "update",
		map[string]any{"field_ids": []string{}, "container_from": "a", "container_to": "b"}, true},
	{"a move to the top level", "incident.v1.yaml", "update",
		map[string]any{"field_ids": []string{}, "container_from": "a", "container_to": nil}, true},

	// --- links: on the source ---
	{"link added", "incident.v1.yaml", lifecycle.OpLinkAdded, map[string]any{"link_type": "blocks", "target": "t"}, true},
	{"link removed", "incident.v1.yaml", lifecycle.OpLinkRemoved, map[string]any{"link_type": "blocks", "target": "t"}, true},

	// --- holds: only what the request itself wrote ---
	{"a hold opens, no patch", "incident.v1.yaml", lifecycle.OpTransitionRequested,
		map[string]any{"from": "new", "requested_to": "in_progress", "hold_id": "h"}, false},
	{"a hold opens with a patch of a caller's field", "incident.v1.yaml", lifecycle.OpTransitionRequested,
		map[string]any{"from": "new", "requested_to": "in_progress", "hold_id": "h", "field_ids": []string{"f_title"}}, true},
	{"a hold opens with a patch that clears a caller's field", "incident.v1.yaml", lifecycle.OpTransitionRequested,
		map[string]any{"from": "new", "requested_to": "in_progress", "hold_id": "h", "field_ids": []string{}, "cleared_field_ids": []string{"f_impact"}}, true},
	{"a hold opens with a patch of a kernel field only", "incident.v1.yaml", lifecycle.OpTransitionRequested,
		map[string]any{"from": "new", "requested_to": "in_progress", "hold_id": "h", "field_ids": []string{"f_due_resolution"}}, false},
	{"a hold is denied", "incident.v1.yaml", lifecycle.OpTransitionDenied,
		map[string]any{"from": "new", "requested_to": "in_progress", "hold_id": "h", "reason": "no"}, false},
	{"a hold's check fails", "incident.v1.yaml", lifecycle.OpTransitionFailed,
		map[string]any{"from": "new", "requested_to": "in_progress", "hold_id": "h", "reason": "budget"}, false},

	// --- field-less records ---
	{"hook_ran", "incident.v1.yaml", lifecycle.OpHookRan, map[string]any{"hook": "notify"}, false},
	{"hook_failed", "incident.v1.yaml", lifecycle.OpHookFailed, map[string]any{"hook": "notify", "reason": "no_recipient"}, false},
	{"reaction_applied", "incident.v1.yaml", lifecycle.OpReactionApplied, map[string]any{"reaction": "incident:sla_start_resolution", "target": "t"}, false},
	{"reaction_failed", "incident.v1.yaml", lifecycle.OpReactionFailed, map[string]any{"reaction": "incident:x", "reason": "guard_failed"}, false},
	{"reaction_skipped", "incident.v1.yaml", lifecycle.OpReactionSkipped, map[string]any{"reaction": "incident:x", "reason": "condition_false"}, false},
}

func TestChangesContent_EveryEventKind(t *testing.T) {
	types := map[string]*typedef.Type{}
	for _, c := range contentRows {
		t.Run(c.name, func(t *testing.T) {
			ty := types[c.typ]
			if ty == nil {
				ty = typeFromFile(t, c.typ)
				types[c.typ] = ty
			}
			got, err := itemservice.ChangesContent(ty, c.op, c.payload)
			if err != nil {
				t.Fatalf("ChangesContent(%s, %v): %v", c.op, c.payload, err)
			}
			if got != c.want {
				t.Errorf("ChangesContent(%s, %v) = %v, want %v", c.op, c.payload, got, c.want)
			}
		})
	}
}

// The thirteen event kinds the kernel writes today. A new kind has no
// decision until someone adds one: the rule answers with an error (the
// write fails), it never guesses.
var allOps = []string{
	"create", "update", "transition",
	lifecycle.OpTransitionRequested, lifecycle.OpTransitionDenied, lifecycle.OpTransitionFailed,
	lifecycle.OpHookFailed, lifecycle.OpHookRan,
	lifecycle.OpReactionApplied, lifecycle.OpReactionFailed, lifecycle.OpReactionSkipped,
	lifecycle.OpLinkAdded, lifecycle.OpLinkRemoved,
}

func TestChangesContent_AnUndecidedKindIsAnError(t *testing.T) {
	ty := typeFromFile(t, "incident.v1.yaml")
	for _, op := range []string{"archive", "", "UPDATE", "comment_added"} {
		if changes, err := itemservice.ChangesContent(ty, op, map[string]any{"field_ids": []string{"f_title"}}); err == nil {
			t.Errorf("ChangesContent(%q) = %v, nil; want an error: nobody decided this kind", op, changes)
		}
	}
}

// DecidedOps is the list the rule answers for: exactly today's kinds, and
// the table above has at least one row for each. (internal/api holds the
// other half: every kind the API can show has a decision.)
func TestChangesContent_TheTableCoversEveryDecidedKind(t *testing.T) {
	decided := itemservice.DecidedOps()
	want := slices.Sorted(slices.Values(allOps))
	if !slices.Equal(decided, want) {
		t.Fatalf("DecidedOps() = %v, want %v (sorted)", decided, want)
	}
	for _, op := range decided {
		if !slices.ContainsFunc(contentRows, func(c contentRow) bool { return c.op == op }) {
			t.Errorf("no row of the table decides %q", op)
		}
		ty := typeFromFile(t, "incident.v1.yaml")
		if _, err := itemservice.ChangesContent(ty, op, map[string]any{}); err != nil {
			t.Errorf("ChangesContent(%q) with an empty payload: %v; a decided kind always answers", op, err)
		}
	}
}
