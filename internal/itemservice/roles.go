package itemservice

import (
	"context"
	"fmt"
	"regexp"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/francoisnyc/work-kernel/internal/item"
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
	// Added are the roles that were not there and now are, in the order
	// asked.
	Added []Role
	// Unlisted are roles already in the database, in one of the namespaces
	// given, that the wanted list does not name. They are kept: EnsureRoles
	// only adds.
	Unlisted []Role
}

// roleName matches a bare role name: lower-case letters, digits, dashes and
// underscores, starting with a letter. "role:approver" is how a Type file
// refers to one, not its name.
var roleName = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,62}$`)

// EnsureRoles adds every wanted role that is missing, in one transaction,
// and reports the roles of the given namespaces that the list does not name.
// It never removes a role. It is for the item service's start in dev-token
// mode, where the token file names the roles. One bad role refuses them all
// (ErrInvalid) before anything is written: a role in a namespace the call
// does not name, a principal that is not user: or agent:, or a malformed
// role name.
func (k *Service) EnsureRoles(ctx context.Context, namespaces []string, want []Role) (RolesResult, error) {
	var wanted []Role
	for _, r := range want {
		switch {
		case !ValidNamespace(r.Namespace) || !slices.Contains(namespaces, r.Namespace):
			return RolesResult{}, fmt.Errorf("%w: role %s for %s: namespace %q is not one of %v",
				ErrInvalid, r.Role, r.Principal, r.Namespace, namespaces)
		case !item.IsKind(r.Principal, item.KindUser, item.KindAgent):
			return RolesResult{}, fmt.Errorf("%w: role %s: principal %q must be user:<id> or agent:<id>",
				ErrInvalid, r.Role, r.Principal)
		case !roleName.MatchString(r.Role):
			return RolesResult{}, fmt.Errorf("%w: role %q for %s: want a bare name such as approver",
				ErrInvalid, r.Role, r.Principal)
		}
		if !slices.Contains(wanted, r) {
			wanted = append(wanted, r)
		}
	}

	var res RolesResult
	err := pgx.BeginFunc(ctx, k.pool, func(tx pgx.Tx) error {
		for _, r := range wanted {
			tag, err := tx.Exec(ctx,
				`insert into principal_roles (namespace_id, principal, role) values ($1, $2, $3)
				 on conflict do nothing`, r.Namespace, r.Principal, r.Role)
			if err != nil {
				return fmt.Errorf("add role: %w", err)
			}
			if tag.RowsAffected() == 1 {
				res.Added = append(res.Added, r)
			}
		}
		rows, err := tx.Query(ctx,
			`select namespace_id, principal, role from principal_roles
			 where namespace_id = any($1) order by namespace_id, principal, role`, namespaces)
		if err != nil {
			return fmt.Errorf("read roles: %w", err)
		}
		have, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Role, error) {
			var r Role
			err := row.Scan(&r.Namespace, &r.Principal, &r.Role)
			return r, err
		})
		if err != nil {
			return fmt.Errorf("read roles: %w", err)
		}
		for _, r := range have {
			if !slices.Contains(wanted, r) {
				res.Unlisted = append(res.Unlisted, r)
			}
		}
		return nil
	})
	if err != nil {
		return RolesResult{}, err
	}
	return res, nil
}
