package itemservice

import (
	"context"
	"errors"
)

// Role is one row of principal_roles: a principal holds a role in a
// namespace. Roles are server-side truth, read on every write that needs
// them; a request never claims one.
type Role struct {
	Namespace string
	Principal string // user:<id> or agent:<id>
	Role      string // approver, without the "role:" prefix
}

// RolesResult is what EnsureRoles did.
type RolesResult struct {
	// Added are the roles that were not there and now are.
	Added []Role
	// Unlisted are roles already in the database, in one of the namespaces
	// given, that the wanted list does not name. They are kept: EnsureRoles
	// only adds.
	Unlisted []Role
}

// errRolesNotBuilt marks what Step 0, part 3 has not built yet.
var errRolesNotBuilt = errors.New("itemservice: EnsureRoles not built yet (Step 0, part 3)")

// EnsureRoles adds every wanted role that is missing, in one transaction,
// and reports the roles of the given namespaces that the list does not name.
// It never removes a role. It is for the item service's start in dev-token
// mode, where the token file names the roles.
func (k *Service) EnsureRoles(ctx context.Context, namespaces []string, want []Role) (RolesResult, error) {
	return RolesResult{}, errRolesNotBuilt
}
