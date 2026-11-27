// Tests for track 1, item 6, step 3b: a migrated event always reaches the
// Item's workflow, starting one if none runs, so a run alive under the old
// version reloads under the new one, and a new version that gives the
// Item's state a timer gets a run to arm it.
package lifecycle_test

import (
	"testing"

	"github.com/francoisnyc/work-kernel/internal/lifecycle"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

func TestStartsWorkflow_Migrated(t *testing.T) {
	for name, src := range map[string]string{
		"a Type with no behavior": "id: plain\nversion: 2\nfields:\n  - {id: f_title, type: string}\n",
		"a Type with a timer": `
id: timed
version: 2
initial_state: open
states:
  - {id: open, category: todo, timer: {after: 2h, to: done}}
  - {id: done, category: done}
transitions:
  - {from: open, to: done}
`,
	} {
		ty, err := typedef.Parse([]byte(src))
		if err != nil {
			t.Fatalf("%s: parse: %v", name, err)
		}
		if !lifecycle.StartsWorkflow(ty, lifecycle.Event{Op: lifecycle.OpMigrated}) {
			t.Errorf("%s: a migrated event does not start the workflow; want it to", name)
		}
	}
}
