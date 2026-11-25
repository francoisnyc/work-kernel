// Tests for the story step that reads the "waiting on me" view (track 1,
// item 5, step 1; the witness of the reading's "it waits on" sentence
// needs it):
//
//	time person finds waiting ITEM, ITEM, ...   the person's view holds exactly these Items
//	time person finds nothing waiting           the person's view is empty
//
// A read, through the item service's public Search with waiting_on = me,
// as the person, every page: a view that holds anything else, or lacks
// one, fails the story at the step's line. Like every read it may not be
// refused, and its Items are ones the story knows.
package story_test

import (
	"reflect"
	"slices"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/contracts"
	"github.com/francoisnyc/work-kernel/internal/story"
)

func TestParse_FindsWaiting(t *testing.T) {
	s, err := story.Parse("waiting.yaml", []byte(minimal(
		"  - day 1 10:00 alice finds waiting request\n"+
			"  - day 1 10:01 alice finds waiting request, other\n"+
			"  - day 1 10:02 alice finds nothing waiting\n")))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := []story.Step{
		{Line: 8, At: at(1, 10, 0, 0), Text: "day 1 10:00 alice finds waiting request", Actor: "alice",
			Verb: story.FindsWaiting, Items: []string{"request"}},
		{Line: 9, At: at(1, 10, 1, 0), Text: "day 1 10:01 alice finds waiting request, other", Actor: "alice",
			Verb: story.FindsWaiting, Items: []string{"request", "other"}},
		{Line: 10, At: at(1, 10, 2, 0), Text: "day 1 10:02 alice finds nothing waiting", Actor: "alice",
			Verb: story.FindsWaiting},
	}
	if len(s.Steps) != 4 {
		t.Fatalf("%d steps, want 4", len(s.Steps))
	}
	for i, w := range want {
		if !reflect.DeepEqual(s.Steps[i+1], w) {
			t.Errorf("step %d =\n%+v\nwant\n%+v", i+1, s.Steps[i+1], w)
		}
	}
}

func TestParse_FindsWaiting_Problems(t *testing.T) {
	cases := []struct {
		name, step string
		words      []string
	}{
		{"finds alone", "alice finds", []string{"finds waiting ITEM", "finds nothing waiting"}},
		{"finds waiting with no Item", "alice finds waiting", []string{"Item"}},
		{"finds nothing", "alice finds nothing", []string{"finds nothing waiting"}},
		{"finds something else", "alice finds request", []string{"finds waiting ITEM"}},
		{"two Items without a comma", "alice finds waiting request other", []string{"other"}},
		{"a trailing comma", "alice finds waiting request,", []string{"Item"}},
		{"nothing waiting, and more", "alice finds nothing waiting request", []string{"request"}},
		{"a refused read", "alice finds waiting request -> refused NOT_FOUND", []string{"refused"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			wantProblem(t, problemsOf(t, minimal("  - day 1 10:00 "+c.step+"\n")), 8, c.words...)
		})
	}
}

// waitingStory is the approve story with one more step, after alice
// submits her request and before anyone decides: who finds what waiting.
// The request then waits on bob, the approver the HR check named.
func waitingStory(actor string, items ...string) *story.Story {
	s := approveStory()
	s.Expect = approveExpect()
	s.Steps = slices.Insert(s.Steps, 2, story.Step{
		Line: 20, At: at(1, 12, 0, 0), Actor: actor, Verb: story.FindsWaiting, Items: items,
		Text: "day 1 12:00 " + actor + " finds waiting",
	})
	return s
}

// Check: the Items a finds-waiting step names are ones the story knows.
func TestCheck_FindsWaiting(t *testing.T) {
	if ps := story.Check(waitingStory("bob", "request"), repoTypes(t), contracts.Default()); len(ps) != 0 {
		t.Errorf("a sound story has problems: %v", ps)
	}
	if ps := story.Check(waitingStory("bob"), repoTypes(t), contracts.Default()); len(ps) != 0 {
		t.Errorf("finds nothing waiting has problems: %v", ps)
	}
	wantProblem(t, story.Check(waitingStory("bob", "request", "ghost"), repoTypes(t), contracts.Default()), 20, "ghost")
	wantProblem(t, story.Check(waitingStory("zed", "request"), repoTypes(t), contracts.Default()), 20, "zed")
}

// Run: the step reads the person's view. bob, the approver, finds the
// request; alice, who asked, finds nothing. A step that expects anything
// else fails the story at its line, saying what the view holds.
func TestRun_FindsWaiting(t *testing.T) {
	for name, s := range map[string]*story.Story{
		"the approver finds it":       waitingStory("bob", "request"),
		"the requester finds nothing": waitingStory("alice"),
		// leave_balance@1: only an hr_admin closes an open balance.
		"hana, in HR, finds the balance": waitingStory("hana", "balance"),
	} {
		if res := run(t, s); len(res.Failures) != 0 {
			t.Errorf("%s: failures = %+v, want none", name, res.Failures)
		}
	}
	wantFailure(t, run(t, waitingStory("alice", "request")), 20, "alice", "finds waiting nothing", "want request")
	wantFailure(t, run(t, waitingStory("bob")), 20, "bob", "finds waiting request", "want nothing")
	wantFailure(t, run(t, waitingStory("bob", "request", "balance")), 20, "finds waiting request", "want balance, request")
}
