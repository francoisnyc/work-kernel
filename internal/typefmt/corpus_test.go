package typefmt_test

import (
	"bytes"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/francoisnyc/work-kernel/internal/typedef"
	"github.com/francoisnyc/work-kernel/internal/typefmt"
)

// corpus is every Type file of the repository: types/, the bake-offs'
// Types and typedef's schema corpus.
func corpus(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, g := range []struct {
		pattern string
		atLeast int
	}{
		{"../../types/*.yaml", 40},
		{"../../types/*/*.yaml", 5},
		{"../../spikes/*/kernel*/types*/*.yaml", 4},
		{"../../internal/typedef/testdata/schema/*.yaml", 5},
	} {
		files, err := filepath.Glob(g.pattern)
		if err != nil {
			t.Fatal(err)
		}
		if len(files) < g.atLeast {
			t.Fatalf("%s: %d files, want at least %d; the test is not looking where it should", g.pattern, len(files), g.atLeast)
		}
		out = append(out, files...)
	}
	return out
}

// formatted is a corpus file before and after Format.
func formatted(t *testing.T, path string) (before, after []byte) {
	t.Helper()
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	after, err = typefmt.Format(before)
	if err != nil {
		t.Fatalf("Format: %v", err)
	}
	return before, after
}

// TestCorpus_TheDataIsTheSame: each file decodes to the same values before
// and after (anchors and aliases expanded).
func TestCorpus_TheDataIsTheSame(t *testing.T) {
	for _, path := range corpus(t) {
		t.Run(path, func(t *testing.T) {
			before, after := formatted(t, path)
			var a, b any
			if err := yaml.Unmarshal(before, &a); err != nil {
				t.Fatalf("test bug: %v", err)
			}
			if err := yaml.Unmarshal(after, &b); err != nil {
				t.Fatalf("the formatted file is not YAML: %v", err)
			}
			if !reflect.DeepEqual(a, b) {
				t.Errorf("the data changed:\n%s", after)
			}
		})
	}
}

// TestCorpus_TheTypeIsTheSame: typedef reads the same Type from each file
// before and after, in the form RegisterType stores (its JSON), which is
// what VERSION_CHANGED compares.
func TestCorpus_TheTypeIsTheSame(t *testing.T) {
	for _, path := range corpus(t) {
		t.Run(path, func(t *testing.T) {
			before, after := formatted(t, path)
			tb, err := typedef.Parse(before)
			if err != nil {
				t.Fatalf("test bug: the file does not load: %v", err)
			}
			ta, err := typedef.Parse(after)
			if err != nil {
				t.Fatalf("the formatted file does not load: %v", err)
			}
			jb, err := json.Marshal(tb)
			if err != nil {
				t.Fatal(err)
			}
			ja, err := json.Marshal(ta)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(jb, ja) {
				t.Errorf("the stored definition changed:\n--- before\n%s\n--- after\n%s", jb, ja)
			}
		})
	}
}

// TestCorpus_TheCommentsAreTheSame: every comment is kept, word for word,
// as yaml.v3 reads them; and every comment line of the file is still
// there, in the same order within its section (the header, or a top-level
// key's block): sections move to the canonical key order, their comments
// with them.
func TestCorpus_TheCommentsAreTheSame(t *testing.T) {
	for _, path := range corpus(t) {
		t.Run(path, func(t *testing.T) {
			before, after := formatted(t, path)
			if b, a := nodeComments(t, before), nodeComments(t, after); !slices.Equal(b, a) {
				t.Errorf("the comments changed:\n--- before\n%s\n--- after\n%s", strings.Join(b, "\n"), strings.Join(a, "\n"))
			}
			b, a := sectionComments(before), sectionComments(after)
			for _, k := range slices.Sorted(maps.Keys(b)) {
				if !slices.Equal(b[k], a[k]) {
					t.Errorf("the comment lines of section %q changed:\n--- before\n%s\n--- after\n%s", k, strings.Join(b[k], "\n"), strings.Join(a[k], "\n"))
				}
			}
			for _, k := range slices.Sorted(maps.Keys(a)) {
				if _, ok := b[k]; !ok {
					t.Errorf("section %q has comments it did not have: %s", k, strings.Join(a[k], "\n"))
				}
			}
		})
	}
}

// TestCorpus_FormattingTwiceChangesNothing.
func TestCorpus_FormattingTwiceChangesNothing(t *testing.T) {
	for _, path := range corpus(t) {
		t.Run(path, func(t *testing.T) {
			_, once := formatted(t, path)
			twice, err := typefmt.Format(once)
			if err != nil {
				t.Fatalf("Format of the formatted: %v", err)
			}
			if !bytes.Equal(once, twice) {
				t.Errorf("formatting twice changed it:\n--- once\n%s--- twice\n%s", once, twice)
			}
		})
	}
}

// nodeComments are the comment lines yaml.v3 attaches to the nodes of
// src, trimmed and sorted.
func nodeComments(t *testing.T, src []byte) []string {
	t.Helper()
	var doc yaml.Node
	if err := yaml.Unmarshal(src, &doc); err != nil {
		t.Fatalf("not YAML: %v", err)
	}
	var out []string
	var walk func(n *yaml.Node)
	walk = func(n *yaml.Node) {
		for _, c := range []string{n.HeadComment, n.LineComment, n.FootComment} {
			for _, l := range strings.Split(c, "\n") {
				if l = strings.TrimSpace(l); l != "" {
					out = append(out, l)
				}
			}
		}
		for _, c := range n.Content {
			walk(c)
		}
	}
	walk(&doc)
	slices.Sort(out)
	return out
}

// sectionComments are the lines of src that hold only a comment, trimmed,
// in order, by section: "" for the header, else the top-level key whose
// block they are in. Comment lines right above a top-level key, with no
// blank line between, are that key's; others belong to the section they
// follow.
func sectionComments(src []byte) map[string][]string {
	out := map[string][]string{}
	section := ""
	var waiting []string // comment lines not yet known to be the next key's
	for _, l := range strings.Split(string(src), "\n") {
		trimmed := strings.TrimSpace(l)
		switch {
		case strings.HasPrefix(trimmed, "#"):
			waiting = append(waiting, trimmed)
		case trimmed == "":
			out[section] = append(out[section], waiting...)
			waiting = nil
		case l[0] != ' ' && strings.Contains(l, ":"):
			section = strings.TrimSpace(strings.SplitN(l, ":", 2)[0])
			out[section] = append(out[section], waiting...)
			waiting = nil
		default:
			out[section] = append(out[section], waiting...)
			waiting = nil
		}
	}
	out[section] = append(out[section], waiting...)
	for k, v := range out {
		if len(v) == 0 {
			delete(out, k)
		}
	}
	return out
}
