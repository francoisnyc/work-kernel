// Tests for Parse: the story file and its one-line grammar (the package
// documentation). Each problem names its line; every problem of a file is
// reported at once.
package story_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/francoisnyc/work-kernel/internal/story"
)

var day1 = time.Date(2026, 12, 7, 0, 0, 0, 0, time.UTC)

// at is day N HH:MM[:SS] of a story whose day 1 is day1.
func at(day, hh, mm, ss int) time.Time {
	return day1.AddDate(0, 0, day-1).Add(time.Duration(hh)*time.Hour + time.Duration(mm)*time.Minute + time.Duration(ss)*time.Second)
}

func word(s string) story.Value   { return story.Value{Kind: story.Word, Text: s} }
func quoted(s string) story.Value { return story.Value{Kind: story.Quoted, Text: s} }
func integer(n int64) story.Value { return story.Value{Kind: story.Int, Int: n} }
func dayValue(n int) story.Value  { return story.Value{Kind: story.Day, Day: n} }
func set(f string, v story.Value) story.Assignment {
	return story.Assignment{Field: f, Value: v}
}

// A file using every form of the grammar, with its line numbers.
const fullStory = `story: every form of the grammar
author: a test
day1: 2026-12-07
observer: olga
people:
  alice: {}
  hana: {roles: [hr_admin, auditor]}
  kim: {customer: true}
  olga: {}
names:
  first: sla_goal in inc where f_metric = first_response
  any: sla_goal
hide_values:
  sla_goal: [f_week, f_zone]
show_skipped: true
fakes:
  hr.manager_of@1:
    - "when createdBy = alice, onBehalfOf = null: correct manager = bob"
    - "from day 1 09:00 until day 1 10:00: fail transient"
    - "when createdBy = carol from day 2 00:00: deny unknown_employee"
  email.notify@1:
    - ok
  erp.create_invoice@1:
    - 'ok invoiceId = "INV-1"'
    - allow
given:
  - hana creates balance as leave_balance@1 with f_owner = alice, f_allowance = 20
steps:
  - day 1 09:00 alice creates request as vacation_request@3 with f_start = day 15, f_days = 5, f_leave_type = annual
  - day 1 09:05:30 alice moves request to submitted
  - day 2 10:00 alice moves request to approved -> refused NOT_ALLOWED
  - day 2 10:00 alice updates request with f_comment = "a, b = c", f_end = null, f_flag = true
  - day 2 10:00 kim creates reply1 as reply@2 in request with f_body = "x", f_when = day 3 14:30, f_tags = [a, "b c"]
  - day 2 11:00 hana links balance owner request
  - day 2 11:00 hana unlinks balance owner request
  - day 2 11:00 hana sees balance with f_used = 5
  - day 2 11:00 kim sees request fields f_priority, f_title
  - day 2 11:00 kim cannot see balance
until: day 10 00:00
expect:
  - "day 1 09:00 request: alice created it in draft"
  - "day 1 09:05:30 request:   alice asked draft -> submitted"
`

func TestParse_EveryForm(t *testing.T) {
	s, err := story.Parse("full.yaml", []byte(fullStory))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if s.Source != "full.yaml" || s.Name != "every form of the grammar" || s.Author != "a test" ||
		!s.Day1.Equal(day1) || s.Observer != "olga" || !s.ShowSkipped || !s.Until.Equal(at(10, 0, 0, 0)) {
		t.Errorf("header = %q %q %q %v %q skipped=%v until %v", s.Source, s.Name, s.Author, s.Day1, s.Observer, s.ShowSkipped, s.Until)
	}

	wantPeople := []story.Person{
		{Line: 6, Name: "alice"},
		{Line: 7, Name: "hana", Roles: []string{"hr_admin", "auditor"}},
		{Line: 8, Name: "kim", Customer: true},
		{Line: 9, Name: "olga"},
	}
	if !reflect.DeepEqual(s.People, wantPeople) {
		t.Errorf("people = %+v\nwant %+v", s.People, wantPeople)
	}
	if got := s.People[2].Principal(); got != "customer:kim" {
		t.Errorf("kim's principal = %q", got)
	}

	where := set("f_metric", word("first_response"))
	wantNames := []story.NameRule{
		{Line: 11, Name: "first", Type: "sla_goal", In: "inc", Where: &where},
		{Line: 12, Name: "any", Type: "sla_goal"},
	}
	if !reflect.DeepEqual(s.Names, wantNames) {
		t.Errorf("names = %+v\nwant %+v", s.Names, wantNames)
	}
	if want := map[string][]string{"sla_goal": {"f_week", "f_zone"}}; !reflect.DeepEqual(s.HideValues, want) {
		t.Errorf("hide_values = %v", s.HideValues)
	}

	wantFakes := []story.Fake{
		{Line: 17, Action: "hr.manager_of@1", Rules: []story.Rule{
			{Line: 18, When: []story.Assignment{set("createdBy", word("alice")), set("onBehalfOf", story.Value{Kind: story.Null})},
				Answer: story.Answer{Kind: story.Correct, Values: []story.Assignment{set("manager", word("bob"))}}},
			{Line: 19, From: at(1, 9, 0, 0), Until: at(1, 10, 0, 0), Answer: story.Answer{Kind: story.Fail, Failure: story.FailTransient}},
			{Line: 20, When: []story.Assignment{set("createdBy", word("carol"))}, From: at(2, 0, 0, 0),
				Answer: story.Answer{Kind: story.Deny, Reason: "unknown_employee"}},
		}},
		{Line: 21, Action: "email.notify@1", Rules: []story.Rule{{Line: 22, Answer: story.Answer{Kind: story.OK}}}},
		{Line: 23, Action: "erp.create_invoice@1", Rules: []story.Rule{
			{Line: 24, Answer: story.Answer{Kind: story.OK, Values: []story.Assignment{set("invoiceId", quoted("INV-1"))}}},
			{Line: 25, Answer: story.Answer{Kind: story.Allow}},
		}},
	}
	if !reflect.DeepEqual(s.Fakes, wantFakes) {
		t.Errorf("fakes = %+v\nwant %+v", s.Fakes, wantFakes)
	}

	wantGiven := []story.Step{{Line: 27, Text: "hana creates balance as leave_balance@1 with f_owner = alice, f_allowance = 20",
		Actor: "hana", Verb: story.Creates, Item: "balance", Type: "leave_balance", Version: 1,
		Values: []story.Assignment{set("f_owner", word("alice")), set("f_allowance", integer(20))}}}
	if !reflect.DeepEqual(s.Given, wantGiven) {
		t.Errorf("given = %+v\nwant %+v", s.Given, wantGiven)
	}

	lines := strings.Split(fullStory, "\n")
	text := func(n int) string { return strings.TrimPrefix(lines[n-1], "  - ") }
	wantSteps := []story.Step{
		{Line: 29, At: at(1, 9, 0, 0), Actor: "alice", Verb: story.Creates, Item: "request", Type: "vacation_request", Version: 3,
			Values: []story.Assignment{set("f_start", dayValue(15)), set("f_days", integer(5)), set("f_leave_type", word("annual"))}},
		{Line: 30, At: at(1, 9, 5, 30), Actor: "alice", Verb: story.Moves, Item: "request", To: "submitted"},
		{Line: 31, At: at(2, 10, 0, 0), Actor: "alice", Verb: story.Moves, Item: "request", To: "approved", Refused: "NOT_ALLOWED"},
		{Line: 32, At: at(2, 10, 0, 0), Actor: "alice", Verb: story.Updates, Item: "request",
			Values: []story.Assignment{set("f_comment", quoted("a, b = c")), set("f_end", story.Value{Kind: story.Null}),
				set("f_flag", story.Value{Kind: story.Bool, Bool: true})}},
		{Line: 33, At: at(2, 10, 0, 0), Actor: "kim", Verb: story.Creates, Item: "reply1", Type: "reply", Version: 2, In: "request",
			Values: []story.Assignment{set("f_body", quoted("x")),
				set("f_when", story.Value{Kind: story.DayTime, Day: 3, Time: 14*time.Hour + 30*time.Minute}),
				set("f_tags", story.Value{Kind: story.List, List: []story.Value{word("a"), quoted("b c")}})}},
		{Line: 34, At: at(2, 11, 0, 0), Actor: "hana", Verb: story.Links, Item: "balance", Link: "owner", Target: "request"},
		{Line: 35, At: at(2, 11, 0, 0), Actor: "hana", Verb: story.Unlinks, Item: "balance", Link: "owner", Target: "request"},
		{Line: 36, At: at(2, 11, 0, 0), Actor: "hana", Verb: story.Sees, Item: "balance",
			Values: []story.Assignment{set("f_used", integer(5))}},
		{Line: 37, At: at(2, 11, 0, 0), Actor: "kim", Verb: story.SeesFields, Item: "request", Fields: []string{"f_priority", "f_title"}},
		{Line: 38, At: at(2, 11, 0, 0), Actor: "kim", Verb: story.CannotSee, Item: "balance"},
	}
	for i := range wantSteps {
		wantSteps[i].Text = text(wantSteps[i].Line)
	}
	if len(s.Steps) != len(wantSteps) {
		t.Fatalf("%d steps, want %d", len(s.Steps), len(wantSteps))
	}
	for i := range wantSteps {
		if !reflect.DeepEqual(s.Steps[i], wantSteps[i]) {
			t.Errorf("step %d =\n  %+v\nwant\n  %+v", i, s.Steps[i], wantSteps[i])
		}
	}

	wantExpect := []story.Line{
		{Line: 41, At: at(1, 9, 0, 0), Item: "request", Text: "alice created it in draft"},
		{Line: 42, At: at(1, 9, 5, 30), Item: "request", Text: "alice asked draft -> submitted"},
	}
	if !reflect.DeepEqual(s.Expect, wantExpect) {
		t.Errorf("expect = %+v\nwant %+v", s.Expect, wantExpect)
	}
}

// Without until, a story has none; Run takes a year after the last step.
func TestParse_UntilIsOptional(t *testing.T) {
	s, err := story.Parse("u.yaml", []byte(minimal("")))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !s.Until.IsZero() {
		t.Errorf("until = %v, want none", s.Until)
	}
	if s.Observer != "" {
		t.Errorf("observer = %q, want none (the default)", s.Observer)
	}
}

// minimal is a sound story with extra appended to its steps (each extra
// line is one step, already indented). Its steps start on line 7.
func minimal(extra string) string {
	return `story: minimal
author: a test
day1: 2026-12-07
people:
  alice: {}
steps:
  - day 1 09:00 alice creates request as vacation_request@3
` + extra + `expect:
  - "day 1 09:00 request: alice created it in draft"
`
}

// problemsOf parses src and returns its problems; it fails the test if
// the file parses cleanly.
func problemsOf(t *testing.T, src string) story.Problems {
	t.Helper()
	_, err := story.Parse("bad.yaml", []byte(src))
	var ps story.Problems
	if !errors.As(err, &ps) || len(ps) == 0 {
		t.Fatalf("Parse error = %v, want Problems", err)
	}
	for _, p := range ps {
		if p.Source != "bad.yaml" {
			t.Errorf("problem %v names source %q", p, p.Source)
		}
	}
	return ps
}

// wantProblem checks that ps holds exactly one problem, on line, whose
// message contains every one of words.
func wantProblem(t *testing.T, ps story.Problems, line int, words ...string) {
	t.Helper()
	if len(ps) != 1 {
		t.Fatalf("problems = %v, want exactly one", ps)
	}
	if ps[0].Line != line {
		t.Errorf("problem on line %d, want %d: %v", ps[0].Line, line, ps[0])
	}
	for _, w := range words {
		if !strings.Contains(ps[0].Message, w) {
			t.Errorf("message %q does not say %q", ps[0].Message, w)
		}
	}
}

func TestParse_Problems(t *testing.T) {
	cases := []struct {
		name  string
		src   string
		line  int
		words []string
	}{
		{"no author", strings.Replace(minimal(""), "author: a test\n", "", 1), 1, []string{"author"}},
		{"a bad day1", strings.Replace(minimal(""), "2026-12-07", "2026-13-01", 1), 3, []string{"day1", "2026-13-01"}},
		{"an unknown key", strings.Replace(minimal(""), "people:", "stories: x\npeople:", 1), 4, []string{"stories"}},
		{"a time without two digits", minimal("  - day 1 9:00 alice moves request to submitted\n"), 8, []string{"9:00"}},
		{"a time past midnight", minimal("  - day 1 24:00 alice moves request to submitted\n"), 8, []string{"24:00"}},
		{"day 0", minimal("  - day 0 10:00 alice moves request to submitted\n"), 8, []string{"day 0"}},
		{"an unknown verb", minimal("  - day 1 10:00 alice deletes request\n"), 8, []string{"deletes"}},
		{"a create without its Type", minimal("  - day 1 10:00 alice creates other\n"), 8, []string{"as"}},
		{"a Type without its version", minimal("  - day 1 10:00 alice creates other as vacation_request\n"), 8, []string{"@"}},
		{"a move without its state", minimal("  - day 1 10:00 alice moves request\n"), 8, []string{"to"}},
		{"steps out of time order", minimal("  - day 1 08:00 alice moves request to submitted\n"), 8, []string{"order"}},
		{"an unterminated quote", minimal("  - 'day 1 10:00 alice updates request with f_comment = \"oops'\n"), 8, []string{"quote"}},
		{"a refusal without its reason", minimal("  - day 1 10:00 alice moves request to approved -> refused\n"), 8, []string{"refused"}},
		{"a refused read", minimal("  - day 1 10:00 alice sees request with f_days = 5 -> refused NOT_FOUND\n"), 8, []string{"refused"}},
		{"a given step with a time", strings.Replace(minimal(""), "steps:", "given:\n  - day 1 09:00 alice creates x as leave_balance@1\nsteps:", 1),
			7, []string{"given", "time"}},
		{"an expected line without its Item", strings.Replace(minimal(""), `"day 1 09:00 request: alice created it in draft"`,
			`"day 1 09:00 alice created it in draft"`, 1), 9, []string{"ITEM:"}},
		{"an unknown answer", strings.Replace(minimal(""), "steps:", "fakes:\n  email.notify@1:\n    - maybe\nsteps:", 1),
			8, []string{"maybe"}},
		{"an unknown failure", strings.Replace(minimal(""), "steps:", "fakes:\n  email.notify@1:\n    - fail sometimes\nsteps:", 1),
			8, []string{"sometimes"}},
		{"a fake without its major", strings.Replace(minimal(""), "steps:", "fakes:\n  email.notify:\n    - ok\nsteps:", 1),
			7, []string{"email.notify", "@"}},
		{"a name rule without its Type", strings.Replace(minimal(""), "steps:", "names:\n  first: where f_metric = x\nsteps:", 1),
			7, []string{"first"}},
		{"a person with an unknown key", strings.Replace(minimal(""), "alice: {}", "alice: {role: [x]}", 1), 5, []string{"role"}},
		{"a bad person's name", strings.Replace(minimal(""), "alice: {}", "Alice: {}", 1), 5, []string{"Alice"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			wantProblem(t, problemsOf(t, c.src), c.line, c.words...)
		})
	}
}

// Every problem of a file is reported at once, in line order.
func TestParse_EveryProblemAtOnce(t *testing.T) {
	ps := problemsOf(t, minimal("  - day 1 10:00 alice deletes request\n  - day 1 11:00 alice moves request\n"))
	if len(ps) != 2 || ps[0].Line != 8 || ps[1].Line != 9 {
		t.Errorf("problems = %v, want one on line 8 and one on line 9", ps)
	}
	if !strings.HasPrefix(ps.Error(), "bad.yaml:8: ") {
		t.Errorf("Error() = %q, want it to start with bad.yaml:8: ", ps.Error())
	}
}

// Every story in stories/ parses.
func TestParse_TheStories(t *testing.T) {
	files := storyFiles(t)
	for _, f := range files {
		t.Run(f.name, func(t *testing.T) {
			if _, err := story.Parse(f.path, f.data); err != nil {
				t.Errorf("%v", err)
			}
		})
	}
}
