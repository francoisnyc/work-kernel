// Tests for pli check, PR 2 (docs/roadmap.md, track 2): dead rules
// (UNREACHABLE_STATE, NEVER_FIRES), loops with no end (NO_WAY_OUT), a
// colleague editing the requester's draft (COLLEAGUE_EDITS_DRAFT), and the
// two fixes to PR 1 (a Type made only by another Type's reaction is
// checked; a multi-value principal field keeps an invariant that holds).
// The owner's decisions, Phase A of PR 2: the all-roles world for "it can
// never happen"; NO_WAY_OUT leaves out what can reach a stuck state and
// judges ways out under every world; a draft's role-limited fields are
// the Type's intent; a draft is a state whose every move out is allowed:
// [creator]; one finding per draft, its fields listed; depends_on
// ["scope:readers"].
package check_test

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/check"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// ofCode returns the findings of one code on one Type version.
func ofCode(r check.Report, id string, version int, code check.Code) []check.Finding {
	var out []check.Finding
	for _, f := range of(r, id, version) {
		if f.Code == code {
			out = append(out, f)
		}
	}
	return out
}

// A Type with dead rules. limbo and island are never reached: nothing
// moves an Item into limbo, and the only move into island is for nobody,
// with no reaction to make it. So limbo's timer, the moves out of limbo
// and island, the move for nobody, the hook on entering limbo and the
// reaction from limbo never happen. Each is an ERROR: the checker took
// every guard as true and let every class hold every role, and still
// nothing reaches them. A dead rule has no path: its witness is empty.
const deadType = `
id: t_dead
version: 1
initial_state: open
states:
  - {id: open, category: todo}
  - {id: done, category: done}
  - {id: limbo, category: active, timer: {after: 1h, to: done}}
  - {id: island, category: todo}
transitions:
  - {from: open, to: done}
  - {from: limbo, to: done}
  - {from: open, to: island, allowed: [nobody]}
  - {from: island, to: done}
hooks:
  - {id: on_limbo, on: enter, state: limbo, kind: action, action: email.notify, reads: [creator]}
reactions:
  - {id: from_limbo, kind: move, on: created_inside, of: [t_dead], from: [limbo], to: done}
`

func TestDeadRules(t *testing.T) {
	r := run(parse(t, deadType))
	got := of(r, "t_dead", 1)
	sameKeys(t, "t_dead@1", got, []key{
		{check.CodeUnreachableState, check.SeverityError, "states[2]", ""},
		{check.CodeUnreachableState, check.SeverityError, "states[3]", ""},
		{check.CodeNeverFires, check.SeverityError, "states[2].timer", ""},
		{check.CodeNeverFires, check.SeverityError, "transitions[1]", ""},
		{check.CodeNeverFires, check.SeverityError, "transitions[2]", ""},
		{check.CodeNeverFires, check.SeverityError, "transitions[3]", ""},
		{check.CodeNeverFires, check.SeverityError, "hooks[0]", ""},
		{check.CodeNeverFires, check.SeverityError, "reactions[0]", ""},
	})
	want := map[string]string{
		"states[2]":       "No Item can ever be in limbo: nothing leads there, whoever holds which role and whatever the guards say.",
		"states[2].timer": "The timer of limbo never fires: no Item can be in limbo.",
		"transitions[1]":  "The move from limbo to done never happens: no Item can be in limbo.",
		"transitions[2]":  "The move from open to island never happens: no one may make it, and no reaction does.",
		"hooks[0]":        "Hook `on_limbo` never runs: no Item can enter limbo.",
		"reactions[0]":    "Reaction `from_limbo` never runs: no Item can be in limbo.",
	}
	for _, f := range got {
		if len(f.Witness) != 0 || len(f.DependsOn) != 0 {
			t.Errorf("%s %s: witness %+v depends_on %v, want both empty", f.Code, f.Path, f.Witness, f.DependsOn)
		}
		if w, ok := want[f.Path]; ok && f.Message != w {
			t.Errorf("%s %s: message\n got %q\nwant %q", f.Code, f.Path, f.Message, w)
		}
	}
	if !r.HasErrors() {
		t.Error("HasErrors() = false with dead rules")
	}
}

// A rule that something else makes happen is not dead: approval@2's move
// for nobody (pending -> canceled) is made by access_request@2's
// close_on_* reactions, checked in the same folder; sprint@3's at-timer
// fires once f_end_at is set; vacation_request@4's timer fires.
func TestRulesOthersMakeHappenAreNotDead(t *testing.T) {
	r := runDir(t, typesDir)
	for _, tv := range []struct {
		id string
		v  int
	}{{"approval", 2}, {"sprint", 3}, {"vacation_request", 4}, {"access_request", 2}} {
		for _, code := range []check.Code{check.CodeNeverFires, check.CodeUnreachableState, check.CodeNoWayOut} {
			if fs := ofCode(r, tv.id, tv.v, code); len(fs) > 0 {
				t.Errorf("%s@%d: %s %+v", tv.id, tv.v, code, keys(fs))
			}
		}
	}
}

// A loop with no end: b and c lead only to each other. From a the Item
// may still end (done), so a is not reported. Both are ERRORs: no guard,
// role or value is needed to get there.
const loopType = `
id: t_loop
version: 1
initial_state: a
states:
  - {id: a, category: todo}
  - {id: b, category: active}
  - {id: c, category: active}
  - {id: done, category: done}
transitions:
  - {from: a, to: b}
  - {from: b, to: c}
  - {from: c, to: b}
  - {from: a, to: done}
`

func TestNoWayOut(t *testing.T) {
	r := run(parse(t, loopType))
	got := of(r, "t_loop", 1)
	sameKeys(t, "t_loop@1", got, []key{
		{check.CodeNoWayOut, check.SeverityError, "states[1]", ""},
		{check.CodeNoWayOut, check.SeverityError, "states[2]", ""},
	})
	for _, f := range got {
		if f.Path != "states[1]" {
			continue
		}
		want := []check.Step{
			{Who: "the requester", Kind: check.StepCreate, To: "a", Words: "The requester creates it in a."},
			{Who: "the requester", Kind: check.StepMove, From: "a", To: "b", Words: "The requester moves it from a to b."},
		}
		if !reflect.DeepEqual(f.Witness, want) {
			t.Errorf("witness\n got %+v\nwant %+v", f.Witness, want)
		}
		if w := "From b it can never reach a done or canceled state: every way out leads back into the loop."; f.Message != w {
			t.Errorf("message\n got %q\nwant %q", f.Message, w)
		}
	}
}

// What can reach a stuck state is the stuck state's finding, not a loop:
// camunda's access request for no system is stuck in owner_review, and
// its draft (which leads only there, at the top level) is not NO_WAY_OUT.
// And the ways out of a state are judged under every world: on the
// staff-role Types a draft nobody has filled is still left by a requester
// holding staff, so it is not NO_WAY_OUT either.
func TestNoWayOutLeavesStuckAndOtherWorldsAlone(t *testing.T) {
	for _, dir := range []string{camundaDir, staffRoleDir} {
		r := runDir(t, dir)
		for _, id := range []string{"system_access", "purchase"} {
			if fs := ofCode(r, id, 1, check.CodeNoWayOut); len(fs) > 0 {
				t.Errorf("%s %s@1: NO_WAY_OUT %+v", dir, id, keys(fs))
			}
		}
	}
}

// The all-roles world: "it can never happen" is claimed only when no one
// could make it happen even holding every role at once. On the staff-role
// purchase a rejection needs finance (the move) and staff (f_reason,
// given): fay holds both, and the attack script shows she rejects
// (results/kernel-staff-role.md, "fay rejects, giving a reason (must still
// work)"). So rejected is reachable and both rejections can happen.
func TestAllRolesWorldForNeverClaims(t *testing.T) {
	r := runDir(t, staffRoleDir)
	for _, code := range []check.Code{check.CodeUnreachableState, check.CodeNeverFires} {
		if fs := ofCode(r, "purchase", 1, code); len(fs) > 0 {
			t.Errorf("staff-role purchase@1: %s %+v", code, keys(fs))
		}
	}
}

// Fix 1 to PR 1: a Type no person can create (its invariant needs a field
// only the kernel writes) but another Type's create reaction makes is
// checked. t_step is made by t_case's reaction; its open way from dropped
// to done reaches a decision state (done, restricted to lead) with no
// role needed: an ERROR for an agent. PR 1 created no t_step and so found
// nothing.
const caseType = `
id: t_case
version: 1
initial_state: open
states:
  - {id: open, category: todo}
  - {id: done, category: done}
transitions:
  - {from: open, to: done, allowed: [creator]}
reactions:
  - {id: open_step, kind: create, on: enter, state: open, type: t_step, version: 1, fields: {f_title: '"a step"'}}
`

const stepType = `
id: t_step
version: 1
initial_state: todo
fields:
  - {id: f_title, type: string, writable_by: kernel}
states:
  - {id: todo, category: todo}
  - {id: done, category: done}
  - {id: dropped, category: canceled}
transitions:
  - {from: todo, to: done, allowed: ['role:lead']}
  - {from: todo, to: dropped}
  - {from: dropped, to: done}
accepts_reactions:
  't_case:open_step': {creates: [f_title]}
invariants:
  - {id: title_missing, rule: 'f_title != null'}
`

func TestReactionMadeTypeIsChecked(t *testing.T) {
	dir := t.TempDir()
	for name, src := range map[string]string{"t_case.v1.yaml": caseType, "t_step.v1.yaml": stepType} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	r := run(loadDir(t, dir)...)
	got := ofCode(r, "t_step", 1, check.CodeAgentDecides)
	var found bool
	for _, f := range got {
		if f.Path == "transitions[2]" && f.Severity == check.SeverityError {
			found = true
			if len(f.Witness) == 0 || f.Witness[0].Who != "a reaction" || f.Witness[0].Kind != check.StepCreate {
				t.Errorf("witness starts %+v, want a reaction's create", f.Witness)
			}
		}
	}
	if !found {
		t.Errorf("t_step@1: no AGENT_DECIDES ERROR on transitions[2]; got %+v", keys(of(r, "t_step", 1)))
	}
	for _, code := range []check.Code{check.CodeUnreachableState, check.CodeNeverFires} {
		if fs := ofCode(r, "t_step", 1, code); len(fs) > 0 {
			t.Errorf("t_step@1: %s %+v (the reaction makes it)", code, keys(fs))
		}
	}
}

// On types/: fulfilment_task@1, made by a resource request's reaction,
// is now checked: a desk agent (a person or an agent) completes it, and
// the requester holding desk_agent may complete their own.
func TestFulfilmentTaskIsChecked(t *testing.T) {
	r := runDir(t, typesDir)
	sameKeys(t, "fulfilment_task@1", of(r, "fulfilment_task", 1), []key{
		{check.CodeAgentDecides, check.SeverityWarning, "transitions[1]", "role:desk_agent"},
		{check.CodeRequesterDecides, check.SeverityWarning, "transitions[1]", "role:desk_agent"},
	})
}

// Fix 2 to PR 1: a multi-value principal field the requester fills keeps
// an invariant over its size. PR 1 wrapped the sample twice, the invariant
// never held, and nothing was created. With the fix the open move from
// pending to approved (a decision state: the approver's move restricts
// it) is an ERROR for an agent and for the requester.
const quorumType = `
id: t_quorum
version: 1
initial_state: draft
fields:
  - {id: f_approvers, type: principal, multiple: true, max: 4}
states:
  - {id: draft, category: todo}
  - {id: pending, category: waiting}
  - {id: approved, category: done}
  - {id: rejected, category: canceled}
transitions:
  - {from: draft, to: pending, allowed: [creator]}
  - {from: draft, to: approved, allowed: ['role:approver']}
  - {from: pending, to: approved}
  - {from: pending, to: rejected, allowed: ['role:approver']}
invariants:
  - {id: no_approvers, rule: 'f_approvers != null && size(f_approvers) >= 1'}
`

func TestMultiValueInvariantHolds(t *testing.T) {
	r := run(parse(t, quorumType))
	got := keys(of(r, "t_quorum", 1))
	for _, k := range []key{
		{check.CodeAgentDecides, check.SeverityError, "transitions[2]", ""},
		{check.CodeRequesterDecides, check.SeverityError, "transitions[2]", ""},
	} {
		if !slices.Contains(got, k) {
			t.Errorf("t_quorum@1: no %+v; got %+v", k, got)
		}
	}
}

// A colleague editing the requester's draft. draft is a draft: its only
// move out is the requester's (allowed: [creator]). Any person may write
// f_what and f_why there: one WARNING, the fields in the Type's order,
// depending on who can see it (scope:readers). f_note is limited to clerks,
// the Type's intent (refinement i); f_ref is the kernel's. review is not a
// draft: a clerk's move leaves it (refinement ii).
const draftType = `
id: t_draft
version: 1
initial_state: draft
fields:
  - {id: f_what, type: string}
  - {id: f_note, type: string, writable_by: ['role:clerk']}
  - {id: f_why,  type: string}
  - {id: f_ref,  type: datetime, writable_by: kernel}
states:
  - {id: draft, category: todo}
  - {id: review, category: waiting}
  - {id: done, category: done}
transitions:
  - {from: draft, to: review, allowed: [creator], sets: {f_ref: 'cause.at'}}
  - {from: review, to: done, allowed: ['role:clerk'], except: [creator]}
  - {from: review, to: draft, allowed: [creator]}
`

func TestColleagueEditsDraft(t *testing.T) {
	r := run(parse(t, draftType))
	got := ofCode(r, "t_draft", 1, check.CodeColleagueEditsDraft)
	want := []check.Finding{{
		Type: "t_draft", Version: 1,
		Code: check.CodeColleagueEditsDraft, Severity: check.SeverityWarning, Path: "states[0]",
		Message:   "While it is in draft, the requester's draft, a colleague may change `f_what` and `f_why`.",
		DependsOn: []string{"scope:readers"},
		Witness: []check.Step{
			{Who: "the requester", Kind: check.StepCreate, To: "draft", Words: "The requester creates it in draft."},
			{Who: "a colleague", Kind: check.StepWrite, From: "draft", To: "draft", Words: "A colleague changes `f_what` and `f_why`."},
		},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("t_draft@1 COLLEAGUE_EDITS_DRAFT:\n got %+v\nwant %+v", got, want)
	}
}

// Not a draft, so nothing to report: a draft whose fields only the
// requester may change (requester_only), and a state with an open move
// out (anyone may move it on: refinement ii).
func TestNotADraftOrClosed(t *testing.T) {
	ty := parse(t, draftType)
	ty.States[0].RequesterOnly = []string{"f_what", "f_why"}
	if fs := ofCode(run(ty), "t_draft", 1, check.CodeColleagueEditsDraft); len(fs) > 0 {
		t.Errorf("requester_only draft: %+v", keys(fs))
	}
	open := parse(t, draftType)
	open.Transitions = append(open.Transitions, typedef.Transition{From: "draft", To: "done"})
	if fs := ofCode(run(open), "t_draft", 1, check.CodeColleagueEditsDraft); len(fs) > 0 {
		t.Errorf("draft with an open move out: %+v", keys(fs))
	}
}

// access_request@2's pending is not a draft: its open, guarded moves out
// (approved, rejected by the approvals) are not the requester's alone.
// Its draft is: f_title and f_reason, which any person may write.
func TestAccessRequestDraftOnly(t *testing.T) {
	r := runDir(t, typesDir)
	sameKeys(t, "access_request@2 drafts", ofCode(r, "access_request", 2, check.CodeColleagueEditsDraft), []key{
		{check.CodeColleagueEditsDraft, check.SeverityWarning, "states[0]", "scope:readers"},
	})
	for _, f := range ofCode(r, "access_request", 2, check.CodeColleagueEditsDraft) {
		if w := "While it is in draft, the requester's draft, a colleague may change `f_title` and `f_reason`."; f.Message != w {
			t.Errorf("message\n got %q\nwant %q", f.Message, w)
		}
	}
}
