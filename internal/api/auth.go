package api

import (
	"context"
	"strings"

	"connectrpc.com/connect"

	"github.com/francoisnyc/work-kernel/internal/devtoken"
	"github.com/francoisnyc/work-kernel/internal/item"
)

// callerKey is the context key of the caller. An unexported type as the key
// means no other package can read or overwrite it.
type callerKey struct{}

// caller returns who is calling, as the interceptor put it in the context.
// Every handler runs behind the interceptor, so it is always there.
func caller(ctx context.Context) item.Principal {
	p, _ := ctx.Value(callerKey{}).(item.Principal)
	return p
}

// newAuth returns the interceptor that turns a bearer token into the caller.
// It runs before every handler: no token, an unknown token or another
// scheme is UNAUTHENTICATED, and nothing else is looked at. Real sign-on
// (OIDC) later replaces this function only.
func newAuth(tokens []devtoken.Token) connect.UnaryInterceptorFunc {
	byToken := make(map[string]item.Principal, len(tokens))
	for _, t := range tokens {
		byToken[t.Token] = item.Principal{Actor: t.Principal, Namespace: t.Namespace}
	}
	// An interceptor wraps the next function in the chain, much like a
	// servlet filter: it can refuse, or call next with a richer context.
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			p, ok := byToken[bearerToken(req.Header().Get("Authorization"))]
			if !ok {
				return nil, apiError(connect.CodeUnauthenticated, "UNAUTHENTICATED",
					"a valid bearer token is required", nil)
			}
			return next(context.WithValue(ctx, callerKey{}, p), req)
		}
	}
}

// bearerToken returns the token of "Bearer <token>"; the scheme is
// case-insensitive (RFC 9110), the token is not. Anything else is "".
func bearerToken(header string) string {
	scheme, token, ok := strings.Cut(header, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	return strings.TrimSpace(token)
}
