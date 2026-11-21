// The lock order of Jira 2.4b, PR 1: container before child, everywhere.
// A decision on a child locks its container FOR SHARE first, then the
// child FOR UPDATE; a container's transition locks the container, then
// reads its children FOR SHARE. Without one order, a container's move
// (holding the container, reading the children) and a child's decision
// (holding the child, reading the container) deadlock.
//
// Postgres breaks a deadlock by aborting one side, and the write path
// retries that, so a deadlock is invisible in the results: the tests read
// the write path's own count of deadlock retries, before and after, and
// want it unchanged. The order (design.md): the container (FOR SHARE) when
// the write reads it; then the Item with its linked rows, in one pass in
// UUID order, as before; then its children, in UUID order.
package itemservice_test

import (
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
)

// expected reports whether an error is one of the refusals a race may give:
// a guard that no longer holds, a move already made, a decision on a bean
// whose jar has moved on.
func expected(err error) bool {
	if err == nil {
		return true
	}
	var rej *itemservice.RejectedError
	return errors.As(err, &rej) && (rej.Code == "guard_failed" || rej.Code == "no_transition" || rej.Code == "invalid_transition")
}

// Three decisions, their reactions at the container, and two hand moves of
// the container, all at once, repeated: never a deadlock, and the jar moves
// to full exactly once.
func TestJira24b_Locks_DecisionsAndTheContainerMove_NeverDeadlock(t *testing.T) {
	w := newWorld4b(t)
	before := itemservice.DeadlockRetries()
	for round := range 12 {
		jar, beans := w.openJar(t)
		var wg sync.WaitGroup
		errs := make(chan error, 16)
		for _, who := range []item.Principal{w.bob, w.dana, w.erin} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				bean := beans[who.Actor]
				res, err := w.k.Transition(testCtx(t), itemservice.TransitionRequest{IdempotencyKey: newKey(), Principal: who,
					ItemID: bean, ToState: "yes"})
				errs <- err
				if err != nil {
					return
				}
				for _, r := range []string{"tip", "tip_loud"} {
					_, err := w.k.Internal().ReactMove(testCtx(t), entered(bean, res.ItemSeq, r))
					errs <- err
				}
			}()
		}
		for range 2 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := w.k.Transition(testCtx(t), itemservice.TransitionRequest{IdempotencyKey: newKey(), Principal: w.lea,
					ItemID: jar, ToState: "full"})
				errs <- err
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if !expected(err) {
				t.Fatalf("round %d: %v", round, err)
			}
		}
		var full int
		if err := pool.QueryRow(testCtx(t), `select count(*) from item_events where item_id = $1 and op = 'transition'
			and payload->>'to' = 'full'`, jar).Scan(&full); err != nil {
			t.Fatal(err)
		}
		if st := w.item(t, jar).State; st != "full" || full != 1 {
			t.Errorf("round %d: the jar is %s after %d moves to full; want full, once", round, st, full)
		}
	}
	if after := itemservice.DeadlockRetries(); after != before {
		t.Errorf("%d deadlocks retried during the rounds; want none", after-before)
	}
}

// Children older than their container (moved in, so their IDs sort first):
// decisions, the set reactions that write the container, and the
// container's own move, at once. The set reaction too takes the container
// first, whatever the IDs' order.
func TestJira24b_Locks_OlderChildren_SetReactions_NeverDeadlock(t *testing.T) {
	w := newWorld4b(t)
	before := itemservice.DeadlockRetries()
	for round := range 12 {
		var peas []uuid.UUID
		for i := range 3 {
			peas = append(peas, w.mk(t, w.lea, "pea", nil, map[string]any{"f_name": string(rune('a' + i))}))
		}
		crate := w.mk(t, w.lea, "crate", nil, map[string]any{"f_name": "c"})
		for _, p := range peas {
			if _, err := w.k.Update(testCtx(t), itemservice.UpdateRequest{IdempotencyKey: newKey(), Principal: w.lea, ItemID: p,
				Move: &itemservice.Move{To: &crate}}); err != nil {
				t.Fatal(err)
			}
		}
		var wg sync.WaitGroup
		errs := make(chan error, 16)
		for _, p := range peas {
			wg.Add(1)
			go func() {
				defer wg.Done()
				res, err := w.k.Transition(testCtx(t), itemservice.TransitionRequest{IdempotencyKey: newKey(), Principal: w.sam,
					ItemID: p, ToState: "done"})
				errs <- err
				if err != nil {
					return
				}
				_, err = w.k.Internal().ReactSet(testCtx(t), entered(p, res.ItemSeq, "mark"))
				errs <- err
			}()
		}
		for range 2 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := w.k.Transition(testCtx(t), itemservice.TransitionRequest{IdempotencyKey: newKey(), Principal: w.lea,
					ItemID: crate, ToState: "closed"})
				errs <- err
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if !expected(err) {
				t.Fatalf("round %d: %v", round, err)
			}
		}
	}
	if after := itemservice.DeadlockRetries(); after != before {
		t.Errorf("%d deadlocks retried during the rounds; want none", after-before)
	}
}

// duo: Items linked to each other both ways, whose closing guard reads the
// links (so a transition locks both ends, in one UUID-ordered pass); a duo
// also contains peas, whose decision reads their container.
const duo4b = `
id: duo
version: 1
initial_state: open
fields:
  - {id: f_name, type: string}
states:
  - {id: open,   category: active}
  - {id: closed, category: done}
links:
  - {name: pairs, inverse: paired_by, targets: [duo]}
transitions:
  - {from: open, to: closed, guard: 'links.pairs.size() >= 0 && links.paired_by.size() >= 0'}
`

// Two duos linked to each other both ways close at the same moment, while
// a pea under one of them is decided: never a deadlock.
func TestJira24b_Locks_LinkedBothWays_AndAChildDecision_NeverDeadlock(t *testing.T) {
	w := newWorld4b(t)
	registerSrc(t, w.k, duo4b)
	before := itemservice.DeadlockRetries()
	for round := range 15 {
		a := w.mk(t, w.lea, "duo", nil, map[string]any{"f_name": "a"})
		b := w.mk(t, w.lea, "duo", nil, map[string]any{"f_name": "b"})
		for _, l := range [][2]uuid.UUID{{a, b}, {b, a}} {
			if _, err := w.k.Link(testCtx(t), itemservice.LinkRequest{IdempotencyKey: newKey(), Principal: w.lea,
				SourceID: l[0], LinkType: "pairs", TargetID: l[1]}); err != nil {
				t.Fatal(err)
			}
		}
		pea := w.mk(t, w.lea, "pea", &a, map[string]any{"f_name": "p"})
		var wg sync.WaitGroup
		errs := make(chan error, 4)
		for _, id := range []uuid.UUID{a, b, pea} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				to := "closed"
				if id == pea {
					to = "done"
				}
				_, err := w.k.Transition(testCtx(t), itemservice.TransitionRequest{IdempotencyKey: newKey(), Principal: w.sam,
					ItemID: id, ToState: to})
				errs <- err
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if !expected(err) {
				t.Fatalf("round %d: %v", round, err)
			}
		}
	}
	if after := itemservice.DeadlockRetries(); after != before {
		t.Errorf("%d deadlocks retried during the rounds; want none", after-before)
	}
}
