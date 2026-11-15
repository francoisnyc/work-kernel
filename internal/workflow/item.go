package workflow

import (
	"errors"
	"fmt"
	"math"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/francoisnyc/work-kernel/internal/lifecycle"
)

// Names shared by the workflow, the worker that registers it and the relay
// that calls it. Workflows and activities are looked up by name, not by Go
// function, so the names are part of the contract with running workflows.
const (
	Name              = "item"      // the workflow type; the workflow ID is the Item's ID
	UpdateApply       = "apply"     // the update that delivers one event
	ActivityLoadItem  = "LoadItem"  // reads a Snapshot from Postgres
	ActivityFireTimer = "FireTimer" // asks the item service to make a timer's move

	// Hooks (Step 6). Each hook activity reads its inputs, calls out, and
	// writes back through the item service itself: the workflow never sees
	// a field value.
	ActivityRunEnrich      = "RunEnrich"      // runs an enrich hook and writes its fields
	ActivityEnrichFallback = "EnrichFallback" // writes an enrich hook's fallback, if it has one
	ActivityRunCheck       = "RunCheck"       // runs a check and resolves its hold
	ActivityResolveHold    = "ResolveHold"    // resolves a hold as error when its check cannot

	// Error types by which the activities report the item service's final answers.
	// All are non-retryable.
	ErrTypeStale      = "Stale"      // the Item moved on; the workflow is behind
	ErrTypeRejected   = "Rejected"   // the Type's rules refuse the move
	ErrTypeNotFound   = "NotFound"   // the Item does not exist (any more)
	ErrTypeInvalid    = "Invalid"    // the request is malformed; the same request can never succeed
	ErrTypeHeld       = "Held"       // an open hold refuses the move; for a timer, final
	ErrTypeHookFailed = "HookFailed" // the hook failed for good (unknown customer, bad input)
)

// DefaultHookBudget is how long a hook may keep retrying before it gives up:
// an enrich hook then writes its fallback, a check resolves its hold as error.
const DefaultHookBudget = 15 * time.Minute

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
	// (its move was written, or the item service refused it). It is reset whenever
	// the Item enters a state.
	TimerDone bool

	// Hooks, reduced like Timers. Enrich maps a state to the hooks that run
	// on entering it, in the Type's order. Checks maps "from->to" to the
	// check hook of that transition.
	Enrich     map[string][]HookSpec
	Checks     map[string]string
	HookBudget time.Duration // each hook's ScheduleToClose; zero means DefaultHookBudget

	// PendingEnrich are the enrich hooks of the current state entry not yet
	// started; they are caused by EnteredSeq. Hold is the Item's open hold,
	// if any. LoadItem fills both, because a run started by the very event
	// that needs a hook loads that event as already applied.
	PendingEnrich []string
	Hold          *OpenHold
}

// HookSpec is one enrich hook, reduced to what the workflow needs.
type HookSpec struct {
	ID          string
	HasFallback bool
}

// OpenHold is an open hold: which check to run, caused by which request.
type OpenHold struct {
	ID            string
	Hook          string
	From          string
	RequestedTo   string
	RequestedSeq  int64  // the transition_requested event
	CorrelationID string // of that event
	Hop           int    // of that event
}

// HookInput is what a hook activity needs to run one hook. It names the
// hook and its cause, never a field value: the activity reads those itself.
type HookInput struct {
	ItemID        string
	TypeID        string
	TypeVersion   int
	HookID        string
	CauseSeq      int64  // the event that triggered the hook
	HoldID        string // check only
	CorrelationID string
	Hop           int
	// IdempotencyKey: hook:<item>:<cause seq>:<hook> for an enrich hook (its
	// own write and its fallback share it), hold:<hold> for a check (its
	// resolution and the error resolution share it). Either way, one write.
	IdempotencyKey string
}

// ResolveInput resolves a hold whose check could not answer.
type ResolveInput struct {
	Hook    HookInput
	Verdict string // "error"
	Reason  string // budget_exhausted or check_failed
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

	// For the events of a hold: the state a transition request asks for,
	// and the hold the event opens (transition_requested) or closes (any
	// other event that names it). Opaque IDs and state names only.
	RequestedTo string
	HoldID      string
}

// ApplyResult answers one delivered event.
type ApplyResult struct {
	Applied bool // false for a duplicate
}

// FireTimerInput is the request the FireTimer activity turns into an item service
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
	// because the item service refuses a key that comes back with a different request.
	IdempotencyKey string
}

// activityOptions: an attempt may take 30 seconds; a failed one is retried
// with growing pauses, forever (MaximumAttempts 0 means no limit), so an
// outage delays a timer but never loses it. Errors the activities mark as
// non-retryable (Stale, Rejected, NotFound, Invalid, Held) are the
// exception: they return at once.
var activityOptions = workflow.ActivityOptions{
	StartToCloseTimeout: 30 * time.Second,
	RetryPolicy: &temporal.RetryPolicy{
		InitialInterval:    time.Second,
		BackoffCoefficient: 2,
		MaximumInterval:    time.Minute,
		MaximumAttempts:    0,
	},
}

// hookOptions bound a hook: retried like any activity, but only within its
// budget (ScheduleToClose counts every attempt and every pause).
//
// The budget is the only bound, so attempts are unlimited. That is said as
// math.MaxInt32 rather than 0: Temporal's test environment turns 0 into 10
// attempts, which would end a hook before its budget in unit tests.
func hookOptions(budget time.Duration) workflow.ActivityOptions {
	opts := activityOptions
	retry := *activityOptions.RetryPolicy // a copy: the shared policy stays as it is
	retry.MaximumAttempts = math.MaxInt32
	opts.RetryPolicy = &retry
	opts.ScheduleToCloseTimeout = budget
	opts.StartToCloseTimeout = min(30*time.Second, budget)
	return opts
}

// ItemWorkflow follows one Item for as long as it has behavior: a timer in
// its state, hooks to run, or an open hold. It runs as one logical thread;
// update handlers and hook coroutines run interleaved with it but never in
// parallel, so the shared variables need no locks.
func ItemWorkflow(ctx workflow.Context, in Input) error {
	return runItem(ctx, in, 0)
}

// runItem is ItemWorkflow with one knob for tests: closeDelay makes the run
// pause between deciding to complete and returning, so a test can send an
// event into that window. Production passes zero.
func runItem(ctx workflow.Context, in Input, closeDelay time.Duration) error {
	ctx = workflow.WithActivityOptions(ctx, activityOptions)
	log := workflow.GetLogger(ctx)

	var (
		s       Snapshot
		ready   bool // s is loaded, or the Item turned out to be gone
		gone    bool // the Item no longer exists
		changes int  // counts applied events; the timer's waits watch it
		entries int  // counts state entries among them
		wake    int  // counts anything the main loop may care about: events, hooks finishing

		running      int                 // hook coroutines still running
		checkStarted bool                // the open hold's check has been started
		cancelCheck  workflow.CancelFunc // cancels the running check, if any
	)

	apply := func(ctx workflow.Context, ev Event) (ApplyResult, error) {
		// An update can arrive together with the start, before the load.
		if err := workflow.Await(ctx, func() bool { return ready }); err != nil {
			return ApplyResult{}, err
		}
		if gone || ev.Seq <= s.LastAppliedSeq {
			return ApplyResult{}, nil // no Item, a duplicate, or older than what was loaded
		}
		if ev.Seq > s.LastAppliedSeq+1 {
			// Only reachable for an update accepted before the load finished;
			// the validator below refuses every later gap.
			return ApplyResult{}, gapError(ev.Seq, s.LastAppliedSeq)
		}
		s.LastAppliedSeq = ev.Seq
		changes++
		wake++
		switch {
		case ev.Op == lifecycle.OpTransitionRequested:
			s.Hold = &OpenHold{
				ID: ev.HoldID, Hook: s.Checks[ev.From+"->"+ev.RequestedTo],
				From: ev.From, RequestedTo: ev.RequestedTo, RequestedSeq: ev.Seq,
				CorrelationID: ev.CorrelationID, Hop: ev.Hop,
			}
			checkStarted = false
		case ev.HoldID != "" && s.Hold != nil && ev.HoldID == s.Hold.ID:
			// The hold is closed (resolved, or canceled by an escape). A
			// check still running has nothing left to decide.
			s.Hold = nil
			if cancelCheck != nil {
				cancelCheck()
				cancelCheck = nil
			}
		}
		if ev.To != "" {
			s.State = ev.To
			s.EnteredSeq, s.EnteredAt = ev.Seq, ev.At
			s.CorrelationID, s.Hop = ev.CorrelationID, ev.Hop
			s.TimerDone = false
			// The new state's enrich hooks. Those of a state already left
			// and not yet started are dropped: the check is the backstop.
			s.PendingEnrich = nil
			for _, h := range s.Enrich[ev.To] {
				s.PendingEnrich = append(s.PendingEnrich, h.ID)
			}
			entries++
		}
		return ApplyResult{Applied: true}, nil
	}
	// A validator runs before an update is accepted. What it refuses is never
	// written to history, so a relay retrying a gap does not grow it. It may
	// not wait, so before the load it lets everything through to the handler.
	validate := func(ctx workflow.Context, ev Event) error {
		if ready && !gone && ev.Seq > s.LastAppliedSeq+1 {
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
		var appErr *temporal.ApplicationError
		if !errors.As(err, &appErr) || appErr.Type() != ErrTypeNotFound {
			return err
		}
		// The Item is gone: there is nothing to follow. s stays empty, so
		// it has no timers and the loop below completes the run. A waiting
		// update is answered as "not applied", so the relay marks it delivered.
		log.Info("the item no longer exists; nothing to wait for", "item", in.ItemID)
		gone = true
	}
	ready = true
	budget := s.HookBudget
	if budget <= 0 {
		budget = DefaultHookBudget
	}

	hookInput := func(hookID string, causeSeq int64, corr string, hop int) HookInput {
		return HookInput{
			ItemID: in.ItemID, TypeID: s.TypeID, TypeVersion: s.TypeVersion, HookID: hookID,
			CauseSeq: causeSeq, CorrelationID: corr, Hop: hop,
			IdempotencyKey: fmt.Sprintf("hook:%s:%d:%s", in.ItemID, causeSeq, hookID),
		}
	}

	// startEnrich runs one enrich hook in a coroutine. workflow.Go is the
	// SDK's deterministic stand-in for a goroutine: the SDK schedules it, so
	// replay runs it the same way. When the budget runs out (or the hook
	// fails for good), its fallback is written, if it has one.
	startEnrich := func(spec HookSpec) {
		hin := hookInput(spec.ID, s.EnteredSeq, s.CorrelationID, s.Hop+1)
		running++
		workflow.Go(ctx, func(gctx workflow.Context) {
			defer func() { running--; wake++ }()
			err := workflow.ExecuteActivity(workflow.WithActivityOptions(gctx, hookOptions(budget)),
				ActivityRunEnrich, hin).Get(gctx, nil)
			if err == nil || !hookGaveUp(err) {
				logHookEnd(log, hin, err)
				return
			}
			if !spec.HasFallback {
				log.Info("enrich hook gave up; its fields stay unset", "item", in.ItemID, "hook", spec.ID)
				return
			}
			err = workflow.ExecuteActivity(gctx, ActivityEnrichFallback, hin).Get(gctx, nil)
			logHookEnd(log, hin, err)
		})
	}

	// startCheck runs the open hold's check in a coroutine that the hold's
	// closing cancels. When the budget runs out, or the check fails for
	// good, it resolves the hold as error, under the same key as the check.
	startCheck := func() {
		h := *s.Hold
		checkStarted = true
		if h.Hook == "" {
			log.Error("no check is known for this hold", "item", in.ItemID, "hold", h.ID)
			return
		}
		hin := hookInput(h.Hook, h.RequestedSeq, h.CorrelationID, h.Hop+1)
		hin.HoldID, hin.IdempotencyKey = h.ID, "hold:"+h.ID
		cctx, cancel := workflow.WithCancel(ctx)
		cancelCheck = cancel
		running++
		workflow.Go(cctx, func(gctx workflow.Context) {
			defer func() { running--; wake++ }()
			err := workflow.ExecuteActivity(workflow.WithActivityOptions(gctx, hookOptions(budget)),
				ActivityRunCheck, hin).Get(gctx, nil)
			if err == nil || gctx.Err() != nil || !hookGaveUp(err) {
				logHookEnd(log, hin, err)
				return
			}
			reason := "budget_exhausted"
			var appErr *temporal.ApplicationError
			if errors.As(err, &appErr) && appErr.Type() == ErrTypeHookFailed {
				reason = "check_failed"
			}
			err = workflow.ExecuteActivity(gctx, ActivityResolveHold,
				ResolveInput{Hook: hin, Verdict: "error", Reason: reason}).Get(gctx, nil)
			logHookEnd(log, hin, err)
		})
	}

	handlersDone := func() bool { return workflow.AllHandlersFinished(ctx) }
	busy := func() bool { return running > 0 || s.Hold != nil || len(s.PendingEnrich) > 0 }

	for {
		// Hook work the last events (or the load) asked for.
		if !gone {
			pending := s.PendingEnrich
			s.PendingEnrich = nil
			for _, id := range pending {
				spec := HookSpec{ID: id}
				for _, h := range s.Enrich[s.State] {
					if h.ID == id {
						spec = h
					}
				}
				startEnrich(spec)
			}
			if s.Hold != nil && !checkStarted {
				startCheck()
			}
		}

		// A long history is slow to replay. When the server suggests it,
		// hand over to a fresh run, once no update is half-handled and no
		// hook is running (a coroutine cannot be carried over).
		if workflow.GetInfo(ctx).GetContinueAsNewSuggested() && running == 0 {
			if err := workflow.Await(ctx, handlersDone); err != nil {
				return err
			}
			carried := s
			return workflow.NewContinueAsNewError(ctx, Name, Input{ItemID: in.ItemID, Carried: &carried})
		}

		timer, hasTimer := s.Timers[s.State]
		if !hasTimer {
			if busy() {
				// Hooks are running or a hold is open: wait for news.
				seen := wake
				if err := workflow.Await(ctx, func() bool { return wake != seen }); err != nil {
					return err
				}
				continue
			}
			// Nothing to wait for: finish, unless an update still being
			// handled puts the Item back into a state with behavior.
			if err := workflow.Await(ctx, handlersDone); err != nil {
				return err
			}
			if closeDelay > 0 {
				// workflow.Sleep is a durable timer, not a blocked thread;
				// updates are still handled while it runs.
				if err := workflow.Sleep(ctx, closeDelay); err != nil {
					return err
				}
				if err := workflow.Await(ctx, handlersDone); err != nil {
					return err
				}
			}
			if _, hasTimer = s.Timers[s.State]; hasTimer || busy() {
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
			// The item service is ahead of us: an event is still on its way. Wait
			// for it, then look again. If the Item is still in this state
			// the deadline has passed, so the timer fires again at once.
			if err := workflow.Await(ctx, changed); err != nil {
				return err
			}
			continue
		case errors.As(err, &appErr) && (appErr.Type() == ErrTypeRejected || appErr.Type() == ErrTypeNotFound ||
			appErr.Type() == ErrTypeInvalid || appErr.Type() == ErrTypeHeld):
			// Final either way: the rules say no, the Item is gone, the
			// request is malformed (a bug, which retrying cannot fix), or an
			// open hold refuses the move. A held timer is spent: it does not
			// fire again once the hold resolves (docs/journal.md, Later).
			log.Info("the item service refused a timer's move",
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

// hookGaveUp reports whether a hook activity's error means "this hook will
// not answer": its budget ran out, or it failed for good. The item
// service's final answers (stale, rejected, not found, invalid, held) are not
// that: the hook ran, and the kernel refused its write.
func hookGaveUp(err error) bool {
	var appErr *temporal.ApplicationError
	if errors.As(err, &appErr) {
		switch appErr.Type() {
		case ErrTypeStale, ErrTypeRejected, ErrTypeNotFound, ErrTypeInvalid, ErrTypeHeld:
			return false
		}
	}
	return !temporal.IsCanceledError(err)
}

func logHookEnd(log interface{ Info(string, ...any) }, in HookInput, err error) {
	if err != nil && !temporal.IsCanceledError(err) {
		log.Info("hook ended without a write", "item", in.ItemID, "hook", in.HookID, "error", err.Error())
	}
}

func gapError(seq, lastApplied int64) error {
	return fmt.Errorf("gap: event %d arrived but the last applied is %d", seq, lastApplied)
}
