package reading_test

import (
	"strings"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/contracts"
	"github.com/francoisnyc/work-kernel/internal/reading"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

func hasTerm(r *reading.Reading, name string) bool {
	for _, k := range r.Key {
		if k.Name == name {
			return true
		}
	}
	return false
}

// The key's "a hold" is the noun: a sentence using hold as a verb ("hold
// the same", for a field unique among siblings) puts no "a hold" in the
// key of a Type without holds; a sentence naming a hold still does.
func TestKey_AHoldIsTheNounOnly(t *testing.T) {
	vote := reading.Read(fixtures(t)["w_vote@1"], contracts.Default())
	if !strings.Contains(vote.Text(), " hold the same ") {
		t.Fatalf("test bug: w_vote@1's reading no longer uses hold as a verb:\n%s", vote.Text())
	}
	if hasTerm(vote, "a hold") {
		t.Errorf("w_vote@1, which has no hold, defines a hold in its key:\n%s", vote.Text())
	}

	types, err := typedef.ParseDir("../../types")
	if err != nil {
		t.Fatal(err)
	}
	for _, ty := range types {
		if ty.ID != "vacation_request" || ty.Version != 3 {
			continue
		}
		vr := reading.Read(ty, contracts.Default())
		if !strings.Contains(vr.Text(), "opens a hold") {
			t.Fatalf("test bug: vacation_request@3's reading no longer opens a hold")
		}
		if !hasTerm(vr, "a hold") {
			t.Errorf("vacation_request@3, whose approval opens a hold, has no \"a hold\" in its key")
		}
		return
	}
	t.Fatal("no vacation_request@3 in types/")
}
