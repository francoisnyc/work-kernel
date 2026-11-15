// Tests for Step 0, part 1: the dev-token file. Proof-of-concept auth only:
// each entry maps a bearer token to a principal in one namespace, and names
// the entry for `pli --as <name>`. The server and pli read the same file.
package devtoken_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/devtoken"
)

const good = `
# Dev tokens for the proof of concept. Never use in production.
tokens:
  - {name: ana, token: dev-ana, principal: "user:ana", namespace: acme}
  - {name: bot, token: dev-bot, principal: "agent:triage", namespace: acme}
  - {name: bea, token: dev-bea, principal: "user:bea", namespace: globex}
`

func TestParse_ReadsEveryEntry(t *testing.T) {
	got, err := devtoken.Parse([]byte(good))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := []devtoken.Token{
		{Name: "ana", Token: "dev-ana", Principal: "user:ana", Namespace: "acme"},
		{Name: "bot", Token: "dev-bot", Principal: "agent:triage", Namespace: "acme"},
		{Name: "bea", Token: "dev-bea", Principal: "user:bea", Namespace: "globex"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Parse = %+v\nwant %+v", got, want)
	}
}

func TestLoad_ReadsAFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dev-tokens.yaml")
	if err := os.WriteFile(path, []byte(good), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := devtoken.Load(path)
	if err != nil || len(got) != 3 {
		t.Fatalf("Load = %d entries, %v; want 3, nil", len(got), err)
	}
}

// A bad entry refuses the whole file, and the error says what is wrong.
// Only people and agents call the public API: the kernel's own actors
// (system:, hook:) never get a token, and groups are not readers yet.
func TestParse_RefusesBadEntries(t *testing.T) {
	entry := func(name, token, principal, ns string) string {
		return "tokens:\n  - {name: " + name + ", token: " + token +
			", principal: \"" + principal + "\", namespace: " + ns + "}\n"
	}
	cases := []struct {
		name, file, wantInError string
	}{
		{"no token", entry("ana", `""`, "user:ana", "acme"), "token"},
		{"no name", entry(`""`, "dev-ana", "user:ana", "acme"), "name"},
		{"no namespace", entry("ana", "dev-ana", "user:ana", `""`), "namespace"},
		{"bad namespace", entry("ana", "dev-ana", "user:ana", `"Acme Corp"`), "namespace"},
		{"untyped principal", entry("ana", "dev-ana", "ana", "acme"), "principal"},
		{"group principal", entry("ana", "dev-ana", "group:ops", "acme"), "principal"},
		{"system principal", entry("timer", "dev-timer", "system:timer", "acme"), "principal"},
		{"hook principal", entry("hook", "dev-hook", "hook:ticket:erp_check", "acme"), "principal"},
		{"unknown key", "tokens:\n  - {name: ana, token: t, principal: \"user:ana\", namespace: acme, role: admin}\n", "role"},
		{"duplicate token", entry("ana", "same", "user:ana", "acme") +
			"  - {name: bob, token: same, principal: \"user:bob\", namespace: acme}\n", "same"},
		{"duplicate name", entry("ana", "t1", "user:ana", "acme") +
			"  - {name: ana, token: t2, principal: \"user:ana\", namespace: globex}\n", "ana"},
		{"no entries", "tokens: []\n", "no tokens"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := devtoken.Parse([]byte(c.file))
			if err == nil {
				t.Fatalf("Parse accepted:\n%s", c.file)
			}
			if !strings.Contains(err.Error(), c.wantInError) {
				t.Errorf("error %q does not mention %q", err, c.wantInError)
			}
		})
	}
}

// The same principal ID may hold a token in two namespaces: they are two
// principals.
func TestParse_SamePrincipalInTwoNamespaces(t *testing.T) {
	file := "tokens:\n" +
		"  - {name: sam-acme, token: t1, principal: \"user:sam\", namespace: acme}\n" +
		"  - {name: sam-globex, token: t2, principal: \"user:sam\", namespace: globex}\n"
	if _, err := devtoken.Parse([]byte(file)); err != nil {
		t.Fatalf("Parse: %v", err)
	}
}
