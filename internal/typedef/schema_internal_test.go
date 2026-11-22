// Tests for Ventoux V2c: the Type format as JSON Schema, written by hand in
// schema/type.schema.json (draft 2020-12) and checked here, in tests only.
// The schema must never be stricter than typedef: whatever typedef loads,
// the schema accepts.
//
// This file is in package typedef, not typedef_test: an internal test sees
// unexported names (the stage (a) walker's key table, yamlKeys; the list of
// categories), like a Java test in the same package. The schema is checked
// against them rather than against a copy.
//
//   - The schema compiles, and uses only the keywords below: keys, shapes,
//     enum and required. Nothing like pattern or minimum can creep in.
//   - Its keys are typedef's keys, both ways, at every level (a key the
//     schema lacks would make it stricter; one typedef lacks would invite a
//     typo).
//   - Shapes: every value stage (a) accepts at a position, the schema
//     accepts there. Stage (a) lets null through everywhere, and any scalar
//     where typedef reads a string (id: 5 loads), so the schema does too.
//   - enum only at states[].category and fields[].type, each equal to
//     typedef's list; required only for id and version. typedef refuses a
//     value outside each list, and null, and a document without id or
//     version, so these make the schema no stricter.
//   - Every file in types/ validates, and so does every file of a corpus
//     (testdata/schema) that, with types/, uses every key and both shapes
//     of accepts_reactions and writable_by. Each corpus file loads.
package typedef

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"go.yaml.in/yaml/v3"
)

const schemaFile = "../../schema/type.schema.json"

// allowedKeywords are the only JSON Schema keywords the schema may use.
var allowedKeywords = []string{
	"$schema", "$id", "$comment", "title", "description", "$defs", "$ref",
	"type", "properties", "additionalProperties", "items", "oneOf", "anyOf",
	"enum", "required",
}

// schemaJSON is the schema as plain JSON, to walk.
func schemaJSON(t *testing.T) map[string]any {
	t.Helper()
	data, err := os.ReadFile(schemaFile)
	if err != nil {
		t.Fatalf("read the schema: %v", err)
	}
	var s map[string]any
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatalf("the schema is not JSON: %v", err)
	}
	return s
}

// compiled is the schema, compiled by santhosh-tekuri/jsonschema.
func compiled(t *testing.T) *jsonschema.Schema {
	t.Helper()
	data, err := os.ReadFile(schemaFile)
	if err != nil {
		t.Fatalf("read the schema: %v", err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("the schema is not JSON: %v", err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource("type.schema.json", doc); err != nil {
		t.Fatal(err)
	}
	sch, err := c.Compile("type.schema.json")
	if err != nil {
		t.Fatalf("the schema does not compile: %v", err)
	}
	return sch
}

// asJSON turns a YAML document into the JSON value the schema checks:
// decoded by yaml.v3 (anchors, aliases and merge keys expanded), then
// encoded as JSON.
func asJSON(t *testing.T, yamlDoc []byte) any {
	t.Helper()
	var v any
	if err := yaml.Unmarshal(yamlDoc, &v); err != nil {
		t.Fatalf("not YAML: %v", err)
	}
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("not JSON-able: %v", err)
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	return inst
}

func TestSchema_Compiles(t *testing.T) {
	s := schemaJSON(t)
	if s["$schema"] != "https://json-schema.org/draft/2020-12/schema" {
		t.Errorf("$schema = %v, want draft 2020-12", s["$schema"])
	}
	compiled(t)
}

// Every keyword the schema uses is one of allowedKeywords. Property names
// (under properties) and definition names (under $defs) are not keywords.
func TestSchema_OnlyTheseKeywords(t *testing.T) {
	var walk func(node any, where string)
	walk = func(node any, where string) {
		m, ok := node.(map[string]any)
		if !ok {
			return
		}
		for _, k := range slices.Sorted(maps.Keys(m)) {
			v := m[k]
			if !slices.Contains(allowedKeywords, k) {
				t.Errorf("%s uses keyword %q; the schema may use only %v", where, k, allowedKeywords)
				continue
			}
			switch k {
			case "properties", "$defs":
				sub, _ := v.(map[string]any)
				for _, name := range slices.Sorted(maps.Keys(sub)) {
					walk(sub[name], where+"/"+k+"/"+name)
				}
			case "items", "additionalProperties":
				walk(v, where+"/"+k)
			case "oneOf", "anyOf":
				list, _ := v.([]any)
				for i, b := range list {
					walk(b, fmt.Sprintf("%s/%s/%d", where, k, i))
				}
			}
		}
	}
	walk(schemaJSON(t), "#")
}

// typeFiles of types/ and its subfolders, and of the corpus.
func repoTypeFiles(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, p := range []string{"../../types/*.yaml", "../../types/*/*.yaml"} {
		files, err := filepath.Glob(p)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, files...)
	}
	if len(out) < 40 {
		t.Fatalf("found %d files in types/; the test is not looking where it should", len(out))
	}
	return out
}

func corpusFiles(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob("testdata/schema/*.yaml")
	if err != nil || len(files) == 0 {
		t.Fatalf("no corpus in testdata/schema (%v)", err)
	}
	return files
}

func TestSchema_TheRepositoryTypesValidate(t *testing.T) {
	sch := compiled(t)
	for _, f := range repoTypeFiles(t) {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if err := sch.Validate(asJSON(t, data)); err != nil {
			t.Errorf("%s does not validate: %v", f, err)
		}
	}
}

// The corpus loads (stages a and b: each file alone) and validates.
func TestSchema_TheCorpusLoadsAndValidates(t *testing.T) {
	sch := compiled(t)
	for _, f := range corpusFiles(t) {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Parse(data); err != nil {
			t.Errorf("%s does not load:\n%v", f, err)
		}
		if err := sch.Validate(asJSON(t, data)); err != nil {
			t.Errorf("%s does not validate: %v", f, err)
		}
	}
}

// ---------------------------------------------------------------------------
// Walking typedef's structs (as stage a does) beside the schema.
// ---------------------------------------------------------------------------

// node resolves a schema node's $ref (local, #/$defs/<name>).
func resolve(t *testing.T, root map[string]any, node any) map[string]any {
	t.Helper()
	for range 10 {
		m, ok := node.(map[string]any)
		if !ok {
			t.Fatalf("schema node %v is not an object", node)
		}
		ref, ok := m["$ref"].(string)
		if !ok {
			return m
		}
		name, found := strings.CutPrefix(ref, "#/$defs/")
		if !found {
			t.Fatalf("$ref %q: only #/$defs/<name> is supported here", ref)
		}
		defs, _ := root["$defs"].(map[string]any)
		node = defs[name]
	}
	t.Fatal("$ref chain too long")
	return nil
}

// branches is a node's alternatives: its oneOf or anyOf branches, else
// itself.
func branches(t *testing.T, root map[string]any, node any) []map[string]any {
	t.Helper()
	m := resolve(t, root, node)
	for _, k := range []string{"oneOf", "anyOf"} {
		if list, ok := m[k].([]any); ok {
			var out []map[string]any
			for _, b := range list {
				out = append(out, branches(t, root, b)...)
			}
			return out
		}
	}
	return []map[string]any{m}
}

// typesOf returns a schema node's "type" as a list.
func typesOf(m map[string]any) []string {
	switch v := m["type"].(type) {
	case string:
		return []string{v}
	case []any:
		var out []string
		for _, s := range v {
			out = append(out, s.(string))
		}
		return out
	}
	return nil
}

// branchOf returns the alternative whose type includes want ("object",
// "array"), or nil.
func branchOf(t *testing.T, root map[string]any, node any, want string) map[string]any {
	t.Helper()
	for _, b := range branches(t, root, node) {
		if slices.Contains(typesOf(b), want) {
			return b
		}
	}
	return nil
}

// TestSchema_KeysAreTypedefs: at each struct, the schema's properties are
// exactly the struct's yaml keys, and no other key is allowed.
func TestSchema_KeysAreTypedefs(t *testing.T) {
	root := schemaJSON(t)
	seen := map[reflect.Type]bool{}
	var walk func(rt reflect.Type, node any, path string)
	walk = func(rt reflect.Type, node any, path string) {
		for rt.Kind() == reflect.Pointer {
			rt = rt.Elem()
		}
		switch {
		case rt == writableByType:
			return // its shapes: TestSchema_NeverStricterThanStageA
		case rt == acceptedType:
			if branchOf(t, root, node, "array") == nil {
				t.Errorf("%s: no list form (accepts_reactions takes a list of fields)", path)
			}
			walk(acceptedDocTyp, node, path)
			return
		}
		switch rt.Kind() {
		case reflect.Struct:
			obj := branchOf(t, root, node, "object")
			if obj == nil {
				t.Errorf("%s: the schema has no object here", path)
				return
			}
			if obj["additionalProperties"] != false {
				t.Errorf("%s: additionalProperties = %v, want false (typedef refuses unknown keys)", path, obj["additionalProperties"])
			}
			props, _ := obj["properties"].(map[string]any)
			want := yamlKeys(rt)
			for _, k := range slices.Sorted(maps.Keys(props)) {
				if _, ok := want[k]; !ok {
					t.Errorf("%s: the schema allows %q, which typedef does not know", path, k)
				}
			}
			if seen[rt] {
				return // a struct met again (recursion): its keys were compared once
			}
			seen[rt] = true
			for _, k := range slices.Sorted(maps.Keys(want)) {
				sub, ok := props[k]
				if !ok {
					t.Errorf("%s: the schema lacks %q, which typedef knows", path, k)
					continue
				}
				walk(want[k].Type, sub, path+"."+k)
			}
		case reflect.Slice:
			arr := branchOf(t, root, node, "array")
			if arr == nil {
				t.Errorf("%s: the schema has no list here", path)
				return
			}
			walk(rt.Elem(), arr["items"], path+"[]")
		case reflect.Map:
			obj := branchOf(t, root, node, "object")
			if obj == nil {
				t.Errorf("%s: the schema has no map here", path)
				return
			}
			if _, ok := obj["properties"]; ok {
				t.Errorf("%s: a map with properties; its keys are the author's", path)
			}
			ap, ok := obj["additionalProperties"]
			if !ok || ap == false {
				t.Errorf("%s: a map needs additionalProperties: <schema>", path)
				return
			}
			walk(rt.Elem(), ap, path+"{}")
		}
	}
	walk(reflect.TypeFor[Type](), root, "")
}

// ---------------------------------------------------------------------------
// Shapes: every value stage (a) accepts, the schema accepts.
// ---------------------------------------------------------------------------

// slot is one place a value can go: the path through the structs, as
// steps (a key, "[]" for a list item, "{}" for a map value), and its Go
// type.
type slot struct {
	steps []string
	rt    reflect.Type
}

func (p slot) String() string {
	var b strings.Builder
	for _, s := range p.steps {
		if s == "[]" || s == "{}" {
			b.WriteString(s)
		} else {
			b.WriteString("." + s)
		}
	}
	return b.String()
}

// positions lists every position below the Type, each struct type
// expanded once per path prefix, to a depth that covers the format.
func positions() []slot {
	var out []slot
	var walk func(rt reflect.Type, steps []string, depth int)
	walk = func(rt reflect.Type, steps []string, depth int) {
		for rt.Kind() == reflect.Pointer {
			rt = rt.Elem()
		}
		out = append(out, slot{slices.Clone(steps), rt})
		if depth > 6 || rt == writableByType {
			return
		}
		if rt == acceptedType {
			rt = acceptedDocTyp
		}
		switch rt.Kind() {
		case reflect.Struct:
			keys := yamlKeys(rt)
			for _, k := range slices.Sorted(maps.Keys(keys)) {
				walk(keys[k].Type, append(steps, k), depth+1)
			}
		case reflect.Slice:
			walk(rt.Elem(), append(steps, "[]"), depth+1)
		case reflect.Map:
			walk(rt.Elem(), append(steps, "{}"), depth+1)
		}
	}
	walk(reflect.TypeFor[Type](), nil, 0)
	return out[1:] // not the document itself
}

// place builds a document with value at the position: every list on the
// way has one item, every map one entry ("k"), and the document has id and
// version.
func place(steps []string, value any) map[string]any {
	var build func(i int) any
	build = func(i int) any {
		if i == len(steps) {
			return value
		}
		switch steps[i] {
		case "[]":
			return []any{build(i + 1)}
		case "{}":
			return map[string]any{"k": build(i + 1)}
		default:
			return map[string]any{steps[i]: build(i + 1)}
		}
	}
	doc := build(0).(map[string]any)
	if _, ok := doc["id"]; !ok {
		doc["id"] = "memo"
	}
	if _, ok := doc["version"]; !ok {
		doc["version"] = 1
	}
	return doc
}

// candidates are the values tried at every position.
var candidates = []any{
	nil, "text", 7, true, 2.5,
	[]any{"a"}, []any{map[string]any{"k": "v"}},
	map[string]any{"k": "v"}, map[string]any{},
	"kernel", []any{"role:clerk"},
}

// enumAt returns the enum the schema puts at a position, if any.
func enumAt(t *testing.T, root map[string]any, steps []string) []any {
	t.Helper()
	node := any(root)
	for _, s := range steps {
		var next any
		switch s {
		case "[]":
			if arr := branchOf(t, root, node, "array"); arr != nil {
				next = arr["items"]
			}
		case "{}":
			if obj := branchOf(t, root, node, "object"); obj != nil {
				next = obj["additionalProperties"]
			}
		default:
			if obj := branchOf(t, root, node, "object"); obj != nil {
				props, _ := obj["properties"].(map[string]any)
				next = props[s]
			}
		}
		if next == nil {
			return nil
		}
		node = next
	}
	for _, b := range branches(t, root, node) {
		if e, ok := b["enum"].([]any); ok {
			return e
		}
	}
	return nil
}

// TestSchema_NeverStricterThanStageA: at every position, every candidate
// that stage (a) lets through, the schema accepts. Positions with an enum,
// and id and version themselves, are TestSchema_EnumsAreTypedefs's and
// TestSchema_RequiredIsTypedefs's: typedef refuses more there (stage b).
func TestSchema_NeverStricterThanStageA(t *testing.T) {
	root, sch := schemaJSON(t), compiled(t)
	checked := 0
	for _, pos := range positions() {
		if pos.String() == ".id" || pos.String() == ".version" || enumAt(t, root, pos.steps) != nil {
			continue
		}
		for _, v := range candidates {
			doc := place(pos.steps, v)
			data, err := yaml.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			if _, ps := readYAML(data); len(ps) > 0 {
				continue // stage (a) refuses it too
			}
			checked++
			if err := sch.Validate(asJSON(t, data)); err != nil {
				t.Errorf("%s = %#v: stage (a) accepts it, the schema does not: %v\n%s", pos, v, err, data)
			}
		}
	}
	if checked < 500 {
		t.Errorf("checked %d values; the test is not looking where it should", checked)
	}
}

// fieldTypes is typedef's closed list of field types (checkField's switch).
var fieldTypes = []FieldType{String, Int, Bool, Enum, Principal, Date, DateTime}

// TestSchema_EnumsAreTypedefs: the schema has enums at states[].category
// and fields[].type only, each exactly typedef's list; typedef loads each
// value and refuses one outside the list, and null.
func TestSchema_EnumsAreTypedefs(t *testing.T) {
	root := schemaJSON(t)
	type enumCase struct {
		steps []string
		list  []string
		doc   func(v any) map[string]any
	}
	cases := map[string]enumCase{
		".states[].category": {
			steps: []string{"states", "[]", "category"},
			list:  categories,
			doc: func(v any) map[string]any {
				return map[string]any{"id": "memo", "version": 1, "initial_state": "open",
					"states": []any{map[string]any{"id": "open", "category": v}}}
			},
		},
		".fields[].type": {
			steps: []string{"fields", "[]", "type"},
			list: func() []string {
				var out []string
				for _, ft := range fieldTypes {
					out = append(out, string(ft))
				}
				return out
			}(),
			doc: func(v any) map[string]any {
				f := map[string]any{"id": "f_x", "type": v}
				if v == string(Enum) {
					f["options"] = []any{"a"}
				}
				return map[string]any{"id": "memo", "version": 1, "fields": []any{f}}
			},
		},
	}
	// Every enum in the schema is at one of these positions.
	found := 0
	for _, pos := range positions() {
		e := enumAt(t, root, pos.steps)
		if e == nil {
			continue
		}
		found++
		c, ok := cases[pos.String()]
		if !ok {
			t.Errorf("%s: an enum where typedef has no closed list in this test; add its list here first", pos)
			continue
		}
		var got []string
		for _, v := range e {
			s, _ := v.(string)
			got = append(got, s)
		}
		if !slices.Equal(slices.Sorted(slices.Values(got)), slices.Sorted(slices.Values(c.list))) {
			t.Errorf("%s: enum %v, want typedef's %v", pos, got, c.list)
		}
	}
	if found != len(cases) {
		t.Errorf("found %d enums, want %d (%v)", found, len(cases), slices.Sorted(maps.Keys(cases)))
	}
	// The lists are closed: typedef loads each value, and refuses others.
	for name, c := range cases {
		for _, v := range c.list {
			data, _ := yaml.Marshal(c.doc(v))
			if _, err := Parse(data); err != nil {
				t.Errorf("%s = %s: %v", name, v, err)
			}
		}
		for _, v := range []any{"not_in_the_list", nil} {
			data, _ := yaml.Marshal(c.doc(v))
			if _, err := Parse(data); err == nil {
				t.Errorf("%s = %#v: typedef loads it; an enum without it would be stricter", name, v)
			}
		}
	}
}

// TestSchema_RequiredIsTypedefs: required appears once, on the document,
// naming id and version, and typedef refuses a document without either,
// or with either null.
func TestSchema_RequiredIsTypedefs(t *testing.T) {
	root := schemaJSON(t)
	count := 0
	var walk func(node any)
	walk = func(node any) {
		switch v := node.(type) {
		case map[string]any:
			if _, ok := v["required"]; ok {
				count++
			}
			for _, sub := range v {
				walk(sub)
			}
		case []any:
			for _, sub := range v {
				walk(sub)
			}
		}
	}
	walk(root)
	req, _ := root["required"].([]any)
	if count != 1 || !slices.Equal(req, []any{"id", "version"}) {
		t.Errorf("required: %d uses, on the document %v; want one, [id version]", count, req)
	}
	for _, doc := range []string{"version: 1\n", "id: ~\nversion: 1\n", "id: memo\n", "id: memo\nversion: ~\n"} {
		if _, err := Parse([]byte(doc)); err == nil {
			t.Errorf("typedef loads %q; required would be stricter", doc)
		}
	}
}

// ---------------------------------------------------------------------------
// The corpus: with types/, every key and both shapes of the two keys that
// take two.
// ---------------------------------------------------------------------------

// used walks a document beside the structs and records each position it
// uses (as slot.String() names it), plus "<pos> as list" / "as map" for
// accepts_reactions and "as scalar" / "as list" for writable_by.
func used(n *yaml.Node, rt reflect.Type, path string, out map[string]bool) {
	n = resolveAlias(n)
	if n == nil || n.Tag == "!!null" {
		return
	}
	for rt.Kind() == reflect.Pointer {
		rt = rt.Elem()
	}
	if path != "" {
		out[path] = true
	}
	switch {
	case rt == writableByType:
		if n.Kind == yaml.SequenceNode {
			out[path+" as list"] = true
		} else {
			out[path+" as scalar"] = true
		}
		return
	case rt == acceptedType:
		if n.Kind == yaml.SequenceNode {
			out[path+" as list"] = true
			used(n, reflect.TypeFor[[]string](), path, out)
			return
		}
		out[path+" as map"] = true
		rt = acceptedDocTyp
	}
	switch rt.Kind() {
	case reflect.Struct:
		keys := yamlKeys(rt)
		for _, kv := range mappingPairs(n) {
			if f, ok := keys[kv[0].Value]; ok {
				used(kv[1], f.Type, path+"."+kv[0].Value, out)
			}
		}
	case reflect.Slice:
		if n.Kind == yaml.SequenceNode {
			for _, item := range n.Content {
				used(item, rt.Elem(), path+"[]", out)
			}
		}
	case reflect.Map:
		for _, kv := range mappingPairs(n) {
			used(kv[1], rt.Elem(), path+"{}", out)
		}
	}
}

func TestSchema_TheCorpusUsesEveryKey(t *testing.T) {
	got := map[string]bool{}
	for _, f := range append(repoTypeFiles(t), corpusFiles(t)...) {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var doc yaml.Node
		if err := yaml.Unmarshal(data, &doc); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		used(doc.Content[0], reflect.TypeFor[Type](), "", got)
	}
	var want []string
	for _, pos := range positions() {
		// A struct met again deeper down (positions expands to a depth) is
		// the same keys: the shallowest use counts.
		if len(pos.steps) > 99 {
			continue
		}
		want = append(want, pos.String())
		switch pos.rt {
		case writableByType:
			want = append(want, pos.String()+" as scalar", pos.String()+" as list")
		case acceptedType:
			want = append(want, pos.String()+" as list", pos.String()+" as map")
		}
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("no file of types/ or testdata/schema uses %s", w)
		}
	}
}
