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
//     door, a field only the kernel writes (writable_by: kernel), a field
//     the writer may not write (policy.MayWriteField: people, agents and
//     roles; a field that lists none is closed to agents), and a field the
//     Item's current state limits to its requester when the writer is not
//     the requester (policy.MayWriteInState; judged, like frozen, against
//     the state a transition leaves); on every door, in a transition's
//     patch, a field the transition's own `allowed` names through
//     field:<id> (the caller would choose who may move).
//  2. Frozen (field_frozen, an ErrRejected): a field the Item's current
//     state freezes, on every door. A transition is checked against the
//     state it leaves, so the one entering a state may set its frozen
//     fields; the decision is lifecycle.CheckFrozen's.
//
// Create asks the writer's part of rule 1 later than the rest, after what
// is not found, the start block and the create power (checkWriter).

// checkPatch applies both rules to a patch. tr is the transition the patch
// travels with, nil for an update or a create; state is the Item's state
// before the write ("" for a create, which enters its initial state).
// writer is who writes on the public door (with roles read, when the patch
// names a role-written field), nil on the internal door: the kernel's own
// writers are bound by their declarations, not by writable_by nor by
// requester_only. facts says who created the Item (its requester).
func checkPatch(ty *typedef.Type, state string, tr *typedef.Transition, fields map[string]any,
	writer *policy.Who, facts policy.ItemFacts) error {
	if len(fields) == 0 {
		return nil
	}
	ids := slices.Sorted(maps.Keys(fields))
	var refused, notRequester []string
	agent := false
	if writer != nil {
		refused = ty.KernelWritten(ids)
		var byWriter []string
		byWriter, agent = writerRefuses(ty, ids, *writer)
		refused = append(refused, byWriter...)
		notRequester = requesterRefuses(ty, state, ids, refused, *writer, facts)
	}
	if tr != nil {
		refused = append(refused, tr.AllowedFields(ids)...)
	}
	if err := notWritable(refused, agent, state, notRequester); err != nil {
		return err
	}
	if state == "" {
		return nil
	}
	return fromLifecycle(lifecycle.CheckFrozen(ty, state, ids))
}

// checkKernelFields is rule 1 for a create, without the writer's part: a
// field only the kernel writes is refused at once.
func checkKernelFields(ty *typedef.Type, fields map[string]any) error {
	return notWritable(ty.KernelWritten(slices.Sorted(maps.Keys(fields))), false, "", nil)
}

// checkWriter is the writer's part of rule 1 alone: Create asks it after
// what is not found, the start block and the create power.
func checkWriter(ty *typedef.Type, fields map[string]any, writer policy.Who) error {
	refused, agent := writerRefuses(ty, slices.Sorted(maps.Keys(fields)), writer)
	return notWritable(refused, agent, "", nil)
}

// writerRefuses returns the fields among ids, other than kernel-written
// ones, that who may not write, and whether who is an agent judged as
// itself (not one acting for a customer), whose refusal says why.
func writerRefuses(ty *typedef.Type, ids []string, who policy.Who) (refused []string, agent bool) {
	for _, id := range ids {
		f, ok := ty.Field(id)
		if !ok || f.WritableBy == typedef.WritableByKernel {
			continue
		}
		if !policy.MayWriteField(f, who) {
			refused = append(refused, id)
		}
	}
	agent = item.IsKind(who.Actor, item.KindAgent) && policy.CustomerOf(item.Principal{Actor: who.Actor, OnBehalfOf: who.OnBehalfOf}) == ""
	return refused, agent
}

// requesterRefuses returns the fields among ids that the Item's state
// limits to its requester when who is not the requester, leaving out those
// already refused (one cause per field: who may write it comes first). A
// create (state "") is always its requester's.
func requesterRefuses(ty *typedef.Type, state string, ids, refused []string, who policy.Who, facts policy.ItemFacts) []string {
	st, ok := ty.State(state)
	if !ok || len(st.RequesterOnly) == 0 {
		return nil
	}
	var out []string
	for _, id := range ids {
		if !slices.Contains(refused, id) && !policy.MayWriteInState(st, id, who, facts) {
			out = append(out, id)
		}
	}
	return out
}

// notWritable is the refusal of fields not writable, sorted, or nil. Its
// reason says each cause, with its fields: an agent is told that its Type
// opens only some fields to agents; anyone else not the requester, that
// only the requester writes the fields the state limits to them.
func notWritable(refused []string, agent bool, state string, notRequester []string) error {
	if len(refused) == 0 && len(notRequester) == 0 {
		return nil
	}
	var causes []string
	if len(refused) > 0 {
		refused = sortedSet(refused)
		reason := "the request may not write " + strings.Join(refused, ", ")
		if agent {
			reason = "an agent writes only the fields its Type opens to agents, or to a role it holds: " + reason
		}
		causes = append(causes, reason)
	}
	if len(notRequester) > 0 {
		notRequester = sortedSet(notRequester)
		causes = append(causes, "only the requester writes these fields while the Item is in "+state+
			": the request may not write "+strings.Join(notRequester, ", "))
	}
	return &FieldNotWritableError{Fields: sortedSet(append(refused, notRequester...)), Reason: strings.Join(causes, "; ")}
}

// sortedSet returns ids sorted, without duplicates.
func sortedSet(ids []string) []string {
	out := slices.Clone(ids)
	slices.Sort(out)
	return slices.Compact(out)
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
