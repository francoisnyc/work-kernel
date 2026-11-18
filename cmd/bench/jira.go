package main

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
	"github.com/francoisnyc/work-kernel/internal/rank"
	"github.com/francoisnyc/work-kernel/internal/relay"
	"github.com/francoisnyc/work-kernel/internal/testdb"
	"github.com/francoisnyc/work-kernel/internal/workflow"
)

// Jira proof of concept, part 1: two measurements.
//
//   - explainOrderMode (--explain-order): the plans of the ordered and
//     cross-Type reads, fresh and analyzed, on 30,000 issues.
//   - rankMode (--rank): what ranking a backlog by hand costs: key length
//     over time, move latency, a rebalance, and the write path per drag.

var lea = item.Principal{Actor: "user:lea", Namespace: "default"}

var issueTypes = []string{"story", "bug", "task"}

// registerPLAT registers the PLAT Types the benches use.
func registerPLAT(ctx context.Context, pool *pgxpool.Pool) error {
	for _, f := range []string{"story.v1.yaml", "bug.v1.yaml", "task.v1.yaml", "epic.v1.yaml", "sprint.v1.yaml", "space.v1.yaml"} {
		if err := registerFile(ctx, pool, f); err != nil {
			return fmt.Errorf("%s: %w", f, err)
		}
	}
	return nil
}

func createItem(ctx context.Context, k *itemservice.Service, typ string, container *uuid.UUID, fields map[string]any) (uuid.UUID, error) {
	res, err := k.Create(ctx, itemservice.CreateRequest{
		IdempotencyKey: key(), Principal: lea, TypeID: typ, TypeVersion: 1, ContainerID: container, Fields: fields,
	})
	return res.ItemID, err
}

// --- EXPLAIN ---

// capture is a pgx tracer that records the statements a pool runs while it
// is on, with their arguments, so the plans below are those of the item
// service's own SQL, not of a copy that can drift.
type capture struct {
	mu  sync.Mutex
	on  bool
	got []captured
}

type captured struct {
	sql  string
	args []any
}

func (c *capture) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.on {
		c.got = append(c.got, captured{data.SQL, data.Args})
	}
	return ctx
}

func (c *capture) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// during runs f and returns the statements it sent.
func (c *capture) during(f func() error) ([]captured, error) {
	c.mu.Lock()
	c.on, c.got = true, nil
	c.mu.Unlock()
	err := f()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.on = false
	return c.got, err
}

// The statements worth a plan: a page of Items, a rollup's count, a
// guard's link set.
func isPage(sql string) bool {
	return strings.Contains(sql, "join scopes s on s.id = m.scope_id") && strings.Contains(sql, "limit @limit")
}
func isRollup(sql string) bool {
	return strings.Contains(sql, "count(*)") && strings.Contains(sql, "from links l")
}
func isGuardSet(sql string) bool {
	return strings.Contains(sql, "select source_id from links where target_id")
}

var (
	execTime  = regexp.MustCompile(`Execution Time: ([0-9.]+) ms`)
	sortLine  = regexp.MustCompile(`Sort Method: (\S+ ?\S*)\s+Memory: (\d+)kB`)
	rowsFirst = regexp.MustCompile(`\(actual time=[0-9.]+\.\.[0-9.]+ rows=(\d+)`)
)

func explainOrderMode(ctx context.Context) error {
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
	if err := registerPLAT(ctx, pool); err != nil {
		return err
	}
	if err := registerFile(ctx, pool, "ticket.v5.yaml"); err != nil {
		return err
	}
	k := itemservice.New(pool)
	me := item.Principal{Actor: "user:me", Namespace: "default"}
	other := item.Principal{Actor: "user:other", Namespace: "default"}

	const issues, onBoard, inEpic, tickets = 30000, 1000, 1000, 5000
	space, err := createItem(ctx, k, "space", nil, map[string]any{"f_key": "PLAT", "f_name": "Platform"})
	if err != nil {
		return err
	}
	sprint, err := createItem(ctx, k, "sprint", &space, map[string]any{"f_name": "Sprint 12", "f_end": "2026-12-04"})
	if err != nil {
		return err
	}
	for i := range 60 { // other sprints, two weeks apart
		end := time.Date(2025, 1, 3, 0, 0, 0, 0, time.UTC).AddDate(0, 0, 14*i).Format(time.DateOnly)
		if _, err := createItem(ctx, k, "sprint", &space, map[string]any{"f_name": fmt.Sprintf("Sprint %d", i), "f_end": end}); err != nil {
			return err
		}
	}
	epic, err := createItem(ctx, k, "epic", &space, map[string]any{"f_summary": "Single sign-on"})
	if err != nil {
		return err
	}
	keys, err := rank.Evenly("", "", issues)
	if err != nil {
		return err
	}
	rand.Shuffle(len(keys), func(i, j int) { keys[i], keys[j] = keys[j], keys[i] })
	ids := make([]uuid.UUID, issues)
	typeOf := func(i int) string { return issueTypes[i%3] }
	if _, _, err := timeAll(issues, 16, func(i int) error {
		fields := map[string]any{
			"f_summary": fmt.Sprintf("issue %d alpha", i), "f_rank": keys[i],
			"f_priority": fmt.Sprintf("P%d", 1+i%4), "f_points": i % 13,
		}
		switch {
		case i%100 == 0:
			fields["f_assignee"] = me.Actor
		case i%10 != 9:
			fields["f_assignee"] = fmt.Sprintf("user:u%d", i%97)
		}
		req := itemservice.CreateRequest{
			IdempotencyKey: key(), Principal: lea, TypeID: typeOf(i), TypeVersion: 1, ContainerID: &space, Fields: fields,
		}
		if i%50 == 7 { // 2% are someone else's, hidden from the reader
			req.Principal, req.Narrow = other, &itemservice.Narrow{Readers: []string{other.Actor}}
		}
		res, err := k.Create(ctx, req)
		if err != nil {
			return err
		}
		ids[i] = res.ItemID
		if i%5 == 0 { // a fifth are in progress
			_, err = k.Transition(ctx, itemservice.TransitionRequest{
				IdempotencyKey: key(), Principal: req.Principal, ItemID: res.ItemID, ToState: "in_progress"})
		}
		return err
	}); err != nil {
		return fmt.Errorf("seed issues: %w", err)
	}
	if _, _, err := timeAll(tickets, 16, func(i int) error {
		_, err := k.Create(ctx, itemservice.CreateRequest{
			IdempotencyKey: key(), Principal: alice, TypeID: "ticket", TypeVersion: 5,
			Fields: map[string]any{"f_title": fmt.Sprintf("ticket %d alpha", i), "f_priority": i % 5},
		})
		return err
	}); err != nil {
		return fmt.Errorf("seed tickets: %w", err)
	}
	// Links in bulk: the plans only depend on the rows.
	var rows [][]any
	for i := range onBoard {
		n := i * (issues / onBoard)
		rows = append(rows, []any{"default", ids[n], typeOf(n), "sprint", sprint, true, false})
	}
	for i := range inEpic {
		n := 7 + i*(issues/inEpic)
		rows = append(rows, []any{"default", ids[n], typeOf(n), "epic", epic, true, false})
	}
	if _, err := plain.CopyFrom(ctx, pgx.Identifier{"links"},
		[]string{"namespace_id", "source_id", "source_type_id", "link_type", "target_id", "src_single", "dst_single"},
		pgx.CopyFromRows(rows)); err != nil {
		return fmt.Errorf("seed links: %w", err)
	}

	fmt.Println("# EXPLAIN (ANALYZE, BUFFERS): ordered and cross-Type reads (Jira part 1)")
	fmt.Println()
	fmt.Printf("%d issues (story, bug, task) in one space, 2%% hidden from the reader; %d linked to one sprint, %d to one epic; %d tickets beside them.\n\n",
		issues, onBoard, inEpic, tickets)
	machine(ctx, plain)

	midKey := keys[issues/2]
	desc := func(f string) *itemservice.OrderBy { return &itemservice.OrderBy{Field: f, Desc: true} }
	asc := func(f string) *itemservice.OrderBy { return &itemservice.OrderBy{Field: f} }
	search := func(req itemservice.SearchRequest, pages int) func() error {
		return func() error {
			req.Principal = me
			for range pages {
				res, err := k.Search(ctx, req)
				if err != nil {
					return err
				}
				req.PageToken = res.NextPageToken
			}
			return nil
		}
	}
	list := func(req itemservice.ListRequest, pages int) func() error {
		return func() error {
			req.Principal, req.ContainerID = me, &space
			for range pages {
				res, err := k.List(ctx, req)
				if err != nil {
					return err
				}
				req.PageToken = res.NextPageToken
			}
			return nil
		}
	}
	onSprint := itemservice.Filter{Links: []itemservice.LinkFilter{{LinkType: "sprint", Direction: item.LinkOutgoing, ItemID: sprint}}}
	open := []string{"todo", "active"}
	steps := []struct {
		name string
		run  func() error
		pick func(string) bool
		last bool // explain the last matching statement (a later page) instead of the first
	}{
		{"The board: linked to the sprint (1,000), by rank, page 1", search(itemservice.SearchRequest{Filter: onSprint, OrderBy: asc("f_rank"), Limit: 50}, 1), isPage, false},
		{"The board, page 2 (keyset on the rank)", search(itemservice.SearchRequest{Filter: onSprint, OrderBy: asc("f_rank"), Limit: 50}, 2), isPage, true},
		{"The backlog: the space's 30,000 issues, three Types, by rank, page 1",
			list(itemservice.ListRequest{Filter: itemservice.Filter{TypeIDs: issueTypes}, OrderBy: asc("f_rank"), Limit: 50}, 1), isPage, false},
		{"The backlog, page 2", list(itemservice.ListRequest{Filter: itemservice.Filter{TypeIDs: issueTypes}, OrderBy: asc("f_rank"), Limit: 50}, 2), isPage, true},
		{"A move's neighbour read: the highest rank below a key, 2 rows (30,000 children)",
			list(itemservice.ListRequest{Filter: itemservice.Filter{Fields: []itemservice.FieldFilter{{Field: "f_rank", Lt: midKey}}},
				OrderBy: desc("f_rank"), Limit: 2}, 1), isPage, false},
		{"A move's tied-run read: rank = a key, 10 rows",
			list(itemservice.ListRequest{Filter: itemservice.Filter{Fields: []itemservice.FieldFilter{{Field: "f_rank", Eq: midKey}}},
				OrderBy: asc("f_rank"), Limit: 10}, 1), isPage, false},
		{"My open work: assignee = me (1%), todo and active, three Types, by priority",
			search(itemservice.SearchRequest{Filter: itemservice.Filter{TypeIDs: issueTypes, Categories: open,
				Fields: []itemservice.FieldFilter{{Field: "f_assignee", Eq: "me"}}}, OrderBy: asc("f_priority"), Limit: 50}, 1), isPage, false},
		{"Open P1 issues (7,500), three Types, by points descending, 20 per page, page 1",
			search(itemservice.SearchRequest{Filter: itemservice.Filter{TypeIDs: issueTypes, Categories: open,
				Fields: []itemservice.FieldFilter{{Field: "f_priority", Eq: "P1"}}}, OrderBy: desc("f_points"), Limit: 20}, 1), isPage, false},
		{"Open P1 issues, page 3", search(itemservice.SearchRequest{Filter: itemservice.Filter{TypeIDs: issueTypes, Categories: open,
			Fields: []itemservice.FieldFilter{{Field: "f_priority", Eq: "P1"}}}, OrderBy: desc("f_points"), Limit: 20}, 3), isPage, true},
		{"No Type named: everything in todo, by rank (the field is resolved across every Type)",
			search(itemservice.SearchRequest{Filter: itemservice.Filter{Categories: []string{"todo"}}, OrderBy: asc("f_rank"), Limit: 50}, 1), isPage, false},
		{"Words and an order: \"alpha\" (35,000 matches) among the three Types, by rank",
			search(itemservice.SearchRequest{Query: "alpha", Filter: itemservice.Filter{TypeIDs: issueTypes}, OrderBy: asc("f_rank"), Limit: 50}, 1), isPage, false},
		{"Sprints ending in a date range, latest first",
			search(itemservice.SearchRequest{Filter: itemservice.Filter{TypeIDs: []string{"sprint"},
				Fields: []itemservice.FieldFilter{{Field: "f_end", Gte: "2026-01-01", Lt: "2027-01-01"}}}, OrderBy: desc("f_end"), Limit: 50}, 1), isPage, false},
		{"The epic's rollup: its 1,000 issues counted by state, as the reader sees them",
			func() error { _, err := k.Get(ctx, me, epic); return err }, isRollup, false},
		{"The epic's guard: the link set of its 1,000 issues",
			func() error {
				// in_progress first (no guard), then done: refused, but read.
				_, _ = k.Transition(ctx, itemservice.TransitionRequest{IdempotencyKey: key(), Principal: lea, ItemID: epic, ToState: "in_progress"})
				_, err := k.Transition(ctx, itemservice.TransitionRequest{IdempotencyKey: key(), Principal: lea, ItemID: epic, ToState: "done"})
				if errors.Is(err, itemservice.ErrRejected) {
					return nil
				}
				return fmt.Errorf("closing the epic: %v, want the guard's refusal", err)
			}, isGuardSet, false},
	}

	type result struct{ ms [2]string }
	summary := make([]result, len(steps))
	notes := make([]string, len(steps))
	for pass, title := range []string{"Before ANALYZE", "After ANALYZE"} {
		if pass == 1 {
			if _, err := plain.Exec(ctx, `analyze`); err != nil {
				return err
			}
		}
		fmt.Printf("## %s\n\n", title)
		if err := tableStats(ctx, plain); err != nil {
			return err
		}
		for i, s := range steps {
			if err := s.run(); err != nil { // warm the cache, as the bench does
				return fmt.Errorf("%s: %w", s.name, err)
			}
			got, err := trace.during(s.run)
			if err != nil {
				return fmt.Errorf("%s: %w", s.name, err)
			}
			got = slices.DeleteFunc(got, func(c captured) bool { return !s.pick(c.sql) })
			if len(got) == 0 {
				return fmt.Errorf("%s: the statement to explain was not captured", s.name)
			}
			stmt := got[0]
			if s.last {
				stmt = got[len(got)-1]
			}
			rows, err := plain.Query(ctx, "explain (analyze, buffers) "+stmt.sql, stmt.args...)
			if err != nil {
				return fmt.Errorf("%s: %w", s.name, err)
			}
			lines, err := pgx.CollectRows(rows, pgx.RowTo[string])
			if err != nil {
				return fmt.Errorf("%s: %w", s.name, err)
			}
			plan := strings.Join(lines, "\n")
			fmt.Printf("### %s\n\n```\n%s\n```\n\n", s.name, plan)
			if m := execTime.FindStringSubmatch(plan); m != nil {
				summary[i].ms[pass] = m[1]
			}
			note := ""
			if m := sortLine.FindStringSubmatch(plan); m != nil {
				note = "sort: " + m[1] + ", " + m[2] + " kB"
			} else {
				note = "no sort"
			}
			if m := rowsFirst.FindAllStringSubmatch(plan, -1); len(m) > 0 {
				most := 0
				for _, x := range m {
					var n int
					fmt.Sscan(x[1], &n)
					most = max(most, n)
				}
				note += fmt.Sprintf("; widest node %d rows", most)
			}
			notes[i] = note
		}
	}
	fmt.Println("## Summary")
	fmt.Println()
	fmt.Println("| Read | Fresh (ms) | Analyzed (ms) | Analyzed plan |")
	fmt.Println("| --- | --- | --- | --- |")
	for i, s := range steps {
		fmt.Printf("| %s | %s | %s | %s |\n", s.name, summary[i].ms[0], summary[i].ms[1], notes[i])
	}
	fmt.Println()
	return nil
}

// --- the rank bench ---

// benchBoard reads one container's ranked cards in process, for rank.Plan:
// the same reads pli makes through the API.
type benchBoard struct {
	k     *itemservice.Service
	space uuid.UUID
	reads *atomic.Int64
}

func (b benchBoard) Scan(ctx context.Context, q rank.Query) ([]rank.Card, error) {
	req := itemservice.ListRequest{
		Principal: lea, ContainerID: &b.space, Limit: q.Limit, OrderBy: &itemservice.OrderBy{Field: "f_rank", Desc: q.Desc},
	}
	switch {
	case q.Eq != "":
		req.Filter.Fields = []itemservice.FieldFilter{{Field: "f_rank", Eq: q.Eq}}
	case q.Gt != "":
		req.Filter.Fields = []itemservice.FieldFilter{{Field: "f_rank", Gt: q.Gt}}
	case q.Lt != "":
		req.Filter.Fields = []itemservice.FieldFilter{{Field: "f_rank", Lt: q.Lt}}
	}
	b.reads.Add(1)
	res, err := b.k.List(ctx, req)
	if err != nil {
		return nil, err
	}
	var cards []rank.Card
	for _, it := range res.Items {
		if key, _ := it.Fields["f_rank"].(string); key != "" {
			cards = append(cards, rank.Card{ID: it.ID.String(), Key: key})
		}
	}
	return cards, nil
}

// drag is one move's outcome.
type drag struct {
	took    time.Duration
	writes  int    // 1, or more when a tied run was re-keyed
	key     string // the moved card's new key
	retries int    // plans redone because the neighbours changed under them
	refused bool   // a tied run past the cap
}

// moveCard plans and writes one move, as pli does.
func moveCard(ctx context.Context, b benchBoard, moved uuid.UUID, pos rank.Position, other uuid.UUID) (drag, error) {
	var d drag
	start := time.Now()
	for {
		var card rank.Card
		if pos == rank.Before || pos == rank.After {
			it, err := b.k.Get(ctx, lea, other)
			if err != nil {
				return d, err
			}
			key, _ := it.Fields["f_rank"].(string)
			card = rank.Card{ID: other.String(), Key: key}
		}
		writes, err := rank.Plan(ctx, b, moved.String(), pos, card)
		switch {
		case errors.Is(err, rank.ErrTiedRunTooLong):
			d.refused, d.took = true, time.Since(start)
			return d, nil
		case errors.Is(err, rank.ErrChanged) && d.retries < 5:
			d.retries++ // someone moved a card between two reads: look again
			continue
		case err != nil:
			return d, err
		}
		for _, w := range writes {
			if _, err := b.k.Update(ctx, itemservice.UpdateRequest{
				IdempotencyKey: key(), Principal: lea, ItemID: uuid.MustParse(w.ID), Fields: map[string]any{"f_rank": w.Key},
			}); err != nil {
				return d, err
			}
			if w.ID == moved.String() {
				d.key = w.Key
			}
		}
		d.writes, d.took = len(writes), time.Since(start)
		return d, nil
	}
}

// keyLengths reads the longest and the mean key of a container.
func keyLengths(ctx context.Context, pool *pgxpool.Pool, space uuid.UUID) (longest int, mean float64, tied int, err error) {
	err = pool.QueryRow(ctx,
		`select coalesce(max(length(fields->>'f_rank')), 0), coalesce(avg(length(fields->>'f_rank')), 0),
		        count(fields->>'f_rank') - count(distinct fields->>'f_rank')
		 from items where container_id = $1`, space).Scan(&longest, &mean, &tied)
	return
}

// orderOf reads a container's cards in rank order.
func orderOf(ctx context.Context, pool *pgxpool.Pool, space uuid.UUID) ([]uuid.UUID, error) {
	rows, err := pool.Query(ctx,
		`select id from items where container_id = $1 and fields ? 'f_rank'
		 order by (fields->>'f_rank') collate "C", id`, space)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
}

// rebalanceSpace rewrites a container's keys as pli's rank rebalance does:
// every ranked card, in order, an even key, under the card's item_seq.
func rebalanceSpace(ctx context.Context, k *itemservice.Service, space uuid.UUID) (rewritten, skipped, pages int, took time.Duration, err error) {
	start := time.Now()
	var cards []item.Item
	token := ""
	for {
		res, lerr := k.List(ctx, itemservice.ListRequest{
			Principal: lea, ContainerID: &space, Limit: 500, PageToken: token, OrderBy: &itemservice.OrderBy{Field: "f_rank"},
		})
		if lerr != nil {
			return 0, 0, 0, 0, lerr
		}
		pages++
		for _, it := range res.Items {
			if key, _ := it.Fields["f_rank"].(string); key != "" {
				cards = append(cards, it)
			}
		}
		if token = res.NextPageToken; token == "" {
			break
		}
	}
	keys, err := rank.Evenly("", "", len(cards))
	if err != nil {
		return 0, 0, 0, 0, err
	}
	for i, it := range cards {
		if it.Fields["f_rank"] == keys[i] {
			continue
		}
		_, uerr := k.Update(ctx, itemservice.UpdateRequest{
			IdempotencyKey: key(), Principal: lea, ItemID: it.ID, Fields: map[string]any{"f_rank": keys[i]},
			ExpectedItemSeq: it.ItemSeq, // a card moved meanwhile is skipped: its new key stands
		})
		switch {
		case uerr == nil:
			rewritten++
		case errors.Is(uerr, itemservice.ErrStale):
			skipped++
		default:
			return 0, 0, 0, 0, uerr
		}
	}
	return rewritten, skipped, pages, time.Since(start), nil
}

// newBacklog makes a space with n cards of a Type, ranked evenly: the
// state right after a rebalance.
func newBacklog(ctx context.Context, k *itemservice.Service, typ string, n int) (uuid.UUID, []uuid.UUID, error) {
	space, err := createItem(ctx, k, "space", nil, map[string]any{"f_key": "PLAT", "f_name": "Platform"})
	if err != nil {
		return uuid.Nil, nil, err
	}
	keys, err := rank.Evenly("", "", n)
	if err != nil {
		return uuid.Nil, nil, err
	}
	ids := make([]uuid.UUID, n)
	_, _, err = timeAll(n, 8, func(i int) error {
		id, err := createItem(ctx, k, typ, &space, map[string]any{"f_summary": fmt.Sprintf("card %d", i), "f_rank": keys[i]})
		ids[i] = id
		return err
	})
	return space, ids, err
}

// dragStats sums up a batch of drags.
type dragStats struct {
	lat                                   stats
	writes, rekeys, largestRun, crossings int
	retries, refused                      int
}

func (s *dragStats) add(d drag) {
	s.lat = append(s.lat, d.took)
	s.writes += d.writes
	s.retries += d.retries
	if d.refused {
		s.refused++
	}
	if d.writes > 1 {
		s.rekeys++
		s.largestRun = max(s.largestRun, d.writes-1)
	}
	if rank.NeedsRebalance(d.key) {
		s.crossings++
	}
}

func rankMode(ctx context.Context) error {
	pool, stopDB, err := testdb.Start(ctx)
	if err != nil {
		return err
	}
	defer stopDB()
	if err := registerPLAT(ctx, pool); err != nil {
		return err
	}
	hooked, err := hookedStory()
	if err != nil {
		return err
	}
	if err := registerYAML(ctx, pool, hooked); err != nil {
		return fmt.Errorf("register story_hooked: %w", err)
	}
	k := itemservice.New(pool)

	fmt.Println("# Rank bench (Jira part 1)")
	fmt.Println()
	fmt.Printf("A backlog of 1,000 cards (stories in one space), ranked by f_rank with the shared package (threshold %d bytes, tied-run cap %d). Moves run in process through the item service: the reads and the update pli makes, without HTTP.\n\n",
		rank.Threshold, rank.MaxTiedRun)
	machine(ctx, pool)

	const cards = 1000
	// --- concurrent drags ---
	space, ids, err := newBacklog(ctx, k, "story", cards)
	if err != nil {
		return err
	}
	var reads atomic.Int64
	board := benchBoard{k: k, space: space, reads: &reads}
	longest, mean, _, err := keyLengths(ctx, pool, space)
	if err != nil {
		return err
	}
	fmt.Println("## Concurrent drags: 8 users, 4,000 random moves (before or after a random card)")
	fmt.Println()
	fmt.Println("| After moves | Longest key (bytes) | Mean key (bytes) | Cards sharing a key |")
	fmt.Println("| --- | --- | --- | --- |")
	fmt.Printf("| 0 (even keys) | %d | %.1f | 0 |\n", longest, mean)
	const users, perRound, rounds = 8, 500, 8
	var all dragStats
	wall := time.Duration(0)
	for round := 1; round <= rounds; round++ {
		var mu sync.Mutex
		lat, took, err := timeAll(perRound, users, func(int) error {
			x, y := ids[rand.IntN(cards)], ids[rand.IntN(cards)]
			if x == y {
				return nil
			}
			pos := rank.Before
			if rand.IntN(2) == 0 {
				pos = rank.After
			}
			d, err := moveCard(ctx, board, x, pos, y)
			mu.Lock()
			all.add(d)
			mu.Unlock()
			return err
		})
		if err != nil {
			return err
		}
		_ = lat
		wall += took
		longest, mean, tied, err := keyLengths(ctx, pool, space)
		if err != nil {
			return err
		}
		fmt.Printf("| %d | %d | %.1f | %d |\n", round*perRound, longest, mean, tied)
	}
	slices.Sort(all.lat)
	moves := len(all.lat)
	fmt.Println()
	fmt.Println("| Moves | p50 (ms) | p95 (ms) | p99 (ms) | Moves/s | Reads per move | Writes per move | Re-keys | Largest tied run | Past the cap | Replans | Keys past the threshold |")
	fmt.Println("| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |")
	fmt.Printf("| %d | %s | %s | %s | %.0f | %.2f | %.3f | %d | %d | %d | %d | %d |\n\n", moves,
		ms(all.lat.pct(0.50)), ms(all.lat.pct(0.95)), ms(all.lat.pct(0.99)), float64(moves)/wall.Seconds(),
		float64(reads.Load())/float64(moves), float64(all.writes)/float64(moves),
		all.rekeys, all.largestRun, all.refused, all.retries, all.crossings)

	// --- always at the top ---
	space, ids, err = newBacklog(ctx, k, "story", cards)
	if err != nil {
		return err
	}
	board = benchBoard{k: k, space: space, reads: &reads}
	fmt.Println("## Adversarial: one user always inserts at the top, 1,000 times, no rebalance")
	fmt.Println()
	fmt.Println("| After inserts | The new key (bytes) | Longest key | Mean key |")
	fmt.Println("| --- | --- | --- | --- |")
	var top dragStats
	firstCross := 0
	for i := 1; i <= 1000; i++ {
		d, err := moveCard(ctx, board, ids[rand.IntN(cards)], rank.Top, uuid.Nil)
		if err != nil {
			return err
		}
		top.add(d)
		if firstCross == 0 && rank.NeedsRebalance(d.key) {
			firstCross = i
		}
		if i%100 == 0 || i == 10 || i == 50 {
			longest, mean, _, err := keyLengths(ctx, pool, space)
			if err != nil {
				return err
			}
			fmt.Printf("| %d | %d | %d | %.1f |\n", i, len(d.key), longest, mean)
		}
	}
	slices.Sort(top.lat)
	fmt.Println()
	fmt.Printf("The threshold (%d bytes) was first crossed at insert %d; %d of the 1,000 inserts produced a key past it. Move latency: p50 %s ms, p95 %s ms, p99 %s ms.\n\n",
		rank.Threshold, firstCross, top.crossings, ms(top.lat.pct(0.50)), ms(top.lat.pct(0.95)), ms(top.lat.pct(0.99)))

	// --- the rebalance ---
	before, _, _, err := keyLengths(ctx, pool, space)
	if err != nil {
		return err
	}
	rowsBefore, err := writeRows(ctx, pool)
	if err != nil {
		return err
	}
	rewritten, skipped, pages, took, err := rebalanceSpace(ctx, k, space)
	if err != nil {
		return err
	}
	rowsAfter, err := writeRows(ctx, pool)
	if err != nil {
		return err
	}
	after, afterMean, _, err := keyLengths(ctx, pool, space)
	if err != nil {
		return err
	}
	fmt.Println("## Rebalance of that container (1,000 cards, nobody else writing)")
	fmt.Println()
	fmt.Println("| Cards rewritten | Skipped | Reads (pages of 500) | Time (s) | Per card (ms) | Events | Value rows | Outbox rows | Longest key before | Longest after | Mean after |")
	fmt.Println("| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |")
	fmt.Printf("| %d | %d | %d | %.2f | %s | %d | %d | %d | %d | %d | %.1f |\n\n", rewritten, skipped, pages, took.Seconds(),
		ms(took/time.Duration(max(rewritten, 1))), rowsAfter[0]-rowsBefore[0], rowsAfter[1]-rowsBefore[1], rowsAfter[2]-rowsBefore[2],
		before, after, afterMean)

	// With the deliberate rebalance run whenever a move reports the threshold.
	rebalances, sinceLast, gaps := 0, 0, []int{}
	for i := 1; i <= 1000; i++ {
		d, err := moveCard(ctx, board, ids[rand.IntN(cards)], rank.Top, uuid.Nil)
		if err != nil {
			return err
		}
		sinceLast++
		if rank.NeedsRebalance(d.key) {
			if _, _, _, _, err := rebalanceSpace(ctx, k, space); err != nil {
				return err
			}
			rebalances++
			gaps = append(gaps, sinceLast)
			sinceLast = 0
		}
	}
	fmt.Printf("Another 1,000 inserts at the top, rebalancing each time a move reports the threshold: %d rebalances, one every %v inserts.\n\n", rebalances, gaps)

	// --- a rebalance while people drag ---
	if err := racingRebalance(ctx, pool, k, true); err != nil {
		return err
	}
	if err := racingRebalance(ctx, pool, k, false); err != nil {
		return err
	}

	// --- the write path per drag ---
	return writePath(ctx, pool, k)
}

// writeRows counts the rows a write adds: events, their values, outbox.
func writeRows(ctx context.Context, pool *pgxpool.Pool) ([3]int, error) {
	var n [3]int
	err := pool.QueryRow(ctx, `select (select count(*) from item_events), (select count(*) from item_event_values),
		(select count(*) from outbox)`).Scan(&n[0], &n[1], &n[2])
	return n, err
}

// racingRebalance measures what a rebalance and concurrent drags do to each
// other. Half the cards are anchors, never dragged: a rebalance changes
// their keys, never their order. Four users each drag movable cards next
// to anchors while the rebalance runs. A drag is out of place when, at the
// end, its card is not between the anchor it was put next to and that
// anchor's neighbouring anchor: it computed its key from keys the rebalance
// was rewriting. With withRebalance false it is the control: the same
// drags, no rebalance.
func racingRebalance(ctx context.Context, pool *pgxpool.Pool, k *itemservice.Service, withRebalance bool) error {
	const cards, users, perUser = 1000, 4, 100
	space, ids, err := newBacklog(ctx, k, "story", cards)
	if err != nil {
		return err
	}
	var reads atomic.Int64
	board := benchBoard{k: k, space: space, reads: &reads}
	// Lengthen the keys first, so the rebalance has every card to rewrite.
	for range 300 {
		if _, err := moveCard(ctx, board, ids[1+2*rand.IntN(cards/2)], rank.Top, uuid.Nil); err != nil {
			return err
		}
	}
	// Anchors: the even cards, which the loop above never moved. Their
	// order is the order they were created in.
	type done struct {
		card, anchor uuid.UUID
		pos          rank.Position
	}
	var mu sync.Mutex
	var drags []done
	var st dragStats
	movable := rand.Perm(cards / 2) // each odd card is dragged at most once
	var wg sync.WaitGroup
	var rewritten, skipped int
	var took time.Duration
	var rebErr error
	if withRebalance {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rewritten, skipped, _, took, rebErr = rebalanceSpace(ctx, k, space)
		}()
	}
	errs := make(chan error, users)
	for u := range users {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range perUser {
				card := ids[1+2*movable[u*perUser+i]]
				anchor := ids[2*rand.IntN(cards/2)]
				pos := rank.Before
				if rand.IntN(2) == 0 {
					pos = rank.After
				}
				d, err := moveCard(ctx, board, card, pos, anchor)
				if err != nil {
					errs <- err
					return
				}
				mu.Lock()
				st.add(d)
				drags = append(drags, done{card, anchor, pos})
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		return err
	}
	if rebErr != nil {
		return rebErr
	}
	order, err := orderOf(ctx, pool, space)
	if err != nil {
		return err
	}
	at := map[uuid.UUID]int{}
	for i, id := range order {
		at[id] = i
	}
	// The anchors' order must be untouched, and each drag must sit between
	// its anchor and the next anchor on its side.
	anchorsOut := 0
	for i := 2; i < cards; i += 2 {
		if at[ids[i]] < at[ids[i-2]] {
			anchorsOut++
		}
	}
	index := map[uuid.UUID]int{}
	for i, id := range ids {
		index[id] = i
	}
	outOfPlace := 0
	for _, d := range drags {
		a := index[d.anchor]
		lo, hi := -1, len(order) // positions of the anchors around the slot
		if d.pos == rank.Before {
			hi = at[d.anchor]
			if a >= 2 {
				lo = at[ids[a-2]]
			}
		} else {
			lo = at[d.anchor]
			if a+2 < cards {
				hi = at[ids[a+2]]
			}
		}
		if p := at[d.card]; p <= lo || p >= hi {
			outOfPlace++
		}
	}
	slices.Sort(st.lat)
	if withRebalance {
		fmt.Println("## A rebalance while 4 users drag (400 drags next to cards that are never dragged)")
		fmt.Println()
		fmt.Println("| Run | Drags | Out of place | Anchors out of order | Rebalance: rewritten | skipped (moved meanwhile) | time (s) | Drag p50 (ms) | p95 (ms) | Re-keys | Largest tied run | Past the cap | Replans |")
		fmt.Println("| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |")
	}
	name := "control: no rebalance"
	if withRebalance {
		name = "with a rebalance"
	}
	fmt.Printf("| %s | %d | %d | %d | %d | %d | %.2f | %s | %s | %d | %d | %d | %d |\n", name, len(drags), outOfPlace, anchorsOut,
		rewritten, skipped, took.Seconds(), ms(st.lat.pct(0.50)), ms(st.lat.pct(0.95)), st.rekeys, st.largestRun, st.refused, st.retries)
	if !withRebalance {
		fmt.Println()
	}
	return nil
}

// hookedStory is the story Type with listeners: an enrich hook on entering
// in_review, and a timer on in_progress so that a workflow is running while
// a card is in progress. Every write to such a Type costs an outbox row.
func hookedStory() ([]byte, error) {
	data, err := os.ReadFile("types/story.v1.yaml")
	if err != nil {
		return nil, err
	}
	s := string(data)
	for old, new := range map[string]string{
		"id: story\n": "id: story_hooked\n",
		"  - {id: in_progress, category: active}\n":  "  - {id: in_progress, category: active, timer: {after: 24h, to: todo}}\n",
		"  - {from: todo,        to: in_progress}\n": "  - {from: todo,        to: in_progress}\n  - {from: in_progress, to: todo}\n",
	} {
		if !strings.Contains(s, old) {
			return nil, fmt.Errorf("story.v1.yaml no longer has %q", old)
		}
		s = strings.Replace(s, old, new, 1)
	}
	s += `
hooks:
  - id: get_plan
    on: enter
    state: in_review
    kind: enrich
    action: crm.get_plan
    reads: [f_summary]
    writes: [f_description]
    fallback: {f_description: unknown}
`
	return []byte(s), nil
}

// writePath measures what one drag costs on the write path: on a story
// (nothing listens) and on the same Type with a hook and a timer.
func writePath(ctx context.Context, pool *pgxpool.Pool, k *itemservice.Service) error {
	fmt.Println("## The write path per drag")
	fmt.Println()
	fmt.Println("200 drags (one update of f_rank each) on a 1,000-card backlog, one user.")
	fmt.Println()
	fmt.Println("| Type | Events | Value rows | Outbox rows | Update p50 (ms) | Update p95 (ms) | Whole move p50 (ms) | p95 (ms) | Reads per move |")
	fmt.Println("| --- | --- | --- | --- | --- | --- | --- | --- | --- |")
	hookedSpace, hookedIDs := uuid.Nil, []uuid.UUID(nil)
	for _, typ := range []string{"story", "story_hooked"} {
		space, ids, err := newBacklog(ctx, k, typ, 1000)
		if err != nil {
			return err
		}
		if typ == "story_hooked" {
			hookedSpace, hookedIDs = space, ids
		}
		var reads atomic.Int64
		board := benchBoard{k: k, space: space, reads: &reads}
		const n = 200
		before, err := writeRows(ctx, pool)
		if err != nil {
			return err
		}
		var whole, update stats
		for range n {
			x, y := ids[rand.IntN(len(ids))], ids[rand.IntN(len(ids))]
			if x == y {
				continue
			}
			d, err := moveCard(ctx, board, x, rank.Before, y)
			if err != nil {
				return err
			}
			whole = append(whole, d.took)
		}
		after, err := writeRows(ctx, pool)
		if err != nil {
			return err
		}
		moves := len(whole)
		// The update alone: the same key written again costs the same write.
		for range n {
			id := ids[rand.IntN(len(ids))]
			key2, _ := rank.Between("", "")
			start := time.Now()
			if _, err := k.Update(ctx, itemservice.UpdateRequest{
				IdempotencyKey: key(), Principal: lea, ItemID: id, Fields: map[string]any{"f_rank": key2},
			}); err != nil {
				return err
			}
			update = append(update, time.Since(start))
		}
		slices.Sort(whole)
		slices.Sort(update)
		per := func(i int) string { return fmt.Sprintf("%.2f", float64(after[i]-before[i])/float64(moves)) }
		fmt.Printf("| %s | %s | %s | %s | %s | %s | %s | %s | %.2f |\n", typ, per(0), per(1), per(2),
			ms(update.pct(0.50)), ms(update.pct(0.95)), ms(whole.pct(0.50)), ms(whole.pct(0.95)), float64(reads.Load())/float64(moves))
	}
	fmt.Println()

	// What the outbox rows cost downstream: the relay delivers each one to
	// the Item's workflow. A workflow runs only while there is something
	// to wait for (here: the timer of in_progress).
	tc, stop, err := startTemporal(ctx)
	if err != nil {
		return err
	}
	defer stop()
	queue := "bench-" + uuid.NewString()
	w := workflow.NewWorker(tc, queue, k, workflow.WorkerOptions{})
	if err := w.Start(); err != nil {
		return err
	}
	defer w.Stop()
	if _, err := pool.Exec(ctx, `update outbox set delivered_at = now() where delivered_at is null`); err != nil {
		return err
	}
	r := relay.New(pool, relay.NewTemporalWorkflows(tc, queue), relay.Options{PollInterval: 50 * time.Millisecond})
	rctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() { _ = r.Run(rctx) }()

	delivered := func(id uuid.UUID) error {
		deadline := time.Now().Add(time.Minute)
		for {
			var n int
			if err := pool.QueryRow(ctx, `select count(*) from outbox where item_id = $1 and delivered_at is null`, id).Scan(&n); err != nil {
				return err
			}
			if n == 0 {
				return nil
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("card %s: events still undelivered", id)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	historyLength := func(id uuid.UUID) (int64, bool) {
		resp, err := tc.DescribeWorkflowExecution(ctx, id.String(), "")
		if err != nil {
			return 0, false // no workflow for this Item
		}
		return resp.GetWorkflowExecutionInfo().GetHistoryLength(), true
	}
	var reads atomic.Int64
	board := benchBoard{k: k, space: hookedSpace, reads: &reads}
	const drags = 20
	dragIt := func(id uuid.UUID) (time.Duration, error) {
		start := time.Now()
		for range drags {
			if _, err := moveCard(ctx, board, id, rank.Top, uuid.Nil); err != nil {
				return 0, err
			}
		}
		if err := delivered(id); err != nil {
			return 0, err
		}
		return time.Since(start), nil
	}
	fmt.Println("| A story_hooked card, dragged 20 times | Workflow running | History events before | after | Events per drag |")
	fmt.Println("| --- | --- | --- | --- | --- |")
	idle := hookedIDs[0]
	if _, err := dragIt(idle); err != nil {
		return err
	}
	if n, running := historyLength(idle); running {
		fmt.Printf("| in todo (nothing to wait for) | yes | | %d | |\n", n)
	} else {
		fmt.Println("| in todo (nothing to wait for) | no: the relay finds nothing listening | 0 | 0 | 0 |")
	}
	busy := hookedIDs[1]
	if err := move2(ctx, k, busy, "in_progress"); err != nil {
		return err
	}
	if err := delivered(busy); err != nil {
		return err
	}
	deadline := time.Now().Add(30 * time.Second)
	var h0 int64
	for {
		n, running := historyLength(busy)
		if running && n > 0 {
			time.Sleep(500 * time.Millisecond) // let the first workflow task settle
			h0, _ = historyLength(busy)
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("card %s: no workflow after entering in_progress", busy)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if _, err := dragIt(busy); err != nil {
		return err
	}
	time.Sleep(time.Second) // the last workflow task
	h1, _ := historyLength(busy)
	fmt.Printf("| in in_progress (its timer is running) | yes | %d | %d | %.1f |\n\n", h0, h1, float64(h1-h0)/drags)
	return nil
}

func move2(ctx context.Context, k *itemservice.Service, id uuid.UUID, to string) error {
	_, err := k.Transition(ctx, itemservice.TransitionRequest{IdempotencyKey: key(), Principal: lea, ItemID: id, ToState: to})
	return err
}
