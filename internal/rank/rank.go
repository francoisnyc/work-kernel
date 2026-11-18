// Package rank makes the keys an app orders Items by hand with: strings
// that sort byte by byte, with room to put a new key between any two.
//
// It is app code, not kernel code. Manual ranking failed the kernel's
// membership test (docs/design.md, "Jira proof of concept"): the kernel
// only sorts by a declared field, and rank is an ordinary string field on
// the Types that want it, scoped to one container. pli and the bench use
// this package; no kernel package may import it (a test holds that).
//
// A key is a fraction written in base 62 with the digits in ASCII order
// (0-9, A-Z, a-z), so comparing keys as bytes compares the fractions. A key
// never ends with the lowest digit, so there is always a key below it.
//
// Phase B: contract stubs. Nothing here works yet.
package rank

import "errors"

// Alphabet is the digits of a key, in the order they sort.
const Alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// Threshold is the key length past which a container needs a rebalance:
// its keys are rewritten short and evenly spaced. A move never rebalances;
// it only reports that the threshold was crossed.
const Threshold = 32

// MaxGrowth bounds how much longer than its longer neighbour a key from
// Between may be.
const MaxGrowth = 4

// ErrNoRoom is Between's refusal when no key fits: the neighbours are
// equal, or out of order. The caller gives the tied Items fresh keys
// (Evenly) and tries again.
var ErrNoRoom = errors.New("rank: no room between the neighbours")

var errNotBuilt = errors.New("rank: not built yet (Phase B stub)")

// Valid reports whether key is a rank key: not empty, only digits of
// Alphabet, not ending with the lowest one.
func Valid(key string) bool { return false }

// Compare orders two keys byte by byte, as Postgres does under COLLATE "C":
// -1, 0 or 1.
func Compare(a, b string) int { return 0 }

// Between returns a key strictly between lo and hi. An empty lo means "no
// lower neighbour" (the top of the list), an empty hi "no upper neighbour"
// (the bottom). The key is near the midpoint, moved by a random jitter so
// two people who insert at the same place at once rarely pick the same key
// (when they do, the Item ID breaks the tie). It is at most MaxGrowth bytes
// longer than the longer neighbour. lo >= hi is ErrNoRoom.
func Between(lo, hi string) (string, error) { return "", errNotBuilt }

// Evenly returns n keys strictly between lo and hi (empty: open, as in
// Between), ascending, distinct and evenly spaced, as short as n allows:
// what a rebalance writes over a whole container (lo and hi empty), and
// what a move writes over a run of Items whose keys are equal.
func Evenly(lo, hi string, n int) ([]string, error) { return nil, errNotBuilt }

// NeedsRebalance reports whether a key is past Threshold.
func NeedsRebalance(key string) bool { return false }
