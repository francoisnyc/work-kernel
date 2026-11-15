package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
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

// Step 7 bench (go run ./cmd/bench -variants): where the ERP check runs.
//
//   - A: every check as a hold (testdata/ticket_a.v1.yaml: no prefetch, no
//     guard). Every approval waits for the ERP.
//   - D: variant D (ticket v3): an on_enter prefetch of on_hold, a CEL guard
//     that refuses at once on it, and the hold as the backstop.
//   - B: a synchronous ERP call before the transaction, 2 s timeout. Only on
//     the throwaway branch spike/variant-b, which adds it to extraVariants.
//
// For each stub latency, wait and mix, each variant approves its tickets
// with 8 concurrent callers. A caller asks with the wait and, while the
// answer is pending, asks again with the same key. Each case runs twice.
const (
	variantTickets = 40
	variantCallers = 8
	variantBudget  = 30 * time.Second
	variantWrites  = 100
	variantWriters = 4
)

// variant is one way to run the ERP check.
type variant struct {
	name     string
	typeID   string
	version  int
	prefetch bool // wait for the prefetch before moving the ticket on
	// decide asks for the approval until the answer is definitive. It
	// returns an outcome code (applied, denied_guard, denied_check, error)
	// and the time to it.
	decide func(ctx context.Context, b *variantBench, id uuid.UUID, wait time.Duration) (string, time.Duration, error)
	// request is one approval with wait 0, for the write latency table.
	request func(ctx context.Context, b *variantBench, id uuid.UUID) error
}

// extraVariants is empty here; spike/variant-b adds B to it.
var extraVariants []func(b *variantBench) variant

type variantBench struct {
	pool     *pgxpool.Pool
	k        *itemservice.Service
	tc       client.Client
	queue    string
	crm, erp *connstub.Server
	w        worker.Worker
	ana      item.Principal
}

// mix says which tickets' customers are on hold, and when.
type mix struct {
	name   string
	onHold func(i int) (before, after bool) // on hold before the prefetch, or put on hold after it
}

var mixes = []mix{
	{"0% on hold", func(int) (bool, bool) { return false, false }},
	{"20% on hold", func(i int) (bool, bool) { return i%10 >= 8, false }},
	{"20% stale", func(i int) (bool, bool) { return false, i%10 >= 8 }},
}

func variantsMode(ctx context.Context) error {
	pool, stopDB, err := testdb.Start(ctx)
	if err != nil {
		return err
	}
	defer stopDB()
	for _, f := range []string{"types/ticket.v3.yaml", "testdata/ticket_a.v1.yaml"} {
		if err := registerPath(ctx, pool, f); err != nil {
			return err
		}
	}
	tc, stopTemporal, err := startTemporal(ctx)
	if err != nil {
		return err
	}
	defer stopTemporal()

	b := &variantBench{
		pool: pool, k: itemservice.New(pool), tc: tc, queue: "bench-" + uuid.NewString(),
		crm: connstub.NewCRM(), erp: connstub.NewERP(), ana: item.Principal{Actor: "user:bench-approver"},
	}
	defer b.crm.Close()
	defer b.erp.Close()
	if _, err := pool.Exec(ctx,
		`insert into principal_roles (namespace_id, principal, role) values ('default', $1, 'approver')`,
		b.ana.Actor); err != nil {
		return err
	}
	b.w = workflow.NewWorker(tc, b.queue, b.k, workflow.WorkerOptions{
		Connectors: hooks.NewConnectors(b.crm.URL, b.erp.URL), HookBudget: variantBudget,
	})
	if err := b.w.Start(); err != nil {
		return err
	}
	defer b.w.Stop()
	r := relay.New(pool, relay.NewTemporalWorkflows(tc, b.queue), relay.Options{PollInterval: 50 * time.Millisecond})
	rctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() { _ = r.Run(rctx) }()

	variants := []variant{heldVariant("A", "ticket_a", 1, false), heldVariant("D", "ticket", 3, true)}
	for _, extra := range extraVariants {
		variants = append(variants, extra(b))
	}

	fmt.Println("# Step 7 bench: A, B and D")
	fmt.Println()
	machine(ctx, pool)
	fmt.Printf("Relay polling every 50 ms; hook budget %v; %d approvals per case, %d concurrent callers; "+
		"each case run twice (run 1 / run 2). The CRM and ERP stubs answer with the latency given.\n",
		variantBudget, variantTickets, variantCallers)
	fmt.Println("Mixes: 0% on hold; 20% on hold (D's prefetch sees it, its guard answers); 20% stale (the customer " +
		"goes on hold after D's prefetch; the check answers). For A the stale mix is the 20% mix: it has no prefetch.")
	fmt.Println("\"Within the wait\": definitive on the first call. B has no wait: it answers within its 2 s ERP timeout or not at all.")
	fmt.Println()
	fmt.Println("## Time to a definitive answer")
	fmt.Println()
	fmt.Println("| Latency | Wait | Mix | Variant | p50 ms | p95 ms | Within the wait | Outcomes (run 1) |")
	fmt.Println("| --- | --- | --- | --- | --- | --- | --- | --- |")

	var breakdown []string
	for _, lat := range []time.Duration{50 * time.Millisecond, 500 * time.Millisecond, 5 * time.Second} {
		b.crm.SetLatency(lat)
		b.erp.SetLatency(lat)
		for _, wait := range []time.Duration{3 * time.Second, 5 * time.Second} {
			for _, m := range mixes {
				for _, v := range variants {
					if m.name == "20% stale" && !v.prefetch {
						continue // the same as 20% on hold without a prefetch
					}
					var p50, p95, within []string
					var outcomes string
					for run := 1; run <= 2; run++ {
						res, err := b.measure(ctx, v, m, wait)
						if err != nil {
							return fmt.Errorf("%s %v %v %s: %w", v.name, lat, wait, m.name, err)
						}
						p50 = append(p50, ms(res.lats.pct(0.5)))
						p95 = append(p95, ms(res.lats.pct(0.95)))
						within = append(within, fmt.Sprintf("%d%%", 100*res.firstCall/len(res.lats)))
						if run == 1 {
							outcomes = res.outcomes()
							if lat == 50*time.Millisecond && wait == 3*time.Second && m.name == "0% on hold" && len(res.legs) > 0 {
								breakdown = append(breakdown, legsRow(v.name, res.legs))
							}
						}
					}
					fmt.Printf("| %v | %v | %s | %s | %s | %s | %s | %s |\n", lat, wait, m.name, v.name,
						strings.Join(p50, " / "), strings.Join(p95, " / "), strings.Join(within, " / "), outcomes)
				}
			}
		}
	}
	fmt.Println()
	fmt.Println("## Where the time goes at 50 ms (0% on hold, wait 3 s, run 1)")
	fmt.Println()
	fmt.Println("Each held approval split into legs, p50 / p95 ms. Request: the call until its transaction " +
		"started. To the ERP: from there until the check reached the ERP (relay poll, update-with-start, " +
		"workflow task, activity, hook input read). ERP + write: the ERP's latency and the resolution's " +
		"transaction start. Back to the caller: until the waiting call returned (the wait polls every 50 ms). " +
		"Event times are the transaction's start (Postgres now()).")
	fmt.Println()
	fmt.Println("| Variant | Request | To the ERP | ERP + write | Back to the caller | Total |")
	fmt.Println("| --- | --- | --- | --- | --- | --- |")
	for _, row := range breakdown {
		fmt.Println(row)
	}
	fmt.Println()
	return b.writeLatency(ctx, variants)
}

// heldVariant is A or D: the check runs as a hold.
func heldVariant(name, typeID string, version int, prefetch bool) variant {
	return variant{
		name: name, typeID: typeID, version: version, prefetch: prefetch,
		decide: func(ctx context.Context, b *variantBench, id uuid.UUID, wait time.Duration) (string, time.Duration, error) {
			k := key()
			start := time.Now()
			for {
				res, err := b.k.Transition(ctx, itemservice.TransitionRequest{
					IdempotencyKey: k, Principal: b.ana, ItemID: id, ToState: "approved", Wait: wait,
				})
				if errors.Is(err, itemservice.ErrRejected) {
					return "denied_guard", time.Since(start), nil
				}
				if err != nil {
					return "", 0, err
				}
				switch res.Outcome {
				case itemservice.OutcomeApplied:
					return "applied", time.Since(start), nil
				case itemservice.OutcomeDenied:
					return "denied_check", time.Since(start), nil
				case itemservice.OutcomeError:
					return "error", time.Since(start), nil
				}
				if time.Since(start) > 2*time.Minute {
					return "", 0, fmt.Errorf("ticket %s: no answer within two minutes", id)
				}
			}
		},
		request: func(ctx context.Context, b *variantBench, id uuid.UUID) error {
			res, err := b.k.Transition(ctx, itemservice.TransitionRequest{
				IdempotencyKey: key(), Principal: b.ana, ItemID: id, ToState: "approved",
			})
			if err == nil && !slices.Contains([]itemservice.Outcome{itemservice.OutcomePending,
				itemservice.OutcomeApplied, itemservice.OutcomeDenied}, res.Outcome) {
				err = fmt.Errorf("unexpected outcome %s", res.Outcome)
			}
			return err
		},
	}
}

// caseResult is one run of one case.
type caseResult struct {
	lats      stats
	firstCall int // answered definitively within the wait
	count     map[string]int
	legs      [][4]time.Duration // per held approval: request, to the ERP, ERP + write, back
}

func (r caseResult) outcomes() string {
	var parts []string
	for _, o := range []string{"applied", "denied_guard", "denied_check", "error", "unavailable"} {
		if n := r.count[o]; n > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", o, n))
		}
	}
	return strings.Join(parts, ", ")
}

// measure prepares the tickets, then times their approvals.
func (b *variantBench) measure(ctx context.Context, v variant, m mix, wait time.Duration) (caseResult, error) {
	ids := make([]uuid.UUID, variantTickets)
	customers := make([]string, variantTickets)
	if _, _, err := timeAll(variantTickets, 16, func(i int) error {
		before, after := m.onHold(i)
		id, customer, err := b.ticket(ctx, v, before, after)
		ids[i], customers[i] = id, customer
		return err
	}); err != nil {
		return caseResult{}, err
	}
	// Time the approvals on a quiet relay, not behind the preparation's
	// own events (the relay delivers one Item at a time).
	if err := b.drained(ctx); err != nil {
		return caseResult{}, err
	}

	res := caseResult{count: map[string]int{}}
	outcomes := make([]string, variantTickets)
	starts := make([]time.Time, variantTickets)
	ends := make([]time.Time, variantTickets)
	lats, _, err := timeAll(variantTickets, variantCallers, func(i int) error {
		starts[i] = time.Now()
		o, took, err := v.decide(ctx, b, ids[i], wait)
		ends[i] = time.Now()
		outcomes[i] = o
		if err == nil && o != "error" && o != "unavailable" && took <= wait {
			// Counted under the lock-free index: each i is one goroutine's.
			outcomes[i] += "+"
		}
		return err
	})
	if err != nil {
		return caseResult{}, err
	}
	res.lats = lats
	for i, o := range outcomes {
		if strings.HasSuffix(o, "+") {
			res.firstCall++
			o = strings.TrimSuffix(o, "+")
		}
		res.count[o]++
		if legs, ok := b.legs(ctx, ids[i], customers[i], starts[i], ends[i]); ok {
			res.legs = append(res.legs, legs)
		}
	}
	return res, nil
}

// drained waits until the relay has delivered every outbox row.
func (b *variantBench) drained(ctx context.Context) error {
	return waitFor(ctx, 5*time.Minute, func() (bool, error) {
		var n int
		err := b.pool.QueryRow(ctx, `select count(*) from outbox where delivered_at is null`).Scan(&n)
		return n == 0, err
	})
}

// ticket creates a ticket of the variant's Type for a new customer and
// moves it to in_progress. With a prefetch, it first waits for it.
func (b *variantBench) ticket(ctx context.Context, v variant, onHold, staleAfter bool) (uuid.UUID, string, error) {
	customer := "cust-" + uuid.NewString()[:8]
	b.crm.SetCustomer(customer, connstub.Customer{Plan: "pro"})
	b.erp.SetCustomer(customer, connstub.Customer{OnHold: onHold, CreditLimit: 1000})
	res, err := b.k.Create(ctx, itemservice.CreateRequest{
		IdempotencyKey: key(), Principal: b.ana, TypeID: v.typeID, TypeVersion: v.version,
		Fields: map[string]any{"f_title": "bench", "f_customer_id": customer, "f_amount": 100, "f_assignee": "user-bob"},
	})
	if err != nil {
		return uuid.Nil, "", err
	}
	if v.prefetch {
		if err := waitFor(ctx, time.Minute, func() (bool, error) {
			it, err := b.k.Get(ctx, b.ana, res.ItemID)
			return err == nil && it.Fields["f_plan"] != nil && it.Fields["f_on_hold"] != nil, err
		}); err != nil {
			return uuid.Nil, "", fmt.Errorf("ticket %s: prefetch: %w", res.ItemID, err)
		}
	}
	if staleAfter {
		b.erp.SetCustomer(customer, connstub.Customer{OnHold: true, CreditLimit: 1000})
	}
	for _, to := range []string{"open", "in_progress"} {
		if _, err := b.k.Transition(ctx, itemservice.TransitionRequest{
			IdempotencyKey: key(), Principal: b.ana, ItemID: res.ItemID, ToState: to,
		}); err != nil {
			return uuid.Nil, "", err
		}
	}
	return res.ItemID, customer, nil
}

// legs splits one held approval's time: the call until the request's
// transaction, until the check reached the ERP, until the resolution's
// transaction, and until the call returned. ok is false for an approval
// without a hold (a guard's refusal, or variant B).
func (b *variantBench) legs(ctx context.Context, id uuid.UUID, customer string, start, end time.Time) ([4]time.Duration, bool) {
	var requested, resolved time.Time
	err := b.pool.QueryRow(ctx,
		`select r.created_at, s.created_at
		 from item_events r
		 join item_holds h on h.item_id = r.item_id and h.requested_seq = r.item_seq
		 join item_events s on s.item_id = h.item_id and s.item_seq = h.resolved_seq
		 where r.item_id = $1 and r.op = 'transition_requested'
		 order by r.item_seq desc limit 1`, id).Scan(&requested, &resolved)
	if err != nil {
		return [4]time.Duration{}, false
	}
	var check time.Time
	for _, a := range b.erp.Arrivals(customer) {
		if a.Path == "/credit-checks" && !a.At.Before(requested) {
			check = a.At
			break
		}
	}
	if check.IsZero() {
		return [4]time.Duration{}, false
	}
	return [4]time.Duration{requested.Sub(start), check.Sub(requested), resolved.Sub(check), end.Sub(resolved)}, true
}

func legsRow(name string, legs [][4]time.Duration) string {
	cells := []string{name}
	var total stats
	for leg := range 4 {
		var s stats
		for _, l := range legs {
			s = append(s, l[leg])
		}
		sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
		cells = append(cells, ms(s.pct(0.5))+" / "+ms(s.pct(0.95)))
	}
	for _, l := range legs {
		total = append(total, l[0]+l[1]+l[2]+l[3])
	}
	sort.Slice(total, func(i, j int) bool { return total[i] < total[j] })
	cells = append(cells, ms(total.pct(0.5))+" / "+ms(total.pct(0.95)))
	return "| " + strings.Join(cells, " | ") + " |"
}

// writeLatency measures plain writes and approval requests (wait 0) for
// each variant: at baseline, with the ERP down (it answers 503 at once),
// and with the ERP hanging (it answers after 30 s).
func (b *variantBench) writeLatency(ctx context.Context, variants []variant) error {
	b.crm.SetLatency(50 * time.Millisecond)
	b.erp.SetLatency(50 * time.Millisecond)
	fmt.Println("## Kernel write latency during an ERP outage")
	fmt.Println()
	fmt.Printf("%d writes per cell, %d writers, stubs at 50 ms. Update: one field. Request: approve with wait 0 "+
		"(A and D open a hold; B calls the ERP first). p50 / p95 ms.\n\n", variantWrites, variantWriters)
	fmt.Println("| Variant | Condition | Update | Request |")
	fmt.Println("| --- | --- | --- | --- |")
	conditions := []struct {
		name        string
		before, end func()
	}{
		{"baseline", func() {}, func() {}},
		{"ERP down (503 at once)", func() { b.erp.SetOutage(true) }, func() { b.erp.SetOutage(false) }},
		{"ERP hanging (answers after 30 s)", func() { b.erp.SetLatency(30 * time.Second) },
			func() { b.erp.SetLatency(50 * time.Millisecond) }},
	}
	for _, v := range variants {
		for _, c := range conditions {
			ids := make([]uuid.UUID, variantWrites)
			if _, _, err := timeAll(variantWrites, 16, func(i int) error {
				id, _, err := b.ticket(ctx, v, false, false)
				ids[i] = id
				return err
			}); err != nil {
				return err
			}
			if err := b.drained(ctx); err != nil {
				return err
			}
			c.before()
			upd, _, err := timeAll(variantWrites, variantWriters, func(i int) error {
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
			req, _, err := timeAll(variantWrites, variantWriters, func(i int) error {
				return v.request(ctx, b, ids[i])
			})
			c.end()
			if err != nil {
				return err
			}
			fmt.Printf("| %s | %s | %s / %s | %s / %s |\n", v.name, c.name,
				ms(upd.pct(0.5)), ms(upd.pct(0.95)), ms(req.pct(0.5)), ms(req.pct(0.95)))
		}
	}
	fmt.Println()
	return nil
}
