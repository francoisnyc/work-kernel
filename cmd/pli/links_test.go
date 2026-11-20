// Tests for proof of concept Step 1: pli commands for links and principal
// fields.
//
//	item link SRC LINK_TYPE TARGET [--etag E] [--request-id ID]
//	item unlink SRC LINK_TYPE TARGET [--etag E] [--request-id ID]
//	item get ITEM      (prints the Item with its links)
//
//	FILTERS: ... [--linked out:NAME=ITEM | in:NAME=ITEM]...
//
// LINK_TYPE is the declared name (blocks). In --linked the name is the one
// the results see: out: a declared name (Items that block ITEM), in: an
// inverse name (Items blocked by ITEM); the prefix is required. On a
// principal field, --where F=me means the caller.
package main

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"testing"
)

// linksOf renders an Item's links as "OUTGOING blocks <name>".
func linksOf(t *testing.T, it map[string]any) []string {
	t.Helper()
	list, _ := it["links"].([]any)
	var out []string
	for _, x := range list {
		l := x.(map[string]any)
		ref, _ := l["item"].(map[string]any)
		other, _ := ref["name"].(string)
		if r, _ := ref["restricted"].(bool); r {
			other = "restricted"
		}
		out = append(out, strings.TrimPrefix(l["direction"].(string), "LINK_DIRECTION_")+" "+l["link_type"].(string)+" "+other)
	}
	return out
}

func bareID(name string) string { return name[strings.LastIndex(name, "/")+1:] }

// Link, show, unlink, history; ends given as names or bare IDs.
func TestPli_LinkAndUnlink(t *testing.T) {
	t1 := create(t, "ana", "--type", "ticket@5", "--set", "f_title=first")
	t2 := create(t, "ana", "--type", "ticket@5", "--set", "f_title=second")

	out := ok(t, "ana", "item", "link", t1, "blocks", bareID(t2))
	if out["item_seq"] != "2" || itemOf(t, out)["name"] != t1 {
		t.Errorf("item link = %v, want the source at item_seq 2", out)
	}
	if got := linksOf(t, itemOf(t, out)); !slices.Equal(got, []string{"OUTGOING blocks " + t2}) {
		t.Errorf("link answer's links = %v", got)
	}
	if got := linksOf(t, itemOf(t, ok(t, "bob", "item", "get", t2))); !slices.Equal(got, []string{"INCOMING blocked_by " + t1}) {
		t.Errorf("item get t2 links = %v", got)
	}

	fails(t, "ana", "FAILED_PRECONDITION ALREADY_LINKED", "item", "link", t1, "blocks", t2)
	fails(t, "ana", "ABORTED STALE", "item", "unlink", t1, "blocks", t2, "--etag", "c1")
	un := ok(t, "ana", "item", "unlink", bareID(t1), "blocks", t2, "--etag", "c2")
	if got := linksOf(t, itemOf(t, un)); len(got) != 0 {
		t.Errorf("after unlink: links %v", got)
	}
	fails(t, "ana", "FAILED_PRECONDITION NOT_LINKED", "item", "unlink", t1, "blocks", t2)

	evs, _ := ok(t, "bob", "item", "history", t1)["events"].([]any)
	var kinds []string
	for _, e := range evs {
		kinds = append(kinds, e.(map[string]any)["kind"].(string))
	}
	want := []string{"ITEM_EVENT_KIND_CREATED", "ITEM_EVENT_KIND_LINK_ADDED", "ITEM_EVENT_KIND_LINK_REMOVED"}
	if !slices.Equal(kinds, want) {
		t.Errorf("history kinds = %v, want %v", kinds, want)
	}

	// A request ID replays.
	again := ok(t, "ana", "item", "link", t1, "duplicate_of", t2, "--request-id", "pli-link-"+word())
	if again["item_seq"] != "4" {
		t.Errorf("link with a request ID = %v", again)
	}
	fails(t, "ana", "FAILED_PRECONDITION LINK_CARDINALITY", "item", "link", t1, "duplicate_of",
		create(t, "ana", "--type", "ticket@5"))
}

// --linked and "me".
func TestPli_LinkedAndMeFilters(t *testing.T) {
	w := word()
	x := create(t, "ana", "--type", "ticket@5", "--set", "f_title="+w)
	y := create(t, "ana", "--type", "ticket@5", "--set", "f_title="+w, "--set", "f_assignee=user:bob")
	ok(t, "ana", "item", "link", x, "blocks", y)

	if got := namesOf(t, ok(t, "bob", "item", "search", "--linked", "out:blocks="+bareID(y)), "items"); !slices.Equal(got, []string{x}) {
		t.Errorf("--linked out:blocks=y = %v, want [%s]", got, x)
	}
	if got := namesOf(t, ok(t, "bob", "item", "search", "--linked", "in:blocked_by="+x), "items"); !slices.Equal(got, []string{y}) {
		t.Errorf("--linked in:blocked_by=x = %v, want [%s]", got, y)
	}
	if got := namesOf(t, ok(t, "bob", "item", "search", w, "--type", "ticket", "--where", "f_assignee=me"), "items"); !slices.Equal(got, []string{y}) {
		t.Errorf("assigned to me (bob) = %v, want [%s]", got, y)
	}
	if got := namesOf(t, ok(t, "ana", "item", "search", w, "--type", "ticket", "--where", "f_assignee=me"), "items"); len(got) != 0 {
		t.Errorf("assigned to me (ana) = %v, want none", got)
	}
}

func TestPli_LinkUsageErrors(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"link with two arguments", []string{"--as", "ana", "item", "link", "a", "blocks"}, "link"},
		{"unlink with one argument", []string{"--as", "ana", "item", "unlink", "a"}, "unlink"},
		{"--linked without a prefix", []string{"--as", "ana", "item", "search", "--linked", "blocks=x"}, "--linked"},
		{"--linked with a bad prefix", []string{"--as", "ana", "item", "search", "--linked", "up:blocks=x"}, "--linked"},
		{"--linked without an Item", []string{"--as", "ana", "item", "search", "--linked", "out:blocks"}, "--linked"},
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
