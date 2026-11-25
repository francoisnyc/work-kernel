package reading_test

import (
	"fmt"
	"slices"
	"sort"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/contracts"
	"github.com/francoisnyc/work-kernel/internal/reading"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// elements are the parts of a Type a reading must say something about,
// by path: each must be cited by at least one sentence.
func elements(ty *typedef.Type) []string {
	var out []string
	add := func(format string, args ...any) { out = append(out, fmt.Sprintf(format, args...)) }
	add("initial_state")
	for i, f := range ty.Fields {
		if f.WritableBy != "" || len(f.WritableByRoles) > 0 || len(f.UniqueAmongSiblings) > 0 {
			add("fields[%d]", i)
		}
	}
	for i, s := range ty.States {
		if !slices.ContainsFunc(ty.Transitions, func(tr typedef.Transition) bool { return tr.From == s.ID }) {
			add("states[%d]", i)
		}
		if len(s.Frozen) > 0 {
			add("states[%d].frozen", i)
		}
		if len(s.RequesterOnly) > 0 {
			add("states[%d].requester_only", i)
		}
		if s.Timer != nil {
			add("states[%d].timer", i)
		}
		if s.At != nil {
			add("states[%d].at", i)
		}
		for j := range s.Acting {
			add("states[%d].acting[%d]", i, j)
		}
	}
	for i := range ty.Transitions {
		add("transitions[%d]", i)
	}
	for i := range ty.Hooks {
		add("hooks[%d]", i)
	}
	for i, l := range ty.Links {
		add("links[%d]", i)
		if l.Resolve != nil {
			add("links[%d].resolve", i)
		}
	}
	for i := range ty.Reactions {
		add("reactions[%d]", i)
	}
	for i := range ty.Invariants {
		add("invariants[%d]", i)
	}
	for i := range ty.Incoming {
		add("incoming[%d]", i)
	}
	for i := range ty.Rollups {
		add("rollups[%d]", i)
	}
	for k := range ty.AcceptsReactions {
		add("accepts_reactions.%s", k)
	}
	if len(ty.ReadableByReactions) > 0 {
		add("readable_by_reactions")
	}
	for k := range ty.Offers {
		add("offers.%s", k)
	}
	add("customer_fields")
	if len(ty.Own) > 0 {
		add("own")
	}
	if len(ty.Shared) > 0 {
		add("shared")
	}
	add("placement_writable_by")
	if ty.Start != nil {
		add("start")
	}
	sort.Strings(out)
	return out
}

// valid are the paths a sentence may cite: its elements, and any state or
// field of the Type.
func valid(ty *typedef.Type) map[string]bool {
	ok := map[string]bool{}
	for _, e := range elements(ty) {
		ok[e] = true
	}
	for i := range ty.States {
		ok[fmt.Sprintf("states[%d]", i)] = true
	}
	for i := range ty.Fields {
		ok[fmt.Sprintf("fields[%d]", i)] = true
	}
	return ok
}

// readingTypes are the Types of types/ and the witness fixtures.
func readingTypes(t *testing.T) []*typedef.Type {
	t.Helper()
	repo, err := typedef.ParseDir("../../types")
	if err != nil {
		t.Fatal(err)
	}
	fx, err := typedef.ParseDir("testdata/types")
	if err != nil {
		t.Fatal(err)
	}
	return append(repo, fx...)
}

// Nothing a Type says goes unread: every element of every Type in types/
// (and of every fixture) is cited by a sentence, and every sentence cites
// what the Type has.
func TestEveryElementIsCited(t *testing.T) {
	for _, ty := range readingTypes(t) {
		name := fmt.Sprintf("%s@%d", ty.ID, ty.Version)
		t.Run(name, func(t *testing.T) {
			r := reading.Read(ty, contracts.Default())
			cited := map[string]bool{}
			ok := valid(ty)
			for _, sec := range r.Sections {
				for _, s := range sec.Sentences {
					if len(s.Paths) == 0 {
						t.Errorf("sentence %d cites nothing: %s", s.N, s.Text)
					}
					for _, p := range s.Paths {
						if !ok[p] {
							t.Errorf("sentence %d cites %s, which %s does not have: %s", s.N, p, name, s.Text)
						}
						cited[p] = true
					}
				}
			}
			for _, e := range elements(ty) {
				if !cited[e] {
					t.Errorf("no sentence cites %s", e)
				}
			}
		})
	}
}

// Every reading is well formed: its Type's name, sentences numbered from
// 1 without a gap, each sentence's kind its first part, every part on the
// closed list, no empty section, the same reading every time.
func TestReadingsAreWellFormed(t *testing.T) {
	parts := reading.Parts()
	for _, ty := range readingTypes(t) {
		name := fmt.Sprintf("%s@%d", ty.ID, ty.Version)
		t.Run(name, func(t *testing.T) {
			r := reading.Read(ty, contracts.Default())
			if r.Type != name {
				t.Errorf("Type = %q, want %q", r.Type, name)
			}
			if len(r.Sections) == 0 {
				t.Fatal("no section")
			}
			n := 0
			for _, sec := range r.Sections {
				if sec.Title == "" || len(sec.Sentences) == 0 {
					t.Errorf("section %q has %d sentences", sec.Title, len(sec.Sentences))
				}
				for _, s := range sec.Sentences {
					n++
					if s.N != n {
						t.Errorf("sentence %q is numbered %d, want %d", s.Text, s.N, n)
					}
					if len(s.Parts) == 0 || s.Parts[0] != s.Kind {
						t.Errorf("sentence %d: parts %v do not start with its kind %s", s.N, s.Parts, s.Kind)
					}
					for _, p := range s.Parts {
						if !slices.Contains(parts, p) {
							t.Errorf("sentence %d carries %q, which is no part", s.N, p)
						}
					}
				}
			}
			if len(r.NotCovered) == 0 {
				t.Error("no note of what a reading does not cover")
			}
			if again := reading.Read(ty, contracts.Default()).Text(); again != r.Text() {
				t.Error("two readings of the same Type differ")
			}
		})
	}
}
