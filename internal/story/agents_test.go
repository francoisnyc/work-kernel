// Tests for agents in stories (fields closed to agents by default): a
// person may be an agent (agent: true, roles allowed, never a customer),
// and a step may say an agent acts for a person ("bot for ana updates
// ..."). Only an agent acts for someone, and only for a person of the story.
package story_test

import (
	"strings"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/story"
)

func withAgent(extra string) string {
	return strings.Replace(minimal(extra), "  alice: {}\n", "  alice: {}\n  bot: {agent: true, roles: [lead]}\n", 1)
}

func TestParse_Agents(t *testing.T) {
	s, err := story.Parse("a.yaml", []byte(withAgent(
		"  - 'day 1 09:01 bot updates request with f_comment = \"x\"'\n"+
			"  - 'day 1 09:02 bot for alice updates request with f_comment = \"y\"'\n")))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	var bot *story.Person
	for i := range s.People {
		if s.People[i].Name == "bot" {
			bot = &s.People[i]
		}
	}
	if bot == nil || bot.Principal() != "agent:bot" {
		t.Errorf("bot = %+v, want the principal agent:bot", bot)
	}
}

func TestParse_AgentProblems(t *testing.T) {
	cases := []struct {
		name  string
		src   string
		line  int
		words []string
	}{
		{"a person acting for someone", withAgent("  - day 1 10:00 alice for bot moves request to submitted\n"), 9, []string{"alice", "agent"}},
		{"an agent acting for no one in the story", withAgent("  - day 1 10:00 bot for zed moves request to submitted\n"), 9, []string{"zed"}},
		{"an agent that is a customer", strings.Replace(withAgent(""), "{agent: true, roles: [lead]}", "{agent: true, customer: true}", 1),
			6, []string{"agent", "customer"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			wantProblem(t, problemsOf(t, c.src), c.line, c.words...)
		})
	}
}
