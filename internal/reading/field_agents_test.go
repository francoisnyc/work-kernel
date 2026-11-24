// Tests for the readings of fields closed to agents by default: who writes
// what says, last, which fields an agent writes (field_agents and its
// clauses), and a field only agents write says so (field_agents_only).
package reading_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/contracts"
	"github.com/francoisnyc/work-kernel/internal/reading"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// whoWrites is the "Who writes what" section of a Type with these fields
// (YAML list items) and extra top-level lines.
func whoWrites(t *testing.T, fields, extra string) []reading.Sentence {
	t.Helper()
	src := "id: fa\nversion: 1\ninitial_state: open\nfields:\n" + fields +
		"states:\n  - {id: open, category: todo}\n  - {id: done, category: done}\n" +
		"transitions:\n  - {from: open, to: done}\n" + extra
	ty, err := typedef.Parse([]byte(src))
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, src)
	}
	for _, sec := range reading.Read(ty, contracts.Default()).Sections {
		if sec.Title == "Who writes what" {
			return sec.Sentences
		}
	}
	return nil
}

func TestReading_FieldAgents(t *testing.T) {
	cases := []struct {
		name, fields, extra string
		want                []string // every sentence of the section, in order
		parts               []reading.Part
	}{
		{"no field open to agents",
			"  - {id: f_a, type: string}\n  - {id: f_b, type: int}\n", "",
			[]string{"No agent writes its fields."},
			[]reading.Part{reading.FieldAgents}},
		{"one field open",
			"  - {id: f_a, type: string, writable_by: [people, agents]}\n  - {id: f_b, type: int}\n", "",
			[]string{"An agent writes only `f_a`."},
			[]reading.Part{reading.FieldAgents}},
		{"two fields open",
			"  - {id: f_a, type: string, writable_by: [people, agents]}\n  - {id: f_b, type: int, writable_by: [agents, people]}\n", "",
			[]string{"An agent writes only `f_a` and `f_b`."},
			[]reading.Part{reading.FieldAgents}},
		{"a role's field, none open",
			"  - {id: f_a, type: string}\n  - {id: f_r, type: int, writable_by: ['role:lead']}\n", "",
			[]string{"Only holders of lead write `f_r`.",
				"No agent writes its fields, except one that names a role the agent holds."},
			[]reading.Part{reading.FieldAgents, reading.FieldAgentsRoles}},
		{"a role's field, one open",
			"  - {id: f_a, type: string, writable_by: [people, agents]}\n  - {id: f_r, type: int, writable_by: [people, 'role:lead']}\n", "",
			[]string{"An agent writes only `f_a`, and a field that names a role the agent holds."},
			[]reading.Part{reading.FieldAgents, reading.FieldAgentsRoles}},
		{"customer fields",
			"  - {id: f_a, type: string}\n", "customer_fields: [f_a]\n",
			[]string{"No agent writes its fields; an agent acting for a customer sets only what the customer may."},
			[]reading.Part{reading.FieldAgents, reading.FieldAgentsCustomers}},
		{"a field only agents write",
			"  - {id: f_a, type: string}\n  - {id: f_t, type: string, writable_by: [agents]}\n", "",
			[]string{"Only agents write `f_t`.", "An agent writes only `f_t`."},
			[]reading.Part{reading.FieldAgentsOnly, reading.FieldAgents}},
		{"a field only agents and a role write",
			"  - {id: f_t, type: string, writable_by: [agents, 'role:lead']}\n", "",
			[]string{"Only agents and holders of lead write `f_t`.", "An agent writes only `f_t`."},
			[]reading.Part{reading.FieldAgentsOnly, reading.FieldRoles, reading.FieldAgents}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := whoWrites(t, c.fields, c.extra)
			var texts []string
			var parts []reading.Part
			for _, s := range got {
				texts = append(texts, s.Text)
				parts = append(parts, s.Parts...)
			}
			if !slices.Equal(texts, c.want) {
				t.Errorf("who writes what =\n  %s\nwant\n  %s", strings.Join(texts, "\n  "), strings.Join(c.want, "\n  "))
			}
			for _, p := range c.parts {
				if !slices.Contains(parts, p) {
					t.Errorf("the sentences carry %v, want %s among them", parts, p)
				}
			}
		})
	}
}

// A Type with no fields says nothing of agents.
func TestReading_FieldAgents_NoFields(t *testing.T) {
	ty, err := typedef.Parse([]byte("id: fa\nversion: 1\n"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(reading.Read(ty, contracts.Default()).Text(), "agent writes") {
		t.Errorf("a Type with no fields speaks of agents:\n%s", reading.Read(ty, contracts.Default()).Text())
	}
}
