// Tests for Ventoux V2b: pli type validate PATH... sends draft Types to
// ValidateTypes. A PATH is a file or a folder; a folder is read as the item
// service reads its Types folder (its .yaml files and one level of
// subfolders; names starting with a dot skipped; a deeper folder is
// FOLDER_TOO_DEEP, found by pli itself). Each source is named by its path as
// given, joined.
//
//	pli type validate [--json] PATH...
//
// By default pli prints one line per problem, in the log-line format of
// docs/design.md, "Type problems":
//
//	<source>:<line>:<col>: <CODE> typedef <id> v<n>: <message>
//
// and nothing else. --json prints the response instead. Exit 1 on any
// ERROR, 0 when clean, 2 on a usage error (nothing sent); an API refusal is
// exit 1 as for every command.
package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tree writes files (slash-separated names) under a new folder.
func tree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func lines(s string) []string {
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// checkLines compares the output with the wanted line prefixes, in order.
func checkLines(t *testing.T, r result, prefixes ...string) {
	t.Helper()
	got := lines(r.stdout)
	if len(got) != len(prefixes) {
		t.Fatalf("%d lines, want %d:\n%s", len(got), len(prefixes), r.stdout)
	}
	for i, p := range prefixes {
		if !strings.HasPrefix(got[i], p) {
			t.Errorf("line %d = %q\nwant it to start with %q", i+1, got[i], p)
		}
	}
}

// The repository's Types are registered: as drafts they pass, as a folder
// or as files. Nothing is printed.
func TestPli_TypeValidate_Clean(t *testing.T) {
	for _, args := range [][]string{
		{"type", "validate", "../../types"},
		{"type", "validate", "../../types/ticket.v1.yaml", "../../types/sla"},
	} {
		r := pli(t, "ana", args...)
		if r.code != 0 || r.stdout != "" || r.stderr != "" {
			t.Errorf("pli %v: exit %d, stdout %q, stderr %q; want exit 0 and no output", args, r.code, r.stdout, r.stderr)
		}
	}
}

// A folder's problems, one line each, sorted by source then position:
// files in a subfolder are read, a README and a hidden folder are not.
func TestPli_TypeValidate_Problems(t *testing.T) {
	dir := tree(t, map[string]string{
		"b.v1.yaml": `id: vtpb
version: 1
initial_state: open
states:
  - {id: open, category: doing}
transitions:
  - {from: open, to: closed}
`,
		"a.v1.yaml":         "id: vtpa\nversion: 1\nfields: [\n",
		"good.v1.yaml":      "id: vtpgood\nversion: 1\n",
		"sub/c.v1.yaml":     "id: vtpc\nversion: 0\n",
		"sub/README.md":     "# not a Type\n",
		".hidden/x.v1.yaml": "not: [a type\n",
		"notes.txt":         "nor this\n",
	})
	r := pli(t, "ana", "type", "validate", dir)
	if r.code != 1 {
		t.Errorf("exit %d (stderr %q), want 1", r.code, r.stderr)
	}
	checkLines(t, r,
		filepath.Join(dir, "a.v1.yaml")+":3: YAML_SYNTAX ",
		filepath.Join(dir, "b.v1.yaml")+`:5:16: INVALID_VALUE typedef vtpb v1: state open: unknown category "doing". Use `,
		filepath.Join(dir, "b.v1.yaml")+`:7:18: UNKNOWN_STATE typedef vtpb v1: transition open -> closed: unknown state "closed". `,
		filepath.Join(dir, "sub", "c.v1.yaml")+":2:1: INVALID_VALUE typedef vtpc v0: ",
	)
}

// A folder too deep is pli's own finding, printed like the others; the
// files it did read are still checked.
func TestPli_TypeValidate_FolderTooDeep(t *testing.T) {
	dir := tree(t, map[string]string{
		"good.v1.yaml":         "id: vtdeep\nversion: 1\n",
		"sub/deeper/x.v1.yaml": "id: vtdeepx\nversion: 1\n",
		"bad.v1.yaml":          "id: vtdeepbad\nversion: 1\nfields: [{id: today, type: date}]\n",
	})
	r := pli(t, "ana", "type", "validate", dir)
	if r.code != 1 {
		t.Errorf("exit %d (stderr %q), want 1", r.code, r.stderr)
	}
	checkLines(t, r,
		filepath.Join(dir, "bad.v1.yaml")+":3:11: RESERVED_NAME typedef vtdeepbad v1: ",
		filepath.Join(dir, "sub", "deeper")+": FOLDER_TOO_DEEP ",
	)
}

// A problem on a registered version is printed with the version's name and
// no position.
func TestPli_TypeValidate_ARegisteredVersion(t *testing.T) {
	v1, err := os.ReadFile("../../types/leave_balance.v1.yaml")
	if err != nil {
		t.Fatal(err)
	}
	v2 := strings.Replace(strings.Replace(string(v1), "\nversion: 1\n", "\nversion: 2\n", 1), "  vacation_request:deduct: [f_used]\n", "", 1)
	dir := tree(t, map[string]string{"leave_balance.v2.yaml": v2})
	r := pli(t, "ana", "type", "validate", filepath.Join(dir, "leave_balance.v2.yaml"))
	if r.code != 1 {
		t.Errorf("exit %d (stderr %q), want 1", r.code, r.stderr)
	}
	checkLines(t, r,
		"namespaces/acme/types/vacation_request/versions/2: CONSENT_MISSING typedef vacation_request v2: reaction deduct ",
		"namespaces/acme/types/vacation_request/versions/3: CONSENT_MISSING typedef vacation_request v3: reaction deduct ",
	)
}

// --json prints the response, with the proto field names; the exit status
// is the same.
func TestPli_TypeValidate_JSON(t *testing.T) {
	dir := tree(t, map[string]string{"memo.yaml": "id: vtjson\nversion: 1\nfields: [{id: today, type: date}]\n"})
	src := filepath.Join(dir, "memo.yaml")
	r := pli(t, "ana", "type", "validate", "--json", src)
	if r.code != 1 {
		t.Errorf("exit %d (stderr %q), want 1", r.code, r.stderr)
	}
	ps, okay := r.json(t)["problems"].([]any)
	if !okay || len(ps) != 1 {
		t.Fatalf("problems = %v, want one", r.stdout)
	}
	p := ps[0].(map[string]any)
	want := map[string]any{
		"source": src, "path": "fields[0].id", "line": 3.0, "column": 11.0, "severity": "TYPE_PROBLEM_SEVERITY_ERROR",
		"code": "RESERVED_NAME", "type": "namespaces/acme/types/vtjson/versions/1",
	}
	for k, v := range want {
		if p[k] != v {
			t.Errorf("%s = %v, want %v", k, p[k], v)
		}
	}
	if m, _ := p["message"].(string); !strings.Contains(m, `"today" is a reserved name`) {
		t.Errorf("message = %q", p["message"])
	}

	clean := ok(t, "ana", "type", "validate", "--json", "../../types/ticket.v1.yaml")
	if len(clean) != 0 {
		t.Errorf("a clean draft: %v, want {}", clean)
	}
}

// The server's limits come back as an API refusal: exit 1.
func TestPli_TypeValidate_OverTheLimit(t *testing.T) {
	files := map[string]string{}
	for i := range 51 {
		files[fmt.Sprintf("m%02d.yaml", i)] = fmt.Sprintf("id: vtlim%d\nversion: 1\n", i)
	}
	fails(t, "ana", "INVALID_ARGUMENT INVALID_REQUEST", "type", "validate", tree(t, files))
}

// Usage errors exit 2 and send nothing (the server here does not exist).
func TestPli_TypeValidate_UsageErrors(t *testing.T) {
	for name, c := range map[string]struct {
		args []string
		want string
	}{
		"no PATH":         {[]string{"type", "validate"}, "PATH"},
		"a missing PATH":  {[]string{"type", "validate", "no/such/types"}, "no/such/types"},
		"only --json":     {[]string{"type", "validate", "--json"}, "PATH"},
		"an unknown flag": {[]string{"type", "validate", "--yaml", "../../types"}, "-yaml"},
	} {
		t.Run(name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			args := append([]string{"--server", "http://127.0.0.1:1", "--tokens", tokensPath, "--as", "ana"}, c.args...)
			code := run(context.Background(), args, &stdout, &stderr)
			if code != 2 || !strings.Contains(stderr.String(), c.want) {
				t.Errorf("exit %d, stderr %q; want exit 2 mentioning %q", code, stderr.String(), c.want)
			}
		})
	}
}
