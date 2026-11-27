package reading_test

import (
	"os"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/contracts"
	"github.com/francoisnyc/work-kernel/internal/reading"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// The approved reading of approval@2 (the Inbox for people, PR 1b), the
// first Type in types/ with labels: readings/approval@2.txt, compared
// exactly, as TestReadings_TheApprovedOnes compares the others. Its
// headings, field list and move sentences carry the labels.
func TestReadings_TheApprovedOneWithLabels(t *testing.T) {
	want, err := os.ReadFile("../../readings/approval@2.txt")
	if err != nil {
		t.Fatal(err)
	}
	types, err := typedef.ParseDir("../../types")
	if err != nil {
		t.Fatal(err)
	}
	for _, ty := range types {
		if ty.ID == "approval" && ty.Version == 2 {
			got := reading.Read(ty, contracts.Default()).Text()
			if got != string(want) {
				t.Errorf("the reading of approval@2 differs from readings/approval@2.txt (first difference at line %d).\nThe reading now:\n%s",
					firstDiffLine(got, string(want)), got)
			}
			return
		}
	}
	t.Fatal("no Type approval@2 in types/")
}
