package workflow

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/kernel"
	"github.com/francoisnyc/work-kernel/internal/lifecycle"
)

// timerActor is the principal a timer's write is checked and recorded as.
const timerActor = "system:timer"

// Activities is the I/O side of the workflow: ordinary Go, run by the worker,
// retried by Temporal when it fails. Its methods are the activities.
type Activities struct {
	Kernel *kernel.Kernel
}

// LoadItem reads what a new run needs to know about its Item: where it is,
// which event put it there, and the Type's timers as a small table. The
// workflow gets no more of the Type than that.
func (a *Activities) LoadItem(ctx context.Context, itemID string) (Snapshot, error) {
	id, err := uuid.Parse(itemID)
	if err != nil {
		return Snapshot{}, fmt.Errorf("item id %q: %w", itemID, err)
	}
	se, err := a.Kernel.StateEntry(ctx, id)
	if errors.Is(err, kernel.ErrNotFound) {
		// Retrying cannot bring the Item back; tell the workflow instead.
		return Snapshot{}, temporal.NewNonRetryableApplicationError(err.Error(), ErrTypeNotFound, err)
	}
	if err != nil {
		return Snapshot{}, err
	}
	ty, err := a.Kernel.Type(ctx, se.TypeID, se.TypeVersion)
	if err != nil {
		return Snapshot{}, err
	}
	timers := map[string]TimerSpec{}
	for _, s := range ty.States {
		if timer, ok := lifecycle.TimerFor(ty, s.ID); ok {
			timers[s.ID] = TimerSpec{After: timer.Duration, To: timer.To}
		}
	}
	return Snapshot{
		TypeID: se.TypeID, TypeVersion: se.TypeVersion, Timers: timers,
		State: se.State, LastAppliedSeq: se.ItemSeq,
		EnteredSeq: se.EnteredSeq, EnteredAt: se.EnteredAt,
		CorrelationID: se.CorrelationID, Hop: se.Hop,
	}, nil
}

// FireTimer makes a timer's move through the kernel's normal write path, so
// guards, history and the outbox all apply as for a person's transition.
//
// The kernel's final answers (stale, rejected, not found) are returned as
// non-retryable errors, which the workflow tells apart by type. Anything else (the
// database is away, a timeout) is returned as is, and Temporal retries it.
func (a *Activities) FireTimer(ctx context.Context, in FireTimerInput) error {
	id, err := uuid.Parse(in.ItemID)
	if err != nil {
		return fmt.Errorf("item id %q: %w", in.ItemID, err)
	}
	_, err = a.Kernel.Transition(ctx, kernel.TransitionRequest{
		IdempotencyKey:  in.IdempotencyKey,
		Principal:       item.Principal{Actor: timerActor},
		ItemID:          id,
		ToState:         in.ToState,
		ExpectedItemSeq: in.ExpectedItemSeq,
		Attribution: item.Attribution{
			OriginatingHook: fmt.Sprintf("timer:%s@v%d:%s", in.TypeID, in.TypeVersion, in.FromState),
			CausationID:     fmt.Sprintf("%s:%d", in.ItemID, in.EnteredSeq),
			CorrelationID:   in.CorrelationID,
			Hop:             in.Hop,
		},
	})
	switch {
	case errors.Is(err, kernel.ErrNotFound):
		return temporal.NewNonRetryableApplicationError(err.Error(), ErrTypeNotFound, err)
	case errors.Is(err, kernel.ErrStale):
		return temporal.NewNonRetryableApplicationError(err.Error(), ErrTypeStale, err)
	case errors.Is(err, kernel.ErrRejected):
		activity.GetLogger(ctx).Info("the kernel refused a timer's move",
			"item", in.ItemID, "from", in.FromState, "to", in.ToState, "reason", err.Error())
		return temporal.NewNonRetryableApplicationError(err.Error(), ErrTypeRejected, err)
	}
	return err
}

// WorkerOptions tune a worker. The zero value is what production uses.
type WorkerOptions struct {
	// CloseDelay is for tests only. It makes a workflow pause this long
	// between deciding to complete and returning, so a test can send an
	// event into that window. It must be the same on every worker of a task
	// queue, because the pause is part of the workflow's recorded history.
	CloseDelay time.Duration
}

// NewWorker returns a worker, not yet started, that runs Item workflows and
// their activities from the given task queue.
func NewWorker(c client.Client, taskQueue string, k *kernel.Kernel, opts WorkerOptions) worker.Worker {
	w := worker.New(c, taskQueue, worker.Options{})
	run := ItemWorkflow
	if opts.CloseDelay > 0 {
		run = func(ctx workflow.Context, in Input) error { return runItem(ctx, in, opts.CloseDelay) }
	}
	w.RegisterWorkflowWithOptions(run, workflow.RegisterOptions{Name: Name})

	// Registered by name, because the workflow calls them by name.
	a := &Activities{Kernel: k}
	w.RegisterActivityWithOptions(a.LoadItem, activity.RegisterOptions{Name: ActivityLoadItem})
	w.RegisterActivityWithOptions(a.FireTimer, activity.RegisterOptions{Name: ActivityFireTimer})
	return w
}
