package itemservice

import (
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/francoisnyc/work-kernel/internal/lifecycle"
	"github.com/francoisnyc/work-kernel/internal/policy"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// Field rules (proof of concept, Step 2, part 1). A patch is checked, in
// this order, on create, update and a transition, before an open hold is:
//
//  1. Not writable (FieldNotWritableError, an ErrForbidden): on the public
//     door, a field only the kernel writes (writable_by: kernel); on every
//     door, in a transition's patch, a field the transition's own `allowed`
//     names through field:<id> (the caller would choose who may move).
//  2. Frozen (field_frozen, an ErrRejected): a field the Item's current
//     state freezes, on every door. A transition is checked against the
//     state it leaves, so the one entering a state may set its frozen
//     fields; the decision is lifecycle.CheckFrozen's.

// checkPatch applies both rules to a patch. tr is the transition the patch
// travels with, nil for an update or a create; state is the Item's state
// before the write ("" for a create, which enters its initial state).
func checkPatch(ty *typedef.Type, state string, tr *typedef.Transition, fields map[string]any, public bool) error {
	if len(fields) == 0 {
		return nil
	}
	ids := slices.Sorted(maps.Keys(fields))
	var refused []string
	if public {
		refused = ty.KernelWritten(ids)
	}
	if tr != nil {
		refused = append(refused, tr.AllowedFields(ids)...)
	}
	if len(refused) > 0 {
		slices.Sort(refused)
		refused = slices.Compact(refused)
		return &FieldNotWritableError{
			Fields: refused,
			Reason: "the request may not write " + strings.Join(refused, ", "),
		}
	}
	if state == "" {
		return nil
	}
	return fromLifecycle(lifecycle.CheckFrozen(ty, state, ids))
}

// today is the date guards read, from the service's clock.
func (k *Service) today() time.Time {
	return k.now().UTC()
}

// facts is what `allowed` and `except` read of a locked Item.
func facts(it lockedItem) policy.ItemFacts {
	return policy.ItemFacts{Fields: it.Fields, CreatedBy: it.CreatedBy, CreatedOnBehalfOf: it.CreatedOnBehalfOf}
}
