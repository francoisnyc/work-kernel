package policy

import (
	"fmt"
	"maps"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// Customers (Jira part 2, step 4a). A customer is a principal of kind
// customer: with fixed permissions, never a role, never in the namespace's
// general audience. What a customer sees is a rule over Items and Types,
// evaluated in SQL beside VisibleCondition, never a reader list per Item:
//
//   - an Item whose Type version owns something (`own`, a portal);
//   - their own request: an Item whose effective creator
//     (created_on_behalf_of, else created_by) is the customer, in a
//     container whose Type version owns the Item's Type;
//   - an Item of a Type its container shares (`shared`), when that
//     container is their own request.
//
// Depth 1, from the current container. On top, the scope test applies as
// for anyone: a narrowed scope the customer does not read still hides.
//
// The rule has two forms of one meaning. CustomerCondition is a predicate
// over one Item row (Get, links, rollups, history references, writes);
// CustomerRelation is the Items a customer may see as a relation, which
// drives their Search and List from the index on the effective creator,
// never a scan of every Item. The property tests hold both to a naive
// evaluator.

// CustomerOf returns the customer the principal is, or an agent acts for;
// "" for everyone else. The kernel's own actors are never a customer: the
// on_behalf_of they carry is attribution only.
func CustomerOf(p item.Principal) string {
	switch {
	case item.IsKind(p.Actor, item.KindCustomer):
		return p.Actor
	case item.IsKind(p.Actor, item.KindAgent) && item.IsKind(p.OnBehalfOf, item.KindCustomer):
		return p.OnBehalfOf
	}
	return ""
}

// CustomerRules is what the registered Types say about customers, as the
// arrays the SQL reads: portals (type, version) whose version owns
// something; own and shared triples (container type, container version,
// child type); and the fields a customer sees (type, version, field), for
// words.
type CustomerRules struct {
	PortalTypes                 []string
	PortalVersions              []int32
	OwnTypes, OwnChildren       []string
	OwnVersions                 []int32
	SharedTypes, SharedChildren []string
	SharedVersions              []int32
	FieldTypes, FieldNames      []string
	FieldVersions               []int32
	portalIDs                   []string
}

// RulesOf reads the customer rules of a set of Type versions.
func RulesOf(types []*typedef.Type) CustomerRules {
	var r CustomerRules
	for _, t := range types {
		v := int32(t.Version)
		if len(t.Own) > 0 {
			r.PortalTypes, r.PortalVersions = append(r.PortalTypes, t.ID), append(r.PortalVersions, v)
			if !slices.Contains(r.portalIDs, t.ID) {
				r.portalIDs = append(r.portalIDs, t.ID)
			}
		}
		for _, c := range t.Own {
			r.OwnTypes, r.OwnVersions, r.OwnChildren = append(r.OwnTypes, t.ID), append(r.OwnVersions, v), append(r.OwnChildren, c)
		}
		for _, c := range t.Shared {
			r.SharedTypes, r.SharedVersions, r.SharedChildren = append(r.SharedTypes, t.ID), append(r.SharedVersions, v), append(r.SharedChildren, c)
		}
		for _, f := range t.CustomerFields {
			if _, ok := t.Field(f); ok {
				r.FieldTypes, r.FieldVersions, r.FieldNames = append(r.FieldTypes, t.ID), append(r.FieldVersions, v), append(r.FieldNames, f)
			}
		}
	}
	return r
}

// CustomerArgs adds the named arguments CustomerCondition and
// CustomerRelation read: @cust (empty for anyone who is not a customer,
// which makes the condition true) and the rule arrays.
func CustomerArgs(p item.Principal, r CustomerRules, args pgx.NamedArgs) {
	args["cust"] = CustomerOf(p)
	// A nil slice is sent as NULL, and unnest(NULL) is no rows: send empty
	// arrays instead.
	orEmpty := func(s []string) []string {
		if s == nil {
			return []string{}
		}
		return s
	}
	orEmpty32 := func(s []int32) []int32 {
		if s == nil {
			return []int32{}
		}
		return s
	}
	args["cr_pt"], args["cr_pv"], args["cr_pids"] = orEmpty(r.PortalTypes), orEmpty32(r.PortalVersions), orEmpty(r.portalIDs)
	args["cr_ot"], args["cr_ov"], args["cr_ok"] = orEmpty(r.OwnTypes), orEmpty32(r.OwnVersions), orEmpty(r.OwnChildren)
	args["cr_st"], args["cr_sv"], args["cr_sk"] = orEmpty(r.SharedTypes), orEmpty32(r.SharedVersions), orEmpty(r.SharedChildren)
	args["cr_ft"], args["cr_fv"], args["cr_ff"] = orEmpty(r.FieldTypes), orEmpty32(r.FieldVersions), orEmpty(r.FieldNames)
}

const (
	portalOf = `(select * from unnest(@cr_pt::text[], @cr_pv::int[]))`
	ownOf    = `(select * from unnest(@cr_ot::text[], @cr_ov::int[], @cr_ok::text[]))`
	sharedOf = `(select * from unnest(@cr_st::text[], @cr_sv::int[], @cr_sk::text[]))`
)

// CustomerCondition is the rule as a predicate over the Item row alias m
// (true for anyone who is not a customer). Use it beside VisibleCondition.
func CustomerCondition(m string) string {
	return fmt.Sprintf(`(@cust = '' or (%[1]s.type_id, %[1]s.type_version) in `+portalOf+`
	or exists (select 1 from items cp where cp.id = %[1]s.container_id and (
	    (coalesce(%[1]s.created_on_behalf_of, %[1]s.created_by) = @cust
	     and (cp.type_id, cp.type_version, %[1]s.type_id) in `+ownOf+`)
	 or (coalesce(cp.created_on_behalf_of, cp.created_by) = @cust
	     and (cp.type_id, cp.type_version, %[1]s.type_id) in `+sharedOf+`
	     and exists (select 1 from items cg where cg.id = cp.container_id
	                 and (cg.type_id, cg.type_version, cp.type_id) in `+ownOf+`)))))`, m)
}

// CustomerRelation is the rule as two CTEs: cust_own, the customer's own
// requests, read from the index on the effective creator (items_requester,
// whose predicate the repeated `like 'customer:%'` matches); and mine,
// those requests, what they share, and the portals. Every column of items,
// so it stands in for items in a page query. Only for a customer
// (@cust set); @ns is the namespace.
const CustomerRelation = `cust_own as (
	    select r.* from items r join items d on d.id = r.container_id
	    where r.namespace_id = @ns and coalesce(r.created_on_behalf_of, r.created_by) = @cust
	      and coalesce(r.created_on_behalf_of, r.created_by) like 'customer:%'
	      and (d.type_id, d.type_version, r.type_id) in ` + ownOf + `
	),
	mine as (
	    select * from cust_own
	    union all
	    select ch.* from items ch join cust_own o on ch.container_id = o.id
	    where (o.type_id, o.type_version, ch.type_id) in ` + sharedOf + `
	    union all
	    select p.* from items p
	    where p.namespace_id = @ns and p.type_id = any(@cr_pids::text[]) and (p.type_id, p.type_version) in ` + portalOf + `
	)`

// CustomerWords is the text of the Item row alias m that a customer sees:
// its customer fields' string values, as a tsvector. A customer's word
// search matches this, never the whole row's search column.
func CustomerWords(m string) string {
	return fmt.Sprintf(`jsonb_to_tsvector('simple', coalesce((select jsonb_object_agg(e.key, e.value) from jsonb_each(%[1]s.fields) e
	    where (%[1]s.type_id, %[1]s.type_version, e.key) in (select * from unnest(@cr_ft::text[], @cr_fv::int[], @cr_ff::text[]))), '{}'::jsonb),
	    '["string"]')`, m)
}

// project returns what a customer sees of an Item of the Type version ty:
// its customer fields, its outgoing links whose name is listed (never a
// restricted one; incoming links are chosen where they are read), its
// listed rollups, no open hold and no etag (content_seq zero). A Type
// version that does not match the Item shows nothing: fail closed.
func projectForCustomer(it item.Item, ty *typedef.Type) (item.Item, bool) {
	if ty == nil || ty.ID != it.TypeID || ty.Version != it.TypeVersion {
		return item.Item{}, false
	}
	out := it
	out.Fields = map[string]any{}
	for k, v := range it.Fields {
		if ty.CustomerSees(k) {
			if _, ok := ty.Field(k); ok {
				out.Fields[k] = v
			}
		}
	}
	out.Links = nil
	for _, l := range it.Links {
		if l.Restricted || (l.Direction == item.LinkOutgoing && !ty.CustomerSees(l.LinkType)) {
			continue
		}
		out.Links = append(out.Links, l)
	}
	if it.Rollups != nil {
		out.Rollups = maps.Clone(it.Rollups)
		maps.DeleteFunc(out.Rollups, func(id string, _ item.Rollup) bool { return !ty.CustomerSees(id) })
		if len(out.Rollups) == 0 {
			out.Rollups = nil
		}
	}
	out.Hold = nil
	out.ContentSeq = 0
	return out, true
}

// MayCustomerTransition answers for a customer, or an agent acting for one:
// only a transition whose allowed names creator, and only as its requester.
func mayCustomerTransition(tr typedef.Transition, who Who, it ItemFacts) bool {
	return slices.Contains(tr.Allowed, typedef.AllowedCreator) &&
		IsRequester(who.Actor, who.OnBehalfOf, it.CreatedBy, it.CreatedOnBehalfOf)
}
