package typedef

// Jira part 2, step 3b: what a Type may newly declare.
//
//   - offers: {<name>: <principal field>}: what the Type offers to the
//     hooks of the Items contained in its Items. A hook there reads
//     container:<name> (ContainerOfferPrefix) and gets the field's value
//     from its container when it runs. So a package of Types (the sla
//     goals) can tell "whoever the host says" without naming a host.
//   - A link's writable_by takes a list of roles, as a field's.

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// OfferOf splits a hook read naming a container's offer (container:<name>).
// ok is false for any other read: a field or a creator attribute.
func OfferOf(read string) (name string, ok bool) {
	return strings.CutPrefix(read, ContainerOfferPrefix)
}

// checkOffers validates the Type's offers: each name is an identifier and
// each field one declared principal field holding a single value.
func (t *Type) checkOffers(r reporter) {
	r = r.within("offers: ")
	// Sorted: Go randomizes map order, and problems come in a stable one.
	for _, name := range slices.Sorted(maps.Keys(t.Offers)) {
		field, p := t.Offers[name], at("offers", name)
		if !linkName.MatchString(name) {
			r.fail(CodeInvalidName, p, "Name the offer in lower case letters, digits and _", "%q is not a name (lower case, digits and _)", name)
			continue
		}
		if field == "" {
			r.fail(CodeRequired, p, "Name the principal field the container offers", "%s offers no field", name)
			continue
		}
		f, ok := t.Field(field)
		switch {
		case !ok:
			r.fail(CodeUnknownField, p, "Declare the field, or offer a declared principal field", "%s: field %q is not declared", name, field)
		case f.Type != Principal:
			r.fail(CodeWrongFieldType, p, "Offer a principal field", "%s: field %s is a %s; an offer names a principal field", name, field, f.Type)
		case f.Multiple:
			r.fail(CodeWrongFieldType, p, "Offer a field holding one principal", "%s: field %s holds several principals; an offer names one", name, field)
		}
	}
}

// checkOfferRead validates one hook read of the form container:<name>.
// Only an action hook reads one: a check's reads are what its hold freezes
// and an enrich hook's are its own Item's facts, and neither can freeze or
// depend on a field of another Item.
func checkOfferRead(r reporter, p string, h Hook, read string) {
	name, _ := OfferOf(read)
	if !linkName.MatchString(name) {
		r.fail(CodeInvalidName, p, "Write container:<name>, with the name in lower case letters, digits and _",
			"read %q: %q is not an offer's name (container:<name>)", read, name)
		return
	}
	if h.Kind != HookAction {
		r.fail(CodeReadNotAllowed, p, "Read the container's offer from an action hook, or read a field of this Type",
			"read %q: only an action hook reads an offer of its container, not a %s hook", read, h.Kind)
	}
}

// OfferReads returns the offers the Type's hooks read from their Item's
// container, sorted, without duplicates.
func (t *Type) OfferReads() []string {
	var out []string
	for _, h := range t.Hooks {
		for _, r := range h.Reads {
			if name, ok := OfferOf(r); ok && !slices.Contains(out, name) {
				out = append(out, name)
			}
		}
	}
	slices.Sort(out)
	return out
}

// CheckOffers checks offers across Types, where a container's Type is
// known: a Type that creates Items inside its own (a create reaction) must
// offer what those Items' hooks read. Any other container is only known
// when the hook runs, and is judged then.
func CheckOffers(types []*Type) error {
	find := func(id string, version int) *Type {
		for _, t := range types {
			if t.ID == id && t.Version == version {
				return t
			}
		}
		return nil
	}
	for _, host := range types {
		for _, r := range host.Reactions {
			if r.Kind != ReactionCreate {
				continue
			}
			made := find(r.Type, r.Version)
			if made == nil {
				continue // CheckReactions names a missing Type
			}
			for _, h := range made.Hooks {
				for _, read := range h.Reads {
					name, ok := OfferOf(read)
					if !ok {
						continue
					}
					if _, offered := host.Offers[name]; !offered {
						return fmt.Errorf("typedef %s v%d: reaction %s creates %s v%d inside it, whose hook %s reads %s: "+
							"%s offers no %s (offers: {%s: <a principal field>})",
							host.ID, host.Version, r.ID, made.ID, made.Version, h.ID, read, host.ID, name, name)
					}
				}
			}
		}
	}
	return nil
}

// checkRoles validates a link type's writable_by roles.
func (l LinkType) checkRoles(r reporter, p string) {
	if len(l.WritableByRoles) == 0 {
		return
	}
	if l.WritableBy != WritableByAnyone {
		r.fail(CodeConflict, at(p, "writable_by"), "Write writable_by: kernel, or a list of roles, not both",
			"link %s: writable_by is either kernel or a list of roles, not both", l.Name)
		return
	}
	seen := map[string]bool{}
	for i, role := range l.WritableByRoles {
		if !roleNameRe.MatchString(role) {
			r.fail(CodeInvalidName, at(p, "writable_by", i), "Name the role in lower case, digits, _ and -",
				"link %s: writable_by role %q is not a role name (lower case, digits, _ and -)", l.Name, role)
		} else if seen[role] {
			r.fail(CodeDuplicate, at(p, "writable_by", i), "Remove the second one", "link %s: writable_by names role %s twice", l.Name, role)
		}
		seen[role] = true
	}
}

// UnmarshalYAML reads a link type. As for a field, writable_by takes a
// scalar (kernel) or a list of role:<name> entries, stored as
// WritableByRoles without the prefix.
func (l *LinkType) UnmarshalYAML(n *yaml.Node) error {
	type plain LinkType // no methods: decoding it does not recurse here
	var wb *yaml.Node
	rest := *n
	if n.Kind == yaml.MappingNode {
		rest.Content = nil
		for i := 0; i+1 < len(n.Content); i += 2 {
			switch key := n.Content[i].Value; key {
			case "writable_by":
				wb = n.Content[i+1]
				continue
			case "name", "inverse", "targets", "max", "inverse_max", "resolve":
			default:
				// Node.Decode does not carry the decoder's KnownFields.
				return fmt.Errorf("line %d: field %s not found in type typedef.LinkType", n.Content[i].Line, key)
			}
			rest.Content = append(rest.Content, n.Content[i], n.Content[i+1])
		}
	}
	var p plain
	if err := rest.Decode(&p); err != nil {
		return err
	}
	*l = LinkType(p)
	if wb == nil {
		return nil
	}
	switch wb.Kind {
	case yaml.ScalarNode:
		l.WritableBy = WritableBy(wb.Value)
	case yaml.SequenceNode:
		var entries []string
		if err := wb.Decode(&entries); err != nil {
			return err
		}
		if len(entries) == 0 {
			return fmt.Errorf("link %s: writable_by is an empty list; leave it out for anyone", l.Name)
		}
		for _, e := range entries {
			name, ok := strings.CutPrefix(e, AllowedRolePrefix)
			if !ok {
				return fmt.Errorf("link %s: writable_by entry %q: a list takes role:<name> entries only "+
					"(kernel stands alone: writable_by: kernel)", l.Name, e)
			}
			l.WritableByRoles = append(l.WritableByRoles, name) // check validates the name
		}
	default:
		return fmt.Errorf("link %s: writable_by must be kernel or a list of role:<name>", l.Name)
	}
	return nil
}
