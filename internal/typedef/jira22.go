package typedef

import (
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"sort"
	"strings"

	"cel.dev/cel-go/cel"
	"go.yaml.in/yaml/v3"
)

// Jira part 2, step 2 (docs/design.md, "Decisions for Jira part 2"):
// reactions that move or create, their consent, and multi-value fields.

// Accepted is what a Type accepts of one reaction of another Type: the int
// fields it may add to (Fields, the only thing a reaction could do before
// this step), the states it may move an Item into (Moves), and whether it
// may create Items, setting the fields of Creates.
//
// In YAML and in the stored JSON it is the list it always was (Fields), or
// a map with fields, moves and creates.
type Accepted struct {
	Fields  []string
	Moves   []string
	Creates []string
	// Sets (Jira part 2, step 3) are the fields a set reaction may set or
	// unset on an Item of the Type.
	Sets []string
	// MayCreate tells `creates: []` (it may create, setting nothing) from
	// no `creates` at all.
	MayCreate bool
}

// acceptedDoc is Accepted's map form.
type acceptedDoc struct {
	Fields  []string  `yaml:"fields" json:"fields,omitempty"`
	Moves   []string  `yaml:"moves" json:"moves,omitempty"`
	Creates *[]string `yaml:"creates" json:"creates,omitempty"`
	Sets    []string  `yaml:"sets" json:"sets,omitempty"`
}

func (a *Accepted) fromDoc(d acceptedDoc) {
	a.Fields, a.Moves, a.Sets = d.Fields, d.Moves, d.Sets
	if d.Creates != nil {
		a.Creates, a.MayCreate = *d.Creates, true
	}
}

// UnmarshalYAML reads the list form or the map form. A method named like
// this is how a Go type takes over its own decoding (like a custom Jackson
// deserializer).
func (a *Accepted) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.SequenceNode {
		return n.Decode(&a.Fields)
	}
	// The map form; stage (a) has checked its keys (shape.go).
	var d acceptedDoc
	if err := n.Decode(&d); err != nil {
		return err
	}
	a.fromDoc(d)
	return nil
}

// MarshalJSON writes the list form when only fields are accepted, so a Type
// stored before this step and one stored after it look the same.
func (a Accepted) MarshalJSON() ([]byte, error) {
	if len(a.Moves) == 0 && !a.MayCreate && len(a.Sets) == 0 {
		return json.Marshal(a.Fields)
	}
	d := acceptedDoc{Fields: a.Fields, Moves: a.Moves, Sets: a.Sets}
	if a.MayCreate {
		creates := a.Creates
		if creates == nil {
			creates = []string{}
		}
		d.Creates = &creates
	}
	return json.Marshal(d)
}

func (a *Accepted) UnmarshalJSON(data []byte) error {
	if strings.HasPrefix(strings.TrimSpace(string(data)), "[") {
		return json.Unmarshal(data, &a.Fields)
	}
	var d acceptedDoc
	if err := json.Unmarshal(data, &d); err != nil {
		return err
	}
	a.fromDoc(d)
	return nil
}

// AcceptsMove reports whether this Type accepts reaction key (<type>:<id>)
// moving its Items into state.
func (t *Type) AcceptsMove(key, state string) bool {
	return slices.Contains(t.AcceptsReactions[key].Moves, state)
}

// AcceptsCreate reports whether this Type accepts reaction key creating
// its Items with these fields set (each must be listed).
func (t *Type) AcceptsCreate(key string, fields []string) bool {
	a, ok := t.AcceptsReactions[key]
	if !ok || !a.MayCreate {
		return false
	}
	for _, f := range fields {
		if !slices.Contains(a.Creates, f) {
			return false
		}
	}
	return true
}

// --- multi-value fields ---

// SetOp is a set operation on a multi-value field, as a patch carries it:
// {"add": [...], "remove": [...]}.
type SetOp struct {
	Add, Remove []string
}

// members reads a list of a multi-value field's members: each a valid value
// of the field's type. A JSON array arrives as []any.
func members(f Field, v any) ([]string, string) {
	var list []any
	switch l := v.(type) {
	case []any:
		list = l
	case []string:
		for _, s := range l {
			list = append(list, s)
		}
	default:
		return nil, "want a list of values"
	}
	out := make([]string, 0, len(list))
	for _, m := range list {
		s, ok := m.(string)
		if !ok {
			return nil, "every member must be a string"
		}
		switch f.Type {
		case Enum:
			if !slices.Contains(f.Options, s) {
				return nil, "every member must be one of " + strings.Join(f.Options, ", ")
			}
		case Principal:
			if !checkPrincipalValue(s) {
				return nil, "every member must be a principal, user:<id> or agent:<id>"
			}
		case Date:
			if !isDate(s) {
				return nil, "every member must be a date, YYYY-MM-DD"
			}
		}
		out = append(out, s)
	}
	return out, ""
}

// AsSetOp reads a patch value of a multi-value field as a set operation.
// ok is false when v is not one (a list replaces the set).
func AsSetOp(f Field, v any) (op SetOp, ok bool, reason string) {
	m, isMap := v.(map[string]any)
	if !isMap {
		return SetOp{}, false, ""
	}
	if len(m) == 0 {
		return SetOp{}, true, "a set operation needs add or remove"
	}
	for k, list := range m {
		got, reason := members(f, list)
		if reason != "" {
			return SetOp{}, true, k + ": " + reason
		}
		switch k {
		case "add":
			op.Add = got
		case "remove":
			op.Remove = got
		default:
			return SetOp{}, true, fmt.Sprintf("unknown set operation %q (want add or remove)", k)
		}
	}
	for _, a := range op.Add {
		if slices.Contains(op.Remove, a) {
			return SetOp{}, true, "a value is both added and removed"
		}
	}
	return op, true, ""
}

// checkSetValue judges a patch value of a multi-value field: a list of
// members, or a set operation. It returns why it is malformed, or "".
// (Whether a set operation is allowed here, and whether the result fits
// max, depend on the Item: the item service's to judge.)
func checkSetValue(f Field, v any) string {
	if _, isOp, reason := AsSetOp(f, v); isOp {
		return reason
	}
	_, reason := members(f, v)
	if reason != "" {
		return reason + " (or {add: [...], remove: [...]})"
	}
	return ""
}

// ValidSetValue reports whether v is a well-formed patch value of the
// multi-value field f: a list of members or a set operation.
func ValidSetValue(f Field, v any) bool { return checkSetValue(f, v) == "" }

// SetMembers returns the members of a stored or sent set: sorted in byte
// order, each once. v is a list that passed CheckFields.
func SetMembers(v any) []string {
	var out []string
	switch l := v.(type) {
	case []any:
		for _, m := range l {
			if s, ok := m.(string); ok {
				out = append(out, s)
			}
		}
	case []string:
		out = slices.Clone(l)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// StoredSet is a set as Item.fields holds it: a JSON array of strings.
func StoredSet(members []string) []any {
	out := make([]any, len(members))
	for i, m := range members {
		out[i] = m
	}
	return out
}

// checkMultiUses refuses the uses of a multi-value field that are not
// designed for sets: a hook reading or writing one. (`sets`, `at` and a
// link's resolve field are refused where they are checked.)
func (t *Type) checkMultiUses(r reporter) {
	for i, h := range t.Hooks {
		for _, list := range []struct {
			key string
			ids []string
		}{{"reads", h.Reads}, {"writes", h.Writes}} {
			for j, id := range list.ids {
				if f, ok := t.Field(id); ok && f.Multiple {
					r.fail(CodeWrongFieldType, at("hooks", i, list.key, j), "Give the hook a field holding one value",
						"hook %q reads or writes %s, a multi-value field; hooks take single values", h.ID, id)
				}
			}
		}
	}
}

// CheckFieldShapes checks, across the versions of each Type loaded
// together, that a field ID is single-valued in all of them or multiple in
// all of them: going from one value to several is a new field ID.
func CheckFieldShapes(types []*Type) error {
	var c crossCheck
	shape := map[string]map[string]bool{} // type ID -> field ID -> multiple
	for _, t := range types {
		if shape[t.ID] == nil {
			shape[t.ID] = map[string]bool{}
		}
		for i, f := range t.Fields {
			if multiple, seen := shape[t.ID][f.ID]; seen && multiple != f.Multiple {
				c.on(t).fail(CodeShapeChanged, at("fields", i, "multiple"), "Keep the field's shape, or give the new shape a new field ID",
					"field %s holds one value in one version and several in another; "+
						"a field that becomes multiple is a new field ID", f.ID)
				continue
			}
			shape[t.ID][f.ID] = f.Multiple
		}
	}
	return c.err()
}

// --- reactions that move or create ---

// eachRef finds a use of each in an expression.
var eachRef = regexp.MustCompile(`\beach\b`)

// childRef finds child.<field> in a `when`: the created Item's fields a
// created_inside condition reads.
var childRef = regexp.MustCompile(`\bchild\.([A-Za-z_][A-Za-z0-9_]*)`)

// newChildEnv is what a created_inside `when` may read: the created Item,
// as child. Its Type is not known when one Type loads (it is any of `of`),
// so its fields are dynamic here; ParseDir checks they are declared.
func newChildEnv() (*cel.Env, error) {
	return cel.NewEnv(cel.Variable("child", cel.MapType(cel.StringType, cel.DynType)))
}

// checkAfterReaction validates one move or create reaction within its Type
// and compiles its expressions.
func (t *Type) checkAfterReaction(rep reporter, p string, r *Reaction, where string) {
	if k := firstKey(keySet{"add", len(r.Add) > 0}, keySet{"undoes", r.Undoes != ""}); k != "" {
		rep.fail(CodeKeyNotApplicable, at(p, k), "Remove it", "%s: a %s reaction takes no add and no undoes", where, r.Kind)
		return
	}
	for j, from := range r.From {
		if _, ok := t.State(from); !ok {
			rep.fail(CodeUnknownState, at(p, "from", j), "Name a declared state", "%s: unknown from state %q", where, from)
			return
		}
	}
	// single checks that a link a reaction follows is declared, max: one.
	single := func(q, link string) bool {
		l, ok := t.Link(link)
		if !ok {
			rep.fail(CodeUnknownLink, q, "Name a declared link", "%s: link %q is not declared", where, link)
			return false
		}
		if !l.SourceSingle() {
			rep.fail(CodeConflict, q, "Follow a link with max: one", "%s: link %s may have many targets; a reaction follows a link of max: one", where, l.Name)
			return false
		}
		return true
	}
	// A condition over the Item's own fields, as a guard is.
	ownWhen := func() bool {
		if r.When == "" {
			return true
		}
		q := at(p, "when")
		env, err := newFieldEnv(t)
		if err != nil {
			rep.fail(CodeExpressionInvalid, q, "Fix the fields the message names", "%s: when: %v", where, err)
			return false
		}
		prg, _, out, err := compileFields(env, r.When)
		if err != nil {
			rep.failExpr(err, q, "%s: when %q: %v", where, r.When, err)
			return false
		}
		if !out.IsExactType(cel.BoolType) {
			rep.fail(CodeExpressionType, q, "Write a condition that is true or false", "%s: when %q must yield a bool, not %s", where, r.When, out)
			return false
		}
		r.WhenProgram = prg
		return true
	}

	// Jira 2.4b: for_each is a create reaction's, on_guard a move's.
	if r.ForEach != "" && r.Kind != ReactionCreate {
		rep.fail(CodeKeyNotApplicable, at(p, "for_each"), "Remove for_each, or make the reaction a create",
			"%s: for_each is for a create reaction, not a %s reaction", where, r.Kind)
		return
	}
	if r.OnGuard != "" {
		if r.Kind != ReactionMove {
			rep.fail(CodeKeyNotApplicable, at(p, "on_guard"), "Remove on_guard, or make the reaction a move",
				"%s: on_guard is for a move reaction, not a %s reaction", where, r.Kind)
			return
		}
		if r.OnGuard != OnGuardSkip {
			rep.fail(CodeInvalidValue, at(p, "on_guard"), "Write on_guard: skip, or leave it out", "%s: on_guard %q: the one value is %s", where, r.OnGuard, OnGuardSkip)
			return
		}
	}
	if r.Kind == ReactionSet {
		t.checkSetReaction(rep, p, r, where, ownWhen)
		return
	}
	if len(r.Set) > 0 {
		rep.fail(CodeKeyNotApplicable, at(p, "set"), "Remove set, or make the reaction a set", "%s: set is for a set reaction", where)
		return
	}
	if r.Target != "" && r.Kind != ReactionMove {
		rep.fail(CodeKeyNotApplicable, at(p, "target"), "Remove target, or make the reaction a move or a set",
			"%s: target is for a set reaction or a move reaction, not a %s reaction", where, r.Kind)
		return
	}
	if r.Target != "" {
		// A move at the container (Jira 2.4b, kernel change 5).
		switch {
		case r.Target != TargetContainer:
			rep.fail(CodeInvalidValue, at(p, "target"), "Write target: container", "%s: target %q: the only value is %s", where, r.Target, TargetContainer)
		case r.Link != "" || r.Children != nil:
			rep.fail(CodeKeyNotApplicable, at(p, firstKey(keySet{"link", r.Link != ""}, keySet{"children", r.Children != nil})),
				"Remove it: a move at the container needs no link", "%s: target: %s moves the container; it takes no link and no children", where, TargetContainer)
		case r.On != OnEnter:
			rep.fail(CodeInvalidValue, at(p, "on"), "Write on: enter", "%s: a move with target: %s is on: enter, not %q", where, TargetContainer, r.On)
		case len(r.Of) > 0 || r.By != "":
			rep.fail(CodeKeyNotApplicable, at(p, firstKey(keySet{"of", len(r.Of) > 0}, keySet{"by", r.By != ""})),
				"Remove it: of and by are for on: created_inside", "%s: of and by are for on: created_inside, not a move with target: %s", where, TargetContainer)
		case r.To == "":
			rep.fail(CodeRequired, at(p, "to"), "Name the state it moves into", "%s: a move reaction names the state it moves into (to)", where)
		default:
			if _, ok := t.State(r.State); !ok {
				rep.fail(CodeUnknownState, at(p, "state"), "Name the declared state whose entry triggers it",
					"%s: unknown state %q (on: enter needs the state entered)", where, r.State)
				return
			}
			ownWhen()
		}
		return
	}

	switch {
	case r.Kind == ReactionCreate:
		t.checkCreateReaction(rep, p, r, where, single, ownWhen)
		return
	case r.Type != "" || r.Version != 0 || len(r.Fields) > 0:
		rep.fail(CodeKeyNotApplicable, at(p, firstKey(keySet{"type", r.Type != ""}, keySet{"version", r.Version != 0}, keySet{"fields", len(r.Fields) > 0})),
			"Remove it, or make the reaction a create", "%s: type, version and fields are for a create reaction", where)
		return
	case r.To == "":
		rep.fail(CodeRequired, at(p, "to"), "Name the state it moves into", "%s: a move reaction names the state it moves into (to)", where)
		return
	}

	if r.Children != nil {
		if r.Link != "" {
			rep.fail(CodeConflict, at(p, "children"), "Follow a link or move contained Items, not both",
				"%s: children and link: a move follows a link or moves contained Items, not both", where)
			return
		}
		if !r.checkChildren(rep, at(p, "children"), where) {
			return
		}
	}

	switch r.On {
	case OnEnter:
		switch {
		case len(r.Of) > 0 || r.By != "":
			rep.fail(CodeKeyNotApplicable, at(p, firstKey(keySet{"of", len(r.Of) > 0}, keySet{"by", r.By != ""})),
				"Remove it, or react on: created_inside", "%s: of and by are for on: created_inside", where)
		case !t.hasState(r.State):
			rep.fail(CodeUnknownState, at(p, "state"), "Name the declared state whose entry triggers it",
				"%s: unknown state %q (on: enter needs the state entered)", where, r.State)
		case r.Children != nil:
			ownWhen()
		case r.Link == "":
			rep.fail(CodeRequired, at(p, "link"), "Name the link to the Item it moves, or move contained Items (children)",
				"%s: a move on entering a state follows a link to the Item it moves (link), or moves contained Items (children)", where)
		case single(at(p, "link"), r.Link):
			ownWhen()
		}
	case OnCreatedInside:
		t.checkCreatedInside(rep, p, r, where)
	default:
		rep.fail(CodeInvalidValue, at(p, "on"), "Write on: enter or on: created_inside", "%s: on must be enter or created_inside, not %q", where, r.On)
	}
}

// hasState reports whether the Type declares the state.
func (t *Type) hasState(id string) bool {
	_, ok := t.State(id)
	return ok
}

// checkCreateReaction checks and compiles a create reaction.
func (t *Type) checkCreateReaction(rep reporter, p string, r *Reaction, where string, single func(q, link string) bool, ownWhen func() bool) {
	if r.On != OnEnter {
		rep.fail(CodeInvalidValue, at(p, "on"), "Write on: enter", "%s: a create reaction is on: enter, not %q", where, r.On)
		return
	}
	if k := firstKey(keySet{"to", r.To != ""}, keySet{"by", r.By != ""}, keySet{"of", len(r.Of) > 0},
		keySet{"target_from", len(r.TargetFrom) > 0}, keySet{"children", r.Children != nil}); k != "" {
		rep.fail(CodeKeyNotApplicable, at(p, k), "Remove it, or make the reaction a move",
			"%s: to, by, of, target_from and children are not for a create reaction", where)
		return
	}
	if !t.hasState(r.State) {
		rep.fail(CodeUnknownState, at(p, "state"), "Name a declared state", "%s: unknown state %q", where, r.State)
		return
	}
	if r.Type == "" {
		rep.fail(CodeRequired, at(p, "type"), "Name the Type it creates", "%s: a create reaction names the type it creates", where)
		return
	}
	if r.Version < 1 {
		rep.fail(CodeRequired, at(p, "version"), "Name the version it creates, 1 or more", "%s: a create reaction names the version of %s it creates (version)", where, r.Type)
		return
	}
	if r.Link != "" && !single(at(p, "link"), r.Link) {
		return
	}
	if !ownWhen() {
		return
	}
	// for_each (Jira 2.4b, kernel change 3): a multi-value field of the
	// Item; one Item per member, which the fields read as each.
	var each *Field
	if r.ForEach != "" {
		f, ok := t.Field(r.ForEach)
		switch {
		case !ok:
			rep.fail(CodeUnknownField, at(p, "for_each"), "Name a declared multi-value field", "%s: for_each %q is not a declared field", where, r.ForEach)
			return
		case !f.Multiple:
			rep.fail(CodeWrongFieldType, at(p, "for_each"), "Name a multi-value field",
				"%s: for_each %s: a field holding one value; for_each takes a multi-value field", where, r.ForEach)
			return
		}
		each = &f
	}
	// The fields may read linked Items: one declared max-one link away
	// from the Item, or from its container; and the container's own fields
	// (container.<field>).
	env, err := newCreateEnv(t, each)
	if err != nil {
		rep.fail(CodeExpressionInvalid, at(p, "fields"), "Fix the fields the message names", "%s: fields: %v", where, err)
		return
	}
	r.FieldPrograms, r.fieldTypes, r.LinkedReads, r.ContainerReads = map[string]cel.Program{}, map[string]*cel.Type{}, nil, nil
	for _, id := range slices.Sorted(maps.Keys(r.Fields)) {
		q, expr := at(p, "fields", id), r.Fields[id]
		ast, iss := env.Compile(expr)
		if iss.Err() != nil {
			rep.fail(CodeExpressionInvalid, q, "Fix the CEL expression", "%s: field %s: %q: %v", where, id, expr, iss.Err())
			return
		}
		reads, containerFields, err := linkedReads(expr)
		if err != nil {
			rep.failExpr(err, q, "%s: field %s: %v", where, id, err)
			return
		}
		for _, c := range containerFields {
			if !slices.Contains(r.ContainerReads, c) {
				r.ContainerReads = append(r.ContainerReads, c)
			}
		}
		// A principal member travels by plain reference only.
		if each != nil && each.Type == Principal && eachRef.MatchString(expr) {
			if ref, ok := plainRef(expr); !ok || !ref.Each {
				rep.fail(CodeExpressionInvalid, q, "Copy it as it is: write each",
					"%s: field %s: %q: each is a principal: it is copied as it is (each), never computed", where, id, expr)
				return
			}
		}
		// An own field copied whole must not be one that holds who someone
		// is, unless copied as it is.
		if ref, ok := plainRef(expr); !ok || (!ref.Container && !ref.Each) {
			for _, f := range t.Fields {
				if f.Type == Principal && regexp.MustCompile(`\b`+f.ID+`\b`).MatchString(expr) && (!ok || ref.Field != f.ID) {
					rep.fail(CodeExpressionInvalid, q, "Copy the principal field as it is, by its id alone",
						"%s: field %s: %q reads %s, a principal field: it is copied as it is, never computed", where, id, expr, f.ID)
					return
				}
			}
		}
		for _, lr := range reads {
			if !lr.Container && !single(q, lr.Link) {
				return
			}
		}
		r.LinkedReads = append(r.LinkedReads, reads...)
		prg, err := env.Program(ast)
		if err != nil {
			rep.fail(CodeExpressionInvalid, q, "Fix the CEL expression", "%s: field %s: %v", where, id, err)
			return
		}
		r.FieldPrograms[id], r.fieldTypes[id] = prg, ast.OutputType()
	}
	r.LinkedReads = sortLinkedReads(r.LinkedReads)
	slices.Sort(r.ContainerReads)
}

// checkCreatedInside checks a move reaction on: created_inside.
func (t *Type) checkCreatedInside(rep reporter, p string, r *Reaction, where string) {
	switch {
	case len(r.Of) == 0:
		rep.fail(CodeRequired, at(p, "of"), "List the Types whose creation inside the Item triggers it",
			"%s: on: created_inside names the Types whose creation triggers it (of)", where)
		return
	case r.State != "" || r.Link != "":
		rep.fail(CodeKeyNotApplicable, at(p, firstKey(keySet{"state", r.State != ""}, keySet{"link", r.Link != ""})),
			"Remove it: the reaction moves the Item itself, or contained Items",
			"%s: on: created_inside takes no state and no link: it moves the Item itself, or contained Items (children)", where)
		return
	case len(r.TargetFrom) > 0 && r.Children == nil:
		rep.fail(CodeKeyNotApplicable, at(p, "target_from"), "Use from for the Item's own states",
			"%s: on: created_inside without children takes no target_from: it moves the Item itself, from `from`", where)
		return
	case r.By != "" && r.By != ByCreator && r.By != ByNotCreator:
		rep.fail(CodeInvalidValue, at(p, "by"), "Write by: creator or by: not_creator", "%s: by %q: want %s or %s", where, r.By, ByCreator, ByNotCreator)
		return
	case r.Children != nil:
		// The move is the children's: `to` and target_from are states of
		// their Types (ParseDir), `from` the Item's own.
		t.compileChildWhen(rep, p, r, where)
		return
	case !t.hasState(r.To):
		rep.fail(CodeUnknownState, at(p, "to"), "Name a declared state", "%s: unknown state %q to move to", where, r.To)
		return
	}
	// The moves it may make must be transitions of the Type, and none may
	// be held: a reaction opening a hold is not designed.
	froms := r.From
	if len(froms) == 0 {
		froms = t.transitionsInto(r.To)
		if len(froms) == 0 {
			rep.fail(CodeUnknownTransition, at(p, "to"), "Declare a transition into the state", "%s: no transition enters %s", where, r.To)
			return
		}
	}
	for _, from := range froms {
		if _, ok := t.Transition(from, r.To); !ok {
			rep.fail(CodeUnknownTransition, at(p, "to"), "Declare the transition, or move from other states",
				"%s: there is no transition %s -> %s", where, from, r.To)
			return
		}
		if h, ok := t.checkOn(from, r.To); ok {
			rep.fail(CodeHeldTransition, at(p, "to"), "Move into a state whose transitions are not held",
				"%s: transition %s -> %s is held by the check %q; a reaction may not ask for a held move", where, from, r.To, h)
			return
		}
		if given := t.givenOn(from, r.To); len(given) > 0 {
			rep.fail(CodeConflict, at(p, "to"), "Move into a state whose transitions ask nothing to be given, or let a person make the move",
				"%s: transition %s -> %s needs %s given in the move; a reaction gives no fields", where, from, r.To, strings.Join(given, ", "))
			return
		}
	}
	t.compileChildWhen(rep, p, r, where)
}

// compileChildWhen compiles a created_inside reaction's `when`, which reads
// the created Item's fields as child.<id>.
func (t *Type) compileChildWhen(rep reporter, p string, r *Reaction, where string) {
	if r.When == "" {
		return
	}
	q := at(p, "when")
	env, err := newChildEnv()
	if err != nil {
		rep.fail(CodeExpressionInvalid, q, "Fix the expression", "%s: when: %v", where, err)
		return
	}
	ast, iss := env.Compile(r.When)
	if iss.Err() != nil {
		rep.fail(CodeExpressionInvalid, q, "Fix the CEL expression; it reads the created Item as child.<id>",
			"%s: when %q: %v (it reads the created Item's fields, as child.<id>)", where, r.When, iss.Err())
		return
	}
	if out := ast.OutputType(); !out.IsExactType(cel.BoolType) && !out.IsExactType(cel.DynType) {
		rep.fail(CodeExpressionType, q, "Write a condition that is true or false", "%s: when %q must yield a bool, not %s", where, r.When, out)
		return
	}
	prg, err := env.Program(ast)
	if err != nil {
		rep.fail(CodeExpressionInvalid, q, "Fix the CEL expression", "%s: when: %v", where, err)
		return
	}
	r.WhenProgram = prg
	for _, m := range childRef.FindAllStringSubmatch(r.When, -1) {
		if !slices.Contains(r.ChildReads, m[1]) {
			r.ChildReads = append(r.ChildReads, m[1])
		}
	}
	sort.Strings(r.ChildReads)
}

// transitionsInto returns the states a Type declares a transition from
// into `to`.
func (t *Type) transitionsInto(to string) []string {
	var out []string
	for _, tr := range t.Transitions {
		if tr.To == to {
			out = append(out, tr.From)
		}
	}
	return out
}

// heldBy returns the check hook or held reaction that holds from -> to.
func (t *Type) heldBy(from, to string) (string, bool) {
	if h, ok := t.checkOn(from, to); ok {
		return h, true
	}
	for _, r := range t.Reactions {
		if r.Kind == ReactionHeld && r.State == to && slices.Contains(r.From, from) {
			return r.ID, true
		}
	}
	return "", false
}

// checkAfterAcross checks move and create reactions across the Types
// loaded together: a move's target Type consents and has the transition,
// which nothing holds; a created_inside names loaded Types and fields they
// declare; a create names a loaded Type version that consents, and sets
// fields it declares with values of their types.
func checkAfterAcross(c *crossCheck, types []*Type, byID map[string][]*Type) {
	for _, src := range types {
		rep := c.on(src)
		for i, r := range src.Reactions {
			checkAfterAcrossOne(rep, at("reactions", i), src, r, byID)
		}
	}
}

// checkAfterAcrossOne checks one move or create reaction against the Types
// it reaches; its first problem stops it.
func checkAfterAcrossOne(rep reporter, p string, src *Type, r Reaction, byID map[string][]*Type) error {
	key := ReactionKey(src.ID, r.ID)
	// triggers checks the Types whose Items trigger the reaction (of), and
	// the fields of theirs its when reads.
	triggers := func() error {
		for j, of := range r.Of {
			if len(byID[of]) == 0 {
				return rep.fail(CodeUnknownType, at(p, "of", j), "Load that Type too, or name a loaded one",
					"reaction %s is triggered by Items of %s, which is not loaded", r.ID, of)
			}
			// Some version must declare each field read. In a version that
			// does not (an older comment), it reads as null, like any unset
			// field.
			for _, f := range r.ChildReads {
				if !slices.ContainsFunc(byID[of], func(c *Type) bool { _, ok := c.Field(f); return ok }) {
					return rep.fail(CodeUnknownField, at(p, "when"), "Read a field some version of the Type declares",
						"reaction %s reads child.%s, which no version of %s declares", r.ID, f, of)
				}
			}
		}
		return nil
	}
	switch {
	case r.Kind == ReactionMove && r.Children != nil:
		// Contained Items: checkStep3Across. The trigger's own Types are
		// checked here.
		return triggers()
	case r.Kind == ReactionMove && r.On == OnEnter && r.Target == "":
		l, _ := src.Link(r.Link)
		for _, targetID := range l.Targets {
			if len(byID[targetID]) == 0 {
				return rep.fail(CodeUnknownType, at(p, "link"), "Load the link's target Types too",
					"reaction %s moves Items of %s, which is not loaded", r.ID, targetID)
			}
			for _, tgt := range byID[targetID] {
				if err := checkMoveInto(rep, p, r, tgt); err != nil {
					return err
				}
				if !tgt.AcceptsMove(key, r.To) {
					return rep.fail(CodeConsentMissing, p, "Add the consent to the target Type's accepts_reactions",
						"reaction %s moves Items of %s v%d into %s, which does not accept %s for it "+
							"(accepts_reactions: {%s: {moves: [%s]}})", r.ID, tgt.ID, tgt.Version, r.To, key, key, r.To)
				}
			}
		}
	case r.Kind == ReactionMove && r.On == OnCreatedInside:
		return triggers()
	case r.Kind == ReactionCreate:
		var created *Type
		for _, c := range byID[r.Type] {
			if c.Version == r.Version {
				created = c
			}
		}
		if created == nil {
			return rep.fail(CodeUnknownType, at(p, "version"), "Load that Type version too, or create a loaded one",
				"reaction %s creates %s v%d, which is not loaded", r.ID, r.Type, r.Version)
		}
		cat := fmt.Sprintf("%s v%d", created.ID, created.Version)
		// Created inside the Item itself, it reaches the new Item through
		// containment: a consent by reaction ID counts.
		consent, _ := created.Consent(src.ID, r.ID, r.Link == "")
		if !consent.MayCreate {
			return rep.fail(CodeConsentMissing, p, "Add the consent to the created Type's accepts_reactions",
				"reaction %s creates Items of %s, which does not accept %s for it "+
					"(accepts_reactions: {%s: {creates: [...]}})", r.ID, cat, key, key)
		}
		for _, id := range slices.Sorted(maps.Keys(r.Fields)) {
			q := at(p, "fields", id)
			f, ok := created.Field(id)
			if !ok {
				return rep.fail(CodeUnknownField, q, "Set a field the created Type declares",
					"reaction %s sets %s, which %s does not declare", r.ID, id, cat)
			}
			// A principal field (or a set) only by plain reference (Jira
			// 2.4b); its shape is judged by checkJira24bAcross.
			if _, err := checkCopy(f, r.Fields[id], src); err != nil {
				return rep.failExpr(err, q, "reaction %s sets %s of %s: %v", r.ID, id, cat, err)
			}
			// A value read from a linked Item is dynamic here: the write
			// judges it.
			if out := r.fieldTypes[id]; out != nil && !out.IsExactType(cel.DynType) && !fieldCELType(f).IsAssignableType(out) {
				return rep.fail(CodeExpressionType, q, "Compute a value of the created field's type",
					"reaction %s: field %s: %q yields %s, not a value of a %s field of %s", r.ID, id, r.Fields[id], out, f.Type, cat)
			}
			if !slices.Contains(consent.Creates, id) {
				return rep.fail(CodeConsentMissing, q, "List the field under creates in the created Type's consent",
					"reaction %s sets %s of %s, which its consent for %s does not list (creates)", r.ID, id, cat, key)
			}
		}
	}
	return nil
}

// checkMoveInto checks that a reaction may move Items of tgt into r.To:
// the state exists, transitions enter it from target_from (or any state),
// and none is held.
func checkMoveInto(rep reporter, p string, r Reaction, tgt *Type) error {
	tat := fmt.Sprintf("%s v%d", tgt.ID, tgt.Version)
	if _, ok := tgt.State(r.To); !ok {
		return rep.fail(CodeUnknownState, at(p, "to"), "Move into a state every target version declares",
			"reaction %s moves into %q, which is no state of %s", r.ID, r.To, tat)
	}
	froms := r.TargetFrom
	if len(froms) == 0 {
		if froms = tgt.transitionsInto(r.To); len(froms) == 0 {
			return rep.fail(CodeUnknownTransition, at(p, "to"), "Move into a state a transition of the target enters",
				"reaction %s moves into %s, which no transition of %s enters", r.ID, r.To, tat)
		}
	}
	for _, from := range froms {
		if _, ok := tgt.Transition(from, r.To); !ok {
			return rep.fail(CodeUnknownTransition, at(p, "target_from"), "Name states the target has a transition from",
				"reaction %s: %s has no transition %s -> %s", r.ID, tat, from, r.To)
		}
		if h, held := tgt.heldBy(from, r.To); held {
			return rep.fail(CodeHeldTransition, at(p, "to"), "Move into a state whose transitions are not held",
				"reaction %s: %s holds %s -> %s by %s; a reaction may not ask for a held move", r.ID, tat, from, r.To, h)
		}
		if given := tgt.givenOn(from, r.To); len(given) > 0 {
			return rep.fail(CodeConflict, at(p, "to"), "Move into a state whose transitions ask nothing to be given, or let a person make the move",
				"reaction %s: %s's transition %s -> %s needs %s given in the move; a reaction gives no fields",
				r.ID, tat, from, r.To, strings.Join(given, ", "))
		}
	}
	return nil
}

// checkReactionCycles refuses a cycle among move and create reactions that
// can be seen in the declarations, whatever their conditions. Nodes are
// "Items of T enter s" and "an Item of K is created inside one of C":
//
//	T enters s --move (link to U)--> U enters `to`
//	T enters s --create K--------> K created inside T
//	K created inside C --move----> C enters `to`
//
// MaxHop stops a chain at run time; a visible loop is refused at load.
func checkReactionCycles(c *crossCheck, types []*Type) {
	enters := func(t, s string) string { return t + " enters " + s }
	inside := func(k, c string) string { return "a " + k + " is created inside a " + c }
	edges := map[string][]string{}
	// by is the reaction behind each edge, where a cycle is reported.
	type reactionAt struct {
		t *Type
		i int
	}
	by := map[[2]string]reactionAt{}
	var cause *reactionAt // the reaction of the edges being added
	add := func(from, to string) {
		if !slices.Contains(edges[from], to) {
			edges[from] = append(edges[from], to)
			if cause != nil {
				by[[2]string{from, to}] = *cause
			}
		}
	}
	// Creation enters the initial state: "a T is created" runs the enter
	// reactions of that state that name no `from`.
	created := func(t string) string { return "a " + t + " is created" }
	for _, t := range types {
		for i, r := range t.Reactions {
			cause = &reactionAt{t, i}
			// from is the trigger's node(s); to what the reaction causes.
			var from, to []string
			switch {
			case r.Kind == ReactionMove && r.On == OnCreatedInside:
				for _, of := range r.Of {
					from = append(from, inside(of, t.ID))
				}
			case r.Kind == ReactionMove || r.Kind == ReactionCreate:
				from = append(from, enters(t.ID, r.State))
				if r.State == t.InitialState && len(r.From) == 0 {
					from = append(from, created(t.ID))
				}
			}
			switch {
			case r.Kind == ReactionCreate:
				to = append(to, inside(r.Type, t.ID))
				cause = nil // creation enters the initial state: no reaction's edge
				add(inside(r.Type, t.ID), created(r.Type))
				cause = &reactionAt{t, i}
			case r.Kind == ReactionMove && r.Children != nil:
				for _, of := range r.Children.Of {
					to = append(to, enters(of, r.To))
				}
			case r.Kind == ReactionMove && r.On == OnCreatedInside:
				to = append(to, enters(t.ID, r.To))
			case r.Kind == ReactionMove && r.Target == TargetContainer:
				// Its container: any loaded Type that accepts the move.
				for _, c := range types {
					if a, _ := c.Consent(t.ID, r.ID, true); slices.Contains(a.Moves, r.To) {
						to = append(to, enters(c.ID, r.To))
					}
				}
			case r.Kind == ReactionMove:
				l, _ := t.Link(r.Link)
				for _, target := range l.Targets {
					to = append(to, enters(target, r.To))
				}
			}
			for _, f := range from {
				for _, n := range to {
					add(f, n)
				}
			}
		}
	}
	// A depth-first walk: a node met again while it is still on the path
	// closes a cycle.
	const (
		onPath = 1
		done   = 2
	)
	mark := map[string]int{}
	var path []string
	var walk func(n string) error
	walk = func(n string) error {
		switch mark[n] {
		case done:
			return nil
		case onPath:
			i := slices.Index(path, n)
			cycle := append(slices.Clone(path[i:]), n)
			// Reported on the first reaction of the cycle as it is spelled.
			for k := 0; k+1 < len(cycle); k++ {
				if ra, ok := by[[2]string{cycle[k], cycle[k+1]}]; ok {
					c.on(ra.t).fail(CodeReactionCycle, at("reactions", ra.i), "Break the cycle: narrow a reaction's state, link or consent",
						"reactions form a cycle: %s", strings.Join(cycle, " -> "))
					break
				}
			}
			return errReported
		}
		mark[n] = onPath
		path = append(path, n)
		for _, next := range edges[n] {
			if err := walk(next); err != nil {
				return err
			}
		}
		path = path[:len(path)-1]
		mark[n] = done
		return nil
	}
	for _, n := range slices.Sorted(maps.Keys(edges)) {
		if walk(n) != nil {
			return // one cycle at a time: the next shows once it is broken
		}
	}
}
