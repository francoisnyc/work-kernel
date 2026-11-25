package workflow_test

import (
	"context"
	"fmt"
	"os"

	"github.com/francoisnyc/work-kernel/internal/itemservice"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// registerNewestTypes registers, before any test runs, the newest version
// of every Type in types/ that has several: each keeps its older versions
// open (start.older_versions: all). The tests of this package register
// versions one at a time, in whatever order they run; with the newest
// there from the start, which version is latest never depends on that
// order, and a create of an older version is judged the same in every run
// (track 1, item 6, step 1: a create names the latest version).
func registerNewestTypes(ctx context.Context) error {
	for _, name := range []string{
		"bug.v4", "comment.v4", "desk.v4", "incident.v5", "note.v3", "reply.v3", "sprint.v3", "story.v4", "task.v4",
		"ticket.v8", "vacation_request.v4", "sla/sla_goal.v4", "sla/sla_policy.v3",
	} {
		data, err := os.ReadFile("../../types/" + name + ".yaml")
		if err != nil {
			return err
		}
		ty, err := typedef.Parse(data)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		if err := itemservice.New(pool).RegisterType(ctx, ty); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return nil
}
