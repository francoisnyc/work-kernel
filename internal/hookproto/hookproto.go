// Package hookproto is pli.hooks/v1: the wire contract between the
// kernel's worker and a connector. A connector is a service (or code
// compiled into the worker) that a Type's hooks call: a check answers
// allow, deny or correct; an enrich hook gets values to write; an action
// changes the outside world (specified, not built over the wire yet).
//
// The types here are the protocol's JSON envelopes. Both bindings use
// them: plain HTTPS (POST {base}/hooks/v1, signed with Standard Webhooks)
// and the Go interface hooks.Connector, so a Type cannot tell a remote
// connector from a compiled-in one.
//
// Within v1 the envelopes only grow: a field may be added, never removed,
// renamed or retyped (testdata/v1.0 holds the frozen fixtures that
// enforce it), and both sides ignore fields they do not know.
//
// design.md, "Hook protocol (pli.hooks/v1)", is the prose contract.
package hookproto

import (
	"errors"
	"time"
)

// Protocol is the protocol version this package speaks.
const Protocol = "pli.hooks/v1"

// Paths a connector serves, relative to its base URL.
const (
	HooksPath      = "/hooks/v1"
	DescriptorPath = "/.well-known/pli-connector"
)

// Content types.
const (
	ContentType        = "application/json"
	ProblemContentType = "application/problem+json" // RFC 9457
)

// Kind is what a hook does.
type Kind string

const (
	KindCheck  Kind = "check"  // decides a held transition: allow, deny or correct
	KindEnrich Kind = "enrich" // returns values the kernel writes
	KindAction Kind = "action" // a side effect outside the kernel (specified only over the wire)
)

// Outcome is a connector's business answer. A business answer is never
// retried and never subject to failure policy; technical failures are
// HTTP errors (Problem).
type Outcome string

const (
	// A check's outcomes. correct is allow with a patch (Pli's addition to
	// AdmissionReview's allow/deny: the HR check writes the approver).
	OutcomeAllow   Outcome = "allow"
	OutcomeDeny    Outcome = "deny"
	OutcomeCorrect Outcome = "correct"
	// An enrich or action hook's outcome.
	OutcomeOK Outcome = "ok"
	// Specified in design.md, not built: an enrich or action hook's
	// business error, and any kind's asynchronous answer (202).
	OutcomeBusinessError Outcome = "business_error"
	OutcomePending       Outcome = "pending"
)

// Event types: what caused the hook to run.
const (
	EventTransitionRequested = "item.transition.requested" // a check
	EventStateEntered        = "item.state.entered"        // an enrich or action hook on enter
	EventTimerFired          = "item.timer.fired"          // an action hook run by an acting timer
	EventReactionApplied     = "item.reaction.applied"     // an action hook run by a reaction
)

// Request is the one envelope every call carries.
type Request struct {
	Protocol string `json:"protocol"` // Protocol
	Kind     Kind   `json:"kind"`
	// RequestID is unique per attempt and echoed in the response.
	RequestID string `json:"requestId"`
	// IdempotencyKey is derived by the kernel from the hook and its cause,
	// stable across retries and reruns; it holds no personal data. A
	// connector stores its successes by it, never its failures.
	IdempotencyKey string `json:"idempotencyKey"`
	Attempt        int    `json:"attempt"`
	// Deadline is when the caller stops waiting; the connector should stop
	// work after it. Zero when the caller set none.
	Deadline time.Time `json:"deadline,omitzero"`

	Hook  HookRef `json:"hook"`
	Event Event   `json:"event"`
	// "actor" (who caused the event) is reserved: left out of this step,
	// it can be added within v1 without breaking anyone (Later).
	Item       ItemRef        `json:"item"`
	Transition *Transition    `json:"transition,omitempty"` // a check only
	Inputs     map[string]any `json:"inputs"`               // the operation's declared inputs, by name; nothing else
	Trace      Trace          `json:"trace"`
	Connection ConnectionRef  `json:"connection"` // a reference, never a secret
}

// HookRef names the hook binding and what it calls.
type HookRef struct {
	Binding   string `json:"binding"`   // <type>@<version>#<hook>, e.g. vacation_request@3#hr_check
	Connector string `json:"connector"` // <connector>@<major>, e.g. hr@1
	Operation string `json:"operation"` // e.g. manager_of
}

// Event identifies the event that caused the call (CloudEvents' id, type,
// subject).
type Event struct {
	ID      string `json:"id"`      // <item>:<item_seq>
	Type    string `json:"type"`    // one of the Event* constants
	Subject string `json:"subject"` // item/<item>
}

// ItemRef names the Item, never its field values (those are Inputs).
type ItemRef struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	TypeVersion int    `json:"typeVersion"`
}

// Transition is the held transition a check decides.
type Transition struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// Trace ties the call to the kernel's chain of causes. (traceparent is
// reserved for when the stack has tracing.)
type Trace struct {
	CorrelationID string `json:"correlationId,omitempty"`
	CausationID   string `json:"causationId"`
}

// ConnectionRef says which configured connection the call goes through.
type ConnectionRef struct {
	Slot string `json:"slot"` // the connector name the Type uses, e.g. hr
	Ref  string `json:"ref"`  // the configured connection, e.g. hr-directory
}

// Response is a connector's answer (HTTP 200).
type Response struct {
	Protocol  string  `json:"protocol"`
	RequestID string  `json:"requestId"` // the request's, echoed
	Outcome   Outcome `json:"outcome"`
	// Patch holds values for the operation's declared outputs, by name: a
	// check's correct, an enrich hook's ok. Nothing else is accepted.
	Patch  map[string]any `json:"patch,omitempty"`
	Reason *Reason        `json:"reason,omitempty"` // a deny (or business_error)
	// ConnectorVersion is the implementation that answered (semver).
	ConnectorVersion string `json:"connectorVersion,omitempty"`
}

// Reason is a denial's reason. Only Code reaches the Item; Message is
// logged, never stored.
type Reason struct {
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
}

// Class is a technical failure's class.
type Class string

const (
	ClassInvalid     Class = "invalid"      // 400, 404: retrying cannot help
	ClassConfig      Class = "config"       // 401, 403: the signature or the credential
	ClassInFlight    Class = "in_flight"    // 409: the same key is still being processed; retry later
	ClassGone        Class = "gone"         // 410
	ClassKeyConflict Class = "key_conflict" // 422: the same key with a different request
	ClassRateLimited Class = "rate_limited" // 429
	ClassTransient   Class = "transient"    // 5xx
)

// Problem is an RFC 9457 problem body: every non-200 answer carries one.
type Problem struct {
	Type   string `json:"type,omitempty"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	Detail string `json:"detail,omitempty"`
	Class  Class  `json:"class"`
	// Retryable, when set, overrides what the status says.
	Retryable *bool `json:"retryable,omitempty"`
}

// Descriptor is what a connector serves at DescriptorPath.
type Descriptor struct {
	Connector                 string      `json:"connector"` // the name Types use, e.g. hr
	Version                   string      `json:"version"`   // the implementation's semver, e.g. 1.4.0
	SupportedProtocolVersions []string    `json:"supportedProtocolVersions"`
	Operations                []Operation `json:"operations"`
}

// Operation is one operation a connector offers.
type Operation struct {
	Name         string   `json:"name"`
	Kinds        []Kind   `json:"kinds"`
	InputSchema  Schema   `json:"inputSchema"`
	OutputSchema *Schema  `json:"outputSchema,omitempty"`
	SideEffects  string   `json:"sideEffects"`      // none | some
	Async        string   `json:"async"`            // forbidden | optional | required
	Errors       []string `json:"errors,omitempty"` // the deny (and business error) codes it may answer
}

// Schema is the subset of JSON Schema 2020-12 v1 uses: an object whose
// properties each have a type.
type Schema struct {
	Type                 string              `json:"type"` // object
	Properties           map[string]Property `json:"properties"`
	Required             []string            `json:"required,omitempty"`
	AdditionalProperties *bool               `json:"additionalProperties,omitempty"`
}

// Property is one property's type: JSON Schema's "type", a string
// ("string") or a list (["string", "null"]); v1 writes the list form.
type Property struct {
	Type TypeList `json:"type"`
}

// TypeList is JSON Schema's type keyword, read in either form.
type TypeList []string

// ErrNotImplemented marks a Phase B stub.
var ErrNotImplemented = errors.New("hookproto: not implemented")

// Operation returns the operation with the given name.
func (d Descriptor) Operation(name string) (Operation, bool) {
	return Operation{}, false
}

// Major returns the major version of a semver string ("1.4.0" -> 1).
func Major(version string) (int, error) {
	return 0, ErrNotImplemented
}
