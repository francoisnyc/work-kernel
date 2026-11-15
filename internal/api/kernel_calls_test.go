// Tests for Step 0, part 1: the API never lets a kernel call fall back to the
// kernel's default namespace. In the kernel an empty Principal.Namespace
// means "default", which is convenient for the earlier tests and exactly how
// a forgotten code path would leak across namespaces. So every API test runs
// through watchedKernel, which records the principal of every call; these
// tests check the calls carry the token's namespace, and TestMain fails the
// run if any call, in any test, had none.
package api_test

import (
	"context"
	"slices"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	kernelv1 "github.com/francoisnyc/work-kernel/gen/kernel/v1"
	"github.com/francoisnyc/work-kernel/internal/api"
	"github.com/francoisnyc/work-kernel/internal/devtoken"
	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
)

type kernelCall struct {
	op string
	p  item.Principal
}

// watchedKernel wraps the kernel and records who every call is made as.
// Embedding api.Kernel promotes its methods (Type, ListTypes, which carry no
// principal); the methods below replace the ones that do, like overriding
// in a Java subclass, and then call the embedded kernel.
type watchedKernel struct {
	api.Kernel
	mu    sync.Mutex
	calls []kernelCall
}

func (w *watchedKernel) record(op string, p item.Principal) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.calls = append(w.calls, kernelCall{op, p})
}

// since returns the calls made after the first n.
func (w *watchedKernel) since(n int) []kernelCall {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.calls[n:])
}

func (w *watchedKernel) count() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.calls)
}

// withoutNamespace returns every recorded call that named no namespace.
func (w *watchedKernel) withoutNamespace() []kernelCall {
	var bad []kernelCall
	for _, c := range w.since(0) {
		if c.p.Namespace == "" {
			bad = append(bad, c)
		}
	}
	return bad
}

func (w *watchedKernel) Create(ctx context.Context, r itemservice.CreateRequest) (itemservice.WriteResult, error) {
	w.record("Create", r.Principal)
	return w.Kernel.Create(ctx, r)
}

func (w *watchedKernel) Get(ctx context.Context, p item.Principal, id uuid.UUID) (item.Item, error) {
	w.record("Get", p)
	return w.Kernel.Get(ctx, p, id)
}

func (w *watchedKernel) Update(ctx context.Context, r itemservice.UpdateRequest) (itemservice.WriteResult, error) {
	w.record("Update", r.Principal)
	return w.Kernel.Update(ctx, r)
}

func (w *watchedKernel) Transition(ctx context.Context, r itemservice.TransitionRequest) (itemservice.TransitionResult, error) {
	w.record("Transition", r.Principal)
	return w.Kernel.Transition(ctx, r)
}

func (w *watchedKernel) Search(ctx context.Context, r itemservice.SearchRequest) (itemservice.SearchResult, error) {
	w.record("Search", r.Principal)
	return w.Kernel.Search(ctx, r)
}

func (w *watchedKernel) List(ctx context.Context, r itemservice.ListRequest) (itemservice.ListResult, error) {
	w.record("List", r.Principal)
	return w.Kernel.List(ctx, r)
}

func (w *watchedKernel) ListEvents(ctx context.Context, r itemservice.ListEventsRequest) (itemservice.ListEventsResult, error) {
	w.record("ListEvents", r.Principal)
	return w.Kernel.ListEvents(ctx, r)
}

func (w *watchedKernel) StartAgentSession(ctx context.Context, r itemservice.StartAgentSessionRequest) (string, error) {
	w.record("StartAgentSession", r.Principal)
	return w.Kernel.StartAgentSession(ctx, r)
}

func (w *watchedKernel) AgentSession(ctx context.Context, p item.Principal) (itemservice.AgentSession, error) {
	w.record("AgentSession", p)
	return w.Kernel.AgentSession(ctx, p)
}

func (w *watchedKernel) ApproveWiden(ctx context.Context, r itemservice.ApproveWidenRequest) error {
	w.record("ApproveWiden", r.Principal)
	return w.Kernel.ApproveWiden(ctx, r)
}

// Every operation, for callers in both namespaces (sam in each): every
// kernel call carries exactly the token's principal and namespace.
func TestKernelCalls_CarryTheTokensNamespace(t *testing.T) {
	for _, tok := range tokens {
		t.Run(tok.Name, func(t *testing.T) {
			ctx := testCtx(t)
			c := as(tok.Name)
			parent := "namespaces/" + tok.Namespace
			start := watched.count()

			res, err := c.CreateItem(ctx, connect.NewRequest(&kernelv1.CreateItemRequest{
				Parent: parent, RequestId: newID(), Type: parent + "/types/ticket/versions/1",
				Fields: fields(t, map[string]any{"f_title": word(), "f_assignee": "user-bob"}),
			}))
			if err != nil {
				t.Fatalf("CreateItem: %v", err)
			}
			name := res.Msg.Item.Name
			if _, err := c.GetItem(ctx, connect.NewRequest(&kernelv1.GetItemRequest{Name: name})); err != nil {
				t.Fatalf("GetItem: %v", err)
			}
			if _, err := c.UpdateItem(ctx, connect.NewRequest(&kernelv1.UpdateItemRequest{
				Name: name, RequestId: newID(), Fields: fields(t, map[string]any{"f_priority": 1}),
			})); err != nil {
				t.Fatalf("UpdateItem: %v", err)
			}
			if _, err := c.TransitionItem(ctx, connect.NewRequest(&kernelv1.TransitionItemRequest{
				Name: name, RequestId: newID(), ToState: "in_progress",
			})); err != nil {
				t.Fatalf("TransitionItem: %v", err)
			}
			if _, err := c.SearchItems(ctx, connect.NewRequest(&kernelv1.SearchItemsRequest{
				Parent: parent, Query: word(),
			})); err != nil {
				t.Fatalf("SearchItems: %v", err)
			}

			assertCalls(t, tok, watched.since(start), "Create", "Get", "Update", "Transition", "Search")
		})
	}
}

// assertCalls checks every call was made as the token's principal in the
// token's namespace, and that each op was seen.
func assertCalls(t *testing.T, tok devtoken.Token, calls []kernelCall, ops ...string) {
	t.Helper()
	seen := map[string]bool{}
	for _, c := range calls {
		seen[c.op] = true
		if c.p.Namespace != tok.Namespace || c.p.Actor != tok.Principal {
			t.Errorf("%s called as %s in %q, want %s in %q", c.op, c.p.Actor, c.p.Namespace, tok.Principal, tok.Namespace)
		}
	}
	for _, op := range ops {
		if !seen[op] {
			t.Errorf("no %s call recorded; the test is not watching the API's calls", op)
		}
	}
}

// Nothing the tests have done so far reached the kernel without a
// namespace. (TestMain checks again after every test has run.)
func TestKernelCalls_NeverWithoutANamespace(t *testing.T) {
	if watched.count() == 0 {
		t.Fatal("no kernel calls recorded; the API is not running through watchedKernel")
	}
	for _, c := range watched.withoutNamespace() {
		t.Errorf("%s reached the kernel as %s with no namespace", c.op, c.p.Actor)
	}
}

// Step 0, part 2: the new reads carry the token's namespace too, for a user
// in each namespace.
func TestKernelCalls_NewReadsCarryTheTokensNamespace(t *testing.T) {
	for _, name := range []string{"ana", "bea"} {
		t.Run(name, func(t *testing.T) {
			i := slices.IndexFunc(tokens, func(tk devtoken.Token) bool { return tk.Name == name })
			tok := tokens[i]
			ctx := testCtx(t)
			c := as(name)
			parent := "namespaces/" + tok.Namespace
			res, err := c.CreateItem(ctx, connect.NewRequest(&kernelv1.CreateItemRequest{
				Parent: parent, RequestId: newID(), Type: parent + "/types/ticket/versions/1",
			}))
			if err != nil {
				t.Fatalf("CreateItem: %v", err)
			}
			start := watched.count()
			if _, err := c.ListItems(ctx, connect.NewRequest(&kernelv1.ListItemsRequest{Parent: res.Msg.Item.Name})); err != nil {
				t.Fatalf("ListItems: %v", err)
			}
			if _, err := c.ListItemEvents(ctx, connect.NewRequest(&kernelv1.ListItemEventsRequest{Parent: res.Msg.Item.Name})); err != nil {
				t.Fatalf("ListItemEvents: %v", err)
			}
			assertCalls(t, tok, watched.since(start), "List", "ListEvents")
		})
	}
}

// An agent's calls reach the kernel as the agent, in its namespace, with its
// session and the user the session acts for, taken from the session, never
// from the request.
func TestKernelCalls_AgentCallsCarryTheSessionsUser(t *testing.T) {
	ctx := testCtx(t)
	session := startSession(t, "bot", "user:ana", []string{"namespaces/acme"}, "read", "edit_fields")
	it := create(t, "ana", ticketV1, nil).Item
	start := watched.count()
	if _, err := asAgent("bot", session).GetItem(ctx, connect.NewRequest(&kernelv1.GetItemRequest{Name: it.Name})); err != nil {
		t.Fatalf("GetItem as the bot: %v", err)
	}
	var got []kernelCall
	for _, c := range watched.since(start) {
		if c.op == "Get" {
			got = append(got, c)
		}
	}
	want := item.Principal{Actor: "agent:triage-bot", OnBehalfOf: "user:ana", AgentSession: session, Namespace: "acme"}
	if len(got) != 1 || got[0].p != want {
		t.Errorf("Get calls = %+v, want one as %+v", got, want)
	}
}
