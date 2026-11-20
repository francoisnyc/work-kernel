// Tests for Jira part 2, step 2, in the lifecycle's pure functions: which
// reactions a trigger runs, which events need a workflow, and how a guard
// reads a multi-value field.
package lifecycle_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/lifecycle"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// A desk: a public remark made inside it by its creator while it waits
// sets it working again; starting work on it creates a todo inside it, and
// closing it creates a survey.
const deskType = `
id: desk
version: 1
initial_state: open
fields:
  - {id: f_title,     type: string}
  - {id: f_labels,    type: string,    multiple: true}
  - {id: f_reviewers, type: principal, multiple: true}
states:
  - {id: open,    category: todo}
  - {id: working, category: active}
  - {id: waiting, category: waiting}
  - {id: done,    category: done}
transitions:
  - {from: open,    to: working}
  - {from: working, to: waiting}
  - {from: waiting, to: working}
  - from: working
    to: done
    guard: '"ready" in f_labels && size(f_reviewers) >= 2 && !("blocked" in f_labels)'
reactions:
  - {id: reopen,    kind: move,   on: created_inside, of: [remark, note], to: working, from: [waiting]}
  - {id: escalate,  kind: move,   on: created_inside, of: [alarm], to: working, from: [waiting]}
  - {id: checklist, kind: create, on: enter, state: working, from: [open], type: todo, version: 1}
  - {id: log,       kind: create, on: enter, state: working, type: todo, version: 1}
  - {id: survey,    kind: create, on: enter, state: done, type: todo, version: 1}
`

func deskTy(t *testing.T) *typedef.Type {
	t.Helper()
	ty, err := typedef.Parse([]byte(deskType))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return ty
}

func ids(rs []typedef.Reaction) []string {
	var out []string
	for _, r := range rs {
		out = append(out, r.ID)
	}
	return out
}

// Entering a state runs its move and create reactions in the Type's order;
// one with `from` only when the entry comes from there.
func TestEnterReactions(t *testing.T) {
	ty := deskTy(t)
	for _, c := range []struct {
		from, to string
		want     []string
	}{
		{"open", "working", []string{"checklist", "log"}},
		{"waiting", "working", []string{"log"}}, // checklist is from open only
		{"working", "done", []string{"survey"}},
		{"working", "waiting", nil},
		{"", "open", nil}, // created
	} {
		if got := ids(lifecycle.EnterReactions(ty, c.from, c.to)); !slices.Equal(got, c.want) {
			t.Errorf("EnterReactions(%q -> %q) = %v, want %v", c.from, c.to, got, c.want)
		}
	}
}

// An Item created inside runs the reactions that name its Type.
func TestInsideReactions(t *testing.T) {
	ty := deskTy(t)
	for child, want := range map[string][]string{
		"remark": {"reopen"}, "note": {"reopen"}, "alarm": {"escalate"}, "todo": nil, "desk": nil,
	} {
		if got := ids(lifecycle.InsideReactions(ty, child)); !slices.Equal(got, want) {
			t.Errorf("InsideReactions(%s) = %v, want %v", child, got, want)
		}
	}
}

// A state whose entry runs a move or create reaction needs a workflow, and
// such a Type has listeners. A Type whose only reactions are created_inside
// has listeners too: its outcome records are its own events, and a run may
// be asked to continue.
func TestReactions_NeedAWorkflow(t *testing.T) {
	ty := deskTy(t)
	if !lifecycle.HasListeners(ty) {
		t.Error("HasListeners = false")
	}
	enters := func(from, to string) bool {
		return lifecycle.StartsWorkflow(ty, lifecycle.Event{Op: "transition", From: from, Entered: to})
	}
	if !enters("open", "working") || !enters("waiting", "working") || !enters("working", "done") {
		t.Error("entering a state with a reaction does not start a workflow")
	}
	// checklist does not run from waiting, but log does: per state, not
	// per reaction. Waiting has nothing.
	if enters("working", "waiting") {
		t.Error("entering waiting starts a workflow; nothing runs there")
	}
	if lifecycle.StartsWorkflow(ty, lifecycle.Event{Op: "create", Entered: "open"}) {
		t.Error("creating a desk starts a workflow; nothing runs on open")
	}
	// The outcome records start nothing by themselves.
	for _, op := range []string{lifecycle.OpReactionSkipped, lifecycle.OpReactionFailed} {
		if lifecycle.StartsWorkflow(ty, lifecycle.Event{Op: op}) {
			t.Errorf("a %s event starts a workflow", op)
		}
	}
}

// A guard reads a multi-value field as a list; unset is null, and a guard
// that cannot be evaluated on null does not hold.
func TestGuard_ReadsAMultiValueFieldAsAList(t *testing.T) {
	ty := deskTy(t)
	l := func(vs ...any) []any { return vs }
	close := func(fields map[string]any) error {
		_, err := lifecycle.Transition(ty, "working", fields, "done")
		return err
	}
	if err := close(map[string]any{"f_labels": l("a", "ready"), "f_reviewers": l("user:ana", "user:bob")}); err != nil {
		t.Errorf("ready, two reviewers: %v", err)
	}
	for name, fields := range map[string]map[string]any{
		"one reviewer":   {"f_labels": l("ready"), "f_reviewers": l("user:ana")},
		"not ready":      {"f_labels": l("a"), "f_reviewers": l("user:ana", "user:bob")},
		"blocked":        {"f_labels": l("blocked", "ready"), "f_reviewers": l("user:ana", "user:bob")},
		"no labels":      {"f_reviewers": l("user:ana", "user:bob")},
		"no reviewers":   {"f_labels": l("ready")},
		"nothing at all": {},
	} {
		var rej *lifecycle.Rejection
		if err := close(fields); !errors.As(err, &rej) || rej.Code != lifecycle.GuardFailed {
			t.Errorf("%s: %v, want guard_failed", name, err)
		}
	}
}
