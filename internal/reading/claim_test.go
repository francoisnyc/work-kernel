// Tests for the reading of a claim that binds (track 1, item 5, step 2).
//
// A state with `claim: f` says, first of what holds while an Item is
// there:
//
//	Whoever moves it into {s} claims it: `f` names them, and only they
//	move it on from there, unless a move releases the claim.      (part claim)
//	While it is in {s}, it waits on whoever claimed it.            (parts waits, waits.claimant)
//
// both citing states[i].claim. The second replaces the list of the people
// the state's moves name: a claimed Item waits on its claimant, not on the
// team. A move with `release: true` says ", which releases the claim"
// (part move.release), before what it clears. A move that clears the claim
// field without being a release says only that it clears it. A Type with
// neither key reads as before.
package reading_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/contracts"
	"github.com/francoisnyc/work-kernel/internal/reading"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// claimReading is the reading of a Type with a queue and a review; review
// has these extra keys, and the move back to the queue these.
func claimReading(t *testing.T, review, back string) *reading.Reading {
	t.Helper()
	src := "id: cr\nversion: 1\ninitial_state: queue\nfields:\n" +
		"  - {id: f_claimed_by, type: principal}\n  - {id: f_note, type: string}\n" +
		"states:\n  - {id: queue, category: waiting}\n  - {id: review, category: active" + review + "}\n" +
		"  - {id: approved, category: done}\n  - {id: rejected, category: canceled}\n" +
		"transitions:\n  - {from: queue, to: review, allowed: ['role:finance']}\n" +
		"  - {from: review, to: queue, allowed: ['role:finance', 'role:lead']" + back + "}\n" +
		"  - {from: review, to: approved, allowed: ['role:finance'], except: [creator]}\n" +
		"  - {from: review, to: rejected, allowed: ['role:finance'], clears: [f_claimed_by]}\n"
	ty, err := typedef.Parse([]byte(src))
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, src)
	}
	return reading.Read(ty, contracts.Default())
}

// sentencesOf is a section's sentences, by the section's heading.
func sentencesOf(r *reading.Reading, heading string) []reading.Sentence {
	for _, sec := range r.Sections {
		if sec.Title == heading {
			return sec.Sentences
		}
	}
	return nil
}

func TestReading_Claim(t *testing.T) {
	r := claimReading(t, ", claim: f_claimed_by, frozen: [f_note]", ", release: true, clears: [f_claimed_by]")
	got := sentencesOf(r, "review (active)")
	want := []struct {
		text  string
		parts []reading.Part
		paths []string
	}{
		{"Whoever moves it into review claims it: `f_claimed_by` names them, and only they move it on from there, unless a move releases the claim.",
			[]reading.Part{reading.Claim}, []string{"states[1].claim"}},
		{"While it is in review, it waits on whoever claimed it.",
			[]reading.Part{reading.Waits, reading.WaitsClaimant}, []string{"states[1].claim"}},
		{"While it is in review, nobody changes `f_note`.", []reading.Part{reading.Frozen}, []string{"states[1].frozen"}},
		{"Anyone with the role finance, or anyone with the role lead, moves it from review to queue, which releases the claim; the move clears `f_claimed_by`.",
			[]reading.Part{reading.Move, reading.WhoRole, reading.WhoRole, reading.MoveRelease, reading.MoveClears}, []string{"transitions[1]"}},
		{"Anyone with the role finance, but never anyone who took part in creating it, moves it from review to approved.",
			[]reading.Part{reading.Move, reading.WhoRole, reading.ExceptCreator}, []string{"transitions[2]"}},
		// It clears the claim and is no release: it says so, and no more.
		{"Anyone with the role finance moves it from review to rejected; the move clears `f_claimed_by`.",
			[]reading.Part{reading.Move, reading.WhoRole, reading.MoveClears}, []string{"transitions[3]"}},
	}
	if len(got) != len(want) {
		var texts []string
		for _, s := range got {
			texts = append(texts, s.Text)
		}
		t.Fatalf("review reads %d sentences, want %d:\n  %s", len(got), len(want), strings.Join(texts, "\n  "))
	}
	for i, w := range want {
		s := got[i]
		if s.Text != w.text {
			t.Errorf("sentence %d =\n  %s\nwant\n  %s", i, s.Text, w.text)
		}
		if !slices.Equal(s.Parts, w.parts) {
			t.Errorf("sentence %d: parts %v, want %v (%s)", i, s.Parts, w.parts, w.text)
		}
		if !slices.Equal(s.Paths, w.paths) {
			t.Errorf("sentence %d: paths %v, want %v (%s)", i, s.Paths, w.paths, w.text)
		}
	}
	if s := got[0]; s.Kind != reading.Claim {
		t.Errorf("the claim sentence's kind = %s, want claim", s.Kind)
	}
	// The queue, which nobody claims, still waits on the role its move names.
	queue := sentencesOf(r, "queue (waiting)")
	if len(queue) == 0 || queue[0].Text != "While it is in queue, it waits on anyone with the role finance." {
		t.Errorf("the queue's first sentence = %v, want what it waits on", queue)
	}
}

// Without the two keys nothing of the claim is said: the state waits on
// the people its moves name, the release included.
func TestReading_Claim_AbsentReadsAsBefore(t *testing.T) {
	r := claimReading(t, "", ", clears: [f_claimed_by]")
	for _, sec := range r.Sections {
		for _, s := range sec.Sentences {
			if strings.Contains(s.Text, "claims it") || strings.Contains(s.Text, "releases the claim") || strings.Contains(s.Text, "whoever claimed it") {
				t.Errorf("a Type with no claim reads: %s", s.Text)
			}
			for _, p := range s.Parts {
				if p == reading.Claim || p == reading.WaitsClaimant || p == reading.MoveRelease {
					t.Errorf("a Type with no claim has a sentence with part %s: %s", p, s.Text)
				}
			}
		}
	}
	got := sentencesOf(r, "review (active)")
	if len(got) == 0 || got[0].Text != "While it is in review, it waits on anyone with the role finance, or anyone with the role lead." {
		t.Errorf("review's first sentence = %v, want the people its moves name", got)
	}
}
