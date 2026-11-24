package purchase

import (
	"net/http"
	"strings"

	"github.com/google/uuid"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"

	"github.com/francoisnyc/work-kernel/spikes/temporal-bakeoff/auth"
	"github.com/francoisnyc/work-kernel/spikes/temporal-bakeoff/httpapi"
)

// TaskQueue is the one queue this team's worker serves.
const TaskQueue = "approvals"

// API is the purchase approval's HTTP side. It signs the caller in, checks
// the shape of the request, and hands over to the workflow, whose
// validators decide who may do what: the API holds no approval rule.
type API struct {
	Temporal client.Client
}

func (a *API) Routes(mux *http.ServeMux, t auth.Tokens) {
	mux.HandleFunc("POST /purchases", t.Require(a.submit))
	mux.HandleFunc("POST /purchases/{id}/preparation", t.Require(a.prepare))
	mux.HandleFunc("POST /purchases/{id}/claim", t.Require(a.actor(UpdateClaim)))
	mux.HandleFunc("POST /purchases/{id}/release", t.Require(a.actor(UpdateRelease)))
	mux.HandleFunc("POST /purchases/{id}/decision", t.Require(a.decide))
}

func (a *API) submit(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	var body struct {
		Amount        int64  `json:"amount"`
		Vendor        string `json:"vendor"`
		CostCentre    string `json:"cost_centre"`
		Justification string `json:"justification"`
	}
	if !httpapi.Decode(w, r, &body) {
		return
	}
	switch {
	case !p.IsUser():
		httpapi.Refuse(w, http.StatusForbidden, "only a person asks for a purchase")
		return
	case body.Amount <= 0 || strings.TrimSpace(body.Vendor) == "" || strings.TrimSpace(body.CostCentre) == "" ||
		strings.TrimSpace(body.Justification) == "" || len(body.Justification) > 4000:
		httpapi.Refuse(w, http.StatusBadRequest, "amount (more than 0), vendor, cost_centre and justification are needed")
		return
	}
	// The requester is the caller: the body cannot say otherwise.
	in := Input{ID: IDPrefix + uuid.NewString(), Requester: p.ID, Amount: body.Amount, Vendor: body.Vendor,
		CostCentre: body.CostCentre, Justification: body.Justification}
	_, err := a.Temporal.ExecuteWorkflow(r.Context(), client.StartWorkflowOptions{
		ID: in.ID, TaskQueue: TaskQueue, WorkflowIDReusePolicy: enumspb.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE,
	}, WorkflowName, in)
	if err != nil {
		httpapi.Fail(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusCreated, map[string]string{"id": in.ID})
}

// id returns the path's request ID if it names a purchase; otherwise it answers 404.
func id(w http.ResponseWriter, r *http.Request) (string, bool) {
	v := r.PathValue("id")
	if !strings.HasPrefix(v, IDPrefix) {
		httpapi.Refuse(w, http.StatusNotFound, "no such purchase")
		return "", false
	}
	return v, true
}

func (a *API) done(w http.ResponseWriter, err error) {
	if err != nil {
		httpapi.Fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) prepare(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	var body struct {
		Summary string `json:"summary"`
		Policy  string `json:"policy"`
		Flags   string `json:"flags"`
	}
	wf, ok := id(w, r)
	if !ok || !httpapi.Decode(w, r, &body) {
		return
	}
	a.done(w, httpapi.Update(r.Context(), a.Temporal, wf, UpdatePrepare,
		PrepareArgs{Actor: p, Summary: body.Summary, Policy: body.Policy, Flags: body.Flags}))
}

func (a *API) actor(update string) auth.Handler {
	return func(w http.ResponseWriter, r *http.Request, p auth.Principal) {
		if wf, ok := id(w, r); ok {
			a.done(w, httpapi.Update(r.Context(), a.Temporal, wf, update, ActorArgs{Actor: p}))
		}
	}
}

func (a *API) decide(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	var body struct {
		Approve bool   `json:"approve"`
		Reason  string `json:"reason"`
	}
	wf, ok := id(w, r)
	if !ok || !httpapi.Decode(w, r, &body) {
		return
	}
	a.done(w, httpapi.Update(r.Context(), a.Temporal, wf, UpdateDecide, DecideArgs{Actor: p, Approve: body.Approve, Reason: body.Reason}))
}
