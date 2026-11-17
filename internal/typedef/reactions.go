package typedef

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"cel.dev/cel-go/cel"
	"go.yaml.in/yaml/v3"

	"github.com/francoisnyc/work-kernel/internal/item"
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
func (t *Type) Accepts(key string, fields []string) bool {
	allowed, ok := t.AcceptsReactions[key]
	if !ok {
		return false
	}
	for _, f := range fields {
		if !slices.Contains(allowed, f) {
			return false
		}
	}
	return true
}

// CheckReactions checks link reactions and link resolution across a set of
// Types loaded together (ParseDir): each reaction's link targets a Type
// that accepts it, for exactly the int fields it writes; each accepted key
// names a reaction that exists and follows a link to the accepting Type;
// each resolve names a principal field and a state of its target Type.
// Every version of a Type is checked against every version of the other.
func CheckReactions(types []*Type) error {
	byID := map[string][]*Type{}
	for _, t := range types {
		byID[t.ID] = append(byID[t.ID], t)
	}
	for _, src := range types {
		fail := func(format string, args ...any) error {
			return fmt.Errorf("typedef %s v%d: %s", src.ID, src.Version, fmt.Sprintf(format, args...))
		}
		for _, l := range src.Links {
			if l.Resolve == nil {
				continue
			}
			for _, tgt := range byID[l.Targets[0]] {
				f, ok := tgt.Field(l.Resolve.Field)
				if !ok || f.Type != Principal {
					return fail("link %s: resolve field %q is not a principal field of %s v%d",
						l.Name, l.Resolve.Field, tgt.ID, tgt.Version)
				}
				if _, ok := tgt.State(l.Resolve.State); !ok {
					return fail("link %s: resolve state %q is not a state of %s v%d", l.Name, l.Resolve.State, tgt.ID, tgt.Version)
				}
			}
		}
		for _, r := range src.Reactions {
			held := r
			if r.Kind == ReactionAfter {
				held, _ = src.Reaction(r.Undoes) // an undo writes what its held reaction wrote
			}
			l, _ := src.Link(held.Link)
			fields := slices.Sorted(maps.Keys(held.Add))
			key := ReactionKey(src.ID, r.ID)
			for _, targetID := range l.Targets {
				for _, tgt := range byID[targetID] {
					for _, f := range fields {
						tf, ok := tgt.Field(f)
						if !ok {
							return fail("reaction %s writes %s, which %s v%d does not declare", r.ID, f, tgt.ID, tgt.Version)
						}
						if tf.Type != Int {
							return fail("reaction %s adds to %s, which is not an int in %s v%d", r.ID, f, tgt.ID, tgt.Version)
						}
					}
					if !tgt.Accepts(key, fields) {
						return fail("reaction %s writes %v of %s v%d, which does not accept %s for them (accepts_reactions)",
							r.ID, fields, tgt.ID, tgt.Version, key)
					}
				}
			}
		}
	}
	// What each Type accepts must exist and point at it.
	for _, tgt := range types {
		for _, key := range slices.Sorted(maps.Keys(tgt.AcceptsReactions)) {
			typeID, reactionID, _ := strings.Cut(key, ":")
			found := false
			for _, src := range byID[typeID] {
				r, ok := src.Reaction(reactionID)
				if !ok {
					continue
				}
				if r.Kind == ReactionAfter {
					r, _ = src.Reaction(r.Undoes)
				}
				if l, ok := src.Link(r.Link); ok && slices.Contains(l.Targets, tgt.ID) {
					found = true
				}
			}
			if !found {
				return fmt.Errorf("typedef %s v%d: accepts_reactions names %s, which is no reaction of a Type "+
					"following a link to %s", tgt.ID, tgt.Version, key, tgt.ID)
			}
		}
	}
	return nil
}

// reactionID is the shape of a reaction or invariant ID: a code, since it
// becomes a principal's last part and a refusal's reason.
var reactionID = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

// roleNameRe matches a role name (as the item service's EnsureRoles).
var roleNameRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,62}$`)

// checkRoles validates a field's writable_by roles.
func (f Field) checkRoles() error {
	if len(f.WritableByRoles) == 0 {
		return nil
	}
	if f.WritableBy != WritableByAnyone {
		return fmt.Errorf("field %s: writable_by is either kernel or a list of roles, not both", f.ID)
	}
	seen := map[string]bool{}
	for _, r := range f.WritableByRoles {
		if !roleNameRe.MatchString(r) {
			return fmt.Errorf("field %s: writable_by role %q is not a role name (lower case, digits, _ and -)", f.ID, r)
		}
		if seen[r] {
			return fmt.Errorf("field %s: writable_by names role %s twice", f.ID, r)
		}
		seen[r] = true
	}
	return nil
}

// checkKernelLinks validates a link type's writable_by and resolve. States
// are known; the resolve's target Type is checked by CheckReactions.
func (t *Type) checkKernelLinks(fail func(string, ...any) error) error {
	resolvedOn := map[string]string{} // on_enter -> the link set there
	for _, l := range t.Links {
		where := "link " + l.Name
		switch l.WritableBy {
		case WritableByAnyone, WritableByKernel:
		default:
			return fail("%s: writable_by %q: the only value is kernel", where, l.WritableBy)
		}
		r := l.Resolve
		if l.WritableBy == WritableByKernel && r == nil {
			return fail("%s is writable_by kernel, but nothing sets it: add a resolve", where)
		}
		if r == nil {
			continue
		}
		if l.WritableBy != WritableByKernel {
			return fail("%s: a resolved link must be writable_by kernel, or the public door could change it", where)
		}
		if !l.SourceSingle() {
			return fail("%s: a resolved link has at most one target (max: one)", where)
		}
		if len(l.Targets) != 1 {
			return fail("%s: a resolved link has exactly one target Type, not %v", where, l.Targets)
		}
		if _, ok := t.State(r.OnEnter); !ok {
			return fail("%s: resolve on_enter %q is not a declared state", where, r.OnEnter)
		}
		if r.OnEnter == t.InitialState {
			return fail("%s: resolve on_enter %q is the initial state, which Create enters without resolving links",
				where, r.OnEnter)
		}
		if r.Equals != ResolveEqualsCreator {
			return fail("%s: resolve equals %q: the only value is creator", where, r.Equals)
		}
		if r.Field == "" || r.State == "" {
			return fail("%s: resolve needs a field and a state of the target", where)
		}
		// One per state: the entering event records the one link it set.
		if other, dup := resolvedOn[r.OnEnter]; dup {
			return fail("%s: link %s is already set on entering %s; one resolved link per state", where, other, r.OnEnter)
		}
		resolvedOn[r.OnEnter] = l.Name
	}
	return nil
}

// newFieldEnv declares one CEL variable per field and nothing else: no
// today and no links. Invariants and reaction expressions are rules over
// an Item's own fields, true at any time and whatever other Items say.
func newFieldEnv(t *Type) (*cel.Env, error) {
	opts := make([]cel.EnvOption, 0, len(t.Fields))
	for _, f := range t.Fields {
		opts = append(opts, cel.Variable(f.ID, celType(f.Type)))
	}
	return cel.NewEnv(opts...)
}

// compileFields compiles expr in env and returns its program, the fields it
// reads (sorted) and its output type.
func compileFields(env *cel.Env, expr string) (cel.Program, []string, *cel.Type, error) {
	ast, iss := env.Compile(expr)
	if iss.Err() != nil {
		return nil, nil, nil, iss.Err()
	}
	var reads []string
	for _, ref := range ast.NativeRep().ReferenceMap() {
		if ref.Name != "" && !slices.Contains(reads, ref.Name) {
			reads = append(reads, ref.Name)
		}
	}
	slices.Sort(reads)
	prg, err := env.Program(ast)
	return prg, reads, ast.OutputType(), err
}

// isInt reports whether a CEL type is an int, nullable or not.
func isInt(ct *cel.Type) bool {
	return ct.IsExactType(cel.IntType) || ct.IsExactType(cel.NullableType(cel.IntType))
}

// checkInvariants validates and compiles the invariants.
func (t *Type) checkInvariants(fail func(string, ...any) error) error {
	env, err := newFieldEnv(t)
	if err != nil {
		return fail("invariant environment: %v", err)
	}
	seen := map[string]bool{}
	for i := range t.Invariants {
		inv := &t.Invariants[i]
		if !reactionID.MatchString(inv.ID) {
			return fail("invariant %q: the ID is a refusal code (lower case, digits and _)", inv.ID)
		}
		if seen[inv.ID] {
			return fail("invariant %s is declared twice", inv.ID)
		}
		seen[inv.ID] = true
		if inv.Rule == "" {
			return fail("invariant %s has no rule", inv.ID)
		}
		prg, _, out, err := compileFields(env, inv.Rule)
		if err != nil {
			return fail("invariant %s: %v", inv.ID, err)
		}
		if !out.IsExactType(cel.BoolType) {
			return fail("invariant %s must yield a bool, not %s", inv.ID, out)
		}
		inv.Program = prg
	}
	return nil
}

// checkAccepts validates accepts_reactions within the Type: the keys'
// shape, and the fields (declared ints: reactions only add).
func (t *Type) checkAccepts(fail func(string, ...any) error) error {
	for _, key := range slices.Sorted(maps.Keys(t.AcceptsReactions)) {
		if _, _, ok := item.ReactionParts(item.KindReaction + ":" + key); !ok {
			return fail("accepts_reactions key %q must be <type>:<reaction>, without a version", key)
		}
		fields := t.AcceptsReactions[key]
		if len(fields) == 0 {
			return fail("accepts_reactions %s names no fields", key)
		}
		for _, id := range fields {
			f, ok := t.Field(id)
			if !ok {
				return fail("accepts_reactions %s: field %s is not declared", key, id)
			}
			if f.Type != Int {
				return fail("accepts_reactions %s: field %s is not an int (a reaction adds)", key, id)
			}
		}
	}
	return nil
}

// checkReactions validates and compiles the reactions. Hooks are known.
func (t *Type) checkReactions(fail func(string, ...any) error) error {
	env, err := newFieldEnv(t)
	if err != nil {
		return fail("reaction environment: %v", err)
	}
	seen := map[string]bool{}
	for i := range t.Reactions {
		r := &t.Reactions[i]
		where := fmt.Sprintf("reaction %q", r.ID)
		if !reactionID.MatchString(r.ID) {
			return fail("%s: not a reaction ID (lower case, digits and _)", where)
		}
		if seen[r.ID] {
			return fail("%s is declared twice", where)
		}
		seen[r.ID] = true
		if _, ok := t.Hook(r.ID); ok {
			return fail("%s: a hook has the same ID; holds name either, so the IDs are shared", where)
		}
		if r.On != OnEnter {
			return fail("%s: on must be enter, not %q", where, r.On)
		}
		if _, ok := t.State(r.State); !ok {
			return fail("%s: unknown state %q", where, r.State)
		}
		for _, from := range r.From {
			if _, ok := t.State(from); !ok {
				return fail("%s: unknown from state %q", where, from)
			}
		}
		switch r.Kind {
		case ReactionHeld:
			if r.Undoes != "" {
				return fail("%s: a held reaction undoes nothing", where)
			}
			if len(r.From) == 0 {
				return fail("%s: a held reaction names the states it holds the move from (from)", where)
			}
			for _, from := range r.From {
				tr, ok := t.Transition(from, r.State)
				if !ok {
					return fail("%s: there is no transition %s -> %s to hold", where, from, r.State)
				}
				if tr.Escape {
					return fail("%s: transition %s -> %s is an escape: it cancels a hold, so it may not open one",
						where, from, r.State)
				}
				if h, ok := t.checkOn(from, r.State); ok {
					return fail("%s: transition %s -> %s already has the check %q; one hold per transition",
						where, from, r.State, h)
				}
			}
			l, ok := t.Link(r.Link)
			if !ok {
				return fail("%s: link %q is not declared", where, r.Link)
			}
			if !l.SourceSingle() {
				return fail("%s: link %s may have many targets; a reaction follows a link of max: one", where, l.Name)
			}
			// A link the kernel resolves is sure to be set only in the state
			// whose entry resolves it. (A link people set is exempt.)
			if l.Resolve != nil {
				for _, from := range r.From {
					if from != l.Resolve.OnEnter {
						return fail("%s: it leaves %s, but link %s is resolved on entering %s, so it may be unset in %s",
							where, from, l.Name, l.Resolve.OnEnter, from)
					}
				}
			}
			if len(r.Add) == 0 {
				return fail("%s: a held reaction adds to some field (add)", where)
			}
			r.Programs = map[string]cel.Program{}
			var reads []string
			for _, field := range slices.Sorted(maps.Keys(r.Add)) {
				prg, rs, out, err := compileFields(env, r.Add[field])
				if err != nil {
					return fail("%s: add %s: %v", where, field, err)
				}
				if !isInt(out) {
					return fail("%s: add %s must yield an int, not %s", where, field, out)
				}
				r.Programs[field] = prg
				reads = append(reads, rs...)
			}
			slices.Sort(reads)
			r.Reads = slices.Compact(reads)
		case ReactionAfter:
			if len(r.Add) > 0 || r.Link != "" {
				return fail("%s: an after reaction only undoes a held one for now (undoes); a patch of its own is Later", where)
			}
			if r.Undoes == "" {
				return fail("%s: an after reaction names the held reaction it undoes (undoes)", where)
			}
		default:
			return fail("%s: unknown kind %q: want held or after", where, r.Kind)
		}
	}
	// Undos: each names a held reaction, and each held reaction has one.
	undos := map[string]string{}
	for _, r := range t.Reactions {
		if r.Kind != ReactionAfter {
			continue
		}
		held, ok := t.Reaction(r.Undoes)
		if !ok || held.Kind != ReactionHeld {
			return fail("reaction %q undoes %q, which is no held reaction", r.ID, r.Undoes)
		}
		if other, dup := undos[held.ID]; dup {
			return fail("held reaction %q has two undos, %q and %q", held.ID, other, r.ID)
		}
		undos[held.ID] = r.ID
	}
	for _, r := range t.Reactions {
		if r.Kind == ReactionHeld && undos[r.ID] == "" {
			return fail("held reaction %q has no undo: what it takes must be given back if its hold "+
				"closes any other way than allowed (an after reaction with undoes: %s)", r.ID, r.ID)
		}
	}
	return nil
}

// checkOn returns the check hook on from -> to, if any.
func (t *Type) checkOn(from, to string) (string, bool) {
	for _, h := range t.Hooks {
		if h.On == OnTransition && h.From == from && h.To == to {
			return h.ID, true
		}
	}
	return "", false
}

// RoleWritten returns the fields among ids that only holders of roles may
// write on the public door (writable_by: [role:<name>, ...]), sorted.
func (t *Type) RoleWritten(ids []string) []string {
	var out []string
	for _, id := range ids {
		if f, ok := t.Field(id); ok && len(f.WritableByRoles) > 0 {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return out
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
		if len(entries) == 0 {
			return fmt.Errorf("field %s: writable_by is an empty list; leave it out for anyone", f.ID)
		}
		for _, e := range entries {
			name, ok := strings.CutPrefix(e, AllowedRolePrefix)
			if !ok {
				return fmt.Errorf("field %s: writable_by entry %q: a list takes role:<name> entries only "+
					"(kernel stands alone: writable_by: kernel)", f.ID, e)
			}
			f.WritableByRoles = append(f.WritableByRoles, name) // check validates the name
		}
	default:
		return fmt.Errorf("field %s: writable_by must be kernel or a list of role:<name>", f.ID)
	}
	return nil
}
