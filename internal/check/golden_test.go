// The bake-off match (pli check, PR 1; the owner's rule, Phase A): on the
// bake-offs' Types, every attack row the scripts recorded as FAIL is found
// by a finding or explained; no finding contradicts a row recorded as
// refused; and every finding beyond the rows is listed, with why. The
// rows are the results files', quoted.
package check_test

import (
	"fmt"
	"slices"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/check"
)

// expected is one finding the checker must report on a bake-off's Type:
// the row it matches, or why it is there beyond the rows.
type expected struct {
	dir     string
	id      string
	version int
	key     key
	why     string
}

var bakeoffFindings = []expected{
	{camundaDir, "system_access", 1,
		key{check.CodeStuckState, check.SeverityError, "states[1]", ""},
		"temporal-bakeoff/results/attacks-kernel.md: submit a request for no system (nobody would own the decision): FAIL"},
	{camundaDir, "purchase", 1,
		key{check.CodeAgentDecides, check.SeverityWarning, "transitions[6]", "guard:f_amount > 0, guard:f_amount >= 1000, role:finance"},
		"camunda-bakeoff/results/test2-compromised-kernel-agent-in-finance.md: claim from the finance queue, as itself; approve it (finance's move), as itself: FAIL"},

	{camundaV2Dir, "system_access", 1,
		key{check.CodeStuckState, check.SeverityError, "states[1]", ""},
		"extra: the same file as camunda's types/system_access.v1.yaml; the same row"},
	{camundaV2Dir, "purchase", 1,
		key{check.CodeAgentDecides, check.SeverityWarning, "transitions[6]", "guard:f_amount > 0, guard:f_amount >= 1000, role:finance"},
		"extra: the same file as camunda's types/purchase.v1.yaml; the same row"},
	{camundaV2Dir, "purchase", 2,
		key{check.CodeAgentDecides, check.SeverityWarning, "transitions[6]", "guard:f_amount <= 5000, guard:f_amount > 0, guard:f_amount >= 1000, role:finance"},
		"extra: v2's finance approval up to 5,000, the shape of the agent-in-finance row; not attacked on v2"},
	{camundaV2Dir, "purchase", 2,
		key{check.CodeAgentDecides, check.SeverityWarning, "transitions[9]", "guard:f_amount > 0, guard:f_amount > 5000, guard:f_amount >= 1000, role:cfo, role:finance"},
		"extra: an agent given cfo decides above 5,000; no row gave an agent cfo"},

	{blindDir, "purchase", 1,
		key{check.CodeAgentDecides, check.SeverityWarning, "transitions[6]", "guard:f_amount > 0, guard:f_amount >= 1000, role:finance"},
		"extra: the blind builder's purchase has the same moves; the agent-in-finance row was run on camunda's Types only"},

	{staffRoleDir, "system_access", 1,
		key{check.CodeStuckState, check.SeverityWarning, "states[1]", "role:staff"},
		"extra: the original access request with f_role written by role staff only; the row for no system was not run on these Types, and the requester must hold staff to submit (a WARNING)"},
	{staffRoleDir, "purchase", 1,
		key{check.CodeAgentDecides, check.SeverityWarning, "transitions[6]", "guard:f_amount > 0, guard:f_amount >= 1000, role:finance, role:staff"},
		"extra: the agent-in-finance row's shape; the requester must hold staff to fill the purchase"},
}

// row is one attack row the scripts recorded, and what the checker says
// of it: the finding that matches it (a key in bakeoffFindings), or why no
// finding should.
type row struct {
	file, attempt, result string
	dir, id               string
	version               int
	finding               *key   // a FAIL row found
	absent                []key  // findings that must not be there, whatever their severity (a refused row): code and path only
	why                   string // a row explained without a finding
}

func ptr(k key) *key { return &k }

var bakeoffRows = []row{
	// The remaining fails.
	{file: "temporal-bakeoff/results/attacks-kernel.md", attempt: "submit a request for no system (nobody would own the decision)", result: "FAIL",
		dir: camundaDir, id: "system_access", version: 1,
		finding: ptr(key{check.CodeStuckState, check.SeverityError, "states[1]", ""})},
	{file: "camunda-bakeoff/results/test2-compromised-kernel-agent-in-finance.md", attempt: "claim from the finance queue, as itself / approve it (finance's move), as itself", result: "FAIL",
		dir: camundaDir, id: "purchase", version: 1,
		finding: ptr(key{check.CodeAgentDecides, check.SeverityWarning, "transitions[6]", "guard:f_amount > 0, guard:f_amount >= 1000, role:finance"})},
	{file: "temporal-bakeoff/results/attacks-review-kernel.md", attempt: "a colleague with no part in it reads ana's purchase (and its history)", result: "FAIL",
		dir: camundaDir, id: "purchase", version: 1,
		why: "who reads an Item is its scope's readers, data in Postgres, not the Type: the report's NotChecked line 'who reads it: scopes decide, not the Type'"},

	// Refused rows that a finding could contradict.
	{file: "temporal-bakeoff/results/attacks-kernel.md", attempt: "the owner approves her own request (fay asks for the ledger, which she owns)", result: "PASS (refused)",
		dir: camundaDir, id: "system_access", version: 1,
		absent: []key{
			{Code: check.CodeRequesterDecides, Path: "transitions[1]"},
		}},
	{file: "temporal-bakeoff/results/attacks-kernel.md", attempt: "approve an access request (the owner's step), as itself / approve acting for the owner (olaf), with his grant", result: "PASS (refused)",
		dir: camundaDir, id: "system_access", version: 1,
		absent: []key{
			{Code: check.CodeAgentDecides, Path: "transitions[1]"},
		}},
	{file: "camunda-bakeoff/results/test2-compromised-kernel-requester.md", attempt: "bob submits a purchase and approves it as manager", result: "PASS (refused)",
		dir: camundaDir, id: "purchase", version: 1,
		absent: []key{
			{Code: check.CodeRequesterDecides, Path: "transitions[1]"},
		}},
	{file: "temporal-bakeoff/results/attacks-kernel.md", attempt: "approve a small purchase (manager's move), as itself / acting for the manager (bob)", result: "PASS (refused)",
		dir: camundaDir, id: "purchase", version: 1,
		absent: []key{
			{Code: check.CodeAgentDecides, Path: "transitions[1]"},
		}},
	{file: "temporal-bakeoff/results/attacks-kernel.md", attempt: "approve a claimed purchase acting for finance (fay)", result: "PASS (refused)",
		dir: camundaDir, id: "purchase", version: 1,
		why: "the agent holds no role of its own there: the AGENT_DECIDES on transitions[6] names role:finance, the agent's own (an agent has its user's rights only where it also has its own)"},
	{file: "temporal-bakeoff/results/attacks-kernel.md", attempt: "plant a rejection reason on a purchase finance is reviewing", result: "PASS (refused)",
		dir: camundaDir, id: "purchase", version: 1,
		absent: []key{
			{Code: check.CodeAgentDecides, Path: "transitions[7]"},
		}},
	{file: "temporal-bakeoff/results/attacks-review-kernel.md", attempt: "a colleague in finance can take over a purchase fay claimed and left", result: "PASS (can)",
		dir: camundaDir, id: "purchase", version: 1,
		absent: []key{
			{Code: check.CodeStuckState, Path: "states[3]"},
		}},
	{file: "temporal-bakeoff/results/attacks-kernel-blind.md", attempt: "submit a request for no system (nobody would own the decision)", result: "PASS (refused)",
		dir: blindDir, id: "system_access", version: 1,
		absent: []key{
			{Code: check.CodeStuckState, Path: "states[1]"},
		}},

	// Rows the checker does not model in PR 1, explained.
	{file: "temporal-bakeoff/results/attacks-kernel.md", attempt: "field writes: f_manager, f_claimed_by, f_amount after submit, f_order_id, f_reason, f_owner, f_role, ...", result: "PASS (refused)",
		dir: camundaDir, id: "purchase", version: 1,
		why: "field writes are not modelled: NotChecked 'field writes'"},
	{file: "temporal-bakeoff/results/attacks-kernel.md", attempt: "hide a purchase from its approvers / move the request to a system it could reach", result: "PASS (refused)",
		dir: camundaDir, id: "purchase", version: 1,
		why: "placement is not modelled: NotChecked 'placement: who moves or narrows an Item'"},
	{file: "temporal-bakeoff/results/kernel-staff-role.md", attempt: "a colleague (olaf, staff) changes ana's draft", result: "PASS (refused)",
		dir: staffRoleDir, id: "purchase", version: 1,
		why: "a colleague editing a draft is PR 2 (COLLEAGUE_EDITS_DRAFT)"},
}

func TestBakeoffMatch(t *testing.T) {
	reports := map[string]check.Report{}
	for _, dir := range []string{camundaDir, camundaV2Dir, blindDir, staffRoleDir} {
		reports[dir] = runDir(t, dir)
	}

	// 1. The findings on every bake-off Type are exactly the expected ones.
	for _, dir := range []string{camundaDir, camundaV2Dir, blindDir, staffRoleDir} {
		for _, ty := range loadDir(t, dir) {
			var want []key
			for _, e := range bakeoffFindings {
				if e.dir == dir && e.id == ty.ID && e.version == ty.Version {
					want = append(want, e.key)
				}
			}
			sameKeys(t, fmt.Sprintf("%s %s@%d", dir, ty.ID, ty.Version), of(reports[dir], ty.ID, ty.Version), want)
		}
	}

	// 2. Every row: a FAIL row is found by a finding or explained; a
	// refused row's absent findings are absent.
	for _, r := range bakeoffRows {
		got := keys(of(reports[r.dir], r.id, r.version))
		name := fmt.Sprintf("%s: %q (%s)", r.file, r.attempt, r.result)
		switch {
		case r.finding != nil:
			if !slices.Contains(got, *r.finding) {
				t.Errorf("%s: not found, want %+v; got %+v", name, *r.finding, got)
			}
			if !expectedHas(r.dir, r.id, r.version, *r.finding) {
				t.Errorf("%s: its finding is not in bakeoffFindings", name)
			}
		case len(r.absent) > 0:
			for _, k := range r.absent {
				for _, g := range got {
					if g.Code == k.Code && g.Path == k.Path {
						t.Errorf("%s: finding %+v contradicts the refused row", name, g)
					}
				}
			}
		case r.why == "":
			t.Errorf("%s: neither found, absent, nor explained", name)
		}
		if r.result == "FAIL" && r.finding == nil && r.why == "" {
			t.Errorf("%s: a FAIL row is found or explained", name)
		}
	}

	// 3. The "who reads" row is explained by a NotChecked line the report
	// does print.
	if !slices.Contains(reports[camundaDir].NotChecked, "who reads it: scopes decide, not the Type") {
		t.Errorf("NotChecked %q: no 'who reads' line", reports[camundaDir].NotChecked)
	}

	// 4. Every expected finding says why it is there.
	for _, e := range bakeoffFindings {
		if e.why == "" {
			t.Errorf("%s %s@%d %+v: no why", e.dir, e.id, e.version, e.key)
		}
	}
}

func expectedHas(dir, id string, version int, k key) bool {
	return slices.ContainsFunc(bakeoffFindings, func(e expected) bool {
		return e.dir == dir && e.id == id && e.version == version && e.key == k
	})
}
