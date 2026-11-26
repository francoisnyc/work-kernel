// Tests for track 1, item 6, step 3a: each state entry is judged by the
// version the Item was on when the entry was made. VersionAt reads that
// version from the Item's migrates.
//
//   - No migrate: every event is of the version the Item is on now.
//   - Before the first migrate: its from; from a migrated event on (that
//     event included), its to, until the next one.
//   - A migrate may skip versions (1 to 3).
package lifecycle_test

import (
	"testing"

	"github.com/francoisnyc/work-kernel/internal/lifecycle"
)

func TestVersionAt(t *testing.T) {
	one := []lifecycle.Migration{{Seq: 4, From: 1, To: 2}}
	two := []lifecycle.Migration{{Seq: 4, From: 1, To: 2}, {Seq: 9, From: 2, To: 3}}
	skip := []lifecycle.Migration{{Seq: 6, From: 1, To: 3}}
	cases := []struct {
		name    string
		ms      []lifecycle.Migration
		current int
		seq     int64
		want    int
	}{
		{"no migrate, the create", nil, 3, 1, 3},
		{"no migrate, a later event", nil, 3, 12, 3},
		{"one migrate, the create", one, 2, 1, 1},
		{"one migrate, just before it", one, 2, 3, 1},
		{"one migrate, the migrated event itself", one, 2, 4, 2},
		{"one migrate, after it", one, 2, 7, 2},
		{"two migrates, before both", two, 3, 2, 1},
		{"two migrates, between them", two, 3, 5, 2},
		{"two migrates, just before the second", two, 3, 8, 2},
		{"two migrates, the second migrated event", two, 3, 9, 3},
		{"two migrates, after both", two, 3, 12, 3},
		{"a migrate that skips a version, before it", skip, 3, 5, 1},
		{"a migrate that skips a version, after it", skip, 3, 7, 3},
	}
	for _, c := range cases {
		if got := lifecycle.VersionAt(c.ms, c.current, c.seq); got != c.want {
			t.Errorf("%s: VersionAt(seq %d) = %d, want %d", c.name, c.seq, got, c.want)
		}
	}
}
