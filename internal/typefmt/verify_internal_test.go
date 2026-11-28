package typefmt

import (
	"errors"
	"testing"
)

// TestVerify_TheGate: the gate Format runs before it returns. A layout
// change passes; a change in value or a lost comment is refused.
func TestVerify_TheGate(t *testing.T) {
	const in = "# head\nid: t\nversion: 1\nfields: [{id: f, type: int}]  # the fields\nreactions:\n  - {id: a, fields: &x {f: 'v'}}\n  - {id: b, fields: *x}\n"
	passes := map[string]string{
		"the same bytes": in,
		"another layout and key order": "# head\nid: t\nversion: 1\n\nfields:  # the fields\n  - type: int\n    id: f\n\n" +
			"reactions:\n  - id: a\n    fields: &x\n      f: 'v'\n  - id: b\n    fields: *x\n",
	}
	for name, out := range passes {
		t.Run(name, func(t *testing.T) {
			if err := verify([]byte(in), []byte(out)); err != nil {
				t.Errorf("verify = %v, want nil", err)
			}
		})
	}

	changed := map[string]string{
		"a quoted number unquoted": "# head\nid: t\nversion: '1'\nfields: [{id: f, type: int}]  # the fields\nreactions:\n  - {id: a, fields: &x {f: 'v'}}\n  - {id: b, fields: *x}\n",
		"a list in another order":  "# head\nid: t\nversion: 1\nfields: [{id: f, type: int}]  # the fields\nreactions:\n  - {id: b, fields: &x {f: 'v'}}\n  - {id: a, fields: *x}\n",
		"a key dropped":            "# head\nid: t\nfields: [{id: f, type: int}]  # the fields\nreactions:\n  - {id: a, fields: &x {f: 'v'}}\n  - {id: b, fields: *x}\n",
		"an alias expanded wrong":  "# head\nid: t\nversion: 1\nfields: [{id: f, type: int}]  # the fields\nreactions:\n  - {id: a, fields: {f: 'v'}}\n  - {id: b, fields: {f: 'w'}}\n",
	}
	for name, out := range changed {
		t.Run(name, func(t *testing.T) {
			if err := verify([]byte(in), []byte(out)); !errors.Is(err, ErrChanged) {
				t.Errorf("verify = %v, want ErrChanged", err)
			}
		})
	}

	t.Run("a comment lost", func(t *testing.T) {
		out := "# head\nid: t\nversion: 1\nfields: [{id: f, type: int}]\nreactions:\n  - {id: a, fields: &x {f: 'v'}}\n  - {id: b, fields: *x}\n"
		err := verify([]byte(in), []byte(out))
		var ce *CommentError
		if !errors.As(err, &ce) {
			t.Fatalf("verify = %v, want a *CommentError", err)
		}
		if ce.Line != 4 || ce.Text != "# the fields" {
			t.Errorf("CommentError = line %d %q, want line 4 %q", ce.Line, ce.Text, "# the fields")
		}
	})
}
