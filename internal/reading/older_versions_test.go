// Tests for the reading of start.older_versions (track 1, item 6, step 1:
// a create names the latest version).
//
// A start block with the key says, last under "Who starts it":
//
//	While this is the latest version of {type}, a {type} of version 1 or 2
//	may still be created; one of any other older version may not.   (part start_older)
//	While this is the latest version of {type}, a {type} of any older
//	version may still be created.                     (parts start_older, start_older.all)
//
// citing start.older_versions. A block without the key says neither; the
// rule that then holds (only the latest version is created) is the
// kernel's, the same for every Type, and the closing note already says
// that the latest version decides who may create one.
package reading_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/contracts"
	"github.com/francoisnyc/work-kernel/internal/reading"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

func olderReading(t *testing.T, version, start string) *reading.Reading {
	t.Helper()
	src := "id: form\nversion: " + version + "\nfields:\n  - {id: f_title, type: string}\n" + start
	ty, err := typedef.Parse([]byte(src))
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, src)
	}
	return reading.Read(ty, contracts.Default())
}

func TestReading_OlderVersions(t *testing.T) {
	cases := []struct {
		name, version, start string
		text                 string
		parts                []reading.Part
	}{
		{"one version", "3", "start:\n  older_versions: [2]\n",
			"While this is the latest version of form, a form of version 2 may still be created; one of any other older version may not.",
			[]reading.Part{reading.StartOlder}},
		{"two versions, in the order of their numbers", "4", "start:\n  older_versions: [3, 1]\n",
			"While this is the latest version of form, a form of version 1 or 3 may still be created; one of any other older version may not.",
			[]reading.Part{reading.StartOlder}},
		{"three versions", "5", "start:\n  in: [top]\n  older_versions: [1, 2, 4]\n",
			"While this is the latest version of form, a form of version 1, 2 or 4 may still be created; one of any other older version may not.",
			[]reading.Part{reading.StartOlder}},
		{"all", "3", "start:\n  older_versions: all\n",
			"While this is the latest version of form, a form of any older version may still be created.",
			[]reading.Part{reading.StartOlder, reading.StartOlderAll}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := sentencesOf(olderReading(t, c.version, c.start), "Who starts it")
			if len(got) == 0 {
				t.Fatalf("no section Who starts it")
			}
			last := got[len(got)-1]
			if last.Text != c.text {
				t.Errorf("the last sentence of Who starts it =\n  %s\nwant\n  %s", last.Text, c.text)
			}
			if !slices.Equal(last.Parts, c.parts) {
				t.Errorf("parts %v, want %v", last.Parts, c.parts)
			}
			if !slices.Equal(last.Paths, []string{"start.older_versions"}) {
				t.Errorf("paths %v, want [start.older_versions]", last.Paths)
			}
		})
	}
}

// A block without the key, and a Type without a block, say nothing of
// older versions.
func TestReading_OlderVersions_AbsentReadsAsBefore(t *testing.T) {
	for _, start := range []string{"", "start:\n  in: [top]\n"} {
		r := olderReading(t, "3", start)
		for _, sec := range r.Sections {
			for _, s := range sec.Sentences {
				if strings.Contains(s.Text, "older version") || slices.Contains(s.Parts, reading.StartOlder) ||
					slices.Contains(s.Parts, reading.StartOlderAll) {
					t.Errorf("a Type without older_versions reads: %s", s.Text)
				}
			}
		}
	}
}
