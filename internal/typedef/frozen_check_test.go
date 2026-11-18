// Tests for the consolidation after Step 2, item 2: a check's correction
// travels with the move out of its transition's from-state, so it may not
// write a field that state freezes. Refused at load, rather than as
// FIELD_FROZEN on every resolution.
package typedef_test

import "testing"

const frozenCheckSrc = `
id: claim
version: 1
initial_state: filed
fields:
  - {id: f_customer, type: string}
  - {id: f_amount,   type: int}
  - {id: f_center,   type: string}
states:
  - {id: filed,  category: todo, frozen: [f_amount]}
  - {id: paid,   category: done}
transitions:
  - {from: filed, to: paid}
hooks:
  - {id: credit, on: transition, from: filed, to: paid, kind: check, action: erp.check_credit,
     reads: [f_customer, f_amount], writes: [f_center]}
`

func TestCheckWritesAFieldItsFromStateFreezes_Refused(t *testing.T) {
	mustParse(t, frozenCheckSrc) // f_center is not frozen in filed
	mustRefuse(t, "a check writing a field frozen in its from-state",
		swap(t, frozenCheckSrc, "frozen: [f_amount]", "frozen: [f_amount, f_center]"), "f_center")
}
