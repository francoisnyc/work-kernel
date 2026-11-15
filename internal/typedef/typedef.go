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
	"time"

	"cel.dev/cel-go/cel"
	"go.yaml.in/yaml/v3"

	"github.com/francoisnyc/work-kernel/internal/item"
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

// categories are the fixed state categories from the design. "none" means
// "not work": views of work leave it out, so a chat message never shows up as
// active or done work.
var categories = []string{"triage", "backlog", "todo", "active", "waiting", "done", "canceled", "none"}

// A Type that declares no states has no lifecycle. It gets this one implicit
// state, so every Item still has a state and nothing else needs a special case.
const (
	ImplicitState         = "default"
	ImplicitStateCategory = "none"
)

// TimerPrincipal is the principal a timer's move is made as. A timer gets no
// silent exemption from `allowed`: the Type must name it (see check).
const TimerPrincipal = "system:timer"

// The two kinds of entry in a transition's `allowed` list.
const (
	AllowedRolePrefix      = "role:"
	AllowedPrincipalPrefix = "principal:"
)

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
	// Timer is nil for a state without one. A pointer field plays the role of
	// an Optional here; omitempty leaves it out of the stored JSON when nil.
	Timer *Timer `yaml:"timer" json:"timer,omitempty"`
}

// Timer moves an Item out of a state it has stayed in for too long. It is a
// spike stand-in: real SLAs become standalone objects later.
type Timer struct {
	After string `yaml:"after" json:"after"` // as written, e.g. "4h" or "90s"
	To    string `yaml:"to" json:"to"`       // the state to move to

	// Duration is After in parsed form, filled when the Type is loaded.
	Duration time.Duration `yaml:"-" json:"-"`
}

// Transition is one allowed move between two states.
type Transition struct {
	From     string   `yaml:"from" json:"from"`
	To       string   `yaml:"to" json:"to"`
	Guard    string   `yaml:"guard" json:"guard,omitempty"`       // a CEL expression; see compileGuard
	Required []string `yaml:"required" json:"required,omitempty"` // field IDs that must be set
	// Allowed says who may make the move: entries are "role:<name>" or
	// "principal:<id>". Empty means anyone who can see the Item. The list is
	// only checked for shape here; package policy decides.
	Allowed []string `yaml:"allowed" json:"allowed,omitempty"`
	// Escape marks a move that may leave a state while a hold is open on it
	// (cancel, say). Making it cancels the hold.
	Escape bool `yaml:"escape" json:"escape,omitempty"`

	// Program is Guard compiled, filled when the Type is loaded; nil when
	// there is no guard. The "-" tag keeps it out of YAML and JSON. Package
	// lifecycle evaluates it; this package only compiles it.
	Program cel.Program `yaml:"-" json:"-"`
}

// Type is one version of a Type definition.
type Type struct {
	ID           string       `yaml:"id" json:"id"`
	Version      int          `yaml:"version" json:"version"`
	InitialState string       `yaml:"initial_state" json:"initial_state"`
	Fields       []Field      `yaml:"fields" json:"fields,omitempty"`
	States       []State      `yaml:"states" json:"states"`
	Transitions  []Transition `yaml:"transitions" json:"transitions,omitempty"`
	Hooks        []Hook       `yaml:"hooks" json:"hooks,omitempty"`
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
// is how the item service stores it in Postgres.
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

// check validates the definition, parses every guard, and gives a Type
// without states its one implicit state.
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
		// No lifecycle: naming an initial state or a transition is a mistake.
		if t.InitialState != "" {
			return fail("initial_state %q is set but the Type declares no states", t.InitialState)
		}
		if len(t.Transitions) > 0 {
			return fail("transitions are declared but the Type declares no states")
		}
		t.States = []State{{ID: ImplicitState, Category: ImplicitStateCategory}}
		t.InitialState = ImplicitState
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

	guards, err := newGuardEnv(t)
	if err != nil {
		return fail("%v", err)
	}
	seen = map[string]bool{}
	// Range copies each element, so index into the slice to store Program.
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
		for _, entry := range tr.Allowed {
			// CutPrefix returns the rest of the string and whether the prefix was there.
			who, ok := strings.CutPrefix(entry, AllowedRolePrefix)
			if !ok {
				who, ok = strings.CutPrefix(entry, AllowedPrincipalPrefix)
			}
			if !ok {
				return fail("transition %s: allowed entry %q must be role:<name> or principal:<id>", name, entry)
			}
			if who == "" {
				return fail("transition %s: allowed entry %q names nothing", name, entry)
			}
			// A principal entry names a typed principal ID that may act.
			if strings.HasPrefix(entry, AllowedPrincipalPrefix) &&
				!item.IsKind(who, item.KindUser, item.KindAgent, item.KindSystem, item.KindHook) {
				return fail("transition %s: allowed entry %q must name user:<id>, agent:<id>, system:<id> "+
					"or hook:<type>:<hook-id>", name, entry)
			}
			// A hook acts only on Items of its own Type, so another Type's
			// hook could never pass this list: naming it is a mistake.
			if hookType, _, ok := item.HookParts(who); ok && strings.HasPrefix(entry, AllowedPrincipalPrefix) &&
				hookType != t.ID {
				return fail("transition %s: allowed entry %q names a hook of Type %q; a hook acts only on its own Type",
					name, entry, hookType)
			}
		}
		prg, err := compileGuard(guards, tr.Guard)
		if err != nil {
			return fail("transition %s: guard %q: %v", name, tr.Guard, err)
		}
		tr.Program = prg
	}

	// Timers come last: each must name a transition checked above.
	for i := range t.States {
		s := &t.States[i]
		if s.Timer == nil {
			continue
		}
		if s.Timer.After == "" {
			return fail("state %s: timer needs `after`, such as 4h", s.ID)
		}
		d, err := time.ParseDuration(s.Timer.After)
		if err != nil {
			return fail("state %s: timer `after`: %v", s.ID, err)
		}
		if d <= 0 {
			return fail("state %s: timer `after` must be more than zero", s.ID)
		}
		if s.Timer.To == "" {
			return fail("state %s: timer needs `to`, the state to move to", s.ID)
		}
		// A timer may only make a move the Type already allows from this
		// state, so it can never do what a person could not.
		if _, ok := t.Transition(s.ID, s.Timer.To); !ok {
			return fail("state %s: timer moves to %q, but there is no transition %s -> %s",
				s.ID, s.Timer.To, s.ID, s.Timer.To)
		}
		// The timer acts as TimerPrincipal and is subject to `allowed` like
		// anyone else. A list that leaves it out would make the timer fire
		// and be forbidden every time, so the Type is refused instead.
		tr, _ := t.Transition(s.ID, s.Timer.To)
		timer := AllowedPrincipalPrefix + TimerPrincipal
		if len(tr.Allowed) > 0 && !slices.Contains(tr.Allowed, timer) {
			return fail("state %s: its timer uses transition %s -> %s, whose `allowed` list must include %s",
				s.ID, s.ID, s.Timer.To, timer)
		}
		s.Timer.Duration = d
	}

	// Hooks last: they name states, transitions and fields checked above.
	return t.checkHooks()
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

// FieldError says which field value a Type refuses, and why. The reason
// names the field ID and never repeats a value, which may be personal data.
type FieldError struct {
	Field  string
	Reason string
}

func (e *FieldError) Error() string { return e.Reason }

// CheckFields validates values against the Type's schema. A nil value means
// "clear this field" and is accepted only when allowNull is true (updates).
// The error is a *FieldError naming the first bad field.
func (t *Type) CheckFields(values map[string]any, allowNull bool) error {
	ids := make([]string, 0, len(values))
	for id := range values {
		ids = append(ids, id)
	}
	sort.Strings(ids) // map order is random; sort so the first error is stable

	for _, id := range ids {
		fail := func(format string, args ...any) error {
			return &FieldError{Field: id, Reason: fmt.Sprintf(format, args...)}
		}
		f, ok := t.Field(id)
		if !ok {
			return fail("field %s is not declared by type %s v%d", id, t.ID, t.Version)
		}
		v := values[id]
		if v == nil {
			if !allowNull {
				return fail("field %s: null is not a value", id)
			}
			continue
		}
		switch f.Type {
		case String:
			if _, ok := v.(string); !ok {
				return fail("field %s: want a string", id)
			}
		case Int:
			if _, ok := AsInt(v); !ok {
				return fail("field %s: want a whole number between -(2^53-1) and 2^53-1", id)
			}
		case Bool:
			if _, ok := v.(bool); !ok {
				return fail("field %s: want true or false", id)
			}
		case Enum:
			s, ok := v.(string)
			if !ok || !slices.Contains(f.Options, s) {
				return fail("field %s: want one of %s", id, strings.Join(f.Options, ", "))
			}
		}
	}
	return nil
}
