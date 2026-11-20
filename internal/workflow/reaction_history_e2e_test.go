// A recorded history for Jira part 2, step 2: a container with no running
// workflow is woken by the create of an Item inside it (the relay's `child`
// update, with start), runs its created_inside reaction, records the
// outcome and completes. TestReplay_ReactionHistories replays it on today's
// workflow code.
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

// Recorded by TestRecordReactionHistory (RECORD_HISTORIES=1) from the code
// that first had reactions that move. Do not re-record it after changing
// the workflow: then it would prove nothing.
var reactionHistories = []string{
	"desk_woken_cold_by_a_child.json", // a run started by the child update: LoadItem, RunAfterReaction, done
}

// TestRecordReactionHistory records the history above. It runs only with
// RECORD_HISTORIES=1.
func TestRecordReactionHistory(t *testing.T) {
	if os.Getenv("RECORD_HISTORIES") != "1" {
		t.Skip("records replay testdata; set RECORD_HISTORIES=1")
	}
	s := newDeskStack(t)
	desk := s.waitingDesk()
	_, before, _, _ := runStatus(t, desk)

	s.makeIn(alice, "wfremark", &desk, map[string]any{"f_body": "any news?", "f_public": true})
	s.waitState(desk, "working", 15*time.Second)
	eventually(t, 15*time.Second, "the outcome recorded on the desk", s.isAt(desk, "working", 6))
	s.waitCompleted(desk)

	// The desk's own move is delivered too, and may start one more run
	// after the reaction's. The one to keep is the run the child woke.
	list, err := tc.ListWorkflow(serverCtx(t), &workflowservice.ListWorkflowExecutionsRequest{
		Query: fmt.Sprintf("WorkflowId = '%s'", desk),
	})
	if err != nil {
		t.Fatalf("list the desk's runs: %v", err)
	}
	for _, e := range list.GetExecutions() {
		run := e.GetExecution().GetRunId()
		if run == before {
			continue
		}
		for _, ev := range runHistory(t, desk, run).Events {
			if a := ev.GetWorkflowExecutionUpdateAcceptedEventAttributes(); a.GetAcceptedRequest().GetInput().GetName() == wf.UpdateChild {
				saveRunHistory(t, desk, run, reactionHistories[0])
				return
			}
		}
	}
	t.Fatalf("none of the desk's %d runs accepted a %s update", len(list.GetExecutions()), wf.UpdateChild)
}

// The recorded reaction history replays on today's workflow code without a
// non-determinism error.
func TestReplay_ReactionHistories(t *testing.T) {
	for _, name := range reactionHistories {
		// A closure per history: t.Run gives each its own name and failure.
		t.Run(name, func(t *testing.T) {
			f, err := os.Open(filepath.Join(historiesDir, name))
			if err != nil {
				t.Fatalf("open recorded history: %v", err)
			}
			defer f.Close() // defer runs when the function returns, like a finally block
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
