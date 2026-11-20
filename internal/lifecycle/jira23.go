package lifecycle

import (
	"fmt"
	"maps"
	"slices"
	"time"

	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/common/types/ref"
	"cel.dev/cel-go/common/types/traits"

	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// Jira part 2, step 3: the pure side of acting timers at an instant, of
// reactions that move children or set a field, and of a create reaction's
// reads of linked Items.

// LinkedFields is what a create reaction may read beyond its source: per
// link name, the fields of the Item it points to, already cut down to the
// fields that Item's Type lets reactions read. Own are the source's links,
// Container its container's.
//
// OwnTypes and ContainerTypes, when given, are those Items' Types: their
// fields then read as any field does (a date as a timestamp, an unset one
// as null). Without a Type the values read in their stored form.
type LinkedFields struct {
	Own, Container           map[string]map[string]any
	OwnTypes, ContainerTypes map[string]*typedef.Type
}

// linkedVars builds the CEL value of `linked` (or container.linked): per
// link, the fields of the Item it points to.
func linkedVars(items map[string]map[string]any, tys map[string]*typedef.Type) map[string]any {
	out := make(map[string]any, len(items))
	for link, fields := range items {
		vars := map[string]any{}
		if ty := tys[link]; ty != nil {
			// Only what was handed over is readable: a field the Type
			// declares but the caller left out stays unknown.
			all := map[string]any{}
			addFieldVars(all, ty, fields)
			for id := range fields {
				if v, ok := all[id]; ok {
					vars[id] = v
				}
			}
		} else {
			for id, v := range fields {
				if v == nil {
					vars[id] = types.NullValue
				} else {
					vars[id] = v
				}
			}
		}
		out[link] = vars
	}
	return out
}

// storedSet turns a CEL list into the stored form of a multi-value field:
// its members sorted, each once; an empty list is unset (nil).
func storedSet(f typedef.Field, v any) (any, bool) {
	var members []string
	add := func(m any) bool {
		if rv, isVal := m.(ref.Val); isVal {
			m = rv.Value()
		}
		switch x := m.(type) {
		case string:
			members = append(members, x)
		case time.Time:
			// A date is a timestamp at 00:00 UTC to CEL.
			s, ok := storedValue(typedef.Field{ID: f.ID, Type: typedef.Date}, x)
			if !ok || f.Type != typedef.Date {
				return false
			}
			members = append(members, s.(string))
		default:
			return false
		}
		return true
	}
	switch l := v.(type) {
	case traits.Lister:
		for it := l.Iterator(); it.HasNext() == types.True; {
			if !add(it.Next()) {
				return nil, false
			}
		}
	case []any:
		for _, m := range l {
			if !add(m) {
				return nil, false
			}
		}
	case []string:
		members = append(members, l...)
	case []ref.Val:
		for _, m := range l {
			if !add(m) {
				return nil, false
			}
		}
	case []time.Time:
		for _, m := range l {
			if !add(m) {
				return nil, false
			}
		}
	default:
		return nil, false
	}
	members = typedef.SetMembers(members)
	if len(members) == 0 {
		return nil, true
	}
	return typedef.StoredSet(members), true
}

// ActingAtTimers returns the acting timers at an instant of a state, in
// the Type's order.
func ActingAtTimers(t *typedef.Type, state string) []typedef.ActingTimer {
	s, ok := t.State(state)
	if !ok {
		return nil
	}
	var out []typedef.ActingTimer
	for _, a := range s.Acting {
		if a.At != "" {
			out = append(out, a)
		}
	}
	return out
}

// ActingEffect evaluates an acting timer at an instant for the instant its
// field holds: whether its `when` holds (no condition: it does), and its
// `sets`, with cause.at = the instant and today its UTC date. Values come
// back in their fields' stored form, nil to unset. An expression that
// cannot be computed, or whose result is no value of its field, is an
// EffectFailed rejection naming the field.
func ActingEffect(t *typedef.Type, timer typedef.ActingTimer, fields map[string]any, instant time.Time) (sets map[string]any, applies bool, err error) {
	vars := make(map[string]any, len(t.Fields)+2)
	vars[typedef.VarCauseAt] = instant.UTC()
	vars[typedef.VarToday] = dateOf(instant)
	addFieldVars(vars, t, fields)
	if timer.When != "" {
		if timer.WhenProgram == nil {
			return nil, false, nil
		}
		out, _, err := timer.WhenProgram.Eval(vars)
		if err != nil {
			return nil, false, nil // a condition that cannot be evaluated does not hold
		}
		if ok, isBool := out.Value().(bool); !isBool || !ok {
			return nil, false, nil
		}
	}
	sets = make(map[string]any, len(timer.Sets))
	for _, id := range slices.Sorted(maps.Keys(timer.Sets)) {
		failed := &Rejection{Code: EffectFailed, Field: id,
			Reason: fmt.Sprintf("acting timer %s: the effect on %s could not be computed", timer.ID, id)}
		prg, f := timer.SetPrograms[id], typedef.Field{}
		var declared bool
		if f, declared = t.Field(id); prg == nil || !declared {
			return nil, false, failed
		}
		res, _, err := prg.Eval(vars)
		if err != nil {
			return nil, false, failed
		}
		v, ok := storedValue(f, res.Value())
		if !ok || (v != nil && t.CheckFields(map[string]any{id: v}, false) != nil) {
			return nil, false, failed
		}
		sets[id] = v
	}
	return sets, true, nil
}

// ChildMatches reports whether a contained Item of Type child, with these
// fields, is one a children reaction moves: its Type is one of
// children.of and children.when holds (a field the child's version does
// not declare reads as null).
func ChildMatches(r typedef.Reaction, child *typedef.Type, fields map[string]any) bool {
	c := r.Children
	if c == nil || !slices.Contains(c.Of, child.ID) {
		return false
	}
	if c.WhenProgram == nil {
		return c.When == ""
	}
	own := make(map[string]any, len(child.Fields))
	addFieldVars(own, child, fields)
	for _, id := range c.Reads {
		if _, declared := own[id]; !declared {
			own[id] = types.NullValue
		}
	}
	out, _, err := c.WhenProgram.Eval(map[string]any{"child": own})
	if err != nil {
		return false
	}
	ok, isBool := out.Value().(bool)
	return isBool && ok
}

// SetValues evaluates a set reaction's expressions over the source's
// fields, cause.at and today, for a target of Type target: field ID ->
// value in the field's stored form, nil to unset. Failures as CreateFields.
func SetValues(t *typedef.Type, r typedef.Reaction, target *typedef.Type, in Input) (map[string]any, error) {
	failed := func(field string) error {
		return &Rejection{Code: EffectFailed, Field: field,
			Reason: fmt.Sprintf("reaction %s: field %s of the %s it writes could not be computed", r.ID, field, target.ID)}
	}
	vars := make(map[string]any, len(t.Fields)+2)
	vars[typedef.VarCauseAt] = in.CauseAt.UTC()
	vars[typedef.VarToday] = dateOf(in.CauseAt)
	addFieldVars(vars, t, in.Fields)
	out := make(map[string]any, len(r.Set))
	for _, id := range slices.Sorted(maps.Keys(r.Set)) {
		prg := r.SetPrograms[id]
		f, declared := target.Field(id)
		// A reaction sets one value, and never who.
		if prg == nil || !declared || f.Multiple || f.Type == typedef.Principal {
			return nil, failed(id)
		}
		res, _, err := prg.Eval(vars)
		if err != nil {
			return nil, failed(id)
		}
		v, ok := storedValue(f, res.Value())
		if !ok || (v != nil && target.CheckFields(map[string]any{id: v}, false) != nil) {
			return nil, failed(id)
		}
		out[id] = v
	}
	return out, nil
}
