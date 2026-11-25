// Tests for the Jira proof of concept, part 1: field effects on
// transitions. The pure function says which fields a legal move clears; the
// item service applies them after the patch. Required fields and the guard
// are judged on the fields as the patch leaves them, before the clear, so a
// reopening guard may still read the resolution it is about to clear.
package lifecycle_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/lifecycle"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

const clearsType = `
id: chore
version: 1
initial_state: s_open
fields:
  - {id: f_resolution, type: enum, options: [fixed, wontfix]}
  - {id: f_note,       type: string}
states:
  - {id: s_open, category: todo}
  - {id: s_done, category: done}
transitions:
  - {from: s_open, to: s_done, required: [f_resolution]}
  - from: s_done
    to: s_open
    guard: 'f_resolution != "wontfix"'
    clears: [f_resolution, f_note]
`

func TestTransition_ReturnsWhatItClears(t *testing.T) {
	ty, err := typedef.Parse([]byte(clearsType))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	fields := map[string]any{"f_resolution": "fixed", "f_note": "done on Friday"}

	res, err := lifecycle.Transition(ty, "s_done", fields, "s_open")
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if res.State != "s_open" || !slices.Equal(res.Clears, []string{"f_note", "f_resolution"}) {
		t.Errorf("reopen = %+v, want s_open clearing [f_note f_resolution] (sorted)", res)
	}
	// Pure: the fields it was given are untouched.
	if fields["f_resolution"] != "fixed" || fields["f_note"] != "done on Friday" {
		t.Errorf("Transition changed its argument: %v", fields)
	}

	// A move that clears nothing says so.
	res, err = lifecycle.Transition(ty, "s_open", fields, "s_done")
	if err != nil || len(res.Clears) != 0 {
		t.Errorf("close = %+v, %v; want no clears", res, err)
	}
}

// The guard reads the value the move is about to clear: a wontfix is not
// reopened, and nothing is cleared by a refused move.
func TestTransition_GuardIsJudgedBeforeTheClear(t *testing.T) {
	ty, err := typedef.Parse([]byte(clearsType))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	res, err := lifecycle.Transition(ty, "s_done", map[string]any{"f_resolution": "wontfix"}, "s_open")
	var rej *lifecycle.Rejection
	if !errors.As(err, &rej) || rej.Code != lifecycle.GuardFailed {
		t.Fatalf("reopen a wontfix: %+v, %v; want guard_failed", res, err)
	}
	if len(res.Clears) != 0 {
		t.Errorf("a refused move clears %v", res.Clears)
	}
}

// A drag on a PLAT story must cost no outbox row: its Type has no
// listeners (no hooks, timers or reactions), like the container, comments
// and subscriptions around it. The one exception is sprint@2 (Jira part 2,
// step 1), which listens by design: its active state closes itself at
// f_end_at, so a run must hear its events.
func TestHasListeners_PLATTypesHaveNone(t *testing.T) {
	types, err := typedef.ParseDir("../../types")
	if err != nil {
		t.Fatalf("ParseDir: %v", err)
	}
	seen := map[string]bool{}
	for _, ty := range types {
		switch ty.ID {
		case "story", "bug", "task", "epic", "sprint", "space", "comment", "subscription":
			seen[ty.ID] = true
			if ty.ID == "sprint" && ty.Version >= 2 { // sprint@3 is sprint@2 with start.older_versions
				if !lifecycle.HasListeners(ty) {
					t.Errorf("sprint v%d has no listeners: its at-timer needs a workflow", ty.Version)
				}
				continue
			}
			if lifecycle.HasListeners(ty) {
				t.Errorf("%s v%d has listeners: every write to it would cost an outbox row", ty.ID, ty.Version)
			}
		}
	}
	if len(seen) != 8 {
		t.Errorf("types/ has %d of the 8 PLAT Types: %v", len(seen), seen)
	}
}
