// Tests for presentation hints in the reading (the Inbox for people, PR
// 1a; the owner's decisions of its Phase A, 2026-11-27). Where a Type
// gives labels:
//
//   - a state's heading names it by its label, its ID in code font and
//     its category after it: "Waiting for a decision (`open`, waiting)";
//   - the field list puts a field's label beside its ID:
//     "`f_owner` (Approver): a person";
//   - a move's sentence starts with the move's label:
//     "Approve: whoever `f_owner` names moves it from open to done."
//
// Inside sentences states and fields stay IDs (labels need not be unique
// across kinds). help, primary, summary and due are left out: they say how
// a screen shows the Type, nothing the engine does. A Type without hints
// reads exactly as before, and so does one with only those four.
package reading_test

import (
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/francoisnyc/work-kernel/internal/contracts"
	"github.com/francoisnyc/work-kernel/internal/reading"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// wordsSrc is a Type with no hints. LABELS, OTHERS and SUMMARY are where
// the hints go.
const wordsSrc = `
id: wd
version: 1
initial_state: open
SUMMARY
fields:
  - {id: f_title,  type: string FTITLE}
  - {id: f_owner,  type: principal FOWNER}
  - {id: f_due_at, type: datetime}
states:
  - {id: open,    category: waiting SOPEN}
  - {id: done,    category: done SDONE, timer: {after: 24h, to: closed}}
  - {id: dropped, category: canceled}
  - {id: closed,  category: done}
transitions:
  - {from: open,    to: done,    allowed: ['field:f_owner'], required: [f_title] TAPPROVE}
  - {from: open,    to: dropped, allowed: ['field:f_owner'] TREFUSE}
  - {from: done,    to: closed,  allowed: ['principal:system:timer']}
  - {from: dropped, to: open,    allowed: [creator]}
`

func words(t *testing.T, hints map[string]string) *typedef.Type {
	t.Helper()
	src := wordsSrc
	for _, k := range []string{"SUMMARY", "FTITLE", "FOWNER", "SOPEN", "SDONE", "TAPPROVE", "TREFUSE"} {
		src = strings.Replace(src, " "+k, hints[k], 1)
		src = strings.Replace(src, k, hints[k], 1) // SUMMARY sits alone on its line
	}
	ty, err := typedef.Parse([]byte(src))
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, src)
	}
	return ty
}

// labels are the hints that change the reading.
var labels = map[string]string{
	"FOWNER":   ", label: Approver",
	"SOPEN":    ", label: Waiting for a decision",
	"SDONE":    ", label: Approved",
	"TAPPROVE": ", label: Approve",
}

// others are the hints the reading leaves out.
var others = map[string]string{
	"SUMMARY":  "summary: [f_title, f_owner]",
	"FTITLE":   ", help: What the person asks for",
	"SOPEN":    ", help: The approver decides, due: f_due_at",
	"TAPPROVE": ", help: The request goes ahead, primary: true",
	"TREFUSE":  ", help: It ends here",
}

func both(a, b map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] += v
	}
	return out
}

func read(ty *typedef.Type) *reading.Reading { return reading.Read(ty, contracts.Default()) }

// help, primary, summary and due change no word of the reading.
func TestReading_HintsLeftOutChangeNothing(t *testing.T) {
	plain := read(words(t, nil)).Text()
	if got := read(words(t, others)).Text(); got != plain {
		t.Errorf("with help, primary, summary and due the reading is\n%s\nwant, as without them,\n%s", got, plain)
	}
}

func TestReading_Labels(t *testing.T) {
	plain := read(words(t, nil))
	got := read(words(t, both(labels, others)))

	// The headings.
	wantTitles := map[string]string{
		"open (waiting)": "Waiting for a decision (`open`, waiting)",
		"done (done)":    "Approved (`done`, done)",
	}
	if len(got.Sections) != len(plain.Sections) {
		t.Fatalf("%d sections, want %d as without labels", len(got.Sections), len(plain.Sections))
	}
	for i, sec := range plain.Sections {
		want := sec.Title
		if w, ok := wantTitles[sec.Title]; ok {
			want = w
		}
		if got.Sections[i].Title != want {
			t.Errorf("section %d is headed %q, want %q", i, got.Sections[i].Title, want)
		}
	}

	// The field list.
	wantFields := []string{"`f_title`: text", "`f_owner` (Approver): a person", "`f_due_at`: an instant"}
	if strings.Join(got.Fields, "\n") != strings.Join(wantFields, "\n") {
		t.Errorf("fields = %q, want %q", got.Fields, wantFields)
	}
	if strings.Join(plain.Fields, "\n") != "`f_title`: text\n`f_owner`: a person\n`f_due_at`: an instant" {
		t.Errorf("fields without labels = %q", plain.Fields)
	}

	// The sentences: the labelled move's starts with its label; every
	// other sentence, its number, kind, parts and paths, as without labels.
	const approve = "Approve: whoever `f_owner` names moves it from open to done, once `f_title` is set."
	found := false
	for i, sec := range plain.Sections {
		if len(got.Sections[i].Sentences) != len(sec.Sentences) {
			t.Fatalf("section %q has %d sentences, want %d", sec.Title, len(got.Sections[i].Sentences), len(sec.Sentences))
		}
		for j, s := range sec.Sentences {
			g := got.Sections[i].Sentences[j]
			want := s.Text
			if s.Kind == reading.Move && strings.Contains(s.Text, " from open to done") {
				want = "Approve: " + lowerFirst(s.Text)
				found = true
				if want != approve {
					t.Errorf("the move without its label reads %q; want it to read %q with it", s.Text, approve)
				}
			}
			if g.Text != want {
				t.Errorf("sentence %d reads\n  %q\nwant\n  %q", s.N, g.Text, want)
			}
			if g.N != s.N || g.Kind != s.Kind || strings.Join(partsOf(g), ",") != strings.Join(partsOf(s), ",") ||
				strings.Join(g.Paths, ",") != strings.Join(s.Paths, ",") {
				t.Errorf("sentence %d: number, kind, parts or paths changed with a label: %+v, want %+v", s.N, g, s)
			}
		}
	}
	if !found {
		t.Errorf("no move sentence reads open to done")
	}
}

// A label is the only change: with labels and without, the text differs in
// exactly the lines above.
func TestReading_LabelsChangeOnlyTheirLines(t *testing.T) {
	plain := strings.Split(read(words(t, nil)).Text(), "\n")
	got := strings.Split(read(words(t, labels)).Text(), "\n")
	if len(got) != len(plain) {
		t.Fatalf("%d lines with labels, want %d", len(got), len(plain))
	}
	var changed []string
	for i := range plain {
		if got[i] != plain[i] {
			changed = append(changed, got[i])
		}
	}
	want := []string{
		"- `f_owner` (Approver): a person",
		"Waiting for a decision (`open`, waiting)",
		"Approve: whoever `f_owner` names moves it from open to done, once `f_title` is set.",
		"Approved (`done`, done)",
	}
	for i, w := range want {
		if i >= len(changed) || !strings.HasSuffix(changed[i], w) {
			t.Fatalf("changed lines = %q, want them to end with %q", changed, want)
		}
	}
	if len(changed) != len(want) {
		t.Errorf("changed lines = %q, want %d", changed, len(want))
	}
}

// Every hint key is left out, with its reason; none is read by a part.
func TestReading_HintKeysAreLeftOut(t *testing.T) {
	for _, k := range []string{
		"summary",
		"fields[].label", "fields[].help",
		"states[].label", "states[].help", "states[].due",
		"transitions[].label", "transitions[].help", "transitions[].primary",
	} {
		if reading.LeftOut[k] == "" {
			t.Errorf("LeftOut has no reason for %s", k)
		}
		if _, ok := reading.KeyParts[k]; ok {
			t.Errorf("KeyParts reads %s; a hint is left out", k)
		}
	}
}

func lowerFirst(s string) string {
	r, n := utf8.DecodeRuneInString(s)
	return string(unicode.ToLower(r)) + s[n:]
}

func partsOf(s reading.Sentence) []string {
	var out []string
	for _, p := range s.Parts {
		out = append(out, string(p))
	}
	return out
}
