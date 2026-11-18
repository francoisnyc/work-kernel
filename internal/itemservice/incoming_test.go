// Tests for the Jira proof of concept, part 1: incoming links from other
// Types, as guards read them and as rollups count them (docs/design.md,
// "Jira proof of concept").
//
// An epic names the stories and bugs that link to it through THEIR link
// type `epic` (incoming: issues). Its guard reads them like Step 1's links:
// state and category only, whoever the caller is. A rollup counts them when
// the epic is read, and counts only what the reader can see.
//
// Locking needs no new protocol. Closing the epic locks the epic FOR UPDATE
// and its issues FOR SHARE in UUID order (lockForTransition): an issue
// cannot change state before the close commits, and no issue can be linked
// or unlinked meanwhile, since a link write takes its target FOR KEY SHARE
// and its source FOR UPDATE. The Step 1 race tests are repeated here across
// Types.
package itemservice_test

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// --- the world ---

// issueTypeYAML is a Type whose Items link to an epic. STORY and BUG use it
// with the inverse `issues`; OUTSIDER links to epics too but the epic does
// not name it.
const issueTypeYAML = `
id: TYPE
version: 1
initial_state: s_open
fields:
  - {id: f_title, type: string}
links:
  - {name: epic, inverse: INVERSE, targets: [EPIC], max: one}
states:
  - {id: s_open,     category: todo}
  - {id: s_work,     category: active}
  - {id: s_done,     category: done}
  - {id: s_canceled, category: canceled}
transitions:
  - {from: s_open, to: s_work}
  - {from: s_work, to: s_done}
  - {from: s_done, to: s_work}
  - {from: s_open, to: s_canceled}
`

// epicTypeYAML: an epic closes only when every story and bug linked to it
// is done or canceled; it counts them by category and by state, and counts
// the epics it depends on (a rollup over one of its own links).
const epicTypeYAML = `
id: EPIC
version: 1
initial_state: e_open
fields:
  - {id: f_title, type: string}
links:
  - {name: depends_on, inverse: needed_by, targets: [EPIC]}
incoming:
  - {name: issues, link: epic, from: [STORY, BUG]}
rollups:
  - {id: progress, over: issues, count_by: category}
  - {id: by_state, over: issues, count_by: state}
  - {id: needs,    over: depends_on, count_by: category}
states:
  - {id: e_open, category: todo}
  - {id: e_done, category: done}
transitions:
  - from: e_open
    to: e_done
    guard: 'links.issues.all(i, i.category in ["done", "canceled"])'
  - {from: e_done, to: e_open}
`

type epicWorld struct {
	k                          *itemservice.Service
	epic, story, bug, outsider string // Type IDs
	ana, bob                   item.Principal
}

func newEpicWorld(t *testing.T) epicWorld {
	t.Helper()
	k := itemservice.New(pool)
	sfx := uuid.NewString()[:8]
	ns := newNamespace(t, k)
	w := epicWorld{
		k: k, epic: "epic-" + sfx, story: "story-" + sfx, bug: "bug-" + sfx, outsider: "out-" + sfx,
		ana: in(person("ana"), ns), bob: in(person("bob"), ns),
	}
	register := func(src string) {
		t.Helper()
		ty, err := typedef.Parse([]byte(strings.NewReplacer("EPIC", w.epic, "STORY", w.story, "BUG", w.bug).Replace(src)))
		if err != nil {
			t.Fatalf("parse epic test Type: %v", err)
		}
		if err := k.RegisterType(testCtx(t), ty); err != nil {
			t.Fatalf("register epic test Type: %v", err)
		}
	}
	// The Types that declare the link first, then the Type that names them.
	for id, inverse := range map[string]string{w.story: "issues", w.bug: "issues", w.outsider: "mentions"} {
		register(strings.NewReplacer("TYPE", id, "INVERSE", inverse).Replace(issueTypeYAML))
	}
	register(epicTypeYAML)
	return w
}

// issuePaths walk an issue from s_open to a state.
var issuePaths = map[string][]string{
	"s_open": nil, "s_work": {"s_work"}, "s_done": {"s_work", "s_done"}, "s_canceled": {"s_canceled"},
}

type epicOpts struct {
	narrow    []string
	container *uuid.UUID
}

func (w epicWorld) create(t *testing.T, p item.Principal, typ string, o epicOpts) uuid.UUID {
	t.Helper()
	req := itemservice.CreateRequest{
		IdempotencyKey: newKey(), Principal: p, TypeID: typ, TypeVersion: 1,
		Fields: map[string]any{"f_title": "epic test " + word()}, ContainerID: o.container,
	}
	if o.narrow != nil {
		req.Narrow = &itemservice.Narrow{Readers: o.narrow}
	}
	res, err := w.k.Create(testCtx(t), req)
	if err != nil {
		t.Fatalf("create %s: %v", typ, err)
	}
	return res.ItemID
}

func (w epicWorld) newEpic(t *testing.T) uuid.UUID { return w.create(t, w.ana, w.epic, epicOpts{}) }

func (w epicWorld) move(ctx context.Context, p item.Principal, id uuid.UUID, to string) error {
	_, err := w.k.Transition(ctx, itemservice.TransitionRequest{
		IdempotencyKey: newKey(), Principal: p, ItemID: id, ToState: to,
	})
	return err
}

func (w epicWorld) mustMove(t *testing.T, p item.Principal, id uuid.UUID, to string) {
	t.Helper()
	if err := w.move(testCtx(t), p, id, to); err != nil {
		t.Fatalf("move %s to %s: %v", id, to, err)
	}
}

func (w epicWorld) linkReq(p item.Principal, issue, epic uuid.UUID) itemservice.LinkRequest {
	return itemservice.LinkRequest{IdempotencyKey: newKey(), Principal: p, SourceID: issue, LinkType: "epic", TargetID: epic}
}

// issue creates an Item of typ as ana, walks it to state, and links it to
// the epic.
func (w epicWorld) issue(t *testing.T, typ, state string, epic uuid.UUID, o epicOpts) uuid.UUID {
	t.Helper()
	id := w.create(t, w.ana, typ, o)
	for _, to := range issuePaths[state] {
		w.mustMove(t, w.ana, id, to)
	}
	if _, err := w.k.Link(testCtx(t), w.linkReq(w.ana, id, epic)); err != nil {
		t.Fatalf("link %s to its epic: %v", typ, err)
	}
	return id
}

// --- the guard ---

// An epic cannot close while a story or a bug linked to it is neither done
// nor canceled. Items of a Type the epic does not name do not count.
func TestIncomingGuard_AnOpenIssueOfAnotherTypeBlocks(t *testing.T) {
	w := newEpicWorld(t)
	e := w.newEpic(t)
	story := w.issue(t, w.story, "s_work", e, epicOpts{})
	w.issue(t, w.bug, "s_done", e, epicOpts{})
	w.issue(t, w.bug, "s_canceled", e, epicOpts{})
	w.issue(t, w.outsider, "s_open", e, epicOpts{}) // not named by the epic

	err := w.move(testCtx(t), w.ana, e, "e_done")
	assertRefusedMeta(t, "close with a story in progress", err, itemservice.CodeGuardFailed,
		map[string]string{"from_state": "e_open", "to_state": "e_done"})
	if s := stateOf(t, e); s != "e_open" {
		t.Errorf("the epic is %s after the refusal, want e_open", s)
	}

	w.mustMove(t, w.ana, story, "s_done")
	w.mustMove(t, w.ana, e, "e_done")

	// A bug blocks like a story does.
	e2 := w.newEpic(t)
	bug := w.issue(t, w.bug, "s_open", e2, epicOpts{})
	assertRejectedCode(t, "close with an open bug", w.move(testCtx(t), w.ana, e2, "e_done"), itemservice.CodeGuardFailed)
	w.mustMove(t, w.ana, bug, "s_canceled")
	w.mustMove(t, w.ana, e2, "e_done")

	// An epic with no issue at all closes.
	w.mustMove(t, w.ana, w.newEpic(t), "e_done")
}

// An issue the caller cannot see still blocks: a rule must hold for
// everyone. The refusal names nothing about it.
func TestIncomingGuard_AHiddenIssueStillBlocks(t *testing.T) {
	w := newEpicWorld(t)
	e := w.newEpic(t)
	secret := w.issue(t, w.story, "s_work", e, epicOpts{narrow: readers(w.ana)})

	err := w.move(testCtx(t), w.bob, e, "e_done")
	assertRejectedCode(t, "bob closes the epic", err, itemservice.CodeGuardFailed)
	var rej *itemservice.RejectedError
	if errors.As(err, &rej) {
		if strings.Contains(rej.Error(), secret.String()) {
			t.Errorf("refusal %q names the hidden story", rej)
		}
		for k, v := range rej.Metadata {
			if strings.Contains(v, secret.String()) {
				t.Errorf("metadata %s = %q names the hidden story", k, v)
			}
		}
	}
	assertHidden(t, w.k, w.bob, secret)
}

// --- the races the locks prevent, across Types ---

// Deterministic: T2 closes the epic (its one story is done) and stops with
// its guard checked and its locks held. T1 reopens the story: it must wait
// for T2 to commit (the story is locked FOR SHARE), so the epic is never
// closed while the story is open.
func TestIncomingGuard_ReopenWaitsForTheClose(t *testing.T) {
	w := newEpicWorld(t)
	e := w.newEpic(t)
	story := w.issue(t, w.story, "s_done", e, epicOpts{})

	held, release := make(chan struct{}), make(chan struct{})
	defer itemservice.SetTestHook(func(ctx context.Context, tx pgx.Tx, point string) error {
		if markOf(ctx) == "T2" && point == "link_guard_held" {
			close(held)
			<-release
		}
		return nil
	})()

	t2 := make(chan error, 1)
	go func() { t2 <- w.move(marked(testCtx(t), "T2"), w.ana, e, "e_done") }()
	select {
	case <-held:
	case err := <-t2:
		t.Fatalf("T2 ended before holding its guard: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("T2 never reached its held guard")
	}
	t1 := make(chan error, 1)
	go func() { t1 <- w.move(testCtx(t), w.ana, story, "s_work") }()
	select {
	case err := <-t1:
		t.Fatalf("T1 reopened the story while T2 held its guard (err %v): the story must be locked FOR SHARE", err)
	case <-time.After(300 * time.Millisecond):
	}
	close(release)
	if err := <-t2; err != nil {
		t.Fatalf("T2 close: %v", err)
	}
	if err := <-t1; err != nil {
		t.Fatalf("T1 reopen: %v", err)
	}
	if c1, c2 := eventCommitSeq(t, story), eventCommitSeq(t, e); c1 <= c2 {
		t.Errorf("reopen commit_seq %d <= close commit_seq %d: the reopen was written before the close committed", c1, c2)
	}
}

// Under many concurrent runs: T1 reopens a bug while T2 closes its epic.
// The epic never closes while the bug is open: either the close commits
// first and the reopen comes after it, or the reopen comes first and the
// close is refused. No other error; no deadlock reaches the caller.
func TestIncomingGuard_ReopenRace_ManyRuns(t *testing.T) {
	w := newEpicWorld(t)
	const runs = 60
	var closedFirst, refused int
	for i := range runs {
		e := w.newEpic(t)
		bug := w.issue(t, w.bug, "s_done", e, epicOpts{})

		start := make(chan struct{})
		var wg sync.WaitGroup
		var err1, err2 error
		wg.Add(2)
		go func() { defer wg.Done(); <-start; err1 = w.move(context.Background(), w.ana, bug, "s_work") }()
		go func() { defer wg.Done(); <-start; err2 = w.move(context.Background(), w.ana, e, "e_done") }()
		close(start)
		wg.Wait()

		if err1 != nil {
			t.Fatalf("run %d: T1 reopen: %v", i, err1)
		}
		switch {
		case err2 == nil:
			closedFirst++
			if c1, c2 := eventCommitSeq(t, bug), eventCommitSeq(t, e); c1 <= c2 {
				t.Errorf("run %d: the epic closed (commit_seq %d) after the bug reopened (commit_seq %d)", i, c2, c1)
			}
		default:
			var rej *itemservice.RejectedError
			if !errors.As(err2, &rej) || rej.Code != itemservice.CodeGuardFailed {
				t.Fatalf("run %d: T2 close: %v, want nil or guard_failed", i, err2)
			}
			refused++
			if s := stateOf(t, e); s != "e_open" {
				t.Errorf("run %d: refused, but the epic is %s", i, s)
			}
		}
	}
	t.Logf("%d runs: %d closed before the reopen, %d refused after it", runs, closedFirst, refused)
}

// An issue linked between the unlocked read and the locks is seen: the
// re-read differs, the transaction starts again, and the new open bug
// refuses the close.
func TestIncomingGuard_AnIssueLinkedBeforeTheLocksIsSeen(t *testing.T) {
	w := newEpicWorld(t)
	e := w.newEpic(t)
	bug := w.create(t, w.ana, w.bug, epicOpts{})

	var calls hookCalls
	defer itemservice.SetTestHook(func(ctx context.Context, tx pgx.Tx, point string) error {
		if markOf(ctx) != "T2" || point != "links_read" {
			return nil
		}
		if calls.add("T2", point) == 1 {
			// Another writer links an open bug before T2 takes its locks.
			if _, err := w.k.Link(context.Background(), w.linkReq(w.ana, bug, e)); err != nil {
				return fmt.Errorf("link the bug: %w", err)
			}
		}
		return nil
	})()

	err := w.move(marked(testCtx(t), "T2"), w.ana, e, "e_done")
	assertRejectedCode(t, "close with a bug linked meanwhile", err, itemservice.CodeGuardFailed)
	if n := calls.get("T2", "links_read"); n != 2 {
		t.Errorf("links read %d times, want 2 (the first attempt saw the link set change)", n)
	}
	if s := stateOf(t, e); s != "e_open" {
		t.Errorf("the epic is %s, want e_open", s)
	}
}

// Deterministic: while T2 holds the epic with its guard checked, T1 links
// an open story to it. The link write takes the epic FOR KEY SHARE, which
// waits for T2's FOR UPDATE: the link lands only after the close committed,
// never between the guard and the commit.
func TestIncomingGuard_ALinkWaitsForTheClose(t *testing.T) {
	w := newEpicWorld(t)
	e := w.newEpic(t)
	w.issue(t, w.story, "s_done", e, epicOpts{}) // so the guard reads and holds its locks
	late := w.create(t, w.ana, w.story, epicOpts{})

	held, release := make(chan struct{}), make(chan struct{})
	defer itemservice.SetTestHook(func(ctx context.Context, tx pgx.Tx, point string) error {
		if markOf(ctx) == "T2" && point == "link_guard_held" {
			close(held)
			<-release
		}
		return nil
	})()

	t2 := make(chan error, 1)
	go func() { t2 <- w.move(marked(testCtx(t), "T2"), w.ana, e, "e_done") }()
	select {
	case <-held:
	case err := <-t2:
		t.Fatalf("T2 ended before holding its guard: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("T2 never reached its held guard")
	}
	t1 := make(chan error, 1)
	go func() {
		_, err := w.k.Link(testCtx(t), w.linkReq(w.ana, late, e))
		t1 <- err
	}()
	select {
	case err := <-t1:
		t.Fatalf("T1 linked a story to the epic while T2 held its guard (err %v): the link must wait for the epic's lock", err)
	case <-time.After(300 * time.Millisecond):
	}
	close(release)
	if err := <-t2; err != nil {
		t.Fatalf("T2 close: %v", err)
	}
	if err := <-t1; err != nil {
		t.Fatalf("T1 link: %v", err)
	}
	if c1, c2 := eventCommitSeq(t, late), eventCommitSeq(t, e); c1 <= c2 {
		t.Errorf("link commit_seq %d <= close commit_seq %d: the link was written before the close committed", c1, c2)
	}
}

// Under many concurrent runs: T1 links an open story to an epic while T2
// closes it. Either the close commits first (the link's event comes after
// it), or the link comes first and the close is refused. Never a close that
// committed after an open story was linked. No deadlock reaches the caller.
func TestIncomingGuard_LinkRace_ManyRuns(t *testing.T) {
	w := newEpicWorld(t)
	const runs = 60
	var closedFirst, refused int
	for i := range runs {
		e := w.newEpic(t)
		w.issue(t, w.bug, "s_done", e, epicOpts{})
		story := w.create(t, w.ana, w.story, epicOpts{})

		start := make(chan struct{})
		var wg sync.WaitGroup
		var err1, err2 error
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			_, err1 = w.k.Link(context.Background(), w.linkReq(w.ana, story, e))
		}()
		go func() { defer wg.Done(); <-start; err2 = w.move(context.Background(), w.ana, e, "e_done") }()
		close(start)
		wg.Wait()

		if err1 != nil {
			t.Fatalf("run %d: T1 link: %v", i, err1)
		}
		switch {
		case err2 == nil:
			closedFirst++
			if c1, c2 := eventCommitSeq(t, story), eventCommitSeq(t, e); c1 <= c2 {
				t.Errorf("run %d: the epic closed (commit_seq %d) after an open story was linked (commit_seq %d)", i, c2, c1)
			}
		default:
			var rej *itemservice.RejectedError
			if !errors.As(err2, &rej) || rej.Code != itemservice.CodeGuardFailed {
				t.Fatalf("run %d: T2 close: %v, want nil or guard_failed", i, err2)
			}
			refused++
		}
	}
	t.Logf("%d runs: %d closed before the link, %d refused after it", runs, closedFirst, refused)
}

// --- rollups ---

// rollupsOf reads an Item as p and returns its rollups.
func (w epicWorld) rollupsOf(t *testing.T, p item.Principal, id uuid.UUID) map[string]item.Rollup {
	t.Helper()
	it, err := w.k.Get(testCtx(t), p, id)
	if err != nil {
		t.Fatalf("Get as %s: %v", p.Actor, err)
	}
	return it.Rollups
}

func assertCounts(t *testing.T, what string, rollups map[string]item.Rollup, id string, want map[string]int64) {
	t.Helper()
	r, ok := rollups[id]
	if !ok {
		t.Errorf("%s: no rollup %q among %v", what, id, slices.Sorted(maps.Keys(rollups)))
		return
	}
	// A key with no Item is absent: an empty rollup has no keys.
	if len(r.Counts) != len(want) || !maps.Equal(r.Counts, want) {
		t.Errorf("%s: %s = %v, want %v", what, id, r.Counts, want)
	}
}

// Get shows every rollup the epic's Type declares: its issues by category
// and by state, across the Types it names, and the epics it depends on.
func TestRollup_CountsByCategoryAndByState(t *testing.T) {
	w := newEpicWorld(t)
	e := w.newEpic(t)
	w.issue(t, w.story, "s_open", e, epicOpts{})
	w.issue(t, w.bug, "s_open", e, epicOpts{})
	w.issue(t, w.story, "s_work", e, epicOpts{})
	w.issue(t, w.bug, "s_done", e, epicOpts{})
	w.issue(t, w.story, "s_canceled", e, epicOpts{})
	w.issue(t, w.outsider, "s_open", e, epicOpts{}) // not named by the epic: not counted
	other := w.newEpic(t)
	if _, err := w.k.Link(testCtx(t), itemservice.LinkRequest{
		IdempotencyKey: newKey(), Principal: w.ana, SourceID: e, LinkType: "depends_on", TargetID: other,
	}); err != nil {
		t.Fatalf("link depends_on: %v", err)
	}

	got := w.rollupsOf(t, w.ana, e)
	assertCounts(t, "the epic", got, "progress", map[string]int64{"todo": 2, "active": 1, "done": 1, "canceled": 1})
	assertCounts(t, "the epic", got, "by_state", map[string]int64{"s_open": 2, "s_work": 1, "s_done": 1, "s_canceled": 1})
	assertCounts(t, "the epic", got, "needs", map[string]int64{"todo": 1})
	if len(got) != 3 {
		t.Errorf("the epic has %d rollups, want the 3 its Type declares: %v", len(got), got)
	}

	// Computed when read: a story moves, the next read counts it there.
	// The epic itself was not written: its item_seq has not moved.
	before := itemSeqOf(t, e)
	for _, id := range idsLinkedTo(t, e) {
		if stateOf(t, id) == "s_work" {
			w.mustMove(t, w.ana, id, "s_done")
		}
	}
	assertCounts(t, "after a story is done", w.rollupsOf(t, w.ana, e), "progress",
		map[string]int64{"todo": 2, "done": 2, "canceled": 1})
	if after := itemSeqOf(t, e); after != before {
		t.Errorf("the epic's item_seq moved from %d to %d: a rollup is never stored", before, after)
	}

	// An epic with no issue has its rollups, empty.
	empty := w.rollupsOf(t, w.ana, other)
	assertCounts(t, "an epic with no issue", empty, "progress", map[string]int64{})
	assertCounts(t, "an epic with no issue", empty, "by_state", map[string]int64{})
	// An Item of a Type that declares none has none.
	if r := w.rollupsOf(t, w.ana, w.create(t, w.ana, w.story, epicOpts{})); len(r) != 0 {
		t.Errorf("a story has rollups %v, want none", r)
	}
}

// idsLinkedTo reads the sources of the links that point to an Item.
func idsLinkedTo(t *testing.T, target uuid.UUID) []uuid.UUID {
	t.Helper()
	rows, err := pool.Query(testCtx(t), `select source_id from links where target_id = $1`, target)
	if err != nil {
		t.Fatalf("read links: %v", err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		t.Fatalf("read links: %v", err)
	}
	return ids
}

// A rollup is a read: it counts only the Items the reader can see, because
// counting a hidden one would reveal that it exists. A guard is a rule: it
// sees them all. So bob reads "nothing open" and still cannot close.
func TestRollup_CountsOnlyWhatTheReaderSees(t *testing.T) {
	w := newEpicWorld(t)
	e := w.newEpic(t)
	w.issue(t, w.story, "s_done", e, epicOpts{})
	w.issue(t, w.story, "s_work", e, epicOpts{narrow: readers(w.ana)})
	w.issue(t, w.bug, "s_open", e, epicOpts{narrow: readers(w.ana)})

	assertCounts(t, "ana, who sees all three", w.rollupsOf(t, w.ana, e), "progress",
		map[string]int64{"todo": 1, "active": 1, "done": 1})
	assertCounts(t, "bob, who sees one", w.rollupsOf(t, w.bob, e), "progress", map[string]int64{"done": 1})
	assertCounts(t, "bob, by state", w.rollupsOf(t, w.bob, e), "by_state", map[string]int64{"s_done": 1})

	assertRejectedCode(t, "bob closes an epic whose open issues he cannot see",
		w.move(testCtx(t), w.bob, e, "e_done"), itemservice.CodeGuardFailed)
}

// Rollups are filled by Get only: Items in Search and List pages carry
// none (a page of epics would run one count per row).
func TestRollup_OnlyOnGet(t *testing.T) {
	w := newEpicWorld(t)
	box := w.create(t, w.ana, w.outsider, epicOpts{})
	e := w.create(t, w.ana, w.epic, epicOpts{container: &box})
	w.issue(t, w.story, "s_open", e, epicOpts{})

	found, err := w.k.Search(testCtx(t), itemservice.SearchRequest{
		Principal: w.ana, Filter: itemservice.Filter{TypeIDs: []string{w.epic}},
	})
	if err != nil || len(found.Items) != 1 {
		t.Fatalf("Search: %d Items, %v; want the epic", len(found.Items), err)
	}
	if found.Items[0].Rollups != nil {
		t.Errorf("a Search result carries rollups %v", found.Items[0].Rollups)
	}
	listed, err := w.k.List(testCtx(t), itemservice.ListRequest{Principal: w.ana, ContainerID: &box})
	if err != nil || len(listed.Items) != 1 {
		t.Fatalf("List: %d Items, %v; want the epic", len(listed.Items), err)
	}
	if listed.Items[0].Rollups != nil {
		t.Errorf("a List result carries rollups %v", listed.Items[0].Rollups)
	}
	assertCounts(t, "Get", w.rollupsOf(t, w.ana, e), "progress", map[string]int64{"todo": 1})
}

// An agent session counts only Items inside its scopes, and counting Items
// in a narrowed scope is a read of that scope: it is recorded, so the write
// rule knows the session has seen something of it.
func TestRollup_AgentSession_CountsItsScopesAndRecordsNarrowedReads(t *testing.T) {
	w := newEpicWorld(t)
	inScope := w.create(t, w.ana, w.outsider, epicOpts{})
	outOfScope := w.create(t, w.ana, w.outsider, epicOpts{})
	agent := scoped(t, w.k, w.ana, []uuid.UUID{inScope}, "read")
	room := w.create(t, w.ana, w.outsider, epicOpts{container: &inScope, narrow: []string{w.ana.Actor, agent.Actor}})

	e := w.create(t, w.ana, w.epic, epicOpts{container: &inScope})
	w.issue(t, w.story, "s_open", e, epicOpts{container: &inScope})
	w.issue(t, w.story, "s_work", e, epicOpts{container: &outOfScope})
	w.issue(t, w.bug, "s_open", e, epicOpts{container: &room})

	assertCounts(t, "ana", w.rollupsOf(t, w.ana, e), "progress", map[string]int64{"todo": 2, "active": 1})
	if got := recordedReads(t, agent.AgentSession); len(got) != 0 {
		t.Fatalf("reads recorded before the agent read anything: %v", got)
	}
	assertCounts(t, "the agent, in its scopes", w.rollupsOf(t, agent, e), "progress", map[string]int64{"todo": 2})
	if got := recordedReads(t, agent.AgentSession); !slices.Contains(got, roomScope(t, room)) {
		t.Errorf("recorded reads %v do not include the narrowed room's scope %s: its bug was counted", got, roomScope(t, room))
	}

	// An agent whose session does not reach the room at all records nothing
	// of it and does not count its bug.
	elsewhere := w.create(t, w.ana, w.outsider, epicOpts{})
	e2 := w.create(t, w.ana, w.epic, epicOpts{container: &elsewhere})
	w.issue(t, w.bug, "s_open", e2, epicOpts{container: &room})
	narrow := scoped(t, w.k, w.ana, []uuid.UUID{elsewhere}, "read")
	assertCounts(t, "an agent whose scopes leave the room out", w.rollupsOf(t, narrow, e2), "progress", map[string]int64{})
	if got := recordedReads(t, narrow.AgentSession); len(got) != 0 {
		t.Errorf("an agent that counted nothing narrowed recorded reads %v", got)
	}
}
