// Tests for "waiting on me" in pli (docs/roadmap.md, track 1, item 5, step 1):
//
//	item search [WORDS...] [--waiting-on me|role:NAME] ...
//	item list [ITEM] [--waiting-on me|role:NAME] ...
//	inbox [WORDS...] [--role NAME] [FILTERS] [PAGING]
//
// --waiting-on is one more FILTER (ItemFilter.waiting_on). inbox is
// `item search --waiting-on me`, and with --role NAME, `--waiting-on
// role:NAME`: the same answer, printed the same way. The server judges the
// value; pli refuses only what it cannot send (inbox with both --role and
// --waiting-on).
//
// ticket@3's approval (in_progress -> approved) names role:approver, which
// ana holds in these tests and bob does not.
package main

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"testing"
)

// inProgress makes a ticket@3 with a word of its own and walks it to
// in_progress, where it waits on the approvers.
func inProgress(t *testing.T) (name, w string) {
	t.Helper()
	w = word()
	name = create(t, "bob", "--type", "ticket@3", "--set", "f_title=approve "+w, "--set", "f_assignee=user-bob",
		"--set", "f_customer_id=cust-1", "--set", "f_amount=100")
	ok(t, "bob", "item", "transition", name, "--to", "open")
	ok(t, "bob", "item", "transition", name, "--to", "in_progress")
	return name, w
}

func TestPli_Inbox(t *testing.T) {
	name, w := inProgress(t)
	open := create(t, "bob", "--type", "ticket@3", "--set", "f_title=not yet "+w) // in triage: nobody is named

	// The word keeps the answer to this test's two tickets.
	if got := namesOf(t, ok(t, "ana", "inbox", w), "items"); !slices.Equal(got, []string{name}) {
		t.Errorf("ana's inbox = %v, want [%s] (not %s, still in triage)", got, name, open)
	}
	if got := namesOf(t, ok(t, "bob", "inbox", w), "items"); len(got) != 0 {
		t.Errorf("bob's inbox = %v, want nothing: he is no approver", got)
	}
	// A role's queue, asked by someone who does not hold the role.
	if got := namesOf(t, ok(t, "bob", "inbox", w, "--role", "approver"), "items"); !slices.Equal(got, []string{name}) {
		t.Errorf("the approvers' queue, asked by bob = %v, want [%s]", got, name)
	}
	// inbox is item search --waiting-on: the same output.
	for _, pair := range [][2][]string{
		{{"inbox", w}, {"item", "search", w, "--waiting-on", "me"}},
		{{"inbox", w, "--role", "approver"}, {"item", "search", w, "--waiting-on", "role:approver"}},
	} {
		a, b := pli(t, "ana", pair[0]...), pli(t, "ana", pair[1]...)
		if a.code != 0 || b.code != 0 || a.stdout != b.stdout {
			t.Errorf("pli %v (exit %d) and pli %v (exit %d) print different answers:\n%s\n%s",
				pair[0], a.code, pair[1], b.code, a.stdout, b.stdout)
		}
	}
	// With no words at all, across every Type: the ticket is in it.
	if got := namesOf(t, ok(t, "ana", "inbox", "--page-size", "500"), "items"); !slices.Contains(got, name) || slices.Contains(got, open) {
		t.Errorf("ana's whole inbox holds this test's waiting ticket: %v, and its triage one: %v; want true, false",
			slices.Contains(got, name), slices.Contains(got, open))
	}
}

// inbox and --waiting-on take the other filters and paging.
func TestPli_Inbox_FiltersAndPaging(t *testing.T) {
	first, w := inProgress(t)
	second := create(t, "bob", "--type", "ticket@3", "--set", "f_title=approve "+w, "--set", "f_assignee=user-bob",
		"--set", "f_customer_id=cust-1", "--set", "f_amount=100")
	ok(t, "bob", "item", "transition", second, "--to", "open")
	ok(t, "bob", "item", "transition", second, "--to", "in_progress")

	if got := namesOf(t, ok(t, "ana", "inbox", w, "--type", "ticket", "--state", "in_progress"), "items"); !slices.Equal(got, []string{second, first}) {
		t.Errorf("inbox with a Type and a state = %v, want [%s %s], newest first", got, second, first)
	}
	if got := namesOf(t, ok(t, "ana", "inbox", w, "--type", "bug"), "items"); len(got) != 0 {
		t.Errorf("inbox of another Type = %v, want nothing", got)
	}
	page := ok(t, "ana", "inbox", w, "--page-size", "1")
	token, _ := page["next_page_token"].(string)
	if got := namesOf(t, page, "items"); !slices.Equal(got, []string{second}) || token == "" {
		t.Fatalf("inbox page 1 = %v, token %q; want [%s] and a token", got, token, second)
	}
	if got := namesOf(t, ok(t, "ana", "inbox", w, "--page-size", "1", "--page-token", token), "items"); !slices.Equal(got, []string{first}) {
		t.Errorf("inbox page 2 = %v, want [%s]", got, first)
	}

	// item list takes --waiting-on too: a container's children that wait.
	box := create(t, "ana", "--type", "ticket@1")
	inside := create(t, "bob", "--type", "ticket@3", "--in", box, "--set", "f_title=inside", "--set", "f_assignee=user-bob",
		"--set", "f_customer_id=cust-1", "--set", "f_amount=100")
	ok(t, "bob", "item", "transition", inside, "--to", "open")
	ok(t, "bob", "item", "transition", inside, "--to", "in_progress")
	create(t, "bob", "--type", "ticket@3", "--in", box, "--set", "f_title=inside, in triage")
	if got := namesOf(t, ok(t, "ana", "item", "list", box, "--waiting-on", "me"), "items"); !slices.Equal(got, []string{inside}) {
		t.Errorf("item list --waiting-on me = %v, want [%s]", got, inside)
	}
}

func TestPli_Inbox_Refusals(t *testing.T) {
	// The server judges the value.
	fails(t, "ana", "INVALID_ARGUMENT INVALID_REQUEST", "item", "search", "--waiting-on", "everyone")
	fails(t, "ana", "INVALID_ARGUMENT INVALID_REQUEST", "inbox", "--role", "Not A Name")

	// inbox with both: nothing is sent.
	var stdout, stderr bytes.Buffer
	args := []string{"--server", srv.URL, "--tokens", tokensPath, "--as", "ana", "inbox", "--role", "approver", "--waiting-on", "me"}
	if code := run(context.Background(), args, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "--role") {
		t.Errorf("inbox --role with --waiting-on: exit %d, stderr %q; want exit 2 mentioning --role", code, stderr.String())
	}

	// The usage names both.
	stdout.Reset()
	stderr.Reset()
	run(context.Background(), []string{"--server", srv.URL, "--tokens", tokensPath, "--as", "ana"}, &stdout, &stderr)
	for _, want := range []string{"inbox [WORDS...] [--role NAME] [FILTERS] [PAGING]", "--waiting-on me|role:NAME"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("the usage does not mention %q", want)
		}
	}
}
