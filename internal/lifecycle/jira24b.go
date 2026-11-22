package lifecycle

import (
	"slices"

	"cel.dev/cel-go/common/types"

	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// Jira 2.4b, PR 1: the pure side of the building blocks. A guard reads the
// Item's container and its contained Items (Around); an effect reads the
// container's fields (ContainerFields); a for_each create reads its member
// (Each).

// Around is what a guard reads around the Item (kernel change 4): its
// container's state and category (nil at the top level) and, by Type, the
// state and category of the Items it contains. Every one of them, whoever
// asks: a guard judges the data, not the reader. A nil *Around means "not
// read": a guard that reads either does not hold, so a caller that forgets
// fails safe.
type Around struct {
	Container *LinkedItem
	Children  map[string][]LinkedItem
}

// ContainerFields is the Item's container as an effect reads it
// (container.<field>, kernel change 1): its Type and its stored fields.
// Only a field the container's Type lists in readable_by_reactions reads
// as its value; any other, and every field at the top level (no Type),
// reads as unset.
type ContainerFields struct {
	Type   *typedef.Type
	Fields map[string]any
}

// GuardReadsAround reports whether a transition's guard reads its
// container or its contained Items: the caller must then read them.
func GuardReadsAround(tr typedef.Transition) bool {
	return tr.GuardContainer || len(tr.GuardChildren) > 0
}

// aroundVars sets container and children for a guard that reads them.
func aroundVars(vars map[string]any, tr typedef.Transition, a *Around) {
	if a.Container == nil {
		vars[typedef.ContainerVar] = types.NullValue
	} else {
		vars[typedef.ContainerVar] = map[string]string{typedef.GuardKeyState: a.Container.State, typedef.GuardKeyCategory: a.Container.Category}
	}
	children := make(map[string][]map[string]string, len(tr.GuardChildren))
	for _, ty := range tr.GuardChildren {
		list := make([]map[string]string, 0, len(a.Children[ty]))
		for _, c := range a.Children[ty] {
			list = append(list, map[string]string{typedef.GuardKeyState: c.State, typedef.GuardKeyCategory: c.Category})
		}
		children[ty] = list
	}
	vars[typedef.ChildrenVar] = children
}

// containerVars builds what container.<field> reads for the names given:
// each a readable field's value as a CEL variable, or null.
func containerVars(c ContainerFields, names []string) map[string]any {
	out := make(map[string]any, len(names))
	var all map[string]any
	if c.Type != nil {
		all = map[string]any{}
		addFieldVars(all, c.Type, c.Fields)
	}
	for _, name := range names {
		v, ok := all[name]
		if !ok || c.Type == nil || !c.Type.ReadableByReaction(name) {
			v = types.NullValue
		}
		out[name] = v
	}
	return out
}

// eachVar is a member as `each` reads it: a string, or a date as a
// timestamp.
func eachVar(f typedef.Field, member string) any {
	if f.Type == typedef.Date {
		if d, ok := typedef.ParseDate(member); ok {
			return d
		}
	}
	return member
}

// ForEachMembers returns the members of a for_each create reaction's
// field on the source, sorted (the order the Items are made in); nil for
// a reaction without for_each, or an unset field.
func ForEachMembers(t *typedef.Type, r typedef.Reaction, fields map[string]any) []string {
	if r.ForEach == "" {
		return nil
	}
	members := typedef.SetMembers(fields[r.ForEach])
	slices.Sort(members)
	return members
}

// SiblingClash reports, for a write leaving an Item of t in state with
// these fields, which of its unique_among_siblings fields (kernel change
// 9) holds a value another Item of the same Type in the same container
// holds in one of the field's states. siblings are those other Items:
// their state and fields. It returns the first field, in the Type's order.
func SiblingClash(t *typedef.Type, state string, fields map[string]any, siblings []Sibling) (string, bool) {
	for _, f := range t.UniqueFields() {
		v, _ := fields[f.ID].(string)
		if v == "" || !slices.Contains(f.UniqueAmongSiblings, state) {
			continue
		}
		for _, s := range siblings {
			if slices.Contains(f.UniqueAmongSiblings, s.State) && s.Fields[f.ID] == v {
				return f.ID, true
			}
		}
	}
	return "", false
}

// Sibling is another Item of the same Type in the same container, as
// SiblingClash reads it.
type Sibling struct {
	State  string
	Fields map[string]any
}
