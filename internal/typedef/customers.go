package typedef

import (
	"fmt"
	"slices"
)

// Customers (Jira part 2, step 4a). A container's Type says which Types a
// customer may create inside its Items (Own: and see when they are their
// own requests; Shared: and see, all of them, inside a request of theirs).
// Any Type whose Items a customer may see lists what they see of them in
// CustomerFields: field IDs, its own link names and rollup IDs. Nothing
// else is shown to a customer, a field added in a later version included.
//
// Own and Shared name Type IDs, all versions: changing what a Type means
// for the audience takes a new Type ID, never a new version.

// checkCustomers validates the Type's own declarations: Type IDs in own and
// shared (each once, never in both), and customer_fields naming its own
// fields, link names and rollups (each once).
func (t *Type) checkCustomers() error {
	fail := func(format string, args ...any) error {
		return fmt.Errorf("typedef %s v%d: %s", t.ID, t.Version, fmt.Sprintf(format, args...))
	}
	for _, list := range []struct {
		name string
		ids  []string
	}{{"own", t.Own}, {"shared", t.Shared}} {
		for i, id := range list.ids {
			if !linkName.MatchString(id) {
				return fail("%s: %q is not a Type ID", list.name, id)
			}
			if slices.Contains(list.ids[:i], id) {
				return fail("%s names %s twice", list.name, id)
			}
		}
	}
	for _, id := range t.Own {
		if slices.Contains(t.Shared, id) {
			return fail("%s is both own and shared: a customer sees their own, or all of them, not both", id)
		}
	}
	for i, name := range t.CustomerFields {
		if name == "" {
			return fail("customer_fields: an empty name")
		}
		if slices.Contains(t.CustomerFields[:i], name) {
			return fail("customer_fields names %s twice", name)
		}
		if _, ok := t.Field(name); ok {
			continue
		}
		if _, ok := t.Link(name); ok {
			continue
		}
		if slices.ContainsFunc(t.Rollups, func(r Rollup) bool { return r.ID == name }) {
			continue
		}
		return fail("customer_fields: %s is none of this version's fields, link names or rollups", name)
	}
	return nil
}

// CustomerSees reports whether a customer sees the field, link name or
// rollup of Items of this version.
func (t *Type) CustomerSees(name string) bool { return slices.Contains(t.CustomerFields, name) }

// CheckCustomers checks across a set of Types that every Type named in own
// or shared is in the set.
func CheckCustomers(types []*Type) error {
	known := map[string]bool{}
	for _, t := range types {
		known[t.ID] = true
	}
	for _, t := range types {
		for _, id := range append(slices.Clone(t.Own), t.Shared...) {
			if !known[id] {
				return fmt.Errorf("typedef %s v%d: admits customers to %s, which is not loaded", t.ID, t.Version, id)
			}
		}
	}
	return nil
}
