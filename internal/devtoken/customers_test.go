// Tests for Jira part 2, step 4a: customers in the dev-token file.
//
//   - A token may be a customer:<id>, who never holds a role.
//   - A grant may let an agent act for a customer, with the fixed powers
//     of a customer only (read, create, comment, transition:*): the file is
//     refused otherwise, as the kernel refuses such a session.
package devtoken_test

import (
	"strings"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/devtoken"
)

const withCustomers = `tokens:
  - {name: lea, token: dev-lea, principal: "user:lea", namespace: acme, roles: [desk_agent]}
  - {name: kim, token: dev-kim, principal: "customer:kim", namespace: acme}
  - {name: portal-bot, token: dev-portal-bot, principal: "agent:portal-bot", namespace: acme}
`

func TestParse_ACustomer(t *testing.T) {
	toks, err := devtoken.Parse([]byte(withCustomers))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(toks) != 3 || toks[1].Principal != "customer:kim" || len(toks[1].Roles) != 0 {
		t.Errorf("tokens = %+v", toks)
	}
	// Never a role.
	_, err = devtoken.Parse([]byte(strings.Replace(withCustomers, `principal: "customer:kim", namespace: acme}`,
		`principal: "customer:kim", namespace: acme, roles: [desk_agent]}`, 1)))
	if err == nil || !strings.Contains(err.Error(), "customer:kim") {
		t.Errorf("a customer with a role: %v; want refused, naming the customer", err)
	}
	// customer: with no ID is no principal.
	if _, err := devtoken.Parse([]byte(strings.Replace(withCustomers, `"customer:kim"`, `"customer:"`, 1))); err == nil {
		t.Error("customer: with no ID accepted")
	}
}

func TestParseGrants_ForACustomer_TheFixedPowersOnly(t *testing.T) {
	grant := func(powers string) string {
		return withCustomers + "grants:\n  - {agent: \"agent:portal-bot\", user: \"customer:kim\", namespace: acme, " +
			"scopes: [namespaces/acme], powers: " + powers + "}\n"
	}
	gs, err := devtoken.ParseGrants([]byte(grant(`[read, create, comment, "transition:*"]`)))
	if err != nil {
		t.Fatalf("a portal bot for kim: %v", err)
	}
	if len(gs) != 1 || gs[0].User != "customer:kim" {
		t.Errorf("grants = %+v", gs)
	}
	for _, extra := range []string{"edit_fields", "move", "outbound"} {
		_, err := devtoken.ParseGrants([]byte(grant("[read, " + extra + "]")))
		if err == nil || !strings.Contains(err.Error(), extra) {
			t.Errorf("a portal bot for kim with %s: %v; want refused, naming the power", extra, err)
		}
	}
	// A customer is never the agent of a grant.
	bad := withCustomers + "grants:\n  - {agent: \"customer:kim\", user: \"user:lea\", namespace: acme, scopes: [namespaces/acme], powers: [read]}\n"
	if _, err := devtoken.ParseGrants([]byte(bad)); err == nil {
		t.Error("a grant whose agent is a customer accepted")
	}
}
