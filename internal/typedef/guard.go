package typedef

import (
	"fmt"
	"sort"
	"strings"

	"cel.dev/cel-go/cel"
)

// Guards are CEL expressions (https://cel.dev). Every field of the Type is a
// variable named by its ID, typed by the schema and nullable: an unset field
// is null. So "the assignee is set" reads `f_assignee != null`, and a guard
// that compares a field with a literal of the wrong type is refused at load.
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
	opts := make([]cel.EnvOption, 0, len(t.Fields))
	for _, f := range t.Fields {
		opts = append(opts, cel.Variable(f.ID, celType(f.Type)))
	}
	for _, name := range t.guardLinks() {
		opts = append(opts, cel.Variable(LinksPrefix+name, linkedItems))
	}
	env, err := cel.NewEnv(opts...)
	if err != nil {
		return nil, fmt.Errorf("guard environment: %w", err)
	}
	return env, nil
}

// compileGuard checks and compiles one guard, and returns the link names
// it reads, sorted. An empty guard always holds and compiles to nil.
func compileGuard(env *cel.Env, guard string) (cel.Program, []string, error) {
	if guard == "" {
		return nil, nil, nil
	}
	// Compile returns "issues" rather than a plain error; Err() is nil
	// when there are none.
	ast, iss := env.Compile(guard)
	if iss.Err() != nil {
		return nil, nil, iss.Err()
	}
	if !ast.OutputType().IsExactType(cel.BoolType) {
		return nil, nil, fmt.Errorf("a guard must yield a bool, not %s", ast.OutputType())
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
	return prg, links, err
}
