package itemservice

import (
	"context"
	"errors"

	"github.com/francoisnyc/work-kernel/internal/item"
)

// The reads an inbox screen needs beside Search (docs/roadmap.md, track 1,
// item 5, step 4): a capped count and who the caller is. Declared so the
// tests compile; not built yet.

// errNotBuilt is what the stubs below answer. errors.New makes a plain
// error value; Go has no exception classes, a package-level variable is
// the usual way to name one.
var errNotBuilt = errors.New("not built yet (track 1 item 5, step 4)")

// CountCap is the most Count ever answers: past it, "50+".
const CountCap = 50

// CountRequest is a search's words and filter, with no page and no order.
type CountRequest struct {
	Principal item.Principal
	Query     string
	Filter    Filter
}

// CountResult: how many Items match and the principal can see, at most
// CountCap; Capped when more do.
type CountResult struct {
	Count  int
	Capped bool
}

// Count answers how many Items Search would return for the same words and
// filter, capped at CountCap.
func (k *Service) Count(ctx context.Context, req CountRequest) (CountResult, error) {
	return CountResult{}, errNotBuilt
}

// Me is who a caller is: the principal, its namespace and its roles there;
// for an agent, the user it acts for, that user's roles and the session.
type Me struct {
	Principal       string
	Namespace       string
	Roles           []string
	OnBehalfOf      string
	OnBehalfOfRoles []string
	Session         *AgentSession
}

// WhoAmI answers who the principal is.
func (k *Service) WhoAmI(ctx context.Context, p item.Principal) (Me, error) {
	return Me{}, errNotBuilt
}
