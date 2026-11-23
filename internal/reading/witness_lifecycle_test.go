package reading_test

import "github.com/francoisnyc/work-kernel/internal/reading"

// A plain lifecycle: where it starts, the moves and their clauses, a frozen
// state, an invariant, a final state, and that no other move is possible.
// It is also the witness that a false guard refuses the move with the
// API's reason (GUARD_FAILED), on both of the guard's conditions, and that
// "on or after" holds on the day itself.
var _ = addWitness(witness{
	name: "lifecycle",
	parts: []reading.Part{reading.Starts, reading.Closed, reading.Invariant, reading.Move, reading.MoveRequired,
		reading.MoveGuard, reading.MoveSets, reading.MoveClears, reading.Frozen, reading.Final},
	sentences: map[string][]string{"w_leave@1": {
		"A w_leave starts in draft.",
		"It moves only as listed below; any other move is refused.",
		"Every write keeps this true: `f_days` is unset or `f_days` is at least 0; a write that would not is refused (days_negative).",
		"Any staff member who can see it moves it from draft to submitted, once `f_start` and `f_end` are set, " +
			"only if `f_end` is on or after `f_start` and `f_start` is on or after today; the move sets `f_sent_at` to the time of the write.",
		"While it is in submitted, nobody changes `f_start` or `f_end`.",
		"Any staff member who can see it moves it from submitted to draft; the move clears `f_note`.",
		"Any staff member who can see it moves it from submitted to done.",
		"Nothing leaves done.",
	}},
	story: `
story: witness - a plain lifecycle
author: written by Claude as a witness of reading item 6
day1: 2026-12-07
people:
  alice: {}
steps:
  - day 1 09:00 alice creates l1 as w_leave@1 with f_start = day 3, f_days = 2
  - day 1 09:00 alice sees l1 fields f_days, f_start
  - day 1 09:05 alice moves l1 to submitted -> refused MISSING_FIELDS
  - day 1 09:10 alice updates l1 with f_end = day 2
  - day 1 09:15 alice moves l1 to submitted -> refused GUARD_FAILED
  - day 1 09:20 alice updates l1 with f_end = day 4
  - day 4 09:00 alice moves l1 to submitted -> refused GUARD_FAILED
  - day 4 09:10 alice updates l1 with f_start = day 4, f_note = "first try"
  - day 4 09:20 alice updates l1 with f_days = -1 -> refused INVARIANT_FAILED
  - day 4 09:30 alice moves l1 to done -> refused NO_TRANSITION
  - day 4 09:40 alice moves l1 to submitted
  - day 4 09:50 alice updates l1 with f_start = day 5 -> refused FIELD_FROZEN
  - day 4 09:55 alice updates l1 with f_days = 3
  - day 4 10:00 alice moves l1 to draft
  - day 4 10:00 alice sees l1 with f_note = null, f_start = day 4
  - day 4 10:10 alice moves l1 to submitted
  - day 4 10:20 alice moves l1 to done
  - day 4 10:30 alice moves l1 to submitted -> refused NO_TRANSITION
  - day 4 10:40 alice moves l1 to draft -> refused NO_TRANSITION
until: day 5 00:00
expect:
  - "day 1 09:00 l1: alice created it in draft"
  - "day 1 09:10 l1: alice updated it"
  - "day 1 09:20 l1: alice updated it"
  - "day 4 09:10 l1: alice updated it"
  - "day 4 09:40 l1: alice moved it draft -> submitted: f_sent_at = day 4 09:40"
  - "day 4 09:55 l1: alice updated it"
  - "day 4 10:00 l1: alice moved it submitted -> draft: f_note = null"
  - "day 4 10:10 l1: alice moved it draft -> submitted: f_sent_at = day 4 10:10"
  - "day 4 10:20 l1: alice moved it submitted -> done"
`,
})
