package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"

	"github.com/francoisnyc/work-kernel/internal/connstub"
	"github.com/francoisnyc/work-kernel/internal/hooks"
	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
	"github.com/francoisnyc/work-kernel/internal/relay"
	"github.com/francoisnyc/work-kernel/internal/testdb"
	"github.com/francoisnyc/work-kernel/internal/workflow"
)

// Step 7 bench (go run ./cmd/bench -invoices): ticket v4's create_invoice
// under forced failures. Every run approves its tickets at once and waits
// until each has its invoice ID; duplicates are counted on the ERP side.
//
//   - late answers: the ERP commits each ticket's first invoice request and
//     answers after 1 s; a hook attempt may take 200 ms, so every first
//     attempt times out and is retried with the same key.
//   - dropped answers: the ERP commits, then closes the connection. With the
//     default connectors net/http resends the request itself (a POST with an
//     Idempotency-Key is replayable on a reused connection), inside the same
//     activity attempt. With "no client resend" every request has its own
//     connection, so the failure reaches the activity and Temporal retries.
//     "Temporal retries" counts the tickets whose invoice action succeeded on
//     a second or later attempt, read from each workflow's history.
//   - control: late answers with connectors that send no key. It must show
//     duplicates, or the runs above prove nothing.
//   - worker stopped: the ERP answers after 15 s (attempts may take 20 s); once
//     every invoice is committed, the worker is stopped and a new one started.
//     Once with worker.Stop() in this process (graceful), once with a real
//     cmd/worker process killed with SIGKILL.
const (
	invoiceTimeouts = 1000
	invoiceControl  = 100
	invoiceKills    = 50
	invoicePrepares = 16 // goroutines preparing tickets
)

type invoiceBench struct {
	pool      *pgxpool.Pool
	k         *itemservice.Service
	tc        client.Client
	hostPort  string
	dsn       string
	ana       item.Principal
	workerBin string
}

// invoiceRun is one run's setup.
type invoiceRun struct {
	name           string
	tickets        int
	attemptTimeout time.Duration
	withoutKeys    bool
	noResend       bool // connectors.WithoutResend(): net/http never resends
	stub           func(erp *connstub.Server)
	// stop, if set, is a worker stop mode: "graceful" or "sigkill".
	stop string
}

func invoicesMode(ctx context.Context) error {
	pool, stopDB, err := testdb.Start(ctx)
	if err != nil {
		return err
	}
	defer stopDB()
	if err := registerFile(ctx, pool, "ticket.v4.yaml"); err != nil {
		return err
	}
	tc, hostPort, stopTemporal, err := startTemporalAt(ctx)
	if err != nil {
		return err
	}
	defer stopTemporal()

	dir, err := os.MkdirTemp("", "bench-worker-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	bin := filepath.Join(dir, "worker")
	if out, err := exec.Command("go", "build", "-o", bin, "./cmd/worker").CombinedOutput(); err != nil {
		return fmt.Errorf("build cmd/worker: %v: %s", err, out)
	}

	b := &invoiceBench{
		pool: pool, k: itemservice.New(pool), tc: tc, hostPort: hostPort,
		dsn: pool.Config().ConnConfig.ConnString(), ana: item.Principal{Actor: "user:bench-approver"},
		workerBin: bin,
	}
	if _, err := pool.Exec(ctx,
		`insert into principal_roles (namespace_id, principal, role) values ('default', $1, 'approver')`,
		b.ana.Actor); err != nil {
		return err
	}

	fmt.Println("# Step 7 bench: the invoice action under forced failures")
	fmt.Println()
	machine(ctx, pool)
	fmt.Println("ticket v4; relay polling every 50 ms; hook budget 5 min. Each ticket has its own customer.")
	fmt.Println("Duplicates = ERP invoices beyond one per ticket, counted on the ERP side.")
	fmt.Println()
	fmt.Println("| Run | Tickets | Keys | Attempt timeout | Forced failures | Temporal retries | Invoices | Duplicates | hook_failed | All invoiced after |")
	fmt.Println("| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |")

	late := func(d time.Duration) func(*connstub.Server) {
		return func(erp *connstub.Server) { erp.SetFirstAnswerDelay(d) }
	}
	drop := func(erp *connstub.Server) { erp.SetDropFirstAnswer(true) }
	runs := []invoiceRun{
		{name: "late answers", tickets: invoiceTimeouts, attemptTimeout: 200 * time.Millisecond, stub: late(time.Second)},
		{name: "dropped answers, client resend on", tickets: invoiceTimeouts, stub: drop},
		{name: "dropped answers, no client resend", tickets: invoiceTimeouts, noResend: true, stub: drop},
		{name: "control: late answers, no keys", tickets: invoiceControl, attemptTimeout: 200 * time.Millisecond,
			withoutKeys: true, stub: late(time.Second)},
		{name: "worker.Stop() mid-activity", tickets: invoiceKills, attemptTimeout: 20 * time.Second,
			stub: late(15 * time.Second), stop: "graceful"},
		{name: "cmd/worker SIGKILL mid-activity", tickets: invoiceKills, attemptTimeout: 20 * time.Second,
			stub: late(15 * time.Second), stop: "sigkill"},
	}
	var notes []string
	for _, r := range runs {
		note, err := b.run(ctx, r)
		if err != nil {
			return fmt.Errorf("%s: %w", r.name, err)
		}
		if note != "" {
			notes = append(notes, note)
		}
	}
	fmt.Println()
	for _, n := range notes {
		fmt.Println(n)
	}
	return nil
}

// workerHandle stops a worker, however it runs.
type workerHandle struct {
	stop func(mode string) error
}

func (b *invoiceBench) startWorker(queue string, crm, erp *connstub.Server, r invoiceRun, logName string) (*workerHandle, error) {
	budget := 5 * time.Minute
	if r.stop == "sigkill" {
		// A real process: the only way to die without telling anyone.
		cmd := exec.Command(b.workerBin)
		cmd.Env = append(os.Environ(),
			"DATABASE_URL="+b.dsn, "TEMPORAL_ADDRESS="+b.hostPort, "TASK_QUEUE="+queue,
			"CRM_URL="+crm.URL, "ERP_URL="+erp.URL,
			"HOOK_BUDGET="+budget.String(), "HOOK_ATTEMPT_TIMEOUT="+r.attemptTimeout.String())
		logFile, err := os.Create(filepath.Join(filepath.Dir(b.workerBin), logName))
		if err != nil {
			return nil, err
		}
		cmd.Stdout, cmd.Stderr = logFile, logFile
		if err := cmd.Start(); err != nil {
			return nil, err
		}
		return &workerHandle{stop: func(mode string) error {
			sig := syscall.SIGTERM
			if mode == "sigkill" {
				sig = syscall.SIGKILL
			}
			_ = cmd.Process.Signal(sig)
			_ = cmd.Wait()
			return logFile.Close()
		}}, nil
	}
	conn := hooks.NewConnectors(crm.URL, erp.URL)
	if r.withoutKeys {
		conn = conn.WithoutKeys()
	}
	if r.noResend {
		conn = conn.WithoutResend()
	}
	w := workflow.NewWorker(b.tc, queue, b.k, workflow.WorkerOptions{
		Connectors: conn, HookBudget: budget, HookAttemptTimeout: r.attemptTimeout,
	})
	if err := w.Start(); err != nil {
		return nil, err
	}
	var once sync.Once
	return &workerHandle{stop: func(string) error { once.Do(w.Stop); return nil }}, nil
}

// run runs one setup and prints its row.
func (b *invoiceBench) run(ctx context.Context, r invoiceRun) (string, error) {
	if _, err := b.pool.Exec(ctx, `update outbox set delivered_at = now() where delivered_at is null`); err != nil {
		return "", err
	}
	crm, erp := connstub.NewCRM(), connstub.NewERP()
	defer crm.Close()
	defer erp.Close()
	queue := "bench-" + uuid.NewString()
	w, err := b.startWorker(queue, crm, erp, r, "worker-1.log")
	if err != nil {
		return "", err
	}
	defer func() { _ = w.stop("graceful") }()
	rl := relay.New(b.pool, relay.NewTemporalWorkflows(b.tc, queue), relay.Options{PollInterval: 50 * time.Millisecond})
	rctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() { _ = rl.Run(rctx) }()

	// Prepare the tickets with the stub answering normally, then force.
	ids := make([]uuid.UUID, r.tickets)
	customers := make([]string, r.tickets)
	if _, _, err := timeAll(r.tickets, invoicePrepares, func(i int) error {
		customers[i] = "cust-" + uuid.NewString()[:8]
		crm.SetCustomer(customers[i], connstub.Customer{Plan: "pro"})
		erp.SetCustomer(customers[i], connstub.Customer{CreditLimit: 1000})
		id, err := b.ticket(ctx, customers[i])
		ids[i] = id
		return err
	}); err != nil {
		return "", err
	}
	r.stub(erp)

	start := time.Now()
	if _, _, err := timeAll(r.tickets, invoicePrepares, func(i int) error {
		res, err := b.k.Transition(ctx, itemservice.TransitionRequest{
			IdempotencyKey: key(), Principal: b.ana, ItemID: ids[i], ToState: "approved",
		})
		if err == nil && res.Outcome != itemservice.OutcomePending {
			err = fmt.Errorf("approve: outcome %s, want pending", res.Outcome)
		}
		return err
	}); err != nil {
		return "", err
	}

	note := ""
	if r.stop != "" {
		if err := waitFor(ctx, 2*time.Minute, func() (bool, error) { return erp.InvoiceCount() >= r.tickets, nil }); err != nil {
			return "", fmt.Errorf("invoices not all committed: %w", err)
		}
		committed := time.Since(start)
		if err := w.stop(r.stop); err != nil {
			return "", err
		}
		stopped := time.Now()
		if w, err = b.startWorker(queue, crm, erp, r, "worker-2.log"); err != nil {
			return "", err
		}
		if err := b.waitInvoiced(ctx, ids); err != nil {
			return "", err
		}
		note = fmt.Sprintf("- %s: every invoice committed %s s after the approvals; all invoiced %s s after the stop.",
			r.name, secs(committed), secs(time.Since(stopped)))
	} else if err := b.waitInvoiced(ctx, ids); err != nil {
		return "", err
	}
	took := time.Since(start)

	dups := 0
	for _, c := range customers {
		if n := len(erp.Invoices(c)); n > 1 {
			dups += n - 1
		}
	}
	var failed int
	if err := b.pool.QueryRow(ctx,
		`select count(*) from item_events where item_id = any($1) and op = 'hook_failed'`, ids).Scan(&failed); err != nil {
		return "", err
	}
	retried, err := b.temporalRetries(ctx, ids)
	if err != nil {
		return "", err
	}
	keys := "yes"
	if r.withoutKeys {
		keys = "no"
	}
	attempt := "30s (default)"
	if r.attemptTimeout > 0 {
		attempt = r.attemptTimeout.String()
	}
	forced := fmt.Sprintf("%d late", erp.LateAnswers())
	if d := erp.DroppedAnswers(); d > 0 {
		forced = fmt.Sprintf("%d dropped", d)
	}
	fmt.Printf("| %s | %d | %s | %s | %s | %d | %d | %d | %d | %s s |\n",
		r.name, r.tickets, keys, attempt, forced, retried, erp.InvoiceCount(), dups, failed, secs(took))
	return note, nil
}

// temporalRetries counts the tickets whose invoice action (RunAction) was
// started more than once by Temporal: the history keeps the last attempt's
// started event, with its attempt number.
func (b *invoiceBench) temporalRetries(ctx context.Context, ids []uuid.UUID) (int, error) {
	retried := make([]bool, len(ids))
	_, _, err := timeAll(len(ids), invoicePrepares, func(i int) error {
		iter := b.tc.GetWorkflowHistory(ctx, ids[i].String(), "", false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
		actions := map[int64]bool{} // scheduled event IDs of RunAction
		for iter.HasNext() {
			ev, err := iter.Next()
			if err != nil {
				return err
			}
			if a := ev.GetActivityTaskScheduledEventAttributes(); a != nil && a.GetActivityType().GetName() == workflow.ActivityRunAction {
				actions[ev.GetEventId()] = true
			}
			if a := ev.GetActivityTaskStartedEventAttributes(); a != nil && actions[a.GetScheduledEventId()] && a.GetAttempt() > 1 {
				retried[i] = true
			}
		}
		return nil
	})
	n := 0
	for _, r := range retried {
		if r {
			n++
		}
	}
	return n, err
}

// ticket creates a v4 ticket for the customer and moves it to in_progress.
func (b *invoiceBench) ticket(ctx context.Context, customer string) (uuid.UUID, error) {
	res, err := b.k.Create(ctx, itemservice.CreateRequest{
		IdempotencyKey: key(), Principal: b.ana, TypeID: "ticket", TypeVersion: 4,
		Fields: map[string]any{"f_title": "bench", "f_customer_id": customer, "f_amount": 100, "f_assignee": "user-bob"},
	})
	if err != nil {
		return uuid.Nil, err
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

// waitInvoiced waits until every ticket has its invoice ID.
func (b *invoiceBench) waitInvoiced(ctx context.Context, ids []uuid.UUID) error {
	return waitFor(ctx, 30*time.Minute, func() (bool, error) {
		var n int
		err := b.pool.QueryRow(ctx,
			`select count(*) from items where id = any($1) and fields ? 'f_invoice_id'`, ids).Scan(&n)
		return n == len(ids), err
	})
}

// waitFor polls cond every 100 ms until it holds, or fails after within.
func waitFor(ctx context.Context, within time.Duration, cond func() (bool, error)) error {
	deadline := time.Now().Add(within)
	for {
		ok, err := cond()
		if err != nil || ok {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("not within %v", within)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func secs(d time.Duration) string { return fmt.Sprintf("%.1f", d.Seconds()) }
