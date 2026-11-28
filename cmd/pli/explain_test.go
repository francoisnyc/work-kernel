// Tests for Ventoux item 6: pli type explain prints a Type version in
// plain language (ExplainType).
//
//	pli type explain [--json] TYPE@VERSION
//	pli type explain [--json] --draft TYPE@VERSION PATH...
//
// TYPE@VERSION names a registered version (vacation_request@3; its full
// name works too). With --draft, the PATHs are read as type validate reads
// them, and the draft version among them is explained. By default pli
// prints the reading's text, exactly; a draft with problems prints them as
// type validate does and exits 1. --json prints the response. Usage
// errors exit 2 and send nothing.
package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readingFile(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile("../../readings/" + name + ".txt")
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestPli_TypeExplain(t *testing.T) {
	for _, name := range []string{"vacation_request@3", "namespaces/acme/types/vacation_request/versions/3"} {
		r := pli(t, "ana", "type", "explain", name)
		if r.code != 0 || r.stderr != "" {
			t.Fatalf("pli type explain %s: exit %d, stderr %q", name, r.code, r.stderr)
		}
		if want := readingFile(t, "vacation_request@3"); r.stdout != want {
			t.Errorf("pli type explain %s =\n%s\nwant readings/vacation_request@3.txt", name, r.stdout)
		}
	}
}

func TestPli_TypeExplain_JSON(t *testing.T) {
	got := ok(t, "ana", "type", "explain", "--json", "incident@3")
	rd, _ := got["reading"].(map[string]any)
	if rd["type"] != "namespaces/acme/types/incident/versions/3" || rd["text"] != readingFile(t, "incident@3") {
		t.Errorf("reading = %v", rd)
	}
	secs, _ := rd["sections"].([]any)
	if len(secs) == 0 {
		t.Fatalf("no sections: %v", rd)
	}
	first := secs[0].(map[string]any)["sentences"].([]any)[0].(map[string]any)
	if first["number"] != 1.0 || first["kind"] != "starts" || first["text"] != "An incident starts in new." {
		t.Errorf("the first sentence = %v", first)
	}
}

// A draft is read from files, as type validate reads them.
func TestPli_TypeExplain_ADraft(t *testing.T) {
	v3, err := os.ReadFile("../../types/incident.v3.yaml")
	if err != nil {
		t.Fatal(err)
	}
	dir := tree(t, map[string]string{"incident.v10.yaml": strings.Replace(string(v3), "\nversion: 3\n", "\nversion: 10\n", 1)})
	r := pli(t, "ana", "type", "explain", "--draft", "incident@10", dir)
	if r.code != 0 || r.stderr != "" {
		t.Fatalf("exit %d, stderr %q", r.code, r.stderr)
	}
	if want := strings.Replace(readingFile(t, "incident@3"), "incident@3\n", "incident@10\n", 1); r.stdout != want {
		t.Errorf("stdout =\n%s", r.stdout)
	}
}

// A draft with problems prints them, as type validate does, and exits 1.
func TestPli_TypeExplain_ADraftWithProblems(t *testing.T) {
	dir := tree(t, map[string]string{"memo.yaml": "id: vtexplain\nversion: 1\nfields: [{id: today, type: date}]\n"})
	src := filepath.Join(dir, "memo.yaml")
	r := pli(t, "ana", "type", "explain", "--draft", "vtexplain@1", src)
	if r.code != 1 {
		t.Errorf("exit %d (stderr %q), want 1", r.code, r.stderr)
	}
	checkLines(t, r, src+":3:11: RESERVED_NAME typedef vtexplain v1: ")
}

// The server's refusals come back as for every command: exit 1.
func TestPli_TypeExplain_Refused(t *testing.T) {
	fails(t, "ana", "NOT_FOUND", "type", "explain", "vacation_request@99")
}

// Usage errors exit 2 and send nothing (the server here does not exist).
func TestPli_TypeExplain_UsageErrors(t *testing.T) {
	for name, c := range map[string]struct {
		args []string
		want string
	}{
		"nothing":                  {[]string{"type", "explain"}, "TYPE@VERSION"},
		"two Types":                {[]string{"type", "explain", "a@1", "b@1"}, "TYPE@VERSION"},
		"no version":               {[]string{"type", "explain", "vacation_request"}, "TYPE@VERSION"},
		"a draft without PATH":     {[]string{"type", "explain", "--draft", "memo@1"}, "PATH"},
		"a draft without its Type": {[]string{"type", "explain", "--draft"}, "TYPE@VERSION"},
		"a missing PATH":           {[]string{"type", "explain", "--draft", "memo@1", "no/such/types"}, "no/such/types"},
		"an unknown flag":          {[]string{"type", "explain", "--yaml", "memo@1"}, "-yaml"},
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
