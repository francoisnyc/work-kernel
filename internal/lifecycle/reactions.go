package lifecycle

import (
	"fmt"
	"maps"
	"slices"

	"cel.dev/cel-go/common/types"

	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// Move and create reactions (Jira part 2, step 2): which a trigger runs,
// and what their expressions evaluate to. Pure, like the rest of the
// package: the item service judges and writes, the workflow only queues.

// isAfterReaction reports whether r is a move or a create reaction.
func isAfterReaction(r typedef.Reaction) bool {
	return r.Kind == typedef.ReactionMove || r.Kind == typedef.ReactionCreate || r.Kind == typedef.ReactionSet
}

// EnterReactions returns the move, create and set reactions an Item of
// Type t runs on entering `to` from `from`, in the Type's order. One that
// names `from` states runs only when the entry comes from one of them.
// Creation (from "") enters the initial state: it runs the reactions of
// that state that name no `from`.
func EnterReactions(t *typedef.Type, from, to string) []typedef.Reaction {
	if from == "" && to != t.InitialState {
		return nil
	}
	var out []typedef.Reaction
	for _, r := range t.Reactions {
		if isAfterReaction(r) && r.On == typedef.OnEnter && r.State == to &&
			(len(r.From) == 0 || (from != "" && slices.Contains(r.From, from))) {
			out = append(out, r)
		}
	}
	return out
}

// InsideReactions returns the reactions of Type t that the creation of an
// Item of childType inside one of its Items triggers, in the Type's order.
func InsideReactions(t *typedef.Type, childType string) []typedef.Reaction {
	var out []typedef.Reaction
	for _, r := range t.Reactions {
		if r.On == typedef.OnCreatedInside && slices.Contains(r.Of, childType) {
			out = append(out, r)
		}
	}
	return out
}

// ReactsToChildren reports whether Type t has a reaction triggered by the
// creation of an Item of childType inside one of its Items: such a child's
// create is told to the container's workflow.
func ReactsToChildren(t *typedef.Type, childType string) bool {
	return len(InsideReactions(t, childType)) > 0
}

// ReactionWhen evaluates a reaction's `when` over the Item's own fields
// (on: enter). No condition holds; one that cannot be evaluated does not.
func ReactionWhen(t *typedef.Type, r typedef.Reaction, fields map[string]any) bool {
	if r.WhenProgram == nil {
		return r.When == ""
	}
	vars := make(map[string]any, len(t.Fields))
	addFieldVars(vars, t, fields)
	return holds(r, vars)
}

// ChildWhen evaluates a created_inside reaction's `when` over the fields
// of the Item created inside, as child.<id>. A field the child's Type
// version does not declare reads as null, like an unset one.
func ChildWhen(r typedef.Reaction, child *typedef.Type, fields map[string]any) bool {
	if r.WhenProgram == nil {
		return r.When == ""
	}
	own := make(map[string]any, len(child.Fields))
	addFieldVars(own, child, fields)
	for _, id := range r.ChildReads {
		if _, declared := own[id]; !declared {
			own[id] = types.NullValue
		}
	}
	return holds(r, map[string]any{"child": own})
}

func holds(r typedef.Reaction, vars map[string]any) bool {
	out, _, err := r.WhenProgram.Eval(vars)
	if err != nil {
		return false
	}
	ok, isBool := out.Value().(bool)
	return isBool && ok
}

// CreateFields evaluates a create reaction's field expressions over the
// source's fields, cause.at and today (the UTC date of cause.at), and
// returns the created Item's fields in the stored form of `created`'s
// field types. A null result leaves the field unset. An expression that
// cannot be evaluated, or whose result is no value of its field, is an
// EffectFailed rejection naming the field.
func CreateFields(t *typedef.Type, r typedef.Reaction, created *typedef.Type, in Input) (map[string]any, error) {
	failed := func(field string) error {
		return &Rejection{Code: EffectFailed, Field: field,
			Reason: fmt.Sprintf("reaction %s: field %s of the created %s could not be computed", r.ID, field, created.ID)}
	}
	vars := make(map[string]any, len(t.Fields)+4)
	vars[typedef.VarCauseAt] = in.CauseAt.UTC()
	vars[typedef.VarToday] = dateOf(in.CauseAt)
	addFieldVars(vars, t, in.Fields)
	vars["linked"] = linkedVars(in.Linked.Own, in.Linked.OwnTypes)
	vars["container"] = map[string]any{"linked": linkedVars(in.Linked.Container, in.Linked.ContainerTypes)}
	out := map[string]any{}
	for _, id := range slices.Sorted(maps.Keys(r.Fields)) {
		prg := r.FieldPrograms[id]
		f, declared := created.Field(id)
		if prg == nil || !declared {
			return nil, failed(id)
		}
		res, _, err := prg.Eval(vars)
		if err != nil {
			return nil, failed(id)
		}
		v, ok := storedValue(f, res.Value())
		if !ok {
			return nil, failed(id)
		}
		if v == nil {
			continue
		}
		if created.CheckFields(map[string]any{id: v}, false) != nil {
			return nil, failed(id)
		}
		out[id] = v
	}
	return out, nil
}
