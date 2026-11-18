package lifecycle

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"cel.dev/cel-go/common/types"

	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// Code says why a transition was rejected, for callers that branch on it.
type Code string

const (
	NoTransition  Code = "no_transition"  // the Type declares no such move
	MissingFields Code = "missing_fields" // a required field is not set
	GuardFailed   Code = "guard_failed"   // the guard does not hold
	FieldFrozen   Code = "field_frozen"   // a write names a field the current state freezes
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
	// For FieldFrozen: the state that freezes them and the fields named.
	State  string
	Frozen []string
	// For InvariantFailed: the invariant's ID.
	Invariant string
}

// Input is what a transition's guards see besides the Type: the fields as
// the write would leave them, the linked Items, and today's date. Who acts
// is not here: identity rules are `allowed` and `except`, policy's question.
type Input struct {
	Fields map[string]any
	Links  Links
	// Today is a date (UTC); the time of day is ignored.
	Today time.Time
}

func (r *Rejection) Error() string { return r.Reason }

// Result describes the state an Item enters.
type Result struct {
	State    string
	Category string
	// Clears are the fields the transition unsets in the same write, as
	// the Type declares them (sorted). The caller applies them after the
	// patch; required fields and the guard were judged before.
	Clears []string
}

// CheckFrozen decides whether a write made while the Item is in state may
// name these fields: the state's frozen fields may not be written, by any
// door. A transition is checked against the state it leaves, so the one
// entering a state may set what that state freezes. The refusal names the
// state and the frozen fields among those named, sorted.
func CheckFrozen(t *typedef.Type, state string, fieldIDs []string) error {
	s, _ := t.State(state)
	var frozen []string
	for _, id := range fieldIDs {
		if slices.Contains(s.Frozen, id) {
			frozen = append(frozen, id)
		}
	}
	if len(frozen) == 0 {
		return nil
	}
	slices.Sort(frozen)
	frozen = slices.Compact(frozen)
	return &Rejection{
		Code: FieldFrozen, State: state, Frozen: frozen,
		Reason: fmt.Sprintf("fields %s are frozen in state %q", strings.Join(frozen, ", "), state),
	}
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
// (see Links). Without a date, a guard reading today does not hold.
func TransitionLinked(t *typedef.Type, current string, fields map[string]any, links Links,
	to string) (Result, error) {
	return TransitionWith(t, current, Input{Fields: fields, Links: links}, to)
}

// TransitionWith is the one pure lifecycle function: Transition with
// everything a guard may read (Input).
func TransitionWith(t *typedef.Type, current string, in Input, to string) (Result, error) {
	fields, links := in.Fields, in.Links
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

	if !guardHolds(t, tr, fields, links, in.Today) {
		return Result{}, &Rejection{
			Code: GuardFailed, From: current, To: to,
			Reason: fmt.Sprintf("transition from %q to %q: guard not satisfied: %s", current, to, tr.Guard),
		}
	}

	state, _ := t.State(to) // a loaded Type only has transitions between declared states
	// What the move clears is the Type's to say, and it is said here, after
	// required fields and the guard were judged on the fields as given: a
	// reopening guard reads the resolution the move then clears.
	clears := slices.Clone(tr.Clears)
	slices.Sort(clears)
	return Result{State: state.ID, Category: state.Category, Clears: clears}, nil
}

// guardHolds evaluates the transition's CEL guard against the fields. Every
// field of the Type is a variable: unset is null, and ints read back from
// JSON (float64) are passed as ints, as the schema says. A guard that cannot
// be evaluated (an unset field in a comparison, say) does not hold.
//
// The reason given for a failed guard is the guard's own text, never the
// values it saw: those may be personal data.
func guardHolds(t *typedef.Type, tr typedef.Transition, fields map[string]any, links Links, today time.Time) bool {
	if tr.Program == nil {
		return true // no guard
	}
	// Links the guard reads but nobody read: fail safe.
	if len(tr.GuardLinks) > 0 && links == nil {
		return false
	}
	vars := make(map[string]any, len(t.Fields)+len(tr.GuardLinks)+1)
	// today is a date: 00:00 UTC of the caller's day. Unknown (zero), it is
	// null, and a guard reading it cannot be evaluated: it does not hold.
	if today.IsZero() {
		vars[typedef.VarToday] = types.NullValue
	} else {
		u := today.UTC()
		vars[typedef.VarToday] = time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
	}
	for _, name := range tr.GuardLinks {
		// A linked Item is its state and category, and nothing else.
		list := make([]map[string]string, 0, len(links[name]))
		for _, li := range links[name] {
			list = append(list, map[string]string{"state": li.State, "category": li.Category})
		}
		vars[typedef.LinksPrefix+name] = list
	}
	addFieldVars(vars, t, fields)
	out, _, err := tr.Program.Eval(vars)
	if err != nil {
		return false
	}
	ok, isBool := out.Value().(bool)
	return isBool && ok
}

// addFieldVars sets one CEL variable per field of t: unset is null, ints
// read back from JSON (float64) are ints, dates are timestamps.
func addFieldVars(vars map[string]any, t *typedef.Type, fields map[string]any) {
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
		case f.Type == typedef.Date:
			// A stored date that does not parse cannot be compared: the
			// rule reading it does not hold.
			if d, ok := typedef.ParseDate(v); ok {
				vars[f.ID] = d
			} else {
				vars[f.ID] = v
			}
		default:
			vars[f.ID] = v
		}
	}
}
