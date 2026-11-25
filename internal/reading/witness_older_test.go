package reading_test

import "github.com/francoisnyc/work-kernel/internal/reading"

// Which versions may be created (track 1, item 6, step 1). w_form@3 is the
// latest form and keeps version 2 open: a form@2 and a form@3 are created,
// a form@1 is refused with the API's reason for a version that cannot be
// created. w_slip@2 keeps every older slip open. w_stub@2 says nothing of
// older versions, so a stub@1 is closed: the kernel's rule, with no
// sentence of its own.
var _ = addWitness(witness{
	name:  "older versions that may still be created",
	parts: []reading.Part{reading.StartOlder, reading.StartOlderAll},
	sentences: map[string][]string{
		"w_form@3": {"While this is the latest version of w_form, a w_form of version 2 may still be created; one of any other older version may not."},
		"w_slip@2": {"While this is the latest version of w_slip, a w_slip of any older version may still be created."},
	},
	story: `
story: witness - older versions that may still be created
author: written by Claude as a witness of track 1 item 6, step 1 (Phase B)
day1: 2026-12-07
people:
  eve: {}
steps:
  - day 1 09:00 eve creates f1 as w_form@1 -> refused INVALID_REQUEST
  - day 1 09:01 eve creates f2 as w_form@2
  - day 1 09:02 eve creates f3 as w_form@3
  - day 1 09:03 eve creates s1 as w_slip@1
  - day 1 09:04 eve creates s2 as w_slip@2
  - day 1 09:05 eve creates t1 as w_stub@1 -> refused INVALID_REQUEST
  - day 1 09:06 eve creates t2 as w_stub@2
until: day 2 00:00
expect:
  - "day 1 09:01 f2: eve created it in default"
  - "day 1 09:02 f3: eve created it in default"
  - "day 1 09:03 s1: eve created it in default"
  - "day 1 09:04 s2: eve created it in default"
  - "day 1 09:06 t2: eve created it in default"
`,
})
