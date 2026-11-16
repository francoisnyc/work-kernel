// Tests for proof of concept Step 1: links and agents.
//
// A link can change the target's behavior (through guards), so for an
// agent a link write is a write to both ends: both must be inside the
// session's scopes (else NOT_FOUND), the session needs a write power (any:
// v1 enforces read versus write), and the write rule (NEEDS_APPROVAL) runs
// against both ends' scopes, the source first. Listing links records no
// read: a link names the other Item by ID only.
package itemservice_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
)

// A read-only session may not link or unlink.
func TestLinkAgent_NeedsAWritePower(t *testing.T) {
	w := newLinkWorld(t)
	x, y := w.open(t), w.open(t)
	w.mustLink(t, w.ana, x, "blocks", y)
	reader := scoped(t, w.k, w.ana, []uuid.UUID{uuid.Nil}, item.PowerRead)
	_, err := w.link(t, reader, y, "blocks", x)
	assertForbidden(t, err)
	_, err = w.unlink(t, reader, x, "blocks", y)
	assertForbidden(t, err)

	// Any write power is enough in v1.
	editor := scoped(t, w.k, w.ana, []uuid.UUID{uuid.Nil}, item.PowerEditFields)
	w.mustLink(t, editor, y, "blocks", x)
}

// Both ends must be inside the session's scopes; outside, an Item does not
// exist for the session.
func TestLinkAgent_BothEndsInsideTheSession(t *testing.T) {
	w := newLinkWorld(t)
	boxA, boxB := w.open(t), w.open(t)
	inA := w.make(t, w.ana, "s_open", itemOpts{container: &boxA})
	inA2 := w.make(t, w.ana, "s_open", itemOpts{container: &boxA})
	inB := w.make(t, w.ana, "s_open", itemOpts{container: &boxB})
	agent := scoped(t, w.k, w.ana, []uuid.UUID{boxA}, item.PowerEditFields)

	_, err := w.link(t, agent, inA, "blocks", inB)
	assertNotFound(t, "a target outside the session", err)
	_, err = w.link(t, agent, inB, "blocks", inA)
	assertNotFound(t, "a source outside the session", err)
	w.mustLink(t, agent, inA, "blocks", inA2)

	w.mustLink(t, w.ana, inA, "about", inB)
	_, err = w.unlink(t, agent, inA, "about", inB)
	assertNotFound(t, "unlink to a target outside the session", err)
}

// The write rule on both ends. The agent read a narrowed space; a link
// from an Item there to an open Item lands content-wise on both ends, so it
// needs approval for the target's scope; the other way round, for the
// source's. Approving the end named lets the same request through.
func TestLinkAgent_WriteRuleOnBothEnds(t *testing.T) {
	w := newLinkWorld(t)
	agent := agentFor(t, w.k, w.ana)
	agent.Namespace = "default"
	space := w.make(t, w.ana, "s_open", itemOpts{narrow: []string{w.ana.Actor, agent.Actor}})
	inSpace := w.make(t, w.ana, "s_open", itemOpts{container: &space})
	open := w.open(t)
	root := scopeOf(t, open)

	if _, err := w.k.Get(testCtx(t), agent, inSpace); err != nil { // records the space's scope
		t.Fatalf("agent reads the space: %v", err)
	}

	req := w.req(agent, inSpace, "blocks", open)
	_, err := w.k.Link(testCtx(t), req)
	assertNeedsApproval(t, err, root)
	var na *itemservice.NeedsApprovalError
	if errors.As(err, &na) && (na.ScopeItem == nil || *na.ScopeItem != open) {
		t.Errorf("NEEDS_APPROVAL names %v, want the target %s", na.ScopeItem, open)
	}

	_, err = w.link(t, agent, open, "about", inSpace)
	assertNeedsApproval(t, err, root) // the source, checked first
	if errors.As(err, &na) && (na.ScopeItem == nil || *na.ScopeItem != open) {
		t.Errorf("NEEDS_APPROVAL names %v, want the source %s", na.ScopeItem, open)
	}

	if err := w.k.ApproveWiden(testCtx(t), itemservice.ApproveWidenRequest{
		IdempotencyKey: newKey(), Principal: w.ana, Session: agent.AgentSession, ScopeItem: &open,
		ExpectedAudience: &itemservice.Audience{Open: true},
	}); err != nil {
		t.Fatalf("ApproveWiden: %v", err)
	}
	if _, err := w.k.Link(testCtx(t), req); err != nil {
		t.Errorf("the same link after approval: %v", err)
	}

	// Unlink follows the same rule: a fresh session that read the space
	// needs approval to unlink it too.
	other := agentFor(t, w.k, w.ana)
	other.Namespace = "default"
	if _, err := w.k.Get(testCtx(t), other, inSpace); err != nil {
		t.Fatalf("the other agent reads the space: %v", err)
	}
	_, err = w.unlink(t, other, inSpace, "blocks", open)
	assertNeedsApproval(t, err, root)
}

// Listing links (Get, history) records no read of the linked Items: a link
// carries only an ID.
func TestLinkAgent_ListingLinksRecordsNoRead(t *testing.T) {
	w := newLinkWorld(t)
	agent := agentFor(t, w.k, w.ana)
	agent.Namespace = "default"
	space := w.make(t, w.ana, "s_open", itemOpts{narrow: []string{w.ana.Actor, agent.Actor}})
	inSpace := w.make(t, w.ana, "s_open", itemOpts{container: &space})
	x := w.open(t)
	w.mustLink(t, w.ana, x, "blocks", inSpace)
	w.mustLink(t, w.ana, inSpace, "about", x)

	got, _ := w.linksOf(t, agent, x)
	assertLinks(t, "x as the agent", got, out("blocks", inSpace), inc("mentioned_by", inSpace))
	_ = history(t, w.k, agent, x)
	if reads := recordedReads(t, agent.AgentSession); len(reads) != 0 {
		t.Errorf("reading x and its links recorded %v, want nothing (x is open)", reads)
	}
	if _, err := w.k.Get(testCtx(t), agent, inSpace); err != nil {
		t.Fatalf("Get inSpace: %v", err)
	}
	if reads := recordedReads(t, agent.AgentSession); !slices.Contains(reads, scopeOf(t, inSpace)) {
		t.Errorf("reading inSpace itself recorded %v, want its scope", reads)
	}
}

// For an agent, an end outside the session's scopes is hidden like one it
// cannot see: restricted when outgoing, left out when incoming.
func TestLinkAgent_EndsOutsideTheSessionAreHidden(t *testing.T) {
	w := newLinkWorld(t)
	boxA, boxB := w.open(t), w.open(t)
	x := w.make(t, w.ana, "s_open", itemOpts{container: &boxA})
	y := w.make(t, w.ana, "s_open", itemOpts{container: &boxB})
	z := w.make(t, w.ana, "s_open", itemOpts{container: &boxB})
	w.mustLink(t, w.ana, x, "blocks", y)
	w.mustLink(t, w.ana, z, "blocks", x)
	agent := scoped(t, w.k, w.ana, []uuid.UUID{boxA}, item.PowerRead)

	got, _ := w.linksOf(t, agent, x)
	assertLinks(t, "x as an agent scoped to A", got, restrictedOut("blocks"))
	got, _ = w.linksOf(t, w.ana, x)
	assertLinks(t, "x as ana", got, out("blocks", y), inc("blocked_by", z))
}
