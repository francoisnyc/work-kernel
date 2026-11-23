package reading_test

import "github.com/francoisnyc/work-kernel/internal/reading"

// Links of every shape (at most one per source, at most one per target,
// kept for a role), the name a Type gives the links pointing at it, a
// rollup counting over that name, a guard reading it, and a move along a
// link by another Type's reaction, only when its condition holds.
var _ = addWitness(witness{
	name: "links",
	parts: []reading.Part{reading.Link, reading.LinkOne, reading.LinkInverseOne, reading.LinkRoles,
		reading.Incoming, reading.Rollup, reading.MoveLink, reading.ReactionWhen, reading.ConsentMove,
		reading.MoveGuard, reading.WhoNobody},
	sentences: map[string][]string{
		"w_part@1": {
			"Anyone who can see it links it to a w_epic as `part_of`, at most one; a w_epic sees it as `parts`.",
			"Only holders of lead link it to a w_epic as `lead_of`, any number; a w_epic takes at most one; a w_epic sees it as `lead`.",
			"On entering doing, if `f_size` is more than 1, `wake_epic` moves the w_epic it links to as `part_of` from idle to active.",
		},
		"w_epic@1": {
			"It reads the w_parts linked to it as `part_of` under the name `parts`.",
			"When it is read, `progress` counts its `parts` by category.",
			"Only a reaction moves it from idle to active.",
			"Any staff member who can see it moves it from active to done, only if a rule over its `parts` links holds.",
			"w_part's reaction `wake_epic` may move it to active.",
		},
	},
	story: `
story: witness - links, and what reads them
author: written by Claude as a witness of reading item 6
day1: 2026-12-07
people:
  alice: {}
  bob: {}
  lena: {roles: [lead]}
steps:
  - day 1 09:00 alice creates e1 as w_epic@1
  - day 1 09:00 alice creates e2 as w_epic@1
  - day 1 09:01 alice creates p1 as w_part@1 with f_size = 2
  - day 1 09:01 alice creates p2 as w_part@1 with f_size = 1
  - day 1 09:02 alice links p1 part_of e1
  - day 1 09:03 alice links p1 part_of e2 -> refused LINK_CARDINALITY
  - day 1 09:04 alice links p2 part_of e1
  - day 1 09:05 bob links p1 lead_of e1 -> refused LINK_NOT_WRITABLE
  - day 1 09:06 lena links p1 lead_of e1
  - day 1 09:06 lena links p1 lead_of e2
  - day 1 09:07 lena links p2 lead_of e1 -> refused LINK_CARDINALITY
  - day 1 09:08 alice sees e1 with progress.todo = 2, progress.active = 0
  - day 1 09:10 alice moves e1 to active -> refused NOT_ALLOWED
  - day 1 09:11 alice moves p2 to doing
  - day 1 09:12 alice moves p1 to doing
  - day 1 09:13 alice moves e1 to done -> refused GUARD_FAILED
  - day 1 09:14 alice moves p1 to done
  - day 1 09:15 alice moves p2 to done
  - day 1 09:16 alice sees e1 with progress.done = 2, progress.todo = 0
  - day 1 09:17 alice moves e1 to done
until: day 2 00:00
expect:
  - "day 1 09:00 e1: alice created it in idle"
  - "day 1 09:00 e2: alice created it in idle"
  - "day 1 09:01 p1: alice created it in todo"
  - "day 1 09:01 p2: alice created it in todo"
  - "day 1 09:02 p1: alice linked part_of to e1"
  - "day 1 09:04 p2: alice linked part_of to e1"
  - "day 1 09:06 p1: lena linked lead_of to e1"
  - "day 1 09:06 p1: lena linked lead_of to e2"
  - "day 1 09:11 p2: alice moved it todo -> doing"
  - "day 1 09:12 p1: alice moved it todo -> doing"
  - "day 1 09:12 p1: wake_epic for alice applied to e1"
  - "day 1 09:12 e1: wake_epic for alice moved it idle -> active"
  - "day 1 09:14 p1: alice moved it doing -> done"
  - "day 1 09:15 p2: alice moved it doing -> done"
  - "day 1 09:17 e1: alice moved it active -> done"
`,
})

// Who moves an Item to another container or narrows who sees it: anyone
// who can see it, only a role, or no one.
var _ = addWitness(witness{
	name:  "placement",
	parts: []reading.Part{reading.PlacementAnyone, reading.PlacementRoles, reading.PlacementKernel},
	sentences: map[string][]string{
		"w_leave@1": {"Anyone who can see it moves it to another container or narrows who sees it."},
		"w_part@1":  {"Only holders of lead move it to another container or narrow who sees it."},
		"w_vote@1":  {"No one moves it to another container or narrows who sees it."},
	},
	story: `
story: witness - who places an Item
author: written by Claude as a witness of reading item 6
day1: 2026-12-07
observer: alice
people:
  alice: {}
  bob: {}
  carol: {}
  lena: {roles: [lead]}
names:
  vote: w_vote in ballot
steps:
  - day 1 09:00 alice creates box as w_flag@1
  - day 1 09:01 alice creates l1 as w_leave@1
  - day 1 09:02 bob places l1 in box
  - day 1 09:03 bob narrows l1 to alice, bob
  - day 1 09:04 carol cannot see l1
  - day 1 09:05 alice creates p1 as w_part@1
  - day 1 09:06 bob places p1 in box -> refused PLACEMENT_NOT_WRITABLE
  - day 1 09:07 bob narrows p1 to alice, bob -> refused PLACEMENT_NOT_WRITABLE
  - day 1 09:08 lena places p1 in box
  - day 1 09:09 lena places p1 at the top level
  - day 1 09:10 alice creates ballot as w_ballot@1 with f_voters = [bob]
  - day 1 09:11 alice moves ballot to open
  - day 1 09:12 alice places vote in box -> refused PLACEMENT_NOT_WRITABLE
  - day 1 09:13 lena places vote in box -> refused PLACEMENT_NOT_WRITABLE
  - day 1 09:14 bob narrows vote to alice, bob -> refused PLACEMENT_NOT_WRITABLE
until: day 2 00:00
expect:
  - "day 1 09:00 box: alice created it in default"
  - "day 1 09:01 l1: alice created it in draft"
  - "day 1 09:02 l1: bob moved it from the top level to box"
  - "day 1 09:03 l1: bob narrowed it to alice, bob"
  - "day 1 09:05 p1: alice created it in todo"
  - "day 1 09:08 p1: lena moved it from the top level to box"
  - "day 1 09:09 p1: lena moved it from box to the top level"
  - "day 1 09:10 ballot: alice created it in draft"
  - "day 1 09:11 ballot: alice moved it draft -> open"
  - "day 1 09:11 ballot: ask_voters for alice applied to vote"
  - "day 1 09:11 vote: ask_voters for alice created it in pending inside ballot: f_voter = bob"
`,
})
