package itemservice

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

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
	res, kind, err := k.transition(ctx, req, nil)
	if err != nil {
		return TransitionResult{}, err
	}
	return k.outcome(ctx, res, kind, req.Wait)
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
		// When the guards into ToState read links, the linked Items are locked
		// too (FOR SHARE), so they cannot change before this commits.
		it, links, resolutions, err := k.lockForTransition(ctx, tx, nsOf(req.Principal), req.ItemID, req.ToState)
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
			if err := checkAgentWrite(ctx, tx, req.Principal, it.ScopeID, &req.ItemID); err != nil {
				return WriteResult{}, err
			}
		}
		ty, err := k.loadType(ctx, tx, it.TypeID, it.TypeVersion)
		if err != nil {
			return WriteResult{}, err
		}
		// Date-time values are stored in one form, whatever offset they
		// came in. req is this closure's own copy from here on (a struct is
		// copied on assignment); a value that is no instant is left as it
		// is, and CheckFields refuses it below.
		req := req
		req.Fields = ty.NormalizeFields(req.Fields)
		// Multi-value fields, as in an update: under the lock.
		// (A malformed value is left as it is: CheckFields refuses it below,
		// in its place in the order of checks.)
		var setOps map[string]typedef.SetOp
		if req.Fields, setOps, err = applySets(ty, it.Fields, req.Fields, false); err != nil {
			return WriteResult{}, err
		}
		// The write's time, read now that every row lock is taken: the
		// move's event carries it, its computed effects read it as
		// cause.at, and a timer at an instant is judged against it.
		at, err := stampWrite(ctx, tx)
		if err != nil {
			return WriteResult{}, err
		}
		if err := checkAtTimer(ty, req.Principal, it, req.ToState, at); err != nil {
			return WriteResult{}, err
		}
		// Not writable, then frozen (against the state the move leaves),
		// then held.
		var declared *typedef.Transition
		if tr, ok := ty.Transition(it.State, req.ToState); ok {
			declared = &tr
		}
		// A field the move computes (`sets`) is the Type's to write: a
		// patch that names it, with a value or with null, contradicts the
		// move. Malformed, before anything else. Such a field is not
		// "named by the write" below, so writable_by does not apply to it.
		if declared != nil {
			for _, id := range slices.Sorted(maps.Keys(declared.Sets)) {
				if _, named := req.Fields[id]; named {
					return WriteResult{}, &InvalidError{Field: id,
						Reason: fmt.Sprintf("the transition %s -> %s sets %s; its patch may not name it", it.State, req.ToState, id)}
				}
			}
		}
		// A field the move clears counts as named by the write: it is
		// judged like a patch's null (not writable, frozen), whether or not
		// it has a value. A patch that sets such a field contradicts the
		// move: malformed, before anything else.
		named := req.Fields
		if declared != nil && len(declared.Clears) > 0 {
			named = maps.Clone(req.Fields)
			if named == nil {
				named = map[string]any{}
			}
			for _, id := range declared.Clears {
				if v, set := req.Fields[id]; set && v != nil {
					return WriteResult{}, &InvalidError{Field: id,
						Reason: fmt.Sprintf("the transition %s -> %s clears %s; its patch may not set it", it.State, req.ToState, id)}
				}
				named[id] = nil
			}
		}
		var writer *policy.Who
		if kc == nil {
			if writer, err = publicWriter(ctx, tx, ty, req.Principal, named); err != nil {
				return WriteResult{}, err
			}
		}
		if err := checkPatch(ty, it.State, declared, named, writer); err != nil {
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
		// asks and records. Its refusal becomes a *RejectedError with the
		// rule's code, which still matches ErrRejected (errors.Is) and holds the
		// *lifecycle.Rejection (errors.As).
		//
		// The guards are checked against the fields as the patch leaves them.
		if err := ty.CheckFields(req.Fields, true); err != nil {
			return WriteResult{}, fieldsInvalid(err)
		}
		merged, set, cleared := applyPatch(it.Fields, req.Fields)
		next, err := lifecycle.TransitionWith(ty, it.State,
			lifecycle.Input{Fields: merged, Links: links, Today: k.today(), CauseAt: at}, req.ToState)
		if err != nil {
			return WriteResult{}, fromLifecycle(err)
		}
		// The move is legal, and says which fields it clears: they go now,
		// after required fields and the guard were judged with them. Only a
		// field that had a value is recorded as cleared.
		clearedByMove := false
		for _, id := range next.Clears {
			if _, has := merged[id]; has {
				delete(merged, id)
				cleared[id] = nil
				clearedByMove = true
			}
		}
		// Then the computed effects, evaluated by the lifecycle on the
		// fields as the patch left them (before the clears): a value is
		// set, null unsets. Recorded like a patch's fields.
		for _, id := range slices.Sorted(maps.Keys(next.Sets)) {
			v := next.Sets[id]
			if v == nil {
				if _, has := merged[id]; has {
					delete(merged, id)
					cleared[id] = nil
				}
				delete(set, id)
			} else {
				merged[id] = v
				set[id] = v
				delete(cleared, id)
			}
			clearedByMove = true // the fields changed: the invariants are asked
		}
		// Whether this principal may make the move is policy's question,
		// asked second, so ErrForbidden always means "legal, but not for
		// you".
		// A held transition is decided by its check hook, or by its held
		// reaction (Step 2, part 2); their IDs share one space.
		decider, checked := holdDecider(ty, it.State, next.State)
		if checked && kc != nil {
			// Only ResolveHold applies a checked transition, for a requester:
			// a kernel actor never makes one on its own authority.
			return WriteResult{}, fmt.Errorf("%w: %s -> %s is checked by %s; only a requester may ask for it",
				ErrForbidden, it.State, next.State, decider)
		}
		if err := authorize(ctx, tx, req.Principal, ty, it.State, next.State, facts(it)); err != nil {
			return WriteResult{}, err
		}
		if links != nil {
			if err := callTestHook(ctx, tx, "link_guard_held"); err != nil {
				return WriteResult{}, err
			}
		}
		// Last: the fields as the write leaves them keep the invariants
		// (a held request's patch is written with the request).
		if len(req.Fields) > 0 || clearedByMove {
			if err := checkInvariants(ty, merged); err != nil {
				return WriteResult{}, err
			}
		}
		if checked {
			// The links entering the state sets are set when the hold allows
			// the move, not now.
			kind = holdOpened
			return requestHold(ctx, tx, ty, req.ItemID, it, decider, next.State, req.Principal, recorded, attr,
				merged, set, cleared)
		}
		if r, bad := unresolved(resolutions); bad {
			return WriteResult{}, linkUnresolvedError(r)
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
		addFieldIDs(payload, set, cleared)
		addSetOps(payload, setOps)
		if hold != nil { // an escape: it cancels the hold, in the same event
			payload["canceled_hold_id"] = hold.id.String()
		}
		if err := applyResolutions(ctx, tx, nsOf(req.Principal), ty, req.ItemID, resolutions, payload); err != nil {
			return WriteResult{}, err
		}
		res, err := appendEvent(ctx, tx, ty, req.ItemID, itemSeq, recorded, attr, "transition", payload, set)
		if err == nil {
			err = storeSetOps(ctx, tx, req.ItemID, itemSeq, setOps)
		}
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

// authorize checks the transition's `allowed` and `except` for the
// principal (policy.MayTransition): the Item's fields for field:<id>
// entries (as stored: a patch may not write them), its creator for creator,
// and the roles of the actor and of the user it acts for, read here.
func authorize(ctx context.Context, tx pgx.Tx, p item.Principal, ty *typedef.Type, from, to string,
	it policy.ItemFacts) error {
	tr, _ := ty.Transition(from, to) // lifecycle.Transition has just found it
	if len(tr.Allowed) == 0 && len(tr.Except) == 0 {
		return nil // anyone who can see the Item; no need to read roles
	}
	who := policy.Who{Actor: p.Actor, OnBehalfOf: p.OnBehalfOf}
	// Roles are read on every write, inside its transaction: no cache.
	var err error
	if who.ActorRoles, err = policy.Roles(ctx, tx, nsOf(p), p.Actor); err != nil {
		return err
	}
	if p.OnBehalfOf != "" {
		if who.OnBehalfOfRoles, err = policy.Roles(ctx, tx, nsOf(p), p.OnBehalfOf); err != nil {
			return err
		}
	}
	if !policy.MayTransition(tr, who, it) {
		return fmt.Errorf("%w: %s may not move this Item from %q to %q", ErrForbidden, p.Actor, from, to)
	}
	return nil
}
