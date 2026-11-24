package reading_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/contracts"
	"github.com/francoisnyc/work-kernel/internal/reading"
)

// Starting work, step 2b: who may move or narrow an Item is judged by the
// latest version's placement rule, for every version, as who may create
// one is. A reading of one version cannot say what a later version will
// decide, so its closing note says that the latest version decides both.
func TestNotCovered_TheLatestVersionDecidesWhoPlaces(t *testing.T) {
	for _, name := range []string{"w_leave@1", "w_portal@1"} {
		ty := fixtures(t)[name]
		r := reading.Read(ty, contracts.Default())
		article := "a "
		want := "Who may create " + article + ty.ID + ", and where, and who may move or narrow one, once a later version exists: " +
			"the latest version of " + ty.ID + " decides, for every version."
		if !slices.Contains(r.NotCovered, want) {
			t.Errorf("%s: Not covered = %q\nwant a line %q", name, r.NotCovered, want)
		}
		// One line says it, not two.
		var n int
		for _, line := range r.NotCovered {
			if strings.Contains(line, "the latest version of") {
				n++
			}
		}
		if n != 1 {
			t.Errorf("%s: %d lines speak of the latest version, want 1: %q", name, n, r.NotCovered)
		}
	}
}
