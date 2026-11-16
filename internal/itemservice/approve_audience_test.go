// Tests for Step 0, part 3, review fix 6b: an approval commits only for the
// audience its approver was shown. ApproveWiden takes the expected audience
// and checks it in the write's own transaction; a different audience at
// commit is a RejectedError (audience_changed) carrying both audiences, and
// nothing is recorded, so the idempotency key stays unused.
package itemservice_test

import (
	"errors"
	"maps"
	"testing"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
)

var (
	openAudience       = itemservice.Audience{Open: true}
	restrictedAudience = itemservice.Audience{Open: false}
)

func approveExpecting(t *testing.T, w capWorld, agent item.Principal, key string, scope uuid.UUID,
	expected itemservice.Audience) error {
	t.Helper()
	return w.k.ApproveWiden(testCtx(t), itemservice.ApproveWidenRequest{
		IdempotencyKey: key, Principal: w.user, Session: agent.AgentSession, ScopeItem: &scope,
		ExpectedAudience: &expected,
	})
}

func approvals(t *testing.T, agent item.Principal) int {
	t.Helper()
	return count(t, `select count(*) from agent_session_approvals where session = $1`, agent.AgentSession)
}

func assertAudienceChanged(t *testing.T, what string, err error, audience, expected string) {
	t.Helper()
	var rej *itemservice.RejectedError
	if !errors.As(err, &rej) || !errors.Is(err, itemservice.ErrRejected) {
		t.Fatalf("%s: error = %v, want a RejectedError", what, err)
	}
	if rej.Code != itemservice.CodeAudienceChanged {
		t.Errorf("%s: code %q, want %q", what, rej.Code, itemservice.CodeAudienceChanged)
	}
	want := map[string]string{"audience": audience, "expected_audience": expected}
	if !maps.Equal(rej.Metadata, want) {
		t.Errorf("%s: metadata %v, want %v", what, rej.Metadata, want)
	}
}

// The audience the approver was shown is the audience at commit: the
// approval is recorded.
func TestApproveWiden_ExpectedAudienceMatches(t *testing.T) {
	w := newCapWorld(t)
	agent := scoped(t, w.k, w.user, []uuid.UUID{uuid.Nil}, allPowers...)
	room := createAs(t, w.k, w.user, "restricted room",
		&itemservice.Narrow{Readers: []string{w.user.Actor, agent.Actor}}, nil).ItemID

	if err := approveExpecting(t, w, agent, newKey(), room, restrictedAudience); err != nil {
		t.Fatalf("approve a restricted room, expecting restricted: %v", err)
	}
	if err := approveExpecting(t, w, agent, newKey(), w.b, openAudience); err != nil {
		t.Fatalf("approve an open container, expecting namespace: %v", err)
	}
	if n := approvals(t, agent); n != 2 {
		t.Errorf("approvals recorded = %d, want 2", n)
	}
}

// The approver was shown one audience; the scope's readers changed before
// the approval committed. Nothing is approved, the refusal names both
// audiences, and the key is not used up: the same key, with the audience as
// it now is, approves.
func TestApproveWiden_AudienceChangedSinceShown(t *testing.T) {
	w := newCapWorld(t)
	agent := scoped(t, w.k, w.user, []uuid.UUID{uuid.Nil}, allPowers...)
	room := createAs(t, w.k, w.user, "restricted room",
		&itemservice.Narrow{Readers: []string{w.user.Actor, agent.Actor}}, nil).ItemID

	// Restricted when shown, open at commit.
	shown, err := w.k.ApprovalAudience(testCtx(t), itemservice.ApproveWidenRequest{
		Principal: w.user, Session: agent.AgentSession, ScopeItem: &room,
	})
	if err != nil || shown.Open {
		t.Fatalf("ApprovalAudience = %+v, %v; want restricted", shown, err)
	}
	setPolicy(t, w.k, w.user, room, itemservice.PolicyPatch{Inherit: true})
	key := newKey()
	assertAudienceChanged(t, "restricted, then opened", approveExpecting(t, w, agent, key, room, shown),
		"namespace", "restricted")
	if n := approvals(t, agent); n != 0 {
		t.Errorf("approvals recorded = %d, want 0", n)
	}
	if n := count(t, `select count(*) from idempotency where actor = $1 and key = $2`, w.user.Actor, key); n != 0 {
		t.Errorf("idempotency rows for the refused key = %d, want 0", n)
	}
	if err := approveExpecting(t, w, agent, key, room, openAudience); err != nil {
		t.Errorf("the same key, expecting the audience as it now is: %v", err)
	}

	// Open when shown, restricted at commit.
	setPolicy(t, w.k, w.user, w.b, itemservice.PolicyPatch{Readers: []string{w.user.Actor, agent.Actor}})
	assertAudienceChanged(t, "open, then narrowed", approveExpecting(t, w, agent, newKey(), w.b, openAudience),
		"restricted", "namespace")
	if n := approvals(t, agent); n != 1 {
		t.Errorf("approvals recorded = %d, want 1 (the room only)", n)
	}
}

// A replay answers from the stored result, as for any write: it is not
// checked again, even if the audience changed since. The same key with
// another expected audience is another request.
func TestApproveWiden_ReplayIsNotCheckedAgain(t *testing.T) {
	w := newCapWorld(t)
	agent := scoped(t, w.k, w.user, []uuid.UUID{uuid.Nil}, allPowers...)
	room := createAs(t, w.k, w.user, "restricted room",
		&itemservice.Narrow{Readers: []string{w.user.Actor, agent.Actor}}, nil).ItemID

	key := newKey()
	if err := approveExpecting(t, w, agent, key, room, restrictedAudience); err != nil {
		t.Fatalf("approve: %v", err)
	}
	setPolicy(t, w.k, w.user, room, itemservice.PolicyPatch{Inherit: true})
	if err := approveExpecting(t, w, agent, key, room, restrictedAudience); err != nil {
		t.Errorf("replay after the audience changed: %v, want the stored answer", err)
	}
	if err := approveExpecting(t, w, agent, key, room, openAudience); !errors.Is(err, itemservice.ErrIdempotencyKeyReuse) {
		t.Errorf("same key, other expected audience: %v, want ErrIdempotencyKeyReuse", err)
	}
	if n := approvals(t, agent); n != 1 {
		t.Errorf("approvals recorded = %d, want 1", n)
	}
}

// The kernel enforces it, not only the API: a real approval without the
// audience its approver was shown is ErrInvalid, and records nothing.
// (ApprovalAudience, which only answers the audience, needs none.)
func TestApproveWiden_ExpectedAudienceIsRequired(t *testing.T) {
	w := newCapWorld(t)
	agent := scoped(t, w.k, w.user, []uuid.UUID{uuid.Nil}, allPowers...)
	key := newKey()
	err := w.k.ApproveWiden(testCtx(t), itemservice.ApproveWidenRequest{
		IdempotencyKey: key, Principal: w.user, Session: agent.AgentSession, ScopeItem: &w.b,
	})
	if !errors.Is(err, itemservice.ErrInvalid) {
		t.Errorf("no ExpectedAudience: error = %v, want ErrInvalid", err)
	}
	if n := approvals(t, agent); n != 0 {
		t.Errorf("approvals recorded = %d, want 0", n)
	}
	if n := count(t, `select count(*) from idempotency where actor = $1 and key = $2`, w.user.Actor, key); n != 0 {
		t.Errorf("idempotency rows for the key = %d, want 0", n)
	}
}
