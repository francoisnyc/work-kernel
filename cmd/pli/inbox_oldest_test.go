// Tests for "oldest waiting first" in pli (docs/roadmap.md, track 1, item
// 5, step 3a):
//
//	item search|list ... --waiting-on me|role:NAME --order-by waiting_since
//	inbox [WORDS...] [--role NAME] [FILTERS] [PAGING] [--oldest]
//
// --order-by takes waiting_since as it takes a field (the server judges
// it); inbox --oldest is inbox --order-by waiting_since. pli refuses only
// what it cannot send: --oldest together with --order-by.
package main

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"testing"
)

func TestPli_Inbox_OldestWaitingFirst(t *testing.T) {
	// Two tickets with one word: first is made, and reaches in_progress,
	// before second. Newest created first is [second first]; oldest
	// waiting first is [first second].
	w := word()
	mk := func() string {
		return create(t, "bob", "--type", "ticket@3", "--set", "f_title=approve "+w, "--set", "f_assignee=user-bob",
			"--set", "f_customer_id=cust-1", "--set", "f_amount=100")
	}
	first, second := mk(), mk()
	for _, name := range []string{first, second} {
		ok(t, "bob", "item", "transition", name, "--to", "open")
		ok(t, "bob", "item", "transition", name, "--to", "in_progress")
	}
	oldest, newest := []string{first, second}, []string{second, first}

	for _, c := range []struct {
		what string
		args []string
		want []string
	}{
		{"inbox", []string{"inbox", w}, newest}, // as before: newest created first
		{"inbox --oldest", []string{"inbox", w, "--oldest"}, oldest},
		{"inbox --order-by waiting_since", []string{"inbox", w, "--order-by", "waiting_since"}, oldest},
		{"inbox --order-by 'waiting_since desc'", []string{"inbox", w, "--order-by", "waiting_since desc"}, newest},
		{"inbox --role approver --oldest", []string{"inbox", w, "--role", "approver", "--oldest"}, oldest},
		{"item search --waiting-on me --order-by waiting_since", []string{"item", "search", w, "--waiting-on", "me", "--order-by", "waiting_since"}, oldest},
	} {
		if got := namesOf(t, ok(t, "ana", c.args...), "items"); !slices.Equal(got, c.want) {
			t.Errorf("%s = %v, want %v", c.what, got, c.want)
		}
	}

	// Paged: the second page follows the first.
	page := ok(t, "ana", "inbox", w, "--oldest", "--page-size", "1")
	token, _ := page["next_page_token"].(string)
	if got := namesOf(t, page, "items"); !slices.Equal(got, []string{first}) || token == "" {
		t.Fatalf("inbox --oldest, page 1 = %v, token %q; want [%s] and a token", got, token, first)
	}
	if got := namesOf(t, ok(t, "ana", "inbox", w, "--oldest", "--page-size", "1", "--page-token", token), "items"); !slices.Equal(got, []string{second}) {
		t.Errorf("inbox --oldest, page 2 = %v, want [%s]", got, second)
	}

	// The server refuses the order without --waiting-on.
	fails(t, "ana", "INVALID_ARGUMENT INVALID_REQUEST", "item", "search", w, "--order-by", "waiting_since")
	fails(t, "ana", "INVALID_ARGUMENT INVALID_REQUEST", "item", "list", "--order-by", "waiting_since")

	// --oldest with --order-by: nothing is sent.
	var stdout, stderr bytes.Buffer
	args := []string{"--server", srv.URL, "--tokens", tokensPath, "--as", "ana", "inbox", "--oldest", "--order-by", "f_amount"}
	if code := run(context.Background(), args, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "--oldest") {
		t.Errorf("inbox --oldest with --order-by: exit %d, stderr %q; want exit 2 mentioning --oldest", code, stderr.String())
	}
	// --oldest is inbox's own: item search does not take it.
	stdout.Reset()
	stderr.Reset()
	args = []string{"--server", srv.URL, "--tokens", tokensPath, "--as", "ana", "item", "search", w, "--waiting-on", "me", "--oldest"}
	if code := run(context.Background(), args, &stdout, &stderr); code != 2 {
		t.Errorf("item search --oldest: exit %d, want 2 (a usage error)", code)
	}

	// The usage names both spellings.
	stdout.Reset()
	stderr.Reset()
	run(context.Background(), []string{"--server", srv.URL, "--tokens", tokensPath, "--as", "ana"}, &stdout, &stderr)
	for _, want := range []string{"[--oldest]", "--order-by waiting_since"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("the usage does not mention %q", want)
		}
	}
}
