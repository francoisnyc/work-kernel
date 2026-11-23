package story

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// Parse reads a story file. source names it in problems. Every problem of
// the file is reported at once, as Problems, each with its line.
func Parse(source string, data []byte) (*Story, error) {
	p := &parser{s: &Story{Source: source}}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, Problems{{Source: source, Line: yamlErrorLine(err), Message: "not a story file: " + err.Error()}}
	}
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, Problems{{Source: source, Line: 1, Message: "a story file is a mapping of keys (story, author, day1, people, steps, expect...)"}}
	}
	p.file(doc.Content[0])
	if len(p.problems) > 0 {
		slices.SortStableFunc(p.problems, func(a, b Problem) int { return a.Line - b.Line })
		return nil, p.problems
	}
	return p.s, nil
}

var yamlLine = regexp.MustCompile(`line (\d+)`)

func yamlErrorLine(err error) int {
	if m := yamlLine.FindStringSubmatch(err.Error()); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	return 1
}

type parser struct {
	s        *Story
	problems Problems
}

func (p *parser) fail(line int, format string, args ...any) {
	p.problems = append(p.problems, Problem{Source: p.s.Source, Line: line, Message: fmt.Sprintf(format, args...)})
}

// The keys of a story file, in the order the documentation gives them.
var fileKeys = []string{"story", "author", "day1", "observer", "people", "names", "hide_values", "show_skipped",
	"fakes", "given", "steps", "until", "expect"}

var requiredKeys = []string{"story", "author", "day1", "people", "steps", "expect"}

func (p *parser) file(m *yaml.Node) {
	nodes := map[string]*yaml.Node{}
	for i := 0; i+1 < len(m.Content); i += 2 {
		k, v := m.Content[i], m.Content[i+1]
		if !slices.Contains(fileKeys, k.Value) {
			p.fail(k.Line, "unknown key %q: a story file has %s", k.Value, strings.Join(fileKeys, ", "))
			continue
		}
		if _, dup := nodes[k.Value]; dup {
			p.fail(k.Line, "%s is given twice", k.Value)
			continue
		}
		nodes[k.Value] = v
	}
	for _, k := range requiredKeys {
		if _, ok := nodes[k]; !ok {
			p.fail(1, "%s is required", k)
		}
	}
	// day1 first: every time is relative to it.
	if n, ok := nodes["day1"]; ok {
		d, err := time.Parse(time.DateOnly, n.Value)
		if n.Kind != yaml.ScalarNode || err != nil {
			p.fail(n.Line, "day1 %q is not a date such as 2026-12-07", n.Value)
		} else {
			p.s.Day1 = d.UTC()
		}
	}
	text := func(key string) string {
		n, ok := nodes[key]
		if !ok {
			return ""
		}
		if n.Kind != yaml.ScalarNode {
			p.fail(n.Line, "%s is one line of text", key)
			return ""
		}
		return strings.TrimSpace(n.Value)
	}
	p.s.Name, p.s.Author, p.s.Observer = text("story"), text("author"), text("observer")
	if n, ok := nodes["story"]; ok && p.s.Name == "" {
		p.fail(n.Line, "story: say in one line what the story shows")
	}
	if n, ok := nodes["author"]; ok && p.s.Author == "" {
		p.fail(n.Line, "author: say who wrote or approved the story")
	}
	if n, ok := nodes["show_skipped"]; ok {
		switch n.Value {
		case "true":
			p.s.ShowSkipped = true
		case "false":
		default:
			p.fail(n.Line, "show_skipped is true or false, not %q", n.Value)
		}
	}
	if n, ok := nodes["people"]; ok {
		p.people(n)
	}
	if n, ok := nodes["names"]; ok {
		p.names(n)
	}
	if n, ok := nodes["hide_values"]; ok {
		p.hideValues(n)
	}
	if n, ok := nodes["fakes"]; ok {
		p.fakes(n)
	}
	for _, n := range p.lines(nodes["given"], "given") {
		if st, err := parseStep(p.s.Day1, n.Value, false); err != nil {
			p.fail(n.Line, "%v", err)
		} else {
			st.Line = n.Line
			p.s.Given = append(p.s.Given, st)
		}
	}
	for _, n := range p.lines(nodes["steps"], "steps") {
		st, err := parseStep(p.s.Day1, n.Value, true)
		if err != nil {
			p.fail(n.Line, "%v", err)
			continue
		}
		st.Line = n.Line
		if k := len(p.s.Steps); k > 0 && st.At.Before(p.s.Steps[k-1].At) {
			p.fail(n.Line, "steps are in time order: this one comes before the step on line %d", p.s.Steps[k-1].Line)
			continue
		}
		p.s.Steps = append(p.s.Steps, st)
	}
	if n, ok := nodes["until"]; ok {
		toks, err := tokenize(n.Value)
		var at time.Time
		next := 0
		if err == nil {
			at, next, err = parseTime(p.s.Day1, toks, 0)
		}
		if err == nil && next != len(toks) {
			err = fmt.Errorf("until is one time, such as day 10 00:00")
		}
		if err != nil {
			p.fail(n.Line, "until: %v", err)
		} else {
			p.s.Until = at
		}
	}
	for _, n := range p.lines(nodes["expect"], "expect") {
		l, err := parseLine(p.s.Day1, n.Value)
		if err != nil {
			p.fail(n.Line, "%v", err)
			continue
		}
		l.Line = n.Line
		p.s.Expect = append(p.s.Expect, l)
	}
}

// lines returns the one-line strings of a list.
func (p *parser) lines(n *yaml.Node, key string) []*yaml.Node {
	if n == nil {
		return nil
	}
	if n.Kind != yaml.SequenceNode {
		p.fail(n.Line, "%s is a list of one-line strings", key)
		return nil
	}
	var out []*yaml.Node
	for _, item := range n.Content {
		if item.Kind != yaml.ScalarNode || strings.Contains(strings.TrimSpace(item.Value), "\n") {
			p.fail(item.Line, "each of %s is one line", key)
			continue
		}
		out = append(out, item)
	}
	return out
}

var personName = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

func (p *parser) people(n *yaml.Node) {
	if n.Kind != yaml.MappingNode {
		p.fail(n.Line, "people is a mapping of names, such as alice: {roles: [hr_admin]}")
		return
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		person := Person{Line: k.Line, Name: k.Value}
		ok := true
		if !personName.MatchString(k.Value) {
			p.fail(k.Line, "a person's name is lower case letters, digits and _, not %q", k.Value)
			ok = false
		}
		switch {
		case v.Kind == yaml.MappingNode:
			for j := 0; j+1 < len(v.Content); j += 2 {
				pk, pv := v.Content[j], v.Content[j+1]
				switch pk.Value {
				case "roles":
					if pv.Kind != yaml.SequenceNode {
						p.fail(pk.Line, "%s's roles are a list, such as [hr_admin]", k.Value)
						ok = false
						continue
					}
					for _, r := range pv.Content {
						person.Roles = append(person.Roles, r.Value)
					}
				case "customer":
					if pv.Value != "true" && pv.Value != "false" {
						p.fail(pk.Line, "%s's customer is true or false", k.Value)
						ok = false
					}
					person.Customer = pv.Value == "true"
				default:
					p.fail(pk.Line, "%s: unknown key %q (a person has roles and customer)", k.Value, pk.Value)
					ok = false
				}
			}
		case v.Kind == yaml.ScalarNode && (v.Value == "" || v.Tag == "!!null"):
		default:
			p.fail(k.Line, "%s is a mapping, such as {roles: [hr_admin]} or {}", k.Value)
			ok = false
		}
		if ok {
			p.s.People = append(p.s.People, person)
		}
	}
}

func (p *parser) names(n *yaml.Node) {
	if n.Kind != yaml.MappingNode {
		p.fail(n.Line, "names is a mapping, such as first: sla_goal in inc where f_metric = first_response")
		return
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		rule, err := parseNameRule(k.Value, v.Value)
		if err != nil {
			p.fail(k.Line, "%v", err)
			continue
		}
		rule.Line = k.Line
		p.s.Names = append(p.s.Names, rule)
	}
}

func (p *parser) hideValues(n *yaml.Node) {
	if n.Kind != yaml.MappingNode {
		p.fail(n.Line, "hide_values is a mapping of Type IDs to fields, such as sla_goal: [f_week]")
		return
	}
	p.s.HideValues = map[string][]string{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		if v.Kind != yaml.SequenceNode {
			p.fail(k.Line, "hide_values: %s takes a list of fields", k.Value)
			continue
		}
		for _, f := range v.Content {
			p.s.HideValues[k.Value] = append(p.s.HideValues[k.Value], f.Value)
		}
	}
}

var actionName = regexp.MustCompile(`^[a-z][a-z0-9_]*\.[a-z][a-z0-9_]*@[1-9][0-9]*$`)

func (p *parser) fakes(n *yaml.Node) {
	if n.Kind != yaml.MappingNode {
		p.fail(n.Line, "fakes is a mapping of actions to rules, such as email.notify@1: [ok]")
		return
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		if !actionName.MatchString(k.Value) {
			p.fail(k.Line, "fake %q: name an action with its major, such as email.notify@1", k.Value)
			continue
		}
		f := Fake{Line: k.Line, Action: k.Value}
		for _, rn := range p.lines(v, k.Value) {
			r, err := parseRule(p.s.Day1, rn.Value)
			if err != nil {
				p.fail(rn.Line, "%v", err)
				continue
			}
			r.Line = rn.Line
			f.Rules = append(f.Rules, r)
		}
		p.s.Fakes = append(p.s.Fakes, f)
	}
}

// --- the one-line grammar ---

// token is a word, a quoted text, or one of , [ ] = :
type token struct {
	text       string
	quoted     bool
	start, end int // in the line
}

func (t token) is(s string) bool { return !t.quoted && t.text == s }

// tokenize splits a line into tokens. A colon is a token of its own when
// a space or the end follows it (so 09:00 stays a word, and "inc: text"
// splits).
func tokenize(s string) ([]token, error) {
	var out []token
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == ' ' || c == '\t':
			i++
		case c == ',' || c == '[' || c == ']' || c == '=':
			out = append(out, token{text: string(c), start: i, end: i + 1})
			i++
		case c == ':' && (i+1 == len(s) || s[i+1] == ' '):
			out = append(out, token{text: ":", start: i, end: i + 1})
			i++
		case c == '"':
			var b strings.Builder
			j := i + 1
			closed := false
			for j < len(s) {
				if s[j] == '\\' && j+1 < len(s) {
					b.WriteByte(s[j+1])
					j += 2
					continue
				}
				if s[j] == '"' {
					closed = true
					break
				}
				b.WriteByte(s[j])
				j++
			}
			if !closed {
				return nil, fmt.Errorf("an unterminated quote at %q", s[i:])
			}
			out = append(out, token{text: b.String(), quoted: true, start: i, end: j + 1})
			i = j + 1
		default:
			j := i
			for j < len(s) {
				d := s[j]
				if d == ' ' || d == '\t' || d == ',' || d == '[' || d == ']' || d == '=' || d == '"' ||
					(d == ':' && (j+1 == len(s) || s[j+1] == ' ')) {
					break
				}
				j++
			}
			out = append(out, token{text: s[i:j], start: i, end: j})
			i = j
		}
	}
	return out, nil
}

var clockWord = regexp.MustCompile(`^(\d\d):(\d\d)(?::(\d\d))?$`)

// parseTime reads day N HH:MM[:SS] at toks[i]: the instant and the index
// after it.
func parseTime(day1 time.Time, toks []token, i int) (time.Time, int, error) {
	if i >= len(toks) || !toks[i].is("day") {
		return time.Time{}, i, fmt.Errorf("want a time such as day 1 09:00")
	}
	if i+1 >= len(toks) {
		return time.Time{}, i, fmt.Errorf("day wants its number, such as day 1 09:00")
	}
	n, err := strconv.Atoi(toks[i+1].text)
	if err != nil || n < 1 || toks[i+1].quoted {
		return time.Time{}, i, fmt.Errorf("day %s: days count from day 1", toks[i+1].text)
	}
	if i+2 >= len(toks) {
		return time.Time{}, i, fmt.Errorf("day %d wants its time, such as day %d 09:00", n, n)
	}
	of, err := parseClock(toks[i+2].text)
	if err != nil {
		return time.Time{}, i, err
	}
	return day1.AddDate(0, 0, n-1).Add(of), i + 3, nil
}

// parseClock reads HH:MM[:SS] as a time of day.
func parseClock(s string) (time.Duration, error) {
	m := clockWord.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("%q is not a time of day such as 09:00 or 09:00:30", s)
	}
	h, _ := strconv.Atoi(m[1])
	mi, _ := strconv.Atoi(m[2])
	sec := 0
	if m[3] != "" {
		sec, _ = strconv.Atoi(m[3])
	}
	if h > 23 || mi > 59 || sec > 59 {
		return 0, fmt.Errorf("%q is not a time of day (00:00 to 23:59:59)", s)
	}
	return time.Duration(h)*time.Hour + time.Duration(mi)*time.Minute + time.Duration(sec)*time.Second, nil
}

// parseValue reads one value at toks[i].
func parseValue(toks []token, i int) (Value, int, error) {
	if i >= len(toks) {
		return Value{}, i, fmt.Errorf("a value is missing")
	}
	t := toks[i]
	if t.quoted {
		return Value{Kind: Quoted, Text: t.text}, i + 1, nil
	}
	switch {
	case t.text == "null":
		return Value{Kind: Null}, i + 1, nil
	case t.text == "true" || t.text == "false":
		return Value{Kind: Bool, Bool: t.text == "true"}, i + 1, nil
	case t.text == "day":
		if i+1 >= len(toks) {
			return Value{}, i, fmt.Errorf("day wants its number")
		}
		n, err := strconv.Atoi(toks[i+1].text)
		if err != nil || n < 1 {
			return Value{}, i, fmt.Errorf("day %s: days count from day 1", toks[i+1].text)
		}
		if i+2 < len(toks) && clockWord.MatchString(toks[i+2].text) {
			of, err := parseClock(toks[i+2].text)
			if err != nil {
				return Value{}, i, err
			}
			return Value{Kind: DayTime, Day: n, Time: of}, i + 3, nil
		}
		return Value{Kind: Day, Day: n}, i + 2, nil
	case t.text == "[":
		v := Value{Kind: List}
		j := i + 1
		if j < len(toks) && toks[j].is("]") {
			return v, j + 1, nil
		}
		for {
			item, next, err := parseValue(toks, j)
			if err != nil {
				return Value{}, i, err
			}
			v.List = append(v.List, item)
			if next >= len(toks) {
				return Value{}, i, fmt.Errorf("a list is not closed with ]")
			}
			if toks[next].is("]") {
				return v, next + 1, nil
			}
			if !toks[next].is(",") {
				return Value{}, i, fmt.Errorf("a list's values are separated by commas, not %q", toks[next].text)
			}
			j = next + 1
		}
	case t.text == "," || t.text == "]" || t.text == "=" || t.text == ":":
		return Value{}, i, fmt.Errorf("a value is missing before %q", t.text)
	}
	if n, err := strconv.ParseInt(t.text, 10, 64); err == nil {
		return Value{Kind: Int, Int: n}, i + 1, nil
	}
	return Value{Kind: Word, Text: t.text}, i + 1, nil
}

// parseValues reads field = value, field = value... at toks[i], and stops
// at the first token after a value that is not a comma.
func parseValues(toks []token, i int) ([]Assignment, int, error) {
	var out []Assignment
	for {
		if i >= len(toks) {
			return nil, i, fmt.Errorf("want field = value")
		}
		f := toks[i]
		if f.quoted || f.text == "," || f.text == "=" {
			return nil, i, fmt.Errorf("want a field's name before =, not %q", f.text)
		}
		if i+1 >= len(toks) || !toks[i+1].is("=") {
			return nil, i, fmt.Errorf("want %s = value", f.text)
		}
		v, next, err := parseValue(toks, i+2)
		if err != nil {
			return nil, i, fmt.Errorf("%s: %v", f.text, err)
		}
		out = append(out, Assignment{Field: f.text, Value: v})
		if next < len(toks) && toks[next].is(",") {
			i = next + 1
			continue
		}
		return out, next, nil
	}
}

var typeRef = regexp.MustCompile(`^([a-z][a-z0-9_]*)@([1-9][0-9]*)$`)

// parseStep reads one step: with its time (steps) or without (given).
func parseStep(day1 time.Time, line string, timed bool) (Step, error) {
	line = strings.TrimSpace(line)
	st := Step{Text: line}
	toks, err := tokenize(line)
	if err != nil {
		return st, err
	}
	i := 0
	if timed {
		st.At, i, err = parseTime(day1, toks, 0)
		if err != nil {
			return st, err
		}
	} else if len(toks) > 1 && toks[0].is("day") {
		if _, err := strconv.Atoi(toks[1].text); err == nil {
			return st, fmt.Errorf("a given step has no time: given steps run at day 1 00:00, in order")
		}
	}
	// "-> refused REASON" at the end.
	for k := i; k < len(toks); k++ {
		if toks[k].is("->") {
			if k+1 >= len(toks) || !toks[k+1].is("refused") {
				return st, fmt.Errorf("a step ends in \"-> refused REASON\"")
			}
			if k+2 >= len(toks) || k+3 != len(toks) {
				return st, fmt.Errorf("-> refused wants one reason, such as NOT_ALLOWED")
			}
			st.Refused = toks[k+2].text
			toks = toks[:k]
			break
		}
	}
	word := func(j int, what string) (string, error) {
		if j >= len(toks) || toks[j].quoted || len(toks[j].text) == 1 && strings.ContainsAny(toks[j].text, ",[]=:") {
			return "", fmt.Errorf("want %s", what)
		}
		return toks[j].text, nil
	}
	if st.Actor, err = word(i, "who acts, then what they do"); err != nil {
		return st, err
	}
	verb, err := word(i+1, "what "+st.Actor+" does: creates, updates, moves, links, unlinks, places, narrows, sees or cannot see")
	if err != nil {
		return st, err
	}
	j := i + 2
	end := func(j int) error {
		if j != len(toks) {
			return fmt.Errorf("unexpected %q", toks[j].text)
		}
		return nil
	}
	withValues := func(j int, required bool) error {
		if j == len(toks) && !required {
			return nil
		}
		if j >= len(toks) || !toks[j].is("with") {
			if required {
				return fmt.Errorf("want with field = value, ...")
			}
			return fmt.Errorf("unexpected %q: want with field = value, ...", toks[j].text)
		}
		vals, next, err := parseValues(toks, j+1)
		if err != nil {
			return err
		}
		st.Values = vals
		return end(next)
	}
	switch verb {
	case "creates":
		st.Verb = Creates
		if st.Item, err = word(j, "the new Item's name"); err != nil {
			return st, err
		}
		if j+1 >= len(toks) || !toks[j+1].is("as") {
			return st, fmt.Errorf("creates %s: want as TYPE@VERSION", st.Item)
		}
		ty, err := word(j+2, "TYPE@VERSION after as")
		if err != nil {
			return st, err
		}
		m := typeRef.FindStringSubmatch(ty)
		if m == nil {
			return st, fmt.Errorf("%q: a Type is named with its version, such as vacation_request@3", ty)
		}
		st.Type = m[1]
		st.Version, _ = strconv.Atoi(m[2])
		k := j + 3
		if k < len(toks) && toks[k].is("in") {
			if st.In, err = word(k+1, "the container's name after in"); err != nil {
				return st, err
			}
			k += 2
		}
		if err := withValues(k, false); err != nil {
			return st, err
		}
	case "updates":
		st.Verb = Updates
		if st.Item, err = word(j, "the Item's name"); err != nil {
			return st, err
		}
		if err := withValues(j+1, true); err != nil {
			return st, err
		}
	case "moves":
		st.Verb = Moves
		if st.Item, err = word(j, "the Item's name"); err != nil {
			return st, err
		}
		if j+1 >= len(toks) || !toks[j+1].is("to") {
			return st, fmt.Errorf("moves %s: want to STATE", st.Item)
		}
		if st.To, err = word(j+2, "the state after to"); err != nil {
			return st, err
		}
		if err := withValues(j+3, false); err != nil {
			return st, err
		}
	case "links", "unlinks":
		st.Verb = Links
		if verb == "unlinks" {
			st.Verb = Unlinks
		}
		if st.Item, err = word(j, "ITEM LINK ITEM"); err != nil {
			return st, err
		}
		if st.Link, err = word(j+1, "ITEM LINK ITEM"); err != nil {
			return st, err
		}
		if st.Target, err = word(j+2, "ITEM LINK ITEM"); err != nil {
			return st, err
		}
		if err := end(j + 3); err != nil {
			return st, err
		}
	case "sees":
		if st.Item, err = word(j, "the Item's name"); err != nil {
			return st, err
		}
		switch {
		case j+1 < len(toks) && toks[j+1].is("with"):
			st.Verb = Sees
			if err := withValues(j+1, true); err != nil {
				return st, err
			}
		case j+1 < len(toks) && toks[j+1].is("fields"):
			st.Verb = SeesFields
			k := j + 2
			for {
				f, err := word(k, "a field's name")
				if err != nil {
					return st, err
				}
				st.Fields = append(st.Fields, f)
				if k+1 < len(toks) && toks[k+1].is(",") {
					k += 2
					continue
				}
				if err := end(k + 1); err != nil {
					return st, err
				}
				break
			}
		default:
			return st, fmt.Errorf("sees %s: want with field = value, ... or fields f, f, ...", st.Item)
		}
	case "places":
		st.Verb = Places
		if st.Item, err = word(j, "the Item's name"); err != nil {
			return st, err
		}
		switch {
		case j+1 < len(toks) && toks[j+1].is("in"):
			if st.In, err = word(j+2, "the container's name after in"); err != nil {
				return st, err
			}
			if err := end(j + 3); err != nil {
				return st, err
			}
		case j+3 < len(toks) && toks[j+1].is("at") && toks[j+2].is("the") && toks[j+3].is("top"):
			if j+4 >= len(toks) || !toks[j+4].is("level") {
				return st, fmt.Errorf("places %s: want in ITEM or at the top level", st.Item)
			}
			st.Top = true
			if err := end(j + 5); err != nil {
				return st, err
			}
		default:
			return st, fmt.Errorf("places %s: want in ITEM or at the top level", st.Item)
		}
	case "narrows":
		st.Verb = Narrows
		if st.Item, err = word(j, "the Item's name"); err != nil {
			return st, err
		}
		if j+1 >= len(toks) || !toks[j+1].is("to") {
			return st, fmt.Errorf("narrows %s: want to person, person, ...", st.Item)
		}
		k := j + 2
		for {
			p, err := word(k, "a person's name")
			if err != nil {
				return st, err
			}
			st.Readers = append(st.Readers, p)
			if k+1 < len(toks) && toks[k+1].is(",") {
				k += 2
				continue
			}
			if err := end(k + 1); err != nil {
				return st, err
			}
			break
		}
	case "cannot":
		if j >= len(toks) || !toks[j].is("see") {
			return st, fmt.Errorf("want cannot see ITEM")
		}
		st.Verb = CannotSee
		if st.Item, err = word(j+1, "the Item's name"); err != nil {
			return st, err
		}
		if err := end(j + 2); err != nil {
			return st, err
		}
	default:
		return st, fmt.Errorf("%q is not something a step does: creates, updates, moves, links, unlinks, places, narrows, sees or cannot see", verb)
	}
	if st.Refused != "" && (st.Verb == Sees || st.Verb == SeesFields || st.Verb == CannotSee) {
		return st, fmt.Errorf("only a write may be refused: a read says what it sees")
	}
	return st, nil
}

// parseRule reads one rule of a fake.
func parseRule(day1 time.Time, line string) (Rule, error) {
	var r Rule
	toks, err := tokenize(strings.TrimSpace(line))
	if err != nil {
		return r, err
	}
	answer := toks
	if k := slices.IndexFunc(toks, func(t token) bool { return t.is(":") }); k >= 0 {
		cond := toks[:k]
		answer = toks[k+1:]
		i := 0
		if i < len(cond) && cond[i].is("when") {
			vals, next, err := parseValues(cond, i+1)
			if err != nil {
				return r, fmt.Errorf("when: %v", err)
			}
			r.When, i = vals, next
		}
		if i < len(cond) && cond[i].is("from") {
			if r.From, i, err = parseTime(day1, cond, i+1); err != nil {
				return r, fmt.Errorf("from: %v", err)
			}
		}
		if i < len(cond) && cond[i].is("until") {
			if r.Until, i, err = parseTime(day1, cond, i+1); err != nil {
				return r, fmt.Errorf("until: %v", err)
			}
		}
		if i != len(cond) {
			return r, fmt.Errorf("unexpected %q: a rule is [when input = value, ...] [from TIME] [until TIME]: ANSWER", cond[i].text)
		}
	}
	if len(answer) == 0 {
		return r, fmt.Errorf("a rule wants its answer: allow, deny REASON, correct ..., ok [...] or fail ...")
	}
	kind := answer[0].text
	rest := answer[1:]
	switch {
	case answer[0].is("allow") && len(rest) == 0:
		r.Kind = Allow
	case answer[0].is("deny") && len(rest) == 1 && !rest[0].quoted:
		r.Kind, r.Reason = Deny, rest[0].text
	case answer[0].is("correct"):
		vals, next, err := parseValues(rest, 0)
		if err != nil {
			return r, fmt.Errorf("correct: %v", err)
		}
		if next != len(rest) {
			return r, fmt.Errorf("correct: unexpected %q", rest[next].text)
		}
		r.Kind, r.Values = Correct, vals
	case answer[0].is("ok"):
		r.Kind = OK
		if len(rest) > 0 {
			vals, next, err := parseValues(rest, 0)
			if err != nil {
				return r, fmt.Errorf("ok: %v", err)
			}
			if next != len(rest) {
				return r, fmt.Errorf("ok: unexpected %q", rest[next].text)
			}
			r.Values = vals
		}
	case answer[0].is("fail") && len(rest) == 1:
		switch f := rest[0].text; f {
		case FailTransient, FailInvalid, FailUnauthorized, FailKeyConflict:
			r.Kind, r.Failure = Fail, f
		default:
			return r, fmt.Errorf("fail %q: a fake fails transient, invalid, unauthorized or key_conflict", f)
		}
	default:
		return r, fmt.Errorf("%q is not an answer: allow, deny REASON, correct ..., ok [...] or fail ...", kind)
	}
	return r, nil
}

// parseLine reads one expected line.
func parseLine(day1 time.Time, line string) (Line, error) {
	var l Line
	line = strings.TrimSpace(line)
	toks, err := tokenize(line)
	if err != nil {
		return l, err
	}
	i := 0
	if len(toks) > 0 && toks[0].is("between") {
		if l.At, i, err = parseTime(day1, toks, 1); err != nil {
			return l, fmt.Errorf("between: %v", err)
		}
		if i >= len(toks) || !toks[i].is("and") {
			return l, fmt.Errorf("between TIME and TIME: want and after the first time")
		}
		if l.To, i, err = parseTime(day1, toks, i+1); err != nil {
			return l, fmt.Errorf("between: %v", err)
		}
		if !l.To.After(l.At) {
			return l, fmt.Errorf("between: the window ends before it starts")
		}
	} else if l.At, i, err = parseTime(day1, toks, 0); err != nil {
		return l, err
	}
	if i+1 >= len(toks) || toks[i].quoted || !toks[i+1].is(":") {
		return l, fmt.Errorf("want ITEM: what happened, after the time")
	}
	l.Item = toks[i].text
	l.Text = strings.TrimSpace(line[toks[i+1].end:])
	if l.Text == "" {
		return l, fmt.Errorf("%s: say what happened", l.Item)
	}
	return l, nil
}

// parseNameRule reads TYPE [in ITEM] [where field = value].
func parseNameRule(name, line string) (NameRule, error) {
	r := NameRule{Name: name}
	toks, err := tokenize(strings.TrimSpace(line))
	if err != nil {
		return r, fmt.Errorf("%s: %v", name, err)
	}
	if len(toks) == 0 || toks[0].quoted || toks[0].is("in") || toks[0].is("where") || !personName.MatchString(toks[0].text) {
		return r, fmt.Errorf("%s: a name rule is TYPE [in ITEM] [where field = value]", name)
	}
	r.Type = toks[0].text
	i := 1
	if i < len(toks) && toks[i].is("in") {
		if i+1 >= len(toks) {
			return r, fmt.Errorf("%s: in wants an Item's name", name)
		}
		r.In = toks[i+1].text
		i += 2
	}
	if i < len(toks) && toks[i].is("where") {
		vals, next, err := parseValues(toks, i+1)
		if err != nil || len(vals) != 1 {
			return r, fmt.Errorf("%s: where wants one field = value", name)
		}
		r.Where = &vals[0]
		i = next
	}
	if i != len(toks) {
		return r, fmt.Errorf("%s: unexpected %q: a name rule is TYPE [in ITEM] [where field = value]", name, toks[i].text)
	}
	return r, nil
}
