// Tests for proof-of-concept Step 2, part 2, part two of three: the held
// deduction and its undo, through the item service. The tests play the
// workflow: they ask for the approval (a hold), call Internal().React as
// the deduct reaction, resolve the hold as the reaction, and call
// Internal().Undo as the refund reaction.
//
//   - React adds the request's f_days to its balance's f_used: one write,
//     on the balance only, as reaction:vacation_request:deduct, on behalf of
//     the approver, caused by the request's transition_requested event,
//     under the key deduct:<request>:<requested seq>:<balance>. A retry
//     writes nothing more. History shows the result and the delta.
//   - The balance's invariant refuses an overdraft: DenyReason gives
//     insufficient_balance, and nothing is written (the key is not used).
//   - React commits only while its own hold is open; it checks the link
//     (not_linked) and the target's consent (reaction_not_accepted).
//   - Undo reverses a committed deduction by its recorded delta, once per
//     deduction, whichever path asks (the hold closed non-allow, or the
//     approved request withdrawn), under <reservation key>:refund. Without
//     a committed deduction it writes nothing. It does not check the link.
//     It reverses the deduction of the hold it names: for a withdrawal,
//     the hold that entered approved (StateEntry.EnteredHold).
//   - StateEntry.PendingUndos lists the undos not yet done, so a workflow
//     run after a crash resumes them.
//   - An undo the target refuses is recorded on the request as
//     reaction_failed, once.
//   - Two approvals on one balance never overdraw it; an escape racing a
//     deduction never leaves days taken.
package itemservice_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

var (
	deductP = item.Principal{Actor: "reaction:vacation_request:deduct"}
	refundP = item.Principal{Actor: "reaction:vacation_request:refund"}
)

func reactionCause(reaction string, id uuid.UUID, seq int64, hop int) item.Attribution {
	return item.Attribution{
		OriginatingHook: "reaction:vacation_request@v2:" + reaction,
		CausationID:     fmt.Sprintf("%s:%d", id, seq),
		Hop:             hop,
	}
}

func hopOf(t *testing.T, id uuid.UUID, seq int64) int {
	t.Helper()
	_, a := attribution(t, id, seq)
	return a.Hop
}

// askApproval asks for submitted -> approved as approver: a hold.
func (w leaveWorld) askApproval(t *testing.T, approver item.Principal, id uuid.UUID) itemservice.TransitionResult {
	t.Helper()
	res, err := w.move(t, approver, id, "approved", nil)
	if err != nil || res.Outcome != itemservice.OutcomePending || res.HoldID == uuid.Nil {
		t.Fatalf("approve = %+v, %v; want pending with a hold (deduct)", res, err)
	}
	return res
}

func (w leaveWorld) react(t *testing.T, id uuid.UUID, ask itemservice.TransitionResult) (itemservice.ReactResult, error) {
	t.Helper()
	return w.k.Internal().React(testCtx(t), itemservice.ReactRequest{
		Principal: deductP, SourceID: id, HoldID: ask.HoldID,
		Attribution: reactionCause("deduct", id, ask.ItemSeq, hopOf(t, id, ask.ItemSeq)+1),
	})
}

// resolveAsDeduct settles the approval hold as the deduct reaction.
func (w leaveWorld) resolveAsDeduct(t *testing.T, id uuid.UUID, ask itemservice.TransitionResult,
	v itemservice.Verdict, reason string) (itemservice.WriteResult, error) {
	t.Helper()
	return w.k.Internal().ResolveHold(testCtx(t), itemservice.ResolveHoldRequest{
		IdempotencyKey: "hold:" + ask.HoldID.String(), Principal: deductP, ItemID: id, HoldID: ask.HoldID,
		Verdict: v, Reason: reason, Attribution: reactionCause("deduct", id, ask.ItemSeq, hopOf(t, id, ask.ItemSeq)+1),
	})
}

// approve plays the whole held deduction, as RunReaction will: React, then
// allow, or deny with the target's refusal. It returns the hold.
func (w leaveWorld) approve(t *testing.T, approver item.Principal, id uuid.UUID) (itemservice.TransitionResult, *item.Hold) {
	t.Helper()
	ask := w.askApproval(t, approver, id)
	_, err := w.react(t, id, ask)
	verdict, reason := itemservice.VerdictAllow, ""
	if err != nil {
		var ok bool
		if reason, ok = itemservice.DenyReason(err); !ok {
			t.Fatalf("React: %v, which is not a refusal", err)
		}
		verdict = itemservice.VerdictDeny
	}
	if _, err := w.resolveAsDeduct(t, id, ask, verdict, reason); err != nil {
		t.Fatalf("resolve the approval as deduct (%s %s): %v", verdict, reason, err)
	}
	return ask, w.holdNow(t, id)
}

func (w leaveWorld) undo(t *testing.T, id, holdID uuid.UUID, causeSeq int64) (itemservice.UndoResult, error) {
	t.Helper()
	return w.k.Internal().Undo(testCtx(t), itemservice.UndoRequest{
		Principal: refundP, SourceID: id, HoldID: holdID,
		Attribution: reactionCause("refund", id, causeSeq, hopOf(t, id, causeSeq)+1),
	})
}

func (w leaveWorld) pendingUndos(t *testing.T, id uuid.UUID) []itemservice.PendingUndo {
	t.Helper()
	se, err := w.k.StateEntry(testCtx(t), id)
	if err != nil {
		t.Fatalf("StateEntry: %v", err)
	}
	return se.PendingUndos
}

func (w leaveWorld) itemSeq(t *testing.T, id uuid.UUID) int64 {
	t.Helper()
	return int64(count(t, `select item_seq from items where id = $1`, id))
}

// eventsBy counts a balance's events written by a reaction.
func eventsBy(t *testing.T, bal uuid.UUID, actor item.Principal) int {
	t.Helper()
	return count(t, `select count(*) from item_events where item_id = $1 and actor = $2`, bal, actor.Actor)
}

// valuesAndDeltas reads an event's values and deltas from item_event_values.
func valuesAndDeltas(t *testing.T, id uuid.UUID, seq int64) (values, deltas map[string]any) {
	t.Helper()
	var v, d []byte
	err := pool.QueryRow(testCtx(t),
		`select field_values, field_deltas from item_event_values where item_id = $1 and item_seq = $2`, id, seq,
	).Scan(&v, &d)
	if err != nil {
		t.Fatalf("read event values %d: %v", seq, err)
	}
	_ = json.Unmarshal(v, &values)
	_ = json.Unmarshal(d, &deltas)
	return values, deltas
}

func idempotencyRows(t *testing.T, actor item.Principal, key string) int {
	t.Helper()
	return count(t, `select count(*) from idempotency where actor = $1 and key = $2`, actor.Actor, key)
}

func assertNumber(t *testing.T, what string, got any, want int) {
	t.Helper()
	if n, ok := typedefInt(got); !ok || n != int64(want) {
		t.Errorf("%s = %v, want %d", what, got, want)
	}
}

// --- the deduction ---

// Approving deducts f_days from the requester's balance, once. bob cannot
// see ana's balance; the reaction skips visibility.
func TestDeduct_ApprovalDeductsOnce(t *testing.T) {
	w := newLeaveWorld(t)
	bal := w.balance(t, w.ana, 25)
	id := w.submitted(t, w.ana, w.bob, 5)
	ask := w.askApproval(t, w.bob, id)

	res, err := w.react(t, id, ask)
	if err != nil {
		t.Fatalf("React: %v", err)
	}
	wantKey := fmt.Sprintf("deduct:%s:%d:%s", id, ask.ItemSeq, bal)
	if res.Target != bal || res.Key != wantKey || res.ItemID != bal {
		t.Errorf("React = %+v; want target %s, key %s", res, bal, wantKey)
	}
	assertUsed(t, w, bal, 5)
	if w.state(t, id) != "submitted" {
		t.Errorf("request state = %s, want submitted until the hold resolves", w.state(t, id))
	}

	// The event on the balance.
	op, payload := event(t, bal, res.ItemSeq)
	if op != "update" || !slices.Equal(anyStrings(payload["field_ids"]), []string{"f_used"}) ||
		!slices.Equal(anyStrings(payload["added_field_ids"]), []string{"f_used"}) {
		t.Errorf("deduction event = %s %v; want an update adding to f_used", op, payload)
	}
	actor, a := attribution(t, bal, res.ItemSeq)
	want := item.Attribution{OriginatingHook: "reaction:vacation_request@v2:deduct",
		CausationID: fmt.Sprintf("%s:%d", id, ask.ItemSeq), Hop: 1}
	if actor != deductP.Actor || a != want {
		t.Errorf("deduction attribution = %s %+v; want %s %+v", actor, a, deductP.Actor, want)
	}
	if obo := onBehalfOf(t, bal, res.ItemSeq); obo != w.bob.Actor {
		t.Errorf("on_behalf_of = %q, want the approver %s", obo, w.bob.Actor)
	}
	values, deltas := valuesAndDeltas(t, bal, res.ItemSeq)
	assertNumber(t, "recorded f_used", values["f_used"], 5)
	assertNumber(t, "recorded delta", deltas["f_used"], 5)
	if n := idempotencyRows(t, deductP, wantKey); n != 1 {
		t.Errorf("reservation key rows = %d, want 1", n)
	}

	// A retry writes nothing more.
	again, err := w.react(t, id, ask)
	if err != nil || again != res {
		t.Errorf("React again = %+v, %v; want the first result %+v", again, err, res)
	}
	if eventsBy(t, bal, deductP) != 1 {
		t.Errorf("deduction events = %d, want 1", eventsBy(t, bal, deductP))
	}

	// The reaction resolves the hold: the approval applies, as the reaction,
	// on behalf of bob.
	dec, err := w.resolveAsDeduct(t, id, ask, itemservice.VerdictAllow, "")
	if err != nil {
		t.Fatalf("resolve allow: %v", err)
	}
	if w.state(t, id) != "approved" {
		t.Errorf("request state = %s, want approved", w.state(t, id))
	}
	if actor, _ := attribution(t, id, dec.ItemSeq); actor != deductP.Actor || onBehalfOf(t, id, dec.ItemSeq) != w.bob.Actor {
		t.Errorf("approval event by %s for %s; want %s for %s", actor, onBehalfOf(t, id, dec.ItemSeq), deductP.Actor, w.bob.Actor)
	}

	// History shows the result and the delta.
	page, err := w.k.ListEvents(testCtx(t), itemservice.ListEventsRequest{Principal: inDefault(w.hana), ItemID: bal})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	last := page.Events[len(page.Events)-1]
	assertNumber(t, "history value", last.Values["f_used"], 5)
	assertNumber(t, "history added", last.Added["f_used"], 5)
}

// A second approval adds to the first; each event records its own delta.
func TestDeduct_DeductionsAddUp(t *testing.T) {
	w := newLeaveWorld(t)
	bal := w.balance(t, w.ana, 25)
	if _, h := w.approve(t, w.bob, w.submitted(t, w.ana, w.bob, 5)); h.Status != item.HoldAllowed {
		t.Fatalf("first approval: %+v", h)
	}
	id := w.submitted(t, w.ana, w.bob, 3)
	ask := w.askApproval(t, w.bob, id)
	res, err := w.react(t, id, ask)
	if err != nil {
		t.Fatalf("React: %v", err)
	}
	values, deltas := valuesAndDeltas(t, bal, res.ItemSeq)
	assertNumber(t, "f_used", values["f_used"], 8)
	assertNumber(t, "delta", deltas["f_used"], 3)
}

// Beyond the allowance: refused by the invariant, named; nothing written,
// the key unused; the approval is denied insufficient_balance and the
// request stays submitted. Exactly the allowance is fine.
func TestDeduct_InsufficientBalanceDenies(t *testing.T) {
	w := newLeaveWorld(t)
	bal := w.balance(t, w.ana, 4)
	id := w.submitted(t, w.ana, w.bob, 5)
	ask := w.askApproval(t, w.bob, id)
	seq := w.itemSeq(t, bal)

	_, err := w.react(t, id, ask)
	assertInvariantFailed(t, "React beyond the allowance", err, "insufficient_balance")
	if reason, ok := itemservice.DenyReason(err); !ok || reason != "insufficient_balance" {
		t.Errorf("DenyReason = %q, %v; want insufficient_balance", reason, ok)
	}
	if w.itemSeq(t, bal) != seq || w.used(t, bal) != nil {
		t.Errorf("balance changed: item_seq %d (was %d), f_used %v", w.itemSeq(t, bal), seq, w.used(t, bal))
	}
	if n := idempotencyRows(t, deductP, fmt.Sprintf("deduct:%s:%d:%s", id, ask.ItemSeq, bal)); n != 0 {
		t.Errorf("reservation key rows = %d, want 0: a refused write uses no key", n)
	}
	if _, err := w.resolveAsDeduct(t, id, ask, itemservice.VerdictDeny, "insufficient_balance"); err != nil {
		t.Fatalf("resolve deny: %v", err)
	}
	if h := w.holdNow(t, id); h.Status != item.HoldDenied || h.Reason != "insufficient_balance" || w.state(t, id) != "submitted" {
		t.Errorf("hold %+v, state %s; want denied insufficient_balance, submitted", h, w.state(t, id))
	}

	w2 := newLeaveWorld(t)
	bal2 := w2.balance(t, w2.ana, 5)
	if _, h := w2.approve(t, w2.bob, w2.submitted(t, w2.ana, w2.bob, 5)); h.Status != item.HoldAllowed {
		t.Errorf("exactly the allowance: hold %+v, want allowed", h)
	}
	assertUsed(t, w2, bal2, 5)
}

// React commits only while its own hold is open: not after an escape
// closed it, and not for an earlier hold while a later one is open.
func TestDeduct_OnlyWhileItsHoldIsOpen(t *testing.T) {
	w := newLeaveWorld(t)
	bal := w.balance(t, w.ana, 25)
	id := w.submitted(t, w.ana, w.bob, 5)
	ask := w.askApproval(t, w.bob, id)
	if _, err := w.move(t, w.ana, id, "withdrawn", nil); err != nil {
		t.Fatalf("ana withdraws during the approval (an escape): %v", err)
	}
	if h := w.holdNow(t, id); h.Status != item.HoldCanceled {
		t.Fatalf("hold = %+v, want canceled", h)
	}
	seq := w.itemSeq(t, bal)
	if _, err := w.react(t, id, ask); !errors.Is(err, itemservice.ErrStale) {
		t.Errorf("React after the escape: %v, want ErrStale (the hold is closed)", err)
	}
	if w.itemSeq(t, bal) != seq {
		t.Error("React after the escape wrote to the balance")
	}

	// A denied hold, then a new one: the old one's React may not commit.
	w2 := newLeaveWorld(t)
	bal2 := w2.balance(t, w2.ana, 25)
	id2 := w2.submitted(t, w2.ana, w2.bob, 5)
	first := w2.askApproval(t, w2.bob, id2)
	if _, err := w2.resolveAsDeduct(t, id2, first, itemservice.VerdictError, "budget_exhausted"); err != nil {
		t.Fatalf("resolve error: %v", err)
	}
	second := w2.askApproval(t, w2.bob, id2)
	if _, err := w2.react(t, id2, first); !errors.Is(err, itemservice.ErrStale) {
		t.Errorf("React for the first hold while the second is open: %v, want ErrStale", err)
	}
	assertUsed(t, w2, bal2, 0)
	if _, err := w2.react(t, id2, second); err != nil {
		t.Errorf("React for the open hold: %v", err)
	}
	assertUsed(t, w2, bal2, 5)
}

// The link is checked when the reaction writes: gone, nothing is written.
func TestDeduct_LinkGone(t *testing.T) {
	w := newLeaveWorld(t)
	bal := w.balance(t, w.ana, 25)
	id := w.submitted(t, w.ana, w.bob, 5)
	ask := w.askApproval(t, w.bob, id)
	if _, err := pool.Exec(testCtx(t), `delete from links where source_id = $1`, id); err != nil {
		t.Fatal(err)
	}
	_, err := w.react(t, id, ask)
	assertRejectedCode(t, "React without the link", err, itemservice.CodeNotLinked)
	if reason, ok := itemservice.DenyReason(err); !ok || reason != "not_linked" {
		t.Errorf("DenyReason = %q, %v; want not_linked", reason, ok)
	}
	assertUsed(t, w, bal, 0)
}

// The target's own Type version decides what it accepts, at write time.
// leave_balance v90 (registered alone, as a later version might be)
// accepts only the refund.
func TestDeduct_TheTargetsVersionMustAcceptIt(t *testing.T) {
	w := newLeaveWorld(t)
	registerInline(t, w.k, balanceVersion(90, "  vacation_request:refund: [f_used]"))
	bal := w.balanceOn(t, 90, w.ana, 25)
	id := w.submitted(t, w.ana, w.bob, 5)
	ask := w.askApproval(t, w.bob, id)
	_, err := w.react(t, id, ask)
	assertRejectedCode(t, "React on a balance that does not accept deduct", err, itemservice.CodeReactionNotAccepted)
	if reason, ok := itemservice.DenyReason(err); !ok || reason != "reaction_not_accepted" {
		t.Errorf("DenyReason = %q, %v; want reaction_not_accepted", reason, ok)
	}
	assertUsed(t, w, bal, 0)
}

// balanceVersion is leave_balance at another version, with the given
// accepts_reactions lines.
func balanceVersion(v int, accepts string) string {
	return fmt.Sprintf(`
id: leave_balance
version: %d
initial_state: open
fields:
  - {id: f_owner,     type: principal, writable_by: ['role:hr_admin']}
  - {id: f_allowance, type: int,       writable_by: ['role:hr_admin']}
  - {id: f_used,      type: int,       writable_by: kernel}
states:
  - {id: open,   category: active}
  - {id: closed, category: done}
transitions:
  - {from: open, to: closed, allowed: ['role:hr_admin']}
invariants:
  - {id: insufficient_balance, rule: 'f_used == null || f_used <= f_allowance'}
accepts_reactions:
%s
`, v, accepts)
}

func registerInline(t *testing.T, k *itemservice.Service, src string) {
	t.Helper()
	ty, err := typedef.Parse([]byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := k.RegisterType(testCtx(t), ty); err != nil {
		t.Fatalf("register %s v%d: %v", ty.ID, ty.Version, err)
	}
}

func (w leaveWorld) balanceOn(t *testing.T, version int, owner item.Principal, allowance int) uuid.UUID {
	t.Helper()
	res, err := w.k.Create(testCtx(t), itemservice.CreateRequest{
		IdempotencyKey: newKey(), Principal: w.hana, TypeID: "leave_balance", TypeVersion: version,
		Fields: map[string]any{"f_owner": owner.Actor, "f_allowance": allowance},
	})
	if err != nil {
		t.Fatalf("create balance v%d: %v", version, err)
	}
	return res.ItemID
}

// React is the held reaction's door, for its own cause only.
func TestDeduct_ReactIsChecked(t *testing.T) {
	w := newLeaveWorld(t)
	w.balance(t, w.ana, 25)
	id := w.submitted(t, w.ana, w.bob, 5)
	ask := w.askApproval(t, w.bob, id)
	good := itemservice.ReactRequest{Principal: deductP, SourceID: id, HoldID: ask.HoldID,
		Attribution: reactionCause("deduct", id, ask.ItemSeq, 1)}
	cases := []struct {
		name   string
		mutate func(r *itemservice.ReactRequest)
		want   error
	}{
		{"a hook", func(r *itemservice.ReactRequest) { r.Principal = hrCheck }, itemservice.ErrInvalid},
		{"a user", func(r *itemservice.ReactRequest) { r.Principal = w.bob }, itemservice.ErrInvalid},
		{"the refund (an after reaction)", func(r *itemservice.ReactRequest) {
			r.Principal = refundP
			r.Attribution.OriginatingHook = "reaction:vacation_request@v2:refund"
		}, itemservice.ErrInvalid},
		{"another Type's reaction", func(r *itemservice.ReactRequest) {
			r.Principal = item.Principal{Actor: "reaction:ticket:deduct"}
			r.Attribution.OriginatingHook = "reaction:ticket@v2:deduct"
		}, itemservice.ErrInvalid},
		{"originating_hook names another version", func(r *itemservice.ReactRequest) {
			r.Attribution.OriginatingHook = "reaction:vacation_request@v1:deduct"
		}, itemservice.ErrInvalid},
		{"originating_hook names another reaction", func(r *itemservice.ReactRequest) {
			r.Attribution.OriginatingHook = "reaction:vacation_request@v2:refund"
		}, itemservice.ErrInvalid},
		{"caused by another event than the request", func(r *itemservice.ReactRequest) {
			r.Attribution.CausationID = fmt.Sprintf("%s:%d", id, ask.ItemSeq-1)
		}, itemservice.ErrInvalid},
		{"on behalf of someone", func(r *itemservice.ReactRequest) { r.Principal.OnBehalfOf = w.ana.Actor }, itemservice.ErrInvalid},
		{"no hop", func(r *itemservice.ReactRequest) { r.Attribution.Hop = 0 }, itemservice.ErrInvalid},
		{"no such hold", func(r *itemservice.ReactRequest) { r.HoldID = uuid.New() }, itemservice.ErrNotFound},
		{"too deep", func(r *itemservice.ReactRequest) { r.Attribution.Hop = itemservice.MaxHop + 1 }, itemservice.ErrRejected},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := good
			c.mutate(&r)
			if _, err := w.k.Internal().React(testCtx(t), r); !errors.Is(err, c.want) {
				t.Errorf("React = %v, want %v", err, c.want)
			}
		})
	}
	if _, err := w.k.Internal().React(testCtx(t), good); err != nil {
		t.Errorf("the good request after all that: %v", err)
	}
}

// The internal door's other operations stay as they were: a reaction
// writes only through React, Undo and its own hold's ResolveHold, and
// hook: and system: principals keep the same-Item, same-Type cause rule.
func TestInternal_ReactionPrincipalsOnlyWhereRelaxed(t *testing.T) {
	w := newLeaveWorld(t)
	bal := w.balance(t, w.ana, 25)
	id := w.submitted(t, w.ana, w.bob, 5)
	ask := w.askApproval(t, w.bob, id)
	cause := reactionCause("deduct", id, ask.ItemSeq, 1)

	if _, err := w.k.Internal().Update(testCtx(t), itemservice.UpdateRequest{IdempotencyKey: newKey(), Principal: deductP,
		ItemID: bal, Fields: map[string]any{"f_used": 5}, Attribution: cause}); !errors.Is(err, itemservice.ErrInvalid) {
		t.Errorf("a reaction through Internal().Update: %v, want ErrInvalid", err)
	}
	if _, err := w.k.Internal().Transition(testCtx(t), itemservice.TransitionRequest{IdempotencyKey: newKey(), Principal: deductP,
		ItemID: bal, ToState: "closed", Attribution: cause}); !errors.Is(err, itemservice.ErrInvalid) {
		t.Errorf("a reaction through Internal().Transition: %v, want ErrInvalid", err)
	}
	// A hook acting on another Item than its cause's, or on another Type: refused as before.
	if _, err := w.k.Internal().Update(testCtx(t), itemservice.UpdateRequest{IdempotencyKey: newKey(), Principal: hrCheck,
		ItemID: bal, Fields: map[string]any{"f_allowance": 99}, Attribution: hrCauseV2(id, 1)}); !errors.Is(err, itemservice.ErrInvalid) {
		t.Errorf("a hook writing another Item than its cause's: %v, want ErrInvalid", err)
	}
	timer := item.Principal{Actor: "system:timer"}
	if _, err := w.k.Internal().Transition(testCtx(t), itemservice.TransitionRequest{IdempotencyKey: newKey(), Principal: timer,
		ItemID: bal, ToState: "closed", Attribution: item.Attribution{OriginatingHook: "timer:vacation_request@v2:submitted",
			CausationID: fmt.Sprintf("%s:1", id), Hop: 1}}); !errors.Is(err, itemservice.ErrInvalid) {
		t.Errorf("the timer moving another Item than its cause's: %v, want ErrInvalid", err)
	}
	// Each check resolves only its own hold: deduct cannot resolve the HR
	// check's hold, and the HR check cannot resolve deduct's.
	if _, err := w.k.Internal().ResolveHold(testCtx(t), itemservice.ResolveHoldRequest{
		IdempotencyKey: "hold:" + ask.HoldID.String(), Principal: hrCheck, ItemID: id, HoldID: ask.HoldID,
		Verdict: itemservice.VerdictAllow, Attribution: hrCauseV2(id, ask.ItemSeq)}); !errors.Is(err, itemservice.ErrInvalid) {
		t.Errorf("the HR check resolving deduct's hold: %v, want ErrInvalid", err)
	}
	if _, err := w.resolveAsDeduct(t, id, ask, itemservice.VerdictCorrect, ""); !errors.Is(err, itemservice.ErrInvalid) {
		t.Errorf("deduct correcting its hold: %v, want ErrInvalid (a reaction allows, denies or errs)", err)
	}
	if w.state(t, id) != "submitted" {
		t.Errorf("state = %s, want submitted", w.state(t, id))
	}
}

// --- the undo ---

// Withdrawn during the hold, after the deduction committed: the hold is
// canceled, the deduction is pending an undo, and the undo refunds it.
func TestUndo_EscapeAfterTheDeduction(t *testing.T) {
	w := newLeaveWorld(t)
	bal := w.balance(t, w.ana, 25)
	id := w.submitted(t, w.ana, w.bob, 5)
	ask := w.askApproval(t, w.bob, id)
	reserved, err := w.react(t, id, ask)
	if err != nil {
		t.Fatalf("React: %v", err)
	}
	withdraw, err := w.move(t, w.ana, id, "withdrawn", nil)
	if err != nil {
		t.Fatalf("ana withdraws: %v", err)
	}
	if h := w.holdNow(t, id); h.Status != item.HoldCanceled {
		t.Fatalf("hold = %+v, want canceled", h)
	}
	if _, err := w.resolveAsDeduct(t, id, ask, itemservice.VerdictAllow, ""); !errors.Is(err, itemservice.ErrStale) {
		t.Errorf("resolving the canceled hold: %v, want ErrStale", err)
	}
	pending := w.pendingUndos(t, id)
	want := []itemservice.PendingUndo{{Reaction: "refund", HoldID: ask.HoldID, CauseSeq: withdraw.ItemSeq, Hop: 0}}
	if !slices.Equal(pending, want) {
		t.Errorf("PendingUndos = %+v, want %+v", pending, want)
	}

	res, err := w.undo(t, id, ask.HoldID, withdraw.ItemSeq)
	if err != nil {
		t.Fatalf("Undo: %v", err)
	}
	if !res.Undone || res.Target != bal || res.Key != reserved.Key+":refund" {
		t.Errorf("Undo = %+v; want undone on %s under %s:refund", res, bal, reserved.Key)
	}
	assertUsed(t, w, bal, 0)
	op, payload := event(t, bal, res.ItemSeq)
	if op != "update" || payload["undoes"] != fmt.Sprintf("%s:%d", bal, reserved.ItemSeq) ||
		!slices.Equal(anyStrings(payload["added_field_ids"]), []string{"f_used"}) {
		t.Errorf("refund event = %s %v; want an update adding to f_used, undoing %s:%d", op, payload, bal, reserved.ItemSeq)
	}
	actor, a := attribution(t, bal, res.ItemSeq)
	wantA := item.Attribution{OriginatingHook: "reaction:vacation_request@v2:refund",
		CausationID: fmt.Sprintf("%s:%d", id, withdraw.ItemSeq), Hop: 1}
	if actor != refundP.Actor || a != wantA || onBehalfOf(t, bal, res.ItemSeq) != w.ana.Actor {
		t.Errorf("refund attribution = %s %+v for %s; want %s %+v for %s",
			actor, a, onBehalfOf(t, bal, res.ItemSeq), refundP.Actor, wantA, w.ana.Actor)
	}
	values, deltas := valuesAndDeltas(t, bal, res.ItemSeq)
	assertNumber(t, "f_used after the refund", values["f_used"], 0)
	assertNumber(t, "refund delta", deltas["f_used"], -5)
	if n := idempotencyRows(t, refundP, reserved.Key+":refund"); n != 1 {
		t.Errorf("refund key rows = %d, want 1", n)
	}
	if p := w.pendingUndos(t, id); len(p) != 0 {
		t.Errorf("PendingUndos after the undo = %+v, want none", p)
	}
}

// Withdrawn before the deduction: React is refused, nothing was taken,
// nothing is pending, and an undo writes nothing (and uses no key).
func TestUndo_EscapeBeforeTheDeduction(t *testing.T) {
	w := newLeaveWorld(t)
	bal := w.balance(t, w.ana, 25)
	id := w.submitted(t, w.ana, w.bob, 5)
	ask := w.askApproval(t, w.bob, id)
	withdraw, err := w.move(t, w.ana, id, "withdrawn", nil)
	if err != nil {
		t.Fatalf("withdraw: %v", err)
	}
	if _, err := w.react(t, id, ask); err == nil {
		t.Fatal("React after the escape succeeded")
	}
	if p := w.pendingUndos(t, id); len(p) != 0 {
		t.Errorf("PendingUndos = %+v, want none: nothing was deducted", p)
	}
	seq := w.itemSeq(t, bal)
	for i := 0; i < 2; i++ {
		res, err := w.undo(t, id, ask.HoldID, withdraw.ItemSeq)
		if err != nil || res.Undone {
			t.Errorf("Undo #%d = %+v, %v; want nothing undone, no error", i+1, res, err)
		}
	}
	if w.itemSeq(t, bal) != seq || eventsBy(t, bal, refundP) != 0 {
		t.Error("an undo without a deduction wrote to the balance")
	}
	assertUsed(t, w, bal, 0)
}

// Each non-allow end of a hold after a committed deduction leaves one
// pending undo, and the undo refunds it once.
func TestUndo_EveryNonAllowOutcome(t *testing.T) {
	cases := []struct {
		name  string
		close func(t *testing.T, w leaveWorld, id uuid.UUID, ask itemservice.TransitionResult)
		hold  string
	}{
		{"the re-check of the approver fails (requester_lost_access)", func(t *testing.T, w leaveWorld, id uuid.UUID, ask itemservice.TransitionResult) {
			if _, err := w.k.Update(testCtx(t), itemservice.UpdateRequest{IdempotencyKey: newKey(), Principal: w.ana, ItemID: id,
				Policy: &itemservice.PolicyPatch{Readers: []string{w.ana.Actor}}}); err != nil {
				t.Fatalf("ana narrows the request: %v", err)
			}
			if _, err := w.resolveAsDeduct(t, id, ask, itemservice.VerdictAllow, ""); err != nil {
				t.Fatalf("resolve allow: %v", err)
			}
		}, item.HoldDenied},
		{"the budget runs out (error)", func(t *testing.T, w leaveWorld, id uuid.UUID, ask itemservice.TransitionResult) {
			if _, err := w.resolveAsDeduct(t, id, ask, itemservice.VerdictError, "budget_exhausted"); err != nil {
				t.Fatalf("resolve error: %v", err)
			}
		}, item.HoldError},
		{"denied", func(t *testing.T, w leaveWorld, id uuid.UUID, ask itemservice.TransitionResult) {
			if _, err := w.resolveAsDeduct(t, id, ask, itemservice.VerdictDeny, "not_linked"); err != nil {
				t.Fatalf("resolve deny: %v", err)
			}
		}, item.HoldDenied},
		{"canceled by an escape", func(t *testing.T, w leaveWorld, id uuid.UUID, ask itemservice.TransitionResult) {
			if _, err := w.move(t, w.ana, id, "withdrawn", nil); err != nil {
				t.Fatalf("withdraw: %v", err)
			}
		}, item.HoldCanceled},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newLeaveWorld(t)
			bal := w.balance(t, w.ana, 25)
			id := w.submitted(t, w.ana, w.bob, 5)
			ask := w.askApproval(t, w.bob, id)
			if _, err := w.react(t, id, ask); err != nil {
				t.Fatalf("React: %v", err)
			}
			c.close(t, w, id, ask)
			h := w.holdNow(t, id)
			if h.Status != c.hold {
				t.Fatalf("hold = %+v, want %s", h, c.hold)
			}
			closing := w.itemSeq(t, id) // the event that closed the hold is the request's latest
			pending := w.pendingUndos(t, id)
			if len(pending) != 1 || pending[0].Reaction != "refund" || pending[0].HoldID != ask.HoldID || pending[0].CauseSeq != closing {
				t.Fatalf("PendingUndos = %+v, want one refund of hold %s caused by event %d", pending, ask.HoldID, closing)
			}
			res, err := w.undo(t, id, ask.HoldID, closing)
			if err != nil || !res.Undone {
				t.Fatalf("Undo = %+v, %v", res, err)
			}
			assertUsed(t, w, bal, 0)
			if eventsBy(t, bal, refundP) != 1 || len(w.pendingUndos(t, id)) != 0 {
				t.Errorf("refunds = %d, pending %v; want 1 and none", eventsBy(t, bal, refundP), w.pendingUndos(t, id))
			}
		})
	}
}

// Denied before anything was deducted: no undo is pending.
func TestUndo_DeniedWithoutADeduction(t *testing.T) {
	w := newLeaveWorld(t)
	w.balance(t, w.ana, 4)
	id := w.submitted(t, w.ana, w.bob, 5)
	if _, h := w.approve(t, w.bob, id); h.Status != item.HoldDenied || h.Reason != "insufficient_balance" {
		t.Fatalf("hold = %+v, want denied insufficient_balance", h)
	}
	if p := w.pendingUndos(t, id); len(p) != 0 {
		t.Errorf("PendingUndos = %+v, want none", p)
	}
}

// A refund retried after a crash, or run twice at once: one refund.
func TestUndo_RetriedOrConcurrent_RefundsOnce(t *testing.T) {
	w := newLeaveWorld(t)
	bal := w.balance(t, w.ana, 25)
	id := w.submitted(t, w.ana, w.bob, 5)
	ask := w.askApproval(t, w.bob, id)
	if _, err := w.react(t, id, ask); err != nil {
		t.Fatalf("React: %v", err)
	}
	if _, err := w.resolveAsDeduct(t, id, ask, itemservice.VerdictError, "budget_exhausted"); err != nil {
		t.Fatalf("resolve error: %v", err)
	}
	closing := w.itemSeq(t, id)

	var wg sync.WaitGroup
	results := make([]itemservice.UndoResult, 8)
	errs := make([]error, 8)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = w.undo(t, id, ask.HoldID, closing)
		}()
	}
	wg.Wait()
	for i := range results {
		if errs[i] != nil || !results[i].Undone || results[i] != results[0] {
			t.Errorf("Undo #%d = %+v, %v; want the same undone result as #0 (%+v)", i, results[i], errs[i], results[0])
		}
	}
	again, err := w.undo(t, id, ask.HoldID, closing) // after a crash: the activity runs again
	if err != nil || again != results[0] {
		t.Errorf("Undo again = %+v, %v; want %+v", again, err, results[0])
	}
	assertUsed(t, w, bal, 0)
	if n := eventsBy(t, bal, refundP); n != 1 {
		t.Errorf("refund events = %d, want 1", n)
	}
}

// Withdrawing an approved request refunds the deduction of the hold that
// entered approved.
func TestUndo_WithdrawingAnApprovedRequest(t *testing.T) {
	w := newLeaveWorld(t)
	bal := w.balance(t, w.ana, 25)
	id := w.submitted(t, w.ana, w.bob, 5)
	ask, h := w.approve(t, w.bob, id)
	if h.Status != item.HoldAllowed {
		t.Fatalf("approval: %+v", h)
	}
	se, err := w.k.StateEntry(testCtx(t), id)
	if err != nil {
		t.Fatalf("StateEntry: %v", err)
	}
	if se.State != "approved" || se.EnteredHold != ask.HoldID || len(se.PendingUndos) != 0 {
		t.Errorf("StateEntry = %s, entered by hold %s, pending %v; want approved, %s, none",
			se.State, se.EnteredHold, se.PendingUndos, ask.HoldID)
	}
	// While approved, the deduction stands: it cannot be undone.
	_, err = w.undo(t, id, ask.HoldID, se.EnteredSeq)
	assertRejectedCode(t, "Undo while approved", err, itemservice.CodeNotUndoable)
	assertUsed(t, w, bal, 5)

	withdraw, err := w.move(t, w.ana, id, "withdrawn", nil)
	if err != nil {
		t.Fatalf("ana withdraws the approved request: %v", err)
	}
	pending := w.pendingUndos(t, id)
	want := []itemservice.PendingUndo{{Reaction: "refund", HoldID: ask.HoldID, CauseSeq: withdraw.ItemSeq}}
	if !slices.Equal(pending, want) {
		t.Errorf("PendingUndos = %+v, want %+v", pending, want)
	}
	if res, err := w.undo(t, id, ask.HoldID, withdraw.ItemSeq); err != nil || !res.Undone {
		t.Fatalf("Undo = %+v, %v", res, err)
	}
	assertUsed(t, w, bal, 0)
}

// The withdrawal's undo matches the hold that entered approved, never an
// earlier denied one: denied insufficient_balance, recalled and edited,
// resubmitted, approved, withdrawn: exactly one refund, of the right amount.
func TestUndo_WithdrawalUndoesTheHoldThatEnteredApproved(t *testing.T) {
	w := newLeaveWorld(t)
	bal := w.balance(t, w.ana, 5)
	id := w.submitted(t, w.ana, w.bob, 8)
	denied, h := w.approve(t, w.bob, id)
	if h.Status != item.HoldDenied || h.Reason != "insufficient_balance" {
		t.Fatalf("first approval: %+v, want denied insufficient_balance", h)
	}
	if _, err := w.move(t, w.ana, id, "draft", nil); err != nil {
		t.Fatalf("recall: %v", err)
	}
	if err := updateFields(t, w.k, w.ana, id, map[string]any{"f_days": 3}); err != nil {
		t.Fatalf("edit the days: %v", err)
	}
	if _, h := w.hrNames(t, id, w.askSubmit(t, w.ana, id), w.bob); h.Status != item.HoldAllowed {
		t.Fatalf("resubmit: %+v", h)
	}
	approved, h := w.approve(t, w.bob, id)
	if h.Status != item.HoldAllowed {
		t.Fatalf("second approval: %+v", h)
	}
	assertUsed(t, w, bal, 3)

	withdraw, err := w.move(t, w.ana, id, "withdrawn", nil)
	if err != nil {
		t.Fatalf("withdraw: %v", err)
	}
	pending := w.pendingUndos(t, id)
	if len(pending) != 1 || pending[0].HoldID != approved.HoldID || pending[0].CauseSeq != withdraw.ItemSeq {
		t.Fatalf("PendingUndos = %+v, want one, for hold %s (not %s)", pending, approved.HoldID, denied.HoldID)
	}
	if res, err := w.undo(t, id, denied.HoldID, withdraw.ItemSeq); err != nil || res.Undone {
		t.Errorf("Undo of the denied hold = %+v, %v; want nothing undone", res, err)
	}
	res, err := w.undo(t, id, approved.HoldID, withdraw.ItemSeq)
	if err != nil || !res.Undone {
		t.Fatalf("Undo of the approving hold = %+v, %v", res, err)
	}
	_, deltas := valuesAndDeltas(t, bal, res.ItemSeq)
	assertNumber(t, "refund delta", deltas["f_used"], -3)
	assertUsed(t, w, bal, 0)
	if n := eventsBy(t, bal, refundP); n != 1 {
		t.Errorf("refund events = %d, want exactly 1", n)
	}
}

// An open hold cannot be undone; the undo gives back what was taken even
// when the link is gone; Undo is the undo reaction's door only.
func TestUndo_IsChecked(t *testing.T) {
	w := newLeaveWorld(t)
	bal := w.balance(t, w.ana, 25)
	id := w.submitted(t, w.ana, w.bob, 5)
	ask := w.askApproval(t, w.bob, id)
	if _, err := w.react(t, id, ask); err != nil {
		t.Fatalf("React: %v", err)
	}
	_, err := w.undo(t, id, ask.HoldID, ask.ItemSeq)
	assertRejectedCode(t, "Undo of an open hold", err, itemservice.CodeNotUndoable)
	assertUsed(t, w, bal, 5)

	withdraw, err := w.move(t, w.ana, id, "withdrawn", nil)
	if err != nil {
		t.Fatalf("withdraw: %v", err)
	}
	for _, c := range []struct {
		name string
		req  itemservice.UndoRequest
		want error
	}{
		{"the deduct reaction", itemservice.UndoRequest{Principal: deductP, SourceID: id, HoldID: ask.HoldID,
			Attribution: reactionCause("deduct", id, withdraw.ItemSeq, 1)}, itemservice.ErrInvalid},
		{"a hook", itemservice.UndoRequest{Principal: hrCheck, SourceID: id, HoldID: ask.HoldID,
			Attribution: hrCauseV2(id, withdraw.ItemSeq)}, itemservice.ErrInvalid},
		{"a hold of another Item", func() itemservice.UndoRequest {
			other := w.request(t, w.ana, 1)
			return itemservice.UndoRequest{Principal: refundP, SourceID: other, HoldID: ask.HoldID,
				Attribution: reactionCause("refund", other, 1, 1)}
		}(), itemservice.ErrNotFound},
		{"caused by an event of another Item", itemservice.UndoRequest{Principal: refundP, SourceID: id, HoldID: ask.HoldID,
			Attribution: reactionCause("refund", bal, 1, 1)}, itemservice.ErrInvalid},
	} {
		if _, err := w.k.Internal().Undo(testCtx(t), c.req); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", c.name, err, c.want)
		}
	}

	if _, err := pool.Exec(testCtx(t), `delete from links where source_id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if res, err := w.undo(t, id, ask.HoldID, withdraw.ItemSeq); err != nil || !res.Undone {
		t.Errorf("Undo without the link = %+v, %v; want undone: it gives back what was taken", res, err)
	}
	assertUsed(t, w, bal, 0)
}

// An undo its target refuses (the balance's version accepts deduct, not
// refund) is recorded on the request as reaction_failed, once, and not
// retried.
func TestUndo_RefusedByTheTarget_RecordedAsReactionFailed(t *testing.T) {
	w := newLeaveWorld(t)
	registerInline(t, w.k, balanceVersion(91, "  vacation_request:deduct: [f_used]"))
	bal := w.balanceOn(t, 91, w.ana, 25)
	id := w.submitted(t, w.ana, w.bob, 5)
	ask, h := w.approve(t, w.bob, id)
	if h.Status != item.HoldAllowed {
		t.Fatalf("approval: %+v", h)
	}
	approvedHold := ask.HoldID
	withdraw, err := w.move(t, w.ana, id, "withdrawn", nil)
	if err != nil {
		t.Fatalf("withdraw: %v", err)
	}
	_, err = w.undo(t, id, approvedHold, withdraw.ItemSeq)
	assertRejectedCode(t, "Undo the target does not accept", err, itemservice.CodeReactionNotAccepted)
	assertUsed(t, w, bal, 5)

	reservation := fmt.Sprintf("deduct:%s:%d:%s", id, ask.ItemSeq, bal)
	req := itemservice.ReactionFailedRequest{
		IdempotencyKey: "reaction_failed:" + reservation + ":refund", Principal: refundP, SourceID: id,
		Reason: itemservice.CodeReactionNotAccepted, Attribution: reactionCause("refund", id, withdraw.ItemSeq, 1),
	}
	first, err := w.k.Internal().ReactionFailed(testCtx(t), req)
	if err != nil {
		t.Fatalf("ReactionFailed: %v", err)
	}
	again, err := w.k.Internal().ReactionFailed(testCtx(t), req)
	if err != nil || again != first {
		t.Errorf("ReactionFailed again = %+v, %v; want %+v", again, err, first)
	}
	op, payload := event(t, id, first.ItemSeq)
	if op != "reaction_failed" || payload["reaction"] != "vacation_request:refund" || payload["reason"] != "reaction_not_accepted" {
		t.Errorf("event = %s %v; want reaction_failed naming vacation_request:refund and the reason", op, payload)
	}
	if w.state(t, id) != "withdrawn" {
		t.Errorf("state = %s, want withdrawn (reaction_failed changes no state)", w.state(t, id))
	}
	page, err := w.k.ListEvents(testCtx(t), itemservice.ListEventsRequest{Principal: inDefault(w.ana), ItemID: id})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	last := page.Events[len(page.Events)-1]
	if last.Op != "reaction_failed" || last.Reaction != "vacation_request:refund" || last.Reason != "reaction_not_accepted" {
		t.Errorf("history = %+v", last)
	}
}

// --- races ---

// Twenty approvals of 5 days at once on an allowance of 25: exactly five
// deduct, the others are refused insufficient_balance, and f_used ends at
// 25. Five rounds.
func TestDeduct_ConcurrentApprovalsNeverOverdraw(t *testing.T) {
	for round := 0; round < 5; round++ {
		w := newLeaveWorld(t)
		bal := w.balance(t, w.ana, 25)
		type pending struct {
			id  uuid.UUID
			ask itemservice.TransitionResult
		}
		var all []pending
		for i := 0; i < 20; i++ {
			id := w.submitted(t, w.ana, w.bob, 5)
			all = append(all, pending{id, w.askApproval(t, w.bob, id)})
		}
		var (
			wg               sync.WaitGroup
			mu               sync.Mutex
			ok, refused, bad int
		)
		for _, p := range all {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := w.k.Internal().React(context.Background(), itemservice.ReactRequest{
					Principal: deductP, SourceID: p.id, HoldID: p.ask.HoldID,
					Attribution: reactionCause("deduct", p.id, p.ask.ItemSeq, 1),
				})
				reason, isRefusal := itemservice.DenyReason(err)
				mu.Lock()
				defer mu.Unlock()
				switch {
				case err == nil:
					ok++
				case isRefusal && reason == "insufficient_balance":
					refused++
				default:
					bad++
					t.Errorf("round %d: React = %v", round, err)
				}
			}()
		}
		wg.Wait()
		if ok != 5 || refused != 15 || bad != 0 {
			t.Errorf("round %d: %d deducted, %d refused, %d other; want 5, 15, 0", round, ok, refused, bad)
		}
		assertUsed(t, w, bal, 25)
		if n := eventsBy(t, bal, deductP); n != 5 {
			t.Errorf("round %d: deduction events = %d, want 5", round, n)
		}
	}
}

// An escape racing the deduction, both orders, many times: whatever wins,
// once the pending undos have run, nothing stays taken and every deduction
// has exactly one refund.
func TestDeduct_EscapeRacingTheDeduction(t *testing.T) {
	w := newLeaveWorld(t)
	bal := w.balance(t, w.ana, 1000)
	for i := 0; i < 20; i++ {
		id := w.submitted(t, w.ana, w.bob, 5)
		ask := w.askApproval(t, w.bob, id)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, _ = w.k.Internal().React(context.Background(), itemservice.ReactRequest{
				Principal: deductP, SourceID: id, HoldID: ask.HoldID, Attribution: reactionCause("deduct", id, ask.ItemSeq, 1),
			})
		}()
		go func() {
			defer wg.Done()
			_, err := w.k.Transition(context.Background(), itemservice.TransitionRequest{
				IdempotencyKey: newKey(), Principal: w.ana, ItemID: id, ToState: "withdrawn"})
			if err != nil {
				t.Errorf("withdraw: %v", err)
			}
		}()
		wg.Wait()
		for _, p := range w.pendingUndos(t, id) {
			if res, err := w.undo(t, id, p.HoldID, p.CauseSeq); err != nil || !res.Undone {
				t.Errorf("Undo = %+v, %v", res, err)
			}
		}
	}
	assertUsed(t, w, bal, 0)
	if d, r := eventsBy(t, bal, deductP), eventsBy(t, bal, refundP); d != r {
		t.Errorf("deductions %d, refunds %d; want as many refunds as deductions", d, r)
	}
}
