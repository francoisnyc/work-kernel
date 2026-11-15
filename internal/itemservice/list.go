package itemservice

import (
	"context"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/item"
)

// ListRequest asks for a container's direct children, or the top-level
// Items, that the principal can see.
type ListRequest struct {
	Principal   item.Principal
	ContainerID *uuid.UUID // nil: the Items at the top level
	Filter      Filter
	Limit       int    // as SearchRequest.Limit
	PageToken   string // as SearchRequest.PageToken
}

// ListResult is one page, newest first (created_at, then id).
type ListResult struct {
	Items         []item.Item
	NextPageToken string
}

// List returns one page of a container's direct children.
func (k *Service) List(ctx context.Context, req ListRequest) (ListResult, error) {
	return ListResult{}, errNotBuilt
}
