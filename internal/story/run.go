package story

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Result is what happened when a story ran.
type Result struct {
	Timeline []Line    // every event and side effect, in story time, in time order
	Calls    []Call    // every call a fake answered (or refused to), in order
	Failures []Failure // what went wrong in the story itself; the run stops at the first
	Ended    time.Time // the instant the run stopped
	// Runs counts the workflow runs each Item had (a continue-as-new
	// starts one), and ContinuedAsNew the continues-as-new, by Item name.
	Runs           map[string]int
	ContinuedAsNew map[string]int
}

// Failure is something that went wrong in the story itself: a step refused
// that was not expected to be (or the reverse), a read that saw something
// else, a call no fake rule answers, a delivery the harness could not make.
type Failure struct {
	Line    int // the story line it belongs to; 0 if none
	Message string
}

// Call is one call of an action, as a fake saw it.
type Call struct {
	At         time.Time
	Action     string // name@major
	ItemID     uuid.UUID
	Hook       string
	Inputs     map[string]any
	Outputs    map[string]any // what the fake answered with
	Answer     AnswerKind     // empty: no rule matched
	SideEffect bool           // the action has side effects, and succeeded
}

// Run checks a story (Check's problems are its error) and runs it.
func Run(ctx context.Context, env *Env, s *Story) (*Result, error) {
	return run(ctx, env, s, runOptions{})
}

// runOptions are for this package's tests only.
type runOptions struct {
	// failActivity makes the first n attempts of an activity, by name,
	// fail with a retryable error.
	failActivity map[string]int
	// afterStep runs after step i's write, before what it caused is
	// delivered, outside the workflow environment's main loop.
	afterStep func(ctx context.Context, h *harness, i int) error
}
