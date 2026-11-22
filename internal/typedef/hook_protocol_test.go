// Tests for the hook protocol spike: a hook's action names a connector, an
// operation and, optionally, the connector's major version,
// <connector>.<operation>[@<major>]. No major means 1, so every existing
// Type already names major 1 and none changes. A major the catalog does not
// declare is refused when the Type loads, as an unknown action is. A new
// major means a new Type version (design.md).
package typedef_test

import (
	"os"
	"strings"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/typedef"
)

const getPlanHook = "action: crm.get_plan,"

func TestHooks_ActionMajor(t *testing.T) {
	if !strings.Contains(hookedType, getPlanHook) {
		t.Fatalf("test bug: %q is not in the base Type", getPlanHook)
	}
	ty, err := typedef.Parse([]byte(strings.Replace(hookedType, getPlanHook, "action: crm.get_plan@1,", 1)))
	if err != nil {
		t.Fatalf("crm.get_plan@1: %v", err)
	}
	if h, _ := ty.Hook("get_plan"); h.Action != "crm.get_plan@1" {
		t.Errorf("action = %q, want it as written", h.Action)
	}
	for _, bad := range []string{"crm.get_plan@2", "crm.get_plan@0", "crm.get_plan@x", "crm.get_plan@", "crm.get_plan@1@1"} {
		_, err := typedef.Parse([]byte(strings.Replace(hookedType, getPlanHook, "action: '"+bad+"',", 1)))
		if err == nil || !strings.Contains(err.Error(), bad) {
			t.Errorf("action %s: Parse = %v, want an error naming it", bad, err)
		}
	}
}

// The vacation request's files are unchanged and still load: their
// hr.manager_of is hr@1, now a remote connector.
func TestHooks_VacationRequestIsUnchanged(t *testing.T) {
	for _, f := range []string{"vacation_request.v1.yaml", "vacation_request.v2.yaml", "vacation_request.v3.yaml"} {
		data, err := os.ReadFile("../../types/" + f)
		if err != nil {
			t.Fatal(err)
		}
		ty, err := typedef.Parse(data)
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		if h, ok := ty.Hook("hr_check"); !ok || h.Action != "hr.manager_of" {
			t.Errorf("%s: hr_check = %+v, %v; want action hr.manager_of", f, h, ok)
		}
	}
}
