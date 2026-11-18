package typedef

import (
	"fmt"
	"slices"
)

// Jira proof of concept, part 1: incoming links from other Types, rollups,
// field effects on transitions, and a link type that targets any Type.

// AnyType, alone in a link type's targets, lets the link point to an Item
// of any Type (a Subscription's `watches`).
const AnyType = "*"

// AllowsTarget reports whether a link of this type may point to an Item of
// the given Type.
func (l LinkType) AllowsTarget(typeID string) bool {
	return slices.Contains(l.Targets, AnyType) || slices.Contains(l.Targets, typeID)
}

// Incoming names the links that other Types declare towards this Type: the
// Items of the `From` Types linked to an Item by their link `Link`. Guards
// read it as links.<Name> (state and category only) and rollups count over
// it. Name must equal the inverse every From Type declares for Link, so one
// name serves reads, search, guards and rollups (checked across Types at
// load, CheckIncoming).
type Incoming struct {
	Name string   `yaml:"name" json:"name"`
	Link string   `yaml:"link" json:"link"`
	From []string `yaml:"from" json:"from"` // Type IDs, any version
}

// LinkSource says where the Items under a link name are found in the links
// table, as seen from an Item of the Type that reads the name.
type LinkSource struct {
	// Outgoing: the Items this one links to (it is the source). Otherwise
	// the Items that link to this one, and From are their Types.
	Outgoing bool
	Link     string   // the declared name, as links.link_type stores it
	From     []string // incoming only: the source Type IDs
}

// LinkSource resolves a link name a guard or a rollup of t may read: a
// declared name (outgoing), the inverse of one of t's own link types that
// may target t (incoming, from t's own Items), or an `incoming` name
// (incoming, from the Types it lists).
func (t *Type) LinkSource(name string) (LinkSource, bool) {
	if l, ok := t.Link(name); ok {
		return LinkSource{Outgoing: true, Link: l.Name}, true
	}
	if l, ok := t.IncomingName(name); ok {
		return LinkSource{Link: l.Name, From: []string{t.ID}}, true
	}
	for _, in := range t.Incoming {
		if in.Name == name {
			return LinkSource{Link: in.Link, From: in.From}, true
		}
	}
	return LinkSource{}, false
}

// checkIncoming validates what one Type's file can tell about its incoming
// declarations; CheckIncoming checks them against the other Types.
func (t *Type) checkIncoming(fail func(string, ...any) error) error {
	taken := map[string]bool{}
	for _, l := range t.Links {
		taken[l.Name], taken[l.Inverse] = true, true
	}
	for _, in := range t.Incoming {
		if in.Name == "" {
			return fail("an incoming declaration has no name")
		}
		where := "incoming " + in.Name
		if !linkName.MatchString(in.Name) {
			return fail("%s: %q is not a link name (lower case letters, digits and _)", where, in.Name)
		}
		if taken[in.Name] {
			return fail("%s: the name %q is used twice among link names, inverses and incoming names", where, in.Name)
		}
		taken[in.Name] = true
		if in.Link == "" {
			return fail("%s: no link: the link type the other Types declare", where)
		}
		if len(in.From) == 0 {
			return fail("%s: no from: the Types that declare link %s", where, in.Link)
		}
		for _, from := range in.From {
			if from == "" {
				return fail("%s: an empty from Type", where)
			}
			if from == t.ID {
				return fail("%s: from names the Type itself (%s); its own links to itself are read by their inverse", where, from)
			}
		}
	}
	return nil
}

// CheckIncoming checks every Type's `incoming` against the set loaded at
// start: each From Type is loaded, some version of it declares Link with
// this Type among its targets, and every such declaration's inverse is the
// incoming name. ParseDir calls it: one Type file cannot know the others.
func CheckIncoming(types []*Type) error {
	byID := map[string][]*Type{}
	for _, t := range types {
		byID[t.ID] = append(byID[t.ID], t)
	}
	for _, t := range types {
		for _, in := range t.Incoming {
			where := fmt.Sprintf("typedef %s v%d: incoming %s", t.ID, t.Version, in.Name)
			for _, from := range in.From {
				versions := byID[from]
				if len(versions) == 0 {
					return fmt.Errorf("%s: from Type %q is not loaded", where, from)
				}
				declared := false
				for _, v := range versions {
					l, ok := v.Link(in.Link)
					if !ok || !l.AllowsTarget(t.ID) {
						continue
					}
					declared = true
					if l.Inverse != in.Name {
						return fmt.Errorf("%s: %s v%d declares link %s with the inverse %q; the incoming name must be that inverse",
							where, v.ID, v.Version, in.Link, l.Inverse)
					}
				}
				if !declared {
					return fmt.Errorf("%s: no version of Type %q declares a link %s that may target %s",
						where, from, in.Link, t.ID)
				}
			}
		}
	}
	return nil
}

// The two things a rollup counts by.
const (
	CountByCategory = "category"
	CountByState    = "state"
)

// Rollup is a value computed when an Item is read: a count of the Items
// linked under Over (a link name the Type can read: declared, own inverse
// or incoming), grouped by state category or by state. It counts only what
// the reader can see; it is never stored and not searchable.
type Rollup struct {
	ID      string `yaml:"id" json:"id"`
	Over    string `yaml:"over" json:"over"`
	CountBy string `yaml:"count_by" json:"count_by"`
}

func (t *Type) checkRollups(fail func(string, ...any) error) error {
	seen := map[string]bool{}
	for _, r := range t.Rollups {
		if r.ID == "" {
			return fail("a rollup has no id")
		}
		where := "rollup " + r.ID
		if !linkName.MatchString(r.ID) {
			return fail("%s: %q is not a rollup id (lower case letters, digits and _)", where, r.ID)
		}
		if seen[r.ID] {
			return fail("%s: declared twice", where)
		}
		seen[r.ID] = true
		if r.Over == "" {
			return fail("%s: no `over`: the link name it counts", where)
		}
		if _, ok := t.LinkSource(r.Over); !ok {
			return fail("%s: counts over %q, which is not a link name the Type can read (a declared link, "+
				"the inverse of a link to itself, or an incoming name)", where, r.Over)
		}
		switch r.CountBy {
		case CountByCategory, CountByState:
		case "":
			return fail("%s: no count_by: category or state", where)
		default:
			return fail("%s: count_by %q is not category or state", where, r.CountBy)
		}
	}
	return nil
}

// checkClears validates each transition's clears. A clear that could never
// run, or that contradicts its own transition, is refused at load:
//   - the field is required by the same transition (judged before the
//     clear, it would be set and unset in one move);
//   - the state the move leaves freezes it (a clear names the field, and
//     naming a frozen field is refused on every door);
//   - the transition's own `allowed` names it (field:<id>);
//   - only the kernel writes it (the public door could never make the move);
//   - the transition is held by a check or a held reaction: a held move is
//     written in two steps, and which one clears is not designed (Later).
func (t *Type) checkClears(fail func(string, ...any) error) error {
	for _, tr := range t.Transitions {
		if len(tr.Clears) == 0 {
			continue
		}
		name := tr.From + " -> " + tr.To
		from, _ := t.State(tr.From)
		seen := map[string]bool{}
		for _, id := range tr.Clears {
			f, ok := t.Field(id)
			switch {
			case !ok:
				return fail("transition %s: clears %q, which is not a declared field", name, id)
			case seen[id]:
				return fail("transition %s: clears %q twice", name, id)
			case slices.Contains(tr.Required, id):
				return fail("transition %s: requires and clears %q", name, id)
			case slices.Contains(from.Frozen, id):
				return fail("transition %s: clears %q, which state %s freezes", name, id, tr.From)
			case slices.Contains(tr.Allowed, AllowedFieldPrefix+id):
				return fail("transition %s: clears %q, which its own allowed list names", name, id)
			case f.WritableBy == WritableByKernel:
				return fail("transition %s: clears %q, which only the kernel writes", name, id)
			}
			seen[id] = true
		}
		for _, h := range t.Hooks {
			if h.Kind == HookCheck && h.From == tr.From && h.To == tr.To {
				return fail("transition %s: it clears fields and is held by the check %s; a held transition may not clear fields",
					name, h.ID)
			}
		}
		for _, r := range t.Reactions {
			if r.Kind == ReactionHeld && r.State == tr.To && (len(r.From) == 0 || slices.Contains(r.From, tr.From)) {
				return fail("transition %s: it clears fields and is held by the reaction %s; a held transition may not clear fields",
					name, r.ID)
			}
		}
	}
	return nil
}
