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
	"cmp"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

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

	// PR 2: dead rules and drafts.
	//
	// A reachable state, not done or canceled, with ways out, from which
	// no path leads to a done or canceled state (a loop with no end). A
	// state from which a stuck state can be reached is STUCK_STATE's.
	CodeNoWayOut Code = "NO_WAY_OUT"
	// A state no Item can ever be in, whoever holds which role.
	CodeUnreachableState Code = "UNREACHABLE_STATE"
	// A transition, timer, at-timer, reaction or hook that can never
	// happen or run.
	CodeNeverFires Code = "NEVER_FIRES"
	// In a draft (a reachable state whose every move out is allowed:
	// [creator]), a colleague may change a field any person may write.
	// Fields only holders of a role may write are the Type's intent, not
	// a finding.
	CodeColleagueEditsDraft Code = "COLLEAGUE_EDITS_DRAFT"
)

// Kinds of witness step.
const (
	StepCreate   = "create"
	StepMove     = "move"
	StepTimer    = "timer"
	StepAt       = "at"
	StepReaction = "reaction"
	// StepWrite: someone writes fields, the Item staying in its state.
	StepWrite = "write"
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
	r := Report{Findings: []Finding{}, NotChecked: slices.Clone(notChecked), Assumed: slices.Clone(assumed)}
	for _, ty := range types {
		if noLifecycle(ty) {
			continue
		}
		r.Findings = append(r.Findings, newTypeCheck(ty, types, cat).run()...)
	}
	slices.SortStableFunc(r.Findings, func(a, b Finding) int {
		return cmp.Or(
			strings.Compare(a.Type, b.Type),
			cmp.Compare(a.Version, b.Version),
			cmp.Compare(rank(a.Severity), rank(b.Severity)),
			comparePaths(a.Path, b.Path),
			strings.Compare(string(a.Code), string(b.Code)))
	})
	return r
}

// What the checker does not look at, and what it takes as given: printed
// with every report, so that no silence reads as "clean".
var (
	notChecked = []string{
		"who reads it: scopes decide, not the Type",
		"field writes outside drafts",
		"placement: who moves or narrows an Item",
	}
	assumed = []string{
		"a principal field only the kernel writes names a person, never an agent; it is empty only where its hook's contract allows no answer",
	}
)

// noLifecycle is a Type with no moves and only the implicit state.
func noLifecycle(t *typedef.Type) bool {
	return len(t.Transitions) == 0 && len(t.States) == 1 && t.States[0].ID == typedef.ImplicitState
}

func rank(s Severity) int {
	if s == SeverityError {
		return 0
	}
	return 1
}

var pathPart = regexp.MustCompile(`^([a-z_]+)\[(\d+)\](.*)$`)

// comparePaths orders states[2] before states[10].
func comparePaths(a, b string) int {
	ma, mb := pathPart.FindStringSubmatch(a), pathPart.FindStringSubmatch(b)
	if ma == nil || mb == nil || ma[1] != mb[1] {
		return strings.Compare(a, b)
	}
	na, _ := strconv.Atoi(ma[2])
	nb, _ := strconv.Atoi(mb[2])
	return cmp.Or(cmp.Compare(na, nb), strings.Compare(ma[3], mb[3]))
}

// HasErrors reports whether any finding is an ERROR.
func (r Report) HasErrors() bool {
	return slices.ContainsFunc(r.Findings, func(f Finding) bool { return f.Severity == SeverityError })
}

// Text lays the report out for a person: each Type version that has
// findings, each finding with what it depends on and its witness; then
// what is not checked and what is assumed; then the counts.
func (r Report) Text() string {
	var b strings.Builder
	last := ""
	errs, warns := 0, 0
	for _, f := range r.Findings {
		if head := fmt.Sprintf("%s@%d", f.Type, f.Version); head != last {
			b.WriteString(head + "\n")
			last = head
		}
		if f.Severity == SeverityError {
			errs++
		} else {
			warns++
		}
		path := f.Path
		if path == "" {
			path = "-"
		}
		fmt.Fprintf(&b, "  %s %s %s: %s\n", f.Code, f.Severity, path, f.Message)
		if len(f.DependsOn) > 0 {
			fmt.Fprintf(&b, "    Depends on: %s.\n", strings.Join(f.DependsOn, ", "))
		}
		for i, s := range f.Witness {
			fmt.Fprintf(&b, "    %d. %s\n", i+1, s.Words)
		}
	}
	fmt.Fprintf(&b, "Not checked: %s.\n", strings.Join(r.NotChecked, "; "))
	fmt.Fprintf(&b, "Assumed: %s.\n", strings.Join(r.Assumed, "; "))
	if len(r.Findings) == 0 {
		b.WriteString("No findings.\n")
	} else {
		fmt.Fprintf(&b, "%s, %s.\n", plural(errs, "error"), plural(warns, "warning"))
	}
	return b.String()
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return strconv.Itoa(n) + " " + word + "s"
}
