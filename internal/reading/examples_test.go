package reading_test

import (
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/contracts"
	"github.com/francoisnyc/work-kernel/internal/reading"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// The approved readings: readings/<type>@<version>.txt, read and approved
// by the owner, compared exactly. A change to a Type or to the renderer
// that changes a reading fails here; the new text is pasted into the file
// by hand and approved again in the pull request's diff. There is no
// -update, as for stories.
func TestReadings_TheApprovedOnes(t *testing.T) {
	types, err := typedef.ParseDir("../../types")
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]*typedef.Type{}
	for _, ty := range types {
		byName[ty.ID+"@"+strconv.Itoa(ty.Version)] = ty
	}
	for _, name := range []string{"vacation_request@3", "incident@3"} {
		t.Run(name, func(t *testing.T) {
			want, err := os.ReadFile("../../readings/" + name + ".txt")
			if err != nil {
				t.Fatal(err)
			}
			ty, ok := byName[name]
			if !ok {
				t.Fatalf("no Type %s in types/", name)
			}
			got := reading.Read(ty, contracts.Default()).Text()
			if got != string(want) {
				t.Errorf("the reading of %s differs from readings/%s.txt (first difference at line %d).\nThe reading now:\n%s",
					name, name, firstDiffLine(got, string(want)), got)
			}
		})
	}
}

func firstDiffLine(a, b string) int {
	al, bl := strings.Split(a, "\n"), strings.Split(b, "\n")
	for i := range min(len(al), len(bl)) {
		if al[i] != bl[i] {
			return i + 1
		}
	}
	return min(len(al), len(bl)) + 1
}

// The layout of a reading, pinned on a small Type: the heading, the key
// (only the terms the sentences use), the field list, the numbered
// sentences under their sections, and what no reading covers. A Type that
// calls no action has no actions section.
func TestText_TheLayout(t *testing.T) {
	ty := fixtures(t)["w_leave@1"]
	want := "w_leave@1\n" +
		"\n" +
		"Key\n" +
		"- staff: people in the organisation, and agents acting for them; a customer is never staff.\n" +
		"- a customer: a person outside the organisation.\n" +
		"- an agent: a program acting for a person; it may do only what both it and that person may do, and a field that names a person never names an agent.\n" +
		"\n" +
		"Fields\n" +
		"- `f_start`: a date\n" +
		"- `f_end`: a date\n" +
		"- `f_days`: a whole number\n" +
		"- `f_note`: text\n" +
		"- `f_sent_at`: an instant\n" +
		"\n" +
		"In short\n" +
		"1. A w_leave starts in draft.\n" +
		"2. It moves only as listed below; any other move is refused.\n" +
		"3. Every write keeps this true: `f_days` is unset or `f_days` is at least 0; a write that would not is refused (days_negative).\n" +
		"\n" +
		"draft (to do)\n" +
		"4. Any staff member who can see it moves it from draft to submitted, once `f_start` and `f_end` are set, " +
		"only if `f_end` is on or after `f_start` and `f_start` is on or after today; the move sets `f_sent_at` to the time of the write.\n" +
		"\n" +
		"submitted (waiting)\n" +
		"5. While it is in submitted, nobody changes `f_start` or `f_end`.\n" +
		"6. Any staff member who can see it moves it from submitted to draft; the move clears `f_note`.\n" +
		"7. Any staff member who can see it moves it from submitted to done.\n" +
		"\n" +
		"done (done)\n" +
		"8. Nothing leaves done.\n" +
		"\n" +
		"Who sees it\n" +
		"9. A customer who sees it sees none of its fields, only its header.\n" +
		"10. Anyone who can see it moves it to another container or narrows who sees it.\n" +
		"\n" +
		"Not covered by this reading\n" +
		"- Who can see a given w_leave: that is set for each Item and its container, not by its Type.\n" +
		"- Who may create a w_leave, and where, once a later version exists: the latest version of w_leave decides, for every version.\n" +
		"- How the kernel keeps its promises (retries, each write made once, recovery after a failure): the same for every Type.\n"
	if got := reading.Read(ty, contracts.Default()).Text(); got != want {
		t.Errorf("Text() =\n%s\nwant\n%s", got, want)
	}
}

// A Type with no lifecycle reads as one, with no state section; a Type
// whose fields are all a customer may see, and which owns others, says so.
func TestText_NoLifecycle(t *testing.T) {
	ty := fixtures(t)["w_portal@1"]
	want := "w_portal@1\n" +
		"\n" +
		"Key\n" +
		"- a customer: a person outside the organisation.\n" +
		"\n" +
		"In short\n" +
		"1. A w_portal has no lifecycle: it stays in `default`.\n" +
		"\n" +
		"Who sees it\n" +
		"2. A customer who sees it sees none of its fields, only its header.\n" +
		"3. A customer may create a w_who or a w_plain inside it, and sees the ones they requested.\n" +
		"4. Anyone who can see it moves it to another container or narrows who sees it.\n" +
		"\n" +
		"Not covered by this reading\n" +
		"- Who can see a given w_portal: that is set for each Item and its container, not by its Type.\n" +
		"- Who may create a w_portal, and where, once a later version exists: the latest version of w_portal decides, for every version.\n" +
		"- How the kernel keeps its promises (retries, each write made once, recovery after a failure): the same for every Type.\n"
	if got := reading.Read(ty, contracts.Default()).Text(); got != want {
		t.Errorf("Text() =\n%s\nwant\n%s", got, want)
	}
}

// Each sentence knows what it reads: the sentences of the vacation
// request's approval cite the move, the held reaction, and the hold.
func TestSentences_CiteWhatTheyRead(t *testing.T) {
	types, err := typedef.ParseDir("../../types")
	if err != nil {
		t.Fatal(err)
	}
	var vr *typedef.Type
	for _, ty := range types {
		if ty.ID == "vacation_request" && ty.Version == 3 {
			vr = ty
		}
	}
	r := reading.Read(vr, contracts.Default())
	want := map[string][]string{
		"Whoever `f_approver` names, but never anyone who took part in creating it, moves it from submitted to approved.": {"transitions[1]"},
		"Asking for this move opens a hold instead: `deduct` adds `f_days` to `f_used` on the leave_balance linked as `balance`; " +
			"the move happens only if that Item accepts the write, else it is denied with that Item's reason.": {"reactions[0]", "links[0]"},
		"While that hold is open, nobody changes `f_days`, and the only move out of submitted is to withdrawn.": {"reactions[0]", "transitions[4]"},
		"Nothing leaves expired.":                            {"states[5]"},
		"Only the timer moves it from submitted to expired.": {"transitions[6]"},
	}
	for text, paths := range want {
		s, ok := findSentence(r, text)
		if !ok {
			t.Errorf("no sentence %q", text)
			continue
		}
		if strings.Join(s.Paths, " ") != strings.Join(paths, " ") {
			t.Errorf("%q cites %v, want %v", text, s.Paths, paths)
		}
	}
}
