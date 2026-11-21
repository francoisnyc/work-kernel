package itemservice

// Customers (Jira part 2, step 4a). The rule itself is package policy's
// (CustomerCondition, CustomerRelation, Project); this file applies it on
// the item service's paths: the named arguments every visibility check
// takes, an Item-level check for writes, and the fixed permissions.

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/policy"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// customerPowers are the powers of a session acting for a customer: read,
// create, comment and moves (transition:* or transition:<state>). Nothing
// that edits, moves an Item or reaches out.
func customerPower(p string) bool {
	switch p {
	case item.PowerRead, item.PowerCreate, item.PowerComment, item.PowerTransitionAll:
		return true
	}
	return len(p) > len(item.PowerTransitionPrefix) && p[:len(item.PowerTransitionPrefix)] == item.PowerTransitionPrefix
}

// seeCondition is "may the principal read the Item row alias m (with its
// scope row s)": the scope test and the customer rule.
func seeCondition(m string) string {
	return policy.VisibleCondition + " and " + policy.CustomerCondition(m)
}

// seeArgs returns the named arguments seeCondition and the customer
// relation read. Only a customer's reads need the rules, read from the
// registered Types (cached).
func (k *Service) seeArgs(ctx context.Context, p item.Principal) (pgx.NamedArgs, error) {
	args := policy.VisibleArgs(p)
	var rules policy.CustomerRules
	if policy.CustomerOf(p) != "" {
		types, err := k.ListTypes(ctx)
		if err != nil {
			return nil, err
		}
		rules = policy.RulesOf(types)
	}
	policy.CustomerArgs(p, rules, args)
	return args, nil
}

// customerSees reports whether the customer rule admits the Item for p
// (always true for anyone who is not a customer). The scope test is
// separate: every path makes it already.
func (k *Service) customerSees(ctx context.Context, q policy.Querier, p item.Principal, id uuid.UUID) (bool, error) {
	if policy.CustomerOf(p) == "" {
		return true, nil
	}
	args, err := k.seeArgs(ctx, p)
	if err != nil {
		return false, err
	}
	args["id"] = id
	var ok bool
	err = q.QueryRow(ctx, `select `+policy.CustomerCondition("m")+` from items m where m.id = @id`, args).Scan(&ok)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check the customer rule: %w", err)
	}
	return ok, nil
}

// checkCustomerSees is ErrNotFound unless the customer rule admits the Item.
func (k *Service) checkCustomerSees(ctx context.Context, q policy.Querier, p item.Principal, id uuid.UUID) error {
	ok, err := k.customerSees(ctx, q, p, id)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotFound
	}
	return nil
}

// refuseCustomer is the fixed permissions' answer to a write a customer
// never makes (an update, a move, a policy change, a link): ErrForbidden
// on an Item they see, and ErrNotFound on one they do not, so the refusal
// never tells that a hidden Item exists. nil for anyone else.
func (k *Service) refuseCustomer(ctx context.Context, p item.Principal, id uuid.UUID) error {
	if policy.CustomerOf(p) == "" {
		return nil
	}
	if _, _, err := k.visibleItem(ctx, p, id); err != nil {
		return err
	}
	return fmt.Errorf("%w: a customer creates, comments and takes the requester's moves, nothing else", ErrForbidden)
}

// checkCustomerCreate holds a customer's create (or an agent's acting for
// one) to what the container admits: a container they see whose Type
// version owns or shares the Type, never the top level, never narrowed,
// and only the Type's customer fields set, never one only the kernel
// writes. nil for anyone else.
func (k *Service) checkCustomerCreate(ctx context.Context, req CreateRequest, ty *typedef.Type) error {
	p := req.Principal
	if policy.CustomerOf(p) == "" {
		return nil
	}
	forbidden := fmt.Errorf("%w: a customer creates only what a container admits", ErrForbidden)
	if req.ContainerID == nil {
		return forbidden
	}
	c, _, err := k.visibleItem(ctx, p, *req.ContainerID)
	if err != nil {
		return err
	}
	ct, err := k.loadType(ctx, k.pool, c.TypeID, c.TypeVersion)
	if err != nil {
		return err
	}
	if !slices.Contains(ct.Own, ty.ID) && !slices.Contains(ct.Shared, ty.ID) {
		return forbidden
	}
	if req.Narrow != nil {
		return fmt.Errorf("%w: a customer never narrows", ErrForbidden)
	}
	var hidden []string
	for _, id := range sortedKeys(req.Fields) {
		f, ok := ty.Field(id)
		if ok && (!ty.CustomerSees(id) || f.WritableBy == typedef.WritableByKernel) {
			hidden = append(hidden, id)
		}
	}
	if len(hidden) > 0 {
		return &FieldNotWritableError{Fields: hidden, Reason: "a customer sets only the Type's customer fields"}
	}
	return nil
}

// requesterRefused is the one refusal of a requester: it never says
// whether the customer exists, or why the container does not admit them.
var requesterRefused = &InvalidError{Path: "requester",
	Reason: "a requester is a customer the container admits, named by a person"}

// checkRequester checks CreateRequest.Requester: named by a user (not an
// agent, not a customer), a customer, in a container whose Type version
// owns the Type, which the customer can see (narrowing included).
func (k *Service) checkRequester(ctx context.Context, req CreateRequest, ty *typedef.Type) error {
	if req.Requester == "" {
		return nil
	}
	p := req.Principal
	if !item.IsKind(p.Actor, item.KindUser) || p.OnBehalfOf != "" || !item.IsKind(req.Requester, item.KindCustomer) ||
		req.ContainerID == nil {
		return requesterRefused
	}
	c, _, err := k.visibleItem(ctx, p, *req.ContainerID)
	if err != nil {
		return err // the caller cannot see the container: as for any create
	}
	ct, err := k.loadType(ctx, k.pool, c.TypeID, c.TypeVersion)
	if err != nil {
		return err
	}
	if !slices.Contains(ct.Own, ty.ID) {
		return requesterRefused
	}
	customer := item.Principal{Actor: req.Requester, Namespace: p.Namespace}
	if _, _, err := k.visibleItem(ctx, customer, *req.ContainerID); errors.Is(err, ErrNotFound) {
		return requesterRefused
	} else if err != nil {
		return err
	}
	return nil
}

// typesFor returns the registered Types as the principal may name their
// fields and links in a filter or an order: everything, or for a customer
// each version with only the fields and link names it lists for them. A
// field or link name a customer cannot see in any version is then refused
// exactly as one nobody declares. Where some versions list it and others
// do not, the rule holds per row (rowListing).
func (k *Service) typesFor(ctx context.Context, p item.Principal) ([]*typedef.Type, error) {
	all, err := k.ListTypes(ctx)
	if err != nil || policy.CustomerOf(p) == "" {
		return all, err
	}
	out := make([]*typedef.Type, 0, len(all))
	for _, t := range all {
		c := *t // a shallow copy: only its field list changes
		c.Fields = slices.DeleteFunc(slices.Clone(t.Fields), func(f typedef.Field) bool { return !t.CustomerSees(f.ID) })
		c.Links = slices.DeleteFunc(slices.Clone(t.Links), func(l typedef.LinkType) bool { return !t.CustomerSees(l.Name) })
		out = append(out, &c)
	}
	return out, nil
}

// forCaller is a write's answer as the caller gets it: a customer, or an
// agent acting for one, never sees commit_seq, a counter of everybody's
// writes (review of 2026-11-21). The idempotency record keeps the real
// value; a replay answers 0 to them too.
func forCaller(p item.Principal, res WriteResult) WriteResult {
	if policy.CustomerOf(p) != "" {
		res.CommitSeq = 0
	}
	return res
}

// checkSeesInTx is ErrNotFound unless p may see the Item now, read in the
// write's transaction after its lock: the scope test and the customer rule
// (seeCondition). It closes the gap between a check made before the
// transaction and the write (review of 2026-11-21).
func (k *Service) checkSeesInTx(ctx context.Context, tx pgx.Tx, p item.Principal, id uuid.UUID) error {
	ok, err := k.seesInTx(ctx, tx, p, id)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotFound
	}
	return nil
}

func (k *Service) seesInTx(ctx context.Context, tx pgx.Tx, p item.Principal, id uuid.UUID) (bool, error) {
	args, err := k.seeArgs(ctx, p)
	if err != nil {
		return false, err
	}
	args["id"] = id
	var ok bool
	err = tx.QueryRow(ctx, `select `+seeCondition("i")+` from items i join scopes s on s.id = i.scope_id where i.id = @id`, args).Scan(&ok)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check visibility: %w", err)
	}
	return ok, nil
}

// rowListing is the one rule for a customer's field filters, order_by and
// link conditions (review of 2026-11-21): a field or link name counts on a
// row only when that row's own Type version lists it for the customer. It
// returns a condition on the Items alias m, true only for such rows, with
// its arguments named after prefix; seen are the Type versions as the
// customer sees them (typesFor), so a version lists the name when it still
// declares it.
func rowListing(seen []*typedef.Type, name, prefix string, args pgx.NamedArgs) string {
	var types []string
	var versions []int32
	for _, t := range seen {
		_, field := t.Field(name)
		_, link := t.Link(name)
		if field || link {
			types, versions = append(types, t.ID), append(versions, int32(t.Version))
		}
	}
	if types == nil {
		types, versions = []string{}, []int32{}
	}
	args[prefix+"_t"], args[prefix+"_v"] = types, versions
	return fmt.Sprintf("(m.type_id, m.type_version) in (select * from unnest(@%s_t::text[], @%s_v::int[]))", prefix, prefix)
}
