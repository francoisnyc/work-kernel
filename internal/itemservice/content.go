package itemservice

import (
	"fmt"
	"maps"
	"slices"

	"github.com/francoisnyc/work-kernel/internal/lifecycle"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// The etag rule (the etag step): an Item's content_seq, from which the
// public etag is derived, moves only when a write changes something a
// caller could overwrite. item_seq still counts every event.
//
// The rule reads the event's shape (its kind and its payload) and the
// Type: never a value, never the actor. When in doubt it says yes: a
// spurious refusal costs the caller a refresh, a missed one loses an
// update.

// A contentDecision says whether one kind of event changes the content.
// It is a function value: Go's stand-in for a one-method strategy object.
type contentDecision func(ty *typedef.Type, payload map[string]any) bool

func always(*typedef.Type, map[string]any) bool { return true }
func never(*typedef.Type, map[string]any) bool  { return false }

// contentDecisions has one entry per event kind the kernel writes. There
// is no default: a new kind must be added here (and to the table test), or
// its write fails.
var contentDecisions = map[string]contentDecision{
	// The first content.
	"create": always,
	// The state changed, whoever moved it (a hold resolved allow too).
	"transition": always,
	// A link, on its source.
	lifecycle.OpLinkAdded:   always,
	lifecycle.OpLinkRemoved: always,
	// Fields, the readers or the container.
	"update": updateChangesContent,
	// A hold opens: only the patch its request carried counts.
	lifecycle.OpTransitionRequested: func(ty *typedef.Type, payload map[string]any) bool {
		return namesCallerField(ty, namedFields(payload))
	},
	// A hold that did not move the Item, and the field-less records.
	lifecycle.OpTransitionDenied: never,
	lifecycle.OpTransitionFailed: never,
	lifecycle.OpHookRan:          never,
	lifecycle.OpHookFailed:       never,
	lifecycle.OpReactionApplied:  never,
	lifecycle.OpReactionFailed:   never,
	lifecycle.OpReactionSkipped:  never,
	// A migrate changes the Item's Type version and nothing a caller could
	// overwrite: no field, no reader, no container, no state.
	lifecycle.OpMigrated: never,
}

// changesContent reports whether the event (op, payload), written on an
// Item of Type ty, changes something a caller could overwrite. payload is
// the event's payload as appendEvent gets it (field lists as []string) or
// as it is stored (as []any). An op without a decision is an error.
func changesContent(ty *typedef.Type, op string, payload map[string]any) (bool, error) {
	decide, ok := contentDecisions[op]
	if !ok {
		return false, fmt.Errorf("itemservice: no etag decision for event kind %q (content.go)", op)
	}
	return decide(ty, payload), nil
}

// A contentBump is the rule's decision for one event: the event's kind and
// what its write adds to the Item's content_seq (0 or 1). Only
// decideContent makes one; appendEvent requires it.
type contentBump struct {
	op string
	n  int64
}

// decideContent asks the rule for an event about to be written. The write
// adds bump.n to content_seq in the same statement that writes item_seq,
// then hands bump to appendEvent. payload must already name the fields the
// event sets and clears, the readers change and the move.
func decideContent(ty *typedef.Type, op string, payload map[string]any) (contentBump, error) {
	changes, err := changesContent(ty, op, payload)
	if err != nil {
		return contentBump{}, err
	}
	bump := contentBump{op: op}
	if changes {
		bump.n = 1
	}
	return bump, nil
}

// DecidedOps returns every event kind the etag rule has a decision for,
// sorted.
func DecidedOps() []string {
	return slices.Sorted(maps.Keys(contentDecisions))
}

// updateChangesContent: a readers change or a move always; else the fields
// the update names decide. An update that names nothing is in doubt.
func updateChangesContent(ty *typedef.Type, payload map[string]any) bool {
	for _, key := range []string{"readers", "inherit", "container_from", "container_to"} {
		if _, has := payload[key]; has {
			return true
		}
	}
	ids := namedFields(payload)
	return len(ids) == 0 || namesCallerField(ty, ids)
}

// namedFields are the fields an event set or cleared.
func namedFields(payload map[string]any) []string {
	return append(stringList(payload["field_ids"]), stringList(payload["cleared_field_ids"])...)
}

// namesCallerField reports whether any of ids is a field a caller may
// write: every field but those marked writable_by: kernel. A role-written
// field counts, and so does a field this Type version does not declare.
func namesCallerField(ty *typedef.Type, ids []string) bool {
	for _, id := range ids {
		if f, ok := ty.Field(id); !ok || f.WritableBy != typedef.WritableByKernel {
			return true
		}
	}
	return false
}

// stringList reads a payload list in either of its two forms. A type
// switch is Go's instanceof chain; v.(type) only exists inside a switch.
func stringList(v any) []string {
	switch list := v.(type) {
	case []string:
		return list
	case []any:
		out := make([]string, 0, len(list))
		for _, e := range list {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}
