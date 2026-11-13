// Package typedef holds a Type: versioned data that describes the fields,
// states and transitions of a kind of Item. A Type names code but never
// contains it. This package only parses and checks Types; deciding whether a
// transition may happen is the job of package lifecycle.
package typedef

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

// FieldType is the kind of value a field holds. A named string type gives
// Go something close to a Java enum: the compiler keeps it apart from plain strings.
type FieldType string

const (
	String FieldType = "string"
	Int    FieldType = "int"
	Bool   FieldType = "bool"
	Enum   FieldType = "enum"
)

// categories are the fixed state categories from the design.
var categories = []string{"triage", "backlog", "todo", "active", "waiting", "done", "canceled"}

// The struct tags (the text in backquotes) tell the YAML and JSON libraries
// which key maps to which field, like Jackson annotations.

// Field is one field of a Type, known by a stable ID.
type Field struct {
	ID      string    `yaml:"id" json:"id"`
	Type    FieldType `yaml:"type" json:"type"`
	Options []string  `yaml:"options" json:"options,omitempty"` // enum only
}

// State is one lifecycle state. Every state has a category.
type State struct {
	ID       string `yaml:"id" json:"id"`
	Category string `yaml:"category" json:"category"`
}

// Transition is one allowed move between two states.
type Transition struct {
	From     string   `yaml:"from" json:"from"`
	To       string   `yaml:"to" json:"to"`
	Guard    string   `yaml:"guard" json:"guard,omitempty"`       // expression; see ParseGuard
	Required []string `yaml:"required" json:"required,omitempty"` // field IDs that must be set
	Allowed  []string `yaml:"allowed" json:"allowed,omitempty"`   // who may act; not enforced until Step 4

	// Conditions is Guard in parsed form, filled when the Type is loaded.
	// The "-" tag keeps it out of YAML and JSON.
	Conditions []Condition `yaml:"-" json:"-"`
}

// Type is one version of a Type definition.
type Type struct {
	ID           string       `yaml:"id" json:"id"`
	Version      int          `yaml:"version" json:"version"`
	InitialState string       `yaml:"initial_state" json:"initial_state"`
	Fields       []Field      `yaml:"fields" json:"fields,omitempty"`
	States       []State      `yaml:"states" json:"states"`
	Transitions  []Transition `yaml:"transitions" json:"transitions,omitempty"`
}

// Parse reads a Type from YAML and checks it. Unknown keys are errors, so a
// typo in a Type file is caught at load time.
func Parse(data []byte) (*Type, error) {
	var t Type
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&t); err != nil {
		return nil, fmt.Errorf("typedef: parse YAML: %w", err)
	}
	if err := t.check(); err != nil {
		return nil, err
	}
	return &t, nil
}

// FromJSON reads a Type from the JSON form produced by json.Marshal, which
// is how the kernel stores it in Postgres.
func FromJSON(data []byte) (*Type, error) {
	var t Type
	if err := json.Unmarshal(data, &t); err != nil {
		return nil, fmt.Errorf("typedef: parse JSON: %w", err)
	}
	if err := t.check(); err != nil {
		return nil, err
	}
	return &t, nil
}

// Field returns the field with the given ID. The second value reports
// whether it exists, Go's usual alternative to returning null.
func (t *Type) Field(id string) (Field, bool) {
	for _, f := range t.Fields {
		if f.ID == id {
			return f, true
		}
	}
	return Field{}, false
}

// State returns the state with the given ID.
func (t *Type) State(id string) (State, bool) {
	for _, s := range t.States {
		if s.ID == id {
			return s, true
		}
	}
	return State{}, false
}

// Transition returns the transition between two states, if the Type declares one.
func (t *Type) Transition(from, to string) (Transition, bool) {
	for _, tr := range t.Transitions {
		if tr.From == from && tr.To == to {
			return tr, true
		}
	}
	return Transition{}, false
}

// check validates the definition and parses every guard.
func (t *Type) check() error {
	if t.ID == "" {
		return errors.New("typedef: id is required")
	}
	// Errors below start with the Type's name so the message stands on its own.
	fail := func(format string, args ...any) error {
		return fmt.Errorf("typedef %s v%d: %s", t.ID, t.Version, fmt.Sprintf(format, args...))
	}
	if t.Version < 1 {
		return fail("version must be 1 or more")
	}

	seen := map[string]bool{}
	for _, f := range t.Fields {
		if f.ID == "" {
			return fail("a field has no id")
		}
		if seen[f.ID] {
			return fail("duplicate field %q", f.ID)
		}
		seen[f.ID] = true
		switch f.Type {
		case String, Int, Bool:
			if len(f.Options) > 0 {
				return fail("field %s: only enum fields take options", f.ID)
			}
		case Enum:
			if len(f.Options) == 0 {
				return fail("field %s: an enum needs options", f.ID)
			}
		default:
			return fail("field %s: unknown type %q", f.ID, f.Type)
		}
	}

	if len(t.States) == 0 {
		return fail("at least one state is required")
	}
	seen = map[string]bool{}
	for _, s := range t.States {
		if s.ID == "" {
			return fail("a state has no id")
		}
		if seen[s.ID] {
			return fail("duplicate state %q", s.ID)
		}
		seen[s.ID] = true
		if !slices.Contains(categories, s.Category) {
			return fail("state %s: unknown category %q", s.ID, s.Category)
		}
	}
	if _, ok := t.State(t.InitialState); !ok {
		return fail("initial_state %q is not a declared state", t.InitialState)
	}

	seen = map[string]bool{}
	// Range copies each element, so index into the slice to store Conditions.
	for i := range t.Transitions {
		tr := &t.Transitions[i]
		name := tr.From + " -> " + tr.To
		for _, s := range []string{tr.From, tr.To} {
			if _, ok := t.State(s); !ok {
				return fail("transition %s: unknown state %q", name, s)
			}
		}
		if seen[name] {
			return fail("duplicate transition %s", name)
		}
		seen[name] = true
		for _, id := range tr.Required {
			if _, ok := t.Field(id); !ok {
				return fail("transition %s: required field %q is not declared", name, id)
			}
		}
		conds, err := ParseGuard(t, tr.Guard)
		if err != nil {
			return fail("transition %s: guard %q: %v", name, tr.Guard, err)
		}
		tr.Conditions = conds
	}
	return nil
}

// maxExactInt is the largest integer a JSON number (a float64) holds exactly.
const maxExactInt = 1<<53 - 1

// AsInt converts a field value to an integer. It accepts Go integers and
// whole float64 values (numbers read back from JSON are float64), and refuses
// anything outside the range JSON can carry without rounding.
func AsInt(v any) (int64, bool) {
	var n int64
	// A type switch branches on the dynamic type, like a chain of instanceof.
	switch x := v.(type) {
	case int:
		n = int64(x)
	case int32:
		n = int64(x)
	case int64:
		n = x
	case float64:
		if x != math.Trunc(x) || math.Abs(x) > maxExactInt {
			return 0, false
		}
		n = int64(x)
	case json.Number:
		i, err := x.Int64()
		if err != nil {
			return 0, false
		}
		n = i
	default:
		return 0, false
	}
	if n > maxExactInt || n < -maxExactInt {
		return 0, false
	}
	return n, true
}

// CheckFields validates values against the Type's schema. A nil value means
// "clear this field" and is accepted only when allowNull is true (updates).
// Error messages name field IDs and never repeat a value, which may be
// personal data.
func (t *Type) CheckFields(values map[string]any, allowNull bool) error {
	ids := make([]string, 0, len(values))
	for id := range values {
		ids = append(ids, id)
	}
	sort.Strings(ids) // map order is random; sort so the first error is stable

	for _, id := range ids {
		f, ok := t.Field(id)
		if !ok {
			return fmt.Errorf("field %s is not declared by type %s v%d", id, t.ID, t.Version)
		}
		v := values[id]
		if v == nil {
			if !allowNull {
				return fmt.Errorf("field %s: null is not a value", id)
			}
			continue
		}
		switch f.Type {
		case String:
			if _, ok := v.(string); !ok {
				return fmt.Errorf("field %s: want a string", id)
			}
		case Int:
			if _, ok := AsInt(v); !ok {
				return fmt.Errorf("field %s: want a whole number between -(2^53-1) and 2^53-1", id)
			}
		case Bool:
			if _, ok := v.(bool); !ok {
				return fmt.Errorf("field %s: want true or false", id)
			}
		case Enum:
			s, ok := v.(string)
			if !ok || !slices.Contains(f.Options, s) {
				return fmt.Errorf("field %s: want one of %s", id, strings.Join(f.Options, ", "))
			}
		}
	}
	return nil
}
