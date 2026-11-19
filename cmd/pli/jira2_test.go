// Tests for Jira part 2, step 1, through pli: a datetime is set and
// filtered like any value (--set, --where, --order-by), the Type shows its
// timers and effects, and a story's resolution time is set and cleared.
// pli needs no flag of its own for any of it.
package main

import (
	"slices"
	"testing"
)

// --set and --where take an RFC 3339 instant in any offset (the + and the
// colons are part of the value); the answer holds the UTC form.
func TestPli_DateTime_SetWhereOrderBy(t *testing.T) {
	space := platSpace(t)
	name := "Sprint " + word()
	sprint := func(end string) string {
		args := []string{"--type", "sprint@2", "--in", space, "--set", "f_name=" + name}
		if end != "" {
			args = append(args, "--set", "f_end_at="+end)
		}
		return create(t, "ana", args...)
	}
	a := sprint("2026-12-04T23:00:00+06:00") // 17:00Z
	b := sprint("2026-12-04T18:00:00Z")
	none := sprint("")

	got := itemOf(t, ok(t, "ana", "item", "get", a))["fields"].(map[string]any)["f_end_at"]
	if got != "2026-12-04T17:00:00.000000Z" {
		t.Errorf("item get: f_end_at = %v, want 2026-12-04T17:00:00.000000Z", got)
	}
	updated := itemOf(t, ok(t, "ana", "item", "update", none, "--set", "f_end_at=2026-12-05T09:00:00+01:00"))
	if got := updated["fields"].(map[string]any)["f_end_at"]; got != "2026-12-05T08:00:00.000000Z" {
		t.Errorf("item update: f_end_at = %v, want 2026-12-05T08:00:00.000000Z", got)
	}

	search := func(more ...string) []string {
		args := append([]string{"item", "search", "--type", "sprint", "--where", "f_name=" + name}, more...)
		return namesOf(t, ok(t, "ana", args...), "items")
	}
	if got := search("--order-by", "f_end_at"); !slices.Equal(got, []string{a, b, none}) {
		t.Errorf("--order-by f_end_at = %v, want %v", got, []string{a, b, none})
	}
	if got := search("--order-by", "f_end_at desc"); !slices.Equal(got, []string{none, b, a}) {
		t.Errorf("--order-by 'f_end_at desc' = %v, want %v", got, []string{none, b, a})
	}
	if got := search("--where", "f_end_at>=2026-12-04T19:00:00+01:00", "--where", "f_end_at<2026-12-05T00:00:00Z"); !slices.Equal(got, []string{b}) {
		t.Errorf("a range with an offset bound = %v, want %v", got, b)
	}
	if got := search("--where", "f_end_at=2026-12-04T17:00:00Z"); !slices.Equal(got, []string{a}) {
		t.Errorf("an instant by equality = %v, want %v", got, a)
	}

	fails(t, "ana", "INVALID_ARGUMENT INVALID_REQUEST", "item", "update", a, "--set", "f_end_at=2026-12-04T17:00:00")
	fails(t, "ana", "INVALID_ARGUMENT INVALID_REQUEST", "item", "search", "--type", "sprint", "--where", "f_end_at>2026-12-04")
}

// type get shows a state's at-timer and a transition's computed effects.
func TestPli_TypeShowsTimersAndEffects(t *testing.T) {
	ty := ok(t, "bob", "type", "get", "sprint@2")["type"].(map[string]any)
	var at map[string]any
	for _, s := range ty["states"].([]any) {
		if s := s.(map[string]any); s["id"] == "active" {
			at, _ = s["at"].(map[string]any)
		}
	}
	if at["field"] != "f_end_at" || at["to_state"] != "closed" {
		t.Errorf("sprint@2's active state: at = %v, want f_end_at to closed", at)
	}

	ty = ok(t, "bob", "type", "get", "story@2")["type"].(map[string]any)
	sets := map[string]any{}
	for _, tr := range ty["transitions"].([]any) {
		tr := tr.(map[string]any)
		if s, has := tr["sets"].(map[string]any); has {
			sets[tr["from_state"].(string)+"->"+tr["to_state"].(string)] = s["f_resolved_at"]
		}
	}
	want := map[string]any{"in_review->done": "cause.at", "in_progress->done": "cause.at", "done->todo": "null"}
	if len(sets) != len(want) {
		t.Fatalf("story@2's effects = %v, want %v", sets, want)
	}
	for move, expr := range want {
		if sets[move] != expr {
			t.Errorf("story@2: %s sets f_resolved_at = %v, want %v", move, sets[move], expr)
		}
	}
}

// Done stamps the story; reopening unstamps it; setting it by hand is
// refused.
func TestPli_ResolvedAt(t *testing.T) {
	space := platSpace(t)
	story := create(t, "ana", "--type", "story@2", "--in", space, "--set", "f_summary=Resolve "+word())
	ok(t, "ana", "item", "transition", story, "--to", "in_progress")
	fails(t, "ana", "PERMISSION_DENIED FIELD_NOT_WRITABLE", "item", "update", story, "--set", "f_resolved_at=2026-12-04T17:00:00Z")

	done := itemOf(t, ok(t, "ana", "item", "transition", story, "--to", "done", "--set", "f_resolution=fixed"))
	at, _ := done["fields"].(map[string]any)["f_resolved_at"].(string)
	if len(at) != len("2026-12-04T17:00:00.000000Z") || at[len(at)-1] != 'Z' {
		t.Fatalf("f_resolved_at = %q after done, want an instant in the UTC form", at)
	}
	hist := ok(t, "ana", "item", "history", story)["events"].([]any)
	last := hist[len(hist)-1].(map[string]any)
	if got := last["fields"].(map[string]any)["f_resolved_at"]; got != at {
		t.Errorf("the done event's f_resolved_at = %v, want %v", got, at)
	}

	reopened := itemOf(t, ok(t, "ana", "item", "transition", story, "--to", "todo"))
	if v, set := reopened["fields"].(map[string]any)["f_resolved_at"]; set {
		t.Errorf("f_resolved_at = %v after reopening, want it unset", v)
	}
}
