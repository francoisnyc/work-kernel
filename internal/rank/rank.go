// Package rank makes the keys an app orders Items by hand with: strings
// that sort byte by byte, with room to put a new key between any two.
//
// It is app code, not kernel code. Manual ranking failed the kernel's
// membership test (docs/design.md, "Jira proof of concept"): the kernel
// only sorts by a declared field, and rank is an ordinary string field on
// the Types that want it, scoped to one container. pli and the bench use
// this package; no kernel package may import it (a test holds that).
//
// A key is a fraction between 0 and 1 written in base 62, with the digits
// in ASCII order (0-9, A-Z, a-z), so comparing keys as bytes compares the
// fractions. A key never ends with the lowest digit: two keys are then
// equal as bytes exactly when they are equal as fractions, and there is
// always a key below any key.
package rank

import (
	"errors"
	"fmt"
	"math/big"
	"math/rand/v2"
	"strings"
)

// Alphabet is the digits of a key, in the order they sort.
const Alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// Threshold is the key length past which a container needs a rebalance:
// its keys are rewritten short and evenly spaced. A move never rebalances;
// it only reports that the threshold was crossed.
const Threshold = 32

// MaxGrowth bounds how much longer than its longer neighbour a key from
// Between may be.
const MaxGrowth = 4

// MaxTiedRun is the longest run of Items with one and the same key that a
// move re-keys by itself. A longer run means the container's keys are in
// poor shape: the move is refused, and a rebalance is the answer.
const MaxTiedRun = 8

// ErrNoRoom is Between's refusal when no key fits: the neighbours are
// equal, or out of order. The caller gives the tied Items fresh keys
// (Evenly) and tries again.
var ErrNoRoom = errors.New("rank: no room between the neighbours")

const (
	base = int64(len(Alphabet))
	// room is how many keys Between wants to choose from: enough that two
	// people who insert into the same gap at once rarely pick the same one.
	room = 1000
	// At an end of the list (no neighbour on one side) Between does not
	// halve the gap: it steps a little way past the last card, between
	// 1/endFar and 1/endNear of the room left. Putting cards at the top or
	// the bottom is the common case, and halving there costs a digit every
	// six inserts; a small step costs one every fifty.
	endNear, endFar = 32, 8
	// spacing is the least distance Evenly leaves between two keys, in
	// units of its last digit: one whole digit of room.
	spacing = base
)

// Valid reports whether key is a rank key: not empty, only digits of
// Alphabet, not ending with the lowest one.
func Valid(key string) bool {
	if key == "" || key[len(key)-1] == Alphabet[0] {
		return false
	}
	for i := 0; i < len(key); i++ {
		if strings.IndexByte(Alphabet, key[i]) < 0 {
			return false
		}
	}
	return true
}

// Compare orders two keys byte by byte, as Postgres does under COLLATE "C":
// -1, 0 or 1.
func Compare(a, b string) int { return strings.Compare(a, b) }

// NeedsRebalance reports whether a key is past Threshold.
func NeedsRebalance(key string) bool { return len(key) > Threshold }

// Between returns a key strictly between lo and hi. An empty lo means "no
// lower neighbour" (the top of the list), an empty hi "no upper neighbour"
// (the bottom).
//
// Between two cards the key is near the midpoint. At the top or the bottom
// it is a small step past the last card instead (see endNear), so that
// always adding at one end uses the room up slowly. Either way a random
// jitter moves it, so two people who insert at the same place at once
// rarely pick the same key (when they do, the Item ID breaks the tie). The
// key is the shortest length that leaves enough keys to choose from, at
// most MaxGrowth bytes longer than the longer neighbour. lo >= hi is
// ErrNoRoom.
func Between(lo, hi string) (string, error) {
	top, bottom := lo == "" && hi != "", lo != "" && hi == ""
	want := int64(room)
	if top || bottom {
		// The step's window is 1/endFar - 1/endNear of the gap: the gap
		// must be that much wider to leave `room` keys in it.
		want = room * endNear * endFar / (endNear - endFar)
	}
	n, first, last, err := window(lo, hi, want)
	if err != nil {
		return "", err
	}
	span := new(big.Int).Sub(last, first)
	k := new(big.Int)
	if top || bottom {
		// A step of span/endNear to span/endFar away from the end card.
		near := new(big.Int).Div(span, big.NewInt(endNear))
		far := new(big.Int).Div(span, big.NewInt(endFar))
		step := near.Add(near, random(far.Sub(far, near)))
		if top {
			return format(k.Sub(last, step), n), nil
		}
		return format(k.Add(first, step), n), nil
	}
	// Somewhere in the middle half of the window: the midpoint, give or
	// take a quarter of the gap.
	quarter := new(big.Int).Rsh(span, 2)
	half := new(big.Int).Rsh(span, 1)
	k.Add(first, quarter)
	k.Add(k, random(half))
	return format(k, n), nil
}

// Evenly returns n keys strictly between lo and hi (empty: open, as in
// Between), ascending, distinct and evenly spaced, as short as n allows:
// what a rebalance writes over a whole container (lo and hi empty), and
// what a move writes over a run of Items whose keys are equal.
func Evenly(lo, hi string, n int) ([]string, error) {
	if n <= 0 {
		// Still say when the bounds leave no room at all.
		if lo != "" && hi != "" && Compare(lo, hi) >= 0 {
			return nil, ErrNoRoom
		}
		return nil, nil
	}
	// n keys and the n+1 gaps around them, each at least `spacing` wide.
	want := new(big.Int).Mul(big.NewInt(int64(n)+1), big.NewInt(spacing))
	digits, first, last, err := windowBig(lo, hi, want)
	if err != nil {
		return nil, err
	}
	// first-1 and last+1 are the bounds themselves (or just inside them).
	low := new(big.Int).Sub(first, big.NewInt(1))
	span := new(big.Int).Sub(new(big.Int).Add(last, big.NewInt(1)), low)
	step := new(big.Int).Div(span, big.NewInt(int64(n)+1))
	keys := make([]string, n)
	k := new(big.Int).Set(low)
	for i := range keys {
		k.Add(k, step)
		keys[i] = format(k, digits)
	}
	return keys, nil
}

// window finds the shortest key length n at which at least `want` keys lie
// strictly between lo and hi, and returns n with the first and last of
// them, as integers of n base-62 digits.
func window(lo, hi string, want int64) (n int, first, last *big.Int, err error) {
	return windowBig(lo, hi, big.NewInt(want))
}

func windowBig(lo, hi string, want *big.Int) (n int, first, last *big.Int, err error) {
	for _, key := range []string{lo, hi} {
		if key != "" && !Valid(key) {
			return 0, nil, nil, fmt.Errorf("rank: %q is not a rank key", key)
		}
	}
	if lo != "" && hi != "" && Compare(lo, hi) >= 0 {
		return 0, nil, nil, ErrNoRoom
	}
	one := big.NewInt(1)
	for n = 1; ; n++ {
		// A key of n digits is above lo exactly when it is above lo cut
		// to n digits: lo is either that, or that plus a remainder.
		first = new(big.Int).Add(prefix(lo, n), one)
		// It is below hi when it is at most hi cut to n digits, if hi has
		// more digits than that; else when it is below hi itself.
		switch {
		case hi == "":
			last = new(big.Int).Exp(big.NewInt(base), big.NewInt(int64(n)), nil)
			last.Sub(last, one)
		case len(hi) > n:
			last = prefix(hi, n)
		default:
			last = new(big.Int).Sub(prefix(hi, n), one)
		}
		count := new(big.Int).Sub(last, first)
		count.Add(count, one)
		if count.Cmp(want) >= 0 {
			return n, first, last, nil
		}
	}
}

// prefix reads the first n digits of key as an integer, padding a shorter
// key with zeros: the key's fraction times 62^n, rounded down.
func prefix(key string, n int) *big.Int {
	v := new(big.Int)
	b := big.NewInt(base)
	for i := range n {
		v.Mul(v, b)
		if i < len(key) {
			v.Add(v, big.NewInt(int64(strings.IndexByte(Alphabet, key[i]))))
		}
	}
	return v
}

// format writes an integer of n base-62 digits as a key, without its
// trailing zeros: the same fraction, and a valid key.
func format(v *big.Int, n int) string {
	digits := make([]byte, n)
	rest := new(big.Int).Set(v)
	b, d := big.NewInt(base), new(big.Int)
	for i := n - 1; i >= 0; i-- {
		rest.DivMod(rest, b, d)
		digits[i] = Alphabet[d.Int64()]
	}
	return strings.TrimRight(string(digits), Alphabet[:1])
}

// int64N draws the jitter: a uniform integer in [0, n). It is a variable
// only so that the golden-vector test (vectors_test.go) can put fixed
// draws in its place; nothing else assigns it.
var int64N = rand.Int64N

// random returns a uniform integer in [0, max], for the jitter. It is not
// a secret: math/rand is enough.
func random(max *big.Int) *big.Int {
	if max.Sign() <= 0 {
		return new(big.Int)
	}
	if max.IsInt64() && max.Int64() < 1<<62 {
		return big.NewInt(int64N(max.Int64() + 1))
	}
	// A very wide gap: jitter its top 62 bits, which is plenty.
	shift := uint(max.BitLen() - 62)
	top := new(big.Int).Rsh(max, shift)
	r := big.NewInt(int64N(top.Int64() + 1))
	return r.Lsh(r, shift)
}
