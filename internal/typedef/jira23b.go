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
func (t *Type) checkOffers() error {
	fail := func(format string, args ...any) error {
		return fmt.Errorf("typedef %s v%d: offers: %s", t.ID, t.Version, fmt.Sprintf(format, args...))
	}
	for name, field := range t.Offers {
		if !linkName.MatchString(name) {
			return fail("%q is not a name (lower case, digits and _)", name)
		}
		if field == "" {
			return fail("%s offers no field", name)
		}
		f, ok := t.Field(field)
		switch {
		case !ok:
			return fail("%s: field %q is not declared", name, field)
		case f.Type != Principal:
			return fail("%s: field %s is a %s; an offer names a principal field", name, field, f.Type)
		case f.Multiple:
			return fail("%s: field %s holds several principals; an offer names one", name, field)
		}
	}
	return nil
}

// checkOfferRead validates one hook read of the form container:<name>.
// Only an action hook reads one: a check's reads are what its hold freezes
// and an enrich hook's are its own Item's facts, and neither can freeze or
// depend on a field of another Item.
func checkOfferRead(h Hook, read string) error {
	name, _ := OfferOf(read)
	if !linkName.MatchString(name) {
		return fmt.Errorf("read %q: %q is not an offer's name (container:<name>)", read, name)
	}
	if h.Kind != HookAction {
		return fmt.Errorf("read %q: only an action hook reads an offer of its container, not a %s hook", read, h.Kind)
	}
	return nil
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
func (l LinkType) checkRoles() error {
	if len(l.WritableByRoles) == 0 {
		return nil
	}
	if l.WritableBy != WritableByAnyone {
		return fmt.Errorf("link %s: writable_by is either kernel or a list of roles, not both", l.Name)
	}
	seen := map[string]bool{}
	for _, r := range l.WritableByRoles {
		if !roleNameRe.MatchString(r) {
			return fmt.Errorf("link %s: writable_by role %q is not a role name (lower case, digits, _ and -)", l.Name, r)
		}
		if seen[r] {
			return fmt.Errorf("link %s: writable_by names role %s twice", l.Name, r)
		}
		seen[r] = true
	}
	return nil
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
