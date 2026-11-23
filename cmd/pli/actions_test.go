// Tests for Ventoux V1: pli action list prints the action catalogue
// (ListActions).
//
//	pli action list [--json]
//
// By default, one block per action, separated by a blank line:
//
//	<action> (<kinds>; risk <risk>; side effects | no side effects)
//	  <description>
//	  in  <name>: <type>[ or null]; trusted (declared, not enforced until Ventoux item 4) | any source
//	  out <name>: <type>[ or null]
//	  reasons: <code>, <code>
//
// An enum's type is "enum (a, b, c)"; a block without inputs, outputs or
// reasons leaves those lines out. --json prints the response instead.
package main

import (
	"strings"
	"testing"
)

// blocks splits the output into its actions' blocks, by blank line.
func blocks(t *testing.T, out string) map[string][]string {
	t.Helper()
	got := map[string][]string{}
	for _, b := range strings.Split(strings.TrimSuffix(out, "\n"), "\n\n") {
		ls := strings.Split(b, "\n")
		action, _, _ := strings.Cut(ls[0], " ")
		got[action] = ls
	}
	return got
}

func TestPli_ActionList(t *testing.T) {
	r := pli(t, "ana", "action", "list")
	if r.code != 0 || r.stderr != "" {
		t.Fatalf("exit %d, stderr %q", r.code, r.stderr)
	}
	got := blocks(t, r.stdout)
	if len(got) != 6 {
		t.Fatalf("%d blocks, want 6:\n%s", len(got), r.stdout)
	}
	const trusted = "trusted (declared, not enforced until Ventoux item 4)"
	want := map[string][]string{
		"hr.manager_of@1": {
			"hr.manager_of@1 (check; risk low; no side effects)",
			"", // the description
			"  in  onBehalfOf: principal or null; " + trusted,
			"  in  createdBy: principal or null; " + trusted,
			"  out manager: principal",
			"  reasons: unknown_employee, no_eligible_approver",
		},
		"crm.get_plan@1": {
			"crm.get_plan@1 (enrich; risk low; no side effects)",
			"",
			"  in  customerId: string; any source",
			"  out plan: enum (free, pro, enterprise)",
		},
		"email.notify@1": {
			"email.notify@1 (action; risk medium; side effects)",
			"",
			"  in  to: principal; " + trusted,
		},
		"erp.create_invoice@1": {
			"erp.create_invoice@1 (action; risk high; side effects)",
			"",
			"  in  customerId: string; " + trusted,
			"  in  amount: int; " + trusted,
			"  out invoiceId: string",
		},
	}
	for action, lines := range want {
		block := got[action]
		if len(block) != len(lines) {
			t.Errorf("%s: %d lines, want %d:\n%s", action, len(block), len(lines), strings.Join(block, "\n"))
			continue
		}
		for i, w := range lines {
			if i == 1 {
				if !strings.HasPrefix(block[i], "  ") || strings.TrimSpace(block[i]) == "" {
					t.Errorf("%s: line 2 = %q, want the description, indented", action, block[i])
				}
				continue
			}
			if block[i] != w {
				t.Errorf("%s: line %d = %q\nwant %q", action, i+1, block[i], w)
			}
		}
	}
}

func TestPli_ActionList_JSON(t *testing.T) {
	out := ok(t, "ana", "action", "list", "--json")
	actions, _ := out["actions"].([]any)
	if len(actions) != 6 {
		t.Fatalf("%d actions, want 6: %v", len(actions), out)
	}
	first, _ := actions[0].(map[string]any)
	if first["action"] != "crm.get_plan@1" || first["digest"] == nil {
		t.Errorf("first action = %v; want crm.get_plan@1 with its digest", first)
	}
}

func TestPli_ActionList_Usage(t *testing.T) {
	if r := pli(t, "ana", "action", "list", "extra"); r.code != 2 {
		t.Errorf("an argument: exit %d, want 2", r.code)
	}
}
