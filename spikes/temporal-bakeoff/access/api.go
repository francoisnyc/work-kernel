package access

import (
	"net/http"
	"strings"

	"go.temporal.io/sdk/client"

	"github.com/francoisnyc/work-kernel/spikes/temporal-bakeoff/auth"
	"github.com/francoisnyc/work-kernel/spikes/temporal-bakeoff/httpapi"
	"github.com/francoisnyc/work-kernel/spikes/temporal-bakeoff/purchase"
)

// API is the access request's HTTP side. Like the purchase's, it signs the
// caller in, checks the shape of the request, and hands over to the
// workflow, whose validators decide who may do what.
type API struct {
	Temporal client.Client
}

func (a *API) Routes(mux *http.ServeMux, t auth.Tokens) {
	mux.HandleFunc("POST /access-requests", t.Require(a.submit))
	mux.HandleFunc("POST /access-requests/{id}/preparation", t.Require(a.prepare))
	mux.HandleFunc("POST /access-requests/{id}/decision", t.Require(a.decide))
}

func (a *API) submit(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	var body struct {
		System        string `json:"system"`
		Role          string `json:"role"`
		Justification string `json:"justification"`
		RequestID     string `json:"request_id"` // optional, as for a purchase
	}
	if !httpapi.Decode(w, r, &body) {
		return
	}
	// A map lookup returns the value and whether the key was there (the
	// "comma ok" form); only the second is needed here.
	_, known := Owners[body.System]
	switch {
	case len(body.RequestID) > 100:
		httpapi.Refuse(w, http.StatusBadRequest, "request_id is 100 characters at most")
		return
	case !p.IsUser():
		httpapi.Refuse(w, http.StatusForbidden, "only a person asks for access")
		return
	case !known:
		httpapi.Refuse(w, http.StatusBadRequest, "system is not one this team knows the owner of")
		return
	case strings.TrimSpace(body.Role) == "" || len(body.Role) > 200 ||
		strings.TrimSpace(body.Justification) == "" || len(body.Justification) > 4000:
		httpapi.Refuse(w, http.StatusBadRequest, "system, role and justification are needed")
		return
	}
	// The requester is the caller: the body cannot say otherwise.
	id, err := httpapi.Start(r.Context(), a.Temporal, IDPrefix, p.ID, body.RequestID, purchase.TaskQueue, WorkflowName, func(id string) any {
		return Input{ID: id, Requester: p.ID, System: body.System, Role: body.Role, Justification: body.Justification}
	})
	if err != nil {
		httpapi.Fail(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusCreated, map[string]string{"id": id})
}

// update sends one update to the access request the path names and answers
// 204, or 404 if the ID is not an access request's (a purchase has the same
// update names: it must not be reached through these routes).
func (a *API) update(w http.ResponseWriter, r *http.Request, name string, arg any) {
	wf := r.PathValue("id")
	if !strings.HasPrefix(wf, IDPrefix) {
		httpapi.Refuse(w, http.StatusNotFound, "no such access request")
		return
	}
	if err := httpapi.Update(r.Context(), a.Temporal, wf, name, arg); err != nil {
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
	if httpapi.Decode(w, r, &body) {
		a.update(w, r, purchase.UpdatePrepare, purchase.PrepareArgs{Actor: p, Summary: body.Summary, Policy: body.Policy, Flags: body.Flags})
	}
}

func (a *API) decide(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	var body struct {
		Approve bool   `json:"approve"`
		Reason  string `json:"reason"`
	}
	if httpapi.Decode(w, r, &body) {
		a.update(w, r, purchase.UpdateDecide, purchase.DecideArgs{Actor: p, Approve: body.Approve, Reason: body.Reason})
	}
}
