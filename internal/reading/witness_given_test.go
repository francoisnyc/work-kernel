package reading_test

import "github.com/francoisnyc/work-kernel/internal/reading"

// "Required means given by the mover": a reason written before the move,
// by a colleague, does not let the decider reject without one (refused
// with the API's reason, MISSING_FIELDS); given in the move, even the same
// text, it does. Once rejected, the reason is frozen.
var _ = addWitness(witness{
	name:  "given by the mover",
	parts: []reading.Part{reading.Part("move.given")},
	sentences: map[string][]string{"w_reject@1": {
		"Anyone with the role finance moves it from review to rejected, once the mover gives `f_reason` in the move itself.",
	}},
	story: `
story: witness - given by the mover
author: written by Claude as a witness of required means given by the mover (Phase B)
day1: 2026-12-07
people:
  ana: {}
  gus: {}
  fay: {roles: [finance]}
steps:
  - day 1 09:00 ana creates r1 as w_reject@1
  - 'day 1 09:01 gus updates r1 with f_reason = "planted by gus"'
  - day 1 09:02 fay moves r1 to rejected -> refused MISSING_FIELDS
  - 'day 1 09:03 fay moves r1 to rejected with f_note = "see the sheet" -> refused MISSING_FIELDS'
  - 'day 1 09:04 fay moves r1 to rejected with f_reason = "Over budget"'
  - 'day 1 09:05 fay sees r1 with f_reason = "Over budget", f_note = null'
  - 'day 1 09:06 ana creates r2 as w_reject@1'
  - 'day 1 09:07 gus updates r2 with f_reason = "Over budget"'
  - 'day 1 09:08 fay moves r2 to rejected with f_reason = "Over budget"'
until: day 2 00:00
expect:
  - "day 1 09:00 r1: ana created it in review"
  - "day 1 09:01 r1: gus updated it"
  - "day 1 09:04 r1: fay moved it review -> rejected"
  - "day 1 09:06 r2: ana created it in review"
  - "day 1 09:07 r2: gus updated it"
  - "day 1 09:08 r2: fay moved it review -> rejected"
`,
})
