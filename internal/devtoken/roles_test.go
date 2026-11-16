// Tests for Step 0, part 3: roles in the dev-token file. An entry may name
// the roles its principal holds in its namespace; the item service adds
// them at start in dev-token mode. A role is a bare name (approver): the
// "role:" prefix belongs to Type files, not here.
package devtoken_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/devtoken"
)

func TestParse_ReadsRoles(t *testing.T) {
	got, err := devtoken.Parse([]byte(`tokens:
  - {name: ana, token: dev-ana, principal: "user:ana", namespace: acme}
  - {name: bob, token: dev-bob, principal: "user:bob", namespace: acme, roles: [approver, auditor]}
  - {name: bob-globex, token: dev-bob-g, principal: "user:bob", namespace: globex}
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("Parse = %d entries, want 3", len(got))
	}
	if got[0].Roles != nil {
		t.Errorf("ana's roles = %v, want none", got[0].Roles)
	}
	if !slices.Equal(got[1].Roles, []string{"approver", "auditor"}) {
		t.Errorf("bob's roles in acme = %v, want [approver auditor]", got[1].Roles)
	}
	// Roles are per namespace: the same principal elsewhere holds none.
	if got[2].Roles != nil {
		t.Errorf("bob's roles in globex = %v, want none", got[2].Roles)
	}
}

// A bad role refuses the whole file, naming the entry and the role.
func TestParse_RefusesBadRoles(t *testing.T) {
	for _, role := range []string{`"role:approver"`, `Approver`, `""`, `"two words"`} {
		t.Run(role, func(t *testing.T) {
			_, err := devtoken.Parse([]byte("tokens:\n  - {name: bob, token: dev-bob, principal: \"user:bob\", namespace: acme, roles: [" + role + "]}\n"))
			if err == nil {
				t.Fatalf("Parse accepted role %s", role)
			}
			if !strings.Contains(err.Error(), "bob") || !strings.Contains(err.Error(), "role") {
				t.Errorf("error %q does not name the entry and the role", err)
			}
		})
	}
}
