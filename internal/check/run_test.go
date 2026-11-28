// Each witness run, run (pli check, PR 3): the story every finding with a
// witness carries is run against the real kernel, in simulated time
// (internal/story: Temporal's test environment, Postgres from
// testcontainers), one database per folder, since the bake-offs' Types
// share their IDs. A run checks what its steps do, never the timeline:
// every step is accepted, and no move a check hook answers is denied or
// fails. A step the kernel refuses is a false finding, the model and the
// engine disagreeing: the test fails, naming the finding and the step.
// For a STUCK_STATE or a NO_WAY_OUT the run shows the state is reached;
// that nothing leaves it stays the checker's word.
package check_test

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/francoisnyc/work-kernel/internal/check"
	"github.com/francoisnyc/work-kernel/internal/story"
	"github.com/francoisnyc/work-kernel/internal/testdb"
)

// guardValues are the values, written by hand, that make a witness's
// guards true, for the findings that depend on one (the owner's decision
// at Phase A: the checker takes every guard as possibly true and does not
// solve them). Each replaces the generated value, which must be in the
// story exactly once. Keyed by folder, then by finding.
var guardValues = map[string]map[string][2]string{
	camundaDir: {
		"purchase@1 AGENT_DECIDES transitions[6]": {"f_amount = 0", "f_amount = 1000"},
	},
	camundaV2Dir: {
		"purchase@1 AGENT_DECIDES transitions[6]": {"f_amount = 0", "f_amount = 1000"},
		"purchase@2 AGENT_DECIDES transitions[6]": {"f_amount = 0", "f_amount = 1000"},
		"purchase@2 AGENT_DECIDES transitions[9]": {"f_amount = 0", "f_amount = 6000"},
	},
	blindDir: {
		"purchase@1 AGENT_DECIDES transitions[6]": {"f_amount = 0", "f_amount = 1000"},
	},
	staffRoleDir: {
		"purchase@1 AGENT_DECIDES transitions[6]": {"f_amount = 0", "f_amount = 1000"},
	},
}

func findingName(f check.Finding) string {
	return fmt.Sprintf("%s@%d %s %s", f.Type, f.Version, f.Code, f.Path)
}

// aMoveDenied is a timeline line of a move a check hook denied, or that
// failed: "manager_check for ana denied draft -> manager_review: ...".
var aMoveDenied = regexp.MustCompile(`^\S+( for \S+)? (denied|failed) \S+ -> \S+:`)

// runWitness runs one witness run and returns what went wrong, one line
// each: nothing when every step did what the witness says.
func runWitness(ctx context.Context, env *story.Env, text string) []string {
	s, err := story.Parse("witness run", []byte(text))
	if err != nil {
		return []string{"parse: " + err.Error()}
	}
	res, err := story.Run(ctx, env, s)
	if err != nil {
		return []string{"run: " + err.Error()}
	}
	var out []string
	for _, f := range res.Failures {
		line := ""
		if f.Line > 0 {
			line = "line " + fmt.Sprint(f.Line) + ": "
		}
		out = append(out, line+f.Message)
	}
	for _, l := range res.Timeline {
		if aMoveDenied.MatchString(l.Text) {
			out = append(out, "the timeline: "+l.Format(s.Day1))
		}
	}
	return out
}

// storyEnv starts a database with a folder's Types registered.
func storyEnv(t *testing.T, ctx context.Context, dir string) *story.Env {
	t.Helper()
	pool, stop, err := testdb.Start(ctx)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(stop)
	env, err := story.NewEnv(ctx, pool, dir)
	if err != nil {
		t.Fatalf("the Types of %s: %v", dir, err)
	}
	return env
}

func TestWitnessRuns(t *testing.T) {
	for _, dir := range []string{typesDir, camundaDir, camundaV2Dir, blindDir, staffRoleDir} {
		t.Run(strings.TrimPrefix(dir, "../../"), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			env := storyEnv(t, ctx, dir)
			values := guardValues[dir]
			used := map[string]bool{}
			began, runs := time.Now(), 0
			for _, f := range stories(t, dir).Findings {
				if len(f.Witness) == 0 {
					continue
				}
				name := findingName(f)
				text := f.Story
				if text == "" {
					t.Errorf("%s: no witness run", name)
					continue
				}
				guarded := slices.ContainsFunc(f.DependsOn, func(d string) bool { return strings.HasPrefix(d, "guard:") })
				if v, ok := values[name]; ok {
					used[name] = true
					if n := strings.Count(text, v[0]); n != 1 {
						t.Errorf("%s: %q is in its witness run %d times, want once:\n%s", name, v[0], n, text)
						continue
					}
					text = strings.Replace(text, v[0], v[1], 1)
				} else if guarded {
					t.Errorf("%s depends on a guard and has no hand-written values", name)
					continue
				}
				runs++
				if problems := runWitness(ctx, env, text); len(problems) > 0 {
					t.Errorf("%s: the kernel does not do what its witness says (a false finding):\n  %s\nThe witness run:\n%s",
						name, strings.Join(problems, "\n  "), text)
				}
			}
			for name := range values {
				if !used[name] {
					t.Errorf("guardValues names %s, which has no witness run here", name)
				}
			}
			if runs == 0 {
				t.Errorf("no witness run in %s", dir)
			}
			t.Logf("%d witness runs in %v", runs, time.Since(began))
		})
	}
}

// The run is not blind: the incident@7 witness run, made to create an
// incident@8, where only a desk agent cancels one in progress, is refused
// at the agent's step.
func TestWitnessRunCatchesARefusal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	env := storyEnv(t, ctx, typesDir)
	text := strings.Replace(wantIncidentStory, "as incident@7", "as incident@8", 1)
	problems := runWitness(ctx, env, text)
	if len(problems) == 0 || !strings.Contains(strings.Join(problems, "\n"), "line 16: ") {
		t.Errorf("the agent's cancel of an incident@8: problems %q, want a refusal on line 16 (bot moves item to canceled)", problems)
	}
	if problems := runWitness(ctx, env, wantIncidentStory); len(problems) > 0 {
		t.Errorf("the incident@7 witness run: %q, want none", problems)
	}
}
