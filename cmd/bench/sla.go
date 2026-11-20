package main

// Jira part 2, step 3b (--sla): what the `sla` package costs to read.
//
//   - The plans of the two reads a service desk makes all day: the queue
//     (open incidents by their resolution due instant) and the breached
//     search (goals whose f_breached_at is set, latest first), fresh and
//     analyzed, on the item service's own SQL.
//   - The size of a goal's row with the calendar it copies: an empty one,
//     and one with 366 days off.
//
// The incidents' goals are made by the reactions themselves, run through
// the worker's activities (no Temporal here).

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
	"github.com/francoisnyc/work-kernel/internal/typedef"
	"github.com/francoisnyc/work-kernel/internal/workflow"
)

func slaMode(ctx context.Context) error {
	plain, stopDB, err := testdb.Start(ctx)
	if err != nil {
		return err
	}
	defer stopDB()
	cfg := plain.Config().Copy()
	trace := &capture{}
	cfg.ConnConfig.Tracer = trace
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return err
	}
	defer pool.Close()
	for _, f := range []string{"bug.v3.yaml", "comment.v2.yaml", "sla/calendar.v1.yaml", "sla/sla_policy.v1.yaml",
		"sla/sla_goal.v1.yaml", "desk.v1.yaml", "incident.v1.yaml"} {
		if err := registerFile(ctx, pool, f); err != nil {
			return fmt.Errorf("%s: %w", f, err)
		}
	}
	for _, table := range []string{"items", "links", "scopes", "scope_access", "item_events"} {
		if _, err := plain.Exec(ctx, `alter table `+table+` set (autovacuum_enabled = false)`); err != nil {
			return err
		}
	}
	k := itemservice.New(pool)
	admin := item.Principal{Actor: "user:admin", Namespace: "default"}
	me := item.Principal{Actor: "user:me", Namespace: "default"}
	if _, err := plain.Exec(ctx, `insert into principal_roles (namespace_id, principal, role) values ('default', $1, 'sla_admin')`, admin.Actor); err != nil {
		return err
	}
	mk := func(p item.Principal, typ string, in *uuid.UUID, fields map[string]any) (uuid.UUID, error) {
		res, err := k.Create(ctx, itemservice.CreateRequest{IdempotencyKey: key(), Principal: p, TypeID: typ, TypeVersion: 1, ContainerID: in, Fields: fields})
		return res.ItemID, err
	}
	week := []any{"mon 09:00-17:00", "tue 09:00-17:00", "wed 09:00-17:00", "thu 09:00-17:00", "fri 09:00-17:00"}
	policy := map[string]any{"f_name": "standard"}
	for i, p := range []string{"p1", "p2", "p3", "p4"} {
		policy["f_first_response_s_"+p] = 3600 * (i + 1)
		policy["f_resolution_s_"+p] = 4 * 3600 * (i + 1)
	}
	policyID, err := mk(admin, "sla_policy", nil, policy)
	if err != nil {
		return err
	}
	// desk makes a desk on a calendar with n days off.
	// A second policy, of one second for everything, on a calendar that is
	// always open: its goals are past their instants a moment after they
	// start, so they can be marked breached on the service's own clock.
	fast := map[string]any{"f_name": "one second"}
	for _, p := range []string{"p1", "p2", "p3", "p4"} {
		fast["f_first_response_s_"+p], fast["f_resolution_s_"+p] = 1, 1
	}
	fastID, err := mk(admin, "sla_policy", nil, fast)
	if err != nil {
		return err
	}
	always := []any{"mon 00:00-24:00", "tue 00:00-24:00", "wed 00:00-24:00", "thu 00:00-24:00", "fri 00:00-24:00", "sat 00:00-24:00", "sun 00:00-24:00"}
	desk := func(daysOff int, quick bool) (uuid.UUID, error) {
		cal := map[string]any{"f_name": fmt.Sprintf("%d days off", daysOff), "f_week": week, "f_zone": "Europe/Paris"}
		pol := policyID
		if quick {
			cal["f_week"], cal["f_zone"], pol = always, "UTC", fastID
		}
		if daysOff > 0 {
			var days []any
			for i := range daysOff {
				days = append(days, time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, i).Format(time.DateOnly))
			}
			cal["f_days_off"] = days
		}
		calID, err := mk(admin, "calendar", nil, cal)
		if err != nil {
			return uuid.Nil, err
		}
		d, err := mk(admin, "desk", nil, map[string]any{"f_key": "IT", "f_name": "IT"})
		if err != nil {
			return uuid.Nil, err
		}
		for link, target := range map[string]uuid.UUID{"calendar": calID, "sla_policy": pol} {
			if _, err := k.Link(ctx, itemservice.LinkRequest{IdempotencyKey: key(), Principal: admin, SourceID: d, LinkType: link, TargetID: target}); err != nil {
				return uuid.Nil, err
			}
		}
		return d, nil
	}
	// The reactions and the breach mark are run through the worker's own
	// activities (only the worker and the hook runner may use the item
	// service's internal door).
	act := &workflow.Activities{Items: k}
	react := func(source uuid.UUID, typeID, reaction string, cause uuid.UUID, seq int64) error {
		return act.RunAfterReaction(ctx, workflow.AfterReactionInput{
			ItemID: source.String(), TypeID: typeID, TypeVersion: 1, ReactionID: reaction,
			CauseItem: cause.String(), CauseSeq: seq, Hop: 1,
		})
	}
	goalType, err := k.Type(ctx, "sla_goal", 1)
	if err != nil {
		return err
	}
	running, _ := goalType.State("running")
	// goalsOf lists an incident's goals: first response, then resolution.
	goalsOf := func(inc uuid.UUID) ([]uuid.UUID, error) {
		rows, err := plain.Query(ctx, `select id from items where container_id = $1 and type_id = 'sla_goal' order by fields->>'f_metric'`, inc)
		if err != nil {
			return nil, err
		}
		return pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	}
	// shows runs the goal's own set reactions for the event that entered
	// its state: the one of its metric writes the incident.
	shows := func(goal uuid.UUID, state string, seq int64) error {
		for _, set := range goalType.Reactions {
			if set.State == state {
				if err := react(goal, "sla_goal", set.ID, goal, seq); err != nil {
					return err
				}
			}
		}
		return nil
	}
	// incident makes an incident with both goals shown on it; it returns
	// the resolution goal.
	incident := func(d uuid.UUID, i int) (inc, res uuid.UUID, err error) {
		inc, err = mk(me, "incident", &d, map[string]any{"f_title": fmt.Sprintf("incident %d alpha", i), "f_priority": fmt.Sprintf("P%d", 1+i%4)})
		if err != nil {
			return
		}
		for _, r := range []string{"sla_start_first_response", "sla_start_resolution"} {
			if err = react(inc, "incident", r, inc, 1); err != nil {
				return
			}
		}
		goals, gerr := goalsOf(inc)
		if gerr != nil || len(goals) != 2 {
			return inc, res, fmt.Errorf("the incident has %d goals, want 2: %v", len(goals), gerr)
		}
		for _, g := range goals {
			if err = shows(g, "running", 1); err != nil {
				return
			}
		}
		return inc, goals[1], nil
	}

	// --- row size ---
	fmt.Println("# The sla package: plans and sizes (Jira part 2, step 3b)")
	fmt.Println()
	machine(ctx, plain)
	fmt.Println("## A goal's row, with the calendar it copies")
	fmt.Println()
	fmt.Println("| Calendar | fields (bytes) | whole row (bytes) | the goal's create event values (bytes) |")
	fmt.Println("| --- | --- | --- | --- |")
	for _, n := range []int{0, 10, 366} {
		d, err := desk(n, false)
		if err != nil {
			return err
		}
		_, goal, err := incident(d, 0)
		if err != nil {
			return err
		}
		var fields, row, values int
		if err := plain.QueryRow(ctx, `select pg_column_size(fields), pg_column_size(i.*) from items i where id = $1`, goal).Scan(&fields, &row); err != nil {
			return err
		}
		if err := plain.QueryRow(ctx, `select coalesce(sum(pg_column_size(v.*)), 0) from item_event_values v where item_id = $1`, goal).Scan(&values); err != nil {
			return err
		}
		fmt.Printf("| a week of 5 intervals, %d days off | %d | %d | %d |\n", n, fields, row, values)
	}
	fmt.Println()

	// --- the plans ---
	const incidents = 5000
	d, err := desk(10, false)
	if err != nil {
		return err
	}
	quick, err := desk(0, true)
	if err != nil {
		return err
	}
	breached := 0
	late := make([]uuid.UUID, incidents) // the resolution goals to mark
	if _, _, err := timeAll(incidents, 8, func(i int) error {
		in := d
		if i%10 == 0 { // a tenth will breach, and stay running
			in = quick
		}
		inc, goal, err := incident(in, i)
		if err != nil {
			return err
		}
		switch {
		case i%10 == 0:
			late[i] = goal
		case i%10 < 5: // four in ten resolved: no due instant shown any more
			if _, err = k.Transition(ctx, itemservice.TransitionRequest{IdempotencyKey: key(), Principal: me, ItemID: inc, ToState: "in_progress"}); err != nil {
				return err
			}
			var res itemservice.TransitionResult
			if res, err = k.Transition(ctx, itemservice.TransitionRequest{IdempotencyKey: key(), Principal: me, ItemID: inc, ToState: "resolved",
				Fields: map[string]any{"f_resolution": "fixed"}}); err != nil {
				return err
			}
			if err = react(inc, "incident", "sla_stop", inc, res.ItemSeq); err != nil {
				return err
			}
			goals, gerr := goalsOf(inc)
			if gerr != nil {
				return gerr
			}
			for _, g := range goals {
				var seq int64
				if err = plain.QueryRow(ctx, `select item_seq from item_events where item_id = $1 and op = 'transition' order by item_seq desc limit 1`, g).Scan(&seq); err != nil {
					return err
				}
				if err = shows(g, "completed", seq); err != nil {
					return err
				}
			}
		}
		return err
	}); err != nil {
		return fmt.Errorf("seed incidents: %w", err)
	}
	time.Sleep(2 * time.Second) // past the one-second targets
	for _, goal := range late {
		if goal == uuid.Nil {
			continue
		}
		due, _ := typedef.ParseDateTime(mustFields(ctx, k, admin, goal)["f_due_at"])
		if _, err := act.ActAt(ctx, workflow.ActAtInput{ItemID: goal.String(), TypeID: "sla_goal", TypeVersion: 1, State: "running",
			TimerID: running.Acting[0].ID, EnteredSeq: 1, Instant: due, Hop: 2}); err != nil {
			return fmt.Errorf("mark a goal breached: %w", err)
		}
	}
	if err := plain.QueryRow(ctx, `select count(*) from items where type_id = 'sla_goal' and fields ? 'f_breached_at'`).Scan(&breached); err != nil {
		return err
	}
	fmt.Printf("%d incidents in two desks, each with two goals (%d goals); a tenth breached and still running (%d goals marked), four in ten resolved.\n\n",
		incidents, 2*incidents, breached)

	now := time.Now().Add(6 * time.Hour).UTC().Format(typedef.DateTimeLayout)
	search := func(req itemservice.SearchRequest) func() error {
		return func() error {
			req.Principal = me
			_, err := k.Search(ctx, req)
			return err
		}
	}
	asc := &itemservice.OrderBy{Field: "f_due_resolution"}
	steps := []struct {
		name string
		run  func() error
	}{
		{"The queue: incidents with a resolution due instant (3,000 of 5,000), most urgent first, page 1",
			search(itemservice.SearchRequest{Filter: itemservice.Filter{TypeIDs: []string{"incident"},
				Fields: []itemservice.FieldFilter{{Field: "f_due_resolution", NotEmpty: true}}}, OrderBy: asc, Limit: 50})},
		{"The queue, open incidents only (triage, active, waiting), by due instant",
			search(itemservice.SearchRequest{Filter: itemservice.Filter{TypeIDs: []string{"incident"}, Categories: []string{"triage", "active", "waiting"}},
				OrderBy: asc, Limit: 50})},
		{"Overdue now: due before an instant, most overdue first",
			search(itemservice.SearchRequest{Filter: itemservice.Filter{TypeIDs: []string{"incident"},
				Fields: []itemservice.FieldFilter{{Field: "f_due_resolution", Lt: now}}}, OrderBy: asc, Limit: 50})},
		{"Ever breached: goals whose f_breached_at is set (500 of 10,000), latest first",
			search(itemservice.SearchRequest{Filter: itemservice.Filter{TypeIDs: []string{"sla_goal"},
				Fields: []itemservice.FieldFilter{{Field: "f_breached_at", NotEmpty: true}}},
				OrderBy: &itemservice.OrderBy{Field: "f_breached_at", Desc: true}, Limit: 50})},
	}
	summary := make([][2]string, len(steps))
	notes := make([]string, len(steps))
	for pass, title := range []string{"Before ANALYZE", "After ANALYZE"} {
		if pass == 1 {
			if _, err := plain.Exec(ctx, `analyze`); err != nil {
				return err
			}
		}
		fmt.Printf("## %s\n\n", title)
		for i, s := range steps {
			if err := s.run(); err != nil {
				return fmt.Errorf("%s: %w", s.name, err)
			}
			got, err := trace.during(s.run)
			if err != nil {
				return fmt.Errorf("%s: %w", s.name, err)
			}
			var stmt *captured
			for j := range got {
				if isPage(got[j].sql) {
					stmt = &got[j]
					break
				}
			}
			if stmt == nil {
				return fmt.Errorf("%s: the statement to explain was not captured", s.name)
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
				summary[i][pass] = m[1]
			}
			notes[i] = "no sort"
			if m := sortLine.FindStringSubmatch(plan); m != nil {
				notes[i] = "sort: " + m[1] + ", " + m[2] + " kB"
			}
			if m := rowsFirst.FindAllStringSubmatch(plan, -1); len(m) > 0 {
				most := 0
				for _, x := range m {
					var n int
					fmt.Sscan(x[1], &n)
					most = max(most, n)
				}
				notes[i] += fmt.Sprintf("; widest node %d rows", most)
			}
		}
	}
	fmt.Println("## Summary")
	fmt.Println()
	fmt.Println("| Read | Fresh (ms) | Analyzed (ms) | Analyzed plan |")
	fmt.Println("| --- | --- | --- | --- |")
	for i, s := range steps {
		fmt.Printf("| %s | %s | %s | %s |\n", s.name, summary[i][0], summary[i][1], notes[i])
	}
	fmt.Println()
	return nil
}

func mustFields(ctx context.Context, k *itemservice.Service, p item.Principal, id uuid.UUID) map[string]any {
	it, err := k.Get(ctx, p, id)
	if err != nil {
		return nil
	}
	return it.Fields
}
