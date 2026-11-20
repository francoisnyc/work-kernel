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
}

func (a *Accepted) fromDoc(d acceptedDoc) {
	a.Fields, a.Moves = d.Fields, d.Moves
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
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("accepts_reactions: want a list of fields, or a map with fields, moves and creates")
	}
	// Unknown keys are errors, as everywhere in a Type file.
	for i := 0; i < len(n.Content); i += 2 {
		if k := n.Content[i].Value; k != "fields" && k != "moves" && k != "creates" {
			return fmt.Errorf("accepts_reactions: unknown key %q (want fields, moves or creates)", k)
		}
	}
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
	if len(a.Moves) == 0 && !a.MayCreate {
		return json.Marshal(a.Fields)
	}
	d := acceptedDoc{Fields: a.Fields, Moves: a.Moves}
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
func (t *Type) checkMultiUses(fail func(string, ...any) error) error {
	for _, h := range t.Hooks {
		for _, id := range slices.Concat(h.Reads, h.Writes) {
			if f, ok := t.Field(id); ok && f.Multiple {
				return fail("hook %q reads or writes %s, a multi-value field; hooks take single values", h.ID, id)
			}
		}
	}
	return nil
}

// CheckFieldShapes checks, across the versions of each Type loaded
// together, that a field ID is single-valued in all of them or multiple in
// all of them: going from one value to several is a new field ID.
func CheckFieldShapes(types []*Type) error {
	shape := map[string]map[string]bool{} // type ID -> field ID -> multiple
	for _, t := range types {
		if shape[t.ID] == nil {
			shape[t.ID] = map[string]bool{}
		}
		for _, f := range t.Fields {
			if multiple, seen := shape[t.ID][f.ID]; seen && multiple != f.Multiple {
				return fmt.Errorf("typedef %s: field %s holds one value in one version and several in another; "+
					"a field that becomes multiple is a new field ID", t.ID, f.ID)
			}
			shape[t.ID][f.ID] = f.Multiple
		}
	}
	return nil
}

// --- reactions that move or create ---

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
func (t *Type) checkAfterReaction(r *Reaction, where string, fail func(string, ...any) error) error {
	if len(r.Add) > 0 || r.Undoes != "" {
		return fail("%s: a %s reaction takes no add and no undoes", where, r.Kind)
	}
	for _, from := range r.From {
		if _, ok := t.State(from); !ok {
			return fail("%s: unknown from state %q", where, from)
		}
	}
	single := func(link string) error {
		l, ok := t.Link(link)
		if !ok {
			return fail("%s: link %q is not declared", where, link)
		}
		if !l.SourceSingle() {
			return fail("%s: link %s may have many targets; a reaction follows a link of max: one", where, l.Name)
		}
		return nil
	}
	// A condition over the Item's own fields, as a guard is.
	ownWhen := func() error {
		if r.When == "" {
			return nil
		}
		env, err := newFieldEnv(t)
		if err != nil {
			return fail("%s: when: %v", where, err)
		}
		prg, _, out, err := compileFields(env, r.When)
		if err != nil {
			return fail("%s: when %q: %v", where, r.When, err)
		}
		if !out.IsExactType(cel.BoolType) {
			return fail("%s: when %q must yield a bool, not %s", where, r.When, out)
		}
		r.WhenProgram = prg
		return nil
	}

	switch {
	case r.Kind == ReactionCreate:
		if r.On != OnEnter {
			return fail("%s: a create reaction is on: enter, not %q", where, r.On)
		}
		if r.To != "" || r.By != "" || len(r.Of) > 0 || len(r.TargetFrom) > 0 {
			return fail("%s: to, by, of and target_from are not for a create reaction", where)
		}
		if _, ok := t.State(r.State); !ok {
			return fail("%s: unknown state %q", where, r.State)
		}
		if r.Type == "" {
			return fail("%s: a create reaction names the type it creates", where)
		}
		if r.Version < 1 {
			return fail("%s: a create reaction names the version of %s it creates (version)", where, r.Type)
		}
		if r.Link != "" {
			if err := single(r.Link); err != nil {
				return err
			}
		}
		if err := ownWhen(); err != nil {
			return err
		}
		env, err := newSetsEnv(t)
		if err != nil {
			return fail("%s: fields: %v", where, err)
		}
		r.FieldPrograms, r.fieldTypes = map[string]cel.Program{}, map[string]*cel.Type{}
		for _, id := range slices.Sorted(maps.Keys(r.Fields)) {
			ast, iss := env.Compile(r.Fields[id])
			if iss.Err() != nil {
				return fail("%s: field %s: %q: %v", where, id, r.Fields[id], iss.Err())
			}
			prg, err := env.Program(ast)
			if err != nil {
				return fail("%s: field %s: %v", where, id, err)
			}
			r.FieldPrograms[id], r.fieldTypes[id] = prg, ast.OutputType()
		}
		return nil

	case r.Type != "" || r.Version != 0 || len(r.Fields) > 0:
		return fail("%s: type, version and fields are for a create reaction", where)
	case r.To == "":
		return fail("%s: a move reaction names the state it moves into (to)", where)
	}

	switch r.On {
	case OnEnter:
		if len(r.Of) > 0 || r.By != "" {
			return fail("%s: of and by are for on: created_inside", where)
		}
		if _, ok := t.State(r.State); !ok {
			return fail("%s: unknown state %q (on: enter needs the state entered)", where, r.State)
		}
		if r.Link == "" {
			return fail("%s: a move on entering a state follows a link to the Item it moves (link)", where)
		}
		if err := single(r.Link); err != nil {
			return err
		}
		return ownWhen()

	case OnCreatedInside:
		if len(r.Of) == 0 {
			return fail("%s: on: created_inside names the Types whose creation triggers it (of)", where)
		}
		if r.State != "" || r.Link != "" || len(r.TargetFrom) > 0 {
			return fail("%s: on: created_inside takes no state, no link and no target_from: it moves the Item itself, from `from`", where)
		}
		if r.By != "" && r.By != ByCreator {
			return fail("%s: by %q: the only value is %s", where, r.By, ByCreator)
		}
		if _, ok := t.State(r.To); !ok {
			return fail("%s: unknown state %q to move to", where, r.To)
		}
		// The moves it may make must be transitions of the Type, and none
		// may be held: a reaction opening a hold is not designed.
		froms := r.From
		if len(froms) == 0 {
			for _, tr := range t.Transitions {
				if tr.To == r.To {
					froms = append(froms, tr.From)
				}
			}
			if len(froms) == 0 {
				return fail("%s: no transition enters %s", where, r.To)
			}
		}
		for _, from := range froms {
			if _, ok := t.Transition(from, r.To); !ok {
				return fail("%s: there is no transition %s -> %s", where, from, r.To)
			}
			if h, ok := t.checkOn(from, r.To); ok {
				return fail("%s: transition %s -> %s is held by the check %q; a reaction may not ask for a held move", where, from, r.To, h)
			}
		}
		if r.When != "" {
			env, err := newChildEnv()
			if err != nil {
				return fail("%s: when: %v", where, err)
			}
			ast, iss := env.Compile(r.When)
			if iss.Err() != nil {
				return fail("%s: when %q: %v (it reads the created Item's fields, as child.<id>)", where, r.When, iss.Err())
			}
			if out := ast.OutputType(); !out.IsExactType(cel.BoolType) && !out.IsExactType(cel.DynType) {
				return fail("%s: when %q must yield a bool, not %s", where, r.When, out)
			}
			prg, err := env.Program(ast)
			if err != nil {
				return fail("%s: when: %v", where, err)
			}
			r.WhenProgram = prg
			for _, m := range childRef.FindAllStringSubmatch(r.When, -1) {
				if !slices.Contains(r.ChildReads, m[1]) {
					r.ChildReads = append(r.ChildReads, m[1])
				}
			}
			sort.Strings(r.ChildReads)
		}
		return nil
	}
	return fail("%s: on must be enter or created_inside, not %q", where, r.On)
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
func checkAfterAcross(types []*Type, byID map[string][]*Type) error {
	for _, src := range types {
		fail := func(format string, args ...any) error {
			return fmt.Errorf("typedef %s v%d: %s", src.ID, src.Version, fmt.Sprintf(format, args...))
		}
		for _, r := range src.Reactions {
			key := ReactionKey(src.ID, r.ID)
			switch {
			case r.Kind == ReactionMove && r.On == OnEnter:
				l, _ := src.Link(r.Link)
				for _, targetID := range l.Targets {
					if len(byID[targetID]) == 0 {
						return fail("reaction %s moves Items of %s, which is not loaded", r.ID, targetID)
					}
					for _, tgt := range byID[targetID] {
						at := fmt.Sprintf("%s v%d", tgt.ID, tgt.Version)
						if _, ok := tgt.State(r.To); !ok {
							return fail("reaction %s moves into %q, which is no state of %s", r.ID, r.To, at)
						}
						froms := r.TargetFrom
						if len(froms) == 0 {
							froms = tgt.transitionsInto(r.To)
							if len(froms) == 0 {
								return fail("reaction %s moves into %s, which no transition of %s enters", r.ID, r.To, at)
							}
						}
						for _, from := range froms {
							if _, ok := tgt.Transition(from, r.To); !ok {
								return fail("reaction %s: %s has no transition %s -> %s", r.ID, at, from, r.To)
							}
							if h, held := tgt.heldBy(from, r.To); held {
								return fail("reaction %s: %s holds %s -> %s by %s; a reaction may not ask for a held move", r.ID, at, from, r.To, h)
							}
						}
						if !tgt.AcceptsMove(key, r.To) {
							return fail("reaction %s moves Items of %s into %s, which does not accept %s for it "+
								"(accepts_reactions: {%s: {moves: [%s]}})", r.ID, at, r.To, key, key, r.To)
						}
					}
				}
			case r.Kind == ReactionMove && r.On == OnCreatedInside:
				for _, of := range r.Of {
					if len(byID[of]) == 0 {
						return fail("reaction %s is triggered by Items of %s, which is not loaded", r.ID, of)
					}
					// Some version must declare each field read. In a
					// version that does not (an older comment), it reads
					// as null, like any unset field.
					for _, f := range r.ChildReads {
						if !slices.ContainsFunc(byID[of], func(c *Type) bool { _, ok := c.Field(f); return ok }) {
							return fail("reaction %s reads child.%s, which no version of %s declares", r.ID, f, of)
						}
					}
				}
			case r.Kind == ReactionCreate:
				var created *Type
				for _, c := range byID[r.Type] {
					if c.Version == r.Version {
						created = c
					}
				}
				if created == nil {
					return fail("reaction %s creates %s v%d, which is not loaded", r.ID, r.Type, r.Version)
				}
				at := fmt.Sprintf("%s v%d", created.ID, created.Version)
				fields := slices.Sorted(maps.Keys(r.Fields))
				if !created.AcceptsReactions[key].MayCreate {
					return fail("reaction %s creates Items of %s, which does not accept %s for it "+
						"(accepts_reactions: {%s: {creates: [...]}})", r.ID, at, key, key)
				}
				for _, id := range fields {
					f, ok := created.Field(id)
					if !ok {
						return fail("reaction %s sets %s, which %s does not declare", r.ID, id, at)
					}
					if f.Multiple || f.Type == Principal {
						return fail("reaction %s sets %s of %s, a %s field a reaction may not set", r.ID, id, at, f.Type)
					}
					if out := r.fieldTypes[id]; out != nil && !celType(f.Type).IsAssignableType(out) {
						return fail("reaction %s: field %s: %q yields %s, not a value of a %s field of %s",
							r.ID, id, r.Fields[id], out, f.Type, at)
					}
					if !created.AcceptsCreate(key, []string{id}) {
						return fail("reaction %s sets %s of %s, which its consent for %s does not list (creates)", r.ID, id, at, key)
					}
				}
			}
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
func checkReactionCycles(types []*Type) error {
	enters := func(t, s string) string { return t + " enters " + s }
	inside := func(k, c string) string { return "a " + k + " is created inside a " + c }
	edges := map[string][]string{}
	add := func(from, to string) {
		if !slices.Contains(edges[from], to) {
			edges[from] = append(edges[from], to)
		}
	}
	for _, t := range types {
		for _, r := range t.Reactions {
			switch {
			case r.Kind == ReactionMove && r.On == OnEnter:
				l, _ := t.Link(r.Link)
				for _, target := range l.Targets {
					add(enters(t.ID, r.State), enters(target, r.To))
				}
			case r.Kind == ReactionMove && r.On == OnCreatedInside:
				for _, of := range r.Of {
					add(inside(of, t.ID), enters(t.ID, r.To))
				}
			case r.Kind == ReactionCreate:
				add(enters(t.ID, r.State), inside(r.Type, t.ID))
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
			return fmt.Errorf("typedef: reactions form a cycle: %s -> %s", strings.Join(path[i:], " -> "), n)
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
		if err := walk(n); err != nil {
			return err
		}
	}
	return nil
}
