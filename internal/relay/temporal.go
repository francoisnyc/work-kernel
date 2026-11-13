package relay

import (
	"context"
	"errors"
	"time"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"

	"github.com/francoisnyc/work-kernel/internal/workflow"
)

// deliveryTimeout bounds one delivery. Without it, an update to a workflow
// whose worker is down would wait forever and hold up every other Item.
const deliveryTimeout = 10 * time.Second

// temporalWorkflows is the real Workflows: it talks to a Temporal server.
type temporalWorkflows struct {
	client    client.Client
	taskQueue string
}

// NewTemporalWorkflows returns the Workflows the relay uses in production.
// Workflows it starts run on the given task queue.
func NewTemporalWorkflows(c client.Client, taskQueue string) Workflows {
	return &temporalWorkflows{client: c, taskQueue: taskQueue}
}

func (t *temporalWorkflows) Deliver(ctx context.Context, d Delivery) error {
	ctx, cancel := context.WithTimeout(ctx, deliveryTimeout)
	defer cancel()

	update := client.UpdateWorkflowOptions{
		WorkflowID: d.ItemID,
		UpdateID:   d.UpdateID,
		UpdateName: workflow.UpdateApply,
		Args:       []any{d.Event},
		// Wait until the handler has finished, not just until the update
		// was accepted: an accepted update can still fail.
		WaitForStage: client.WorkflowUpdateStageCompleted,
	}

	var (
		handle client.WorkflowUpdateHandle
		err    error
	)
	if d.Start {
		// Update-With-Start: one call that starts the workflow if none is
		// running (USE_EXISTING: otherwise use the running one) and sends
		// the update. A new run loads its state from Postgres itself.
		start := t.client.NewWithStartWorkflowOperation(client.StartWorkflowOptions{
			ID:                       d.ItemID,
			TaskQueue:                t.taskQueue,
			WorkflowIDConflictPolicy: enumspb.WORKFLOW_ID_CONFLICT_POLICY_USE_EXISTING,
		}, workflow.Name, workflow.Input{ItemID: d.ItemID})
		handle, err = t.client.UpdateWithStartWorkflow(ctx, client.UpdateWithStartWorkflowOptions{
			StartWorkflowOperation: start,
			UpdateOptions:          update,
		})
	} else {
		handle, err = t.client.UpdateWorkflow(ctx, update)
		// The server answers NotFound both for a workflow that never
		// existed and for one that has completed (see server_test.go).
		var notFound *serviceerror.NotFound
		if errors.As(err, &notFound) {
			return ErrNothingListening
		}
	}
	if err != nil {
		return err
	}
	// Get returns the handler's error, if it failed.
	return handle.Get(ctx, nil)
}
