// A recorded history for Jira part 2, step 3a: a run with an acting timer
// at an instant. A meter is created by its job's reaction; its run starts
// cold, shows its due instant on the job (a set reaction), reads its
// instant, waits, marks itself late at the instant (ActAt) and goes on;
// stopped, it clears the job's field and completes.
// TestReplay_ActingAtHistories replays it on today's workflow code.
package workflow_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	wf "github.com/francoisnyc/work-kernel/internal/workflow"
)

// Recorded by TestRecordActingAtHistory (RECORD_HISTORIES=1) from the code
// that first had acting timers at an instant. Do not re-record it after
// changing the workflow: then it would prove nothing.
var actingAtHistories = []string{
	"meter_late_at_an_instant.json", // LoadItem, show, LoadActingInstants, a timer, ActAt, events, clear, done
}

// TestRecordActingAtHistory records the history above. It runs only with
// RECORD_HISTORIES=1.
func TestRecordActingAtHistory(t *testing.T) {
	if os.Getenv("RECORD_HISTORIES") != "1" {
		t.Skip("records replay testdata; set RECORD_HISTORIES=1")
	}
	s := newJobStack(t)
	job, main, _ := s.started(2)
	eventually(t, 20*time.Second, "the meter marked late", func() bool { return s.field(main, "f_late_at") != nil })
	s.move(job, "working")
	s.move(job, "done")
	eventually(t, 20*time.Second, "the meter stopped", s.stateIs(main, "stopped"))
	eventually(t, 15*time.Second, "the job's f_due cleared", func() bool { return s.field(job, "f_due") == nil })
	s.waitDelivered(main)
	s.waitCompleted(main)

	// The meter's own late events may start one more run; the one to keep
	// is the run that made the timer act.
	list, err := tc.ListWorkflow(serverCtx(t), &workflowservice.ListWorkflowExecutionsRequest{
		Query: fmt.Sprintf("WorkflowId = '%s'", main),
	})
	if err != nil {
		t.Fatalf("list the meter's runs: %v", err)
	}
	for _, e := range list.GetExecutions() {
		run := e.GetExecution().GetRunId()
		for _, ev := range runHistory(t, main, run).Events {
			if a := ev.GetActivityTaskScheduledEventAttributes(); a.GetActivityType().GetName() == wf.ActivityActAt {
				saveRunHistory(t, main, run, actingAtHistories[0])
				return
			}
		}
	}
	t.Fatalf("none of the meter's %d runs scheduled %s", len(list.GetExecutions()), wf.ActivityActAt)
}

// The recorded history replays on today's workflow code without a
// non-determinism error.
func TestReplay_ActingAtHistories(t *testing.T) {
	for _, name := range actingAtHistories {
		t.Run(name, func(t *testing.T) {
			f, err := os.Open(filepath.Join(historiesDir, name))
			if err != nil {
				t.Fatalf("open recorded history: %v", err)
			}
			defer f.Close()
			history, err := client.HistoryFromJSON(f, client.HistoryJSONOptions{})
			if err != nil {
				t.Fatalf("read history: %v", err)
			}
			replayer := worker.NewWorkflowReplayer()
			replayer.RegisterWorkflowWithOptions(wf.ItemWorkflow, workflow.RegisterOptions{Name: wf.Name})
			if err := replayer.ReplayWorkflowHistory(nil, history); err != nil {
				t.Fatalf("replay %s: %v", name, err)
			}
		})
	}
}
