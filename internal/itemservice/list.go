package itemservice

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/policy"
)

// ListRequest asks for a container's direct children, or the top-level
// Items, that the principal can see.
type ListRequest struct {
	Principal   item.Principal
	ContainerID *uuid.UUID // nil: the Items at the top level
	Filter      Filter
	Limit       int      // as SearchRequest.Limit
	PageToken   string   // as SearchRequest.PageToken
	OrderBy     *OrderBy // as SearchRequest.OrderBy
}

// ListResult is one page, newest first (created_at, then id) or in the
// request's order.
type ListResult struct {
	Items         []item.Item
	NextPageToken string
}

// List returns one page of a container's direct children (or of the
// top-level Items) that the principal can see, filtered like Search. A
// container the principal cannot see, or outside an agent session's scopes,
// is ErrNotFound; so is the top level for a session that does not cover the
// whole namespace.
func (k *Service) List(ctx context.Context, req ListRequest) (ListResult, error) {
	q, err := k.listQuery(ctx, req)
	if err != nil {
		return ListResult{}, err
	}
	found, next, err := k.page(ctx, q)
	if err != nil {
		return ListResult{}, fmt.Errorf("list: %w", err)
	}
	// Every outbound path goes through Project, as in Search.
	items := make([]item.Item, 0, len(found))
	var read []*uuid.UUID
	for _, h := range found {
		ty, err := k.loadType(ctx, k.pool, h.it.TypeID, h.it.TypeVersion)
		if err != nil {
			return ListResult{}, err
		}
		if projected, ok := policy.Project(req.Principal, []uuid.UUID{*h.it.ScopeID}, h.it, ty); ok {
			items = append(items, projected)
			read = append(read, h.policyScope)
		}
	}
	if err := recordReads(ctx, k.pool, req.Principal, read); err != nil {
		return ListResult{}, err
	}
	if req.Filter.WaitingOn != "" {
		if err := k.fillMoves(ctx, req.Principal, items); err != nil {
			return ListResult{}, err
		}
	}
	return ListResult{Items: items, NextPageToken: next}, nil
}

// listQuery checks a listing and resolves it into the page query that List
// runs (pageSQL): the same for the read and for the plan test.
func (k *Service) listQuery(ctx context.Context, req ListRequest) (itemQuery, error) {
	if err := requireNamespace(req.Principal); err != nil {
		return itemQuery{}, err
	}
	if err := checkPublicPrincipal(req.Principal); err != nil {
		return itemQuery{}, err
	}
	limit, err := pageSize(req.Limit)
	if err != nil {
		return itemQuery{}, err
	}
	if req.Filter.In != nil {
		return itemQuery{}, &InvalidError{Path: "filter.in", Reason: "a listing has its container: use it, or SearchItems with filter.in"}
	}
	filter, err := k.resolveFilter(ctx, req.Principal, req.Filter)
	if err != nil {
		return itemQuery{}, err
	}
	order, err := k.resolveOrder(ctx, req.Principal, req.Filter, req.OrderBy)
	if err != nil {
		return itemQuery{}, err
	}
	// The container is read like Get reads an Item: visible to the
	// principal, in the session's scopes, in the namespace. Its own read is
	// recorded for an agent, and it is not returned.
	if req.ContainerID != nil {
		if _, err := k.Get(ctx, req.Principal, *req.ContainerID); err != nil {
			return itemQuery{}, err
		}
	} else if err := checkInSession(ctx, k.pool, req.Principal, nil); err != nil {
		return itemQuery{}, err
	}
	key := requestKey("list", req.Principal, struct {
		Container *uuid.UUID
		Filter    Filter
		OrderBy   *OrderBy
	}{req.ContainerID, req.Filter, req.OrderBy})
	after, err := decodeToken(req.PageToken, key)
	if err != nil {
		return itemQuery{}, err
	}
	return itemQuery{
		p: req.Principal, parent: req.ContainerID, list: true, filter: filter, order: order,
		limit: limit, after: after, key: key,
	}, nil
}
