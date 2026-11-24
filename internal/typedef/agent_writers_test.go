// Tests for fields closed to agents by default: a field's writable_by list
// takes people and agents besides role:<name>, in any order and any mix.
// Absent means [people]. A link's writable_by takes neither (links are not
// part of this step). The stored definition keeps them.
package typedef_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// writers is a Type whose field f_x has the given writable_by (YAML flow).
func writers(wb string) string {
	return "id: wr\nversion: 1\nfields:\n  - {id: f_x, type: string, writable_by: " + wb + "}\n"
}

func TestWritableBy_PeopleAndAgents_Parse(t *testing.T) {
	cases := []struct {
		wb             string
		people, agents bool
		roles          []string
	}{
		{"[people]", true, false, nil},
		{"[agents]", false, true, nil},
		{"[people, agents]", true, true, nil},
		{"[agents, people]", true, true, nil},
		{"[agents, 'role:lead']", false, true, []string{"lead"}},
		{"['role:lead', people]", true, false, []string{"lead"}},
	}
	for _, c := range cases {
		ty := mustParse(t, writers(c.wb))
		f, _ := ty.Field("f_x")
		if f.WritableByPeople != c.people || f.WritableByAgents != c.agents ||
			strings.Join(f.WritableByRoles, ",") != strings.Join(c.roles, ",") || f.WritableBy != typedef.WritableByAnyone {
			t.Errorf("writable_by %s = people %v, agents %v, roles %v, by %q; want %v, %v, %v",
				c.wb, f.WritableByPeople, f.WritableByAgents, f.WritableByRoles, f.WritableBy, c.people, c.agents, c.roles)
		}
		// The stored definition keeps them.
		data, err := json.Marshal(ty) // as the item service stores it
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		back, err := typedef.FromJSON(data)
		if err != nil {
			t.Fatalf("FromJSON(%s): %v", data, err)
		}
		if g, _ := back.Field("f_x"); g.WritableByPeople != c.people || g.WritableByAgents != c.agents {
			t.Errorf("writable_by %s after the round trip: people %v, agents %v", c.wb, g.WritableByPeople, g.WritableByAgents)
		}
	}
}

// A field without writable_by stores nothing new: every registered version
// keeps its stored definition byte for byte.
func TestWritableBy_AbsentStoresNothingNew(t *testing.T) {
	data, err := json.Marshal(mustParse(t, "id: wr\nversion: 1\nfields:\n  - {id: f_x, type: string}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if s := string(data); strings.Contains(s, "people") || strings.Contains(s, "agents") {
		t.Errorf("stored definition %s names people or agents", s)
	}
}

func TestWritableBy_PeopleAndAgents_Problems(t *testing.T) {
	cases := []struct {
		name, src, path string
		code            typedef.Code
		says            []string
	}{
		{"people twice", writers("[people, people]"), "fields[0].writable_by[1]", typedef.CodeDuplicate, []string{"people"}},
		{"agents twice", writers("[agents, 'role:lead', agents]"), "fields[0].writable_by[2]", typedef.CodeDuplicate, []string{"agents"}},
		{"an entry it does not know", writers("[robots]"), "fields[0].writable_by[0]", typedef.CodeInvalidValue,
			[]string{"robots", "people", "agents", "role:<name>"}},
		{"kernel in a list", writers("[kernel, agents]"), "fields[0].writable_by[0]", typedef.CodeInvalidValue, []string{"kernel"}},
		{"a link opened to agents", "id: wr\nversion: 1\nlinks:\n  - {name: rel, inverse: rels, targets: [wr], writable_by: [agents]}\n",
			"links[0].writable_by[0]", typedef.CodeInvalidValue, []string{"agents"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := typedef.Parse([]byte(c.src))
			ps := problemsOf(t, err)
			if len(ps) != 1 {
				t.Fatalf("problems = %v, want one", ps)
			}
			p := ps[0]
			if p.Code != c.code || p.Path != c.path {
				t.Errorf("problem = %s at %s, want %s at %s (%s)", p.Code, p.Path, c.code, c.path, p.Message)
			}
			for _, s := range c.says {
				if !strings.Contains(p.Message, s) {
					t.Errorf("message %q does not say %q", p.Message, s)
				}
			}
		})
	}
}

// The published JSON Schema says what a writable_by list takes.
func TestWritableBy_SchemaSaysPeopleAndAgents(t *testing.T) {
	data, err := os.ReadFile("../../schema/type.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Defs map[string]struct {
			Description string `json:"description"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	d := schema.Defs["writableBy"].Description
	for _, s := range []string{"people", "agents", "role:<name>", "kernel"} {
		if !strings.Contains(d, s) {
			t.Errorf("writableBy's description %q does not say %q", d, s)
		}
	}
}
