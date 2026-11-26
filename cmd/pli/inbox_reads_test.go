// Tests for the inbox screen's reads in pli (docs/roadmap.md, track 1,
// item 5, step 4, PR 1):
//
//	whoami
//	item count [WORDS...] [FILTERS]
//	inbox [WORDS...] [--role NAME] [FILTERS] [PAGING] [--oldest] [--count]
//	FILTERS: ... [--in ITEM]
//
// whoami prints WhoAmI's answer. item count is item search's words and
// filters, answered by CountItems: always both fields, {"count": 0,
// "capped": false}, so a script reads .count without a default. inbox
// --count is item count --waiting-on me (or role:NAME with --role). --in is
// one more FILTER (ItemFilter.in): the server judges it. pli refuses only
// what it cannot send: a count with an order or a page.
//
// Every Item answer prints state_time; a waiting-on answer prints moves.
package main

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"testing"
)

// refusedUnsent runs pli and expects exit 2 with nothing sent; it returns stderr.
func refusedUnsent(t *testing.T, who string, args ...string) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	all := append([]string{"--server", srv.URL, "--tokens", tokensPath, "--as", who}, args...)
	if code := run(context.Background(), all, &stdout, &stderr); code != 2 {
		t.Errorf("pli %v: exit %d, stderr %q; want 2 (a usage error)", args, code, stderr.String())
	}
	return stderr.String()
}

func TestPli_WhoAmI(t *testing.T) {
	// ana holds approver (granted when the tests start); bob holds nothing.
	ana := ok(t, "ana", "whoami")
	roles, _ := ana["roles"].([]any)
	if ana["principal"] != "user:ana" || ana["namespace"] != "namespaces/acme" || !slices.Contains(roles, any("approver")) {
		t.Errorf("whoami as ana = %v; want user:ana in namespaces/acme with the role approver", ana)
	}
	for _, key := range []string{"on_behalf_of", "on_behalf_of_roles", "session"} {
		if _, has := ana[key]; has {
			t.Errorf("whoami as ana prints %s: %v; a user's answer has none", key, ana[key])
		}
	}
	bob := ok(t, "bob", "whoami")
	if bob["principal"] != "user:bob" || slices.Contains(anyList(bob["roles"]), any("approver")) {
		t.Errorf("whoami as bob = %v; want user:bob without ana's role", bob)
	}

	// The bot, with the session agent start saved for it.
	started := ok(t, "bot", "agent", "start", "--for", "user:ana", "--scopes", "namespace", "--powers", "read")["session"].(map[string]any)
	bot := ok(t, "bot", "whoami")
	session, _ := bot["session"].(map[string]any)
	if bot["principal"] != "agent:triage-bot" || bot["on_behalf_of"] != "user:ana" ||
		!slices.Contains(anyList(bot["on_behalf_of_roles"]), any("approver")) ||
		session["id"] != started["id"] || session["acting_for"] != "user:ana" {
		t.Errorf("whoami as the bot = %v; want agent:triage-bot for user:ana (an approver) with session %v", bot, started["id"])
	}
	// A session that does not exist.
	var stdout, stderr bytes.Buffer
	args := []string{"--server", srv.URL, "--tokens", tokensPath, "--as", "bot", "--session", "00000000-0000-7000-8000-000000000000", "whoami"}
	if code := run(context.Background(), args, &stdout, &stderr); code != 1 || !strings.HasPrefix(stderr.String(), "error: UNAUTHENTICATED SESSION_INVALID") {
		t.Errorf("whoami with a session that does not exist: exit %d, stderr %q; want exit 1, UNAUTHENTICATED SESSION_INVALID", code, stderr.String())
	}

	if stderr := refusedUnsent(t, "ana", "whoami", "extra"); !strings.Contains(stderr, "extra") {
		t.Errorf("whoami with an argument: stderr %q; want it to name the argument", stderr)
	}
}

func anyList(v any) []any { l, _ := v.([]any); return l }

func TestPli_In(t *testing.T) {
	w := word()
	box := create(t, "ana", "--type", "ticket@1", "--set", "f_title=box "+w)
	inside := create(t, "ana", "--type", "ticket@1", "--in", box, "--set", "f_title=inside "+w)
	deeper := create(t, "ana", "--type", "ticket@1", "--in", inside, "--set", "f_title=deeper "+w)
	create(t, "ana", "--type", "ticket@1", "--set", "f_title=outside "+w)

	// A filter alone is a search; a bare ID is in the caller's namespace.
	if got := namesOf(t, ok(t, "bob", "item", "search", "--in", box), "items"); !slices.Equal(got, []string{deeper, inside}) {
		t.Errorf("item search --in the box = %v, want [%s %s]: everything below it, newest first", got, deeper, inside)
	}
	bare := box[strings.LastIndex(box, "/")+1:]
	if got := namesOf(t, ok(t, "bob", "item", "search", w, "--in", bare), "items"); !slices.Equal(got, []string{deeper, inside}) {
		t.Errorf("item search WORD --in a bare ID = %v, want [%s %s]", got, deeper, inside)
	}
	if got := namesOf(t, ok(t, "bob", "item", "search", w, "--in", inside), "items"); !slices.Equal(got, []string{deeper}) {
		t.Errorf("item search WORD --in the inner box = %v, want [%s]", got, deeper)
	}
	// The server judges it: a missing container, and item list (which has
	// its ITEM).
	fails(t, "bob", "NOT_FOUND NOT_FOUND", "item", "search", "--in", "00000000-0000-7000-8000-000000000000")
	fails(t, "bob", "INVALID_ARGUMENT INVALID_REQUEST", "item", "list", "--in", box)

	// inbox takes it: what waits on ana below a container.
	desk := create(t, "ana", "--type", "ticket@1")
	waiting := create(t, "bob", "--type", "ticket@3", "--in", desk, "--set", "f_title=approve "+w, "--set", "f_assignee=user-bob",
		"--set", "f_customer_id=cust-1", "--set", "f_amount=100")
	ok(t, "bob", "item", "transition", waiting, "--to", "open")
	ok(t, "bob", "item", "transition", waiting, "--to", "in_progress")
	elsewhere, _ := inProgress(t)
	if got := namesOf(t, ok(t, "ana", "inbox", "--in", desk), "items"); !slices.Equal(got, []string{waiting}) {
		t.Errorf("inbox --in the desk = %v, want [%s] (not %s, which waits elsewhere)", got, waiting, elsewhere)
	}
}

func TestPli_Count(t *testing.T) {
	first, w := inProgress(t)
	second := create(t, "bob", "--type", "ticket@3", "--set", "f_title=approve "+w, "--set", "f_assignee=user-bob",
		"--set", "f_customer_id=cust-1", "--set", "f_amount=100")
	ok(t, "bob", "item", "transition", second, "--to", "open")
	ok(t, "bob", "item", "transition", second, "--to", "in_progress")
	create(t, "bob", "--type", "ticket@3", "--set", "f_title=in triage "+w)
	_ = first

	for _, c := range []struct {
		who  string
		args []string
		want float64
	}{
		{"ana", []string{"item", "count", w}, 3},
		{"ana", []string{"item", "count", w, "--state", "in_progress"}, 2},
		{"ana", []string{"item", "count", w, "--waiting-on", "me"}, 2},
		{"ana", []string{"inbox", w, "--count"}, 2},
		{"bob", []string{"inbox", w, "--count"}, 0}, // no approver: both fields all the same
		{"bob", []string{"inbox", w, "--role", "approver", "--count"}, 2},
		{"ana", []string{"item", "count", word()}, 0},
	} {
		got := ok(t, c.who, c.args...)
		if len(got) != 2 || got["count"] != c.want || got["capped"] != false {
			t.Errorf("pli %v as %s = %v, want {count: %v, capped: false} and nothing else", c.args, c.who, got, c.want)
		}
	}

	// More than fifty: 50 and capped.
	many := word()
	for range 51 {
		create(t, "ana", "--type", "ticket@1", "--set", "f_title=many "+many)
	}
	if got := ok(t, "ana", "item", "count", many); got["count"] != float64(50) || got["capped"] != true {
		t.Errorf("item count of 51 matches = %v, want {count: 50, capped: true}", got)
	}

	// The server refuses what it refuses of item search.
	fails(t, "ana", "INVALID_ARGUMENT INVALID_REQUEST", "item", "count")
	fails(t, "ana", "INVALID_ARGUMENT INVALID_REQUEST", "item", "count", "--waiting-on", "user:bob")

	// A count has no order and no page: nothing is sent.
	for _, args := range [][]string{
		{"item", "count", w, "--order-by", "f_amount"},
		{"item", "count", w, "--page-size", "10"},
		{"item", "count", w, "--page-token", "t"},
		{"item", "count", w, "--min-seq", "1"},
		{"inbox", w, "--count", "--oldest"},
		{"inbox", w, "--count", "--page-size", "10"},
	} {
		if stderr := refusedUnsent(t, "ana", args...); !strings.Contains(stderr, "count") {
			t.Errorf("pli %v: stderr %q; want it to say what a count takes", args, stderr)
		}
	}
	// --count is inbox's own.
	refusedUnsent(t, "ana", "item", "search", w, "--count")
}

// Every Item pli prints has state_time; a waiting-on answer has the moves
// the caller may make.
func TestPli_StateTimeAndMoves(t *testing.T) {
	name, w := inProgress(t)
	got := itemOf(t, ok(t, "ana", "item", "get", name))
	if st, _ := got["state_time"].(string); st == "" || st == got["create_time"] {
		t.Errorf("item get: state_time %q, create_time %v; want when it reached in_progress, after its creation", st, got["create_time"])
	}
	if _, has := got["moves"]; has {
		t.Errorf("item get prints moves: %v; only a waiting-on answer has them", got["moves"])
	}
	items, _ := ok(t, "ana", "inbox", w)["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("ana's inbox for the word holds %d Items, want 1", len(items))
	}
	it := items[0].(map[string]any)
	if it["state_time"] != got["state_time"] || !slices.Contains(anyList(it["moves"]), any("approved")) {
		t.Errorf("inbox: state_time %v, moves %v; want %v and the approval among the moves", it["state_time"], it["moves"], got["state_time"])
	}
	// The approvers' queue read by bob, who is none: no approval for him.
	items, _ = ok(t, "bob", "inbox", w, "--role", "approver")["items"].([]any)
	if len(items) != 1 || slices.Contains(anyList(items[0].(map[string]any)["moves"]), any("approved")) {
		t.Errorf("bob reads the approvers' queue: %v; want the ticket without the approval among his moves", items)
	}
}

func TestPli_InboxReads_Usage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	run(context.Background(), []string{"--server", srv.URL, "--tokens", tokensPath, "--as", "ana"}, &stdout, &stderr)
	for _, want := range []string{"whoami", "item count [WORDS...] [FILTERS]", "[--oldest] [--count]", "[--in ITEM]"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("the usage does not mention %q", want)
		}
	}
}
