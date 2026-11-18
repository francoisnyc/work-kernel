package itemservice

import (
	"context"
	"fmt"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/francoisnyc/work-kernel/internal/lifecycle"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// Kernel-set links (proof of concept, Step 2, part 2). A link type with a
// resolve is set by the kernel in the write that enters its on_enter state,
// on any door: the unique Item of its target Type, in the resolve's state,
// whose principal field equals the Item's creator (created_on_behalf_of,
// else created_by). None or several refuse the entry: a direct transition
// is FAILED_PRECONDITION / LINK_UNRESOLVED, a hold's resolution is denied
// with the code (no_<link>, ambiguous_<link>).
//
// Locks, without deadlocks (lockForTransition): the candidates and the old
// target are read before any lock; then the Item (FOR UPDATE), the guards'
// linked Items (FOR SHARE), the candidates (FOR SHARE: their owner field
// cannot change before the commit) and the old target (FOR KEY SHARE) are
// locked in one pass in UUID order; then everything is read again, and a
// change starts the transaction again (errRetry). An Item created as a new
// candidate in between is not locked (a phantom): two open balances for one
// person are refused as ambiguous the next time, never prevented (Later).

// linkResolution is what entering a state resolves for one link type.
type linkResolution struct {
	link       typedef.LinkType
	candidates []uuid.UUID // sorted; one means resolved
	old        []uuid.UUID // the link's current targets (at most one)
}

// code is the refusal code when the link is not resolved, else "".
func (r linkResolution) code() string {
	switch len(r.candidates) {
	case 1:
		return ""
	case 0:
		return "no_" + r.link.Name
	default:
		return "ambiguous_" + r.link.Name
	}
}

func (r linkResolution) equal(o linkResolution) bool {
	return slices.Equal(r.candidates, o.candidates) && slices.Equal(r.old, o.old)
}

// resolvedOn are the link types the kernel sets on entering state to.
func resolvedOn(ty *typedef.Type, to string) []typedef.LinkType {
	var out []typedef.LinkType
	for _, l := range ty.Links {
		if l.Resolve != nil && l.Resolve.OnEnter == to {
			out = append(out, l)
		}
	}
	return out
}

// readResolutions reads, unlocked or again under the locks, the candidates
// and the old target of each link the kernel sets on entering to.
func readResolutions(ctx context.Context, tx pgx.Tx, ns string, ty *typedef.Type, id uuid.UUID,
	to string) ([]linkResolution, error) {
	links := resolvedOn(ty, to)
	if len(links) == 0 {
		return nil, nil
	}
	var creator string
	if err := tx.QueryRow(ctx,
		`select coalesce(created_on_behalf_of, created_by) from items where id = $1`, id).Scan(&creator); err != nil {
		return nil, fmt.Errorf("read creator: %w", err)
	}
	out := make([]linkResolution, 0, len(links))
	for _, l := range links {
		r := linkResolution{link: l}
		var err error
		if r.candidates, err = resolveCandidates(ctx, tx, ns, l, creator); err != nil {
			return nil, err
		}
		rows, err := tx.Query(ctx,
			`select target_id from links where source_id = $1 and link_type = $2 order by target_id`, id, l.Name)
		if err != nil {
			return nil, fmt.Errorf("read the current link: %w", err)
		}
		if r.old, err = pgx.CollectRows(rows, pgx.RowTo[uuid.UUID]); err != nil {
			return nil, fmt.Errorf("read the current link: %w", err)
		}
		out = append(out, r)
	}
	return out, nil
}

// resolveCandidates is a resolve link's lookup: the Items of its target
// Type, in the resolve's state, whose principal field holds creator, in ID
// order, whoever can see them. Resolution and React both ask it.
func resolveCandidates(ctx context.Context, tx pgx.Tx, ns string, l typedef.LinkType, creator string) ([]uuid.UUID, error) {
	rows, err := tx.Query(ctx,
		`select id from items
		 where namespace_id = $1 and type_id = $2 and state = $3 and fields ->> $4 = $5
		 order by id`,
		ns, l.Targets[0], l.Resolve.State, l.Resolve.Field, creator)
	if err != nil {
		return nil, fmt.Errorf("read link candidates: %w", err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return nil, fmt.Errorf("read link candidates: %w", err)
	}
	return ids, nil
}

func sameResolutions(a, b []linkResolution) bool {
	return slices.EqualFunc(a, b, linkResolution.equal)
}

// unresolved returns the first link that entering cannot set, if any.
func unresolved(res []linkResolution) (linkResolution, bool) {
	for _, r := range res {
		if r.code() != "" {
			return r, true
		}
	}
	return linkResolution{}, false
}

// linkUnresolvedError is the direct transition's refusal.
func linkUnresolvedError(r linkResolution) error {
	return &RejectedError{
		Code:     CodeLinkUnresolved,
		Reason:   fmt.Sprintf("entering this state sets link %s, and %s", r.link.Name, r.code()),
		Metadata: map[string]string{"link_type": r.link.Name, "reason": r.code()},
	}
}

// applyResolutions sets each resolved link, in the write that enters the
// state, and records it in that write's payload: link_type and link_target,
// and link_replaced when another target is replaced. The same target again
// changes nothing and is not recorded. Every link must be resolved.
func applyResolutions(ctx context.Context, tx pgx.Tx, ns string, ty *typedef.Type, id uuid.UUID,
	res []linkResolution, payload map[string]any) error {
	for _, r := range res {
		target := r.candidates[0]
		if slices.Equal(r.old, []uuid.UUID{target}) {
			continue
		}
		if len(r.old) > 0 {
			if _, err := tx.Exec(ctx, `delete from links where source_id = $1 and link_type = $2`, id, r.link.Name); err != nil {
				return fmt.Errorf("remove the replaced link: %w", err)
			}
			payload["link_replaced"] = r.old[0].String()
		}
		_, err := tx.Exec(ctx,
			`insert into links (namespace_id, source_id, source_type_id, link_type, target_id, src_single, dst_single)
			 values ($1, $2, $3, $4, $5, $6, $7)`,
			ns, id, ty.ID, r.link.Name, target, r.link.SourceSingle(), r.link.TargetSingle())
		if err != nil {
			return linkInsertError(err, r.link.Name)
		}
		payload["link_type"], payload["link_target"] = r.link.Name, target.String()
	}
	return nil
}

// holdDecider returns what decides a held transition from -> to: its check
// hook or its held reaction (their IDs share one space), if any.
func holdDecider(ty *typedef.Type, from, to string) (string, bool) {
	if h, ok := lifecycle.CheckFor(ty, from, to); ok {
		return h.ID, true
	}
	if r, ok := lifecycle.HeldReaction(ty, from, to); ok {
		return r.ID, true
	}
	return "", false
}
