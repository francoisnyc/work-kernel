// Tests for Step 4: full-text search with min_seq.
//
// Search matches the string field values of Items the principal can see. The
// visibility filter is part of the SQL query, never applied after fetching.
// min_seq lets a caller read its own write: Search waits, up to a bound, until
// commit_seq has reached it, then answers ErrNotCaughtUp.
package itemservice_test

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/itemservice"
)

// lastCommitSeq returns the last commit_seq handed out. Tests in a package
// run one at a time, so nothing else takes a number under a test. (Step A:
// commit_seq comes from a sequence; is_called is false until its first use.)
func lastCommitSeq(t *testing.T) int64 {
	t.Helper()
	var n int64
	err := pool.QueryRow(testCtx(t),
		`select case when is_called then last_value else last_value - 1 end from commit_seq`).Scan(&n)
	if err != nil {
		t.Fatalf("read commit_seq: %v", err)
	}
	return n
}

// --- matching ---

// Search finds an Item by a word in one of its string fields, and returns it
// as Get would.
func TestSearch_FindsAnItemByAWordInAStringField(t *testing.T) {
	k := itemservice.New(pool)
	alice := person("alice")
	term := word()
	hit := createAs(t, k, alice, "Printer "+term+" on fire", nil, nil)
	createAs(t, k, alice, "Printer is fine", nil, nil)

	res, err := k.Search(testCtx(t), itemservice.SearchRequest{Principal: alice, Query: term, MinSeq: hit.CommitSeq})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res.Items) != 1 {
		t.Fatalf("Search returned %d Items, want 1", len(res.Items))
	}
	want := getAs(t, k, alice, hit.ItemID)
	if !reflect.DeepEqual(res.Items[0], want) {
		t.Errorf("Search returned %+v, want what Get returns: %+v", res.Items[0], want)
	}
}

// The index follows the Item: after an Update, the new word matches and the
// old one no longer does.
func TestSearch_FollowsAnUpdate(t *testing.T) {
	k := itemservice.New(pool)
	alice := person("alice")
	before, after := word(), word()
	created := createAs(t, k, alice, "title "+before, nil, nil)

	upd, err := k.Update(testCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: alice, ItemID: created.ItemID,
		Fields: map[string]any{"f_title": "title " + after},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}

	if got := searchIDs(t, k, alice, after, upd.CommitSeq); !reflect.DeepEqual(got, []uuid.UUID{created.ItemID}) {
		t.Errorf("search for the new word = %v, want the Item", got)
	}
	if got := searchIDs(t, k, alice, before, upd.CommitSeq); len(got) != 0 {
		t.Errorf("search for the old word = %v, want nothing", got)
	}
}

// A search that matches nothing returns an empty result, not an error.
func TestSearch_NoMatch_ReturnsNothing(t *testing.T) {
	k := itemservice.New(pool)
	if got := searchIDs(t, k, person("alice"), word(), 0); len(got) != 0 {
		t.Errorf("search = %v, want nothing", got)
	}
}

// The scope filter runs in SQL, before the limit. Filtering after fetching
// would spend the limit on Items the caller cannot see and return too few.
func TestSearch_FiltersByScopeBeforeTheLimit(t *testing.T) {
	k := itemservice.New(pool)
	alice, bob := person("alice"), person("bob")
	term := word()
	visible := createAs(t, k, alice, "for all "+term, nil, nil)
	var last itemservice.WriteResult
	for range 5 { // Go 1.22+: range over an integer counts 0..4
		last = createAs(t, k, alice, "secret "+term, &itemservice.Narrow{Readers: readers(alice)}, nil)
	}

	res, err := k.Search(testCtx(t), itemservice.SearchRequest{
		Principal: bob, Query: term, MinSeq: last.CommitSeq, Limit: 1,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res.Items) != 1 || res.Items[0].ID != visible.ItemID {
		t.Errorf("bob's search with limit 1 = %+v, want the one Item he can see", res.Items)
	}

	all, err := k.Search(testCtx(t), itemservice.SearchRequest{Principal: alice, Query: term, MinSeq: last.CommitSeq})
	if err != nil {
		t.Fatalf("Search as alice: %v", err)
	}
	if len(all.Items) != 6 {
		t.Errorf("alice's search returned %d Items, want 6", len(all.Items))
	}
}

// Search reads only the caller's namespace, like Get.
func TestSearch_ItemInAnotherNamespace_IsNotReturned(t *testing.T) {
	k := itemservice.New(pool)
	alice := person("alice")
	term := word()
	res := createAs(t, k, alice, "elsewhere "+term, nil, nil)
	if _, err := pool.Exec(testCtx(t), `update items set namespace_id = 'other' where id = $1`, res.ItemID); err != nil {
		t.Fatalf("move item to another namespace: %v", err)
	}
	if got := searchIDs(t, k, alice, term, 0); len(got) != 0 {
		t.Errorf("search = %v, want nothing", got)
	}
}

func TestSearch_MalformedRequest_IsInvalid(t *testing.T) {
	k := itemservice.New(pool)
	alice := person("alice")
	cases := []struct {
		name string
		req  itemservice.SearchRequest
	}{
		{"no actor", itemservice.SearchRequest{Query: "x"}},
		{"no query", itemservice.SearchRequest{Principal: alice}},
		{"negative min_seq", itemservice.SearchRequest{Principal: alice, Query: "x", MinSeq: -1}},
		{"negative wait", itemservice.SearchRequest{Principal: alice, Query: "x", Wait: -time.Second}},
		{"negative limit", itemservice.SearchRequest{Principal: alice, Query: "x", Limit: -1}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := k.Search(testCtx(t), c.req)
			if !errors.Is(err, itemservice.ErrInvalid) {
				t.Errorf("Search error = %v, want ErrInvalid", err)
			}
		})
	}
}

// --- min_seq ---

// A min_seq already reached is answered at once.
func TestSearch_MinSeqAlreadyReached_AnswersAtOnce(t *testing.T) {
	k := itemservice.New(pool)
	alice := person("alice")
	term := word()
	res := createAs(t, k, alice, "now "+term, nil, nil)
	// A new Service's first Search loads every registered Type version (a
	// second under -race at this suite's size): not what is timed here.
	searchIDs(t, k, alice, term, 0)

	start := time.Now()
	got := searchIDs(t, k, alice, term, res.CommitSeq)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("Search took %v, want an immediate answer", elapsed)
	}
	if !slices.Contains(got, res.ItemID) {
		t.Errorf("search = %v, want it to hold the Item just written", got)
	}
}

// A min_seq ahead of the counter makes Search wait for the bound given, then
// answer ErrNotCaughtUp: retryable, and distinct from every other refusal.
func TestSearch_MinSeqNotReached_WaitsThenReturnsNotCaughtUp(t *testing.T) {
	k := itemservice.New(pool)
	const wait = 300 * time.Millisecond

	start := time.Now()
	_, err := k.Search(testCtx(t), itemservice.SearchRequest{
		Principal: person("alice"), Query: word(), MinSeq: lastCommitSeq(t) + 1, Wait: wait,
	})
	elapsed := time.Since(start)

	if !errors.Is(err, itemservice.ErrNotCaughtUp) {
		t.Fatalf("Search error = %v, want ErrNotCaughtUp", err)
	}
	for _, other := range []error{itemservice.ErrInvalid, itemservice.ErrNotFound, itemservice.ErrStale, itemservice.ErrRejected} {
		if errors.Is(err, other) {
			t.Errorf("error also matches %v; ErrNotCaughtUp must be distinct", other)
		}
	}
	if elapsed < wait {
		t.Errorf("Search gave up after %v, before its %v bound", elapsed, wait)
	}
	if elapsed > wait+1500*time.Millisecond {
		t.Errorf("Search took %v, far beyond its %v bound", elapsed, wait)
	}
}

// With no Wait given, the bound is the default of 2 seconds.
func TestSearch_MinSeqNotReached_DefaultWaitIsTwoSeconds(t *testing.T) {
	k := itemservice.New(pool)

	start := time.Now()
	_, err := k.Search(testCtx(t), itemservice.SearchRequest{
		Principal: person("alice"), Query: word(), MinSeq: lastCommitSeq(t) + 1,
	})
	elapsed := time.Since(start)

	if !errors.Is(err, itemservice.ErrNotCaughtUp) {
		t.Fatalf("Search error = %v, want ErrNotCaughtUp", err)
	}
	if elapsed < 1900*time.Millisecond || elapsed > 4*time.Second {
		t.Errorf("Search waited %v, want about 2s", elapsed)
	}
}

// The wait ends as soon as the write arrives: a Search that starts before the
// write commits still returns it.
func TestSearch_MinSeqReachedDuringTheWait_ReturnsTheWrite(t *testing.T) {
	k := itemservice.New(pool)
	alice := person("alice")
	term := word()
	next := lastCommitSeq(t) + 1 // the commit_seq the write below will get

	// A channel hands the goroutine's result back to the test, like a
	// CompletableFuture. t.Fatalf may only be called from the test's own goroutine.
	type created struct {
		res itemservice.WriteResult
		err error
	}
	done := make(chan created, 1)
	go func() {
		time.Sleep(300 * time.Millisecond)
		res, err := k.Create(testCtx(t), itemservice.CreateRequest{
			IdempotencyKey: newKey(), Principal: alice, TypeID: "ticket", TypeVersion: 1,
			Fields: map[string]any{"f_title": "late " + term},
		})
		done <- created{res, err}
	}()

	start := time.Now()
	res, err := k.Search(testCtx(t), itemservice.SearchRequest{
		Principal: alice, Query: term, MinSeq: next, Wait: 10 * time.Second,
	})
	elapsed := time.Since(start)
	c := <-done
	if c.err != nil {
		t.Fatalf("Create: %v", c.err)
	}
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if c.res.CommitSeq != next {
		t.Fatalf("the write got commit_seq %d, the test expected %d", c.res.CommitSeq, next)
	}
	if len(res.Items) != 1 || res.Items[0].ID != c.res.ItemID {
		t.Errorf("Search = %+v, want the Item written during the wait", res.Items)
	}
	if elapsed < 250*time.Millisecond || elapsed > 5*time.Second {
		t.Errorf("Search took %v, want it to end soon after the write at ~300ms", elapsed)
	}
}

// The caller's context still wins: cancel it and the wait stops.
func TestSearch_WaitStopsWhenTheContextEnds(t *testing.T) {
	k := itemservice.New(pool)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := k.Search(ctx, itemservice.SearchRequest{
		Principal: person("alice"), Query: word(), MinSeq: lastCommitSeq(t) + 1, Wait: 10 * time.Second,
	})
	if err == nil {
		t.Fatal("Search returned no error after its context ended")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("Search took %v after a 200ms context, want it to stop", elapsed)
	}
}
