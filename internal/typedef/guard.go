package typedef

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// Op is a guard operator.
type Op string

const (
	OpEq    Op = "=="
	OpNotEq Op = "!="
	OpIsSet Op = "is set"
)

// Condition is one term of a guard: a field, an operator and, for == and !=,
// a literal (string, int64 or bool).
type Condition struct {
	Field string
	Op    Op
	Value any
}

// String renders the condition the way it is written in a Type file. Having
// this method makes Condition satisfy fmt.Stringer, like overriding toString().
func (c Condition) String() string {
	switch v := c.Value.(type) {
	case nil:
		return c.Field + " " + string(c.Op)
	case string:
		return fmt.Sprintf("%s %s %s", c.Field, c.Op, strconv.Quote(v))
	default:
		return fmt.Sprintf("%s %s %v", c.Field, c.Op, v)
	}
}

// ParseGuard parses a guard expression for Type t. The spike's grammar is
// deliberately tiny (CEL or similar comes later):
//
//	guard     = condition { "&&" condition }
//	condition = field "is set" | field "==" literal | field "!=" literal
//	literal   = "double-quoted string" | integer | true | false
//
// An empty guard has no conditions and always holds.
func ParseGuard(t *Type, guard string) ([]Condition, error) {
	if strings.TrimSpace(guard) == "" {
		return nil, nil
	}
	var conds []Condition
	for _, term := range splitTerms(guard) {
		c, err := parseCondition(t, strings.TrimSpace(term))
		if err != nil {
			return nil, err
		}
		conds = append(conds, c)
	}
	return conds, nil
}

// splitTerms splits on "&&", ignoring any "&&" inside a quoted string.
func splitTerms(guard string) []string {
	var (
		terms    []string
		start    int
		inQuotes bool
	)
	for i := 0; i < len(guard); i++ {
		switch {
		case guard[i] == '\\' && inQuotes:
			i++ // skip the escaped character
		case guard[i] == '"':
			inQuotes = !inQuotes
		case !inQuotes && strings.HasPrefix(guard[i:], "&&"):
			terms = append(terms, guard[start:i])
			start = i + 2
			i++
		}
	}
	return append(terms, guard[start:])
}

func parseCondition(t *Type, term string) (Condition, error) {
	// strings.Cut splits around the first separator: "f_x == 3" -> "f_x", "== 3".
	fieldID, rest, _ := strings.Cut(term, " ")
	rest = strings.TrimSpace(rest)
	if fieldID == "" || rest == "" {
		return Condition{}, fmt.Errorf("cannot read %q: want `field is set`, `field == literal` or `field != literal`", term)
	}
	field, ok := t.Field(fieldID)
	if !ok {
		return Condition{}, fmt.Errorf("field %q is not declared", fieldID)
	}
	if rest == string(OpIsSet) {
		return Condition{Field: fieldID, Op: OpIsSet}, nil
	}

	opText, literal, _ := strings.Cut(rest, " ")
	op := Op(opText)
	if op != OpEq && op != OpNotEq {
		return Condition{}, fmt.Errorf("cannot read %q: unknown operator %q", term, opText)
	}
	value, err := parseLiteral(field, strings.TrimSpace(literal))
	if err != nil {
		return Condition{}, fmt.Errorf("%s: %w", fieldID, err)
	}
	return Condition{Field: fieldID, Op: op, Value: value}, nil
}

// parseLiteral reads a literal and checks that it fits the field's type.
func parseLiteral(f Field, text string) (any, error) {
	switch f.Type {
	case String, Enum:
		s, err := strconv.Unquote(text)
		if err != nil || !strings.HasPrefix(text, `"`) {
			return nil, fmt.Errorf("want a double-quoted string, got %s", text)
		}
		if f.Type == Enum && !slices.Contains(f.Options, s) {
			return nil, fmt.Errorf("%q is not one of the enum's options", s)
		}
		return s, nil
	case Int:
		n, err := strconv.ParseInt(text, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("want a whole number, got %s", text)
		}
		return n, nil
	case Bool:
		b, err := strconv.ParseBool(text)
		if err != nil || (text != "true" && text != "false") {
			return nil, fmt.Errorf("want true or false, got %s", text)
		}
		return b, nil
	}
	return nil, errors.New("unsupported field type")
}
