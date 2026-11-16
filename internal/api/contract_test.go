// Tests for Step 0, part 1: properties of the contract itself.
package api_test

import (
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	kernelv1 "github.com/francoisnyc/work-kernel/gen/kernel/v1"
	"github.com/francoisnyc/work-kernel/gen/kernel/v1/kernelv1connect"
)

// No request message carries a principal: who is calling comes from the
// token only. on_behalf_of is never a request field either.
func TestContract_NoRequestNamesAPrincipal(t *testing.T) {
	forbidden := []string{"principal", "actor", "on_behalf_of", "caller", "user", "namespace"}
	file := kernelv1.File_kernel_v1_kernel_proto
	var walk func(m protoreflect.MessageDescriptor)
	walk = func(m protoreflect.MessageDescriptor) {
		for i := 0; i < m.Fields().Len(); i++ {
			f := m.Fields().Get(i)
			if slices.Contains(forbidden, string(f.Name())) {
				t.Errorf("%s has a field %q: a request must not say who is calling", m.FullName(), f.Name())
			}
			if f.Message() != nil && f.Message().ParentFile() == file {
				walk(f.Message())
			}
		}
	}
	methods := file.Services().ByName("ItemService").Methods()
	for i := 0; i < methods.Len(); i++ {
		walk(methods.Get(i).Input())
	}
}

// The internal door never goes over the network: the service has exactly the
// public operations of parts 1 and 2.
func TestContract_OnlyPublicOperations(t *testing.T) {
	methods := kernelv1.File_kernel_v1_kernel_proto.Services().ByName("ItemService").Methods()
	var got []string
	for i := 0; i < methods.Len(); i++ {
		got = append(got, string(methods.Get(i).Name()))
	}
	want := []string{"CreateItem", "GetItem", "UpdateItem", "TransitionItem", "SearchItems",
		"ListItems", "ListItemEvents", "GetType", "ListTypes", "StartAgentSession", "ApproveWiden", // part 2 added five
		"LinkItems", "UnlinkItems"} // proof of concept Step 1
	if !slices.Equal(got, want) {
		t.Errorf("ItemService methods = %v, want %v", got, want)
	}
	for _, m := range got {
		for _, internal := range []string{"Resolve", "Hook", "Internal", "Action"} {
			if strings.Contains(m, internal) {
				t.Errorf("method %s looks like the internal door", m)
			}
		}
	}
}

// connect-go serves every operation over the Connect protocol (JSON and
// binary), gRPC and gRPC-Web, with the same answers.
func TestContract_EveryProtocol(t *testing.T) {
	want := create(t, "ana", ticketV1, map[string]any{"f_title": "protocols", "f_priority": 9}).Item
	protocols := map[string]kernelv1connect.ItemServiceClient{
		"connect+json": as("ana"),
		// The default codec is binary protobuf.
		"connect+proto": kernelv1connect.NewItemServiceClient(srv.Client(), srv.URL, bearer("tok-ana")),
		"grpc":          as("ana", connect.WithGRPC()),
		"grpc-web":      as("ana", connect.WithGRPCWeb()),
	}
	for name, c := range protocols {
		t.Run(name, func(t *testing.T) {
			res, err := c.GetItem(testCtx(t), connect.NewRequest(&kernelv1.GetItemRequest{Name: want.Name}))
			if err != nil {
				t.Fatalf("GetItem: %v", err)
			}
			if !proto.Equal(res.Msg.Item, want) {
				t.Errorf("GetItem = %v, want %v", res.Msg.Item, want)
			}
			_, err = c.GetItem(testCtx(t), connect.NewRequest(&kernelv1.GetItemRequest{
				Name: "namespaces/acme/items/00000000-0000-0000-0000-000000000000",
			}))
			notFound(t, name+" GetItem of nothing", err)
		})
	}
}
