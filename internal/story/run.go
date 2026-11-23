package story

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/francoisnyc/work-kernel/internal/contracts"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// Env is what runs share: the database, with every Type of a Types folder
// registered, and the action contracts.
type Env struct {
	pool    *pgxpool.Pool
	types   []*typedef.Type
	catalog *contracts.Catalog
}

// NewEnv registers every Type of typesDir (read as the item service reads
// its folder) in the database behind pool.
func NewEnv(ctx context.Context, pool *pgxpool.Pool, typesDir string) (*Env, error) {
	_, _, _ = ctx, pool, typesDir
	return nil, errNotBuilt
}

// Types are the Types a story may use.
func (e *Env) Types() []*typedef.Type { return e.types }

// Catalog is the action contracts fakes are held to.
func (e *Env) Catalog() *contracts.Catalog { return e.catalog }

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

// harness is one run.
type harness struct {
	env *Env
	ids map[string]uuid.UUID // Item name -> ID
}

// id is the ID of the Item the story names name.
func (h *harness) id(name string) uuid.UUID { return h.ids[name] }

func run(ctx context.Context, env *Env, s *Story, opts runOptions) (*Result, error) {
	_, _, _, _ = ctx, env, s, opts
	return nil, errNotBuilt
}
