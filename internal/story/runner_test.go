// Tests for Run, against the real kernel in simulated time: anything
// unexpected fails a story (a call no fake answers, a refusal, a read),
// the whole timeline counts, runs never see each other, and a run ends
// where its story says.
package story_test

import (
	"errors"
	"os"
	"os/exec"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/francoisnyc/work-kernel/internal/story"
)

// approveExpect is the whole expected timeline of approveStory: the lines
// of stories/vacation/approve.yaml.
func approveExpect() []story.Line {
	return []story.Line{
		line(at(1, 9, 0, 0), "request", "alice created it in draft"),
		line(at(1, 9, 5, 0), "request", "alice asked draft -> submitted"),
		line(at(1, 9, 5, 0), "request", "hr_check for alice moved it draft -> submitted: f_approver = bob; linked balance to balance"),
		line(at(2, 10, 0, 0), "request", "bob asked submitted -> approved"),
		line(at(2, 10, 0, 0), "balance", "deduct for bob updated it: f_used +5 = 5"),
		line(at(2, 10, 0, 0), "request", "deduct for bob moved it submitted -> approved"),
		line(at(2, 10, 0, 0), "request", "notify_approved for bob ran"),
		line(at(2, 10, 0, 0), "email", "notify for request (notify_approved): to = alice"),
	}
}

// run runs s and fails the test if the harness itself fails.
func run(t *testing.T, s *story.Story) *story.Result {
	t.Helper()
	res, err := story.Run(story.RunCtx(t), story.TestEnv(t), s)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res
}

// wantFailure checks that the run stopped on one failure, on line, naming
// every one of words.
func wantFailure(t *testing.T, res *story.Result, line int, words ...string) {
	t.Helper()
	if len(res.Failures) != 1 {
		t.Fatalf("failures = %+v, want exactly one", res.Failures)
	}
	f := res.Failures[0]
	if f.Line != line {
		t.Errorf("failure on line %d, want %d: %+v", f.Line, line, f)
	}
	for _, w := range words {
		if !strings.Contains(f.Message, w) {
			t.Errorf("failure %q does not say %q", f.Message, w)
		}
	}
}

func TestRun_TheApproveStory(t *testing.T) {
	s := approveStory()
	s.Expect = approveExpect()
	res := run(t, s)
	if len(res.Failures) != 0 {
		t.Fatalf("failures = %+v", res.Failures)
	}
	d := story.Compare(s.Expect, res.Timeline)
	if !d.Empty() || d.Matched != len(s.Expect) {
		t.Errorf("%s", story.Report(s, res, d))
	}
	if !res.Ended.Equal(s.Until) {
		t.Errorf("ended at %v, want until, %v", res.Ended, s.Until)
	}
	// The given step's own events are not lines: nothing at day 1 00:00.
	for _, l := range res.Timeline {
		if l.At.Equal(day1) {
			t.Errorf("a line of the given step: %s", l.Format(day1))
		}
	}
}

// The whole timeline counts: a line the story leaves out is unexpected, a
// line it adds is missing.
func TestRun_TheWholeTimelineCounts(t *testing.T) {
	s := approveStory()
	res := run(t, s)
	full := approveExpect()

	less := slices.Delete(slices.Clone(full), 6, 7) // no notify_approved line
	d := story.Compare(less, res.Timeline)
	if !reflect.DeepEqual(d.Unexpected, full[6:7]) || len(d.Missing) != 0 {
		t.Errorf("leaving a line out: diff = %+v, want just it unexpected", d)
	}

	extra := line(at(3, 10, 0, 0), "request", "remind_approver for bob ran (remind run 1)")
	d = story.Compare(append(slices.Clone(full), extra), res.Timeline)
	if !reflect.DeepEqual(d.Missing, []story.Line{extra}) || len(d.Unexpected) != 0 {
		t.Errorf("adding a line: diff = %+v, want just it missing", d)
	}
}

// Every call a fake answered is kept, with its inputs and its answer.
func TestRun_KeepsTheCalls(t *testing.T) {
	res := run(t, approveStory())
	if len(res.Calls) != 2 {
		t.Fatalf("calls = %+v, want the HR check and the approval email", res.Calls)
	}
	hr, mail := res.Calls[0], res.Calls[1]
	if hr.Action != "hr.manager_of@1" || hr.Hook != "hr_check" || hr.Answer != story.Correct || hr.SideEffect ||
		!hr.At.Equal(at(1, 9, 5, 0)) || hr.Inputs["createdBy"] != "user:alice" || hr.Inputs["onBehalfOf"] != nil {
		t.Errorf("the HR call = %+v", hr)
	}
	if mail.Action != "email.notify@1" || mail.Hook != "notify_approved" || mail.Answer != story.OK || !mail.SideEffect ||
		!mail.At.Equal(at(2, 10, 0, 0)) || mail.Inputs["to"] != "user:alice" {
		t.Errorf("the email = %+v", mail)
	}
}

// A story with problems does not run: Check's problems are Run's error.
func TestRun_RefusesAStoryWithProblems(t *testing.T) {
	s := approveStory()
	s.Fakes = s.Fakes[:1] // no email fake
	res, err := story.Run(story.RunCtx(t), story.TestEnv(t), s)
	var ps story.Problems
	if !errors.As(err, &ps) || res != nil {
		t.Fatalf("Run = %v, %v; want Problems and no result", res, err)
	}
}

// A call that no rule answers fails the story, at the step that caused
// it, and the run stops there.
func TestRun_ACallNoRuleAnswersFailsTheStory(t *testing.T) {
	s := approveStory()
	s.Fakes[0].Rules[0].When = []story.Assignment{set("createdBy", word("bob"))}
	res := run(t, s)
	wantFailure(t, res, 11, "hr.manager_of@1", "no rule", "createdBy = alice")
	if !res.Ended.Equal(at(1, 9, 5, 0)) {
		t.Errorf("ended at %v, want the step's instant (day 1 09:05)", res.Ended)
	}
}

// A write refused that the story did not expect to be refused fails it.
func TestRun_AnUnexpectedRefusalFailsTheStory(t *testing.T) {
	s := approveStory()
	s.Steps[2].Refused = ""
	wantFailure(t, run(t, s), 12, "NOT_ALLOWED")
}

// So does a write expected to be refused that was not.
func TestRun_ARefusalThatDoesNotComeFailsTheStory(t *testing.T) {
	s := approveStory()
	s.Steps[3].Refused = "NOT_ALLOWED"
	wantFailure(t, run(t, s), 13, "NOT_ALLOWED", "not refused")
}

// A read that sees another value fails the story, naming what it saw.
func TestRun_AReadThatSeesSomethingElseFailsTheStory(t *testing.T) {
	s := approveStory()
	s.Steps[4].Values = []story.Assignment{set("f_used", integer(4))}
	wantFailure(t, run(t, s), 14, "f_used", "4", "5")
}

// Reads are a person's: kim sees her own fields and none of the goals.
// A read naming one field too many fails.
func TestRun_ReadsAreThePersons(t *testing.T) {
	if res := run(t, deskStory()); len(res.Failures) != 0 {
		t.Fatalf("failures = %+v", res.Failures)
	}
	s := deskStory()
	s.Steps[2].Fields = []string{"f_due_first_response", "f_priority", "f_title"}
	wantFailure(t, run(t, s), 10, "f_due_first_response")
}

// The timeline is read as the observer. One who cannot see an Item of the
// story fails it, asking for another observer.
func TestRun_TheObserverMustSeeEveryItem(t *testing.T) {
	s := deskStory()
	s.Observer = "kim"
	res := run(t, s)
	if len(res.Failures) == 0 {
		t.Fatal("no failure: kim, a customer, cannot see the goals")
	}
	if m := res.Failures[0].Message; !strings.Contains(m, "kim") || !strings.Contains(m, "observer") {
		t.Errorf("failure = %q, want it to name kim and ask for an observer", m)
	}
}

// reaction_skipped lines are left out unless the story asks for them.
func TestRun_SkippedReactionsOnlyWhenAsked(t *testing.T) {
	hasSkipped := func(res *story.Result) bool {
		for _, l := range res.Timeline {
			if strings.Contains(l.Text, " skipped: ") {
				return true
			}
		}
		return false
	}
	if res := run(t, deskStory()); hasSkipped(res) {
		t.Error("a skipped line without show_skipped")
	}
	s := deskStory()
	s.ShowSkipped = true
	if res := run(t, s); !hasSkipped(res) {
		t.Error("no skipped line with show_skipped: every goal skips the set reactions of the other metrics")
	}
}

// Without until, a run goes on for a year after the last step.
func TestRun_UntilByDefault(t *testing.T) {
	s := approveStory()
	s.Until = time.Time{}
	res := run(t, s)
	if want := at(2, 10, 0, 0).Add(365 * 24 * time.Hour); !res.Ended.Equal(want) {
		t.Errorf("ended at %v, want a year after the last step, %v", res.Ended, want)
	}
}

// Each run has a namespace of its own: the same story twice, and the
// second never finds the first's balance (two would make the request's
// balance link ambiguous).
func TestRun_RunsNeverSeeEachOther(t *testing.T) {
	for i := range 2 {
		s := approveStory()
		s.Expect = approveExpect()
		res := run(t, s)
		if d := story.Compare(s.Expect, res.Timeline); len(res.Failures) != 0 || !d.Empty() {
			t.Fatalf("run %d:\n%s", i+1, story.Report(s, res, d))
		}
	}
}

// Five simulated days take no real time worth speaking of.
func TestRun_SimulatedTimeIsFast(t *testing.T) {
	data, err := os.ReadFile("../../stories/vacation/expiry.yaml")
	if err != nil {
		t.Fatal(err)
	}
	s, err := story.Parse("stories/vacation/expiry.yaml", data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	story.TestEnv(t) // Postgres up first: its start is not the story's
	began := time.Now()
	res := run(t, s)
	took := time.Since(began)
	t.Logf("the expiry story: %d lines, nine simulated days, in %v", len(res.Timeline), took)
	if took > 15*time.Second {
		t.Errorf("the expiry story took %v; simulated time should make it a second or so", took)
	}
}

// Report says what went wrong, for a person: the failure with its line,
// then the timeline instant by instant, missing lines with -, unexpected
// with +, and how many matched.
func TestReport_ForAPerson(t *testing.T) {
	s := &story.Story{Source: "stories/vacation/approve.yaml", Name: "an approved request", Day1: day1}
	res := &story.Result{Failures: []story.Failure{{Line: 14, Message: "hana sees balance: f_used is 4, want 5"}},
		Ended: at(2, 10, 0, 0)}
	d := story.Diff{Matched: 3,
		Missing:    []story.Line{line(at(2, 10, 0, 0), "balance", "deduct for bob updated it: f_used +5 = 5")},
		Unexpected: []story.Line{line(at(2, 10, 0, 0), "balance", "deduct for bob updated it: f_used +5 = 15")},
	}
	r := story.Report(s, res, d)
	for _, want := range []string{
		"stories/vacation/approve.yaml", "an approved request",
		"stories/vacation/approve.yaml:14: hana sees balance: f_used is 4, want 5",
		"3 lines matched",
		"day 2 10:00",
		"- balance: deduct for bob updated it: f_used +5 = 5",
		"+ balance: deduct for bob updated it: f_used +5 = 15",
	} {
		if !strings.Contains(r, want) {
			t.Errorf("the report does not say %q:\n%s", want, r)
		}
	}
	if strings.Index(r, "- balance") > strings.Index(r, "+ balance") {
		t.Errorf("the missing line should come before the unexpected one:\n%s", r)
	}
	if ok := story.Report(s, &story.Result{Ended: at(10, 0, 0, 0)}, story.Diff{Matched: 8}); !strings.Contains(ok, "as expected") {
		t.Errorf("a clean run's report = %q, want it to say as expected", ok)
	}
}

// The runner is a test harness: no binary links it (it may set the item
// service's Clock, which only tests may).
func TestNoBinaryLinksTheRunner(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "../../cmd/...").CombinedOutput()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, out)
	}
	for _, dep := range strings.Fields(string(out)) {
		if dep == "github.com/francoisnyc/work-kernel/internal/story" {
			t.Error("a command under cmd/ links internal/story")
		}
	}
	if !strings.Contains(string(out), "github.com/francoisnyc/work-kernel/internal/itemservice") {
		t.Error("go list found no command linking the item service: the test is not looking where it should")
	}
}
