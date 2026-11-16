package lifecycle

import (
	"fmt"
	"slices"
	"strings"

	"cel.dev/cel-go/common/types"

	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// Code says why a transition was rejected, for callers that branch on it.
type Code string

const (
	NoTransition  Code = "no_transition"  // the Type declares no such move
	MissingFields Code = "missing_fields" // a required field is not set
	GuardFailed   Code = "guard_failed"   // the guard does not hold
)

// Rejection is the error returned for an illegal transition. Any type with
// an Error() string method is an error in Go; there is no base class to extend.
// The reason names states and field IDs, never field values.
type Rejection struct {
	Code   Code
	Reason string
	// The move refused and, for MissingFields, the fields not set: the same
	// facts as Reason, as data a caller can act on.
	From, To string
	Missing  []string
}

func (r *Rejection) Error() string { return r.Reason }

// Result describes the state an Item enters.
type Result struct {
	State    string
	Category string
}

// Transition decides whether an Item of Type t, in state current with the
// given fields, may move to state to. It is pure: no I/O, no clock, no
// randomness, and it does not modify its arguments. That makes it safe to
// call from the item service and from inside a Temporal workflow alike.
//
// A legal move returns the new state. An illegal one returns a *Rejection.
//
// It reads no links: a guard that reads them does not hold. Callers with
// links use TransitionLinked.
func Transition(t *typedef.Type, current string, fields map[string]any, to string) (Result, error) {
	return TransitionLinked(t, current, fields, nil, to)
}

// TransitionLinked is Transition with the linked Items the guard may read
// (see Links). It is the one pure lifecycle function.
func TransitionLinked(t *typedef.Type, current string, fields map[string]any, links Links,
	to string) (Result, error) {
	tr, ok := t.Transition(current, to)
	if !ok {
		return Result{}, &Rejection{
			Code: NoTransition, From: current, To: to,
			Reason: fmt.Sprintf("type %s v%d has no transition from %q to %q", t.ID, t.Version, current, to),
		}
	}

	var missing []string
	for _, id := range tr.Required {
		if fields[id] == nil { // absent and null both count as not set
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		slices.Sort(missing)
		return Result{}, &Rejection{
			Code: MissingFields, From: current, To: to, Missing: missing,
			Reason: fmt.Sprintf("transition from %q to %q needs these fields set: %s", current, to, strings.Join(missing, ", ")),
		}
	}

	if !guardHolds(t, tr, fields, links) {
		return Result{}, &Rejection{
			Code: GuardFailed, From: current, To: to,
			Reason: fmt.Sprintf("transition from %q to %q: guard not satisfied: %s", current, to, tr.Guard),
		}
	}

	state, _ := t.State(to) // a loaded Type only has transitions between declared states
	return Result{State: state.ID, Category: state.Category}, nil
}

// guardHolds evaluates the transition's CEL guard against the fields. Every
// field of the Type is a variable: unset is null, and ints read back from
// JSON (float64) are passed as ints, as the schema says. A guard that cannot
// be evaluated (an unset field in a comparison, say) does not hold.
//
// The reason given for a failed guard is the guard's own text, never the
// values it saw: those may be personal data.
func guardHolds(t *typedef.Type, tr typedef.Transition, fields map[string]any, links Links) bool {
	if tr.Program == nil {
		return true // no guard
	}
	// Links the guard reads but nobody read: fail safe.
	if len(tr.GuardLinks) > 0 && links == nil {
		return false
	}
	vars := make(map[string]any, len(t.Fields)+len(tr.GuardLinks))
	for _, name := range tr.GuardLinks {
		// A linked Item is its state and category, and nothing else.
		list := make([]map[string]string, 0, len(links[name]))
		for _, li := range links[name] {
			list = append(list, map[string]string{"state": li.State, "category": li.Category})
		}
		vars[typedef.LinksPrefix+name] = list
	}
	for _, f := range t.Fields {
		v := fields[f.ID]
		switch {
		case v == nil:
			vars[f.ID] = types.NullValue
		case f.Type == typedef.Int:
			if n, ok := typedef.AsInt(v); ok {
				v = n
			}
			vars[f.ID] = v
		default:
			vars[f.ID] = v
		}
	}
	out, _, err := tr.Program.Eval(vars)
	if err != nil {
		return false
	}
	ok, isBool := out.Value().(bool)
	return isBool && ok
}
