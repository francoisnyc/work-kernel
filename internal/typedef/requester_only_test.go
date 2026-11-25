// Tests for "only the requester" for drafts (docs/roadmap.md, track 1,
// item 4): a state's requester_only lists the fields only the Item's
// requester writes while it is in that state. Field by field, like frozen.
// A field the kernel writes, or one the same state freezes, is a
// contradiction (CONFLICT). The stored definition keeps the list, and a
// state without one stores nothing new.
package typedef_test

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// requesterOnly is a Type whose draft state has these extra keys (YAML
// flow, inside the state's braces).
func requesterOnly(draft string) string {
	return "id: ro\nversion: 1\ninitial_state: draft\nfields:\n" +
		"  - {id: f_amount, type: int}\n" +
		"  - {id: f_note, type: string}\n" +
		"  - {id: f_manager, type: principal, writable_by: kernel}\n" +
		"states:\n" +
		"  - {id: draft, category: todo" + draft + "}\n" +
		"  - {id: sent, category: done, frozen: [f_amount]}\n" +
		"transitions:\n  - {from: draft, to: sent, allowed: [creator]}\n" +
		"hooks:\n  - {id: who, on: transition, from: draft, to: sent, kind: check, action: hr.manager_of, reads: [created_on_behalf_of, created_by], writes: [f_manager]}\n"
}

func TestRequesterOnly_Parse(t *testing.T) {
	ty := mustParse(t, requesterOnly(", requester_only: [f_amount, f_note]"))
	st, _ := ty.State("draft")
	if !slices.Equal(st.RequesterOnly, []string{"f_amount", "f_note"}) {
		t.Errorf("draft's requester_only = %v, want [f_amount f_note]", st.RequesterOnly)
	}
	data, err := json.Marshal(ty) // as the item service stores it
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	back, err := typedef.FromJSON(data)
	if err != nil {
		t.Fatalf("FromJSON(%s): %v", data, err)
	}
	if st, _ := back.State("draft"); !slices.Equal(st.RequesterOnly, []string{"f_amount", "f_note"}) {
		t.Errorf("after the round trip, draft's requester_only = %v", st.RequesterOnly)
	}
}

// A state without requester_only stores nothing new: every registered
// version keeps its stored definition byte for byte.
func TestRequesterOnly_AbsentStoresNothingNew(t *testing.T) {
	data, err := json.Marshal(mustParse(t, requesterOnly("")))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "requester_only") {
		t.Errorf("stored definition %s names requester_only", data)
	}
}

func TestRequesterOnly_Problems(t *testing.T) {
	cases := []struct {
		name, draft, path string
		code              typedef.Code
		says              []string
	}{
		{"a field it does not declare", ", requester_only: [f_amount, f_nope]",
			"states[0].requester_only[1]", typedef.CodeUnknownField, []string{"f_nope", "requester_only"}},
		{"a field listed twice", ", requester_only: [f_amount, f_note, f_amount]",
			"states[0].requester_only[2]", typedef.CodeDuplicate, []string{"f_amount"}},
		{"a field only the kernel writes", ", requester_only: [f_manager]",
			"states[0].requester_only[0]", typedef.CodeConflict, []string{"f_manager", "kernel"}},
		{"a field the same state freezes", ", frozen: [f_note], requester_only: [f_amount, f_note]",
			"states[0].requester_only[1]", typedef.CodeConflict, []string{"f_note", "frozen"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := typedef.Parse([]byte(requesterOnly(c.draft)))
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

// A field frozen in another state is no contradiction: the draft is the
// requester's, and once submitted nobody changes it.
func TestRequesterOnly_FrozenElsewhereLoads(t *testing.T) {
	mustParse(t, requesterOnly(", requester_only: [f_amount]"))
}

// The published JSON Schema says what requester_only means.
func TestRequesterOnly_SchemaSaysTheRequester(t *testing.T) {
	data, err := os.ReadFile("../../schema/type.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"requester_only": {"description": "Fields only the requester writes while an Item is in this state`) {
		t.Errorf("the schema's state has no requester_only saying who writes the fields, and when")
	}
}
