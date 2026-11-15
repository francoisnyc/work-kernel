package itemservice

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/francoisnyc/work-kernel/internal/hooks"
	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/lifecycle"
	"github.com/francoisnyc/work-kernel/internal/policy"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// TransitionRequest moves an Item to another lifecycle state. Fields is an
// optional patch with the same meaning as in UpdateRequest: it is applied
// first, and the transition's guards see the fields as they are after it.
type TransitionRequest struct {
	IdempotencyKey string
	Principal      item.Principal
	ItemID         uuid.UUID
	ToState        string
	Fields         map[string]any // optional; keyed by stable field ID
	// ExpectedItemSeq is the same optional if-match as on UpdateRequest.
	ExpectedItemSeq int64
	Attribution     item.Attribution // optional; what caused this write
	// Wait is how long to wait for a held transition's check, up to the
	// service's cap (MaxWait). Zero answers at once. It is not part of the
	// request's identity (the "-" tag keeps it out of the idempotency hash):
	// a retry with the same key may wait more or less.
	Wait time.Duration `json:"-"`
}

// Transition moves an Item to another state if its Type allows it, and
// records a "transition" event. With a Fields patch, the patch and the move
// are one write: one row lock, one event. An illegal move or an invalid patch
// returns ErrRejected with the reason and writes nothing. It is the public
// door: the kernel's own actors use Internal().Transition.
//
// A transition with a check hook is not made at once: the request is
// recorded and a hold opens (outcome pending). With Wait, the call waits up
// to that long for the check, and answers applied, denied or error.
func (k *Service) Transition(ctx context.Context, req TransitionRequest) (TransitionResult, error) {
	if err := checkPublic(req.Principal, req.Attribution); err != nil {
		return TransitionResult{}, err
	}
	if req.Wait < 0 || req.Wait > k.maxWait {
		return TransitionResult{}, fmt.Errorf("%w: wait must be between 0 and %v", ErrInvalid, k.maxWait)
	}
	if k.syncChecks != nil {
		var err error
		if ctx, req, err = k.syncCheck(ctx, req); err != nil {
			return TransitionResult{}, err
		}
	}
	res, kind, err := k.transition(ctx, req, nil)
	if err != nil {
		return TransitionResult{}, err
	}
	return k.outcome(ctx, res, kind, req.Wait)
}

// ErrUnavailable is variant B's answer when its synchronous check could not
// answer in time (or failed): the transition is refused.
var ErrUnavailable = errors.New("itemservice: check unavailable")

type syncCheckedKey struct{}

// syncCheck is variant B: before the write transaction, read the Item and,
// if the transition has a check, call the check's action synchronously with
// a timeout. A deny is ErrRejected; a timeout or error is ErrUnavailable;
// allow (or correct, whose values join the patch) lets the write go on,
// marked in the context so that it opens no hold. The fields may change
// between this read and the write: B cannot freeze them.
func (k *Service) syncCheck(ctx context.Context, req TransitionRequest) (context.Context, TransitionRequest, error) {
	var typeID, state string
	var version int
	var fields map[string]any
	err := k.pool.QueryRow(ctx,
		`select type_id, type_version, state, fields from items where id = $1 and namespace_id = $2`,
		req.ItemID, defaultNamespace).Scan(&typeID, &version, &state, &fields)
	if err != nil {
		return ctx, req, nil // let the write report it (not found, and so on)
	}
	ty, err := k.loadType(ctx, k.pool, typeID, version)
	if err != nil {
		return ctx, req, err
	}
	check, ok := lifecycle.CheckFor(ty, state, req.ToState)
	if !ok {
		return ctx, req, nil
	}
	args := make([]any, len(check.Reads))
	for i, f := range check.Reads {
		args[i] = fields[f]
		if v, ok := req.Fields[f]; ok {
			args[i] = v
		}
	}
	cctx, cancel := context.WithTimeout(ctx, k.syncCheckTimeout)
	defer cancel()
	res, err := k.syncChecks.Run(cctx, check.Action, "", args)
	if err != nil {
		return ctx, req, fmt.Errorf("%w: %s: %v", ErrUnavailable, check.ID, err)
	}
	switch res.Verdict {
	case hooks.Deny:
		return ctx, req, fmt.Errorf("%w: %s denied: %s", ErrRejected, check.ID, res.Reason)
	case hooks.Correct:
		patch := maps.Clone(req.Fields)
		if patch == nil {
			patch = map[string]any{}
		}
		for i, f := range check.Writes {
			if i < len(res.Values) {
				patch[f] = res.Values[i]
			}
		}
		req.Fields = patch
	}
	return context.WithValue(ctx, syncCheckedKey{}, true), req, nil
}

// transition is Transition's body for both doors; kc is as in update. It
// also says whether the write opened a hold, so the caller knows whether
// there is a hold to read (holdUnknown when the key was already used).
func (k *Service) transition(ctx context.Context, req TransitionRequest, kc *kernelCause) (WriteResult, holdKind, error) {
	if req.IdempotencyKey == "" || req.Principal.Actor == "" || req.ToState == "" {
		return WriteResult{}, holdUnknown, fmt.Errorf("%w: idempotency key, actor and target state are required", ErrInvalid)
	}
	if err := checkPrincipal(req.Principal); err != nil {
		return WriteResult{}, holdUnknown, err
	}
	if err := checkOptions(req.ExpectedItemSeq, req.Attribution); err != nil {
		return WriteResult{}, holdUnknown, err
	}
	attr, err := sessionAttribution(req.Principal, req.Attribution)
	if err != nil {
		return WriteResult{}, holdUnknown, err
	}
	hash, err := requestHash("transition", req)
	if err != nil {
		return WriteResult{}, holdUnknown, err
	}

	// Set by the write below when it runs; it stays holdUnknown when the
	// idempotency record answers instead. A rolled-back attempt that is run
	// again sets it again.
	kind := holdUnknown
	res, err := k.write(ctx, req.Principal, req.IdempotencyKey, hash, func(tx pgx.Tx) (WriteResult, error) {
		// The decision depends on the current state and fields, so it is made
		// under the row lock: no other write can slip in between check and write.
		it, err := lockItem(ctx, tx, req.ItemID)
		if err != nil {
			return WriteResult{}, err
		}
		// A kernel write (timer, hook) is internal infrastructure, not a
		// principal reading the Item, so it is not subject to visibility
		// (docs/design.md, Policy, "Boundary"). It is still subject to
		// `allowed`, below.
		recorded, err := admit(ctx, tx, req.Principal, req.ItemID, it, kc)
		if err != nil {
			return WriteResult{}, err
		}
		if err := checkExpectedSeq(it, req.ExpectedItemSeq); err != nil {
			return WriteResult{}, err
		}
		// A transition with fields puts content into the Item's scope; a
		// plain transition does not.
		if len(req.Fields) > 0 {
			if err := checkAgentWrite(ctx, tx, req.Principal, it.ScopeID); err != nil {
				return WriteResult{}, err
			}
		}
		ty, err := k.loadType(ctx, tx, it.TypeID, it.TypeVersion)
		if err != nil {
			return WriteResult{}, err
		}
		// While a hold is open, only an escape leaves the state, and it
		// cancels the hold. Everything else, the timer included, is held.
		hold, err := readOpenHold(ctx, tx, req.ItemID)
		if err != nil {
			return WriteResult{}, err
		}
		if hold != nil {
			if err := lifecycle.TransitionDuringHold(ty, hold.rule(), req.ToState); err != nil {
				return WriteResult{}, heldError(hold, err)
			}
		}
		if err := checkHookWrite(ty, kc, req.Fields); err != nil {
			return WriteResult{}, err
		}

		// All lifecycle logic lives in lifecycle.Transition; the item service only
		// asks and records. Two %w verbs wrap both errors, so callers can match
		// ErrRejected with errors.Is and read the *lifecycle.Rejection with errors.As.
		//
		// The guards are checked against the fields as the patch leaves them.
		if err := ty.CheckFields(req.Fields, true); err != nil {
			return WriteResult{}, fmt.Errorf("%w: %v", ErrRejected, err)
		}
		merged, set, cleared := applyPatch(it.Fields, req.Fields)
		next, err := lifecycle.Transition(ty, it.State, merged, req.ToState)
		if err != nil {
			return WriteResult{}, fmt.Errorf("%w: %w", ErrRejected, err)
		}
		// The move is legal. Whether this principal may make it is policy's
		// question, asked second, so ErrForbidden always means "legal, but
		// not for you".
		check, checked := lifecycle.CheckFor(ty, it.State, next.State)
		if checked && kc != nil {
			// Only ResolveHold applies a checked transition, for a requester:
			// a kernel actor never makes one on its own authority.
			return WriteResult{}, fmt.Errorf("%w: %s -> %s is checked by %s; only a requester may ask for it",
				ErrForbidden, it.State, next.State, check.ID)
		}
		if err := authorize(ctx, tx, req.Principal, ty, it.State, next.State); err != nil {
			return WriteResult{}, err
		}
		if checked && ctx.Value(syncCheckedKey{}) == nil {
			kind = holdOpened
			return requestHold(ctx, tx, ty, req.ItemID, it, check, next.State, req.Principal, recorded, attr,
				merged, set, cleared)
		}
		kind = holdNone

		itemSeq := it.ItemSeq + 1
		_, err = tx.Exec(ctx,
			`update items set state = $2, fields = $3, item_seq = $4, updated_at = now() where id = $1`,
			req.ItemID, next.State, merged, itemSeq)
		if err != nil {
			return WriteResult{}, fmt.Errorf("update item: %w", err)
		}

		// A plain transition keeps its two-key payload; the field lists appear
		// only when the request carried a patch.
		payload := map[string]any{"from": it.State, "to": next.State}
		if len(req.Fields) > 0 {
			payload["field_ids"] = sortedKeys(set)
			if len(cleared) > 0 {
				payload["cleared_field_ids"] = sortedKeys(cleared)
			}
		}
		if hold != nil { // an escape: it cancels the hold, in the same event
			payload["canceled_hold_id"] = hold.id.String()
		}
		res, err := appendEvent(ctx, tx, ty, req.ItemID, itemSeq, recorded, attr, "transition", payload, set)
		if err != nil || hold == nil {
			return res, err
		}
		return res, closeHold(ctx, tx, hold.id, item.HoldCanceled, "", itemSeq)
	})
	if err != nil {
		return WriteResult{}, holdUnknown, err
	}
	return res, kind, nil
}

// authorize checks the transition's `allowed` list against the principal's
// roles. When an agent acts on behalf of a user, both must pass: the agent
// has the user's rights only where it also has its own.
func authorize(ctx context.Context, tx pgx.Tx, p item.Principal, ty *typedef.Type, from, to string) error {
	tr, _ := ty.Transition(from, to) // lifecycle.Transition has just found it
	if len(tr.Allowed) == 0 {
		return nil // anyone who can see the Item; no need to read roles
	}
	for _, who := range []string{p.Actor, p.OnBehalfOf} {
		if who == "" {
			continue
		}
		// Roles are read on every write, inside its transaction: no cache.
		roles, err := policy.Roles(ctx, tx, defaultNamespace, who)
		if err != nil {
			return err
		}
		if !policy.AuthorizeTransition(who, roles, tr) {
			return fmt.Errorf("%w: %s may not move this Item from %q to %q", ErrForbidden, who, from, to)
		}
	}
	return nil
}
