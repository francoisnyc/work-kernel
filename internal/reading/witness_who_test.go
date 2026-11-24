package reading_test

import "github.com/francoisnyc/work-kernel/internal/reading"

// Who may make a move, one kind of `allowed` entry at a time: each move is
// made by the one it names and refused (NOT_ALLOWED) to someone else.
var _ = addWitness(witness{
	name: "who",
	parts: []reading.Part{reading.WhoStaff, reading.WhoCreator, reading.WhoRole, reading.WhoField,
		reading.WhoPrincipal, reading.ExceptCreator},
	sentences: map[string][]string{"w_who@1": {
		"Any staff member who can see it moves it from open to by_staff.",
		"The requester moves it from open to by_requester.",
		"Anyone with the role lead moves it from open to by_role.",
		"Whoever `f_owner` names, but never anyone who took part in creating it, moves it from open to by_owner.",
		"`user:root`, or anyone with the role lead, moves it from open to by_root.",
	}},
	story: `
story: witness - who may make a move
author: written by Claude as a witness of reading item 6
day1: 2026-12-07
people:
  alice: {}
  bob: {}
  carol: {}
  lena: {roles: [lead]}
  root: {}
steps:
  - day 1 09:00 alice creates w1 as w_who@1 with f_owner = bob
  - day 1 09:01 carol moves w1 to by_requester -> refused NOT_ALLOWED
  - day 1 09:02 bob moves w1 to by_requester -> refused NOT_ALLOWED
  - day 1 09:03 alice moves w1 to by_requester
  - day 1 09:10 alice creates w2 as w_who@1 with f_owner = alice
  - day 1 09:11 alice moves w2 to by_owner -> refused NOT_ALLOWED
  - day 1 09:12 carol moves w2 to by_owner -> refused NOT_ALLOWED
  - day 1 09:20 alice creates w3 as w_who@1 with f_owner = bob
  - day 1 09:21 carol moves w3 to by_owner -> refused NOT_ALLOWED
  - day 1 09:22 bob moves w3 to by_owner
  - day 1 09:30 alice creates w4 as w_who@1
  - day 1 09:31 alice moves w4 to by_role -> refused NOT_ALLOWED
  - day 1 09:32 lena moves w4 to by_role
  - day 1 09:40 alice creates w5 as w_who@1
  - day 1 09:41 carol moves w5 to by_root -> refused NOT_ALLOWED
  - day 1 09:42 root moves w5 to by_root
  - day 1 09:43 alice creates w6 as w_who@1
  - day 1 09:44 lena moves w6 to by_root
  - day 1 09:50 alice creates w7 as w_who@1
  - day 1 09:51 carol moves w7 to by_staff
until: day 2 00:00
expect:
  - "day 1 09:00 w1: alice created it in open"
  - "day 1 09:03 w1: alice moved it open -> by_requester"
  - "day 1 09:10 w2: alice created it in open"
  - "day 1 09:20 w3: alice created it in open"
  - "day 1 09:22 w3: bob moved it open -> by_owner"
  - "day 1 09:30 w4: alice created it in open"
  - "day 1 09:32 w4: lena moved it open -> by_role"
  - "day 1 09:40 w5: alice created it in open"
  - "day 1 09:42 w5: root moved it open -> by_root"
  - "day 1 09:43 w6: alice created it in open"
  - "day 1 09:44 w6: lena moved it open -> by_root"
  - "day 1 09:50 w7: alice created it in open"
  - "day 1 09:51 w7: carol moved it open -> by_staff"
`,
})

// What a customer sees and does: the Types a container owns, the fields a
// Type shows them, what a request of theirs shares, and that a move open
// to any staff member is not open to them. A Type with no lifecycle has
// no move at all.
var _ = addWitness(witness{
	name: "customers",
	parts: []reading.Part{reading.WhoStaff, reading.Own, reading.CustomerFields, reading.CustomerFieldsNone,
		reading.Shared, reading.NoLifecycle},
	sentences: map[string][]string{
		"w_portal@1": {
			"A w_portal has no lifecycle: it stays in `default`.",
			"A customer may create a w_who or a w_plain inside it, and sees the ones they requested.",
		},
		"w_who@1": {
			"Any staff member who can see it moves it from open to by_staff.",
			"A customer who sees it sees only `f_title`, and sets only that when creating it.",
			"The customer who requested it sees every w_note inside it, and may create one there.",
		},
		"w_plain@1": {
			"A customer who sees it sees none of its fields, only its header.",
		},
	},
	story: `
story: witness - what a customer sees and does
author: written by Claude as a witness of reading item 6
day1: 2026-12-07
people:
  alice: {}
  kim: {customer: true}
  lee: {customer: true}
given:
  - alice creates portal as w_portal@1
steps:
  - day 1 09:00 alice moves portal to default -> refused NO_TRANSITION
  - 'day 1 09:01 kim creates bad as w_who@1 in portal with f_title = "Printer", f_secret = "x" -> refused FIELD_NOT_WRITABLE'
  - 'day 1 09:02 kim creates r1 as w_who@1 in portal with f_title = "Printer"'
  - 'day 1 09:03 alice updates r1 with f_secret = "toner"'
  - day 1 09:04 kim sees r1 fields f_title
  - day 1 09:04 alice sees r1 fields f_secret, f_title
  - day 1 09:05 lee cannot see r1
  - day 1 09:06 kim moves r1 to by_staff -> refused NOT_ALLOWED
  - day 1 09:07 alice creates a1 as w_who@1 in portal
  - day 1 09:07 kim cannot see a1
  - day 1 09:08 kim creates p1 as w_plain@1 in portal
  - 'day 1 09:09 alice updates p1 with f_body = "seen"'
  - day 1 09:10 kim sees p1 with f_body = null
  - 'day 1 09:11 alice creates n1 as w_note@1 in r1 with f_body = "on it"'
  - 'day 1 09:12 kim sees n1 with f_body = "on it"'
  - 'day 1 09:13 kim creates n2 as w_note@1 in r1 with f_body = "thanks"'
  - day 1 09:14 lee cannot see n1
  - 'day 1 09:15 alice creates m1 as w_memo@1 in r1 with f_body = "internal"'
  - day 1 09:15 kim cannot see m1
  - 'day 1 09:16 kim creates m2 as w_memo@1 in r1 with f_body = "x" -> refused NOT_ALLOWED'
  - day 1 09:17 kim moves r1 to by_requester
until: day 2 00:00
expect:
  - "day 1 09:02 r1: kim created it in open inside portal"
  - "day 1 09:03 r1: alice updated it"
  - "day 1 09:07 a1: alice created it in open inside portal"
  - "day 1 09:08 p1: kim created it in default inside portal"
  - "day 1 09:09 p1: alice updated it"
  - "day 1 09:11 n1: alice created it in default inside r1"
  - "day 1 09:13 n2: kim created it in default inside r1"
  - "day 1 09:15 m1: alice created it in default inside r1"
  - "day 1 09:17 r1: kim moved it open -> by_requester"
`,
})

// A container that asks each of its voters, is moved by its votes and
// closes the rest: create for each value, a move of the container that
// skips while its guard does not hold, a move of the Items inside, moves
// no person makes, the container's creator kept out, a voter unique
// among the open votes, and a field only the kernel writes.
var _ = addWitness(witness{
	name: "ballot",
	parts: []reading.Part{reading.Create, reading.CreateForEach, reading.ReactionFrom, reading.MoveContainer,
		reading.ReactionSkip, reading.MoveChildren, reading.WhoNobody, reading.ExceptContainerCreatr,
		reading.FieldUnique, reading.FieldKernel, reading.ConsentCreate, reading.ConsentMove, reading.WhoField},
	sentences: map[string][]string{
		"w_ballot@1": {
			"On entering open from draft, `ask_voters` creates a w_vote@1 inside it, one for each value of `f_voters`, with `f_voter` = each value.",
			"On entering passed, `close_votes` moves the w_votes inside it from pending to closed.",
			"Only a reaction moves it from open to passed, only if a rule over the w_votes inside it holds.",
			"w_vote's reaction `carry` may move it to passed.",
		},
		"w_vote@1": {
			"Only the kernel writes `f_voter`: w_ballot's reaction `ask_voters`, and the move from pending to pending.",
			"No two w_votes in one container, both in pending or yes, hold the same `f_voter`.",
			"Whoever `f_voter` names, but never anyone who took part in creating its container, moves it from pending to yes.",
			"Whoever `f_voter` names moves it from pending to pending, once `f_delegate` is set; the move sets `f_voter` to `f_delegate`.",
			"Only a reaction moves it from pending to closed.",
			"On entering yes, `carry` moves its container from open to passed; if its container's rules refuse the move, it is skipped.",
			"w_ballot's reaction `ask_voters` may create it, setting `f_voter`.",
			"w_ballot's reaction `close_votes` may move it to closed.",
		},
	},
	story: `
story: witness - a ballot and its votes
author: written by Claude as a witness of reading item 6
day1: 2026-12-07
people:
  alice: {}
  bob: {}
  carol: {}
  dave: {}
names:
  va: w_vote in ballot where f_voter = alice
  vb: w_vote in ballot where f_voter = bob
  vc: w_vote in ballot where f_voter = carol
show_skipped: true
steps:
  - day 1 09:00 alice creates ballot as w_ballot@1 with f_voters = [alice, bob, carol]
  - day 1 09:05 alice moves ballot to open
  - day 1 09:06 alice sees va with f_voter = alice
  - day 1 09:10 alice moves va to yes -> refused NOT_ALLOWED
  - day 1 09:11 dave moves va to closed -> refused NOT_ALLOWED
  - day 1 09:13 bob updates vb with f_voter = dave -> refused FIELD_NOT_WRITABLE
  - day 1 09:20 bob moves vb to pending with f_delegate = carol -> refused NOT_UNIQUE
  - day 1 09:21 bob moves vb to pending with f_delegate = dave
  - day 1 09:22 bob moves vb to yes -> refused NOT_ALLOWED
  - day 1 09:25 dave moves vb to yes
  - day 1 09:30 carol moves vc to yes
until: day 2 00:00
expect:
  - "day 1 09:00 ballot: alice created it in draft"
  - "day 1 09:05 ballot: alice moved it draft -> open"
  - "day 1 09:05 ballot: ask_voters for alice applied to va"
  - "day 1 09:05 ballot: ask_voters for alice applied to vb"
  - "day 1 09:05 ballot: ask_voters for alice applied to vc"
  - "day 1 09:05 va: ask_voters for alice created it in pending inside ballot: f_voter = alice"
  - "day 1 09:05 vb: ask_voters for alice created it in pending inside ballot: f_voter = bob"
  - "day 1 09:05 vc: ask_voters for alice created it in pending inside ballot: f_voter = carol"
  - "day 1 09:21 vb: bob moved it pending -> pending: f_voter = dave"
  - "day 1 09:25 vb: dave moved it pending -> yes"
  - "day 1 09:25 vb: carry for dave skipped: guard_false"
  - "day 1 09:30 vc: carol moved it pending -> yes"
  - "day 1 09:30 ballot: carry for carol moved it open -> passed"
  - "day 1 09:30 vc: carry for carol applied to ballot"
  - "day 1 09:30 ballot: close_votes for carol applied to va"
  - "day 1 09:30 va: close_votes for carol moved it pending -> closed"
`,
})
