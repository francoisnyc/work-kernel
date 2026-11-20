package itemservice

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strconv"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// Multi-value fields (Jira part 2, step 2). A field declared `multiple`
// holds a set, stored as a sorted JSON array without duplicates; an empty
// set is an unset field. A patch replaces it (a list), clears it (null or
// an empty list) or changes it with a set operation ({add, remove}).

// applySets turns a patch's values for multi-value fields into what is
// stored: a list becomes its set (nil if empty), and a set operation is
// applied to the Item's current set. It runs under the Item's row lock,
// so two set operations at once both land: neither works from a set the
// other has already changed.
//
// It returns the patch with those values resolved (copied if anything
// changed) and the set operations asked for, by field, for the event.
// current is nil on create, where a set operation is malformed: a new Item
// has no set to change. A set over its field's max is too_many_values.
func applySets(ty *typedef.Type, current, patch map[string]any, creating bool) (map[string]any, map[string]typedef.SetOp, error) {
	out, copied := patch, false
	var ops map[string]typedef.SetOp
	for _, id := range slices.Sorted(maps.Keys(patch)) {
		f, ok := ty.Field(id)
		v := patch[id]
		// A malformed value is left for CheckFields to refuse.
		if !ok || !f.Multiple || v == nil || !typedef.ValidSetValue(f, v) {
			continue
		}
		var members []string
		if op, isOp, _ := typedef.AsSetOp(f, v); isOp {
			if creating {
				return nil, nil, &InvalidError{Field: id,
					Reason: fmt.Sprintf("field %s: a new Item has no set to add to or remove from; send the list", id)}
			}
			members = typedef.SetMembers(current[id])
			members = slices.DeleteFunc(members, func(m string) bool { return slices.Contains(op.Remove, m) })
			members = typedef.SetMembers(append(members, op.Add...))
			if ops == nil {
				ops = map[string]typedef.SetOp{}
			}
			ops[id] = op
		} else {
			members = typedef.SetMembers(v)
		}
		if f.Max > 0 && len(members) > f.Max {
			return nil, nil, &RejectedError{
				Code:     CodeTooManyValues,
				Reason:   fmt.Sprintf("field %s would hold %d values; it takes at most %d", id, len(members), f.Max),
				Metadata: map[string]string{"field": id, "max": strconv.Itoa(f.Max)},
			}
		}
		if !copied {
			out, copied = maps.Clone(patch), true
		}
		if len(members) == 0 {
			if creating {
				delete(out, id) // nothing to store, and nothing to clear
			} else {
				out[id] = nil // an empty set is an unset field
			}
			continue
		}
		out[id] = typedef.StoredSet(members)
	}
	return out, ops, nil
}

// addSetOps names, in an event's payload, the fields a set operation
// changed: IDs only. What was asked goes to item_event_values, beside the
// values (storeSetOps).
func addSetOps(payload map[string]any, ops map[string]typedef.SetOp) {
	if len(ops) > 0 {
		payload["set_op_field_ids"] = slices.Sorted(maps.Keys(ops))
	}
}

// storeSetOps records what each set operation of an event asked for, in
// item_event_values.field_deltas (the column a reaction's `add` uses for
// its amounts): erasable with the values, never in the payload.
func storeSetOps(ctx context.Context, tx pgx.Tx, id uuid.UUID, itemSeq int64, ops map[string]typedef.SetOp) error {
	if len(ops) == 0 {
		return nil
	}
	deltas := map[string]any{}
	for f, op := range ops {
		d := map[string]any{}
		if len(op.Add) > 0 {
			d["add"] = op.Add
		}
		if len(op.Remove) > 0 {
			d["remove"] = op.Remove
		}
		deltas[f] = d
	}
	// An event whose patch only emptied sets has no values row yet.
	_, err := tx.Exec(ctx,
		`insert into item_event_values (item_id, item_seq, field_values, field_deltas) values ($1, $2, '{}', $3)
		 on conflict (item_id, item_seq) do update set field_deltas = excluded.field_deltas`,
		id, itemSeq, deltas)
	if err != nil {
		return fmt.Errorf("store set operations: %w", err)
	}
	return nil
}
