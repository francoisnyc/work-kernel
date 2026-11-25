package main

// Track 1 item 5, step 1 (--waiting): what the "waiting on me" view costs
// while it is derived at read time (itemservice, waiting.go), so that the
// owner can decide on these numbers whether step 3 (who may act, stored
// per open Item) is needed.
//
// The data: 400,000 Items in one namespace, nothing narrowed. 250,000 are
// purchases on two versions of one Type, 62% of them closed; 55,000 are
// open and wait on someone: 30,000 on the manager a field names (60
// managers with 500 each; the other 440 managers have none), 22,500 on
// finance (a role), 2,500 on the CFO. The other 150,000 are stories whose
// moves name nobody.
//
// The Items are loaded with one INSERT, not through the item service: at
// this size a Create each would take the best part of an hour, and only
// reads are measured here. They have no events and no workflow; nothing
// else may ever load Items this way. It can only reach the throwaway
// Postgres this mode starts itself (testdb.Start, a fresh container that
// is removed when it returns): the mode takes no database address, by
// flag or by environment, and the INSERT runs on that container's pool.
//
// Each read is the item service's own Search (so its own SQL), a page of
// 50, timed 30 times after a warm-up; the plan printed is that same
// statement's, under EXPLAIN (ANALYZE).

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
	"github.com/francoisnyc/work-kernel/internal/testdb"
)

// bw_purchase: the bake-off's purchase cut to who decides. Version 2 is the
// same rules again: the view reads every registered version.
const benchPurchaseSrc = `
id: bw_purchase
version: %d
initial_state: draft
fields:
  - {id: f_amount,  type: int}
  - {id: f_vendor,  type: string}
  - {id: f_manager, type: principal}
states:
  - {id: draft,          category: todo}
  - {id: manager_review, category: waiting}
  - {id: finance_queue,  category: waiting}
  - {id: finance_review, category: active}
  - {id: cfo_review,     category: waiting}
  - {id: approved,       category: done}
  - {id: rejected,       category: canceled}
transitions:
  - {from: draft,          to: manager_review, allowed: [creator]}
  - {from: manager_review, to: finance_queue,  allowed: ['field:f_manager'], except: [creator]}
  - {from: manager_review, to: rejected,       allowed: ['field:f_manager'], except: [creator]}
  - {from: finance_queue,  to: finance_review, allowed: ['role:bw_finance'], except: [creator]}
  - {from: finance_review, to: finance_queue,  allowed: ['role:bw_finance']}
  - {from: finance_review, to: cfo_review,     allowed: ['role:bw_finance'], except: [creator]}
  - {from: finance_review, to: rejected,       allowed: ['role:bw_finance'], except: [creator]}
  - {from: cfo_review,     to: approved,       allowed: ['role:bw_cfo']}
  - {from: cfo_review,     to: rejected,       allowed: ['role:bw_cfo']}
`

const benchStorySrc = `
id: bw_story
version: 1
initial_state: todo
fields:
  - {id: f_title, type: string}
states:
  - {id: todo,        category: todo}
  - {id: in_progress, category: active}
  - {id: done,        category: done}
transitions:
  - {from: todo,        to: in_progress}
  - {from: in_progress, to: done}
`

// loadWaiting is the one INSERT. g % 100 decides a purchase's state and
// g % 500 its manager, so only the 60 managers whose number ends in 62 to
// 73 have purchases in review, 500 each: m70 and m170 do, m5 has none.
const loadWaiting = `
insert into items (id, namespace_id, type_id, type_version, scope_id, state, fields, item_seq, content_seq,
                   created_at, created_by, updated_at)
select gen_random_uuid(), 'default',
       case when g <= 250000 then 'bw_purchase' else 'bw_story' end,
       case when g <= 250000 then 1 + (g % 2) else 1 end,
       (select id from scopes where namespace_id = 'default' and parent_id is null),
       case when g > 250000 then (array['todo','in_progress','done'])[1 + g % 3]
            when g % 100 < 62 then (array['approved','rejected'])[1 + g % 2]
            when g % 100 < 74 then 'manager_review'
            when g % 100 < 82 then 'finance_queue'
            when g % 100 < 83 then 'finance_review'
            when g % 100 < 84 then 'cfo_review'
            else 'draft' end,
       case when g <= 250000 then jsonb_build_object('f_amount', g % 9000, 'f_vendor', 'vendor-' || (g % 300),
                                                     'f_manager', 'user:m' || (g % 500))
            else jsonb_build_object('f_title', 'story ' || g) end,
       1, 1, now() - (g || ' seconds')::interval, 'user:u' || (g % 5000), now()
from generate_series(1, 400000) g`

func waitingMode(ctx context.Context) error {
	plain, stopDB, err := testdb.Start(ctx)
	if err != nil {
		return err
	}
	defer stopDB()
	// A second pool on the same database, traced: the item service runs
	// through it, the EXPLAINs through the plain one.
	cfg := plain.Config().Copy()
	trace := &capture{}
	cfg.ConnConfig.Tracer = trace
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return err
	}
	defer pool.Close()
	for _, src := range []string{fmt.Sprintf(benchPurchaseSrc, 1), fmt.Sprintf(benchPurchaseSrc, 2), benchStorySrc} {
		if err := registerYAML(ctx, pool, []byte(src)); err != nil {
			return err
		}
	}
	k := itemservice.New(pool)
	if err := k.EnsureNamespace(ctx, "default"); err != nil {
		return err
	}
	start := time.Now()
	if _, err := plain.Exec(ctx, loadWaiting); err != nil {
		return fmt.Errorf("load the Items: %w", err)
	}
	// fay is in finance; m70 manages 500 waiting purchases and is in
	// finance too; m5 manages none (no purchase of theirs is in review).
	for principal, role := range map[string]string{"user:fay": "bw_finance", "user:m70": "bw_finance", "user:cho": "bw_cfo"} {
		if _, err := plain.Exec(ctx,
			`insert into principal_roles (namespace_id, principal, role) values ('default', $1, $2)`, principal, role); err != nil {
			return err
		}
	}
	if _, err := plain.Exec(ctx, `analyze`); err != nil {
		return err
	}
	var total, open int
	if err := plain.QueryRow(ctx, `select count(*), count(*) filter (where type_id = 'bw_purchase' and state in
		('manager_review', 'finance_queue', 'finance_review', 'cfo_review')) from items`).Scan(&total, &open); err != nil {
		return err
	}
	fmt.Printf("# cmd/bench -waiting: the waiting-on-me view, derived at read time\n\n")
	fmt.Printf("%d Items loaded in %.1f s, %d of them open and waiting on someone; analyzed. A page of 50, 30 runs each.\n\n",
		total, time.Since(start).Seconds(), open)
	fmt.Println("| Read | Items in the view | median ms | p90 ms | max ms |")
	fmt.Println("| --- | --- | --- | --- | --- |")

	user := func(id string) item.Principal { return item.Principal{Actor: "user:" + id, Namespace: "default"} }
	purchases := itemservice.Filter{TypeIDs: []string{"bw_purchase"}}
	cases := []struct {
		name string
		req  itemservice.SearchRequest
		plan bool
	}{
		{"finance member, me (a role: 22,500 match)", itemservice.SearchRequest{Principal: user("fay"), Filter: itemservice.Filter{WaitingOn: "me"}}, true},
		{"the finance queue, asked by someone outside it", itemservice.SearchRequest{Principal: user("u1"), Filter: itemservice.Filter{WaitingOn: "role:bw_finance"}}, false},
		{"the CFO, me (2,500 match)", itemservice.SearchRequest{Principal: user("cho"), Filter: itemservice.Filter{WaitingOn: "me"}}, false},
		{"a manager with 500 waiting, me (a field)", itemservice.SearchRequest{Principal: user("m170"), Filter: itemservice.Filter{WaitingOn: "me"}}, true},
		{"a manager with nothing waiting, me (reads every purchase in review)", itemservice.SearchRequest{Principal: user("m5"), Filter: itemservice.Filter{WaitingOn: "me"}}, true},
		{"a manager who is also in finance, me", itemservice.SearchRequest{Principal: user("m70"), Filter: itemservice.Filter{WaitingOn: "me"}}, true},
		{"someone nothing names, me", itemservice.SearchRequest{Principal: user("u2"), Filter: itemservice.Filter{WaitingOn: "me"}}, false},
		{"finance member, me, purchases only (one Type)", itemservice.SearchRequest{Principal: user("fay"), Filter: withWaiting(purchases, "me")}, false},
		{"finance member, me, ordered by amount", itemservice.SearchRequest{Principal: user("fay"), Filter: withWaiting(purchases, "me"),
			OrderBy: &itemservice.OrderBy{Field: "f_amount"}}, false},
		{"for comparison: every purchase in the finance queue, no waiting_on", itemservice.SearchRequest{Principal: user("fay"),
			Filter: itemservice.Filter{TypeIDs: []string{"bw_purchase"}, States: []string{"finance_queue"}}}, false},
	}
	var plans []string
	for _, c := range cases {
		c.req.Limit = 50
		inView, err := countView(ctx, k, c.req)
		if err != nil {
			return fmt.Errorf("%s: %w", c.name, err)
		}
		var ms []float64
		for i := range 33 {
			t0 := time.Now()
			if _, err := k.Search(ctx, c.req); err != nil {
				return fmt.Errorf("%s: %w", c.name, err)
			}
			if i >= 3 { // the first three warm the caches
				ms = append(ms, float64(time.Since(t0).Microseconds())/1000)
			}
		}
		slices.Sort(ms)
		fmt.Printf("| %s | %s | %.1f | %.1f | %.1f |\n", c.name, inView, ms[len(ms)/2], ms[len(ms)*9/10], ms[len(ms)-1])
		if !c.plan {
			continue
		}
		got, err := trace.during(func() error { _, err := k.Search(ctx, c.req); return err })
		if err != nil {
			return err
		}
		for _, st := range got {
			if !isPage(st.sql) {
				continue
			}
			text, err := explainAnalyze(ctx, plain, st)
			if err != nil {
				return err
			}
			plans = append(plans, "## "+c.name+"\n\n```\n"+text+"\n```\n")
		}
	}
	fmt.Println()
	fmt.Println(strings.Join(plans, "\n"))
	return nil
}

func withWaiting(f itemservice.Filter, on string) itemservice.Filter {
	f.WaitingOn = on
	return f
}

// countView pages through the whole view and says how many Items it holds
// ("5,000+" once it passes ten pages of 500: counting is not what is
// measured).
func countView(ctx context.Context, k *itemservice.Service, req itemservice.SearchRequest) (string, error) {
	req.Limit = 500
	n := 0
	for page := 0; page < 10; page++ {
		res, err := k.Search(ctx, req)
		if err != nil {
			return "", err
		}
		n += len(res.Items)
		if req.PageToken = res.NextPageToken; req.PageToken == "" {
			return fmt.Sprint(n), nil
		}
	}
	return fmt.Sprintf("%d+", n), nil
}

// explainAnalyze runs one captured statement under EXPLAIN (ANALYZE) and
// returns its plan, without the lines that only repeat the arguments.
func explainAnalyze(ctx context.Context, plain *pgxpool.Pool, st captured) (string, error) {
	rows, err := plain.Query(ctx, "explain (analyze, costs off, timing off) "+st.sql, st.args...)
	if err != nil {
		return "", fmt.Errorf("explain: %w", err)
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			return "", err
		}
		if len(line) > 160 {
			line = line[:160] + " ..."
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n"), rows.Err()
}
