// Tests for Jira part 2, step 2, through pli: a multi-value field is set
// as a JSON list (--set), changed with set operations (--add, --remove on
// item update and item transition), and found with --where (contains) and
// --empty; the Type shows its reactions. Reactions themselves need the
// worker: the scenario runs them.
package main

import (
	"slices"
	"sync"
	"testing"
)

func setOf(t *testing.T, out map[string]any, field string) []string {
	t.Helper()
	fields, _ := itemOf(t, out)["fields"].(map[string]any)
	list, _ := fields[field].([]any)
	var got []string
	for _, v := range list {
		got = append(got, v.(string))
	}
	return got
}

func TestPli_MultiValue_SetAddRemove(t *testing.T) {
	space := platSpace(t)
	story := create(t, "ana", "--type", "story@3", "--in", space, "--set", "f_summary=Labelled "+word(),
		"--set", `f_labels=["sso","backend","sso"]`)
	if got := setOf(t, ok(t, "ana", "item", "get", story), "f_labels"); !slices.Equal(got, []string{"backend", "sso"}) {
		t.Errorf("f_labels = %v, want [backend sso]", got)
	}
	// --add and --remove may repeat, for one field or several; a principal
	// is a plain value.
	out := ok(t, "ana", "item", "update", story, "--add", "f_labels=urgent", "--add", "f_labels=ui",
		"--remove", "f_labels=sso", "--add", "f_reviewers=user:sam")
	if got := setOf(t, out, "f_labels"); !slices.Equal(got, []string{"backend", "ui", "urgent"}) {
		t.Errorf("after --add and --remove: f_labels = %v, want [backend ui urgent]", got)
	}
	if got := setOf(t, out, "f_reviewers"); !slices.Equal(got, []string{"user:sam"}) {
		t.Errorf("f_reviewers = %v, want [user:sam]", got)
	}
	// A value that looks like a number or a bool is still a label.
	out = ok(t, "ana", "item", "update", story, "--add", "f_labels=2026", "--add", "f_labels=true", "--remove", "f_labels=ui")
	if got := setOf(t, out, "f_labels"); !slices.Equal(got, []string{"2026", "backend", "true", "urgent"}) {
		t.Errorf("f_labels = %v, want the labels 2026 and true as text", got)
	}
	// With a transition, and with other fields.
	ok(t, "ana", "item", "transition", story, "--to", "in_progress", "--add", "f_labels=started", "--set", "f_points=3")
	hist := ok(t, "ana", "item", "history", story)["events"].([]any)
	last := hist[len(hist)-1].(map[string]any)
	ops, _ := last["set_ops"].(map[string]any)
	added, _ := ops["f_labels"].(map[string]any)
	if list, _ := added["add"].([]any); len(list) != 1 || list[0] != "started" {
		t.Errorf("the transition's set_ops = %v, want f_labels add [started]", last["set_ops"])
	}
	// Clearing: an empty list.
	out = ok(t, "ana", "item", "update", story, "--set", "f_labels=[]")
	if got := setOf(t, out, "f_labels"); got != nil {
		t.Errorf("after []: f_labels = %v, want unset", got)
	}

	// --set and --add of one field in one command contradict each other.
	r := pli(t, "ana", "item", "update", story, "--set", `f_labels=["a"]`, "--add", "f_labels=b")
	if r.code != 2 {
		t.Errorf("--set and --add of one field: exit %d (%s), want a usage error", r.code, r.stderr)
	}
	if r := pli(t, "ana", "item", "update", story, "--add", "f_labels"); r.code != 2 {
		t.Errorf("--add without a value: exit %d, want a usage error", r.code)
	}
	fails(t, "ana", "INVALID_ARGUMENT INVALID_REQUEST", "item", "update", story, "--add", "f_summary=x") // not a set
	// A new Item has no set to change: item create takes --set only.
	if r := pli(t, "ana", "item", "create", "--type", "story@3", "--in", space, "--add", "f_labels=x"); r.code != 2 {
		t.Errorf("item create --add: exit %d, want a usage error", r.code)
	}
}

func TestPli_MultiValue_WhereAndEmpty(t *testing.T) {
	space := platSpace(t)
	w := word()
	mk := func(labels string) string {
		args := []string{"--type", "story@3", "--in", space, "--set", "f_summary=Find " + w}
		if labels != "" {
			args = append(args, "--set", "f_labels="+labels)
		}
		return create(t, "ana", args...)
	}
	a, b, none := mk(`["backend","urgent"]`), mk(`["backend"]`), mk("")
	search := func(more ...string) []string {
		args := append([]string{"item", "search", "--type", "story", "--where", "f_summary=Find " + w}, more...)
		got := namesOf(t, ok(t, "ana", args...), "items")
		slices.Sort(got)
		return got
	}
	sorted := func(names ...string) []string { slices.Sort(names); return names }
	if got := search("--where", "f_labels=backend"); !slices.Equal(got, sorted(a, b)) {
		t.Errorf("--where f_labels=backend = %v, want the two", got)
	}
	if got := search("--where", "f_labels=backend", "--where", "f_labels=urgent"); !slices.Equal(got, []string{a}) {
		t.Errorf("backend and urgent = %v, want %v", got, a)
	}
	if got := search("--empty", "f_labels"); !slices.Equal(got, []string{none}) {
		t.Errorf("--empty f_labels = %v, want %v", got, none)
	}
	if got := search("--empty", "f_labels", "--empty", "f_resolved_at"); !slices.Equal(got, []string{none}) {
		t.Errorf("--empty twice = %v, want %v", got, none)
	}
	// On item list too.
	list := namesOf(t, ok(t, "ana", "item", "list", space, "--type", "story", "--where", "f_labels=urgent"), "items")
	if !slices.Equal(list, []string{a}) {
		t.Errorf("item list --where f_labels=urgent = %v, want %v", list, a)
	}
	fails(t, "ana", "INVALID_ARGUMENT INVALID_REQUEST", "item", "search", "--type", "story", "--where", "f_labels>=a")
	fails(t, "ana", "INVALID_ARGUMENT INVALID_REQUEST", "item", "search", "--type", "story", "--order-by", "f_labels")
}

// Eight --add commands at once, no etag: every label lands.
func TestPli_MultiValue_ConcurrentAdds(t *testing.T) {
	space := platSpace(t)
	story := create(t, "ana", "--type", "story@3", "--in", space, "--set", "f_summary=Race "+word())
	labels := []string{"a", "b", "c", "d", "e", "f", "g", "h"}
	var wg sync.WaitGroup
	codes := make([]int, len(labels))
	for i, l := range labels {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = pli(t, "ana", "item", "update", story, "--add", "f_labels="+l).code
		}()
	}
	wg.Wait()
	for i, code := range codes {
		if code != 0 {
			t.Fatalf("--add f_labels=%s: exit %d", labels[i], code)
		}
	}
	if got := setOf(t, ok(t, "ana", "item", "get", story), "f_labels"); !slices.Equal(got, labels) {
		t.Errorf("f_labels = %v, want all eight", got)
	}
}

// type get shows a container's reactions and what a created Type accepts.
func TestPli_TypeShowsReactions(t *testing.T) {
	ty := ok(t, "bob", "type", "get", "ticket@6")["type"].(map[string]any)
	byID := map[string]map[string]any{}
	for _, r := range ty["reactions"].([]any) {
		r := r.(map[string]any)
		byID[r["id"].(string)] = r
	}
	if r := byID["reopen"]; r["kind"] != "REACTION_KIND_MOVE" || r["trigger"] != "REACTION_TRIGGER_CREATED_INSIDE" ||
		r["to_state"] != "in_progress" || r["by"] != "creator" {
		t.Errorf("ticket@6 reopen = %v", r)
	}
	if r := byID["open_checklist"]; r["kind"] != "REACTION_KIND_CREATE" || r["create_type"] != "namespaces/acme/types/checklist/versions/1" {
		t.Errorf("ticket@6 open_checklist = %v", r)
	}
}
