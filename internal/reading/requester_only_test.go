// Tests for the reading of "only the requester" for drafts (track 1, item
// 4): a state's requester_only reads, among what holds while the Item is
// there and after what it freezes, "While it is in {s}, only the requester
// changes {fields}." (requester_only), citing states[i].requester_only.
package reading_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/contracts"
	"github.com/francoisnyc/work-kernel/internal/reading"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// stateSentences: every sentence of the reading of a Type whose draft state
// has these extra keys (YAML flow, inside its braces) that mentions draft
// "while it is in".
func stateSentences(t *testing.T, draft string) []reading.Sentence {
	t.Helper()
	src := "id: ro\nversion: 1\ninitial_state: draft\nfields:\n" +
		"  - {id: f_amount, type: int}\n  - {id: f_vendor, type: string}\n  - {id: f_note, type: string}\n" +
		"states:\n  - {id: draft, category: todo" + draft + "}\n  - {id: sent, category: done}\n" +
		"transitions:\n  - {from: draft, to: sent, allowed: [creator]}\n"
	ty, err := typedef.Parse([]byte(src))
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, src)
	}
	var out []reading.Sentence
	for _, sec := range reading.Read(ty, contracts.Default()).Sections {
		for _, s := range sec.Sentences {
			if strings.HasPrefix(s.Text, "While it is in draft") {
				out = append(out, s)
			}
		}
	}
	return out
}

func TestReading_RequesterOnly(t *testing.T) {
	cases := []struct {
		name, draft string
		want        []string
		kinds       []reading.Part
		paths       []string
	}{
		{"one field", ", requester_only: [f_amount]",
			[]string{"While it is in draft, only the requester changes `f_amount`."},
			[]reading.Part{reading.RequesterOnly}, []string{"states[0].requester_only"}},
		{"two fields", ", requester_only: [f_amount, f_vendor]",
			[]string{"While it is in draft, only the requester changes `f_amount` or `f_vendor`."},
			[]reading.Part{reading.RequesterOnly}, []string{"states[0].requester_only"}},
		{"after what the state freezes", ", frozen: [f_note], requester_only: [f_amount, f_vendor]",
			[]string{"While it is in draft, nobody changes `f_note`.",
				"While it is in draft, only the requester changes `f_amount` or `f_vendor`."},
			[]reading.Part{reading.Frozen, reading.RequesterOnly},
			[]string{"states[0].frozen", "states[0].requester_only"}},
		{"none", "", nil, nil, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := stateSentences(t, c.draft)
			var texts, paths []string
			var kinds []reading.Part
			for _, s := range got {
				texts, kinds = append(texts, s.Text), append(kinds, s.Kind)
				paths = append(paths, s.Paths...)
			}
			if !slices.Equal(texts, c.want) {
				t.Errorf("sentences =\n  %s\nwant\n  %s", strings.Join(texts, "\n  "), strings.Join(c.want, "\n  "))
			}
			if !slices.Equal(kinds, c.kinds) {
				t.Errorf("kinds = %v, want %v", kinds, c.kinds)
			}
			if !slices.Equal(paths, c.paths) {
				t.Errorf("paths = %v, want %v", paths, c.paths)
			}
		})
	}
}
