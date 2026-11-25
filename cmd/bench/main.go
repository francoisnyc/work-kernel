// Command bench measures the spike for Step 5 and prints a Markdown report.
//
// It starts its own Postgres and Temporal dev server in containers (Docker
// needed), so the numbers do not depend on what `make up` left behind. The
// numbers are relative: one machine, containers, no tuning. Run it with
// `make bench`; it takes a few minutes.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"os"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	tlog "go.temporal.io/sdk/log"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
	"github.com/francoisnyc/work-kernel/internal/relay"
	"github.com/francoisnyc/work-kernel/internal/testdb"
	"github.com/francoisnyc/work-kernel/internal/typedef"
	"github.com/francoisnyc/work-kernel/internal/workflow"
)

const temporalImage = "temporalio/temporal:1.10.0" // same as the tests and docker-compose.yml

var alice = item.Principal{Actor: "user:alice"}

func main() {
	// flag is the standard library's command-line parser.
	explain := flag.Bool("explain", false, "print query plans for the unstable reads instead of benchmarking")
	narrowing := flag.Bool("narrowing", false, "measure what narrowing a container costs in scope_access rows")
	holds := flag.Bool("holds", false, "measure Step 6: answers to held approvals (variant D) and write latency")
	invoices := flag.Bool("invoices", false, "measure Step 7: the invoice action under forced timeouts and worker kills")
	variants := flag.Bool("variants", false, "measure Step 7: time to an answer for variants A, D (and B on its branch)")
	explainLinks := flag.Bool("explain-links", false, "print query plans for the link reads and the link and principal filters")
	explainOrder := flag.Bool("explain-order", false, "print query plans for the ordered and cross-Type reads (Jira part 1)")
	explainSets := flag.Bool("explain-sets", false, "print query plans for the contains and empty filters on multi-value fields (Jira part 2)")
	sla := flag.Bool("sla", false, "print the plans of the queue and the breached search, and a goal's row size (Jira part 2, step 3b)")
	rankBench := flag.Bool("rank", false, "measure ranking a backlog by hand: key length, move latency, a rebalance, the write path (Jira part 1)")
	waiting := flag.Bool("waiting", false, "measure the waiting-on-me view, derived at read time, on 400,000 Items (track 1 item 5)")
	flag.Parse()
	run := run
	if *waiting {
		run = func() error { return waitingMode(context.Background()) }
	}
	if *explain {
		run = func() error { return explainMode(context.Background()) }
	}
	if *narrowing {
		run = func() error { return narrowingMode(context.Background()) }
	}
	if *holds {
		run = func() error { return holdsMode(context.Background()) }
	}
	if *invoices {
		run = func() error { return invoicesMode(context.Background()) }
	}
	if *variants {
		run = func() error { return variantsMode(context.Background()) }
	}
	if *explainLinks {
		run = func() error { return linksExplainMode(context.Background()) }
	}
	if *explainOrder {
		run = func() error { return explainOrderMode(context.Background(), false) }
	}
	if *explainSets {
		run = func() error { return explainOrderMode(context.Background(), true) }
	}
	if *sla {
		run = func() error { return slaMode(context.Background()) }
	}
	if *rankBench {
		run = func() error { return rankMode(context.Background()) }
	}
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	ctx := context.Background()
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

	fmt.Println("# Step 5 bench")
	fmt.Println()
	machine(ctx, pool)

	if err := latency(ctx, pool); err != nil {
		return err
	}
	if err := typeRead(ctx, pool); err != nil {
		return err
	}
	if err := syncCommitOff(ctx, pool); err != nil {
		return err
	}
	// The burst runs before manyScopes: every write checks visibility, and
	// that check walks the namespace's narrowed scopes.
	if err := burst(ctx, pool); err != nil {
		return err
	}
	if err := manyScopes(ctx, pool); err != nil {
		return err
	}
	return temporalCost(ctx, pool)
}

// --- helpers ---

func registerFile(ctx context.Context, pool *pgxpool.Pool, name string) error {
	data, err := os.ReadFile("types/" + name)
	if err != nil {
		return err
	}
	return registerYAML(ctx, pool, data)
}

// registerPath registers a Type file given by its path from the repo root.
func registerPath(ctx context.Context, pool *pgxpool.Pool, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return registerYAML(ctx, pool, data)
}

func registerYAML(ctx context.Context, pool *pgxpool.Pool, data []byte) error {
	ty, err := typedef.Parse(data)
	if err != nil {
		return err
	}
	return itemservice.New(pool).RegisterType(ctx, ty)
}

func key() string { return "bench-" + uuid.NewString() }

// stats holds a sorted sample of durations.
type stats []time.Duration

func (s stats) pct(p float64) time.Duration {
	if len(s) == 0 {
		return 0
	}
	return s[int(float64(len(s)-1)*p)]
}

func ms(d time.Duration) string { return fmt.Sprintf("%.2f", float64(d.Microseconds())/1000) }

// timeAll runs op n times on `writers` goroutines and returns the sorted
// latencies and the wall time. op gets the call's index.
func timeAll(n, writers int, op func(i int) error) (stats, time.Duration, error) {
	lat := make([]time.Duration, n)
	errs := make(chan error, writers)
	start := time.Now()
	var wg sync.WaitGroup
	for w := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := w; i < n; i += writers {
				t0 := time.Now()
				if err := op(i); err != nil {
					errs <- err
					return
				}
				lat[i] = time.Since(t0)
			}
		}()
	}
	wg.Wait()
	wall := time.Since(start)
	close(errs)
	if err := <-errs; err != nil {
		return nil, 0, err
	}
	slices.Sort(lat)
	return lat, wall, nil
}

// --- the machine ---

func machine(ctx context.Context, pool *pgxpool.Pool) {
	cpu := "unknown"
	if b, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(line, "model name") {
				cpu = strings.TrimSpace(strings.SplitN(line, ":", 2)[1])
				break
			}
		}
	}
	mem := "unknown"
	if b, err := os.ReadFile("/proc/meminfo"); err == nil {
		mem = strings.Join(strings.Fields(strings.SplitN(string(b), "\n", 2)[0])[1:], " ")
	}
	var pg string
	_ = pool.QueryRow(ctx, `show server_version`).Scan(&pg)
	fmt.Println("## Machine")
	fmt.Println()
	fmt.Printf("- CPU: %s, %d logical CPUs\n", cpu, runtime.NumCPU())
	fmt.Printf("- Memory: %s\n", mem)
	fmt.Printf("- %s, Postgres %s (container), Temporal %s dev server (container)\n", runtime.Version(), pg, temporalImage)
	fmt.Printf("- Date: %s\n\n", time.Now().UTC().Format("2006-01-02"))
}

// --- write latency ---

const (
	latencyN      = 1000
	latencyWarmUp = 100
)

func createTickets(ctx context.Context, k *itemservice.Service, n int) ([]uuid.UUID, error) {
	ids := make([]uuid.UUID, n)
	_, _, err := timeAll(n, 16, func(i int) error {
		res, err := k.Create(ctx, itemservice.CreateRequest{
			IdempotencyKey: key(), Principal: alice, TypeID: "ticket", TypeVersion: 1,
			Fields: map[string]any{"f_title": "printer", "f_assignee": "user-bob"},
		})
		ids[i] = res.ItemID
		return err
	})
	return ids, err
}

func latency(ctx context.Context, pool *pgxpool.Pool) error {
	fmt.Println("## Write latency (ticket v1, ms)")
	fmt.Println()
	fmt.Printf("%d calls per row after %d warm-up calls. Tickets have a timer, so every write also writes an outbox row.\n\n", latencyN, latencyWarmUp)
	fmt.Println("Lock waits: share of 5 ms samples of pg_stat_activity in which some backend of this")
	fmt.Println("database waited on a heavyweight lock (row or transaction), and the most waiting at once.")
	fmt.Println()
	fmt.Println("| Operation | Writers | Type cache | p50 | p95 | writes/s | Lock waits |")
	fmt.Println("| --- | --- | --- | --- | --- | --- | --- |")
	for _, cache := range []bool{false, true} {
		k := itemservice.NewWithOptions(pool, itemservice.Options{CacheTypes: cache})
		if _, err := createTickets(ctx, k, latencyWarmUp); err != nil {
			return err
		}
		for _, writers := range []int{1, 16} {
			label := map[bool]string{false: "off", true: "on"}[cache]

			w := watchLocks(ctx, pool)
			lat, wall, err := timeAll(latencyN, writers, func(i int) error {
				_, err := k.Create(ctx, itemservice.CreateRequest{
					IdempotencyKey: key(), Principal: alice, TypeID: "ticket", TypeVersion: 1,
					Fields: map[string]any{"f_title": "printer", "f_assignee": "user-bob"},
				})
				return err
			})
			if err != nil {
				return err
			}
			report("Create", writers, label, lat, wall, w.stop())

			ids, err := createTickets(ctx, k, latencyN)
			if err != nil {
				return err
			}
			w = watchLocks(ctx, pool)
			lat, wall, err = timeAll(latencyN, writers, func(i int) error {
				_, err := k.Update(ctx, itemservice.UpdateRequest{
					IdempotencyKey: key(), Principal: alice, ItemID: ids[i],
					Fields: map[string]any{"f_priority": float64(i % 5)},
				})
				return err
			})
			if err != nil {
				return err
			}
			report("Update", writers, label, lat, wall, w.stop())

			w = watchLocks(ctx, pool)
			lat, wall, err = timeAll(latencyN, writers, func(i int) error {
				_, err := k.Transition(ctx, itemservice.TransitionRequest{
					IdempotencyKey: key(), Principal: alice, ItemID: ids[i], ToState: "in_progress",
				})
				return err
			})
			if err != nil {
				return err
			}
			report("Transition", writers, label, lat, wall, w.stop())
		}
	}
	fmt.Println()
	return nil
}

func report(op string, writers int, cache string, lat stats, wall time.Duration, locks string) {
	fmt.Printf("| %s | %d | %s | %s | %s | %.0f | %s |\n", op, writers, cache,
		ms(lat.pct(0.5)), ms(lat.pct(0.95)), float64(len(lat))/wall.Seconds(), locks)
}

// lockWatch samples pg_stat_activity while a run is going, to show whether
// writers wait on each other's locks.
type lockWatch struct {
	done   chan struct{}
	result chan string
}

// watchLocks starts sampling every 5 ms on a connection of its own.
func watchLocks(ctx context.Context, pool *pgxpool.Pool) *lockWatch {
	w := &lockWatch{done: make(chan struct{}), result: make(chan string, 1)}
	go func() {
		var samples, waiting, most int
		events := map[string]int{}
		tick := time.NewTicker(5 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-w.done:
				top := ""
				for e, n := range events {
					if top == "" || n > events[top] {
						top = e
					}
				}
				if waiting == 0 {
					w.result <- fmt.Sprintf("0 of %d", samples)
				} else {
					w.result <- fmt.Sprintf("%d of %d (max %d, mostly %s)", waiting, samples, most, top)
				}
				return
			case <-tick.C:
			}
			rows, err := pool.Query(ctx,
				`select wait_event from pg_stat_activity
				 where datname = current_database() and pid <> pg_backend_pid() and wait_event_type = 'Lock'`)
			if err != nil {
				continue
			}
			n := 0
			for rows.Next() {
				var e string
				if rows.Scan(&e) == nil {
					events[e]++
					n++
				}
			}
			rows.Close()
			samples++
			if n > 0 {
				waiting++
			}
			most = max(most, n)
		}
	}()
	return w
}

func (w *lockWatch) stop() string {
	close(w.done)
	return <-w.result
}

// syncCommitOff is a diagnostic only: with synchronous_commit off a commit
// does not wait for the WAL flush. If writes then scale much better, the
// ceiling left after Step A is the disk flush, not a lock.
func syncCommitOff(ctx context.Context, pool *pgxpool.Pool) error {
	cfg := pool.Config().Copy()
	cfg.ConnConfig.RuntimeParams["synchronous_commit"] = "off"
	off, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return err
	}
	defer off.Close()
	fmt.Println("## Diagnostic: synchronous_commit = off (ticket Create, ms)")
	fmt.Println()
	fmt.Println("Not a setting to run with; it shows what the disk flush costs.")
	fmt.Println()
	fmt.Println("| synchronous_commit | Writers | p50 | p95 | writes/s | Lock waits |")
	fmt.Println("| --- | --- | --- | --- | --- | --- |")
	for _, c := range []struct {
		label string
		pool  *pgxpool.Pool
	}{{"on", pool}, {"off", off}} {
		k := itemservice.New(c.pool)
		for _, writers := range []int{1, 16} {
			w := watchLocks(ctx, pool)
			lat, wall, err := timeAll(latencyN, writers, func(int) error {
				_, err := k.Create(ctx, itemservice.CreateRequest{
					IdempotencyKey: key(), Principal: alice, TypeID: "ticket", TypeVersion: 1,
					Fields: map[string]any{"f_title": "printer", "f_assignee": "user-bob"},
				})
				return err
			})
			if err != nil {
				return err
			}
			fmt.Printf("| %s | %d | %s | %s | %.0f | %s |\n", c.label, writers,
				ms(lat.pct(0.5)), ms(lat.pct(0.95)), float64(len(lat))/wall.Seconds(), w.stop())
		}
	}
	fmt.Println()
	return nil
}

// burstWord gives every third message the same word, so after the three
// burst scenarios about 10,000 messages share each of three words: the
// common-word search case.
func burstWord(i int) string {
	return []string{"alpha", "beta", "gamma"}[i%3]
}

// --- the cost of reading a Type ---

func typeRead(ctx context.Context, pool *pgxpool.Pool) error {
	fmt.Println("## Reading the Type (ticket v1, one call, ms)")
	fmt.Println()
	fmt.Println("| Type cache | p50 | p95 |")
	fmt.Println("| --- | --- | --- |")
	for _, cache := range []bool{false, true} {
		k := itemservice.NewWithOptions(pool, itemservice.Options{CacheTypes: cache})
		lat, _, err := timeAll(latencyN, 1, func(int) error {
			_, err := k.Type(ctx, "ticket", 1)
			return err
		})
		if err != nil {
			return err
		}
		fmt.Printf("| %s | %s | %s |\n", map[bool]string{false: "off", true: "on"}[cache], ms(lat.pct(0.5)), ms(lat.pct(0.95)))
	}
	fmt.Println()
	return nil
}

// --- Get and Search for a principal with many scopes ---

const heavyScopes = 1000

func manyScopes(ctx context.Context, pool *pgxpool.Pool) error {
	k := itemservice.New(pool)
	heavy := item.Principal{Actor: "user:heavy"}
	light := item.Principal{Actor: "user:light"}

	term := "w" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	var narrowed uuid.UUID
	_, _, err := timeAll(heavyScopes, 16, func(i int) error {
		res, err := k.Create(ctx, itemservice.CreateRequest{
			IdempotencyKey: key(), Principal: heavy, TypeID: "ticket", TypeVersion: 1,
			Fields: map[string]any{"f_title": "private " + term},
			Narrow: &itemservice.Narrow{Readers: []string{heavy.Actor}},
		})
		if i == 0 {
			narrowed = res.ItemID
		}
		return err
	})
	if err != nil {
		return err
	}
	plain, err := k.Create(ctx, itemservice.CreateRequest{
		IdempotencyKey: key(), Principal: light, TypeID: "ticket", TypeVersion: 1,
		Fields: map[string]any{"f_title": "public " + term},
	})
	if err != nil {
		return err
	}

	fmt.Printf("## Reads for a principal with %d narrowed scopes (ms)\n\n", heavyScopes)
	fmt.Println("| Read | Principal | Narrowed scopes it reads | p50 | p95 |")
	fmt.Println("| --- | --- | --- | --- | --- |")
	cases := []struct {
		name   string
		p      item.Principal
		scopes int
		op     func() error
	}{
		{"Get", light, 0, func() error { _, err := k.Get(ctx, light, plain.ItemID); return err }},
		{"Get", heavy, heavyScopes, func() error { _, err := k.Get(ctx, heavy, narrowed); return err }},
		{"Search", light, 0, func() error {
			_, err := k.Search(ctx, itemservice.SearchRequest{Principal: light, Query: term})
			return err
		}},
		{"Search", heavy, heavyScopes, func() error {
			_, err := k.Search(ctx, itemservice.SearchRequest{Principal: heavy, Query: term})
			return err
		}},
		// About 10,000 messages share "alpha" (see burstWord).
		{"Search, common word", light, 0, func() error {
			_, err := k.Search(ctx, itemservice.SearchRequest{Principal: light, Query: "alpha"})
			return err
		}},
		{"Search, common word, flattened (before Step A)", light, 0, func() error {
			return flatSearch(ctx, pool, light, "alpha")
		}},
		{"Search, rare word, flattened (before Step A)", light, 0, func() error {
			return flatSearch(ctx, pool, light, term)
		}},
	}
	for _, c := range cases {
		lat, _, err := timeAll(500, 1, func(int) error { return c.op() })
		if err != nil {
			return err
		}
		fmt.Printf("| %s | %s | %d | %s | %s |\n", c.name, c.p.Actor, c.scopes, ms(lat.pct(0.5)), ms(lat.pct(0.95)))
	}
	fmt.Println()

	// Writes check visibility too. Compare with the Create rows above,
	// measured before any narrowed scope existed.
	lat, _, err := timeAll(latencyN, 1, func(int) error {
		_, err := k.Create(ctx, itemservice.CreateRequest{
			IdempotencyKey: key(), Principal: light, TypeID: "ticket", TypeVersion: 1,
			Fields: map[string]any{"f_title": "printer", "f_assignee": "user-bob"},
		})
		return err
	})
	if err != nil {
		return err
	}
	fmt.Printf("A ticket Create by %s (1 visible scope) once %d narrowed scopes exist in the namespace: p50 %s ms, p95 %s ms.\n\n",
		light.Actor, heavyScopes, ms(lat.pct(0.5)), ms(lat.pct(0.95)))
	return nil
}

// flatSearch runs the search without the MATERIALIZED step, the query shape
// before Step A, for comparison only.
func flatSearch(ctx context.Context, pool *pgxpool.Pool, p item.Principal, q string) error {
	rows, err := pool.Query(ctx, explainSearchFlat,
		pgx.NamedArgs{"actor": p.Actor, "behalf": "", "ns": "default", "q": q, "limit": 50})
	if err != nil {
		return err
	}
	rows.Close()
	return rows.Err()
}

// --- the burst ---

const burstMessages = 10000

var burstTables = []string{
	"items", "item_events", "item_event_values", "outbox", "idempotency",
	"scopes", "scope_readers", "grants", "principal_roles", "types",
}

type snapshot struct {
	rows  map[string]int64
	bytes int64
}

func snap(ctx context.Context, pool *pgxpool.Pool) (snapshot, error) {
	s := snapshot{rows: map[string]int64{}}
	for _, t := range burstTables {
		var n, b int64
		err := pool.QueryRow(ctx, `select count(*), pg_total_relation_size('`+t+`') from `+t).Scan(&n, &b)
		if err != nil {
			return s, err
		}
		s.rows[t] = n
		s.bytes += b
	}
	return s, nil
}

func burst(ctx context.Context, pool *pgxpool.Pool) error {
	k := itemservice.New(pool)
	fmt.Printf("## Burst: %d chat messages\n\n", burstMessages)
	fmt.Println("`message` and `channel` have no lifecycle, so no listeners. Bytes are the growth of all tables with their indexes.")
	fmt.Println()
	fmt.Println("| Scenario | Wall | msgs/s | p50 ms | p95 ms | Rows/msg | Outbox rows | Bytes/msg | Lock waits |")
	fmt.Println("| --- | --- | --- | --- | --- | --- | --- | --- | --- |")
	var perTable map[string]int64
	for _, sc := range []struct {
		name              string
		writers, channels int
	}{
		{"1 writer, 1 channel", 1, 1},
		{"16 writers, 1 channel", 16, 1},
		{"16 writers, 16 channels", 16, 16},
	} {
		chans := make([]uuid.UUID, sc.channels)
		for i := range chans {
			res, err := k.Create(ctx, itemservice.CreateRequest{
				IdempotencyKey: key(), Principal: alice, TypeID: "channel", TypeVersion: 1,
				Fields: map[string]any{"f_name": fmt.Sprintf("channel %d", i)},
			})
			if err != nil {
				return err
			}
			chans[i] = res.ItemID
			// The first message gives the channel its scope node; send it
			// before counting so the burst measures the steady state.
			if _, err := k.Create(ctx, itemservice.CreateRequest{
				IdempotencyKey: key(), Principal: alice, TypeID: "message", TypeVersion: 1,
				ContainerID: &chans[i], Fields: map[string]any{"f_body": "first"},
			}); err != nil {
				return err
			}
		}
		before, err := snap(ctx, pool)
		if err != nil {
			return err
		}
		w := watchLocks(ctx, pool)
		lat, wall, err := timeAll(burstMessages, sc.writers, func(i int) error {
			ch := chans[i%sc.channels]
			_, err := k.Create(ctx, itemservice.CreateRequest{
				IdempotencyKey: key(), Principal: alice, TypeID: "message", TypeVersion: 1,
				ContainerID: &ch, Fields: map[string]any{"f_body": fmt.Sprintf("hello %s message %d", burstWord(i), i)},
			})
			return err
		})
		locks := w.stop()
		if err != nil {
			return err
		}
		after, err := snap(ctx, pool)
		if err != nil {
			return err
		}
		var rows int64
		perTable = map[string]int64{}
		for _, t := range burstTables {
			d := after.rows[t] - before.rows[t]
			rows += d
			if d != 0 {
				perTable[t] = d
			}
		}
		fmt.Printf("| %s | %.1fs | %.0f | %s | %s | %.2f | %d | %d | %s |\n", sc.name, wall.Seconds(),
			burstMessages/wall.Seconds(), ms(lat.pct(0.5)), ms(lat.pct(0.95)),
			float64(rows)/burstMessages, after.rows["outbox"]-before.rows["outbox"],
			(after.bytes-before.bytes)/burstMessages, locks)
	}
	fmt.Println()
	names := make([]string, 0, len(perTable))
	for t := range perTable {
		names = append(names, t)
	}
	slices.Sort(names)
	fmt.Print("Rows added by the last scenario: ")
	for i, t := range names {
		if i > 0 {
			fmt.Print(", ")
		}
		fmt.Printf("%s %d", t, perTable[t])
	}
	fmt.Println(".")
	fmt.Println()
	return nil
}

// --- Temporal: actions and history bytes per ticket ---

// benchTicket is ticket v1 with a 2-second timer. History does not depend
// on the duration.
func benchTicket() ([]byte, error) {
	data, err := os.ReadFile("types/ticket.v1.yaml")
	if err != nil {
		return nil, err
	}
	s := strings.Replace(string(data), "id: ticket", "id: bench_ticket", 1)
	s = strings.Replace(s, "after: 48h", "after: 2s", 1)
	return []byte(s), nil
}

func startTemporal(ctx context.Context) (client.Client, func(), error) {
	c, _, stop, err := startTemporalAt(ctx)
	return c, stop, err
}

// startTemporalAt is startTemporal that also returns the server's host:port,
// for a worker run as its own process.
func startTemporalAt(ctx context.Context) (client.Client, string, func(), error) {
	server, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        temporalImage,
			Cmd:          []string{"server", "start-dev", "--ip", "0.0.0.0", "--headless"},
			ExposedPorts: []string{"7233/tcp"},
			WaitingFor:   wait.ForListeningPort("7233/tcp").WithStartupTimeout(2 * time.Minute),
		},
		Started: true,
	})
	if err != nil {
		return nil, "", nil, fmt.Errorf("start temporal: %w", err)
	}
	stop := func() { _ = testcontainers.TerminateContainer(server) }
	hostPort, err := server.PortEndpoint(ctx, "7233/tcp", "")
	if err != nil {
		stop()
		return nil, "", nil, err
	}
	for attempt := 0; ; attempt++ {
		// Warnings and errors only: the SDK's debug lines would drown the
		// report and cost time while measuring.
		quiet := tlog.NewStructuredLogger(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))
		c, err := client.Dial(client.Options{HostPort: hostPort, Logger: quiet})
		if err == nil {
			return c, hostPort, func() { c.Close(); stop() }, nil
		}
		if attempt == 30 {
			stop()
			return nil, "", nil, fmt.Errorf("connect to temporal: %w", err)
		}
		time.Sleep(time.Second)
	}
}

// runCost is what one workflow run cost.
type runCost struct {
	events, bytes int64
	actions       map[string]int
}

func temporalCost(ctx context.Context, pool *pgxpool.Pool) error {
	def, err := benchTicket()
	if err != nil {
		return err
	}
	if err := registerYAML(ctx, pool, def); err != nil {
		return err
	}
	tc, stop, err := startTemporal(ctx)
	if err != nil {
		return err
	}
	defer stop()

	k := itemservice.New(pool)
	queue := "bench-" + uuid.NewString()
	w := workflow.NewWorker(tc, queue, k, workflow.WorkerOptions{})
	if err := w.Start(); err != nil {
		return err
	}
	defer w.Stop()
	// Leave the earlier sections' outbox rows behind: they are not this run's.
	if _, err := pool.Exec(ctx, `update outbox set delivered_at = now() where delivered_at is null`); err != nil {
		return err
	}
	r := relay.New(pool, relay.NewTemporalWorkflows(tc, queue), relay.Options{PollInterval: 50 * time.Millisecond})
	rctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() { _ = r.Run(rctx) }()

	const tickets = 3
	var runs []runCost
	for range tickets {
		id, err := lifecycle(ctx, pool, k)
		if err != nil {
			return err
		}
		rc, err := historyOf(ctx, tc, id)
		if err != nil {
			return err
		}
		runs = append(runs, rc...)
	}

	total := runCost{actions: map[string]int{}}
	for _, rc := range runs {
		total.events += rc.events
		total.bytes += rc.bytes
		for a, n := range rc.actions {
			total.actions[a] += n
		}
	}
	nActions := 0
	for _, n := range total.actions {
		nActions += n
	}
	fmt.Println("## Temporal cost per ticket")
	fmt.Println()
	fmt.Println("Lifecycle: create → set assignee → in_progress → timer fires (2 s) → open → in_progress → resolved.")
	fmt.Printf("Averaged over %d tickets.\n\n", tickets)
	fmt.Println("| Workflow runs | History events | History bytes | Actions (estimated) |")
	fmt.Println("| --- | --- | --- | --- |")
	fmt.Printf("| %.1f | %.1f | %.0f | %.1f |\n\n", float64(len(runs))/tickets,
		float64(total.events)/tickets, float64(total.bytes)/tickets, float64(nActions)/tickets)
	names := make([]string, 0, len(total.actions))
	for a := range total.actions {
		names = append(names, a)
	}
	slices.Sort(names)
	fmt.Print("Actions by kind, per ticket: ")
	for i, a := range names {
		if i > 0 {
			fmt.Print(", ")
		}
		fmt.Printf("%s %.1f", a, float64(total.actions[a])/tickets)
	}
	fmt.Println(".")
	fmt.Println()
	return nil
}

// lifecycle walks one ticket through the measured lifecycle and returns once
// its last workflow run has completed.
func lifecycle(ctx context.Context, pool *pgxpool.Pool, k *itemservice.Service) (uuid.UUID, error) {
	res, err := k.Create(ctx, itemservice.CreateRequest{
		IdempotencyKey: key(), Principal: alice, TypeID: "bench_ticket", TypeVersion: 1,
		Fields: map[string]any{"f_title": "printer"},
	})
	if err != nil {
		return uuid.Nil, err
	}
	id := res.ItemID
	steps := []func() error{
		func() error {
			_, err := k.Update(ctx, itemservice.UpdateRequest{IdempotencyKey: key(), Principal: alice, ItemID: id,
				Fields: map[string]any{"f_assignee": "user-bob"}})
			return err
		},
		func() error { return move(ctx, k, id, "in_progress", nil) },
		func() error { return waitState(ctx, k, id, "open") }, // the timer's move
		func() error { return move(ctx, k, id, "in_progress", nil) },
		func() error { return move(ctx, k, id, "resolved", map[string]any{"f_resolution": "fixed"}) },
	}
	for _, step := range steps {
		if err := step(); err != nil {
			return uuid.Nil, err
		}
	}
	// Wait until the relay has delivered everything, so the histories are final.
	deadline := time.Now().Add(time.Minute)
	for {
		var n int
		if err := pool.QueryRow(ctx, `select count(*) from outbox where item_id = $1 and delivered_at is null`, id).Scan(&n); err != nil {
			return uuid.Nil, err
		}
		if n == 0 {
			return id, nil
		}
		if time.Now().After(deadline) {
			return uuid.Nil, fmt.Errorf("ticket %s: events still undelivered", id)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func move(ctx context.Context, k *itemservice.Service, id uuid.UUID, to string, fields map[string]any) error {
	_, err := k.Transition(ctx, itemservice.TransitionRequest{
		IdempotencyKey: key(), Principal: alice, ItemID: id, ToState: to, Fields: fields})
	return err
}

func waitState(ctx context.Context, k *itemservice.Service, id uuid.UUID, state string) error {
	deadline := time.Now().Add(time.Minute)
	for {
		it, err := k.Get(ctx, alice, id)
		if err != nil {
			return err
		}
		if it.State == state {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("ticket %s: not in %s after a minute", id, state)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// historyOf returns the cost of every closed run of the Item's workflow.
// Visibility lags a little, so it waits until no run is still open.
func historyOf(ctx context.Context, tc client.Client, id uuid.UUID) ([]runCost, error) {
	deadline := time.Now().Add(time.Minute)
	for {
		resp, err := tc.ListWorkflow(ctx, &workflowservice.ListWorkflowExecutionsRequest{
			Namespace: "default", Query: fmt.Sprintf("WorkflowId = '%s'", id),
		})
		if err != nil {
			return nil, err
		}
		open := false
		var runIDs []string
		for _, e := range resp.GetExecutions() {
			if e.GetStatus() == enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING {
				open = true
			}
			runIDs = append(runIDs, e.GetExecution().GetRunId())
		}
		if !open && len(runIDs) > 0 {
			var out []runCost
			for _, run := range runIDs {
				rc, err := costOf(ctx, tc, id.String(), run)
				if err != nil {
					return nil, err
				}
				out = append(out, rc)
			}
			return out, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("workflow %s: runs still open after a minute", id)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// costOf reads one run's history. Actions are counted with Temporal Cloud's
// billing rules (docs.temporal.io/cloud/actions, read 2026-11-14): a
// workflow start, each accepted update, each timer started, each activity
// scheduled plus each retry, each signal. Completion is free. Updates
// rejected by the validator are billed but never reach history; the relay
// would have to count those.
func costOf(ctx context.Context, tc client.Client, workflowID, runID string) (runCost, error) {
	desc, err := tc.DescribeWorkflowExecution(ctx, workflowID, runID)
	if err != nil {
		return runCost{}, err
	}
	rc := runCost{
		events:  desc.GetWorkflowExecutionInfo().GetHistoryLength(),
		bytes:   desc.GetWorkflowExecutionInfo().GetHistorySizeBytes(),
		actions: map[string]int{},
	}
	iter := tc.GetWorkflowHistory(ctx, workflowID, runID, false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
	for iter.HasNext() {
		ev, err := iter.Next()
		if err != nil {
			return runCost{}, err
		}
		switch ev.GetEventType() {
		case enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_STARTED:
			rc.actions["start"]++
		case enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_UPDATE_ACCEPTED:
			rc.actions["update"]++
		case enumspb.EVENT_TYPE_TIMER_STARTED:
			rc.actions["timer"]++
		case enumspb.EVENT_TYPE_ACTIVITY_TASK_STARTED:
			// A retried activity records only its last attempt's start, with
			// the attempt number: one action when scheduled, one per retry.
			rc.actions["activity"] += int(ev.GetActivityTaskStartedEventAttributes().GetAttempt())
		case enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_SIGNALED:
			rc.actions["signal"]++
		}
		// Continue-as-new is billed as the new run's start, which that run's
		// own history counts.
	}
	return rc, nil
}
