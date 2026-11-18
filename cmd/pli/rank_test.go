// Tests for the Jira proof of concept, part 1: ordering and rank in pli.
//
//	item search|list ... [--order-by 'FIELD [asc|desc]']
//	rank ITEM before|after OTHER        (same container)
//	rank ITEM top|bottom
//	rank rebalance --in CONTAINER
//
// Rank is an app convention, not a kernel feature: the string field f_rank
// on the PLAT Types, scoped to the Item's container. pli computes the keys
// with the shared package internal/rank and writes them with an ordinary
// UpdateItem; the kernel only sorts by the field.
//
//   - A key is computed against the CONTAINER's order (the backlog), so the
//     card lands right before or after the other one in every view that
//     shows both: the backlog, a sprint's board.
//   - A move is one write, whatever the keys look like. When the new key is
//     longer than rank.Threshold, pli says on stderr that the container
//     needs a rebalance, and still exits 0: maintenance never runs inside a
//     drag.
//   - The one exception to "one write": the two neighbours have the SAME
//     key (two people dropped cards into one gap and the jitter collided, or
//     keys were set by hand). No key fits between equal keys, so pli gives
//     the tied cards fresh keys too: a local re-spread of the run of equal
//     keys, never the container. A run longer than rank.MaxTiedRun is not
//     re-keyed: the move is refused and says to run a rebalance.
//   - rank rebalance rewrites one container's keys short and evenly spaced,
//     keeping the order; it is a separate, deliberate command.
package main

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/rank"
)

// platSpace makes a PLAT container.
func platSpace(t *testing.T) string {
	t.Helper()
	return create(t, "ana", "--type", "space@1", "--set", "f_key=PLAT", "--set", "f_name=Platform "+word())
}

// card makes a story in a container; with a rank when one is given.
func card(t *testing.T, space, summary, key string) string {
	t.Helper()
	args := []string{"--type", "story@1", "--in", space, "--set", "f_summary=" + summary}
	if key != "" {
		// Quoted, so a key of digits stays a string.
		args = append(args, "--set", `f_rank="`+key+`"`)
	}
	return create(t, "ana", args...)
}

var issueFilter = []string{"--type", "story", "--type", "bug", "--type", "task"}

// backlog lists a container's issues by rank.
func backlog(t *testing.T, space string) []string {
	t.Helper()
	args := append([]string{"item", "list", space, "--order-by", "f_rank", "--page-size", "500"}, issueFilter...)
	return namesOf(t, ok(t, "ana", args...), "items")
}

// rankOf reads an Item's rank key ("" when it has none) and its item_seq.
func rankOf(t *testing.T, name string) (key, seq string) {
	t.Helper()
	it := itemOf(t, ok(t, "ana", "item", "get", name))
	f, _ := it["fields"].(map[string]any)
	key, _ = f["f_rank"].(string)
	return key, it["etag"].(string)
}

func assertBacklog(t *testing.T, what, space string, want ...string) {
	t.Helper()
	if got := backlog(t, space); !slices.Equal(got, want) {
		t.Errorf("%s: the backlog is\n %v\nwant\n %v", what, got, want)
	}
}

// --- order_by ---

func TestPli_OrderBy(t *testing.T) {
	space := platSpace(t)
	b, a, c := card(t, space, "b", "b"), card(t, space, "a", "a"), card(t, space, "c", "c")
	unranked := card(t, space, "unranked", "")

	assertBacklog(t, "ascending", space, a, b, c, unranked)
	list := append([]string{"item", "list", space, "--order-by", "f_rank desc"}, issueFilter...)
	if got := namesOf(t, ok(t, "ana", list...), "items"); !slices.Equal(got, []string{c, b, a, unranked}) {
		t.Errorf("item list --order-by 'f_rank desc' = %v, want c b a, then the unranked one", got)
	}

	// On search too, with paging: the token goes with its order.
	search := func(orderBy string, more ...string) []string {
		args := append([]string{"item", "search", "--where", "f_rank>=a", "--order-by", orderBy, "--page-size", "2"}, issueFilter...)
		return append(args, more...)
	}
	first := ok(t, "ana", search("f_rank")...)
	if got := namesOf(t, first, "items"); len(got) != 2 {
		t.Fatalf("item search --order-by, first page = %v, want 2 Items", got)
	}
	token, _ := first["next_page_token"].(string)
	if token == "" {
		t.Fatal("item search --order-by: no next page token")
	}
	ok(t, "ana", search("f_rank", "--page-token", token)...)
	fails(t, "ana", "INVALID_ARGUMENT INVALID_REQUEST", search("f_rank desc", "--page-token", token)...)

	fails(t, "ana", "INVALID_ARGUMENT INVALID_REQUEST", "item", "list", space, "--order-by", "f_rank sideways")
	fails(t, "ana", "INVALID_ARGUMENT INVALID_REQUEST", "item", "list", space, "--order-by", "f_assignee", "--type", "story")
}

// --- moving a card ---

// before, after, top and bottom, each one write: the moved card's, and
// nobody else's.
func TestPli_Rank_MovesOneCardWithOneWrite(t *testing.T) {
	space := platSpace(t)
	a, b, c, d := card(t, space, "a", ""), card(t, space, "b", ""), card(t, space, "c", ""), card(t, space, "d", "")

	// The first key of an empty backlog, then the bottom twice.
	out := ok(t, "ana", "rank", a, "bottom")
	if it := itemOf(t, out); it["name"] != a || !rank.Valid(it["fields"].(map[string]any)["f_rank"].(string)) {
		t.Errorf("rank a bottom printed %v, want the Item with a valid rank key", out)
	}
	ok(t, "ana", "rank", b, "bottom")
	ok(t, "ana", "rank", c, "bottom")
	assertBacklog(t, "three cards ranked at the bottom in turn", space, a, b, c, d) // d: unranked, last

	steps := []struct {
		args []string
		want []string
	}{
		{[]string{"rank", c, "before", a}, []string{c, a, b, d}},
		{[]string{"rank", a, "after", b}, []string{c, b, a, d}},
		{[]string{"rank", d, "top"}, []string{d, c, b, a}},
		{[]string{"rank", d, "after", bareID(c)}, []string{c, d, b, a}},
		{[]string{"rank", b, "before", c}, []string{b, c, d, a}},
		{[]string{"rank", b, "bottom"}, []string{c, d, a, b}},
		// Moving a card to where it already is still lands it there.
		{[]string{"rank", d, "after", c}, []string{c, d, a, b}},
	}
	for _, s := range steps {
		moved := s.args[1]
		before := map[string]string{}
		for _, name := range []string{a, b, c, d} {
			_, before[name] = rankOf(t, name)
		}
		r := pli(t, "ana", s.args...)
		if r.code != 0 {
			t.Fatalf("pli %v: exit %d, stderr %q", s.args[2:], r.code, r.stderr)
		}
		if r.stderr != "" {
			t.Errorf("pli %v wrote to stderr: %q (keys this short need no rebalance)", s.args[2:], r.stderr)
		}
		assertBacklog(t, strings.Join(s.args[2:], " "), space, s.want...)
		for _, name := range []string{a, b, c, d} {
			key, etag := rankOf(t, name)
			switch {
			case name == moved && etag == before[name]:
				t.Errorf("pli %v: the moved card was not written", s.args[2:])
			case name != moved && etag != before[name]:
				t.Errorf("pli %v: wrote a card it did not move (etag %s -> %s): a move is one write", s.args[2:], before[name], etag)
			case !rank.Valid(key):
				t.Errorf("pli %v: a card has the rank %q, not a valid key", s.args[2:], key)
			}
		}
	}

	// One write is one event: a was created, ranked at the bottom, and
	// moved once in the steps above.
	evs, _ := ok(t, "ana", "item", "history", a)["events"].([]any)
	if len(evs) != 3 {
		t.Errorf("a's history has %d events, want 3: created, ranked at the bottom, moved after b", len(evs))
	}
}

// The key comes from the container's order, so a card moved next to
// another lands next to it on a board that shows only some of the cards.
func TestPli_Rank_HoldsOnASubsetView(t *testing.T) {
	space := platSpace(t)
	sprint := create(t, "ana", "--type", "sprint@1", "--in", space, "--set", "f_name=Sprint 12")
	var cards []string
	for _, s := range []string{"a", "b", "c", "d", "e"} {
		c := card(t, space, s, "")
		ok(t, "ana", "rank", c, "bottom")
		cards = append(cards, c)
	}
	a, b, c, d, e := cards[0], cards[1], cards[2], cards[3], cards[4]
	for _, onBoard := range []string{a, c, e} {
		ok(t, "ana", "item", "link", onBoard, "sprint", sprint)
	}
	board := func() []string {
		return namesOf(t, ok(t, "ana", "item", "search", "--linked", "out:sprint="+sprint, "--order-by", "f_rank"), "items")
	}
	if got := board(); !slices.Equal(got, []string{a, c, e}) {
		t.Fatalf("the board = %v, want a c e", got)
	}

	// e before c: on the board e is now between a and c. In the backlog it
	// is right before c, after b (which the board does not show).
	ok(t, "ana", "rank", e, "before", c)
	if got := board(); !slices.Equal(got, []string{a, e, c}) {
		t.Errorf("the board after ranking e before c = %v, want a e c", got)
	}
	assertBacklog(t, "the backlog after ranking e before c", space, a, b, e, c, d)
}

// What a move refuses: nothing is written.
func TestPli_Rank_Refusals(t *testing.T) {
	space, elsewhere := platSpace(t), platSpace(t)
	a, b := card(t, space, "a", "V"), card(t, space, "b", "")
	far := card(t, elsewhere, "far", "V")

	usage := func(what string, args ...string) {
		t.Helper()
		if r := pli(t, "ana", args...); r.code != 2 {
			t.Errorf("%s: exit %d (stderr %q), want a usage error (2)", what, r.code, r.stderr)
		}
	}
	usage("an unknown position", "rank", a, "sideways", b)
	usage("before without the other card", "rank", a, "before")
	usage("top with another card", "rank", a, "top", b)
	usage("a card before itself", "rank", a, "before", a)
	usage("rebalance without a container", "rank", "rebalance")

	refused := func(what, mention string, args ...string) {
		t.Helper()
		r := pli(t, "ana", args...)
		if r.code != 1 || !strings.Contains(r.stderr, mention) {
			t.Errorf("%s: exit %d, stderr %q; want exit 1 mentioning %q", what, r.code, r.stderr, mention)
		}
	}
	// Rank is scoped to a container: a key in one says nothing in another.
	refused("the other card is in another container", "container", "rank", a, "before", far)
	// A card with no rank has no place to be next to.
	refused("the other card has no rank", "no rank", "rank", a, "after", b)
	fails(t, "ana", "NOT_FOUND NOT_FOUND", "rank", a, "before", "00000000-0000-0000-0000-000000000000")

	if key, etag := rankOf(t, a); key != "V" || etag != "1" {
		t.Errorf("after the refusals a has rank %q, etag %s; want it untouched (V, 1)", key, etag)
	}
}

// A key past the threshold: the move is still one write and still
// succeeds; pli only says that the container needs a rebalance.
func TestPli_Rank_SaysWhenARebalanceIsDue(t *testing.T) {
	space := platSpace(t)
	long := strings.Repeat("V", rank.Threshold+2)
	above, below := card(t, space, "above", long), card(t, space, "below", long+"V")
	moved := card(t, space, "moved", "")
	_, aboveTag := rankOf(t, above)
	_, belowTag := rankOf(t, below)

	r := pli(t, "ana", "rank", moved, "after", above)
	if r.code != 0 {
		t.Fatalf("rank into a gap between long keys: exit %d, stderr %q; want success", r.code, r.stderr)
	}
	if !strings.Contains(r.stderr, "rebalance") || !strings.Contains(r.stderr, bareID(space)) {
		t.Errorf("stderr = %q, want it to say that the container %s needs a rebalance", r.stderr, bareID(space))
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(r.stdout), &out); err != nil {
		t.Errorf("stdout is not the Item as JSON (%v): %q", err, r.stdout)
	}
	assertBacklog(t, "after the move", space, above, moved, below)
	if key, _ := rankOf(t, moved); !rank.NeedsRebalance(key) {
		t.Errorf("the moved card's key is %d bytes, test bug: not past the threshold", len(key))
	}
	// One write, and no rebalance ran: the neighbours are untouched.
	if _, tag := rankOf(t, above); tag != aboveTag {
		t.Error("the card above was rewritten: a move must never rebalance")
	}
	if _, tag := rankOf(t, below); tag != belowTag {
		t.Error("the card below was rewritten: a move must never rebalance")
	}
}

// Two neighbours with the same key leave no room between them: the move
// re-keys the tied cards as well, and only them.
func TestPli_Rank_EqualNeighboursAreRespread(t *testing.T) {
	space := platSpace(t)
	first, second := card(t, space, "first", "V"), card(t, space, "second", "V") // tied: by ID, first then second
	top, bottom := card(t, space, "top", "A"), card(t, space, "bottom", "k")
	moved := card(t, space, "moved", "")
	assertBacklog(t, "before", space, top, first, second, bottom, moved)
	_, topTag := rankOf(t, top)
	_, bottomTag := rankOf(t, bottom)

	ok(t, "ana", "rank", moved, "before", second)
	assertBacklog(t, "moved between two cards that had the same key", space, top, first, moved, second, bottom)
	keys := map[string]bool{}
	for _, name := range []string{top, first, moved, second, bottom} {
		key, _ := rankOf(t, name)
		if !rank.Valid(key) || keys[key] {
			t.Errorf("after the re-spread the key %q is invalid or used twice", key)
		}
		keys[key] = true
	}
	if _, tag := rankOf(t, top); tag != topTag {
		t.Error("the re-spread rewrote a card outside the tied run (top)")
	}
	if _, tag := rankOf(t, bottom); tag != bottomTag {
		t.Error("the re-spread rewrote a card outside the tied run (bottom)")
	}
	if _, tag := rankOf(t, moved); tag != "2" {
		t.Errorf("the moved card's etag = %s, want 2: it is written once", tag)
	}
}

// The re-spread has a cap. A run of rank.MaxTiedRun cards with one key is
// still re-keyed by the move, each card once; one card more and the move is
// refused, writes nothing, and says to rebalance. After the rebalance the
// same move is an ordinary one.
func TestPli_Rank_TiedRunCap(t *testing.T) {
	tied := func(space string, n int) []string {
		var cards []string
		for range n {
			cards = append(cards, card(t, space, "tied", "V"))
		}
		return cards // in ID order: the order they sort in
	}

	// At the cap: re-keyed.
	space := platSpace(t)
	top, run, bottom := card(t, space, "top", "A"), tied(space, rank.MaxTiedRun), card(t, space, "bottom", "k")
	moved := card(t, space, "moved", "")
	ok(t, "ana", "rank", moved, "before", run[4])
	want := slices.Concat([]string{top}, run[:4], []string{moved}, run[4:], []string{bottom})
	assertBacklog(t, "a move into a run at the cap", space, want...)
	keys := map[string]bool{}
	for _, name := range want {
		key, etag := rankOf(t, name)
		if !rank.Valid(key) || keys[key] {
			t.Errorf("after the re-spread the key %q is invalid or used twice", key)
		}
		keys[key] = true
		if outside := name == top || name == bottom; outside && etag != "1" {
			t.Errorf("the re-spread rewrote a card outside the tied run (etag %s)", etag)
		} else if !outside && etag != "2" {
			t.Errorf("a card of the tied run has etag %s after the re-spread, want 2: written once", etag)
		}
	}

	// One past the cap: refused, nothing written.
	space = platSpace(t)
	run = tied(space, rank.MaxTiedRun+1)
	moved = card(t, space, "moved", "")
	r := pli(t, "ana", "rank", moved, "before", run[4])
	if r.code != 1 || !strings.Contains(r.stderr, "rebalance") || !strings.Contains(r.stderr, bareID(space)) {
		t.Fatalf("a move into a run past the cap: exit %d, stderr %q; want exit 1 saying to rebalance %s", r.code, r.stderr, bareID(space))
	}
	for _, name := range append(slices.Clone(run), moved) {
		if _, etag := rankOf(t, name); etag != "1" {
			t.Errorf("the refused move wrote a card (etag %s)", etag)
		}
	}
	// The first card of the run still has room above it: one write.
	ok(t, "ana", "rank", moved, "before", run[0])
	assertBacklog(t, "a move above the whole run", space, append([]string{moved}, run...)...)
	if _, etag := rankOf(t, run[0]); etag != "1" {
		t.Errorf("a move above the run rewrote its first card (etag %s)", etag)
	}

	ok(t, "ana", "rank", "rebalance", "--in", space)
	r = pli(t, "ana", "rank", moved, "before", run[4])
	if r.code != 0 || r.stderr != "" {
		t.Fatalf("the same move after a rebalance: exit %d, stderr %q", r.code, r.stderr)
	}
	assertBacklog(t, "the same move after a rebalance", space, slices.Concat(run[:4], []string{moved}, run[4:])...)
}

// --- rebalance ---

// rank rebalance rewrites one container's keys short and evenly spaced,
// in the same order, ties by ID; cards without a rank and other containers
// are left alone. It reports what it did.
func TestPli_Rank_Rebalance(t *testing.T) {
	space, elsewhere := platSpace(t), platSpace(t)
	long := strings.Repeat("V", rank.Threshold+8)
	c1 := card(t, space, "1", "A")
	c2 := card(t, space, "2", long)
	c3 := card(t, space, "3", long) // the same key as c2: after it, by ID
	c4 := card(t, space, "4", long+"V")
	c5 := create(t, "ana", "--type", "bug@1", "--in", space, "--set", "f_summary=5", "--set", "f_rank=z")
	unranked := card(t, space, "unranked", "")
	epic := create(t, "ana", "--type", "epic@1", "--in", space, "--set", "f_summary=an epic has no rank")
	other := card(t, elsewhere, "other", long)
	assertBacklog(t, "before", space, c1, c2, c3, c4, c5, unranked)

	r := pli(t, "ana", "rank", "rebalance", "--in", space)
	if r.code != 0 {
		t.Fatalf("rank rebalance: exit %d, stderr %q", r.code, r.stderr)
	}
	var report struct {
		Container string `json:"container"`
		Rewritten int    `json:"rewritten"`
		Skipped   int    `json:"skipped"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &report); err != nil {
		t.Fatalf("rank rebalance printed %q, not its report as JSON: %v", r.stdout, err)
	}
	if report.Container != space || report.Rewritten != 5 || report.Skipped != 0 {
		t.Errorf("report = %+v, want container %s, 5 rewritten, 0 skipped", report, space)
	}

	assertBacklog(t, "after", space, c1, c2, c3, c4, c5, unranked)
	keys := map[string]bool{}
	for _, name := range []string{c1, c2, c3, c4, c5} {
		key, etag := rankOf(t, name)
		if !rank.Valid(key) || len(key) > 3 || keys[key] {
			t.Errorf("after the rebalance a card has the key %q: want a valid, distinct key of at most 3 bytes", key)
		}
		keys[key] = true
		if etag != "2" {
			t.Errorf("a rebalanced card's etag = %s, want 2: one write each", etag)
		}
	}
	for what, name := range map[string]string{"the unranked card": unranked, "the epic": epic, "a card of another container": other} {
		if _, etag := rankOf(t, name); etag != "1" {
			t.Errorf("%s was written by the rebalance (etag %s)", what, etag)
		}
	}
	if key, _ := rankOf(t, other); key != long {
		t.Errorf("a card of another container has the key %q after the rebalance", key)
	}

	// The next move is an ordinary one-write move, with nothing to report.
	r = pli(t, "ana", "rank", unranked, "after", c2)
	if r.code != 0 || r.stderr != "" {
		t.Errorf("a move after the rebalance: exit %d, stderr %q", r.code, r.stderr)
	}
	assertBacklog(t, "a move after the rebalance", space, c1, c2, unranked, c3, c4, c5)

	// A container the caller cannot see, or with nothing ranked.
	fails(t, "ana", "NOT_FOUND NOT_FOUND", "rank", "rebalance", "--in", "00000000-0000-0000-0000-000000000000")
	empty := platSpace(t)
	if out := ok(t, "ana", "rank", "rebalance", "--in", empty); out["rewritten"] != float64(0) {
		t.Errorf("rebalancing an empty container = %v, want 0 rewritten", out)
	}
}
