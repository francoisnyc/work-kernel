package reading_test

import "github.com/francoisnyc/work-kernel/internal/reading"

// A held reaction on a linked Item, its undo and a hook on the undo, the
// link the kernel resolves on entering a state, and fields only a role or
// only the kernel writes. The move waits for the reaction's write on the
// budget: it happens when the budget accepts it, and is denied with the
// budget's reason when it does not.
var _ = addWitness(witness{
	name: "held",
	parts: []reading.Part{reading.Held, reading.Undo, reading.ReactionHook, reading.Resolve, reading.Link,
		reading.LinkOne, reading.LinkKernel, reading.FieldRoles, reading.FieldKernel, reading.ConsentAdd,
		reading.WhoRole, reading.WhoCreator, reading.Invariant},
	sentences: map[string][]string{
		"w_spend@1": {
			"Only the kernel links it to a w_budget as `budget`, at most one; a w_budget sees it as `spends`.",
			"The requester moves it from draft to asked.",
			"On entering asked, the kernel links `budget` to the requester's open w_budget (the one whose `f_holder` is the requester); " +
				"if there is none, or more than one, the move into asked is refused.",
			"Anyone with the role treasurer moves it from asked to spent.",
			"Asking for this move opens a hold instead: `take` adds `f_amount` to `f_spent` on the w_budget linked as `budget`; " +
				"the move happens only if that Item accepts the write, else it is denied with that Item's reason.",
			"The requester moves it from spent to undone.",
			"On entering undone from spent, `give_back` gives back what `take` added.",
			"When `give_back` has applied, `tell` calls `email.notify@1` with to = the requester.",
		},
		"w_budget@1": {
			"Every write keeps this true: `f_spent` is unset or `f_spent` is at most `f_limit`; a write that would not is refused (over_limit).",
			"Only holders of treasurer write `f_holder`.",
			"Only holders of treasurer write `f_limit`.",
			"Only the kernel writes `f_spent`: w_spend's reactions `give_back` and `take`.",
			"w_spend's reaction `give_back` may change `f_spent`.",
			"w_spend's reaction `take` may change `f_spent`.",
		},
	},
	story: `
story: witness - a held reaction and its undo
author: written by Claude as a witness of reading item 6
day1: 2026-12-07
people:
  alice: {}
  bob: {}
  carol: {}
  hana: {roles: [treasurer]}
fakes:
  email.notify@1:
    - ok
given:
  - hana creates budget as w_budget@1 with f_holder = alice, f_limit = 10
  - hana creates budget2 as w_budget@1 with f_holder = bob, f_limit = 5
steps:
  - day 1 09:00 bob updates budget with f_limit = 100 -> refused FIELD_NOT_WRITABLE
  - day 1 09:01 hana updates budget with f_spent = 1 -> refused FIELD_NOT_WRITABLE
  - day 1 09:05 alice creates s1 as w_spend@1 with f_amount = 4
  - day 1 09:06 alice moves s1 to asked
  - day 1 09:07 alice links s1 budget budget2 -> refused LINK_NOT_WRITABLE
  - day 1 09:08 carol creates s3 as w_spend@1 with f_amount = 1
  - day 1 09:09 carol moves s3 to asked -> refused LINK_UNRESOLVED
  - day 1 09:10 alice moves s1 to spent -> refused NOT_ALLOWED
  - day 1 09:11 hana moves s1 to spent
  - day 1 09:12 hana sees budget with f_spent = 4
  - day 1 09:20 alice creates s2 as w_spend@1 with f_amount = 8
  - day 1 09:21 alice moves s2 to asked
  - day 1 09:22 hana moves s2 to spent
  - day 1 09:23 hana sees budget with f_spent = 4
  - day 1 09:30 bob moves s1 to undone -> refused NOT_ALLOWED
  - day 1 09:31 alice moves s1 to undone
  - day 1 09:32 hana sees budget with f_spent = 0
until: day 2 00:00
expect:
  - "day 1 09:05 s1: alice created it in draft"
  - "day 1 09:06 s1: alice moved it draft -> asked; linked budget to budget"
  - "day 1 09:08 s3: carol created it in draft"
  - "day 1 09:11 s1: hana asked asked -> spent"
  - "day 1 09:11 budget: take for hana updated it: f_spent +4 = 4"
  - "day 1 09:11 s1: take for hana moved it asked -> spent"
  - "day 1 09:20 s2: alice created it in draft"
  - "day 1 09:21 s2: alice moved it draft -> asked; linked budget to budget"
  - "day 1 09:22 s2: hana asked asked -> spent"
  - "day 1 09:22 s2: take for hana denied asked -> spent: over_limit"
  - "day 1 09:31 s1: alice moved it spent -> undone"
  - "day 1 09:31 budget: give_back for alice updated it: f_spent -4 = 0"
  - "day 1 09:31 s1: give_back for alice applied to budget"
  - "day 1 09:31 s1: tell for alice ran"
  - "day 1 09:31 email: notify for s1 (tell): to = alice"
`,
})
