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
	if err := requireNamespace(req.Principal); err != nil {
		return ListResult{}, err
	}
	if err := checkPublicPrincipal(req.Principal); err != nil {
		return ListResult{}, err
	}
	limit, err := pageSize(req.Limit)
	if err != nil {
		return ListResult{}, err
	}
	filter, err := k.resolveFilter(ctx, req.Principal, req.Filter)
	if err != nil {
		return ListResult{}, err
	}
	order, err := k.resolveOrder(ctx, req.Filter.TypeIDs, req.OrderBy)
	if err != nil {
		return ListResult{}, err
	}
	// The container is read like Get reads an Item: visible to the
	// principal, in the session's scopes, in the namespace. Its own read is
	// recorded for an agent, and it is not returned.
	if req.ContainerID != nil {
		if _, err := k.Get(ctx, req.Principal, *req.ContainerID); err != nil {
			return ListResult{}, err
		}
	} else if err := checkInSession(ctx, k.pool, req.Principal, nil); err != nil {
		return ListResult{}, err
	}
	key := requestKey("list", req.Principal, struct {
		Container *uuid.UUID
		Filter    Filter
		OrderBy   *OrderBy
	}{req.ContainerID, req.Filter, req.OrderBy})
	after, err := decodeToken(req.PageToken, key)
	if err != nil {
		return ListResult{}, err
	}
	found, next, err := k.page(ctx, itemQuery{
		p: req.Principal, parent: req.ContainerID, list: true, filter: filter, order: order,
		limit: limit, after: after, key: key,
	})
	if err != nil {
		return ListResult{}, fmt.Errorf("list: %w", err)
	}
	// Every outbound path goes through Project. The query has already
	// checked each Item's scope, so this drops nothing today; it is where
	// field-level redaction will happen. Any count or total is taken from
	// what Project returns, never before it. An agent's read of every
	// result is recorded for the write rule.
	items := make([]item.Item, 0, len(found))
	var read []*uuid.UUID
	for _, h := range found {
		if projected, ok := policy.Project(req.Principal, []uuid.UUID{*h.it.ScopeID}, h.it); ok {
			items = append(items, projected)
			read = append(read, h.policyScope)
		}
	}
	if err := recordReads(ctx, k.pool, req.Principal, read); err != nil {
		return ListResult{}, err
	}
	return ListResult{Items: items, NextPageToken: next}, nil
}
