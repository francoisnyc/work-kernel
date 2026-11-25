package reading_test

import "github.com/francoisnyc/work-kernel/internal/reading"

// Who a state waits on ("waiting on me", track 1 item 5): in review the
// request is in the view of the owner its field names and of a holder of
// the role, and in nobody else's: not its requester's, whose own move out
// of review names nobody, nor a colleague's with no part in it, who is
// refused the move with the API's reason. Before review (a draft: the
// requester's own move) and after it (done: the role's reopen is not
// work), it waits on no one.
var _ = addWitness(witness{
	name:  "who a state waits on",
	parts: []reading.Part{reading.Waits},
	sentences: map[string][]string{"w_queue@1": {
		"While it is in review, it waits on whoever `f_owner` names, or anyone with the role w_desk.",
		"Whoever `f_owner` names, or anyone with the role w_desk, moves it from review to done.",
	}},
	story: `
story: witness - who a state waits on
author: written by Claude as a witness of the waiting-on-me view (Phase B)
day1: 2026-12-07
people:
  ana: {}
  bob: {}
  dee: {roles: [w_desk]}
  olaf: {}
steps:
  - 'day 1 09:00 ana creates q1 as w_queue@1 with f_what = "a key", f_owner = bob'
  - day 1 09:01 ana finds nothing waiting
  - day 1 09:02 bob finds nothing waiting
  - day 1 09:03 ana moves q1 to review
  - day 1 09:04 bob finds waiting q1
  - day 1 09:05 dee finds waiting q1
  - day 1 09:06 ana finds nothing waiting
  - day 1 09:07 olaf finds nothing waiting
  - day 1 09:08 olaf moves q1 to done -> refused NOT_ALLOWED
  - day 1 09:09 bob moves q1 to done
  - day 1 09:10 bob finds nothing waiting
  - day 1 09:11 dee finds nothing waiting
until: day 2 00:00
expect:
  - "day 1 09:00 q1: ana created it in draft"
  - "day 1 09:03 q1: ana moved it draft -> review"
  - "day 1 09:09 q1: bob moved it review -> done"
`,
})
