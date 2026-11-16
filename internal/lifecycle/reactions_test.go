// Tests for proof-of-concept Step 2, part 2: the pure rules of link
// reactions and invariants.
//
//   - A held reaction holds the transitions into its state from its `from`
//     states; HeldReaction finds it. Its undo is the after reaction that
//     `undoes` it (UndoOf), which also runs on its own trigger
//     (UndoOnEntry: entering its state from its `from` states).
//   - ReactionAdds evaluates a held reaction's add expressions over the
//     source's fields; a null field is an error, never zero.
//   - While a held reaction's hold is open, the source fields its
//     expressions read are frozen (UpdateDuringHold), as a check's reads
//     are; only an escape leaves the held state.
//   - CheckInvariants: every invariant must hold on the fields a write
//     leaves; the first that does not, in the order declared, refuses with
//     InvariantFailed naming it; a rule that cannot be evaluated (a null)
//     does not hold.
//   - A Type with reactions has listeners, and entering a state that runs
//     an after reaction starts the workflow.
package lifecycle_test

import (
	"errors"
	"maps"
	"strings"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/lifecycle"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// pledge: paying a pledge takes its amount from the jar it links to (held),
// and dropping a paid pledge gives it back. f_amount is not frozen by any
// state, so only the hold freezes it. No hooks and no timers.
const pledgeYAML = `
id: pledge
version: 1
initial_state: open
fields:
  - {id: f_amount, type: int}
  - {id: f_note,   type: string}
states:
  - {id: open,    category: todo}
  - {id: filed,   category: active}
  - {id: paid,    category: done}
  - {id: dropped, category: canceled}
transitions:
  - {from: open,  to: filed}
  - {from: filed, to: open}
  - {from: filed, to: paid}
  - {from: filed, to: dropped, escape: true}
  - {from: paid,  to: dropped}
links:
  - {name: jar, inverse: pledges, targets: [jar], max: one, writable_by: kernel,
     resolve: {on_enter: filed, field: f_holder, equals: creator, state: active}}
reactions:
  - {id: take, kind: held, on: enter, state: paid, from: [filed], link: jar,
     add: {f_level: f_amount, f_count: '1'}}
  - {id: give_back, kind: after, on: enter, state: dropped, from: [paid], undoes: take}
`

// jar: accepts the pledge's reactions; its invariants say the level stays
// within the cap and the count is never negative.
const jarYAML = `
id: jar
version: 1
initial_state: active
fields:
  - {id: f_holder, type: principal}
  - {id: f_cap,    type: int}
  - {id: f_level,  type: int, writable_by: kernel}
  - {id: f_count,  type: int, writable_by: kernel}
states:
  - {id: active,  category: active}
  - {id: retired, category: done}
transitions:
  - {from: active, to: retired}
invariants:
  - {id: no_cap,    rule: 'f_cap != null'}
  - {id: over_cap,  rule: 'f_level == null || f_level <= f_cap'}
  - {id: negative,  rule: 'f_count == null || f_count >= 0'}
accepts_reactions:
  pledge:take:      [f_level, f_count]
  pledge:give_back: [f_level, f_count]
`

func parseYAML(t *testing.T, def string) *typedef.Type {
	t.Helper()
	ty, err := typedef.Parse([]byte(def))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return ty
}

func TestHeldReaction(t *testing.T) {
	ty := parseYAML(t, pledgeYAML)
	if r, ok := lifecycle.HeldReaction(ty, "filed", "paid"); !ok || r.ID != "take" {
		t.Errorf("HeldReaction(filed, paid) = %q, %v; want take", r.ID, ok)
	}
	for _, move := range [][2]string{{"open", "filed"}, {"paid", "dropped"}, {"filed", "dropped"}, {"filed", "open"}} {
		if r, ok := lifecycle.HeldReaction(ty, move[0], move[1]); ok {
			t.Errorf("HeldReaction(%s, %s) = %q; want none (an after reaction holds nothing)", move[0], move[1], r.ID)
		}
	}
}

func TestUndoOfAndOnEntry(t *testing.T) {
	ty := parseYAML(t, pledgeYAML)
	if r, ok := lifecycle.UndoOf(ty, "take"); !ok || r.ID != "give_back" {
		t.Errorf("UndoOf(take) = %q, %v; want give_back", r.ID, ok)
	}
	if r, ok := lifecycle.UndoOf(ty, "give_back"); ok {
		t.Errorf("UndoOf(give_back) = %q; want none", r.ID)
	}
	if r, ok := lifecycle.UndoOnEntry(ty, "paid", "dropped"); !ok || r.ID != "give_back" {
		t.Errorf("UndoOnEntry(paid, dropped) = %q, %v; want give_back", r.ID, ok)
	}
	// Dropping a filed pledge is the escape from the take hold: its undo
	// runs because the hold closed, not on entry.
	if r, ok := lifecycle.UndoOnEntry(ty, "filed", "dropped"); ok {
		t.Errorf("UndoOnEntry(filed, dropped) = %q; want none", r.ID)
	}
}

func TestReactionAdds(t *testing.T) {
	ty := parseYAML(t, pledgeYAML)
	take, _ := ty.Reaction("take")
	got, err := lifecycle.ReactionAdds(take, map[string]any{"f_amount": 5, "f_note": "x"})
	if err != nil {
		t.Fatalf("ReactionAdds: %v", err)
	}
	if want := map[string]int64{"f_level": 5, "f_count": 1}; !maps.Equal(got, want) {
		t.Errorf("ReactionAdds = %v, want %v", got, want)
	}
	// A number read back from JSON is a float64.
	if got, err := lifecycle.ReactionAdds(take, map[string]any{"f_amount": float64(3)}); err != nil || got["f_level"] != 3 {
		t.Errorf("ReactionAdds(f_amount 3.0) = %v, %v; want f_level 3", got, err)
	}
	for _, fields := range []map[string]any{{"f_amount": nil}, {}} {
		if got, err := lifecycle.ReactionAdds(take, fields); err == nil {
			t.Errorf("ReactionAdds(%v) = %v; want an error: a null amount is not zero", fields, got)
		}
	}
}

// 1e: while the take hold is open, f_amount (which its add reads) is frozen;
// f_note is not.
func TestUpdateDuringHold_AReactionsReadsAreFrozen(t *testing.T) {
	ty := parseYAML(t, pledgeYAML)
	h := lifecycle.Hold{Hook: "take", From: "filed", To: "paid"}
	err := lifecycle.UpdateDuringHold(ty, h, []string{"f_amount", "f_note"})
	var rej *lifecycle.Rejection
	if !errors.As(err, &rej) || rej.Code != lifecycle.Held {
		t.Fatalf("UpdateDuringHold(f_amount) = %v, want a Held rejection", err)
	}
	if !strings.Contains(rej.Reason, "f_amount") || strings.Contains(rej.Reason, "f_note") {
		t.Errorf("reason %q should name f_amount only", rej.Reason)
	}
	if err := lifecycle.UpdateDuringHold(ty, h, []string{"f_note"}); err != nil {
		t.Errorf("UpdateDuringHold(f_note) = %v, want nil", err)
	}
}

func TestTransitionDuringHold_AReactionsHold(t *testing.T) {
	ty := parseYAML(t, pledgeYAML)
	h := lifecycle.Hold{Hook: "take", From: "filed", To: "paid"}
	if err := lifecycle.TransitionDuringHold(ty, h, "dropped"); err != nil {
		t.Errorf("the escape filed -> dropped = %v, want nil", err)
	}
	if err := lifecycle.TransitionDuringHold(ty, h, "open"); err == nil {
		t.Error("filed -> open during the hold = nil, want Held")
	}
}

func TestCheckInvariants(t *testing.T) {
	jar := parseYAML(t, jarYAML)
	if err := lifecycle.CheckInvariants(jar, map[string]any{"f_cap": 10, "f_level": 10, "f_count": 2}); err != nil {
		t.Errorf("at the cap: %v, want nil", err)
	}
	if err := lifecycle.CheckInvariants(jar, map[string]any{"f_cap": 10}); err != nil {
		t.Errorf("a new jar, f_level and f_count unset: %v, want nil (the rules allow null)", err)
	}
	cases := []struct {
		name   string
		fields map[string]any
		want   string
	}{
		{"over the cap", map[string]any{"f_cap": 10, "f_level": 11}, "over_cap"},
		{"a negative count", map[string]any{"f_cap": 10, "f_count": -1}, "negative"},
		// The first that fails, in the order declared.
		{"over the cap and negative", map[string]any{"f_cap": 10, "f_level": 11, "f_count": -1}, "over_cap"},
		{"no cap at all", map[string]any{"f_level": 1}, "no_cap"},
		// over_cap reads f_cap, which is null: the rule cannot be
		// evaluated, so it does not hold (no_cap is checked first here, so
		// a Type without it shows the fail-safe below).
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assertInvariantFailed(t, lifecycle.CheckInvariants(jar, c.fields), c.want)
		})
	}

	// Fail safe: a rule over a null field that does not test for null.
	strict := parseYAML(t, `
id: strict
version: 1
fields:
  - {id: f_n, type: int}
invariants:
  - {id: positive, rule: 'f_n > 0'}
`)
	assertInvariantFailed(t, lifecycle.CheckInvariants(strict, map[string]any{}), "positive")
	assertInvariantFailed(t, lifecycle.CheckInvariants(strict, map[string]any{"f_n": nil}), "positive")
	if err := lifecycle.CheckInvariants(strict, map[string]any{"f_n": float64(2)}); err != nil {
		t.Errorf("f_n = 2.0 (as read back from JSON): %v, want nil", err)
	}

	// A Type without invariants accepts anything its schema does.
	if err := lifecycle.CheckInvariants(parseYAML(t, pledgeYAML), map[string]any{"f_amount": -5}); err != nil {
		t.Errorf("no invariants: %v, want nil", err)
	}
}

func assertInvariantFailed(t *testing.T, err error, invariant string) {
	t.Helper()
	var rej *lifecycle.Rejection
	if !errors.As(err, &rej) || rej.Code != lifecycle.InvariantFailed {
		t.Fatalf("error = %v, want an InvariantFailed rejection", err)
	}
	if rej.Invariant != invariant {
		t.Errorf("invariant = %q, want %q", rej.Invariant, invariant)
	}
	if !strings.Contains(rej.Reason, invariant) {
		t.Errorf("reason %q should name the invariant %s", rej.Reason, invariant)
	}
}

// Reactions are behavior: a Type with reactions and no hooks or timers
// still has listeners (its events must reach the workflow), and entering
// a state that runs an after reaction starts it. A Type that only accepts
// reactions needs no workflow of its own.
func TestListeners_Reactions(t *testing.T) {
	pledge, jar := parseYAML(t, pledgeYAML), parseYAML(t, jarYAML)
	if !lifecycle.HasListeners(pledge) {
		t.Error("HasListeners(pledge) = false, want true: it has reactions")
	}
	if lifecycle.HasListeners(jar) {
		t.Error("HasListeners(jar) = true, want false: accepting reactions needs no workflow")
	}
	starts := []lifecycle.Event{
		{Op: lifecycle.OpTransitionRequested},                // the take hold
		{Op: "transition", From: "paid", Entered: "dropped"}, // give_back's own trigger
	}
	for _, ev := range starts {
		if !lifecycle.StartsWorkflow(pledge, ev) {
			t.Errorf("StartsWorkflow(%+v) = false, want true", ev)
		}
	}
	if lifecycle.StartsWorkflow(pledge, lifecycle.Event{Op: "transition", From: "open", Entered: "filed"}) {
		t.Error("StartsWorkflow(open -> filed) = true, want false: nothing runs there")
	}
}

// The repository's vacation request: withdrawing an approved request starts
// the workflow (it runs refund), and approving opens a hold (deduct).
func TestListeners_VacationRequestV2(t *testing.T) {
	types, err := typedef.ParseDir("../../types")
	if err != nil {
		t.Fatalf("ParseDir: %v", err)
	}
	var v2 *typedef.Type
	for _, ty := range types {
		if ty.ID == "vacation_request" && ty.Version == 2 {
			v2 = ty
		}
	}
	if v2 == nil {
		t.Fatal("types/ has no vacation_request v2")
	}
	if r, ok := lifecycle.HeldReaction(v2, "submitted", "approved"); !ok || r.ID != "deduct" {
		t.Errorf("HeldReaction(submitted, approved) = %q, %v; want deduct", r.ID, ok)
	}
	if !lifecycle.StartsWorkflow(v2, lifecycle.Event{Op: "transition", From: "approved", Entered: "withdrawn"}) {
		t.Error("StartsWorkflow(approved -> withdrawn) = false, want true: refund runs there")
	}
}
