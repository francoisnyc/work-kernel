// Tests for the consolidation after Step 2, item 5: an escape leaves a
// state whatever hold is open there, canceling it, so it may not open a
// hold of its own: a check or a held reaction on an escape transition is
// refused at load (today a second open hold hits the one-open-hold index
// and surfaces as an internal error).
package typedef_test

import "testing"

func TestHoldOnAnEscape_Refused(t *testing.T) {
	mustRefuse(t, "a check on an escape", swap(t, frozenCheckSrc,
		"  - {from: filed, to: paid}", "  - {from: filed, to: paid, escape: true}"), "escape")
	mustRefuse(t, "a held reaction on an escape", swap(t, pledgeSrc,
		"  - {from: filed, to: paid}", "  - {from: filed, to: paid, escape: true}"), "escape")
	mustParse(t, pledgeSrc) // filed -> dropped is an escape, and holds nothing
}
