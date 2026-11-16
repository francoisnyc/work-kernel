// Tests for proof of concept Step 1: guards that read links, and the locks
// that keep them true.
//
// A guard reads linked Items regardless of the caller's visibility (a rule
// must hold for everyone), but only their state and category, and a refusal
// never names them. It is checked on all three doors: the public
// Transition, the internal door (a timer), and a hold's re-check when it is
// resolved.
//
// Locking: a transition whose guard reads links reads the Item and its link
// set unlocked, then locks the Item FOR UPDATE and the linked rows FOR SHARE
// together in UUID order, re-reads the link set, and starts again
// (errRetry) if it changed. So a linked Item cannot change state between
// the guard and the commit, and nothing deadlocks. write() also retries a
// Postgres deadlock (40P01) or serialization failure (40001).
package itemservice_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
)

// --- test hook plumbing ---

// marker tags a context, so the test hook acts on one call only.
type marker struct{}

func marked(ctx context.Context, name string) context.Context {
	return context.WithValue(ctx, marker{}, name)
}

func markOf(ctx context.Context) string {
	s, _ := ctx.Value(marker{}).(string)
	return s
}

// hookCalls counts the hook's calls per (mark, point).
type hookCalls struct {
	mu sync.Mutex
	n  map[string]int
}

func (h *hookCalls) add(mark, point string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.n == nil {
		h.n = map[string]int{}
	}
	h.n[mark+"/"+point]++
	return h.n[mark+"/"+point]
}

func (h *hookCalls) get(mark, point string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.n[mark+"/"+point]
}

// eventCommitSeq reads the commit_seq of an Item's latest event.
func eventCommitSeq(t *testing.T, id uuid.UUID) int64 {
	t.Helper()
	var seq int64
	err := pool.QueryRow(testCtx(t),
		`select commit_seq from item_events where item_id = $1 order by item_seq desc limit 1`, id).Scan(&seq)
	if err != nil {
		t.Fatalf("read commit_seq: %v", err)
	}
	return seq
}

func stateOf(t *testing.T, id uuid.UUID) string {
	t.Helper()
	var s string
	if err := pool.QueryRow(testCtx(t), `select state from items where id = $1`, id).Scan(&s); err != nil {
		t.Fatalf("read state: %v", err)
	}
	return s
}

// --- the guard on each door ---

// The public door: Y cannot close while X blocks it and X is not done or
// canceled.
func TestLinkGuard_BlockedItemCannotClose(t *testing.T) {
	w := newLinkWorld(t)
	x := w.make(t, w.ana, "s_work", itemOpts{})
	y := w.make(t, w.ana, "s_work", itemOpts{})
	gone := w.make(t, w.ana, "s_canceled", itemOpts{})
	w.mustLink(t, w.ana, x, "blocks", y)
	w.mustLink(t, w.ana, gone, "blocks", y) // a canceled blocker does not block

	_, err := w.moveItem(testCtx(t), w.ana, y, "s_done")
	assertRefusedMeta(t, "close while blocked", err, itemservice.CodeGuardFailed,
		map[string]string{"from_state": "s_work", "to_state": "s_done"})
	if s := stateOf(t, y); s != "s_work" {
		t.Errorf("y is %s after the refusal, want s_work", s)
	}

	w.mustMove(t, w.ana, x, "s_done")
	w.mustMove(t, w.ana, y, "s_done")

	// What y blocks does not matter to y's own guard.
	z := w.make(t, w.ana, "s_work", itemOpts{})
	w.mustLink(t, w.ana, z, "blocks", w.make(t, w.ana, "s_work", itemOpts{}))
	w.mustMove(t, w.ana, z, "s_done")
}

// A blocker the caller cannot see still blocks; the refusal names nothing
// about it.
func TestLinkGuard_HiddenBlockerStillBlocks(t *testing.T) {
	w := newLinkWorld(t)
	secret := w.make(t, w.ana, "s_work", itemOpts{narrow: readers(w.ana)})
	y := w.make(t, w.ana, "s_work", itemOpts{})
	w.mustLink(t, w.ana, secret, "blocks", y)

	_, err := w.moveItem(testCtx(t), w.bob, y, "s_done")
	assertRejectedCode(t, "bob closes y", err, itemservice.CodeGuardFailed)
	var rej *itemservice.RejectedError
	if errors.As(err, &rej) {
		if strings.Contains(rej.Error(), secret.String()) {
			t.Errorf("refusal %q names the hidden blocker", rej)
		}
		for k, v := range rej.Metadata {
			if strings.Contains(v, secret.String()) {
				t.Errorf("metadata %s = %q names the hidden blocker", k, v)
			}
		}
	}
	// bob still cannot see it, and y's links do not show it to him.
	assertHidden(t, w.k, w.bob, secret)
	got, _ := w.linksOf(t, w.bob, y)
	assertLinks(t, "y as bob", got)
}

// The internal door: a timer's move is refused by the same guard.
func TestLinkGuard_TimerDoor(t *testing.T) {
	w := newLinkWorld(t)
	x := w.make(t, w.ana, "s_work", itemOpts{})
	y := w.make(t, w.ana, "s_work", itemOpts{}) // event 2 entered s_work
	w.mustLink(t, w.ana, x, "blocks", y)
	timerMove := func() error {
		_, err := w.k.Internal().Transition(testCtx(t), itemservice.TransitionRequest{
			IdempotencyKey: newKey(), Principal: item.Principal{Actor: "system:timer"}, ItemID: y, ToState: "s_done",
			Attribution: item.Attribution{
				OriginatingHook: fmt.Sprintf("timer:%s@v1:s_work", w.typ),
				CausationID:     fmt.Sprintf("%s:2", y), Hop: 1,
			},
		})
		return err
	}
	assertRejectedCode(t, "the timer closes y while x blocks it", timerMove(), itemservice.CodeGuardFailed)
	w.mustMove(t, w.ana, x, "s_done")
	if err := timerMove(); err != nil {
		t.Errorf("the timer closes y once x is done: %v", err)
	}
}

// The hold's re-check: a guard that held when the hold opened is checked
// again when the check allows it, with the links as they are then.
func TestLinkGuard_HoldReCheck(t *testing.T) {
	w := newLinkWorld(t)
	f := map[string]any{"f_customer_id": "c1", "f_amount": 10}
	y := w.make(t, w.ana, "s_work", itemOpts{fields: f})

	// Asked while blocked: refused at once, no hold.
	x := w.make(t, w.ana, "s_work", itemOpts{})
	w.mustLink(t, w.ana, x, "blocks", y)
	_, err := w.moveItem(testCtx(t), w.ana, y, "s_check")
	assertRejectedCode(t, "ask for the check while blocked", err, itemservice.CodeGuardFailed)
	if _, err := w.unlink(t, w.ana, x, "blocks", y); err != nil {
		t.Fatalf("Unlink: %v", err)
	}

	// Not blocked: the hold opens. Then a blocker appears; allow is denied.
	res := w.mustMove(t, w.ana, y, "s_check")
	if res.Outcome != itemservice.OutcomePending {
		t.Fatalf("ask for the check = %+v, want pending", res)
	}
	w.mustLink(t, w.ana, x, "blocks", y)
	check := item.Principal{Actor: "hook:" + w.typ + ":erp_check"}
	_, err = w.k.Internal().ResolveHold(testCtx(t), itemservice.ResolveHoldRequest{
		IdempotencyKey: "hold:" + res.HoldID.String(), Principal: check, ItemID: y, HoldID: res.HoldID,
		Verdict: itemservice.VerdictAllow,
		Attribution: item.Attribution{
			OriginatingHook: fmt.Sprintf("hook:%s@v1:erp_check", w.typ),
			CausationID:     fmt.Sprintf("%s:%d", y, res.ItemSeq), Hop: 1,
		},
	})
	if err != nil {
		t.Fatalf("ResolveHold: %v", err)
	}
	status, hold := holdOf(t, w.k, w.ana, y)
	if status != "s_work" || hold == nil || hold.Status != item.HoldDenied || hold.Reason != "guard_failed" {
		t.Errorf("after allow with a new blocker: state %s, hold %+v; want s_work, denied guard_failed", status, hold)
	}
}

// --- the race the locks prevent ---

// Deterministic: T2 closes Y (blocked by X, which is done) and stops, with
// its guard checked and its locks held. T1 reopens X: it must wait for T2
// to commit (X is locked FOR SHARE), so Y is never closed while X is open.
func TestLinkGuard_ReopenWaitsForTheClose(t *testing.T) {
	w := newLinkWorld(t)
	x := w.make(t, w.ana, "s_done", itemOpts{})
	y := w.make(t, w.ana, "s_work", itemOpts{})
	w.mustLink(t, w.ana, x, "blocks", y)

	held, release := make(chan struct{}), make(chan struct{})
	defer itemservice.SetTestHook(func(ctx context.Context, tx pgx.Tx, point string) error {
		if markOf(ctx) == "T2" && point == "link_guard_held" {
			close(held)
			<-release
		}
		return nil
	})()

	t2 := make(chan error, 1)
	go func() {
		_, err := w.moveItem(marked(testCtx(t), "T2"), w.ana, y, "s_done")
		t2 <- err
	}()
	select {
	case <-held:
	case <-time.After(10 * time.Second):
		t.Fatal("T2 never reached its held guard")
	}
	t1 := make(chan error, 1)
	go func() {
		_, err := w.moveItem(testCtx(t), w.ana, x, "s_work")
		t1 <- err
	}()
	select {
	case err := <-t1:
		t.Fatalf("T1 reopened X while T2 held its guard (err %v): X must be locked FOR SHARE", err)
	case <-time.After(300 * time.Millisecond):
	}
	close(release)
	if err := <-t2; err != nil {
		t.Fatalf("T2 close: %v", err)
	}
	if err := <-t1; err != nil {
		t.Fatalf("T1 reopen: %v", err)
	}
	if c1, c2 := eventCommitSeq(t, x), eventCommitSeq(t, y); c1 <= c2 {
		t.Errorf("reopen commit_seq %d <= close commit_seq %d: the reopen was written before the close committed", c1, c2)
	}
}

// Under many concurrent runs: T1 reopens X while T2 closes Y. Y never
// closes while X is open: either T2 closes first and T1's write comes after
// (a higher commit_seq, taken under X's lock), or T1 reopens first and T2
// is refused. No other error, and no deadlock reaches the caller.
func TestLinkGuard_ReopenRace_ManyRuns(t *testing.T) {
	w := newLinkWorld(t)
	const runs = 60
	var closedFirst, refused int
	for i := range runs {
		x := w.make(t, w.ana, "s_done", itemOpts{})
		y := w.make(t, w.ana, "s_work", itemOpts{})
		w.mustLink(t, w.ana, x, "blocks", y)

		start := make(chan struct{})
		var wg sync.WaitGroup
		var err1, err2 error
		wg.Add(2)
		go func() { defer wg.Done(); <-start; _, err1 = w.moveItem(context.Background(), w.ana, x, "s_work") }()
		go func() { defer wg.Done(); <-start; _, err2 = w.moveItem(context.Background(), w.ana, y, "s_done") }()
		close(start)
		wg.Wait()

		if err1 != nil {
			t.Fatalf("run %d: T1 reopen: %v", i, err1)
		}
		switch {
		case err2 == nil:
			closedFirst++
			if c1, c2 := eventCommitSeq(t, x), eventCommitSeq(t, y); c1 <= c2 {
				t.Errorf("run %d: Y closed (commit_seq %d) after X reopened (commit_seq %d)", i, c2, c1)
			}
		default:
			var rej *itemservice.RejectedError
			if !errors.As(err2, &rej) || rej.Code != itemservice.CodeGuardFailed {
				t.Fatalf("run %d: T2 close: %v, want nil or guard_failed", i, err2)
			}
			refused++
			if s := stateOf(t, y); s != "s_work" {
				t.Errorf("run %d: refused, but y is %s", i, s)
			}
		}
	}
	t.Logf("%d runs: %d closed before the reopen, %d refused after it", runs, closedFirst, refused)
}

// A link added between the unlocked read and the locks is seen: the
// re-read differs, the transaction starts again, and the new blocker
// refuses the close.
func TestLinkGuard_LinkAddedBeforeTheLocksIsSeen(t *testing.T) {
	w := newLinkWorld(t)
	y := w.make(t, w.ana, "s_work", itemOpts{})
	x := w.make(t, w.ana, "s_work", itemOpts{})

	var calls hookCalls
	defer itemservice.SetTestHook(func(ctx context.Context, tx pgx.Tx, point string) error {
		if markOf(ctx) != "T2" || point != "links_read" {
			return nil
		}
		if calls.add("T2", point) == 1 {
			// Another writer links a blocker before T2 takes its locks.
			if _, err := w.k.Link(context.Background(), w.req(w.ana, x, "blocks", y)); err != nil {
				return fmt.Errorf("link the blocker: %w", err)
			}
		}
		return nil
	})()

	_, err := w.moveItem(marked(testCtx(t), "T2"), w.ana, y, "s_done")
	assertRejectedCode(t, "close with a blocker linked meanwhile", err, itemservice.CodeGuardFailed)
	if n := calls.get("T2", "links_read"); n != 2 {
		t.Errorf("links read %d times, want 2 (the first attempt saw the link set change)", n)
	}
	if s := stateOf(t, y); s != "s_work" {
		t.Errorf("y is %s, want s_work", s)
	}
}

// Two Items that block each other, closed at once, many times: each locks
// itself FOR UPDATE and the other FOR SHARE, in UUID order, so neither
// waits on the other in a cycle. Both are refused; nothing deadlocks.
func TestLinkGuard_MutualBlockersDoNotDeadlock(t *testing.T) {
	w := newLinkWorld(t)
	for i := range 30 {
		a := w.make(t, w.ana, "s_work", itemOpts{})
		b := w.make(t, w.ana, "s_work", itemOpts{})
		w.mustLink(t, w.ana, a, "blocks", b)
		w.mustLink(t, w.ana, b, "blocks", a)
		var wg sync.WaitGroup
		errs := make([]error, 2)
		start := make(chan struct{})
		for j, id := range []uuid.UUID{a, b} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				_, errs[j] = w.moveItem(context.Background(), w.ana, id, "s_done")
			}()
		}
		close(start)
		wg.Wait()
		for j, err := range errs {
			var rej *itemservice.RejectedError
			if !errors.As(err, &rej) || rej.Code != itemservice.CodeGuardFailed {
				t.Fatalf("run %d, item %d: error = %v, want guard_failed", i, j, err)
			}
		}
	}
}

// --- the deadlock backstop ---

// Two writes that lock two rows in opposite orders deadlock; Postgres
// aborts one (40P01). write() runs that one again, which then succeeds:
// both writes land, with exactly one retry.
func TestWrite_RetriesADeadlock(t *testing.T) {
	w := newLinkWorld(t)
	a, b := w.open(t), w.open(t)
	order := map[string][2]uuid.UUID{"one": {a, b}, "two": {b, a}}

	var calls hookCalls
	var firstLocks sync.WaitGroup
	firstLocks.Add(2)
	var deadlocks atomic.Int32
	defer itemservice.SetTestHook(func(ctx context.Context, tx pgx.Tx, point string) error {
		mark := markOf(ctx)
		if point != "write" || (mark != "one" && mark != "two") {
			return nil
		}
		if calls.add(mark, point) > 1 {
			return nil // a retry: no more games
		}
		rows := order[mark]
		if _, err := tx.Exec(ctx, `select 1 from items where id = $1 for update`, rows[0]); err != nil {
			return err
		}
		firstLocks.Done()
		firstLocks.Wait()
		_, err := tx.Exec(ctx, `select 1 from items where id = $1 for update`, rows[1])
		if err != nil && strings.Contains(err.Error(), "40P01") {
			deadlocks.Add(1)
		}
		return err
	})()

	var wg sync.WaitGroup
	errs := map[string]error{}
	var mu sync.Mutex
	for mark, id := range map[string]uuid.UUID{"one": a, "two": b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := w.k.Update(marked(testCtx(t), mark), itemservice.UpdateRequest{
				IdempotencyKey: newKey(), Principal: w.ana, ItemID: id, Fields: map[string]any{"f_title": mark},
			})
			mu.Lock()
			errs[mark] = err
			mu.Unlock()
		}()
	}
	wg.Wait()
	for mark, err := range errs {
		if err != nil {
			t.Errorf("update %s: %v, want success after the retry", mark, err)
		}
	}
	if n := deadlocks.Load(); n != 1 {
		t.Fatalf("%d deadlocks detected, want 1 (the test did not deadlock)", n)
	}
	if total := calls.get("one", "write") + calls.get("two", "write"); total != 3 {
		t.Errorf("%d attempts in all, want 3 (two writes, one retried once)", total)
	}
	for _, id := range []uuid.UUID{a, b} {
		if seq := itemSeqOf(t, id); seq != 2 {
			t.Errorf("item_seq = %d, want 2: each update written once", seq)
		}
	}
}

// The retry classifier: a deadlock (40P01) and a serialization failure
// (40001) are run again, wrapped or not; other Postgres codes and errors
// that only look like one are not.
func TestWrite_RetryableErrors(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"deadlock", &pgconn.PgError{Code: "40P01"}, true},
		{"serialization failure", &pgconn.PgError{Code: "40001"}, true},
		{"wrapped deadlock", fmt.Errorf("lock item: %w", &pgconn.PgError{Code: "40P01"}), true},
		{"wrapped serialization failure", fmt.Errorf("insert: %w", &pgconn.PgError{Code: "40001"}), true},
		{"unique violation", &pgconn.PgError{Code: "23505"}, false},
		{"statement timeout", &pgconn.PgError{Code: "57014"}, false},
		{"a plain error naming a code", errors.New("SQLSTATE 40P01"), false},
		{"a refusal", itemservice.ErrRejected, false},
		{"nil", nil, false},
	}
	for _, c := range cases {
		if got := itemservice.Retryable(c.err); got != c.want {
			t.Errorf("%s: Retryable = %v, want %v", c.name, got, c.want)
		}
	}
}
