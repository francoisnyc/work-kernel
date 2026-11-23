package reading_test

import "github.com/francoisnyc/work-kernel/internal/reading"

// Hooks on entering a state: an enrich hook writes its answer, or its
// fallback when it has none within 15 minutes; an action writes what it
// made, once, and completes even though the Item has moved on. A move
// kept for a hook is refused to people. A field the kernel writes is
// refused to people.
var _ = addWitness(witness{
	name: "hooks",
	parts: []reading.Part{reading.Enrich, reading.EnrichFallbk, reading.Action, reading.ActionWrites,
		reading.WhoHook, reading.FieldKernel},
	sentences: map[string][]string{"w_order@1": {
		"Only the kernel writes `f_invoice`: `invoice`.",
		"On entering placed, `plan` calls `crm.get_plan@1` with customerId = `f_customer` and writes its answer into `f_plan`; with no answer within 15 minutes it writes `f_plan` = free.",
		"Only the hook `plan` may move it from placed to flagged.",
		"On entering billed, `invoice` calls `erp.create_invoice@1` with customerId = `f_customer`, amount = `f_amount` and writes what it made into `f_invoice`, once per entry, even if it has moved on.",
	}},
	story: `
story: witness - hooks on entering a state
author: written by Claude as a witness of reading item 6
day1: 2026-12-07
people:
  alice: {}
fakes:
  crm.get_plan@1:
    - "when customerId = acme: ok plan = pro"
    - "when customerId = slow: fail transient"
  erp.create_invoice@1:
    - "when customerId = acme until day 1 11:05: fail transient"
    - 'when customerId = acme: ok invoiceId = "INV-1"'
steps:
  - 'day 1 09:00 alice creates o1 as w_order@1 with f_customer = "acme", f_amount = 100'
  - day 1 10:00 alice moves o1 to placed
  - day 1 10:00 alice sees o1 with f_plan = pro
  - day 1 10:01 alice moves o1 to flagged -> refused NOT_ALLOWED
  - day 1 11:00 alice moves o1 to billed
  - day 1 11:01 alice moves o1 to archived
  - 'day 1 11:10 alice updates o1 with f_invoice = "X" -> refused FIELD_NOT_WRITABLE'
  - 'day 1 11:10 alice sees o1 with f_invoice = "INV-1"'
  - 'day 1 12:00 alice creates o2 as w_order@1 with f_customer = "slow", f_amount = 1'
  - day 1 12:00 alice moves o2 to placed
until: day 2 00:00
expect:
  - "day 1 09:00 o1: alice created it in new"
  - "day 1 10:00 o1: alice moved it new -> placed"
  - "day 1 10:00 o1: plan for alice updated it: f_plan = pro"
  - "day 1 11:00 o1: alice moved it placed -> billed"
  - "day 1 11:01 o1: alice moved it billed -> archived"
  - 'between day 1 11:05 and day 1 11:08 o1: invoice for alice updated it: f_invoice = "INV-1"'
  - 'between day 1 11:05 and day 1 11:08 erp: create_invoice for o1 (invoice): amount = 100, customerId = "acme" -> invoiceId = "INV-1"'
  - "day 1 12:00 o2: alice created it in new"
  - "day 1 12:00 o2: alice moved it new -> placed"
  - "between day 1 12:00 and day 1 12:20 o2: plan for alice updated it: f_plan = free"
`,
})
