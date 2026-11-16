// Tests for proof of concept Step 1: guards that read links. The item
// service reads the linked Items (their state and category, never their
// fields) and passes them in; the function stays pure.
package lifecycle_test

import (
	"errors"
	"maps"
	"slices"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/lifecycle"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

const blockType = `
id: job
version: 1
initial_state: open
fields:
  - {id: f_title, type: string}
links:
  - {name: blocks, inverse: blocked_by, targets: [job]}
states:
  - {id: open,     category: todo}
  - {id: running,  category: active}
  - {id: done,     category: done}
  - {id: canceled, category: canceled}
transitions:
  - from: running
    to: done
    guard: 'links.blocked_by.all(b, b.category in ["done", "canceled"])'
  - from: open
    to: running
    guard: '!links.blocks.exists(b, b.state == "open")'
  - from: open
    to: canceled
    guard: 'links.blocked_by.all(b, b.f_title == "x")'
`

func blockJob(t *testing.T) *typedef.Type {
	t.Helper()
	ty, err := typedef.Parse([]byte(blockType))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return ty
}

func done() lifecycle.LinkedItem { return lifecycle.LinkedItem{State: "done", Category: "done"} }
func canceled() lifecycle.LinkedItem {
	return lifecycle.LinkedItem{State: "canceled", Category: "canceled"}
}
func running() lifecycle.LinkedItem {
	return lifecycle.LinkedItem{State: "running", Category: "active"}
}
func open() lifecycle.LinkedItem { return lifecycle.LinkedItem{State: "open", Category: "todo"} }

// "Not while blocked by something that is neither done nor canceled."
func TestTransitionLinked_GuardReadsIncomingLinks(t *testing.T) {
	ty := blockJob(t)
	holds := []lifecycle.Links{
		{},                                   // nothing blocks it
		{"blocked_by": nil},                  // named, empty
		{"blocked_by": {done()}},             // a finished blocker
		{"blocked_by": {done(), canceled()}}, // a canceled blocker does not block
		{"blocks": {running()}},              // what it blocks does not matter here
	}
	for _, links := range holds {
		res, err := lifecycle.TransitionLinked(ty, "running", nil, links, "done")
		if err != nil {
			t.Errorf("links %v: %v, want the move to hold", links, err)
			continue
		}
		if res.State != "done" || res.Category != "done" {
			t.Errorf("links %v: result %+v, want done/done", links, res)
		}
	}
	for _, links := range []lifecycle.Links{
		{"blocked_by": {running()}},
		{"blocked_by": {done(), open()}},
	} {
		_, err := lifecycle.TransitionLinked(ty, "running", nil, links, "done")
		var rej *lifecycle.Rejection
		if !errors.As(err, &rej) || rej.Code != lifecycle.GuardFailed {
			t.Errorf("links %v: error = %v, want guard_failed", links, err)
			continue
		}
		if rej.From != "running" || rej.To != "done" {
			t.Errorf("links %v: rejection names %s -> %s, want running -> done", links, rej.From, rej.To)
		}
	}
}

// Outgoing links are read by their declared name, and a guard sees the
// linked Items' states.
func TestTransitionLinked_GuardReadsOutgoingLinksAndStates(t *testing.T) {
	ty := blockJob(t)
	if _, err := lifecycle.TransitionLinked(ty, "open", nil, lifecycle.Links{"blocks": {running(), done()}}, "running"); err != nil {
		t.Errorf("blocking nothing open: %v", err)
	}
	_, err := lifecycle.TransitionLinked(ty, "open", nil, lifecycle.Links{"blocks": {running(), open()}}, "running")
	var rej *lifecycle.Rejection
	if !errors.As(err, &rej) || rej.Code != lifecycle.GuardFailed {
		t.Errorf("blocking an open job: error = %v, want guard_failed", err)
	}
}

// A guard sees a linked Item's state and category only. Reading one of its
// fields cannot be evaluated, so the guard does not hold.
func TestTransitionLinked_GuardCannotReadLinkedFields(t *testing.T) {
	ty := blockJob(t)
	_, err := lifecycle.TransitionLinked(ty, "open", nil, lifecycle.Links{"blocked_by": {done()}}, "canceled")
	var rej *lifecycle.Rejection
	if !errors.As(err, &rej) || rej.Code != lifecycle.GuardFailed {
		t.Errorf("guard reading a linked field: error = %v, want guard_failed", err)
	}
}

// Links that were not read (nil) fail safe: a guard that reads links does
// not hold, so a caller that forgets to read them cannot slip a move past
// the rule. Transition, which takes no links, is the same.
func TestTransitionLinked_UnreadLinksDoNotHold(t *testing.T) {
	ty := blockJob(t)
	for name, err := range map[string]error{
		"nil links":  func() error { _, err := lifecycle.TransitionLinked(ty, "running", nil, nil, "done"); return err }(),
		"Transition": func() error { _, err := lifecycle.Transition(ty, "running", nil, "done"); return err }(),
	} {
		var rej *lifecycle.Rejection
		if !errors.As(err, &rej) || rej.Code != lifecycle.GuardFailed {
			t.Errorf("%s: error = %v, want guard_failed", name, err)
		}
	}
}

// Pure: the links passed in are not changed.
func TestTransitionLinked_DoesNotModifyItsArguments(t *testing.T) {
	ty := blockJob(t)
	links := lifecycle.Links{"blocked_by": {done(), running()}, "blocks": {open()}}
	before := maps.Clone(links)
	for k, v := range before {
		before[k] = slices.Clone(v)
	}
	_, _ = lifecycle.TransitionLinked(ty, "running", map[string]any{"f_title": "x"}, links, "done")
	if len(links) != len(before) {
		t.Fatalf("links changed: %v, was %v", links, before)
	}
	for k, v := range before {
		if !slices.Equal(links[k], v) {
			t.Errorf("links[%s] = %v, was %v", k, links[k], v)
		}
	}
}

// A guard that reads no links does not need them: the earlier behavior of
// Transition is unchanged for every Type that declares none.
func TestTransitionLinked_NoLinkGuard_IgnoresLinks(t *testing.T) {
	ty := mustParse(t) // docType, from lifecycle_test.go: no link types
	fields := map[string]any{"f_owner": "user-ana"}
	if _, err := lifecycle.TransitionLinked(ty, "draft", fields, nil, "review"); err != nil {
		t.Errorf("nil links, no link guard: %v", err)
	}
	if _, err := lifecycle.TransitionLinked(ty, "draft", fields, lifecycle.Links{}, "review"); err != nil {
		t.Errorf("empty links, no link guard: %v", err)
	}
}
