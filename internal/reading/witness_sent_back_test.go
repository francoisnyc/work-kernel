package reading_test

import "github.com/francoisnyc/work-kernel/internal/reading"

// Sent back to the requester (track 1 item 5, step 5). kim, a customer,
// opens a request in the help desk; it waits on the help team. alice, in
// the team, asks kim a question: the request moves to asked, where it
// waits on whoever asked for it, kim, and on nobody else (alice, lee,
// another customer). kim answers by sending it back to open, where it
// waits on the team again. The same holds for staff: ana's own request,
// sent back, waits on ana.
var _ = addWitness(witness{
	name:  "sent back to the requester",
	parts: []reading.Part{reading.WaitsRequester},
	sentences: map[string][]string{"w_back@1": {
		"While it is in asked, it waits on whoever asked for it.",
	}},
	story: `
story: witness - sent back to the requester
author: written by Claude as a witness of sent back to the requester (Phase B)
day1: 2026-12-07
people:
  alice: {roles: [w_help]}
  ana: {}
  kim: {customer: true}
  lee: {customer: true}
given:
  - alice creates desk as w_helpdesk@1
steps:
  - 'day 1 09:00 kim creates r1 as w_back@1 in desk with f_title = "Printer"'
  - day 1 09:01 alice finds waiting r1
  - day 1 09:02 kim finds nothing waiting
  - day 1 09:03 alice moves r1 to asked
  - day 1 09:04 kim finds waiting r1
  - day 1 09:05 alice finds nothing waiting
  - day 1 09:06 lee finds nothing waiting
  - day 1 09:07 kim moves r1 to open
  - day 1 09:08 kim finds nothing waiting
  - day 1 09:09 alice finds waiting r1
  - 'day 1 09:10 ana creates r2 as w_back@1 in desk with f_title = "Badge"'
  - day 1 09:11 alice moves r2 to asked
  - day 1 09:12 ana finds waiting r2
  - day 1 09:13 alice finds waiting r1
until: day 2 00:00
expect:
  - "day 1 09:00 r1: kim created it in open inside desk"
  - "day 1 09:03 r1: alice moved it open -> asked"
  - "day 1 09:07 r1: kim moved it asked -> open"
  - "day 1 09:10 r2: ana created it in open inside desk"
  - "day 1 09:11 r2: alice moved it open -> asked"
`,
})
