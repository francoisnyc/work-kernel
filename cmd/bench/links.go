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

// Proof of concept, Step 1: the plans of the link reads and the new
// filters. The SQL is copied from internal/itemservice (itemLinks,
// readLinkSet, linkCond, fieldConds and page), with the visibility
// condition spelled out; keep it in step with the code.
const (
	// Get, outgoing: every target is shown (restricted when hidden), so
	// visibility is a column, not a filter.
	explainLinksOut = `select l.link_type, l.target_id, ` + explainVisible + `
		 from links l join items o on o.id = l.target_id join scopes s on s.id = o.scope_id
		 where l.source_id = @id
		 order by l.created_at, l.link_type, l.target_id
		 limit 201`
	// Get, incoming: a hidden source is left out, so visibility filters.
	explainLinksIn = `select l.source_type_id, l.link_type, l.source_id
		 from links l join items o on o.id = l.source_id join scopes s on s.id = o.scope_id
		 where l.target_id = @id and ` + explainVisible + `
		 order by l.created_at, l.link_type, l.source_id
		 limit 201`
	// A guard's link set (blocked_by), read before and after the locks.
	explainGuardLinks = `select source_id from links where target_id = @id and link_type = 'blocks' and source_type_id = 'ticket'
		 order by source_id`
	// "Assigned to me": one Type and a principal field's equality.
	explainAssigned = `select ` + explainItemCols + `
		 from items m join scopes s on s.id = m.scope_id
		 where m.namespace_id = @ns and m.type_id = 'ticket' and ((m.fields ->> 'f_assignee') = @me::text)
		   and ` + explainVisible + `
		 order by m.created_at desc, m.id desc
		 limit 51`
	// "Linked to", outgoing: Items that block @id.
	explainLinkedOut = `select ` + explainItemCols + `
		 from items m join scopes s on s.id = m.scope_id
		 where m.namespace_id = @ns
		   and m.id in (select l.source_id from links l where l.target_id = @id and l.link_type = 'blocks')
		   and ` + explainVisible + `
		 order by m.created_at desc, m.id desc
		 limit 51`
	// "Linked to", incoming: Items blocked by @id (blocked_by, the inverse).
	explainLinkedIn = `select ` + explainItemCols + `
		 from items m join scopes s on s.id = m.scope_id
		 where m.namespace_id = @ns
		   and m.id in (select l.target_id from links l where l.source_id = @id
		       and (l.source_type_id, l.link_type) in (select * from unnest('{ticket}'::text[], '{blocks}'::text[])))
		   and ` + explainVisible + `
		 order by m.created_at desc, m.id desc
		 limit 51`
)

// linksExplainMode builds 30,000 tickets (v5), 1,000 more hidden from the
// reader, a hub ticket with 2,000 links each way (10% to or from hidden
// tickets), 10,000 other links, and a small ticket with two links each
// way; then prints the plans fresh and after ANALYZE. Items are created
// through the item service; links are bulk-inserted in SQL (the plans only
// depend on the rows), with created_at a millisecond apart.
func linksExplainMode(ctx context.Context) error {
	pool, stopDB, err := testdb.Start(ctx)
	if err != nil {
		return err
	}
	defer stopDB()
	if err := registerFile(ctx, pool, "ticket.v5.yaml"); err != nil {
		return err
	}
	k := itemservice.New(pool)
	me := item.Principal{Actor: "user:me"}
	heavy := item.Principal{Actor: "user:heavy"}

	const open, hidden = 30000, 1000
	ids := make([]uuid.UUID, open+hidden)
	assignee := func(i int) any {
		switch {
		case i%100 == 0:
			return me.Actor // 1% of the open tickets, half the hidden ones
		case i%10 == 9:
			return nil
		}
		return fmt.Sprintf("user:u%d", i%97)
	}
	if _, _, err := timeAll(open+hidden, 16, func(i int) error {
		req := itemservice.CreateRequest{
			IdempotencyKey: key(), Principal: alice, TypeID: "ticket", TypeVersion: 5,
			Fields: map[string]any{"f_title": fmt.Sprintf("ticket %d", i)},
		}
		if a := assignee(i); a != nil {
			req.Fields["f_assignee"] = a
		}
		if i >= open {
			req.Principal = heavy
			req.Narrow = &itemservice.Narrow{Readers: []string{heavy.Actor}}
			if i%2 == 0 {
				req.Fields["f_assignee"] = me.Actor
			}
		}
		res, err := k.Create(ctx, req)
		ids[i] = res.ItemID
		return err
	}); err != nil {
		return err
	}

	hub, small := ids[1], ids[2]
	type link struct{ src, dst uuid.UUID }
	var links []link
	seen := map[link]bool{}
	add := func(l link) {
		if l.src != l.dst && !seen[l] {
			seen[l] = true
			links = append(links, l)
		}
	}
	for i := range 2000 {
		other := ids[10+i]
		if i%10 == 0 {
			other = ids[open+i%hidden] // 10% hidden from the reader
		}
		add(link{other, hub})
		add(link{hub, ids[5000+i]})
		if i%10 == 0 {
			add(link{hub, ids[open+(i+500)%hidden]})
		}
	}
	add(link{ids[3], small})
	add(link{ids[4], small})
	add(link{small, ids[5]})
	add(link{small, ids[6]})
	for i := range 10000 {
		add(link{ids[(i*7919)%open], ids[(i*104729+13)%open]})
	}
	rows := make([][]any, 0, len(links))
	for i, l := range links {
		rows = append(rows, []any{"default", l.src, "ticket", "blocks", l.dst, false, false, i})
	}
	// A temp table lives on one connection: seed through one.
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `create temp table link_seed (namespace_id text, source_id uuid,
		source_type_id text, link_type text, target_id uuid, src_single bool, dst_single bool, n int)`); err != nil {
		return err
	}
	if _, err := conn.CopyFrom(ctx, pgx.Identifier{"link_seed"},
		[]string{"namespace_id", "source_id", "source_type_id", "link_type", "target_id", "src_single", "dst_single", "n"},
		pgx.CopyFromRows(rows)); err != nil {
		return err
	}
	if _, err := conn.Exec(ctx, `insert into links (namespace_id, source_id, source_type_id, link_type, target_id,
		src_single, dst_single, created_at)
		select namespace_id, source_id, source_type_id, link_type, target_id, src_single, dst_single,
		       now() + n * interval '1 millisecond' from link_seed`); err != nil {
		return fmt.Errorf("seed links: %w", err)
	}

	fmt.Println("# EXPLAIN (ANALYZE, BUFFERS): links and principal fields")
	fmt.Println()
	machine(ctx, pool)
	if err := explainLinks(ctx, pool, "Before ANALYZE", me, hub, small); err != nil {
		return err
	}
	if _, err := pool.Exec(ctx, `analyze`); err != nil {
		return err
	}
	return explainLinks(ctx, pool, "After ANALYZE", me, hub, small)
}

func explainLinks(ctx context.Context, pool *pgxpool.Pool, title string, me item.Principal, hub, small uuid.UUID) error {
	fmt.Printf("## %s\n\n", title)
	var n int
	if err := pool.QueryRow(ctx, `select count(*) from links`).Scan(&n); err != nil {
		return err
	}
	fmt.Printf("links: %d rows\n\n", n)
	args := func(id uuid.UUID) pgx.NamedArgs {
		return pgx.NamedArgs{"actor": me.Actor, "behalf": "", "ns": "default", "me": me.Actor, "id": id}
	}
	steps := []struct {
		name string
		sql  string
		args pgx.NamedArgs
	}{
		{"Get, outgoing links, hub (2,200 targets, 400 hidden)", explainLinksOut, args(hub)},
		{"Get, incoming links, hub (2,000 sources, 200 hidden)", explainLinksIn, args(hub)},
		{"Get, outgoing links, small (2)", explainLinksOut, args(small)},
		{"Get, incoming links, small (2)", explainLinksIn, args(small)},
		{"Guard link set (blocked_by), hub", explainGuardLinks, args(hub)},
		{"Assigned to me (300 visible, 500 hidden, of 31,000)", explainAssigned, args(uuid.Nil)},
		{"Linked to, outgoing: Items that block the hub", explainLinkedOut, args(hub)},
		{"Linked to, incoming: Items blocked by the hub", explainLinkedIn, args(hub)},
		{"Linked to, outgoing: Items that block small", explainLinkedOut, args(small)},
		{"Linked to, incoming: Items blocked by small", explainLinkedIn, args(small)},
	}
	for _, s := range steps {
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
