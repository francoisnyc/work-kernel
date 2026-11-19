package itemservice

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/lifecycle"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// Timers at a field's instant (Jira part 2, step 1): what the item service
// gives the workflow, and what it refuses it.

// timerActor is the principal every timer's move is made as.
const timerActor = typedef.TimerPrincipal

// AtInstant is what the workflow may know of an Item's timer at a field's
// instant: the instant held by the field its current state names in `at`.
// It is the one field value that reaches the workflow (docs/design.md).
type AtInstant struct {
	State   string
	ItemSeq int64
	// Set is false when the state declares no `at`, or its field is unset.
	Set bool
	At  time.Time
}

// AtInstant reads the instant of the field the Item's current state names
// in `at`, and nothing else: the caller names no field, so it can ask for
// no other. A kernel read, for the worker only; it is not subject to
// visibility, like StateEntry. ErrNotFound if the Item does not exist.
func (k *Service) AtInstant(ctx context.Context, id uuid.UUID) (AtInstant, error) {
	var (
		out         AtInstant
		typeID      string
		typeVersion int
		fields      map[string]any
	)
	err := k.pool.QueryRow(ctx,
		`select type_id, type_version, state, item_seq, fields from items where id = $1`, id,
	).Scan(&typeID, &typeVersion, &out.State, &out.ItemSeq, &fields)
	if errors.Is(err, pgx.ErrNoRows) {
		return AtInstant{}, ErrNotFound
	}
	if err != nil {
		return AtInstant{}, fmt.Errorf("read item: %w", err)
	}
	ty, err := k.loadType(ctx, k.pool, typeID, typeVersion)
	if err != nil {
		return AtInstant{}, err
	}
	timer, ok := lifecycle.AtTimerFor(ty, out.State)
	if !ok {
		return out, nil
	}
	out.At, out.Set = typedef.ParseDateTime(fields[timer.Field])
	return out, nil
}

// checkAtTimer judges a timer's move out of a state with a timer at a
// field's instant: it never applies before the instant. at is the write's
// time, the time its event would carry.
//
//   - Still before the instant: ErrNotYet with the remainder. The worker's
//     clock ran ahead of this one; the workflow arms again.
//   - The field is unset: there is no instant to have passed, so the
//     timer's picture is old. ErrStale: the workflow looks again.
//
// Anything else is not this rule's business: another principal (a person
// closing by hand), a state without `at`, a move the `at` does not name.
func checkAtTimer(ty *typedef.Type, p item.Principal, it lockedItem, to string, at time.Time) error {
	if p.Actor != timerActor {
		return nil
	}
	timer, ok := lifecycle.AtTimerFor(ty, it.State)
	if !ok || timer.To != to {
		return nil
	}
	instant, set := typedef.ParseDateTime(it.Fields[timer.Field])
	if !set {
		return fmt.Errorf("%w: %s is not set; no timer is armed in state %q", ErrStale, timer.Field, it.State)
	}
	if remaining := instant.Sub(at); remaining > 0 {
		return &NotYetError{Remaining: remaining}
	}
	return nil
}
