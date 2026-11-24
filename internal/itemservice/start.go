package itemservice

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/francoisnyc/work-kernel/internal/policy"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// checkStart applies the start block (starting work, step 2) to a create,
// inside its transaction. The block is the latest registered version's,
// whatever version the caller names: a client still creating an old
// version cannot dodge a rule a later one adds, and a later version that
// drops the block loosens every version at once. The container is read
// under a FOR SHARE lock, so a change to its fields cannot commit between
// this check and the create.
//
// in comes first: it is static (a Type against a Type), and the caller
// sees the container, so "not here" tells them nothing new. Then allowed,
// by policy.MayStart. Both refusals are a *StartRefusedError, an
// ErrForbidden: over the API, NOT_ALLOWED with its fixed message.
func (k *Service) checkStart(ctx context.Context, tx pgx.Tx, req CreateRequest, ns string) error {
	latest, err := k.latestType(ctx, tx, req.TypeID)
	if err != nil {
		return err
	}
	st := latest.Start
	if st == nil {
		return nil
	}
	var (
		where string // the container's Type; "" at the top level
		facts policy.ContainerFacts
	)
	if req.ContainerID != nil {
		c, err := lockContainer(ctx, tx, ns, *req.ContainerID, false)
		if err != nil {
			return err
		}
		where = c.TypeID
		facts = policy.ContainerFacts{Fields: c.Fields, CreatedBy: c.CreatedBy, CreatedOnBehalfOf: c.CreatedForUser}
	}
	if !st.Admits(where) {
		return &StartRefusedError{Reason: StartNotIn, TypeID: req.TypeID}
	}
	who := policy.Who{Actor: req.Principal.Actor, OnBehalfOf: req.Principal.OnBehalfOf}
	// Roles are read only when an entry asks for one.
	if slices.ContainsFunc(st.Allowed, func(e string) bool { return strings.HasPrefix(e, typedef.AllowedRolePrefix) }) {
		if who.ActorRoles, err = policy.Roles(ctx, tx, ns, who.Actor); err != nil {
			return err
		}
		if who.OnBehalfOf != "" && who.OnBehalfOf != who.Actor {
			if who.OnBehalfOfRoles, err = policy.Roles(ctx, tx, ns, who.OnBehalfOf); err != nil {
				return err
			}
		}
	}
	if !policy.MayStart(st, who, facts) {
		return &StartRefusedError{Reason: StartNotAllowed, TypeID: req.TypeID}
	}
	return nil
}

// latestType is the latest registered version of a Type. Which version
// that is is read in the write's transaction every time (one indexed
// lookup), never cached: a version registered by another process counts
// at once. The definition itself comes from the cache.
func (k *Service) latestType(ctx context.Context, tx pgx.Tx, id string) (*typedef.Type, error) {
	var version *int
	if err := tx.QueryRow(ctx, `select max(version) from types where id = $1`, id).Scan(&version); err != nil {
		return nil, fmt.Errorf("read the latest version of %s: %w", id, err)
	}
	if version == nil {
		return nil, fmt.Errorf("%w: type %s is not registered", ErrInvalid, id)
	}
	return k.loadType(ctx, tx, id, *version)
}
