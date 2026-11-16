package workflow

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"go.temporal.io/sdk/temporal"

	"github.com/francoisnyc/work-kernel/internal/hooks"
	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// The hook activities. Each reads the hook's inputs from the item service
// (its declared reads, nothing else), calls the action, and writes back
// through the internal door as the hook. Retries are the workflow's: an
// error returned here is retried within the hook's budget, unless it is
// marked non-retryable.

// hookCall is what every hook activity starts from.
type hookCall struct {
	id        uuid.UUID
	hook      typedef.Hook
	principal item.Principal
	attr      item.Attribution
}

func (a *Activities) prepare(ctx context.Context, in HookInput) (hookCall, error) {
	id, err := uuid.Parse(in.ItemID)
	if err != nil {
		return hookCall{}, temporal.NewNonRetryableApplicationError(fmt.Sprintf("item id %q", in.ItemID), ErrTypeInvalid, err)
	}
	ty, err := a.Items.Type(ctx, in.TypeID, in.TypeVersion)
	if err != nil {
		return hookCall{}, err
	}
	h, ok := ty.Hook(in.HookID)
	if !ok {
		return hookCall{}, temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("%s v%d declares no hook %s", in.TypeID, in.TypeVersion, in.HookID), ErrTypeInvalid, nil)
	}
	return hookCall{
		id:        id,
		hook:      h,
		principal: item.Principal{Actor: fmt.Sprintf("%s:%s:%s", item.KindHook, in.TypeID, in.HookID)},
		attr: item.Attribution{
			OriginatingHook: fmt.Sprintf("hook:%s@v%d:%s", in.TypeID, in.TypeVersion, in.HookID),
			CausationID:     fmt.Sprintf("%s:%d", in.ItemID, in.CauseSeq),
			CorrelationID:   in.CorrelationID,
			Hop:             in.Hop,
		},
	}, nil
}

// run reads the hook's inputs and calls its action. key is passed to the
// connector by actions with a side effect; read-only actions get "".
func (a *Activities) run(ctx context.Context, c hookCall, key string) (hooks.Result, error) {
	if a.Connectors == nil {
		return hooks.Result{}, temporal.NewNonRetryableApplicationError("no connectors configured", ErrTypeHookFailed, nil)
	}
	values, err := a.Items.Internal().HookInput(ctx, itemservice.HookInputRequest{
		Principal: c.principal, ItemID: c.id, Attribution: c.attr,
	})
	if err != nil {
		return hooks.Result{}, kernelError(err)
	}
	args := make([]any, len(c.hook.Reads))
	for i, f := range c.hook.Reads {
		args[i] = values[f] // nil when unset; the action refuses it
	}
	res, err := a.Connectors.Run(ctx, c.hook.Action, key, args)
	if errors.Is(err, hooks.ErrKeyConflict) { // before ErrPermanent, which it also is
		return hooks.Result{}, temporal.NewNonRetryableApplicationError(err.Error(), ErrTypeKeyConflict, err)
	}
	if errors.Is(err, hooks.ErrPermanent) {
		return hooks.Result{}, temporal.NewNonRetryableApplicationError(err.Error(), ErrTypeHookFailed, err)
	}
	return res, err
}

// writes pairs an action's values with the hook's writes, by position.
func writes(h typedef.Hook, values []any) map[string]any {
	out := make(map[string]any, len(h.Writes))
	for i, f := range h.Writes {
		if i < len(values) {
			out[f] = values[i]
		}
	}
	return out
}

// RunEnrich runs an enrich hook and writes what it returns.
func (a *Activities) RunEnrich(ctx context.Context, in HookInput) error {
	c, err := a.prepare(ctx, in)
	if err != nil {
		return err
	}
	res, err := a.run(ctx, c, "")
	if err != nil {
		return err
	}
	return a.writeFields(ctx, c, in.IdempotencyKey, writes(c.hook, res.Values))
}

// EnrichFallback writes an enrich hook's fallback (the Type's values, not
// the workflow's). Under the hook's own key: if the hook did write after
// all, the key is taken and nothing more is written.
func (a *Activities) EnrichFallback(ctx context.Context, in HookInput) error {
	c, err := a.prepare(ctx, in)
	if err != nil {
		return err
	}
	if len(c.hook.Fallback) == 0 {
		return nil
	}
	return a.writeFields(ctx, c, in.IdempotencyKey, c.hook.Fallback)
}

func (a *Activities) writeFields(ctx context.Context, c hookCall, key string, fields map[string]any) error {
	_, err := a.Items.Internal().Update(ctx, itemservice.UpdateRequest{
		IdempotencyKey: key, Principal: c.principal, ItemID: c.id, Fields: fields, Attribution: c.attr,
	})
	if errors.Is(err, itemservice.ErrIdempotencyKeyReuse) {
		return nil // this hook already wrote for this entry
	}
	return kernelError(err)
}

// RunAction runs an action hook: it passes the hook's key to the connector,
// so a retry gets what the first attempt made, and writes back the result
// under the same key.
//
// The write-back's key coming back with a different request means the
// connector made a second, different thing for one key: a duplicate. It is
// a non-retryable key conflict, never "already written". (An enrich hook
// treats the same case as already written: its fallback shares its key.)
func (a *Activities) RunAction(ctx context.Context, in HookInput) error {
	c, err := a.prepare(ctx, in)
	if err != nil {
		return err
	}
	res, err := a.run(ctx, c, in.IdempotencyKey)
	if err != nil {
		return err
	}
	_, err = a.Items.Internal().Update(ctx, itemservice.UpdateRequest{
		IdempotencyKey: in.IdempotencyKey, Principal: c.principal, ItemID: c.id,
		Fields: writes(c.hook, res.Values), Attribution: c.attr,
	})
	if errors.Is(err, itemservice.ErrIdempotencyKeyReuse) {
		return temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("%s ran again for key %s and got a different result", in.HookID, in.IdempotencyKey),
			ErrTypeKeyConflict, err)
	}
	return kernelError(err)
}

// HookFailed records on the Item that an action hook gave up. Its key is its
// own (hook_failed:<item>:<entry>:<hook>), not the hook's: after a key
// conflict the hook's key is already taken by its first write-back.
func (a *Activities) HookFailed(ctx context.Context, in HookFailedInput) error {
	c, err := a.prepare(ctx, in.Hook)
	if err != nil {
		return err
	}
	_, err = a.Items.Internal().HookFailed(ctx, itemservice.HookFailedRequest{
		IdempotencyKey: fmt.Sprintf("hook_failed:%s:%d:%s", in.Hook.ItemID, in.Hook.CauseSeq, in.Hook.HookID),
		Principal:      c.principal, ItemID: c.id, Reason: in.Reason, Attribution: c.attr,
	})
	if errors.Is(err, itemservice.ErrIdempotencyKeyReuse) {
		return nil // already recorded for this entry
	}
	return kernelError(err)
}

// verdicts maps an action's answer onto the item service's.
var verdicts = map[hooks.Verdict]itemservice.Verdict{
	hooks.Allow:   itemservice.VerdictAllow,
	hooks.Deny:    itemservice.VerdictDeny,
	hooks.Correct: itemservice.VerdictCorrect,
}

// RunCheck runs a check and resolves its hold with the answer.
func (a *Activities) RunCheck(ctx context.Context, in HookInput) error {
	c, err := a.prepare(ctx, in)
	if err != nil {
		return err
	}
	res, err := a.run(ctx, c, "")
	if err != nil {
		return err
	}
	v, ok := verdicts[res.Verdict]
	if !ok {
		return temporal.NewNonRetryableApplicationError(fmt.Sprintf("unknown verdict %q", res.Verdict), ErrTypeHookFailed, nil)
	}
	req := a.resolveRequest(c, in, v, res.Reason)
	if v == itemservice.VerdictCorrect {
		req.Fields = writes(c.hook, res.Values)
	}
	return a.resolve(ctx, req)
}

// ResolveHold resolves a hold as error, when its check could not answer.
// Under the check's own key: if the check did resolve it after all, the key
// is taken and nothing more is written.
func (a *Activities) ResolveHold(ctx context.Context, in ResolveInput) error {
	c, err := a.prepare(ctx, in.Hook)
	if err != nil {
		return err
	}
	return a.resolve(ctx, a.resolveRequest(c, in.Hook, itemservice.Verdict(in.Verdict), in.Reason))
}

func (a *Activities) resolveRequest(c hookCall, in HookInput, v itemservice.Verdict, reason string) itemservice.ResolveHoldRequest {
	holdID, _ := uuid.Parse(in.HoldID) // a bad ID is uuid.Nil, which ResolveHold refuses
	return itemservice.ResolveHoldRequest{
		IdempotencyKey: in.IdempotencyKey, Principal: c.principal, ItemID: c.id, HoldID: holdID,
		Verdict: v, Reason: reason, Attribution: c.attr,
	}
}

func (a *Activities) resolve(ctx context.Context, req itemservice.ResolveHoldRequest) error {
	_, err := a.Items.Internal().ResolveHold(ctx, req)
	if errors.Is(err, itemservice.ErrIdempotencyKeyReuse) {
		return nil // the hold was already resolved under this key
	}
	return kernelError(err)
}

// kernelError marks the item service's final answers as non-retryable, by
// type; anything else (the database is away) is returned as is, to retry.
func kernelError(err error) error {
	for _, k := range []struct {
		target error
		typ    string
	}{
		{itemservice.ErrNotFound, ErrTypeNotFound},
		{itemservice.ErrHeld, ErrTypeHeld}, // before ErrStale, which it also is
		{itemservice.ErrStale, ErrTypeStale},
		{itemservice.ErrRejected, ErrTypeRejected},
		{itemservice.ErrForbidden, ErrTypeRejected},
		{itemservice.ErrInvalid, ErrTypeInvalid},
	} {
		if errors.Is(err, k.target) {
			return temporal.NewNonRetryableApplicationError(err.Error(), k.typ, err)
		}
	}
	return err
}

// --- link reactions (proof of concept, Step 2, part 2) ---

// RunReaction applies a held reaction to its target and resolves its hold:
// allow if the write committed (or had committed: a retry), deny with the
// target's refusal (DenyReason) if refused. A hold already closed is not an
// error: there is nothing left to decide.
//
// Phase B stub: not implemented yet.
func (a *Activities) RunReaction(ctx context.Context, in ReactionInput) error {
	return temporal.NewNonRetryableApplicationError("RunReaction is not implemented", ErrTypeInvalid, nil)
}

// ResolveReaction resolves a held reaction's hold as error, as the reaction.
//
// Phase B stub: not implemented yet.
func (a *Activities) ResolveReaction(ctx context.Context, in ResolveReactionInput) error {
	return temporal.NewNonRetryableApplicationError("ResolveReaction is not implemented", ErrTypeInvalid, nil)
}

// Undo reverses a hold's reservation, if one committed.
//
// Phase B stub: not implemented yet.
func (a *Activities) Undo(ctx context.Context, in ReactionInput) error {
	return temporal.NewNonRetryableApplicationError("Undo is not implemented", ErrTypeInvalid, nil)
}

// ReactionFailed records on the source that an undo gave up.
//
// Phase B stub: not implemented yet.
func (a *Activities) ReactionFailed(ctx context.Context, in ReactionFailedInput) error {
	return temporal.NewNonRetryableApplicationError("ReactionFailed is not implemented", ErrTypeInvalid, nil)
}
