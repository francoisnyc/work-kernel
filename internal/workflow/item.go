package workflow

import (
	"errors"
	"fmt"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// Names shared by the workflow, the worker that registers it and the relay
// that calls it. Workflows and activities are looked up by name, not by Go
// function, so the names are part of the contract with running workflows.
const (
	Name              = "item"      // the workflow type; the workflow ID is the Item's ID
	UpdateApply       = "apply"     // the update that delivers one event
	ActivityLoadItem  = "LoadItem"  // reads a Snapshot from Postgres
	ActivityFireTimer = "FireTimer" // asks the kernel to make a timer's move

	// Error types by which FireTimer reports the kernel's two kinds of "no".
	ErrTypeStale    = "Stale"    // the Item moved on; the workflow is behind
	ErrTypeRejected = "Rejected" // the Type's rules refuse the move
)

// TimerSpec is one state's timer, reduced to what the workflow needs.
type TimerSpec struct {
	After time.Duration
	To    string
}

// Snapshot is all a run knows about its Item. The first run loads it from
// Postgres; a run that continues as new hands it to the next one. It holds
// opaque IDs and state names only, never field values.
type Snapshot struct {
	TypeID      string
	TypeVersion int
	// Timers maps each state that has a timer to that timer. The workflow
	// never sees the Type itself, so a change to how Types are parsed cannot
	// break the replay of a running workflow.
	Timers map[string]TimerSpec

	State          string
	LastAppliedSeq int64 // the highest item_seq this workflow has taken into account

	EnteredSeq    int64     // the event that put the Item in State
	EnteredAt     time.Time // when that event was written; timers count from here
	CorrelationID string    // of that event
	Hop           int       // of that event

	// TimerDone is true once the current state's timer has been dealt with
	// (its move was written, or the kernel refused it). It is reset whenever
	// the Item enters a state.
	TimerDone bool
}

// Input starts a run. Carried is set only by continue-as-new.
type Input struct {
	ItemID  string
	Carried *Snapshot
}

// Event is the shape of one item_events row, as the relay delivers it.
type Event struct {
	Seq  int64
	Op   string    // create, update, transition, ...
	From string    // transition only
	To   string    // the state the event enters; empty if it enters none
	At   time.Time // when the event was written

	CorrelationID string
	Hop           int
}

// ApplyResult answers one delivered event.
type ApplyResult struct {
	Applied bool // false for a duplicate
}

// FireTimerInput is the request the FireTimer activity turns into a kernel
// Transition.
type FireTimerInput struct {
	ItemID          string
	TypeID          string
	TypeVersion     int
	FromState       string
	ToState         string
	ExpectedItemSeq int64 // the move is made only if the Item is still here
	EnteredSeq      int64 // the event that armed the timer
	CorrelationID   string
	Hop             int
	// IdempotencyKey names this exact request. It includes ExpectedItemSeq
	// because the kernel refuses a key that comes back with a different request.
	IdempotencyKey string
}

// activityOptions: an attempt may take 30 seconds; a failed one is retried
// with growing pauses, forever (MaximumAttempts 0 means no limit), so an
// outage delays a timer but never loses it. Errors the activities mark as
// non-retryable (Stale, Rejected) are the exception: they return at once.
var activityOptions = workflow.ActivityOptions{
	StartToCloseTimeout: 30 * time.Second,
	RetryPolicy: &temporal.RetryPolicy{
		InitialInterval:    time.Second,
		BackoffCoefficient: 2,
		MaximumInterval:    time.Minute,
		MaximumAttempts:    0,
	},
}

// ItemWorkflow follows one Item for as long as its state has behavior (today:
// a timer). It runs as one logical thread; update handlers run interleaved
// with it but never in parallel, so the shared variables need no locks.
func ItemWorkflow(ctx workflow.Context, in Input) error {
	ctx = workflow.WithActivityOptions(ctx, activityOptions)
	log := workflow.GetLogger(ctx)

	var (
		s       Snapshot
		ready   bool // s is loaded
		changes int  // counts applied events; the main loop waits on it
		entries int  // counts state entries among them
	)

	apply := func(ctx workflow.Context, ev Event) (ApplyResult, error) {
		// An update can arrive together with the start, before the load.
		if err := workflow.Await(ctx, func() bool { return ready }); err != nil {
			return ApplyResult{}, err
		}
		if ev.Seq <= s.LastAppliedSeq {
			return ApplyResult{}, nil // duplicate, or older than what was loaded
		}
		if ev.Seq > s.LastAppliedSeq+1 {
			// Only reachable for an update accepted before the load finished;
			// the validator below refuses every later gap.
			return ApplyResult{}, gapError(ev.Seq, s.LastAppliedSeq)
		}
		s.LastAppliedSeq = ev.Seq
		changes++
		if ev.To != "" {
			s.State = ev.To
			s.EnteredSeq, s.EnteredAt = ev.Seq, ev.At
			s.CorrelationID, s.Hop = ev.CorrelationID, ev.Hop
			s.TimerDone = false
			entries++
		}
		return ApplyResult{Applied: true}, nil
	}
	// A validator runs before an update is accepted. What it refuses is never
	// written to history, so a relay retrying a gap does not grow it. It may
	// not wait, so before the load it lets everything through to the handler.
	validate := func(ctx workflow.Context, ev Event) error {
		if ready && ev.Seq > s.LastAppliedSeq+1 {
			return gapError(ev.Seq, s.LastAppliedSeq)
		}
		return nil
	}
	err := workflow.SetUpdateHandlerWithOptions(ctx, UpdateApply, apply,
		workflow.UpdateHandlerOptions{Validator: validate})
	if err != nil {
		return err
	}

	if in.Carried != nil {
		s = *in.Carried
	} else if err := workflow.ExecuteActivity(ctx, ActivityLoadItem, in.ItemID).Get(ctx, &s); err != nil {
		return err
	}
	ready = true

	handlersDone := func() bool { return workflow.AllHandlersFinished(ctx) }

	for {
		// A long history is slow to replay. When the server suggests it,
		// hand over to a fresh run, once no update is half-handled.
		if workflow.GetInfo(ctx).GetContinueAsNewSuggested() {
			if err := workflow.Await(ctx, handlersDone); err != nil {
				return err
			}
			carried := s
			return workflow.NewContinueAsNewError(ctx, Name, Input{ItemID: in.ItemID, Carried: &carried})
		}

		timer, hasTimer := s.Timers[s.State]
		if !hasTimer {
			// Nothing to wait for: finish, unless an update still being
			// handled puts the Item back into a state with behavior.
			if err := workflow.Await(ctx, handlersDone); err != nil {
				return err
			}
			if _, hasTimer = s.Timers[s.State]; hasTimer {
				continue
			}
			return nil
		}

		seen, entry := changes, entries
		changed := func() bool { return changes != seen }

		if s.TimerDone {
			// The timer has had its turn; wait for the Item's next event.
			if err := workflow.Await(ctx, changed); err != nil {
				return err
			}
			continue
		}

		// Wait until the deadline, or until an event changes the picture. The
		// deadline hangs on EnteredAt, so a field update or a restart never
		// moves it. workflow.Now is the replay-safe clock.
		if wait := s.EnteredAt.Add(timer.After).Sub(workflow.Now(ctx)); wait > 0 {
			woken, err := workflow.AwaitWithTimeout(ctx, wait, changed)
			if err != nil {
				return err
			}
			if woken {
				continue
			}
		}

		req := FireTimerInput{
			ItemID: in.ItemID, TypeID: s.TypeID, TypeVersion: s.TypeVersion,
			FromState: s.State, ToState: timer.To,
			ExpectedItemSeq: s.LastAppliedSeq, EnteredSeq: s.EnteredSeq,
			CorrelationID: s.CorrelationID, Hop: s.Hop + 1,
			IdempotencyKey: fmt.Sprintf("timer:%s:%d:%d", in.ItemID, s.EnteredSeq, s.LastAppliedSeq),
		}
		err := workflow.ExecuteActivity(ctx, ActivityFireTimer, req).Get(ctx, nil)

		// errors.As digs the ApplicationError out of the SDK's wrappers.
		var appErr *temporal.ApplicationError
		switch {
		case err == nil:
			// The move is written; its event will arrive like any other.
		case errors.As(err, &appErr) && appErr.Type() == ErrTypeStale:
			// The kernel is ahead of us: an event is still on its way. Wait
			// for it, then look again. If the Item is still in this state
			// the deadline has passed, so the timer fires again at once.
			if err := workflow.Await(ctx, changed); err != nil {
				return err
			}
			continue
		case errors.As(err, &appErr) && appErr.Type() == ErrTypeRejected:
			log.Info("the kernel refused a timer's move",
				"item", in.ItemID, "from", req.FromState, "to", req.ToState, "reason", appErr.Message())
		default:
			return err // only a canceled workflow gets here; other failures retry forever
		}
		// While the activity ran, the Item may have entered a state anew;
		// that entry's timer is not this one.
		if entries == entry {
			s.TimerDone = true
		}
	}
}

func gapError(seq, lastApplied int64) error {
	return fmt.Errorf("gap: event %d arrived but the last applied is %d", seq, lastApplied)
}
