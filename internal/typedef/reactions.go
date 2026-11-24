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
	// ReactionMove runs once its trigger has committed and moves its target
	// into To, by the target's own transition rules. On entering a state
	// the target is the Item Link points to, whose Type must accept the
	// move; on created_inside it is the Item itself.
	ReactionMove ReactionKind = "move"
	// ReactionCreate runs once its trigger (entering a state) has committed
	// and creates an Item of Type@Version contained in the Item.
	ReactionCreate ReactionKind = "create"
)

// ByCreator is the one value Reaction.By takes: the Item created inside was
// created by the declaring Item's creator (policy.IsRequester).
const ByCreator = "creator"

// ChildPrefix starts the CEL variable of a created Item's field in a
// created_inside reaction's `when`: child.f_public.
const ChildPrefix = "child."

// ContainerLink is reserved: no link type may take the name.
const ContainerLink = "container"

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

	// Jira part 2, step 2: move and create reactions.
	//
	// Of (on: created_inside): the Types whose creation inside the Item
	// triggers the reaction.
	Of []string `yaml:"of" json:"of,omitempty"`
	// When is a CEL condition, bool. on: enter: over the Item's own fields.
	// on: created_inside: over the created Item's fields, as child.<id>.
	When string `yaml:"when" json:"when,omitempty"`
	// By (on: created_inside): who must have created the Item inside.
	By string `yaml:"by" json:"by,omitempty"`
	// To (move): the state the target is moved into.
	To string `yaml:"to" json:"to,omitempty"`
	// TargetFrom (move, on: enter): the target is moved only from these
	// states. (From keeps its meaning there: the states the trigger comes
	// from. On created_inside, From is the Item's own states: it is both.)
	TargetFrom []string `yaml:"target_from" json:"target_from,omitempty"`
	// Type, Version and Fields (create): the Type version of the Item
	// created inside this one, and its fields as CEL over the Item's
	// fields plus cause.at.
	Type    string            `yaml:"type" json:"type,omitempty"`
	Version int               `yaml:"version" json:"version,omitempty"`
	Fields  map[string]string `yaml:"fields" json:"fields,omitempty"`

	// Jira part 2, step 3 (see jira23.go).
	//
	// Children (move): the reaction moves Items contained in this one,
	// instead of following Link or moving the Item itself.
	Children *Children `yaml:"children" json:"children,omitempty"`
	// Target and Set (set): the Item written (TargetContainer) and its
	// fields, each a CEL expression over this Item's fields plus cause.at.
	Target string `yaml:"target" json:"target,omitempty"`
	// ForEach (Jira 2.4b, kernel change 3; create only): a multi-value
	// field of the source; one Item per member, which the fields read as
	// `each`. OnGuard (kernel change 6; move only): "skip" records a target
	// guard that does not hold as reaction_skipped guard_false. Phase B
	// stubs: parsed only.
	ForEach string            `yaml:"for_each" json:"for_each,omitempty"`
	OnGuard string            `yaml:"on_guard" json:"on_guard,omitempty"`
	Set     map[string]string `yaml:"set" json:"set,omitempty"`

	// Programs are Add's expressions compiled, by target field; Reads are
	// the source fields they read, sorted (frozen while the reaction's hold
	// is open). Filled when the Type is loaded.
	Programs map[string]cel.Program `yaml:"-" json:"-"`
	Reads    []string               `yaml:"-" json:"-"`
	// Move and create reactions, filled when the Type is loaded: When
	// compiled; the created Item's fields When reads as child.<id>
	// (created_inside), sorted; Fields compiled, with each expression's type.
	WhenProgram   cel.Program            `yaml:"-" json:"-"`
	ChildReads    []string               `yaml:"-" json:"-"`
	FieldPrograms map[string]cel.Program `yaml:"-" json:"-"`
	fieldTypes    map[string]*cel.Type
	// Step 3, filled when the Type is loaded: Set compiled; the fields of
	// linked Items a create's Fields read, sorted.
	SetPrograms map[string]cel.Program `yaml:"-" json:"-"`
	LinkedReads []LinkedRead           `yaml:"-" json:"-"`
	setTypes    map[string]*cel.Type
	// Jira 2.4b, filled when the Type is loaded: the container's own
	// fields a create's Fields read (container.<field>), sorted.
	ContainerReads []string `yaml:"-" json:"-"`
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
	a, ok := t.AcceptsReactions[key]
	if !ok {
		return false
	}
	for _, f := range fields {
		if !slices.Contains(a.Fields, f) {
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
	var c crossCheck
	byID := map[string][]*Type{}
	for _, t := range types {
		byID[t.ID] = append(byID[t.ID], t)
	}
	for _, src := range types {
		rep := c.on(src)
		for i, l := range src.Links {
			if l.Resolve == nil {
				continue
			}
			for _, tgt := range byID[l.Targets[0]] {
				f, ok := tgt.Field(l.Resolve.Field)
				if !ok || f.Type != Principal || f.Multiple {
					rep.fail(CodeWrongFieldType, at("links", i, "resolve", "field"), "Name a principal field holding one principal, in every version of the target",
						"link %s: resolve field %q is not a principal field of %s v%d", l.Name, l.Resolve.Field, tgt.ID, tgt.Version)
					break
				}
				if _, ok := tgt.State(l.Resolve.State); !ok {
					rep.fail(CodeUnknownState, at("links", i, "resolve", "state"), "Name a state every version of the target declares",
						"link %s: resolve state %q is not a state of %s v%d", l.Name, l.Resolve.State, tgt.ID, tgt.Version)
					break
				}
			}
		}
		for i, r := range src.Reactions {
			if r.Kind == ReactionMove || r.Kind == ReactionCreate || r.Kind == ReactionSet {
				continue // checked by checkAfterAcross, below
			}
			checkHeldAcross(rep, at("reactions", i), src, r, byID)
		}
	}
	checkAfterAcross(&c, types, byID)
	checkStep3Across(&c, types, byID)
	checkJira24bAcross(&c, types, byID)
	checkReactionCycles(&c, types)
	// What each Type accepts must exist and point at it.
	for _, tgt := range types {
		for _, key := range slices.Sorted(maps.Keys(tgt.AcceptsReactions)) {
			typeID, reactionID, _ := strings.Cut(key, ":")
			// A consent by reaction ID names no Type: nothing has to use it
			// for the Types to load.
			if typeID == WildcardType {
				continue
			}
			if !consentUsed(tgt, byID[typeID], reactionID) {
				c.on(tgt).fail(CodeUnknownReaction, at("accepts_reactions", key), "Name a reaction that reaches this Type, or remove the consent",
					"accepts_reactions names %s, which is no reaction of a Type following a link to %s", key, tgt.ID)
			}
		}
	}
	return c.err()
}

// checkHeldAcross checks a held reaction, or the undo of one, against the
// Types its link targets: the fields it adds to, and their consent.
func checkHeldAcross(rep reporter, p string, src *Type, r Reaction, byID map[string][]*Type) {
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
					rep.fail(CodeUnknownField, p, "Add to a field every target version declares",
						"reaction %s writes %s, which %s v%d does not declare", r.ID, f, tgt.ID, tgt.Version)
					return
				}
				if tf.Type != Int {
					rep.fail(CodeWrongFieldType, p, "Add to an int field",
						"reaction %s adds to %s, which is not an int in %s v%d", r.ID, f, tgt.ID, tgt.Version)
					return
				}
			}
			if !tgt.Accepts(key, fields) {
				rep.fail(CodeConsentMissing, p, "Add the consent to the target Type's accepts_reactions",
					"reaction %s writes %v of %s v%d, which does not accept %s for them (accepts_reactions)", r.ID, fields, tgt.ID, tgt.Version, key)
				return
			}
		}
	}
}

// consentUsed reports whether a reaction reactionID of one of the versions
// srcs reaches tgt, so that tgt's consent to it means something.
func consentUsed(tgt *Type, srcs []*Type, reactionID string) bool {
	for _, src := range srcs {
		r, ok := src.Reaction(reactionID)
		if !ok {
			continue
		}
		// Children it moves, or a field it sets on its container (which the
		// Type that accepts it may be).
		if r.Children != nil && slices.Contains(r.Children.Of, tgt.ID) || r.Kind == ReactionSet {
			return true
		}
		// A move at its container (Jira 2.4b), which the Type that accepts
		// it may be.
		if r.Kind == ReactionMove && r.Target == TargetContainer {
			return true
		}
		if r.Kind == ReactionAfter {
			r, _ = src.Reaction(r.Undoes)
		}
		if l, ok := src.Link(r.Link); ok && slices.Contains(l.Targets, tgt.ID) {
			return true
		}
		// A create names the Type it creates, not a link.
		if r.Kind == ReactionCreate && r.Type == tgt.ID {
			return true
		}
	}
	return false
}

// reactionID is the shape of a reaction or invariant ID: a code, since it
// becomes a principal's last part and a refusal's reason.
var reactionID = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

// roleNameRe matches a role name (as the item service's EnsureRoles).
var roleNameRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,62}$`)

// checkRoles validates a field's writable_by list: role names, and no
// entry (a role, people, agents) twice. Paths index the list as written.
func (f Field) checkRoles(r reporter, p string) error {
	entries := f.writers
	if entries == nil {
		for _, role := range f.WritableByRoles {
			entries = append(entries, AllowedRolePrefix+role)
		}
		if f.WritableByPeople {
			entries = append(entries, WriterPeople)
		}
		if f.WritableByAgents {
			entries = append(entries, WriterAgents)
		}
	}
	if len(entries) == 0 {
		return nil
	}
	if f.WritableBy != WritableByAnyone {
		return r.fail(CodeConflict, at(p, "writable_by"), "Write writable_by: kernel, or a list of writers, not both",
			"field %s: writable_by is either kernel or a list of writers (people, agents, role:<name>), not both", f.ID)
	}
	seen := map[string]bool{}
	for i, e := range entries {
		if e == WriterPeople || e == WriterAgents {
			if seen[e] {
				r.fail(CodeDuplicate, at(p, "writable_by", i), "Remove the second one",
					"field %s: writable_by names %s twice", f.ID, e)
			}
			seen[e] = true
			continue
		}
		role := strings.TrimPrefix(e, AllowedRolePrefix)
		if !roleNameRe.MatchString(role) {
			r.fail(CodeInvalidName, at(p, "writable_by", i), "Name the role in lower case, digits, _ and -",
				"field %s: writable_by role %q is not a role name (lower case, digits, _ and -)", f.ID, role)
			continue
		}
		if seen[AllowedRolePrefix+role] {
			r.fail(CodeDuplicate, at(p, "writable_by", i), "Remove the second one",
				"field %s: writable_by names role %s twice", f.ID, role)
		}
		seen[AllowedRolePrefix+role] = true
	}
	return nil
}

// checkKernelLinks validates a link type's writable_by and resolve. States
// are known; the resolve's target Type is checked by CheckReactions.
func (t *Type) checkKernelLinks(r reporter) {
	resolvedOn := map[string]string{} // on_enter -> the link set there
	for i, l := range t.Links {
		t.checkKernelLink(r, at("links", i), l, resolvedOn)
	}
}

func (t *Type) checkKernelLink(r reporter, p string, l LinkType, resolvedOn map[string]string) {
	where := "link " + l.Name
	l.checkRoles(r, p)
	switch l.WritableBy {
	case WritableByAnyone, WritableByKernel:
	default:
		r.fail(CodeInvalidValue, at(p, "writable_by"), "Write writable_by: kernel, a list of role:<name>, or leave it out",
			"%s: writable_by %q: the only value is kernel", where, l.WritableBy)
	}
	res, q := l.Resolve, at(p, "resolve")
	if l.WritableBy == WritableByKernel && res == nil {
		r.fail(CodeMissingCounterpart, at(p, "writable_by"), "Add a resolve that says how the kernel sets the link",
			"%s is writable_by kernel, but nothing sets it: add a resolve", where)
		return
	}
	if res == nil {
		return
	}
	if l.WritableBy != WritableByKernel {
		r.fail(CodeConflict, q, "Add writable_by: kernel to the link",
			"%s: a resolved link must be writable_by kernel, or the public door could change it", where)
	}
	if !l.SourceSingle() {
		r.fail(CodeConflict, q, "Give the link max: one", "%s: a resolved link has at most one target (max: one)", where)
	}
	if len(l.Targets) != 1 || l.Targets[0] == AnyType {
		r.fail(CodeConflict, at(p, "targets"), "Name exactly one target Type", "%s: a resolved link has exactly one target Type, not %v", where, l.Targets)
	}
	if _, ok := t.State(res.OnEnter); !ok {
		r.fail(CodeUnknownState, at(q, "on_enter"), "Name a declared state", "%s: resolve on_enter %q is not a declared state", where, res.OnEnter)
	} else if res.OnEnter == t.InitialState {
		r.fail(CodeInvalidValue, at(q, "on_enter"), "Resolve the link on entering a later state",
			"%s: resolve on_enter %q is the initial state, which Create enters without resolving links", where, res.OnEnter)
	}
	if res.Equals != ResolveEqualsCreator {
		r.fail(CodeInvalidValue, at(q, "equals"), "Write equals: creator", "%s: resolve equals %q: the only value is creator", where, res.Equals)
	}
	if res.Field == "" || res.State == "" {
		r.fail(CodeRequired, q, "Give the resolve a field and a state of the target", "%s: resolve needs a field and a state of the target", where)
	}
	// One per state: the entering event records the one link it set.
	if other, dup := resolvedOn[res.OnEnter]; dup {
		r.fail(CodeOnlyOneAllowed, at(q, "on_enter"), "Resolve the two links on entering different states",
			"%s: link %s is already set on entering %s; one resolved link per state", where, other, res.OnEnter)
		return
	}
	resolvedOn[res.OnEnter] = l.Name
}

// newFieldEnv declares one CEL variable per field and nothing else: no
// today and no links. Invariants and reaction expressions are rules over
// an Item's own fields, true at any time and whatever other Items say.
func newFieldEnv(t *Type) (*cel.Env, error) {
	opts := calendarFunctions()
	for _, f := range t.celFields() {
		opts = append(opts, cel.Variable(f.ID, fieldCELType(f)))
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
func (t *Type) checkInvariants(r reporter) {
	env, err := newFieldEnv(t)
	if err != nil {
		r.fail(CodeExpressionInvalid, "invariants", "Fix the fields the message names", "invariant environment: %v", err)
		return
	}
	seen := map[string]bool{}
	for i := range t.Invariants {
		inv, p := &t.Invariants[i], at("invariants", i)
		if !reactionID.MatchString(inv.ID) {
			r.fail(CodeInvalidName, at(p, "id"), "Name it in lower case, digits and _: the ID is the code a refused write carries",
				"invariant %q: the ID is a refusal code (lower case, digits and _)", inv.ID)
		} else if seen[inv.ID] {
			r.fail(CodeDuplicate, at(p, "id"), "Rename one of the two invariants", "invariant %s is declared twice", inv.ID)
		}
		seen[inv.ID] = true
		if inv.Rule == "" {
			r.fail(CodeRequired, at(p, "rule"), "Write the rule, a condition that is always true", "invariant %s has no rule", inv.ID)
			continue
		}
		prg, _, out, err := compileFields(env, inv.Rule)
		switch {
		case err != nil:
			r.failExpr(err, at(p, "rule"), "invariant %s: %v", inv.ID, err)
		case !out.IsExactType(cel.BoolType):
			r.fail(CodeExpressionType, at(p, "rule"), "Write a condition that is true or false", "invariant %s must yield a bool, not %s", inv.ID, out)
		default:
			inv.Program = prg
		}
	}
}

// checkAccepts validates accepts_reactions within the Type: the keys'
// shape, and the fields (declared ints: reactions only add).
func (t *Type) checkAccepts(r reporter) {
	for _, key := range slices.Sorted(maps.Keys(t.AcceptsReactions)) {
		p := at("accepts_reactions", key)
		typeID, id, ok := item.ReactionParts(item.KindReaction + ":" + key)
		// A star stands for any Type, whole, and only there: *:<id>.
		if !ok || !reactionID.MatchString(id) || (typeID != WildcardType && strings.Contains(typeID, "*")) {
			r.fail(CodeInvalidName, p, "Write the key as <type>:<reaction> or *:<reaction>",
				"accepts_reactions key %q must be <type>:<reaction> or *:<reaction>, without a version", key)
			continue
		}
		acc := t.AcceptsReactions[key]
		if len(acc.Fields) == 0 && len(acc.Moves) == 0 && !acc.MayCreate && len(acc.Sets) == 0 {
			r.fail(CodeRequired, p, "Name the fields, moves, creates or sets it accepts, or remove the consent",
				"accepts_reactions %s accepts nothing: name fields, moves, creates or sets", key)
			continue
		}
		// A reaction adds to a field over a link only, and there consent
		// names the Type.
		if typeID == WildcardType && len(acc.Fields) > 0 {
			r.fail(CodeKeyNotApplicable, at(p, "fields"), "Name the Type in the key, or consent to moves, creates and sets only",
				"accepts_reactions %s: fields are added over a link, where consent names the Type; "+
					"*:<reaction> is for moves, creates and sets from the container or a contained Item", key)
		}
		for j, id := range acc.Sets {
			if _, ok := t.Field(id); !ok {
				r.fail(CodeUnknownField, at(p, "sets", j), "Declare the field, or remove it", "accepts_reactions %s: sets %s, which is not declared", key, id)
			}
		}
		for j, id := range acc.Fields {
			if f, ok := t.Field(id); !ok {
				r.fail(CodeUnknownField, at(p, "fields", j), "Declare the field, or remove it", "accepts_reactions %s: field %s is not declared", key, id)
			} else if f.Type != Int {
				r.fail(CodeWrongFieldType, at(p, "fields", j), "Accept additions to an int field only", "accepts_reactions %s: field %s is not an int (a reaction adds)", key, id)
			}
		}
		for j, st := range acc.Moves {
			if _, ok := t.State(st); !ok {
				r.fail(CodeUnknownState, at(p, "moves", j), "Name a declared state", "accepts_reactions %s: moves into %q, which is not a state", key, st)
			}
		}
		for j, id := range acc.Creates {
			if _, ok := t.Field(id); !ok {
				r.fail(CodeUnknownField, at(p, "creates", j), "Declare the field, or remove it", "accepts_reactions %s: creates with field %s, which is not declared", key, id)
			}
		}
	}
}

// checkReactions validates and compiles the reactions. Hooks are known.
func (t *Type) checkReactions(rep reporter) {
	env, err := newFieldEnv(t)
	if err != nil {
		rep.fail(CodeExpressionInvalid, "reactions", "Fix the fields the message names", "reaction environment: %v", err)
		return
	}
	seen := map[string]bool{}
	for i := range t.Reactions {
		t.checkReaction(rep, i, seen, env)
	}
	// Undos: each names a held reaction, and each held reaction has one.
	undos := map[string]string{}
	for i, r := range t.Reactions {
		if r.Kind != ReactionAfter || r.Undoes == "" {
			continue
		}
		held, ok := t.Reaction(r.Undoes)
		if !ok || held.Kind != ReactionHeld {
			rep.fail(CodeUnknownReaction, at("reactions", i, "undoes"), "Name a held reaction of this Type",
				"reaction %q undoes %q, which is no held reaction", r.ID, r.Undoes)
			continue
		}
		if other, dup := undos[held.ID]; dup {
			rep.fail(CodeOnlyOneAllowed, at("reactions", i, "undoes"), "Keep one undo per held reaction",
				"held reaction %q has two undos, %q and %q", held.ID, other, r.ID)
			continue
		}
		undos[held.ID] = r.ID
	}
	for i, r := range t.Reactions {
		if r.Kind == ReactionHeld && reactionID.MatchString(r.ID) && undos[r.ID] == "" {
			rep.fail(CodeMissingCounterpart, at("reactions", i), "Add an after reaction with undoes naming it",
				"held reaction %q has no undo: what it takes must be given back if its hold "+
					"closes any other way than allowed (an after reaction with undoes: %s)", r.ID, r.ID)
		}
	}
}

// keySet pairs a key with whether a declaration sets it.
type keySet struct {
	key string
	set bool
}

// firstKey returns the first key that is set: where a problem about keys
// that do not apply points.
func firstKey(keys ...keySet) string {
	for _, k := range keys {
		if k.set {
			return k.key
		}
	}
	return ""
}

// checkReaction checks reactions[i]: its first problem stops it.
func (t *Type) checkReaction(rep reporter, i int, seen map[string]bool, env *cel.Env) {
	r, p := &t.Reactions[i], at("reactions", i)
	where := fmt.Sprintf("reaction %q", r.ID)
	if !reactionID.MatchString(r.ID) {
		rep.fail(CodeInvalidName, at(p, "id"), "Name it in lower case, digits and _", "%s: not a reaction ID (lower case, digits and _)", where)
		return
	}
	if seen[r.ID] {
		rep.fail(CodeDuplicate, at(p, "id"), "Rename one of the two reactions", "%s is declared twice", where)
		return
	}
	seen[r.ID] = true
	if _, ok := t.Hook(r.ID); ok {
		rep.fail(CodeConflict, at(p, "id"), "Rename the reaction or the hook", "%s: a hook has the same ID; holds name either, so the IDs are shared", where)
		return
	}
	if r.Kind == ReactionMove || r.Kind == ReactionCreate || r.Kind == ReactionSet {
		t.checkAfterReaction(rep, p, r, where)
		return
	}
	if k := firstKey(keySet{"of", len(r.Of) > 0}, keySet{"when", r.When != ""}, keySet{"by", r.By != ""}, keySet{"to", r.To != ""},
		keySet{"target_from", len(r.TargetFrom) > 0}, keySet{"type", r.Type != ""}, keySet{"fields", len(r.Fields) > 0}); k != "" {
		rep.fail(CodeKeyNotApplicable, at(p, k), "Remove it, or make the reaction a move or a create",
			"%s: of, when, by, to, target_from, type and fields are for move and create reactions", where)
		return
	}
	if k := firstKey(keySet{"children", r.Children != nil}, keySet{"target", r.Target != ""}, keySet{"set", len(r.Set) > 0}); k != "" {
		rep.fail(CodeKeyNotApplicable, at(p, k), "Remove it, or make the reaction a move or a set",
			"%s: children, target and set are for move and set reactions", where)
		return
	}
	if r.On != OnEnter {
		rep.fail(CodeInvalidValue, at(p, "on"), "Write on: enter", "%s: on must be enter, not %q", where, r.On)
		return
	}
	if _, ok := t.State(r.State); !ok {
		rep.fail(CodeUnknownState, at(p, "state"), "Name a declared state", "%s: unknown state %q", where, r.State)
		return
	}
	for j, from := range r.From {
		if _, ok := t.State(from); !ok {
			rep.fail(CodeUnknownState, at(p, "from", j), "Name a declared state", "%s: unknown from state %q", where, from)
			return
		}
	}
	switch r.Kind {
	case ReactionHeld:
		t.checkHeldReaction(rep, p, r, where, env)
	case ReactionAfter:
		if k := firstKey(keySet{"add", len(r.Add) > 0}, keySet{"link", r.Link != ""}); k != "" {
			rep.fail(CodeKeyNotApplicable, at(p, k), "Remove it: an after reaction only undoes a held one",
				"%s: an after reaction only undoes a held one for now (undoes); a patch of its own is Later", where)
			return
		}
		if r.Undoes == "" {
			rep.fail(CodeRequired, at(p, "undoes"), "Name the held reaction it undoes", "%s: an after reaction names the held reaction it undoes (undoes)", where)
		}
	default:
		rep.fail(CodeInvalidValue, at(p, "kind"), "Use kind: held, after, move, create or set",
			"%s: unknown kind %q: want held, after, move, create or set", where, r.Kind)
	}
}

// checkHeldReaction checks and compiles a held reaction.
func (t *Type) checkHeldReaction(rep reporter, p string, r *Reaction, where string, env *cel.Env) {
	if r.Undoes != "" {
		rep.fail(CodeKeyNotApplicable, at(p, "undoes"), "Remove undoes: an after reaction undoes a held one", "%s: a held reaction undoes nothing", where)
		return
	}
	if len(r.From) == 0 {
		rep.fail(CodeRequired, at(p, "from"), "List the states it holds the move from", "%s: a held reaction names the states it holds the move from (from)", where)
		return
	}
	for j, from := range r.From {
		tr, ok := t.Transition(from, r.State)
		if !ok {
			rep.fail(CodeUnknownTransition, at(p, "from", j), "Declare the transition, or hold another",
				"%s: there is no transition %s -> %s to hold", where, from, r.State)
			return
		}
		if tr.Escape {
			rep.fail(CodeHeldTransition, at(p, "from", j), "Hold another transition: an escape may not be held",
				"%s: transition %s -> %s is an escape: it cancels a hold, so it may not open one", where, from, r.State)
			return
		}
		if h, ok := t.checkOn(from, r.State); ok {
			rep.fail(CodeOnlyOneAllowed, at(p, "from", j), "Hold the transition by the check or by the reaction, not both",
				"%s: transition %s -> %s already has the check %q; one hold per transition", where, from, r.State, h)
			return
		}
	}
	l, ok := t.Link(r.Link)
	if !ok {
		rep.fail(CodeUnknownLink, at(p, "link"), "Name a declared link", "%s: link %q is not declared", where, r.Link)
		return
	}
	if !l.SourceSingle() {
		rep.fail(CodeConflict, at(p, "link"), "Follow a link with max: one", "%s: link %s may have many targets; a reaction follows a link of max: one", where, l.Name)
		return
	}
	// A link the kernel resolves is sure to be set only in the state whose
	// entry resolves it. (A link people set is exempt.)
	if l.Resolve != nil {
		for j, from := range r.From {
			if from != l.Resolve.OnEnter {
				rep.fail(CodeConflict, at(p, "from", j), "Hold the move only from the state that resolves the link",
					"%s: it leaves %s, but link %s is resolved on entering %s, so it may be unset in %s",
					where, from, l.Name, l.Resolve.OnEnter, from)
				return
			}
		}
	}
	if len(r.Add) == 0 {
		rep.fail(CodeRequired, at(p, "add"), "Name the field it adds to, and the expression it adds", "%s: a held reaction adds to some field (add)", where)
		return
	}
	r.Programs = map[string]cel.Program{}
	var reads []string
	for _, field := range slices.Sorted(maps.Keys(r.Add)) {
		prg, rs, out, err := compileFields(env, r.Add[field])
		if err != nil {
			rep.failExpr(err, at(p, "add", field), "%s: add %s: %v", where, field, err)
			return
		}
		if !isInt(out) {
			rep.fail(CodeExpressionType, at(p, "add", field), "Add a whole number", "%s: add %s must yield an int, not %s", where, field, out)
			return
		}
		r.Programs[field] = prg
		reads = append(reads, rs...)
	}
	slices.Sort(reads)
	r.Reads = slices.Compact(reads)
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
// prefix. Stage (a) has checked the keys and writable_by's form (shape.go);
// the rules on the list are check's.
func (f *Field) UnmarshalYAML(n *yaml.Node) error {
	type plain Field // no methods: decoding it does not recurse here
	rest, wb := withoutKey(n, "writable_by")
	var p plain
	if err := rest.Decode(&p); err != nil {
		return err
	}
	*f = Field(p)
	f.writers = listedWriters(wb)
	wb, f.WritableByPeople, f.WritableByAgents = takeWriters(wb)
	by, roles, err := decodeWritableBy(wb)
	f.WritableBy, f.WritableByRoles = by, roles
	return err
}

// listedWriters returns a writable_by list's entries as written, when it
// names people or agents; nil otherwise.
func listedWriters(wb *yaml.Node) []string {
	wb = resolveAlias(wb)
	if wb == nil || wb.Kind != yaml.SequenceNode {
		return nil
	}
	var out []string
	other := false
	for _, e := range wb.Content {
		v := resolveAlias(e)
		out = append(out, v.Value)
		if v.Value == WriterPeople || v.Value == WriterAgents {
			other = true
		}
	}
	if !other {
		return nil
	}
	return out
}

// takeWriters returns a field's writable_by list without its people and
// agents entries, and whether it had each. A scalar, or nothing, is
// returned as it is.
func takeWriters(wb *yaml.Node) (rest *yaml.Node, people, agents bool) {
	wb = resolveAlias(wb)
	if wb == nil || wb.Kind != yaml.SequenceNode {
		return wb, false, false
	}
	cp := *wb
	cp.Content = nil
	for _, e := range wb.Content {
		switch v := resolveAlias(e); {
		case v.Kind == yaml.ScalarNode && v.Value == WriterPeople:
			people = true
		case v.Kind == yaml.ScalarNode && v.Value == WriterAgents:
			agents = true
		default:
			cp.Content = append(cp.Content, e)
		}
	}
	return &cp, people, agents
}

// withoutKey returns a copy of mapping n without key, and key's value
// (nil if absent).
func withoutKey(n *yaml.Node, key string) (*yaml.Node, *yaml.Node) {
	rest := *n
	if n.Kind != yaml.MappingNode {
		return &rest, nil
	}
	var v *yaml.Node
	rest.Content = nil
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			v = n.Content[i+1]
			continue
		}
		rest.Content = append(rest.Content, n.Content[i], n.Content[i+1])
	}
	return &rest, v
}

// decodeWritableBy reads writable_by: kernel (a scalar), or a list of
// role:<name> entries, returned without the prefix.
func decodeWritableBy(wb *yaml.Node) (WritableBy, []string, error) {
	if wb == nil {
		return WritableByAnyone, nil, nil
	}
	if wb.Kind == yaml.ScalarNode {
		return WritableBy(wb.Value), nil, nil
	}
	var entries []string
	if err := wb.Decode(&entries); err != nil {
		return WritableByAnyone, nil, err
	}
	roles := make([]string, len(entries))
	for i, e := range entries {
		roles[i] = strings.TrimPrefix(e, AllowedRolePrefix)
	}
	return WritableByAnyone, roles, nil
}
