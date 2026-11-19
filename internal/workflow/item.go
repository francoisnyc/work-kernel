package workflow

import (
	"errors"
	"fmt"
	"math"
	"slices"
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
	// ActivityLoadInstant reads the instant of the field the Item's state
	// names in `at`: the one field value the workflow sees.
	ActivityLoadInstant = "LoadInstant"

	// Hooks (Step 6). Each hook activity reads its inputs, calls out, and
	// writes back through the item service itself: the workflow never sees
	// a field value.
	ActivityRunEnrich      = "RunEnrich"      // runs an enrich hook and writes its fields
	ActivityEnrichFallback = "EnrichFallback" // writes an enrich hook's fallback, if it has one
	ActivityRunCheck       = "RunCheck"       // runs a check and resolves its hold
	ActivityResolveHold    = "ResolveHold"    // resolves a hold as error when its check cannot

	// Action hooks (Step 7): a side effect on entering a state, keyed by the
	// entry, never dropped.
	ActivityRunAction  = "RunAction"  // runs an action hook and writes back what it made
	ActivityHookFailed = "HookFailed" // records that an action hook gave up

	// Link reactions (proof of concept, Step 2, part 2). A held reaction
	// decides its hold: RunReaction writes on the target, then resolves the
	// hold (allow, or deny with the target's refusal). ResolveReaction
	// resolves it as error when the reaction gives up. Undo reverses a
	// reaction's committed write; ReactionFailed records an undo that gave
	// up. Each activity writes through the item service itself.
	ActivityRunReaction     = "RunReaction"
	ActivityResolveReaction = "ResolveReaction"
	ActivityUndo            = "Undo"
	ActivityReactionFailed  = "ReactionFailed"

	// Error types by which the activities report the item service's final answers.
	// All are non-retryable.
	ErrTypeStale      = "Stale"      // the Item moved on; the workflow is behind
	ErrTypeRejected   = "Rejected"   // the Type's rules refuse the move
	ErrTypeNotFound   = "NotFound"   // the Item does not exist (any more)
	ErrTypeInvalid    = "Invalid"    // the request is malformed; the same request can never succeed
	ErrTypeHeld       = "Held"       // an open hold refuses the move; for a timer, final
	ErrTypeHookFailed = "HookFailed" // the hook failed for good (unknown customer, bad input)
	// ErrTypeNotYet: a timer at an instant fired before the instant, by
	// the item service's clock. Its details hold the remainder (a
	// time.Duration); the workflow arms again for it.
	ErrTypeNotYet = "NotYet"
	// ErrTypeKeyConflict: an action ran again for the same key and got a
	// different result (a second invoice), or the connector saw the key with
	// a different request. A duplicate, never "already written".
	ErrTypeKeyConflict = "KeyConflict"
)

// versionTimerWaitsForHold is the workflow.GetVersion change ID of Step 2,
// part 3: a state timer due during a hold waits for it to close, and a Held
// answer is not final. Version 1 is that; DefaultVersion is the old rule
// (fire anyway; Held spends the timer), which replays recorded before it
// keep.
const versionTimerWaitsForHold = "timer-waits-for-hold"

// versionCanceledHoldNotAllowed is the GetVersion change ID of the
// consolidation's item 5: an event that cancels a hold (Event.Canceled) is
// never read as the hold allowed. DefaultVersion: the old reading
// (To == RequestedTo), which no recorded event with Canceled can reach.
const versionCanceledHoldNotAllowed = "canceled-hold-not-allowed"

// MaxRetryInterval caps the pause between two attempts of an activity. A
// hook gives up when its next attempt cannot start within its budget, so it
// may end up to this long before the budget runs out.
const MaxRetryInterval = time.Minute

// DefaultHookBudget is how long a hook may keep retrying before it gives up:
// an enrich hook then writes its fallback, a check resolves its hold as error.
const DefaultHookBudget = 15 * time.Minute

// TimerSpec is one state's timer, reduced to what the workflow needs.
type TimerSpec struct {
	After time.Duration
	To    string
}

// AtSpec is one state's timer at a field's instant, reduced to what the
// workflow needs. Field is an ID, for the events that name it.
type AtSpec struct {
	Field string
	To    string
}

// Instant is LoadInstant's answer: the instant of the field the Item's
// state names in `at`, unset or not.
type Instant struct {
	Set bool
	At  time.Time
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
	// HookAttemptTimeout bounds one attempt of a hook (StartToClose); zero
	// means min(30 s, budget). Set by tests and the bench only.
	HookAttemptTimeout time.Duration
	// Actions maps a state to its action hooks, in the Type's order.
	Actions map[string][]string

	// PendingEnrich are the enrich hooks of the current state entry not yet
	// started; they are caused by EnteredSeq. Hold is the Item's open hold,
	// if any. LoadItem fills both, because a run started by the very event
	// that needs a hook loads that event as already applied.
	PendingEnrich []string
	Hold          *OpenHold
	// PendingActions are action hooks not yet started, for any entry of
	// their state: LoadItem reports those not yet completed, and entering a
	// state adds its actions. Leaving the state never drops one.
	PendingActions []PendingAction

	// Link reactions, reduced like Checks. HeldReactions maps "from->to" to
	// the held reaction deciding that transition; Undos maps a held
	// reaction to the reaction that undoes it; UndoOnEntry maps "from->to"
	// to the undo an Item entering `to` from `from` runs.
	HeldReactions map[string]string
	Undos         map[string]string
	UndoOnEntry   map[string]string
	// EnteredHold is the hold whose allowed resolution entered State, ""
	// if none: leaving State by an undo's trigger reverses that hold's
	// reservation. PendingUndos are undos not yet started; LoadItem reports
	// those not yet done, and closing a reaction's hold non-allow (or
	// leaving its state by the trigger) adds one. Nothing drops one.
	EnteredHold  string
	PendingUndos []PendingUndo

	// Acting timers (Step 2, part 3), reduced like Timers: per state, its
	// acting timers. ActingRuns counts, per acting timer of State, the runs
	// already started for the current entry (LoadItem: completed); entering
	// a state resets it. ReactionHooks maps an undo reaction to the action
	// hooks its reaction_applied event runs.
	Acting        map[string][]ActingSpec
	ActingRuns    map[string]int
	ReactionHooks map[string][]string
	// DevDay is the length of a day the Type's durations were scaled to
	// (zero: real time), recorded so the history shows the clock it ran on.
	DevDay time.Duration

	// At maps each state with a timer at a field's instant to it (never
	// scaled by DevDay: an instant is not a duration). AtInstant is the
	// instant last read for the current state (LoadInstant), nil when the
	// field is unset; AtLoaded says it was read, so a run that continues
	// as new does not read it again.
	At        map[string]AtSpec
	AtInstant *time.Time
	AtLoaded  bool
}

// ActingSpec is one acting timer, reduced to what the workflow needs (its
// durations already scaled when DevDay is set).
type ActingSpec struct {
	ID    string
	After time.Duration
	Every time.Duration
	Max   int
	Hook  string
}

// PendingUndo is one undo to run: the undo reaction, the hold whose
// reservation it reverses, and the event that asked for it. Attempts and
// LastError say how it has fared so far; a run that continues as new
// carries them, so the count and the backoff go on where they were.
type PendingUndo struct {
	ReactionID    string
	HoldID        string
	CauseSeq      int64
	CorrelationID string
	Hop           int // of the cause event
	Attempts      int
	LastError     string
}

// An undo is never dropped: a failure that may pass is retried without
// limit, the pause doubling from one second up to UndoMaxInterval. Only a
// refusal (non-retryable Rejected) ends it, recorded with ReactionFailed.
const UndoMaxInterval = time.Minute

// QueryUndos is the query that shows the run's undos not yet done, with
// their attempts and last error, so a stuck refund is visible (an alert is
// Later). It answers []UndoStatus.
const QueryUndos = "undos"

// UndoStatus is one undo not yet done, as QueryUndos reports it. LastError
// is the last failure, at most MaxUndoErrorLen bytes, empty before the
// first.
type UndoStatus struct {
	ReactionID string
	HoldID     string
	CauseSeq   int64
	Attempts   int
	LastError  string
}

// MaxUndoErrorLen bounds UndoStatus.LastError.
const MaxUndoErrorLen = 200

// UndoAttemptsPerRun bounds the history a stuck undo adds to one run (each
// attempt is an activity and a timer). After this many undo attempts the
// run continues as new, carrying its pending undos with their attempts and
// last errors.
const UndoAttemptsPerRun = 100

// ReactionInput is what RunReaction, ResolveReaction and Undo need: the
// reaction, the hold, and the cause. Opaque IDs only. The item service
// derives the keys itself.
type ReactionInput struct {
	ItemID        string // the source
	TypeID        string
	TypeVersion   int
	ReactionID    string
	HoldID        string
	CauseSeq      int64 // RunReaction: the request; Undo: the event that closed the hold or left the state
	CorrelationID string
	Hop           int // the cause's hop + 1
}

// ResolveReactionInput resolves a held reaction's hold as error.
type ResolveReactionInput struct {
	Reaction ReactionInput
	Reason   string // budget_exhausted or reaction_failed
}

// ReactionFailedInput records that an undo gave up.
type ReactionFailedInput struct {
	Reaction ReactionInput
	Reason   string // the refusal's code
}

// PendingAction is one action hook to run for one entry of its state.
type PendingAction struct {
	HookID        string
	CauseSeq      int64 // the event that entered the state
	CorrelationID string
	Hop           int // of that event
}

// HookSpec is one enrich hook, reduced to what the workflow needs.
type HookSpec struct {
	ID          string
	HasFallback bool
}

// OpenHold is an open hold: which check to run, caused by which request.
type OpenHold struct {
	ID            string
	Hook          string // the check hook, or the held reaction (Reaction)
	Reaction      bool
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
	// Timer and Run: the acting timer's run that runs an action hook
	// (Step 2, part 3); empty and 0 otherwise.
	Timer string
	Run   int
	// IdempotencyKey: hook:<item>:<cause seq>:<hook> for an enrich hook (its
	// own write and its fallback share it), hold:<hold> for a check (its
	// resolution and the error resolution share it). Either way, one write.
	IdempotencyKey string
}

// HookFailedInput records that an action hook gave up.
type HookFailedInput struct {
	Hook   HookInput
	Reason string // budget_exhausted, key_conflict or action_failed
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
	// Reaction: a reaction_applied event's reaction (Step 2, part 3).
	Reaction string
	// FieldIDs are the fields the event set or cleared, among those a state
	// of the Item's Type names in `at`: IDs only, sorted; nil for every
	// other Type and event. The workflow reads them to re-arm a timer at a
	// field's instant.
	FieldIDs []string
	// Canceled: the event closes HoldID by canceling it (an escape,
	// canceled_hold_id), never by allowing it (consolidation, item 5).
	Canceled bool
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
		MaximumInterval:    MaxRetryInterval,
		MaximumAttempts:    0,
	},
}

// hookOptions bound a hook: retried like any activity, but only within its
// budget (ScheduleToClose counts every attempt and every pause).
//
// The budget is the only bound, so attempts are unlimited. That is said as
// math.MaxInt32 rather than 0: Temporal's test environment turns 0 into 10
// attempts, which would end a hook before its budget in unit tests.
func hookOptions(budget, attempt time.Duration) workflow.ActivityOptions {
	opts := activityOptions
	retry := *activityOptions.RetryPolicy // a copy: the shared policy stays as it is
	retry.MaximumAttempts = math.MaxInt32
	opts.RetryPolicy = &retry
	opts.ScheduleToCloseTimeout = budget
	opts.StartToCloseTimeout = min(30*time.Second, budget)
	if attempt > 0 {
		opts.StartToCloseTimeout = min(attempt, budget)
	}
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

		// Undos (Step 2, part 2). undoing marks the undos a coroutine is
		// working on, by undoKey; undoAttempts counts this run's attempts,
		// and undoCapped says the run reached UndoAttemptsPerRun and should
		// continue as new once the coroutines have stopped.
		undoing      = map[string]bool{}
		undoAttempts int
		undoCapped   bool

		// Acting timers (Step 2, part 3). Each acting timer of the current
		// entry has a scheduler coroutine (actingLive counts them) that
		// entering a state cancels (cancelActing); actingStarted says the
		// current entry's schedulers were started.
		actingLive    int
		cancelActing  workflow.CancelFunc
		actingStarted bool

		// A timer at a field's instant (Jira part 2, step 1). atEpoch
		// counts the reasons to read the instant again (a state entered, an
		// event naming the field), so that a read which an event overtook
		// is not taken for current. atNotBefore is set when the item
		// service answered NotYet: no firing before that moment.
		atEpoch     int
		atNotBefore time.Time
	)
	// news gets one token per applied event, so the at-timer's selector can
	// wait for "the timer, or an event". Buffered and sent without
	// blocking: an update handler never waits on the main loop. A channel
	// of the SDK, not a Go channel: it is replayed like everything else.
	news := workflow.NewBufferedChannel(ctx, 1)

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
		news.SendAsync(true)
		// The event names the field the current state's timer hangs on:
		// the instant is read again, and a timer the item service had
		// refused is armed again. (An entry into a state is handled below.)
		if at, ok := s.At[s.State]; ok && ev.To == "" && slices.Contains(ev.FieldIDs, at.Field) {
			s.AtLoaded, s.AtInstant, s.TimerDone = false, nil, false
			atNotBefore = time.Time{}
			atEpoch++
		}
		enteredBy := "" // the hold whose allowed resolution this event is
		switch {
		case ev.Op == lifecycle.OpTransitionRequested:
			move := ev.From + "->" + ev.RequestedTo
			s.Hold = &OpenHold{
				ID: ev.HoldID, Hook: s.Checks[move],
				From: ev.From, RequestedTo: ev.RequestedTo, RequestedSeq: ev.Seq,
				CorrelationID: ev.CorrelationID, Hop: ev.Hop,
			}
			if r, ok := s.HeldReactions[move]; ok && s.Hold.Hook == "" {
				s.Hold.Hook, s.Hold.Reaction = r, true
			}
			checkStarted = false
		case ev.HoldID != "" && s.Hold != nil && ev.HoldID == s.Hold.ID:
			// The hold is closed (resolved, or canceled by an escape). A
			// check still running has nothing left to decide.
			allowed := ev.Op == "transition" && ev.To == s.Hold.RequestedTo
			// An escape canceled the hold: never allowed, whatever state it
			// entered. Asked only of such an event, which no history
			// recorded before the change carries.
			if ev.Canceled && workflow.GetVersion(ctx, versionCanceledHoldNotAllowed, workflow.DefaultVersion, 1) >= 1 {
				allowed = false
			}
			if allowed {
				enteredBy = s.Hold.ID
			} else if undo := s.Undos[s.Hold.Hook]; s.Hold.Reaction && undo != "" {
				// Closed any other way than allowed: what the reaction may
				// have taken is given back (a no-op if it took nothing).
				s.PendingUndos = append(s.PendingUndos, PendingUndo{
					ReactionID: undo, HoldID: s.Hold.ID, CauseSeq: ev.Seq,
					CorrelationID: ev.CorrelationID, Hop: ev.Hop,
				})
			}
			s.Hold = nil
			if cancelCheck != nil {
				cancelCheck()
				cancelCheck = nil
			}
		case ev.Op == lifecycle.OpReactionApplied:
			// An undo gave something back: its action hooks (the refund
			// email) run, caused by this event, whatever the state.
			for _, id := range s.ReactionHooks[ev.Reaction] {
				s.PendingActions = append(s.PendingActions, PendingAction{
					HookID: id, CauseSeq: ev.Seq, CorrelationID: ev.CorrelationID, Hop: ev.Hop,
				})
			}
		}
		if ev.To != "" {
			// Leaving the state stops its acting timers; a new entry counts
			// its runs from the start.
			if cancelActing != nil {
				cancelActing()
				cancelActing = nil
			}
			s.ActingRuns, actingStarted = nil, false
			// Leaving the state by an undo's trigger gives back what the
			// hold that entered it took.
			if undo := s.UndoOnEntry[ev.From+"->"+ev.To]; undo != "" && s.EnteredHold != "" {
				s.PendingUndos = append(s.PendingUndos, PendingUndo{
					ReactionID: undo, HoldID: s.EnteredHold, CauseSeq: ev.Seq,
					CorrelationID: ev.CorrelationID, Hop: ev.Hop,
				})
			}
			s.EnteredHold = enteredBy
			s.State = ev.To
			s.EnteredSeq, s.EnteredAt = ev.Seq, ev.At
			s.CorrelationID, s.Hop = ev.CorrelationID, ev.Hop
			s.TimerDone = false
			// A timer at a field's instant belongs to the state: the new
			// state's, if it has one, is read afresh.
			s.AtLoaded, s.AtInstant = false, nil
			atNotBefore = time.Time{}
			atEpoch++
			// The new state's enrich hooks. Those of a state already left
			// and not yet started are dropped: the check is the backstop.
			s.PendingEnrich = nil
			for _, h := range s.Enrich[ev.To] {
				s.PendingEnrich = append(s.PendingEnrich, h.ID)
			}
			// Its action hooks are added, never replaced: an action of a
			// state already left still runs, for the entry that caused it.
			for _, id := range s.Actions[ev.To] {
				s.PendingActions = append(s.PendingActions, PendingAction{
					HookID: id, CauseSeq: ev.Seq, CorrelationID: ev.CorrelationID, Hop: ev.Hop,
				})
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
	// The undos not yet done, with their attempts and last errors, so a
	// stuck refund is visible. A query adds nothing to the history.
	if err := workflow.SetQueryHandler(ctx, QueryUndos, func() ([]UndoStatus, error) {
		out := make([]UndoStatus, 0, len(s.PendingUndos))
		for _, u := range s.PendingUndos {
			out = append(out, UndoStatus{ReactionID: u.ReactionID, HoldID: u.HoldID, CauseSeq: u.CauseSeq,
				Attempts: u.Attempts, LastError: u.LastError})
		}
		return out, nil
	}); err != nil {
		return err
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
			err := workflow.ExecuteActivity(workflow.WithActivityOptions(gctx, hookOptions(budget, s.HookAttemptTimeout)),
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

	reactionInput := func(reaction, holdID string, causeSeq int64, corr string, hop int) ReactionInput {
		return ReactionInput{
			ItemID: in.ItemID, TypeID: s.TypeID, TypeVersion: s.TypeVersion, ReactionID: reaction,
			HoldID: holdID, CauseSeq: causeSeq, CorrelationID: corr, Hop: hop,
		}
	}

	// startReaction runs a held reaction for the open hold, like a check:
	// within the hook budget, called off when the hold closes. The activity
	// resolves the hold itself; when the reaction gives up, the hold
	// resolves as error (budget_exhausted, or reaction_failed when it
	// failed for good).
	startReaction := func(h OpenHold) {
		rin := reactionInput(h.Hook, h.ID, h.RequestedSeq, h.CorrelationID, h.Hop+1)
		cctx, cancel := workflow.WithCancel(ctx)
		cancelCheck = cancel
		running++
		workflow.Go(cctx, func(gctx workflow.Context) {
			defer func() { running--; wake++ }()
			err := workflow.ExecuteActivity(workflow.WithActivityOptions(gctx, hookOptions(budget, s.HookAttemptTimeout)),
				ActivityRunReaction, rin).Get(gctx, nil)
			if err == nil || gctx.Err() != nil || !reactionGaveUp(err) {
				return
			}
			reason := "budget_exhausted"
			var appErr *temporal.ApplicationError
			if errors.As(err, &appErr) && appErr.NonRetryable() {
				reason = "reaction_failed"
			}
			_ = workflow.ExecuteActivity(gctx, ActivityResolveReaction,
				ResolveReactionInput{Reaction: rin, Reason: reason}).Get(gctx, nil)
		})
	}

	// startUndo runs one pending undo until it is done, in a coroutine
	// nothing cancels: a refund must happen. Each attempt is one activity
	// try; a failure that may pass is counted, kept as LastError, and tried
	// again after a pause that doubles from a second up to UndoMaxInterval,
	// without limit. A refusal is recorded once (ReactionFailed) and ends
	// it. After UndoAttemptsPerRun attempts in this run the coroutine stops
	// and the run continues as new, carrying the undo as it stands.
	startUndo := func(key string) {
		undoing[key] = true
		running++
		workflow.Go(ctx, func(gctx workflow.Context) {
			defer func() { running--; wake++; delete(undoing, key) }()
			// The first attempt of a run goes at once; then the pause
			// follows the undo's attempts, carried across runs.
			for first := true; ; first = false {
				i := slices.IndexFunc(s.PendingUndos, func(u PendingUndo) bool { return undoKey(u) == key })
				if i < 0 {
					return
				}
				u := s.PendingUndos[i]
				if !first && u.Attempts > 0 {
					pause := min(time.Second<<min(u.Attempts-1, 16), UndoMaxInterval)
					if err := workflow.Sleep(gctx, pause); err != nil {
						return
					}
				}
				if undoCapped {
					return
				}
				rin := reactionInput(u.ReactionID, u.HoldID, u.CauseSeq, u.CorrelationID, u.Hop+1)
				err := workflow.ExecuteActivity(workflow.WithActivityOptions(gctx, undoOptions), ActivityUndo, rin).Get(gctx, nil)
				undoAttempts++
				if done, reason := undoFinished(err); done {
					if reason != "" {
						_ = workflow.ExecuteActivity(gctx, ActivityReactionFailed,
							ReactionFailedInput{Reaction: rin, Reason: reason}).Get(gctx, nil)
					}
					s.PendingUndos = slices.DeleteFunc(s.PendingUndos, func(p PendingUndo) bool { return undoKey(p) == key })
					return
				}
				j := slices.IndexFunc(s.PendingUndos, func(p PendingUndo) bool { return undoKey(p) == key })
				s.PendingUndos[j].Attempts++
				s.PendingUndos[j].LastError = undoError(err)
				if undoAttempts >= UndoAttemptsPerRun {
					undoCapped = true
					return
				}
			}
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
		if h.Reaction {
			startReaction(h)
			return
		}
		hin := hookInput(h.Hook, h.RequestedSeq, h.CorrelationID, h.Hop+1)
		hin.HoldID, hin.IdempotencyKey = h.ID, "hold:"+h.ID
		cctx, cancel := workflow.WithCancel(ctx)
		cancelCheck = cancel
		running++
		workflow.Go(cctx, func(gctx workflow.Context) {
			defer func() { running--; wake++ }()
			err := workflow.ExecuteActivity(workflow.WithActivityOptions(gctx, hookOptions(budget, s.HookAttemptTimeout)),
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

	// runAction runs one action hook in a coroutine, for one entry of its
	// state (startAction), or one acting timer's run. Nothing cancels it: once the state was entered, the action runs
	// to completion. If it cannot complete, it records hook_failed with the
	// reason, through the item service, so the failure is visible on the Item.
	runAction := func(hin HookInput) {
		running++
		workflow.Go(ctx, func(gctx workflow.Context) {
			defer func() { running--; wake++ }()
			err := workflow.ExecuteActivity(workflow.WithActivityOptions(gctx, hookOptions(budget, s.HookAttemptTimeout)),
				ActivityRunAction, hin).Get(gctx, nil)
			reason, failed := actionFailure(err)
			if !failed {
				logHookEnd(log, hin, err)
				return
			}
			err = workflow.ExecuteActivity(gctx, ActivityHookFailed, HookFailedInput{Hook: hin, Reason: reason}).Get(gctx, nil)
			logHookEnd(log, hin, err)
		})
	}
	startAction := func(pa PendingAction) {
		runAction(hookInput(pa.HookID, pa.CauseSeq, pa.CorrelationID, pa.Hop+1))
	}

	// startActing runs one acting timer of the current entry in a
	// coroutine that leaving the state cancels: run n is due After the
	// entering event, then Every, Max runs in all (s.ActingRuns counts
	// those started). An overdue run goes at once, and the next is due
	// Every after it: never a burst. Each run is an action (runAction), which
	// leaving the state does not cancel; it does not wait for a hold.
	startActing := func(actx workflow.Context, spec ActingSpec) {
		entered, enteredAt, corr, hop := s.EnteredSeq, s.EnteredAt, s.CorrelationID, s.Hop
		actingLive++
		workflow.Go(actx, func(gctx workflow.Context) {
			defer func() { actingLive--; wake++ }()
			var last time.Time // the previous run of this run's, if any
			for gctx.Err() == nil && s.ActingRuns[spec.ID] < spec.Max {
				n := s.ActingRuns[spec.ID] + 1
				due := enteredAt.Add(spec.After + time.Duration(n-1)*spec.Every)
				if next := last.Add(spec.Every); !last.IsZero() && next.After(due) {
					due = next
				}
				if wait := due.Sub(workflow.Now(gctx)); wait > 0 {
					if err := workflow.Sleep(gctx, wait); err != nil {
						return // the state was left
					}
				}
				if gctx.Err() != nil {
					return
				}
				if s.ActingRuns == nil {
					s.ActingRuns = map[string]int{}
				}
				s.ActingRuns[spec.ID] = n
				last = workflow.Now(gctx)
				hin := hookInput(spec.Hook, entered, corr, hop+1)
				hin.Timer, hin.Run = spec.ID, n
				hin.IdempotencyKey = fmt.Sprintf("timer:%s:%s:%d:%d", in.ItemID, spec.ID, entered, n)
				runAction(hin)
			}
		})
	}

	handlersDone := func() bool { return workflow.AllHandlersFinished(ctx) }
	busy := func() bool {
		return running > 0 || s.Hold != nil || len(s.PendingEnrich) > 0 || len(s.PendingActions) > 0 ||
			len(s.PendingUndos) > 0 || actingLive > 0
	}

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
			actions := s.PendingActions
			s.PendingActions = nil
			for _, pa := range actions {
				startAction(pa)
			}
			if !undoCapped {
				for _, u := range s.PendingUndos {
					if !undoing[undoKey(u)] {
						startUndo(undoKey(u))
					}
				}
			}
			if !actingStarted {
				actingStarted = true
				if specs := s.Acting[s.State]; len(specs) > 0 {
					actx, cancel := workflow.WithCancel(ctx)
					cancelActing = cancel
					for _, spec := range specs {
						startActing(actx, spec)
					}
				}
			}
		}

		// A stuck undo has added UndoAttemptsPerRun attempts to this run:
		// hand over to a fresh one, carrying the undos (their attempts and
		// last errors), once nothing runs and no update is half-handled.
		if undoCapped && running == 0 {
			if err := workflow.Await(ctx, handlersDone); err != nil {
				return err
			}
			carried := s
			return workflow.NewContinueAsNewError(ctx, Name, Input{ItemID: in.ItemID, Carried: &carried})
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

		// A timer at a field's instant (Jira part 2, step 1). Only a Type
		// that declares one has an At table, so every other run never
		// enters this branch and issues the commands it always did. A state
		// has `at` or `timer`, never both: every path below goes round the
		// loop again or ends the run.
		if at, hasAt := s.At[s.State]; hasAt && !gone {
			seen, entry := changes, entries
			changed := func() bool { return changes != seen }

			// Read the instant: once on entering the state (or starting in
			// it), and again after an event that names its field. It is the
			// one field value this workflow sees. If an event overtook the
			// read (atEpoch moved), the answer may be old: read again.
			if !s.AtLoaded {
				epoch := atEpoch
				var inst Instant
				err := workflow.ExecuteActivity(ctx, ActivityLoadInstant, in.ItemID).Get(ctx, &inst)
				var appErr *temporal.ApplicationError
				switch {
				case errors.As(err, &appErr) && appErr.Type() == ErrTypeNotFound:
					log.Info("the item no longer exists; nothing to wait for", "item", in.ItemID)
					gone = true
				case err != nil:
					return err // only a canceled workflow gets here
				case epoch == atEpoch:
					s.AtLoaded, s.AtInstant = true, nil
					if inst.Set {
						t := inst.At
						s.AtInstant = &t
					}
				}
				continue
			}

			// Nothing armed (the field is unset), or the timer has had its
			// turn: wait for news. The run stays, to hear the field change.
			if s.AtInstant == nil || s.TimerDone {
				woke := wake
				if err := workflow.Await(ctx, func() bool { return wake != woke }); err != nil {
					return err
				}
				continue
			}

			// Wait until the instant, or until an event changes the
			// picture. The time left is the instant minus workflow.Now, the
			// replay-safe clock: never the machine's own. The timer lives
			// in a context of its own, so re-arming is: cancel that
			// context (the server drops the timer), go round, start a new
			// one. The selector waits for the timer or for an event; which
			// of the two it reports does not matter, because an event
			// always wins below (changed), whatever fired with it.
			due := *s.AtInstant
			if atNotBefore.After(due) {
				due = atNotBefore
			}
			if wait := due.Sub(workflow.Now(ctx)); wait > 0 {
				for news.ReceiveAsync(nil) { // tokens of events already taken into account
				}
				timerCtx, cancelTimer := workflow.WithCancel(ctx)
				sel := workflow.NewSelector(ctx)
				sel.AddFuture(workflow.NewTimer(timerCtx, wait), func(workflow.Future) {})
				sel.AddReceive(news, func(c workflow.ReceiveChannel, _ bool) { c.Receive(ctx, nil) })
				sel.Select(ctx)
				cancelTimer() // a no-op once it fired; else the old timer is gone
				if err := ctx.Err(); err != nil {
					return err
				}
				if changed() {
					continue
				}
			}

			// Due while a hold is open: wait for the hold to close (or
			// anything else to happen), then look again, as a duration
			// timer does.
			if s.Hold != nil {
				if err := workflow.Await(ctx, changed); err != nil {
					return err
				}
				continue
			}

			req := FireTimerInput{
				ItemID: in.ItemID, TypeID: s.TypeID, TypeVersion: s.TypeVersion,
				FromState: s.State, ToState: at.To,
				ExpectedItemSeq: s.LastAppliedSeq, EnteredSeq: s.EnteredSeq,
				CorrelationID: s.CorrelationID, Hop: s.Hop + 1,
				IdempotencyKey: fmt.Sprintf("timer:%s:%d:%d", in.ItemID, s.EnteredSeq, s.LastAppliedSeq),
			}
			err := workflow.ExecuteActivity(ctx, ActivityFireTimer, req).Get(ctx, nil)
			var appErr *temporal.ApplicationError
			switch {
			case err == nil:
				// The move is written; its event will arrive like any other.
			case errors.As(err, &appErr) && appErr.Type() == ErrTypeNotYet:
				// The item service's clock has not reached the instant: it
				// wrote nothing and says how long remains. Arm again for
				// that long (unless an event moves the instant meanwhile).
				var remaining time.Duration
				if appErr.Details(&remaining) != nil || remaining <= 0 {
					remaining = time.Second
				}
				if !changed() {
					atNotBefore = workflow.Now(ctx).Add(remaining)
				}
				continue
			case errors.As(err, &appErr) && (appErr.Type() == ErrTypeStale || appErr.Type() == ErrTypeHeld):
				// The item service is ahead of this run (an event is on its
				// way), or a hold it has not heard of is open: wait for the
				// next event, then look again.
				if err := workflow.Await(ctx, changed); err != nil {
					return err
				}
				continue
			case errors.As(err, &appErr) && (appErr.Type() == ErrTypeRejected || appErr.Type() == ErrTypeNotFound ||
				appErr.Type() == ErrTypeInvalid):
				// Final: the timer is spent, until the field changes.
				log.Info("the item service refused a timer's move",
					"item", in.ItemID, "from", req.FromState, "to", req.ToState, "reason", appErr.Message())
			default:
				return err // only a canceled workflow gets here
			}
			if entries == entry && !changed() {
				s.TimerDone = true
			}
			continue
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

		// Due while a hold is open: wait for the hold to close (or anything
		// else to happen), then look again: if the Item left the state,
		// this timer is gone; if not, it fires at once. Versioned: a run
		// recorded before it fired at once, and was refused as held.
		if s.Hold != nil && workflow.GetVersion(ctx, versionTimerWaitsForHold, workflow.DefaultVersion, 1) >= 1 {
			if err := workflow.Await(ctx, changed); err != nil {
				return err
			}
			continue
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
		case errors.As(err, &appErr) && appErr.Type() == ErrTypeHeld &&
			workflow.GetVersion(ctx, versionTimerWaitsForHold, workflow.DefaultVersion, 1) >= 1:
			// A hold this run has not heard of yet: like stale, wait for
			// the next event (its request, then its closing), then look again.
			if err := workflow.Await(ctx, changed); err != nil {
				return err
			}
			continue
		case errors.As(err, &appErr) && (appErr.Type() == ErrTypeRejected || appErr.Type() == ErrTypeNotFound ||
			appErr.Type() == ErrTypeInvalid || appErr.Type() == ErrTypeHeld):
			// Final either way: the rules say no, the Item is gone, the
			// request is malformed (a bug, which retrying cannot fix), or (in
			// a run recorded before versionTimerWaitsForHold) an open hold
			// refuses the move, and the held timer is spent.
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

// reactionGaveUp says whether a held reaction could not decide its hold:
// its budget ran out, or it failed for good (the hold then resolves as
// error, so it does not stay open). Only the Item being gone, or the
// reaction being called off, leaves nothing to resolve.
func reactionGaveUp(err error) bool {
	var appErr *temporal.ApplicationError
	if errors.As(err, &appErr) && appErr.Type() == ErrTypeNotFound {
		return false
	}
	return !temporal.IsCanceledError(err)
}

// actionFailure says whether an action hook's error should be recorded as
// hook_failed, and with which reason. Its budget ran out or it failed for
// good: yes. The Item is gone, or the workflow was canceled: there is
// nothing to record on.
func actionFailure(err error) (reason string, failed bool) {
	if err == nil || temporal.IsCanceledError(err) {
		return "", false
	}
	var appErr *temporal.ApplicationError
	if errors.As(err, &appErr) {
		switch appErr.Type() {
		case ErrTypeNotFound:
			return "", false
		case ErrTypeKeyConflict:
			return "key_conflict", true
		case ErrTypeHookFailed, ErrTypeRejected, ErrTypeInvalid, ErrTypeStale, ErrTypeHeld:
			// Failed for good, or the kernel refused the write-back:
			// retrying the same request cannot help.
			return "action_failed", true
		}
	}
	return "budget_exhausted", true
}

func logHookEnd(log interface{ Info(string, ...any) }, in HookInput, err error) {
	if err != nil && !temporal.IsCanceledError(err) {
		log.Info("hook ended without a write", "item", in.ItemID, "hook", in.HookID, "error", err.Error())
	}
}

func gapError(seq, lastApplied int64) error {
	return fmt.Errorf("gap: event %d arrived but the last applied is %d", seq, lastApplied)
}

// undoOptions: one try per activity call; the undo's own loop retries, so
// it can count attempts and keep the last error (startUndo).
var undoOptions = workflow.ActivityOptions{
	StartToCloseTimeout: 30 * time.Second,
	RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 1},
}

func undoKey(u PendingUndo) string { return u.HoldID + "/" + u.ReactionID }

// undoFinished says whether an undo attempt ends the undo, and the code to
// record with ReactionFailed when it ends refused. Done (nothing left to
// give back, or given back) ends it with no code; the Item gone ends it
// too, with nothing to record on; a refusal (non-retryable Rejected or
// Invalid) ends it with its code. Anything else may pass: try again.
func undoFinished(err error) (done bool, reason string) {
	if err == nil {
		return true, ""
	}
	var appErr *temporal.ApplicationError
	if !errors.As(err, &appErr) || !appErr.NonRetryable() {
		return false, ""
	}
	switch appErr.Type() {
	case ErrTypeNotFound:
		return true, ""
	case ErrTypeRejected, ErrTypeInvalid:
		reason = "reaction_failed"
		var code string
		if appErr.HasDetails() && appErr.Details(&code) == nil && code != "" {
			reason = code
		}
		return true, reason
	}
	return false, ""
}

// undoError is an attempt's failure as the query shows it: its message,
// cut to MaxUndoErrorLen bytes. Kernel errors name IDs and codes, never
// field values.
func undoError(err error) string {
	msg := err.Error()
	if len(msg) > MaxUndoErrorLen {
		msg = msg[:MaxUndoErrorLen]
	}
	return msg
}
