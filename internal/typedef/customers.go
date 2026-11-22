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
func (t *Type) checkCustomers(r reporter) {
	for _, list := range []struct {
		name string
		ids  []string
	}{{"own", t.Own}, {"shared", t.Shared}} {
		for i, id := range list.ids {
			if !linkName.MatchString(id) {
				r.fail(CodeInvalidName, at(list.name, i), "Name a Type by its id (lower case letters, digits and _)",
					"%s: %q is not a Type ID", list.name, id)
			} else if slices.Contains(list.ids[:i], id) {
				r.fail(CodeDuplicate, at(list.name, i), "Remove the second one", "%s names %s twice", list.name, id)
			}
		}
	}
	for i, id := range t.Own {
		if slices.Contains(t.Shared, id) {
			r.fail(CodeConflict, at("own", i), "List the Type under own or shared, not both",
				"%s is both own and shared: a customer sees their own, or all of them, not both", id)
		}
	}
	for i, name := range t.CustomerFields {
		p := at("customer_fields", i)
		if name == "" {
			r.fail(CodeInvalidValue, p, "Name a field, a link or a rollup, or remove the empty entry", "customer_fields: an empty name")
			continue
		}
		if slices.Contains(t.CustomerFields[:i], name) {
			r.fail(CodeDuplicate, p, "Remove the second one", "customer_fields names %s twice", name)
			continue
		}
		if _, ok := t.Field(name); ok {
			continue
		}
		if _, ok := t.Link(name); ok {
			continue
		}
		if j := slices.IndexFunc(t.Rollups, func(ru Rollup) bool { return ru.ID == name }); j >= 0 {
			// A rollup counts over a link: listed without its link, its
			// count would tell a customer that hidden links exist (review
			// of 2026-11-21). An incoming name is never this version's to
			// list, so a rollup over one is never listed.
			over := t.Rollups[j].Over
			if _, own := t.Link(over); !own || !slices.Contains(t.CustomerFields, over) {
				r.fail(CodeMissingCounterpart, p, "List the link it counts over too, or leave the rollup out",
					"customer_fields: rollup %s counts over %s, which customer_fields does not list", name, over)
			}
			continue
		}
		r.fail(CodeUnknownField, p, "Name a field, a link or a rollup of this version",
			"customer_fields: %s is none of this version's fields, link names or rollups", name)
	}
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
