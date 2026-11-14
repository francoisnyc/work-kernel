// Tests for Step 6, part 1: a transition's `allowed` list may name a hook of
// the same Type, principal:hook:<type>:<hook-id>. The load-time rule that a
// hook may only use a guarded transition its list names arrives with hook
// declarations, in part 2.
package typedef_test

import (
	"strings"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/typedef"
)

const hookAllowedType = `
id: request
version: 1
initial_state: open
states:
  - {id: open,     category: todo}
  - {id: approved, category: done}
transitions:
  - {from: open, to: approved, allowed: ['role:support', 'principal:hook:request:erp_check']}
`

func TestParse_AllowedMayNameAHookOfTheSameType(t *testing.T) {
	ty, err := typedef.Parse([]byte(hookAllowedType))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := []string{"role:support", "principal:hook:request:erp_check"}
	if got := ty.Transitions[0].Allowed; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("allowed = %v, want %v", got, want)
	}
}

func TestParse_RejectsBrokenHookEntries(t *testing.T) {
	const good = "'principal:hook:request:erp_check'"
	cases := []struct{ name, entry string }{
		{"no hook ID", "'principal:hook:request'"},
		{"a version in the actor", "'principal:hook:request@v1:erp_check'"},
		// A hook acts only on Items of its own Type, so another Type's hook
		// can never pass this list: naming it is a mistake.
		{"another Type's hook", "'principal:hook:ticket:erp_check'"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			broken := strings.Replace(hookAllowedType, good, c.entry, 1)
			_, err := typedef.Parse([]byte(broken))
			if err == nil {
				t.Fatalf("Parse accepted allowed entry %s", c.entry)
			}
			if !strings.Contains(err.Error(), strings.Trim(c.entry, "'")) {
				t.Errorf("error %q does not name the entry %s", err, c.entry)
			}
		})
	}
}
