package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
	"github.com/francoisnyc/work-kernel/internal/testdb"
)

// The SQL below is copied from Service.Get and Service.Search (with
// policy.VisibleCondition spelled out) so the plans are those of the real
// queries. searchFlat is the same search without the MATERIALIZED step, the
// shape before Step A; searchUnion splits the visibility OR into two branches.
// Keep them in step with the code; they are used for nothing else.
const (
	explainVisible = `(s.policy_scope_id is null or (
	exists (select 1 from scope_access a where a.scope_id = s.policy_scope_id and a.principal = @actor)
	and (@behalf = '' or exists (select 1 from scope_access b where b.scope_id = s.policy_scope_id and b.principal = @behalf))))`
	explainGet = `select i.type_id, i.type_version, i.container_id, i.scope_id, i.state, i.fields, i.item_seq, ` + explainVisible + `
		 from items i join scopes s on s.id = i.scope_id
		 where i.id = @id and i.namespace_id = @ns`
	explainSearch = `with matches as materialized (
		     select id, type_id, type_version, container_id, scope_id, state, fields, item_seq
		     from items
		     where namespace_id = @ns and search @@ plainto_tsquery('simple', @q)
		 )
		 select m.id, m.type_id, m.type_version, m.container_id, m.scope_id, m.state, m.fields, m.item_seq
		 from matches m join scopes s on s.id = m.scope_id
		 where ` + explainVisible + `
		 order by m.id desc
		 limit @limit`
	explainSearchFlat = `select i.id, i.type_id, i.type_version, i.container_id, i.scope_id, i.state, i.fields, i.item_seq
		 from items i join scopes s on s.id = i.scope_id
		 where i.namespace_id = @ns and i.search @@ plainto_tsquery('simple', @q)
		   and ` + explainVisible + `
		 order by i.id desc
		 limit @limit`
	explainSearchUnion = `with matches as materialized (
		     select id, type_id, type_version, container_id, scope_id, state, fields, item_seq
		     from items
		     where namespace_id = @ns and search @@ plainto_tsquery('simple', @q)
		 )
		 select * from (
		     select m.* from matches m join scopes s on s.id = m.scope_id where s.policy_scope_id is null
		     union all
		     select m.* from matches m join scopes s on s.id = m.scope_id
		     join scope_access a on a.scope_id = s.policy_scope_id and a.principal = @actor
		 ) v
		 order by id desc
		 limit @limit`
)

// Step 0, part 2: the new reads, copied from page (Search and List) and
// ListEvents in internal/itemservice, with the visibility condition spelled
// out. A page reads one row more than it returns, to know there is a next.
const (
	explainItemCols = `m.id, m.type_id, m.type_version, m.container_id, m.scope_id, m.state, m.fields, m.item_seq,
		        m.created_at, m.created_by, coalesce(m.created_on_behalf_of, ''), s.policy_scope_id`
	explainListChildren = `select ` + explainItemCols + `
		 from items m join scopes s on s.id = m.scope_id
		 where m.namespace_id = @ns and m.container_id = @parent and ` + explainVisible + `
		 order by m.created_at desc, m.id desc
		 limit @limit`
	explainListTop = `select ` + explainItemCols + `
		 from items m join scopes s on s.id = m.scope_id
		 where m.namespace_id = @ns and m.container_id is null and ` + explainVisible + `
		 order by m.created_at desc, m.id desc
		 limit @limit`
	explainFilterTypeState = `select ` + explainItemCols + `
		 from items m join scopes s on s.id = m.scope_id
		 where m.namespace_id = @ns and m.type_id = any(@f_types) and m.state = any(@f_states)
		   and ` + explainVisible + `
		 order by m.created_at desc, m.id desc
		 limit @limit`
	explainFilterCategory = `select ` + explainItemCols + `
		 from items m join scopes s on s.id = m.scope_id
		 where m.namespace_id = @ns and (m.type_id, m.type_version, m.state) in
		     (select * from unnest(@f_cat_types::text[], @f_cat_versions::int[], @f_cat_states::text[]))
		   and ` + explainVisible + `
		 order by m.created_at desc, m.id desc
		 limit @limit`
	// One Type and one state are compared with "=" (oneOrAny in the filter).
	explainFilterOneTypeState = `select ` + explainItemCols + `
		 from items m join scopes s on s.id = m.scope_id
		 where m.namespace_id = @ns and m.type_id = @f_type and m.state = @f_state
		   and ` + explainVisible + `
		 order by m.created_at desc, m.id desc
		 limit @limit`
	explainListPage2 = `select ` + explainItemCols + `
		 from items m join scopes s on s.id = m.scope_id
		 where m.namespace_id = @ns and m.container_id = @parent
		   and (m.created_at, m.id) < (@after_at, @after_id) and ` + explainVisible + `
		 order by m.created_at desc, m.id desc
		 limit @limit`
	explainEvents = `select e.item_seq, e.commit_seq, e.op, e.created_at, e.actor, coalesce(e.on_behalf_of, ''),
		        coalesce(e.originating_hook, ''), coalesce(e.causation_id, ''), coalesce(e.correlation_id, ''),
		        e.hop, coalesce(e.payload, '{}'), v.field_values
		 from item_events e
		 left join item_event_values v on v.item_id = e.item_id and v.item_seq = e.item_seq
		 where e.item_id = @id and e.item_seq > 0
		 order by e.item_seq
		 limit @limit`
)

// explainMode builds a database like the bench's scopes section, prints the
// plans of the heavy Get and the light Search, adds the 30,000-message burst,
// and prints them again. It changes nothing in the item service.
func explainMode(ctx context.Context) error {
	pool, stopDB, err := testdb.Start(ctx)
	if err != nil {
		return err
	}
	defer stopDB()
	for _, f := range []string{"ticket.v1.yaml", "channel.v1.yaml", "message.v1.yaml"} {
		if err := registerFile(ctx, pool, f); err != nil {
			return err
		}
	}

	k := itemservice.New(pool)
	heavy := item.Principal{Actor: "user:heavy"}
	light := item.Principal{Actor: "user:light"}
	term := "w" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	var narrowed uuid.UUID
	if _, _, err := timeAll(heavyScopes, 16, func(i int) error {
		res, err := k.Create(ctx, itemservice.CreateRequest{
			IdempotencyKey: key(), Principal: heavy, TypeID: "ticket", TypeVersion: 1,
			Fields: map[string]any{"f_title": "private " + term},
			Narrow: &itemservice.Narrow{Readers: []string{heavy.Actor}},
		})
		if i == 0 {
			narrowed = res.ItemID
		}
		return err
	}); err != nil {
		return err
	}
	if _, err := k.Create(ctx, itemservice.CreateRequest{
		IdempotencyKey: key(), Principal: light, TypeID: "ticket", TypeVersion: 1,
		Fields: map[string]any{"f_title": "public " + term},
	}); err != nil {
		return err
	}

	// 30,000 messages in 16 channels; every third one carries the word
	// "alpha", so about 10,000 Items match it: the common-word case.
	chans := make([]uuid.UUID, 16)
	for i := range chans {
		res, err := k.Create(ctx, itemservice.CreateRequest{
			IdempotencyKey: key(), Principal: alice, TypeID: "channel", TypeVersion: 1,
			Fields: map[string]any{"f_name": fmt.Sprintf("channel %d", i)},
		})
		if err != nil {
			return err
		}
		chans[i] = res.ItemID
	}
	if _, _, err := timeAll(3*burstMessages, 16, func(i int) error {
		ch := chans[i%len(chans)]
		_, err := k.Create(ctx, itemservice.CreateRequest{
			IdempotencyKey: key(), Principal: alice, TypeID: "message", TypeVersion: 1,
			ContainerID: &ch, Fields: map[string]any{"f_body": fmt.Sprintf("hello %s message %d", burstWord(i), i)},
		})
		return err
	}); err != nil {
		return err
	}

	fmt.Println("# EXPLAIN (ANALYZE, BUFFERS)")
	fmt.Println()
	machine(ctx, pool)
	explainChannel = chans[0]
	if err := explainAll(ctx, pool, "Before ANALYZE", heavy, light, narrowed, term); err != nil {
		return err
	}
	if _, err := pool.Exec(ctx, `analyze`); err != nil {
		return err
	}
	return explainAll(ctx, pool, "After ANALYZE", heavy, light, narrowed, term)
}

// explainChannel is a channel of ~1,900 messages, for the list plans.
var explainChannel uuid.UUID

func explainAll(ctx context.Context, pool *pgxpool.Pool, title string,
	heavy, light item.Principal, narrowed uuid.UUID, term string) error {

	fmt.Printf("## %s\n\n", title)
	if err := tableStats(ctx, pool); err != nil {
		return err
	}
	args := func(p item.Principal, extra pgx.NamedArgs) pgx.NamedArgs {
		a := pgx.NamedArgs{"actor": p.Actor, "behalf": "", "ns": "default", "limit": 50}
		for k, v := range extra {
			a[k] = v
		}
		return a
	}
	steps := []struct {
		name string
		sql  string
		args pgx.NamedArgs
	}{
		{"Get, user-heavy (1,001 scopes)", explainGet, args(heavy, pgx.NamedArgs{"id": narrowed})},
		{"Search, rare word, user-light", explainSearch, args(light, pgx.NamedArgs{"q": term})},
		{"Search, rare word, user-heavy", explainSearch, args(heavy, pgx.NamedArgs{"q": term})},
		{"Search, common word (~10,000 matches), user-light", explainSearch, args(light, pgx.NamedArgs{"q": "alpha"})},
		{"Search, common word, user-light, flattened (before Step A)", explainSearchFlat, args(light, pgx.NamedArgs{"q": "alpha"})},
		{"Search, rare word, user-light, flattened (before Step A)", explainSearchFlat, args(light, pgx.NamedArgs{"q": term})},
		{"Search, common word, user-light, UNION ALL", explainSearchUnion, args(light, pgx.NamedArgs{"q": "alpha"})},
		// Step 0, part 2.
		{"ListItems, a channel's children (~1,900), user-light", explainListChildren,
			args(light, pgx.NamedArgs{"parent": explainChannel, "limit": 51})},
		{"ListItems, a channel's children, page 2 (keyset)", explainListPage2,
			args(light, pgx.NamedArgs{"parent": explainChannel, "limit": 51,
				"after_at": time.Now().Add(-time.Hour), "after_id": uuid.Max})},
		{"ListItems, the top level, user-light", explainListTop, args(light, pgx.NamedArgs{"limit": 51})},
		{"SearchItems, filter only: Type message, state default (~30,000), user-light", explainFilterOneTypeState,
			args(light, pgx.NamedArgs{"f_type": "message", "f_state": "default", "limit": 51})},
		{"SearchItems, filter only: Type message, state default, as = any (before)", explainFilterTypeState,
			args(light, pgx.NamedArgs{"f_types": []string{"message"}, "f_states": []string{"default"}, "limit": 51})},
		{"SearchItems, filter only: Type ticket, state open (1,001 hidden), user-light", explainFilterOneTypeState,
			args(light, pgx.NamedArgs{"f_type": "ticket", "f_state": "open", "limit": 51})},
		{"SearchItems, filter only: Types message or channel, two states", explainFilterTypeState,
			args(light, pgx.NamedArgs{"f_types": []string{"message", "channel"}, "f_states": []string{"default", "open"}, "limit": 51})},
		{"SearchItems, filter only: category todo, user-light", explainFilterCategory,
			args(light, pgx.NamedArgs{"f_cat_types": []string{"ticket"}, "f_cat_versions": []int32{1},
				"f_cat_states": []string{"open"}, "limit": 51})},
		{"ListItemEvents, one Item", explainEvents, args(light, pgx.NamedArgs{"id": narrowed, "limit": 51})},
	}
	for _, s := range steps {
		// Run once first, as the bench does, so the plan reflects a warm cache.
		if rows, err := pool.Query(ctx, s.sql, s.args); err == nil {
			rows.Close()
		}
		rows, err := pool.Query(ctx, "explain (analyze, buffers) "+s.sql, s.args)
		if err != nil {
			return fmt.Errorf("%s: %w", s.name, err)
		}
		lines, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return fmt.Errorf("%s: %w", s.name, err)
		}
		fmt.Printf("### %s\n\n```\n%s\n```\n\n", s.name, strings.Join(lines, "\n"))
	}
	return nil
}

// tableStats shows whether the planner has statistics for the tables.
func tableStats(ctx context.Context, pool *pgxpool.Pool) error {
	rows, err := pool.Query(ctx,
		`select relname, n_live_tup, coalesce(to_char(greatest(last_analyze, last_autoanalyze), 'HH24:MI:SS'), 'never')
		 from pg_stat_user_tables where relname in ('items', 'scopes', 'scope_readers') order by relname`)
	if err != nil {
		return err
	}
	defer rows.Close()
	fmt.Println("| Table | Live rows | Last analyzed |")
	fmt.Println("| --- | --- | --- |")
	for rows.Next() {
		var name, analyzed string
		var n int64
		if err := rows.Scan(&name, &n, &analyzed); err != nil {
			return err
		}
		fmt.Printf("| %s | %d | %s |\n", name, n, analyzed)
	}
	fmt.Println()
	return rows.Err()
}
