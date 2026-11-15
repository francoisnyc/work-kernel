// Tests for Step 6, part 2: guards are CEL expressions (cel-go), compiled
// when a Type is loaded. The spike's own grammar ("is set") is gone. Every
// field is a variable named by its ID, typed by the schema and nullable:
// an unset field is null, so "is set" is written `f_x != null`.
package typedef_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/typedef"
)

const celType = `
id: doc
version: 1
initial_state: draft
fields:
  - {id: f_owner, type: string}
  - {id: f_score, type: int}
  - {id: f_ok,    type: bool}
  - {id: f_kind,  type: enum, options: [memo, report]}
states:
  - {id: draft, category: backlog}
  - {id: published, category: done}
transitions:
  - {from: draft, to: published, guard: 'GUARD'}
`

func withGuard(guard string) []byte {
	return []byte(strings.Replace(celType, "GUARD", guard, 1))
}

// Each of these is a valid CEL guard over the Type's fields.
func TestParse_CELGuards_Compile(t *testing.T) {
	for _, guard := range []string{
		`f_owner != null`,
		`f_owner == null`,
		`f_score == 3 && f_kind != "memo"`,
		`f_score > 2 || f_ok == true`,
		`f_ok != true`,
		`f_kind in ["memo", "report"]`,
	} {
		t.Run(guard, func(t *testing.T) {
			if _, err := typedef.Parse(withGuard(guard)); err != nil {
				t.Errorf("Parse refused guard %q: %v", guard, err)
			}
		})
	}
}

// A guard that does not compile, or does not yield a bool, refuses the
// Type at load, with a message that names the transition's guard.
func TestParse_BadCELGuards_AreRefused(t *testing.T) {
	cases := []struct {
		name, guard, wantInError string
	}{
		// One grammar: the spike's own syntax is gone.
		{"the old 'is set' grammar", `f_owner is set`, "guard"},
		{"syntax error", `f_owner >>> 3`, "guard"},
		{"unknown field", `f_ghost != null`, "f_ghost"},
		{"literal of the wrong type", `f_owner == 3`, "f_owner"},
		{"int compared with a string", `f_score == "3"`, "f_score"},
		{"not a bool", `f_score + 1`, "bool"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := typedef.Parse(withGuard(c.guard))
			if err == nil {
				t.Fatalf("Parse accepted guard %q", c.guard)
			}
			if !strings.Contains(err.Error(), c.wantInError) {
				t.Errorf("error %q does not mention %q", err, c.wantInError)
			}
		})
	}
}

// The stored form (JSON in Postgres) compiles its guards too, so a Type the
// item service reads back is as checked as one read from YAML.
func TestFromJSON_CompilesGuards(t *testing.T) {
	ty, err := typedef.Parse(withGuard(`f_owner != null`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	data, err := json.Marshal(ty)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if _, err := typedef.FromJSON(data); err != nil {
		t.Fatalf("FromJSON of a good Type: %v", err)
	}
	broken := strings.Replace(string(data), `f_owner != null`, `f_owner is set`, 1)
	if _, err := typedef.FromJSON([]byte(broken)); err == nil || !strings.Contains(err.Error(), "guard") {
		t.Errorf("FromJSON with an old-grammar guard: error = %v, want one naming the guard", err)
	}
}

// Every Type file in types/ is written in CEL and loads.
func TestTypeFiles_GuardsAreCEL(t *testing.T) {
	for _, name := range []string{"ticket.v1.yaml", "ticket.v2.yaml", "ticket.v3.yaml"} {
		ty := parseFile(t, name)
		for _, tr := range ty.Transitions {
			if strings.Contains(tr.Guard, "is set") {
				t.Errorf("%s: transition %s -> %s still uses the old grammar: %q", name, tr.From, tr.To, tr.Guard)
			}
		}
	}
	v1 := parseFile(t, "ticket.v1.yaml")
	tr, _ := v1.Transition("open", "in_progress")
	if tr.Guard != "f_assignee != null" {
		t.Errorf("ticket v1 open -> in_progress guard = %q, want %q", tr.Guard, "f_assignee != null")
	}
}
