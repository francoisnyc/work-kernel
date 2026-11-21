package itemservice

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// MaxHop is the deepest a chain of kernel writes may go. A write one hop
// deeper is rejected ("hop limit"), so hooks that trigger each other stop.
const MaxHop = 8

// Internal is the item service's door for the kernel's own actors:
// system:timer and hooks (hook:<type>:<hook-id>). It is the only way to
// write as one of them, or to set originating_hook, causation_id and hop.
//
// A kernel write acts on the Item that triggered it, so it is not subject to
// visibility (docs/design.md, Policy, "Boundary"). It is subject to
// `allowed`. It changes fields and state only, never policy or container.
//
// Only the worker and the hook runner may call Service.Internal(); a test
// (internal_door_test.go) reads the module's source to enforce it, because
// Go cannot restrict an exported method to some packages of a module.
type Internal struct {
	k *Service
}

// Internal returns the kernel's write door.
func (k *Service) Internal() *Internal {
	return &Internal{k: k}
}

// Update applies a field patch as a kernel actor.
func (in *Internal) Update(ctx context.Context, req UpdateRequest) (WriteResult, error) {
	if req.Policy != nil || req.Move != nil {
		return WriteResult{}, fmt.Errorf("%w: a kernel write changes fields and state only, "+
			"not policy or container", ErrInvalid)
	}
	kc, err := checkKernel(req.Principal, req.ItemID, req.Attribution)
	if err != nil {
		return WriteResult{}, err
	}
	if err := kc.notReaction("Update"); err != nil {
		return WriteResult{}, err
	}
	if req.Principal, err = in.k.inItemsNamespace(ctx, req.Principal, req.ItemID); err != nil {
		return WriteResult{}, err
	}
	return in.k.update(ctx, req, kc)
}

// Transition moves an Item as a kernel actor.
func (in *Internal) Transition(ctx context.Context, req TransitionRequest) (WriteResult, error) {
	kc, err := checkKernel(req.Principal, req.ItemID, req.Attribution)
	if err != nil {
		return WriteResult{}, err
	}
	if err := kc.notReaction("Transition"); err != nil {
		return WriteResult{}, err
	}
	if req.Principal, err = in.k.inItemsNamespace(ctx, req.Principal, req.ItemID); err != nil {
		return WriteResult{}, err
	}
	res, _, err := in.k.transition(ctx, req, kc) // a kernel actor never opens a hold
	return res, err
}

// inItemsNamespace returns the kernel actor p placed in the namespace of the
// Item it acts on. A timer or a hook belongs to no namespace: it acts where
// its Item is, and the internal door finds the Item by ID alone. A missing
// Item is ErrNotFound.
func (k *Service) inItemsNamespace(ctx context.Context, p item.Principal, id uuid.UUID) (item.Principal, error) {
	err := k.pool.QueryRow(ctx, `select namespace_id from items where id = $1`, id).Scan(&p.Namespace)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, ErrNotFound
	}
	if err != nil {
		return p, fmt.Errorf("read the Item's namespace: %w", err)
	}
	return p, nil
}

// kernelCause is what a kernel write's attribution says, once checked: the
// event that caused it, and the Type version the timer or hook ran for.
type kernelCause struct {
	causeSeq    int64
	typeID      string
	typeVersion int
	hookID      string // the hook acting; empty for the timer and a reaction
	reactionID  string // the reaction acting (Step 2, part 2); empty otherwise
	// move is set for a move reaction's transition (Jira part 2, step 2):
	// see moveCause. Its cause may be an event of another Item, and the
	// Item moved may be of another Type: ReactMove has checked both.
	move *moveCause
}

// decider is the hook or reaction acting: what a hold names as deciding it.
func (kc *kernelCause) decider() string {
	if kc.reactionID != "" {
		return kc.reactionID
	}
	return kc.hookID
}

// notReaction refuses a reaction on the internal door's operations other
// than React, Undo, ReactionFailed and its own hold's ResolveHold.
func (kc *kernelCause) notReaction(op string) error {
	if kc.reactionID != "" {
		return fmt.Errorf("%w: a reaction writes only through React, Undo and its hold's ResolveHold, not %s",
			ErrInvalid, op)
	}
	return nil
}

// checkKernel checks a kernel write before any work is done. Everything here
// is ErrInvalid (the caller is broken) except the hop limit, which is
// ErrRejected: the chain is well formed but has gone too deep.
func checkKernel(p item.Principal, itemID uuid.UUID, a item.Attribution) (*kernelCause, error) {
	invalid := func(format string, args ...any) (*kernelCause, error) {
		return nil, fmt.Errorf("%w: kernel write: "+format, append([]any{ErrInvalid}, args...)...)
	}
	hookType, hookID, isHook := item.HookParts(p.Actor)
	reactionType, reactionID, isReaction := item.ReactionParts(p.Actor)
	if p.Actor != typedef.TimerPrincipal && !isHook && !isReaction {
		return invalid("the internal door is for system:timer, hook:<type>:<hook-id> and "+
			"reaction:<type>:<id>, not %q", p.Actor)
	}
	if p.OnBehalfOf != "" {
		return invalid("on_behalf_of is filled by the kernel from the cause, not by the caller")
	}
	if p.AgentSession != "" || a.AgentSession != "" {
		return invalid("a kernel actor has no agent session")
	}

	kc := &kernelCause{}
	switch {
	case isReaction:
		// reaction:<type>@v<version>:<id>, naming the reaction that is acting.
		t, v, r, ok := parseOrigin(item.KindReaction, a.OriginatingHook)
		if !ok || t != reactionType || r != reactionID {
			return invalid("originating_hook %q must be reaction:%s@v<version>:%s", a.OriginatingHook, reactionType, reactionID)
		}
		kc.typeID, kc.typeVersion, kc.reactionID = t, v, r
	case isHook:
		// hook:<type>@v<version>:<hook-id>, naming the hook that is acting.
		t, v, h, ok := parseOrigin(item.KindHook, a.OriginatingHook)
		if !ok || t != hookType || h != hookID {
			return invalid("originating_hook %q must be hook:%s@v<version>:%s", a.OriginatingHook, hookType, hookID)
		}
		kc.typeID, kc.typeVersion, kc.hookID = t, v, h
	default:
		// timer:<type>@v<version>:<state>, the state the timer was armed in.
		t, v, _, ok := parseOrigin("timer", a.OriginatingHook)
		if !ok {
			return invalid("the timer's originating_hook must be timer:<type>@v<version>:<state>, not %q",
				a.OriginatingHook)
		}
		kc.typeID, kc.typeVersion = t, v
	}

	// causation_id is "<item>:<seq>", and the item is this one: a kernel
	// actor acts only on the Item that triggered it. (A reaction writes on
	// another Item, the target, caused by an event of the source: React
	// and Undo pass the source here and check the target themselves.)
	idPart, seqPart, _ := strings.Cut(a.CausationID, ":")
	causeItem, err := uuid.Parse(idPart)
	if err != nil {
		return invalid("causation_id %q must be <item>:<seq>", a.CausationID)
	}
	seq, err := strconv.ParseInt(seqPart, 10, 64)
	if err != nil || seq < 1 {
		return invalid("causation_id %q must be <item>:<seq>", a.CausationID)
	}
	if causeItem != itemID {
		return invalid("caused by an event of another Item")
	}
	kc.causeSeq = seq

	if a.Hop < 1 {
		return invalid("a kernel write is at least one hop from its cause")
	}
	if a.Hop > MaxHop {
		return nil, rejected(CodeHopLimit, "hop limit: hop %d is deeper than %d", a.Hop, MaxHop)
	}
	return kc, nil
}

// parseOrigin splits an originating_hook, "<kind>:<type>@v<version>:<name>",
// where kind is "hook" (name: the hook ID) or "timer" (name: the state).
func parseOrigin(kind, o string) (typeID string, version int, name string, ok bool) {
	rest, found := strings.CutPrefix(o, kind+":")
	if !found {
		return "", 0, "", false
	}
	head, name, found := strings.Cut(rest, ":")
	if !found || name == "" {
		return "", 0, "", false
	}
	typeID, v, found := strings.Cut(head, "@v")
	if !found || typeID == "" {
		return "", 0, "", false
	}
	version, err := strconv.Atoi(v)
	if err != nil || version < 1 {
		return "", 0, "", false
	}
	return typeID, version, name, true
}

// admit is the door's check under the Item's row lock. It returns the
// principal the event is recorded as.
//
// The public door checks that the principal can see the Item. The internal
// door skips visibility; instead it checks that the timer or hook ran for
// this Item's Type and version, and that the cause exists. It records as on_behalf_of the
// human at the root of the cause: the cause's on_behalf_of, else its actor if
// that is a user, else nobody. That is attribution only, never checked: a
// hook must not fail because that human has since lost access.
func admit(ctx context.Context, tx pgx.Tx, p item.Principal, id uuid.UUID, it lockedItem,
	kc *kernelCause) (item.Principal, error) {
	if kc == nil {
		if err := checkVisible(ctx, tx, p, it.ScopeID); err != nil {
			return p, err
		}
		// Outside an agent session's scopes the Item does not exist for it;
		// inside, a read-only session may not write.
		if err := checkInSession(ctx, tx, p, &id); err != nil {
			return p, err
		}
		return p, checkWritePower(ctx, tx, p)
	}
	if kc.move != nil {
		return kc.move.recorded, nil
	}
	if kc.typeID != it.TypeID || kc.typeVersion != it.TypeVersion {
		return item.Principal{}, fmt.Errorf("%w: kernel write: %s ran for %s v%d, on an Item of %s v%d",
			ErrInvalid, p.Actor, kc.typeID, kc.typeVersion, it.TypeID, it.TypeVersion)
	}
	var actor, onBehalfOf string
	err := tx.QueryRow(ctx,
		`select actor, coalesce(on_behalf_of, '') from item_events where item_id = $1 and item_seq = $2`,
		id, kc.causeSeq).Scan(&actor, &onBehalfOf)
	if errors.Is(err, pgx.ErrNoRows) {
		return item.Principal{}, fmt.Errorf("%w: kernel write: the cause is not an event of this Item", ErrInvalid)
	}
	if err != nil {
		return item.Principal{}, fmt.Errorf("read cause: %w", err)
	}
	root := onBehalfOf
	if root == "" && item.IsKind(actor, item.KindUser, item.KindCustomer) {
		root = actor
	}
	return item.Principal{Actor: p.Actor, OnBehalfOf: root}, nil
}
