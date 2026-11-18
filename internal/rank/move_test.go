package rank_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/rank"
)

// board is a container in memory: cards in the kernel's order (key, then
// ID), read through rank.Source.
type board struct{ cards []rank.Card }

func (b *board) sorted() []rank.Card {
	out := slices.Clone(b.cards)
	slices.SortFunc(out, func(x, y rank.Card) int {
		if c := strings.Compare(x.Key, y.Key); c != 0 {
			return c
		}
		return strings.Compare(x.ID, y.ID)
	})
	return out
}

func (b *board) Scan(_ context.Context, q rank.Query) ([]rank.Card, error) {
	var out []rank.Card
	for _, c := range b.sorted() {
		switch {
		case c.Key == "":
		case q.Eq != "" && c.Key != q.Eq, q.Gt != "" && c.Key <= q.Gt, q.Lt != "" && c.Key >= q.Lt:
		default:
			out = append(out, c)
		}
	}
	if q.Desc {
		// Descending by key, ties still by ID ascending.
		slices.SortStableFunc(out, func(x, y rank.Card) int { return strings.Compare(y.Key, x.Key) })
	}
	if len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return out, nil
}

func (b *board) apply(writes []rank.Write) {
	for _, w := range writes {
		i := slices.IndexFunc(b.cards, func(c rank.Card) bool { return c.ID == w.ID })
		if i < 0 {
			b.cards = append(b.cards, rank.Card{ID: w.ID, Key: w.Key})
			continue
		}
		b.cards[i].Key = w.Key
	}
}

func (b *board) order() string {
	var ids []string
	for _, c := range b.sorted() {
		if c.Key != "" {
			ids = append(ids, c.ID)
		}
	}
	return strings.Join(ids, " ")
}

func (b *board) card(id string) rank.Card {
	return b.cards[slices.IndexFunc(b.cards, func(c rank.Card) bool { return c.ID == id })]
}

// A move is one write when the neighbours have different keys, wherever
// the card goes.
func TestPlan_OneWrite(t *testing.T) {
	b := &board{cards: []rank.Card{{"a", "F"}, {"b", "V"}, {"c", "k"}, {"x", ""}}}
	steps := []struct {
		pos   rank.Position
		other string
		want  string
	}{
		{rank.Bottom, "", "a b c x"},
		{rank.Top, "", "x a b c"},
		{rank.Before, "b", "a x b c"},
		{rank.After, "b", "a b x c"},
		{rank.After, "c", "a b c x"},
		{rank.Before, "a", "x a b c"},
		{rank.After, "a", "a x b c"}, // x was a's neighbour on the other side
	}
	for _, s := range steps {
		var other rank.Card
		if s.other != "" {
			other = b.card(s.other)
		}
		writes, err := rank.Plan(context.Background(), b, "x", s.pos, other)
		if err != nil {
			t.Fatalf("Plan(%v %s): %v", s.pos, s.other, err)
		}
		if len(writes) != 1 || writes[0].ID != "x" || !rank.Valid(writes[0].Key) {
			t.Fatalf("Plan(%v %s) = %v, want one valid key for x", s.pos, s.other, writes)
		}
		b.apply(writes)
		if got := b.order(); got != s.want {
			t.Errorf("after %v %s the order is %q, want %q", s.pos, s.other, got, s.want)
		}
	}
	// The first key of an empty container.
	empty := &board{}
	writes, err := rank.Plan(context.Background(), empty, "x", rank.Bottom, rank.Card{})
	if err != nil || len(writes) != 1 {
		t.Errorf("Plan into an empty container = %v, %v", writes, err)
	}
	if _, err := rank.Plan(context.Background(), b, "x", rank.Before, rank.Card{ID: "u"}); !errors.Is(err, rank.ErrNoRank) {
		t.Errorf("Plan next to a card with no rank: %v, want ErrNoRank", err)
	}
}

// Tied neighbours: the run is re-keyed with the moved card in its place,
// up to MaxTiedRun cards; a longer run is refused. The edges of a run need
// no re-keying, however long it is.
func TestPlan_TiedRun(t *testing.T) {
	tied := func(n int) *board {
		b := &board{cards: []rank.Card{{"a", "F"}, {"z", "k"}, {"x", ""}}}
		for i := range n {
			b.cards = append(b.cards, rank.Card{ID: fmt.Sprintf("t%02d", i), Key: "V"})
		}
		return b
	}
	for _, pos := range []rank.Position{rank.Before, rank.After} {
		b := tied(rank.MaxTiedRun)
		writes, err := rank.Plan(context.Background(), b, "x", pos, b.card("t03"))
		if err != nil {
			t.Fatalf("Plan into a run at the cap: %v", err)
		}
		if len(writes) != rank.MaxTiedRun+1 {
			t.Errorf("Plan into a run at the cap wrote %d keys, want %d: the run and the moved card", len(writes), rank.MaxTiedRun+1)
		}
		b.apply(writes)
		want := "a t00 t01 t02 x t03 t04 t05 t06 t07 z"
		if pos == rank.After {
			want = "a t00 t01 t02 t03 x t04 t05 t06 t07 z"
		}
		if got := b.order(); got != want {
			t.Errorf("after the re-spread (%v) the order is %q, want %q", pos, got, want)
		}

		long := tied(rank.MaxTiedRun + 1)
		if w, err := rank.Plan(context.Background(), long, "x", pos, long.card("t03")); !errors.Is(err, rank.ErrTiedRunTooLong) {
			t.Errorf("Plan into a run past the cap = %v, %v; want ErrTiedRunTooLong", w, err)
		}
	}
	long := tied(rank.MaxTiedRun + 5)
	writes, err := rank.Plan(context.Background(), long, "x", rank.Before, long.card("t00"))
	if err != nil || len(writes) != 1 {
		t.Fatalf("Plan above a long run = %v, %v; want one write", writes, err)
	}
	long.apply(writes)
	if got := long.order(); !strings.HasPrefix(got, "a x t00 ") {
		t.Errorf("after a move above a long run the order is %q", got)
	}
	// The bottom edge of a run that fits in one read is one write too.
	b := tied(3)
	writes, err = rank.Plan(context.Background(), b, "x", rank.After, b.card("t02"))
	if err != nil || len(writes) != 1 {
		t.Fatalf("Plan below a short run = %v, %v; want one write", writes, err)
	}
	b.apply(writes)
	if got := b.order(); got != "a t00 t01 t02 x z" {
		t.Errorf("after a move below a short run the order is %q", got)
	}
}

// The reads of a plan are not one snapshot. A card that was moved since the
// caller read it is not where its old key says: the plan is refused as
// changed, to be made again, never as a tied run.
func TestPlan_TheOtherCardMoved(t *testing.T) {
	b := &board{cards: []rank.Card{{"a", "F"}, {"b", "V"}, {"c", "k"}, {"x", ""}}}
	stale := rank.Card{ID: "b", Key: "T"} // b as read before someone moved it to V
	for _, pos := range []rank.Position{rank.Before, rank.After} {
		if w, err := rank.Plan(context.Background(), b, "x", pos, stale); !errors.Is(err, rank.ErrChanged) {
			t.Errorf("Plan next to a card that moved = %v, %v; want ErrChanged", w, err)
		}
	}
	// Also when another card now holds the key it had.
	b.cards = append(b.cards, rank.Card{ID: "d", Key: "T"})
	if w, err := rank.Plan(context.Background(), b, "x", rank.Before, stale); !errors.Is(err, rank.ErrChanged) {
		t.Errorf("Plan next to a card that moved, its old key taken = %v, %v; want ErrChanged", w, err)
	}
}

// A rebalance written in RewriteOrder never puts the container out of
// order: after every single write, the cards are still in the order they
// started in, whatever the old keys looked like.
func TestRewriteOrder_KeepsTheOrderAtEveryStep(t *testing.T) {
	shapes := map[string]func(n int) []string{
		// Bunched at the top, as after many inserts there.
		"bunched at the top": func(n int) []string {
			keys := []string{"zzz"}
			for len(keys) < n {
				k, _ := rank.Between("", keys[0])
				keys = append([]string{k}, keys...)
			}
			return keys
		},
		"bunched at the bottom": func(n int) []string {
			keys := []string{"1"}
			for len(keys) < n {
				k, _ := rank.Between(keys[len(keys)-1], "")
				keys = append(keys, k)
			}
			return keys
		},
		"random inserts": func(n int) []string {
			var keys []string
			for len(keys) < n {
				at := (len(keys) * 7919) % (len(keys) + 1)
				lo, hi := "", ""
				if at > 0 {
					lo = keys[at-1]
				}
				if at < len(keys) {
					hi = keys[at]
				}
				k, _ := rank.Between(lo, hi)
				keys = slices.Insert(keys, at, k)
			}
			return keys
		},
	}
	for name, shape := range shapes {
		keys := shape(300)
		if !slices.IsSortedFunc(keys, strings.Compare) {
			t.Fatalf("%s: test bug, the starting keys are not in order", name)
		}
		target, err := rank.Evenly("", "", len(keys))
		if err != nil {
			t.Fatal(err)
		}
		order := rank.RewriteOrder(keys, target)
		for step, i := range order {
			keys[i] = target[i]
			// Card i against its neighbours is enough: nothing else moved.
			if (i > 0 && keys[i-1] >= keys[i]) || (i+1 < len(keys) && keys[i] >= keys[i+1]) {
				t.Fatalf("%s: after write %d (card %d) the container is out of order", name, step, i)
			}
		}
		if !slices.Equal(keys, target) {
			t.Errorf("%s: after every write the keys are not the targets", name)
		}
	}
	if got := rank.RewriteOrder([]string{"A", "V"}, []string{"A", "V"}); len(got) != 0 {
		t.Errorf("RewriteOrder with nothing to change = %v", got)
	}
}
