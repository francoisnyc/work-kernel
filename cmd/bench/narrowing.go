package main

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
	"github.com/francoisnyc/work-kernel/internal/testdb"
)

// narrowingMode measures what narrowing one container costs in scope_access
// rows when it has N descendant scope nodes, for R readers.
func narrowingMode(ctx context.Context) error {
	pool, stopDB, err := testdb.Start(ctx)
	if err != nil {
		return err
	}
	defer stopDB()
	if err := registerFile(ctx, pool, "ticket.v1.yaml"); err != nil {
		return err
	}
	fmt.Println("# Narrowing cost")
	fmt.Println()
	machine(ctx, pool)
	fmt.Println("A container with N children, each of which has one child of its own, so each")
	fmt.Println("child owns a scope node: N descendant nodes. The container is then narrowed to R")
	fmt.Println("readers, and narrowed again to a different R readers (a reader change).")
	fmt.Println()
	fmt.Println("| N nodes | R readers | Narrow: scope_access rows written | Narrow: ms | Reader change: rows written | Reader change: ms |")
	fmt.Println("| --- | --- | --- | --- | --- | --- |")
	for _, c := range []struct{ n, r int }{{100, 1}, {100, 50}, {1000, 1}, {1000, 10}, {1000, 50}} {
		if err := narrowOnce(ctx, pool, c.n, c.r); err != nil {
			return err
		}
	}
	fmt.Println()
	return nil
}

func narrowOnce(ctx context.Context, pool *pgxpool.Pool, n, r int) error {
	k := itemservice.New(pool)
	owner := item.Principal{Actor: "user:owner-" + uuid.NewString()[:8]}
	create := func(container *uuid.UUID) (uuid.UUID, error) {
		res, err := k.Create(ctx, itemservice.CreateRequest{
			IdempotencyKey: key(), Principal: owner, TypeID: "ticket", TypeVersion: 1,
			Fields: map[string]any{"f_title": "x"}, ContainerID: container,
		})
		return res.ItemID, err
	}
	root, err := create(nil)
	if err != nil {
		return err
	}
	if _, _, err := timeAll(n, 8, func(int) error {
		child, err := create(&root)
		if err != nil {
			return err
		}
		_, err = create(&child) // gives the child a node of its own
		return err
	}); err != nil {
		return err
	}

	readers := func(tag string) []string {
		out := []string{owner.Actor}
		for i := 1; i < r; i++ {
			out = append(out, fmt.Sprintf("user:%s-%d", tag, i))
		}
		return out
	}
	// The recompute deletes and rewrites every scope_access row of the
	// subtree, so the rows written are the subtree's rows afterwards.
	measure := func(rs []string) (written int64, took time.Duration, err error) {
		start := time.Now()
		_, err = k.Update(ctx, itemservice.UpdateRequest{
			IdempotencyKey: key(), Principal: owner, ItemID: root,
			Policy: &itemservice.PolicyPatch{Readers: rs},
		})
		took = time.Since(start)
		if err != nil {
			return 0, 0, err
		}
		err = pool.QueryRow(ctx,
			`with recursive t as (
			     select scope_id as id from items where id = $1
			     union all
			     select s.id from scopes s join t on s.parent_id = t.id
			 )
			 select count(*) from scope_access where scope_id in (select id from t)`, root).Scan(&written)
		return written, took, err
	}
	w1, t1, err := measure(readers("a"))
	if err != nil {
		return err
	}
	w2, t2, err := measure(readers("b"))
	if err != nil {
		return err
	}
	fmt.Printf("| %d | %d | %d | %s | %d | %s |\n", n, r, w1, ms(t1), w2, ms(t2))
	return nil
}
