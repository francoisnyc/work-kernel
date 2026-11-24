// Package story runs stories: walkthroughs of Types written as data, run in
// simulated time against the real kernel (Ventoux item 5).
//
// A story names the people and their roles, a fake for every action its
// Types' hooks call, a timeline of steps ("day 2 10:00 bob moves request to
// approved"), and the WHOLE expected timeline of what follows: every event
// on every Item of the story, and every side effect a fake was asked for.
// Anything that happens and is not expected fails the story, and so does
// anything expected that does not happen. Stories are written or approved
// by people, never generated alongside the Type they test; nothing here
// ever writes expected lines into a story.
//
// A story is a YAML file (stories/<domain>/<name>.yaml). Its steps, fake
// rules, name rules and expected lines are one-line strings in the small
// closed grammar below. Parse reads the file into a Story; Check holds it
// against the Types and the action contracts; Run runs it and returns what
// happened as data; Compare holds the result against the expected lines.
// Item 6's witnesses build a Story in Go and use the same four.
//
// # The file
//
//	story: an approved request takes its days from the balance, once
//	author: <who wrote or approved it>             # required
//	day1: 2026-12-07                               # day 1; every time is UTC
//	observer: <person>                             # whose reads make the timeline; default: observer
//	people:
//	  alice: {}
//	  hana: {roles: [hr_admin]}                    # roles held in the story's namespace
//	  kim: {customer: true}                        # customer:kim; customers hold no roles
//	  bot: {agent: true}                           # agent:bot; it may hold roles, never be a customer
//	names:                                         # Items the kernel creates
//	  first: sla_goal in inc where f_metric = first_response
//	hide_values:                                   # values left out of lines (the field stays)
//	  sla_goal: [f_week, f_days_off, f_zone]
//	show_skipped: false                            # reaction_skipped lines; default false
//	fakes:
//	  hr.manager_of@1:
//	    - "when createdBy = alice: correct manager = bob"
//	  email.notify@1:
//	    - ok
//	given:                                         # setup at day 1 00:00; its own events are not compared
//	  - hana creates balance as leave_balance@1 with f_owner = alice, f_allowance = 20
//	steps:
//	  - day 1 09:00 alice creates request as vacation_request@3 with f_start = day 15, f_end = day 19, f_days = 5, f_leave_type = annual
//	until: day 10 00:00                            # default: a year after the last step
//	expect:
//	  - "day 1 09:00 request: alice created it in draft"
//
// A person is user:<name>, customer:<name> with customer: true, or
// agent:<name> with agent: true. An agent acts alone ("bot updates ...")
// or for a person of the story ("bot for kim creates ..."), in a session
// the harness starts over the whole namespace with every power its user
// may grant (for a customer: read, create, comment, every move). The
// default observer is the person observer (user:observer), a staff user
// with no roles, added when no person has that name. Each run has a
// namespace of its own, so runs never see each other's Items.
//
// # Times and values
//
//	time  = "day" N HH:MM[:SS]          day 1 is day1; steps are in time order
//	value = INT | true | false | null
//	      | "quoted text"
//	      | word                       an enum option, a person (for a principal), or text
//	      | day N                      a date
//	      | day N HH:MM[:SS]           an instant
//	      | [value, value, ...]        a multi-value field
//	values = field = value, field = value, ...
//
// A value takes its meaning from the field (or the contract input or
// output) it is given to; Check refuses one that does not fit.
//
// # Steps
//
//	person = NAME | AGENT for NAME                   an agent acting for a person
//	time person creates ITEM as TYPE@V [in ITEM] [with values]
//	time person updates ITEM with values             (field = null clears it)
//	time person moves ITEM to STATE [with values]
//	time person links ITEM LINK ITEM
//	time person unlinks ITEM LINK ITEM
//	time person places ITEM in ITEM                  moves it into a container (Update with a move)
//	time person places ITEM at the top level
//	time person narrows ITEM to person, person, ...  sets its readers (Update with a policy change)
//	time person sees ITEM with values                each field holds that value (null: unset or hidden)
//	                                                 or ROLLUP.KEY = N: a rollup's count for one
//	                                                 category or state (0: it counts nothing)
//	time person sees ITEM fields field, field, ...   exactly the fields set and visible to the person
//	time person cannot see ITEM
//
// A place that would widen who can see is sent unconfirmed, so it is
// refused WIDEN_NOT_CONFIRMED. A step may name an Item the kernel created
// by its name rule, before any read named it.
//
// A write step may end in "-> refused REASON", REASON being the API's
// refusal reason (NOT_ALLOWED, GUARD_FAILED, ...): the write must be
// refused with it. A write refused without it fails the story, and so does
// one that was expected to be refused and was not. A move with a check is
// asked with no wait: its outcome is in the timeline. Given steps have no
// time. Reads go through the item service's public reads, so through
// policy.Project.
//
// # Fakes
//
// Every action a hook may call, of every Type the story creates (and the
// Types those create by reaction), has a fake. Its rules are tried in
// order; the first that matches answers; a call that none matches fails the
// story.
//
//	rule   = [when input = value, ...] [from time] [until time]: answer
//	       | answer
//	answer = allow | deny REASON | correct values      (a check)
//	       | ok [values]                               (an enrich or an action)
//	       | fail transient | invalid | unauthorized | key_conflict
//
// from is inclusive, until exclusive. Check holds every answer to the
// action's contract (its kinds, its outputs and their types, its reasons),
// and the worker holds it again when the fake answers.
//
// # Names
//
// An Item a step creates has the step's name. An Item the kernel creates is
// named by the first rule that matches it, by its Type, its container and
// one value of its creation; a second match of a rule is name#2, and so
// on. An Item no rule names is TYPE#N in the order of creation.
//
//	rule = TYPE [in ITEM] [where field = value]
//
// # Expected lines
//
//	line = time ITEM: sentence
//	     | between time and time ITEM: sentence
//
// The second form, a window, matches one line seen at any instant from
// the first time to the second, both included. It is only for timing the
// kernel does not promise: when a hook gives up within its budget depends
// on its retries' rhythm, which a story should not have to know.
//
// A sentence says who did what. Who is a person, timer, a hook's ID or a
// reaction's ID, followed by "for PERSON" when it acted for someone else.
//
//	create               who created it in STATE[ inside ITEM][: values]
//	transition           who moved it FROM -> TO[: values][; linked LINK to ITEM]
//	transition_requested who asked FROM -> TO[: values]
//	transition_denied    who denied FROM -> TO: REASON
//	transition_failed    who failed FROM -> TO: REASON
//	update               who updated it[ (TIMER)]: values
//	link_added           who linked LINK to ITEM
//	link_removed         who unlinked LINK from ITEM
//	hook_ran             who ran[ (TIMER[ run N])]
//	hook_failed          who failed[ (TIMER[ run N])]: REASON
//	reaction_applied     who applied to ITEM
//	reaction_failed      who failed on ITEM: REASON
//	reaction_skipped     who skipped: REASON              (with show_skipped only)
//	a container move     who moved it from ITEM to ITEM    (the top level is "the top level")
//	readers              who narrowed it to PRINCIPAL, ... / who gave it its container's readers
//	a side effect        CONNECTOR: OPERATION for ITEM (HOOK): inputs[ -> outputs]
//
// reaction_skipped lines are left out unless show_skipped is true: every
// move of an Item writes one for each of its reactions whose condition did
// not hold, which would swamp a story. Nothing is lost: a reaction that
// should have applied and was skipped leaves its effect's line missing, and
// the story fails on that.
//
// Values are field = value, sorted by field ID: an enum option bare, text
// quoted, a person by name, a date as day N, an instant as day N HH:MM[:SS],
// a list in brackets, a cleared field as null, a reaction's addition as
// field +N = value, a hidden value as field = .... A step's own event leaves
// out the values the step sent. An Item the observer cannot see is
// (hidden).
//
// # Comparing
//
// Lines are grouped by instant, to the second. The order of instants is
// strict; within an instant, lines compare as a multiset, because Items'
// workflows work concurrently. A window matches one line within it, once
// every line at an exact instant has been matched. Runs of spaces count
// as one.
//
// # How a story runs, and where that differs from production
//
// The real Item workflow runs in Temporal's in-process test environment,
// whose clock skips ahead whenever nothing is working. Its activities run
// against a real item service on Postgres, whose clock is the story's
// (itemservice.Options.Clock), with every connector replaced by the
// story's fakes (hooks.Connectors.WithConnector). A driver workflow,
// harness code, starts each Item's workflow as its child: it stands in for
// the server's Update-With-Start. The real relay delivers the outbox from
// inside a settle step that the driver runs after every write: while it
// runs, time cannot move, so everything a step causes happens at the step's
// instant. scripts/scenario.sh on the real stack still covers the real
// path: the server, Update-With-Start, the relay's own process.
package story
