// Package devtoken reads the dev-token file: proof-of-concept auth only.
// Each entry maps a bearer token to a principal in one namespace, and names
// the entry so `pli --as <name>` can pick it. The item service and pli read
// the same file. Real sign-on (OIDC) later replaces this package and the
// API's interceptor, nothing else.
package devtoken

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"

	"github.com/francoisnyc/work-kernel/internal/item"
)

// Token is one entry of the file.
type Token struct {
	Name      string `yaml:"name"`      // what pli --as takes
	Token     string `yaml:"token"`     // the bearer token
	Principal string `yaml:"principal"` // user:<id> or agent:<id>
	Namespace string `yaml:"namespace"` // the namespace the principal acts in
	// Roles the principal holds in that namespace (approver, without the
	// "role:" prefix). The item service adds them at start, in dev-token
	// mode only; it never removes one.
	Roles []string `yaml:"roles,omitempty"`
	// DisplayName is an agent's name for screens (the Inbox, the look),
	// until sign-on brings real names: optional, agents only (a person's
	// screen name comes from Name), plain text of 1 to 60 characters.
	DisplayName string `yaml:"display_name,omitempty"`
}

type file struct {
	Tokens []Token `yaml:"tokens"`
	Grants []Grant `yaml:"grants"`
}

// Grant is what a user let an agent do, in one namespace: the scopes a
// session may cover (namespaces/{ns} for all of it, or container names) and
// the powers it may hold. A grant with no user is for the agent acting for
// itself.
type Grant struct {
	Agent     string   `yaml:"agent"`     // agent:<id>
	User      string   `yaml:"user"`      // user:<id>, or empty
	Namespace string   `yaml:"namespace"` // the namespace of both
	Scopes    []string `yaml:"scopes"`
	Powers    []string `yaml:"powers"`
}

// LoadGrants reads a dev-token file's grants.
func LoadGrants(path string) ([]Grant, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("dev tokens: %w", err)
	}
	gs, err := ParseGrants(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return gs, nil
}

// ParseGrants reads and checks the grants of a dev-token file. One bad grant
// refuses the whole file. A file without grants has none: no agent may
// start a session.
func ParseGrants(data []byte) ([]Grant, error) {
	var f file
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("dev tokens: %w", err)
	}
	seen := map[[3]string]bool{}
	for i, g := range f.Grants {
		fail := func(format string, args ...any) error {
			return fmt.Errorf("dev tokens: grant %d (%s for %q): %s", i+1, g.Agent, g.User, fmt.Sprintf(format, args...))
		}
		switch {
		case !item.IsKind(g.Agent, item.KindAgent):
			return nil, fail("agent %q must be agent:<id>", g.Agent)
		case g.User != "" && !item.IsKind(g.User, item.KindUser, item.KindCustomer):
			return nil, fail("user %q must be user:<id>, customer:<id>, or empty for the agent itself", g.User)
		case !namespaceName.MatchString(g.Namespace):
			return nil, fail("namespace %q must be lower-case letters, digits and dashes", g.Namespace)
		case len(g.Scopes) == 0:
			return nil, fail("no scopes")
		case len(g.Powers) == 0:
			return nil, fail("no powers")
		}
		for _, s := range g.Scopes {
			if !scopeIn(s, g.Namespace) {
				return nil, fail("scope %q must be namespaces/%s or an Item in it", s, g.Namespace)
			}
		}
		for _, p := range g.Powers {
			if !item.KnownPower(p) {
				return nil, fail("unknown power %q", p)
			}
			// Acting for a customer, an agent gets a customer's fixed
			// powers at most: the kernel refuses any other in a session.
			if item.IsKind(g.User, item.KindCustomer) && !customerPower(p) {
				return nil, fail("power %q is not a customer's (read, create, comment, transition:*)", p)
			}
		}
		key := [3]string{g.Agent, g.User, g.Namespace}
		if seen[key] {
			return nil, fail("the same agent and user twice in one namespace")
		}
		seen[key] = true
	}
	return f.Grants, nil
}

// itemInNamespace matches namespaces/{ns}/items/{uuid}.
var itemInNamespace = regexp.MustCompile(`^namespaces/([a-z0-9][a-z0-9-]{0,62})/items/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// scopeIn reports whether a grant's scope is its namespace, or an Item in it.
func scopeIn(scope, ns string) bool {
	if scope == "namespaces/"+ns {
		return true
	}
	m := itemInNamespace.FindStringSubmatch(scope)
	return m != nil && m[1] == ns
}

// roleName matches the item service's role names (EnsureRoles), repeated
// here for the same reason as namespaceName.
var roleName = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,62}$`)

// namespaceName matches itemservice.ValidNamespace. It is repeated here so
// pli does not import the item service.
var namespaceName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// Load reads and checks a dev-token file.
func Load(path string) ([]Token, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("dev tokens: %w", err)
	}
	toks, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return toks, nil
}

// Parse reads and checks the file's contents. One bad entry refuses the
// whole file: a server must not start with half its users.
func Parse(data []byte) ([]Token, error) {
	var f file
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true) // an unknown key is a typo, not something to ignore
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("dev tokens: %w", err)
	}
	if len(f.Tokens) == 0 {
		return nil, errors.New("dev tokens: no tokens")
	}
	// Which entries write display_name at all: an empty one is written,
	// and refused, not absent.
	var keys struct {
		Tokens []map[string]any `yaml:"tokens"`
	}
	if err := yaml.Unmarshal(data, &keys); err != nil {
		return nil, fmt.Errorf("dev tokens: %w", err)
	}
	tokens, names := map[string]bool{}, map[string]bool{}
	for i, t := range f.Tokens {
		_, hasDisplay := keys.Tokens[i]["display_name"]
		fail := func(format string, args ...any) error {
			return fmt.Errorf("dev tokens: entry %d (%s): %s", i+1, t.Name, fmt.Sprintf(format, args...))
		}
		switch {
		case t.Name == "":
			return nil, fail("no name")
		case t.Token == "":
			return nil, fail("no token")
		case !namespaceName.MatchString(t.Namespace):
			return nil, fail("namespace %q must be lower-case letters, digits and dashes", t.Namespace)
		// Only people, customers and agents call the public API. The
		// kernel's own actors (system:, hook:) never hold a token, and
		// groups are not readers yet.
		case !item.IsKind(t.Principal, item.KindUser, item.KindAgent, item.KindCustomer):
			return nil, fail("principal %q must be user:<id>, agent:<id> or customer:<id>", t.Principal)
		// A customer never holds a role (Jira part 2, step 4a).
		case item.IsKind(t.Principal, item.KindCustomer) && len(t.Roles) > 0:
			return nil, fail("principal %s is a customer: a customer never holds a role", t.Principal)
		case tokens[t.Token]:
			return nil, fail("token %q is used twice", t.Token)
		case names[t.Name]:
			return nil, fail("name %q is used twice", t.Name)
		case hasDisplay && !item.IsKind(t.Principal, item.KindAgent):
			return nil, fail("display_name is for an agent; %s's screen name is its name", t.Principal)
		case hasDisplay && !plainName(t.DisplayName):
			return nil, fail("display_name %q must be plain text of 1 to 60 characters on one line, with no space at either end", t.DisplayName)
		}
		for _, r := range t.Roles {
			if !roleName.MatchString(r) {
				return nil, fail("role %q must be a bare name such as approver (no role: prefix)", r)
			}
		}
		tokens[t.Token], names[t.Name] = true, true
	}
	return f.Tokens, nil
}

// customerPower is the kernel's fixed set for a customer (itemservice):
// read, create, comment and moves. Repeated here so pli does not import the
// item service.
func customerPower(p string) bool {
	switch p {
	case item.PowerRead, item.PowerCreate, item.PowerComment, item.PowerTransitionAll:
		return true
	}
	return strings.HasPrefix(p, item.PowerTransitionPrefix)
}

// plainName reports whether s is a screen name: 1 to 60 characters, one
// line, no control character, no space at either end.
func plainName(s string) bool {
	n := utf8.RuneCountInString(s)
	return n >= 1 && n <= 60 && strings.IndexFunc(s, unicode.IsControl) < 0 && strings.TrimSpace(s) == s
}
