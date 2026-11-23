// Tests for Compare and for how lines and times are written. The order of
// instants is strict; within an instant, lines compare as a multiset;
// runs of spaces count as one.
package story_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/francoisnyc/work-kernel/internal/story"
)

func line(t time.Time, item, text string) story.Line {
	return story.Line{At: t, Item: item, Text: text}
}

func TestFormatTime(t *testing.T) {
	for _, c := range []struct {
		at   time.Time
		want string
	}{
		{at(1, 0, 0, 0), "day 1 00:00"},
		{at(3, 10, 0, 0), "day 3 10:00"},
		{at(1, 9, 19, 3), "day 1 09:19:03"},
		{at(1, 9, 0, 0).Add(500 * time.Millisecond), "day 1 09:00"}, // to the second
		{at(14, 23, 59, 59), "day 14 23:59:59"},
	} {
		if got := story.FormatTime(day1, c.at); got != c.want {
			t.Errorf("FormatTime(%v) = %q, want %q", c.at, got, c.want)
		}
	}
}

func TestLine_Format(t *testing.T) {
	l := line(at(2, 10, 0, 0), "balance", "deduct for bob updated it: f_used +5 = 5")
	if got, want := l.Format(day1), "day 2 10:00 balance: deduct for bob updated it: f_used +5 = 5"; got != want {
		t.Errorf("Format = %q, want %q", got, want)
	}
}

func TestCompare_TheSameTimeline(t *testing.T) {
	tl := []story.Line{
		line(at(1, 9, 0, 0), "request", "alice created it in draft"),
		line(at(1, 9, 5, 0), "request", "alice asked draft -> submitted"),
		line(at(1, 9, 5, 0), "request", "hr_check for alice moved it draft -> submitted"),
	}
	d := story.Compare(tl, tl)
	if !d.Empty() || d.Matched != 3 {
		t.Errorf("diff = %+v, want empty with 3 matched", d)
	}
}

// Concurrent workflows: the lines of one instant may come in any order.
func TestCompare_AnInstantIsAMultiset(t *testing.T) {
	expected := []story.Line{
		line(at(5, 15, 0, 0), "inc", "show_first_response for kim updated it: f_due_first_response = day 8 11:00"),
		line(at(5, 15, 0, 0), "inc", "show_resolution for kim updated it: f_due_resolution = day 9 15:00"),
	}
	seen := []story.Line{expected[1], expected[0]}
	if d := story.Compare(expected, seen); !d.Empty() || d.Matched != 2 {
		t.Errorf("diff = %+v, want empty", d)
	}
}

// Across instants the order is strict: the same lines at other instants
// are missing at one and unexpected at the other.
func TestCompare_InstantsAreStrict(t *testing.T) {
	expected := []story.Line{
		line(at(3, 9, 0, 0), "request", "remind_approver for alice ran (remind run 1)"),
		line(at(4, 9, 0, 0), "request", "remind_approver for alice ran (remind run 2)"),
	}
	seen := []story.Line{
		line(at(3, 9, 0, 1), "request", "remind_approver for alice ran (remind run 1)"),
		line(at(4, 9, 0, 0), "request", "remind_approver for alice ran (remind run 2)"),
	}
	d := story.Compare(expected, seen)
	if d.Matched != 1 || !reflect.DeepEqual(d.Missing, expected[:1]) || !reflect.DeepEqual(d.Unexpected, seen[:1]) {
		t.Errorf("diff = %+v, want run 1 missing at 09:00 and unexpected at 09:00:01", d)
	}
}

func TestCompare_MissingAndUnexpected(t *testing.T) {
	expected := []story.Line{
		line(at(2, 10, 0, 0), "request", "bob asked submitted -> approved"),
		line(at(2, 10, 0, 0), "balance", "deduct for bob updated it: f_used +5 = 5"),
	}
	seen := []story.Line{
		line(at(2, 10, 0, 0), "request", "bob asked submitted -> approved"),
		line(at(2, 10, 0, 0), "balance", "deduct for bob updated it: f_used +5 = 15"),
		line(at(2, 10, 0, 0), "email", "notify for request (notify_approved): to = alice"),
	}
	d := story.Compare(expected, seen)
	if d.Matched != 1 {
		t.Errorf("matched = %d, want 1", d.Matched)
	}
	if !reflect.DeepEqual(d.Missing, expected[1:]) {
		t.Errorf("missing = %+v, want the deduction of 5", d.Missing)
	}
	if !reflect.DeepEqual(d.Unexpected, seen[1:]) {
		t.Errorf("unexpected = %+v, want the deduction of 15 and the email", d.Unexpected)
	}
}

// A line expected twice and seen once is missing once: a multiset, not a
// set.
func TestCompare_CountsRepeats(t *testing.T) {
	l := line(at(1, 10, 0, 0), "email", "notify for request (remind_approver): to = bob")
	d := story.Compare([]story.Line{l, l}, []story.Line{l})
	if d.Matched != 1 || len(d.Missing) != 1 || len(d.Unexpected) != 0 {
		t.Errorf("diff = %+v, want 1 matched and 1 missing", d)
	}
	d = story.Compare([]story.Line{l}, []story.Line{l, l})
	if d.Matched != 1 || len(d.Missing) != 0 || len(d.Unexpected) != 1 {
		t.Errorf("diff = %+v, want 1 matched and 1 unexpected", d)
	}
}

// Runs of spaces count as one, so a story may align its lines; the item
// and the text are compared, not their spacing.
func TestCompare_Spacing(t *testing.T) {
	expected := []story.Line{line(at(1, 9, 0, 0), "request", "alice  created it   in draft")}
	seen := []story.Line{line(at(1, 9, 0, 0), "request", "alice created it in draft")}
	if d := story.Compare(expected, seen); !d.Empty() {
		t.Errorf("diff = %+v, want empty", d)
	}
}

// Missing and unexpected lines come in time order whatever order they
// were given in, so a report reads from the start.
func TestCompare_ReportsInTimeOrder(t *testing.T) {
	late := line(at(6, 9, 0, 0), "request", "timer for alice moved it submitted -> expired")
	early := line(at(3, 9, 0, 0), "request", "remind_approver for alice ran (remind run 1)")
	d := story.Compare([]story.Line{late, early}, nil)
	if !reflect.DeepEqual(d.Missing, []story.Line{early, late}) {
		t.Errorf("missing = %+v, want day 3 then day 6", d.Missing)
	}
}
