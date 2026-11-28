// Tests for pli check PATH... (pli check, PR 1): it reads Type files or
// folders, as type validate does, and calls no server, so it needs no --as.
// It prints the report's text, or with --json the report. Exit 0 when no
// finding is an ERROR (warnings print but pass), 1 when one is, 2 on a
// usage error or a Type that does not load.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// checkCmd runs pli with no --as and no server.
func checkCmd(t *testing.T, args ...string) result {
	t.Helper()
	var out, errb bytes.Buffer
	code := run(context.Background(), append([]string{"--server", "http://127.0.0.1:1", "check"}, args...), &out, &errb)
	return result{code: code, stdout: out.String(), stderr: errb.String()}
}

// Warnings only (the blind bake-off: an agent holding finance decides):
// printed, exit 0.
func TestCheckWarningsExitZero(t *testing.T) {
	r := checkCmd(t, "../../spikes/temporal-bakeoff/kernel-blind/types")
	if r.code != exitOK {
		t.Fatalf("exit %d, want 0; stderr %q", r.code, r.stderr)
	}
	for _, want := range []string{"purchase@1\n", "AGENT_DECIDES WARNING transitions[6]", "Not checked: who reads it", "0 errors, 1 warning.\n"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("stdout lacks %q:\n%s", want, r.stdout)
		}
	}
}

// An ERROR (the access request for no system): exit 1.
func TestCheckErrorExitOne(t *testing.T) {
	r := checkCmd(t, "../../spikes/camunda-bakeoff/kernel/types")
	if r.code != exitAPI {
		t.Fatalf("exit %d, want 1; stdout %q stderr %q", r.code, r.stdout, r.stderr)
	}
	if !strings.Contains(r.stdout, "STUCK_STATE ERROR states[1]: It can stay in owner_review for ever") {
		t.Errorf("stdout:\n%s", r.stdout)
	}
}

// A file works as a folder does, and --json prints the report.
func TestCheckJSONOfAFile(t *testing.T) {
	r := checkCmd(t, "--json", "../../spikes/camunda-bakeoff/kernel/types/purchase.v1.yaml")
	if r.code != exitOK {
		t.Fatalf("exit %d, want 0; stderr %q", r.code, r.stderr)
	}
	var doc struct {
		Findings []struct {
			Type, Code, Severity, Path string
			Version                    int
		} `json:"findings"`
		NotChecked []string `json:"not_checked"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &doc); err != nil {
		t.Fatalf("stdout is not JSON (%v): %q", err, r.stdout)
	}
	if len(doc.Findings) != 1 || doc.Findings[0].Code != "AGENT_DECIDES" || doc.Findings[0].Path != "transitions[6]" ||
		doc.Findings[0].Type != "purchase" || doc.Findings[0].Version != 1 || len(doc.NotChecked) == 0 {
		t.Errorf("JSON: %s", r.stdout)
	}
}

// No PATH, or a PATH that is not there: a usage error, exit 2.
func TestCheckUsage(t *testing.T) {
	for _, args := range [][]string{{}, {"--json"}, {"no/such/folder"}} {
		r := checkCmd(t, args...)
		if r.code != exitUsage || r.stderr == "" {
			t.Errorf("pli check %q: exit %d stderr %q, want 2 and a message", args, r.code, r.stderr)
		}
	}
}

// A Type that does not load: its problems on stderr, as type validate
// prints them, nothing checked, exit 2.
func TestCheckTypeThatDoesNotLoad(t *testing.T) {
	dir := tree(t, map[string]string{"bad.v1.yaml": "id: bad\nversion: 1\ninitial_state: nowhere\nstates:\n  - {id: open, category: todo}\n"})
	r := checkCmd(t, dir)
	if r.code != exitUsage {
		t.Fatalf("exit %d, want 2; stdout %q stderr %q", r.code, r.stdout, r.stderr)
	}
	if !strings.Contains(r.stderr, "bad.v1.yaml") || !strings.Contains(r.stderr, "typedef bad v1") {
		t.Errorf("stderr does not name the file and the Type: %q", r.stderr)
	}
	if r.stdout != "" {
		t.Errorf("stdout %q, want nothing", r.stdout)
	}
}
