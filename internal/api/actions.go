package api

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	kernelv1 "github.com/francoisnyc/work-kernel/gen/kernel/v1"
)

// ListActions serves the action catalogue (Ventoux V1). Phase B stub.
func (s *server) ListActions(ctx context.Context, req *connect.Request[kernelv1.ListActionsRequest]) (*connect.Response[kernelv1.ListActionsResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("ListActions: not implemented"))
}
