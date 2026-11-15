package typedef

import (
	"fmt"

	"cel.dev/cel-go/cel"
)

// Guards are CEL expressions (https://cel.dev). Every field of the Type is a
// variable named by its ID, typed by the schema and nullable: an unset field
// is null. So "the assignee is set" reads `f_assignee != null`, and a guard
// that compares a field with a literal of the wrong type is refused at load.
//
// This package compiles guards; package lifecycle evaluates them.

// celType maps a field type to its CEL type. Enums are strings to CEL.
func celType(t FieldType) *cel.Type {
	switch t {
	case Int:
		return cel.NullableType(cel.IntType)
	case Bool:
		return cel.NullableType(cel.BoolType)
	default: // String, Enum
		return cel.NullableType(cel.StringType)
	}
}

// newGuardEnv declares one CEL variable per field of t.
func newGuardEnv(t *Type) (*cel.Env, error) {
	opts := make([]cel.EnvOption, 0, len(t.Fields))
	for _, f := range t.Fields {
		opts = append(opts, cel.Variable(f.ID, celType(f.Type)))
	}
	env, err := cel.NewEnv(opts...)
	if err != nil {
		return nil, fmt.Errorf("guard environment: %w", err)
	}
	return env, nil
}

// compileGuard checks and compiles one guard. An empty guard always holds
// and compiles to nil.
func compileGuard(env *cel.Env, guard string) (cel.Program, error) {
	if guard == "" {
		return nil, nil
	}
	// Compile returns "issues" rather than a plain error; Err() is nil
	// when there are none.
	ast, iss := env.Compile(guard)
	if iss.Err() != nil {
		return nil, iss.Err()
	}
	if !ast.OutputType().IsExactType(cel.BoolType) {
		return nil, fmt.Errorf("a guard must yield a bool, not %s", ast.OutputType())
	}
	return env.Program(ast)
}
