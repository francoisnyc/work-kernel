package itemservice

import (
	"context"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/francoisnyc/work-kernel/internal/item"
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
// writer is who writes on the public door (with roles read, when the patch
// names a role-written field), nil on the internal door: the kernel's own
// writers are bound by their declarations, not by writable_by.
func checkPatch(ty *typedef.Type, state string, tr *typedef.Transition, fields map[string]any, writer *policy.Who) error {
	if len(fields) == 0 {
		return nil
	}
	ids := slices.Sorted(maps.Keys(fields))
	var refused []string
	if writer != nil {
		refused = ty.KernelWritten(ids)
		for _, id := range ty.RoleWritten(ids) {
			if f, _ := ty.Field(id); !policy.MayWriteRoleField(f, *writer) {
				refused = append(refused, id)
			}
		}
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

// publicWriter is the public door's writer for checkPatch: the principal,
// with its roles (and its user's) read only when the patch names a field
// that only roles may write. The internal door passes nil instead.
func publicWriter(ctx context.Context, q policy.Querier, ty *typedef.Type, p item.Principal,
	fields map[string]any) (*policy.Who, error) {
	who := &policy.Who{Actor: p.Actor, OnBehalfOf: p.OnBehalfOf}
	if len(ty.RoleWritten(slices.Collect(maps.Keys(fields)))) == 0 {
		return who, nil
	}
	var err error
	if who.ActorRoles, err = policy.Roles(ctx, q, nsOf(p), p.Actor); err != nil {
		return nil, err
	}
	if p.OnBehalfOf != "" {
		if who.OnBehalfOfRoles, err = policy.Roles(ctx, q, nsOf(p), p.OnBehalfOf); err != nil {
			return nil, err
		}
	}
	return who, nil
}

// checkInvariants is the last check of every write that changes fields:
// the fields as the write leaves them keep every invariant of the Type
// (invariant_failed, metadata invariant).
func checkInvariants(ty *typedef.Type, merged map[string]any) error {
	return fromLifecycle(lifecycle.CheckInvariants(ty, merged))
}

// today is the date guards read, from the service's clock.
func (k *Service) today() time.Time {
	return k.now().UTC()
}

// facts is what `allowed` and `except` read of a locked Item.
func facts(it lockedItem) policy.ItemFacts {
	return policy.ItemFacts{Fields: it.Fields, CreatedBy: it.CreatedBy, CreatedOnBehalfOf: it.CreatedOnBehalfOf}
}
