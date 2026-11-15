package api

import (
	"context"
	"slices"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	kernelv1 "github.com/francoisnyc/work-kernel/gen/kernel/v1"
	"github.com/francoisnyc/work-kernel/internal/devtoken"
	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
)

// Agents in capability mode (docs/design.md, "Who is calling"). The API
// resolves the user's grant from the dev config; the kernel checks the
// session fits inside it and enforces the session on every call.

// scopeIn reads a scope's name: namespaces/{ns} is the whole namespace
// (uuid.Nil), a container's name is that Item. A name in another namespace
// is NOT_FOUND; anything else is INVALID_ARGUMENT on field.
func scopeIn(p item.Principal, name, field string) (uuid.UUID, *connect.Error) {
	if ns, ok := parseNamespace(name); ok {
		if ns != p.Namespace {
			return uuid.Nil, notFound()
		}
		return uuid.Nil, nil
	}
	id, cerr := inNamespace(p, name, field)
	if cerr != nil {
		return uuid.Nil, badFieldOr(cerr, field, "want namespaces/{ns} or namespaces/{ns}/items/{item}")
	}
	return id, nil
}

// grantFor is what user granted agent in the namespace (dev config), with
// its scope names read; nil when there is no such grant.
func (s *server) grantFor(agent, user, ns string) *itemservice.Grant {
	i := slices.IndexFunc(s.grants, func(g devtoken.Grant) bool {
		return g.Agent == agent && g.User == user && g.Namespace == ns
	})
	if i < 0 {
		return nil
	}
	g := s.grants[i]
	out := &itemservice.Grant{Powers: g.Powers}
	for _, name := range g.Scopes {
		// The dev-token file is checked when it loads: every scope is the
		// grant's namespace or an Item name in it.
		if _, ok := parseNamespace(name); ok {
			out.Scopes = append(out.Scopes, uuid.Nil)
		} else if _, id, ok := parseItemName(name); ok {
			out.Scopes = append(out.Scopes, id)
		}
	}
	return out
}

func (s *server) StartAgentSession(ctx context.Context, req *connect.Request[kernelv1.StartAgentSessionRequest]) (*connect.Response[kernelv1.StartAgentSessionResponse], error) {
	p, m := caller(ctx), req.Msg
	if !item.IsKind(p.Actor, item.KindAgent) {
		return nil, apiError(connect.CodeInvalidArgument, "INVALID_REQUEST", nil)
	}
	if cerr := parentNamespace(p, m.Parent); cerr != nil {
		return nil, cerr
	}
	if m.ActingFor != "" && !item.IsKind(m.ActingFor, item.KindUser) {
		return nil, badField("acting_for", "want a user, or empty for the agent itself")
	}
	if len(m.Scopes) == 0 {
		return nil, badField("scopes", "required: at least one")
	}
	scopes := make([]uuid.UUID, 0, len(m.Scopes))
	for _, name := range m.Scopes {
		id, cerr := scopeIn(p, name, "scopes")
		if cerr != nil {
			return nil, cerr
		}
		scopes = append(scopes, id)
	}
	if len(m.Powers) == 0 {
		return nil, badField("powers", "required: at least one")
	}
	p.OnBehalfOf = m.ActingFor
	id, err := s.k.StartAgentSession(ctx, itemservice.StartAgentSessionRequest{
		Principal: p, Scopes: scopes, Powers: m.Powers, Grant: s.grantFor(p.Actor, m.ActingFor, p.Namespace),
	})
	if err != nil {
		return nil, errorIn(err, p.Namespace, "")
	}
	return connect.NewResponse(&kernelv1.StartAgentSessionResponse{Session: &kernelv1.AgentSession{
		Name: namespaceName(p.Namespace) + "/agentSessions/" + id, Id: id,
		Agent: p.Actor, ActingFor: m.ActingFor, Scopes: m.Scopes, Powers: m.Powers,
	}}), nil
}

func (s *server) ApproveWiden(ctx context.Context, req *connect.Request[kernelv1.ApproveWidenRequest]) (*connect.Response[kernelv1.ApproveWidenResponse], error) {
	// The approver is the caller: the token says who, never the request.
	p, m := caller(ctx), req.Msg
	if cerr := parentNamespace(p, m.Parent); cerr != nil {
		return nil, cerr
	}
	if m.RequestId == "" {
		return nil, badField("request_id", "required")
	}
	if m.Session == "" {
		return nil, badField("session", "required")
	}
	scope, cerr := scopeIn(p, m.Scope, "scope")
	if cerr != nil {
		return nil, cerr
	}
	var scopeItem *uuid.UUID
	if scope != uuid.Nil {
		scopeItem = &scope
	}
	err := s.k.ApproveWiden(ctx, itemservice.ApproveWidenRequest{
		IdempotencyKey: m.RequestId, Principal: p, Session: m.Session, ScopeItem: scopeItem,
	})
	if err != nil {
		return nil, errorIn(err, p.Namespace, "")
	}
	return connect.NewResponse(&kernelv1.ApproveWidenResponse{}), nil
}
