package itemservice

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/item"
)

// Jira part 2, step 3: reactions that move children or set a field on the
// container, a create reaction's reads of linked Items, and acting timers
// at an instant.
//
// PHASE B: declarations only, so the failing tests compile. Every
// operation refuses and writes nothing.

// MaxReactionTargets is the most contained Items one reaction moves for
// one trigger. More match: nothing is moved, and the reaction fails with
// CodeTooManyTargets.
const MaxReactionTargets = 8

// Why a step 3 reaction failed.
const (
	CodeTooManyTargets  = "too_many_targets"  // more children match than MaxReactionTargets
	CodeNoContainer     = "no_container"      // the source is contained in nothing
	CodeReadNotAccepted = "read_not_accepted" // the linked Item's Type version does not let reactions read the field
)

// ReactSet runs a set reaction for one trigger, like ReactMove: it sets the
// reaction's fields on the source's container in one write, under the key
// <reaction>:<source>:<cause>:set, then records the outcome on the source.
func (in *Internal) ReactSet(ctx context.Context, req ReactionRunRequest) (ReactionOutcome, error) {
	return ReactionOutcome{}, fmt.Errorf("%w: set reactions are not built yet", ErrInvalid)
}

// ActingInstant is what the workflow may know of one acting timer at an
// instant: the instant its field holds, if any.
type ActingInstant struct {
	Timer string
	Set   bool
	At    time.Time
}

// ActingInstants reads, for the Item's current state, the instant of each
// acting timer at an instant, in the Type's order, and nothing else. A
// kernel read for the worker only, like AtInstant.
func (k *Service) ActingInstants(ctx context.Context, id uuid.UUID) ([]ActingInstant, error) {
	return nil, fmt.Errorf("%w: acting timers at an instant are not built yet", ErrInvalid)
}

// ActAtRequest makes an acting timer at an instant act.
type ActAtRequest struct {
	ItemID uuid.UUID
	Timer  string
	// EnteredSeq is the event that entered the state the timer belongs to.
	EnteredSeq int64
	// Instant is the instant the timer was armed for: the value its field
	// held when the workflow read it.
	Instant time.Time
	// Attribution: the entering event's correlation ID, and its hop + 1.
	// The item service sets the cause (the entering event) and the
	// originating timer itself.
	Attribution item.Attribution
}

// ActAtResult is what ActAt did.
type ActAtResult struct {
	WriteResult
	// Applied is false when nothing was written: the timer's `when` did
	// not hold, or it has no `sets`.
	Applied bool
	// RunHook is true when the timer declares a hook and it should now
	// run: its `when` held (whether or not it has sets).
	RunHook bool
}

// ActAt applies an acting timer at an instant: in one write by
// system:timer, without a transition, it sets the timer's `sets`, each
// evaluated with cause.at = Instant. The key is
// timer:<item>:<timer>:<entered seq>:<instant>.
//
//   - Before the instant, by the write's own time: ErrNotYet with the
//     remainder, nothing written, the key not used up.
//   - The Item left the state, was entered again since, or the field no
//     longer holds Instant: ErrStale, nothing written.
//   - `when` does not hold: nothing written, Applied false.
func (in *Internal) ActAt(ctx context.Context, req ActAtRequest) (ActAtResult, error) {
	return ActAtResult{}, fmt.Errorf("%w: acting timers at an instant are not built yet", ErrInvalid)
}
