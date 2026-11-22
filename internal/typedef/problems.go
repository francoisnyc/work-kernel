package typedef

import (
	"cmp"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Problems in a Type definition (Ventoux item 2a). Parse, FromJSON and
// ParseDir report every problem they find, not just the first, as data a
// tool can act on: where it is, a stable code, and a message that says
// what is wrong and then what to do.
//
// Checking runs in three stages, and a stage runs only when the ones
// before it found nothing:
//
//	(a) the YAML: syntax, unknown keys, values of the wrong shape;
//	(b) each Type on its own;
//	(c) the Types together (links, inverses, reactions and consent,
//	    shapes across versions, duplicate versions, the folder layout).
//
// Within a stage every problem is reported, except one that only follows
// from another: a declaration with a problem still counts as declared.
//

// Severity says whether a problem stops a Type from loading. Only ERROR
// exists for now; warnings (Ventoux item 3) will not stop a load.
type Severity string

const SeverityError Severity = "ERROR"

// Code names a kind of problem. Codes are stable, like the API's refusal
// reasons: tools match on them. Each is documented in docs/design.md,
// "Type problems". A named string type keeps codes apart from other strings,
// close to a Java enum.
type Code string

// Stage (a): the YAML.
const (
	// The file is not YAML.
	CodeYAMLSyntax Code = "YAML_SYNTAX"
	// A key the Type format does not have (often a typo).
	CodeUnknownKey Code = "UNKNOWN_KEY"
	// A value of the wrong YAML kind: text for a number, a map for a list.
	CodeWrongShape Code = "WRONG_SHAPE"
)

// Stage (b): one Type.
const (
	// Something that must be there is missing (an ID, a link's inverse).
	CodeRequired Code = "REQUIRED"
	// Declared twice: a field, a state, a transition, an ID, a list entry.
	CodeDuplicate Code = "DUPLICATE"
	// A name the kernel keeps for itself (today, caller, links, ...).
	CodeReservedName Code = "RESERVED_NAME"
	// A name or ID that is not well formed (lower case, digits and _).
	CodeInvalidName Code = "INVALID_NAME"
	// A value outside what the key allows (a category, a field type, a
	// version below 1, a duration that is not positive).
	CodeInvalidValue Code = "INVALID_VALUE"
	// A key that does not apply here (options on a string field, a
	// fallback on a check hook).
	CodeKeyNotApplicable Code = "KEY_NOT_APPLICABLE"
	// Names a field, state, transition, link, hook, reaction or action that
	// does not exist.
	CodeUnknownField      Code = "UNKNOWN_FIELD"
	CodeUnknownState      Code = "UNKNOWN_STATE"
	CodeUnknownTransition Code = "UNKNOWN_TRANSITION"
	CodeUnknownLink       Code = "UNKNOWN_LINK"
	CodeUnknownHook       Code = "UNKNOWN_HOOK"
	CodeUnknownReaction   Code = "UNKNOWN_REACTION"
	CodeUnknownAction     Code = "UNKNOWN_ACTION"
	// A hook's reads or writes do not match its action's.
	CodeActionArity Code = "ACTION_ARITY"
	// A field of the wrong type for this use (an at-timer on a string).
	CodeWrongFieldType Code = "WRONG_FIELD_TYPE"
	// A CEL expression that does not compile.
	CodeExpressionInvalid Code = "EXPRESSION_INVALID"
	// A CEL expression that yields the wrong type (a guard that is not a bool).
	CodeExpressionType Code = "EXPRESSION_TYPE"
	// An expression or reaction reads something it may not read here.
	CodeReadNotAllowed Code = "READ_NOT_ALLOWED"
	// Something writes (sets, clears) a field a state freezes.
	CodeWritesFrozenField Code = "WRITES_FROZEN_FIELD"
	// Something other than a hook writes a field only the kernel writes.
	CodeWritesKernelField Code = "WRITES_KERNEL_FIELD"
	// A held transition, or a move into one, does what a hold forbids.
	CodeHeldTransition Code = "HELD_TRANSITION"
	// Two declarations contradict each other (requires and clears a field).
	CodeConflict Code = "CONFLICT"
	// A second one where only one is allowed (two checks on a transition).
	CodeOnlyOneAllowed Code = "ONLY_ONE_ALLOWED"
	// A declaration needs another that is missing: a kernel-written field
	// no hook writes, a timer hook no timer runs, a held reaction with no undo.
	CodeMissingCounterpart Code = "MISSING_COUNTERPART"
	// A timer's transition does not allow the timer to make it.
	CodeTimerNotAllowed Code = "TIMER_NOT_ALLOWED"
)

// Stage (c): the Types together.
const (
	// Names a Type (or a Type version) that is not loaded.
	CodeUnknownType Code = "UNKNOWN_TYPE"
	// A reaction moves, creates or writes Items of a Type that does not
	// consent to it (accepts_reactions).
	CodeConsentMissing Code = "CONSENT_MISSING"
	// A link name has another inverse in another version of the Type.
	CodeInverseChanged Code = "INVERSE_CHANGED"
	// An incoming declaration does not match the link it names.
	CodeInverseMismatch Code = "INVERSE_MISMATCH"
	// A field holds one value in one version and several in another.
	CodeShapeChanged Code = "SHAPE_CHANGED"
	// Reactions form a cycle.
	CodeReactionCycle Code = "REACTION_CYCLE"
	// Two files declare the same Type version.
	CodeDuplicateVersion Code = "DUPLICATE_VERSION"
	// A folder deeper than one level below the Types folder.
	CodeFolderTooDeep Code = "FOLDER_TOO_DEEP"
	// A draft declares a registered Type version with another definition
	// (Check, Ventoux V2b).
	CodeVersionChanged Code = "VERSION_CHANGED"
)

// Problem is one thing wrong with a Type definition.
type Problem struct {
	// The file, as ParseDir joined it (dir + name); empty from Parse and
	// FromJSON. For FOLDER_TOO_DEEP, the folder.
	Source string
	// Where in the document: transitions[3].allowed[0], fields[1].id,
	// accepts_reactions["part:finish"]; empty for the whole document.
	Path string
	// 1-based position in the YAML: of the key when Path ends in a key, of
	// the item when it ends in an index, of the nearest enclosing node that
	// exists when Path names something missing. Zero when unknown (FromJSON;
	// a syntax error's column; a folder).
	Line, Column int
	// The Type the problem belongs to, when its id and version could be read.
	TypeID  string
	Version int
	// ERROR for now.
	Severity Severity
	Code     Code
	// What is wrong, then what to do. It may name fields, states and Types
	// (a definition holds no personal data).
	Message string
}

// Problems is every problem found, sorted by Source, then position.
// It is an error, so a caller that only checks err != nil keeps working;
// errors.As(err, &problems) gets the list. In Go, any type with an
// Error() string method is an error, so a slice can be one.
type Problems []Problem

// Error lists one problem per line:
//
//	<source>:<line>:<col>: <CODE> typedef <id> v<n>: <message>
//
// leaving out <source>: when there is none, :<col> when the column is
// unknown (a syntax error), <line>:<col>: when the line is unknown, and
// "typedef <id> v<n>: " when the Type is unknown.
func (ps Problems) Error() string {
	lines := make([]string, len(ps))
	for i, p := range ps {
		lines[i] = p.String()
	}
	return strings.Join(lines, "\n")
}

// String is the problem's line in Error().
func (p Problem) String() string {
	var where []string
	if p.Source != "" {
		where = append(where, p.Source)
	}
	if p.Line > 0 {
		where = append(where, strconv.Itoa(p.Line))
		if p.Column > 0 {
			where = append(where, strconv.Itoa(p.Column))
		}
	}
	var b strings.Builder
	if len(where) > 0 {
		b.WriteString(strings.Join(where, ":") + ": ")
	}
	b.WriteString(string(p.Code) + " ")
	if p.TypeID != "" {
		fmt.Fprintf(&b, "typedef %s v%d: ", p.TypeID, p.Version)
	}
	b.WriteString(p.Message)
	return b.String()
}

// sortProblems orders problems by source, then position. The sort is
// stable, so problems without a position (FromJSON) keep the order they
// were found in, which is the order of the definition.
func sortProblems(ps Problems) {
	slices.SortStableFunc(ps, func(a, b Problem) int {
		return cmp.Or(strings.Compare(a.Source, b.Source), cmp.Compare(a.Line, b.Line), cmp.Compare(a.Column, b.Column))
	})
}

// errReported is what a check returns once it has recorded a problem: the
// declaration it was checking stops there, and the caller goes on to the
// next one. It never leaves the package.
var errReported = errors.New("typedef: problem reported")

// reporter records problems for one Type. A check calls
// r.fail(code, path, todo, format, args...): the message is the formatted
// text (what is wrong) then todo (what to do). within adds context to the
// start of every message, such as the hook being checked. It is a small
// value, passed by copy, like a Java record.
type reporter struct {
	list    *Problems
	typeID  string
	version int
	prefix  string
}

func newReporter(list *Problems, typeID string, version int) reporter {
	return reporter{list: list, typeID: typeID, version: version}
}

func (r reporter) fail(code Code, at, todo, format string, args ...any) error {
	*r.list = append(*r.list, Problem{
		Path: at, TypeID: r.typeID, Version: r.version, Severity: SeverityError, Code: code,
		Message: oneLine(r.prefix+fmt.Sprintf(format, args...)) + ". " + todo,
	})
	return errReported
}

// oneLine keeps a message on one line, so Error() is one line per problem.
// CEL's errors quote the expression with a caret on lines of their own
// (" | ..."); its <input>:line:col already says where, so those go.
func oneLine(msg string) string {
	if !strings.Contains(msg, "\n") {
		return msg
	}
	var keep []string
	for _, line := range strings.Split(msg, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "|") && strings.TrimSpace(line) != "" {
			keep = append(keep, strings.TrimSpace(line))
		}
	}
	return strings.Join(keep, " ")
}

// failExpr reports an expression's problem under the code its exprError
// carries (EXPRESSION_INVALID for any other error).
func (r reporter) failExpr(err error, at, format string, args ...any) error {
	code := CodeExpressionInvalid
	var ee exprError
	if errors.As(err, &ee) {
		code = ee.code
	}
	switch code {
	case CodeExpressionType:
		return r.fail(CodeExpressionType, at, "Write an expression that yields a value of the right type", format, args...)
	case CodeReadNotAllowed:
		return r.fail(CodeReadNotAllowed, at, "Read only what the message says this expression may read", format, args...)
	case CodeUnknownField:
		return r.fail(CodeUnknownField, at, "Declare the field, or name one that is declared", format, args...)
	case CodeWrongFieldType:
		return r.fail(CodeWrongFieldType, at, "Name a field of the same type and shape", format, args...)
	default:
		return r.fail(CodeExpressionInvalid, at, "Fix the expression; it reads fields by their ids", format, args...)
	}
}

// within returns a reporter whose messages start with the given context.
func (r reporter) within(format string, args ...any) reporter {
	r.prefix += fmt.Sprintf(format, args...)
	return r
}

// crossCheck collects the problems of stage (c), the Types together, each
// on the Type it belongs to (where the fix goes).
type crossCheck struct{ ps Problems }

func (c *crossCheck) on(t *Type) reporter { return newReporter(&c.ps, t.ID, t.Version) }

// err is the problems as an error, or nil.
func (c *crossCheck) err() error {
	if len(c.ps) == 0 {
		return nil
	}
	return c.ps
}

// at builds a path into the YAML. The first part is written as it is (a
// top-level key, or a path built before); after it, a string is a key and
// an int an index: at("transitions", 3, "allowed", 0) is
// transitions[3].allowed[0]. A key that is not a plain name is quoted:
// accepts_reactions["part:finish"].
func at(parts ...any) string {
	var b strings.Builder
	for i, part := range parts {
		switch v := part.(type) {
		case int:
			fmt.Fprintf(&b, "[%d]", v)
		case string:
			switch {
			case i == 0:
				b.WriteString(v)
			case plainKey.MatchString(v):
				b.WriteString("." + v)
			default:
				b.WriteString("[" + strconv.Quote(v) + "]")
			}
		}
	}
	return b.String()
}

var (
	plainKey  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	pathToken = regexp.MustCompile(`\.?([A-Za-z_][A-Za-z0-9_]*)|\[(\d+)\]|\[("(?:[^"\\]|\\.)*")\]`)
)

// locate finds a path's position in a YAML document: the key's when the
// path ends in a key, the item's when it ends in an index, the nearest
// enclosing node's when the path names something missing. 0, 0 without a
// document.
func locate(doc *yaml.Node, path string) (line, col int) {
	if doc == nil {
		return 0, 0
	}
	n := doc
	if n.Kind == yaml.DocumentNode {
		if len(n.Content) == 0 {
			return 0, 0
		}
		n = n.Content[0]
	}
	line, col = n.Line, n.Column
	for _, m := range pathToken.FindAllStringSubmatch(path, -1) {
		n = resolveAlias(n)
		switch {
		case m[2] != "": // an index
			i, _ := strconv.Atoi(m[2])
			if n.Kind != yaml.SequenceNode || i >= len(n.Content) {
				return line, col
			}
			n = n.Content[i]
			line, col = n.Line, n.Column
		default: // a key, plain or quoted
			key := m[1]
			if m[3] != "" {
				key, _ = strconv.Unquote(m[3])
			}
			k, v := mappingEntry(n, key)
			if k == nil {
				return line, col
			}
			line, col, n = k.Line, k.Column, v
		}
	}
	return line, col
}

func resolveAlias(n *yaml.Node) *yaml.Node {
	for n != nil && n.Kind == yaml.AliasNode {
		n = n.Alias
	}
	return n
}

// mappingEntry returns the key and value nodes of key in a mapping node.
func mappingEntry(n *yaml.Node, key string) (k, v *yaml.Node) {
	for _, kv := range mappingPairs(n) {
		if kv[0].Value == key {
			return kv[0], kv[1]
		}
	}
	return nil, nil
}

// position fills in each problem's line and column from the document.
func position(ps Problems, doc *yaml.Node) {
	for i := range ps {
		ps[i].Line, ps[i].Column = locate(doc, ps[i].Path)
	}
}
