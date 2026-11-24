package reading_test

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/reading"
)

// schemaKeys walks schema/type.schema.json (which V2c holds equal to the
// keys typedef knows) and returns every leaf key's path: "id",
// "transitions[].guard", "states[].timer.after", "accepts_reactions.*.moves".
// A key whose value is a map of free names (sets, add, fields, fallback,
// offers) is a leaf.
func schemaKeys(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile("../../schema/type.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatal(err)
	}
	defs, _ := root["$defs"].(map[string]any)
	var keys []string
	var walk func(node map[string]any, path string)
	resolve := func(node map[string]any) map[string]any {
		if ref, ok := node["$ref"].(string); ok {
			d, _ := defs[strings.TrimPrefix(ref, "#/$defs/")].(map[string]any)
			return d
		}
		return node
	}
	walk = func(node map[string]any, path string) {
		node = resolve(node)
		if props, ok := node["properties"].(map[string]any); ok {
			for name, p := range props {
				child := name
				if path != "" {
					child = path + "." + name
				}
				walk(p.(map[string]any), child)
			}
			return
		}
		if items, ok := node["items"].(map[string]any); ok {
			if inner := resolve(items); inner["properties"] != nil {
				walk(inner, path+"[]")
				return
			}
		}
		for _, alt := range []string{"anyOf", "oneOf"} {
			if list, ok := node[alt].([]any); ok {
				found := false
				for _, a := range list {
					if inner := resolve(a.(map[string]any)); inner["properties"] != nil {
						walk(inner, path)
						found = true
					}
				}
				if found {
					return
				}
			}
		}
		if ap, ok := node["additionalProperties"].(map[string]any); ok {
			if inner := resolve(ap); inner["properties"] != nil || inner["anyOf"] != nil {
				before := len(keys)
				walk(inner, path+".*")
				if len(keys) > before {
					return
				}
			}
		}
		keys = append(keys, path)
	}
	walk(root, "")
	sort.Strings(keys)
	return keys
}

// Every key of the Type format is read by a part, or left out with a
// reason, never both; and the table names no key the format lacks.
func TestEveryKeyIsReadOrLeftOut(t *testing.T) {
	keys := schemaKeys(t)
	if len(keys) < 80 {
		t.Fatalf("found %d keys in the schema; the walk is wrong: %v", len(keys), keys)
	}
	parts := reading.Parts()
	for _, k := range keys {
		ps, read := reading.KeyParts[k]
		reason, left := reading.LeftOut[k]
		switch {
		case read && left:
			t.Errorf("%s is both read and left out", k)
		case !read && !left:
			t.Errorf("%s is neither read by a part nor left out with a reason", k)
		case left && strings.TrimSpace(reason) == "":
			t.Errorf("%s is left out without a reason", k)
		case read && len(ps) == 0:
			t.Errorf("%s is read by no part", k)
		}
		for _, p := range ps {
			if !slices.Contains(parts, p) {
				t.Errorf("%s is read by %q, which is no part", k, p)
			}
		}
	}
	for k := range reading.KeyParts {
		if !slices.Contains(keys, k) {
			t.Errorf("KeyParts names %s, which is no key of the Type format", k)
		}
	}
	for k := range reading.LeftOut {
		if !slices.Contains(keys, k) {
			t.Errorf("LeftOut names %s, which is no key of the Type format", k)
		}
	}
}

// Every part reads at least one key: a part no key feeds says nothing
// about the Type.
func TestEveryPartReadsAKey(t *testing.T) {
	fed := map[reading.Part]bool{}
	for _, ps := range reading.KeyParts {
		for _, p := range ps {
			fed[p] = true
		}
	}
	for _, p := range reading.Parts() {
		if !fed[p] {
			t.Errorf("part %s reads no key of the Type format", p)
		}
	}
}

// A source walk: every Part constant the package declares is on the closed
// list, so a new kind of sentence cannot be rendered without a witness.
func TestEveryDeclaredPartIsListed(t *testing.T) {
	fset := token.NewFileSet()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	listed := map[string]bool{}
	for _, p := range reading.Parts() {
		listed[string(p)] = true
	}
	found := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, spec := range gd.Specs {
				vs := spec.(*ast.ValueSpec)
				if id, ok := vs.Type.(*ast.Ident); !ok || id.Name != "Part" {
					continue
				}
				for _, v := range vs.Values {
					lit, ok := v.(*ast.BasicLit)
					if !ok {
						continue
					}
					found++
					if value, _ := strconv.Unquote(lit.Value); !listed[value] {
						t.Errorf("%s declares the part %s, which Parts() does not list", fset.Position(lit.Pos()), lit.Value)
					}
				}
			}
		}
	}
	if found != len(reading.Parts()) {
		t.Errorf("found %d Part constants, and Parts() lists %d", found, len(reading.Parts()))
	}
}
