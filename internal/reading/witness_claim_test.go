package reading_test

import "github.com/francoisnyc/work-kernel/internal/reading"

// A claim that binds (track 1 item 5, step 2). In the queue the request
// waits on everyone in w_fin. gus moves it into review: the kernel records
// him, and from then on it waits on him alone. fay, in w_fin like him, is
// refused the approval with the API's reason, and cannot write the claim
// field; she can put the request back, which releases it. She cannot claim
// it for gus; she claims it herself, and then gus is the one refused.
var _ = addWitness(witness{
	name:  "a claim that binds",
	parts: []reading.Part{reading.Claim, reading.WaitsClaimant, reading.MoveRelease},
	sentences: map[string][]string{"w_claim@1": {
		"Whoever moves it into review claims it: `f_claimed_by` names them, and only they move it on from there, unless a move releases the claim.",
		"While it is in review, it waits on whoever claimed it.",
		"Anyone with the role w_fin moves it from review to queue, which releases the claim; the move clears `f_claimed_by`.",
		"Anyone with the role w_fin moves it from review to approved.",
	}},
	story: `
story: witness - a claim that binds
author: written by Claude as a witness of the claim that binds (Phase B)
day1: 2026-12-07
people:
  ana: {}
  gus: {roles: [w_fin]}
  fay: {roles: [w_fin]}
steps:
  - 'day 1 09:00 ana creates c1 as w_claim@1 with f_what = "a laptop"'
  - day 1 09:01 gus finds waiting c1
  - day 1 09:02 fay finds waiting c1
  - day 1 09:03 gus moves c1 to review
  - day 1 09:04 gus sees c1 with f_claimed_by = gus
  - day 1 09:05 gus finds waiting c1
  - day 1 09:06 fay finds nothing waiting
  - day 1 09:07 fay moves c1 to approved -> refused NOT_ALLOWED
  - day 1 09:08 fay updates c1 with f_claimed_by = fay -> refused FIELD_FROZEN
  - day 1 09:09 fay moves c1 to queue
  - day 1 09:10 fay sees c1 with f_claimed_by = null
  - day 1 09:11 gus finds waiting c1
  - day 1 09:12 fay moves c1 to review with f_claimed_by = gus -> refused FIELD_NOT_WRITABLE
  - day 1 09:13 fay moves c1 to review
  - day 1 09:14 gus moves c1 to approved -> refused NOT_ALLOWED
  - day 1 09:15 fay moves c1 to approved
until: day 2 00:00
expect:
  - "day 1 09:00 c1: ana created it in queue"
  - "day 1 09:03 c1: gus moved it queue -> review: f_claimed_by = gus"
  - "day 1 09:09 c1: fay moved it review -> queue: f_claimed_by = null"
  - "day 1 09:13 c1: fay moved it queue -> review: f_claimed_by = fay"
  - "day 1 09:15 c1: fay moved it review -> approved"
`,
})
