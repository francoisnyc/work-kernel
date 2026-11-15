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

	"go.yaml.in/yaml/v3"

	"github.com/francoisnyc/work-kernel/internal/item"
)

// Token is one entry of the file.
type Token struct {
	Name      string `yaml:"name"`      // what pli --as takes
	Token     string `yaml:"token"`     // the bearer token
	Principal string `yaml:"principal"` // user:<id> or agent:<id>
	Namespace string `yaml:"namespace"` // the namespace the principal acts in
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
	return nil, errors.New("dev tokens: grants not built yet (Step 0, part 2)")
}

// ParseGrants reads and checks the grants of a dev-token file.
func ParseGrants(data []byte) ([]Grant, error) {
	return nil, errors.New("dev tokens: grants not built yet (Step 0, part 2)")
}

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
	tokens, names := map[string]bool{}, map[string]bool{}
	for i, t := range f.Tokens {
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
		// Only people and agents call the public API. The kernel's own actors
		// (system:, hook:) never hold a token, and groups are not readers yet.
		case !item.IsKind(t.Principal, item.KindUser, item.KindAgent):
			return nil, fail("principal %q must be user:<id> or agent:<id>", t.Principal)
		case tokens[t.Token]:
			return nil, fail("token %q is used twice", t.Token)
		case names[t.Name]:
			return nil, fail("name %q is used twice", t.Name)
		}
		tokens[t.Token], names[t.Name] = true, true
	}
	return f.Tokens, nil
}
