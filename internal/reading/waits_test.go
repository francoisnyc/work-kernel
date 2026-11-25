// Tests for the reading of who a state waits on ("waiting on me", track 1,
// item 5, step 1): the first thing said of what holds while an Item is in
// a state, before what the state freezes:
//
//	While it is in {s}, it waits on {who}.
//
// (part waits), citing every move out of the state that
// names someone. {who} is the people those moves name, in the Type's
// order, each once, in the words a move's sentence uses: "whoever `f`
// names", "anyone with the role r", a person's ID. A state whose moves
// name nobody (open to anyone, the requester's own, nobody's, the timer's)
// has no such sentence, and neither has a state in category done or
// canceled: the rule is policy.WaitingMoves.
package reading_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/contracts"
	"github.com/francoisnyc/work-kernel/internal/reading"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// waitsSentences: the "it waits on" sentences of the reading of a Type
// with one state, here (in the given category, with the given keys), and
// these moves out of it (YAML flow, one a line, each with its own target).
func waitsSentences(t *testing.T, category, stateKeys string, moves ...string) []reading.Sentence {
	t.Helper()
	src := "id: wt\nversion: 1\ninitial_state: here\nfields:\n" +
		"  - {id: f_owner, type: principal}\n  - {id: f_second, type: principal}\n  - {id: f_note, type: string}\n" +
		"states:\n  - {id: here, category: " + category + stateKeys + "}\n"
	for i := range moves {
		src += "  - {id: t" + string(rune('0'+i)) + ", category: active}\n"
	}
	src += "transitions:\n"
	for i, m := range moves {
		src += "  - {from: here, to: t" + string(rune('0'+i)) + m + "}\n"
	}
	ty, err := typedef.Parse([]byte(src))
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, src)
	}
	var out []reading.Sentence
	for _, sec := range reading.Read(ty, contracts.Default()).Sections {
		for _, s := range sec.Sentences {
			if strings.Contains(s.Text, "it waits on") {
				out = append(out, s)
			}
		}
	}
	return out
}

func TestReading_Waits(t *testing.T) {
	cases := []struct {
		name, category string
		moves          []string
		want           string
		paths          []string
	}{
		{"a field", "waiting", []string{", allowed: ['field:f_owner']"},
			"While it is in here, it waits on whoever `f_owner` names.", []string{"transitions[0]"}},
		{"a role", "active", []string{", allowed: ['role:finance']"},
			"While it is in here, it waits on anyone with the role finance.", []string{"transitions[0]"}},
		{"a person", "todo", []string{", allowed: ['principal:user:bob']"},
			"While it is in here, it waits on `user:bob`.", []string{"transitions[0]"}},
		{"a field or a role, in one move", "waiting", []string{", allowed: ['field:f_owner', 'role:finance']"},
			"While it is in here, it waits on whoever `f_owner` names, or anyone with the role finance.",
			[]string{"transitions[0]"}},
		{"several moves: each person once, in the Type's order", "waiting",
			[]string{", allowed: ['role:finance'], except: [creator]", ", allowed: ['field:f_owner', 'role:finance']",
				", allowed: ['field:f_second']"},
			"While it is in here, it waits on anyone with the role finance, or whoever `f_owner` names, or whoever `f_second` names.",
			[]string{"transitions[0]", "transitions[1]", "transitions[2]"}},
		{"only the moves that name someone are cited", "waiting",
			[]string{"", ", allowed: [creator]", ", allowed: ['role:finance']"},
			"While it is in here, it waits on anyone with the role finance.", []string{"transitions[2]"}},
		{"the requester beside a role: the role", "waiting", []string{", allowed: [creator, 'role:finance']"},
			"While it is in here, it waits on anyone with the role finance.", []string{"transitions[0]"}},

		{"open to anyone: no sentence", "active", []string{""}, "", nil},
		{"the requester's own move: no sentence", "todo", []string{", allowed: [creator]"}, "", nil},
		{"a done state: no sentence", "done", []string{", allowed: ['role:finance']"}, "", nil},
		{"a canceled state: no sentence", "canceled", []string{", allowed: ['field:f_owner']"}, "", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := waitsSentences(t, c.category, "", c.moves...)
			if c.want == "" {
				if len(got) != 0 {
					t.Errorf("sentences = %q, want none", got[0].Text)
				}
				return
			}
			if len(got) != 1 {
				t.Fatalf("%d sentences say what it waits on, want one: %q", len(got), c.want)
			}
			s := got[0]
			if s.Text != c.want {
				t.Errorf("sentence =\n  %s\nwant\n  %s", s.Text, c.want)
			}
			if s.Kind != reading.Waits || !slices.Equal(s.Parts, []reading.Part{reading.Waits}) {
				t.Errorf("kind %s, parts %v; want waits, [waits]", s.Kind, s.Parts)
			}
			if !slices.Equal(s.Paths, c.paths) {
				t.Errorf("paths = %v, want %v", s.Paths, c.paths)
			}
		})
	}
}

// It is the first thing said of what holds while the Item is there: before
// what the state freezes, after what entering it does.
func TestReading_Waits_ComesBeforeWhatTheStateFreezes(t *testing.T) {
	ty, err := typedef.Parse([]byte("id: wt\nversion: 1\ninitial_state: here\nfields:\n" +
		"  - {id: f_owner, type: principal}\n  - {id: f_note, type: string}\n" +
		"states:\n  - {id: here, category: waiting, frozen: [f_note], requester_only: [f_owner]}\n  - {id: there, category: done}\n" +
		"transitions:\n  - {from: here, to: there, allowed: ['role:finance']}\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var texts []string
	for _, sec := range reading.Read(ty, contracts.Default()).Sections {
		for _, s := range sec.Sentences {
			if strings.HasPrefix(s.Text, "While it is in here") {
				texts = append(texts, s.Text)
			}
		}
	}
	want := []string{
		"While it is in here, it waits on anyone with the role finance.",
		"While it is in here, nobody changes `f_note`.",
		"While it is in here, only the requester changes `f_owner`.",
	}
	if !slices.Equal(texts, want) {
		t.Errorf("what holds while it is in here =\n  %s\nwant\n  %s", strings.Join(texts, "\n  "), strings.Join(want, "\n  "))
	}
}
