// Package purchase is the purchase approval, written directly on Temporal.
//
// One workflow per request. The workflow is the source of truth: its
// state changes only through its own update handlers, each guarded by a
// validator, and it reports every change to Postgres (the inbox and the
// audit trail) through an activity. What was asked (amount, vendor, cost
// centre, justification) is the workflow's input and no handler changes it.
package purchase

import (
	"encoding/json"
	"strings"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/francoisnyc/work-kernel/spikes/temporal-bakeoff/acts"
	"github.com/francoisnyc/work-kernel/spikes/temporal-bakeoff/auth"
	"github.com/francoisnyc/work-kernel/spikes/temporal-bakeoff/store"
)

const (
	WorkflowName = "PurchaseWorkflow"
	Kind         = "purchase"
	IDPrefix     = "purchase-"

	QueryState    = "state"
	UpdatePrepare = "prepare"
	UpdateClaim   = "claim"
	UpdateRelease = "release"
	UpdateDecide  = "decide"

	StateSubmitted     = "submitted"
	StateManagerReview = "manager_review"
	StateFinanceQueue  = "finance_queue"
	StateFinanceReview = "finance_review"
	StateCFOReview     = "cfo_review"
	StateApproved      = "approved"
	StateRejected      = "rejected"
	StateFailed        = "failed"
	StateOrderFailed   = "order_failed" // approved, but the purchase system refused the order: finance looks at it

	// From this amount, finance decides after the manager.
	FinanceFrom = 1000
	// Over this amount, the CFO decides after finance (the owner's change).
	CFOOver = 5000
	// The agent may rewrite its preparation this many times. Temporal accepts
	// at most 2,000 updates per workflow; without a cap the agent could use
	// them all and no decision could reach the request (found in review).
	MaxPreparations = 20
)

// Input is what the requester asked. It never changes.
type Input struct {
	ID            string `json:"id"`
	Requester     string `json:"requester"`
	Amount        int64  `json:"amount"`
	Vendor        string `json:"vendor"`
	CostCentre    string `json:"cost_centre"`
	Justification string `json:"justification"`
}

// Preparation is what the agent proposes. It decides nothing.
type Preparation struct {
	Summary string `json:"summary"`
	Policy  string `json:"policy"` // ok, check or fail
	Flags   string `json:"flags"`
	By      string `json:"by"`
}

// State is the request as the workflow knows it: the query's answer and
// the picture written to Postgres.
type State struct {
	Input
	Kind        string       `json:"kind"`
	State       string       `json:"state"`
	Manager     string       `json:"manager,omitempty"`
	ClaimedBy   string       `json:"claimed_by,omitempty"`
	Preparation *Preparation `json:"preparation"`
	Reason      string       `json:"reason,omitempty"`
	OrderID     string       `json:"order_id,omitempty"`
	Failure     string       `json:"failure,omitempty"`
}

// The updates' arguments. The API fills Actor from the caller's token.
type (
	PrepareArgs struct {
		Actor   auth.Principal `json:"actor"`
		Summary string         `json:"summary"`
		Policy  string         `json:"policy"`
		Flags   string         `json:"flags"`
	}
	ActorArgs struct {
		Actor auth.Principal `json:"actor"`
	}
	DecideArgs struct {
		Actor   auth.Principal `json:"actor"`
		Approve bool           `json:"approve"`
		Reason  string         `json:"reason"`
	}
)

// Refusals carry a type the API turns into an HTTP status.
const (
	ErrForbidden = "FORBIDDEN"
	ErrConflict  = "CONFLICT"
	ErrInvalid   = "INVALID"
)

func refuse(kind, msg string) error { return temporal.NewApplicationError(msg, kind) }

// run is one execution's memory. Workflow code runs on one goroutine at a
// time, so no lock is needed (Temporal's rule, unlike ordinary Go).
type run struct {
	st     State
	outbox []store.Event // changes not yet written to Postgres
	seq    int
	preps  int // preparations accepted so far
}

func (w *run) emit(actor, action string, detail map[string]any) {
	w.seq++
	w.outbox = append(w.outbox, store.Event{Seq: w.seq, Actor: actor, Action: action, Detail: detail})
}

// decider is the one rule for a person's step: a person, never the
// requester, and the named principal or a holder of the role.
func decider(actor auth.Principal, requester, principal, role string) error {
	switch {
	case !actor.IsUser():
		return refuse(ErrForbidden, "only a person decides")
	case actor.ID == requester:
		return refuse(ErrForbidden, "the requester does not decide their own request")
	case principal != "" && actor.ID != principal:
		return refuse(ErrForbidden, "this step is someone else's")
	case role != "" && !actor.Has(role):
		return refuse(ErrForbidden, "this step needs the role "+role)
	}
	return nil
}

func (w *run) checkPrepare(a PrepareArgs) error {
	switch {
	case !a.Actor.Has("preparer"):
		return refuse(ErrForbidden, "only the preparer writes the preparation")
	case w.st.State != StateManagerReview:
		return refuse(ErrConflict, "the preparation is closed once the manager has decided")
	case w.preps >= MaxPreparations:
		return refuse(ErrConflict, "the preparation has been rewritten too often")
	case a.Policy != "ok" && a.Policy != "check" && a.Policy != "fail":
		return refuse(ErrInvalid, "policy is ok, check or fail")
	case strings.TrimSpace(a.Summary) == "" || len(a.Summary) > 2000 || len(a.Flags) > 500:
		return refuse(ErrInvalid, "a summary is needed (2,000 characters at most; flags 500)")
	}
	return nil
}

func (w *run) checkClaim(a ActorArgs) error {
	if w.st.State != StateFinanceQueue {
		return refuse(ErrConflict, "it is not in the finance queue")
	}
	return decider(a.Actor, w.st.Requester, "", "finance")
}

// checkRelease: any finance member may hand a claimed purchase back to the
// queue, so one absent member cannot hold it (found in review).
func (w *run) checkRelease(a ActorArgs) error {
	if w.st.State != StateFinanceReview {
		return refuse(ErrConflict, "nobody has claimed it")
	}
	return decider(a.Actor, w.st.Requester, "", "finance")
}

func (w *run) checkDecide(a DecideArgs) error {
	var err error
	switch w.st.State {
	case StateManagerReview:
		err = decider(a.Actor, w.st.Requester, w.st.Manager, "")
	case StateFinanceReview:
		err = decider(a.Actor, w.st.Requester, w.st.ClaimedBy, "finance")
	case StateCFOReview:
		err = decider(a.Actor, w.st.Requester, "", "cfo")
	default:
		return refuse(ErrConflict, "no decision is open")
	}
	if err == nil && !a.Approve && strings.TrimSpace(a.Reason) == "" {
		return refuse(ErrInvalid, "a reason is needed to reject")
	}
	return err
}

func (w *run) decide(ctx workflow.Context, a DecideArgs) {
	step := w.st.State
	switch {
	case !a.Approve:
		w.st.State, w.st.Reason = StateRejected, strings.TrimSpace(a.Reason)
	case step == StateManagerReview && w.st.Amount >= FinanceFrom:
		w.st.State = StateFinanceQueue
	case step == StateFinanceReview && w.st.Amount > CFOOver && cfoStepApplies(ctx):
		w.st.State = StateCFOReview
	default:
		w.st.State = StateApproved
	}
	w.emit(a.Actor.ID, "decided", map[string]any{"step": step, "approve": a.Approve, "reason": w.st.Reason, "to": w.st.State})
}

// register wires the query and the updates. A validator runs before an
// update is accepted: a refused update never enters the workflow's history.
func (w *run) register(ctx workflow.Context) error {
	if err := workflow.SetQueryHandler(ctx, QueryState, func() (State, error) { return w.st, nil }); err != nil {
		return err
	}
	set := func(name string, handler, validator any) error {
		return workflow.SetUpdateHandlerWithOptions(ctx, name, handler, workflow.UpdateHandlerOptions{Validator: validator})
	}
	if err := set(UpdatePrepare, func(_ workflow.Context, a PrepareArgs) error {
		w.preps++
		w.st.Preparation = &Preparation{Summary: a.Summary, Policy: a.Policy, Flags: a.Flags, By: a.Actor.ID}
		w.emit(a.Actor.ID, "prepared", map[string]any{"policy": a.Policy, "flags": a.Flags})
		return nil
	}, w.checkPrepare); err != nil {
		return err
	}
	if err := set(UpdateClaim, func(_ workflow.Context, a ActorArgs) error {
		w.st.State, w.st.ClaimedBy = StateFinanceReview, a.Actor.ID
		w.emit(a.Actor.ID, "claimed", nil)
		return nil
	}, w.checkClaim); err != nil {
		return err
	}
	if err := set(UpdateRelease, func(_ workflow.Context, a ActorArgs) error {
		w.st.State, w.st.ClaimedBy = StateFinanceQueue, ""
		w.emit(a.Actor.ID, "released", nil)
		return nil
	}, w.checkRelease); err != nil {
		return err
	}
	return set(UpdateDecide, func(ctx workflow.Context, a DecideArgs) error {
		w.decide(ctx, a)
		return nil
	}, w.checkDecide)
}

// row is the request as Postgres holds it: who it waits on, who may see it.
func (w *run) row() store.Row {
	data, _ := json.Marshal(w.st) // a struct of strings and numbers: cannot fail
	r := store.Row{ID: w.st.ID, Kind: Kind, Requester: w.st.Requester, State: w.st.State, ClaimedBy: w.st.ClaimedBy,
		PrepareOpen: w.st.State == StateManagerReview, Data: data,
		Readers: []string{w.st.Requester, "role:finance", "role:cfo", "role:preparer"}, WaitingOn: []string{}}
	if w.st.Manager != "" {
		r.Readers = append(r.Readers, w.st.Manager)
	}
	switch w.st.State {
	case StateManagerReview:
		r.WaitingOn = []string{w.st.Manager}
	case StateFinanceQueue, StateFinanceReview, StateOrderFailed:
		r.WaitingOn = []string{"role:finance"}
	case StateCFOReview:
		r.WaitingOn = []string{"role:cfo"}
	}
	return r
}

// flush writes every pending change to Postgres, in order. The write is
// retried until it succeeds: a Postgres outage delays the inbox, it never
// ends the request (found in review: a capped retry could).
func (w *run) flush(ctx workflow.Context) error {
	var a *acts.Activities
	rctx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 30 * time.Second,
		RetryPolicy:         &temporal.RetryPolicy{InitialInterval: time.Second, MaximumInterval: time.Minute},
	})
	for len(w.outbox) > 0 {
		ev := w.outbox[0]
		w.outbox = w.outbox[1:]
		if err := workflow.ExecuteActivity(rctx, a.Record, w.row(), &ev).Get(rctx, nil); err != nil {
			return err
		}
	}
	return nil
}

func (w *run) fail(actx workflow.Context, why string) (State, error) {
	w.st.State, w.st.Failure = StateFailed, why
	w.emit("system", "failed", map[string]any{"why": why})
	return w.st, w.flush(actx)
}

// Workflow runs one purchase request from submission to its end.
func Workflow(ctx workflow.Context, in Input) (State, error) {
	w := &run{st: State{Input: in, Kind: Kind, State: StateSubmitted}}
	if err := w.register(ctx); err != nil {
		return w.st, err
	}
	actx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 30 * time.Second,
		RetryPolicy:         &temporal.RetryPolicy{InitialInterval: time.Second, MaximumAttempts: 8},
	})
	var a *acts.Activities // only names the activities; never dereferenced here

	// The manager comes from the HR directory, never from the requester.
	var manager string
	if err := workflow.ExecuteActivity(actx, a.ManagerOf, in.Requester).Get(ctx, &manager); err != nil {
		return w.fail(actx, "no manager was found for the requester")
	}
	if manager == in.Requester {
		return w.fail(actx, "the requester has no manager but themselves")
	}
	w.st.Manager, w.st.State = manager, StateManagerReview
	w.emit(in.Requester, "submitted", map[string]any{"manager": manager})

	for {
		if err := w.flush(actx); err != nil {
			return w.st, err
		}
		switch w.st.State {
		case StateApproved:
			// The order carries the workflow's own amount and vendor, and a key
			// that makes a retry return the first order.
			if err := workflow.ExecuteActivity(actx, a.PlaceOrder, in.ID, in.Vendor, in.Amount).Get(ctx, &w.st.OrderID); err != nil {
				// Approved, but no order: say so where finance will see it,
				// rather than end with the request still reading "approved".
				w.st.State, w.st.Failure = StateOrderFailed, "the purchase system refused the order: "+err.Error()
				w.emit("system", "order_failed", map[string]any{"error": err.Error()})
				return w.st, w.flush(actx)
			}
			w.emit("system", "ordered", map[string]any{"order_id": w.st.OrderID})
			return w.st, w.flush(actx)
		case StateRejected:
			if err := workflow.ExecuteActivity(actx, a.Notify, "rejected-"+in.ID, in.Requester, "notify_rejected", in.ID).Get(ctx, nil); err != nil {
				return w.st, err
			}
			w.emit("system", "requester_told", nil)
			return w.st, w.flush(actx)
		}
		// Wait for the next accepted update.
		if err := workflow.Await(ctx, func() bool { return len(w.outbox) > 0 }); err != nil {
			return w.st, err
		}
	}
}
