package story

// Diff is how a timeline differs from the one expected.
type Diff struct {
	Matched    int    // lines seen as expected
	Missing    []Line // expected and not seen, in time order
	Unexpected []Line // seen and not expected, in time order
}

// Empty reports whether the timelines agree.
func (d Diff) Empty() bool { return len(d.Missing) == 0 && len(d.Unexpected) == 0 }

// Compare holds what was seen against what was expected: instants in
// strict order, the lines of one instant as a multiset.
func Compare(expected, seen []Line) Diff {
	return Diff{Missing: expected, Unexpected: seen} // stub
}
