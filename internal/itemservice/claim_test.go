// Tests for a claim that binds (docs/roadmap.md, track 1, item 5, step 2).
// Both bake-offs' test 3 found that on the kernel a claim was only a field:
// gus claimed a purchase and fay, his colleague in finance, approved it
// (docs/research/camunda-bakeoff.md; docs/research/temporal-bakeoff.md).
//
// A state says `claim: <a principal field>`; a move out of it says
// `release: true` when it gives the claim up. The owner's rule:
//
//   - Claiming. A person's move into the claimed state records them in the
//     field: the user an agent acts for, else the actor. A patch may name
//     the field only with that same person (the bake-offs' scripts do);
//     anyone else, or null, is FIELD_NOT_WRITABLE. It is the claimant's
//     write of the field: writable_by is asked, so an agent claims only
//     where the Type opens the field to it.
//   - While claimed, nobody writes the field: FIELD_FROZEN, in an update
//     and in the patch of a move out.
//   - Moving on. Every move out that is not a release is the claimant's
//     only, on top of `allowed` and `except`: anyone else is NOT_ALLOWED.
//     Whether or not the move clears the field: an approval that tidies
//     the claim away is still the claimant's.
//   - Releasing. A move marked release is decided by `allowed` alone, and
//     clears the field.
//   - The kernel's own moves (the timer) are exempt, so that an escalation
//     can take a claim back.
//   - The view (step 1): a claimed Item waits on its claimant only; a
//     role's queue holds what nobody has claimed; a release is not work.
//
// cl_purchase is the bake-off's purchase cut to finance's part, with the
// two lines its Types gain: claim on finance_review, release on the move
// back to the queue.
package itemservice_test

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// %[1]s is the Type's ID, %[2]s the claim field's writable_by (with its
// leading comma, or empty: people only).
const clPurchaseSrc = `
id: %[1]s
version: 1
initial_state: draft
fields:
  - {id: f_amount,     type: int}
  - {id: f_manager,    type: principal}
  - {id: f_claimed_by, type: principal%[2]s}
  - {id: f_reason,     type: string}
states:
  - {id: draft,          category: todo}
  - {id: manager_review, category: waiting}
  - {id: finance_queue,  category: waiting}
  - {id: finance_review, category: active, claim: f_claimed_by, timer: {after: 48h, to: escalated}}
  - {id: cfo_review,     category: waiting}
  - {id: escalated,      category: waiting}
  - {id: approved,       category: done}
  - {id: rejected,       category: canceled}
transitions:
  - {from: draft,          to: manager_review, allowed: [creator], required: [f_amount, f_manager]}
  - {from: manager_review, to: finance_queue,  allowed: ['field:f_manager'], except: [creator]}
  # Claim from the shared queue.
  - {from: finance_queue,  to: finance_review, allowed: ['role:cl_finance'], except: [creator], required: [f_claimed_by]}
  # Hand back: the release. A lead may release and decides nothing.
  - {from: finance_review, to: finance_queue,  allowed: ['role:cl_finance', 'role:cl_lead'], release: true, clears: [f_claimed_by]}
  # Decide. The rejection clears the claim and is no release.
  - {from: finance_review, to: approved,       allowed: ['role:cl_finance'], except: [creator]}
  - {from: finance_review, to: rejected,       allowed: ['role:cl_finance'], except: [creator], required: [f_reason], clears: [f_claimed_by]}
  - {from: finance_review, to: cfo_review,     allowed: ['role:cl_finance'], except: [creator]}
  - {from: finance_review, to: escalated,      allowed: ['principal:system:timer']}
  - {from: cfo_review,     to: approved,       allowed: ['role:cl_cfo']}
`

// claimWorld is waitWorld (its people, its namespace, its helpers) with
// the claim Types: cl_purchase, whose claim field finance writes, and
// cl_closed, the same with the field left to people only.
type claimWorld struct {
	*waitWorld
}

func newClaimWorld(t *testing.T) claimWorld {
	t.Helper()
	w := claimWorld{newWaitWorld(t)}
	for _, src := range []string{
		fmt.Sprintf(clPurchaseSrc, "cl_purchase", ", writable_by: ['role:cl_finance']"),
		fmt.Sprintf(clPurchaseSrc, "cl_closed", ""),
	} {
		ty, err := typedef.Parse([]byte(src))
		if err != nil {
			t.Fatalf("parse: %v\n%s", err, src)
		}
		if err := w.k.RegisterType(testCtx(t), ty); err != nil {
			t.Fatalf("register %s: %v", ty.ID, err)
		}
		w.types[fmt.Sprintf("%s@%d", ty.ID, ty.Version)] = ty
	}
	w.grant(t, w.fay, "cl_finance")
	w.grant(t, w.gus, "cl_finance")
	return w
}

// queued is ana's purchase of typeID, approved by bob, her manager, and
// waiting in the finance queue.
func (w claimWorld) queued(t *testing.T, typeID string) uuid.UUID {
	t.Helper()
	id := w.create(t, itemservice.CreateRequest{
		Principal: w.ana, TypeID: typeID, TypeVersion: 1,
		Fields: map[string]any{"f_amount": 2500, "f_manager": w.bob.Actor},
	})
	w.move(t, w.ana, id, "manager_review", nil)
	w.move(t, w.bob, id, "finance_queue", nil)
	return id
}

// claimed is a queued cl_purchase that p has claimed, as the bake-offs'
// scripts claim: naming themselves in the move's patch.
func (w claimWorld) claimed(t *testing.T, p item.Principal) uuid.UUID {
	t.Helper()
	id := w.queued(t, "cl_purchase")
	w.move(t, p, id, "finance_review", map[string]any{"f_claimed_by": p.Actor})
	return id
}

func (w claimWorld) try(t *testing.T, p item.Principal, id uuid.UUID, to string, patch map[string]any) error {
	t.Helper()
	_, err := w.k.Transition(testCtx(t), itemservice.TransitionRequest{
		IdempotencyKey: newKey(), Principal: p, ItemID: id, ToState: to, Fields: patch,
	})
	return err
}

// is reads the Item as fay and checks its state and who its claim field
// names ("" for nobody).
func (w claimWorld) is(t *testing.T, what string, id uuid.UUID, state, claimedBy string) {
	t.Helper()
	it, err := w.k.Get(testCtx(t), w.fay, id)
	if err != nil {
		t.Fatalf("%s: Get: %v", what, err)
	}
	got, _ := it.Fields["f_claimed_by"].(string)
	if it.State != state || got != claimedBy {
		t.Errorf("%s: in %s, claimed by %q; want in %s, claimed by %q", what, it.State, got, state, claimedBy)
	}
}

// assertNotTheirs: a refusal for who is asking (NOT_ALLOWED over the
// API), not for a field.
func assertNotTheirs(t *testing.T, what string, err error) {
	t.Helper()
	var fe *itemservice.FieldNotWritableError
	if !errors.Is(err, itemservice.ErrForbidden) || errors.As(err, &fe) {
		t.Errorf("%s: error = %v, want ErrForbidden (NOT_ALLOWED)", what, err)
	}
}

// assertRefusedForWho: refused for who is asking, by `allowed`
// (NOT_ALLOWED) or by the claim field's writable_by (FIELD_NOT_WRITABLE):
// PERMISSION_DENIED over the API either way.
func assertRefusedForWho(t *testing.T, what string, err error) {
	t.Helper()
	if !errors.Is(err, itemservice.ErrForbidden) {
		t.Errorf("%s: error = %v, want ErrForbidden", what, err)
	}
}

// assertClaimNotWritable: FIELD_NOT_WRITABLE naming the claim field.
func assertClaimNotWritable(t *testing.T, what string, err error) {
	t.Helper()
	var fe *itemservice.FieldNotWritableError
	if !errors.Is(err, itemservice.ErrForbidden) || !errors.As(err, &fe) || !slices.Equal(fe.Fields, []string{"f_claimed_by"}) {
		t.Errorf("%s: error = %v, want a *FieldNotWritableError (FIELD_NOT_WRITABLE) naming f_claimed_by", what, err)
	}
}

// assertClaimFrozen: FIELD_FROZEN naming the claim field.
func assertClaimFrozen(t *testing.T, what string, err error) {
	t.Helper()
	if !errors.Is(err, itemservice.ErrRejected) || !strings.Contains(fmt.Sprint(err), "f_claimed_by") ||
		!strings.Contains(fmt.Sprint(err), "frozen") {
		t.Errorf("%s: error = %v, want ErrRejected (FIELD_FROZEN) naming f_claimed_by", what, err)
	}
}

// The bake-offs' failing row: gus claims a purchase, and fay, in finance
// like him, may not decide it. Not approve it, not reject it, not send it
// on. gus, who claimed it, approves.
func TestClaim_Bakeoff_AColleagueMayNotDecideWhatGusClaimed(t *testing.T) {
	w := newClaimWorld(t)
	id := w.claimed(t, w.gus)
	w.is(t, "after gus's claim", id, "finance_review", w.gus.Actor)

	assertNotTheirs(t, "fay approves the purchase gus claimed", w.try(t, w.fay, id, "approved", nil))
	assertNotTheirs(t, "fay rejects it, giving a reason",
		w.try(t, w.fay, id, "rejected", map[string]any{"f_reason": "over budget"}))
	assertNotTheirs(t, "fay sends it to the CFO", w.try(t, w.fay, id, "cfo_review", nil))
	w.is(t, "after fay's three attempts", id, "finance_review", w.gus.Actor)

	if err := w.try(t, w.gus, id, "approved", nil); err != nil {
		t.Fatalf("gus approves the purchase he claimed: %v", err)
	}
	// The claim stays on the approved purchase, as the record of who
	// decided.
	w.is(t, "after gus's approval", id, "approved", w.gus.Actor)
}

// The row that must stay as it is (the Temporal bake-off's review: "a
// colleague in finance can take over a purchase fay claimed and left"):
// gus puts fay's purchase back in the queue, and may then claim it.
func TestClaim_Bakeoff_AColleagueMayStillRelease(t *testing.T) {
	w := newClaimWorld(t)
	id := w.claimed(t, w.fay)

	if err := w.try(t, w.gus, id, "finance_queue", nil); err != nil {
		t.Fatalf("gus puts fay's purchase back in the queue: %v", err)
	}
	w.is(t, "after gus's release", id, "finance_queue", "")
	if err := w.try(t, w.gus, id, "finance_review", map[string]any{"f_claimed_by": w.gus.Actor}); err != nil {
		t.Fatalf("gus claims it: %v", err)
	}
	assertNotTheirs(t, "fay approves it, now gus's", w.try(t, w.fay, id, "approved", nil))
	if err := w.try(t, w.gus, id, "approved", nil); err != nil {
		t.Errorf("gus approves it: %v", err)
	}
}

// Claiming records whoever makes the move. Named in the patch as oneself
// (what every bake-off script sends), or not named at all: the same. For
// someone else, or as nobody: refused, and nothing is claimed.
func TestClaim_ClaimingRecordsTheMover(t *testing.T) {
	w := newClaimWorld(t)

	named := w.queued(t, "cl_purchase")
	if err := w.try(t, w.gus, named, "finance_review", map[string]any{"f_claimed_by": w.gus.Actor}); err != nil {
		t.Fatalf("gus claims, naming himself: %v", err)
	}
	w.is(t, "claimed naming himself", named, "finance_review", w.gus.Actor)

	// The move requires f_claimed_by: the kernel's own write meets it.
	unnamed := w.queued(t, "cl_purchase")
	if err := w.try(t, w.gus, unnamed, "finance_review", nil); err != nil {
		t.Fatalf("gus claims with no patch: %v", err)
	}
	w.is(t, "claimed with no patch", unnamed, "finance_review", w.gus.Actor)

	other := w.queued(t, "cl_purchase")
	assertClaimNotWritable(t, "gus claims it for fay",
		w.try(t, w.gus, other, "finance_review", map[string]any{"f_claimed_by": w.fay.Actor}))
	assertClaimNotWritable(t, "gus claims it for olaf, who is not in finance",
		w.try(t, w.gus, other, "finance_review", map[string]any{"f_claimed_by": w.olaf.Actor}))
	assertClaimNotWritable(t, "gus claims it for nobody",
		w.try(t, w.gus, other, "finance_review", map[string]any{"f_claimed_by": nil}))
	w.is(t, "after three refused claims", other, "finance_queue", "")

	// The claim is a move like any other: `allowed` and `except` hold.
	// (Which refusal comes first is the order of every write's checks: the
	// field, closed to who is not in finance, before `allowed`.)
	assertRefusedForWho(t, "olaf, not in finance, claims", w.try(t, w.olaf, other, "finance_review", nil))
	w.grant(t, w.ana, "cl_finance")
	assertNotTheirs(t, "ana, in finance, claims her own request",
		w.try(t, w.ana, other, "finance_review", map[string]any{"f_claimed_by": w.ana.Actor}))
}

// While it is claimed, nobody writes the claim field: not a colleague, not
// the claimant, in an update or in the patch of a move out.
func TestClaim_NobodyRewritesAClaim(t *testing.T) {
	w := newClaimWorld(t)
	id := w.claimed(t, w.gus)

	update := func(p item.Principal, v any) error {
		_, err := w.k.Update(testCtx(t), itemservice.UpdateRequest{
			IdempotencyKey: newKey(), Principal: p, ItemID: id, Fields: map[string]any{"f_claimed_by": v},
		})
		return err
	}
	assertClaimFrozen(t, "fay takes the claim with an update", update(w.fay, w.fay.Actor))
	assertClaimFrozen(t, "fay clears the claim with an update", update(w.fay, nil))
	assertClaimFrozen(t, "gus hands his claim to fay with an update", update(w.gus, w.fay.Actor))
	assertClaimFrozen(t, "gus approves, naming fay as the claimant in the move",
		w.try(t, w.gus, id, "approved", map[string]any{"f_claimed_by": w.fay.Actor}))
	w.is(t, "after four refused writes", id, "finance_review", w.gus.Actor)

	// Other fields are written as before.
	if _, err := w.k.Update(testCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: w.fay, ItemID: id, Fields: map[string]any{"f_reason": "a note"},
	}); err != nil {
		t.Errorf("fay writes another field of the claimed purchase: %v", err)
	}
	// Back in the queue the field is finance's to write again; the next
	// claim overwrites it.
	w.move(t, w.fay, id, "finance_queue", nil)
	if err := update(w.fay, w.fay.Actor); err != nil {
		t.Errorf("fay writes the field in the queue, where nothing is claimed: %v", err)
	}
	if err := w.try(t, w.gus, id, "finance_review", nil); err != nil {
		t.Fatalf("gus claims it: %v", err)
	}
	w.is(t, "after gus's claim over fay's value", id, "finance_review", w.gus.Actor)
}

// The accident the explicit release prevents. The rejection clears the
// claim field, to tidy up, and is not marked release: it is still the
// claimant's only. Clearing a field does not open a decision to the team.
func TestClaim_AMoveThatClearsTheClaimIsNotARelease(t *testing.T) {
	w := newClaimWorld(t)
	id := w.claimed(t, w.gus)

	assertNotTheirs(t, "fay rejects the purchase gus claimed, by the move that clears the claim",
		w.try(t, w.fay, id, "rejected", map[string]any{"f_reason": "over budget"}))
	w.is(t, "after fay's attempt", id, "finance_review", w.gus.Actor)

	if err := w.try(t, w.gus, id, "rejected", map[string]any{"f_reason": "over budget"}); err != nil {
		t.Fatalf("gus rejects the purchase he claimed: %v", err)
	}
	w.is(t, "after gus's rejection", id, "rejected", "")
}

// A release is `allowed`'s alone: a lead, named on the release move and on
// no other, takes a stuck claim back and decides nothing. Someone the
// release does not name cannot release.
func TestClaim_WhoMayRelease(t *testing.T) {
	w := newClaimWorld(t)
	w.grant(t, w.olaf, "cl_lead")
	id := w.claimed(t, w.gus)

	assertNotTheirs(t, "bob, no part of finance, releases gus's claim", w.try(t, w.bob, id, "finance_queue", nil))
	assertNotTheirs(t, "olaf, a lead, approves it", w.try(t, w.olaf, id, "approved", nil))
	if err := w.try(t, w.olaf, id, "finance_queue", nil); err != nil {
		t.Fatalf("olaf, a lead, releases gus's claim: %v", err)
	}
	w.is(t, "after the lead's release", id, "finance_queue", "")
	// A lead releases and does not claim: the claim move does not name them.
	assertRefusedForWho(t, "olaf, a lead, claims it", w.try(t, w.olaf, id, "finance_review", nil))

	// The claimant releases their own claim too.
	w.move(t, w.gus, id, "finance_review", nil)
	if err := w.try(t, w.gus, id, "finance_queue", nil); err != nil {
		t.Errorf("gus releases his own claim: %v", err)
	}
}

// The claim is on top of `allowed`, not instead of it: a claimant who
// left finance decides nothing, and a colleague releases what they held.
func TestClaim_AClaimantWhoLeftTheRoleCannotDecide(t *testing.T) {
	w := newClaimWorld(t)
	id := w.claimed(t, w.gus)

	w.revoke(t, w.gus, "cl_finance")
	assertNotTheirs(t, "gus, no longer in finance, approves his claim", w.try(t, w.gus, id, "approved", nil))
	assertNotTheirs(t, "fay approves it: it is still gus's", w.try(t, w.fay, id, "approved", nil))
	if err := w.try(t, w.fay, id, "finance_queue", nil); err != nil {
		t.Fatalf("fay releases the claim gus left: %v", err)
	}
	if err := w.try(t, w.fay, id, "finance_review", nil); err != nil {
		t.Fatalf("fay claims it: %v", err)
	}
	if err := w.try(t, w.fay, id, "approved", nil); err != nil {
		t.Errorf("fay approves it: %v", err)
	}
}

// Agents. Claiming is the claimant's write of the claim field, so an agent
// claims only where the Type opens the field to it: here to holders of
// cl_finance, agents among them. Acting for fay, the claim is fay's: she
// decides, and so does her agent; an agent acting for gus does not.
func TestClaim_Agents(t *testing.T) {
	w := newClaimWorld(t)
	agentID := "agent:cl-bot-" + uuid.NewString()[:8]
	forFay := withSession(t, w.k, item.Principal{Actor: agentID, OnBehalfOf: w.fay.Actor, Namespace: w.ns})
	forGus := withSession(t, w.k, item.Principal{Actor: agentID, OnBehalfOf: w.gus.Actor, Namespace: w.ns})
	alone := withSession(t, w.k, item.Principal{Actor: agentID, Namespace: w.ns})

	id := w.queued(t, "cl_purchase")
	// Without the role the agent makes no finance move, whoever it acts for.
	if err := w.try(t, forFay, id, "finance_review", nil); !errors.Is(err, itemservice.ErrForbidden) {
		t.Errorf("the agent for fay, holding no role, claims: error = %v, want ErrForbidden", err)
	}
	w.is(t, "after the refused claim", id, "finance_queue", "")

	w.grant(t, item.Principal{Actor: agentID}, "cl_finance")
	assertClaimNotWritable(t, "the agent for fay claims it for itself",
		w.try(t, forFay, id, "finance_review", map[string]any{"f_claimed_by": agentID}))
	if err := w.try(t, forFay, id, "finance_review", nil); err != nil {
		t.Fatalf("the agent for fay, in finance too, claims: %v", err)
	}
	w.is(t, "claimed by the agent for fay", id, "finance_review", w.fay.Actor)

	assertNotTheirs(t, "the same agent, acting for gus, approves fay's claim", w.try(t, forGus, id, "approved", nil))
	assertNotTheirs(t, "the same agent, on its own, approves fay's claim", w.try(t, alone, id, "approved", nil))
	if err := w.try(t, forFay, id, "approved", nil); err != nil {
		t.Errorf("the agent for fay approves fay's claim: %v", err)
	}

	// On its own, the agent's claim is the agent's: fay may not decide it.
	own := w.queued(t, "cl_purchase")
	if err := w.try(t, alone, own, "finance_review", nil); err != nil {
		t.Fatalf("the agent, on its own, claims: %v", err)
	}
	w.is(t, "claimed by the agent alone", own, "finance_review", agentID)
	assertNotTheirs(t, "fay approves the agent's claim", w.try(t, w.fay, own, "approved", nil))

	// cl_closed leaves the claim field to people (no writable_by): the
	// agent, in finance and acting for fay, cannot claim, named or not.
	closed := w.queued(t, "cl_closed")
	assertClaimNotWritable(t, "the agent claims where the claim field is closed to agents",
		w.try(t, forFay, closed, "finance_review", nil))
	assertClaimNotWritable(t, "the same, naming fay",
		w.try(t, forFay, closed, "finance_review", map[string]any{"f_claimed_by": w.fay.Actor}))
	if err := w.try(t, w.fay, closed, "finance_review", nil); err != nil {
		t.Errorf("fay claims it herself: %v", err)
	}
}

// The kernel's own moves are exempt: the state's timer moves a claimed
// purchase on, though it is nobody's claimant. An escalation that takes a
// claim back (Later) rests on this.
func TestClaim_TheTimersMoveIsExempt(t *testing.T) {
	w := newClaimWorld(t)
	id := w.claimed(t, w.gus)
	it, err := w.k.Get(testCtx(t), w.gus, id)
	if err != nil {
		t.Fatal(err)
	}
	_, err = w.k.Internal().Transition(testCtx(t), itemservice.TransitionRequest{
		IdempotencyKey: newKey(), Principal: item.Principal{Actor: "system:timer", Namespace: w.ns}, ItemID: id, ToState: "escalated",
		Attribution: item.Attribution{
			OriginatingHook: "timer:cl_purchase@v1:finance_review",
			CausationID:     fmt.Sprintf("%s:%d", id, it.ItemSeq),
			Hop:             1,
		},
	})
	if err != nil {
		t.Fatalf("the timer moves the claimed purchase to escalated: %v", err)
	}
	w.is(t, "after the timer's move", id, "escalated", w.gus.Actor)
}

// The view (step 1) follows the claim. In the queue a purchase waits on
// finance. Claimed, it waits on its claimant only, and is in nobody's
// queue: a role's queue is what nobody has claimed yet, and putting a
// purchase back is not work that waits on the team. Released, it is the
// team's again.
func TestClaim_TheViewShowsAClaimedItemToItsClaimantOnly(t *testing.T) {
	w := newClaimWorld(t)
	w.grant(t, w.olaf, "cl_lead")
	queued := w.queued(t, "cl_purchase")
	held := w.queued(t, "cl_purchase")

	none := itemservice.Filter{}
	both := these("queued", queued, "held", held)
	assertWaiting(t, "fay, before any claim", w.waiting(t, w.fay, "me", none), both)
	assertWaiting(t, "the finance queue, before any claim", w.waiting(t, w.bob, "role:cl_finance", none), both)

	w.move(t, w.gus, held, "finance_review", nil)
	assertWaiting(t, "gus, who claimed one", w.waiting(t, w.gus, "me", none), both)
	assertWaiting(t, "fay, his colleague", w.waiting(t, w.fay, "me", none), these("queued", queued))
	assertWaiting(t, "the finance queue", w.waiting(t, w.fay, "role:cl_finance", none), these("queued", queued))
	// The lead is named on the release only: nothing waits on them.
	assertWaiting(t, "olaf, a lead", w.waiting(t, w.olaf, "me", none), these())
	assertWaiting(t, "the leads' queue", w.waiting(t, w.olaf, "role:cl_lead", none), these())

	// A claimant who left finance can no longer act: nothing waits on them.
	w.revoke(t, w.gus, "cl_finance")
	assertWaiting(t, "gus, no longer in finance", w.waiting(t, w.gus, "me", none), these())
	w.grant(t, w.gus, "cl_finance")

	w.move(t, w.fay, held, "finance_queue", nil)
	assertWaiting(t, "fay, after the release", w.waiting(t, w.fay, "me", none), both)
	assertWaiting(t, "the finance queue, after the release", w.waiting(t, w.fay, "role:cl_finance", none), both)
}

// The claim is judged where `allowed` is, so a decision that is checked
// (a hold) is the claimant's when it is asked: a colleague's request is
// refused at once and opens no hold.
func TestClaim_AColleaguesHeldDecisionOpensNoHold(t *testing.T) {
	w := newClaimWorld(t)
	ty, err := typedef.Parse([]byte(clHeldSrc))
	if err != nil {
		t.Fatalf("parse cl_held: %v", err)
	}
	if err := w.k.RegisterType(testCtx(t), ty); err != nil {
		t.Fatalf("register cl_held: %v", err)
	}
	id := w.create(t, itemservice.CreateRequest{
		Principal: w.ana, TypeID: "cl_held", TypeVersion: 1,
		Fields: map[string]any{"f_customer_id": "cust-1", "f_amount": 100},
	})
	w.move(t, w.gus, id, "review", nil)

	res, err := w.k.Transition(testCtx(t), itemservice.TransitionRequest{
		IdempotencyKey: newKey(), Principal: w.fay, ItemID: id, ToState: "granted",
	})
	assertNotTheirs(t, "fay asks for the checked decision on gus's claim", err)
	if res.HoldID != uuid.Nil {
		t.Errorf("fay's refused request opened the hold %s", res.HoldID)
	}
	res, err = w.k.Transition(testCtx(t), itemservice.TransitionRequest{
		IdempotencyKey: newKey(), Principal: w.gus, ItemID: id, ToState: "granted",
	})
	if err != nil || res.HoldID == uuid.Nil {
		t.Errorf("gus asks for it: hold %v, error %v; want an open hold", res.HoldID, err)
	}
}

// cl_held: the claimed state's decision is checked by a hook.
const clHeldSrc = `
id: cl_held
version: 1
initial_state: queue
fields:
  - {id: f_customer_id, type: string}
  - {id: f_amount,      type: int}
  - {id: f_cost_center, type: string, writable_by: kernel}
  - {id: f_claimed_by,  type: principal}
states:
  - {id: queue,   category: waiting}
  - {id: review,  category: active, claim: f_claimed_by}
  - {id: granted, category: done}
transitions:
  - {from: queue,  to: review,  allowed: ['role:cl_finance']}
  - {from: review, to: queue,   allowed: ['role:cl_finance'], release: true, clears: [f_claimed_by]}
  - {from: review, to: granted, allowed: ['role:cl_finance']}
hooks:
  - {id: credit, on: transition, from: review, to: granted, kind: check, action: erp.check_credit,
     reads: [f_customer_id, f_amount], writes: [f_cost_center]}
`
