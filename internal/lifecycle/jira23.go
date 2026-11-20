package lifecycle

import (
	"time"

	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// Jira part 2, step 3: the pure side of acting timers at an instant, of
// reactions that move children or set a field, and of a create reaction's
// reads of linked Items.
//
// PHASE B: declarations only, so the failing tests compile.

// LinkedFields is what a create reaction may read beyond its source: per
// link name, the fields of the Item it points to, already cut down to the
// fields that Item's Type lets reactions read. Own are the source's links,
// Container its container's.
type LinkedFields struct {
	Own       map[string]map[string]any
	Container map[string]map[string]any
}

// ActingAtTimers returns the acting timers at an instant of a state, in
// the Type's order.
func ActingAtTimers(t *typedef.Type, state string) []typedef.ActingTimer {
	return nil // Phase B stub
}

// ActingEffect evaluates an acting timer at an instant for the instant its
// field holds: whether its `when` holds (no condition: it does), and its
// `sets`, with cause.at = the instant and today its UTC date. Values come
// back in their fields' stored form, nil to unset. An expression that
// cannot be computed, or whose result is no value of its field, is an
// EffectFailed rejection naming the field.
func ActingEffect(t *typedef.Type, timer typedef.ActingTimer, fields map[string]any, instant time.Time) (sets map[string]any, applies bool, err error) {
	return nil, false, nil // Phase B stub
}

// ChildMatches reports whether a contained Item of Type child, with these
// fields, is one a children reaction moves: its Type is one of
// children.of and children.when holds (a field the child's version does
// not declare reads as null).
func ChildMatches(r typedef.Reaction, child *typedef.Type, fields map[string]any) bool {
	return false // Phase B stub
}

// SetValues evaluates a set reaction's expressions over the source's
// fields, cause.at and today, for a target of Type target: field ID ->
// value in the field's stored form, nil to unset. Failures as CreateFields.
func SetValues(t *typedef.Type, r typedef.Reaction, target *typedef.Type, in Input) (map[string]any, error) {
	return nil, nil // Phase B stub
}
