package rank

// The golden vectors that hold the TypeScript port of this package
// (web/src/rank) to this one. This test computes every answer with the Go
// code and compares the result with testdata/vectors.json; the web UI's
// unit tests read the same file and must give the same answers.
//
//	go test ./internal/rank -run TestVectors -update    rewrite the file
//
// Between's jitter is random, so the vectors fix it: each case names a
// draw NUM/DEN, and the generator in its place answers (n-1)*NUM/DEN,
// rounded down, when asked for an integer in [0, n). 0/1 is the lowest
// draw, 1/1 the highest.
//
// This file is in package rank itself (not rank_test, like the other
// tests) because it swaps the unexported int64N. Go compiles both kinds of
// test file into one test binary; only this kind sees unexported names.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"math/big"
	"os"
	"slices"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite testdata/vectors.json")

const vectorsFile = "testdata/vectors.json"

type vectors struct {
	Comment        string            `json:"comment"`
	Alphabet       string            `json:"alphabet"`
	Threshold      int               `json:"threshold"`
	MaxGrowth      int               `json:"maxGrowth"`
	MaxTiedRun     int               `json:"maxTiedRun"`
	Valid          []validVector     `json:"valid"`
	Compare        []compareVector   `json:"compare"`
	NeedsRebalance []rebalanceVector `json:"needsRebalance"`
	Evenly         []evenlyVector    `json:"evenly"`
	Between        []betweenVector   `json:"between"`
	Plan           []planVector      `json:"plan"`
}

type validVector struct {
	Key  string `json:"key"`
	Want bool   `json:"want"`
}

type compareVector struct {
	A    string `json:"a"`
	B    string `json:"b"`
	Want int    `json:"want"`
}

type rebalanceVector struct {
	Key  string `json:"key"`
	Want bool   `json:"want"`
}

// Error is "" or one of: no_room, invalid_key, no_rank, tied_run_too_long,
// changed.
type evenlyVector struct {
	Lo    string   `json:"lo"`
	Hi    string   `json:"hi"`
	N     int      `json:"n"`
	Keys  []string `json:"keys"`
	Error string   `json:"error"`
}

type betweenVector struct {
	Lo    string `json:"lo"`
	Hi    string `json:"hi"`
	Draw  string `json:"draw"`
	Key   string `json:"key"`
	Error string `json:"error"`
}

type planVector struct {
	Name  string `json:"name"`
	Cards []card `json:"cards"`
	Moved string `json:"moved"`
	Pos   string `json:"pos"` // before, after, top, bottom
	Other card   `json:"other"`
	Draw  string `json:"draw"`
	Want  []card `json:"writes"`
	Error string `json:"error"`
}

// card is a Card, or a Write, as the file spells it: lower-case names.
type card struct {
	ID  string `json:"id"`
	Key string `json:"key"`
}

// fixedDraw puts a generator that always answers (n-1)*num/den in int64N's
// place, and returns the function that puts the real one back.
func fixedDraw(t *testing.T, draw string) (restore func()) {
	t.Helper()
	num, den, ok := strings.Cut(draw, "/")
	n, okN := new(big.Int).SetString(num, 10)
	d, okD := new(big.Int).SetString(den, 10)
	if !ok || !okN || !okD || d.Sign() <= 0 || n.Sign() < 0 || n.Cmp(d) > 0 {
		t.Fatalf("draw %q: want NUM/DEN with 0 <= NUM <= DEN", draw)
	}
	real := int64N
	int64N = func(max int64) int64 {
		v := big.NewInt(max - 1)
		v.Mul(v, n).Div(v, d) // big: (max-1)*num overflows an int64
		return v.Int64()
	}
	return func() { int64N = real }
}

func errorName(t *testing.T, err error) string {
	t.Helper()
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrNoRoom):
		return "no_room"
	case errors.Is(err, ErrNoRank):
		return "no_rank"
	case errors.Is(err, ErrTiedRunTooLong):
		return "tied_run_too_long"
	case errors.Is(err, ErrChanged):
		return "changed"
	case strings.Contains(err.Error(), "is not a rank key"):
		return "invalid_key"
	}
	t.Fatalf("an error the vectors have no name for: %v", err)
	return ""
}

// memory is a container in memory, read as the kernel's order_by reads
// one: by key, ties by ID ascending in both directions.
type memory []card

func (m memory) Scan(_ context.Context, q Query) ([]Card, error) {
	var out []Card
	for _, mc := range m {
		c := Card(mc) // same fields: a conversion, no copy by hand
		switch {
		case c.Key == "":
		case q.Eq != "" && c.Key != q.Eq, q.Gt != "" && c.Key <= q.Gt, q.Lt != "" && c.Key >= q.Lt:
		default:
			out = append(out, c)
		}
	}
	slices.SortFunc(out, func(x, y Card) int {
		c := strings.Compare(x.Key, y.Key)
		if q.Desc {
			c = -c
		}
		if c != 0 {
			return c
		}
		return strings.Compare(x.ID, y.ID)
	})
	if len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return out, nil
}

var positions = map[string]Position{"before": Before, "after": After, "top": Top, "bottom": Bottom}

// draws are the jitters every Between case is computed with.
var draws = []string{"0/1", "1/1", "1/2", "1/3", "617/1000"}

func buildVectors(t *testing.T) vectors {
	t.Helper()
	v := vectors{
		Comment: "Golden vectors shared by internal/rank (Go, the reference) and web/src/rank (TypeScript). " +
			"Written by `go test ./internal/rank -run TestVectors -update`; never edit by hand.",
		Alphabet: Alphabet, Threshold: Threshold, MaxGrowth: MaxGrowth, MaxTiedRun: MaxTiedRun,
	}

	for _, k := range []string{"1", "V", "z", "0V", "a0b", "Zz9", "", "a b", "a-b", "é", "a|b", "0", "a0", "V00", "00", "001"} {
		v.Valid = append(v.Valid, validVector{k, Valid(k)})
	}

	sorted := []string{"0V", "1", "9z", "A", "B", "Zz", "a", "aa", "b", "z"}
	for _, a := range sorted {
		for _, b := range sorted {
			v.Compare = append(v.Compare, compareVector{a, b, Compare(a, b)})
		}
	}

	for _, n := range []int{1, Threshold - 1, Threshold, Threshold + 1, 3 * Threshold} {
		k := strings.Repeat("V", n)
		v.NeedsRebalance = append(v.NeedsRebalance, rebalanceVector{k, NeedsRebalance(k)})
	}

	for _, c := range []struct {
		lo, hi string
		n      int
	}{
		{"", "", 0}, {"", "", 1}, {"", "", 2}, {"", "", 10}, {"", "", 61}, {"", "", 62}, {"", "", 300},
		{"V", "", 3}, {"", "V", 3}, {"V", "W", 3}, {"V", "W", 100}, {"V", "V1", 8}, {"a", "a001", 9},
		{"0001", "0002", 4}, {"zzzz", "", 5}, {"", "0001", 5}, {"Vabc", "Vabd", 9},
		{"V", "V", 2}, {"W", "V", 2}, {"V", "V", 0}, {"a0", "b", 1}, {"a", "b c", 1},
	} {
		keys, err := Evenly(c.lo, c.hi, c.n)
		if keys == nil {
			keys = []string{} // an empty list in JSON, never null
		}
		v.Evenly = append(v.Evenly, evenlyVector{c.lo, c.hi, c.n, keys, errorName(t, err)})
	}

	long := strings.Repeat("V", Threshold)
	for _, c := range [][2]string{
		{"", ""}, {"", "V"}, {"V", ""}, {"", "1"}, {"z", ""}, {"", "0001"}, {"zzzz", ""}, {"zzzzzzzz", ""},
		{"A", "z"}, {"V", "W"}, {"a", "b"}, {"a", "a1"}, {"a", "a001"}, {"az", "b"}, {"azzz", "b"}, {"Vabc", "Vabd"},
		{"0001", "0002"}, {"9", "A"}, {"Z", "a"}, {"1", "z"}, {"V", "V0001"},
		{long, ""}, {"", long}, {long, long + "1"}, {long + "1", long + "2"},
		{"V", "V"}, {"W", "V"}, {"a0", "b"}, {"a", "b c"}, {"é", ""},
	} {
		for _, draw := range draws {
			restore := fixedDraw(t, draw)
			key, err := Between(c[0], c[1])
			restore()
			v.Between = append(v.Between, betweenVector{c[0], c[1], draw, key, errorName(t, err)})
		}
	}

	// A container of five cards, and one where four cards share a key.
	five := []card{{"a", "F"}, {"b", "K"}, {"c", "P"}, {"d", "U"}, {"e", "Z"}, {"n", ""}}
	tied := []card{{"a", "F"}, {"t1", "K"}, {"t2", "K"}, {"t3", "K"}, {"t4", "K"}, {"e", "Z"}}
	var crowd []card // more cards on one key than a move re-keys
	for i := range MaxTiedRun + 1 {
		crowd = append(crowd, card{ID: "c" + string(rune('0'+i)), Key: "K"})
	}
	atCap := append([]card{{"x", "A"}}, crowd[:MaxTiedRun-1]...)
	for _, c := range []planVector{
		{Name: "top of an empty container", Cards: []card{{"x", ""}}, Moved: "x", Pos: "top"},
		{Name: "bottom of an empty container", Cards: []card{{"x", ""}}, Moved: "x", Pos: "bottom"},
		{Name: "an unranked card to the top", Cards: five, Moved: "n", Pos: "top"},
		{Name: "an unranked card to the bottom", Cards: five, Moved: "n", Pos: "bottom"},
		{Name: "the first card to the top: its own key is not its neighbour", Cards: five, Moved: "a", Pos: "top"},
		{Name: "the last card to the bottom", Cards: five, Moved: "e", Pos: "bottom"},
		{Name: "before a card in the middle", Cards: five, Moved: "e", Pos: "before", Other: card{"c", "P"}},
		{Name: "after a card in the middle", Cards: five, Moved: "a", Pos: "after", Other: card{"c", "P"}},
		{Name: "before the first card", Cards: five, Moved: "d", Pos: "before", Other: card{"a", "F"}},
		{Name: "after the last card", Cards: five, Moved: "b", Pos: "after", Other: card{"e", "Z"}},
		{Name: "before its own lower neighbour's successor: the moved card is skipped", Cards: five, Moved: "b", Pos: "before", Other: card{"c", "P"}},
		{Name: "after its own upper neighbour's predecessor", Cards: five, Moved: "d", Pos: "after", Other: card{"c", "P"}},
		{Name: "an unranked card next to a ranked one", Cards: five, Moved: "n", Pos: "after", Other: card{"b", "K"}},
		{Name: "the other card has no rank", Cards: five, Moved: "a", Pos: "before", Other: card{"n", ""}, Error: "no_rank"},
		{Name: "before the first card of a tied run: one write", Cards: tied, Moved: "e", Pos: "before", Other: card{"t1", "K"}},
		{Name: "after the last card of a tied run: one write", Cards: tied, Moved: "a", Pos: "after", Other: card{"t4", "K"}},
		{Name: "into a tied run, before: the run is re-keyed", Cards: tied, Moved: "e", Pos: "before", Other: card{"t3", "K"}},
		{Name: "into a tied run, after: the run is re-keyed", Cards: tied, Moved: "a", Pos: "after", Other: card{"t1", "K"}},
		{Name: "a card of the tied run moved within it", Cards: tied, Moved: "t4", Pos: "before", Other: card{"t2", "K"}},
		{Name: "a tied run at the cap is re-keyed", Cards: atCap, Moved: "x", Pos: "after", Other: card{"c0", "K"}},
		{Name: "a tied run past the cap is refused", Cards: append([]card{{"x", "A"}}, crowd...), Moved: "x", Pos: "after", Other: card{"c0", "K"}},
		{Name: "the other card was moved since it was read", Cards: five, Moved: "a", Pos: "before", Other: card{"c", "Q"}},
	} {
		for _, draw := range []string{"0/1", "617/1000", "1/1"} {
			c.Draw = draw
			restore := fixedDraw(t, draw)
			writes, err := Plan(context.Background(), memory(c.Cards), c.Moved, positions[c.Pos], Card(c.Other))
			restore()
			c.Want, c.Error = []card{}, errorName(t, err)
			for _, w := range writes {
				c.Want = append(c.Want, card(w))
			}
			v.Plan = append(v.Plan, c)
		}
	}
	return v
}

// The file is what the Go code answers today. A change to the algorithm
// shows up here first; rewrite the file with -update and the TypeScript
// port must follow, or its tests fail.
func TestVectors(t *testing.T) {
	// SetEscapeHTML(false) keeps "<" out of < escapes; an Encoder is
	// the only way to turn that off.
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", " ")
	if err := enc.Encode(buildVectors(t)); err != nil {
		t.Fatal(err)
	}
	if *update {
		if err := os.WriteFile(vectorsFile, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(vectorsFile)
	if err != nil {
		t.Fatalf("%v (write it with: go test ./internal/rank -run TestVectors -update)", err)
	}
	if !bytes.Equal(buf.Bytes(), want) {
		t.Fatalf("%s is not what the Go code answers: if the change is meant, rewrite it with -update "+
			"and bring web/src/rank in line", vectorsFile)
	}
}

// The vectors must exercise what they claim to: every error, a re-keyed
// tied run, and a draw that changes the key.
func TestVectors_CoverTheCases(t *testing.T) {
	v := buildVectors(t)
	seen := map[string]bool{}
	rekeyed, jitter := false, map[string]map[string]bool{}
	for _, b := range v.Between {
		seen[b.Error] = true
		if b.Error == "" {
			gap := b.Lo + "|" + b.Hi
			if jitter[gap] == nil {
				jitter[gap] = map[string]bool{}
			}
			jitter[gap][b.Key] = true
			if (b.Lo != "" && Compare(b.Lo, b.Key) >= 0) || (b.Hi != "" && Compare(b.Key, b.Hi) >= 0) || !Valid(b.Key) {
				t.Errorf("Between(%q, %q) at draw %s = %q: not a key strictly between", b.Lo, b.Hi, b.Draw, b.Key)
			}
		}
	}
	for _, e := range v.Evenly {
		seen[e.Error] = true
	}
	for _, p := range v.Plan {
		seen[p.Error] = true
		rekeyed = rekeyed || len(p.Want) > 1
	}
	for _, name := range []string{"", "no_room", "invalid_key", "no_rank", "tied_run_too_long", "changed"} {
		if !seen[name] {
			t.Errorf("no vector answers %q", name)
		}
	}
	if !rekeyed {
		t.Error("no plan vector re-keys a tied run")
	}
	for gap, keys := range jitter {
		if len(keys) < 2 {
			t.Errorf("gap %s: every draw gives the same key, so the vectors do not hold the jitter", gap)
		}
	}
}
