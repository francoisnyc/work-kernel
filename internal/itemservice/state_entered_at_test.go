// Tests for items.state_entered_at (docs/roadmap.md, track 1, item 5, step
// 3a): when an Item entered the state it is in. The owner's rule:
//
//   - It is the time of the event that put the Item in its state: its
//     create, or its latest transition (the event StateEntry reads), to the
//     microsecond.
//   - A create sets it, and so does every state change, whoever makes it: a
//     person, the timer, a reaction, a hold that resolves and moves the
//     Item, a claim, a release.
//   - Nothing else touches it: a field update, a hold that opens or is
//     denied, a link, a move of container, narrowing, a migrate, a hook's
//     write.
//   - It is written by the statement that writes the state, so in the same
//     transaction as the Item row, its event and its outbox row. (A second
//     UPDATE of the row would make Postgres re-check the row's foreign keys
//     and wait on the container's row lock: docs/design.md, "Writes".)
//   - Items that existed before the column take the time of their latest
//     create or transition event; one with no such event, its create time.
//
// "Oldest waiting first" (waiting_since_test.go) sorts by it.
package itemservice_test

import (
	"context"
	"database/sql"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pressly/goose/v3"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
	"github.com/francoisnyc/work-kernel/migrations"
)

// stateEnteredAt reads the column.
func stateEnteredAt(t *testing.T, id uuid.UUID) time.Time {
	t.Helper()
	var at time.Time
	if err := pool.QueryRow(testCtx(t), `select state_entered_at from items where id = $1`, id).Scan(&at); err != nil {
		t.Fatalf("read state_entered_at of %s: %v", id, err)
	}
	return at
}

// entryEvent is the event that put the Item in its state, as StateEntry
// reads it: its latest create or transition.
func entryEvent(t *testing.T, id uuid.UUID) (op string, at time.Time) {
	t.Helper()
	err := pool.QueryRow(testCtx(t),
		`select op, created_at from item_events where item_id = $1 and op in ('create', 'transition')
		 order by item_seq desc limit 1`, id).Scan(&op, &at)
	if err != nil {
		t.Fatalf("read the entry event of %s: %v", id, err)
	}
	return op, at
}

// assertEntered: the column is the time of the Item's entry event, which
// must be of kind wantOp. It returns that time.
func assertEntered(t *testing.T, what string, id uuid.UUID, wantOp string) time.Time {
	t.Helper()
	op, at := entryEvent(t, id)
	got := stateEnteredAt(t, id)
	if op != wantOp {
		t.Errorf("%s: the Item's entry event is a %s, want a %s", what, op, wantOp)
	}
	if !got.Equal(at) {
		t.Errorf("%s: state_entered_at = %s, want the time of its %s event, %s", what,
			got.Format(time.RFC3339Nano), op, at.Format(time.RFC3339Nano))
	}
	return got
}

// assertMoved: a state change. The column is the new transition's time,
// and later than it was.
func assertMoved(t *testing.T, what string, id uuid.UUID, before time.Time) time.Time {
	t.Helper()
	got := assertEntered(t, what, id, "transition")
	if !got.After(before) {
		t.Errorf("%s: state_entered_at = %s, want later than before the move (%s)", what,
			got.Format(time.RFC3339Nano), before.Format(time.RFC3339Nano))
	}
	return got
}

// assertUntouched: a write that is no state change. The Item's item_seq
// grew (the write happened) and the column did not move.
func assertUntouched(t *testing.T, what string, id uuid.UUID, before time.Time, seqBefore int64) {
	t.Helper()
	if seq := itemSeqOf(t, id); seq <= seqBefore {
		t.Fatalf("%s: item_seq is still %d: the write did not happen, the test proves nothing", what, seq)
	}
	if got := stateEnteredAt(t, id); !got.Equal(before) {
		t.Errorf("%s: state_entered_at moved from %s to %s; only a state change may set it", what,
			before.Format(time.RFC3339Nano), got.Format(time.RFC3339Nano))
	}
}

// A create sets it: a person's, and a reaction's.
func TestStateEnteredAt_ACreateSetsIt(t *testing.T) {
	w := newClaimWorld(t)
	id := w.create(t, itemservice.CreateRequest{
		Principal: w.ana, TypeID: "cl_purchase", TypeVersion: 1,
		Fields: map[string]any{"f_amount": 2500, "f_manager": w.bob.Actor},
	})
	at := assertEntered(t, "a purchase ana created", id, "create")
	var created time.Time
	if err := pool.QueryRow(testCtx(t), `select created_at from items where id = $1`, id).Scan(&created); err != nil {
		t.Fatal(err)
	}
	if !at.Equal(created) {
		t.Errorf("a new Item: state_entered_at = %s, created_at = %s; want the same instant", at, created)
	}

	// An Item a reaction creates (the job's start reactions make two).
	r := newR3World(t)
	_, main, reply := r.started(t, 600)
	assertEntered(t, "an Item a create reaction made", main, "create")
	assertEntered(t, "a second Item a create reaction made", reply, "create")
}

// Every state change sets it, whoever makes the move.
func TestStateEnteredAt_EveryStateChangeSetsIt(t *testing.T) {
	w := newClaimWorld(t)

	t.Run("a person's moves, a claim and a release", func(t *testing.T) {
		id := w.create(t, itemservice.CreateRequest{
			Principal: w.ana, TypeID: "cl_purchase", TypeVersion: 1,
			Fields: map[string]any{"f_amount": 2500, "f_manager": w.bob.Actor},
		})
		at := assertEntered(t, "created", id, "create")
		w.move(t, w.ana, id, "manager_review", nil)
		at = assertMoved(t, "ana sends it to her manager", id, at)
		w.move(t, w.bob, id, "finance_queue", nil)
		at = assertMoved(t, "bob sends it to the finance queue", id, at)
		w.move(t, w.gus, id, "finance_review", nil)
		at = assertMoved(t, "gus claims it", id, at)
		w.move(t, w.fay, id, "finance_queue", nil)
		at = assertMoved(t, "fay releases it", id, at)
		w.move(t, w.fay, id, "finance_review", nil)
		at = assertMoved(t, "fay claims it", id, at)
		w.move(t, w.fay, id, "approved", nil)
		assertMoved(t, "fay approves it", id, at)
	})

	t.Run("the timer's move", func(t *testing.T) {
		id := w.claimed(t, w.gus)
		at := assertEntered(t, "claimed", id, "transition")
		_, err := w.k.Internal().Transition(testCtx(t), itemservice.TransitionRequest{
			IdempotencyKey: newKey(), Principal: item.Principal{Actor: "system:timer", Namespace: w.ns}, ItemID: id, ToState: "escalated",
			Attribution: item.Attribution{
				OriginatingHook: "timer:cl_purchase@v1:finance_review",
				CausationID:     fmt.Sprintf("%s:%d", id, itemSeqOf(t, id)),
				Hop:             1,
			},
		})
		if err != nil {
			t.Fatalf("the timer's move: %v", err)
		}
		assertMoved(t, "the timer escalates it", id, at)
	})

	t.Run("a held move, when its check allows it", func(t *testing.T) {
		id := w.create(t, itemservice.CreateRequest{
			Principal: w.ana, TypeID: "wq_held", TypeVersion: 1,
			Fields: map[string]any{"f_customer_id": "cust-1", "f_amount": 100},
		})
		at := assertEntered(t, "created", id, "create")
		res := w.askHeld(t, id)
		// Asking opens a hold and moves nothing.
		assertUntouched(t, "the hold opens", id, at, 1)
		w.resolveHeld(t, id, res, itemservice.VerdictAllow)
		if it, err := w.k.Get(testCtx(t), w.fay, id); err != nil || it.State != "granted" {
			t.Fatalf("after the check allows: state %q, error %v; want granted", it.State, err)
		}
		assertMoved(t, "the check allows, and the Item moves", id, at)
	})

	t.Run("a reaction's move", func(t *testing.T) {
		r := newR3World(t)
		job := r.job(t, 600)
		var tags []uuid.UUID
		var before []time.Time
		for range 3 {
			tag := r.create(t, r.ana, "r3tag", 1, &job, nil)
			tags = append(tags, tag)
			before = append(before, assertEntered(t, "a tag, created", tag, "create"))
		}
		r.move(t, r.k, r.ana, job, "working")
		done := r.move(t, r.k, r.ana, job, "done")
		if out := r.reactMove(t, r.k, entered(job, done, "sweep")); out.Outcome != itemservice.ReactionApplied {
			t.Fatalf("the sweep reaction: %+v, want applied", out)
		}
		for i, tag := range tags {
			assertMoved(t, fmt.Sprintf("tag %d, moved by the job's reaction", i), tag, before[i])
		}
	})
}

// askHeld: fay asks for wq_held's checked move, which opens a hold.
func (w *waitWorld) askHeld(t *testing.T, id uuid.UUID) itemservice.TransitionResult {
	t.Helper()
	res, err := w.k.Transition(testCtx(t), itemservice.TransitionRequest{
		IdempotencyKey: newKey(), Principal: w.fay, ItemID: id, ToState: "granted",
	})
	if err != nil || res.HoldID == uuid.Nil {
		t.Fatalf("fay asks for the checked move: hold %v, error %v; want an open hold", res.HoldID, err)
	}
	return res
}

// resolveHeld answers the hold as wq_held's check would.
func (w *waitWorld) resolveHeld(t *testing.T, id uuid.UUID, asked itemservice.TransitionResult, v itemservice.Verdict) {
	t.Helper()
	req := itemservice.ResolveHoldRequest{
		IdempotencyKey: "hold:" + asked.HoldID.String(), Principal: item.Principal{Actor: "hook:wq_held:credit", Namespace: w.ns},
		ItemID: id, HoldID: asked.HoldID, Verdict: v,
		Attribution: item.Attribution{
			OriginatingHook: "hook:wq_held@v1:credit", CausationID: fmt.Sprintf("%s:%d", id, asked.ItemSeq), Hop: 1,
		},
	}
	if v == itemservice.VerdictDeny {
		req.Reason = "credit_hold"
	}
	if _, err := w.k.Internal().ResolveHold(testCtx(t), req); err != nil {
		t.Fatalf("the check answers %s: %v", v, err)
	}
}

// What is no state change does not touch it.
func TestStateEnteredAt_WhatDoesNotTouchIt(t *testing.T) {
	w := newClaimWorld(t)

	t.Run("an update of a field", func(t *testing.T) {
		id := w.queued(t, "cl_purchase")
		at, seq := stateEnteredAt(t, id), itemSeqOf(t, id)
		if _, err := w.k.Update(testCtx(t), itemservice.UpdateRequest{
			IdempotencyKey: newKey(), Principal: w.ana, ItemID: id, Fields: map[string]any{"f_reason": "for the design team"},
		}); err != nil {
			t.Fatalf("ana updates a field: %v", err)
		}
		assertUntouched(t, "a field update", id, at, seq)
	})

	t.Run("the claim field, written in the queue", func(t *testing.T) {
		id := w.queued(t, "cl_purchase")
		at, seq := stateEnteredAt(t, id), itemSeqOf(t, id)
		if _, err := w.k.Update(testCtx(t), itemservice.UpdateRequest{
			IdempotencyKey: newKey(), Principal: w.fay, ItemID: id, Fields: map[string]any{"f_claimed_by": w.fay.Actor},
		}); err != nil {
			t.Fatalf("fay writes the claim field without moving the purchase: %v", err)
		}
		assertUntouched(t, "the claim field written by an update", id, at, seq)
	})

	t.Run("a hold that opens, and a check that denies", func(t *testing.T) {
		id := w.create(t, itemservice.CreateRequest{
			Principal: w.ana, TypeID: "wq_held", TypeVersion: 1,
			Fields: map[string]any{"f_customer_id": "cust-1", "f_amount": 100},
		})
		at, seq := stateEnteredAt(t, id), itemSeqOf(t, id)
		res := w.askHeld(t, id)
		assertUntouched(t, "the hold opens", id, at, seq)
		seq = itemSeqOf(t, id)
		w.resolveHeld(t, id, res, itemservice.VerdictDeny)
		assertUntouched(t, "the check denies: the Item stays where it was", id, at, seq)
	})

	t.Run("a move of container, and narrowing", func(t *testing.T) {
		desk := w.create(t, itemservice.CreateRequest{
			Principal: w.ana, TypeID: "wq_desk", TypeVersion: 1, Fields: map[string]any{"f_name": "Purchasing"},
		})
		id := w.queued(t, "cl_purchase")
		at, seq := stateEnteredAt(t, id), itemSeqOf(t, id)
		if _, err := w.k.Update(testCtx(t), itemservice.UpdateRequest{
			IdempotencyKey: newKey(), Principal: w.ana, ItemID: id, Move: &itemservice.Move{To: &desk},
		}); err != nil {
			t.Fatalf("ana moves it into the desk: %v", err)
		}
		assertUntouched(t, "a move of container", id, at, seq)
		seq = itemSeqOf(t, id)
		if _, err := w.k.Update(testCtx(t), itemservice.UpdateRequest{
			IdempotencyKey: newKey(), Principal: w.ana, ItemID: id,
			Policy: &itemservice.PolicyPatch{Readers: readers(w.ana, w.bob, w.fay, w.gus)},
		}); err != nil {
			t.Fatalf("ana narrows it: %v", err)
		}
		assertUntouched(t, "narrowing", id, at, seq)
	})

	t.Run("a link", func(t *testing.T) {
		r := newR3World(t)
		yard := r.create(t, r.admin, "r3yard", 1, nil, map[string]any{"f_name": "yard " + word()})
		at, seq := stateEnteredAt(t, yard), itemSeqOf(t, yard)
		r.link(t, r.admin, yard, "hours", r.hours)
		assertUntouched(t, "a link, on its source", yard, at, seq)
	})

	t.Run("a migrate", func(t *testing.T) {
		m := newMigWorld(t)
		m.reg(t, migPurchaseSrc(1, ""))
		id := m.inFlight(t)
		m.reg(t, migPurchaseSrc(2, migAllowed))
		deliverAll(t, id)
		at, seq := stateEnteredAt(t, id), itemSeqOf(t, id)
		if _, err := m.migrate(t, m.adm, id, m.id("pur"), 2); err != nil {
			t.Fatalf("adm migrates the purchase: %v", err)
		}
		if v := versionOf(t, id); v != 2 {
			t.Fatalf("the purchase is on version %d, want 2", v)
		}
		assertUntouched(t, "a migrate", id, at, seq)
	})

	t.Run("the kernel's record of a reaction, on the Item that caused it", func(t *testing.T) {
		r := newR3World(t)
		job := r.job(t, 600)
		at, seq := stateEnteredAt(t, job), itemSeqOf(t, job)
		if out := r.reactCreate(t, r.k, entered(job, 1, "start")); out.Outcome != itemservice.ReactionApplied {
			t.Fatalf("the job's start reaction: %+v, want applied", out)
		}
		assertUntouched(t, "a reaction's record", job, at, seq)
	})
}

// --- the statement that writes the state writes the column ---

var (
	// An UPDATE of items that sets its state. (Not type_version's "state"
	// in another word, nor item_holds' own from_state and to_state.)
	updatesState = regexp.MustCompile(`^update items set (.*, ?)?state\s*=`)
	// An UPDATE of items that sets state_entered_at, before its WHERE.
	updatesEnteredAt = regexp.MustCompile(`^update items set [^;]*?\bstate_entered_at\s*=.*\bwhere\b`)
	// An INSERT into items, and one that names the column.
	insertsItem      = regexp.MustCompile(`^insert into items \(`)
	insertsEnteredAt = regexp.MustCompile(`^insert into items \([^)]*\bstate_entered_at\b`)
)

// stateWrites returns, for every string literal in src that is a statement
// creating an Item or writing its state or state_entered_at, what is wrong
// with it ("" when nothing is).
func stateWrites(t *testing.T, fset *token.FileSet, path string, src any) map[string]string {
	t.Helper()
	f, err := parser.ParseFile(fset, path, src, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	found := map[string]string{}
	ast.Inspect(f, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		text, err := strconv.Unquote(lit.Value)
		if err != nil {
			return true
		}
		q := strings.ToLower(strings.Join(strings.Fields(text), " "))
		at := fset.Position(lit.Pos()).String()
		state, entered := updatesState.MatchString(q), updatesEnteredAt.MatchString(q)
		switch {
		case insertsItem.MatchString(q) && insertsEnteredAt.MatchString(q):
			found[at] = ""
		case insertsItem.MatchString(q):
			found[at] = "this statement creates an Item and does not set state_entered_at"
		case state && entered:
			found[at] = ""
		case state:
			found[at] = "this statement writes items.state and not state_entered_at: set it in the same statement"
		case entered:
			found[at] = "this statement writes items.state_entered_at and not the state: only a state change may set it"
		}
		return true
	})
	return found
}

// No write path may forget the column, and none may set it on its own.
// Like the etag's test (content_statements_test.go), this reads the
// module's source: every statement that creates an Item or writes its
// state writes state_entered_at too, and no other statement does.
func TestStateEnteredAt_TheStatementThatWritesTheStateWritesIt(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	statements := 0
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); path != root && (strings.HasPrefix(name, ".") || name == "vendor" || name == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		// Tests seed rows by hand, some of them before this migration.
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		for at, wrong := range stateWrites(t, fset, path, nil) {
			statements++
			if wrong != "" {
				rel, _ := filepath.Rel(root, at)
				t.Errorf("%s: %s", rel, wrong)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Two UPDATEs of the state (a transition, a hold that resolves) and
	// three INSERTs (a create, a reaction's create, the bench's loader)
	// today. Fewer means the patterns stopped recognising them.
	if statements < 5 {
		t.Errorf("found %d statements that create an Item or write its state, want at least 5: the test no longer sees them", statements)
	}
}

// The test's own eyes.
func TestStateEnteredAt_TheStatementCheck_SeesAForgottenColumn(t *testing.T) {
	const src = "package x\n" +
		"var bad1 = `update items set state = $2, fields = $3, item_seq = $4 where id = $1`\n" +
		"var bad2 = `update items set fields = $3,\n\t\tstate = $2 where id = $1 and state_entered_at < now()`\n" +
		"var bad3 = `insert into items (id, state, item_seq, created_at) values ($1, $2, $3, $4)`\n" +
		"var bad4 = `update items set state_entered_at = now() where id = $1`\n" +
		"var good1 = `update items set state = $2, item_seq = $3,\n\t\tstate_entered_at = $4 where id = $1`\n" +
		"var good2 = `insert into items (id, state, item_seq,\n\t\tstate_entered_at) values ($1, $2, $3, $4)`\n" +
		"var other1 = `select state, state_entered_at from items where id = $1`\n" +
		"var other2 = `update items set fields = $2, item_seq = $3 where id = $1 and state = 'open'`\n" +
		"var other3 = `update item_holds set status = $2 where id = $1`\n" +
		"var other4 = `update items set type_version = $2, item_seq = $3 where id = $1`\n"
	got := stateWrites(t, token.NewFileSet(), "x.go", src)
	want := map[string]bool{"x.go:2:12": false, "x.go:3:12": false, "x.go:5:12": false, "x.go:6:12": false,
		"x.go:7:13": true, "x.go:9:13": true}
	if len(got) != len(want) {
		t.Fatalf("saw %v, want the six statements %v", got, want)
	}
	for at, ok := range want {
		if wrong, has := got[at]; !has || (wrong == "") != ok {
			t.Errorf("%s: found %v, judged %q; want ok = %v", at, has, wrong, ok)
		}
	}
}

// --- the migration's backfill ---

// Items that existed before the column take the time of their latest
// create or transition event, whatever was written after it; an Item with
// no such event takes its create time. After the migration the column has
// no default and takes no null.
func TestStateEnteredAt_TheMigrationBackfillsFromTheEvents(t *testing.T) {
	ctx := testCtx(t)
	dbName := "entered_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
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
	const lastBefore = 13 // 00013_customers.sql
	if _, err := provider.UpTo(ctx, lastBefore); err != nil {
		t.Fatalf("migrate to %d: %v", lastBefore, err)
	}

	day := func(n int) time.Time { return time.Date(2026, 11, n, 9, 0, 0, 123456000, time.UTC) }
	type ev struct {
		op string
		at time.Time
	}
	cases := []struct {
		name    string
		created time.Time
		events  []ev
		want    time.Time
	}{
		{"created, never moved", day(1), []ev{{"create", day(1)}}, day(1)},
		{"created, then updated", day(1), []ev{{"create", day(1)}, {"update", day(5)}}, day(1)},
		{"moved once", day(1), []ev{{"create", day(1)}, {"transition", day(3)}}, day(3)},
		{"moved twice, the second time back", day(1), []ev{{"create", day(1)}, {"transition", day(3)}, {"transition", day(4)}}, day(4)},
		{"moved, then updated, linked, migrated and written by a hook", day(1), []ev{{"create", day(1)}, {"transition", day(3)},
			{"update", day(5)}, {"link_added", day(6)}, {"migrated", day(7)}, {"hook_ran", day(8)}}, day(3)},
		{"a hold asked, then denied", day(2), []ev{{"create", day(2)}, {"transition_requested", day(4)}, {"transition_denied", day(5)}}, day(2)},
		{"a hold asked, then allowed", day(2), []ev{{"create", day(2)}, {"transition_requested", day(4)}, {"transition", day(6)}}, day(6)},
		{"no event at all", day(9), nil, day(9)},
	}
	ids := make([]uuid.UUID, len(cases))
	for i, c := range cases {
		ids[i] = uuid.New()
		if _, err := db.ExecContext(ctx,
			`insert into items (id, namespace_id, type_id, type_version, scope_id, state, item_seq, created_at, created_by)
			 select $1, 'default', 'ticket', 1, id, 'open', $2, $3, 'user:ana'
			 from scopes where namespace_id = 'default' and parent_id is null`, ids[i], len(c.events), c.created); err != nil {
			t.Fatalf("seed %q: %v", c.name, err)
		}
		for seq, e := range c.events {
			if _, err := db.ExecContext(ctx,
				`insert into item_events (item_id, item_seq, actor, op, created_at) values ($1, $2, 'user:ana', $3, $4)`,
				ids[i], seq+1, e.op, e.at); err != nil {
				t.Fatalf("seed %q, event %d: %v", c.name, seq+1, err)
			}
		}
	}
	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("migrate to the end: %v", err)
	}
	for i, c := range cases {
		var got time.Time
		if err := db.QueryRowContext(ctx, `select state_entered_at from items where id = $1`, ids[i]).Scan(&got); err != nil {
			t.Fatalf("%s: read: %v", c.name, err)
		}
		if !got.Equal(c.want) {
			t.Errorf("%s: backfilled state_entered_at = %s, want %s", c.name, got.Format(time.RFC3339Nano), c.want.Format(time.RFC3339Nano))
		}
	}
	// No null, and no default that would hide a write path that forgets it.
	var nullable string
	var def sql.NullString
	if err := db.QueryRowContext(ctx,
		`select is_nullable, column_default from information_schema.columns
		 where table_name = 'items' and column_name = 'state_entered_at'`).Scan(&nullable, &def); err != nil {
		t.Fatalf("read the column: %v", err)
	}
	if nullable != "NO" || def.Valid {
		t.Errorf("items.state_entered_at: nullable %s, default %q; want NOT NULL and no default", nullable, def.String)
	}
	// Down, then up again: the column goes and comes back the same.
	if _, err := provider.Down(ctx); err != nil {
		t.Fatalf("migrate down: %v", err)
	}
	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("migrate up again: %v", err)
	}
	var got time.Time
	if err := db.QueryRowContext(ctx, `select state_entered_at from items where id = $1`, ids[2]).Scan(&got); err != nil || !got.Equal(day(3)) {
		t.Errorf("after down and up: state_entered_at = %s, error %v; want %s", got, err, day(3))
	}
}
