package reading_test

import "github.com/francoisnyc/work-kernel/internal/reading"

// "Only the requester" for drafts: while the Item is in the state, only its
// requester changes the fields the state lists; a colleague is refused
// with the API's reason (FIELD_NOT_WRITABLE) and still writes a field the
// state does not list. Once sent, frozen holds for everyone, the requester
// included.
var _ = addWitness(witness{
	name:  "only the requester",
	parts: []reading.Part{reading.RequesterOnly},
	sentences: map[string][]string{"w_draft@1": {
		"While it is in draft, only the requester changes `f_amount` or `f_vendor`.",
		"While it is in sent, nobody changes `f_amount` or `f_vendor`.",
	}},
	story: `
story: witness - only the requester
author: written by Claude as a witness of only the requester for drafts (Phase B)
day1: 2026-12-07
people:
  ana: {}
  olaf: {}
steps:
  - 'day 1 09:00 ana creates d1 as w_draft@1 with f_amount = 2500, f_vendor = "officeco"'
  - day 1 09:01 olaf updates d1 with f_amount = 950 -> refused FIELD_NOT_WRITABLE
  - 'day 1 09:02 olaf updates d1 with f_vendor = "shadyparts" -> refused FIELD_NOT_WRITABLE'
  - 'day 1 09:03 olaf updates d1 with f_note = "see the quote"'
  - day 1 09:04 ana updates d1 with f_amount = 2400
  - day 1 09:05 ana moves d1 to sent
  - day 1 09:06 ana updates d1 with f_amount = 1 -> refused FIELD_FROZEN
  - 'day 1 09:07 ana sees d1 with f_amount = 2400, f_vendor = "officeco", f_note = "see the quote"'
until: day 2 00:00
expect:
  - "day 1 09:00 d1: ana created it in draft"
  - "day 1 09:03 d1: olaf updated it"
  - "day 1 09:04 d1: ana updated it"
  - "day 1 09:05 d1: ana moved it draft -> sent"
`,
})
