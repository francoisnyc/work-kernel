// Tests for Step 0, part 1: the public API v1 over connect-go.
//
// The tests drive a real server (httptest, TLS and HTTP/2, so gRPC works too)
// over a real Postgres, through generated clients, as a client would. Each
// client sends its user's dev token; nothing in a request names a principal.
package api_test

import (
	"context"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
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
	// watched is the kernel as the API sees it: k, with every call recorded
	// (kernel_calls_test.go).
	watched *watchedKernel
)

// The two namespaces of these tests and who is in them. sam is one principal
// ID with a token in each namespace: two principals.
var tokens = []devtoken.Token{
	{Name: "ana", Token: "tok-ana", Principal: "user:ana", Namespace: "acme"},
	{Name: "bob", Token: "tok-bob", Principal: "user:bob", Namespace: "acme"},
	{Name: "bea", Token: "tok-bea", Principal: "user:bea", Namespace: "globex"},
	{Name: "hana", Token: "tok-hana", Principal: "user:hana", Namespace: "acme"}, // hr_admin (leave_test.go)
	{Name: "sam-acme", Token: "tok-sam-acme", Principal: "user:sam", Namespace: "acme"},
	{Name: "sam-globex", Token: "tok-sam-globex", Principal: "user:sam", Namespace: "globex"},
}

// Step 0, part 2: agents. Their tokens are kept apart from tokens because an
// agent calls with a session header, which the part 1 tests that loop over
// tokens do not send. bot and bot-globex are one agent ID in two
// namespaces; helper is another agent in acme.
var agentTokens = []devtoken.Token{
	{Name: "bot", Token: "tok-bot", Principal: "agent:triage-bot", Namespace: "acme"},
	{Name: "helper", Token: "tok-helper", Principal: "agent:helper", Namespace: "acme"},
	{Name: "bot-globex", Token: "tok-bot-globex", Principal: "agent:triage-bot", Namespace: "globex"},
}

// What each user granted each agent (dev config). ana lets the bot do
// anything in acme; bob lets it read only. Nobody granted helper anything
// for bob, and the bot acting for itself has no grant.
var grants = []devtoken.Grant{
	{Agent: "agent:triage-bot", User: "user:ana", Namespace: "acme", Scopes: []string{"namespaces/acme"},
		Powers: []string{"read", "create", "edit_fields", "transition:*", "move", "comment", "outbound"}},
	{Agent: "agent:triage-bot", User: "user:bob", Namespace: "acme", Scopes: []string{"namespaces/acme"},
		Powers: []string{"read"}},
	{Agent: "agent:helper", User: "user:ana", Namespace: "acme", Scopes: []string{"namespaces/acme"},
		Powers: []string{"read", "edit_fields"}},
	{Agent: "agent:triage-bot", User: "user:bea", Namespace: "globex", Scopes: []string{"namespaces/globex"},
		Powers: []string{"read", "edit_fields"}},
}

// Jira part 2, step 4a: two customers and a portal bot, kept apart from
// tokens and agentTokens so the earlier tests' loops over those are
// unchanged. kim lets the portal bot act for her, with the fixed powers of
// a customer.
var customerTokens = []devtoken.Token{
	{Name: "kim", Token: "tok-kim", Principal: "customer:kim", Namespace: "acme"},
	{Name: "lee", Token: "tok-lee", Principal: "customer:lee", Namespace: "acme"},
	{Name: "portal-bot", Token: "tok-portal-bot", Principal: "agent:portal-bot", Namespace: "acme"},
}

var customerGrants = []devtoken.Grant{
	{Agent: "agent:portal-bot", User: "customer:kim", Namespace: "acme", Scopes: []string{"namespaces/acme"},
		Powers: []string{"read", "create", "comment", "transition:*"}},
}

// searchWait is the server's min_seq wait in these tests (2 s in production).
const searchWait = 300 * time.Millisecond

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

	for _, ns := range []string{"acme", "globex"} {
		if err := k.EnsureNamespace(ctx, ns); err != nil {
			log.Printf("ensure namespace %s: %v", ns, err)
			return 1
		}
	}
	types, err := typedef.ParseDir("../../types")
	if err != nil {
		log.Printf("parse types: %v", err)
		return 1
	}
	for _, ty := range types {
		if err := k.RegisterType(ctx, ty); err != nil {
			log.Printf("register %s v%d: %v", ty.ID, ty.Version, err)
			return 1
		}
	}
	// ana approves in acme (ticket v3's approve needs role:approver).
	if _, err := pool.Exec(ctx,
		`insert into principal_roles (namespace_id, principal, role) values ('acme', 'user:ana', 'approver')`); err != nil {
		log.Printf("grant role: %v", err)
		return 1
	}

	watched = &watchedKernel{Kernel: k}
	all := append(append(slices.Clone(tokens), agentTokens...), customerTokens...)
	path, handler := api.NewHandler(watched, api.Options{Tokens: all, Grants: append(slices.Clone(grants), customerGrants...),
		SearchWait: searchWait})
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	srv = httptest.NewUnstartedServer(mux)
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()

	code := m.Run()
	// Whatever the tests did, no call may have reached the kernel without a
	// namespace: in the kernel, that silently means "default".
	if bad := watched.withoutNamespace(); len(bad) > 0 {
		for _, c := range bad {
			log.Printf("%s reached the kernel as %s with no namespace", c.op, c.p.Actor)
		}
		return 1
	}
	return code
}

func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// bearer adds an Authorization header to every call; an empty token sends none.
func bearer(token string) connect.Option {
	return connect.WithInterceptors(connect.UnaryInterceptorFunc(
		func(next connect.UnaryFunc) connect.UnaryFunc {
			return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
				if token != "" {
					req.Header().Set("Authorization", "Bearer "+token)
				}
				return next(ctx, req)
			}
		}))
}

// as returns a client (Connect protocol, JSON) that calls as the named user.
func as(name string, opts ...connect.ClientOption) kernelv1connect.ItemServiceClient {
	i := slices.IndexFunc(tokens, func(tk devtoken.Token) bool { return tk.Name == name })
	if i < 0 {
		panic("no token named " + name)
	}
	return withToken(tokens[i].Token, opts...)
}

// asAgent returns a client that calls as the named agent, sending session in
// the Pli-Agent-Session header when it is not empty.
func asAgent(name, session string) kernelv1connect.ItemServiceClient {
	i := slices.IndexFunc(agentTokens, func(tk devtoken.Token) bool { return tk.Name == name })
	if i < 0 {
		panic("no agent token named " + name)
	}
	return withToken(agentTokens[i].Token, sessionHeader(session))
}

// sessionHeader sends an agent session ID on every call; "" sends none.
func sessionHeader(session string) connect.Option {
	return connect.WithInterceptors(connect.UnaryInterceptorFunc(
		func(next connect.UnaryFunc) connect.UnaryFunc {
			return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
				if session != "" {
					req.Header().Set(api.AgentSessionHeader, session)
				}
				return next(ctx, req)
			}
		}))
}

func withToken(token string, opts ...connect.ClientOption) kernelv1connect.ItemServiceClient {
	opts = append([]connect.ClientOption{connect.WithProtoJSON(), bearer(token)}, opts...)
	return kernelv1connect.NewItemServiceClient(srv.Client(), srv.URL, opts...)
}

func newID() string { return "req-" + uuid.NewString() }

// word returns a search term no other test uses.
func word() string { return "w" + uuid.NewString()[:8] }

func fields(t *testing.T, m map[string]any) *structpb.Struct {
	t.Helper()
	s, err := structpb.NewStruct(m)
	if err != nil {
		t.Fatalf("struct %v: %v", m, err)
	}
	return s
}

const (
	ticketV1 = "namespaces/acme/types/ticket/versions/1"
	ticketV3 = "namespaces/acme/types/ticket/versions/3"
	channel  = "namespaces/acme/types/channel/versions/1"
)

// create makes an Item in acme as the named user and returns the response.
func create(t *testing.T, who string, typ string, f map[string]any) *kernelv1.CreateItemResponse {
	t.Helper()
	res, err := as(who).CreateItem(testCtx(t), connect.NewRequest(&kernelv1.CreateItemRequest{
		Parent: "namespaces/acme", RequestId: newID(), Type: typ, Fields: fields(t, f),
	}))
	if err != nil {
		t.Fatalf("CreateItem %s as %s: %v", typ, who, err)
	}
	return res.Msg
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

// violations returns the fields named by the error's BadRequest detail.
func violations(t *testing.T, ce *connect.Error) []string {
	t.Helper()
	var out []string
	for _, d := range ce.Details() {
		v, err := d.Value()
		if err != nil {
			t.Fatalf("decode error detail: %v", err)
		}
		if br, ok := v.(*errdetails.BadRequest); ok {
			for _, fv := range br.FieldViolations {
				out = append(out, fv.Field)
			}
		}
	}
	return out
}

func invalid(t *testing.T, what string, err error, field string) {
	t.Helper()
	ce := apiError(t, what, err, connect.CodeInvalidArgument, "INVALID_REQUEST")
	if got := violations(t, ce); !slices.Contains(got, field) {
		t.Errorf("%s: field violations = %v, want %q", what, got, field)
	}
}

// malformed checks an INVALID_ARGUMENT whose field the API cannot name: the
// kernel found the request malformed (a reader list, a move).
func malformed(t *testing.T, what string, err error) {
	t.Helper()
	apiError(t, what, err, connect.CodeInvalidArgument, "INVALID_REQUEST")
}

func notFound(t *testing.T, what string, err error) {
	t.Helper()
	apiError(t, what, err, connect.CodeNotFound, "NOT_FOUND")
}

func countEvents(t *testing.T, itemName string) int {
	t.Helper()
	id := itemName[len(itemName)-36:]
	var n int
	if err := pool.QueryRow(testCtx(t), `select count(*) from item_events where item_id = $1`, id).Scan(&n); err != nil {
		t.Fatalf("count events: %v", err)
	}
	return n
}
