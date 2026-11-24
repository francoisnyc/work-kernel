// Package httpapi holds what every route of the API shares: JSON in and
// out, turning a workflow's refusal into an HTTP status, and the routes
// that are the same for every kind of request (the inbox, reading one
// request and its history, the agent's to-do list).
package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"

	"github.com/francoisnyc/work-kernel/spikes/temporal-bakeoff/auth"
	"github.com/francoisnyc/work-kernel/spikes/temporal-bakeoff/store"
)

func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func Refuse(w http.ResponseWriter, status int, msg string) {
	WriteJSON(w, status, map[string]string{"error": msg})
}

// Decode reads a small JSON body into v and refuses unknown fields, so a
// caller cannot slip in a field the handler does not expect.
func Decode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		Refuse(w, http.StatusBadRequest, "bad request body: "+err.Error())
		return false
	}
	return true
}

// Fail answers for an error from Temporal or Postgres. A workflow's
// refusal travels as an ApplicationError whose type names the status.
func Fail(w http.ResponseWriter, err error) {
	var app *temporal.ApplicationError
	var notFound *serviceerror.NotFound
	switch {
	case errors.As(err, &app):
		status := map[string]int{"FORBIDDEN": http.StatusForbidden, "CONFLICT": http.StatusConflict, "INVALID": http.StatusBadRequest}[app.Type()]
		if status == 0 {
			status = http.StatusInternalServerError
		}
		Refuse(w, status, app.Message())
	case errors.As(err, &notFound), errors.Is(err, pgx.ErrNoRows):
		Refuse(w, http.StatusNotFound, "no such request")
	default:
		Refuse(w, http.StatusInternalServerError, "internal error")
	}
}

// Update sends one update to a request's workflow and waits for its answer.
func Update(ctx context.Context, c client.Client, id, name string, arg any) error {
	h, err := c.UpdateWorkflow(ctx, client.UpdateWorkflowOptions{
		WorkflowID: id, UpdateName: name, Args: []any{arg}, WaitForStage: client.WorkflowUpdateStageCompleted,
	})
	if err != nil {
		return err
	}
	return h.Get(ctx, nil)
}

// Names are what a principal answers to in waiting_on and readers.
func Names(p auth.Principal) []string {
	out := []string{p.ID}
	for _, r := range p.Roles {
		out = append(out, "role:"+r)
	}
	return out
}

// CanSee says whether p may read a request.
func CanSee(p auth.Principal, row store.Row) bool {
	return slices.ContainsFunc(Names(p), func(n string) bool { return slices.Contains(row.Readers, n) })
}

// Common serves the routes every kind of request shares.
type Common struct {
	Store    *store.Store
	Temporal client.Client
}

func (c *Common) Routes(mux *http.ServeMux, t auth.Tokens) {
	mux.HandleFunc("GET /inbox", t.Require(c.inbox))
	mux.HandleFunc("GET /agent/todo", t.Require(c.todo))
	mux.HandleFunc("GET /requests/{id}", t.Require(c.get))
	mux.HandleFunc("GET /requests/{id}/history", t.Require(c.history))
}

// inbox: everything waiting on the caller or a role they hold, any kind.
func (c *Common) inbox(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	rows, err := c.Store.WaitingOn(r.Context(), Names(p))
	if err != nil {
		Fail(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"requests": rows})
}

// todo: what the agent has still to prepare.
func (c *Common) todo(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	if !p.Has("preparer") {
		Refuse(w, http.StatusForbidden, "only the preparer has a to-do list")
		return
	}
	rows, err := c.Store.ToPrepare(r.Context())
	if err != nil {
		Fail(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"requests": rows})
}

// visible loads a request the caller may see; otherwise it answers 404, so
// a caller cannot tell a hidden request from a missing one.
func (c *Common) visible(w http.ResponseWriter, r *http.Request, p auth.Principal) (store.Row, bool) {
	row, err := c.Store.Get(r.Context(), r.PathValue("id"))
	if err == nil && !CanSee(p, row) {
		err = pgx.ErrNoRows
	}
	if err != nil {
		Fail(w, err)
		return row, false
	}
	return row, true
}

// get answers with the workflow's own state (a query), not the copy in
// Postgres: the copy only decides who may ask.
func (c *Common) get(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	row, ok := c.visible(w, r, p)
	if !ok {
		return
	}
	val, err := c.Temporal.QueryWorkflow(r.Context(), row.ID, "", "state")
	if err == nil {
		var state json.RawMessage
		if err = val.Get(&state); err == nil {
			row.Data = state
		}
	}
	if err != nil {
		Fail(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, row)
}

func (c *Common) history(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	row, ok := c.visible(w, r, p)
	if !ok {
		return
	}
	events, err := c.Store.History(r.Context(), row.ID)
	if err != nil {
		Fail(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"events": events})
}

// Start starts a request's workflow and returns its ID. A caller that sends
// a request ID gets one request however often it retries: the workflow ID
// is derived from the requester and that ID, and a second start answers
// with the first (found in review: a retried submission made two). With no
// request ID, every call makes a new request.
func Start(ctx context.Context, c client.Client, prefix, requester, requestID, taskQueue, workflow string,
	input func(id string) any) (string, error) {
	id := prefix + uuid.NewString()
	if requestID != "" {
		sum := sha256.Sum256([]byte(requester + "\x00" + requestID))
		id = prefix + hex.EncodeToString(sum[:16])
	}
	_, err := c.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID: id, TaskQueue: taskQueue,
		WorkflowIDReusePolicy:    enumspb.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE,
		WorkflowIDConflictPolicy: enumspb.WORKFLOW_ID_CONFLICT_POLICY_USE_EXISTING,
	}, workflow, input(id))
	var done *serviceerror.WorkflowExecutionAlreadyStarted
	if errors.As(err, &done) {
		return id, nil // the request was made earlier and has already finished
	}
	return id, err
}
