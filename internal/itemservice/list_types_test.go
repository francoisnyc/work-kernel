// Tests for Step 0, part 1: ListTypes returns every registered Type version,
// for GetType/ListTypes in the API. Types are global for now (not per
// namespace) and readable by anyone in a namespace, so this read is exempt
// from policy.Project (see project_paths_test.go).
package itemservice_test

import (
	"strconv"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/itemservice"
)

func TestListTypes_ReturnsEveryVersionInOrder(t *testing.T) {
	k := itemservice.New(pool)
	registerFromFile(t, k, "ticket.v3.yaml")
	registerMessageType(t, k)

	types, err := k.ListTypes(testCtx(t))
	if err != nil {
		t.Fatalf("ListTypes: %v", err)
	}
	var got []string
	seen := map[string]bool{}
	for i, ty := range types {
		key := ty.ID + "@" + strconv.Itoa(ty.Version)
		if seen[key] {
			t.Errorf("%s listed twice", key)
		}
		seen[key] = true
		got = append(got, key)
		if i > 0 {
			prev := types[i-1]
			if prev.ID > ty.ID || (prev.ID == ty.ID && prev.Version >= ty.Version) {
				t.Errorf("not ordered by ID, then version: %s before %s", got[i-1], key)
			}
		}
	}
	for _, want := range []string{"ticket@1", "ticket@3", "message@1"} {
		if !seen[want] {
			t.Errorf("ListTypes = %v, missing %s", got, want)
		}
	}
	// A listed Type is the parsed definition, not just its name.
	for _, ty := range types {
		if ty.ID == "ticket" && ty.Version == 3 {
			if _, ok := ty.Transition("in_progress", "approved"); !ok || ty.InitialState != "triage" {
				t.Errorf("ticket v3 listed without its definition: %+v", ty)
			}
		}
	}
}
