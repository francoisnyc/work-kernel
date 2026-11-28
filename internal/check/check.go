// Package check reads Types as a graph of states and of who may take each
// move, and reports what a Type can do that its author may not have meant
// (pli check, docs/roadmap.md, track 2): a state an Item can stay in for
// ever, a decision an agent or the requester can make, each with the
// shortest path to it as its witness, in plain words.
//
// The method is ventoux's (docs/research/poly-ecosystem-design.md): every
// guard is taken as possibly true, so what is still impossible is an
// error, and what depends on a guard, a role or a field's value is a
// warning that names what it depends on. Something the checker cannot
// model is an error too (UNCHECKED): it never reports clean by omission.
//
// It applies no rule of its own. Who may make a move is policy's answer
// (policy.MayMove and the field rules), asked for stand-in principals of
// each class; what can move an Item out of a state is lifecycle's
// (lifecycle.Exits). It reads no Item and no person's data.
package check

import (
	"github.com/francoisnyc/work-kernel/internal/contracts"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// Severity says whether a finding is proven on every reading of the
// Type (ERROR) or depends on something the Type does not decide (WARNING).
type Severity string

const (
	SeverityError   Severity = "ERROR"
	SeverityWarning Severity = "WARNING"
)

// Code names a kind of finding. Codes are stable: tools match on them.
type Code string

const (
	// A reachable state, not done or canceled, that no person, agent,
	// timer or reaction can move an Item out of.
	CodeStuckState Code = "STUCK_STATE"
	// An agent makes the move into a decision state: a done or canceled
	// state that at least one move into it restricts (a non-empty
	// allowed). A move whose allowed names creator or the principal
	// itself is the Type's intent, not a finding.
	CodeAgentDecides Code = "AGENT_DECIDES"
	// The requester makes the move into a decision state, on their own
	// request (the same rule for intent).
	CodeRequesterDecides Code = "REQUESTER_DECIDES"
	// Something in the Type the checker cannot model: never a silent
	// clean.
	CodeUnchecked Code = "UNCHECKED"
)

// Kinds of witness step.
const (
	StepCreate   = "create"
	StepMove     = "move"
	StepTimer    = "timer"
	StepAt       = "at"
	StepReaction = "reaction"
)

// Step is one step of a witness: who does what, in plain words.
type Step struct {
	// Who, as a class: "the requester", "an agent holding finance",
	// "whoever `f_manager` names", "the timer".
	Who  string `json:"who"`
	Kind string `json:"kind"`
	// From is empty for a create.
	From  string `json:"from,omitempty"`
	To    string `json:"to"`
	Words string `json:"words"`
}

// Finding is one thing a Type can do, with its witness.
type Finding struct {
	Type     string   `json:"type"`
	Version  int      `json:"version"`
	Code     Code     `json:"code"`
	Severity Severity `json:"severity"`
	// Path points into the Type's YAML: states[1], transitions[6].
	Path string `json:"path"`
	// Message says what can happen, in plain words.
	Message string `json:"message"`
	// DependsOn names what a WARNING depends on, in the path's order:
	// role:<name>, guard:<expression>, field:<id>. Empty for an ERROR.
	DependsOn []string `json:"depends_on"`
	// Witness is the shortest path that shows it, from the Item's creation.
	Witness []Step `json:"witness"`
}

// Report is what one run found, over every Type it was given.
type Report struct {
	Findings []Finding `json:"findings"`
	// NotChecked says, in plain words, what the checker does not look at,
	// so that no finding is read as "clean" there.
	NotChecked []string `json:"not_checked"`
	// Assumed says what the checker takes as given.
	Assumed []string `json:"assumed"`
}

// Check reads the Types together (a reaction of one may move another's
// Items) and returns every finding, ordered by Type and version, then
// errors before warnings, then path, then code. cat holds the action
// contracts the Types' hooks name (what a hook's answer may leave empty).
func Check(types []*typedef.Type, cat *contracts.Catalog) Report {
	return Report{} // Phase B stub
}

// HasErrors reports whether any finding is an ERROR.
func (r Report) HasErrors() bool {
	return false // Phase B stub
}

// Text lays the report out for a person (docs/design.md, "pli check").
func (r Report) Text() string {
	return "" // Phase B stub
}
