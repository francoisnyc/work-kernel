package reading_test

import "github.com/francoisnyc/work-kernel/internal/reading"

// A case and its goals (the shape of incident and sla_goal, without
// working time): the case creates its goals, reading a value its desk's
// rules let a create reaction read; a goal shows its due instant on the
// case, only when its condition holds; the case pauses and resumes the
// goals its condition picks; what is created inside the case, and by whom,
// moves the case or its goals (the requester's own message while a goal
// runs meets nothing, m1b; someone else's message in a waiting case
// brings nothing back, m3); a goal tells the case's owner, whom the case
// offers. No person moves a goal.
var _ = addWitness(witness{
	name: "case",
	parts: []reading.Part{reading.Create, reading.Set, reading.ReactionWhen, reading.MoveChildren,
		reading.MoveChildrenWhen, reading.ReactionFrom, reading.CreatedInsideSelf, reading.CreatedInsideChildren,
		reading.CreatedByCreator, reading.CreatedByOther, reading.CreatedByAnyone, reading.Offer, reading.Readable,
		reading.ConsentCreate, reading.ConsentSet, reading.ConsentMove, reading.ConsentWildcard, reading.WhoNobody,
		reading.Action},
	sentences: map[string][]string{
		"w_case@1": {
			"On entering new, `start_reply` creates a w_goal@1 inside it, with `f_due_at` = `f_target_at`, " +
				"`f_hours` = `f_hours` of its container's `rules`, `f_kind` = reply, and `f_weight` computed from `f_size`.",
			"On entering new, `start_fix` creates a w_goal@1 inside it, with `f_due_at` = `f_target_at` and `f_kind` = fix.",
			"On entering waiting, `pause_goals` moves the w_goals inside it whose `f_kind` is reply from running to paused.",
			"On entering working from waiting, `resume_goals` moves the w_goals inside it from paused to running.",
			"When a person other than the requester creates a w_msg inside it, `answered` moves the w_goals inside it whose `f_kind` is reply from running to met.",
			"When the requester creates a w_msg inside it while it is in waiting, `back` moves it to working.",
			"When anyone creates a w_flag inside it while it is in new, `flagged` moves it to working.",
			"Items inside it may read `f_owner` as `contact`.",
			"w_goal's reaction `show` may set `f_due`.",
		},
		"w_goal@1": {
			"On entering running, if `f_kind` is reply, `show` sets `f_due` on its container to `f_due_at`.",
			"Only a reaction moves it from paused to running.",
			"On entering met, `tell_contact` calls `email.notify@1` with to = its container's `contact`, once per entry, even if it has moved on.",
			"Any Type's reaction `start_reply`, from its container, may create it, setting `f_due_at`, `f_hours`, `f_kind` and `f_weight`.",
			"Any Type's reaction `pause_goals`, from its container, may move it to paused.",
			"w_case's reaction `resume_goals` may move it to running.",
		},
		"w_rules@1": {
			"Another Type's create reaction may read `f_hours` from it.",
		},
	},
	story: `
story: witness - a case and its goals
author: written by Claude as a witness of reading item 6
day1: 2026-12-07
people:
  alice: {}
  bob: {}
names:
  reply: w_goal in case where f_kind = reply
  fix: w_goal in case where f_kind = fix
  reply2: w_goal in case2 where f_kind = reply
  fix2: w_goal in case2 where f_kind = fix
fakes:
  email.notify@1:
    - ok
given:
  - alice creates rules as w_rules@1 with f_hours = 8
  - alice creates desk as w_desk@1
  - alice links desk rules rules
steps:
  - day 1 09:00 alice creates case as w_case@1 in desk with f_size = large, f_owner = bob, f_target_at = day 2 09:00
  - day 1 09:00 alice sees case with f_due = day 2 09:00
  - day 1 09:10 alice moves case to working
  - day 1 09:20 alice moves case to waiting
  - day 1 09:21 alice moves reply to running -> refused NOT_ALLOWED
  - day 1 09:30 alice creates m1 as w_msg@1 in case
  - day 1 09:35 alice creates m1b as w_msg@1 in case
  - day 1 09:40 bob creates m2 as w_msg@1 in case
  - day 1 09:50 alice moves case to waiting
  - day 1 09:55 bob creates m3 as w_msg@1 in case
  - day 1 10:00 alice creates case2 as w_case@1 in desk with f_size = small, f_target_at = day 3 09:00
  - day 1 10:05 bob creates flag as w_flag@1 in case2
until: day 2 00:00
expect:
  - "day 1 09:00 case: alice created it in new inside desk"
  - "day 1 09:00 case: start_reply for alice applied to reply"
  - "day 1 09:00 case: start_fix for alice applied to fix"
  - "day 1 09:00 reply: start_reply for alice created it in running inside case: f_due_at = day 2 09:00, f_hours = 8, f_kind = reply, f_weight = 2"
  - "day 1 09:00 fix: start_fix for alice created it in running inside case: f_due_at = day 2 09:00, f_kind = fix"
  - "day 1 09:00 reply: show for alice applied to case"
  - "day 1 09:00 case: show for alice updated it: f_due = day 2 09:00"
  - "day 1 09:10 case: alice moved it new -> working"
  - "day 1 09:20 case: alice moved it working -> waiting"
  - "day 1 09:20 case: pause_goals for alice applied to reply"
  - "day 1 09:20 reply: pause_goals for alice moved it running -> paused"
  - "day 1 09:30 m1: alice created it in default inside case"
  - "day 1 09:30 case: back for alice moved it waiting -> working"
  - "day 1 09:30 case: back for alice applied to case"
  - "day 1 09:30 case: resume_goals for alice applied to reply"
  - "day 1 09:30 reply: resume_goals for alice moved it paused -> running"
  - "day 1 09:30 reply: show for alice applied to case"
  - "day 1 09:30 case: show for alice updated it: f_due = day 2 09:00"
  - "day 1 09:35 m1b: alice created it in default inside case"
  - "day 1 09:40 m2: bob created it in default inside case"
  - "day 1 09:40 case: answered for bob applied to reply"
  - "day 1 09:40 reply: answered for bob moved it running -> met"
  - "day 1 09:40 reply: tell_contact for bob ran"
  - "day 1 09:40 email: notify for reply (tell_contact): to = bob"
  - "day 1 09:50 case: alice moved it working -> waiting"
  - "day 1 09:55 m3: bob created it in default inside case"
  - "day 1 10:00 case2: alice created it in new inside desk"
  - "day 1 10:00 case2: start_reply for alice applied to reply2"
  - "day 1 10:00 case2: start_fix for alice applied to fix2"
  - "day 1 10:00 reply2: start_reply for alice created it in running inside case2: f_due_at = day 3 09:00, f_hours = 8, f_kind = reply, f_weight = 1"
  - "day 1 10:00 fix2: start_fix for alice created it in running inside case2: f_due_at = day 3 09:00, f_kind = fix"
  - "day 1 10:00 reply2: show for alice applied to case2"
  - "day 1 10:00 case2: show for alice updated it: f_due = day 3 09:00"
  - "day 1 10:05 flag: bob created it in default inside case2"
  - "day 1 10:05 case2: flagged for bob moved it new -> working"
  - "day 1 10:05 case2: flagged for bob applied to case2"
`,
})
