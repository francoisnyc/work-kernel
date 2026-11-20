// Recorded histories for Jira part 2, step 3b: an incident's cycle and a
// goal's breach, on the repository's own Types. Step 3b changed no
// workflow code (nobody to tell is answered inside the RunAction activity),
// so these are baselines: the first real host of the step 3a machinery
// (create, children moves, set reactions, an acting timer at an instant
// with its hook, a scaled state timer), held against later changes.
// TestReplay_IncidentHistories replays them on today's workflow code.
package workflow_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	wf "github.com/francoisnyc/work-kernel/internal/workflow"
)

// Recorded by TestRecordIncidentHistories (RECORD_HISTORIES=1) from the
// code of step 3b. Do not re-record them after changing the workflow: then
// they would prove nothing.
var incidentHistories = []string{
	// The incident's longest run: both goals started from its creation,
	// then, as it moved, the pause, the requester's comment bringing it
	// back, the resume, the stop, and the timer that closes it.
	"incident_cycle.json",
	// A resolution goal's run: its due instant shown on the incident,
	// LoadActingInstants, a timer, ActAt at the instant, the breach hook
	// (an email to the assignee), then stopped by the incident.
	"sla_goal_breach_and_mail.json",
}

// runsOf lists an Item's workflow runs.
func runsOf(t *testing.T, id uuid.UUID) []string {
	t.Helper()
	list, err := tc.ListWorkflow(serverCtx(t), &workflowservice.ListWorkflowExecutionsRequest{
		Query: fmt.Sprintf("WorkflowId = '%s'", id),
	})
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	var out []string
	for _, e := range list.GetExecutions() {
		out = append(out, e.GetExecution().GetRunId())
	}
	return out
}

// TestRecordIncidentHistories records the histories above. It runs only
// with RECORD_HISTORIES=1.
func TestRecordIncidentHistories(t *testing.T) {
	if os.Getenv("RECORD_HISTORIES") != "1" {
		t.Skip("records replay testdata; set RECORD_HISTORIES=1")
	}
	s := newIncStack(t, time.Second) // 120 h last five seconds
	inc, _, res := s.started("P1", map[string]any{"f_assignee": s.sam.Actor})
	s.act(s.sam, inc, "in_progress", nil)
	s.act(s.sam, inc, "waiting_for_customer", nil)
	eventually(t, 20*time.Second, "resolution paused", s.is(res, "paused"))
	s.comment(s.ana, inc, true)
	eventually(t, 20*time.Second, "the incident back in progress", s.is(inc, "in_progress"))
	eventually(t, 20*time.Second, "resolution running again", s.is(res, "running"))
	eventually(t, 25*time.Second, "resolution breached and sam told", func() bool {
		return s.f(res, "f_breached_at") != nil && len(s.told(res)) == 1
	})
	s.act(s.sam, inc, "resolved", map[string]any{"f_resolution": "fixed"})
	eventually(t, 20*time.Second, "resolution completed", s.is(res, "completed"))
	eventually(t, 30*time.Second, "the incident closed by its timer", s.is(inc, "closed"))
	s.waitDelivered(inc)
	s.waitCompleted(inc)
	s.waitDelivered(res)
	s.waitCompleted(res)
	// Visibility is eventually consistent: give the list a moment.
	time.Sleep(2 * time.Second)

	longest, most := "", 0
	for _, run := range runsOf(t, inc) {
		if n := len(runHistory(t, inc, run).Events); n > most {
			longest, most = run, n
		}
	}
	if longest == "" {
		t.Fatal("the incident has no run")
	}
	t.Logf("the incident had %d runs; the longest has %d events", len(runsOf(t, inc)), most)
	saveRunHistory(t, inc, longest, incidentHistories[0])

	for _, run := range runsOf(t, res) {
		for _, ev := range runHistory(t, res, run).Events {
			if a := ev.GetActivityTaskScheduledEventAttributes(); a.GetActivityType().GetName() == wf.ActivityActAt {
				saveRunHistory(t, res, run, incidentHistories[1])
				return
			}
		}
	}
	t.Fatalf("none of the goal's %d runs scheduled %s", len(runsOf(t, res)), wf.ActivityActAt)
}

// The recorded histories replay on today's workflow code without a
// non-determinism error.
func TestReplay_IncidentHistories(t *testing.T) {
	for _, name := range incidentHistories {
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
