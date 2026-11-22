package typedef

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
// Phase B stub: the types and codes are the contract; nothing returns them yet.

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
func (ps Problems) Error() string { return "" } // Phase B stub
