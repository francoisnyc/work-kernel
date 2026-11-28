package typefmt_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/typefmt"
)

// TestKeys_AreTheSchemasInItsOrder: Keys holds, at every path, the
// properties schema/type.schema.json declares there, in the order it
// declares them, and no other path. (typedef's own test,
// TestTypefmtKeys_AreTypedefs, holds Keys to typedef's keys.)
func TestKeys_AreTheSchemasInItsOrder(t *testing.T) {
	want := schemaKeys(t)
	if len(want) < 15 {
		t.Fatalf("found %d mappings in the schema; the walk is not looking where it should", len(want))
	}
	for _, path := range slices.Sorted(maps.Keys(want)) {
		if got, ok := typefmt.Keys[path]; !ok {
			t.Errorf("Keys lacks %q: want %v", path, want[path])
		} else if !slices.Equal(got, want[path]) {
			t.Errorf("Keys[%q] =\n  %v\nwant the schema's\n  %v", path, got, want[path])
		}
	}
	for _, path := range slices.Sorted(maps.Keys(typefmt.Keys)) {
		if _, ok := want[path]; !ok {
			t.Errorf("Keys has %q, which the schema does not declare", path)
		}
	}
}

// node is a JSON value that keeps its object keys in order.
type node struct {
	keys   []string         // an object's keys, in order
	fields map[string]*node // an object's values
	items  []*node          // an array's
	value  any              // anything else
}

func decode(d *json.Decoder) (*node, error) {
	tok, err := d.Token()
	if err != nil {
		return nil, err
	}
	switch tok {
	case json.Delim('{'):
		n := &node{fields: map[string]*node{}}
		for d.More() {
			k, err := d.Token()
			if err != nil {
				return nil, err
			}
			v, err := decode(d)
			if err != nil {
				return nil, err
			}
			n.keys = append(n.keys, k.(string))
			n.fields[k.(string)] = v
		}
		_, err := d.Token()
		return n, err
	case json.Delim('['):
		n := &node{}
		for d.More() {
			v, err := decode(d)
			if err != nil {
				return nil, err
			}
			n.items = append(n.items, v)
		}
		_, err := d.Token()
		return n, err
	}
	return &node{value: tok}, nil
}

// schemaKeys walks the schema as Keys names its paths: each object with
// properties is a mapping of the format; "[]" steps into a list's items,
// "{}" into a map's values; $ref, oneOf and anyOf are followed.
func schemaKeys(t *testing.T) map[string][]string {
	t.Helper()
	data, err := os.ReadFile("../../schema/type.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	root, err := decode(json.NewDecoder(bytes.NewReader(data)))
	if err != nil {
		t.Fatal(err)
	}
	defs := root.fields["$defs"]
	resolve := func(n *node) *node {
		for n.fields["$ref"] != nil {
			ref := fmt.Sprint(n.fields["$ref"].value)
			name, ok := strings.CutPrefix(ref, "#/$defs/")
			if !ok || defs.fields[name] == nil {
				t.Fatalf("a $ref the walk cannot follow: %s", ref)
			}
			n = defs.fields[name]
		}
		return n
	}
	var branches func(n *node) []*node
	branches = func(n *node) []*node {
		n = resolve(n)
		var out []*node
		for _, k := range []string{"oneOf", "anyOf"} {
			if b := n.fields[k]; b != nil {
				for _, item := range b.items {
					out = append(out, branches(item)...)
				}
			}
		}
		if out == nil {
			out = []*node{n}
		}
		return out
	}
	is := func(n *node, kind string) bool {
		ty := n.fields["type"]
		if ty == nil {
			return false
		}
		if ty.value == kind {
			return true
		}
		return slices.ContainsFunc(ty.items, func(i *node) bool { return i.value == kind })
	}
	out := map[string][]string{}
	var walk func(n *node, path string)
	walk = func(n *node, path string) {
		for _, b := range branches(n) {
			if is(b, "object") {
				if props := b.fields["properties"]; props != nil {
					if _, seen := out[path]; !seen {
						out[path] = props.keys
						for _, k := range props.keys {
							walk(props.fields[k], path+"."+k)
						}
					}
				} else if ap := b.fields["additionalProperties"]; ap != nil && ap.fields != nil {
					walk(ap, path+"{}")
				}
			}
			if is(b, "array") && b.fields["items"] != nil {
				walk(b.fields["items"], path+"[]")
			}
		}
	}
	walk(root, "")
	return out
}
