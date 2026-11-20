package api

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	kernelv1 "github.com/francoisnyc/work-kernel/gen/kernel/v1"
	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// Each handler checks the request's shape first (INVALID_ARGUMENT naming the
// field), then that every name is in the caller's namespace (else NOT_FOUND,
// like a hidden Item), then asks the kernel. A request refused before the
// kernel uses no request_id.

// inNamespace reads an Item name in the caller's namespace. A malformed name
// is INVALID_ARGUMENT on field; a name in another namespace is NOT_FOUND.
func inNamespace(p item.Principal, name, field string) (uuid.UUID, *connect.Error) {
	ns, id, ok := parseItemName(name)
	if !ok {
		return uuid.Nil, badField(field, "want namespaces/{ns}/items/{item}")
	}
	if ns != p.Namespace {
		return uuid.Nil, notFound()
	}
	return id, nil
}

// parentNamespace checks a parent (namespaces/{ns}) names the caller's
// namespace.
func parentNamespace(p item.Principal, parent string) *connect.Error {
	ns, ok := parseNamespace(parent)
	if !ok {
		return badField("parent", "want namespaces/{ns}")
	}
	if ns != p.Namespace {
		return notFound()
	}
	return nil
}

// optionalItem reads an optional Item name: empty is nil.
func optionalItem(p item.Principal, name, field string) (*uuid.UUID, *connect.Error) {
	if name == "" {
		return nil, nil
	}
	id, cerr := inNamespace(p, name, field)
	if cerr != nil {
		return nil, cerr
	}
	return &id, nil
}

// view is an Item as the caller sees it, for the wire. Only an open hold is
// shown; past holds belong to history.
func (s *server) view(ctx context.Context, p item.Principal, it item.Item) (*kernelv1.Item, error) {
	ty, err := s.k.Type(ctx, it.TypeID, it.TypeVersion)
	if err != nil {
		return nil, err
	}
	fields, err := fieldsOut(ty, it.Fields)
	if err != nil {
		return nil, err
	}
	name := itemName(p.Namespace, it.ID)
	out := &kernelv1.Item{
		Name:              name,
		Type:              typeName(p.Namespace, it.TypeID, it.TypeVersion),
		State:             it.State,
		Fields:            fields,
		Etag:              etagOf(it),
		Creator:           it.CreatedBy,
		CreatorOnBehalfOf: it.CreatedOnBehalfOf,
	}
	if !it.CreatedAt.IsZero() {
		out.CreateTime = timestamppb.New(it.CreatedAt)
	}
	// The category is the Type version's, for the state the Item is in.
	if st, ok := ty.State(it.State); ok {
		out.Category = categories[st.Category]
	}
	if it.ContainerID != nil {
		out.Container = itemName(p.Namespace, *it.ContainerID)
	}
	if len(it.Links) > 0 {
		out.Links = linksOut(p.Namespace, it.Links)
	}
	out.LinksTruncated = it.LinksTruncated
	if h := it.Hold; h != nil && h.Status == item.HoldOpen {
		out.Hold = &kernelv1.Hold{
			Name: holdName(name, h.ID), Check: h.Hook, FromState: h.From, RequestedState: h.To,
		}
	}
	return out, nil
}

// current reads the Item as the caller sees it now, for the wire: a write's
// answer. It carries no rollups; only GetItem does.
func (s *server) current(ctx context.Context, p item.Principal, id uuid.UUID) (*kernelv1.Item, error) {
	v, _, err := s.read(ctx, p, id)
	return v, err
}

// read is current, with the Item as the kernel returned it.
func (s *server) read(ctx context.Context, p item.Principal, id uuid.UUID) (*kernelv1.Item, item.Item, error) {
	it, err := s.k.Get(ctx, p, id)
	if err != nil {
		return nil, item.Item{}, errorFor(err, itemName(p.Namespace, id))
	}
	v, err := s.view(ctx, p, it)
	if err != nil {
		return nil, item.Item{}, errorFor(err, "")
	}
	return v, it, nil
}

// rollupsOut is an Item's rollups for the wire, as the kernel counted them
// for the caller.
func rollupsOut(rollups map[string]item.Rollup) map[string]*kernelv1.RollupValue {
	if len(rollups) == 0 {
		return nil
	}
	out := make(map[string]*kernelv1.RollupValue, len(rollups))
	for id, r := range rollups {
		out[id] = &kernelv1.RollupValue{Counts: r.Counts}
	}
	return out
}

// itemType reads the Item the caller names and its Type: an update needs the
// Type to read the request's field values. An Item the caller cannot see is
// NOT_FOUND here, before anything else about the request is looked at.
func (s *server) itemType(ctx context.Context, p item.Principal, id uuid.UUID) (*typedef.Type, error) {
	it, err := s.k.Get(ctx, p, id)
	if err != nil {
		return nil, errorFor(err, "")
	}
	ty, err := s.k.Type(ctx, it.TypeID, it.TypeVersion)
	if err != nil {
		return nil, errorFor(err, "")
	}
	return ty, nil
}

// The etag is the Item's content_seq, which moves only when something a
// caller could overwrite changes (itemservice, content.go), written as
// "c7": opaque to clients, and not a number, so that nobody takes it for
// item_seq.
const etagPrefix = "c"

func etagOf(it item.Item) string {
	return etagPrefix + strconv.FormatInt(it.ContentSeq, 10)
}

// etagSeq reads an etag: empty is no check (0). An etag this server never
// made cannot match the Item: STALE, like any other mismatch. Only the
// exact text etagOf writes is read: no sign, no leading zero, no spaces; a
// decimal etag from before the etag step is STALE too.
func etagSeq(etag string) (int64, *connect.Error) {
	if etag == "" {
		return 0, nil
	}
	digits, ok := strings.CutPrefix(etag, etagPrefix)
	n, err := strconv.ParseInt(digits, 10, 64)
	if !ok || err != nil || n < 1 || etagPrefix+strconv.FormatInt(n, 10) != etag {
		return 0, errorFor(fmt.Errorf("%w: etag %q does not match the Item", itemservice.ErrStale, etag), "")
	}
	return n, nil
}

func (s *server) CreateItem(ctx context.Context, req *connect.Request[kernelv1.CreateItemRequest]) (*connect.Response[kernelv1.CreateItemResponse], error) {
	p, m := caller(ctx), req.Msg
	if cerr := parentNamespace(p, m.Parent); cerr != nil {
		return nil, cerr
	}
	if m.RequestId == "" {
		return nil, badField("request_id", "required")
	}
	ns, typeID, version, ok := parseTypeName(m.Type)
	if !ok {
		return nil, badField("type", "want namespaces/{ns}/types/{type}/versions/{n}")
	}
	if ns != p.Namespace {
		return nil, notFound()
	}
	container, cerr := optionalItem(p, m.Container, "container")
	if cerr != nil {
		return nil, cerr
	}
	ty, err := s.k.Type(ctx, typeID, version)
	if errors.Is(err, itemservice.ErrInvalid) { // Type's only refusal: not registered
		return nil, badField("type", fmt.Sprintf("type %s v%d is not registered", typeID, version))
	}
	if err != nil {
		return nil, errorFor(err, "")
	}
	fields, cerr := fieldsIn(ty, m.Fields)
	if cerr != nil {
		return nil, cerr
	}
	kreq := itemservice.CreateRequest{
		IdempotencyKey: m.RequestId, Principal: p, TypeID: typeID, TypeVersion: version,
		Fields: fields, ContainerID: container,
	}
	if m.Narrow != nil {
		kreq.Narrow = &itemservice.Narrow{Readers: m.Narrow.Principals}
	}
	res, err := s.k.Create(ctx, kreq)
	if err != nil {
		return nil, errorWith(err, "", "narrow.principals", p.Namespace)
	}
	it, err := s.current(ctx, p, res.ItemID)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&kernelv1.CreateItemResponse{
		Item: it, ItemSeq: res.ItemSeq, CommitSeq: res.CommitSeq,
	}), nil
}

func (s *server) GetItem(ctx context.Context, req *connect.Request[kernelv1.GetItemRequest]) (*connect.Response[kernelv1.GetItemResponse], error) {
	p := caller(ctx)
	id, cerr := inNamespace(p, req.Msg.Name, "name")
	if cerr != nil {
		return nil, cerr
	}
	it, read, err := s.read(ctx, p, id)
	if err != nil {
		return nil, err
	}
	// The values the Type computes on read: on GetItem only.
	it.Rollups = rollupsOut(read.Rollups)
	return connect.NewResponse(&kernelv1.GetItemResponse{Item: it}), nil
}

func (s *server) UpdateItem(ctx context.Context, req *connect.Request[kernelv1.UpdateItemRequest]) (*connect.Response[kernelv1.UpdateItemResponse], error) {
	p, m := caller(ctx), req.Msg
	id, cerr := inNamespace(p, m.Name, "name")
	if cerr != nil {
		return nil, cerr
	}
	if m.RequestId == "" {
		return nil, badField("request_id", "required")
	}
	if (m.Fields == nil || len(m.Fields.Fields) == 0) && m.Policy == nil && m.Move == nil {
		return nil, badField("fields", "an update changes fields, policy or the container")
	}
	if m.Policy != nil && m.Move != nil {
		return nil, badField("move", "a policy change and a move are separate updates")
	}
	kreq := itemservice.UpdateRequest{IdempotencyKey: m.RequestId, Principal: p, ItemID: id}
	if m.Policy != nil {
		// A type switch on the oneof: one case per alternative.
		switch c := m.Policy.Change.(type) {
		case *kernelv1.PolicyChange_Narrow:
			kreq.Policy = &itemservice.PolicyPatch{Readers: c.Narrow.GetPrincipals()}
		case *kernelv1.PolicyChange_Inherit:
			if !c.Inherit {
				return nil, badField("policy", "inherit must be true; to change readers, narrow")
			}
			kreq.Policy = &itemservice.PolicyPatch{Inherit: true}
		default:
			return nil, badField("policy", "narrow or inherit")
		}
	}
	if m.Move != nil {
		to, cerr := optionalItem(p, m.Move.Container, "move.container")
		if cerr != nil {
			return nil, cerr
		}
		kreq.Move = &itemservice.Move{To: to, ConfirmWiden: m.Move.ConfirmWiden}
	}

	ty, err := s.itemType(ctx, p, id)
	if err != nil {
		return nil, err
	}
	if kreq.ExpectedContentSeq, cerr = etagSeq(m.Etag); cerr != nil {
		return nil, cerr
	}
	if kreq.Fields, cerr = fieldsIn(ty, m.Fields); cerr != nil {
		return nil, cerr
	}
	res, err := s.k.Update(ctx, kreq)
	if err != nil {
		return nil, errorWith(err, m.Name, "policy.narrow.principals", p.Namespace)
	}
	it, err := s.current(ctx, p, id)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&kernelv1.UpdateItemResponse{
		Item: it, ItemSeq: res.ItemSeq, CommitSeq: res.CommitSeq,
	}), nil
}

var outcomes = map[itemservice.Outcome]kernelv1.TransitionOutcome{
	itemservice.OutcomeApplied: kernelv1.TransitionOutcome_TRANSITION_OUTCOME_APPLIED,
	itemservice.OutcomeDenied:  kernelv1.TransitionOutcome_TRANSITION_OUTCOME_DENIED,
	itemservice.OutcomePending: kernelv1.TransitionOutcome_TRANSITION_OUTCOME_PENDING,
	itemservice.OutcomeError:   kernelv1.TransitionOutcome_TRANSITION_OUTCOME_ERROR,
}

func (s *server) TransitionItem(ctx context.Context, req *connect.Request[kernelv1.TransitionItemRequest]) (*connect.Response[kernelv1.TransitionItemResponse], error) {
	p, m := caller(ctx), req.Msg
	id, cerr := inNamespace(p, m.Name, "name")
	if cerr != nil {
		return nil, cerr
	}
	if m.RequestId == "" {
		return nil, badField("request_id", "required")
	}
	if m.ToState == "" {
		return nil, badField("to_state", "required")
	}
	wait := m.Wait.AsDuration() // a nil Duration is zero
	if m.Wait != nil && (m.Wait.CheckValid() != nil || wait < 0 || wait > maxWait) {
		return nil, badField("wait", fmt.Sprintf("between 0 and %v", maxWait))
	}

	ty, err := s.itemType(ctx, p, id)
	if err != nil {
		return nil, err
	}
	kreq := itemservice.TransitionRequest{
		IdempotencyKey: m.RequestId, Principal: p, ItemID: id, ToState: m.ToState, Wait: wait,
	}
	if kreq.ExpectedContentSeq, cerr = etagSeq(m.Etag); cerr != nil {
		return nil, cerr
	}
	if kreq.Fields, cerr = fieldsIn(ty, m.Fields); cerr != nil {
		return nil, cerr
	}
	res, err := s.k.Transition(ctx, kreq)
	if err != nil {
		return nil, errorFor(err, m.Name)
	}
	it, err := s.current(ctx, p, id)
	if err != nil {
		return nil, err
	}
	out := &kernelv1.TransitionItemResponse{
		Item: it, ItemSeq: res.ItemSeq, CommitSeq: res.CommitSeq,
		Outcome: outcomes[res.Outcome], Reason: strings.ToUpper(res.Reason),
	}
	if res.HoldID != uuid.Nil {
		out.Hold = holdName(it.Name, res.HoldID)
	}
	return connect.NewResponse(out), nil
}

func (s *server) SearchItems(ctx context.Context, req *connect.Request[kernelv1.SearchItemsRequest]) (*connect.Response[kernelv1.SearchItemsResponse], error) {
	p, m := caller(ctx), req.Msg
	if cerr := parentNamespace(p, m.Parent); cerr != nil {
		return nil, cerr
	}
	filter, cerr := s.filterIn(ctx, p, m.Filter)
	if cerr != nil {
		return nil, cerr
	}
	if strings.TrimSpace(m.Query) == "" && filter.empty() {
		return nil, badField("query", "required: words, a filter or both")
	}
	if m.MinSeq < 0 {
		return nil, badField("min_seq", "cannot be negative")
	}
	if m.PageSize < 0 {
		return nil, badField("page_size", "cannot be negative")
	}
	order, cerr := orderByIn(m.OrderBy)
	if cerr != nil {
		return nil, cerr
	}
	res, err := s.k.Search(ctx, itemservice.SearchRequest{
		Principal: p, Query: m.Query, MinSeq: m.MinSeq, Wait: s.searchWait,
		Filter: filter.Filter, Limit: int(m.PageSize), PageToken: m.PageToken, OrderBy: order,
	})
	if err != nil {
		return nil, errorIn(err, p.Namespace, "")
	}
	items, err := s.views(ctx, p, res.Items)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&kernelv1.SearchItemsResponse{Items: items, NextPageToken: res.NextPageToken}), nil
}

// views is a page of Items for the wire.
func (s *server) views(ctx context.Context, p item.Principal, its []item.Item) ([]*kernelv1.Item, error) {
	out := make([]*kernelv1.Item, 0, len(its))
	for _, it := range its {
		v, err := s.view(ctx, p, it)
		if err != nil {
			return nil, errorFor(err, "")
		}
		out = append(out, v)
	}
	return out, nil
}
