package typedef

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"cel.dev/cel-go/cel"
	celast "cel.dev/cel-go/common/ast"
	"cel.dev/cel-go/common/operators"
)

// Jira 2.4b, PR 1: the building blocks (docs/design.md, "Jira 2.4b").
// Nothing here is named after a request or an approval.
//
//  1. `sets` and create reactions read container.<field>: a field of the
//     Item's container that the container's Type lists in
//     readable_by_reactions (any other reads as unset).
//  2. A principal field (or any multi-value field) is set only by a plain
//     reference to another field of its shape: container.<field>, one of
//     the Item's own fields, or `each`. Never computed.
//  3. for_each: a create reaction makes one Item per member of a
//     multi-value field of the source, which its fields read as `each`.
//  4. Guards read the container (container.state, container.category) and
//     the contained Items of a Type (children.<type>, a list of
//     {state, category}). No visibility filter: a guard judges the data.
//  5. A move reaction with target: container moves the Item's container.
//  6. on_guard: skip records a target guard that does not hold as a skip.
//  7. except: [container_creator].
//  9. unique_among_siblings on a principal field.
//  10. placement_writable_by: who may move or narrow Items of the Type.
//  11. A transition's `sets` may write a field its own `allowed` names, by
//     plain reference (a hand-over).
//  12. allowed: [nobody].

// The two keys a guard reads of another Item: the container, a child or a
// linked Item.
const (
	GuardKeyState    = "state"
	GuardKeyCategory = "category"
)

// ContainerVar and ChildrenVar are the CEL names a guard reads the
// container and the contained Items by; ContainerVar is also the name a
// `sets` expression or a create reaction reads the container's fields by.
const (
	ContainerVar = "container"
	ChildrenVar  = "children"
)

// PlacementKernel is the placement_writable_by entry no person passes: only
// the kernel places the Items (they stay where they were created).
const PlacementKernel = "kernel"

// childItem is the CEL type of a contained Item as a guard sees it: a map
// with only state and category. (Dyn values: a type of its own, apart from
// a linked Item's, so that the check below knows a child when it sees one.
// A linked Item's other keys are refused when the guard runs, as before.)
var childItem = cel.MapType(cel.StringType, cel.DynType)

// guardChildren is the CEL type of `children`: Type ID -> the contained
// Items of that Type.
var guardChildren = cel.MapType(cel.StringType, cel.ListType(childItem))

// checkGuardReads walks a compiled guard: what it reads of the container
// (container.state or .category only), the Types of children it reads
// (children.<type>), and that it reads nothing of a child but its state and
// category. It reports whether the guard reads the container.
func checkGuardReads(a *celast.AST) (container bool, children []string, err error) {
	isGuardItem := func(e celast.Expr) bool {
		t := a.GetType(e.ID())
		return t != nil && t.IsExactType(childItem)
	}
	other := func(field string) bool { return field != GuardKeyState && field != GuardKeyCategory }
	var (
		selectedChildren int
		childrenIdents   int
	)
	celast.PreOrderVisit(a.Expr(), celast.NewExprVisitor(func(e celast.Expr) {
		if err != nil {
			return
		}
		switch e.Kind() {
		case celast.IdentKind:
			switch e.AsIdent() {
			case ContainerVar:
				container = true
			case ChildrenVar:
				childrenIdents++
			}
		case celast.SelectKind:
			s := e.AsSelect()
			op := s.Operand()
			switch {
			case op.Kind() == celast.IdentKind && op.AsIdent() == ContainerVar:
				if other(s.FieldName()) {
					err = fmt.Errorf("it reads container.%s; a guard reads the container's state and category only", s.FieldName())
				}
			case op.Kind() == celast.IdentKind && op.AsIdent() == ChildrenVar:
				selectedChildren++
				if !slices.Contains(children, s.FieldName()) {
					children = append(children, s.FieldName())
				}
			case isGuardItem(op) && other(s.FieldName()):
				err = fmt.Errorf("it reads %s of a contained Item; a guard reads its state and category only", s.FieldName())
			}
		case celast.CallKind:
			// An index (x["f"]) on another Item, or on what holds them:
			// read them with a dot, state and category only.
			c := e.AsCall()
			if c.FunctionName() != operators.Index || len(c.Args()) == 0 {
				return
			}
			arg := c.Args()[0]
			if isGuardItem(arg) || arg.Kind() == celast.IdentKind && (arg.AsIdent() == ContainerVar || arg.AsIdent() == ChildrenVar) {
				err = fmt.Errorf("it indexes another Item, or the container's children; read container.state, children.<type> and an Item's state or category")
			}
		}
	}))
	if err == nil && childrenIdents != selectedChildren {
		err = fmt.Errorf("children is read as children.<type> only")
	}
	slices.Sort(children)
	return container, children, err
}

// containerFieldRef finds container.<name>, once container.linked reads
// are taken out.
var containerFieldRef = regexp.MustCompile(`\bcontainer\.([A-Za-z_][A-Za-z0-9_]*)`)

// takeContainerFields takes the container.<field> reads out of expr: the
// field names, and expr with each replaced. container.state and the like
// are a guard's, not an effect's.
func takeContainerFields(expr string) (string, []string, error) {
	var fields []string
	for _, m := range containerFieldRef.FindAllStringSubmatchIndex(expr, -1) {
		name := expr[m[2]:m[3]]
		if rest := expr[m[1]:]; strings.HasPrefix(rest, ".") || strings.HasPrefix(rest, "[") || strings.HasPrefix(rest, "(") {
			return "", nil, exprError{CodeReadNotAllowed, fmt.Errorf("%s is no read of a container's field (container.<field>)", expr[m[0]:m[1]]+rest)}
		}
		if name == GuardKeyState || name == GuardKeyCategory || name == "linked" {
			return "", nil, exprError{CodeReadNotAllowed, fmt.Errorf("container.%s: an effect reads the container's fields (container.<field>); "+
				"its state and category are a guard's", name)}
		}
		if !slices.Contains(fields, name) {
			fields = append(fields, name)
		}
	}
	slices.Sort(fields)
	return containerFieldRef.ReplaceAllString(expr, "x"), fields, nil
}

// identRe is a bare CEL identifier: a field ID, or each.
var identRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// PlainRef is what a plain reference names: exactly one field of the
// container (container.<field>), of the Item itself, or `each`.
type PlainRef struct {
	Container bool   // container.<Field>
	Each      bool   // each
	Linked    bool   // one linked read (linked.<link>.<field>, container.linked...), judged by checkStep3Across
	Field     string // the field named (empty for each and a linked read)
}

// plainRef reports whether expr is exactly one plain reference.
func plainRef(expr string) (PlainRef, bool) {
	e := strings.TrimSpace(expr)
	if e == EachVar {
		return PlainRef{Each: true}, true
	}
	if soleLinkedRead(e) {
		return PlainRef{Linked: true}, true
	}
	if name, ok := strings.CutPrefix(e, ContainerVar+"."); ok && identRe.MatchString(name) &&
		name != GuardKeyState && name != GuardKeyCategory && name != "linked" {
		return PlainRef{Container: true, Field: name}, true
	}
	if identRe.MatchString(e) && e != ContainerVar && e != ChildrenVar && e != "null" && e != "true" && e != "false" {
		return PlainRef{Field: e}, true
	}
	return PlainRef{}, false
}

// checkCopy judges a value set on field f (of this Type, or of the Type a
// create reaction makes) by expr: a principal field, or a field holding a
// set, takes only a plain reference. An own field it names must have f's
// shape; a container's field is judged across Types. own is the Type whose
// fields the expression reads (nil: none, as for a created Item's own).
func checkCopy(f Field, expr string, own *Type) (PlainRef, error) {
	if f.Type != Principal && !f.Multiple {
		return PlainRef{}, nil
	}
	what := "a principal field"
	if f.Type != Principal {
		what = "a multi-value field"
	}
	ref, ok := plainRef(expr)
	if !ok {
		return ref, exprError{CodeExpressionInvalid, fmt.Errorf("%s is %s: it is set by a plain reference only (container.<field>, a field, or each), not %q",
			f.ID, what, expr)}
	}
	if ref.Container || ref.Each || ref.Linked {
		return ref, nil
	}
	src, declared := own.Field(ref.Field)
	if !declared {
		return ref, exprError{CodeUnknownField, fmt.Errorf("%s: %q is not a declared field", f.ID, ref.Field)}
	}
	if src.Multiple != f.Multiple || !sameStoredType(src, f) {
		return ref, exprError{CodeWrongFieldType, fmt.Errorf("%s is %s: %s is not a field of its shape", f.ID, what, ref.Field)}
	}
	return ref, nil
}

// newTransitionSetsEnv is the `sets` environment of a transition: the
// Item's fields, cause.at, today, and its container's fields
// (container.<field>; dynamic here, ParseDir checks them).
func newTransitionSetsEnv(t *Type) (*cel.Env, error) {
	env, err := newSetsEnv(t)
	if err != nil {
		return nil, err
	}
	return env.Extend(cel.Variable(ContainerVar, cel.DynType))
}

// memberCELType is the CEL type of one member of a multi-value field.
func memberCELType(f Field) *cel.Type {
	if f.Type == Date {
		return cel.TimestampType
	}
	return cel.StringType
}

// checkJira24b validates what a Type declares of 2.4b that check does not
// reach elsewhere: unique_among_siblings and placement_writable_by.
func (t *Type) checkJira24b(r reporter) {
	for i, f := range t.Fields {
		if f.UniqueAmongSiblings == nil {
			continue
		}
		p := at("fields", i, "unique_among_siblings")
		switch {
		case len(f.UniqueAmongSiblings) == 0:
			r.fail(CodeInvalidValue, p, "Name the states, or leave unique_among_siblings out",
				"field %s: unique_among_siblings names the states in which its value is unique; leave it out for none", f.ID)
			continue
		case f.Type != Principal || f.Multiple:
			r.fail(CodeKeyNotApplicable, p, "Remove it, or use it on a single principal field",
				"field %s: unique_among_siblings is for a single principal field, not a %s field", f.ID, f.Type)
		}
		seen := map[string]bool{}
		for j, s := range f.UniqueAmongSiblings {
			if _, ok := t.State(s); !ok {
				r.fail(CodeUnknownState, at(p, j), "Name a declared state",
					"field %s: unique_among_siblings names %q, which is not a declared state", f.ID, s)
			} else if seen[s] {
				r.fail(CodeDuplicate, at(p, j), "Remove the second one", "field %s: unique_among_siblings names %s twice", f.ID, s)
			}
			seen[s] = true
		}
	}
	if t.PlacementWritableBy == nil {
		return
	}
	pw := t.PlacementWritableBy
	if len(pw) == 0 {
		r.fail(CodeInvalidValue, "placement_writable_by", "Name kernel or roles, or leave placement_writable_by out",
			"placement_writable_by is an empty list; leave it out for anyone who sees the Item")
		return
	}
	seen := map[string]bool{}
	for j, e := range pw {
		p := at("placement_writable_by", j)
		if seen[e] {
			r.fail(CodeDuplicate, p, "Remove the second one", "placement_writable_by names an entry twice")
			continue
		}
		seen[e] = true
		if e == PlacementKernel {
			if len(pw) > 1 {
				r.fail(CodeConflict, p, "Write kernel alone, or name roles without it",
					"placement_writable_by: %s stands alone (no person places the Items)", PlacementKernel)
			}
			continue
		}
		name, ok := strings.CutPrefix(e, AllowedRolePrefix)
		if !ok {
			r.fail(CodeInvalidValue, p, "Write kernel or role:<name>", "placement_writable_by entry %q: want %s or role:<name>", e, PlacementKernel)
		} else if !roleNameRe.MatchString(name) {
			r.fail(CodeInvalidName, p, "Name the role in lower case, digits, _ and -", "placement_writable_by entry %q: not a role name", e)
		}
	}
}

// PlacementRoles returns the roles that may place Items of t, and whether
// the Type limits placement at all. kernel limits it to no one.
func (t *Type) PlacementRoles() (roles []string, limited bool) {
	if len(t.PlacementWritableBy) == 0 {
		return nil, false
	}
	for _, e := range t.PlacementWritableBy {
		if name, ok := strings.CutPrefix(e, AllowedRolePrefix); ok {
			roles = append(roles, name)
		}
	}
	return roles, true
}

// UniqueFields returns the fields of t whose value is unique among
// siblings, in the order declared.
func (t *Type) UniqueFields() []Field {
	var out []Field
	for _, f := range t.Fields {
		if len(f.UniqueAmongSiblings) > 0 {
			out = append(out, f)
		}
	}
	return out
}

// --- across Types ---

// checkJira24bAcross checks, across the Types loaded together, what 2.4b
// added: container reads, children read by guards, for_each copies, and
// move reactions at the container.
func checkJira24bAcross(c *crossCheck, types []*Type, byID map[string][]*Type) {
	// readable returns the loaded fields named id that reactions may read.
	readable := func(id string) []Field {
		var out []Field
		for _, c := range types {
			if f, ok := c.Field(id); ok && c.ReadableByReaction(id) {
				out = append(out, f)
			}
		}
		return out
	}
	principal := func(fs []Field) bool {
		return slices.ContainsFunc(fs, func(f Field) bool { return f.Type == Principal })
	}
	// copyInto judges a plain container read into field f.
	copyInto := func(f Field, from string) error {
		for _, rf := range readable(from) {
			if rf.Multiple != f.Multiple || !sameStoredType(rf, f) {
				return exprError{CodeWrongFieldType, fmt.Errorf("%s is set from container.%s, which is not a value of its shape somewhere it is readable", f.ID, from)}
			}
		}
		return nil
	}
	// containerReads judges the container fields an expression reads, and
	// that a principal one is read only by a plain reference into f.
	containerReads := func(where string, reads []string, expr string, f *Field) error {
		for _, x := range reads {
			fs := readable(x)
			if len(fs) == 0 {
				return exprError{CodeReadNotAllowed, fmt.Errorf("%s reads container.%s, but no loaded Type lets reactions read %s (readable_by_reactions)", where, x, x)}
			}
			if principal(fs) {
				ref, ok := plainRef(expr)
				if !ok || !ref.Container || f == nil || f.Type != Principal {
					return exprError{CodeExpressionInvalid, fmt.Errorf("%s reads container.%s, a principal field: it is copied by plain reference into a principal field only", where, x)}
				}
			}
		}
		if ref, ok := plainRef(expr); ok && ref.Container && f != nil {
			return copyInto(*f, ref.Field)
		}
		return nil
	}

	for _, src := range types {
		rep := c.on(src)
		for i, tr := range src.Transitions {
			p, name := at("transitions", i), tr.From+" -> "+tr.To
			for _, ty := range tr.GuardChildren {
				if len(byID[ty]) == 0 {
					rep.fail(CodeUnknownType, at(p, "guard"), "Load that Type too, or read the children of a loaded one",
						"transition %s: its guard reads children.%s, and %s is not loaded", name, ty, ty)
				}
			}
			for _, id := range slices.Sorted(maps.Keys(tr.Sets)) {
				_, reads, _ := takeContainerFields(tr.Sets[id])
				f, _ := src.Field(id)
				if err := containerReads(fmt.Sprintf("transition %s: sets %s", name, id), reads, tr.Sets[id], &f); err != nil {
					rep.failExpr(err, at(p, "sets", id), "%v", err)
				}
			}
		}
		for i, r := range src.Reactions {
			p := at("reactions", i)
			if r.Kind == ReactionCreate {
				checkCreateAcross(rep, p, byID, src, r, containerReads)
			}
			if r.Kind == ReactionMove && r.Target == TargetContainer {
				checkMoveContainer(rep, p, types, src, r)
			}
		}
	}
}

// checkCreateAcross checks a create reaction's container reads and each
// copies against the Types loaded.
func checkCreateAcross(rep reporter, p string, byID map[string][]*Type, src *Type, r Reaction,
	containerReads func(where string, reads []string, expr string, f *Field) error) {
	var created *Type
	for _, c := range byID[r.Type] {
		if c.Version == r.Version {
			created = c
		}
	}
	for _, id := range slices.Sorted(maps.Keys(r.Fields)) {
		q := at(p, "fields", id)
		var target *Field
		if created != nil {
			if f, ok := created.Field(id); ok {
				target = &f
			}
		}
		_, reads, _ := takeContainerFields(stripContainerLinked(r.Fields[id]))
		if err := containerReads(fmt.Sprintf("reaction %s: field %s", r.ID, id), reads, r.Fields[id], target); err != nil {
			rep.failExpr(err, q, "%v", err)
			return
		}
		if target == nil {
			continue
		}
		ref, plain := plainRef(r.Fields[id])
		if plain && ref.Each {
			each, _ := src.Field(r.ForEach)
			if target.Multiple || !sameStoredType(each, *target) {
				rep.fail(CodeWrongFieldType, q, "Copy each into a field holding one value of the member's type",
					"reaction %s: field %s of %s v%d takes each, a member of %s, which is not a value of its shape",
					r.ID, id, created.ID, created.Version, r.ForEach)
				return
			}
		}
	}
}

// checkMoveContainer checks a move at the container against every loaded
// Type that consents to it; one must.
func checkMoveContainer(rep reporter, p string, types []*Type, src *Type, r Reaction) {
	key := ReactionKey(src.ID, r.ID)
	consenting := 0
	for _, tgt := range types {
		a, _ := tgt.Consent(src.ID, r.ID, true)
		if !slices.Contains(a.Moves, r.To) {
			continue
		}
		consenting++
		tat := fmt.Sprintf("%s v%d", tgt.ID, tgt.Version)
		if _, ok := tgt.State(r.To); !ok {
			rep.fail(CodeUnknownState, at(p, "to"), "Move into a state every consenting Type declares",
				"reaction %s moves its container into %q, which is no state of %s", r.ID, r.To, tat)
			return
		}
		froms := r.TargetFrom
		if len(froms) == 0 {
			if froms = tgt.transitionsInto(r.To); len(froms) == 0 {
				rep.fail(CodeUnknownTransition, at(p, "to"), "Move into a state a transition of the container enters",
					"reaction %s moves into %s, which no transition of %s enters", r.ID, r.To, tat)
				return
			}
		}
		for _, from := range froms {
			if _, ok := tgt.Transition(from, r.To); !ok {
				rep.fail(CodeUnknownTransition, at(p, "target_from"), "Name states the container has a transition from",
					"reaction %s: %s has no transition %s -> %s", r.ID, tat, from, r.To)
				return
			}
			if h, held := tgt.heldBy(from, r.To); held {
				rep.fail(CodeHeldTransition, at(p, "to"), "Move into a state whose transitions are not held",
					"reaction %s: %s holds %s -> %s by %s; a reaction may not ask for a held move", r.ID, tat, from, r.To, h)
				return
			}
			if given := tgt.givenOn(from, r.To); len(given) > 0 {
				rep.fail(CodeConflict, at(p, "to"), "Move into a state whose transitions ask nothing to be given, or let a person make the move",
					"reaction %s: %s's transition %s -> %s needs %s given in the move; a reaction gives no fields",
					r.ID, tat, from, r.To, strings.Join(given, ", "))
				return
			}
		}
	}
	if consenting == 0 {
		rep.fail(CodeConsentMissing, p, "Add the consent to the container Type's accepts_reactions",
			"reaction %s moves its container into %s, but no loaded Type accepts it "+
				"(accepts_reactions: {%s: {moves: [%s]}})", r.ID, r.To, key, r.To)
	}
}

// stripContainerLinked replaces container.linked.<link>.<field> reads, so
// that what is left names the container's own fields only.
func stripContainerLinked(expr string) string { return containerLinkedRef.ReplaceAllString(expr, "x") }
