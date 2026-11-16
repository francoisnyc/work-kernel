package api

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	kernelv1 "github.com/francoisnyc/work-kernel/gen/kernel/v1"
)

// LinkItems adds a link from the source Item (name) to a target.
func (s *server) LinkItems(ctx context.Context,
	req *connect.Request[kernelv1.LinkItemsRequest]) (*connect.Response[kernelv1.LinkItemsResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("LinkItems: Step 1, Phase B stub"))
}

// UnlinkItems removes a link from the source Item (name) to a target.
func (s *server) UnlinkItems(ctx context.Context,
	req *connect.Request[kernelv1.UnlinkItemsRequest]) (*connect.Response[kernelv1.UnlinkItemsResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("UnlinkItems: Step 1, Phase B stub"))
}
