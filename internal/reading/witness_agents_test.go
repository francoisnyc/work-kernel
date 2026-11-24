package reading_test

import "github.com/francoisnyc/work-kernel/internal/reading"

// Fields closed to agents by default: a field with no writable_by is
// written by people and not by agents, alone or acting for someone; one
// opened to agents is written by both; one only agents write refuses
// people; a role's field is written by an agent holding the role. An agent
// acting for a customer sets what the customer may: the customer fields,
// when creating. The story's agents (people with agent: true) act alone,
// or for a person with "bot for ana".
var _ = addWitness(witness{
	name: "fields closed to agents",
	parts: []reading.Part{reading.FieldAgents, reading.FieldAgentsRoles, reading.FieldAgentsCustomers,
		reading.FieldAgentsOnly, reading.FieldRoles},
	sentences: map[string][]string{
		"w_bot@1": {
			"Only agents write `f_tag`.",
			"Only holders of grader write `f_grade`.",
			"An agent writes only `f_score` and `f_tag`, and a field that names a role the agent holds; " +
				"an agent acting for a customer sets only what the customer may.",
		},
	},
	story: `
story: witness - fields closed to agents by default
author: written by Claude as a witness of fields closed to agents (Phase B)
day1: 2026-12-07
people:
  ana: {}
  gil: {roles: [grader]}
  kim: {customer: true}
  bot: {agent: true}
  marker: {agent: true, roles: [grader]}
given:
  - ana creates desk as w_botdesk@1
steps:
  - 'day 1 09:00 ana creates b1 as w_bot@1 in desk with f_note = "ana"'
  - 'day 1 09:01 bot updates b1 with f_note = "bot" -> refused FIELD_NOT_WRITABLE'
  - 'day 1 09:02 bot for ana updates b1 with f_note = "bot" -> refused FIELD_NOT_WRITABLE'
  - day 1 09:03 bot updates b1 with f_score = 3
  - day 1 09:04 bot for ana updates b1 with f_score = 4
  - 'day 1 09:05 bot updates b1 with f_tag = "triaged"'
  - 'day 1 09:06 ana updates b1 with f_tag = "ana" -> refused FIELD_NOT_WRITABLE'
  - 'day 1 09:07 bot for ana updates b1 with f_tag = "bot" -> refused FIELD_NOT_WRITABLE'
  - day 1 09:08 bot updates b1 with f_grade = 1 -> refused FIELD_NOT_WRITABLE
  - day 1 09:09 marker updates b1 with f_grade = 2
  - day 1 09:10 gil updates b1 with f_grade = 3
  - 'day 1 09:11 ana sees b1 with f_note = "ana", f_score = 4, f_tag = "triaged", f_grade = 3'
  - 'day 1 09:12 bot for kim creates k1 as w_bot@1 in desk with f_note = "from the portal"'
  - day 1 09:13 bot for kim creates k2 as w_bot@1 in desk with f_score = 1 -> refused FIELD_NOT_WRITABLE
until: day 2 00:00
expect:
  - "day 1 09:00 b1: ana created it in open inside desk"
  - "day 1 09:03 b1: bot updated it"
  - "day 1 09:04 b1: bot for ana updated it"
  - "day 1 09:05 b1: bot updated it"
  - "day 1 09:09 b1: marker updated it"
  - "day 1 09:10 b1: gil updated it"
  - "day 1 09:12 k1: bot for kim created it in open inside desk"
`,
})
