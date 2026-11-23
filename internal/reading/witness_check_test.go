package reading_test

import "github.com/francoisnyc/work-kernel/internal/reading"

// A check on a move: asking opens a hold; the check allows with a
// correction, denies with one of its reasons, or gives up after 15
// minutes. While the hold is open the fields it reads cannot change (other
// fields can) and only the escape leaves; the escape cancels the hold. A
// state timer due while a hold is open waits for it to close, then looks
// again: c1 was booked meanwhile (nothing happens), c2 was denied (it
// lapses at once). From parked, where no move escapes, a hold lets
// nothing move the Item (c5).
var _ = addWitness(witness{
	name: "check",
	parts: []reading.Part{reading.Check, reading.CheckCorrect, reading.Hold, reading.HoldFrozen, reading.HoldEscape, reading.HoldNone,
		reading.MoveEscape, reading.TimerWaits},
	sentences: map[string][]string{"w_check@1": {
		"1 hour after it enters draft, if it is still there, the timer moves it to lapsed; if a hold is open then, the timer waits for it to close and looks again.",
		"Any staff member who can see it moves it from draft to booked.",
		"Asking for this move opens a hold instead: `credit` calls `erp.check_credit@1` with customerId = `f_customer`, amount = `f_amount`; " +
			"if it allows, or corrects `f_cost`, the move happens; it may deny it (customer_on_hold, over_credit_limit); with no answer within 15 minutes the move fails.",
		"While that hold is open, nobody changes `f_customer` or `f_amount`, and the only move out of draft is to dropped.",
		"Any staff member who can see it moves it from draft to dropped, even while a hold is open on it, which cancels the hold.",
		"Asking for this move opens a hold instead: `recheck` calls `erp.check_credit@1` with customerId = `f_customer`, amount = `f_amount`; " +
			"if it allows, or corrects `f_cost`, the move happens; it may deny it (customer_on_hold, over_credit_limit); with no answer within 15 minutes the move fails.",
		"While that hold is open, nobody changes `f_customer` or `f_amount`, and nothing moves it.",
	}},
	story: `
story: witness - a check, its hold and an escape
author: written by Claude as a witness of reading item 6
day1: 2026-12-07
people:
  alice: {}
fakes:
  erp.check_credit@1:
    - "when customerId = acme until day 1 10:02: fail transient"
    - "when customerId = acme: correct costCenter = CC1"
    - "when customerId = bolt until day 1 10:02: fail transient"
    - "when customerId = bolt: deny customer_on_hold"
    - "when customerId = cora: fail transient"
    - "when customerId = dora: fail transient"
    - "when customerId = emma until day 2 12:10: fail transient"
    - "when customerId = emma: allow"
steps:
  - 'day 1 09:00 alice creates c1 as w_check@1 with f_customer = "acme", f_amount = 10'
  - 'day 1 09:00 alice creates c2 as w_check@1 with f_customer = "bolt", f_amount = 5'
  - 'day 1 09:00 alice creates c3 as w_check@1 with f_customer = "cora", f_amount = 1'
  - day 1 09:10 alice moves c3 to booked
  - day 1 09:12 alice moves c3 to parked -> refused HELD
  - day 1 09:15 alice moves c3 to dropped
  - day 1 09:50 alice moves c1 to booked
  - day 1 09:50 alice moves c2 to booked
  - day 1 09:55 alice updates c1 with f_amount = 11 -> refused HELD
  - 'day 1 09:56 alice updates c1 with f_note = "rush"'
  - day 1 09:57 alice moves c1 to parked -> refused HELD
  - 'day 2 09:00 alice creates c4 as w_check@1 with f_customer = "dora", f_amount = 1'
  - day 2 09:05 alice moves c4 to booked
  - 'day 2 12:00 alice creates c5 as w_check@1 with f_customer = "emma", f_amount = 1'
  - day 2 12:00 alice moves c5 to parked
  - day 2 12:01 alice moves c5 to booked
  - day 2 12:02 alice moves c5 to draft -> refused HELD
until: day 3 00:00
expect:
  - "day 1 09:00 c1: alice created it in draft"
  - "day 1 09:00 c2: alice created it in draft"
  - "day 1 09:00 c3: alice created it in draft"
  - "day 1 09:10 c3: alice asked draft -> booked"
  - "day 1 09:15 c3: alice moved it draft -> dropped"
  - "day 1 09:50 c1: alice asked draft -> booked"
  - "day 1 09:50 c2: alice asked draft -> booked"
  - "day 1 09:56 c1: alice updated it"
  - 'between day 1 10:02 and day 1 10:05 c1: credit for alice moved it draft -> booked: f_cost = "CC1"'
  - "between day 1 10:02 and day 1 10:05 c2: credit for alice denied draft -> booked: customer_on_hold"
  - "between day 1 10:02 and day 1 10:05 c2: timer for alice moved it draft -> lapsed"
  - "day 2 09:00 c4: alice created it in draft"
  - "day 2 09:05 c4: alice asked draft -> booked"
  - "between day 2 09:05 and day 2 09:20 c4: credit for alice failed draft -> booked: budget_exhausted"
  - "day 2 10:00 c4: timer for alice moved it draft -> lapsed"
  - "day 2 12:00 c5: alice created it in draft"
  - "day 2 12:00 c5: alice moved it draft -> parked"
  - "day 2 12:01 c5: alice asked parked -> booked"
  - "between day 2 12:10 and day 2 12:13 c5: recheck for alice moved it parked -> booked"
`,
})
