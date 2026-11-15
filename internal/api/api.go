// Package api serves the public API v1 (proto/kernel/v1) over connect-go:
// every operation answers the Connect protocol (JSON and binary), gRPC and
// gRPC-Web. It is the one way to Items from outside the kernel, and it
// reaches only the item service's public door: the Kernel interface below
// has no Internal(), so timers' and hooks' door cannot be served even by
// mistake.
//
// Who is calling comes from the bearer token (auth.go), never from a request
// field. Every kernel call carries the token's principal and namespace.
package api

import (
	"context"
	"net/http"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/gen/kernel/v1/kernelv1connect"
	"github.com/francoisnyc/work-kernel/internal/devtoken"
	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// Kernel is the item service as the API uses it: its public operations. A
// *itemservice.Service satisfies it; Go interfaces are implicit, so no
// "implements" clause is needed. Tests wrap it to watch every call.
type Kernel interface {
	Create(context.Context, itemservice.CreateRequest) (itemservice.WriteResult, error)
	Get(context.Context, item.Principal, uuid.UUID) (item.Item, error)
	Update(context.Context, itemservice.UpdateRequest) (itemservice.WriteResult, error)
	Transition(context.Context, itemservice.TransitionRequest) (itemservice.TransitionResult, error)
	Search(context.Context, itemservice.SearchRequest) (itemservice.SearchResult, error)
	List(context.Context, itemservice.ListRequest) (itemservice.ListResult, error)
	ListEvents(context.Context, itemservice.ListEventsRequest) (itemservice.ListEventsResult, error)
	StartAgentSession(context.Context, itemservice.StartAgentSessionRequest) (string, error)
	AgentSession(context.Context, item.Principal) (itemservice.AgentSession, error)
	ApproveWiden(context.Context, itemservice.ApproveWidenRequest) error
	Type(ctx context.Context, id string, version int) (*typedef.Type, error)
	ListTypes(context.Context) ([]*typedef.Type, error)
}

// Options configure the API.
type Options struct {
	// Tokens are the dev tokens: the only auth there is in the proof of
	// concept. A request without one of them is UNAUTHENTICATED.
	Tokens []devtoken.Token
	// Grants are what each user let each agent do (dev config for now). An
	// agent session must fit inside its grant.
	Grants []devtoken.Grant
	// SearchWait bounds how long SearchItems waits for min_seq; zero means
	// the kernel's default (2 s). Tests shorten it.
	SearchWait time.Duration
}

// AgentSessionHeader carries an agent's session ID on every call it makes
// after StartAgentSession.
const AgentSessionHeader = "Pli-Agent-Session"

// maxWait is the longest TransitionItemRequest.wait accepted.
const maxWait = itemservice.MaxWait

// server implements kernelv1connect.ItemServiceHandler.
type server struct {
	k          Kernel
	grants     []devtoken.Grant
	searchWait time.Duration
}

// NewHandler returns the path to mount the API under and its handler.
func NewHandler(k Kernel, opts Options) (string, http.Handler) {
	s := &server{k: k, searchWait: opts.SearchWait, grants: opts.Grants}
	return kernelv1connect.NewItemServiceHandler(s, connect.WithInterceptors(newAuth(opts.Tokens, k)))
}
