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

// checkLinks validates the link types. fail formats an error naming the Type.
func (t *Type) checkLinks(fail func(string, ...any) error) error {
	seen := map[string]bool{} // names and inverses share one space
	for _, l := range t.Links {
		if l.Name == "" {
			return fail("a link type has no name")
		}
		where := fmt.Sprintf("link %s", l.Name)
		if l.Inverse == "" {
			return fail("%s: no inverse name", where)
		}
		if l.Inverse == l.Name {
			return fail("%s: its inverse is the same name", where)
		}
		for _, n := range []string{l.Name, l.Inverse} {
			if !linkName.MatchString(n) {
				return fail("%s: %q is not a link name (lower case letters, digits and _)", where, n)
			}
			if seen[n] {
				return fail("%s: the name %q is used twice among link names and inverses", where, n)
			}
			seen[n] = true
		}
		if len(l.Targets) == 0 {
			return fail("%s: no target Types", where)
		}
		if slices.Contains(l.Targets, "") {
			return fail("%s: an empty target Type", where)
		}
		for _, c := range []Cardinality{l.Max, l.InverseMax} {
			if c != "" && c != One && c != Many {
				return fail("%s: cardinality %q is not one or many", where, c)
			}
		}
	}
	return nil
}

// guardLinks are the names a guard of t may read as links.<name>: the
// declared names (outgoing), and the inverses of link types that target t
// itself (incoming). Incoming links declared by other Types are not visible
// in Step 1.
func (t *Type) guardLinks() []string {
	var names []string
	for _, l := range t.Links {
		names = append(names, l.Name)
		if slices.Contains(l.Targets, t.ID) {
			names = append(names, l.Inverse)
		}
	}
	sort.Strings(names)
	return names
}

// IncomingName returns the link type whose inverse is name, among t's link
// types that target t itself: what a guard's links.<name> means when name
// is not a declared name.
func (t *Type) IncomingName(name string) (LinkType, bool) {
	for _, l := range t.Links {
		if l.Inverse == name && slices.Contains(l.Targets, t.ID) {
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
