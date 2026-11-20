package workflow

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"github.com/francoisnyc/work-kernel/internal/hooks"
	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
	"github.com/francoisnyc/work-kernel/internal/lifecycle"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// timerActor is the principal a timer's write is checked and recorded as.
const timerActor = "system:timer"

// Activities is the I/O side of the workflow: ordinary Go, run by the worker,
// retried by Temporal when it fails. Its methods are the activities.
type Activities struct {
	Items      *itemservice.Service
	Connectors *hooks.Connectors // the outside world the hooks call; nil when no Type has hooks
	HookBudget time.Duration     // zero means DefaultHookBudget
	// HookAttemptTimeout bounds one attempt of a hook; zero means the
	// default. Tests and the bench only, to force timeouts.
	HookAttemptTimeout time.Duration
	// DevDay (dev only, Step 2, part 3) is the length of a day for the
	// durations a Type declares (state timers and acting timers), when the
	// worker runs on a scaled clock; zero is real time. Nothing else is
	// scaled: hook budgets, the undo backoff and every operational timeout
	// keep real time. LoadItem records it in the Snapshot.
	DevDay time.Duration
	// DevTypes are the Types DevDay applies to; every other Type keeps
	// real time. DevDay without DevTypes scales nothing.
	DevTypes []string
}

// LoadItem reads what a new run needs to know about its Item: where it is,
// which event put it there, and the Type's timers as a small table. The
// workflow gets no more of the Type than that.
func (a *Activities) LoadItem(ctx context.Context, itemID string) (Snapshot, error) {
	id, err := uuid.Parse(itemID)
	if err != nil {
		return Snapshot{}, fmt.Errorf("item id %q: %w", itemID, err)
	}
	se, err := a.Items.StateEntry(ctx, id)
	if errors.Is(err, itemservice.ErrNotFound) {
		// Retrying cannot bring the Item back; tell the workflow instead.
		return Snapshot{}, temporal.NewNonRetryableApplicationError(err.Error(), ErrTypeNotFound, err)
	}
	if err != nil {
		return Snapshot{}, err
	}
	ty, err := a.Items.Type(ctx, se.TypeID, se.TypeVersion)
	if err != nil {
		return Snapshot{}, err
	}
	timers := map[string]TimerSpec{}
	enrich := map[string][]HookSpec{}
	actions := map[string][]string{}
	for _, s := range ty.States {
		for _, h := range lifecycle.ActionHooks(ty, s.ID) {
			actions[s.ID] = append(actions[s.ID], h.ID)
		}
		if timer, ok := lifecycle.TimerFor(ty, s.ID); ok {
			timers[s.ID] = TimerSpec{After: timer.Duration, To: timer.To}
		}
		for _, h := range lifecycle.EnrichHooks(ty, s.ID) {
			enrich[s.ID] = append(enrich[s.ID], HookSpec{ID: h.ID, HasFallback: len(h.Fallback) > 0})
		}
	}
	checks := map[string]string{}
	var held, undos, onEntry map[string]string // nil for a Type without reactions, as before them
	for _, tr := range ty.Transitions {
		move := tr.From + "->" + tr.To
		if h, ok := lifecycle.CheckFor(ty, tr.From, tr.To); ok {
			checks[move] = h.ID
		}
		if r, ok := lifecycle.HeldReaction(ty, tr.From, tr.To); ok {
			if held == nil {
				held = map[string]string{}
			}
			held[move] = r.ID
		}
		if r, ok := lifecycle.UndoOnEntry(ty, tr.From, tr.To); ok {
			if onEntry == nil {
				onEntry = map[string]string{}
			}
			onEntry[move] = r.ID
		}
	}
	for _, r := range ty.Reactions {
		if u, ok := lifecycle.UndoOf(ty, r.ID); ok {
			if undos == nil {
				undos = map[string]string{}
			}
			undos[r.ID] = u.ID
		}
	}
	// The current entry's enrich hooks that have not written yet. A hook
	// that ran and wrote nothing (no fallback) runs again; its key makes any
	// second write a no-op.
	var pending []string
	for _, h := range enrich[se.State] {
		if !slices.Contains(se.HooksRun, h.ID) {
			pending = append(pending, h.ID)
		}
	}
	snap := Snapshot{
		TypeID: se.TypeID, TypeVersion: se.TypeVersion, Timers: timers,
		State: se.State, LastAppliedSeq: se.ItemSeq,
		EnteredSeq: se.EnteredSeq, EnteredAt: se.EnteredAt,
		CorrelationID: se.CorrelationID, Hop: se.Hop,
		Enrich: enrich, Checks: checks, HookBudget: a.HookBudget, PendingEnrich: pending,
		HookAttemptTimeout: a.HookAttemptTimeout, Actions: actions,
		HeldReactions: held, Undos: undos, UndoOnEntry: onEntry,
	}
	if se.EnteredHold != uuid.Nil {
		snap.EnteredHold = se.EnteredHold.String()
	}
	// Timers at a field's instant: the state, the field's ID and the target
	// state. Never scaled by the dev clock: an instant is not a duration.
	// The instant itself is LoadInstant's to read.
	for _, s := range ty.States {
		if at, ok := lifecycle.AtTimerFor(ty, s.ID); ok {
			if snap.At == nil {
				snap.At = map[string]AtSpec{}
			}
			snap.At[s.ID] = AtSpec{Field: at.Field, To: at.To}
		}
	}
	// Step 2, part 3: acting timers and reaction hooks. On a dev clock that
	// names this Type, its declared durations are scaled (a day lasts
	// DevDay); nothing else is (budgets, backoffs).
	scale := func(d time.Duration) time.Duration { return d }
	if a.DevDay > 0 && slices.Contains(a.DevTypes, ty.ID) {
		snap.DevDay = a.DevDay
		scale = func(d time.Duration) time.Duration {
			return time.Duration(float64(d) * float64(a.DevDay) / float64(24*time.Hour))
		}
		for st, t := range snap.Timers {
			t.After = scale(t.After)
			snap.Timers[st] = t
		}
	}
	for _, st := range ty.States {
		for _, at := range lifecycle.ActingTimers(ty, st.ID) {
			if snap.Acting == nil {
				snap.Acting = map[string][]ActingSpec{}
			}
			snap.Acting[st.ID] = append(snap.Acting[st.ID], ActingSpec{
				ID: at.ID, After: scale(at.AfterDuration), Every: scale(at.EveryDuration), Max: at.Max, Hook: at.Hook,
			})
		}
	}
	if len(se.ActingRuns) > 0 {
		snap.ActingRuns = se.ActingRuns
	}
	// Move and create reactions (Jira part 2, step 2): which a state's
	// entry runs, which a child's creation runs, and those not yet done.
	for _, r := range ty.Reactions {
		if r.Kind != typedef.ReactionMove && r.Kind != typedef.ReactionCreate {
			continue
		}
		if r.On == typedef.OnCreatedInside {
			if snap.InsideReactions == nil {
				snap.InsideReactions = map[string][]string{}
			}
			for _, of := range r.Of {
				snap.InsideReactions[of] = append(snap.InsideReactions[of], r.ID)
			}
			continue
		}
		if snap.EnterReactions == nil {
			snap.EnterReactions = map[string][]EnterReaction{}
		}
		snap.EnterReactions[r.State] = append(snap.EnterReactions[r.State], EnterReaction{ID: r.ID, From: r.From})
	}
	for _, pr := range se.PendingReactions {
		snap.PendingReactions = append(snap.PendingReactions, PendingReaction{
			ReactionID: pr.Reaction, CauseItem: pr.CauseItem.String(), CauseSeq: pr.CauseSeq,
			CorrelationID: pr.CorrelationID, Hop: pr.Hop,
		})
	}
	for _, r := range ty.Reactions {
		for _, h := range lifecycle.ReactionHooks(ty, r.ID) {
			if snap.ReactionHooks == nil {
				snap.ReactionHooks = map[string][]string{}
			}
			snap.ReactionHooks[r.ID] = append(snap.ReactionHooks[r.ID], h.ID)
		}
	}
	for _, u := range se.PendingUndos {
		snap.PendingUndos = append(snap.PendingUndos, PendingUndo{
			ReactionID: u.Reaction, HoldID: u.HoldID.String(), CauseSeq: u.CauseSeq,
			CorrelationID: u.CorrelationID, Hop: u.Hop,
		})
	}
	for _, pa := range se.PendingActions {
		snap.PendingActions = append(snap.PendingActions, PendingAction{
			HookID: pa.Hook, CauseSeq: pa.CauseSeq, CorrelationID: pa.CorrelationID, Hop: pa.Hop,
		})
	}
	if h := se.OpenHold; h != nil {
		_, isReaction := ty.Reaction(h.Hook)
		snap.Hold = &OpenHold{
			ID: h.ID.String(), Hook: h.Hook, Reaction: isReaction, From: h.From, RequestedTo: h.To,
			RequestedSeq: h.RequestedSeq, CorrelationID: h.CorrelationID, Hop: h.Hop,
		}
	}
	return snap, nil
}

// FireTimer makes a timer's move through the item service's normal write path, so
// guards, history and the outbox all apply as for a person's transition. It
// uses the internal door: only the kernel may act as system:timer.
//
// The item service's final answers (stale, rejected, not found, invalid) are
// returned as non-retryable errors, which the workflow tells apart by type.
// Anything else (the database is away, a timeout) is returned as is, and
// Temporal retries it.
func (a *Activities) FireTimer(ctx context.Context, in FireTimerInput) error {
	id, err := uuid.Parse(in.ItemID)
	if err != nil {
		return fmt.Errorf("item id %q: %w", in.ItemID, err)
	}
	_, err = a.Items.Internal().Transition(ctx, itemservice.TransitionRequest{
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
	var notYet *itemservice.NotYetError
	switch {
	case errors.As(err, &notYet):
		// A timer at an instant fired before the instant, by the item
		// service's clock. Nothing was written; the remainder travels as
		// the error's detail, and the workflow arms again for it.
		return temporal.NewNonRetryableApplicationError(err.Error(), ErrTypeNotYet, err, notYet.Remaining)
	case errors.Is(err, itemservice.ErrNotFound):
		return temporal.NewNonRetryableApplicationError(err.Error(), ErrTypeNotFound, err)
	case errors.Is(err, itemservice.ErrHeld):
		// Checked before ErrStale, which it also is: for a timer, held is
		// final, where stale means "an event is on its way".
		return temporal.NewNonRetryableApplicationError(err.Error(), ErrTypeHeld, err)
	case errors.Is(err, itemservice.ErrStale):
		return temporal.NewNonRetryableApplicationError(err.Error(), ErrTypeStale, err)
	case errors.Is(err, itemservice.ErrRejected):
		activity.GetLogger(ctx).Info("the item service refused a timer's move",
			"item", in.ItemID, "from", in.FromState, "to", in.ToState, "reason", err.Error())
		return temporal.NewNonRetryableApplicationError(err.Error(), ErrTypeRejected, err)
	case errors.Is(err, itemservice.ErrInvalid):
		// A malformed request is a bug; the same request can never succeed,
		// and each retry would be billed. The workflow logs it with the reason.
		return temporal.NewNonRetryableApplicationError(err.Error(), ErrTypeInvalid, err)
	}
	return err
}

// LoadInstant reads the instant of the field the Item's current state names
// in `at`. It is the declared exception to "the workflow never sees a field
// value": that one value, of a field the Type names in `at`, and nothing
// else. The item service chooses the field; the workflow names none.
func (a *Activities) LoadInstant(ctx context.Context, itemID string) (Instant, error) {
	id, err := uuid.Parse(itemID)
	if err != nil {
		return Instant{}, fmt.Errorf("item id %q: %w", itemID, err)
	}
	got, err := a.Items.AtInstant(ctx, id)
	if errors.Is(err, itemservice.ErrNotFound) {
		return Instant{}, temporal.NewNonRetryableApplicationError(err.Error(), ErrTypeNotFound, err)
	}
	if err != nil {
		return Instant{}, err
	}
	return Instant{Set: got.Set, At: got.At}, nil
}

// afterRequest is the item service's request for a reaction's trigger.
func afterRequest(in AfterReactionInput) (itemservice.ReactionRunRequest, error) {
	source, err := uuid.Parse(in.ItemID)
	if err != nil {
		return itemservice.ReactionRunRequest{}, fmt.Errorf("item id %q: %w", in.ItemID, err)
	}
	cause, err := uuid.Parse(in.CauseItem)
	if err != nil {
		return itemservice.ReactionRunRequest{}, fmt.Errorf("cause item id %q: %w", in.CauseItem, err)
	}
	return itemservice.ReactionRunRequest{
		SourceID: source, Reaction: in.ReactionID, CauseItem: cause, CauseSeq: in.CauseSeq,
		CorrelationID: in.CorrelationID, Hop: in.Hop,
	}, nil
}

// reactionError marks the item service's final answers as non-retryable:
// the Item is gone, or the request is malformed (a bug). Anything else
// (the database is away, the target is held or has moved on) is returned
// as it is, and Temporal retries it within the budget.
func reactionError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, itemservice.ErrNotFound):
		return temporal.NewNonRetryableApplicationError(err.Error(), ErrTypeNotFound, err)
	case errors.Is(err, itemservice.ErrInvalid):
		return temporal.NewNonRetryableApplicationError(err.Error(), ErrTypeInvalid, err)
	}
	return err
}

// RunAfterReaction runs one move or create reaction for one trigger: the
// item service judges it, writes its target and records its outcome on the
// Item. A target that refuses is an outcome, recorded, not an error here.
func (a *Activities) RunAfterReaction(ctx context.Context, in AfterReactionInput) error {
	req, err := afterRequest(in)
	if err != nil {
		return temporal.NewNonRetryableApplicationError(err.Error(), ErrTypeInvalid, err)
	}
	ty, err := a.Items.Type(ctx, in.TypeID, in.TypeVersion)
	if err != nil {
		return err
	}
	r, ok := ty.Reaction(in.ReactionID)
	if !ok {
		err := fmt.Errorf("%s v%d declares no reaction %s", in.TypeID, in.TypeVersion, in.ReactionID)
		return temporal.NewNonRetryableApplicationError(err.Error(), ErrTypeInvalid, err)
	}
	if r.Kind == typedef.ReactionCreate {
		_, err = a.Items.Internal().ReactCreate(ctx, req)
	} else {
		_, err = a.Items.Internal().ReactMove(ctx, req)
	}
	return reactionError(err)
}

// AfterReactionFailed records that a move or create reaction gave up (its
// budget ran out, or it failed for good), so that it is finished.
func (a *Activities) AfterReactionFailed(ctx context.Context, in AfterReactionFailedInput) error {
	req, err := afterRequest(in.Reaction)
	if err != nil {
		return temporal.NewNonRetryableApplicationError(err.Error(), ErrTypeInvalid, err)
	}
	_, err = a.Items.Internal().ReactionGaveUp(ctx, req, in.Reason)
	return reactionError(err)
}

// WorkerOptions tune a worker. The zero value is what production uses.
type WorkerOptions struct {
	// CloseDelay is for tests only. It makes a workflow pause this long
	// between deciding to complete and returning, so a test can send an
	// event into that window. It must be the same on every worker of a task
	// queue, because the pause is part of the workflow's recorded history.
	CloseDelay time.Duration
	// Connectors are what the hook actions call (the CRM and the ERP).
	Connectors *hooks.Connectors
	// HookBudget bounds each hook (ScheduleToClose); zero means
	// DefaultHookBudget (15 min). Tests and the bench set it short.
	HookBudget time.Duration
	// HookAttemptTimeout bounds one attempt of a hook (StartToClose); zero
	// means min(30 s, budget). Tests and the bench only: it forces timeouts.
	HookAttemptTimeout time.Duration
	// DevDay: see Activities.DevDay. cmd/worker sets it only from its
	// --dev-time-scale flag, which it refuses without the explicit dev flag.
	DevDay time.Duration
	// DevTypes: see Activities.DevTypes (--dev-time-scale-types).
	DevTypes []string
}

// NewWorker returns a worker, not yet started, that runs Item workflows and
// their activities from the given task queue.
func NewWorker(c client.Client, taskQueue string, k *itemservice.Service, opts WorkerOptions) worker.Worker {
	w := worker.New(c, taskQueue, worker.Options{})
	run := ItemWorkflow
	if opts.CloseDelay > 0 {
		run = func(ctx workflow.Context, in Input) error { return runItem(ctx, in, opts.CloseDelay) }
	}
	w.RegisterWorkflowWithOptions(run, workflow.RegisterOptions{Name: Name})

	// Registered by name, because the workflow calls them by name.
	a := &Activities{Items: k, Connectors: opts.Connectors, HookBudget: opts.HookBudget,
		HookAttemptTimeout: opts.HookAttemptTimeout, DevDay: opts.DevDay, DevTypes: opts.DevTypes}
	w.RegisterActivityWithOptions(a.LoadItem, activity.RegisterOptions{Name: ActivityLoadItem})
	w.RegisterActivityWithOptions(a.FireTimer, activity.RegisterOptions{Name: ActivityFireTimer})
	w.RegisterActivityWithOptions(a.LoadInstant, activity.RegisterOptions{Name: ActivityLoadInstant})
	w.RegisterActivityWithOptions(a.RunAfterReaction, activity.RegisterOptions{Name: ActivityRunAfterReaction})
	w.RegisterActivityWithOptions(a.AfterReactionFailed, activity.RegisterOptions{Name: ActivityAfterReactionFailed})
	w.RegisterActivityWithOptions(a.RunEnrich, activity.RegisterOptions{Name: ActivityRunEnrich})
	w.RegisterActivityWithOptions(a.EnrichFallback, activity.RegisterOptions{Name: ActivityEnrichFallback})
	w.RegisterActivityWithOptions(a.RunCheck, activity.RegisterOptions{Name: ActivityRunCheck})
	w.RegisterActivityWithOptions(a.ResolveHold, activity.RegisterOptions{Name: ActivityResolveHold})
	w.RegisterActivityWithOptions(a.RunAction, activity.RegisterOptions{Name: ActivityRunAction})
	w.RegisterActivityWithOptions(a.HookFailed, activity.RegisterOptions{Name: ActivityHookFailed})
	w.RegisterActivityWithOptions(a.RunReaction, activity.RegisterOptions{Name: ActivityRunReaction})
	w.RegisterActivityWithOptions(a.ResolveReaction, activity.RegisterOptions{Name: ActivityResolveReaction})
	w.RegisterActivityWithOptions(a.Undo, activity.RegisterOptions{Name: ActivityUndo})
	w.RegisterActivityWithOptions(a.ReactionFailed, activity.RegisterOptions{Name: ActivityReactionFailed})
	return w
}
