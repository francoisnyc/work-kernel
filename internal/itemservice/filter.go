package itemservice

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// Filter narrows Search and List. Every part that is set must hold; within a
// list, any entry may match. The zero Filter matches everything.
type Filter struct {
	TypeIDs []string // Items of any version of these Types
	States  []string
	// Categories are read in each Item's own Type version, so a category
	// becomes a set of (type, version, state).
	Categories []string
	// Fields needs exactly one Type in TypeIDs, and the field must have one
	// type in every version of it; else ErrInvalid.
	Fields []FieldFilter
	// Links keeps Items linked to or from given Items (see LinkFilter).
	Links []LinkFilter
}

// FieldFilter is a condition on one field: Eq, or a range of one or two
// bounds. nil means "no such bound". Values are as Item.Fields holds them
// (an int field takes any whole number). Ranges are for int fields only.
type FieldFilter struct {
	Field                string
	Eq, Gt, Gte, Lt, Lte any
}

// empty reports whether the filter says nothing.
func (f Filter) empty() bool {
	return len(f.TypeIDs) == 0 && len(f.States) == 0 && len(f.Categories) == 0 && len(f.Fields) == 0 &&
		len(f.Links) == 0
}

// sqlFilter is a Filter checked against the registered Types and turned into
// SQL conditions on the Items alias m, with their named arguments.
type sqlFilter struct {
	conds []string
	args  pgx.NamedArgs
}

// resolveFilter checks f against the registered Types and builds its SQL,
// for the principal p ("me" on a principal field; the Items a link
// condition names must be visible to p). A malformed filter is an
// *InvalidError naming where: filter.types, filter.categories,
// filter.fields[i] or filter.links[i].
func (k *Service) resolveFilter(ctx context.Context, p item.Principal, f Filter) (sqlFilter, error) {
	out := sqlFilter{args: pgx.NamedArgs{}}
	all, err := k.ListTypes(ctx)
	if err != nil {
		return out, err
	}
	versions := func(id string) []*typedef.Type {
		var vs []*typedef.Type
		for _, t := range all {
			if t.ID == id {
				vs = append(vs, t)
			}
		}
		return vs
	}

	for _, id := range f.TypeIDs {
		if len(versions(id)) == 0 {
			return out, &InvalidError{Path: "filter.types", Reason: "unknown Type"}
		}
	}
	// One value is "=", not "= any(...)": Postgres 17 then walks
	// items_namespace_type_state in (created_at, id) order and stops at the
	// page, instead of sorting every match (Step 0, part 2, EXPLAIN).
	out.conds = append(out.conds, oneOrAny("m.type_id", "f_types", f.TypeIDs, out.args)...)
	out.conds = append(out.conds, oneOrAny("m.state", "f_states", f.States, out.args)...)

	// A category is a set of (type, version, state), read in every version
	// of every Type: the same state may be active in v1 and waiting in v2.
	if len(f.Categories) > 0 {
		var ids, states []string
		var vs []int32
		for _, c := range f.Categories {
			if !typedef.ValidCategory(c) {
				return out, &InvalidError{Path: "filter.categories", Reason: "unknown category"}
			}
		}
		for _, t := range all {
			for _, s := range t.States {
				if slices.Contains(f.Categories, s.Category) {
					ids, vs, states = append(ids, t.ID), append(vs, int32(t.Version)), append(states, s.ID)
				}
			}
		}
		out.conds = append(out.conds, `(m.type_id, m.type_version, m.state) in
		    (select * from unnest(@f_cat_types::text[], @f_cat_versions::int[], @f_cat_states::text[]))`)
		out.args["f_cat_types"], out.args["f_cat_versions"], out.args["f_cat_states"] = ids, vs, states
	}

	if len(f.Fields) > 0 && len(f.TypeIDs) != 1 {
		return out, &InvalidError{Path: "filter.types", Reason: "a field filter needs exactly one Type"}
	}
	for i, ff := range f.Fields {
		path := fmt.Sprintf("filter.fields[%d]", i)
		ft, err := fieldTypeAcross(versions(f.TypeIDs[0]), ff.Field)
		if err != nil {
			return out, &InvalidError{Path: path, Reason: err.Error()}
		}
		conds, err := fieldConds(ft, ff, i, out.args, p)
		if err != nil {
			return out, &InvalidError{Path: path, Reason: err.Error()}
		}
		out.conds = append(out.conds, conds...)
	}
	for i, lf := range f.Links {
		cond, err := k.linkCond(ctx, p, all, lf, i, out.args)
		if err != nil {
			return out, err
		}
		out.conds = append(out.conds, cond)
	}
	return out, nil
}

// fieldTypeAcross returns the field's definition, which must be the same
// type in every version that declares it, and declared in at least one.
func fieldTypeAcross(versions []*typedef.Type, id string) (typedef.Field, error) {
	var found *typedef.Field
	for _, t := range versions {
		f, ok := t.Field(id)
		if !ok {
			continue
		}
		if found != nil && found.Type != f.Type {
			// A string in one version and a principal in another is one
			// stored type: it filters as a principal (eq only).
			if !stringOrPrincipal(found.Type) || !stringOrPrincipal(f.Type) {
				return typedef.Field{}, fmt.Errorf("the field has different types across the Type's versions")
			}
			f.Type = typedef.Principal
		}
		if found == nil || len(f.Options) > len(found.Options) || f.Type == typedef.Principal {
			found = &f // for an enum, the widest set of options; a principal wins over a string
		}
	}
	if found == nil {
		return typedef.Field{}, fmt.Errorf("the Type has no such field")
	}
	return *found, nil
}

// fieldConds checks one field filter and returns its SQL conditions, adding
// the values to args under names numbered by the filter's position.
func fieldConds(f typedef.Field, ff FieldFilter, i int, args pgx.NamedArgs, p item.Principal) ([]string, error) {
	type bound struct {
		op string
		v  any
	}
	var bounds []bound
	for _, b := range []bound{{">", ff.Gt}, {">=", ff.Gte}, {"<", ff.Lt}, {"<=", ff.Lte}} {
		if b.v != nil {
			bounds = append(bounds, b)
		}
	}
	switch {
	case ff.Eq == nil && len(bounds) == 0:
		return nil, fmt.Errorf("a field filter needs eq or a bound")
	case ff.Eq != nil && len(bounds) > 0:
		return nil, fmt.Errorf("eq and a bound do not go together")
	case ff.Gt != nil && ff.Gte != nil, ff.Lt != nil && ff.Lte != nil:
		return nil, fmt.Errorf("one lower bound and one upper bound at most")
	case len(bounds) > 0 && f.Type != typedef.Int:
		return nil, fmt.Errorf("ranges are for int fields only")
	}
	// On a principal field "me" is the caller: the user an agent acts for,
	// else the actor itself.
	if f.Type == typedef.Principal && ff.Eq == "me" {
		ff.Eq = p.Actor
		if p.OnBehalfOf != "" {
			ff.Eq = p.OnBehalfOf
		}
	}

	// The field's ID is a Type's stable ID, passed as an argument, never
	// spliced into the SQL.
	fieldArg := fmt.Sprintf("f_field_%d", i)
	args[fieldArg] = ff.Field
	value := func(v any) (any, error) {
		switch f.Type {
		case typedef.Int:
			n, ok := typedef.AsInt(v)
			if !ok {
				return nil, fmt.Errorf("want a whole number")
			}
			return n, nil
		case typedef.Bool:
			b, ok := v.(bool)
			if !ok {
				return nil, fmt.Errorf("want true or false")
			}
			return b, nil
		case typedef.Enum:
			s, ok := v.(string)
			if !ok || !slices.Contains(f.Options, s) {
				return nil, fmt.Errorf("want one of the field's options")
			}
			return s, nil
		case typedef.Principal:
			s, ok := v.(string)
			if !ok || !item.IsKind(s, item.KindUser, item.KindAgent) {
				return nil, fmt.Errorf("want user:<id>, agent:<id> or me")
			}
			return s, nil
		default:
			s, ok := v.(string)
			if !ok {
				return nil, fmt.Errorf("want a string")
			}
			return s, nil
		}
	}
	// How the stored JSON value is read for comparison, by field type.
	read := map[typedef.FieldType]string{
		// CASE, because SQL does not promise to test a guard before a cast.
		typedef.Int: "(case when jsonb_typeof(m.fields -> @" + fieldArg + ") = 'number' " +
			"then (m.fields ->> @" + fieldArg + ")::numeric end)",
		typedef.Bool:      "(m.fields ->> @" + fieldArg + ")::boolean",
		typedef.Enum:      "(m.fields ->> @" + fieldArg + ")",
		typedef.String:    "(m.fields ->> @" + fieldArg + ")",
		typedef.Principal: "(m.fields ->> @" + fieldArg + ")",
	}[f.Type]
	cast := map[typedef.FieldType]string{
		typedef.Int: "::numeric", typedef.Bool: "::boolean", typedef.Enum: "::text", typedef.String: "::text",
		typedef.Principal: "::text",
	}[f.Type]

	if ff.Eq != nil {
		bounds = []bound{{"=", ff.Eq}}
	}
	var conds []string
	for j, b := range bounds {
		v, err := value(b.v)
		if err != nil {
			return nil, err
		}
		arg := fmt.Sprintf("f_value_%d_%d", i, j)
		args[arg] = v
		conds = append(conds, fmt.Sprintf("%s %s @%s%s", read, b.op, arg, cast))
	}
	return []string{"(" + strings.Join(conds, " and ") + ")"}, nil
}

// oneOrAny is the condition that column is one of values: "=" for one
// value, "= any" for more, nothing for none.
func oneOrAny(column, arg string, values []string, args pgx.NamedArgs) []string {
	switch len(values) {
	case 0:
		return nil
	case 1:
		args[arg] = values[0]
		return []string{column + " = @" + arg}
	}
	args[arg] = values
	return []string{column + " = any(@" + arg + ")"}
}

func stringOrPrincipal(t typedef.FieldType) bool {
	return t == typedef.String || t == typedef.Principal
}

// linkCond checks one link condition and returns its SQL on the Items alias
// m. Outgoing: m is the source of a link named lf.LinkType to the Item.
// Incoming: m is the target of a link from the Item whose declared name has
// lf.LinkType as its inverse in the Item's Type. A name no registered Type
// declares in that direction is refused: Types only grow, so a refused name
// can only start working later, never stop working. The Item must be one
// the principal sees (else ErrNotFound, like a missing one); naming it in a
// filter records no read.
func (k *Service) linkCond(ctx context.Context, p item.Principal, all []*typedef.Type, lf LinkFilter, i int,
	args pgx.NamedArgs) (string, error) {
	path := fmt.Sprintf("filter.links[%d]", i)
	if lf.LinkType == "" || lf.ItemID == uuid.Nil {
		return "", &InvalidError{Path: path, Reason: "a link condition needs a link type and an Item"}
	}
	known := false
	var declared []string // incoming: the declared names whose inverse is lf.LinkType
	for _, t := range all {
		for _, l := range t.Links {
			switch {
			case lf.Direction == item.LinkOutgoing && l.Name == lf.LinkType:
				known = true
			case lf.Direction == item.LinkIncoming && l.Inverse == lf.LinkType:
				known = true
				if !slices.Contains(declared, t.ID+"\x00"+l.Name) {
					declared = append(declared, t.ID+"\x00"+l.Name)
				}
			}
		}
	}
	if lf.Direction != item.LinkOutgoing && lf.Direction != item.LinkIncoming {
		return "", &InvalidError{Path: path, Reason: "a link condition needs a direction, outgoing or incoming"}
	}
	if !known {
		return "", &InvalidError{Path: path, Reason: "no Type declares that link name in that direction"}
	}
	if _, _, err := k.visibleItem(ctx, p, lf.ItemID); err != nil {
		return "", err
	}
	itemArg, nameArg := fmt.Sprintf("f_link_item_%d", i), fmt.Sprintf("f_link_name_%d", i)
	args[itemArg] = lf.ItemID
	if lf.Direction == item.LinkOutgoing {
		args[nameArg] = lf.LinkType
		return fmt.Sprintf(`m.id in (select l.source_id from links l where l.target_id = @%s and l.link_type = @%s)`,
			itemArg, nameArg), nil
	}
	var types, names []string
	for _, d := range declared {
		t, n, _ := strings.Cut(d, "\x00")
		types, names = append(types, t), append(names, n)
	}
	typesArg := fmt.Sprintf("f_link_types_%d", i)
	args[typesArg], args[nameArg] = types, names
	return fmt.Sprintf(`m.id in (select l.target_id from links l where l.source_id = @%s
	    and (l.source_type_id, l.link_type) in (select * from unnest(@%s::text[], @%s::text[])))`,
		itemArg, typesArg, nameArg), nil
}
