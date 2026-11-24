// Command ptreplay replays the recorded history of every purchase workflow
// against the code as it is now, as Temporal recommends before deploying a
// change to workflow code. A history that the code no longer matches is a
// request that would break after the deploy.
//
//	ptreplay [--purchase-code before|naive|patched]
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"log/slog"
	"os"
	"strings"

	enumspb "go.temporal.io/api/enums/v1"
	historypb "go.temporal.io/api/history/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	sdklog "go.temporal.io/sdk/log"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"github.com/francoisnyc/work-kernel/spikes/temporal-bakeoff/purchase"
)

func main() {
	flag.StringVar(&purchase.Deployed, "purchase-code", purchase.Deployed, "before, naive or patched (see purchase/change.go)")
	flag.Parse()
	addr := os.Getenv("TEMPORAL_ADDRESS")
	if addr == "" {
		addr = "127.0.0.1:27233"
	}
	quiet := sdklog.NewStructuredLogger(slog.New(slog.NewTextHandler(io.Discard, nil)))
	c, err := client.Dial(client.Options{HostPort: addr, Logger: quiet})
	if err != nil {
		log.Fatal(err)
	}
	defer c.Close()
	ctx := context.Background()

	replayer := worker.NewWorkflowReplayer()
	replayer.RegisterWorkflowWithOptions(purchase.Workflow, workflow.RegisterOptions{Name: purchase.WorkflowName})

	list, err := c.ListWorkflow(ctx, &workflowservice.ListWorkflowExecutionsRequest{Query: "WorkflowType = '" + purchase.WorkflowName + "'", PageSize: 200})
	if err != nil {
		log.Fatal(err)
	}
	broken := 0
	for _, e := range list.Executions {
		id, runID := e.Execution.WorkflowId, e.Execution.RunId
		// The history is read event by event, as a history file would hold it.
		var events []*historypb.HistoryEvent
		iter := c.GetWorkflowHistory(ctx, id, runID, false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
		for iter.HasNext() {
			ev, err := iter.Next()
			if err != nil {
				log.Fatal(err)
			}
			events = append(events, ev)
		}
		err := replayer.ReplayWorkflowHistory(quiet, &historypb.History{Events: events})
		status := "replays"
		if err != nil {
			broken++
			status = "DOES NOT REPLAY: " + firstLine(err.Error())
		}
		fmt.Printf("%s (%s, %d events): %s\n", id, strings.ToLower(strings.TrimPrefix(e.Status.String(), "WORKFLOW_EXECUTION_STATUS_")), len(events), status)
	}
	fmt.Printf("code %q: %d of %d purchase histories no longer replay\n", purchase.Deployed, broken, len(list.Executions))
	if broken > 0 {
		os.Exit(1)
	}
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(s, "\n")
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}
