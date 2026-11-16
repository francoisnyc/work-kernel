package typedef

import (
	"fmt"
	"strings"

	"cel.dev/cel-go/cel"
	"go.yaml.in/yaml/v3"
)

// Link reactions, invariants and role writers (proof of concept, Step 2,
// part 2). A Type declares writes its transitions make on the Items its
// links point to; the target Type declares which of them it accepts; and
// any Type may declare invariants, rules over its fields that every write
// must keep.

// ReactionKind says how a reaction relates to the transition that triggers it.
type ReactionKind string

const (
	// ReactionHeld holds the triggering transition: asking for it opens a
	// hold, and the reaction's write on the target decides it (allow if it
	// commits, deny with the target's refusal code if refused). Every held
	// reaction has an undo: an `after` reaction that `undoes` it, which
	// also gives back what it took when its hold closes any other way than
	// allowed.
	ReactionHeld ReactionKind = "held"
	// ReactionAfter runs once the triggering transition has committed and
	// does not hold it. For now it only undoes a held reaction (`undoes`);
	// an after reaction with a free patch is Later.
	ReactionAfter ReactionKind = "after"
)

// Reaction is one link reaction. Its principal is reaction:<type>:<id>.
type Reaction struct {
	ID   string       `yaml:"id" json:"id"`
	Kind ReactionKind `yaml:"kind" json:"kind"`
	// On is always enter: the reaction runs when an Item enters State, from
	// one of From (empty: from any state). For a held reaction, each
	// from -> State must be a declared transition: those are the
	// transitions it holds.
	On    HookOn   `yaml:"on" json:"on"`
	State string   `yaml:"state" json:"state"`
	From  []string `yaml:"from" json:"from,omitempty"`
	// Link is the declared link type (max: one) a held reaction follows to
	// its target.
	Link string `yaml:"link" json:"link,omitempty"`
	// Add (held only): target field ID -> a CEL expression over the
	// source's fields, of type int; its value is added to the target field.
	Add map[string]string `yaml:"add" json:"add,omitempty"`
	// Undoes (after only): the held reaction whose committed write this
	// reaction reverses, by the delta that write recorded.
	Undoes string `yaml:"undoes" json:"undoes,omitempty"`

	// Programs are Add's expressions compiled, by target field; Reads are
	// the source fields they read, sorted (frozen while the reaction's hold
	// is open). Filled when the Type is loaded.
	Programs map[string]cel.Program `yaml:"-" json:"-"`
	Reads    []string               `yaml:"-" json:"-"`
}

// Invariant is a rule over an Item's fields that every write must keep, on
// every door. Its ID is the refusal code (INVARIANT_FAILED names it, and a
// held reaction refused by it is denied with it as the reason). A rule that
// cannot be evaluated (a null field, say) does not hold.
type Invariant struct {
	ID   string `yaml:"id" json:"id"`
	Rule string `yaml:"rule" json:"rule"`

	// Program is Rule compiled, filled when the Type is loaded.
	Program cel.Program `yaml:"-" json:"-"`
}

// LinkResolve makes the kernel set a link (max: one, writable_by: kernel)
// in the write that enters OnEnter: the target is the unique Item of the
// link's one target Type, in State, whose principal field Field equals
// the source's creator (created_on_behalf_of, else created_by). None or
// more than one refuses the entry: no_<link> or ambiguous_<link>.
type LinkResolve struct {
	OnEnter string `yaml:"on_enter" json:"on_enter"`
	Field   string `yaml:"field" json:"field"`
	Equals  string `yaml:"equals" json:"equals"` // only "creator" for now
	State   string `yaml:"state" json:"state"`
}

// ResolveEqualsCreator is the one value LinkResolve.Equals takes.
const ResolveEqualsCreator = "creator"

// ReactionKey names a reaction across Types, unversioned: <type>:<id>, as
// accepts_reactions keys it (the principal is reaction:<type>:<id>).
func ReactionKey(typeID, reactionID string) string { return typeID + ":" + reactionID }

// Reaction returns the reaction with the given ID.
func (t *Type) Reaction(id string) (Reaction, bool) {
	for _, r := range t.Reactions {
		if r.ID == id {
			return r, true
		}
	}
	return Reaction{}, false
}

// Accepts reports whether this Type accepts reaction key (<type>:<id>)
// writing every one of fields.
//
// Phase B stub: not implemented yet.
func (t *Type) Accepts(key string, fields []string) bool {
	return false
}

// CheckReactions checks link reactions and link resolution across a set of
// Types loaded together (ParseDir): each reaction's link targets a Type
// that accepts it, for exactly the int fields it writes; each accepted key
// names a reaction that exists and follows a link to this Type; each
// resolve names a principal field and a state of its target Type.
//
// Phase B stub: not implemented yet.
func CheckReactions(types []*Type) error {
	return nil
}

// RoleWritten returns the fields among ids that only holders of roles may
// write on the public door (writable_by: [role:<name>, ...]), sorted.
//
// Phase B stub: not implemented yet.
func (t *Type) RoleWritten(ids []string) []string {
	return nil
}

// UnmarshalYAML reads a field. writable_by takes a scalar (kernel) or a
// list of role:<name> entries, stored as WritableByRoles without the
// prefix. (The rules on the list are check's.)
func (f *Field) UnmarshalYAML(n *yaml.Node) error {
	type plain Field // no methods: decoding it does not recurse here
	var wb *yaml.Node
	rest := *n
	if n.Kind == yaml.MappingNode {
		// Take writable_by out of a copy; decode the rest as usual.
		rest.Content = nil
		for i := 0; i+1 < len(n.Content); i += 2 {
			switch key := n.Content[i].Value; key {
			case "writable_by":
				wb = n.Content[i+1]
				continue
			case "id", "type", "options":
			default:
				// Node.Decode does not carry the decoder's KnownFields, so
				// unknown keys are refused here, as everywhere else.
				return fmt.Errorf("line %d: field %s not found in type typedef.Field", n.Content[i].Line, key)
			}
			rest.Content = append(rest.Content, n.Content[i], n.Content[i+1])
		}
	}
	var p plain
	if err := rest.Decode(&p); err != nil {
		return err
	}
	*f = Field(p)
	if wb == nil {
		return nil
	}
	switch wb.Kind {
	case yaml.ScalarNode:
		f.WritableBy = WritableBy(wb.Value)
	case yaml.SequenceNode:
		var entries []string
		if err := wb.Decode(&entries); err != nil {
			return err
		}
		for _, e := range entries {
			name, ok := strings.CutPrefix(e, AllowedRolePrefix)
			if !ok {
				name = e // kept as written; check refuses it
			}
			f.WritableByRoles = append(f.WritableByRoles, name)
		}
	default:
		return fmt.Errorf("field %s: writable_by must be kernel or a list of role:<name>", f.ID)
	}
	return nil
}
