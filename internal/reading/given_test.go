// Tests for the reading of "required means given by the mover" (track 1,
// item 3): a transition's given reads, in its move sentence, "once the
// mover gives {fields} in the move itself" (part move.given), after the
// required fields that are only to be set ("once {f} is set and ..."),
// before the guard. A move without given reads as today.
package reading_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/contracts"
	"github.com/francoisnyc/work-kernel/internal/reading"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// moveGiven is the new part, by its name.
const moveGiven = reading.Part("move.given")

// moveSentence: the sentence of the reading of a Type whose review ->
// rejected move has these keys (YAML flow, inside its braces).
func moveSentence(t *testing.T, move string) reading.Sentence {
	t.Helper()
	src := "id: gr\nversion: 1\ninitial_state: review\nfields:\n" +
		"  - {id: f_amount, type: int}\n  - {id: f_reason, type: string}\n  - {id: f_note, type: string}\n" +
		"states:\n  - {id: review, category: active}\n  - {id: rejected, category: canceled}\n" +
		"transitions:\n  - {from: review, to: rejected, allowed: ['role:finance'], except: [creator]" + move + "}\n"
	ty, err := typedef.Parse([]byte(src))
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, src)
	}
	for _, sec := range reading.Read(ty, contracts.Default()).Sections {
		for _, s := range sec.Sentences {
			if strings.Contains(s.Text, " it from review to rejected") {
				return s
			}
		}
	}
	t.Fatalf("no sentence reads the move")
	return reading.Sentence{}
}

func TestReading_Given(t *testing.T) {
	const who = "Anyone with the role finance, but never anyone who took part in creating it, moves it from review to rejected"
	cases := []struct {
		name, move, want string
		parts            []reading.Part
	}{
		{"one field, given", ", required: [f_reason], given: [f_reason]",
			who + ", once the mover gives `f_reason` in the move itself.",
			[]reading.Part{reading.Move, reading.WhoRole, reading.ExceptCreator, moveGiven}},
		{"two fields, given", ", required: [f_note, f_reason], given: [f_note, f_reason]",
			who + ", once the mover gives `f_note` and `f_reason` in the move itself.",
			[]reading.Part{reading.Move, reading.WhoRole, reading.ExceptCreator, moveGiven}},
		{"one set, one given, and a guard", ", required: [f_amount, f_reason], given: [f_reason], guard: 'f_amount > 0'",
			who + ", once `f_amount` is set and the mover gives `f_reason` in the move itself, only if `f_amount` is more than 0.",
			[]reading.Part{reading.Move, reading.WhoRole, reading.ExceptCreator, reading.MoveRequired, moveGiven, reading.MoveGuard}},
		{"none given: as today", ", required: [f_reason]",
			who + ", once `f_reason` is set.",
			[]reading.Part{reading.Move, reading.WhoRole, reading.ExceptCreator, reading.MoveRequired}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := moveSentence(t, c.move)
			if s.Text != c.want {
				t.Errorf("sentence =\n  %s\nwant\n  %s", s.Text, c.want)
			}
			if !slices.Equal(s.Parts, c.parts) {
				t.Errorf("parts = %v, want %v", s.Parts, c.parts)
			}
			if !slices.Equal(s.Paths, []string{"transitions[0]"}) {
				t.Errorf("paths = %v, want [transitions[0]]", s.Paths)
			}
		})
	}
}

// The part is in the closed list, and the key reads it.
func TestReading_GivenIsAPart(t *testing.T) {
	if !slices.Contains(reading.Parts(), moveGiven) {
		t.Errorf("reading.Parts() has no %s", moveGiven)
	}
	if !slices.Contains(reading.KeyParts["transitions[].given"], moveGiven) {
		t.Errorf("KeyParts[transitions[].given] = %v, want it to hold %s", reading.KeyParts["transitions[].given"], moveGiven)
	}
}
