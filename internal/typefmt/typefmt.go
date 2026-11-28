// Package typefmt rewrites a Type file's YAML into one canonical layout, as
// gofmt does for Go: block mappings, one key per line, keys in a fixed
// order, lists of plain scalars kept on one line, comments kept. It changes
// no value: before it returns, it decodes its input and its output and
// refuses (ErrChanged) unless they hold the same data and the same
// comments.
//
// It reads YAML, not Types: it does not import typedef, so pli, which talks
// to the item service and does not link typedef, can use it. A file that is
// YAML but not a sound Type is formatted all the same.
//
// The layout:
//   - Every mapping is in block style, two spaces deeper than its key.
//   - A list whose items are all scalars (or an empty list) is written on
//     one line, `[a, b]`; any other list is in block style, its items at
//     the same depth as... two spaces deeper than its key.
//   - Keys are in the order Keys gives for their place; keys it does not
//     name follow, in their own order.
//   - Scalars are written as they were: quotes, escapes and numbers kept.
//     A block scalar (`|`, `>-`) keeps its lines, moved to its new depth.
//   - One blank line before each top-level key whose value is a list or a
//     mapping; no other blank lines. A line comment follows its line after
//     two spaces.
package typefmt

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"reflect"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Keys is the canonical key order of each mapping of the Type format,
// keyed by its path: "" for the document, ".fields[]" for an entry of
// fields, ".states[].timer" for a state's timer, ".accepts_reactions{}"
// for a value of that map. The order is the JSON Schema's
// (schema/type.schema.json). Keys a mapping has that its list does not
// name keep their order, after the known ones; a mapping at a path not
// listed here (the author's own keys, as in sets) keeps its order.
var Keys = map[string][]string{
	"": {"id", "version", "initial_state", "fields", "states", "transitions", "hooks", "links", "incoming",
		"rollups", "reactions", "invariants", "accepts_reactions", "readable_by_reactions", "offers",
		"customer_fields", "own", "shared", "placement_writable_by", "start", "migrate", "summary"},
	".fields[]":             {"id", "type", "options", "multiple", "max", "writable_by", "unique_among_siblings", "label", "help"},
	".states[]":             {"id", "category", "frozen", "claim", "requester_only", "waits_on", "timer", "at", "acting", "label", "help", "due"},
	".states[].timer":       {"after", "to"},
	".states[].at":          {"field", "to"},
	".states[].acting[]":    {"id", "after", "every", "at", "max", "when", "hook", "sets"},
	".transitions[]":        {"from", "to", "guard", "required", "given", "allowed", "except", "release", "escape", "clears", "sets", "label", "help", "primary"},
	".hooks[]":              {"id", "on", "state", "from", "to", "reaction", "kind", "action", "reads", "writes", "fallback"},
	".links[]":              {"name", "inverse", "targets", "max", "inverse_max", "writable_by", "resolve"},
	".links[].resolve":      {"on_enter", "state", "field", "equals"},
	".incoming[]":           {"name", "link", "from"},
	".rollups[]":            {"id", "over", "count_by"},
	".reactions[]":          {"id", "kind", "on", "state", "from", "to", "when", "on_guard", "link", "target", "target_from", "for_each", "by", "undoes", "type", "version", "of", "children", "fields", "add", "set"},
	".reactions[].children": {"of", "when"},
	".invariants[]":         {"id", "rule"},
	".accepts_reactions{}":  {"fields", "moves", "creates", "sets"},
	".start":                {"in", "allowed", "older_versions"},
	".migrate":              {"allowed"},
}

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
	dec := yaml.NewDecoder(bytes.NewReader(src))
	var doc yaml.Node
	if err := dec.Decode(&doc); err == io.EOF {
		return []byte{}, nil
	} else if err != nil {
		return nil, fmt.Errorf("typefmt: %w", err)
	}
	var more yaml.Node
	if err := dec.Decode(&more); err != io.EOF {
		if err != nil {
			return nil, fmt.Errorf("typefmt: %w", err)
		}
		return nil, errors.New("typefmt: more than one YAML document; a Type file holds one")
	}

	p := &printer{src: strings.Split(string(src), "\n")}
	if err := p.document(&doc); err != nil {
		return nil, err
	}
	out := []byte(p.out.String())
	if err := verify(src, out); err != nil {
		return nil, err
	}
	return out, nil
}

// verify is the gate: in and out must decode to the same data, and hold the
// same comments.
func verify(in, out []byte) error {
	var a, b any
	if err := yaml.Unmarshal(in, &a); err != nil {
		return fmt.Errorf("typefmt: %w", err)
	}
	if err := yaml.Unmarshal(out, &b); err != nil {
		return fmt.Errorf("%w (%v)", ErrChanged, err)
	}
	if !reflect.DeepEqual(a, b) {
		return ErrChanged
	}
	before, err := comments(in)
	if err != nil {
		return err
	}
	after, err := comments(out)
	if err != nil {
		return fmt.Errorf("%w (%v)", ErrChanged, err)
	}
	left := map[string]int{}
	for _, c := range after {
		left[c]++
	}
	lines := strings.Split(string(in), "\n")
	for _, c := range before {
		if left[c] == 0 {
			return &CommentError{Line: findComment(lines, c, 0), Text: c}
		}
		left[c]--
	}
	for _, n := range left {
		if n > 0 {
			return ErrChanged
		}
	}
	return nil
}

// comments are the comment lines yaml.v3 reads in src, trimmed, in the
// order of the nodes that carry them.
func comments(src []byte) ([]string, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(src, &doc); err != nil {
		return nil, err
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
	return out, nil
}

// findComment is the 1-based line of src holding the comment c, the first
// at or after line from (1-based); else from.
func findComment(lines []string, c string, from int) int {
	for i := max(from-1, 0); i < len(lines); i++ {
		if j := strings.Index(lines[i], c); j >= 0 && strings.TrimSpace(lines[i][j:]) == c {
			return i + 1
		}
	}
	return from
}

// printer writes the canonical layout, line by line.
type printer struct {
	src []string // the input's lines, for scalars copied as written
	out strings.Builder

	// dashes is the number of "- " the next line starts with: an item of
	// a block list begins on the line of its first key or value.
	dashes int
	// pending is a line comment for the next line written: a list item's,
	// placed on its first line.
	pending string
	// levels are the blocks open around what is written: where each began
	// in the input (its column) and its depth in the output. A foot
	// comment goes back to the depth of the block it was written in.
	levels []level
}

type level struct{ col, ind int }

// line writes one line at depth ind, with the item dashes and the pending
// comment it is owed.
func (p *printer) line(ind int, text string) {
	prefix := strings.Repeat(" ", ind-2*p.dashes) + strings.Repeat("- ", p.dashes)
	p.dashes = 0
	if p.pending != "" {
		text += "  " + p.pending
		p.pending = ""
	}
	p.out.WriteString(strings.TrimRight(prefix+text, " ") + "\n")
}

// raw writes a line as it is (a block scalar's, a continuation's).
func (p *printer) raw(l string) {
	p.out.WriteString(strings.TrimRight(l, " ") + "\n")
}

func (p *printer) blank() { p.out.WriteString("\n") }

// head writes a comment above the line about to be written. Above a list
// item's first line, it goes at the item's depth.
func (p *printer) head(c string, ind int) {
	if c == "" {
		return
	}
	ind -= 2 * p.dashes
	for _, l := range strings.Split(c, "\n") {
		if l = strings.TrimSpace(l); l == "" {
			p.blank()
		} else {
			p.raw(strings.Repeat(" ", ind) + l)
		}
	}
}

// foot writes a comment that follows node n, at the depth of the open
// block (or of inner, a block n holds) whose column in the input is the
// deepest at or left of the comment's.
func (p *printer) foot(c string, n *yaml.Node, inner ...level) {
	if c == "" {
		return
	}
	first := strings.TrimSpace(strings.SplitN(c, "\n", 2)[0])
	ind := 0
	if i := findComment(p.src, first, n.Line+1) - 1; i >= 0 && i < len(p.src) {
		col := len(p.src[i]) - len(strings.TrimLeft(p.src[i], " "))
		for _, l := range append(slices.Clone(p.levels), inner...) {
			if l.col <= col {
				ind = l.ind
			}
		}
	}
	for _, l := range strings.Split(c, "\n") {
		if l = strings.TrimSpace(l); l == "" {
			p.blank()
		} else {
			p.raw(strings.Repeat(" ", ind) + l)
		}
	}
}

func (p *printer) document(doc *yaml.Node) error {
	if doc.HeadComment != "" {
		p.head(doc.HeadComment, 0)
		p.blank()
	}
	if len(doc.Content) > 0 {
		root := doc.Content[0]
		var err error
		switch root.Kind {
		case yaml.MappingNode:
			err = p.mapping(root, 0, "", true)
		case yaml.SequenceNode:
			err = p.items(root, 0, "")
		default:
			first, rest := p.scalar(root, 2)
			p.head(root.HeadComment, 0)
			p.pending = root.LineComment
			p.line(0, first)
			for _, l := range rest {
				p.raw(l)
			}
			p.foot(root.FootComment, root)
		}
		if err != nil {
			return err
		}
	}
	if doc.FootComment != "" {
		p.blank()
		p.foot(doc.FootComment, doc)
	}
	return nil
}

// childPath is the path of the value of key in a mapping at path.
func childPath(path, key string) string {
	if _, known := Keys[path]; known {
		return path + "." + key
	}
	return path + "{}"
}

// pairs are m's key-value pairs in the canonical order for path.
func pairs(m *yaml.Node, path string) [][2]*yaml.Node {
	var out [][2]*yaml.Node
	for i := 0; i+1 < len(m.Content); i += 2 {
		out = append(out, [2]*yaml.Node{m.Content[i], m.Content[i+1]})
	}
	order, known := Keys[path]
	if !known {
		return out
	}
	rank := func(k string) int {
		if i := slices.Index(order, k); i >= 0 {
			return i
		}
		return len(order)
	}
	slices.SortStableFunc(out, func(a, b [2]*yaml.Node) int { return rank(a[0].Value) - rank(b[0].Value) })
	return out
}

// mapping writes m's pairs at depth ind. At the top level, a blank line
// comes before each key whose value is a collection.
func (p *printer) mapping(m *yaml.Node, ind int, path string, top bool) error {
	p.levels = append(p.levels, level{m.Column - 1, ind})
	defer func() { p.levels = p.levels[:len(p.levels)-1] }()
	for i, kv := range pairs(m, path) {
		if top && i > 0 && (kv[1].Kind == yaml.MappingNode || kv[1].Kind == yaml.SequenceNode) {
			p.blank()
		}
		if err := p.pair(kv[0], kv[1], ind, childPath(path, kv[0].Value)); err != nil {
			return err
		}
	}
	p.foot(m.FootComment, m)
	return nil
}

// props are a node's anchor and explicit tag, as they are written before
// it.
func props(n *yaml.Node) string {
	s := ""
	if n.Anchor != "" {
		s += "&" + n.Anchor + " "
	}
	if n.Style&yaml.TaggedStyle != 0 {
		s += n.Tag + " "
	}
	return s
}

// joinComments puts the line comments of a key and its value on one line.
func joinComments(cs ...string) string {
	var out []string
	for _, c := range cs {
		if c != "" {
			out = append(out, c)
		}
	}
	return strings.Join(out, " ")
}

// pair writes key k and its value v at depth ind; path is v's.
func (p *printer) pair(k, v *yaml.Node, ind int, path string) error {
	p.head(k.HeadComment, ind)
	key, _ := p.scalar(k, ind+2)
	switch {
	case v.Kind == yaml.AliasNode:
		p.head(v.HeadComment, ind)
		p.pending = joinComments(p.pending, k.LineComment, v.LineComment)
		p.line(ind, key+": *"+v.Value)
	case v.Kind == yaml.ScalarNode:
		p.head(v.HeadComment, ind)
		first, rest := p.scalar(v, ind+2)
		c := joinComments(k.LineComment, v.LineComment)
		if len(rest) == 0 || v.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0 {
			p.pending = joinComments(p.pending, c)
			p.line(ind, key+": "+first)
			for _, l := range rest {
				p.raw(l)
			}
		} else {
			p.line(ind, key+": "+first)
			for i, l := range rest {
				if i == len(rest)-1 && c != "" {
					l += "  " + c
				}
				p.raw(l)
			}
		}
	case v.Kind == yaml.SequenceNode && flowable(v):
		p.head(v.HeadComment, ind)
		list, err := p.flow(v)
		if err != nil {
			return err
		}
		p.pending = joinComments(p.pending, k.LineComment, v.LineComment)
		p.line(ind, key+": "+props(v)+list)
	case v.Kind == yaml.MappingNode && len(v.Content) == 0:
		p.head(v.HeadComment, ind)
		p.pending = joinComments(p.pending, k.LineComment, v.LineComment)
		p.line(ind, key+": "+props(v)+"{}")
	default: // a block mapping or list
		p.pending = joinComments(p.pending, k.LineComment, v.LineComment)
		p.line(ind, strings.TrimRight(key+": "+props(v), " "))
		p.head(v.HeadComment, ind+2)
		var err error
		if v.Kind == yaml.MappingNode {
			err = p.mapping(v, ind+2, path, false)
		} else {
			err = p.items(v, ind+2, path)
		}
		if err != nil {
			return err
		}
	}
	if v.Kind == yaml.MappingNode || v.Kind == yaml.SequenceNode {
		p.foot(k.FootComment, k, level{v.Column - 1, ind + 2})
	} else {
		p.foot(k.FootComment, k)
	}
	if v.Kind == yaml.ScalarNode || v.Kind == yaml.AliasNode || (v.Kind == yaml.SequenceNode && flowable(v)) {
		p.foot(v.FootComment, v)
	}
	return nil
}

// flowable reports whether list s is written on one line: it is empty, or
// every item is a scalar on one line.
func flowable(s *yaml.Node) bool {
	for _, it := range s.Content {
		if it.Kind != yaml.ScalarNode || it.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0 || strings.Contains(it.Value, "\n") {
			return false
		}
	}
	return true
}

// flow is a one-line list. A comment on one of its items has no place.
func (p *printer) flow(s *yaml.Node) (string, error) {
	var items []string
	for _, it := range s.Content {
		for _, c := range []string{it.HeadComment, it.LineComment, it.FootComment} {
			if c = strings.TrimSpace(strings.SplitN(c, "\n", 2)[0]); c != "" {
				return "", &CommentError{Line: findComment(p.src, c, it.Line-strings.Count(it.HeadComment, "\n")-1), Text: c}
			}
		}
		first, rest := p.scalar(it, 0)
		if len(rest) > 0 {
			return "", fmt.Errorf("typefmt: line %d: a list item over several lines", it.Line)
		}
		items = append(items, first)
	}
	return "[" + strings.Join(items, ", ") + "]", nil
}

// items writes block list s's items at depth ind; path is the list's.
func (p *printer) items(s *yaml.Node, ind int, path string) error {
	p.levels = append(p.levels, level{s.Column - 1, ind})
	defer func() { p.levels = p.levels[:len(p.levels)-1] }()
	for _, it := range s.Content {
		p.head(it.HeadComment, ind)
		switch {
		case it.Kind == yaml.AliasNode:
			p.pending = joinComments(p.pending, it.LineComment)
			p.line(ind, "- *"+it.Value)
		case it.Kind == yaml.ScalarNode:
			first, rest := p.scalar(it, ind+2)
			p.pending = joinComments(p.pending, it.LineComment)
			p.line(ind, "- "+first)
			for _, l := range rest {
				p.raw(l)
			}
		case it.Kind == yaml.SequenceNode && flowable(it):
			list, err := p.flow(it)
			if err != nil {
				return err
			}
			p.pending = joinComments(p.pending, it.LineComment)
			p.line(ind, "- "+props(it)+list)
		case it.Kind == yaml.MappingNode && len(it.Content) == 0:
			p.pending = joinComments(p.pending, it.LineComment)
			p.line(ind, "- "+props(it)+"{}")
		case it.Anchor != "" || it.Style&yaml.TaggedStyle != 0:
			// An anchored block item: its props alone on the dash line.
			p.pending = joinComments(p.pending, it.LineComment)
			p.line(ind, "- "+strings.TrimSpace(props(it)))
			if err := p.block(it, ind+2, path+"[]"); err != nil {
				return err
			}
		default:
			p.dashes++
			p.pending = joinComments(p.pending, it.LineComment)
			if err := p.block(it, ind+2, path+"[]"); err != nil {
				return err
			}
		}
		p.foot(it.FootComment, it)
	}
	p.foot(s.FootComment, s)
	return nil
}

func (p *printer) block(n *yaml.Node, ind int, path string) error {
	if n.Kind == yaml.MappingNode {
		return p.mapping(n, ind, path, false)
	}
	return p.items(n, ind, path)
}

// scalar is n as written in the input: its first line, and the full lines
// after it (a block scalar's, or a quoted scalar's continuations), indented
// to depth ind.
func (p *printer) scalar(n *yaml.Node, ind int) (string, []string) {
	pre := props(n)
	switch {
	case n.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0:
		return p.blockScalar(n, pre, ind)
	case n.Style&(yaml.SingleQuotedStyle|yaml.DoubleQuotedStyle) != 0:
		if first, rest, ok := p.quoted(n, ind); ok {
			return pre + first, rest
		}
	}
	if strings.Contains(n.Value, "\n") {
		// A plain scalar over several lines with a blank one: written
		// quoted, the same value.
		b, _ := yaml.Marshal(n.Value)
		return pre + strings.TrimSpace(string(b)), nil
	}
	return pre + n.Value, nil
}

// at is the input from n's position: its line, from its column, after its
// anchor and tag.
func (p *printer) at(n *yaml.Node) (line int, rest []rune, ok bool) {
	i := n.Line - 1
	if i < 0 || i >= len(p.src) {
		return 0, nil, false
	}
	r := []rune(p.src[i])
	c := n.Column - 1
	if c < 0 || c > len(r) {
		return 0, nil, false
	}
	r = r[c:]
	for len(r) > 0 && (r[0] == '&' || r[0] == '!') {
		for len(r) > 0 && r[0] != ' ' {
			r = r[1:]
		}
		for len(r) > 0 && r[0] == ' ' {
			r = r[1:]
		}
	}
	return i, r, true
}

// quoted copies a quoted scalar from the input, over as many lines as it
// takes; its next lines go at depth ind.
func (p *printer) quoted(n *yaml.Node, ind int) (string, []string, bool) {
	i, r, ok := p.at(n)
	if !ok || len(r) == 0 || (r[0] != '\'' && r[0] != '"') {
		return "", nil, false
	}
	q := r[0]
	var lines []string
	cur := []rune{q}
	r = r[1:]
	for {
		for j := 0; j < len(r); j++ {
			switch {
			case q == '\'' && r[j] == '\'' && j+1 < len(r) && r[j+1] == '\'':
				cur = append(cur, '\'', '\'')
				j++
			case q == '"' && r[j] == '\\' && j+1 < len(r):
				cur = append(cur, r[j], r[j+1])
				j++
			case r[j] == q:
				cur = append(cur, q)
				lines = append(lines, string(cur))
				first, rest := lines[0], lines[1:]
				for k, l := range rest {
					if l = strings.TrimSpace(l); l != "" {
						rest[k] = strings.Repeat(" ", ind) + l
					} else {
						rest[k] = ""
					}
				}
				return first, rest, true
			default:
				cur = append(cur, r[j])
			}
		}
		lines = append(lines, string(cur))
		cur = nil
		i++
		if i >= len(p.src) {
			return "", nil, false
		}
		r = []rune(p.src[i])
	}
}

// blockScalar copies a literal or folded scalar from the input: its header
// and its lines, moved to depth ind.
func (p *printer) blockScalar(n *yaml.Node, pre string, ind int) (string, []string) {
	i, r, ok := p.at(n)
	if !ok || len(r) == 0 || (r[0] != '|' && r[0] != '>') {
		b, _ := yaml.Marshal(n.Value)
		return pre + strings.TrimSpace(string(b)), nil
	}
	header := string(r)
	if j := strings.IndexAny(header, " \t"); j >= 0 {
		header = header[:j]
	}
	var body []string
	if n.Value != "" {
		indent := -1
		for _, l := range p.src[i+1:] {
			trimmed := strings.TrimLeft(l, " ")
			if trimmed == "" {
				body = append(body, "")
				continue
			}
			lead := len(l) - len(trimmed)
			if indent < 0 {
				indent = lead
			}
			if lead < indent {
				break
			}
			body = append(body, strings.Repeat(" ", ind)+l[indent:])
		}
		for len(body) > 0 && body[len(body)-1] == "" {
			body = body[:len(body)-1]
		}
	}
	return pre + header, body
}
