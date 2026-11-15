package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"

	"github.com/francoisnyc/work-kernel/internal/connstub"
	"github.com/francoisnyc/work-kernel/internal/hooks"
	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
	"github.com/francoisnyc/work-kernel/internal/relay"
	"github.com/francoisnyc/work-kernel/internal/testdb"
	"github.com/francoisnyc/work-kernel/internal/workflow"
)

// Step 6 bench (go run ./cmd/bench -holds): variant D, with ticket v3, the
// real relay and worker, and stub CRM and ERP servers.
//
//   - Time to a definitive answer for an approval, with a 3 s wait: the
//     caller retries with the same key while the answer is "pending".
//   - Kernel write latency, at baseline, during a stub outage, and with the
//     worker stopped.
const (
	holdsWait     = 3 * time.Second
	holdsBudget   = 10 * time.Second
	holdsTickets  = 60 // approvals per stub latency
	holdsCallers  = 4  // concurrent approvers
	holdsWrites   = 200
	holdsWriters  = 4
	holdsPrefetch = 15 * time.Second
)

type holdsBench struct {
	pool     *pgxpool.Pool
	k        *itemservice.Service
	tc       client.Client
	queue    string
	crm, erp *connstub.Server
	w        worker.Worker
	ana      item.Principal
}

func holdsMode(ctx context.Context) error {
	pool, stopDB, err := testdb.Start(ctx)
	if err != nil {
		return err
	}
	defer stopDB()
	if err := registerFile(ctx, pool, "ticket.v3.yaml"); err != nil {
		return err
	}
	tc, stopTemporal, err := startTemporal(ctx)
	if err != nil {
		return err
	}
	defer stopTemporal()

	b := &holdsBench{
		pool: pool, k: itemservice.New(pool), tc: tc, queue: "bench-" + uuid.NewString(),
		crm: connstub.NewCRM(), erp: connstub.NewERP(),
		ana: item.Principal{Actor: "user:bench-approver"},
	}
	defer b.crm.Close()
	defer b.erp.Close()
	if _, err := pool.Exec(ctx,
		`insert into principal_roles (namespace_id, principal, role) values ('default', $1, 'approver')`,
		b.ana.Actor); err != nil {
		return err
	}
	if err := b.startWorker(); err != nil {
		return err
	}
	defer func() { b.stopWorker() }()
	r := relay.New(pool, relay.NewTemporalWorkflows(tc, b.queue), relay.Options{PollInterval: 50 * time.Millisecond})
	rctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() { _ = r.Run(rctx) }()

	fmt.Println("# Step 6 bench: holds and connectors, variant D")
	fmt.Println()
	machine(ctx, pool)
	fmt.Printf("ticket v3; relay polling every 50 ms; hook budget %v; approvals with wait %v, "+
		"%d concurrent approvers; %d approvals per stub latency.\n", holdsBudget, holdsWait, holdsCallers, holdsTickets)
	fmt.Println("Mix per 10 tickets: 6 clean, 2 prefetched on hold (the CEL guard answers), " +
		"2 put on hold after the prefetch (stale: the hold's check answers).")
	fmt.Println()
	fmt.Println("## Time to a definitive answer")
	fmt.Println()
	fmt.Println("| Stub latency | Answered | p50 ms | p95 ms | max ms | Within the wait | Applied / denied (guard) / denied (check) |")
	fmt.Println("| --- | --- | --- | --- | --- | --- | --- |")
	for _, lat := range []time.Duration{50 * time.Millisecond, 500 * time.Millisecond} {
		if err := b.answers(ctx, lat); err != nil {
			return err
		}
	}
	fmt.Println()
	return b.writeLatency(ctx)
}

func (b *holdsBench) startWorker() error {
	b.w = workflow.NewWorker(b.tc, b.queue, b.k, workflow.WorkerOptions{
		Connectors: hooks.NewConnectors(b.crm.URL, b.erp.URL), HookBudget: holdsBudget,
	})
	return b.w.Start()
}

func (b *holdsBench) stopWorker() {
	if b.w != nil {
		b.w.Stop()
		b.w = nil
	}
}

// ticket creates a ticket for a new customer, waits for its prefetch, and
// moves it to in_progress. kind: 0 clean, 1 prefetched on hold, 2 stale.
func (b *holdsBench) ticket(ctx context.Context, kind int) (uuid.UUID, error) {
	customer := "cust-" + uuid.NewString()[:8]
	b.crm.SetCustomer(customer, connstub.Customer{Plan: "pro"})
	b.erp.SetCustomer(customer, connstub.Customer{OnHold: kind == 1, CreditLimit: 1000})
	res, err := b.k.Create(ctx, itemservice.CreateRequest{
		IdempotencyKey: key(), Principal: b.ana, TypeID: "ticket", TypeVersion: 3,
		Fields: map[string]any{"f_title": "bench", "f_customer_id": customer, "f_amount": 100, "f_assignee": "user-bob"},
	})
	if err != nil {
		return uuid.Nil, err
	}
	deadline := time.Now().Add(holdsPrefetch)
	for {
		it, err := b.k.Get(ctx, b.ana, res.ItemID)
		if err != nil {
			return uuid.Nil, err
		}
		if it.Fields["f_plan"] != nil && it.Fields["f_on_hold"] != nil {
			break
		}
		if time.Now().After(deadline) {
			return uuid.Nil, fmt.Errorf("ticket %s: prefetch not done within %v", res.ItemID, holdsPrefetch)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if kind == 2 { // the fact goes stale after the prefetch read it
		b.erp.SetCustomer(customer, connstub.Customer{OnHold: true, CreditLimit: 1000})
	}
	for _, to := range []string{"open", "in_progress"} {
		if _, err := b.k.Transition(ctx, itemservice.TransitionRequest{
			IdempotencyKey: key(), Principal: b.ana, ItemID: res.ItemID, ToState: to,
		}); err != nil {
			return uuid.Nil, err
		}
	}
	return res.ItemID, nil
}

// decide asks for the approval and keeps asking, with the same key, until
// the answer is definitive. A guard's refusal is definitive at once.
func (b *holdsBench) decide(ctx context.Context, id uuid.UUID) (outcome string, took time.Duration, err error) {
	k := key()
	start := time.Now()
	for {
		res, err := b.k.Transition(ctx, itemservice.TransitionRequest{
			IdempotencyKey: k, Principal: b.ana, ItemID: id, ToState: "approved", Wait: holdsWait,
		})
		if errors.Is(err, itemservice.ErrRejected) {
			return "denied_guard", time.Since(start), nil
		}
		if err != nil {
			return "", 0, err
		}
		if res.Outcome != itemservice.OutcomePending {
			return string(res.Outcome) + "_" + res.Reason, time.Since(start), nil
		}
		if time.Since(start) > time.Minute {
			return "", 0, fmt.Errorf("ticket %s: no answer within a minute", id)
		}
	}
}

func (b *holdsBench) answers(ctx context.Context, lat time.Duration) error {
	b.crm.SetLatency(lat)
	b.erp.SetLatency(lat)
	ids := make([]uuid.UUID, holdsTickets)
	for i := range ids {
		kind := 0
		switch i % 10 {
		case 6, 7:
			kind = 1
		case 8, 9:
			kind = 2
		}
		id, err := b.ticket(ctx, kind)
		if err != nil {
			return err
		}
		ids[i] = id
	}
	outcomes := make([]string, holdsTickets)
	lats, _, err := timeAll(holdsTickets, holdsCallers, func(i int) error {
		o, _, err := b.decide(ctx, ids[i])
		outcomes[i] = o
		return err
	})
	if err != nil {
		return err
	}
	within := 0
	for _, d := range lats {
		if d <= holdsWait {
			within++
		}
	}
	count := map[string]int{}
	for _, o := range outcomes {
		count[o]++
	}
	fmt.Printf("| %v | %d | %s | %s | %s | %d%% | %d / %d / %d |\n", lat, len(lats),
		ms(lats.pct(0.5)), ms(lats.pct(0.95)), ms(lats[len(lats)-1]), 100*within/len(lats),
		count["applied_"], count["denied_guard"], count["denied_customer_on_hold"])
	if other := len(lats) - count["applied_"] - count["denied_guard"] - count["denied_customer_on_hold"]; other > 0 {
		fmt.Printf("|  | other outcomes: %v |  |  |  |  |  |\n", count)
	}
	return nil
}

// writeLatency measures plain kernel writes (a field update) and the
// request that opens a hold (approve with wait 0), at baseline, during an
// outage of both stubs, and with the worker stopped.
func (b *holdsBench) writeLatency(ctx context.Context) error {
	b.crm.SetLatency(50 * time.Millisecond)
	b.erp.SetLatency(50 * time.Millisecond)
	fmt.Println("## Kernel write latency")
	fmt.Println()
	fmt.Printf("%d writes per row, %d writers. Update: one field. Request: approve with wait 0, which opens a hold.\n\n",
		holdsWrites, holdsWriters)
	fmt.Println("| Condition | Update p50 ms | Update p95 ms | Request p50 ms | Request p95 ms |")
	fmt.Println("| --- | --- | --- | --- | --- |")
	conditions := []struct {
		name        string
		before, end func()
	}{
		{"baseline", func() {}, func() {}},
		{"CRM and ERP down", func() { b.crm.SetOutage(true); b.erp.SetOutage(true) },
			func() { b.crm.SetOutage(false); b.erp.SetOutage(false) }},
		{"worker stopped", b.stopWorker, func() { _ = b.startWorker() }},
	}
	for _, c := range conditions {
		// Tickets are prepared with everything up; then the condition starts.
		ids := make([]uuid.UUID, holdsWrites)
		for i := range ids {
			id, err := b.ticket(ctx, 0)
			if err != nil {
				return err
			}
			ids[i] = id
		}
		c.before()
		upd, _, err := timeAll(holdsWrites, holdsWriters, func(i int) error {
			_, err := b.k.Update(ctx, itemservice.UpdateRequest{
				IdempotencyKey: key(), Principal: b.ana, ItemID: ids[i],
				Fields: map[string]any{"f_title": fmt.Sprintf("edit %d", i)},
			})
			return err
		})
		if err != nil {
			c.end()
			return err
		}
		req, _, err := timeAll(holdsWrites, holdsWriters, func(i int) error {
			res, err := b.k.Transition(ctx, itemservice.TransitionRequest{
				IdempotencyKey: key(), Principal: b.ana, ItemID: ids[i], ToState: "approved",
			})
			if err == nil && !slices.Contains([]itemservice.Outcome{itemservice.OutcomePending,
				itemservice.OutcomeApplied, itemservice.OutcomeDenied}, res.Outcome) {
				err = fmt.Errorf("unexpected outcome %s", res.Outcome)
			}
			return err
		})
		c.end()
		if err != nil {
			return err
		}
		fmt.Printf("| %s | %s | %s | %s | %s |\n", c.name, ms(upd.pct(0.5)), ms(upd.pct(0.95)), ms(req.pct(0.5)), ms(req.pct(0.95)))
	}
	fmt.Println()
	return nil
}
