// Tests for Step 0, part 3, review fix 6c: resolving the names in an Item's
// history is not reading those Items. ListEvents records an agent's read of
// the Item itself, as Get does; the containers its events name are checked
// for visibility (a hidden one is Restricted) but are not recorded as read,
// so reading a history never makes a later write need approval.
package itemservice_test

import (
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/itemservice"
)

func TestListEvents_NamingAContainerIsNotReadingIt(t *testing.T) {
	w := newCapWorld(t)
	agent := scoped(t, w.k, w.user, []uuid.UUID{uuid.Nil}, allPowers...)
	// A narrowed container the agent and its user see, and one only the
	// user sees. A ticket was created in the first, then moved to the top
	// level (open); another passed through the second.
	shared := createAs(t, w.k, w.user, "shared room", &itemservice.Narrow{Readers: []string{w.user.Actor, agent.Actor}}, nil).ItemID
	hidden := createAs(t, w.k, w.user, "user's room", &itemservice.Narrow{Readers: readers(w.user)}, nil).ItemID
	// x is an agent_ticket: the agent writes its fields below (fields are
	// closed to agents by default).
	x := createOpenAs(t, w.k, w.user, "was in the shared room", nil, &shared).ItemID
	if _, err := moveTo(t, w.k, w.user, x, nil, true); err != nil {
		t.Fatalf("move x to the top level: %v", err)
	}
	y := createAs(t, w.k, w.user, "was in the user's room", nil, &hidden).ItemID
	if _, err := moveTo(t, w.k, w.user, y, nil, true); err != nil {
		t.Fatalf("move y to the top level: %v", err)
	}

	history := func(id uuid.UUID) []itemservice.Event {
		t.Helper()
		res, err := w.k.ListEvents(testCtx(t), itemservice.ListEventsRequest{Principal: agent, ItemID: id})
		if err != nil {
			t.Fatalf("ListEvents as the agent: %v", err)
		}
		return res.Events
	}
	// Visibility still decides the names: the shared room is named, the
	// user's room is Restricted.
	hx, hy := history(x), history(y)
	if c := hx[0].Container; c == nil || c.Restricted || c.ID != shared {
		t.Errorf("x's create names %+v, want the shared room %s", c, shared)
	}
	if c := hy[0].Container; c == nil || !c.Restricted || c.ID != uuid.Nil {
		t.Errorf("y's create names %+v, want Restricted with no ID", c)
	}

	// x and y are in the open root scope: reading them records nothing,
	// and naming the rooms must not either.
	if got := recordedReads(t, agent.AgentSession); len(got) != 0 {
		t.Errorf("reads recorded after two histories = %v, want none", got)
	}
	// So a write to an open Item needs no approval.
	if err := agentUpdate(t, w.k, agent, x); err != nil {
		t.Errorf("update x after reading its history: %v, want no NEEDS_APPROVAL", err)
	}
}

// The history's own Item is still read: an Item in a narrowed scope is
// recorded, as Get records it.
func TestListEvents_StillRecordsTheItemItself(t *testing.T) {
	w := newCapWorld(t)
	agent := scoped(t, w.k, w.user, []uuid.UUID{uuid.Nil}, allPowers...)
	room := createAs(t, w.k, w.user, "shared room", &itemservice.Narrow{Readers: []string{w.user.Actor, agent.Actor}}, nil).ItemID
	inRoom := createAs(t, w.k, w.user, "in the room", nil, &room).ItemID
	if _, err := w.k.ListEvents(testCtx(t), itemservice.ListEventsRequest{Principal: agent, ItemID: inRoom}); err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if got := recordedReads(t, agent.AgentSession); len(got) != 1 || !slices.Contains(got, roomScope(t, room)) {
		t.Errorf("reads recorded = %v, want the room's scope only", got)
	}
}

// roomScope is the scope content in a narrowed container lands in.
func roomScope(t *testing.T, room uuid.UUID) uuid.UUID {
	t.Helper()
	var scope uuid.UUID
	if err := pool.QueryRow(testCtx(t), `select scope_id from items where id = $1`, room).Scan(&scope); err != nil {
		t.Fatalf("read scope: %v", err)
	}
	return scope
}
