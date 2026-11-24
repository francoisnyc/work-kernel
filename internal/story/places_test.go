// Tests for what Ventoux item 6's witnesses added to stories: two steps
// (places, narrows), a read of a rollup's count (sees ITEM with
// ROLLUP.KEY = N), and a fix: a step naming an Item the kernel created
// reaches that Item. Before the fix, such a step went to no Item at all,
// so "kim cannot see first" passed whatever kim could see.
package story_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/story"
)

const placesStory = `story: places, narrows and a rollup
author: a test
day1: 2026-12-07
people:
  alice: {}
  bob: {}
  carol: {}
steps:
  - day 1 09:00 alice creates sp as space@1
  - day 1 09:01 alice creates e as epic@1
  - day 1 09:02 alice places e in sp
  - day 1 09:03 alice narrows e to alice, bob
  - day 1 09:04 carol cannot see e
  - day 1 09:05 alice places e at the top level
  - day 1 09:06 alice creates s as story@3
  - day 1 09:07 alice links s epic e
  - day 1 09:08 alice sees e with progress.todo = 1, progress.done = 0
until: day 2 00:00
observer: alice
expect:
  - "day 1 09:00 sp: alice created it in default"
  - "day 1 09:01 e: alice created it in todo"
  - "day 1 09:02 e: alice moved it from the top level to sp"
  - "day 1 09:03 e: alice narrowed it to alice, bob"
  - "day 1 09:05 e: alice moved it from sp to the top level"
  - "day 1 09:06 s: alice created it in todo"
  - "day 1 09:07 s: alice linked epic to e"
`

func TestParse_PlacesNarrowsAndRollups(t *testing.T) {
	s, err := story.Parse("places.yaml", []byte(placesStory))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := map[int]story.Step{
		2: {Line: 11, At: at(1, 9, 2, 0), Text: "day 1 09:02 alice places e in sp", Actor: "alice", Verb: story.Places, Item: "e", In: "sp"},
		3: {Line: 12, At: at(1, 9, 3, 0), Text: "day 1 09:03 alice narrows e to alice, bob", Actor: "alice", Verb: story.Narrows, Item: "e",
			Readers: []string{"alice", "bob"}},
		5: {Line: 14, At: at(1, 9, 5, 0), Text: "day 1 09:05 alice places e at the top level", Actor: "alice", Verb: story.Places, Item: "e",
			Top: true},
		8: {Line: 17, At: at(1, 9, 8, 0), Text: "day 1 09:08 alice sees e with progress.todo = 1, progress.done = 0", Actor: "alice",
			Verb: story.Sees, Item: "e", Values: []story.Assignment{set("progress.todo", integer(1)), set("progress.done", integer(0))}},
	}
	for i, w := range want {
		if !reflect.DeepEqual(s.Steps[i], w) {
			t.Errorf("step %d = %+v\nwant %+v", i, s.Steps[i], w)
		}
	}
	if ps := check(t, s); len(ps) != 0 {
		t.Errorf("Check: %v, want no problem", ps)
	}
}

func TestParse_PlacesAndNarrowsProblems(t *testing.T) {
	cases := []struct {
		name  string
		step  string
		words []string
	}{
		{"places without where", "alice places request", []string{"in ITEM", "top level"}},
		{"places at the top", "alice places request at the top", []string{"top level"}},
		{"places in nothing", "alice places request in", []string{"container"}},
		{"places in two", "alice places request in a b", []string{"\"b\""}},
		{"narrows without to", "alice narrows request alice", []string{"to"}},
		{"narrows to nobody", "alice narrows request to", []string{"person"}},
		{"narrows to a trailing comma", "alice narrows request to alice,", []string{"person"}},
		{"a refused read of a rollup", "alice sees request with progress.done = 1 -> refused NOT_FOUND", []string{"refused"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			wantProblem(t, problemsOf(t, minimal("  - day 1 10:00 "+c.step+"\n")), 8, c.words...)
		})
	}
	// A place or a narrowing may be refused, like any write.
	s, err := story.Parse("ok.yaml", []byte(minimal("  - day 1 10:00 alice places request in request -> refused INVALID_REQUEST\n"+
		"  - day 1 10:01 alice narrows request to alice -> refused NOT_ALLOWED\n")))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if s.Steps[1].Refused != "INVALID_REQUEST" || s.Steps[2].Refused != "NOT_ALLOWED" {
		t.Errorf("refusals = %q, %q", s.Steps[1].Refused, s.Steps[2].Refused)
	}
}

func TestCheck_PlacesNarrowsAndRollups(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(s *story.Story)
		line   int
		words  []string
	}{
		{"a place in an unknown container", func(s *story.Story) { s.Steps[2].In = "nowhere" }, 11, []string{"nowhere"}},
		{"a narrowing to someone not in the story", func(s *story.Story) { s.Steps[3].Readers = []string{"alice", "zed"} }, 12,
			[]string{"zed"}},
		{"an unknown rollup", func(s *story.Story) { s.Steps[8].Values[0].Field = "progres.todo" }, 17, []string{"progres"}},
		{"a rollup without a key", func(s *story.Story) { s.Steps[8].Values[0].Field = "progress." }, 17, []string{"progress"}},
		{"a count that is no number", func(s *story.Story) { s.Steps[8].Values[0].Value = word("many") }, 17,
			[]string{"progress.todo", "number"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, err := story.Parse("places.yaml", []byte(placesStory))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			c.mutate(s)
			wantOne(t, check(t, s), "places.yaml", c.line, c.words...)
		})
	}
}

// Places, narrows and a rollup's count, run: what a person sees follows,
// and a count that differs fails the story at its line.
func TestRun_PlacesNarrowsAndRollups(t *testing.T) {
	s, err := story.Parse("places.yaml", []byte(placesStory))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	res := run(t, s)
	if d := story.Compare(s.Expect, res.Timeline); len(res.Failures) != 0 || !d.Empty() {
		t.Fatalf("%s", story.Report(s, res, d))
	}

	s, _ = story.Parse("places.yaml", []byte(strings.Replace(placesStory, "progress.todo = 1", "progress.todo = 2", 1)))
	wantFailure(t, run(t, s), 17, "progress.todo", "1", "2")

	// carol can see e once it is narrowed to her too.
	s, _ = story.Parse("places.yaml", []byte(strings.Replace(placesStory, "narrows e to alice, bob", "narrows e to alice, carol", 1)))
	wantFailure(t, run(t, s), 13, "carol", "e")
}

// A step may name an Item the kernel created, by a name rule, before any
// read named it: reads and writes reach that Item, so a read of it is
// what the person sees, a write is judged on it, and "cannot see" fails
// when the person can.
func TestRun_AStepNamesAnItemTheKernelCreated(t *testing.T) {
	src := func(steps string) string {
		return `story: the goals of an incident, by name
author: a test
day1: 2026-12-07
people:
  lea: {roles: [sla_admin, desk_agent]}
  kim: {customer: true}
names:
  first: sla_goal in inc where f_metric = first_response
hide_values:
  sla_goal: [f_week, f_days_off, f_zone]
fakes:
  email.notify@1:
    - ok
given:
  - 'lea creates cal as calendar@1 with f_name = "hours", f_zone = "UTC", f_week = ["mon 09:00-17:00"]'
  - lea creates policy as sla_policy@2 with f_name = "p", f_first_response_s_p1 = 60, f_first_response_s_p2 = 60, f_first_response_s_p3 = 60, f_first_response_s_p4 = 60, f_resolution_s_p1 = 60, f_resolution_s_p2 = 60, f_resolution_s_p3 = 60, f_resolution_s_p4 = 60
  - lea creates portal as desk@2 with f_key = "IT", f_name = "IT"
  - lea links portal calendar cal
  - lea links portal sla_policy policy
steps:
  - 'day 1 10:00 kim creates inc as incident@3 in portal with f_title = "x", f_priority = P3'
` + steps + `until: day 1 10:30
expect:
  - "day 1 10:00 inc: kim created it in new inside portal"
`
	}
	// lea sees the goal; a write to it is judged on it (a field only the
	// kernel writes), not refused as missing.
	s, err := story.Parse("names.yaml", []byte(src(
		"  - day 1 10:01 lea sees first with f_metric = first_response\n"+
			"  - day 1 10:02 lea updates first with f_target_s = 1 -> refused FIELD_NOT_WRITABLE\n"+
			"  - day 1 10:03 kim cannot see first\n")))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if res := run(t, s); len(res.Failures) != 0 {
		t.Errorf("failures = %+v, want none", res.Failures)
	}
	// lea can see the goal: "lea cannot see first" fails the story.
	s, err = story.Parse("names.yaml", []byte(src("  - day 1 10:01 lea cannot see first\n")))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	wantFailure(t, run(t, s), 22, "lea", "first")
}
