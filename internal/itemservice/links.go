package itemservice

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/lifecycle"
	"github.com/francoisnyc/work-kernel/internal/policy"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// Links (docs/design.md, "Links, containment and people"). A link is a row
// in links, added and removed through Link and Unlink: one event on the
// SOURCE Item (link_added / link_removed, with item_seq), none on the
// target. Cardinality is enforced by the database: each row carries
// src_single and dst_single, snapshots of the source Type's declaration, and
// two partial unique indexes refuse a second link on a max-one side.

// Link refusal codes.
const (
	CodeLinkCardinality = "link_cardinality" // a max-one side already has its link; metadata link_type, side
	CodeAlreadyLinked   = "already_linked"   // the same link exists
	CodeNotLinked       = "not_linked"       // no such link to remove
	CodeLinkTargetType  = "link_target_type" // the target's Type is not one the link type allows
	// CodeLinkTargetChanged: a held reaction's resolve link no longer
	// resolves to its target (closed, given to someone else, or another
	// candidate appeared); the hold is denied with it.
	CodeLinkTargetChanged = "link_target_changed"
)

// MaxLinksShown is how many links Get shows per direction (restricted ones
// count); Item.LinksTruncated says there are more.
const MaxLinksShown = 200

// LinkRequest adds (Link) or removes (Unlink) one link from a source Item
// to a target. LinkType is the declared name, never the inverse.
type LinkRequest struct {
	IdempotencyKey string
	Principal      item.Principal
	SourceID       uuid.UUID
	LinkType       string
	TargetID       uuid.UUID
	// ExpectedItemSeq is the optional if-match, on the source.
	ExpectedItemSeq int64
	// ExpectedContentSeq is the if-match behind the public etag: when set,
	// the write happens only if the source's content_seq is still this one,
	// and fails with ErrStale otherwise. Zero means "no check". It is left
	// out of the idempotency hash when zero (omitempty), so a request
	// without it hashes as it did before the field existed.
	ExpectedContentSeq int64            `json:",omitempty"`
	Attribution        item.Attribution // optional; what caused this write
}

// LinkFilter keeps Items with a link of this name to or from ItemID, as the
// result Items see it: outgoing takes a declared name, incoming an inverse
// name. The name matches every Type that declares it in that direction; a
// name no Type declares in that direction is ErrInvalid.
type LinkFilter struct {
	LinkType  string
	Direction item.LinkDirection
	ItemID    uuid.UUID
}

// Link adds a link and records link_added on the source.
func (k *Service) Link(ctx context.Context, req LinkRequest) (WriteResult, error) {
	return k.linkWrite(ctx, req, true)
}

// Unlink removes a link and records link_removed on the source.
func (k *Service) Unlink(ctx context.Context, req LinkRequest) (WriteResult, error) {
	return k.linkWrite(ctx, req, false)
}

// linkWrite is Link (add) and Unlink. The caller must see both ends (else
// ErrNotFound, before anything else); for an agent both are inside its
// session, it holds a write power, and the write rule runs against both
// ends' scopes, the source first: a link can change the target's behavior
// through guards, so it is a write to both.
func (k *Service) linkWrite(ctx context.Context, req LinkRequest, add bool) (WriteResult, error) {
	if req.IdempotencyKey == "" || req.Principal.Actor == "" {
		return WriteResult{}, fmt.Errorf("%w: idempotency key and actor are required", ErrInvalid)
	}
	if err := checkPublic(req.Principal, req.Attribution); err != nil {
		return WriteResult{}, err
	}
	if err := k.refuseCustomer(ctx, req.Principal, req.SourceID); err != nil {
		return WriteResult{}, err
	}
	if err := checkOptions(req.ExpectedItemSeq, req.ExpectedContentSeq, req.Attribution); err != nil {
		return WriteResult{}, err
	}
	if req.LinkType == "" {
		return WriteResult{}, &InvalidError{Path: "link_type", Reason: "a link type is required"}
	}
	if req.SourceID == req.TargetID {
		return WriteResult{}, &InvalidError{Path: "target", Reason: "an Item cannot link to itself"}
	}
	attr, err := sessionAttribution(req.Principal, req.Attribution)
	if err != nil {
		return WriteResult{}, err
	}
	op, opName := lifecycle.OpLinkRemoved, "unlink"
	if add {
		op, opName = lifecycle.OpLinkAdded, "link"
	}
	hash, err := requestHash(opName, req)
	if err != nil {
		return WriteResult{}, err
	}

	ns := nsOf(req.Principal)
	return k.write(ctx, req.Principal, req.IdempotencyKey, hash, func(tx pgx.Tx) (WriteResult, error) {
		// Both ends, in UUID order: the source FOR UPDATE, the target FOR
		// KEY SHARE (it must exist and keep its Type; its state may change).
		src, ends, err := lockLinkEnds(ctx, tx, ns, req.SourceID, []uuid.UUID{req.TargetID})
		if err != nil {
			return WriteResult{}, err
		}
		dst := ends[req.TargetID]
		recorded, err := admit(ctx, tx, req.Principal, req.SourceID, src, nil)
		if err != nil {
			return WriteResult{}, err
		}
		if err := checkVisible(ctx, tx, req.Principal, dst.ScopeID); err != nil {
			return WriteResult{}, err
		}
		if err := checkInSession(ctx, tx, req.Principal, &req.TargetID); err != nil {
			return WriteResult{}, err
		}
		if err := checkExpectedSeq(src, req.ExpectedItemSeq, req.ExpectedContentSeq); err != nil {
			return WriteResult{}, err
		}
		ty, err := k.loadType(ctx, tx, src.TypeID, src.TypeVersion)
		if err != nil {
			return WriteResult{}, err
		}
		lt, ok := ty.Link(req.LinkType)
		if !ok {
			return WriteResult{}, &InvalidError{Path: "link_type",
				Reason: fmt.Sprintf("type %s v%d declares no link type %q", ty.ID, ty.Version, req.LinkType)}
		}
		// A link only the kernel sets: refused to everyone, after NOT_FOUND
		// (both ends were found visible above).
		if lt.WritableBy == typedef.WritableByKernel {
			return WriteResult{}, &LinkNotWritableError{LinkType: lt.Name}
		}
		// A link only roles write (Jira part 2, step 3b): the rule of a
		// role-written field, so an agent and its user must both hold one.
		if len(lt.WritableByRoles) > 0 {
			who := policy.Who{Actor: req.Principal.Actor, OnBehalfOf: req.Principal.OnBehalfOf}
			if who.ActorRoles, err = policy.Roles(ctx, tx, nsOf(req.Principal), who.Actor); err != nil {
				return WriteResult{}, err
			}
			if who.OnBehalfOf != "" {
				if who.OnBehalfOfRoles, err = policy.Roles(ctx, tx, nsOf(req.Principal), who.OnBehalfOf); err != nil {
					return WriteResult{}, err
				}
			}
			if !policy.MayWriteRoleField(typedef.Field{ID: lt.Name, WritableByRoles: lt.WritableByRoles}, who) {
				return WriteResult{}, &LinkNotWritableError{LinkType: lt.Name, Roles: lt.WritableByRoles}
			}
		}
		if err := checkAgentWrite(ctx, tx, req.Principal, src.ScopeID, &req.SourceID); err != nil {
			return WriteResult{}, err
		}
		if err := checkAgentWrite(ctx, tx, req.Principal, dst.ScopeID, &req.TargetID); err != nil {
			return WriteResult{}, err
		}

		if add {
			if !lt.AllowsTarget(dst.TypeID) {
				return WriteResult{}, rejected(CodeLinkTargetType, "link %s does not allow a target of that Type", lt.Name)
			}
			_, err := tx.Exec(ctx,
				`insert into links (namespace_id, source_id, source_type_id, link_type, target_id, src_single, dst_single)
				 values ($1, $2, $3, $4, $5, $6, $7)`,
				ns, req.SourceID, ty.ID, lt.Name, req.TargetID, lt.SourceSingle(), lt.TargetSingle())
			if err != nil {
				return WriteResult{}, linkInsertError(err, lt.Name)
			}
		} else {
			tag, err := tx.Exec(ctx,
				`delete from links where source_id = $1 and link_type = $2 and target_id = $3`,
				req.SourceID, lt.Name, req.TargetID)
			if err != nil {
				return WriteResult{}, fmt.Errorf("delete link: %w", err)
			}
			if tag.RowsAffected() == 0 {
				return WriteResult{}, rejected(CodeNotLinked, "there is no such link to remove")
			}
		}

		// The shape only: the link's name and the target's opaque ID.
		payload := map[string]any{"link_type": lt.Name, "target": req.TargetID.String()}
		bump, err := decideContent(ty, op, payload)
		if err != nil {
			return WriteResult{}, err
		}
		itemSeq := src.ItemSeq + 1
		if _, err := tx.Exec(ctx, `update items set item_seq = $2, content_seq = content_seq + $3, updated_at = now() where id = $1`,
			req.SourceID, itemSeq, bump.n); err != nil {
			return WriteResult{}, fmt.Errorf("update item: %w", err)
		}
		return appendEvent(ctx, tx, ty, req.SourceID, itemSeq, recorded, attr, bump, payload, nil)
	})
}

// linkInsertError turns a unique violation into the refusal it means. The
// statement failed, so the transaction rolls back with it.
func linkInsertError(err error, linkType string) error {
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23505" {
		switch pe.ConstraintName {
		case "links_pkey":
			return rejected(CodeAlreadyLinked, "the link already exists")
		case "links_src_single", "links_dst_single":
			side := "source"
			if pe.ConstraintName == "links_dst_single" {
				side = "target"
			}
			// Never the Item that holds the slot: the caller may not see it.
			return &RejectedError{Code: CodeLinkCardinality,
				Reason:   fmt.Sprintf("link %s allows one link per %s, and it has one", linkType, side),
				Metadata: map[string]string{"link_type": linkType, "side": side}}
		}
	}
	return fmt.Errorf("insert link: %w", err)
}

// linkEnd is what a link write needs of an Item at the other end.
type linkEnd struct {
	TypeID      string
	TypeVersion int
	ScopeID     uuid.UUID
}

// lockLinkEnds locks the source FOR UPDATE and the targets FOR KEY SHARE,
// all in UUID order, one shared order for every write that touches links
// (locking the source first and a target after it is how two link writes
// in opposite directions would deadlock). A missing Item, or one of another
// namespace, is ErrNotFound. With a move, the namespace move lock is taken
// first, as lockForMove does.
func lockLinkEnds(ctx context.Context, tx pgx.Tx, ns string, source uuid.UUID,
	targets []uuid.UUID) (lockedItem, map[uuid.UUID]linkEnd, error) {
	ids := append([]uuid.UUID{source}, targets...)
	slices.SortFunc(ids, compareUUID)
	ids = slices.Compact(ids)
	var src lockedItem
	ends := map[uuid.UUID]linkEnd{}
	for _, id := range ids {
		if id == source {
			it, err := lockItem(ctx, tx, ns, id)
			if err != nil {
				return lockedItem{}, nil, err
			}
			src = it
			continue
		}
		var e linkEnd
		err := tx.QueryRow(ctx,
			`select type_id, type_version, scope_id from items where id = $1 and namespace_id = $2 for key share`,
			id, ns).Scan(&e.TypeID, &e.TypeVersion, &e.ScopeID)
		if errors.Is(err, pgx.ErrNoRows) {
			return lockedItem{}, nil, ErrNotFound
		}
		if err != nil {
			return lockedItem{}, nil, fmt.Errorf("lock link target: %w", err)
		}
		ends[id] = e
	}
	return src, ends, nil
}

// compareUUID orders UUIDs as Postgres does: byte by byte.
func compareUUID(a, b uuid.UUID) int { return strings.Compare(string(a[:]), string(b[:])) }

// --- guards that read links ---

// guardLinkNames are the link names read by the guards of transitions into
// state `to`, from any state: the union, so the set does not depend on the
// Item's state before it is locked.
func guardLinkNames(ty *typedef.Type, to string) []string {
	var names []string
	for _, tr := range ty.Transitions {
		if tr.To == to {
			names = append(names, tr.GuardLinks...)
		}
	}
	slices.Sort(names)
	return slices.Compact(names)
}

// linkSet is the IDs linked under each name a guard reads, sorted.
type linkSet map[string][]uuid.UUID

// readLinkSet reads, for each name, the Items linked to id under it
// (typedef.LinkSource): a declared name reads the outgoing links; an
// inverse name the incoming ones from Items of the same Type; an incoming
// name those from the Types it lists, which declare the link.
func readLinkSet(ctx context.Context, tx pgx.Tx, ty *typedef.Type, id uuid.UUID, names []string) (linkSet, error) {
	set := linkSet{}
	for _, name := range names {
		var rows pgx.Rows
		var err error
		src, ok := ty.LinkSource(name)
		switch {
		case !ok:
			return nil, fmt.Errorf("type %s v%d: a guard reads link %q, which it does not declare", ty.ID, ty.Version, name)
		case src.Outgoing:
			rows, err = tx.Query(ctx,
				`select target_id from links where source_id = $1 and link_type = $2 order by target_id`, id, src.Link)
		default:
			rows, err = tx.Query(ctx,
				`select source_id from links where target_id = $1 and link_type = $2 and source_type_id = any($3::text[])
				 order by source_id`, id, src.Link, src.From)
		}
		if err != nil {
			return nil, fmt.Errorf("read links: %w", err)
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
		if err != nil {
			return nil, fmt.Errorf("read links: %w", err)
		}
		set[name] = ids
	}
	return set, nil
}

func (s linkSet) equal(o linkSet) bool {
	return maps.EqualFunc(s, o, func(a, b []uuid.UUID) bool { return slices.Equal(a, b) })
}

func (s linkSet) ids() []uuid.UUID {
	var all []uuid.UUID
	for _, ids := range s {
		all = append(all, ids...)
	}
	slices.SortFunc(all, compareUUID)
	return slices.Compact(all)
}

// lockForTransition locks the Item a transition into `to` writes, and, when
// the guards of transitions into `to` read links, the linked Items too, and
// returns what those guards see of them (nil when they read none). When
// entering `to` sets kernel-resolved links (resolve.go), it also locks their
// candidates and current targets and returns what entering resolves.
//
// Without deadlocks: the Item, its link set and the resolutions are read
// unlocked; then the Item is locked FOR UPDATE, the linked rows and the
// candidates FOR SHARE and the current targets FOR KEY SHARE, together in
// UUID order; then all of it is read again, and if anything changed the
// whole transaction starts again (errRetry). FOR SHARE, not FOR KEY SHARE:
// every state change takes the row FOR UPDATE, and a plain UPDATE takes FOR
// NO KEY UPDATE, which only FOR SHARE (and stronger) blocks. So a linked
// Item cannot change state, nor a candidate its fields, between this read
// and the commit. Links themselves cannot change either: adding or removing
// one locks its source FOR UPDATE and its target FOR KEY SHARE, and both
// conflict with the Item's FOR UPDATE.
//
// Guards read linked Items whoever the caller is (a rule must hold for
// everyone), but only their state and category.
func (k *Service) lockForTransition(ctx context.Context, tx pgx.Tx, ns string, id uuid.UUID,
	to string) (lockedItem, lifecycle.Links, []linkResolution, error) {
	var typeID string
	var version int
	err := tx.QueryRow(ctx, `select type_id, type_version from items where id = $1 and namespace_id = $2`,
		id, ns).Scan(&typeID, &version)
	if errors.Is(err, pgx.ErrNoRows) {
		return lockedItem{}, nil, nil, ErrNotFound
	}
	if err != nil {
		return lockedItem{}, nil, nil, fmt.Errorf("read item: %w", err)
	}
	ty, err := k.loadType(ctx, tx, typeID, version) // a Type version never changes
	if err != nil {
		return lockedItem{}, nil, nil, err
	}
	names := guardLinkNames(ty, to)
	if len(names) == 0 && len(resolvedOn(ty, to)) == 0 {
		it, err := lockItem(ctx, tx, ns, id)
		return it, nil, nil, err
	}

	before, err := readLinkSet(ctx, tx, ty, id, names)
	if err != nil {
		return lockedItem{}, nil, nil, err
	}
	if err := callTestHook(ctx, tx, "links_read"); err != nil {
		return lockedItem{}, nil, nil, err
	}
	resBefore, err := readResolutions(ctx, tx, ns, ty, id, to)
	if err != nil {
		return lockedItem{}, nil, nil, err
	}
	if resBefore != nil {
		if err := callTestHook(ctx, tx, "resolve_candidates_read"); err != nil {
			return lockedItem{}, nil, nil, err
		}
	}

	// One pass, in UUID order, each row at the strongest lock it needs.
	const (
		keyShare = iota + 1
		share
	)
	modes := map[uuid.UUID]int{}
	want := func(other uuid.UUID, m int) { modes[other] = max(modes[other], m) }
	for _, other := range before.ids() {
		want(other, share)
	}
	for _, r := range resBefore {
		for _, c := range r.candidates {
			want(c, share)
		}
		for _, o := range r.old {
			want(o, keyShare)
		}
	}
	all := append(slices.Collect(maps.Keys(modes)), id)
	slices.SortFunc(all, compareUUID)
	all = slices.Compact(all)
	var it lockedItem
	for _, other := range all {
		if other == id {
			if it, err = lockItem(ctx, tx, ns, id); err != nil {
				return lockedItem{}, nil, nil, err
			}
			continue
		}
		q := `select 1 from items where id = $1 for share`
		if modes[other] == keyShare {
			q = `select 1 from items where id = $1 for key share`
		}
		if _, err := tx.Exec(ctx, q, other); err != nil {
			return lockedItem{}, nil, nil, fmt.Errorf("lock linked item: %w", err)
		}
	}
	after, err := readLinkSet(ctx, tx, ty, id, names)
	if err != nil {
		return lockedItem{}, nil, nil, err
	}
	if !after.equal(before) {
		return lockedItem{}, nil, nil, errRetry // a link changed before the locks: read them again
	}
	resAfter, err := readResolutions(ctx, tx, ns, ty, id, to)
	if err != nil {
		return lockedItem{}, nil, nil, err
	}
	if !sameResolutions(resAfter, resBefore) {
		return lockedItem{}, nil, nil, errRetry // a candidate or the current target changed: read again
	}
	var links lifecycle.Links
	if len(names) > 0 {
		if links, err = k.linkedItems(ctx, tx, after); err != nil {
			return lockedItem{}, nil, nil, err
		}
	}
	return it, links, resAfter, nil
}

// linkedItems reads the state and category of every Item in the set.
func (k *Service) linkedItems(ctx context.Context, tx pgx.Tx, set linkSet) (lifecycle.Links, error) {
	links := lifecycle.Links{}
	ids := set.ids()
	if len(ids) == 0 {
		return links, nil
	}
	rows, err := tx.Query(ctx, `select id, type_id, type_version, state from items where id = any($1)`, ids)
	if err != nil {
		return nil, fmt.Errorf("read linked items: %w", err)
	}
	type row struct {
		ID      uuid.UUID
		TypeID  string
		Version int
		State   string
	}
	got, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (row, error) {
		var x row
		err := r.Scan(&x.ID, &x.TypeID, &x.Version, &x.State)
		return x, err
	})
	if err != nil {
		return nil, fmt.Errorf("read linked items: %w", err)
	}
	byID := map[uuid.UUID]lifecycle.LinkedItem{}
	for _, x := range got {
		ty, err := k.loadType(ctx, tx, x.TypeID, x.Version)
		if err != nil {
			return nil, err
		}
		st, _ := ty.State(x.State)
		byID[x.ID] = lifecycle.LinkedItem{State: x.State, Category: st.Category}
	}
	for name, ids := range set {
		list := make([]lifecycle.LinkedItem, 0, len(ids))
		for _, id := range ids {
			list = append(list, byID[id])
		}
		links[name] = list
	}
	return links, nil
}

// --- reading links (Get) ---

// itemLinks reads an Item's links as the principal sees them, outgoing
// first, each direction in the fixed order (created_at, the declared name,
// the other end's ID), at most MaxLinksShown per direction. An outgoing
// link whose target the principal cannot see (or outside an agent session's
// scopes) is restricted; an incoming link whose source it cannot see is left
// out, since the target has no event that could show it. Naming the other
// end records no read: a link carries only its ID.
//
// A customer's links are chosen in the SQL (review of 2026-11-21), so the
// limit, and links_truncated, count only links they may see: outgoing ones
// whose name the Item's own version (ty) lists and whose target they see;
// incoming ones whose source they see and whose source's version lists the
// name. Restricted ones are never theirs.
func (k *Service) itemLinks(ctx context.Context, p item.Principal, ty *typedef.Type, id uuid.UUID) ([]item.Link, bool, error) {
	c, err := loadCaps(ctx, k.pool, p)
	if err != nil {
		return nil, false, err
	}
	args, err := k.seeArgs(ctx, p)
	if err != nil {
		return nil, false, err
	}
	args["id"], args["limit"] = id, MaxLinksShown+1
	cte, inSession := sessionFilter(c, "o", args)
	with := ""
	if cte != "" {
		with = "with recursive " + cte + "\n"
	}
	// A customer's links pass the customer rule too (Jira part 2, step 4a).
	visible := seeCondition("o") + inSession
	outOnly, inOnly := "", ""
	if policy.CustomerOf(p) != "" {
		listed := []string{}
		for _, l := range ty.Links {
			if ty.CustomerSees(l.Name) {
				listed = append(listed, l.Name)
			}
		}
		types, err := k.ListTypes(ctx)
		if err != nil {
			return nil, false, err
		}
		var lt, ln []string
		var lv []int32
		for _, t := range types {
			for _, l := range t.Links {
				if t.CustomerSees(l.Name) {
					lt, lv, ln = append(lt, t.ID), append(lv, int32(t.Version)), append(ln, l.Name)
				}
			}
		}
		if lt == nil {
			lt, lv, ln = []string{}, []int32{}, []string{}
		}
		args["out_listed"], args["in_t"], args["in_v"], args["in_n"] = listed, lt, lv, ln
		outOnly = " and l.link_type = any(@out_listed::text[]) and " + visible
		inOnly = " and (o.type_id, o.type_version, l.link_type) in (select * from unnest(@in_t::text[], @in_v::int[], @in_n::text[]))"
	}

	outRows, err := k.pool.Query(ctx, with+
		`select l.link_type, l.target_id, `+visible+`
		 from links l join items o on o.id = l.target_id join scopes s on s.id = o.scope_id
		 where l.source_id = @id`+outOnly+`
		 order by l.created_at, l.link_type, l.target_id
		 limit @limit`, args)
	if err != nil {
		return nil, false, fmt.Errorf("read outgoing links: %w", err)
	}
	out, err := pgx.CollectRows(outRows, func(r pgx.CollectableRow) (item.Link, error) {
		l := item.Link{Direction: item.LinkOutgoing}
		var seen bool
		err := r.Scan(&l.LinkType, &l.Other, &seen)
		if !seen {
			l.Other, l.Restricted = uuid.Nil, true
		}
		return l, err
	})
	if err != nil {
		return nil, false, fmt.Errorf("read outgoing links: %w", err)
	}

	inRows, err := k.pool.Query(ctx, with+
		`select l.source_type_id, o.type_version, l.link_type, l.source_id
		 from links l join items o on o.id = l.source_id join scopes s on s.id = o.scope_id
		 where l.target_id = @id and `+visible+inOnly+`
		 order by l.created_at, l.link_type, l.source_id
		 limit @limit`, args)
	if err != nil {
		return nil, false, fmt.Errorf("read incoming links: %w", err)
	}
	type incoming struct {
		typeID, name string
		version      int
		source       uuid.UUID
	}
	in, err := pgx.CollectRows(inRows, func(r pgx.CollectableRow) (incoming, error) {
		var x incoming
		err := r.Scan(&x.typeID, &x.version, &x.name, &x.source)
		return x, err
	})
	if err != nil {
		return nil, false, fmt.Errorf("read incoming links: %w", err)
	}

	truncated := false
	if len(out) > MaxLinksShown {
		out, truncated = out[:MaxLinksShown], true
	}
	if len(in) > MaxLinksShown {
		in, truncated = in[:MaxLinksShown], true
	}
	var links []item.Link // nil when there are none, like an Item read any other way
	links = append(links, out...)
	customer := policy.CustomerOf(p) != ""
	for _, x := range in {
		// A customer sees an incoming link only when the source's Type
		// version lists the link's name for them (the source is visible to
		// them already: the query says so). Outgoing ones are chosen by
		// policy.Project, from the Item's own Type.
		if customer {
			st, err := k.loadType(ctx, k.pool, x.typeID, x.version)
			if err != nil {
				return nil, false, err
			}
			if !st.CustomerSees(x.name) {
				continue
			}
		}
		inverse, err := k.inverseOf(ctx, x.typeID, x.name)
		if err != nil {
			return nil, false, err
		}
		links = append(links, item.Link{LinkType: inverse, Direction: item.LinkIncoming, Other: x.source})
	}
	return links, truncated, nil
}

// inverseOf returns the inverse of a Type's link name. Every version of a
// Type that declares the name gives it the same inverse (CheckLinkInverses,
// at load and in RegisterType), so any version will do.
func (k *Service) inverseOf(ctx context.Context, typeID, name string) (string, error) {
	types, err := k.ListTypes(ctx)
	if err != nil {
		return "", err
	}
	for _, t := range types {
		if t.ID != typeID {
			continue
		}
		if lt, ok := t.Link(name); ok {
			return lt.Inverse, nil
		}
	}
	return "", fmt.Errorf("itemservice: no version of type %s declares link %s", typeID, name)
}

// --- the write path's retries and test hook ---

// retryable reports whether write runs a transaction again after err: a
// lost race it backed out of (errRetry), a deadlock Postgres broke (40P01)
// or a serialization failure (40001). Lock order rules out deadlocks among
// the kernel's own writes; this is the backstop.
func retryable(err error) bool {
	if errors.Is(err, errRetry) {
		return true
	}
	var pe *pgconn.PgError
	return errors.As(err, &pe) && (pe.Code == "40P01" || pe.Code == "40001")
}

// testHook is nil in production. Tests set it (export_test.go) to pause or
// act at named points of a write, inside its transaction:
//   - "write": after the idempotency key is claimed, on every attempt;
//   - "links_read": after a link-reading transition read its links unlocked;
//   - "link_guard_held": after its guard held, with the linked rows locked.
//
// An error it returns fails the attempt as if the point had failed.
var testHook func(ctx context.Context, tx pgx.Tx, point string) error

func callTestHook(ctx context.Context, tx pgx.Tx, point string) error {
	if testHook == nil {
		return nil
	}
	return testHook(ctx, tx, point)
}
