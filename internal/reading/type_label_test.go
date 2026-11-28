// The reading's first sentence names the Type by its own label, when it
// has one (the Inbox, the look, item 4): "An Access request
// (`access_request`) starts in draft.", as a state reads "Draft (`draft`,
// to do)". The sentence also reads the label's path. Without a label it
// reads as before: "A w_leave starts in draft."
package reading_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/contracts"
	"github.com/francoisnyc/work-kernel/internal/reading"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

func firstSentence(t *testing.T, src string) reading.Sentence {
	t.Helper()
	ty, err := typedef.Parse([]byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	r := reading.Read(ty, contracts.Default())
	for _, sec := range r.Sections {
		if sec.Title == "In short" && len(sec.Sentences) > 0 {
			return sec.Sentences[0]
		}
	}
	t.Fatalf("no In short section in\n%s", r.Text())
	return reading.Sentence{}
}

func TestReading_TheTypesLabelStartsIt(t *testing.T) {
	const lifecycle = `
id: access_request
version: 1
label: Access request
initial_state: draft
states:
  - {id: draft, category: todo}
  - {id: done, category: done}
transitions:
  - {from: draft, to: done}
`
	s := firstSentence(t, lifecycle)
	if want := "An Access request (`access_request`) starts in draft."; s.Text != want {
		t.Errorf("first sentence = %q, want %q", s.Text, want)
	}
	if !slices.Contains(s.Paths, "label") || !slices.Contains(s.Paths, "initial_state") {
		t.Errorf("first sentence reads %v, want label and initial_state", s.Paths)
	}

	// A consonant: "A", from the label, not from the ID.
	s = firstSentence(t, strings.Replace(lifecycle, "label: Access request", "label: Request for access", 1))
	if want := "A Request for access (`access_request`) starts in draft."; s.Text != want {
		t.Errorf("first sentence = %q, want %q", s.Text, want)
	}

	// No lifecycle: the same name.
	s = firstSentence(t, `
id: portal
version: 1
label: Customer portal
`)
	if want := "A Customer portal (`portal`) has no lifecycle: it stays in `default`."; s.Text != want {
		t.Errorf("first sentence = %q, want %q", s.Text, want)
	}

	// No label: as before.
	s = firstSentence(t, strings.Replace(lifecycle, "label: Access request\n", "", 1))
	if want := "An access_request starts in draft."; s.Text != want {
		t.Errorf("first sentence = %q, want %q", s.Text, want)
	}
	if slices.Contains(s.Paths, "label") {
		t.Errorf("first sentence without a label reads %v", s.Paths)
	}
}
