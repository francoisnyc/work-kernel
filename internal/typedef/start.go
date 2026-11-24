package typedef

import (
	"slices"
	"strings"

	"github.com/francoisnyc/work-kernel/internal/item"
)

// Starting work, step 2 (docs/plans/starting-work.md, part 1): a Type's
// optional start block says where its Items may be created and by whom.
// This file parses and checks it; package policy judges a create against
// it (MayStart), and the item service applies the latest registered
// version's block to a create of every version.

// Start is a Type's start block. In lists container Type IDs (any version
// of each), or StartTop for no container; absent means anywhere. Allowed
// lists who may create one: role:<name>, principal:user:<id>, field:<id>
// (a principal field of the container), container_creator, or nobody;
// absent or empty means anyone who sees the container. Only the latest
// registered version's block judges a create, of any version.
type Start struct {
	In      []string `yaml:"in" json:"in,omitempty"`
	Allowed []string `yaml:"allowed" json:"allowed,omitempty"`
}

// StartTop in start.in means "at the top level, in no container". A Type
// may not be named top (RESERVED_NAME), so the word means one thing.
const StartTop = "top"

// Admits reports whether start.in lets an Item be created in a container
// of Type containerType; "" is the top level. A nil block, or one without
// in, admits anywhere. A method on a pointer may be called on nil in Go,
// so callers need not check for a block first.
func (s *Start) Admits(containerType string) bool {
	if s == nil || len(s.In) == 0 {
		return true
	}
	switch containerType {
	case "":
		return slices.Contains(s.In, StartTop)
	case StartTop: // no Type is named top: this is no container
		return false
	}
	return slices.Contains(s.In, containerType)
}

// Nobody reports whether the block lets no person, agent or customer
// create one: only a create reaction does, by consent.
func (s *Start) Nobody() bool { return s != nil && slices.Contains(s.Allowed, AllowedNobody) }

// where says where start.in lets Items be created, for messages:
// "room or hall", "room or the top level".
func (s *Start) where() string {
	var out []string
	for _, id := range s.In {
		if id == StartTop {
			id = "the top level"
		}
		out = append(out, id)
	}
	return strings.Join(out, " or ")
}

// checkStart checks the block on this Type alone (stage b). Whether the
// Types in in exist, and declare the fields field: names, is stage c's
// question (CheckStart).
func (t *Type) checkStart(r reporter) {
	if t.ID == StartTop {
		r.fail(CodeReservedName, "id", "Choose another id",
			"%q is a reserved name: start.in uses it for the top level", t.ID)
	}
	s := t.Start
	if s == nil {
		return
	}
	// YAML gives an empty list for in: [], and nil for a key left out.
	if s.In == nil && s.Allowed == nil {
		r.fail(CodeRequired, "start", "Say where (in) or who (allowed), or remove start",
			"start says nothing")
	}
	if s.In != nil && len(s.In) == 0 {
		r.fail(CodeInvalidValue, "start.in", "List the container Types, or top, or leave in out to allow anywhere",
			"start.in lists nothing: no create could pass")
	}
	top := slices.Contains(s.In, StartTop)
	for i, id := range s.In {
		p := at("start", "in", i)
		switch {
		case slices.Contains(s.In[:i], id):
			r.fail(CodeDuplicate, p, "Remove the second one", "start.in names %s twice", id)
		case id != StartTop && !linkName.MatchString(id):
			r.fail(CodeInvalidName, p, "Name a Type by its id (lower case letters, digits and _), or write top",
				"start.in: %q is not a Type ID", id)
		}
	}
	for i, e := range s.Allowed {
		t.checkStartEntry(r, at("start", "allowed", i), s, i, e, top)
	}
}

// checkStartEntry checks start.allowed[i].
func (t *Type) checkStartEntry(r reporter, p string, s *Start, i int, e string, top bool) {
	readsContainer := func() {
		if len(s.In) == 0 || top {
			r.fail(CodeConflict, p, "List the container Types in start.in, without top",
				"start.allowed entry %s reads the container, but start.in allows creating at the top level, in no container", e)
		}
	}
	if slices.Contains(s.Allowed[:i], e) {
		r.fail(CodeDuplicate, p, "Remove the second one", "start.allowed names %s twice", e)
		return
	}
	switch {
	case e == AllowedNobody:
		if len(s.Allowed) > 1 {
			r.fail(CodeConflict, p, "Leave nobody alone in allowed, or remove it",
				"start.allowed entry %s stands alone: no one else may be named beside it", AllowedNobody)
		}
	case e == AllowedContainerCreator:
		readsContainer()
	case strings.HasPrefix(e, AllowedFieldPrefix):
		if strings.TrimPrefix(e, AllowedFieldPrefix) == "" {
			r.fail(CodeInvalidValue, p, "Name a principal field of the container after field:", "start.allowed entry %q names no field", e)
			return
		}
		readsContainer()
	case strings.HasPrefix(e, AllowedRolePrefix):
		if strings.TrimPrefix(e, AllowedRolePrefix) == "" {
			r.fail(CodeInvalidValue, p, "Name the role after role:", "start.allowed entry %q names no role", e)
		}
	case strings.HasPrefix(e, AllowedPrincipalPrefix):
		if who := strings.TrimPrefix(e, AllowedPrincipalPrefix); !item.IsKind(who, item.KindUser) {
			r.fail(CodeInvalidValue, p, "Write principal:user:<id>; a customer creates what a container's own or shared admits",
				"start.allowed entry %q must name a user", e)
		}
	default:
		r.fail(CodeInvalidValue, p, "Write role:<name>, principal:user:<id>, field:<id>, container_creator or nobody",
			"start.allowed entry %q is none of role:, principal:user:, field:, container_creator or nobody "+
				"(creator has no meaning here: the Item has no creator yet)", e)
	}
}

// CheckStart checks the start blocks across a set of Types (stage c):
//   - start.in names loaded Types; field: names a principal field of every
//     version of each of them;
//   - what creates Items of a Type is let in by its latest version's
//     block: a container's own or shared entry (else START_NOT_IN, or
//     CONFLICT when nobody may start it: fail closed), and a create
//     reaction, whose Items are created inside its source (START_NOT_IN).
func CheckStart(types []*Type) error {
	var c crossCheck
	byID := map[string][]*Type{}
	latest := map[string]*Type{}
	for _, t := range types {
		byID[t.ID] = append(byID[t.ID], t)
		if l, ok := latest[t.ID]; !ok || t.Version > l.Version {
			latest[t.ID] = t
		}
	}
	for _, t := range types {
		if t.Start == nil {
			continue
		}
		for i, id := range t.Start.In {
			if id != StartTop && len(byID[id]) == 0 {
				c.on(t).fail(CodeUnknownType, at("start", "in", i), "Load that Type too, or name a loaded one",
					"start.in names %s, which is not loaded", id)
			}
		}
		for i, e := range t.Start.Allowed {
			if f, ok := strings.CutPrefix(e, AllowedFieldPrefix); ok {
				checkStartField(c.on(t), at("start", "allowed", i), t.Start, f, byID)
			}
		}
	}
	for _, t := range types {
		for _, list := range []struct {
			key string
			ids []string
		}{{"own", t.Own}, {"shared", t.Shared}} {
			for j, id := range list.ids {
				target, ok := latest[id]
				if !ok || target.Start == nil {
					continue
				}
				p := at(list.key, j)
				switch {
				case target.Start.Nobody():
					c.on(t).fail(CodeConflict, p, "Remove the Type from the list, or let someone start it",
						"admits customers to %s, whose latest version (v%d) nobody may start", id, target.Version)
				case !target.Start.Admits(t.ID):
					c.on(t).fail(CodeStartNotIn, p, "Remove the Type from the list, or add this container Type to its start.in",
						"admits customers to %s, whose latest version (v%d) is created only in %s", id, target.Version, target.Start.where())
				}
			}
		}
		for i, r := range t.Reactions {
			if r.Kind != ReactionCreate {
				continue
			}
			if target, ok := latest[r.Type]; ok && !target.Start.Admits(t.ID) {
				c.on(t).fail(CodeStartNotIn, at("reactions", i), "Add this Type to the start.in of the Type it creates, or remove the reaction",
					"reaction %s creates %s inside a %s, but its latest version (v%d) is created only in %s",
					r.ID, r.Type, t.ID, target.Version, target.Start.where())
			}
		}
	}
	return c.err()
}

// checkStartField checks that field: names a principal field of every
// version of every Type in start.in. A container without it would match
// no one, silently.
func checkStartField(r reporter, p string, s *Start, field string, byID map[string][]*Type) {
	for _, id := range s.In {
		for _, v := range byID[id] {
			f, ok := v.Field(field)
			switch {
			case !ok:
				r.fail(CodeUnknownField, p, "Name a principal field every version of every Type in start.in declares",
					"start.allowed entry field:%s: %s v%d does not declare %s", field, v.ID, v.Version, field)
				return
			case f.Type != Principal:
				r.fail(CodeWrongFieldType, p, "Name a principal field",
					"start.allowed entry field:%s: %s is a %s field in %s v%d, not a principal field", field, field, f.Type, v.ID, v.Version)
				return
			}
		}
	}
}
