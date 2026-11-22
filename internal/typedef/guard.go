package typedef

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"cel.dev/cel-go/cel"
)

// Guards are CEL expressions (https://cel.dev). Every field of the Type is a
// variable named by its ID, typed by the schema and nullable: an unset field
// is null. So "the assignee is set" reads `f_assignee != null`, and a guard
// that compares a field with a literal of the wrong type is refused at load.
// A date field is a timestamp at 00:00 UTC, and `today` is one too.
//
// Who acts is never a guard variable: identity rules are declared, in a
// transition's `allowed` and `except` (creator), and judged by package
// policy in one place.
//
// Links (proof of concept, Step 1): links.<name> is the list of Items linked
// under that name, each a map with only "state" and "category" (a guard
// never sees a linked Item's fields). The names are the Type's declared link
// names (outgoing) and the inverses of its link types that target the Type
// itself (incoming); see guardLinks. An unknown name is refused at load.
//
// This package compiles guards; package lifecycle evaluates them.

// LinksPrefix starts the CEL variable of a link name: links.blocked_by.
const LinksPrefix = "links."

// celType maps a field type to its CEL type. Enums and principals are
// strings to CEL.
func celType(t FieldType) *cel.Type {
	switch t {
	case Int:
		return cel.NullableType(cel.IntType)
	case Bool:
		return cel.NullableType(cel.BoolType)
	case Date, DateTime:
		return cel.NullableType(cel.TimestampType)
	default: // String, Enum, Principal
		return cel.NullableType(cel.StringType)
	}
}

// linkedItems is the CEL type of links.<name>.
var linkedItems = cel.ListType(cel.MapType(cel.StringType, cel.StringType))

// newGuardEnv declares one CEL variable per field of t, and one per link
// name its guards may read. A qualified name such as links.blocks is
// declared as it is written; CEL resolves links.blocks to it, and links
// alone is no variable at all.
func newGuardEnv(t *Type) (*cel.Env, error) {
	opts := calendarFunctions()
	// today is a date (00:00 UTC), set by the caller; never null.
	opts = append(opts, cel.Variable(VarToday, cel.TimestampType))
	for _, f := range t.celFields() {
		opts = append(opts, cel.Variable(f.ID, fieldCELType(f)))
	}
	for _, name := range t.guardLinks() {
		opts = append(opts, cel.Variable(LinksPrefix+name, linkedItems))
	}
	// Jira 2.4b (kernel change 4): the container, as {state, category} or
	// null at the top level, and the contained Items by Type. See
	// checkGuardReads.
	opts = append(opts, cel.Variable(ContainerVar, cel.DynType), cel.Variable(ChildrenVar, guardChildren))
	env, err := cel.NewEnv(opts...)
	if err != nil {
		return nil, fmt.Errorf("guard environment: %w", err)
	}
	return env, nil
}

// celFields are the fields an expression environment declares as
// variables: each ID once (its first declaration), leaving out a missing or
// reserved ID. Those are reported where they are declared; declaring them
// would break every expression of the Type, a cascade.
func (t *Type) celFields() []Field {
	var out []Field
	seen := map[string]bool{}
	for _, f := range t.Fields {
		if f.ID == "" || seen[f.ID] || slices.Contains(reservedNames, f.ID) {
			continue
		}
		seen[f.ID] = true
		out = append(out, f)
	}
	return out
}

// exprError is an expression's problem with its code: EXPRESSION_INVALID
// (it does not compile), EXPRESSION_TYPE (the wrong type of value) or
// READ_NOT_ALLOWED (it reads what it may not read here).
type exprError struct {
	code Code
	err  error
}

func (e exprError) Error() string { return e.err.Error() }
func (e exprError) Unwrap() error { return e.err }

// guardReads is what a guard reads beyond the Item's own fields: link
// names, sorted; whether it reads the container; the Types of children it
// reads, sorted.
type guardReads struct {
	links     []string
	container bool
	children  []string
}

// compileGuard checks and compiles one guard, and returns what it reads.
// An empty guard always holds and compiles to nil.
func compileGuard(env *cel.Env, guard string) (cel.Program, guardReads, error) {
	if guard == "" {
		return nil, guardReads{}, nil
	}
	// Compile returns "issues" rather than a plain error; Err() is nil
	// when there are none.
	ast, iss := env.Compile(guard)
	if iss.Err() != nil {
		return nil, guardReads{}, exprError{CodeExpressionInvalid, iss.Err()}
	}
	if !ast.OutputType().IsExactType(cel.BoolType) {
		return nil, guardReads{}, exprError{CodeExpressionType, fmt.Errorf("a guard must yield a bool, not %s", ast.OutputType())}
	}
	container, children, err := checkGuardReads(ast.NativeRep())
	if err != nil {
		return nil, guardReads{}, exprError{CodeReadNotAllowed, err}
	}
	// The checker resolved every identifier; the ones under links. are
	// the link names this guard reads.
	seen := map[string]bool{}
	for _, ref := range ast.NativeRep().ReferenceMap() {
		if name, ok := strings.CutPrefix(ref.Name, LinksPrefix); ok {
			seen[name] = true
		}
	}
	var links []string
	for name := range seen {
		links = append(links, name)
	}
	sort.Strings(links)
	prg, err := env.Program(ast)
	return prg, guardReads{links: links, container: container, children: children}, err
}

// fieldCELType is a field's CEL type: its type's, or for a multi-value
// field a list of strings (every type that may be multiple is text to CEL
// but date; a set of dates is a list of timestamps). Unset is null, as for
// any field, so "has members" reads f != null.
func fieldCELType(f Field) *cel.Type {
	if !f.Multiple {
		return celType(f.Type)
	}
	if f.Type == Date {
		return cel.NullableType(cel.ListType(cel.TimestampType))
	}
	return cel.NullableType(cel.ListType(cel.StringType))
}
