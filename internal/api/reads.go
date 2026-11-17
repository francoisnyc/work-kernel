package api

import (
	"context"
	"strconv"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	kernelv1 "github.com/francoisnyc/work-kernel/gen/kernel/v1"
	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// filter is an ItemFilter read for the kernel.
type filter struct{ itemservice.Filter }

func (f filter) empty() bool {
	return len(f.TypeIDs) == 0 && len(f.States) == 0 && len(f.Categories) == 0 && len(f.Fields) == 0 &&
		len(f.Links) == 0
}

// parseTypeID reads a Type's name without a version: namespaces/{ns}/types/{type}.
func parseTypeID(name string) (ns, typeID string, ok bool) {
	parts, ok := segments(name, "namespaces", "", "types", "")
	if !ok || !itemservice.ValidNamespace(parts[1]) {
		return "", "", false
	}
	return parts[1], parts[3], true
}

// filterIn reads an ItemFilter. Type names must be in the caller's
// namespace (else NOT_FOUND, like any other name); values are plain JSON
// read against the field's type, an int as a string or a number. The kernel
// checks the rest (an unknown Type or field, a range on a string).
func (s *server) filterIn(ctx context.Context, p item.Principal, f *kernelv1.ItemFilter) (filter, *connect.Error) {
	var out filter
	if f == nil {
		return out, nil
	}
	for _, name := range f.Types {
		ns, id, ok := parseTypeID(name)
		if !ok {
			return out, badField("filter.types", "want namespaces/{ns}/types/{type}, without a version")
		}
		if ns != p.Namespace {
			return out, notFound()
		}
		out.TypeIDs = append(out.TypeIDs, id)
	}
	out.States = f.States
	for _, c := range f.Categories {
		name, ok := categoryNames[c]
		if !ok {
			return out, badField("filter.categories", "want a state category")
		}
		out.Categories = append(out.Categories, name)
	}

	// An int field's values come as strings or numbers; which fields are
	// ints is the Type's to say, in any of its versions.
	var types []*typedef.Type
	if len(f.Fields) > 0 && len(out.TypeIDs) == 1 {
		all, err := s.k.ListTypes(ctx)
		if err != nil {
			return out, errorFor(err, "")
		}
		for _, t := range all {
			if t.ID == out.TypeIDs[0] {
				types = append(types, t)
			}
		}
	}
	isInt := func(field string) bool {
		for _, t := range types {
			if fd, ok := t.Field(field); ok && fd.Type == typedef.Int {
				return true
			}
		}
		return false
	}
	for i, ff := range f.Fields {
		path := "filter.fields[" + strconv.Itoa(i) + "]"
		if ff.Field == "" {
			return out, badField(path+".field", "required")
		}
		kf := itemservice.FieldFilter{Field: ff.Field}
		for _, b := range []struct {
			in  *structpb.Value
			out *any
		}{{ff.Eq, &kf.Eq}, {ff.Gt, &kf.Gt}, {ff.Gte, &kf.Gte}, {ff.Lt, &kf.Lt}, {ff.Lte, &kf.Lte}} {
			if b.in == nil {
				continue
			}
			v, ok := valueIn(b.in, isInt(ff.Field))
			if !ok {
				return out, badField(path, "want a whole number")
			}
			*b.out = v
		}
		out.Fields = append(out.Fields, kf)
	}
	for i, lf := range f.Links {
		path := "filter.links[" + strconv.Itoa(i) + "]"
		id, cerr := inNamespace(p, lf.Item, path+".item")
		if cerr != nil {
			return out, cerr
		}
		// An unspecified direction reaches the kernel as none: it names the
		// condition it refuses.
		out.Links = append(out.Links, itemservice.LinkFilter{
			LinkType: lf.LinkType, Direction: directionsIn[lf.Direction], ItemID: id,
		})
	}
	return out, nil
}

// valueIn reads one filter value: for an int field, a numeric string or a
// whole number becomes an int64; anything else passes as plain JSON.
func valueIn(v *structpb.Value, isInt bool) (any, bool) {
	if !isInt {
		return v.AsInterface(), true
	}
	switch x := v.Kind.(type) {
	case *structpb.Value_StringValue:
		n, err := strconv.ParseInt(x.StringValue, 10, 64)
		return n, err == nil
	case *structpb.Value_NumberValue:
		n, ok := typedef.AsInt(x.NumberValue)
		return n, ok
	}
	return nil, false
}

// categoryNames maps the wire's categories back to the Type's names.
var categoryNames = func() map[kernelv1.StateCategory]string {
	m := make(map[kernelv1.StateCategory]string, len(categories))
	for name, c := range categories {
		m[c] = name
	}
	return m
}()

func (s *server) ListItems(ctx context.Context, req *connect.Request[kernelv1.ListItemsRequest]) (*connect.Response[kernelv1.ListItemsResponse], error) {
	p, m := caller(ctx), req.Msg
	// The parent is a container's name, or the namespace for the top level.
	var container *itemservice.ListRequest
	if ns, ok := parseNamespace(m.Parent); ok {
		if ns != p.Namespace {
			return nil, notFound()
		}
		container = &itemservice.ListRequest{}
	} else {
		id, cerr := inNamespace(p, m.Parent, "parent")
		if cerr != nil {
			return nil, badFieldOr(cerr, "parent", "want namespaces/{ns} or namespaces/{ns}/items/{item}")
		}
		container = &itemservice.ListRequest{ContainerID: &id}
	}
	if m.PageSize < 0 {
		return nil, badField("page_size", "cannot be negative")
	}
	filter, cerr := s.filterIn(ctx, p, m.Filter)
	if cerr != nil {
		return nil, cerr
	}
	res, err := s.k.List(ctx, itemservice.ListRequest{
		Principal: p, ContainerID: container.ContainerID, Filter: filter.Filter,
		Limit: int(m.PageSize), PageToken: m.PageToken,
	})
	if err != nil {
		return nil, errorIn(err, p.Namespace, "")
	}
	items, err := s.views(ctx, p, res.Items)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&kernelv1.ListItemsResponse{Items: items, NextPageToken: res.NextPageToken}), nil
}

// badFieldOr keeps a NOT_FOUND (a name in another namespace) and replaces
// a malformed-name error with a fuller description of what the field takes.
func badFieldOr(cerr *connect.Error, field, description string) *connect.Error {
	if cerr.Code() == connect.CodeNotFound {
		return cerr
	}
	return badField(field, description)
}

// eventKinds maps the kernel's ops to the wire's kinds.
var eventKinds = map[string]kernelv1.ItemEventKind{
	"create":               kernelv1.ItemEventKind_ITEM_EVENT_KIND_CREATED,
	"update":               kernelv1.ItemEventKind_ITEM_EVENT_KIND_UPDATED,
	"transition":           kernelv1.ItemEventKind_ITEM_EVENT_KIND_TRANSITIONED,
	"transition_requested": kernelv1.ItemEventKind_ITEM_EVENT_KIND_TRANSITION_REQUESTED,
	"transition_denied":    kernelv1.ItemEventKind_ITEM_EVENT_KIND_TRANSITION_DENIED,
	"transition_failed":    kernelv1.ItemEventKind_ITEM_EVENT_KIND_TRANSITION_FAILED,
	"hook_failed":          kernelv1.ItemEventKind_ITEM_EVENT_KIND_HOOK_FAILED,
	"link_added":           kernelv1.ItemEventKind_ITEM_EVENT_KIND_LINK_ADDED,
	"link_removed":         kernelv1.ItemEventKind_ITEM_EVENT_KIND_LINK_REMOVED,
	"reaction_failed":      kernelv1.ItemEventKind_ITEM_EVENT_KIND_REACTION_FAILED,
	"hook_ran":             kernelv1.ItemEventKind_ITEM_EVENT_KIND_HOOK_RAN,
	"reaction_applied":     kernelv1.ItemEventKind_ITEM_EVENT_KIND_REACTION_APPLIED,
}

func (s *server) ListItemEvents(ctx context.Context, req *connect.Request[kernelv1.ListItemEventsRequest]) (*connect.Response[kernelv1.ListItemEventsResponse], error) {
	p, m := caller(ctx), req.Msg
	id, cerr := inNamespace(p, m.Parent, "parent")
	if cerr != nil {
		return nil, cerr
	}
	if m.PageSize < 0 {
		return nil, badField("page_size", "cannot be negative")
	}
	res, err := s.k.ListEvents(ctx, itemservice.ListEventsRequest{
		Principal: p, ItemID: id, Limit: int(m.PageSize), PageToken: m.PageToken,
	})
	if err != nil {
		return nil, errorIn(err, p.Namespace, m.Parent)
	}
	ty, err := s.k.Type(ctx, res.TypeID, res.TypeVersion)
	if err != nil {
		return nil, errorFor(err, "")
	}
	out := &kernelv1.ListItemEventsResponse{NextPageToken: res.NextPageToken}
	for _, e := range res.Events {
		ev, err := eventOut(p.Namespace, m.Parent, ty, e)
		if err != nil {
			return nil, errorFor(err, "")
		}
		out.Events = append(out.Events, ev)
	}
	return connect.NewResponse(out), nil
}

// eventOut is one event for the wire. Values are read against the Item's
// Type (an int as a string); a cleared field is null.
func eventOut(ns, item string, ty *typedef.Type, e itemservice.Event) (*kernelv1.ItemEvent, error) {
	out := &kernelv1.ItemEvent{
		Name: item + "/events/" + strconv.FormatInt(e.ItemSeq, 10), ItemSeq: e.ItemSeq, CommitSeq: e.CommitSeq,
		Kind: eventKinds[e.Op], CreateTime: timestamppb.New(e.CreatedAt),
		Actor: e.Actor, OnBehalfOf: e.OnBehalfOf, OriginatingHook: e.OriginatingHook,
		CausationId: e.CausationID, CorrelationId: e.CorrelationID, Hop: int32(e.Hop),
		FromState: e.From, ToState: e.To, RequestedState: e.RequestedTo, Hook: e.Hook, Reaction: e.Reaction,
		Reason: strings.ToUpper(e.Reason), ErasedFields: e.Erased, Inherit: e.Inherit, Moved: e.Moved,
		Timer: e.Timer, Run: int32(e.Run),
	}
	if e.TypeID != "" {
		out.Type = typeName(ns, e.TypeID, e.TypeVersion)
	}
	if e.HoldID != nil {
		out.Hold = holdName(item, *e.HoldID)
	}
	if len(e.Values) > 0 {
		f, err := fieldsOut(ty, e.Values)
		if err != nil {
			return nil, err
		}
		out.Fields = f
	}
	if len(e.Added) > 0 {
		a, err := fieldsOut(ty, e.Added) // an amount reads like the field: an int as a string
		if err != nil {
			return nil, err
		}
		out.Added = a
	}
	if e.Readers != nil {
		out.Readers = &kernelv1.Readers{Principals: e.Readers}
	}
	ref := func(r *itemservice.ItemRef) *kernelv1.ItemRef {
		switch {
		case r == nil:
			return nil
		case r.Restricted:
			return &kernelv1.ItemRef{Restricted: true}
		}
		return &kernelv1.ItemRef{Name: itemName(ns, r.ID)}
	}
	out.Container, out.ContainerFrom, out.ContainerTo = ref(e.Container), ref(e.ContainerFrom), ref(e.ContainerTo)
	out.LinkType, out.LinkTarget, out.LinkReplaced = e.LinkType, ref(e.LinkTarget), ref(e.LinkReplaced)
	return out, nil
}
