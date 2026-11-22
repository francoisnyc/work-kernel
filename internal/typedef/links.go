package typedef

import (
	"fmt"
	"regexp"
	"slices"
	"sort"

	"github.com/francoisnyc/work-kernel/internal/item"
)

// Links (proof of concept, Step 1). A Type declares its link types like its
// fields: a name from the source (blocks), an inverse name from the target
// (blocked_by), the Types a target may be of, and at most one or many on
// each side. Containment is not a link type: it keeps its own storage and
// rules. Links never change who can see anything.

// Principal is a field holding a principal: user:<id> or agent:<id>. It
// grants nothing; search can filter on it and guards can read it (a string
// to CEL).
const Principal FieldType = "principal"

// Cardinality says how many links of a type one end may have.
type Cardinality string

const (
	One  Cardinality = "one"
	Many Cardinality = "many" // the default when none is written
)

// LinkType is one link type a Type declares. Its Items are the sources.
type LinkType struct {
	Name    string   `yaml:"name" json:"name"`
	Inverse string   `yaml:"inverse" json:"inverse"`
	Targets []string `yaml:"targets" json:"targets"` // Type IDs; a target may be of any version
	// Max is per source, InverseMax per target; empty means Many.
	Max        Cardinality `yaml:"max" json:"max,omitempty"`
	InverseMax Cardinality `yaml:"inverse_max" json:"inverse_max,omitempty"`
	// WritableBy: kernel blocks LinkItems and UnlinkItems on the public door
	// (LINK_NOT_WRITABLE); only the kernel sets the link (Resolve).
	WritableBy WritableBy `yaml:"writable_by" json:"writable_by,omitempty"`
	// WritableByRoles (from writable_by: [role:<name>, ...]; Jira part 2,
	// step 3b; see jira23b.go): only holders of one of
	// these roles add or remove the link on the public door.
	WritableByRoles []string `yaml:"-" json:"writable_by_roles,omitempty"`
	// Resolve makes the kernel set the link on entering a state.
	Resolve *LinkResolve `yaml:"resolve" json:"resolve,omitempty"`
}

// SourceSingle reports whether a source has at most one link of this type.
func (l LinkType) SourceSingle() bool { return l.Max == One }

// TargetSingle reports whether a target has at most one link of this type.
func (l LinkType) TargetSingle() bool { return l.InverseMax == One }

// Link returns the link type the Type declares under this (declared) name.
func (t *Type) Link(name string) (LinkType, bool) {
	for _, l := range t.Links {
		if l.Name == name {
			return l, true
		}
	}
	return LinkType{}, false
}

// linkName is the shape of a link name or inverse: a CEL identifier, since
// guards read links.<name>.
var linkName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

// checkLinks validates the link types.
func (t *Type) checkLinks(r reporter) {
	seen := map[string]bool{} // names and inverses share one space
	for i, l := range t.Links {
		t.checkLink(r, at("links", i), l, seen)
	}
}

func (t *Type) checkLink(r reporter, p string, l LinkType, seen map[string]bool) {
	if l.Name == "" {
		r.fail(CodeRequired, at(p, "name"), "Name the link type, such as blocks", "a link type has no name")
		return
	}
	where := fmt.Sprintf("link %s", l.Name)
	switch {
	case l.Name == ContainerLink:
		r.fail(CodeReservedName, at(p, "name"), "Choose another name; containment is declared by placing Items, not by a link",
			"%s: %q is a reserved name (containment is not a link)", where, ContainerLink)
	case l.Inverse == ContainerLink:
		r.fail(CodeReservedName, at(p, "inverse"), "Choose another name; containment is declared by placing Items, not by a link",
			"%s: %q is a reserved name (containment is not a link)", where, ContainerLink)
	case l.Inverse == "":
		r.fail(CodeRequired, at(p, "inverse"), "Name the link as seen from its target, such as blocked_by", "%s: no inverse name", where)
	case l.Inverse == l.Name:
		r.fail(CodeConflict, at(p, "inverse"), "Give the inverse its own name, such as blocked_by for blocks", "%s: its inverse is the same name", where)
	default:
		for _, k := range []string{"name", "inverse"} {
			n := l.Name
			if k == "inverse" {
				n = l.Inverse
			}
			if !linkName.MatchString(n) {
				r.fail(CodeInvalidName, at(p, k), "Use lower case letters, digits and _, starting with a letter",
					"%s: %q is not a link name (lower case letters, digits and _)", where, n)
				continue
			}
			if seen[n] {
				r.fail(CodeDuplicate, at(p, k), "Rename one of the two: link names and inverses share one space",
					"%s: the name %q is used twice among link names and inverses", where, n)
			}
			seen[n] = true
		}
	}
	switch {
	case len(l.Targets) == 0:
		r.fail(CodeRequired, at(p, "targets"), "List the Types the link may point to, or [\"*\"] for any",
			"%s: no target Types", where)
	case slices.Contains(l.Targets, ""):
		r.fail(CodeInvalidValue, at(p, "targets", slices.Index(l.Targets, "")), "Name a Type, or remove the empty entry",
			"%s: an empty target Type", where)
	case slices.Contains(l.Targets, AnyType) && len(l.Targets) != 1:
		r.fail(CodeConflict, at(p, "targets"), "Write targets: [\"*\"] alone, or name the Types without *",
			"%s: %q means any Type and stands alone in targets", where, AnyType)
	}
	for _, k := range []string{"max", "inverse_max"} {
		c := l.Max
		if k == "inverse_max" {
			c = l.InverseMax
		}
		if c != "" && c != One && c != Many {
			r.fail(CodeInvalidValue, at(p, k), "Write one or many", "%s: cardinality %q is not one or many", where, c)
		}
	}
}

// guardLinks are the names a guard of t may read as links.<name>: the
// declared names (outgoing), the inverses of link types that may target t
// itself (incoming), and the incoming names t declares for links of other
// Types (LinkSource resolves each).
func (t *Type) guardLinks() []string {
	var names []string
	for _, l := range t.Links {
		names = append(names, l.Name)
		if l.AllowsTarget(t.ID) {
			names = append(names, l.Inverse)
		}
	}
	for _, in := range t.Incoming {
		names = append(names, in.Name)
	}
	sort.Strings(names)
	return names
}

// IncomingName returns the link type whose inverse is name, among t's link
// types that target t itself: what a guard's links.<name> means when name
// is not a declared name.
func (t *Type) IncomingName(name string) (LinkType, bool) {
	for _, l := range t.Links {
		if l.Inverse == name && l.AllowsTarget(t.ID) {
			return l, true
		}
	}
	return LinkType{}, false
}

// checkPrincipalValue reports whether v is a principal a field may hold.
func checkPrincipalValue(v any) bool {
	s, ok := v.(string)
	return ok && item.IsKind(s, item.KindUser, item.KindAgent)
}

// CheckLinkTargets checks that every link type of every Type in the set
// names target Types that are in the set. ParseDir calls it: one Type file
// cannot know the others.
func CheckLinkTargets(types []*Type) error {
	ids := map[string]bool{}
	for _, t := range types {
		ids[t.ID] = true
	}
	for _, t := range types {
		for _, l := range t.Links {
			for _, target := range l.Targets {
				if target == AnyType {
					continue
				}
				if !ids[target] {
					return fmt.Errorf("typedef %s v%d: link %s: target Type %q is not loaded", t.ID, t.Version, l.Name, target)
				}
			}
		}
	}
	return nil
}

// CheckLinkInverses checks that, within each Type, every version that
// declares a link name gives it the same inverse, so (Type, link name)
// names one inverse for incoming links and filters.
func CheckLinkInverses(types []*Type) error {
	type seenAt struct {
		inverse string
		version int
	}
	seen := map[string]seenAt{} // "type\x00name"
	for _, t := range types {
		for _, l := range t.Links {
			key := t.ID + "\x00" + l.Name
			first, ok := seen[key]
			if !ok {
				seen[key] = seenAt{l.Inverse, t.Version}
				continue
			}
			if first.inverse != l.Inverse {
				return fmt.Errorf("typedef %s: link %s has inverse %s in v%d and %s in v%d; "+
					"a link keeps its inverse across versions", t.ID, l.Name, first.inverse, first.version, l.Inverse, t.Version)
			}
		}
	}
	return nil
}
