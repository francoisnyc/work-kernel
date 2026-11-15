// Tests for Step 0, part 2: pli commands for listing, history, search
// filters, paging and agent sessions (see the usage in main_test.go).
package main

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"testing"
)

func namesOf(t *testing.T, out map[string]any, key string) []string {
	t.Helper()
	list, _ := out[key].([]any)
	var names []string
	for _, x := range list {
		names = append(names, x.(map[string]any)["name"].(string))
	}
	return names
}

func create(t *testing.T, who string, args ...string) string {
	t.Helper()
	return itemOf(t, ok(t, who, append([]string{"item", "create"}, args...)...))["name"].(string)
}

// item list: a container's children (newest first) or the top level; paging.
func TestPli_List(t *testing.T) {
	box := create(t, "ana", "--type", "ticket@1")
	a := create(t, "ana", "--type", "ticket@1", "--in", box)
	b := create(t, "ana", "--type", "ticket@1", "--in", box)

	if got := namesOf(t, ok(t, "bob", "item", "list", box), "items"); !slices.Equal(got, []string{b, a}) {
		t.Errorf("item list = %v, want [%s %s]", got, b, a)
	}
	page := ok(t, "bob", "item", "list", box, "--page-size", "1")
	token, _ := page["next_page_token"].(string)
	if got := namesOf(t, page, "items"); !slices.Equal(got, []string{b}) || token == "" {
		t.Fatalf("page 1 = %v, token %q", got, token)
	}
	if got := namesOf(t, ok(t, "bob", "item", "list", box, "--page-size", "1", "--page-token", token), "items"); !slices.Equal(got, []string{a}) {
		t.Errorf("page 2 = %v, want [%s]", got, a)
	}

	newest := create(t, "ana", "--type", "ticket@1")
	if got := namesOf(t, ok(t, "bob", "item", "list", "--page-size", "1"), "items"); !slices.Equal(got, []string{newest}) {
		t.Errorf("item list (top level) = %v, want the newest, %s", got, newest)
	}
	listed := ok(t, "bob", "item", "list", box)["items"].([]any)[0].(map[string]any)
	if listed["creator"] != "user:ana" || listed["category"] != "STATE_CATEGORY_TODO" || listed["create_time"] == nil {
		t.Errorf("a listed Item = %v, want creator, category and create_time", listed)
	}
}

// item history: every event, oldest first, with values.
func TestPli_History(t *testing.T) {
	name := create(t, "ana", "--type", "ticket@1", "--set", "f_title=printer")
	ok(t, "bob", "item", "update", name, "--set", "f_assignee=user-bob")
	ok(t, "bob", "item", "transition", name, "--to", "in_progress")

	events := ok(t, "bob", "item", "history", name)["events"].([]any)
	var kinds []string
	for _, e := range events {
		kinds = append(kinds, e.(map[string]any)["kind"].(string))
	}
	want := []string{"ITEM_EVENT_KIND_CREATED", "ITEM_EVENT_KIND_UPDATED", "ITEM_EVENT_KIND_TRANSITIONED"}
	if !slices.Equal(kinds, want) {
		t.Fatalf("history kinds = %v, want %v", kinds, want)
	}
	first := events[0].(map[string]any)
	if first["actor"] != "user:ana" || first["fields"].(map[string]any)["f_title"] != "printer" {
		t.Errorf("create event = %v", first)
	}
	if page := ok(t, "bob", "item", "history", name, "--page-size", "2"); len(page["events"].([]any)) != 2 || page["next_page_token"] == "" {
		t.Errorf("history page of 2 = %v", page)
	}
}

// item search with filters, with and without words.
func TestPli_SearchFilters(t *testing.T) {
	w := word()
	low := create(t, "ana", "--type", "ticket@1", "--set", "f_title="+w, "--set", "f_priority=1")
	high := create(t, "ana", "--type", "ticket@1", "--set", "f_title="+w, "--set", "f_priority=7", "--set", "f_assignee=user-bob")
	ok(t, "ana", "item", "transition", high, "--to", "in_progress")

	cases := []struct {
		name string
		args []string
		want []string
	}{
		{"words and a state", []string{w, "--state", "open"}, []string{low}},
		{"a category", []string{w, "--category", "active"}, []string{high}},
		{"a filter alone", []string{"--type", "ticket", "--where", "f_title=" + w}, []string{low, high}},
		{"a range", []string{"--type", "ticket", "--where", "f_title=" + w, "--where", "f_priority>=5"}, []string{high}},
		{"a range below", []string{"--type", "ticket", "--where", "f_title=" + w, "--where", "f_priority<5"}, []string{low}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := namesOf(t, ok(t, "ana", append([]string{"item", "search"}, c.args...)...), "items")
			slices.Sort(got)
			want := slices.Clone(c.want)
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Errorf("search %v = %v, want %v", c.args, got, want)
			}
		})
	}
	fails(t, "ana", "INVALID_ARGUMENT INVALID_REQUEST", "item", "search", "--where", "f_priority>=5")
}

// agent start saves the session for the --as name; later commands use it;
// --session overrides; a read-only session cannot write.
func TestPli_AgentSession(t *testing.T) {
	name := create(t, "ana", "--type", "ticket@1")

	s := ok(t, "bot", "agent", "start", "--for", "user:bob", "--scopes", "namespace", "--powers", "read")
	session := s["session"].(map[string]any)
	if session["agent"] != "agent:triage-bot" || session["acting_for"] != "user:bob" || session["id"] == "" {
		t.Fatalf("agent start = %v", s)
	}
	ok(t, "bot", "item", "get", name)
	fails(t, "bot", "PERMISSION_DENIED NOT_ALLOWED", "item", "update", name, "--set", "f_title=bot")
	fails(t, "bot", "UNAUTHENTICATED SESSION_INVALID", "--session", "ses_made-up", "item", "get", name)
	fails(t, "bot", "PERMISSION_DENIED NOT_ALLOWED", "agent", "start", "--for", "user:bob", "--scopes", "namespace", "--powers", "read,edit_fields")

	// A session scoped to one container sees nothing else.
	box := create(t, "ana", "--type", "ticket@1")
	inside := create(t, "ana", "--type", "ticket@1", "--in", box)
	ok(t, "bot", "agent", "start", "--for", "user:ana", "--scopes", box, "--powers", "read,edit_fields")
	ok(t, "bot", "item", "get", inside)
	fails(t, "bot", "NOT_FOUND NOT_FOUND", "item", "get", name)
	ok(t, "bot", "item", "update", inside, "--set", "f_title=bot was here")

	// The older session still works when named.
	ok(t, "bot", "--session", session["id"].(string), "item", "get", name)
}

// A NEEDS_APPROVAL prints the scope; the user approves it with agent approve,
// and the agent's retry works. As in the scenario script, the session covers
// one container (capability mode), inside a grant for the whole namespace.
func TestPli_NeedsApprovalAndApprove(t *testing.T) {
	box := create(t, "ana", "--type", "channel@1", "--set", "f_name=support")
	private := create(t, "ana", "--type", "ticket@1", "--in", box, "--narrow", "user:ana,agent:triage-bot")
	sub := create(t, "ana", "--type", "ticket@1", "--in", box)
	session := ok(t, "bot", "agent", "start", "--for", "user:ana", "--scopes", box,
		"--powers", "read,create")["session"].(map[string]any)["id"].(string)
	ok(t, "bot", "item", "get", private)

	r := fails(t, "bot", "PERMISSION_DENIED NEEDS_APPROVAL", "item", "create", "--type", "ticket@1", "--in", sub)
	if !strings.Contains(r.stderr, "scope: "+sub) {
		t.Fatalf("stderr %q does not name the scope %s", r.stderr, sub)
	}
	// The approver is whoever --as names: the user, never the agent.
	fails(t, "bot", "INVALID_ARGUMENT INVALID_REQUEST", "agent", "approve", session, sub)
	ok(t, "ana", "agent", "approve", session, sub)
	ok(t, "bot", "item", "create", "--type", "ticket@1", "--in", sub)
	// Outside the session's container nothing exists, approval or not.
	fails(t, "bot", "NOT_FOUND NOT_FOUND", "item", "create", "--type", "ticket@1")
}

// Usage errors for the new commands exit 2 and send nothing.
func TestPli_Part2UsageErrors(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"agent start without scopes", []string{"--as", "bot", "agent", "start", "--powers", "read"}, "--scopes"},
		{"agent start without powers", []string{"--as", "bot", "agent", "start", "--scopes", "namespace"}, "--powers"},
		{"agent approve with one argument", []string{"--as", "ana", "agent", "approve", "ses_x"}, "approve"},
		// Its help says the approver comes from --as.
		{"agent approve's help", []string{"--as", "ana", "agent", "approve"}, "run it --as the user"},
		{"--where without an operator", []string{"--as", "ana", "item", "search", "--where", "f_priority"}, "--where"},
		{"unknown category", []string{"--as", "ana", "item", "search", "--category", "urgent"}, "urgent"},
		{"history without an Item", []string{"--as", "ana", "item", "history"}, "history"},
		{"bad page size", []string{"--as", "ana", "item", "list", "--page-size", "many"}, "--page-size"},
		{"unknown agent command", []string{"--as", "bot", "agent", "frobnicate"}, "frobnicate"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			args := append([]string{"--server", srv.URL, "--tokens", tokensPath}, c.args...)
			code := run(context.Background(), args, &stdout, &stderr)
			if code != 2 || !strings.Contains(stderr.String(), c.want) {
				t.Errorf("exit %d, stderr %q; want exit 2 mentioning %q", code, stderr.String(), c.want)
			}
		})
	}
}
