package lifecycle

import (
	"fmt"
	"slices"
	"strings"

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
func Transition(t *typedef.Type, current string, fields map[string]any, to string) (Result, error) {
	tr, ok := t.Transition(current, to)
	if !ok {
		return Result{}, &Rejection{
			Code:   NoTransition,
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
			Code:   MissingFields,
			Reason: fmt.Sprintf("transition from %q to %q needs these fields set: %s", current, to, strings.Join(missing, ", ")),
		}
	}

	for _, c := range tr.Conditions {
		if !holds(c, fields[c.Field]) {
			return Result{}, &Rejection{
				Code:   GuardFailed,
				Reason: fmt.Sprintf("transition from %q to %q: guard not satisfied: %s", current, to, c),
			}
		}
	}

	state, _ := t.State(to) // a loaded Type only has transitions between declared states
	return Result{State: state.ID, Category: state.Category}, nil
}

// holds evaluates one guard condition against a field's value (nil if unset).
func holds(c typedef.Condition, value any) bool {
	switch c.Op {
	case typedef.OpIsSet:
		return value != nil
	case typedef.OpEq:
		return value != nil && equal(c.Value, value)
	case typedef.OpNotEq:
		return value == nil || !equal(c.Value, value)
	}
	return false
}

// equal compares a guard literal with a field value. Integers need care:
// the literal is an int64 but a value read back from JSON is a float64.
func equal(literal, value any) bool {
	if want, ok := literal.(int64); ok {
		got, ok := typedef.AsInt(value)
		return ok && got == want
	}
	return literal == value
}
