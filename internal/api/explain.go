package api

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	kernelv1 "github.com/francoisnyc/work-kernel/gen/kernel/v1"
)

// ExplainType reads a Type version in plain language (Ventoux item 6): a
// registered version by name, or a draft from sources. Phase B stub.
func (s *server) ExplainType(ctx context.Context, req *connect.Request[kernelv1.ExplainTypeRequest]) (*connect.Response[kernelv1.ExplainTypeResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("ExplainType is not built yet"))
}
