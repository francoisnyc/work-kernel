package typedef

import (
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Stage (a): the YAML. readYAML parses a document into a node tree, then
// walks the tree against the Type's Go structs (their yaml tags, found by
// reflection) and reports every syntax error, unknown key and value of the
// wrong shape, each with its path. Only a clean tree is decoded into a Type.
//
// yaml.v3's own decoder cannot do this: it stops at the first custom
// decoder that fails, and its errors name Go types, not paths.

// readYAML returns the document node, or the problems that keep it from
// being decoded. The problems are positioned; Source is the caller's.
func readYAML(data []byte) (*yaml.Node, Problems) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		line, msg := 0, err.Error()
		if m := yamlErrLine.FindStringSubmatch(msg); m != nil {
			line, _ = strconv.Atoi(m[1])
			msg = m[2]
		}
		return nil, Problems{{Line: line, Severity: SeverityError, Code: CodeYAMLSyntax,
			Message: msg + ". Fix the YAML syntax on or just before this line"}}
	}
	var ps Problems
	if len(doc.Content) > 0 {
		id, version := typeOf(doc.Content[0])
		w := shapeWalker{r: newReporter(&ps, id, version)}
		w.walk(doc.Content[0], reflect.TypeFor[Type](), "")
	}
	position(ps, &doc)
	return &doc, ps
}

var yamlErrLine = regexp.MustCompile(`^yaml: line (\d+): (.*)$`)

// typeOf reads the Type's id and version from the document, when both are
// well formed, so problems found before decoding can still name the Type.
func typeOf(root *yaml.Node) (string, int) {
	_, id := mappingEntry(root, "id")
	_, v := mappingEntry(root, "version")
	if id == nil || v == nil || id.Kind != yaml.ScalarNode || id.Tag != "!!str" || id.Value == "" ||
		v.Kind != yaml.ScalarNode || v.Tag != "!!int" {
		return "", 0
	}
	version, err := strconv.Atoi(v.Value)
	if err != nil || version < 1 {
		return "", 0
	}
	return id.Value, version
}

type shapeWalker struct{ r reporter }

var (
	acceptedType   = reflect.TypeFor[Accepted]()
	acceptedDocTyp = reflect.TypeFor[acceptedDoc]()
	writableByType = reflect.TypeFor[WritableBy]()
	fieldType      = reflect.TypeFor[Field]()
	linkType       = reflect.TypeFor[LinkType]()
)

// walk checks that node n has the shape of Go type t, at path.
func (w shapeWalker) walk(n *yaml.Node, t reflect.Type, path string) {
	n = resolveAlias(n)
	if n == nil || n.Tag == "!!null" {
		return // no value: the zero value, which the checks judge
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch {
	case t == acceptedType:
		// accepts_reactions takes a list of fields, or a map.
		switch n.Kind {
		case yaml.SequenceNode:
			w.walk(n, reflect.TypeFor[[]string](), path)
		case yaml.MappingNode:
			w.walk(n, acceptedDocTyp, path)
		default:
			w.r.fail(CodeWrongShape, path, "Write a list of fields, or a map with fields, moves, creates and sets",
				"accepts_reactions: want a list of fields, or a map with fields, moves and creates")
		}
		return
	case t == writableByType:
		// Checked by the struct that holds it, which knows its owner.
		return
	}
	switch t.Kind() {
	case reflect.Struct:
		if n.Kind != yaml.MappingNode {
			w.wrongShape(n, path, "a map of keys")
			return
		}
		keys := yamlKeys(t)
		for _, kv := range mappingPairs(n) {
			k, v := kv[0], kv[1]
			kp := at(path, k.Value)
			if path == "" {
				kp = at(k.Value)
			}
			f, ok := keys[k.Value]
			if !ok {
				w.r.fail(CodeUnknownKey, kp, "Check its spelling, or remove it",
					"unknown key %q (the keys here are %s)", k.Value, strings.Join(sortedKeys(keys), ", "))
				continue
			}
			if f.Type == writableByType && (t == fieldType || t == linkType) {
				w.writableBy(n, t, v, kp)
				continue
			}
			w.walk(v, f.Type, kp)
		}
	case reflect.Slice:
		if n.Kind != yaml.SequenceNode {
			w.wrongShape(n, path, "a list")
			return
		}
		for i, item := range n.Content {
			w.walk(item, t.Elem(), at(path, i))
		}
	case reflect.Map:
		if n.Kind != yaml.MappingNode {
			w.wrongShape(n, path, "a map")
			return
		}
		for _, kv := range mappingPairs(n) {
			w.walk(kv[1], t.Elem(), at(path, kv[0].Value))
		}
	case reflect.String:
		if n.Kind != yaml.ScalarNode {
			w.wrongShape(n, path, "a single value")
		}
	case reflect.Int, reflect.Int64, reflect.Int32:
		if n.Kind != yaml.ScalarNode || n.Tag != "!!int" {
			w.wrongShape(n, path, "a whole number")
		}
	case reflect.Bool:
		if n.Kind != yaml.ScalarNode || n.Tag != "!!bool" {
			w.wrongShape(n, path, "true or false")
		}
	}
	// Anything else (fallback values: any) takes any shape.
}

func (w shapeWalker) wrongShape(n *yaml.Node, path, want string) {
	got := "a map"
	switch n.Kind {
	case yaml.SequenceNode:
		got = "a list"
	case yaml.ScalarNode:
		got = strconv.Quote(n.Value)
	}
	name := path
	if name == "" {
		name = "the document"
	}
	w.r.fail(CodeWrongShape, path, "Write the value in the shape the message names", "%s: want %s, not %s", name, want, got)
}

// writableBy checks a field's or a link type's writable_by: kernel, or a
// non-empty list of role:<name> entries (the names are check's).
func (w shapeWalker) writableBy(owner *yaml.Node, t reflect.Type, v *yaml.Node, path string) {
	v = resolveAlias(v)
	kind, nameKey := "field", "id"
	if t == linkType {
		kind, nameKey = "link", "name"
	}
	name := ""
	if _, n := mappingEntry(owner, nameKey); n != nil {
		name = n.Value
	}
	switch v.Kind {
	case yaml.ScalarNode:
	case yaml.SequenceNode:
		if len(v.Content) == 0 {
			w.r.fail(CodeInvalidValue, path, "Leave writable_by out, or name at least one role:<name>",
				"%s %s: writable_by is an empty list; leave it out for anyone", kind, name)
			return
		}
		for i, e := range v.Content {
			e = resolveAlias(e)
			if e.Kind != yaml.ScalarNode {
				w.wrongShape(e, at(path, i), "a single value")
				continue
			}
			// people and agents: a field's other writers (Phase B stub:
			// accepted here, checked by Phase C).
			if t == fieldType && (e.Value == WriterPeople || e.Value == WriterAgents) {
				continue
			}
			if !strings.HasPrefix(e.Value, AllowedRolePrefix) {
				w.r.fail(CodeInvalidValue, at(path, i), "Write role:<name>, or writable_by: kernel on its own",
					"%s %s: writable_by entry %q: a list takes role:<name> entries only "+
						"(kernel stands alone: writable_by: kernel)", kind, name, e.Value)
			}
		}
	default:
		w.r.fail(CodeWrongShape, path, "Write kernel, or a list of role:<name>",
			"%s %s: writable_by must be kernel or a list of role:<name>", kind, name)
	}
}

// mappingPairs returns a mapping's key and value nodes, with YAML merge
// keys (<<: *anchor, or a list of anchors) expanded: the merged entries
// first, each overridden by a key the mapping writes itself, as the decoder
// does.
func mappingPairs(n *yaml.Node) [][2]*yaml.Node {
	n = resolveAlias(n)
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	var merged, own [][2]*yaml.Node
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		if k.Tag != "!!merge" {
			own = append(own, [2]*yaml.Node{k, v})
			continue
		}
		v = resolveAlias(v)
		sources := []*yaml.Node{v}
		if v.Kind == yaml.SequenceNode {
			sources = v.Content
		}
		for _, src := range sources {
			merged = append(merged, mappingPairs(src)...)
		}
	}
	out := own
	for _, kv := range merged {
		if !slices.ContainsFunc(out, func(o [2]*yaml.Node) bool { return o[0].Value == kv[0].Value }) {
			out = append(out, kv)
		}
	}
	return out
}

// yamlKeys maps each yaml key of struct t to its struct field.
func yamlKeys(t reflect.Type) map[string]reflect.StructField {
	keys := map[string]reflect.StructField{}
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
		if name == "-" || name == "" {
			continue
		}
		keys[name] = f
	}
	return keys
}

func sortedKeys(keys map[string]reflect.StructField) []string {
	out := make([]string, 0, len(keys))
	for k := range keys {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// decodeType decodes a clean document into a Type. An error here would mean
// the walker let a shape through that the decoder refuses: reported as
// WRONG_SHAPE on the document, never lost.
func decodeType(doc *yaml.Node) (*Type, Problems) {
	var t Type
	if len(doc.Content) == 0 {
		return &t, nil
	}
	if err := doc.Content[0].Decode(&t); err != nil {
		id, version := typeOf(doc.Content[0])
		var ps Problems
		newReporter(&ps, id, version).fail(CodeWrongShape, "", "Fix the value the message names",
			"%s", strings.TrimPrefix(err.Error(), "yaml: "))
		position(ps, doc)
		return nil, ps
	}
	return &t, nil
}
