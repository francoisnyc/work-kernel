package typedef

import (
	"slices"
	"strings"

	"github.com/francoisnyc/work-kernel/internal/item"
)

// Track 1, item 6, step 2: a Type's optional migrate block says who may
// migrate an open Item of an older version to this one, while it is the
// latest. This file checks it; package policy judges a caller against it
// (MayMigrate), and the item service applies the latest version's block.

// checkMigrate checks the block on this Type alone (stage b): it names
// someone, each entry is a role or a user, and each once. Migrating is an
// administrator's act, so nothing of the Item decides it (no creator, no
// field:), and it is never an agent's or a customer's.
func (t *Type) checkMigrate(r reporter) {
	m := t.Migrate
	if m == nil {
		return
	}
	// YAML gives an empty list for allowed: [], and nil for a key left out.
	if m.Allowed == nil {
		r.fail(CodeRequired, "migrate", "Say who may (allowed), or remove migrate",
			"migrate says nothing")
		return
	}
	if len(m.Allowed) == 0 {
		r.fail(CodeInvalidValue, "migrate.allowed", "List the roles or users who may migrate Items, or remove migrate: without it nobody may",
			"migrate.allowed lists nobody")
	}
	for i, e := range m.Allowed {
		p := at("migrate", "allowed", i)
		role, isRole := strings.CutPrefix(e, AllowedRolePrefix)
		who, isPrincipal := strings.CutPrefix(e, AllowedPrincipalPrefix)
		switch {
		case slices.Contains(m.Allowed[:i], e):
			r.fail(CodeDuplicate, p, "Remove the second one", "migrate.allowed names %s twice", e)
		case isRole && role != "", isPrincipal && item.IsKind(who, item.KindUser):
		default:
			r.fail(CodeInvalidValue, p, "Write role:<name> or principal:user:<id>",
				"migrate.allowed entry %q is neither role:<name> nor principal:user:<id> "+
					"(only people migrate Items: no agent, no customer, and nothing of the Item decides it)", e)
		}
	}
}
