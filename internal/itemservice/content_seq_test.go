// Tests for the etag step, in the item service: items.content_seq, a
// second counter per Item, in the same row and the same transaction as
// the write. It moves only when the write changes something a caller
// could overwrite (content_rule_test.go has the rule, kind by kind); the
// public etag is derived from it.
//
//   - Item.ContentSeq is 1 after a create and is shown by Get, Search and
//     List alike.
//   - ExpectedContentSeq on Update, Transition, Link and Unlink is the
//     if-match behind the etag: ErrStale when the Item's content moved on,
//     checked under the row lock.
//   - item_seq, ExpectedItemSeq and commit_seq keep their meaning exactly:
//     a kernel-only write still bumps item_seq, and ExpectedItemSeq still
//     refuses after it.
//
// The measurement of Jira 2.3b is the reason: a person's save of an
// incident, with what they read right after its create, was refused 10
// times in 10 because the incident's goals had written on it meanwhile.
package itemservice_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/pressly/goose/v3"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
	"github.com/francoisnyc/work-kernel/migrations"
)

// seqs reads both counters as p sees the Item.
func seqs(t *testing.T, k *itemservice.Service, p item.Principal, id uuid.UUID) (itemSeq, contentSeq int64) {
	t.Helper()
	it, err := k.Get(testCtx(t), p, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	return it.ItemSeq, it.ContentSeq
}

func assertSeqs(t *testing.T, what string, k *itemservice.Service, p item.Principal, id uuid.UUID, wantItem, wantContent int64) {
	t.Helper()
	if is, cs := seqs(t, k, p, id); is != wantItem || cs != wantContent {
		t.Errorf("%s: item_seq %d, content_seq %d; want %d and %d", what, is, cs, wantItem, wantContent)
	}
}

// save is a person's field patch with what they read: the if-match on
// content_seq.
func save(t *testing.T, k *itemservice.Service, p item.Principal, id uuid.UUID, read int64, fields map[string]any) error {
	t.Helper()
	_, err := k.Update(testCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: p, ItemID: id, ExpectedContentSeq: read, Fields: fields,
	})
	return err
}

// --- the counter ---

func TestContentSeq_ACreateIsOne_ACallersWriteBumpsIt(t *testing.T) {
	k := itemservice.New(pool)
	id := createTicket(t, k, map[string]any{"f_title": "Printer on fire"})
	assertSeqs(t, "after the create", k, alice, id, 1, 1)

	update(t, k, id, map[string]any{"f_assignee": "user-bob"})
	assertSeqs(t, "after a field update", k, alice, id, 2, 2)

	transition(t, k, id, "in_progress")
	assertSeqs(t, "after a transition", k, alice, id, 3, 3)
}

// A write that names a caller's field bumps the counter even when the
// value is the one the field already had: the rule reads the event's
// shape, not its values (Later: no-op updates are not suppressed yet).
func TestContentSeq_AWriteOfTheSameValueStillBumps(t *testing.T) {
	k := itemservice.New(pool)
	id := createTicket(t, k, map[string]any{"f_title": "Printer on fire"})
	update(t, k, id, map[string]any{"f_title": "Printer on fire"})
	assertSeqs(t, "after writing the same title", k, alice, id, 2, 2)
}

// --- the measurement of Jira 2.3b, by hand ---

// An incident's goals write on it right after its create: two outcome
// records (the goals were created) and two due instants (fields only the
// kernel writes). item_seq counts them; content_seq does not. So the save
// made with what was read at the create is accepted, and ExpectedItemSeq,
// which means what it meant, still refuses.
func TestContentSeq_KernelWritesLeaveItAlone_TheEarlySaveIsAccepted(t *testing.T) {
	w := newSLAWorld(t)
	inc := w.incident(t, "P4", nil)
	readItem, readContent := seqs(t, w.k, w.ana, inc)
	if readItem != 1 || readContent != 1 {
		t.Fatalf("a new incident is at item_seq %d, content_seq %d; want 1 and 1", readItem, readContent)
	}

	// The reactions, as the worker runs them.
	f := w.reactCreate(t, entered(inc, 1, "sla_start_first_response"))
	r := w.reactCreate(t, entered(inc, 1, "sla_start_resolution"))
	w.goalSets(t, f.Target, 1)
	w.goalSets(t, r.Target, 1)
	fields := w.fields(t, inc)
	if fields["f_due_first_response"] == nil || fields["f_due_resolution"] == nil {
		t.Fatalf("the due instants are not on the incident: %v", fields)
	}
	records := count(t, `select count(*) from item_events where item_id = $1 and op = 'reaction_applied'`, inc)
	writes := count(t, `select count(*) from item_events where item_id = $1 and op = 'update' and actor like 'reaction:%'`, inc)
	if records != 2 || writes != 2 {
		t.Fatalf("the incident has %d outcome records and %d due-instant writes, want 2 and 2", records, writes)
	}
	assertSeqs(t, "after the kernel's four writes", w.k, w.ana, inc, 5, 1)

	// ExpectedItemSeq keeps its meaning: the Item is no longer at 1.
	_, err := w.k.Update(testCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: w.ana, ItemID: inc, ExpectedItemSeq: readItem,
		Fields: map[string]any{"f_impact": "minor"},
	})
	assertStale(t, err)

	// The save with what ana read at the create.
	if err := save(t, w.k, w.ana, inc, readContent, map[string]any{"f_impact": "minor"}); err != nil {
		t.Fatalf("ana's save with content_seq %d, after the kernel's writes: %v; want it accepted", readContent, err)
	}
	assertSeqs(t, "after ana's save", w.k, w.ana, inc, 6, 2)
	if got := w.fields(t, inc)["f_impact"]; got != "minor" {
		t.Errorf("f_impact = %v, want minor", got)
	}

	// The goals are Items too: created by a reaction (1), then written
	// only by the kernel.
	if is, cs := seqs(t, w.k, w.admin, r.Target); is < 2 || cs != 1 {
		t.Errorf("the resolution goal: item_seq %d, content_seq %d; want its records counted (>= 2) and content_seq 1", is, cs)
	}
}

// A whole cycle (answer, wait, customer reply) moves content_seq with the
// people's transitions only: the goals' pause, resume and due-instant
// writes do not.
func TestContentSeq_OnlyThePeoplesMovesCount(t *testing.T) {
	w := newSLAWorld(t)
	inc, _, res := w.started(t, "P4", map[string]any{"f_assignee": w.sam.Actor})
	assertSeqs(t, "started", w.k, w.ana, inc, 5, 1)

	w.move(t, w.sam, inc, "in_progress", nil) // 6: content 2
	seq := w.move(t, w.sam, inc, "waiting_for_customer", nil)
	if out := w.reactMove(t, entered(inc, seq, "sla_pause")); out.Outcome != itemservice.ReactionApplied {
		t.Fatalf("sla_pause = %+v, want applied", out)
	}
	w.goalSets(t, res, itemSeqOf(t, res))
	if w.fields(t, inc)["f_due_resolution"] != nil {
		t.Fatalf("the paused goal still shows its due instant")
	}
	is, cs := seqs(t, w.k, w.ana, inc)
	if cs != 3 {
		t.Errorf("after two moves by sam and the goal's pause: content_seq %d, want 3 (create, two moves)", cs)
	}
	if is <= 7 {
		t.Errorf("item_seq %d: the pause's record and the cleared due instant are events too (want > 7)", is)
	}
}

// --- real conflicts are still refused ---

// Two people read the same Item and change the same field: the second
// save is stale, and writes nothing.
func TestContentSeq_TwoPeopleOnTheSameField_TheSecondIsStale(t *testing.T) {
	w := newSLAWorld(t)
	inc, _, _ := w.started(t, "P4", nil)
	_, read := seqs(t, w.k, w.ana, inc) // both read this

	if err := save(t, w.k, w.sam, inc, read, map[string]any{"f_title": "VPN down for everyone"}); err != nil {
		t.Fatalf("sam's save: %v", err)
	}
	before := itemSeqOf(t, inc)
	err := save(t, w.k, w.ana, inc, read, map[string]any{"f_title": "VPN down in Paris"})
	assertStale(t, err)
	// The reason gives both numbers, so the caller knows how far behind it is.
	if !strings.Contains(err.Error(), fmt.Sprint(read)) || !strings.Contains(err.Error(), fmt.Sprint(read+1)) {
		t.Errorf("error %q does not give the expected and the current content_seq (%d, %d)", err, read, read+1)
	}
	if got := w.fields(t, inc)["f_title"]; got != "VPN down for everyone" {
		t.Errorf("f_title = %v, want sam's", got)
	}
	if after := itemSeqOf(t, inc); after != before {
		t.Errorf("the refused save wrote: item_seq %d -> %d", before, after)
	}
	// Another field is no different: the etag is the Item's, not a field's.
	assertStale(t, save(t, w.k, w.ana, inc, read, map[string]any{"f_impact": "minor"}))
	// With what she reads now, it goes through.
	_, now := seqs(t, w.k, w.ana, inc)
	if err := save(t, w.k, w.ana, inc, now, map[string]any{"f_impact": "minor"}); err != nil {
		t.Errorf("ana's save with the current content_seq: %v", err)
	}
}

// A transition between the read and the save: stale. And a transition
// asked with an old read is stale too.
func TestContentSeq_ATransitionInBetween_IsStale(t *testing.T) {
	w := newSLAWorld(t)
	inc, _, _ := w.started(t, "P4", nil)
	_, read := seqs(t, w.k, w.ana, inc)

	w.move(t, w.sam, inc, "in_progress", nil)
	assertStale(t, save(t, w.k, w.ana, inc, read, map[string]any{"f_impact": "minor"}))

	_, err := w.k.Transition(testCtx(t), itemservice.TransitionRequest{
		IdempotencyKey: newKey(), Principal: w.ana, ItemID: inc, ToState: "canceled", ExpectedContentSeq: read,
	})
	assertStale(t, err)
	if got := w.state(t, inc); got != "in_progress" {
		t.Errorf("state = %q, want in_progress: the stale move wrote nothing", got)
	}
	_, now := seqs(t, w.k, w.ana, inc)
	if _, err := w.k.Transition(testCtx(t), itemservice.TransitionRequest{
		IdempotencyKey: newKey(), Principal: w.ana, ItemID: inc, ToState: "canceled", ExpectedContentSeq: now,
	}); err != nil {
		t.Errorf("the move with the current content_seq: %v", err)
	}
}

// Ten saves made at once with the same read: the row lock runs them one
// after the other, and exactly one is accepted.
func TestContentSeq_ConcurrentSavesWithTheSameRead_OneWins(t *testing.T) {
	k := itemservice.New(pool)
	id := createTicket(t, k, map[string]any{"f_title": "Printer on fire"})
	const n = 10
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = k.Update(context.Background(), itemservice.UpdateRequest{
				IdempotencyKey: newKey(), Principal: alice, ItemID: id, ExpectedContentSeq: 1,
				Fields: map[string]any{"f_priority": float64(i)},
			})
		}()
	}
	wg.Wait()
	won := 0
	for _, err := range errs {
		switch {
		case err == nil:
			won++
		case !errors.Is(err, itemservice.ErrStale):
			t.Errorf("a losing save: %v, want ErrStale", err)
		}
	}
	if won != 1 {
		t.Errorf("%d of %d saves with the same read were accepted, want exactly 1", won, n)
	}
	assertSeqs(t, "after the race", k, alice, id, 2, 2)
}

// The same key again returns the first answer: a retry of an accepted
// save is not judged stale against the content it wrote itself.
func TestContentSeq_ARetryWithTheSameKey_ReturnsTheFirstAnswer(t *testing.T) {
	k := itemservice.New(pool)
	id := createTicket(t, k, map[string]any{"f_title": "Printer on fire"})
	req := itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: alice, ItemID: id, ExpectedContentSeq: 1,
		Fields: map[string]any{"f_priority": float64(1)},
	}
	first, err := k.Update(testCtx(t), req)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	again, err := k.Update(testCtx(t), req)
	if err != nil || again != first {
		t.Errorf("the retry = %+v, %v; want the first answer %+v", again, err, first)
	}
	assertSeqs(t, "after the retry", k, alice, id, 2, 2)
}

// Both if-matches may be sent; each is checked. A negative one is
// malformed, like a negative ExpectedItemSeq.
func TestContentSeq_BothExpectations(t *testing.T) {
	w := newSLAWorld(t)
	inc, _, _ := w.started(t, "P4", nil) // item_seq 5, content_seq 1
	try := func(itemSeq, contentSeq int64) error {
		_, err := w.k.Update(testCtx(t), itemservice.UpdateRequest{
			IdempotencyKey: newKey(), Principal: w.ana, ItemID: inc,
			ExpectedItemSeq: itemSeq, ExpectedContentSeq: contentSeq, Fields: map[string]any{"f_impact": "minor"},
		})
		return err
	}
	assertStale(t, try(1, 1)) // item_seq moved on
	assertStale(t, try(5, 2)) // content_seq is not 2
	if err := try(0, -1); !errors.Is(err, itemservice.ErrInvalid) {
		t.Errorf("a negative content_seq: %v, want ErrInvalid", err)
	}
	if err := try(5, 1); err != nil {
		t.Errorf("both current: %v", err)
	}
}

// --- what else bumps it ---

// A link is written on its source: the source's content moved, the
// target's did not. The if-match of Link and Unlink is the source's.
func TestContentSeq_Links_OnTheSourceOnly(t *testing.T) {
	w := newSLAWorld(t) // the admin linked the desk to its calendar and its policy
	assertSeqs(t, "the desk, after its create and two links", w.k, w.admin, w.desk, 3, 3)
	assertSeqs(t, "the calendar, a target", w.k, w.admin, w.cal, 1, 1)

	req := itemservice.LinkRequest{IdempotencyKey: newKey(), Principal: w.admin, SourceID: w.desk, LinkType: "calendar",
		TargetID: w.cal, ExpectedContentSeq: 2}
	_, err := w.k.Unlink(testCtx(t), req)
	assertStale(t, err)
	req.IdempotencyKey, req.ExpectedContentSeq = newKey(), 3
	if _, err := w.k.Unlink(testCtx(t), req); err != nil {
		t.Fatalf("unlink with the desk's current content_seq: %v", err)
	}
	assertSeqs(t, "the desk, after the unlink", w.k, w.admin, w.desk, 4, 4)
	req.IdempotencyKey = newKey() // still 3: stale now
	_, err = w.k.Link(testCtx(t), req)
	assertStale(t, err)
	req.IdempotencyKey, req.ExpectedContentSeq = newKey(), 4
	if _, err := w.k.Link(testCtx(t), req); err != nil {
		t.Fatalf("link with the desk's current content_seq: %v", err)
	}
	assertSeqs(t, "the desk, linked again", w.k, w.admin, w.desk, 5, 5)
	assertSeqs(t, "the calendar, still", w.k, w.admin, w.cal, 1, 1)
}

// A readers change and a move change who can read the Item and where it
// is: both bump. A move bumps the moved Item only.
func TestContentSeq_ReadersAndMoves(t *testing.T) {
	k := itemservice.New(pool)
	admin := person("admin")
	src := createAs(t, k, admin, "source", nil, nil)
	dst := createAs(t, k, admin, "destination", nil, nil)
	it := createAs(t, k, admin, "moving", nil, &src.ItemID)

	if _, err := k.Update(testCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: admin, ItemID: it.ItemID,
		Policy: &itemservice.PolicyPatch{Readers: readers(admin)},
	}); err != nil {
		t.Fatalf("narrow: %v", err)
	}
	assertSeqs(t, "after narrowing", k, admin, it.ItemID, 2, 2)
	if _, err := k.Update(testCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: admin, ItemID: it.ItemID, Policy: &itemservice.PolicyPatch{Inherit: true},
	}); err != nil {
		t.Fatalf("inherit: %v", err)
	}
	assertSeqs(t, "after inheriting again", k, admin, it.ItemID, 3, 3)

	if _, err := moveTo(t, k, admin, it.ItemID, &dst.ItemID, false); err != nil {
		t.Fatalf("move: %v", err)
	}
	assertSeqs(t, "the moved Item", k, admin, it.ItemID, 4, 4)
	assertSeqs(t, "the container it left", k, admin, src.ItemID, 1, 1)
	assertSeqs(t, "the container it entered", k, admin, dst.ItemID, 1, 1)

	// A move asked with an old read is stale.
	_, err := k.Update(testCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: admin, ItemID: it.ItemID, ExpectedContentSeq: 3,
		Move: &itemservice.Move{To: &src.ItemID},
	})
	assertStale(t, err)
}

// A set operation needs no if-match (it merges under the lock), but it
// bumps: a set can also be replaced by a list sent with one, and that
// replace must not drop a member someone added meanwhile.
func TestContentSeq_SetOperationsBump(t *testing.T) {
	w := newMVWorld(t)
	id, err := w.create(t, w.b, map[string]any{"one": l("a", "b")})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_, read := seqs(t, w.k, w.ana, id)

	// bob adds a member, with no if-match.
	if err := w.update(testCtx(t), w.bob, id, map[string]any{"one": add("c")}); err != nil {
		t.Fatalf("bob's add: %v", err)
	}
	assertSeqs(t, "after bob's add", w.k, w.ana, id, 2, 2)

	// ana replaces the set she read, with what she read: stale, and c stays.
	err = save(t, w.k, w.ana, id, read, w.named(map[string]any{"one": l("a", "b", "x")}))
	assertStale(t, err)
	if got := w.set(t, id, "one"); strings.Join(got, ",") != "a,b,c" {
		t.Errorf("the set = %v, want a, b, c: the stale replace wrote nothing", got)
	}
}

// A field only holders of a role write is a field a caller may write.
func TestContentSeq_ARoleWrittenFieldBumps(t *testing.T) {
	w := newSLAWorld(t)
	assertSeqs(t, "the policy, created", w.k, w.admin, w.policy, 1, 1)
	if err := save(t, w.k, w.admin, w.policy, 1, map[string]any{"f_resolution_s_p1": 900}); err != nil {
		t.Fatalf("the admin's save: %v", err)
	}
	assertSeqs(t, "the policy, after a target changed", w.k, w.admin, w.policy, 2, 2)
}

// A hook's write is judged by its field, not by its writer. Ticket v4's
// f_invoice_id is not marked writable_by: kernel, so a caller may write
// it and the hook's write bumps (journal: the fields the kernel writes
// that are not marked). hook_failed is a record: it never bumps.
func TestContentSeq_AHooksWrite_IsJudgedByItsField(t *testing.T) {
	k := itemservice.New(pool)
	id, ana := v4Approved(t, k) // 1 create, 2 open, 3 in_progress, 4 requested, 5 approved
	assertSeqs(t, "approved", k, ana, id, 5, 4)

	if _, err := writeInvoice(t, k, id, 5, "inv-1"); err != nil {
		t.Fatalf("the hook's write: %v", err)
	}
	assertSeqs(t, "after the hook wrote f_invoice_id (a field anyone may write)", k, ana, id, 6, 5)

	other, bea := v4Approved(t, k)
	if _, err := hookFailed(t, k, createInvoiceV4, other, 5, "budget_exhausted"); err != nil {
		t.Fatalf("hook_failed: %v", err)
	}
	assertSeqs(t, "after hook_failed", k, bea, other, 6, 4)
}

// --- holds ---

// A hold that opens with no patch, and is denied or fails, changed
// nothing a caller wrote: what was read before it opened is still good
// once it has resolved. (While it is open, the fields its check reads
// and the moves out of the state are HELD whatever the if-match says.)
func TestContentSeq_AHoldDeniedOrFailed_ChangedNothing(t *testing.T) {
	for _, v := range []itemservice.Verdict{itemservice.VerdictDeny, itemservice.VerdictError} {
		t.Run(string(v), func(t *testing.T) {
			k := itemservice.New(pool)
			id, ana := v3Ticket(t, k) // 1 create, 2 open, 3 in_progress
			_, read := seqs(t, k, ana, id)
			if read != 3 {
				t.Fatalf("content_seq %d before the hold, want 3", read)
			}
			hold := requestHold(t, k, ana, id) // 4: transition_requested, no patch
			assertSeqs(t, "the hold is open", k, ana, id, 4, 3)

			// A frozen field is HELD, not STALE, with a good if-match.
			err := save(t, k, ana, id, read, map[string]any{"f_amount": 5})
			assertHeldErr(t, "a frozen field while held", err, hold.HoldID)

			if _, err := resolve(t, k, id, hold.HoldID, v, nil, "credit_hold"); err != nil {
				t.Fatalf("resolve %s: %v", v, err)
			}
			assertSeqs(t, "the hold resolved "+string(v), k, ana, id, 5, 3)
			if err := save(t, k, ana, id, read, map[string]any{"f_amount": 5}); err != nil {
				t.Errorf("the save with what was read before the hold: %v; want it accepted", err)
			}
		})
	}
}

// A hold that resolves allow changed the state: stale.
func TestContentSeq_AHoldAllowed_ChangedTheState(t *testing.T) {
	k := itemservice.New(pool)
	id, ana := v3Ticket(t, k)
	_, read := seqs(t, k, ana, id)
	hold := requestHold(t, k, ana, id)
	if _, err := resolve(t, k, id, hold.HoldID, itemservice.VerdictAllow, nil, ""); err != nil {
		t.Fatalf("allow: %v", err)
	}
	assertSeqs(t, "allowed", k, ana, id, 5, 4)
	assertStale(t, save(t, k, ana, id, read, map[string]any{"f_title": "late"}))
}

// A request that carries a patch wrote a caller's field with it: the
// content moved when the hold opened.
func TestContentSeq_AHoldRequestedWithAPatch_Bumps(t *testing.T) {
	k := itemservice.New(pool)
	id, ana := v3Ticket(t, k)
	res, err := k.Transition(testCtx(t), itemservice.TransitionRequest{
		IdempotencyKey: newKey(), Principal: ana, ItemID: id, ToState: approveState,
		Fields:      map[string]any{"f_title": "Onboarding, approved"},
		Attribution: item.Attribution{CorrelationID: approveCorr},
	})
	if err != nil || res.Outcome != itemservice.OutcomePending {
		t.Fatalf("approve with a patch = %+v, %v; want pending", res, err)
	}
	assertSeqs(t, "the hold opened with a patch", k, ana, id, 4, 4)
}

// --- reads ---

// Search and List show the same content_seq as Get.
func TestContentSeq_SearchAndListShowIt(t *testing.T) {
	w := newSLAWorld(t)
	inc, _, _ := w.started(t, "P4", nil)
	if err := save(t, w.k, w.ana, inc, 1, map[string]any{"f_impact": "minor"}); err != nil {
		t.Fatalf("save: %v", err)
	}
	wantItem, wantContent := seqs(t, w.k, w.ana, inc) // 6, 2

	list, err := w.k.List(testCtx(t), itemservice.ListRequest{Principal: w.ana, ContainerID: &w.desk,
		Filter: itemservice.Filter{TypeIDs: []string{"incident"}}})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	title, _ := w.fields(t, inc)["f_title"].(string)
	found, err := w.k.Search(testCtx(t), itemservice.SearchRequest{Principal: w.ana, Query: strings.Fields(title)[2]})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	for what, items := range map[string][]item.Item{"List": list.Items, "Search": found.Items} {
		var got *item.Item
		for i := range items {
			if items[i].ID == inc {
				got = &items[i]
			}
		}
		switch {
		case got == nil:
			t.Errorf("%s did not return the incident", what)
		case got.ItemSeq != wantItem || got.ContentSeq != wantContent || wantContent != 2:
			t.Errorf("%s: item_seq %d, content_seq %d; want Get's %d and %d (content_seq 2)",
				what, got.ItemSeq, got.ContentSeq, wantItem, wantContent)
		}
	}
}

// --- nothing internal changes meaning ---

// The new if-match is left out of the idempotency hash when it is not
// sent, so every request that does not use it (the workflow's among
// them) hashes as it did before the field existed. The three values were
// computed on main, before this step.
func TestContentSeq_ARequestWithoutIt_HashesAsBefore(t *testing.T) {
	id := uuid.MustParse("11111111-2222-3333-4444-555555555555")
	dst := uuid.MustParse("66666666-7777-8888-9999-aaaaaaaaaaaa")
	p := item.Principal{Actor: "user:ana", Namespace: "acme"}
	a := item.Attribution{CorrelationID: "corr-1"}
	update := itemservice.UpdateRequest{IdempotencyKey: "k", Principal: p, ItemID: id,
		Fields: map[string]any{"f_title": "x"}, ExpectedItemSeq: 3, Attribution: a}
	for _, c := range []struct {
		op   string
		req  any
		want string
	}{
		{"update", update, "6860a187a39d2f735d5bf94bf6cc9848699a42eb6052c0c093af428decf31bd9"},
		{"transition", itemservice.TransitionRequest{IdempotencyKey: "k", Principal: p, ItemID: id, ToState: "open",
			ExpectedItemSeq: 3, Attribution: a}, "30a5643a11aeb7c4d6eb6644229303c244f9a7ce54f7e2a8c7f64bc586c1ce77"},
		{"link_added", itemservice.LinkRequest{IdempotencyKey: "k", Principal: p, SourceID: id, LinkType: "blocks",
			TargetID: dst, Attribution: a}, "43240f67814f21cf13efadf353cf562011d85db1c14fc161a88df8951f37c84f"},
	} {
		got, err := itemservice.RequestHash(c.op, c.req)
		if err != nil || got != c.want {
			t.Errorf("hash of a %s without the new if-match = %s, %v; want main's %s", c.op, got, err, c.want)
		}
	}
	// And it is part of the request when sent: the same key with another
	// expectation is another request.
	with := update
	with.ExpectedContentSeq = 2
	h1, _ := itemservice.RequestHash("update", update)
	h2, _ := itemservice.RequestHash("update", with)
	if h1 == h2 {
		t.Errorf("a request with ExpectedContentSeq hashes like one without")
	}
}

// --- the migration ---

// Existing Items start with content_seq = item_seq: what a client held
// before the migration stays current or stale exactly as it was. It runs
// on a database of its own, migrated to the last migration before this
// step, seeded by hand, then migrated to the end.
func TestContentSeq_MigrationBackfillsFromItemSeq(t *testing.T) {
	ctx := testCtx(t)
	dbName := "contentseq_" + strings.ReplaceAll(uuid.NewString()[:8], "-", "")
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
	const lastBefore = 11 // 00011_poc_step2_part2_field_deltas.sql
	if _, err := provider.UpTo(ctx, lastBefore); err != nil {
		t.Fatalf("migrate to %d: %v", lastBefore, err)
	}
	quiet, busy := uuid.New(), uuid.New()
	for id, seq := range map[uuid.UUID]int64{quiet: 1, busy: 20} {
		if _, err := db.ExecContext(ctx,
			`insert into items (id, namespace_id, type_id, type_version, scope_id, state, item_seq, created_at, created_by)
			 select $1, 'default', 'ticket', 1, id, 'open', $2, now(), 'user:ana'
			 from scopes where namespace_id = 'default' and parent_id is null`, id, seq); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("migrate to the end: %v", err)
	}
	for id, want := range map[uuid.UUID]int64{quiet: 1, busy: 20} {
		var got int64
		if err := db.QueryRowContext(ctx, `select content_seq from items where id = $1`, id).Scan(&got); err != nil {
			t.Fatalf("read content_seq: %v", err)
		}
		if got != want {
			t.Errorf("content_seq = %d after the migration, want the Item's item_seq %d", got, want)
		}
	}
	// Down drops the column and nothing else. By version, not one step:
	// a later migration must not change what this undoes.
	if _, err := provider.DownTo(ctx, lastBefore); err != nil {
		t.Fatalf("migrate down: %v", err)
	}
	var n int
	if err := db.QueryRowContext(ctx, `select count(*) from information_schema.columns
		where table_name = 'items' and column_name = 'content_seq'`).Scan(&n); err != nil || n != 0 {
		t.Errorf("after down: %d content_seq columns (%v), want 0", n, err)
	}
}
