package connstub

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"

	"github.com/francoisnyc/work-kernel/internal/contracts"
	"github.com/francoisnyc/work-kernel/internal/hookproto"
)

// The HR directory as a pli.hooks/v1 connector: connector hr, one
// operation, manager_of, a check. It decides what the worker's
// hr.manager_of decided before the hook protocol: the requester is
// onBehalfOf if set, else createdBy; the answer is correct with the
// requester's manager, or deny unknown_employee (not in the directory) or
// no_eligible_approver (the requester manages themselves). Who may approve
// stays the Type's rule.
//
// Every request is verified against the server's secret first (401
// otherwise). Then the knobs (latency, outage, injected errors) apply, as
// to the other stubs. A success is stored under its idempotency key with a
// digest of what was asked; the same key asking the same again gets it
// back, asking something else gets 422, and a key still being answered
// gets 409. A failure is never stored, so a rerun under the same key is
// answered afresh.

const hrOperation = "manager_of"

// hrAnswer is a stored success: what was asked (a digest), and the answer.
type hrAnswer struct {
	digest string
	res    hookproto.Response
}

// hrDescriptor serves the descriptor. Its operation is the contract
// (contracts/hr.manager_of@1.json, Ventoux V1), so the two cannot drift;
// the stub only adds additionalProperties: false on its inputs, since it
// refuses any other.
func (s *Server) hrDescriptor(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	version := s.hrVersion
	s.mu.Unlock()
	contract, ok := contracts.Default().Lookup("hr." + hrOperation + "@1")
	if !ok {
		panic("connstub: no contract for hr.manager_of@1")
	}
	op := contract.Operation
	closed := false
	op.InputSchema.AdditionalProperties = &closed
	writeJSON(w, hookproto.Descriptor{
		Connector: "hr", Version: version, SupportedProtocolVersions: []string{hookproto.Protocol},
		Operations: []hookproto.Operation{op},
	})
}

// writeProblem answers a refusal with an RFC 9457 problem body.
func writeProblem(w http.ResponseWriter, status int, class hookproto.Class, title string) {
	w.Header().Set("Content-Type", hookproto.ProblemContentType)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(hookproto.Problem{Title: title, Status: status, Class: class})
}

func (s *Server) hrHook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, hookproto.ClassInvalid, "unreadable body")
		return
	}
	if !s.hrVerified(r.Header, body) {
		writeProblem(w, http.StatusUnauthorized, hookproto.ClassConfig, "bad or missing signature")
		return
	}
	if !s.knobsWith(w, r, func(w http.ResponseWriter, status int, msg string) {
		writeProblem(w, status, hookproto.ClassTransient, msg)
	}) {
		return
	}

	var req hookproto.Request
	if err := json.Unmarshal(body, &req); err != nil {
		writeProblem(w, http.StatusBadRequest, hookproto.ClassInvalid, "the body is not a pli.hooks/v1 request")
		return
	}
	switch {
	case req.Protocol != hookproto.Protocol:
		writeProblem(w, http.StatusBadRequest, hookproto.ClassInvalid, "unsupported protocol "+req.Protocol)
		return
	case req.Hook.Operation != hrOperation:
		writeProblem(w, http.StatusNotFound, hookproto.ClassInvalid, "no operation "+req.Hook.Operation)
		return
	case req.Kind != hookproto.KindCheck:
		writeProblem(w, http.StatusBadRequest, hookproto.ClassInvalid, "manager_of is a check, not "+string(req.Kind))
		return
	case req.IdempotencyKey == "":
		writeProblem(w, http.StatusBadRequest, hookproto.ClassInvalid, "no idempotency key")
		return
	}
	requester, err := hrRequester(req.Inputs)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, hookproto.ClassInvalid, err.Error())
		return
	}

	// What is asked, for the key: the operation, the kind, the inputs.
	// encoding/json writes a map's keys sorted, so equal inputs give equal
	// bytes.
	asked, _ := json.Marshal(map[string]any{"operation": req.Hook.Operation, "kind": req.Kind, "inputs": req.Inputs})
	sum := sha256.Sum256(asked)
	digest := hex.EncodeToString(sum[:])

	s.mu.Lock()
	if stored, ok := s.hrStored[req.IdempotencyKey]; ok {
		s.mu.Unlock()
		if stored.digest != digest {
			writeProblem(w, http.StatusUnprocessableEntity, hookproto.ClassKeyConflict,
				"the idempotency key was used with a different request")
			return
		}
		res := stored.res
		res.RequestID = req.RequestID
		writeJSON(w, res)
		return
	}
	if s.hrBusy[req.IdempotencyKey] {
		s.mu.Unlock()
		writeProblem(w, http.StatusConflict, hookproto.ClassInFlight, "the idempotency key is being answered")
		return
	}
	s.hrBusy[req.IdempotencyKey] = true
	manager, known := s.managers[requester]
	version := s.hrVersion
	s.mu.Unlock()

	res := hookproto.Response{Protocol: hookproto.Protocol, RequestID: req.RequestID, ConnectorVersion: version}
	switch {
	case !known:
		res.Outcome, res.Reason = hookproto.OutcomeDeny, &hookproto.Reason{Code: "unknown_employee"}
	case manager == requester:
		res.Outcome, res.Reason = hookproto.OutcomeDeny, &hookproto.Reason{Code: "no_eligible_approver"}
	default:
		res.Outcome, res.Patch = hookproto.OutcomeCorrect, map[string]any{"manager": manager}
	}

	// Stored before it is sent: an answer lost on the way is not lost.
	s.mu.Lock()
	s.hrStored[req.IdempotencyKey] = hrAnswer{digest: digest, res: res}
	delete(s.hrBusy, req.IdempotencyKey)
	drop := s.dropFirst && !s.hrDropped[req.IdempotencyKey]
	if drop {
		s.hrDropped[req.IdempotencyKey] = true
	}
	s.mu.Unlock()

	if drop {
		if hj, ok := w.(http.Hijacker); ok {
			if conn, _, err := hj.Hijack(); err == nil {
				s.count(&s.dropped)
				_ = conn.Close()
				return
			}
		}
	}
	writeJSON(w, res)
}

// hrVerified checks the request's signature against the current secret.
// With no secret, nothing is verified.
func (s *Server) hrVerified(h http.Header, body []byte) bool {
	s.mu.Lock()
	whsec := s.hrSecret
	s.mu.Unlock()
	secret, err := hookproto.ParseSecret(whsec)
	if err != nil {
		return false
	}
	_, err = hookproto.Verifier{Secrets: []hookproto.Secret{secret}}.Verify(h, body)
	return err == nil
}

// hrRequester reads manager_of's inputs: onBehalfOf if set, else
// createdBy, each null or a string; nothing else.
func hrRequester(inputs map[string]any) (string, error) {
	for name := range inputs {
		if !slices.Contains([]string{"onBehalfOf", "createdBy"}, name) {
			return "", errors.New("manager_of takes no input " + name)
		}
	}
	read := func(name string) (string, error) {
		switch v := inputs[name].(type) {
		case nil:
			return "", nil
		case string:
			return v, nil
		default:
			return "", errors.New(name + " must be a principal ID or null")
		}
	}
	onBehalfOf, err := read("onBehalfOf")
	if err != nil {
		return "", err
	}
	createdBy, err := read("createdBy")
	if err != nil {
		return "", err
	}
	if onBehalfOf != "" {
		return onBehalfOf, nil
	}
	if createdBy == "" {
		return "", errors.New("no requester: onBehalfOf and createdBy are both unset")
	}
	return createdBy, nil
}
