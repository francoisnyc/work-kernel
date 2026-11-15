// Tests for Step 0, part 2: grants in the dev-token file. A grant says what a
// user let an agent do in one namespace: the scopes a session may cover
// (namespaces/{ns}, or container names in it) and the powers it may hold.
// The server reads them; pli reads the same file and ignores them.
package devtoken_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/devtoken"
)

const withGrants = good + `
grants:
  - agent: "agent:triage"
    user: "user:ana"
    namespace: acme
    scopes: [namespaces/acme]
    powers: [read, edit_fields, "transition:*"]
  - agent: "agent:triage"
    namespace: acme
    scopes: [namespaces/acme/items/0190c0de-0000-7000-8000-000000000001]
    powers: [read]
`

func TestParseGrants_ReadsEveryGrant(t *testing.T) {
	got, err := devtoken.ParseGrants([]byte(withGrants))
	if err != nil {
		t.Fatalf("ParseGrants: %v", err)
	}
	want := []devtoken.Grant{
		{Agent: "agent:triage", User: "user:ana", Namespace: "acme",
			Scopes: []string{"namespaces/acme"}, Powers: []string{"read", "edit_fields", "transition:*"}},
		{Agent: "agent:triage", Namespace: "acme",
			Scopes: []string{"namespaces/acme/items/0190c0de-0000-7000-8000-000000000001"}, Powers: []string{"read"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParseGrants = %+v\nwant %+v", got, want)
	}
	// The tokens of the same file still read; a file with no grants has none.
	if toks, err := devtoken.Parse([]byte(withGrants)); err != nil || len(toks) != 3 {
		t.Errorf("Parse of a file with grants = %d tokens, %v; want 3", len(toks), err)
	}
	if gs, err := devtoken.ParseGrants([]byte(good)); err != nil || len(gs) != 0 {
		t.Errorf("ParseGrants of a file without grants = %v, %v; want none", gs, err)
	}
}

func TestLoadGrants_ReadsAFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dev-tokens.yaml")
	if err := os.WriteFile(path, []byte(withGrants), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := devtoken.LoadGrants(path)
	if err != nil || len(got) != 2 {
		t.Fatalf("LoadGrants = %d grants, %v; want 2", len(got), err)
	}
}

// A bad grant refuses the whole file, saying what is wrong.
func TestParseGrants_RefusesBadGrants(t *testing.T) {
	grant := func(agent, user, ns, scopes, powers string) string {
		return good + "grants:\n  - {agent: \"" + agent + "\", user: \"" + user + "\", namespace: " + ns +
			", scopes: " + scopes + ", powers: " + powers + "}\n"
	}
	cases := []struct {
		name, file, wantInError string
	}{
		{"agent not an agent", grant("user:bot", "user:ana", "acme", "[namespaces/acme]", "[read]"), "agent"},
		{"user not a user", grant("agent:bot", "agent:x", "acme", "[namespaces/acme]", "[read]"), "user"},
		{"bad namespace", grant("agent:bot", "user:ana", `"Acme Corp"`, "[namespaces/acme]", "[read]"), "namespace"},
		{"no scopes", grant("agent:bot", "user:ana", "acme", "[]", "[read]"), "scopes"},
		{"scope in another namespace", grant("agent:bot", "user:ana", "acme", "[namespaces/globex]", "[read]"), "namespaces/globex"},
		{"malformed scope", grant("agent:bot", "user:ana", "acme", "[acme]", "[read]"), "acme"},
		{"no powers", grant("agent:bot", "user:ana", "acme", "[namespaces/acme]", "[]"), "powers"},
		{"unknown power", grant("agent:bot", "user:ana", "acme", "[namespaces/acme]", "[read, fly]"), "fly"},
		{"unknown key", good + "grants:\n  - {agent: \"agent:bot\", namespace: acme, scopes: [namespaces/acme], powers: [read], ttl: 1h}\n", "ttl"},
		{"the same agent and user twice", grant("agent:bot", "user:ana", "acme", "[namespaces/acme]", "[read]") +
			"  - {agent: \"agent:bot\", user: \"user:ana\", namespace: acme, scopes: [namespaces/acme], powers: [edit_fields]}\n",
			"twice"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := devtoken.ParseGrants([]byte(c.file))
			if err == nil {
				t.Fatalf("ParseGrants accepted:\n%s", c.file)
			}
			if !strings.Contains(err.Error(), c.wantInError) {
				t.Errorf("error %q does not mention %q", err, c.wantInError)
			}
		})
	}
}
