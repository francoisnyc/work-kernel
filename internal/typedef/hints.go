package typedef

// Presentation hints (the Inbox for people, PR 1a): words a screen uses to
// show a Type to people. They enforce nothing and decide nothing; the
// kernel stores them and sends them out through GetType. This file refuses,
// at load, a hint a screen could not use as written:
//
//   - label and help, on the Type, a field, a state and a transition: plain text
//     (one line, no control characters, no space at either end, none of
//     < > or a backtick), a label 1 to 60 characters, a help 1 to 280
//     (INVALID_VALUE);
//   - the same label twice among the fields, among the states, or among
//     the moves out of one state, compared without regard to case
//     (DUPLICATE, on the second);
//   - primary: at most one per source state (ONLY_ONE_ALLOWED), and only on
//     a move a person can make (KEY_NOT_APPLICABLE);
//   - summary: fields the Type declares (UNKNOWN_FIELD), each once
//     (DUPLICATE), at most SummaryMax (INVALID_VALUE);
//   - due: a field the Type declares (UNKNOWN_FIELD) that holds a datetime
//     (WRONG_FIELD_TYPE).

import (
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"
)

// The limits of the hints, in characters (runes), not bytes.
const (
	LabelMax   = 60
	HelpMax    = 280
	SummaryMax = 3
)

// checkHints runs the checks above on the whole Type.
func (t *Type) checkHints(r reporter) {
	t.checkWords(r, "", "the Type", t.Label, t.Help)
	labels := map[string]string{} // lower-case label -> the field that has it
	for i, f := range t.Fields {
		t.checkWords(r, at("fields", i), "field "+f.ID, f.Label, f.Help)
		dupLabel(r, labels, at("fields", i, "label"), f.Label, "field "+f.ID, "fields")
	}
	labels = map[string]string{}
	for i, s := range t.States {
		t.checkWords(r, at("states", i), "state "+s.ID, s.Label, s.Help)
		dupLabel(r, labels, at("states", i, "label"), s.Label, "state "+s.ID, "states")
		t.checkDue(r, i)
	}
	moveLabels := map[string]map[string]string{} // from state -> lower-case label -> the move
	primary := map[string]string{}               // from state -> the primary move
	for i, tr := range t.Transitions {
		move := "move " + tr.From + " -> " + tr.To
		t.checkWords(r, at("transitions", i), move, tr.Label, tr.Help)
		if moveLabels[tr.From] == nil {
			moveLabels[tr.From] = map[string]string{}
		}
		dupLabel(r, moveLabels[tr.From], at("transitions", i, "label"), tr.Label, move, "moves out of state "+tr.From)
		if !tr.Primary {
			continue
		}
		p := at("transitions", i, "primary")
		switch {
		case !personMakes(tr):
			r.fail(CodeKeyNotApplicable, p, "Remove primary: a screen's main button is for a move a person makes",
				"%s: primary, but no person can make it (only a reaction or the timer)", move)
		case primary[tr.From] != "":
			r.fail(CodeOnlyOneAllowed, p, "Keep primary on one move out of the state",
				"%s: primary, but %s is already the primary move out of state %s", move, primary[tr.From], tr.From)
		default:
			primary[tr.From] = move
		}
	}
	t.checkSummary(r)
}

// checkWords checks a label and a help at path p, of what (a field, a state
// or a move).
func (t *Type) checkWords(r reporter, p, what, label, help string) {
	for _, w := range []struct {
		key, text string
		max       int
		set       bool
	}{{"label", label, LabelMax, label != "" || t.keySet(p, "label")}, {"help", help, HelpMax, help != "" || t.keySet(p, "help")}} {
		if !w.set {
			continue
		}
		if why := notPlainText(w.text, w.max); why != "" {
			r.fail(CodeInvalidValue, hintPath(p, w.key), "Write plain text on one line, within its length, or remove the key",
				"%s: %s %q %s", what, w.key, w.text, why)
		}
	}
}

// notPlainText says what is wrong with text as a hint of at most max
// characters, or "" when nothing is.
func notPlainText(text string, max int) string {
	n := utf8.RuneCountInString(text)
	switch {
	case n == 0:
		return "is empty"
	case n > max:
		return "is " + strconv.Itoa(n) + " characters long; at most " + strconv.Itoa(max)
	case strings.ContainsAny(text, "<>`"):
		return "is not plain text: it holds < > or a backtick"
	case strings.IndexFunc(text, unicode.IsControl) >= 0:
		return "is not plain text: it holds a line break, a tab or another control character"
	}
	first, _ := utf8.DecodeRuneInString(text)
	last, _ := utf8.DecodeLastRuneInString(text)
	if unicode.IsSpace(first) || unicode.IsSpace(last) {
		return "starts or ends with a space"
	}
	return ""
}

// dupLabel records label for owner in seen, and reports a second one there.
func dupLabel(r reporter, seen map[string]string, p, label, owner, among string) {
	if label == "" || notPlainText(label, LabelMax) != "" {
		return // empty or malformed: reported once, as itself
	}
	key := strings.ToLower(label)
	if first, ok := seen[key]; ok {
		r.fail(CodeDuplicate, p, "Give each a label of its own: a reader cannot tell them apart",
			"%s: label %q is already the label of %s, among the %s", owner, label, first, among)
		return
	}
	seen[key] = owner
}

// personMakes reports whether a person can make the move: its allowed list
// names someone other than nobody, the timer and hooks.
func personMakes(tr Transition) bool {
	if slices.Contains(tr.Allowed, AllowedNobody) {
		return false
	}
	if len(tr.Allowed) == 0 {
		return true
	}
	for _, e := range tr.Allowed {
		if e != AllowedPrincipalPrefix+TimerPrincipal && !strings.HasPrefix(e, AllowedPrincipalPrefix+"hook:") {
			return true
		}
	}
	return false
}

// checkDue checks states[i].due.
func (t *Type) checkDue(r reporter, i int) {
	s := t.States[i]
	if s.Due == "" {
		return
	}
	p := at("states", i, "due")
	f, ok := t.Field(s.Due)
	switch {
	case !ok:
		r.fail(CodeUnknownField, p, "Name a datetime field the Type declares",
			"state %s: due names field %s, which the Type does not declare", s.ID, s.Due)
	case f.Type != DateTime || f.Multiple:
		r.fail(CodeWrongFieldType, p, "Name a datetime field: the instant the Item is due by",
			"state %s: due names field %s, a %s; it must hold one datetime", s.ID, s.Due, f.Type)
	}
}

// checkSummary checks the Type's summary.
func (t *Type) checkSummary(r reporter) {
	if len(t.Summary) > SummaryMax {
		r.fail(CodeInvalidValue, "summary", "Keep the fields that say most about an Item",
			"summary names %d fields; at most %d", len(t.Summary), SummaryMax)
	}
	seen := map[string]bool{}
	for i, id := range t.Summary {
		p := at("summary", i)
		switch {
		case seen[id]:
			r.fail(CodeDuplicate, p, "Remove the second", "summary names field %s twice", id)
		case !t.hasField(id):
			r.fail(CodeUnknownField, p, "Name a field the Type declares", "summary names field %s, which the Type does not declare", id)
		}
		seen[id] = true
	}
}

// keySet reports whether the YAML wrote key at path p with an empty
// value: a label or a help of "" reads as none, but was written.
func (t *Type) keySet(p, key string) bool { return slices.Contains(t.emptyHints, hintPath(p, key)) }

// hintPath is the path of key at p; p is "" for the Type's own keys.
func hintPath(p, key string) string {
	if p == "" {
		return key
	}
	return at(p, key)
}

// emptyHintPaths lists the labels and helps the document writes as "" on
// a field, a state or a transition (decodeType keeps them on the Type).
func emptyHintPaths(root *yaml.Node) []string {
	var out []string
	empty := func(v *yaml.Node) bool {
		v = resolveAlias(v)
		return v != nil && v.Kind == yaml.ScalarNode && v.Tag == "!!str" && v.Value == ""
	}
	for _, key := range []string{"label", "help"} {
		if _, v := mappingEntry(root, key); empty(v) {
			out = append(out, key)
		}
	}
	for _, list := range []string{"fields", "states", "transitions"} {
		_, seq := mappingEntry(root, list)
		if seq = resolveAlias(seq); seq == nil || seq.Kind != yaml.SequenceNode {
			continue
		}
		for i, item := range seq.Content {
			for _, key := range []string{"label", "help"} {
				_, v := mappingEntry(resolveAlias(item), key)
				if v = resolveAlias(v); v != nil && v.Kind == yaml.ScalarNode && v.Tag == "!!str" && v.Value == "" {
					out = append(out, at(list, i, key))
				}
			}
		}
	}
	return out
}

func (t *Type) hasField(id string) bool {
	_, ok := t.Field(id)
	return ok
}
