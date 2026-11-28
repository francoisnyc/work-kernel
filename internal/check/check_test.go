// Tests for pli check, PR 1 (docs/roadmap.md, track 2): stuck states and
// who can reach a decision, each with its shortest witness in plain words;
// UNCHECKED failing closed; the "not checked" lines; the text and JSON
// output. The bake-offs' match is golden_test.go.
package check_test

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/check"
	"github.com/francoisnyc/work-kernel/internal/contracts"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// Folders of Types, relative to this package.
const (
	typesDir     = "../../types"
	camundaDir   = "../../spikes/camunda-bakeoff/kernel/types"
	camundaV2Dir = "../../spikes/camunda-bakeoff/kernel/types-v2"
	blindDir     = "../../spikes/temporal-bakeoff/kernel-blind/types"
	staffRoleDir = "../../spikes/temporal-bakeoff/kernel-staff-role/types"
)

func loadDir(t *testing.T, dir string) []*typedef.Type {
	t.Helper()
	types, err := typedef.ParseDir(dir)
	if err != nil {
		t.Fatalf("parse %s: %v", dir, err)
	}
	return types
}

func parse(t *testing.T, src string) *typedef.Type {
	t.Helper()
	ty, err := typedef.Parse([]byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return ty
}

func run(types ...*typedef.Type) check.Report { return check.Check(types, contracts.Default()) }

func runDir(t *testing.T, dir string) check.Report {
	t.Helper()
	return check.Check(loadDir(t, dir), contracts.Default())
}

// of returns the findings on one Type version, in the report's order.
func of(r check.Report, id string, version int) []check.Finding {
	var out []check.Finding
	for _, f := range r.Findings {
		if f.Type == id && f.Version == version {
			out = append(out, f)
		}
	}
	return out
}

// key is what the tests compare when the witness is not the point: the
// code, the severity, where, and what a warning depends on (as a set).
type key struct {
	Code      check.Code
	Severity  check.Severity
	Path      string
	DependsOn string
}

func keyOf(f check.Finding) key {
	deps := slices.Clone(f.DependsOn)
	slices.Sort(deps)
	return key{f.Code, f.Severity, f.Path, strings.Join(deps, ", ")}
}

func keys(fs []check.Finding) []key {
	out := []key{}
	for _, f := range fs {
		out = append(out, keyOf(f))
	}
	return out
}

func sameKeys(t *testing.T, what string, got []check.Finding, want []key) {
	t.Helper()
	g := keys(got)
	sortKeys := func(ks []key) {
		slices.SortFunc(ks, func(a, b key) int {
			return strings.Compare(a.Path+string(a.Code)+a.DependsOn, b.Path+string(b.Code)+b.DependsOn)
		})
	}
	w := slices.Clone(want)
	if w == nil {
		w = []key{}
	}
	sortKeys(g)
	sortKeys(w)
	if !reflect.DeepEqual(g, w) {
		t.Errorf("%s: findings\n got %+v\nwant %+v", what, g, w)
	}
}

// The bake-off's "submit a request for no system" (results/attacks-kernel.md,
// FAIL): made at the top level, the request copies no owner on submit, the
// invariant lets it through (both copies are empty), and owner_review's
// moves are for whoever f_owner names: no one. Nothing in the path is a
// guard, a role or a field's value the path did not set: an ERROR.
func TestStuckAccessRequestForNoSystem(t *testing.T) {
	r := runDir(t, camundaDir)
	got := of(r, "system_access", 1)
	want := []check.Finding{{
		Type: "system_access", Version: 1,
		Code: check.CodeStuckState, Severity: check.SeverityError, Path: "states[1]",
		Message:   "It can stay in owner_review for ever: no one may move it on, and no timer or reaction does (`f_owner` names no one).",
		DependsOn: []string{},
		Witness: []check.Step{
			{Who: "the requester", Kind: check.StepCreate, To: "draft",
				Words: "The requester creates it in draft, outside any container."},
			{Who: "the requester", Kind: check.StepMove, From: "draft", To: "owner_review",
				Words: "The requester moves it from draft to owner_review."},
		},
	}, {
		// PR 2: a colleague may change what is asked in ana's draft.
		Type: "system_access", Version: 1,
		Code: check.CodeColleagueEditsDraft, Severity: check.SeverityWarning, Path: "states[0]",
		Message:   "While it is in draft, the requester's draft, a colleague may change `f_role`, `f_justification` and `f_reason`.",
		DependsOn: []string{"scope:readers"},
		Witness: []check.Step{
			{Who: "the requester", Kind: check.StepCreate, To: "draft", Words: "The requester creates it in draft."},
			{Who: "a colleague", Kind: check.StepWrite, From: "draft", To: "draft",
				Words: "A colleague changes `f_role`, `f_justification` and `f_reason`."},
		},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("system_access@1 (camunda):\n got %+v\nwant %+v", got, want)
	}
}

// The blind builder's access request passed all 18 attempts: its invariant
// submitted_outside_system refuses the submit when nothing was copied
// (f_submitted_at is always set by the submit, cause.at), so owner_review
// (states[1]) is never stuck. But the same refusal strands a request made
// at the top level: the Type has no start.in, no withdraw and kernel-only
// placement, so that draft can never leave draft. An ERROR on states[0]
// (the owner's change, Phase C: the checker found what the test missed).
func TestBlindRequestOutsideASystemStaysADraft(t *testing.T) {
	r := runDir(t, blindDir)
	got := of(r, "system_access", 1)
	sameKeys(t, "system_access@1 (blind)", got, []key{
		{check.CodeStuckState, check.SeverityError, "states[0]", ""},
		// PR 2: a colleague may change what is asked in the draft.
		{check.CodeColleagueEditsDraft, check.SeverityWarning, "states[0]", "scope:readers"},
	})
	for _, f := range got {
		if f.Path == "states[1]" {
			t.Errorf("owner_review is stuck: %s", f.Message)
		}
		if f.Path == "states[0]" && f.Code == check.CodeStuckState {
			want := []check.Step{{Who: "the requester", Kind: check.StepCreate, To: "draft",
				Words: "The requester creates it in draft, outside any container."}}
			if !reflect.DeepEqual(f.Witness, want) {
				t.Errorf("witness\n got %+v\nwant %+v", f.Witness, want)
			}
		}
	}
}

// The bake-off's agent given the finance role (camunda results,
// test2-compromised-kernel-agent-in-finance.md: claim, then approve, both
// FAIL). The Type lets any holder of finance claim and decide; an agent
// holding it does. It depends on who holds the role and on two guards: a
// WARNING. The manager's moves are for whoever f_manager names, which only
// the kernel writes (the hook's answer, always a person): no agent there.
// Rejecting needs f_reason given by the mover, which no agent may write:
// no finding on transitions[7].
func TestAgentHoldingFinanceDecides(t *testing.T) {
	r := runDir(t, camundaDir)
	got := of(r, "purchase", 1)
	want := []check.Finding{{
		// PR 2: a colleague may change what ana asks for in her draft.
		Type: "purchase", Version: 1,
		Code: check.CodeColleagueEditsDraft, Severity: check.SeverityWarning, Path: "states[0]",
		Message:   "While it is in draft, the requester's draft, a colleague may change `f_amount`, `f_vendor`, `f_cost_centre`, `f_justification` and `f_reason`.",
		DependsOn: []string{"scope:readers"},
		Witness: []check.Step{
			{Who: "the requester", Kind: check.StepCreate, To: "draft", Words: "The requester creates it in draft."},
			{Who: "a colleague", Kind: check.StepWrite, From: "draft", To: "draft",
				Words: "A colleague changes `f_amount`, `f_vendor`, `f_cost_centre`, `f_justification` and `f_reason`."},
		},
	}, {
		Type: "purchase", Version: 1,
		Code: check.CodeAgentDecides, Severity: check.SeverityWarning, Path: "transitions[6]",
		Message:   "An agent holding finance may move it from finance_review to approved, a decision.",
		DependsOn: []string{"guard:f_amount > 0", "guard:f_amount >= 1000", "role:finance"},
		Witness: []check.Step{
			{Who: "the requester", Kind: check.StepCreate, To: "draft",
				Words: "The requester creates it in draft."},
			{Who: "the requester", Kind: check.StepMove, From: "draft", To: "manager_review",
				Words: "The requester moves it from draft to manager_review, if `f_amount` is more than 0."},
			{Who: "whoever `f_manager` names", Kind: check.StepMove, From: "manager_review", To: "finance_queue",
				Words: "Whoever `f_manager` names moves it from manager_review to finance_queue, if `f_amount` is at least 1000."},
			{Who: "an agent holding finance", Kind: check.StepMove, From: "finance_queue", To: "finance_review",
				Words: "An agent holding finance moves it from finance_queue to finance_review."},
			{Who: "an agent holding finance", Kind: check.StepMove, From: "finance_review", To: "approved",
				Words: "An agent holding finance moves it from finance_review to approved."},
		},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("purchase@1 (camunda):\n got %+v\nwant %+v", got, want)
	}
}

// purchase@2 adds the CFO over 5,000: an agent holding finance approves up
// to 5,000, and an agent holding cfo approves above it, after a person in
// finance sent it on. Both WARNINGs, each naming its role and guards.
func TestAgentHoldingCFODecides(t *testing.T) {
	r := runDir(t, camundaV2Dir)
	sameKeys(t, "purchase@2", of(r, "purchase", 2), []key{
		{check.CodeColleagueEditsDraft, check.SeverityWarning, "states[0]", "scope:readers"}, // PR 2
		{check.CodeAgentDecides, check.SeverityWarning, "transitions[6]",
			"guard:f_amount <= 5000, guard:f_amount > 0, guard:f_amount >= 1000, role:finance"},
		{check.CodeAgentDecides, check.SeverityWarning, "transitions[9]",
			"guard:f_amount > 0, guard:f_amount > 5000, guard:f_amount >= 1000, role:cfo, role:finance"},
	})
	// The witness of the CFO's decision: a person in finance sends it on,
	// then the agent decides.
	for _, f := range of(r, "purchase", 2) {
		if f.Path != "transitions[9]" {
			continue
		}
		var who []string
		for _, s := range f.Witness {
			who = append(who, s.Who)
		}
		wantWho := []string{"the requester", "the requester", "whoever `f_manager` names",
			"a colleague holding finance", "a colleague holding finance", "an agent holding cfo"}
		if !slices.Equal(who, wantWho) {
			t.Errorf("transitions[9] witness: who\n got %q\nwant %q", who, wantWho)
		}
	}
}

// A test Type with a hole: an open move from draft straight to approved,
// beside the approver's move. approved is a decision state (the approver's
// move restricts it), so anyone taking the open move is a finding, and it
// needs no guard, role or value: an ERROR, for an agent and for the
// requester. The approver's move is a WARNING for an agent holding
// approver; the requester is excluded by except: [creator]. withdrawn is
// entered only by moves that name creator: the Type's intent, no finding,
// for the requester or an agent acting for them.
const decideType = `
id: t_decide
version: 1
initial_state: draft
states:
  - {id: draft, category: todo}
  - {id: review, category: waiting}
  - {id: approved, category: done}
  - {id: withdrawn, category: canceled}
transitions:
  - {from: draft, to: review, allowed: [creator]}
  - {from: review, to: approved, allowed: ['role:approver'], except: [creator]}
  - {from: draft, to: approved}
  - {from: draft, to: withdrawn, allowed: [creator]}
  - {from: review, to: withdrawn, allowed: [creator]}
`

func TestDecisionWithNoRoleIsAnError(t *testing.T) {
	r := run(parse(t, decideType))
	got := of(r, "t_decide", 1)
	sameKeys(t, "t_decide@1", got, []key{
		{check.CodeAgentDecides, check.SeverityError, "transitions[2]", ""},
		{check.CodeRequesterDecides, check.SeverityError, "transitions[2]", ""},
		{check.CodeAgentDecides, check.SeverityWarning, "transitions[1]", "role:approver"},
	})
	for _, f := range got {
		if f.Path == "transitions[2]" && f.Code == check.CodeAgentDecides {
			want := []check.Step{
				{Who: "the requester", Kind: check.StepCreate, To: "draft", Words: "The requester creates it in draft."},
				{Who: "an agent", Kind: check.StepMove, From: "draft", To: "approved", Words: "An agent moves it from draft to approved."},
			}
			if !reflect.DeepEqual(f.Witness, want) {
				t.Errorf("agent's witness\n got %+v\nwant %+v", f.Witness, want)
			}
		}
	}
	if !r.HasErrors() {
		t.Error("HasErrors() = false with two ERROR findings")
	}
}

// The requester holding hr_admin closes their own leave balance
// (leave_balance@1: open -> closed for role hr_admin, no except). A
// WARNING naming the role; an agent holding hr_admin likewise.
func TestRequesterHoldingTheRoleDecides(t *testing.T) {
	r := runDir(t, typesDir)
	sameKeys(t, "leave_balance@1", of(r, "leave_balance", 1), []key{
		{check.CodeAgentDecides, check.SeverityWarning, "transitions[0]", "role:hr_admin"},
		{check.CodeRequesterDecides, check.SeverityWarning, "transitions[0]", "role:hr_admin"},
	})
	for _, f := range of(r, "leave_balance", 1) {
		if f.Code == check.CodeRequesterDecides {
			want := "The requester holding hr_admin may move it from open to closed, a decision on their own request."
			if f.Message != want {
				t.Errorf("message\n got %q\nwant %q", f.Message, want)
			}
		}
	}
}

// A claim carries the except of the move that took it: equipment_request@2
// lets any desk_agent fulfil, with no except, but only the claimant moves
// on from in_hand, and taking it says except: [creator]. The requester
// never decides it; an agent holding desk_agent does (a WARNING).
func TestClaimCarriesTheExcept(t *testing.T) {
	r := runDir(t, typesDir)
	for _, f := range of(r, "equipment_request", 2) {
		if f.Code == check.CodeRequesterDecides {
			t.Errorf("equipment_request@2: %s %s: %s (the claim is never the requester's)", f.Code, f.Path, f.Message)
		}
	}
	var agent bool
	for _, f := range of(r, "equipment_request", 2) {
		if f.Code == check.CodeAgentDecides && slices.Contains(f.DependsOn, "role:desk_agent") {
			agent = true
		}
	}
	if !agent {
		t.Error("equipment_request@2: no AGENT_DECIDES naming role:desk_agent")
	}
}

// Something the checker cannot model is UNCHECKED, an ERROR: never a silent
// clean. Here an allowed entry no Type may have (load would refuse it; the
// checker is handed the Type as data).
func TestUncheckedFailsClosed(t *testing.T) {
	ty := parse(t, decideType)
	ty.Transitions[1].Allowed = []string{"group:approvers"}
	r := run(ty)
	var found bool
	for _, f := range of(r, "t_decide", 1) {
		if f.Code == check.CodeUnchecked {
			found = true
			if f.Severity != check.SeverityError || f.Path != "transitions[1].allowed[0]" {
				t.Errorf("UNCHECKED: severity %s path %s, want ERROR transitions[1].allowed[0]", f.Severity, f.Path)
			}
		}
	}
	if !found {
		t.Errorf("no UNCHECKED finding; findings: %+v", keys(of(r, "t_decide", 1)))
	}
	if !r.HasErrors() {
		t.Error("HasErrors() = false with an UNCHECKED finding")
	}
}

// Every report says what it does not look at, and what it assumes, even
// when it finds nothing.
func TestNotCheckedLines(t *testing.T) {
	r := run(parse(t, `
id: t_clean
version: 1
initial_state: open
states:
  - {id: open, category: todo}
  - {id: done, category: done}
transitions:
  - {from: open, to: done}
`))
	if len(r.Findings) != 0 {
		t.Errorf("findings on a clean Type: %+v", keys(r.Findings))
	}
	if r.HasErrors() {
		t.Error("HasErrors() on a clean Type")
	}
	for _, want := range []string{
		"who reads it: scopes decide, not the Type",
		"field writes outside drafts",
		"placement: who moves or narrows an Item",
	} {
		if !slices.Contains(r.NotChecked, want) {
			t.Errorf("NotChecked %q: missing %q", r.NotChecked, want)
		}
	}
	wantAssumed := "a principal field only the kernel writes names a person, never an agent; it is empty only where its hook's contract allows no answer"
	if !slices.Contains(r.Assumed, wantAssumed) {
		t.Errorf("Assumed %q: missing %q", r.Assumed, wantAssumed)
	}
}

// The text a person reads: per Type, each finding with its depends-on line
// and witness; then what is not checked and assumed; then the counts.
func TestText(t *testing.T) {
	got := run(parse(t, decideType)).Text()
	want := `t_decide@1
  AGENT_DECIDES ERROR transitions[2]: An agent may move it from draft to approved, a decision.
    1. The requester creates it in draft.
    2. An agent moves it from draft to approved.
  REQUESTER_DECIDES ERROR transitions[2]: The requester may move it from draft to approved, a decision on their own request.
    1. The requester creates it in draft.
    2. The requester moves it from draft to approved.
  AGENT_DECIDES WARNING transitions[1]: An agent holding approver may move it from review to approved, a decision.
    Depends on: role:approver.
    1. The requester creates it in draft.
    2. The requester moves it from draft to review.
    3. An agent holding approver moves it from review to approved.
Not checked: who reads it: scopes decide, not the Type; field writes outside drafts; placement: who moves or narrows an Item.
Assumed: a principal field only the kernel writes names a person, never an agent; it is empty only where its hook's contract allows no answer.
2 errors, 1 warning.
`
	if got != want {
		t.Errorf("Text()\n got:\n%s\nwant:\n%s", got, want)
	}
}

// A clean report still prints what is not checked, then "No findings."
func TestTextClean(t *testing.T) {
	got := run(parse(t, `
id: t_clean
version: 1
initial_state: open
states:
  - {id: open, category: todo}
  - {id: done, category: done}
transitions:
  - {from: open, to: done}
`)).Text()
	if !strings.HasSuffix(got, "No findings.\n") || !strings.Contains(got, "Not checked: who reads it") {
		t.Errorf("Text() of a clean report:\n%s", got)
	}
}

// The JSON a tool reads: the report's own fields, depends_on never null,
// from left out of a create.
func TestJSON(t *testing.T) {
	data, err := json.Marshal(run(parse(t, decideType)))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Findings   []map[string]any `json:"findings"`
		NotChecked []string         `json:"not_checked"`
		Assumed    []string         `json:"assumed"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Findings) != 3 || len(doc.NotChecked) == 0 || len(doc.Assumed) == 0 {
		t.Fatalf("JSON: %s", data)
	}
	f := doc.Findings[0]
	for _, k := range []string{"type", "version", "code", "severity", "path", "message", "depends_on", "witness"} {
		if _, ok := f[k]; !ok {
			t.Errorf("finding has no %q: %s", k, data)
		}
	}
	if deps, ok := f["depends_on"].([]any); !ok || len(deps) != 0 {
		t.Errorf("depends_on of an ERROR = %v, want []", f["depends_on"])
	}
	steps, _ := f["witness"].([]any)
	if len(steps) == 0 {
		t.Fatalf("no witness: %s", data)
	}
	first, _ := steps[0].(map[string]any)
	if _, ok := first["from"]; ok || first["kind"] != check.StepCreate {
		t.Errorf("first step = %v, want a create with no from", first)
	}
}

// It runs on every Type in types/ and on every bake-off's Types: nothing
// there is beyond the model (no UNCHECKED), and a second run gives the same
// report.
func TestRunsOnEveryType(t *testing.T) {
	for _, dir := range []string{typesDir, camundaDir, camundaV2Dir, blindDir, staffRoleDir} {
		t.Run(dir, func(t *testing.T) {
			types := loadDir(t, dir)
			r := check.Check(types, contracts.Default())
			for _, f := range r.Findings {
				if f.Code == check.CodeUnchecked {
					t.Errorf("%s@%d: UNCHECKED %s: %s", f.Type, f.Version, f.Path, f.Message)
				}
				if len(f.Witness) == 0 || f.Witness[0].Kind != check.StepCreate {
					t.Errorf("%s@%d %s %s: a witness starts with the Item's creation: %+v", f.Type, f.Version, f.Code, f.Path, f.Witness)
				}
				if f.Severity == check.SeverityWarning && len(f.DependsOn) == 0 {
					t.Errorf("%s@%d %s %s: a WARNING names what it depends on", f.Type, f.Version, f.Code, f.Path)
				}
			}
			if len(r.NotChecked) == 0 {
				t.Error("no NotChecked lines")
			}
			if again := check.Check(types, contracts.Default()); !reflect.DeepEqual(again, r) {
				t.Error("a second run gave another report")
			}
		})
	}
}
