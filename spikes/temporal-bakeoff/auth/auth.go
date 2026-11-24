// Package auth is the plain-Temporal side's sign-in stand-in: the same dev
// tokens as the kernel side (a YAML file of bearer tokens), read by this
// team's own code. A real team would put its single sign-on here.
package auth

import (
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Principal is who is calling: "user:ana" or "agent:prep-bot", and the
// roles the token file gives them. It travels into workflows as plain data.
type Principal struct {
	ID    string   `json:"id"`
	Roles []string `json:"roles,omitempty"`
}

func (p Principal) IsAgent() bool        { return strings.HasPrefix(p.ID, "agent:") }
func (p Principal) IsUser() bool         { return strings.HasPrefix(p.ID, "user:") }
func (p Principal) Has(role string) bool { return slices.Contains(p.Roles, role) }

// Tokens maps a bearer token to its principal.
type Tokens map[string]Principal

// Load reads the dev-token file (the kernel spike's format; only the
// fields used here are read).
func Load(path string) (Tokens, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var file struct {
		Tokens []struct {
			Token     string   `yaml:"token"`
			Principal string   `yaml:"principal"`
			Roles     []string `yaml:"roles"`
		} `yaml:"tokens"`
	}
	if err := yaml.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	out := Tokens{}
	for _, t := range file.Tokens {
		if t.Token == "" || t.Principal == "" {
			return nil, fmt.Errorf("%s: a token without a token or a principal", path)
		}
		out[t.Token] = Principal{ID: t.Principal, Roles: t.Roles}
	}
	return out, nil
}

// Handler is an HTTP handler that knows who is calling.
type Handler func(w http.ResponseWriter, r *http.Request, p Principal)

// Require wraps h so it runs only for a known bearer token: every route of
// the API goes through it, so no handler can forget to sign people in.
func (t Tokens) Require(h Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		p, known := t[token]
		if !ok || !known {
			http.Error(w, `{"error":"unauthenticated"}`, http.StatusUnauthorized)
			return
		}
		h(w, r, p)
	}
}
