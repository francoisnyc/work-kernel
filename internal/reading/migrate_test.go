// Tests for the reading of migrate.allowed (track 1, item 6, step 2).
//
// A Type with the key has a section "Who migrates Items to it", after "Who
// starts it", with one sentence:
//
//	While this is the latest version of {type}, {who} may migrate an open
//	{type} of an older version to it, once it fits.        (part migrate_who)
//
// citing migrate. A Type without the key has no such section: nobody may,
// which is the kernel's rule for every Type and no sentence of this one.
package reading_test

import (
	"slices"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/contracts"
	"github.com/francoisnyc/work-kernel/internal/reading"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

func migrateReading(t *testing.T, rest string) *reading.Reading {
	t.Helper()
	src := "id: bill\nversion: 2\nfields:\n  - {id: f_title, type: string}\n" + rest
	ty, err := typedef.Parse([]byte(src))
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, src)
	}
	return reading.Read(ty, contracts.Default())
}

func TestReading_Migrate(t *testing.T) {
	cases := []struct {
		name, block, text string
		parts             []reading.Part
	}{
		{"a role", "migrate:\n  allowed: ['role:bill_admin']\n",
			"While this is the latest version of bill, anyone with the role bill_admin may migrate an open bill of an older version to it, once it fits.",
			[]reading.Part{reading.MigrateWho, reading.WhoRole}},
		{"a role and a named user", "migrate:\n  allowed: ['role:bill_admin', 'principal:user:root']\n",
			"While this is the latest version of bill, anyone with the role bill_admin, or `user:root`, may migrate an open bill of an older version to it, once it fits.",
			[]reading.Part{reading.MigrateWho, reading.WhoRole, reading.WhoPrincipal}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := sentencesOf(migrateReading(t, c.block), "Who migrates Items to it")
			if len(got) != 1 {
				t.Fatalf("the section Who migrates Items to it has %d sentences, want 1", len(got))
			}
			s := got[0]
			if s.Text != c.text {
				t.Errorf("the sentence =\n  %s\nwant\n  %s", s.Text, c.text)
			}
			if !slices.Equal(s.Parts, c.parts) {
				t.Errorf("parts %v, want %v", s.Parts, c.parts)
			}
			if !slices.Equal(s.Paths, []string{"migrate"}) {
				t.Errorf("paths %v, want [migrate]", s.Paths)
			}
		})
	}
}

// The section comes right after Who starts it (or after In short, for a
// Type with no start block).
func TestReading_Migrate_WhereTheSectionSits(t *testing.T) {
	titles := func(r *reading.Reading) []string {
		var out []string
		for _, sec := range r.Sections {
			out = append(out, sec.Title)
		}
		return out
	}
	with := titles(migrateReading(t, "start:\n  in: [top]\nmigrate:\n  allowed: ['role:bill_admin']\n"))
	if i := slices.Index(with, "Who starts it"); i < 0 || i+1 >= len(with) || with[i+1] != "Who migrates Items to it" {
		t.Errorf("sections = %v, want Who migrates Items to it right after Who starts it", with)
	}
	alone := titles(migrateReading(t, "migrate:\n  allowed: ['role:bill_admin']\n"))
	if i := slices.Index(alone, "In short"); i < 0 || i+1 >= len(alone) || alone[i+1] != "Who migrates Items to it" {
		t.Errorf("sections = %v, want Who migrates Items to it right after In short", alone)
	}
}

func TestReading_Migrate_AbsentReadsAsBefore(t *testing.T) {
	r := migrateReading(t, "")
	for _, sec := range r.Sections {
		if sec.Title == "Who migrates Items to it" {
			t.Errorf("a Type without the key has the section %q", sec.Title)
		}
		for _, s := range sec.Sentences {
			if slices.Contains(s.Parts, reading.MigrateWho) {
				t.Errorf("a Type without the key reads: %s", s.Text)
			}
		}
	}
}
