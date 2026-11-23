// The two risks Phase A named, each confirmed by a test of the harness
// with the real Item workflow:
//
//   - Continue-as-new: Temporal's test environment runs a child workflow's
//     continue-as-new itself. The Item's workflow must go on in its new run,
//     carrying what it was doing, and later deliveries must reach that run.
//     An Item's workflow that completes must also start again when its
//     Type says an event needs one.
//   - Update-validator refusals: a delivery the workflow's validator refuses
//     (a gap in item_seq) must reach the relay as an error, as it does on
//     the server, and the harness must report it, not hang.
package story

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	wf "github.com/francoisnyc/work-kernel/internal/workflow"
)

func readStory(t *testing.T, path string) *Story {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s, err := Parse(path, data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return s
}

// count is how many lines of the timeline are item: text, whenever.
func count(tl []Line, item, text string) int {
	n := 0
	for _, l := range tl {
		if l.Item == item && l.Text == text {
			n++
		}
	}
	return n
}

// A refund that fails UndoAttemptsPerRun times makes the request's run
// continue as new, carrying the undo; its next attempt, in the new run,
// succeeds. The refund, its record and its email each happen once, and
// nothing failed.
func TestHarness_ContinueAsNewCarriesTheItemsWork(t *testing.T) {
	s := readStory(t, "../../stories/vacation/refund.yaml")
	// Without the story's read at the withdrawal: the injected failures
	// delay the refund past it, by design (agreed in Phase C).
	s.Steps = s.Steps[:4]
	res, err := run(runCtx(t), testEnv(t), s, runOptions{failActivity: map[string]int{wf.ActivityUndo: wf.UndoAttemptsPerRun}})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.Failures) != 0 {
		t.Fatalf("failures = %+v", res.Failures)
	}
	if res.ContinuedAsNew["request"] < 1 {
		t.Errorf("the request's workflow continued as new %d times, want at least once", res.ContinuedAsNew["request"])
	}
	for _, want := range []struct{ item, text string }{
		{"balance", "refund for alice updated it: f_used -5 = 0"},
		{"request", "refund for alice applied to balance"},
		{"request", "refund_email for alice ran"},
		{"email", "notify for request (refund_email): to = alice"},
	} {
		if n := count(res.Timeline, want.item, want.text); n != 1 {
			t.Errorf("%s: %q seen %d times, want once", want.item, want.text, n)
		}
	}
	for _, l := range res.Timeline {
		if strings.Contains(l.Text, "failed") {
			t.Errorf("a failure in the timeline: %s", l.Format(s.Day1))
		}
	}
	// Before the withdrawal, the story's own lines, exactly.
	withdrawn := s.Steps[3].At
	var expected, seen []Line
	for _, l := range s.Expect {
		if l.At.Before(withdrawn) {
			expected = append(expected, l)
		}
	}
	for _, l := range res.Timeline {
		if l.At.Before(withdrawn) {
			seen = append(seen, l)
		}
	}
	if d := Compare(expected, seen); !d.Empty() {
		t.Errorf("before the withdrawal:\n%s", Report(s, res, d))
	}
}

// The approved request's workflow completes (approved has no behavior);
// withdrawing it needs one again (the refund), and the harness starts a new
// run for the same Item, as Update-With-Start would.
func TestHarness_AnItemsWorkflowStartsAgainAfterItCompletes(t *testing.T) {
	s := readStory(t, "../../stories/vacation/refund.yaml")
	res, err := run(runCtx(t), testEnv(t), s, runOptions{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.Failures) != 0 {
		t.Fatalf("failures = %+v", res.Failures)
	}
	if res.Runs["request"] < 2 {
		t.Errorf("the request's workflow had %d runs, want at least 2 (one until approved, one for the refund)", res.Runs["request"])
	}
	if d := Compare(s.Expect, res.Timeline); !d.Empty() {
		t.Errorf("%s", Report(s, res, d))
	}
}

// One of the request's events is marked delivered behind the relay's back,
// so the next one arrives with a gap. The workflow's validator refuses it;
// the refusal reaches the relay as an error, and the harness reports it as
// the story's failure, at the step whose write could not be delivered,
// instead of waiting forever.
func TestHarness_AValidatorRefusalIsReported(t *testing.T) {
	s := &Story{
		Source: "gap.go", Name: "a gap", Author: "a test", Day1: time.Date(2026, 12, 7, 0, 0, 0, 0, time.UTC),
		People: []Person{{Line: 1, Name: "alice"}, {Line: 2, Name: "bob"}, {Line: 3, Name: "hana", Roles: []string{"hr_admin"}}},
		Fakes: []Fake{
			{Line: 4, Action: "hr.manager_of@1", Rules: []Rule{{Line: 5,
				When:   []Assignment{{Field: "createdBy", Value: Value{Kind: Word, Text: "alice"}}},
				Answer: Answer{Kind: Correct, Values: []Assignment{{Field: "manager", Value: Value{Kind: Word, Text: "bob"}}}}}}},
			{Line: 6, Action: "email.notify@1", Rules: []Rule{{Line: 7, Answer: Answer{Kind: OK}}}},
		},
		Given: []Step{{Line: 8, Actor: "hana", Verb: Creates, Item: "balance", Type: "leave_balance", Version: 1,
			Values: []Assignment{{Field: "f_owner", Value: Value{Kind: Word, Text: "alice"}},
				{Field: "f_allowance", Value: Value{Kind: Int, Int: 20}}}}},
	}
	day := func(n, hh int) time.Time { return s.Day1.AddDate(0, 0, n-1).Add(time.Duration(hh) * time.Hour) }
	s.Steps = []Step{
		{Line: 10, At: day(1, 9), Actor: "alice", Verb: Creates, Item: "request", Type: "vacation_request", Version: 3,
			Values: []Assignment{{Field: "f_start", Value: Value{Kind: Day, Day: 15}}, {Field: "f_end", Value: Value{Kind: Day, Day: 19}},
				{Field: "f_days", Value: Value{Kind: Int, Int: 5}}, {Field: "f_leave_type", Value: Value{Kind: Word, Text: "annual"}}}},
		{Line: 11, At: day(1, 10), Actor: "alice", Verb: Moves, Item: "request", To: "submitted"},
		{Line: 12, At: day(2, 9), Actor: "alice", Verb: Updates, Item: "request",
			Values: []Assignment{{Field: "f_comment", Value: Value{Kind: Quoted, Text: "one"}}}},
		{Line: 13, At: day(2, 9), Actor: "alice", Verb: Updates, Item: "request",
			Values: []Assignment{{Field: "f_comment", Value: Value{Kind: Quoted, Text: "two"}}}},
	}
	skip := func(ctx context.Context, h *harness, i int) error {
		if i != 2 {
			return nil
		}
		// The first update's row, delivered behind the relay's back.
		_, err := h.env.pool.Exec(ctx, `update outbox set delivered_at = now()
			where item_id = $1 and item_seq = (select max(item_seq) from item_events where item_id = $1)`, h.id("request"))
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	began := time.Now()
	res, err := run(ctx, testEnv(t), s, runOptions{afterStep: skip})
	if err != nil {
		t.Fatalf("run: %v (after %v)", err, time.Since(began))
	}
	if len(res.Failures) != 1 {
		t.Fatalf("failures = %+v, want the refused delivery", res.Failures)
	}
	f := res.Failures[0]
	if f.Line != 13 || !strings.Contains(f.Message, "request") || !strings.Contains(f.Message, "gap") {
		t.Errorf("failure = %+v, want line 13, naming the request and the gap", f)
	}
}
