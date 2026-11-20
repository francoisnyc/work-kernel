// The harness of package api's tests, cut down to what these need: a real
// server (httptest, TLS and HTTP/2) over a real Postgres, driven through
// generated clients with dev tokens.
package apistep3_test

import (
	"context"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/protobuf/types/known/structpb"

	kernelv1 "github.com/francoisnyc/work-kernel/gen/kernel/v1"
	"github.com/francoisnyc/work-kernel/gen/kernel/v1/kernelv1connect"
	"github.com/francoisnyc/work-kernel/internal/api"
	"github.com/francoisnyc/work-kernel/internal/devtoken"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
	"github.com/francoisnyc/work-kernel/internal/testdb"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

var (
	pool *pgxpool.Pool
	k    *itemservice.Service
	srv  *httptest.Server
)

var tokens = []devtoken.Token{
	{Name: "ana", Token: "tok-ana", Principal: "user:ana", Namespace: "acme"},
	{Name: "bob", Token: "tok-bob", Principal: "user:bob", Namespace: "acme"},
}

func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	ctx := context.Background()
	p, stop, err := testdb.Start(ctx)
	if err != nil {
		log.Printf("start database: %v", err)
		return 1
	}
	defer stop()
	pool = p
	k = itemservice.New(pool)
	if err := k.EnsureNamespace(ctx, "acme"); err != nil {
		log.Printf("ensure namespace: %v", err)
		return 1
	}
	// The repository's Types, then the test Types beside them.
	types, err := typedef.ParseDir("../../../types")
	if err != nil {
		log.Printf("parse types: %v", err)
		return 1
	}
	for _, doc := range strings.Split(a3Types, "\n---\n") {
		ty, err := typedef.Parse([]byte(doc))
		if err != nil {
			log.Printf("parse a test Type: %v", err)
			return 1
		}
		types = append(types, ty)
	}
	for _, ty := range types {
		if err := k.RegisterType(ctx, ty); err != nil {
			log.Printf("register %s v%d: %v", ty.ID, ty.Version, err)
			return 1
		}
	}
	path, handler := api.NewHandler(k, api.Options{Tokens: tokens, SearchWait: 300 * time.Millisecond})
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	srv = httptest.NewUnstartedServer(mux)
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()
	return m.Run()
}

func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// as returns a client (Connect protocol, JSON) that calls as the named user.
func as(name string) kernelv1connect.ItemServiceClient {
	i := slices.IndexFunc(tokens, func(tk devtoken.Token) bool { return tk.Name == name })
	if i < 0 {
		panic("no token named " + name)
	}
	bearer := connect.WithInterceptors(connect.UnaryInterceptorFunc(
		func(next connect.UnaryFunc) connect.UnaryFunc {
			return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
				req.Header().Set("Authorization", "Bearer "+tokens[i].Token)
				return next(ctx, req)
			}
		}))
	return kernelv1connect.NewItemServiceClient(srv.Client(), srv.URL, connect.WithProtoJSON(), bearer)
}

func newID() string { return "req-" + uuid.NewString() }

func word() string { return "w" + uuid.NewString()[:8] }

func fields(t *testing.T, m map[string]any) *structpb.Struct {
	t.Helper()
	s, err := structpb.NewStruct(m)
	if err != nil {
		t.Fatalf("struct %v: %v", m, err)
	}
	return s
}

func strs(v any) []string {
	list, _ := v.([]any)
	out := make([]string, len(list))
	for i, m := range list {
		out[i], _ = m.(string)
	}
	return out
}

func itemID(t *testing.T, name string) uuid.UUID {
	t.Helper()
	return uuid.MustParse(name[strings.LastIndex(name, "/")+1:])
}

func typeNamed(t *testing.T, name string) *kernelv1.Type {
	t.Helper()
	res, err := as("ana").GetType(testCtx(t), connect.NewRequest(&kernelv1.GetTypeRequest{Name: name}))
	if err != nil {
		t.Fatalf("GetType %s: %v", name, err)
	}
	return res.Msg.Type
}

func get(t *testing.T, who string, name string) *kernelv1.Item {
	t.Helper()
	res, err := as(who).GetItem(testCtx(t), connect.NewRequest(&kernelv1.GetItemRequest{Name: name}))
	if err != nil {
		t.Fatalf("GetItem %s as %s: %v", name, who, err)
	}
	return res.Msg.Item
}

func transition(t *testing.T, who string, name, to string) *kernelv1.TransitionItemResponse {
	t.Helper()
	res, err := as(who).TransitionItem(testCtx(t), connect.NewRequest(&kernelv1.TransitionItemRequest{
		Name: name, RequestId: newID(), ToState: to,
	}))
	if err != nil {
		t.Fatalf("TransitionItem %s to %s as %s: %v", name, to, who, err)
	}
	return res.Msg
}

func events(t *testing.T, who, name string) []*kernelv1.ItemEvent {
	t.Helper()
	res, err := as(who).ListItemEvents(testCtx(t), connect.NewRequest(&kernelv1.ListItemEventsRequest{Parent: name, PageSize: 500}))
	if err != nil {
		t.Fatalf("ListItemEvents %s as %s: %v", name, who, err)
	}
	return res.Msg.Events
}

// apiError checks err's code and its ErrorInfo reason, and returns it.
func apiError(t *testing.T, what string, err error, code connect.Code, reason string) *connect.Error {
	t.Helper()
	var ce *connect.Error
	if !errors.As(err, &ce) {
		t.Fatalf("%s: error = %v, want a %v / %s", what, err, code, reason)
	}
	if ce.Code() != code {
		t.Errorf("%s: code = %v (%v), want %v", what, ce.Code(), ce.Message(), code)
	}
	info := errorInfo(t, ce)
	if info == nil {
		t.Errorf("%s: no ErrorInfo in %v", what, ce)
		return ce
	}
	if info.Reason != reason || info.Domain != api.ErrorDomain {
		t.Errorf("%s: ErrorInfo = %s/%s, want %s/%s", what, info.Domain, info.Reason, api.ErrorDomain, reason)
	}
	return ce
}

func errorInfo(t *testing.T, ce *connect.Error) *errdetails.ErrorInfo {
	t.Helper()
	for _, d := range ce.Details() {
		v, err := d.Value()
		if err != nil {
			t.Fatalf("decode error detail: %v", err)
		}
		if info, ok := v.(*errdetails.ErrorInfo); ok {
			return info
		}
	}
	return nil
}
