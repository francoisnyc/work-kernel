package reading_test

import "github.com/francoisnyc/work-kernel/internal/reading"

// Who migrates an Item to the latest version (track 1, item 6, step 2).
// w_bill@2 names the role w_bill_admin and root. adm (the role) and root
// each migrate a bill of version 1; eve, who holds no role, is refused
// with the API's reason. A bill already on version 2 cannot be migrated
// again: it is refused as not migratable. The timeline shows each migrate
// with both versions.
var _ = addWitness(witness{
	name:  "who migrates an Item to the latest version",
	parts: []reading.Part{reading.MigrateWho},
	sentences: map[string][]string{
		"w_bill@2": {"While this is the latest version of w_bill, anyone with the role w_bill_admin, or `user:root`, may migrate an open w_bill of an older version to it, once it fits."},
	},
	story: `
story: witness - who migrates an Item to the latest version
author: written by Claude as a witness of track 1 item 6, step 2 (Phase B)
day1: 2026-12-07
people:
  ana: {}
  adm: {roles: [w_bill_admin]}
  eve: {}
  root: {}
steps:
  - day 1 09:00 ana creates b1 as w_bill@1
  - day 1 09:01 ana creates b2 as w_bill@1
  - day 1 09:02 eve migrates b1 to w_bill@2 -> refused NOT_ALLOWED
  - day 1 09:03 ana migrates b1 to w_bill@2 -> refused NOT_ALLOWED
  - day 1 09:04 adm migrates b1 to w_bill@2
  - day 1 09:05 adm migrates b1 to w_bill@2 -> refused NOT_MIGRATABLE
  - day 1 09:06 root migrates b2 to w_bill@2
until: day 2 00:00
expect:
  - "day 1 09:00 b1: ana created it in default"
  - "day 1 09:01 b2: ana created it in default"
  - "day 1 09:04 b1: adm migrated it w_bill@1 -> w_bill@2"
  - "day 1 09:06 b2: root migrated it w_bill@1 -> w_bill@2"
`,
})
