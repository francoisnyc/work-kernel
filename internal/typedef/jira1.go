package typedef

import (
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
func (t *Type) checkIncoming(r reporter) {
	taken := map[string]bool{}
	for _, l := range t.Links {
		taken[l.Name], taken[l.Inverse] = true, true
	}
	for i, in := range t.Incoming {
		p := at("incoming", i)
		if in.Name == "" {
			r.fail(CodeRequired, at(p, "name"), "Name it as the inverse of the link the other Types declare",
				"an incoming declaration has no name")
			continue
		}
		where := "incoming " + in.Name
		switch {
		case !linkName.MatchString(in.Name):
			r.fail(CodeInvalidName, at(p, "name"), "Use lower case letters, digits and _, starting with a letter",
				"%s: %q is not a link name (lower case letters, digits and _)", where, in.Name)
		case taken[in.Name]:
			r.fail(CodeDuplicate, at(p, "name"), "Rename one of the two: link names, inverses and incoming names share one space",
				"%s: the name %q is used twice among link names, inverses and incoming names", where, in.Name)
		}
		taken[in.Name] = true
		if in.Link == "" {
			r.fail(CodeRequired, at(p, "link"), "Name the link type the other Types declare",
				"%s: no link: the link type the other Types declare", where)
		}
		if len(in.From) == 0 {
			r.fail(CodeRequired, at(p, "from"), "List the Types that declare the link",
				"%s: no from: the Types that declare link %s", where, in.Link)
		}
		for j, from := range in.From {
			switch from {
			case "":
				r.fail(CodeInvalidValue, at(p, "from", j), "Name a Type, or remove the empty entry", "%s: an empty from Type", where)
			case t.ID:
				r.fail(CodeConflict, at(p, "from", j), "Remove the Type itself: read its links to itself by their inverse",
					"%s: from names the Type itself (%s); its own links to itself are read by their inverse", where, from)
			}
		}
	}
}

// CheckIncoming checks every Type's `incoming` against the set loaded at
// start: each From Type is loaded, some version of it declares Link with
// this Type among its targets, and every such declaration's inverse is the
// incoming name. ParseDir calls it: one Type file cannot know the others.
func CheckIncoming(types []*Type) error {
	var c crossCheck
	byID := map[string][]*Type{}
	for _, t := range types {
		byID[t.ID] = append(byID[t.ID], t)
	}
	for _, t := range types {
		for i, in := range t.Incoming {
			rep := c.on(t).within("incoming %s: ", in.Name)
			for j, from := range in.From {
				versions := byID[from]
				if len(versions) == 0 {
					rep.fail(CodeUnknownType, at("incoming", i, "from", j), "Load that Type too, or name a loaded one",
						"from Type %q is not loaded", from)
					continue
				}
				declared := false
				for _, v := range versions {
					l, ok := v.Link(in.Link)
					if !ok || !l.AllowsTarget(t.ID) {
						continue
					}
					declared = true
					if l.Inverse != in.Name {
						rep.fail(CodeInverseMismatch, at("incoming", i, "name"), "Name it as the link's inverse",
							"%s v%d declares link %s with the inverse %q; the incoming name must be that inverse",
							v.ID, v.Version, in.Link, l.Inverse)
						break
					}
				}
				if !declared {
					rep.fail(CodeUnknownLink, at("incoming", i, "link"), "Name a link the Type declares, with this Type among its targets",
						"no version of Type %q declares a link %s that may target %s", from, in.Link, t.ID)
				}
			}
		}
	}
	return c.err()
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

func (t *Type) checkRollups(r reporter) {
	seen := map[string]bool{}
	for i, ru := range t.Rollups {
		p := at("rollups", i)
		if ru.ID == "" {
			r.fail(CodeRequired, at(p, "id"), "Give the rollup an id, such as open_tasks", "a rollup has no id")
			continue
		}
		where := "rollup " + ru.ID
		switch {
		case !linkName.MatchString(ru.ID):
			r.fail(CodeInvalidName, at(p, "id"), "Use lower case letters, digits and _, starting with a letter",
				"%s: %q is not a rollup id (lower case letters, digits and _)", where, ru.ID)
		case seen[ru.ID]:
			r.fail(CodeDuplicate, at(p, "id"), "Rename one of the two rollups", "%s: declared twice", where)
		}
		seen[ru.ID] = true
		if ru.Over == "" {
			r.fail(CodeRequired, at(p, "over"), "Name the link the rollup counts over", "%s: no `over`: the link name it counts", where)
		} else if _, ok := t.LinkSource(ru.Over); !ok {
			r.fail(CodeUnknownLink, at(p, "over"), "Name a declared link, the inverse of a link to this Type, or an incoming name",
				"%s: counts over %q, which is not a link name the Type can read (a declared link, "+
					"the inverse of a link to itself, or an incoming name)", where, ru.Over)
		}
		switch ru.CountBy {
		case CountByCategory, CountByState:
		case "":
			r.fail(CodeRequired, at(p, "count_by"), "Write count_by: category or state", "%s: no count_by: category or state", where)
		default:
			r.fail(CodeInvalidValue, at(p, "count_by"), "Write count_by: category or state", "%s: count_by %q is not category or state", where, ru.CountBy)
		}
	}
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
func (t *Type) checkClears(r reporter) {
	for i, tr := range t.Transitions {
		if len(tr.Clears) == 0 {
			continue
		}
		p := at("transitions", i)
		name := tr.From + " -> " + tr.To
		from, _ := t.State(tr.From)
		seen := map[string]bool{}
		for j, id := range tr.Clears {
			f, ok := t.Field(id)
			q := at(p, "clears", j)
			switch {
			case !ok:
				r.fail(CodeUnknownField, q, "Declare the field, or remove it from clears",
					"transition %s: clears %q, which is not a declared field", name, id)
			case seen[id]:
				r.fail(CodeDuplicate, q, "Remove the second one", "transition %s: clears %q twice", name, id)
			case slices.Contains(tr.Required, id):
				r.fail(CodeConflict, q, "Either require the field or clear it, not both", "transition %s: requires and clears %q", name, id)
			case slices.Contains(from.Frozen, id):
				r.fail(CodeWritesFrozenField, q, "Clear it on another transition, or stop freezing it in the state the move leaves",
					"transition %s: clears %q, which state %s freezes", name, id, tr.From)
			case slices.Contains(tr.Allowed, AllowedFieldPrefix+id):
				r.fail(CodeConflict, q, "Clear another field: the move may not unset who may make it",
					"transition %s: clears %q, which its own allowed list names", name, id)
			case f.WritableBy == WritableByKernel:
				r.fail(CodeWritesKernelField, q, "Clear a field people write, or let a hook clear it",
					"transition %s: clears %q, which only the kernel writes", name, id)
			}
			seen[id] = true
		}
		for _, h := range t.Hooks {
			if h.Kind == HookCheck && h.From == tr.From && h.To == tr.To {
				r.fail(CodeHeldTransition, at(p, "clears"), "Remove clears, or clear on another transition that is not held",
					"transition %s: it clears fields and is held by the check %s; a held transition may not clear fields", name, h.ID)
				break
			}
		}
		for _, rx := range t.Reactions {
			if rx.Kind == ReactionHeld && rx.State == tr.To && (len(rx.From) == 0 || slices.Contains(rx.From, tr.From)) {
				r.fail(CodeHeldTransition, at(p, "clears"), "Remove clears, or clear on another transition that is not held",
					"transition %s: it clears fields and is held by the reaction %s; a held transition may not clear fields", name, rx.ID)
				break
			}
		}
	}
}
