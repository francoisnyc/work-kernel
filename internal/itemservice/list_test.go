// Tests for Step 0, part 2: new kernel reads.
//
//   - Every Item carries its create time, its creator (event 1's actor) and,
//     when an agent created it, the user it acted for: columns on items,
//     backfilled from event 1 by the migration.
//   - List: a container's direct children, or the top-level Items, that the
//     principal can see; the same filters as Search.
//   - Filters (Search and List): Types, states, categories (read in each
//     Item's own Type version), field equality and int ranges. A filter alone
//     is a search.
//   - Paging (Search, List, ListEvents): Limit (default 50, max 500), an
//     opaque page token bound to the request, the caller and the namespace.
//     Items come newest first by (created_at, id).
//   - The new reads (List, ListEvents) refuse a principal without a
//     namespace. Search keeps part 1's behaviour (Later: require it
//     everywhere).
package itemservice_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pressly/goose/v3"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
	"github.com/francoisnyc/work-kernel/internal/typedef"
	"github.com/francoisnyc/work-kernel/migrations"
)

// --- helpers ---

func idsOf(items []item.Item) []uuid.UUID {
	ids := make([]uuid.UUID, len(items))
	for i, it := range items {
		ids[i] = it.ID
	}
	return ids
}

// sameIDs compares two sets of IDs, ignoring order.
func sameIDs(a, b []uuid.UUID) bool {
	key := func(id uuid.UUID) string { return id.String() }
	as, bs := make([]string, len(a)), make([]string, len(b))
	for i := range a {
		as[i] = key(a[i])
	}
	for i := range b {
		bs[i] = key(b[i])
	}
	slices.Sort(as)
	slices.Sort(bs)
	return slices.Equal(as, bs)
}

// assertInvalidAt checks err is an InvalidError on the request path (or a
// path below it, such as filter.fields[0] under filter.fields).
func assertInvalidAt(t *testing.T, what string, err error, path string) {
	t.Helper()
	var ie *itemservice.InvalidError
	if !errors.As(err, &ie) {
		t.Errorf("%s: error = %v, want an InvalidError on %s", what, err, path)
		return
	}
	if ie.Path != path && !strings.HasPrefix(ie.Path, path+"[") && !strings.HasPrefix(ie.Path, path+".") {
		t.Errorf("%s: InvalidError on %q (%v), want %s", what, ie.Path, err, path)
	}
}

// newestFirst reads the IDs in the order the reads promise, from the table.
func newestFirst(t *testing.T, ids []uuid.UUID) []uuid.UUID {
	t.Helper()
	rows, err := pool.Query(testCtx(t),
		`select id from items where id = any($1) order by created_at desc, id desc`, ids)
	if err != nil {
		t.Fatalf("order: %v", err)
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, id)
	}
	return out
}

// filterType registers a Type of its own (fresh ID) in two versions, for the
// filter tests:
//   - s_work is "active" in v1 and "waiting" in v2: a category is read in
//     each Item's own version.
//   - f_mixed is an int in v1 and a string in v2: it cannot be filtered on.
func filterType(t *testing.T, k *itemservice.Service) string {
	t.Helper()
	id := "flt-" + uuid.NewString()[:8]
	for version, def := range map[int]struct{ workCategory, mixed string }{
		1: {"active", "int"},
		2: {"waiting", "string"},
	} {
		ty, err := typedef.Parse(fmt.Appendf(nil, `
id: %s
version: %d
initial_state: s_open
fields:
  - {id: f_n,     type: int}
  - {id: f_s,     type: string}
  - {id: f_e,     type: enum, options: [red, green]}
  - {id: f_b,     type: bool}
  - {id: f_mixed, type: %s}
states:
  - {id: s_open, category: todo}
  - {id: s_work, category: %s}
  - {id: s_done, category: done}
transitions:
  - {from: s_open, to: s_work}
  - {from: s_work, to: s_done}
`, id, version, def.mixed, def.workCategory))
		if err != nil {
			t.Fatalf("parse filter Type v%d: %v", version, err)
		}
		if err := k.RegisterType(testCtx(t), ty); err != nil {
			t.Fatalf("register filter Type v%d: %v", version, err)
		}
	}
	return id
}

// filterWorld: a namespace of its own (so filter-only searches see only
// these Items), a user, and Items of the filter Type.
type filterWorld struct {
	k    *itemservice.Service
	user item.Principal
	typ  string
}

func newFilterWorld(t *testing.T) filterWorld {
	t.Helper()
	k := itemservice.New(pool)
	ns := newNamespace(t, k)
	return filterWorld{k: k, user: in(person("filterer"), ns), typ: filterType(t, k)}
}

// make creates an Item of the filter Type (version v), moves it to state,
// and returns its ID.
func (w filterWorld) make(t *testing.T, v int, state string, fields map[string]any) uuid.UUID {
	t.Helper()
	res, err := w.k.Create(testCtx(t), itemservice.CreateRequest{
		IdempotencyKey: newKey(), Principal: w.user, TypeID: w.typ, TypeVersion: v, Fields: fields,
	})
	if err != nil {
		t.Fatalf("create %s v%d: %v", w.typ, v, err)
	}
	path := map[string][]string{"s_open": nil, "s_work": {"s_work"}, "s_done": {"s_work", "s_done"}}[state]
	for _, to := range path {
		if _, err := w.k.Transition(testCtx(t), itemservice.TransitionRequest{
			IdempotencyKey: newKey(), Principal: w.user, ItemID: res.ItemID, ToState: to,
		}); err != nil {
			t.Fatalf("transition to %s: %v", to, err)
		}
	}
	return res.ItemID
}

func (w filterWorld) search(t *testing.T, f itemservice.Filter) []uuid.UUID {
	t.Helper()
	res, err := w.k.Search(testCtx(t), itemservice.SearchRequest{Principal: w.user, Filter: f, Limit: 500})
	if err != nil {
		t.Fatalf("Search %+v: %v", f, err)
	}
	return idsOf(res.Items)
}

// --- create time and creator ---

// An Item carries when and by whom it was created, on every read; later
// writes by others do not change it.
func TestCreated_ItemCarriesCreateTimeAndCreator(t *testing.T) {
	k := itemservice.New(pool)
	ana, bob := person("ana"), person("bob")
	before := time.Now()
	id := createAs(t, k, ana, "made by ana "+word(), nil, nil).ItemID
	if err := agentUpdate(t, k, bob, id); err != nil {
		t.Fatalf("bob updates: %v", err)
	}

	var eventTime time.Time
	if err := pool.QueryRow(testCtx(t), `select created_at from item_events where item_id = $1 and item_seq = 1`,
		id).Scan(&eventTime); err != nil {
		t.Fatalf("read event 1: %v", err)
	}
	check := func(what string, it item.Item) {
		t.Helper()
		if it.CreatedBy != ana.Actor || it.CreatedOnBehalfOf != "" {
			t.Errorf("%s: created by %q for %q, want %s for nobody", what, it.CreatedBy, it.CreatedOnBehalfOf, ana.Actor)
		}
		if !it.CreatedAt.Equal(eventTime) || it.CreatedAt.Before(before.Add(-time.Minute)) {
			t.Errorf("%s: created at %v, want event 1's time %v", what, it.CreatedAt, eventTime)
		}
	}
	it, err := k.Get(testCtx(t), bob, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	check("Get", it)

	res, err := k.Search(testCtx(t), itemservice.SearchRequest{Principal: bob, Query: "made by ana"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if i := slices.Index(idsOf(res.Items), id); i < 0 {
		t.Errorf("Search did not find the Item")
	} else {
		check("Search", res.Items[i])
	}
}

// An Item an agent created names the agent as creator and the user it acted
// for, so a UI can say "Tom, via triage-bot" without reading history.
func TestCreated_ByAnAgent_NamesTheUserToo(t *testing.T) {
	k := itemservice.New(pool)
	tom := person("tom")
	bot := agentFor(t, k, tom)
	res, err := k.Create(testCtx(t), itemservice.CreateRequest{
		IdempotencyKey: newKey(), Principal: bot, TypeID: "ticket", TypeVersion: 1,
	})
	if err != nil {
		t.Fatalf("agent creates: %v", err)
	}
	it, err := k.Get(testCtx(t), tom, res.ItemID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if it.CreatedBy != bot.Actor || it.CreatedOnBehalfOf != tom.Actor {
		t.Errorf("created by %q for %q, want %s for %s", it.CreatedBy, it.CreatedOnBehalfOf, bot.Actor, tom.Actor)
	}
	n := count(t, `select count(*) from items where id = $1 and created_by = $2 and created_on_behalf_of = $3
	               and created_at is not null`, res.ItemID, bot.Actor, tom.Actor)
	if n != 1 {
		t.Errorf("items row does not hold created_at, created_by and created_on_behalf_of")
	}
}

// The migration backfills the three columns from each Item's first event,
// not a later one. It runs on a database of its own, migrated to the last
// migration before part 2, seeded by hand, then migrated to the end.
func TestCreated_MigrationBackfillsFromEventOne(t *testing.T) {
	ctx := testCtx(t)
	dbName := "backfill_" + strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	if _, err := pool.Exec(ctx, "create database "+dbName); err != nil {
		t.Fatalf("create database: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), "drop database "+dbName+" with (force)") })

	cfg := pool.Config().ConnConfig
	dsn := fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=disable", cfg.User, cfg.Password, cfg.Host, cfg.Port, dbName)
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
	if err != nil {
		t.Fatalf("goose: %v", err)
	}
	const lastBeforePart2 = 8 // 00008_step6_item_holds.sql
	if _, err := provider.UpTo(ctx, lastBeforePart2); err != nil {
		t.Fatalf("migrate to %d: %v", lastBeforePart2, err)
	}

	plain, byAgent := uuid.New(), uuid.New()
	first := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	seed := []string{
		`insert into items (id, namespace_id, type_id, type_version, scope_id, state, item_seq)
		 select $1, 'default', 'ticket', 1, id, 'open', 2 from scopes where namespace_id = 'default' and parent_id is null`,
		`insert into items (id, namespace_id, type_id, type_version, scope_id, state, item_seq)
		 select $2, 'default', 'ticket', 1, id, 'open', 1 from scopes where namespace_id = 'default' and parent_id is null`,
		`insert into item_events (item_id, item_seq, actor, op, created_at) values ($1, 1, 'user:ana', 'create', $3)`,
		`insert into item_events (item_id, item_seq, actor, op, created_at) values ($1, 2, 'user:bob', 'update', $3::timestamptz + interval '1 day')`,
		`insert into item_events (item_id, item_seq, actor, on_behalf_of, agent_session, op, created_at)
		 values ($2, 1, 'agent:bot', 'user:tom', 'ses_x', 'create', $3)`,
	}
	for _, q := range seed {
		if _, err := db.ExecContext(ctx, q, plain, byAgent, first); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("migrate to the end: %v", err)
	}

	for _, c := range []struct {
		id             uuid.UUID
		by, onBehalfOf string
	}{{plain, "user:ana", ""}, {byAgent, "agent:bot", "user:tom"}} {
		var (
			at         time.Time
			by         string
			onBehalfOf sql.NullString
		)
		if err := db.QueryRowContext(ctx,
			`select created_at, created_by, created_on_behalf_of from items where id = $1`, c.id,
		).Scan(&at, &by, &onBehalfOf); err != nil {
			t.Fatalf("read backfilled %s: %v", c.id, err)
		}
		if !at.Equal(first) || by != c.by || onBehalfOf.String != c.onBehalfOf {
			t.Errorf("backfilled %v, %q, %q; want %v, %q, %q", at, by, onBehalfOf.String, first, c.by, c.onBehalfOf)
		}
	}
	// Every Item has a creator from now on.
	var nullable string
	if err := db.QueryRowContext(ctx,
		`select is_nullable from information_schema.columns where table_name = 'items' and column_name = 'created_by'`,
	).Scan(&nullable); err != nil || nullable != "NO" {
		t.Errorf("items.created_by nullable = %q (%v), want NO", nullable, err)
	}
}

// --- List ---

// List gives a container's direct children, not grandchildren, newest first.
func TestList_DirectChildrenOnly(t *testing.T) {
	k := itemservice.New(pool)
	ana := person("ana")
	box := createAs(t, k, ana, "box", nil, nil).ItemID
	c1 := createAs(t, k, ana, "child 1", nil, &box).ItemID
	c2 := createAs(t, k, ana, "child 2", nil, &box).ItemID
	createAs(t, k, ana, "grandchild", nil, &c1)

	res, err := k.List(testCtx(t), itemservice.ListRequest{Principal: ana, ContainerID: &box})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got, want := idsOf(res.Items), newestFirst(t, []uuid.UUID{c1, c2}); !slices.Equal(got, want) {
		t.Errorf("List = %v, want %v (newest first)", got, want)
	}
	if res.NextPageToken != "" {
		t.Errorf("one page only, but next page token %q", res.NextPageToken)
	}
	if res.Items[0].CreatedBy != ana.Actor {
		t.Errorf("listed Items carry no creator: %+v", res.Items[0])
	}
}

// No container lists the top-level Items of the caller's namespace.
func TestList_TopLevel(t *testing.T) {
	k := itemservice.New(pool)
	ns := newNamespace(t, k)
	ana := in(person("ana"), ns)
	top := createIn(t, k, ana, nil, nil)
	createIn(t, k, ana, nil, &top)
	other := createIn(t, k, ana, nil, nil)

	res, err := k.List(testCtx(t), itemservice.ListRequest{Principal: ana})
	if err != nil {
		t.Fatalf("List top: %v", err)
	}
	if !sameIDs(idsOf(res.Items), []uuid.UUID{top, other}) {
		t.Errorf("List top = %v, want %v and %v", idsOf(res.Items), top, other)
	}
}

// A container the caller cannot see, or that does not exist, or that is in
// another namespace, is ErrNotFound alike; a narrowed child the caller cannot
// see is simply not listed.
func TestList_Visibility(t *testing.T) {
	k := itemservice.New(pool)
	ana, bob := person("ana"), person("bob")
	box := createAs(t, k, ana, "box", nil, nil).ItemID
	shared := createAs(t, k, ana, "shared", nil, &box).ItemID
	createAs(t, k, ana, "ana only", &itemservice.Narrow{Readers: readers(ana)}, &box)
	private := createAs(t, k, ana, "private box", &itemservice.Narrow{Readers: readers(ana)}, nil).ItemID

	res, err := k.List(testCtx(t), itemservice.ListRequest{Principal: bob, ContainerID: &box})
	if err != nil {
		t.Fatalf("List as bob: %v", err)
	}
	if !sameIDs(idsOf(res.Items), []uuid.UUID{shared}) {
		t.Errorf("bob lists %v, want only %s", idsOf(res.Items), shared)
	}
	_, err = k.List(testCtx(t), itemservice.ListRequest{Principal: bob, ContainerID: &private})
	assertNotFound(t, "List of a hidden container", err)
	missing := uuid.New()
	_, err = k.List(testCtx(t), itemservice.ListRequest{Principal: bob, ContainerID: &missing})
	assertNotFound(t, "List of a missing container", err)
	_, err = k.List(testCtx(t), itemservice.ListRequest{Principal: in(ana, newNamespace(t, k)), ContainerID: &box})
	assertNotFound(t, "List of a container in another namespace", err)
}

// List takes the same filters as Search.
func TestList_Filters(t *testing.T) {
	w := newFilterWorld(t)
	box := w.make(t, 1, "s_open", nil)
	in := func(state string, n int) uuid.UUID {
		res, err := w.k.Create(testCtx(t), itemservice.CreateRequest{
			IdempotencyKey: newKey(), Principal: w.user, TypeID: w.typ, TypeVersion: 1, ContainerID: &box,
			Fields: map[string]any{"f_n": n},
		})
		if err != nil {
			t.Fatalf("create in box: %v", err)
		}
		if state == "s_work" {
			if _, err := w.k.Transition(testCtx(t), itemservice.TransitionRequest{
				IdempotencyKey: newKey(), Principal: w.user, ItemID: res.ItemID, ToState: "s_work",
			}); err != nil {
				t.Fatalf("transition: %v", err)
			}
		}
		return res.ItemID
	}
	in("s_open", 1)
	working := in("s_work", 5)
	in("s_work", 9)

	res, err := w.k.List(testCtx(t), itemservice.ListRequest{Principal: w.user, ContainerID: &box, Filter: itemservice.Filter{
		TypeIDs: []string{w.typ}, States: []string{"s_work"},
		Fields: []itemservice.FieldFilter{{Field: "f_n", Lt: 6}},
	}})
	if err != nil {
		t.Fatalf("List with a filter: %v", err)
	}
	if !sameIDs(idsOf(res.Items), []uuid.UUID{working}) {
		t.Errorf("List filtered = %v, want only %s", idsOf(res.Items), working)
	}
}

// An agent's List records the narrowing scopes of what it read, like Search.
func TestList_AgentReadsAreRecorded(t *testing.T) {
	w := newAgentWorld(t)
	if _, err := w.k.List(testCtx(t), itemservice.ListRequest{Principal: w.agent, ContainerID: &w.space}); err != nil {
		t.Fatalf("List as the agent: %v", err)
	}
	if got := recordedReads(t, w.agent.AgentSession); len(got) == 0 {
		t.Errorf("no narrowing read recorded for the agent's List of a narrowed space")
	}
}

// The new reads refuse a principal with no namespace: in the kernel, empty
// would mean "default", which is how a forgotten path would leak.
func TestNewReads_RefuseAnEmptyNamespace(t *testing.T) {
	k := itemservice.New(pool)
	ana := person("ana") // Namespace ""
	id := createAs(t, k, ana, "x", nil, nil).ItemID
	_, err := k.List(testCtx(t), itemservice.ListRequest{Principal: ana, ContainerID: &id})
	if !errors.Is(err, itemservice.ErrInvalid) {
		t.Errorf("List: error = %v, want ErrInvalid", err)
	}
	_, err = k.ListEvents(testCtx(t), itemservice.ListEventsRequest{Principal: ana, ItemID: id})
	if !errors.Is(err, itemservice.ErrInvalid) {
		t.Errorf("ListEvents: error = %v, want ErrInvalid", err)
	}
}

// --- filters ---

// A filter alone is a search; words and a filter together must both match.
func TestFilters_AloneOrWithWords(t *testing.T) {
	w := newFilterWorld(t)
	a := w.make(t, 1, "s_open", map[string]any{"f_s": "alpha"})
	b := w.make(t, 1, "s_open", map[string]any{"f_s": "beta"})
	if got := w.search(t, itemservice.Filter{TypeIDs: []string{w.typ}}); !sameIDs(got, []uuid.UUID{a, b}) {
		t.Errorf("filter alone = %v, want both", got)
	}
	res, err := w.k.Search(testCtx(t), itemservice.SearchRequest{
		Principal: w.user, Query: "beta", Filter: itemservice.Filter{TypeIDs: []string{w.typ}},
	})
	if err != nil || !sameIDs(idsOf(res.Items), []uuid.UUID{b}) {
		t.Errorf("words and filter = %v (%v), want only %s", idsOf(res.Items), err, b)
	}
	_, err = w.k.Search(testCtx(t), itemservice.SearchRequest{Principal: w.user})
	if !errors.Is(err, itemservice.ErrInvalid) {
		t.Errorf("neither words nor filter: error = %v, want ErrInvalid", err)
	}
}

// Within a list any entry matches; across lists all must.
func TestFilters_TypesAndStates(t *testing.T) {
	w := newFilterWorld(t)
	open := w.make(t, 1, "s_open", nil)
	work := w.make(t, 2, "s_work", nil)
	done := w.make(t, 1, "s_done", nil)
	ticket := createIn(t, w.k, w.user, nil, nil) // a ticket, in state open

	cases := []struct {
		name string
		f    itemservice.Filter
		want []uuid.UUID
	}{
		{"one Type, every version", itemservice.Filter{TypeIDs: []string{w.typ}}, []uuid.UUID{open, work, done}},
		{"two Types", itemservice.Filter{TypeIDs: []string{w.typ, "ticket"}}, []uuid.UUID{open, work, done, ticket}},
		{"states, any Type", itemservice.Filter{States: []string{"s_work", "open"}}, []uuid.UUID{work, ticket}},
		{"Type and state", itemservice.Filter{TypeIDs: []string{w.typ}, States: []string{"s_open", "s_done"}}, []uuid.UUID{open, done}},
		{"nothing matches", itemservice.Filter{TypeIDs: []string{"ticket"}, States: []string{"s_open"}}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := w.search(t, c.f); !sameIDs(got, c.want) {
				t.Errorf("found %v, want %v", got, c.want)
			}
		})
	}
}

// A category is read in each Item's own Type version: s_work is active in v1
// and waiting in v2. A Type without states is category none.
func TestFilters_CategoryIsPerTypeVersion(t *testing.T) {
	w := newFilterWorld(t)
	work1 := w.make(t, 1, "s_work", nil)
	work2 := w.make(t, 2, "s_work", nil)
	done := w.make(t, 2, "s_done", nil)
	registerMessageType(t, w.k)
	msg, err := w.k.Create(testCtx(t), itemservice.CreateRequest{
		IdempotencyKey: newKey(), Principal: w.user, TypeID: "message", TypeVersion: 1,
	})
	if err != nil {
		t.Fatalf("create message: %v", err)
	}

	for _, c := range []struct {
		categories []string
		want       []uuid.UUID
	}{
		{[]string{"active"}, []uuid.UUID{work1}},
		{[]string{"waiting"}, []uuid.UUID{work2}},
		{[]string{"active", "done"}, []uuid.UUID{work1, done}},
		{[]string{"none"}, []uuid.UUID{msg.ItemID}},
	} {
		if got := w.search(t, itemservice.Filter{Categories: c.categories}); !sameIDs(got, c.want) {
			t.Errorf("categories %v: found %v, want %v", c.categories, got, c.want)
		}
	}
}

// Field equality on every type; ranges on ints, one or two bounds.
func TestFilters_Fields(t *testing.T) {
	w := newFilterWorld(t)
	n := func(v int, s, e string, b bool) uuid.UUID {
		return w.make(t, 1, "s_open", map[string]any{"f_n": v, "f_s": s, "f_e": e, "f_b": b})
	}
	one, five, nine := n(1, "a", "red", true), n(5, "b", "green", false), n(9, "a", "green", true)
	unset := w.make(t, 1, "s_open", nil)
	_ = unset

	eq := func(field string, v any) itemservice.FieldFilter { return itemservice.FieldFilter{Field: field, Eq: v} }
	cases := []struct {
		name   string
		fields []itemservice.FieldFilter
		want   []uuid.UUID
	}{
		{"int eq", []itemservice.FieldFilter{eq("f_n", 5)}, []uuid.UUID{five}},
		{"int eq as int64", []itemservice.FieldFilter{eq("f_n", int64(9))}, []uuid.UUID{nine}},
		{"string eq", []itemservice.FieldFilter{eq("f_s", "a")}, []uuid.UUID{one, nine}},
		{"enum eq", []itemservice.FieldFilter{eq("f_e", "green")}, []uuid.UUID{five, nine}},
		{"bool eq", []itemservice.FieldFilter{eq("f_b", false)}, []uuid.UUID{five}},
		{"gt", []itemservice.FieldFilter{{Field: "f_n", Gt: 5}}, []uuid.UUID{nine}},
		{"gte", []itemservice.FieldFilter{{Field: "f_n", Gte: 5}}, []uuid.UUID{five, nine}},
		{"lt", []itemservice.FieldFilter{{Field: "f_n", Lt: 5}}, []uuid.UUID{one}},
		{"lte", []itemservice.FieldFilter{{Field: "f_n", Lte: 5}}, []uuid.UUID{one, five}},
		{"a range", []itemservice.FieldFilter{{Field: "f_n", Gt: 1, Lte: 9}}, []uuid.UUID{five, nine}},
		{"two fields, both hold", []itemservice.FieldFilter{eq("f_s", "a"), {Field: "f_n", Gt: 2}}, []uuid.UUID{nine}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := w.search(t, itemservice.Filter{TypeIDs: []string{w.typ}, Fields: c.fields})
			if !sameIDs(got, c.want) {
				t.Errorf("found %v, want %v", got, c.want)
			}
		})
	}
}

// Malformed filters name where they are wrong.
func TestFilters_Refusals(t *testing.T) {
	w := newFilterWorld(t)
	typ := []string{w.typ}
	cases := []struct {
		name string
		f    itemservice.Filter
		path string
	}{
		{"field filter without a Type", itemservice.Filter{Fields: []itemservice.FieldFilter{{Field: "f_n", Eq: 1}}}, "filter.types"},
		{"field filter with two Types", itemservice.Filter{TypeIDs: []string{w.typ, "ticket"},
			Fields: []itemservice.FieldFilter{{Field: "f_n", Eq: 1}}}, "filter.types"},
		{"unknown Type", itemservice.Filter{TypeIDs: []string{"no-such-type"}}, "filter.types"},
		{"unknown field", itemservice.Filter{TypeIDs: typ, Fields: []itemservice.FieldFilter{{Field: "f_nope", Eq: 1}}}, "filter.fields"},
		{"field type differs across versions", itemservice.Filter{TypeIDs: typ,
			Fields: []itemservice.FieldFilter{{Field: "f_mixed", Eq: "1"}}}, "filter.fields"},
		{"range on a string", itemservice.Filter{TypeIDs: typ, Fields: []itemservice.FieldFilter{{Field: "f_s", Gt: "a"}}}, "filter.fields"},
		{"range on an enum", itemservice.Filter{TypeIDs: typ, Fields: []itemservice.FieldFilter{{Field: "f_e", Lt: "red"}}}, "filter.fields"},
		{"range on a bool", itemservice.Filter{TypeIDs: typ, Fields: []itemservice.FieldFilter{{Field: "f_b", Gte: true}}}, "filter.fields"},
		{"eq and a bound", itemservice.Filter{TypeIDs: typ, Fields: []itemservice.FieldFilter{{Field: "f_n", Eq: 1, Gt: 0}}}, "filter.fields"},
		{"gt and gte", itemservice.Filter{TypeIDs: typ, Fields: []itemservice.FieldFilter{{Field: "f_n", Gt: 1, Gte: 2}}}, "filter.fields"},
		{"no condition", itemservice.Filter{TypeIDs: typ, Fields: []itemservice.FieldFilter{{Field: "f_n"}}}, "filter.fields"},
		{"value of the wrong type", itemservice.Filter{TypeIDs: typ, Fields: []itemservice.FieldFilter{{Field: "f_n", Eq: "five"}}}, "filter.fields"},
		{"enum value not an option", itemservice.Filter{TypeIDs: typ, Fields: []itemservice.FieldFilter{{Field: "f_e", Eq: "blue"}}}, "filter.fields"},
		{"unknown category", itemservice.Filter{Categories: []string{"urgent"}}, "filter.categories"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := w.k.Search(testCtx(t), itemservice.SearchRequest{Principal: w.user, Filter: c.f})
			assertInvalidAt(t, "Search", err, c.path)
			_, err = w.k.List(testCtx(t), itemservice.ListRequest{Principal: w.user, Filter: c.f})
			assertInvalidAt(t, "List", err, c.path)
		})
	}
	// The second field filter is the one named.
	_, err := w.k.Search(testCtx(t), itemservice.SearchRequest{Principal: w.user, Filter: itemservice.Filter{
		TypeIDs: typ, Fields: []itemservice.FieldFilter{{Field: "f_n", Eq: 1}, {Field: "f_nope", Eq: 1}},
	}})
	assertInvalidAt(t, "second filter", err, "filter.fields[1]")
}

// A filter-only search still shows only what the caller can see.
func TestFilters_OnlyWhatTheCallerSees(t *testing.T) {
	w := newFilterWorld(t)
	bob := in(person("bob"), w.user.Namespace)
	shared := w.make(t, 1, "s_open", nil)
	res, err := w.k.Create(testCtx(t), itemservice.CreateRequest{
		IdempotencyKey: newKey(), Principal: w.user, TypeID: w.typ, TypeVersion: 1,
		Narrow: &itemservice.Narrow{Readers: readers(w.user)},
	})
	if err != nil {
		t.Fatalf("create narrowed: %v", err)
	}
	got, err := w.k.Search(testCtx(t), itemservice.SearchRequest{Principal: bob, Filter: itemservice.Filter{TypeIDs: []string{w.typ}}})
	if err != nil {
		t.Fatalf("Search as bob: %v", err)
	}
	if !sameIDs(idsOf(got.Items), []uuid.UUID{shared}) {
		t.Errorf("bob finds %v, want only %s (not the narrowed %s)", idsOf(got.Items), shared, res.ItemID)
	}
}

// --- paging ---

// pageAll reads every page with the given size and returns the IDs in order
// and the number of pages.
func pageAll(t *testing.T, read func(token string) ([]uuid.UUID, string, error)) ([]uuid.UUID, int) {
	t.Helper()
	var (
		all   []uuid.UUID
		token string
		pages int
	)
	for {
		ids, next, err := read(token)
		if err != nil {
			t.Fatalf("page %d: %v", pages+1, err)
		}
		pages++
		all = append(all, ids...)
		if next == "" {
			return all, pages
		}
		if pages > 100 {
			t.Fatal("more than 100 pages: the token does not advance")
		}
		token = next
	}
}

// Pages cover everything once, newest first, and end with no token.
func TestPaging_SearchAndList(t *testing.T) {
	w := newFilterWorld(t)
	box := w.make(t, 1, "s_open", nil)
	var made []uuid.UUID
	for range 7 {
		res, err := w.k.Create(testCtx(t), itemservice.CreateRequest{
			IdempotencyKey: newKey(), Principal: w.user, TypeID: w.typ, TypeVersion: 1, ContainerID: &box,
		})
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		made = append(made, res.ItemID)
	}
	want := newestFirst(t, made)

	list, pages := pageAll(t, func(token string) ([]uuid.UUID, string, error) {
		res, err := w.k.List(testCtx(t), itemservice.ListRequest{Principal: w.user, ContainerID: &box, Limit: 3, PageToken: token})
		return idsOf(res.Items), res.NextPageToken, err
	})
	if !slices.Equal(list, want) || pages != 3 {
		t.Errorf("List pages: %d pages of %v, want 3 pages of %v", pages, list, want)
	}

	found, pages := pageAll(t, func(token string) ([]uuid.UUID, string, error) {
		res, err := w.k.Search(testCtx(t), itemservice.SearchRequest{
			Principal: w.user, Filter: itemservice.Filter{TypeIDs: []string{w.typ}}, Limit: 3, PageToken: token,
		})
		return idsOf(res.Items), res.NextPageToken, err
	})
	if wantAll := newestFirst(t, append(slices.Clone(made), box)); !slices.Equal(found, wantAll) || pages != 3 {
		t.Errorf("Search pages: %d pages of %v, want 3 pages of %v", pages, found, wantAll)
	}
}

// An Item created between two pages does not shift the next page: no Item
// is shown twice or skipped (keyset paging, not offsets).
func TestPaging_StableUnderInserts(t *testing.T) {
	w := newFilterWorld(t)
	box := w.make(t, 1, "s_open", nil)
	var made []uuid.UUID
	for range 4 {
		res, err := w.k.Create(testCtx(t), itemservice.CreateRequest{
			IdempotencyKey: newKey(), Principal: w.user, TypeID: w.typ, TypeVersion: 1, ContainerID: &box,
		})
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		made = append(made, res.ItemID)
	}
	want := newestFirst(t, made)
	first, err := w.k.List(testCtx(t), itemservice.ListRequest{Principal: w.user, ContainerID: &box, Limit: 2})
	if err != nil {
		t.Fatalf("page 1: %v", err)
	}
	if _, err := w.k.Create(testCtx(t), itemservice.CreateRequest{
		IdempotencyKey: newKey(), Principal: w.user, TypeID: w.typ, TypeVersion: 1, ContainerID: &box,
	}); err != nil {
		t.Fatalf("create between pages: %v", err)
	}
	second, err := w.k.List(testCtx(t), itemservice.ListRequest{
		Principal: w.user, ContainerID: &box, Limit: 2, PageToken: first.NextPageToken,
	})
	if err != nil {
		t.Fatalf("page 2: %v", err)
	}
	if got := append(idsOf(first.Items), idsOf(second.Items)...); !slices.Equal(got, want) {
		t.Errorf("two pages = %v, want %v", got, want)
	}
}

// Fifty by default; more than 500 is read as 500; negative is malformed.
func TestPaging_PageSize(t *testing.T) {
	w := newFilterWorld(t)
	box := w.make(t, 1, "s_open", nil)
	for range 51 {
		if _, err := w.k.Create(testCtx(t), itemservice.CreateRequest{
			IdempotencyKey: newKey(), Principal: w.user, TypeID: w.typ, TypeVersion: 1, ContainerID: &box,
		}); err != nil {
			t.Fatalf("create: %v", err)
		}
	}
	res, err := w.k.List(testCtx(t), itemservice.ListRequest{Principal: w.user, ContainerID: &box})
	if err != nil || len(res.Items) != 50 || res.NextPageToken == "" {
		t.Errorf("default page: %d Items, token %q, %v; want 50 and a token", len(res.Items), res.NextPageToken, err)
	}
	res, err = w.k.List(testCtx(t), itemservice.ListRequest{Principal: w.user, ContainerID: &box, Limit: 10_000})
	if err != nil || len(res.Items) != 51 || res.NextPageToken != "" {
		t.Errorf("page of 10,000: %d Items, token %q, %v; want all 51, read as 500", len(res.Items), res.NextPageToken, err)
	}
	_, err = w.k.List(testCtx(t), itemservice.ListRequest{Principal: w.user, ContainerID: &box, Limit: -1})
	assertInvalidAt(t, "negative page size", err, "page_size")
}

// A page token is bound to its request, caller and namespace; anything else,
// or a token that is not one, is malformed.
func TestPaging_TokenIsBound(t *testing.T) {
	w := newFilterWorld(t)
	box := w.make(t, 1, "s_open", nil)
	for range 3 {
		w.make(t, 1, "s_open", nil)
	}
	first, err := w.k.Search(testCtx(t), itemservice.SearchRequest{
		Principal: w.user, Filter: itemservice.Filter{TypeIDs: []string{w.typ}}, Limit: 1,
	})
	if err != nil || first.NextPageToken == "" {
		t.Fatalf("page 1: %v, token %q", err, first.NextPageToken)
	}
	token := first.NextPageToken
	bob := in(person("bob"), w.user.Namespace)
	elsewhere := in(w.user, newNamespace(t, w.k))

	cases := map[string]func() error{
		"other filter": func() error {
			_, err := w.k.Search(testCtx(t), itemservice.SearchRequest{Principal: w.user,
				Filter: itemservice.Filter{TypeIDs: []string{w.typ}, States: []string{"s_open"}}, Limit: 1, PageToken: token})
			return err
		},
		"other words": func() error {
			_, err := w.k.Search(testCtx(t), itemservice.SearchRequest{Principal: w.user, Query: "x",
				Filter: itemservice.Filter{TypeIDs: []string{w.typ}}, Limit: 1, PageToken: token})
			return err
		},
		"other caller": func() error {
			_, err := w.k.Search(testCtx(t), itemservice.SearchRequest{Principal: bob,
				Filter: itemservice.Filter{TypeIDs: []string{w.typ}}, Limit: 1, PageToken: token})
			return err
		},
		"other namespace": func() error {
			_, err := w.k.Search(testCtx(t), itemservice.SearchRequest{Principal: elsewhere,
				Filter: itemservice.Filter{TypeIDs: []string{w.typ}}, Limit: 1, PageToken: token})
			return err
		},
		"another read": func() error {
			_, err := w.k.List(testCtx(t), itemservice.ListRequest{Principal: w.user, ContainerID: &box, Limit: 1, PageToken: token})
			return err
		},
		"not a token": func() error {
			_, err := w.k.Search(testCtx(t), itemservice.SearchRequest{Principal: w.user,
				Filter: itemservice.Filter{TypeIDs: []string{w.typ}}, Limit: 1, PageToken: "bm90IGEgdG9rZW4"})
			return err
		},
	}
	for name, call := range cases {
		t.Run(name, func(t *testing.T) { assertInvalidAt(t, name, call(), "page_token") })
	}
	// The page size may change between pages (AIP-158).
	if _, err := w.k.Search(testCtx(t), itemservice.SearchRequest{Principal: w.user,
		Filter: itemservice.Filter{TypeIDs: []string{w.typ}}, Limit: 2, PageToken: token}); err != nil {
		t.Errorf("same request, other page size: %v", err)
	}
}
