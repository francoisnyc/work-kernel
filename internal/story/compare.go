package story

import (
	"slices"
	"strings"
	"time"
)

// Diff is how a timeline differs from the one expected.
type Diff struct {
	Matched    int    // lines seen as expected
	Missing    []Line // expected and not seen, in time order
	Unexpected []Line // seen and not expected, in time order
}

// Empty reports whether the timelines agree.
func (d Diff) Empty() bool { return len(d.Missing) == 0 && len(d.Unexpected) == 0 }

// Compare holds what was seen against what was expected: instants in
// strict order, the lines of one instant as a multiset; then each window
// takes one line left within it.
func Compare(expected, seen []Line) Diff {
	var d Diff
	type key struct {
		at         time.Time
		item, text string
	}
	keyOf := func(l Line, at time.Time) key { return key{at.UTC().Truncate(time.Second), l.Item, spaced(l.Text)} }

	want := map[key]int{}
	var windows []Line
	for _, l := range expected {
		if !l.To.IsZero() {
			windows = append(windows, l)
			continue
		}
		want[keyOf(l, l.At)]++
	}
	var left []Line // seen, not matched by an exact line
	for _, l := range seen {
		k := keyOf(l, l.At)
		if want[k] > 0 {
			want[k]--
			d.Matched++
			continue
		}
		left = append(left, l)
	}
	for _, l := range expected {
		if !l.To.IsZero() {
			continue
		}
		if k := keyOf(l, l.At); want[k] > 0 {
			want[k]--
			d.Missing = append(d.Missing, l)
		}
	}
	slices.SortStableFunc(windows, func(a, b Line) int { return a.At.Compare(b.At) })
	for _, w := range windows {
		from, to := w.At.UTC().Truncate(time.Second), w.To.UTC().Truncate(time.Second)
		i := slices.IndexFunc(left, func(l Line) bool {
			at := l.At.UTC().Truncate(time.Second)
			return l.Item == w.Item && spaced(l.Text) == spaced(w.Text) && !at.Before(from) && !at.After(to)
		})
		if i < 0 {
			d.Missing = append(d.Missing, w)
			continue
		}
		left = slices.Delete(left, i, i+1)
		d.Matched++
	}
	d.Unexpected = left
	byTime := func(a, b Line) int { return a.At.Compare(b.At) }
	slices.SortStableFunc(d.Missing, byTime)
	slices.SortStableFunc(d.Unexpected, byTime)
	return d
}

// spaced is text with every run of spaces as one.
func spaced(s string) string { return strings.Join(strings.Fields(s), " ") }
