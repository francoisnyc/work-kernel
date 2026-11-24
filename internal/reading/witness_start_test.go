package reading_test

import "github.com/francoisnyc/work-kernel/internal/reading"

// Who starts an Item, and where (starting work, step 2): the latest
// version's start block judges a create of every version. Each entry of
// allowed lets its principal in, someone else is refused (NOT_ALLOWED);
// a customer is judged by own, not by allowed; nobody leaves the create
// to a reaction; in refuses the wrong container and the top level, unless
// it says top.
var _ = addWitness(witness{
	name: "start",
	parts: []reading.Part{reading.StartIn, reading.StartInTop, reading.StartWho, reading.StartWhoField,
		reading.StartWhoContainerCreator, reading.StartWhoNobody, reading.StartWhoCustomers, reading.StartLatest,
		reading.WhoRole, reading.WhoPrincipal, reading.WhoStaff},
	sentences: map[string][]string{
		"w_ask@2": {
			"A w_ask is created only inside a w_room.",
			"Anyone with the role lead, or `user:root`, or whoever `f_lead` of the container names, or anyone who took part in creating the container, " +
				"creates one; a customer creates one only where a container lets them.",
			"While this is the latest version of w_ask, these rules apply to creating a w_ask of any version.",
		},
		"w_chore@1": {
			"A w_chore is created only inside a w_room.",
			"No person creates a w_chore: only a reaction does.",
		},
		"w_jot@1": {
			"A w_jot is created only inside a w_room, or at the top level.",
			"Any staff member who can see the container creates one; a customer creates one only where a container lets them.",
		},
	},
	story: `
story: witness - who starts an Item, and where
author: written by Claude as a witness of starting work, step 2
day1: 2026-12-07
people:
  dana: {}
  lea: {roles: [lead]}
  bob: {}
  eve: {}
  root: {}
  kim: {customer: true}
names:
  chore: w_chore in room
given:
  - dana creates room as w_room@1 with f_lead = bob
steps:
  - day 1 09:00 lea creates a1 as w_ask@1 in room
  - day 1 09:01 eve creates a2 as w_ask@1 in room -> refused NOT_ALLOWED
  - day 1 09:02 lea creates a3 as w_ask@1 -> refused NOT_ALLOWED
  - day 1 09:03 root creates a4 as w_ask@2 in room
  - day 1 09:04 bob creates a5 as w_ask@1 in room
  - day 1 09:05 dana creates a6 as w_ask@1 in room
  - day 1 09:06 dana updates room with f_lead = eve
  - day 1 09:07 eve creates a7 as w_ask@1 in room
  - day 1 09:08 bob creates a8 as w_ask@1 in room -> refused NOT_ALLOWED
  - day 1 09:09 kim creates a9 as w_ask@1 in room
  - day 1 09:10 lea creates c1 as w_chore@1 in room -> refused NOT_ALLOWED
  - day 1 09:11 lea moves room to busy
  - day 1 09:12 eve creates j1 as w_jot@1
  - day 1 09:13 eve creates j2 as w_jot@1 in room
  - day 1 09:14 eve creates j3 as w_jot@1 in a1 -> refused NOT_ALLOWED
until: day 2 00:00
expect:
  - "day 1 09:00 a1: lea created it in default inside room"
  - "day 1 09:03 a4: root created it in default inside room"
  - "day 1 09:04 a5: bob created it in default inside room"
  - "day 1 09:05 a6: dana created it in default inside room"
  - "day 1 09:06 room: dana updated it"
  - "day 1 09:07 a7: eve created it in default inside room"
  - "day 1 09:09 a9: kim created it in default inside room"
  - "day 1 09:11 room: lea moved it open -> busy"
  - "day 1 09:11 room: make_chore for lea applied to chore"
  - "day 1 09:11 chore: make_chore for lea created it in default inside room: f_kind = chore"
  - "day 1 09:12 j1: eve created it in default"
  - "day 1 09:13 j2: eve created it in default inside room"
`,
})
