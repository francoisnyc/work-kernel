// Tests for the Jira proof of concept, part 1: fractional rank keys
// (docs/design.md, "Jira proof of concept"). The package is app code: pli
// and the bench use it, the kernel never does.
package rank_test

import (
	"errors"
	"go/parser"
	"go/token"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/rank"
)

// Keys compare byte by byte, as Postgres compares them under COLLATE "C":
// digits, then upper case, then lower case. A locale would put "a" before
// "B"; rank order must not depend on one.
func TestCompare_IsBytewise(t *testing.T) {
	sorted := []string{"0V", "1", "9z", "A", "B", "Zz", "a", "aa", "b", "z"}
	for i := range sorted {
		for j := range sorted {
			want := 0
			switch {
			case i < j:
				want = -1
			case i > j:
				want = 1
			}
			if got := rank.Compare(sorted[i], sorted[j]); got != want {
				t.Errorf("Compare(%q, %q) = %d, want %d", sorted[i], sorted[j], got, want)
			}
		}
	}
}

func TestValid(t *testing.T) {
	for _, k := range []string{"1", "V", "z", "0V", "a0b", "Zz9"} {
		if !rank.Valid(k) {
			t.Errorf("Valid(%q) = false, want true", k)
		}
	}
	// Empty, a digit outside the alphabet, or a trailing lowest digit (no
	// key could then go below it).
	for _, k := range []string{"", "a b", "a-b", "é", "a|b", "0", "a0", "V00"} {
		if rank.Valid(k) {
			t.Errorf("Valid(%q) = true, want false", k)
		}
	}
}

// between asks for a key and checks the contract: valid, strictly between,
// and at most MaxGrowth bytes longer than the longer neighbour.
func between(t *testing.T, lo, hi string) string {
	t.Helper()
	k, err := rank.Between(lo, hi)
	if err != nil {
		t.Fatalf("Between(%q, %q): %v", lo, hi, err)
	}
	if !rank.Valid(k) {
		t.Fatalf("Between(%q, %q) = %q, not a valid key", lo, hi, k)
	}
	if lo != "" && strings.Compare(lo, k) >= 0 {
		t.Fatalf("Between(%q, %q) = %q, not above the lower neighbour", lo, hi, k)
	}
	if hi != "" && strings.Compare(k, hi) >= 0 {
		t.Fatalf("Between(%q, %q) = %q, not below the upper neighbour", lo, hi, k)
	}
	if longest := max(len(lo), len(hi)); len(k) > longest+rank.MaxGrowth {
		t.Fatalf("Between(%q, %q) = %q: %d bytes, more than %d past the longer neighbour", lo, hi, k, len(k), rank.MaxGrowth)
	}
	return k
}

func TestBetween_FixedCases(t *testing.T) {
	for _, c := range [][2]string{
		{"", ""},      // the first key of an empty container
		{"", "V"},     // the top
		{"V", ""},     // the bottom
		{"", "1"},     // above the lowest one-digit key
		{"", "01"},    // above a key that already starts with the lowest digit
		{"z", ""},     // below the highest one-digit key
		{"zz", ""},    // and below a longer one
		{"A", "B"},    // adjacent digits: the key must grow
		{"A", "A1"},   // a key and its closest extension
		{"Az", "B"},   // adjacent across a carry
		{"a", "b"},    // lower case
		{"Z", "a"},    // across the upper/lower case boundary
		{"9", "A"},    // across the digit/letter boundary
		{"A", "z"},    // far apart
		{"A1", "A1V"}, // a prefix of the other
	} {
		between(t, c[0], c[1])
	}
}

// Equal or reversed neighbours have no key between them: the caller must
// re-key the tied Items (Evenly) rather than get a wrong key.
func TestBetween_NoRoom(t *testing.T) {
	for _, c := range [][2]string{{"V", "V"}, {"b", "a"}, {"A1", "A"}} {
		if k, err := rank.Between(c[0], c[1]); !errors.Is(err, rank.ErrNoRoom) {
			t.Errorf("Between(%q, %q) = %q, %v; want ErrNoRoom", c[0], c[1], k, err)
		}
	}
}

// The jitter: two people dropping a card into the same gap at the same
// moment rarely pick the same key. In a wide gap, 200 picks are nearly all
// different; even between adjacent keys they are not all the same.
func TestBetween_Jitter(t *testing.T) {
	distinct := func(lo, hi string) int {
		seen := map[string]bool{}
		for range 200 {
			seen[between(t, lo, hi)] = true
		}
		return len(seen)
	}
	if n := distinct("A", "z"); n < 150 {
		t.Errorf("200 keys between A and z: only %d distinct, want at least 150", n)
	}
	if n := distinct("A", "B"); n < 20 {
		t.Errorf("200 keys between A and B: only %d distinct, want at least 20", n)
	}
	if n := distinct("", ""); n < 20 {
		t.Errorf("200 first keys: only %d distinct, want at least 20", n)
	}
}

// Random drags keep a list in order: every key lands strictly between its
// neighbours, wherever it is inserted, 5,000 times over.
func TestBetween_RandomInsertsKeepOrder(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	var keys []string
	longest := 0
	for range 5000 {
		at := r.IntN(len(keys) + 1)
		lo, hi := "", ""
		if at > 0 {
			lo = keys[at-1]
		}
		if at < len(keys) {
			hi = keys[at]
		}
		k := between(t, lo, hi)
		keys = slices.Insert(keys, at, k)
		longest = max(longest, len(k))
	}
	if !slices.IsSortedFunc(keys, strings.Compare) {
		t.Fatal("the list is out of order")
	}
	if n := len(slices.Compact(slices.Clone(keys))); n != len(keys) {
		t.Errorf("%d of %d keys are duplicates", len(keys)-n, len(keys))
	}
	// Random positions leave room: keys stay well under the threshold.
	if longest > rank.Threshold {
		t.Errorf("the longest key after 5,000 random inserts is %d bytes, past the threshold (%d)", longest, rank.Threshold)
	}
	t.Logf("5,000 random inserts: longest key %d bytes", longest)
}

// Always inserting at an end is the common case (a new card goes to the
// top or the bottom), so Between steps a little past the last card there
// instead of halving the gap. Order holds, each key is at most MaxGrowth
// longer than its neighbour, and 1,000 inserts in a row at either end stay
// under the threshold: no rebalance is due.
func TestBetween_AlwaysAtAnEnd(t *testing.T) {
	for name, next := range map[string]func(end string) string{
		"the top":    func(end string) string { return between(t, "", end) },
		"the bottom": func(end string) string { return between(t, end, "") },
	} {
		end := between(t, "", "")
		longest := len(end)
		for range 1000 {
			end = next(end)
			longest = max(longest, len(end))
		}
		if longest > rank.Threshold {
			t.Errorf("1,000 inserts at %s: a key of %d bytes, past the threshold (%d)", name, longest, rank.Threshold)
		}
		t.Logf("always at %s: after 1,000 inserts the key is %d bytes", name, len(end))
	}
}

// Room does run out when cards keep going into the same gap: always right
// below one card. The midpoint halves the gap each time, so a base-62
// digit lasts about six inserts: 60 in a row stay under the threshold, and
// well before 1,000 a key crosses it and NeedsRebalance says so.
func TestBetween_AlwaysIntoTheSameGap(t *testing.T) {
	lo, hi := "A", "B"
	crossed := 0
	for i := 1; i <= 1000; i++ {
		hi = between(t, lo, hi) // the new card is now lo's lower neighbour
		if i == 60 && len(hi) > rank.Threshold {
			t.Errorf("after 60 inserts into one gap the key is %d bytes, past the threshold (%d)", len(hi), rank.Threshold)
		}
		if crossed == 0 && rank.NeedsRebalance(hi) {
			crossed = i
		}
	}
	if crossed == 0 {
		t.Errorf("1,000 inserts into one gap never crossed the threshold (last key %d bytes): NeedsRebalance never said so", len(hi))
	}
	t.Logf("always into the same gap: the threshold (%d) was crossed at insert %d; after 1,000 the key is %d bytes", rank.Threshold, crossed, len(hi))
}

func TestNeedsRebalance(t *testing.T) {
	at := strings.Repeat("V", rank.Threshold)
	if rank.NeedsRebalance(at) {
		t.Errorf("a key of exactly %d bytes needs a rebalance; only longer ones do", rank.Threshold)
	}
	if !rank.NeedsRebalance(at + "V") {
		t.Errorf("a key of %d bytes does not need a rebalance", rank.Threshold+1)
	}
}

// Evenly is what a rebalance writes: n valid keys, ascending, distinct,
// strictly inside the bounds, short. A thousand cards fit in three bytes.
func TestEvenly(t *testing.T) {
	for _, c := range []struct {
		lo, hi string
		n      int
		maxLen int
	}{
		{"", "", 1, 3},
		{"", "", 2, 3},
		{"", "", 1000, 3},
		{"", "", 5000, 4},
		{"A", "B", 3, 5},  // a tied run re-keyed between its neighbours
		{"A", "", 10, 3},  // open below
		{"", "A1", 10, 5}, // open above
	} {
		keys, err := rank.Evenly(c.lo, c.hi, c.n)
		if err != nil {
			t.Errorf("Evenly(%q, %q, %d): %v", c.lo, c.hi, c.n, err)
			continue
		}
		if len(keys) != c.n {
			t.Errorf("Evenly(%q, %q, %d) returned %d keys", c.lo, c.hi, c.n, len(keys))
			continue
		}
		for i, k := range keys {
			switch {
			case !rank.Valid(k):
				t.Errorf("Evenly(%q, %q, %d)[%d] = %q, not a valid key", c.lo, c.hi, c.n, i, k)
			case len(k) > c.maxLen:
				t.Errorf("Evenly(%q, %q, %d)[%d] = %q, longer than %d bytes", c.lo, c.hi, c.n, i, k, c.maxLen)
			case i > 0 && strings.Compare(keys[i-1], k) >= 0:
				t.Errorf("Evenly(%q, %q, %d): key %d (%q) is not above key %d (%q)", c.lo, c.hi, c.n, i, k, i-1, keys[i-1])
			case c.lo != "" && strings.Compare(c.lo, k) >= 0, c.hi != "" && strings.Compare(k, c.hi) >= 0:
				t.Errorf("Evenly(%q, %q, %d)[%d] = %q, outside the bounds", c.lo, c.hi, c.n, i, k)
			}
		}
		// Room is left between the keys, at both ends too: the next move
		// after a rebalance is one ordinary write.
		if c.n > 1 && len(keys) == c.n {
			between(t, "", keys[0])
			between(t, keys[0], keys[1])
			between(t, keys[c.n-1], "")
		}
	}
	if keys, err := rank.Evenly("", "", 0); err != nil || len(keys) != 0 {
		t.Errorf("Evenly of nothing = %v, %v; want no keys", keys, err)
	}
	if _, err := rank.Evenly("V", "V", 2); !errors.Is(err, rank.ErrNoRoom) {
		t.Errorf("Evenly between equal bounds: %v, want ErrNoRoom", err)
	}
}

// Rank is not a kernel feature: no kernel package imports this one. Only
// the app side does (pli, the bench).
func TestNoKernelPackageImportsRank(t *testing.T) {
	const self = "github.com/francoisnyc/work-kernel/internal/rank"
	for _, dir := range []string{
		"../api", "../item", "../itemservice", "../lifecycle", "../policy", "../relay", "../typedef", "../workflow",
		"../hooks", "../../cmd/itemservice", "../../cmd/relay", "../../cmd/worker",
	} {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil || len(files) == 0 {
			t.Fatalf("no Go files in %s (%v): the list of kernel packages is stale", dir, err)
		}
		for _, f := range files {
			src, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			parsed, err := parser.ParseFile(token.NewFileSet(), f, src, parser.ImportsOnly)
			if err != nil {
				t.Fatalf("parse %s: %v", f, err)
			}
			for _, imp := range parsed.Imports {
				if strings.Trim(imp.Path.Value, `"`) == self {
					t.Errorf("%s imports the rank package: rank is app code, the kernel only sorts by a field", f)
				}
			}
		}
	}
}
