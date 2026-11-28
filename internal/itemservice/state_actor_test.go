// Tests for who put an Item in its state (the Inbox for people, PR 3a):
// Item.StateActor and Item.StateActorOnBehalfOf. The owner's rule (Phase
// A, 2026-11-27):
//
//   - they are the actor, and the user it acted for, of the event that put
//     the Item in its state: its create, or its latest transition (the
//     event at StateEnteredAt); an update does not change them;
//   - filled on every read (Get, Search, Search with waiting_on, List);
//   - the same visibility as a history event's actor, through
//     policy.Project: a customer sees exactly what their own history shows
//     for that event.
//
// The world is sent_back_test.go's: sb_req in bob's desk; bob and cyd are
// the team (sb_team); kim and lee are customers; anasBot acts for ana and
// kimsBot for kim.
package itemservice_test

import (
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
)

type actor struct{ by, behalf string }

func stateActorOf(it item.Item) actor { return actor{it.StateActor, it.StateActorOnBehalfOf} }

// lastEntry is who the Item's own history, as p reads it, says put it in
// its state: the actor of its latest create or transition event.
func (w *sbWorld) lastEntry(t *testing.T, p item.Principal, id uuid.UUID) actor {
	t.Helper()
	res, err := w.k.ListEvents(testCtx(t), itemservice.ListEventsRequest{Principal: p, ItemID: id, Limit: 500})
	if err != nil {
		t.Fatalf("history of %s as %s: %v", w.byName[id], p.Actor, err)
	}
	var got actor
	for _, e := range res.Events {
		if e.Op == "create" || e.Op == "transition" {
			got = actor{e.Actor, e.OnBehalfOf}
		}
	}
	return got
}

// everyRead is the Item as p reads it by Get, Search, List and Search with
// waiting_on (when the Item is in that view), each checked against want.
func (w *sbWorld) everyRead(t *testing.T, p item.Principal, id uuid.UUID, waitingOn string, want actor) {
	t.Helper()
	name := w.byName[id]
	got, err := w.k.Get(testCtx(t), p, id)
	if err != nil {
		t.Fatalf("Get %s as %s: %v", name, p.Actor, err)
	}
	if stateActorOf(got) != want {
		t.Errorf("Get %s as %s: state actor %+v, want %+v", name, p.Actor, stateActorOf(got), want)
	}
	found, err := w.k.Search(testCtx(t), itemservice.SearchRequest{Principal: p, Limit: 500,
		Filter: itemservice.Filter{TypeIDs: []string{"sb_req"}, In: &w.desk}})
	if err != nil {
		t.Fatalf("Search as %s: %v", p.Actor, err)
	}
	if i := slices.IndexFunc(found.Items, func(it item.Item) bool { return it.ID == id }); i < 0 {
		t.Errorf("Search as %s: %s not found", p.Actor, name)
	} else if stateActorOf(found.Items[i]) != want {
		t.Errorf("Search %s as %s: state actor %+v, want %+v", name, p.Actor, stateActorOf(found.Items[i]), want)
	}
	listed, err := w.k.List(testCtx(t), itemservice.ListRequest{Principal: p, ContainerID: &w.desk, Limit: 500})
	if err != nil {
		t.Fatalf("List as %s: %v", p.Actor, err)
	}
	if i := slices.IndexFunc(listed.Items, func(it item.Item) bool { return it.ID == id }); i < 0 {
		t.Errorf("List as %s: %s not found", p.Actor, name)
	} else if stateActorOf(listed.Items[i]) != want {
		t.Errorf("List %s as %s: state actor %+v, want %+v", name, p.Actor, stateActorOf(listed.Items[i]), want)
	}
	if waitingOn != "" {
		view, err := w.k.Search(testCtx(t), itemservice.SearchRequest{Principal: p, Limit: 500,
			Filter: itemservice.Filter{WaitingOn: waitingOn, TypeIDs: []string{"sb_req"}}})
		if err != nil {
			t.Fatalf("Search waiting on %s as %s: %v", waitingOn, p.Actor, err)
		}
		if i := slices.IndexFunc(view.Items, func(it item.Item) bool { return it.ID == id }); i < 0 {
			t.Errorf("waiting on %s as %s: %s not in the view", waitingOn, p.Actor, name)
		} else if stateActorOf(view.Items[i]) != want {
			t.Errorf("waiting on %s, %s as %s: state actor %+v, want %+v", waitingOn, name, p.Actor, stateActorOf(view.Items[i]), want)
		}
	}
	// It is the actor that p's own history shows for that event.
	if h := w.lastEntry(t, p, id); h != want {
		t.Errorf("%s as %s: the history's last create or move is by %+v, want %+v (the test's own expectation is off)", name, p.Actor, h, want)
	}
}

func TestStateActor_CreatedByAPerson(t *testing.T) {
	w := newSBWorld(t)
	id := w.create(t, w.bob, "sb_req", &w.desk, map[string]any{"f_title": "laptop " + word()}, "bob's")
	w.everyRead(t, w.bob, id, "role:sb_team", actor{w.bob.Actor, ""})
}

func TestStateActor_CreatedByAnAgentForAUser(t *testing.T) {
	w := newSBWorld(t)
	id := w.create(t, w.anasBot, "sb_req", &w.desk, nil, "the bot's for ana")
	w.everyRead(t, w.bob, id, "role:sb_team", actor{w.anasBot.Actor, w.ana.Actor})
}

func TestStateActor_MovedByAnAgentThenByAPerson(t *testing.T) {
	w := newSBWorld(t)
	// An agent acting for bob, in the team as bob is.
	bobsBot := withSession(t, w.k, item.Principal{Actor: "agent:desk-" + word(), OnBehalfOf: w.bob.Actor, Namespace: w.ns})
	grantRoleIn(t, w.ns, item.Principal{Actor: bobsBot.Actor}, "sb_team")
	id := w.create(t, w.ana, "sb_req", &w.desk, map[string]any{"f_title": "chair " + word()}, "ana's")

	w.move(t, bobsBot, id, "taken")
	w.everyRead(t, w.cyd, id, "", actor{bobsBot.Actor, w.bob.Actor})

	// An update is not a move: it changes nothing here.
	if _, err := w.k.Update(testCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: w.cyd, ItemID: id, Fields: map[string]any{"f_secret": "noted"},
	}); err != nil {
		t.Fatalf("cyd updates: %v", err)
	}
	w.everyRead(t, w.cyd, id, "", actor{bobsBot.Actor, w.bob.Actor})

	// A person moves it after the agent: theirs now.
	w.move(t, w.cyd, id, "hold")
	w.everyRead(t, w.bob, id, "role:sb_team", actor{w.cyd.Actor, ""})
}

// A customer sees who put their request in its state as their own history
// shows it: staff who moved it, and the portal's agent acting for them.
func TestStateActor_ACustomerSeesWhatTheirHistoryShows(t *testing.T) {
	w := newSBWorld(t)
	id := w.create(t, w.kimsBot, "sb_req", &w.desk, map[string]any{"f_title": "printer " + word()}, "kim's")
	w.everyRead(t, w.kim, id, "", actor{w.kimsBot.Actor, w.kim.Actor})

	w.move(t, w.bob, id, "taken")
	w.move(t, w.bob, id, "asked")
	w.everyRead(t, w.kim, id, "me", actor{w.bob.Actor, ""})

	// The portal's agent answers for her: it sends it back to the team.
	w.move(t, w.kimsBot, id, "taken")
	w.everyRead(t, w.kim, id, "", actor{w.kimsBot.Actor, w.kim.Actor})
	w.everyRead(t, w.bob, id, "", actor{w.kimsBot.Actor, w.kim.Actor})
}
