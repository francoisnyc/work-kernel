package itemservice

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/lifecycle"
	"github.com/francoisnyc/work-kernel/internal/policy"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// Holds (docs/design.md, "Holds"). Asking for a transition that has a check
// hook does not move the Item: it writes a transition_requested event and
// opens a hold, in one transaction. The check, run by the workflow, settles
// the hold through Internal().ResolveHold. While the hold is open, the
// fields the check reads are frozen and only an escape leaves the state.

// ErrHeld means an open hold refuses the write: a frozen field, or a move
// out of a held state. It is an ErrStale: the write may succeed once the
// hold resolves. The message names the hold.
var ErrHeld = fmt.Errorf("%w: held", ErrStale)

// MaxWait is the longest a Transition may wait for its hold to resolve.
const MaxWait = 5 * time.Second

// holdPoll is how often a waiting Transition looks at its hold.
const holdPoll = 50 * time.Millisecond

// Outcome is how a Transition ended, as far as the caller can know.
type Outcome string

const (
	OutcomeApplied Outcome = "applied" // the Item moved
	OutcomeDenied  Outcome = "denied"  // the check, or a re-check of the requester, said no
	OutcomePending Outcome = "pending" // the hold is still open; ask again later (same key)
	OutcomeError   Outcome = "error"   // the check could not answer; the requester may ask again
)

// TransitionResult is a Transition's answer. Embedding WriteResult promotes
// its fields, so res.ItemSeq works as before: Go's composition in place of
// inheritance. For a held transition, WriteResult names the event that
// decided it (the resolution) or, while pending, the request.
type TransitionResult struct {
	WriteResult
	Outcome Outcome
	HoldID  uuid.UUID // set when the transition was held
	Reason  string    // a code, for denied and error
}

// Verdict is a check's answer to a hold.
type Verdict string

const (
	VerdictAllow   Verdict = "allow"   // apply the requested transition
	VerdictCorrect Verdict = "correct" // apply it, with the hook's field patch
	VerdictDeny    Verdict = "deny"    // refuse it, with a reason
	VerdictError   Verdict = "error"   // the check could not answer, with a reason
)

// reasonCode is the shape of a reason: a code, never free text, because it
// goes into the event payload, which holds no personal data.
var reasonCode = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// ResolveHoldRequest settles a hold. Only the hold's own check hook may send
// it, caused by the transition_requested event.
type ResolveHoldRequest struct {
	IdempotencyKey string
	Principal      item.Principal
	ItemID         uuid.UUID
	HoldID         uuid.UUID
	Verdict        Verdict
	Fields         map[string]any // correct only: values for the hook's writes
	Reason         string         // deny and error: a code such as customer_on_hold
	Attribution    item.Attribution
}

// HookInputRequest asks for the values a hook reads.
type HookInputRequest struct {
	Principal   item.Principal
	ItemID      uuid.UUID
	Attribution item.Attribution
}

// openHold is the row of an Item's open hold.
type openHold struct {
	id             uuid.UUID
	hook, from, to string
}

func (h *openHold) rule() lifecycle.Hold {
	return lifecycle.Hold{Hook: h.hook, From: h.from, To: h.to}
}

// heldError turns a lifecycle refusal into ErrHeld, naming the hold.
func heldError(h *openHold, err error) error {
	var rej *lifecycle.Rejection
	if errors.As(err, &rej) {
		return fmt.Errorf("%w: hold %s: %s", ErrHeld, h.id, rej.Reason)
	}
	return err
}

// readOpenHold returns the Item's open hold, or nil. Called under the Item's
// row lock, so the answer holds until the write commits.
func readOpenHold(ctx context.Context, tx pgx.Tx, itemID uuid.UUID) (*openHold, error) {
	var h openHold
	err := tx.QueryRow(ctx,
		`select id, hook, from_state, to_state from item_holds where item_id = $1 and status = 'open'`, itemID,
	).Scan(&h.id, &h.hook, &h.from, &h.to)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read open hold: %w", err)
	}
	return &h, nil
}

// checkHookWrite limits a hook to the fields its declaration writes. A Type
// that declares no hooks still lets its own hooks write any field: part 1
// relies on it, and it is in docs/journal.md under Later.
func checkHookWrite(ty *typedef.Type, kc *kernelCause, fields map[string]any) error {
	if kc == nil || kc.hookID == "" || len(fields) == 0 || len(ty.Hooks) == 0 {
		return nil
	}
	h, ok := ty.Hook(kc.hookID)
	if !ok {
		return fmt.Errorf("%w: hook %s is not declared by %s v%d", ErrInvalid, kc.hookID, ty.ID, ty.Version)
	}
	for _, id := range slices.Sorted(maps.Keys(fields)) {
		if !slices.Contains(h.Writes, id) {
			return fmt.Errorf("%w: hook %s does not write %s", ErrInvalid, kc.hookID, id)
		}
	}
	return nil
}

// requestHold writes a transition request: the patch (if any), a
// transition_requested event, and the hold. The Item stays in its state.
func requestHold(ctx context.Context, tx pgx.Tx, ty *typedef.Type, id uuid.UUID, it lockedItem,
	check typedef.Hook, to string, p item.Principal, recorded item.Principal, attr item.Attribution,
	merged, set, cleared map[string]any) (WriteResult, error) {

	holdID, err := uuid.NewV7()
	if err != nil {
		return WriteResult{}, fmt.Errorf("new hold id: %w", err)
	}
	itemSeq := it.ItemSeq + 1
	if _, err := tx.Exec(ctx,
		`update items set fields = $2, item_seq = $3, updated_at = now() where id = $1`,
		id, merged, itemSeq); err != nil {
		return WriteResult{}, fmt.Errorf("update item: %w", err)
	}
	// The target is `requested_to`, never `to`: this event enters no state.
	payload := map[string]any{"from": it.State, "requested_to": to, "hold_id": holdID.String()}
	addFieldIDs(payload, set, cleared)
	res, err := appendEvent(ctx, tx, ty, id, itemSeq, recorded, attr, lifecycle.OpTransitionRequested, payload, set)
	if err != nil {
		return WriteResult{}, err
	}
	_, err = tx.Exec(ctx,
		`insert into item_holds (id, item_id, hook, from_state, to_state, requested_seq,
		                         requester, requester_on_behalf_of, requester_agent_session)
		 values ($1, $2, $3, $4, $5, $6, $7, nullif($8, ''), nullif($9, ''))`,
		holdID, id, check.ID, it.State, to, itemSeq, p.Actor, p.OnBehalfOf, p.AgentSession)
	if err != nil {
		return WriteResult{}, fmt.Errorf("open hold: %w", err)
	}
	return res, nil
}

// closeHold records how a hold ended and which event ended it.
func closeHold(ctx context.Context, tx pgx.Tx, holdID uuid.UUID, status, reason string, seq int64) error {
	_, err := tx.Exec(ctx,
		`update item_holds set status = $2, reason = nullif($3, ''), resolved_seq = $4, resolved_at = now()
		 where id = $1`, holdID, status, reason, seq)
	if err != nil {
		return fmt.Errorf("close hold: %w", err)
	}
	return nil
}

// addFieldIDs records a patch's shape in a payload: the field IDs it set and
// cleared, never their values. Nothing is added for an empty patch.
func addFieldIDs(payload, set, cleared map[string]any) {
	if len(set)+len(cleared) == 0 {
		return
	}
	payload["field_ids"] = sortedKeys(set)
	if len(cleared) > 0 {
		payload["cleared_field_ids"] = sortedKeys(cleared)
	}
}

// ResolveHold settles a hold as its check hook. allow and correct apply the
// requester's transition, after checking the requester again: they must
// still see the Item, pass `allowed`, and the guards must hold on the fields
// as they are now. If not, the hold is denied with the reason. deny and
// error leave the Item where it is.
//
// The event records the hook as actor and the human at the root of the
// request as on_behalf_of. A hold resolves once: another resolution is
// ErrStale; the same key again returns the same result.
func (in *Internal) ResolveHold(ctx context.Context, req ResolveHoldRequest) (WriteResult, error) {
	k := in.k
	if req.IdempotencyKey == "" || req.HoldID == uuid.Nil {
		return WriteResult{}, fmt.Errorf("%w: idempotency key and hold are required", ErrInvalid)
	}
	kc, err := checkKernel(req.Principal, req.ItemID, req.Attribution)
	if err != nil {
		return WriteResult{}, err
	}
	if kc.hookID == "" {
		return WriteResult{}, fmt.Errorf("%w: only the hold's check hook resolves it, not %s", ErrInvalid, req.Principal.Actor)
	}
	if err := checkVerdict(req); err != nil {
		return WriteResult{}, err
	}
	hash, err := requestHash("resolve_hold", req)
	if err != nil {
		return WriteResult{}, err
	}

	return k.write(ctx, req.Principal, req.IdempotencyKey, hash, func(tx pgx.Tx) (WriteResult, error) {
		it, err := lockItem(ctx, tx, req.ItemID)
		if err != nil {
			return WriteResult{}, err
		}
		recorded, err := admit(ctx, tx, req.Principal, req.ItemID, it, kc)
		if err != nil {
			return WriteResult{}, err
		}
		var (
			status         string
			hook, from, to string
			requestedSeq   int64
			requester      item.Principal
		)
		err = tx.QueryRow(ctx,
			`select status, hook, from_state, to_state, requested_seq,
			        requester, coalesce(requester_on_behalf_of, ''), coalesce(requester_agent_session, '')
			 from item_holds where id = $1 and item_id = $2`, req.HoldID, req.ItemID,
		).Scan(&status, &hook, &from, &to, &requestedSeq, &requester.Actor, &requester.OnBehalfOf, &requester.AgentSession)
		if errors.Is(err, pgx.ErrNoRows) {
			return WriteResult{}, fmt.Errorf("%w: no hold %s on this Item", ErrNotFound, req.HoldID)
		}
		if err != nil {
			return WriteResult{}, fmt.Errorf("read hold: %w", err)
		}
		if hook != kc.hookID {
			return WriteResult{}, fmt.Errorf("%w: hold %s is checked by %s, not %s", ErrInvalid, req.HoldID, hook, kc.hookID)
		}
		if kc.causeSeq != requestedSeq {
			return WriteResult{}, fmt.Errorf("%w: a resolution is caused by the request (event %d), not event %d",
				ErrInvalid, requestedSeq, kc.causeSeq)
		}
		if status != item.HoldOpen {
			return WriteResult{}, fmt.Errorf("%w: hold %s is already %s", ErrStale, req.HoldID, status)
		}
		ty, err := k.loadType(ctx, tx, it.TypeID, it.TypeVersion)
		if err != nil {
			return WriteResult{}, err
		}
		if err := checkHookWrite(ty, kc, req.Fields); err != nil {
			return WriteResult{}, err
		}
		if err := ty.CheckFields(req.Fields, false); err != nil {
			return WriteResult{}, fmt.Errorf("%w: correction: %v", ErrInvalid, err)
		}

		verdict, reason := req.Verdict, req.Reason
		var set map[string]any
		merged := it.Fields
		if verdict == VerdictAllow || verdict == VerdictCorrect {
			merged, set, _ = applyPatch(it.Fields, req.Fields)
			// The hold applies the requester's transition, so it is the
			// requester who is checked again: their access and roles may
			// have changed since they asked, and the fields may have too.
			if reason, err = recheckRequester(ctx, tx, ty, it, requester, from, to, merged); err != nil {
				return WriteResult{}, err
			}
			if reason != "" {
				verdict, merged, set = VerdictDeny, it.Fields, nil
			}
		}

		itemSeq := it.ItemSeq + 1
		state := it.State
		var op, holdStatus string
		payload := map[string]any{"from": from, "hold_id": req.HoldID.String()}
		switch verdict {
		case VerdictAllow, VerdictCorrect:
			op, holdStatus, state = "transition", item.HoldAllowed, to
			payload["to"] = to
			addFieldIDs(payload, set, nil)
		case VerdictDeny:
			op, holdStatus = lifecycle.OpTransitionDenied, item.HoldDenied
			payload["requested_to"], payload["reason"] = to, reason
		case VerdictError:
			op, holdStatus = lifecycle.OpTransitionFailed, item.HoldError
			payload["requested_to"], payload["reason"] = to, reason
		}
		if _, err := tx.Exec(ctx,
			`update items set state = $2, fields = $3, item_seq = $4, updated_at = now() where id = $1`,
			req.ItemID, state, merged, itemSeq); err != nil {
			return WriteResult{}, fmt.Errorf("update item: %w", err)
		}
		res, err := appendEvent(ctx, tx, ty, req.ItemID, itemSeq, recorded, req.Attribution, op, payload, set)
		if err != nil {
			return WriteResult{}, err
		}
		if holdStatus == item.HoldAllowed {
			reason = ""
		}
		return res, closeHold(ctx, tx, req.HoldID, holdStatus, reason, itemSeq)
	})
}

// checkVerdict checks a resolution's shape before any work is done.
func checkVerdict(req ResolveHoldRequest) error {
	invalid := func(format string, args ...any) error {
		return fmt.Errorf("%w: resolve hold: %s", ErrInvalid, fmt.Sprintf(format, args...))
	}
	switch req.Verdict {
	case VerdictAllow:
		if len(req.Fields) > 0 || req.Reason != "" {
			return invalid("allow takes no fields and no reason")
		}
	case VerdictCorrect:
		if len(req.Fields) == 0 || req.Reason != "" {
			return invalid("correct takes fields and no reason")
		}
	case VerdictDeny, VerdictError:
		if len(req.Fields) > 0 {
			return invalid("%s takes no fields", req.Verdict)
		}
		if !reasonCode.MatchString(req.Reason) {
			return invalid("%s needs a reason code (lower case, digits, _), not free text", req.Verdict)
		}
	default:
		return invalid("unknown verdict %q", req.Verdict)
	}
	return nil
}

// recheckRequester asks again what was asked when the hold was requested.
// It returns the reason the transition may no longer be made, or "".
func recheckRequester(ctx context.Context, tx pgx.Tx, ty *typedef.Type, it lockedItem, requester item.Principal,
	from, to string, fields map[string]any) (string, error) {
	visible, err := policy.CanSee(ctx, tx, requester, it.ScopeID)
	if err != nil {
		return "", err
	}
	if !visible {
		return "requester_lost_access", nil
	}
	if _, err := lifecycle.Transition(ty, from, fields, to); err != nil {
		var rej *lifecycle.Rejection
		if errors.As(err, &rej) {
			return string(rej.Code), nil
		}
		return "", err
	}
	err = authorize(ctx, tx, requester, ty, from, to)
	if errors.Is(err, ErrForbidden) {
		return "forbidden", nil
	}
	return "", err
}

// HookInput returns the values a hook reads: its declared `reads` and
// nothing else. It is the only input a connector gets, so fields the hook
// does not declare (which may hold personal data) never leave the kernel.
func (in *Internal) HookInput(ctx context.Context, req HookInputRequest) (map[string]any, error) {
	k := in.k
	kc, err := checkKernel(req.Principal, req.ItemID, req.Attribution)
	if err != nil {
		return nil, err
	}
	if kc.hookID == "" {
		return nil, fmt.Errorf("%w: only a hook reads hook input, not %s", ErrInvalid, req.Principal.Actor)
	}
	it := item.Item{ID: req.ItemID}
	err = k.pool.QueryRow(ctx,
		`select type_id, type_version, scope_id, state, fields, item_seq from items where id = $1 and namespace_id = $2`,
		req.ItemID, defaultNamespace,
	).Scan(&it.TypeID, &it.TypeVersion, &it.ScopeID, &it.State, &it.Fields, &it.ItemSeq)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read item: %w", err)
	}
	if kc.typeID != it.TypeID || kc.typeVersion != it.TypeVersion {
		return nil, fmt.Errorf("%w: %s ran for %s v%d, on an Item of %s v%d",
			ErrInvalid, req.Principal.Actor, kc.typeID, kc.typeVersion, it.TypeID, it.TypeVersion)
	}
	// The cause must be an event of this Item, as admit requires of a write.
	var causeExists bool
	if err := k.pool.QueryRow(ctx,
		`select exists (select 1 from item_events where item_id = $1 and item_seq = $2)`,
		req.ItemID, kc.causeSeq).Scan(&causeExists); err != nil {
		return nil, fmt.Errorf("read cause: %w", err)
	}
	if !causeExists {
		return nil, fmt.Errorf("%w: hook input: the cause is not an event of this Item", ErrInvalid)
	}
	ty, err := k.loadType(ctx, k.pool, it.TypeID, it.TypeVersion)
	if err != nil {
		return nil, err
	}
	h, ok := ty.Hook(kc.hookID)
	if !ok {
		return nil, fmt.Errorf("%w: hook %s is not declared by %s v%d", ErrInvalid, kc.hookID, ty.ID, ty.Version)
	}
	// A hook acts on the Item that triggered it, so it is not subject to
	// visibility; Project is still the one door out. Then only the reads.
	projected, ok := policy.Project(req.Principal, []uuid.UUID{*it.ScopeID}, it)
	if !ok {
		return nil, ErrNotFound
	}
	out := map[string]any{}
	for _, id := range h.Reads {
		if v, ok := projected.Fields[id]; ok {
			out[id] = v
		}
	}
	return out, nil
}

// --- the outcome and the bounded wait ---

// heldRequest is a hold, looked up by the event that requested it.
type heldRequest struct {
	id          uuid.UUID
	status      string
	reason      string
	resolvedSeq *int64
	resolvedCS  *int64 // commit_seq of the resolving event
}

// holdKind says whether a transition's write opened a hold.
type holdKind int

const (
	holdUnknown holdKind = iota // a retry answered from the idempotency record: the write did not run here
	holdNone                    // the move had no check: it was applied, and no hold can exist
	holdOpened                  // the move was checked: the write opened a hold
)

// outcome turns a Transition's write into its answer. A move without a
// check was applied, and costs no read. A request is answered from its
// hold, waiting up to `wait` for it to resolve. A retry with the same key
// reaches here with the stored result, which does not say whether it was a
// request, so it reads the hold too, and gets the hold's current outcome.
func (k *Service) outcome(ctx context.Context, res WriteResult, kind holdKind, wait time.Duration) (TransitionResult, error) {
	if kind == holdNone {
		return TransitionResult{WriteResult: res, Outcome: OutcomeApplied}, nil
	}
	deadline := time.Now().Add(wait)
	for {
		h, err := k.holdRequestedAt(ctx, res.ItemID, res.ItemSeq)
		if err != nil {
			return TransitionResult{}, err
		}
		if h == nil {
			return TransitionResult{WriteResult: res, Outcome: OutcomeApplied}, nil
		}
		left := time.Until(deadline)
		if h.status != item.HoldOpen || left <= 0 {
			return h.result(res), nil
		}
		// Sleep, but stop early if the caller gives up. A timer channel in
		// a select is Go's way to wait on "whichever comes first".
		timer := time.NewTimer(min(holdPoll, left))
		select {
		case <-ctx.Done():
			timer.Stop()
			return TransitionResult{}, ctx.Err()
		case <-timer.C:
		}
	}
}

func (k *Service) holdRequestedAt(ctx context.Context, itemID uuid.UUID, seq int64) (*heldRequest, error) {
	var h heldRequest
	err := k.pool.QueryRow(ctx,
		`select h.id, h.status, coalesce(h.reason, ''), h.resolved_seq, e.commit_seq
		 from item_holds h
		 left join item_events e on e.item_id = h.item_id and e.item_seq = h.resolved_seq
		 where h.item_id = $1 and h.requested_seq = $2`, itemID, seq,
	).Scan(&h.id, &h.status, &h.reason, &h.resolvedSeq, &h.resolvedCS)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read hold: %w", err)
	}
	return &h, nil
}

// result answers for a hold. Once resolved, the result names the resolving
// event, so a read with min_seq = commit_seq sees the outcome.
func (h *heldRequest) result(request WriteResult) TransitionResult {
	out := TransitionResult{WriteResult: request, HoldID: h.id, Reason: h.reason}
	if h.status != item.HoldOpen && h.resolvedSeq != nil && h.resolvedCS != nil {
		out.WriteResult = WriteResult{ItemID: request.ItemID, ItemSeq: *h.resolvedSeq, CommitSeq: *h.resolvedCS}
	}
	switch h.status {
	case item.HoldOpen:
		out.Outcome = OutcomePending
	case item.HoldAllowed:
		out.Outcome = OutcomeApplied
	case item.HoldError:
		out.Outcome = OutcomeError
	case item.HoldCanceled:
		out.Outcome, out.Reason = OutcomeDenied, "canceled" // an escape left the state first
	default:
		out.Outcome = OutcomeDenied
	}
	return out
}
