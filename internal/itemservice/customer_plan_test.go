// The plan test of Jira part 2, step 4a: a customer's Search and List are
// driven by the relation form of the rule (`mine`, from the index on the
// effective creator), never by the predicate over every Item. On a seeded
// set (4,000 customers with 5 incidents each, a reply on one in four), no
// plan of a customer's read may hold a sequential scan of items. Phase A
// measured the predicate as the driver at 767 ms on 500,000 Items, and
// `mine` at 0.25 ms.
package itemservice_test

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/itemservice"
)

func TestCustomer_SearchAndListPlans_NeverScanEveryItem(t *testing.T) {
	if testing.Short() {
		t.Skip("seeds 25,000 Items")
	}
	w := newCustWorld(t)
	// The portal's node exists once it has a child.
	first := w.incident(t, w.kim, w.desk)
	var node uuid.UUID
	if err := pool.QueryRow(testCtx(t), `select id from scopes where item_id = $1`, w.desk).Scan(&node); err != nil {
		t.Fatalf("the portal's node: %v", err)
	}
	ctx := testCtx(t)
	for _, q := range []string{
		`insert into items (id, namespace_id, type_id, type_version, container_id, scope_id, state, fields, item_seq, content_seq,
		                    created_at, created_by, state_entered_at)
		 select gen_random_uuid(), $1, 'incident', 2, $2, $3, 'new',
		        jsonb_build_object('f_title', 'printer ' || n || ' vpn', 'f_priority', 'P3'), 1, 1,
		        now() - (n || ' minutes')::interval, 'customer:c' || (n % 4000), now() - (n || ' minutes')::interval
		 from generate_series(1, 20000) n`,
		`insert into items (id, namespace_id, type_id, type_version, container_id, scope_id, state, fields, item_seq, content_seq,
		                    created_at, created_by, state_entered_at)
		 select gen_random_uuid(), $1, 'reply', 1, i.id, $3, '', jsonb_build_object('f_body', 'vpn again'), 1, 1, i.created_at, 'user:sam', i.created_at
		 from items i where i.namespace_id = $1 and i.container_id = $2 and i.type_id = 'incident' and random() < 0.25`,
	} {
		if _, err := pool.Exec(ctx, q, w.ns, w.desk, node); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	for _, q := range []string{`analyze items`, `analyze scopes`} {
		if _, err := pool.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	c42 := customer("c42", w.ns)
	plans := map[string]func() (string, error){
		"search incidents": func() (string, error) {
			return itemservice.ExplainSearch(w.k, ctx, itemservice.SearchRequest{Principal: c42, Filter: itemservice.Filter{TypeIDs: []string{"incident"}}})
		},
		"search every Type": func() (string, error) {
			return itemservice.ExplainSearch(w.k, ctx, itemservice.SearchRequest{Principal: c42, Filter: anyType})
		},
		"search words": func() (string, error) {
			return itemservice.ExplainSearch(w.k, ctx, itemservice.SearchRequest{Principal: c42, Query: "vpn"})
		},
		"search new, by title": func() (string, error) {
			return itemservice.ExplainSearch(w.k, ctx, itemservice.SearchRequest{Principal: c42,
				Filter: itemservice.Filter{TypeIDs: []string{"incident"}, States: []string{"new"}}, OrderBy: &itemservice.OrderBy{Field: "f_title"}})
		},
		"list the portal": func() (string, error) {
			return itemservice.ExplainList(w.k, ctx, itemservice.ListRequest{Principal: c42, ContainerID: &w.desk})
		},
		"list the top level": func() (string, error) {
			return itemservice.ExplainList(w.k, ctx, itemservice.ListRequest{Principal: c42})
		},
		"list an incident (kim's)": func() (string, error) {
			return itemservice.ExplainList(w.k, ctx, itemservice.ListRequest{Principal: w.kim, ContainerID: &first})
		},
	}
	for name, explain := range plans {
		plan, err := explain()
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if strings.Contains(plan, "Seq Scan on items") {
			t.Errorf("%s: the plan scans every Item:\n%s", name, plan)
		}
	}
}
