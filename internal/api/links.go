package api

import (
	"context"

	"connectrpc.com/connect"

	kernelv1 "github.com/francoisnyc/work-kernel/gen/kernel/v1"
	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
)

// Links (proof of concept, Step 1). A link is added or removed on its
// source: the event, the item_seq and the etag are the source's. The answer
// is the source Item after the write, its links included.

// linkRequest is LinkItems and UnlinkItems: the same request.
type linkRequest interface {
	GetName() string
	GetRequestId() string
	GetEtag() string
	GetLinkType() string
	GetTarget() string
}

// linkIn checks a link request's shape and names, and builds the kernel's
// request. A name in another namespace is NOT_FOUND, like a hidden Item.
func linkIn(p item.Principal, m linkRequest) (itemservice.LinkRequest, *connect.Error) {
	src, cerr := inNamespace(p, m.GetName(), "name")
	if cerr != nil {
		return itemservice.LinkRequest{}, cerr
	}
	if m.GetRequestId() == "" {
		return itemservice.LinkRequest{}, badField("request_id", "required")
	}
	if m.GetLinkType() == "" {
		return itemservice.LinkRequest{}, badField("link_type", "required: a link type the source's Type declares")
	}
	dst, cerr := inNamespace(p, m.GetTarget(), "target")
	if cerr != nil {
		return itemservice.LinkRequest{}, cerr
	}
	seq, cerr := etagSeq(m.GetEtag())
	if cerr != nil {
		return itemservice.LinkRequest{}, cerr
	}
	return itemservice.LinkRequest{
		IdempotencyKey: m.GetRequestId(), Principal: p, SourceID: src, LinkType: m.GetLinkType(),
		TargetID: dst, ExpectedItemSeq: seq,
	}, nil
}

// linkWrite runs Link or Unlink and reads the source after it.
func (s *server) linkWrite(ctx context.Context, m linkRequest,
	op func(context.Context, itemservice.LinkRequest) (itemservice.WriteResult, error)) (*kernelv1.Item, itemservice.WriteResult, error) {
	p := caller(ctx)
	kreq, cerr := linkIn(p, m)
	if cerr != nil {
		return nil, itemservice.WriteResult{}, cerr
	}
	res, err := op(ctx, kreq)
	if err != nil {
		return nil, itemservice.WriteResult{}, errorIn(err, p.Namespace, m.GetName())
	}
	it, err := s.current(ctx, p, kreq.SourceID)
	if err != nil {
		return nil, itemservice.WriteResult{}, err
	}
	return it, res, nil
}

// LinkItems adds a link from the source Item (name) to a target.
func (s *server) LinkItems(ctx context.Context,
	req *connect.Request[kernelv1.LinkItemsRequest]) (*connect.Response[kernelv1.LinkItemsResponse], error) {
	it, res, err := s.linkWrite(ctx, req.Msg, s.k.Link)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&kernelv1.LinkItemsResponse{Item: it, ItemSeq: res.ItemSeq, CommitSeq: res.CommitSeq}), nil
}

// UnlinkItems removes a link from the source Item (name) to a target.
func (s *server) UnlinkItems(ctx context.Context,
	req *connect.Request[kernelv1.UnlinkItemsRequest]) (*connect.Response[kernelv1.UnlinkItemsResponse], error) {
	it, res, err := s.linkWrite(ctx, req.Msg, s.k.Unlink)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&kernelv1.UnlinkItemsResponse{Item: it, ItemSeq: res.ItemSeq, CommitSeq: res.CommitSeq}), nil
}

var directionsOut = map[item.LinkDirection]kernelv1.LinkDirection{
	item.LinkOutgoing: kernelv1.LinkDirection_LINK_DIRECTION_OUTGOING,
	item.LinkIncoming: kernelv1.LinkDirection_LINK_DIRECTION_INCOMING,
}

var directionsIn = map[kernelv1.LinkDirection]item.LinkDirection{
	kernelv1.LinkDirection_LINK_DIRECTION_OUTGOING: item.LinkOutgoing,
	kernelv1.LinkDirection_LINK_DIRECTION_INCOMING: item.LinkIncoming,
}

// linksOut is an Item's links for the wire: a restricted end has no name.
func linksOut(ns string, links []item.Link) []*kernelv1.Link {
	out := make([]*kernelv1.Link, 0, len(links))
	for _, l := range links {
		ref := &kernelv1.ItemRef{Restricted: true}
		if !l.Restricted {
			ref = &kernelv1.ItemRef{Name: itemName(ns, l.Other)}
		}
		out = append(out, &kernelv1.Link{LinkType: l.LinkType, Direction: directionsOut[l.Direction], Item: ref})
	}
	return out
}
