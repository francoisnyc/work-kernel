// Agents' names for screens (the Inbox, the look, item 6; the owner's
// decision of 2026-11-28): an agent's entry may carry `display_name`, the
// name a screen shows ("Triage agent"), until sign-on brings real names.
// `name` stays the handle pli --as takes. The key is optional, for agents
// only (a person's screen name comes from `name`), and plain text: 1 to 60
// characters, one line, no space at either end. A bad one refuses the
// whole file, naming the entry.
package devtoken_test

import (
	"strings"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/devtoken"
)

func TestParse_ReadsAnAgentsDisplayName(t *testing.T) {
	got, err := devtoken.Parse([]byte(`tokens:
  - {name: ana, token: dev-ana, principal: "user:ana", namespace: acme}
  - {name: triage-bot, token: dev-triage, principal: "agent:triage-bot", namespace: acme, display_name: Triage agent}
  - {name: sweeper, token: dev-sweeper, principal: "agent:sweeper", namespace: acme}
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("Parse = %d entries, want 3", len(got))
	}
	if got[1].DisplayName != "Triage agent" || got[1].Name != "triage-bot" {
		t.Errorf("triage-bot: name, display_name = %q, %q, want triage-bot, Triage agent", got[1].Name, got[1].DisplayName)
	}
	if got[0].DisplayName != "" || got[2].DisplayName != "" {
		t.Errorf("display names = %q, %q, want none", got[0].DisplayName, got[2].DisplayName)
	}
}

func TestParse_RefusesBadDisplayNames(t *testing.T) {
	for name, entry := range map[string]string{
		"on a person":         `{name: ana, token: dev-ana, principal: "user:ana", namespace: acme, display_name: Ana Ruiz}`,
		"on a customer":       `{name: kim, token: dev-kim, principal: "customer:kim", namespace: acme, display_name: Kim Park}`,
		"empty":               `{name: bot, token: dev-bot, principal: "agent:bot", namespace: acme, display_name: ""}`,
		"of 61 characters":    `{name: bot, token: dev-bot, principal: "agent:bot", namespace: acme, display_name: ` + strings.Repeat("a", 61) + `}`,
		"of two lines":        `{name: bot, token: dev-bot, principal: "agent:bot", namespace: acme, display_name: "Triage\nagent"}`,
		"with a space before": `{name: bot, token: dev-bot, principal: "agent:bot", namespace: acme, display_name: " Triage agent"}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := devtoken.Parse([]byte("tokens:\n  - " + entry + "\n"))
			if err == nil {
				t.Fatalf("Parse accepted %s", entry)
			}
			if !strings.Contains(err.Error(), "display_name") {
				t.Errorf("error %q does not name display_name", err)
			}
		})
	}
}

// The repository's example file names its two agents for screens.
func TestLoad_TheExampleFilesAgents(t *testing.T) {
	toks, err := devtoken.Load("../../dev-tokens.example.yaml")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := map[string]string{"triage-bot": "Triage agent", "portal-bot": "Portal agent"}
	for _, tok := range toks {
		if w := want[tok.Name]; tok.DisplayName != w {
			t.Errorf("%s: display_name = %q, want %q", tok.Name, tok.DisplayName, w)
		}
	}
}
