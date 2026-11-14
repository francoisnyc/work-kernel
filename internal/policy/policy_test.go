// Tests for Step 4: the two pure policy functions. No database: the item
// service loads roles and visible scopes, then asks these.
package policy_test

import (
	"reflect"
	"testing"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/policy"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// AuthorizeTransition answers "may this principal make this move?" from the
// transition's `allowed` list alone. Entries are role:<name> or principal:<id>;
// an absent or empty list allows anyone.
func TestAuthorizeTransition(t *testing.T) {
	cases := []struct {
		name      string
		principal string
		roles     []string
		allowed   []string
		want      bool
	}{
		{"no list: anyone", "user:a", nil, nil, true},
		{"empty list: anyone", "user:a", nil, []string{}, true},
		{"holds the role", "user:a", []string{"support"}, []string{"role:support"}, true},
		{"holds one of several roles listed", "user:a", []string{"lead"}, []string{"role:support", "role:lead"}, true},
		{"holds another role", "user:a", []string{"sales"}, []string{"role:support"}, false},
		{"holds no role", "user:a", nil, []string{"role:support"}, false},
		{"named as a principal", "user:a", nil, []string{"principal:user:a"}, true},
		{"another principal is named", "user:a", nil, []string{"principal:user:b"}, false},
		{"role or principal: the principal matches", "user:a", nil, []string{"role:support", "principal:user:a"}, true},
		{"a principal ID with a colon", "system:timer", nil, []string{"principal:system:timer"}, true},
		// A role and a principal are different things, whatever they are called.
		{"a role named like the principal", "support", nil, []string{"role:support"}, false},
		{"a principal named like the role", "user:a", []string{"user:a"}, []string{"principal:user:b"}, false},
		{"the role name is not a prefix match", "user:a", []string{"sup"}, []string{"role:support"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tr := typedef.Transition{From: "open", To: "in_progress", Allowed: c.allowed}
			if got := policy.AuthorizeTransition(c.principal, c.roles, tr); got != c.want {
				t.Errorf("AuthorizeTransition(%q, %v, allowed %v) = %v, want %v",
					c.principal, c.roles, c.allowed, got, c.want)
			}
		})
	}
}

// Project is all-or-nothing in Step 4: an Item in a scope the principal can
// see comes back unchanged; any other Item does not come back at all.
func TestProject(t *testing.T) {
	p := item.Principal{Actor: "user:a"}
	seen, other := uuid.New(), uuid.New()
	it := item.Item{
		ID: uuid.New(), TypeID: "ticket", TypeVersion: 1, ScopeID: &seen,
		State: "open", Fields: map[string]any{"f_title": "hello"}, ItemSeq: 3,
	}

	t.Run("visible scope: the Item, unchanged", func(t *testing.T) {
		got, ok := policy.Project(p, []uuid.UUID{other, seen}, it)
		if !ok || !reflect.DeepEqual(got, it) {
			t.Errorf("Project = %+v, %v; want the Item and true", got, ok)
		}
	})
	t.Run("scope not visible: nothing", func(t *testing.T) {
		got, ok := policy.Project(p, []uuid.UUID{other}, it)
		if ok || !reflect.DeepEqual(got, item.Item{}) {
			t.Errorf("Project = %+v, %v; want the zero Item and false", got, ok)
		}
	})
	t.Run("no visible scopes: nothing", func(t *testing.T) {
		if got, ok := policy.Project(p, nil, it); ok || !reflect.DeepEqual(got, item.Item{}) {
			t.Errorf("Project = %+v, %v; want the zero Item and false", got, ok)
		}
	})
	// An Item without a scope is never "visible to everyone".
	t.Run("an Item with no scope: nothing", func(t *testing.T) {
		noScope := it
		noScope.ScopeID = nil
		if _, ok := policy.Project(p, []uuid.UUID{seen, other}, noScope); ok {
			t.Error("Project let through an Item with no scope")
		}
	})
}
