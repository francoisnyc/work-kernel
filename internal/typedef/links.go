package typedef

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

// CheckLinkTargets checks that every link type of every Type in the set
// names target Types that are in the set. ParseDir calls it: one Type file
// cannot know the others.
func CheckLinkTargets(types []*Type) error {
	return nil // Step 1, Phase B stub
}

// CheckLinkInverses checks that, within each Type, every version that
// declares a link name gives it the same inverse.
func CheckLinkInverses(types []*Type) error {
	return nil // Step 1, Phase C: review change 3, stub until built
}
