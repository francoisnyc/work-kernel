package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
	"github.com/francoisnyc/work-kernel/internal/testdb"
)

// The SQL below is copied from policy.VisibleScopes, Service.Get and
// Service.Search so the plans are those of the real queries. Keep it in
// step with them; it is used for nothing else.
const (
	explainVisibleScopes = `with recursive visible as (
		     select id from scopes where namespace_id = $1 and parent_id is null
		     union all
		     select s.id
		     from scopes s
		     join visible v on s.parent_id = v.id
		     where not s.narrows
		        or (select count(*) from scope_readers r
		            where r.scope_id = s.id and r.principal = any($2)) = $3
		 )
		 select id from visible`
	explainGet = `select type_id, type_version, container_id, scope_id, state, fields, item_seq
		 from items where id = $1 and namespace_id = $2`
	explainSearch = `select id, type_id, type_version, container_id, scope_id, state, fields, item_seq
		 from items
		 where namespace_id = $1
		   and scope_id = any($2)
		   and search @@ plainto_tsquery('simple', $3)
		 order by id desc
		 limit $4`
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
	heavy := item.Principal{Actor: "user-heavy"}
	light := item.Principal{Actor: "user-light"}
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

	fmt.Println("# EXPLAIN (ANALYZE, BUFFERS)")
	fmt.Println()
	machine(ctx, pool)
	if err := explainAll(ctx, pool, "Fresh database: 1,000 narrowed tickets, 1 plain ticket", heavy, light, narrowed, term); err != nil {
		return err
	}

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
			ContainerID: &ch, Fields: map[string]any{"f_body": fmt.Sprintf("hello, this is message %d", i)},
		})
		return err
	}); err != nil {
		return err
	}
	return explainAll(ctx, pool, "After the 30,000-message burst", heavy, light, narrowed, term)
}

func explainAll(ctx context.Context, pool *pgxpool.Pool, title string,
	heavy, light item.Principal, narrowed uuid.UUID, term string) error {

	fmt.Printf("## %s\n\n", title)
	if err := tableStats(ctx, pool); err != nil {
		return err
	}
	visible := map[string][]uuid.UUID{}
	for _, p := range []item.Principal{heavy, light} {
		rows, err := pool.Query(ctx, explainVisibleScopes, "default", []string{p.Actor}, 1)
		if err != nil {
			return err
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
		if err != nil {
			return err
		}
		visible[p.Actor] = ids
	}

	steps := []struct {
		name string
		sql  string
		args []any
	}{
		{"VisibleScopes for user-heavy (step 1 of the heavy Get)", explainVisibleScopes, []any{"default", []string{heavy.Actor}, 1}},
		{"The Item read (step 2 of the heavy Get)", explainGet, []any{narrowed, "default"}},
		{"VisibleScopes for user-light (step 1 of the light Search)", explainVisibleScopes, []any{"default", []string{light.Actor}, 1}},
		{"The search query for user-light (step 2)", explainSearch, []any{"default", visible[light.Actor], term, 50}},
		{"The search query for user-heavy, for comparison", explainSearch, []any{"default", visible[heavy.Actor], term, 50}},
	}
	for _, s := range steps {
		// Run once first, as the bench does, so the plan reflects a warm cache.
		if rows, err := pool.Query(ctx, s.sql, s.args...); err == nil {
			rows.Close()
		}
		rows, err := pool.Query(ctx, "explain (analyze, buffers) "+s.sql, s.args...)
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
