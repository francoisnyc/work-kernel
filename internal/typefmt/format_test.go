package typefmt_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/typefmt"
)

// formatCases are the canonical layout, one rule at a time: each input and
// the exact bytes Format returns for it.
var formatCases = []struct {
	name, in, want string
}{
	{
		name: "a small Type: block mappings, flow scalar lists, the key order, a blank line before each section",
		in: `# The thing, version 1.
id: thing
version: 1
initial_state: open
states:
  - {id: open,   category: todo}
  - {id: closed, category: done, frozen: [f_a]}
fields:
  - {id: f_a, type: string}   # the first
  - {type: enum, id: f_b, options: [x, y]}
transitions:
  - {from: open, to: closed, allowed: ['role:clerk'], guard: 'f_a != null'}
`,
		want: `# The thing, version 1.
id: thing
version: 1
initial_state: open

fields:
  - id: f_a  # the first
    type: string
  - id: f_b
    type: enum
    options: [x, y]

states:
  - id: open
    category: todo
  - id: closed
    category: done
    frozen: [f_a]

transitions:
  - from: open
    to: closed
    guard: 'f_a != null'
    allowed: ['role:clerk']
`,
	},
	{
		name: "the document's own comment keeps its blank line",
		in:   "# The header.\n\n# The id.\nid: t\nversion: 1\n",
		want: "# The header.\n\n# The id.\nid: t\nversion: 1\n",
	},
	{
		name: "unknown keys come after the known ones, in their own order",
		in:   "zeta: 1\nversion: 2\nid: t\nalpha: [b]\nfields:\n  - {zz: 1, id: f, type: int, aa: 2}\n",
		want: "id: t\nversion: 2\n\nfields:\n  - id: f\n    type: int\n    zz: 1\n    aa: 2\nzeta: 1\n\nalpha: [b]\n",
	},
	{
		name: "a nested mapping takes its own order",
		in:   "id: t\nstates:\n  - {id: a, category: waiting, timer: {to: b, after: 48h}}\n",
		want: "id: t\n\nstates:\n  - id: a\n    category: waiting\n    timer:\n      after: 48h\n      to: b\n",
	},
	{
		name: "the author's own keys keep their order (sets, the keys of accepts_reactions)",
		in: `id: t
transitions:
  - {from: a, to: b, sets: {f_b: 'x', f_a: 'y'}}
accepts_reactions:
  other:react: {sets: [f_x], fields: [f_y]}
  b:c: [f_z]
`,
		want: `id: t

transitions:
  - from: a
    to: b
    sets:
      f_b: 'x'
      f_a: 'y'

accepts_reactions:
  other:react:
    fields: [f_y]
    sets: [f_x]
  b:c: [f_z]
`,
	},
	{
		name: "a block list of plain scalars goes on one line; a list with a mapping in it is block",
		in:   "id: t\nsummary:\n  - f_a\n  - f_b\nx: [a, {b: c}]\n",
		want: "id: t\n\nsummary: [f_a, f_b]\n\nx:\n  - a\n  - b: c\n",
	},
	{
		name: "empty collections stay as they are",
		in:   "id: t\nstart: {}\nfields: []\n",
		want: "id: t\n\nfields: []\n\nstart: {}\n",
	},
	{
		name: "scalars are copied as written: quotes, escapes, numbers, nulls",
		in: `id: '5'
version: "5"
initial_state: ~
fields:
  - {id: f_a, type: string, label: "a\tb", help: 'it''s'}
  - {id: f_b, type: int, max: 0x10}
`,
		want: `id: '5'
version: "5"
initial_state: ~

fields:
  - id: f_a
    type: string
    label: "a\tb"
    help: 'it''s'
  - id: f_b
    type: int
    max: 0x10
`,
	},
	{
		name: "block scalars keep their line breaks, indented to their new place",
		in: `id: t
invariants:
    -   id: r
        rule: >-
            f_a != null &&
              f_b != null
    -   id: s
        rule: |
            one
            two
`,
		want: `id: t

invariants:
  - id: r
    rule: >-
      f_a != null &&
        f_b != null
  - id: s
    rule: |
      one
      two
`,
	},
	{
		name: "a quoted scalar over two lines keeps its break, its next line under its key",
		in:   "id: t\ntransitions:\n  - {from: a, to: b, guard: 'f_a != null &&\n        f_b != null'}\n",
		want: "id: t\n\ntransitions:\n  - from: a\n    to: b\n    guard: 'f_a != null &&\n      f_b != null'\n",
	},
	{
		name: "blank lines: one before each top-level collection, none elsewhere",
		in:   "id: t\n\n\nversion: 1\nstates:\n  - {id: a, category: todo}\n\n  - {id: b, category: done}\ntransitions:\n\n\n  - {from: a, to: b}\n",
		want: "id: t\nversion: 1\n\nstates:\n  - id: a\n    category: todo\n  - id: b\n    category: done\n\ntransitions:\n  - from: a\n    to: b\n",
	},
	{
		name: "comments are kept: head, line and foot",
		in: `# Header.
id: t
states:  # the states
  - {id: a, category: todo}
transitions:
  # Only the clerk.
  - {from: a, to: b, allowed: ['role:clerk']}  # one move
  - from: b
    to: a
    guard: 'true'  # always
  # the end of the moves
`,
		want: `# Header.
id: t

states:  # the states
  - id: a
    category: todo

transitions:
  # Only the clerk.
  - from: a  # one move
    to: b
    allowed: ['role:clerk']
  - from: b
    to: a
    guard: 'true'  # always
  # the end of the moves
`,
	},
	{
		name: "a comment moves with its key",
		in:   "version: 1\n# the id\nid: t\n",
		want: "# the id\nid: t\nversion: 1\n",
	},
	{
		name: "anchors and aliases are kept",
		in: `id: t
reactions:
  - id: a
    kind: create
    fields: &common
      f_x: 'cause.at'
  - {id: b, kind: create, fields: *common}
`,
		want: `id: t

reactions:
  - id: a
    kind: create
    fields: &common
      f_x: 'cause.at'
  - id: b
    kind: create
    fields: *common
`,
	},
	{
		name: "the file ends with one newline",
		in:   "id: t\nversion: 1",
		want: "id: t\nversion: 1\n",
	},
	{
		name: "an empty file stays empty",
		in:   "",
		want: "",
	},
}

func TestFormat_TheCanonicalLayout(t *testing.T) {
	for _, c := range formatCases {
		t.Run(c.name, func(t *testing.T) {
			got, err := typefmt.Format([]byte(c.in))
			if err != nil {
				t.Fatalf("Format: %v", err)
			}
			if string(got) != c.want {
				t.Errorf("Format:\n--- got\n%s--- want\n%s", got, c.want)
			}
		})
	}
}

// TestFormat_IsIdempotent: formatted input comes back byte for byte.
func TestFormat_IsIdempotent(t *testing.T) {
	for _, c := range formatCases {
		t.Run(c.name, func(t *testing.T) {
			once, err := typefmt.Format([]byte(c.in))
			if err != nil {
				t.Fatalf("Format: %v", err)
			}
			twice, err := typefmt.Format(once)
			if err != nil {
				t.Fatalf("Format of the formatted: %v", err)
			}
			if string(twice) != string(once) {
				t.Errorf("formatting twice changed it:\n--- once\n%s--- twice\n%s", once, twice)
			}
		})
	}
}

// TestFormat_ACommentWithNoPlaceRefusesTheFile: a list of plain scalars is
// written on one line, so a comment on one of its items has nowhere to go.
// The file is refused, not stripped.
func TestFormat_ACommentWithNoPlaceRefusesTheFile(t *testing.T) {
	for name, c := range map[string]struct {
		in   string
		line int
		text string
	}{
		"in a flow list":             {"id: t\nsummary: [f_a,  # first\n  f_b]\n", 2, "# first"},
		"on an item of a block list": {"id: t\nsummary:\n  - f_a  # the title\n  - f_b\n", 3, "# the title"},
	} {
		t.Run(name, func(t *testing.T) {
			out, err := typefmt.Format([]byte(c.in))
			var ce *typefmt.CommentError
			if !errors.As(err, &ce) {
				t.Fatalf("Format = %q, %v; want a *CommentError", out, err)
			}
			if ce.Line != c.line || ce.Text != c.text {
				t.Errorf("CommentError = line %d %q, want line %d %q", ce.Line, ce.Text, c.line, c.text)
			}
			if want := `cannot place comment "` + c.text + `"`; !strings.Contains(ce.Error(), want) {
				t.Errorf("Error() = %q, want it to contain %q", ce.Error(), want)
			}
			if out != nil {
				t.Errorf("Format returned output with its error: %q", out)
			}
		})
	}
}

// TestFormat_RefusesWhatIsNotOneYAMLDocument: no output, an error that is
// neither a comment's nor the gate's.
func TestFormat_RefusesWhatIsNotOneYAMLDocument(t *testing.T) {
	for name, in := range map[string]string{
		"not YAML":          "id: [t\n",
		"a tab indentation": "id: t\nfields:\n\t- {id: f, type: int}\n",
		"two documents":     "id: a\n---\nid: b\n",
	} {
		t.Run(name, func(t *testing.T) {
			out, err := typefmt.Format([]byte(in))
			if err == nil {
				t.Fatalf("Format = %q, want an error", out)
			}
			var ce *typefmt.CommentError
			if errors.As(err, &ce) || errors.Is(err, typefmt.ErrChanged) {
				t.Errorf("error = %v, want a YAML error", err)
			}
			if out != nil {
				t.Errorf("Format returned output with its error: %q", out)
			}
		})
	}
}
