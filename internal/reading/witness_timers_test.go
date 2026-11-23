package reading_test

import "github.com/francoisnyc/work-kernel/internal/reading"

// Every kind of timer: a state timer (and that it does nothing once the
// Item has left), a timer at a field's instant (at once when the instant
// has passed before the Item entered), an acting timer that runs at most
// so many times, and an acting timer at an instant that sets a field to
// that instant, not to when it runs (t4 runs at 10:25 and sets 08:00),
// and runs only while its condition holds (t3 re-enters after its instant
// with f_seen_at set, and nothing runs; t4, entering the same way with it
// unset, runs). Only the timer makes the timer's moves.
var _ = addWitness(witness{
	name: "timers",
	parts: []reading.Part{reading.Timer, reading.AtTimer, reading.Acting, reading.ActingEvery, reading.ActingAt,
		reading.ActingAtWhen, reading.ActingAtSets, reading.WhoTimer, reading.FieldKernel},
	sentences: map[string][]string{"w_timer@1": {
		"Only the kernel writes `f_seen_at`: the acting timer `mark`.",
		"48 hours after it enters open, if it is still there, the timer moves it to expired.",
		"24 hours after it enters open, then every 12 hours, `nudge_who` calls `email.notify@1` with to = `f_who`, at most 2 times while it stays there.",
		"Only the timer moves it from open to expired.",
		"When it is in running and the instant in `f_end_at` has passed, the timer moves it to ended.",
		"Only the timer moves it from running to ended.",
		"When it is in watching and the instant in `f_end_at` has passed, if `f_seen_at` is unset, `mark` sets `f_seen_at` to that instant and `tell_who` calls `email.notify@1` with to = `f_who`.",
	}},
	story: `
story: witness - every kind of timer
author: written by Claude as a witness of reading item 6
day1: 2026-12-07
people:
  alice: {}
  bob: {}
fakes:
  email.notify@1:
    - ok
steps:
  - day 1 09:00 alice creates t1 as w_timer@1 with f_who = bob
  - day 1 09:00 alice creates t2 as w_timer@1 with f_who = bob
  - day 1 09:00 alice creates t3 as w_timer@1 with f_who = bob, f_end_at = day 2 06:00
  - day 1 09:30 alice moves t2 to running with f_end_at = day 2 12:00
  - day 1 10:00 alice moves t3 to watching
  - day 1 10:20 alice creates t4 as w_timer@1 with f_who = bob, f_end_at = day 1 08:00
  - day 1 10:25 alice moves t4 to watching
  - day 1 10:30 bob moves t1 to expired -> refused NOT_ALLOWED
  - day 1 10:40 bob moves t2 to ended -> refused NOT_ALLOWED
  - day 1 10:50 alice creates t5 as w_timer@1
  - day 1 10:55 alice moves t5 to running with f_end_at = day 1 08:00
  - day 2 07:00 alice moves t3 to paused
  - day 2 07:10 alice moves t3 to watching
  - day 2 07:10 alice sees t3 with f_seen_at = day 2 06:00
until: day 5 00:00
expect:
  - "day 1 09:00 t1: alice created it in open"
  - "day 1 09:00 t2: alice created it in open"
  - "day 1 09:00 t3: alice created it in open"
  - "day 1 09:30 t2: alice moved it open -> running"
  - "day 1 10:00 t3: alice moved it open -> watching"
  - "day 1 10:20 t4: alice created it in open"
  - "day 1 10:25 t4: alice moved it open -> watching"
  - "day 1 10:25 t4: timer for alice updated it (mark): f_seen_at = day 1 08:00"
  - "day 1 10:25 t4: tell_who for alice ran (mark)"
  - "day 1 10:25 email: notify for t4 (tell_who): to = bob"
  - "day 1 10:50 t5: alice created it in open"
  - "day 1 10:55 t5: alice moved it open -> running"
  - "day 1 10:55 t5: timer for alice moved it running -> ended"
  - "day 2 06:00 t3: timer for alice updated it (mark): f_seen_at = day 2 06:00"
  - "day 2 06:00 t3: tell_who for alice ran (mark)"
  - "day 2 06:00 email: notify for t3 (tell_who): to = bob"
  - "day 2 07:00 t3: alice moved it watching -> paused"
  - "day 2 07:10 t3: alice moved it paused -> watching"
  - "day 2 09:00 t1: nudge_who for alice ran (nudge run 1)"
  - "day 2 09:00 email: notify for t1 (nudge_who): to = bob"
  - "day 2 12:00 t2: timer for alice moved it running -> ended"
  - "day 2 21:00 t1: nudge_who for alice ran (nudge run 2)"
  - "day 2 21:00 email: notify for t1 (nudge_who): to = bob"
  - "day 3 09:00 t1: timer for alice moved it open -> expired"
`,
})
