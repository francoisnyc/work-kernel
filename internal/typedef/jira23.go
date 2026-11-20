package typedef

import "cel.dev/cel-go/cel"

// Jira part 2, step 3 (docs/design.md, "Decisions for Jira part 2"): what
// the kernel needs for goals kept as contained Items. Nothing here is named
// after them.
//
// PHASE B: declarations only, so the failing tests compile. Nothing below
// is checked, compiled or honoured yet.

// ByNotCreator is Reaction.By's second value: the Item created inside was
// created by someone else than the declaring Item's requester (the
// negation of policy.IsRequester). The kernel's own actors are neither.
const ByNotCreator = "not_creator"

// ReactionSet runs once its trigger (entering a state) has committed and
// sets fields on its target.
const ReactionSet ReactionKind = "set"

// TargetContainer is the one value Reaction.Target takes: the Item the
// source is contained in.
const TargetContainer = "container"

// WildcardType stands for any Type in an accepts_reactions key: *:<id>.
// It is honoured only for a reaction that reaches the Item through
// containment.
const WildcardType = "*"

// LinkedPrefix and ContainerLinkedPrefix start the CEL names of a linked
// Item's field in a create reaction's fields: linked.<link>.<field> (one
// declared max-one link away from the source) and
// container.linked.<link>.<field> (from the source's container).
const (
	LinkedPrefix          = "linked."
	ContainerLinkedPrefix = "container.linked."
)

// Children says which contained Items a move reaction moves: those of one
// of Of whose fields make When hold (child.<id>; no condition: all).
type Children struct {
	Of   []string `yaml:"of" json:"of"`
	When string   `yaml:"when" json:"when,omitempty"`

	// When compiled, and the child fields it reads, sorted; filled when
	// the Type is loaded.
	WhenProgram cel.Program `yaml:"-" json:"-"`
	Reads       []string    `yaml:"-" json:"-"`
}

// LinkedRead is one field of a linked Item a create reaction reads.
type LinkedRead struct {
	// Container: the link is the source's container's, not the source's.
	Container bool
	Link      string
	Field     string
}

// Consent returns what this Type accepts of reaction reactionID declared by
// Type typeID. contained says the reaction reaches the Item through
// containment (its container creating it or moving it as a child, or a
// contained Item setting a field on it): only then does a wildcard key
// (*:<id>) count. An exact key wins over the wildcard.
func (t *Type) Consent(typeID, reactionID string, contained bool) (Accepted, bool) {
	return Accepted{}, false // Phase B stub
}

// ReadableByReaction reports whether a create reaction of another Type may
// read field from an Item of this Type.
func (t *Type) ReadableByReaction(field string) bool {
	return false // Phase B stub
}
