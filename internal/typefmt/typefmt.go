// Package typefmt rewrites a Type file's YAML into one canonical layout, as
// gofmt does for Go: block mappings, one key per line, keys in a fixed
// order, lists of plain scalars kept on one line, comments kept. It changes
// no value: before it returns, it decodes its input and its output and
// refuses (ErrChanged) unless they hold the same data.
//
// It reads YAML, not Types: it does not import typedef, so pli, which talks
// to the item service and does not link typedef, can use it. A file that is
// YAML but not a sound Type is formatted all the same.
package typefmt

import (
	"errors"
	"fmt"
)

// Keys is the canonical key order of each mapping of the Type format,
// keyed by its path: "" for the document, ".fields[]" for an entry of
// fields, ".states[].timer" for a state's timer, ".accepts_reactions{}"
// for a value of that map. The order is the JSON Schema's
// (schema/type.schema.json). Keys a mapping has that its list does not
// name keep their order, after the known ones; a mapping at a path not
// listed here (the author's own keys, as in sets) keeps its order.
var Keys = map[string][]string{}

// ErrChanged is returned when the formatted file would hold other data than
// the input. It is a bug in typefmt if it is ever seen: nothing is written.
var ErrChanged = errors.New("typefmt: formatting would change the file's data; nothing written")

// CommentError is a comment the canonical layout has no place for (a
// comment inside a list that is written on one line). Nothing is written.
type CommentError struct {
	Line int    // the comment's line in the input
	Text string // the comment, with its #
}

func (e *CommentError) Error() string {
	return fmt.Sprintf("line %d: cannot place comment %q", e.Line, e.Text)
}

// Format returns src in the canonical layout. Formatted input comes back
// unchanged.
func Format(src []byte) ([]byte, error) {
	return nil, errors.New("typefmt: not built yet")
}

// verify is the gate: in and out must decode to the same data, and hold the
// same comments.
func verify(in, out []byte) error {
	return nil
}
