// Tests for the Type's own label and help (the Inbox, the look:
// docs/plans/inbox-the-look.md, item 4, and the owner's decisions of
// 2026-11-28). `label` and `help` at the top of a Type are presentation
// hints, as on a field, a state and a move: they enforce nothing and decide
// nothing, and are checked as the other labels are:
//
//   - plain text (one line, no control characters, no space at either end,
//     none of < > or a backtick); a label 1 to 60 characters, a help 1 to
//     280 (characters, not bytes). Otherwise INVALID_VALUE, at `label` or
//     `help`.
//   - a label or help of the wrong shape (a list): WRONG_SHAPE.
//   - stored with the definition right after the version; a Type without
//     them stores nothing new.
//
// And the four versions of types/ that gain one (approval@3,
// access_request@4, equipment_request@3, incident@9): each is the version
// before it with its label added, and its start block carrying the older
// versions open; nothing else changes.
package typedef_test

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// labelSrc is a small Type with a label and a help at its top. Cases change
// it with strings.Replace on the exact text below.
const labelSrc = `
id: hl
version: 1
label: Access request
help: Ask for access to a shared resource
initial_state: open
states:
  - {id: open, category: todo, label: Open}
  - {id: done, category: done, label: Done}
transitions:
  - {from: open, to: done, label: Finish}
`

func labelled(t *testing.T, pairs ...string) string {
	t.Helper()
	src := labelSrc
	for i := 0; i+1 < len(pairs); i += 2 {
		if !strings.Contains(src, pairs[i]) {
			t.Fatalf("labelSrc has no %q", pairs[i])
		}
		src = strings.Replace(src, pairs[i], pairs[i+1], 1)
	}
	return src
}

func TestTypeLabel_Parse(t *testing.T) {
	ty := mustParse(t, labelled(t))
	if ty.Label != "Access request" || ty.Help != "Ask for access to a shared resource" {
		t.Errorf("label, help = %q, %q", ty.Label, ty.Help)
	}
	data, err := json.Marshal(ty) // as the item service stores it
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"id":"hl","version":1,"label":"Access request","help":"Ask for access to a shared resource","initial_state":"open"`; !strings.HasPrefix(string(data), want) {
		t.Errorf("stored definition %s, want it to start %s", data, want)
	}
	back, err := typedef.FromJSON(data)
	if err != nil {
		t.Fatalf("FromJSON(%s): %v", data, err)
	}
	if back.Label != ty.Label || back.Help != ty.Help {
		t.Errorf("after the round trip label, help = %q, %q", back.Label, back.Help)
	}

	bare := mustParse(t, labelled(t, "label: Access request\n", "", "help: Ask for access to a shared resource\n", ""))
	if bare.Label != "" || bare.Help != "" {
		t.Errorf("without the keys: label, help = %q, %q, want none", bare.Label, bare.Help)
	}
	if data, _ := json.Marshal(bare); strings.HasPrefix(string(data), `{"id":"hl","version":1,"label"`) || strings.Contains(string(data), `"help"`) {
		t.Errorf("a Type without label or help stores %s", data)
	}
}

func TestTypeLabel_Loads(t *testing.T) {
	for name, src := range map[string]string{
		"a label and a help":              labelled(t),
		"a label alone":                   labelled(t, "help: Ask for access to a shared resource\n", ""),
		"a help alone":                    labelled(t, "label: Access request\n", ""),
		"a label of 60 characters":        labelled(t, "label: Access request", "label: "+strings.Repeat("é", 60)),
		"a help of 280 characters":        labelled(t, "help: Ask for access to a shared resource", "help: "+strings.Repeat("a", 280)),
		"punctuation in a label":          labelled(t, "label: Access request", `label: "Access (once, for good): yes & no / maybe?"`),
		"the Type's label on a state too": labelled(t, "label: Open}", "label: Access request}"),
		"the Type's label on a move too":  labelled(t, "label: Finish}", "label: Access request}"),
	} {
		t.Run(name, func(t *testing.T) { mustParse(t, src) })
	}
}

func TestTypeLabel_Problems(t *testing.T) {
	cases := []struct {
		name, src, path string
		says            []string
	}{
		{"a label of 61 characters", labelled(t, "label: Access request", "label: "+strings.Repeat("é", 61)), "label", []string{"60"}},
		{"an empty label", labelled(t, "label: Access request", `label: ""`), "label", nil},
		{"a label of two lines", labelled(t, "label: Access request", `label: "Access\nrequest"`), "label", nil},
		{"a tab in a label", labelled(t, "label: Access request", `label: "Access\trequest"`), "label", nil},
		{"a space before a label", labelled(t, "label: Access request", `label: " Access request"`), "label", nil},
		{"a space after a label", labelled(t, "label: Access request", `label: "Access request "`), "label", nil},
		{"markup in a label", labelled(t, "label: Access request", `label: "<b>Access</b>"`), "label", []string{"plain text"}},
		{"a backtick in a label", labelled(t, "label: Access request", "label: \"`access`\""), "label", nil},
		{"a help of 281 characters", labelled(t, "help: Ask for access to a shared resource", "help: "+strings.Repeat("a", 281)), "help", []string{"280"}},
		{"an empty help", labelled(t, "help: Ask for access to a shared resource", `help: ""`), "help", nil},
		{"a help of two lines", labelled(t, "help: Ask for access to a shared resource", `help: "Ask\nfor access"`), "help", nil},
		{"markup in a help", labelled(t, "help: Ask for access to a shared resource", `help: "Ask <i>now</i>"`), "help", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := typedef.Parse([]byte(c.src))
			wantOneProblem(t, err, typedef.CodeInvalidValue, c.path, c.says...)
		})
	}
}

func TestTypeLabel_WrongShape(t *testing.T) {
	for name, c := range map[string]struct{ src, path string }{
		"a label that is a list": {labelled(t, "label: Access request", "label: [Access request]"), "label"},
		"a help that is a map":   {labelled(t, "help: Ask for access to a shared resource", "help: {text: Ask}"), "help"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := typedef.Parse([]byte(c.src))
			wantOneProblem(t, err, typedef.CodeWrongShape, c.path)
		})
	}
}

// The versions of types/ that gain the Type's label, each with the version
// before it, its label, and the older versions its start block keeps open
// (nil: no start block, as the version before).
var labelledVersions = []struct {
	id         string
	before     int
	label      string
	keepsOlder any
}{
	{"approval", 2, "Approval", nil},
	{"access_request", 3, "Access request", []any{1.0, 2.0, 3.0}},
	{"equipment_request", 2, "Equipment request", []any{1.0, 2.0}},
	{"incident", 8, "Incident", "all"},
}

// Each new version is the version before it with its label added and its
// start block keeping the older versions open, and nothing else changed;
// no other version in types/ has a label of its own.
func TestTypeLabel_TheRepositoryTypes(t *testing.T) {
	types, err := typedef.ParseDir("../../types")
	if err != nil {
		t.Fatalf("ParseDir: %v", err)
	}
	byName := map[string]*typedef.Type{}
	for _, ty := range types {
		byName[fmt.Sprintf("%s@%d", ty.ID, ty.Version)] = ty
	}
	stored := func(ty *typedef.Type) map[string]any {
		data, err := json.Marshal(ty)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(data, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	want := map[string]bool{}
	for _, v := range labelledVersions {
		name := fmt.Sprintf("%s@%d", v.id, v.before+1)
		want[name] = true
		was, now := byName[fmt.Sprintf("%s@%d", v.id, v.before)], byName[name]
		if was == nil || now == nil {
			t.Errorf("%s: want versions %d and %d in types/", v.id, v.before, v.before+1)
			continue
		}
		if now.Label != v.label || now.Help != "" {
			t.Errorf("%s label, help = %q, %q, want %q and no help", name, now.Label, now.Help, v.label)
		}
		w, n := stored(was), stored(now)
		start, _ := n["start"].(map[string]any)
		if got := start["older_versions"]; !reflect.DeepEqual(got, v.keepsOlder) {
			t.Errorf("%s start.older_versions = %v, want %v", name, got, v.keepsOlder)
		}
		// Take away the label, the version and the older versions kept
		// open: the rest is the version before it.
		for _, m := range []map[string]any{w, n} {
			delete(m, "label")
			delete(m, "version")
			if s, ok := m["start"].(map[string]any); ok {
				delete(s, "older_versions")
				if len(s) == 0 {
					delete(m, "start")
				}
			}
		}
		if !reflect.DeepEqual(w, n) {
			t.Errorf("%s differs from %s@%d in more than its label and start.older_versions", name, v.id, v.before)
		}
	}
	for name, ty := range byName {
		if !want[name] && (ty.Label != "" || ty.Help != "") {
			t.Errorf("%s has a Type label or help; only %v should", name, labelledVersions)
		}
	}
}
