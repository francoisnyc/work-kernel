// Package access is the access request: a person asks for a role on a
// system and the system's owner approves or rejects.
//
// It is built like the purchase approval (one workflow per request, state
// changed only by validated updates, every change reported to Postgres) and
// shares with it what is the same for both: the agent's preparation, the
// decision's arguments, the update and query names, and the refusal types
// the API turns into HTTP statuses.
package access

import (
	"encoding/json"
	"strings"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/francoisnyc/work-kernel/spikes/temporal-bakeoff/acts"
	"github.com/francoisnyc/work-kernel/spikes/temporal-bakeoff/purchase"
	"github.com/francoisnyc/work-kernel/spikes/temporal-bakeoff/store"
)

const (
	WorkflowName = "AccessWorkflow"
	Kind         = "access"
	IDPrefix     = "access-"

	StateSubmitted   = "submitted"
	StateOwnerReview = "owner_review"
	StateApproved    = "approved"
	StateRejected    = "rejected"
	StateFailed      = "failed"
)

// Owners says who decides for each system. The API reads it to refuse an
// unknown system; the workflow reads it once, when a request starts.
var Owners = map[string]string{
	"payroll": "user:olaf",
	"ledger":  "user:fay",
}

// Input is what the requester asked. It never changes.
type Input struct {
	ID            string `json:"id"`
	Requester     string `json:"requester"`
	System        string `json:"system"`
	Role          string `json:"role"` // the access role asked for, free text
	Justification string `json:"justification"`
}

// State is the request as the workflow knows it: the query's answer and
// the picture written to Postgres.
type State struct {
	Input
	Kind        string                `json:"kind"`
	State       string                `json:"state"`
	Owner       string                `json:"owner,omitempty"`
	Preparation *purchase.Preparation `json:"preparation"`
	Reason      string                `json:"reason,omitempty"`
	Failure     string                `json:"failure,omitempty"`
}

func refuse(kind, msg string) error { return temporal.NewApplicationError(msg, kind) }

// run is one execution's memory (see purchase: workflow code runs on one
// goroutine at a time, so no lock is needed).
type run struct {
	st     State
	outbox []store.Event // changes not yet written to Postgres
	seq    int
}

func (w *run) emit(actor, action string, detail map[string]any) {
	w.seq++
	w.outbox = append(w.outbox, store.Event{Seq: w.seq, Actor: actor, Action: action, Detail: detail})
}

func (w *run) checkPrepare(a purchase.PrepareArgs) error {
	switch {
	case !a.Actor.Has("preparer"):
		return refuse(purchase.ErrForbidden, "only the preparer writes the preparation")
	case w.st.State != StateOwnerReview:
		return refuse(purchase.ErrConflict, "the preparation is closed once the owner has decided")
	case a.Policy != "ok" && a.Policy != "check" && a.Policy != "fail":
		return refuse(purchase.ErrInvalid, "policy is ok, check or fail")
	case strings.TrimSpace(a.Summary) == "" || len(a.Summary) > 2000 || len(a.Flags) > 500:
		return refuse(purchase.ErrInvalid, "a summary is needed (2,000 characters at most; flags 500)")
	}
	return nil
}

// checkDecide is the one rule for the decision: the system's owner, a
// person, never the requester; and a rejection gives its reason.
func (w *run) checkDecide(a purchase.DecideArgs) error {
	switch {
	case w.st.State != StateOwnerReview:
		return refuse(purchase.ErrConflict, "no decision is open")
	case !a.Actor.IsUser():
		return refuse(purchase.ErrForbidden, "only a person decides")
	case a.Actor.ID == w.st.Requester:
		return refuse(purchase.ErrForbidden, "the requester does not decide their own request")
	case a.Actor.ID != w.st.Owner:
		return refuse(purchase.ErrForbidden, "only the system's owner decides")
	case !a.Approve && strings.TrimSpace(a.Reason) == "":
		return refuse(purchase.ErrInvalid, "a reason is needed to reject")
	}
	return nil
}

// register wires the query and the updates. A validator runs before an
// update is accepted: a refused update never enters the workflow's history.
func (w *run) register(ctx workflow.Context) error {
	if err := workflow.SetQueryHandler(ctx, purchase.QueryState, func() (State, error) { return w.st, nil }); err != nil {
		return err
	}
	err := workflow.SetUpdateHandlerWithOptions(ctx, purchase.UpdatePrepare, func(_ workflow.Context, a purchase.PrepareArgs) error {
		w.st.Preparation = &purchase.Preparation{Summary: a.Summary, Policy: a.Policy, Flags: a.Flags, By: a.Actor.ID}
		w.emit(a.Actor.ID, "prepared", map[string]any{"policy": a.Policy, "flags": a.Flags})
		return nil
	}, workflow.UpdateHandlerOptions{Validator: w.checkPrepare})
	if err != nil {
		return err
	}
	return workflow.SetUpdateHandlerWithOptions(ctx, purchase.UpdateDecide, func(_ workflow.Context, a purchase.DecideArgs) error {
		w.st.State = StateApproved
		if !a.Approve {
			w.st.State, w.st.Reason = StateRejected, strings.TrimSpace(a.Reason)
		}
		w.emit(a.Actor.ID, "decided", map[string]any{"step": StateOwnerReview, "approve": a.Approve, "reason": w.st.Reason, "to": w.st.State})
		return nil
	}, workflow.UpdateHandlerOptions{Validator: w.checkDecide})
}

// row is the request as Postgres holds it: who it waits on, who may see it.
func (w *run) row() store.Row {
	data, _ := json.Marshal(w.st) // a struct of strings: cannot fail
	r := store.Row{ID: w.st.ID, Kind: Kind, Requester: w.st.Requester, State: w.st.State,
		PrepareOpen: w.st.State == StateOwnerReview, Data: data,
		Readers: []string{w.st.Requester, "role:preparer"}, WaitingOn: []string{}}
	if w.st.Owner != "" {
		r.Readers = append(r.Readers, w.st.Owner)
	}
	if w.st.State == StateOwnerReview {
		r.WaitingOn = []string{w.st.Owner}
	}
	return r
}

// flush writes every pending change to Postgres, in order.
func (w *run) flush(actx workflow.Context) error {
	var a *acts.Activities // only names the activity; never dereferenced here
	for len(w.outbox) > 0 {
		ev := w.outbox[0]
		w.outbox = w.outbox[1:]
		if err := workflow.ExecuteActivity(actx, a.Record, w.row(), &ev).Get(actx, nil); err != nil {
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

// Workflow runs one access request from submission to its end.
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

	// The owner comes from the list, never from the requester. SideEffect
	// writes the answer into the workflow's history, so a replay after the
	// list has changed still finds the owner this request started with
	// (workflow code must give the same answer every time it is replayed).
	var owner string
	if err := workflow.SideEffect(ctx, func(workflow.Context) any { return Owners[in.System] }).Get(&owner); err != nil {
		return w.st, err
	}
	switch {
	case !strings.HasPrefix(owner, "user:"): // also an unknown system: a missing key reads as ""
		return w.fail(actx, "no owner is known for the system")
	case owner == in.Requester:
		return w.fail(actx, "the requester owns the system: nobody else could decide")
	}
	w.st.Owner, w.st.State = owner, StateOwnerReview
	w.emit(in.Requester, "submitted", map[string]any{"system": in.System, "owner": owner})

	for {
		if err := w.flush(actx); err != nil {
			return w.st, err
		}
		switch w.st.State {
		case StateApproved:
			return w.st, nil // granting the access is someone else's job
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
